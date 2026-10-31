package server

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trezor/blockbook/api"
	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/common"
	"github.com/trezor/blockbook/db"
)

type insightFake struct {
	address *api.Address
	utxos   api.Utxos
	txs     map[string]*api.Tx
	info    *api.SystemInfo
	err     error
	detail  api.AccountDetails
	prefix  int
	fetched []string
	enter   chan struct{}
	release chan struct{}
	blocks  *api.Blocks
	// blocksPage and blocksSize record the last GetBlocks call.
	blocksPage, blocksSize int
	// chain, when set, backs GetBlocks like the real index: page p of one-row
	// pages is height best-(p-1). blocksCalls counts those calls.
	chain       []db.BlockInfo
	blocksCalls int
	// shiftReads makes that many index reads beyond the tip row report the
	// wrong height, as if the tip moved between two reads.
	shiftReads int
	// blockCalls counts GetBlock calls.
	blockCalls int
	// blockByHash backs GetBlock (paged like the worker); rawBlock and rawErr
	// back GetBlockRaw.
	blockByHash map[string]*api.Block
	rawBlock    *api.BlockRaw
	rawErr      error
	// spending records the spendingTxs flag of each GetTransaction call;
	// txErr fails single txids. clampPages makes GetBlock clamp a page
	// beyond the end to the last page, as the worker does.
	spending []bool
	txErr    map[string]error
	// txHook runs on every GetTransaction call (e.g. to cancel a request).
	txHook     func(id string)
	clampPages bool
	// pages records the page of every GetAddress call, historyPages only
	// those of txid-history calls; pageHook rewrites the txids of a history
	// page (e.g. to inject a duplicate).
	pages        []int
	historyPages []int
	pageHook     func(page, size int, ids []string) []string
	// fees backs EstimateFee in sat/kB per block target, feeErr fails a
	// target, feeCalls records the targets and the conservative flag.
	fees     map[int]int64
	feeErr   map[int]error
	feeCalls []int
	feeCons  []bool
	// fiat and fiatErr back GetCurrentFiatRates; fiatArgs records the
	// currencies and token of the last call.
	fiat     *api.FiatTicker
	fiatErr  error
	fiatArgs []string
}

func (f *insightFake) GetSystemInfo(bool) (*api.SystemInfo, error) {
	if f.enter != nil {
		f.enter <- struct{}{}
		<-f.release
	}
	return f.info, f.err
}
func (f *insightFake) GetAddress(_ string, page, size int, d api.AccountDetails, filter *api.AddressFilter, _ string) (*api.Address, error) {
	f.detail = d
	f.prefix = size
	if page < 1 || size < 1 || filter == nil || filter.Vout != api.AddressFilterVoutOff {
		panic("wrong worker query")
	}
	f.pages = append(f.pages, page)
	if f.err != nil || f.address == nil {
		return f.address, f.err
	}
	out := *f.address
	out.Txids = nil
	if d < api.AccountDetailsTxidHistory {
		return &out, nil
	}
	f.historyPages = append(f.historyPages, page)
	// address.Txids is the merged pending+confirmed sequence; page it like
	// computeAccountPaging, which clamps a page past the end to the last page.
	ids := f.address.Txids
	last := 1
	if len(ids) > 0 {
		last = (len(ids)-1)/size + 1
	}
	if page > last {
		page = last
	}
	lo, hi := (page-1)*size, page*size
	if hi > len(ids) {
		hi = len(ids)
	}
	out.Txids = append([]string{}, ids[lo:hi]...)
	if f.pageHook != nil {
		out.Txids = f.pageHook(page, size, out.Txids)
	}
	out.Paging = api.Paging{Page: page, TotalPages: last, ItemsOnPage: size}
	return &out, nil
}
func (f *insightFake) GetAddressUtxo(_ string, confirmed bool) (api.Utxos, error) {
	if confirmed {
		panic("pending omitted")
	}
	return f.utxos, f.err
}
func (f *insightFake) GetTransaction(id string, spending bool, _ bool) (*api.Tx, error) {
	f.fetched = append(f.fetched, id)
	f.spending = append(f.spending, spending)
	if f.txHook != nil {
		f.txHook(id)
	}
	if err := f.txErr[id]; err != nil {
		return nil, err
	}
	if f.err == nil && f.txs[id] == nil {
		return nil, api.NewAPIError("Transaction '"+id+"' not found", true)
	}
	return f.txs[id], f.err
}
func (f *insightFake) GetBlocks(page int, size int) (*api.Blocks, error) {
	f.blocksPage, f.blocksSize = page, size
	f.blocksCalls++
	if f.chain != nil {
		out := &api.Blocks{}
		for i := (page - 1) * size; i < page*size && i < len(f.chain); i++ {
			out.Blocks = append(out.Blocks, f.chain[len(f.chain)-1-i])
		}
		if page > 1 && f.shiftReads > 0 {
			f.shiftReads--
			for i := range out.Blocks {
				out.Blocks[i].Height++
			}
		}
		return out, f.err
	}
	return f.blocks, f.err
}
func (f *insightFake) GetBlock(bid string, page int, size int) (*api.Block, error) {
	f.blockCalls++
	b := f.blockByHash[bid]
	if b == nil {
		return nil, api.NewAPIError("Block not found", true)
	}
	out := *b
	if f.clampPages && size > 0 && b.TxCount > 0 && (page-1)*size >= b.TxCount {
		page = (b.TxCount + size - 1) / size
	}
	from, to := (page-1)*size, page*size
	if to > len(b.Transactions) {
		to = len(b.Transactions)
	}
	out.Transactions = nil
	if from < to {
		out.Transactions = b.Transactions[from:to]
	}
	out.Page, out.TotalPages = page, (b.TxCount+size-1)/size
	return &out, f.err
}
func (f *insightFake) GetBlockRaw(bid string) (*api.BlockRaw, error) {
	return f.rawBlock, f.rawErr
}
func (f *insightFake) EstimateFee(blocks int, conservative bool) (big.Int, error) {
	f.feeCalls = append(f.feeCalls, blocks)
	f.feeCons = append(f.feeCons, conservative)
	if err := f.feeErr[blocks]; err != nil {
		return big.Int{}, err
	}
	return *big.NewInt(f.fees[blocks]), nil
}
func (f *insightFake) GetCurrentFiatRates(currencies []string, token string) (*api.FiatTicker, error) {
	f.fiatArgs = append(append([]string{}, currencies...), token)
	return f.fiat, f.fiatErr
}
func insightAmount(n int64) *api.Amount { return (*api.Amount)(big.NewInt(n)) }
func insightFixture() *insightFake {
	return &insightFake{
		address: &api.Address{AddrStr: "A", BalanceSat: insightAmount(1), UnconfirmedBalanceSat: insightAmount(-1), Txs: 3, UnconfirmedTxs: 1, Txids: []string{"pending", "a", "b", "c"}},
		utxos:   api.Utxos{{Txid: "a", AmountSat: insightAmount(1), Coinbase: true}},
		txs:     map[string]*api.Tx{"pending": {Txid: "pending"}, "a": {Txid: "a", FeesSat: insightAmount(1), Vin: []api.Vin{{Coinbase: "abc"}}, Vout: []api.Vout{{ValueSat: insightAmount(1), Hex: "6a026869", Addresses: []string{"OP_RETURN (hi)"}}}}, "b": {Txid: "b"}, "c": {Txid: "c"}},
		info:    &api.SystemInfo{Blockbook: &api.BlockbookInfo{InSync: true, BestHeight: 10}, Backend: &common.BackendInfo{Blocks: 10, Version: "123", ProtocolVersion: "456", Difficulty: "2.5", BestBlockHash: "bb"}},
		blocks:  &api.Blocks{Blocks: []db.BlockInfo{{Hash: "tip"}}},
	}
}
func insightTestServer(t *testing.T, f *insightFake) *InsightServer {
	t.Helper()
	s, err := NewInsightServer("127.0.0.1:0", bchain.ChainBitcoinType, 8, f, func(raw string) (string, error) { return "sent", f.err })
	if err != nil {
		t.Fatal(err)
	}
	s.SetReady()
	return s
}
func insightRequest(s *InsightServer, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func TestInsightContracts(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		contains   []string
	}{
		{"status", "/api/status", []string{`"blocks":10`, `"version":123`, `"protocolversion":456`, `"errors":""`}},
		{"utxo", "/api/addr/A/utxo", []string{`"amount":0.00000001`, `"satoshis":1`, `"coinbase":true`}},
		{"summary", "/api/addr/A", []string{`"balanceSat":"1"`, `"unconfirmedBalance":-0.00000001`, `"totalReceived":0`, `"totalSentSat":0`, `"txApperances":3`}},
		{"history", "/api/addrs/A/txs?from=1&to=3", []string{`"totalItems":4`, `"from":1`, `"to":3`, `"txid":"a"`, `"txid":"b"`, `"asm":"OP_RETURN 6869"`, `"value":0.00000001`, `"coinbase":"abc"`, `"isCoinBase":true`, `"blockHeight":0`, `"blockheight":0`, `"blockTime":0`, `"valueOut":0`, `"fees":0.00000001`, `"addresses":["OP_RETURN (hi)"]`}},
		{"query summary", "/api/addr?address=A", []string{`"addrStr":"A"`}},
		{"noTxList", "/api/addr/A/true", []string{`"addrStr":"A"`}},
		{"query utxo", "/api/addr/utxo?address=A", []string{`"satoshis":1`}},
		{"query history", "/api/addrs/txs?address=A&from=1&to=3", []string{`"to":3`}},
		{"path offsets", "/api/addrs/A/txs/1/3", []string{`"to":3`}},
		{"beyond history", "/api/addrs/A/txs?from=20&to=30", []string{`"from":20`, `"to":20`, `"items":[]`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := insightFixture()
			s := insightTestServer(t, f)
			w := insightRequest(s, "GET", tc.path, "")
			if w.Code != 200 {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			for _, v := range tc.contains {
				if !strings.Contains(w.Body.String(), v) {
					t.Errorf("missing %s in %s", v, w.Body)
				}
			}
			if tc.name == "history" && (!reflect.DeepEqual(f.fetched, []string{"a", "b"}) || f.prefix != 2 || f.detail != api.AccountDetailsTxidHistory) {
				t.Fatalf("query/enrichment: %+v", f)
			}
			if tc.name == "summary" && f.detail != api.AccountDetailsTxidHistory {
				t.Fatal("summary and its transactions list must come from one history query")
			}
		})
	}
}
func TestInsightInvalidRequests(t *testing.T) {
	for _, tc := range []struct {
		method, path, body string
		code               int
	}{
		{"GET", "/unknown", "", 404}, {"OPTIONS", "/unknown", "", 404}, {"POST", "/api/status", "", 405}, {"GET", "/api/tx/send", "", 405},
		{"POST", "/api/sync", "", 405}, {"OPTIONS", "/insight-api/status", "", 204},
		{"GET", "/api/addr", "", 400}, {"GET", "/api/addrs/A,B/txs", "", 200}, {"GET", "/api/addrs/A,B/txs?to=1001", "", 400},
		{"GET", "/api/addrs/A/txs?from=-1&to=5", "", 400}, {"GET", "/api/addrs/A/txs?from=0&to=1001", "", 400},
		{"GET", "/api/addrs/A/txs?to=1001", "", 200}, {"GET", "/api/addrs/A/txs?from=-1", "", 200},
		{"GET", "/api/addrs/A/txs?from=0&to=1000", "", 200}, {"GET", "/api/addrs/A/txs?from=2000&to=3000", "", 200}, {"GET", "/api/addrs/A/txs//5", "", 200}, {"GET", "/api/addrs/A/txs/0/", "", 200},
		{"GET", "/api/addrs/A/txs/0//", "", 200}, {"GET", "/api/addrs/A/txs/1/x", "", 400}, {"GET", "/api/addr//balance", "", 400}, {"GET", "/api/txs?address=", "", 400},
		{"GET", "/api/txs?address=A,B", "", 400}, {"GET", "/api/addr/A?from=x&to=1", "", 400}, {"GET", "/api/addr/A?from=x", "", 200}, {"GET", "/api/addr/A?from=0&to=1000", "", 200},
		{"GET", "/api/addr/A?from=0&to=1001", "", 400},
		{"GET", "/api/addrs/A/txs?from=2&to=1", "", 400}, {"GET", "/api/addrs/A/txs?from=1.5&to=3", "", 400},
		{"POST", "/api/tx/send", `{}`, 400}, {"POST", "/api/tx/send", `{"rawtx":1}`, 400},
		{"POST", "/api/tx/send", `{"rawtx":"a"}`, 400}, {"POST", "/api/tx/send", `{"rawtx":"gg"}`, 400},
		{"POST", "/api/tx/send", `{"rawtx":""}`, 400}, {"POST", "/api/tx/send", `{"rawtx":"aa"} {}`, 400},
		{"POST", "/api/tx/send", `{"rawtx":`, 400}, {"POST", "/api/tx/send", `null`, 400},
		{"POST", "/api/tx/send", strings.Repeat(" ", 8*1024*1024+1), 413},
	} {
		t.Run(tc.method+tc.path+tc.body[:min(len(tc.body), 25)], func(t *testing.T) {
			s := insightTestServer(t, insightFixture())
			w := insightRequest(s, tc.method, tc.path, tc.body)
			if w.Code != tc.code {
				t.Fatalf("%d: %s", w.Code, w.Body)
			}
			if w.Header().Get("Access-Control-Allow-Origin") != "*" {
				t.Fatal("CORS missing")
			}
		})
	}
	s := insightTestServer(t, insightFixture())
	w := insightRequest(s, "POST", "/api/tx/send", `{"rawtx":"aA00"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"txid":"sent"`) {
		t.Fatal(w)
	}
	w = insightRequest(s, "OPTIONS", "/api/tx/send", "")
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
	r := httptest.NewRequest("POST", "/api/tx/send", strings.NewReader(strings.Repeat(" ", 8*1024*1024+1)))
	r.ContentLength = -1
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 413 {
		t.Fatal(w.Code)
	}
}

func TestInsightCorrespondingSourceHeader(t *testing.T) {
	s := insightTestServer(t, insightFixture())
	url, label := sourceLink(common.GetVersionInfo().GitCommit)
	want := "<" + url + ">; rel=\"source\"; title=\"" + label + "\""
	for _, tc := range []struct{ method, path string }{{"GET", "/api/status"}, {"GET", "/unknown"}, {"OPTIONS", "/api/tx/send"}} {
		w := insightRequest(s, tc.method, tc.path, "")
		if got := w.Header().Get("Link"); got != want {
			t.Fatalf("%s %s: source header %q, want %q", tc.method, tc.path, got, want)
		}
		if got := w.Header().Get("Access-Control-Expose-Headers"); got != "Link" {
			t.Fatalf("source header is not exposed through CORS: %q", got)
		}
	}
}
func TestInsightErrorsAndAmounts(t *testing.T) {
	for _, path := range []string{"/api/status", "/api/addr/A", "/api/addr/A/utxo", "/api/addrs/A/txs", "/api/tx/send"} {
		f := insightFixture()
		f.err = errors.New("http://user:password@secret/")
		s := insightTestServer(t, f)
		method := "GET"
		if path == "/api/tx/send" {
			method = "POST"
		}
		w := insightRequest(s, method, path, `{"rawtx":"aa"}`)
		if w.Code != 500 || strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "password") {
			t.Fatal(w)
		}
	}
	f := insightFixture()
	f.info.Backend.BackendError = "http://user:password@secret"
	w := insightRequest(insightTestServer(t, f), "GET", "/api/status", "")
	if w.Code != 503 || strings.Contains(w.Body.String(), "secret") {
		t.Fatal(w)
	}
	// Amounts past 2^53 are written exactly instead of failing.
	for _, tc := range []struct {
		sat  int64
		want string
	}{
		{0, `"amount":0,"satoshis":0,`},
		{1, `"amount":0.00000001,"satoshis":1,`},
		{-1, `"amount":-0.00000001,"satoshis":-1,`},
		{9007199254740991, `"amount":90071992.54740991,"satoshis":9007199254740991,`},
		{9007199254740993, `"amount":90071992.54740993,"satoshis":9007199254740993,`},
		{3260000000000000, `"amount":32600000,"satoshis":3260000000000000,`},
		{math.MaxInt64, `"amount":92233720368.54775807,"satoshis":9223372036854775807,`},
	} {
		f := insightFixture()
		f.utxos[0].AmountSat = insightAmount(tc.sat)
		w := insightRequest(insightTestServer(t, f), "GET", "/api/addr/A/utxo", "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), tc.want) {
			t.Fatalf("%d: %d %s, want %s", tc.sat, w.Code, w.Body, tc.want)
		}
	}
	f = insightFixture()
	f.utxos[0].AmountSat = nil
	w = insightRequest(insightTestServer(t, f), "GET", "/api/addr/A/utxo", "")
	var rows []map[string]interface{}
	if json.Unmarshal(w.Body.Bytes(), &rows) != nil || rows[0]["amount"] != float64(0) {
		t.Fatal(w.Body)
	}
}

func TestInsightInvalidAddress(t *testing.T) {
	for _, path := range []string{"/api/addr/invalid", "/api/addr/invalid/utxo", "/api/addrs/invalid/txs"} {
		f := insightFixture()
		f.err = api.NewAPIError("Invalid address: http://user:password@secret", true)
		w := insightRequest(insightTestServer(t, f), "GET", path, "")
		if w.Code != 400 || strings.Contains(w.Body.String(), "secret") {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
	}
}

// Offsets index the worker's paged sequence, so a window that holds the same
// txid twice returns it once and later windows keep their positions.
func TestInsightHistoryDedup(t *testing.T) {
	f := insightFixture()
	f.address.Txids = []string{"pending", "a", "a", "b", "c"}
	w := insightRequest(insightTestServer(t, f), "GET", "/api/addrs/A/txs?from=1&to=4", "")
	if w.Code != 200 || !reflect.DeepEqual(f.fetched, []string{"a", "b"}) || !strings.Contains(w.Body.String(), `"from":1,"to":3,`) {
		t.Fatal(w, f.fetched)
	}
}

func TestInsightHistoryWindows(t *testing.T) {
	for _, tc := range []struct {
		query    string
		from, to int
		detail   api.AccountDetails
		history  []int
		ids      []string
	}{
		// The worker page size is the window size, so the window lies in at
		// most two worker pages; an empty window reads the totals alone.
		{"", 0, 4, api.AccountDetailsTxidHistory, []int{1}, []string{"pending", "a", "b", "c"}},
		{"?from=0&to=1", 0, 1, api.AccountDetailsTxidHistory, []int{1}, []string{"pending"}},
		{"?from=1&to=2", 1, 2, api.AccountDetailsTxidHistory, []int{2}, []string{"a"}},
		{"?from=2&to=4", 2, 4, api.AccountDetailsTxidHistory, []int{2}, []string{"b", "c"}},
		{"?from=0&to=0", 0, 0, api.AccountDetailsTokens, nil, nil},
		{"?from=1000&to=1000", 1000, 1000, api.AccountDetailsTokens, nil, nil},
		{"?from=3", 0, 4, api.AccountDetailsTxidHistory, []int{1}, []string{"pending", "a", "b", "c"}},
		{"?to=3", 0, 4, api.AccountDetailsTxidHistory, []int{1}, []string{"pending", "a", "b", "c"}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			f := insightFixture()
			w := insightRequest(insightTestServer(t, f), "GET", "/api/addrs/A/txs"+tc.query, "")
			var out struct {
				TotalItems, From, To int
				Items                []json.RawMessage
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
				t.Fatal(w)
			}
			if out.TotalItems != 4 || out.From != tc.from || out.To != tc.to || f.detail != tc.detail || !reflect.DeepEqual(f.historyPages, tc.history) || !reflect.DeepEqual(f.fetched, tc.ids) {
				t.Fatalf("response=%s detail=%d history pages=%v enriched=%v", w.Body, f.detail, f.historyPages, f.fetched)
			}
		})
	}
}

func TestInsightTransactionWalletFields(t *testing.T) {
	tx := &api.Tx{Txid: "a", Blocktime: 123, Blockheight: 20, Blockhash: "block", Confirmations: 2, Hex: "abcd", FeesSat: insightAmount(1),
		Vin:  []api.Vin{{Addresses: []string{"A"}, IsAddress: true, ValueSat: insightAmount(1)}, {Coinbase: "coinbase"}},
		Vout: []api.Vout{{N: 2, ValueSat: insightAmount(1), IsAddress: true, Addresses: []string{"B"}, Hex: "51"}, {N: 3, Addresses: []string{"OP_RETURN (fake)"}}},
	}
	out, err := insightTransaction(tx)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"time":123`, `"blockheight":20`, `"blockhash":"block"`, `"confirmations":2`, `"hex":"abcd"`, `"fees":0.00000001`, `"addr":"A"`, `"valueSat":1`, `"coinbase":"coinbase"`, `"n":2`, `"addresses":["B"]`, `"asm":"1"`, `"blockHeight":20`, `"blockHash":"block"`, `"blockTime":123`, `"blocktime":123`, `"value":"0"`, `"valueOut":0`} {
		if !strings.Contains(string(data), field) {
			t.Fatalf("missing %s in %s", field, data)
		}
	}
	// The converter puts the vout addresses (including OP_RETURN text) into
	// scriptPubKey.addresses; asm still comes only from script bytes.
	if out.Vin[1].Addr != nil || out.Vout[1].ScriptPubKey.Asm != "" || !reflect.DeepEqual(out.Vout[1].ScriptPubKey.Addresses, []string{"OP_RETURN (fake)"}) {
		t.Fatal("fabricated script or input address")
	}
	if strings.Contains(string(data), `"isCoinBase"`) || strings.Contains(string(data), `"valueIn"`) {
		t.Fatalf("isCoinBase/valueIn: %s", data)
	}
	tx.Vin[0].ValueSat = insightAmount(9007199254740993)
	if out, err = insightTransaction(tx); err != nil || out.Vin[0].Value != "90071992.54740993" || out.Vin[0].ValueSat != "9007199254740993" {
		t.Fatal("large input amount", err)
	}
}
func TestInsightLifecycle(t *testing.T) {
	f := insightFixture()
	s := insightTestServer(t, f)
	s.ready.Store(false)
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/status"},
		{"GET", "/api/addr/A"},
		{"GET", "/api/addr/A/utxo"},
		{"GET", "/api/addrs/A/txs"},
		{"POST", "/api/tx/send"},
		{"GET", "/api/addr?address=A"},
		{"GET", "/api/addr/utxo?address=A"},
		{"GET", "/api/addrs/txs?address=A"},
	} {
		if w := insightRequest(s, tc.method, tc.path, ""); w.Code != 503 {
			t.Fatalf("unready %s %s returned %d", tc.method, tc.path, w.Code)
		}
	}
	for _, tc := range []struct {
		chain    bchain.ChainType
		decimals int
	}{{bchain.ChainEthereumType, 8}, {bchain.ChainBitcoinType, 18}} {
		if _, err := NewInsightServer("127.0.0.1:0", tc.chain, tc.decimals, f, nil); err == nil {
			t.Fatal("unsupported chain accepted")
		}
	}
	if _, err := NewInsightServer(":9876", bchain.ChainBitcoinType, 8, f, nil); err == nil {
		t.Fatal("implicit host")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	collision, err := NewInsightServer(listener.Addr().String(), bchain.ChainBitcoinType, 8, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = collision.Start(); err == nil {
		t.Fatal("port collision")
	}
	f = insightFixture()
	f.enter = make(chan struct{}, 32)
	f.release = make(chan struct{})
	s = insightTestServer(t, f)
	if err = s.Start(); err != nil {
		t.Fatal(err)
	}
	var requests sync.WaitGroup
	for i := 0; i < 32; i++ {
		requests.Add(1)
		go func() { defer requests.Done(); insightRequest(s, "GET", "/api/status", "") }()
	}
	for i := 0; i < 32; i++ {
		select {
		case <-f.enter:
		case <-time.After(5 * time.Second):
			t.Fatal("request did not enter")
		}
	}
	w := insightRequest(s, "GET", "/api/status", "")
	if w.Code != 503 || w.Header().Get("Retry-After") != "1" {
		t.Fatal(w)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { s.Shutdown(ctx); close(done) }()
	select {
	case <-done:
		t.Fatal("shutdown returned with active worker calls")
	case <-time.After(50 * time.Millisecond):
	}
	close(f.release)
	requests.Wait()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not finish")
	}
	if w = insightRequest(s, "GET", "/api/status", ""); w.Code != 503 {
		t.Fatal("accepted after shutdown")
	}
}

func TestInsightShutdownWaitsPastHTTPDeadline(t *testing.T) {
	f := insightFixture()
	f.enter = make(chan struct{}, 1)
	f.release = make(chan struct{})
	s := insightTestServer(t, f)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		client := &http.Client{Timeout: 5 * time.Second}
		response, err := client.Get("http://" + s.listener.Addr().String() + "/api/status")
		if err == nil {
			response.Body.Close()
		}
	}()
	select {
	case <-f.enter:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- s.Shutdown(ctx) }()
	select {
	case <-clientDone:
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP connection did not close after deadline")
	}
	select {
	case <-shutdownDone:
		t.Fatal("returned before worker finished")
	default:
	}
	close(f.release)
	select {
	case err := <-shutdownDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected HTTP deadline, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker drain did not complete")
	}
}
