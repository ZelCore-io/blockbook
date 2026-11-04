//go:build unittest

package server

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trezor/blockbook/api"
	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/bchain/coins/bitcore"
	"github.com/trezor/blockbook/bchain/coins/btc"
	"github.com/trezor/blockbook/db"
)

// The fixtures in testdata/electrumhttp are bodies the live BTX proxy
// (proxy.btx.zelcore.io, electrum-http 1.6.2 over ElectrumX) returned on
// 2026-11-01, byte for byte: the stubs below are fed the same chain data and
// the listener must answer identically.

type ehStubHistory struct {
	txid   string
	height uint32
}

type ehStubIndex struct {
	best    uint32
	hashes  map[uint32]string
	history map[string][]ehStubHistory // addrDesc hex -> ascending history, block order
	// inputOnly marks the txids that only spend from the address.
	inputOnly map[string]bool
	calls     int
}

// GetAddrDescTransactions yields the history as RocksDB does: blocks newest
// first and, within a block, the reverse of the indexing order, which adds
// the transactions paying the address (output index i) in block order, then
// those only spending from it (input index ^i) in block order.
func (x *ehStubIndex) GetAddrDescTransactions(addrDesc bchain.AddressDescriptor, lower, higher uint32, fn db.GetTransactionsCallback) error {
	x.calls++
	h := x.history[hex.EncodeToString(addrDesc)]
	var groups [][]ehStubHistory
	for _, e := range h {
		if n := len(groups); n == 0 || groups[n-1][0].height != e.height {
			groups = append(groups, nil)
		}
		groups[len(groups)-1] = append(groups[len(groups)-1], e)
	}
	for i := len(groups) - 1; i >= 0; i-- {
		var stored []ehStubHistory
		for _, e := range groups[i] {
			if !x.inputOnly[e.txid] {
				stored = append(stored, e)
			}
		}
		for _, e := range groups[i] {
			if x.inputOnly[e.txid] {
				stored = append(stored, e)
			}
		}
		for j := len(stored) - 1; j >= 0; j-- {
			e := stored[j]
			if e.height < lower || e.height > higher {
				continue
			}
			indexes := []int32{0}
			if x.inputOnly[e.txid] {
				indexes = []int32{^int32(0)}
			}
			if err := fn(e.txid, e.height, indexes); err != nil {
				if _, ok := err.(*db.StopIteration); ok {
					return nil
				}
				return err
			}
		}
	}
	return nil
}

func (x *ehStubIndex) GetBestBlock() (uint32, string, error) {
	return x.best, x.hashes[x.best], nil
}

func (x *ehStubIndex) GetBlockHash(height uint32) (string, error) {
	return x.hashes[height], nil
}

type ehStubMempool struct {
	outpoints map[string][]bchain.Outpoint // addrDesc hex
	times     map[string]uint32
}

func (m *ehStubMempool) GetAddrDescTransactions(addrDesc bchain.AddressDescriptor) ([]bchain.Outpoint, error) {
	return m.outpoints[hex.EncodeToString(addrDesc)], nil
}

func (m *ehStubMempool) GetTransactionTime(txid string) uint32 { return m.times[txid] }

type ehStubWorker struct {
	addresses map[string]*api.Address
	utxos     map[string]api.Utxos
	txs       map[string]*api.Tx
}

func (w *ehStubWorker) GetAddress(addr string, page, size int, d api.AccountDetails, f *api.AddressFilter, c string) (*api.Address, error) {
	if a, ok := w.addresses[addr]; ok {
		return a, nil
	}
	return &api.Address{AddrStr: addr, BalanceSat: (*api.Amount)(big.NewInt(0)), UnconfirmedBalanceSat: (*api.Amount)(big.NewInt(0))}, nil
}

func (w *ehStubWorker) GetAddressUtxo(addr string, onlyConfirmed bool) (api.Utxos, error) {
	return w.utxos[addr], nil
}

func (w *ehStubWorker) GetTransaction(txid string, spending, specific bool) (*api.Tx, error) {
	if tx, ok := w.txs[txid]; ok {
		return tx, nil
	}
	return nil, errors.New("not found")
}

// ehStubRPC answers getrawtransaction from fixtures and records every call.
type ehStubRPC struct {
	raw       map[string]string          // txid -> raw hex
	verbose   map[string]json.RawMessage // txid -> verbose JSON
	headers   map[string]string          // block hash -> header hex
	blocks    map[string][]string        // block hash -> txids in block order
	broadcast func(string) (string, error)
	calls     []string
}

func (r *ehStubRPC) call(method string, params []interface{}, result interface{}) error {
	r.calls = append(r.calls, fmt.Sprint(method, params))
	var res interface{}
	switch method {
	case "getrawtransaction":
		txid := params[0].(string)
		if params[1] == 1 {
			v, ok := r.verbose[txid]
			if !ok {
				return &bchain.RPCError{Code: -5, Message: "No such mempool or blockchain transaction. Use gettransaction for wallet transactions."}
			}
			return json.Unmarshal(v, result)
		}
		v, ok := r.raw[txid]
		if !ok {
			return &bchain.RPCError{Code: -5, Message: "No such mempool or blockchain transaction. Use gettransaction for wallet transactions."}
		}
		res = v
	case "getblock":
		v, ok := r.blocks[params[0].(string)]
		if !ok {
			return &bchain.RPCError{Code: -5, Message: "Block not found"}
		}
		res = map[string]interface{}{"tx": v}
	case "getblockheader":
		v, ok := r.headers[params[0].(string)]
		if !ok {
			return &bchain.RPCError{Code: -5, Message: "Block not found"}
		}
		res = v
	default:
		return &bchain.RPCError{Code: -32601, Message: "Method not found"}
	}
	b, _ := json.Marshal(res)
	return json.Unmarshal(b, result)
}

func ehFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(ehFixturePath(name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ehFixturePath locates a fixture from this file: TestMain changes the
// working directory.
func ehFixturePath(name string) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "testdata", "electrumhttp", name)
}

func ehParser() bchain.BlockChainParser {
	return bitcore.NewBitcoreParser(bitcore.GetChainParams("main"), &btc.Configuration{})
}

func ehDescHex(t *testing.T, p bchain.BlockChainParser, addr string) string {
	t.Helper()
	d, err := p.GetAddrDescFromAddress(addr)
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(d)
}

type ehEnv struct {
	s       *ElectrumHTTPServer
	index   *ehStubIndex
	mempool *ehStubMempool
	worker  *ehStubWorker
	rpc     *ehStubRPC
	parser  bchain.BlockChainParser
}

const ehBest = 1890700

func newEHEnv(t *testing.T) *ehEnv {
	t.Helper()
	e := &ehEnv{
		index:   &ehStubIndex{best: ehBest, hashes: map[uint32]string{}, history: map[string][]ehStubHistory{}, inputOnly: map[string]bool{}},
		mempool: &ehStubMempool{outpoints: map[string][]bchain.Outpoint{}, times: map[string]uint32{}},
		worker:  &ehStubWorker{addresses: map[string]*api.Address{}, utxos: map[string]api.Utxos{}, txs: map[string]*api.Tx{}},
		rpc:     &ehStubRPC{raw: map[string]string{}, verbose: map[string]json.RawMessage{}, headers: map[string]string{}, blocks: map[string][]string{}},
		parser:  ehParser(),
	}
	s, err := NewElectrumHTTPServer("127.0.0.1:0", e.parser, e.worker, e.index, e.mempool, e.rpc.call, func(raw string) (string, error) {
		if e.rpc.broadcast == nil {
			return "", errors.New("no broadcast stub")
		}
		return e.rpc.broadcast(raw)
	})
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Unix(1793530000, 0) }
	s.SetReady()
	e.s = s
	return e
}

// loadHistory feeds an address's live entirehistory to the index stub.
func (e *ehEnv) loadHistory(t *testing.T, addr, fixture string) {
	t.Helper()
	var entries []struct {
		TxHash string `json:"tx_hash"`
		Height uint32 `json:"height"`
	}
	if err := json.Unmarshal(ehFixture(t, fixture), &entries); err != nil {
		t.Fatal(err)
	}
	var h []ehStubHistory
	for _, x := range entries {
		h = append(h, ehStubHistory{x.TxHash, x.Height})
	}
	e.index.history[ehDescHex(t, e.parser, addr)] = h
}

// loadUtxos feeds the live listunspent to the worker stub in the worker's
// order (newest first).
func (e *ehEnv) loadUtxos(t *testing.T, addr, fixture string) {
	t.Helper()
	var entries []struct {
		TxHash string `json:"tx_hash"`
		TxPos  int32  `json:"tx_pos"`
		Height int    `json:"height"`
		Value  int64  `json:"value"`
	}
	if err := json.Unmarshal(ehFixture(t, fixture), &entries); err != nil {
		t.Fatal(err)
	}
	var u api.Utxos
	for i := len(entries) - 1; i >= 0; i-- {
		x := entries[i]
		u = append(u, api.Utxo{Txid: x.TxHash, Vout: x.TxPos, AmountSat: (*api.Amount)(big.NewInt(x.Value)), Height: x.Height, Confirmations: ehBest - x.Height + 1})
	}
	e.worker.utxos[addr] = u
}

func (e *ehEnv) loadTx(t *testing.T, txid string) {
	t.Helper()
	var raw string
	if err := json.Unmarshal(ehFixture(t, "transaction_"+txid+".json"), &raw); err != nil {
		t.Fatal(err)
	}
	e.rpc.raw[txid] = raw
	if b, err := os.ReadFile(ehFixturePath("transactionverbose_" + txid + ".json")); err == nil {
		e.rpc.verbose[txid] = b
	}
}

// addWorkerTx gives the worker stub the transaction as the worker resolves
// it from the index: inputs carry the spent output's value and descriptor.
func (e *ehEnv) addWorkerTx(t *testing.T, txid string) {
	t.Helper()
	tx, err := e.parser.ParseTx(mustHex(t, e.rpc.raw[txid]))
	if err != nil {
		t.Fatal(err)
	}
	wtx := &api.Tx{Txid: txid, Confirmations: 1}
	for i, in := range tx.Vin {
		v := api.Vin{N: i, Txid: in.Txid, Vout: in.Vout, Sequence: int64(in.Sequence), Hex: in.ScriptSig.Hex, Coinbase: in.Coinbase}
		if in.Coinbase == "" {
			prev, err := e.parser.ParseTx(mustHex(t, e.rpc.raw[in.Txid]))
			if err != nil {
				t.Fatal(err)
			}
			out := prev.Vout[in.Vout]
			v.ValueSat = (*api.Amount)(new(big.Int).Set(&out.ValueSat))
			if v.AddrDesc, err = e.parser.GetAddrDescFromVout(&out); err != nil {
				t.Fatal(err)
			}
		}
		wtx.Vin = append(wtx.Vin, v)
	}
	e.worker.txs[txid] = wtx
}

func (e *ehEnv) rawTxCalls() int {
	n := 0
	for _, c := range e.rpc.calls {
		if strings.HasPrefix(c, "getrawtransaction") && strings.HasSuffix(c, " 0]") {
			n++
		}
	}
	return n
}

const ehProxyPrefix = "/?server=127.0.0.1&port=50002&contype=tls&coin=bitcore&call="

func (e *ehEnv) get(target string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "http://proxy.example"+target, nil)
	r.RequestURI = target
	w := httptest.NewRecorder()
	e.s.ServeHTTP(w, r)
	return w
}

func ehExpect(t *testing.T, w *httptest.ResponseRecorder, want string) {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("status %d, body %s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != want {
		t.Fatalf("body mismatch\n got: %.2000s\nwant: %.2000s", got, want)
	}
}

const (
	ehAddrUtxo  = "2PBEcZzuTT4rXTmUujL2w4pii6WwDmnCoW" // 19 txs, 15 utxos
	ehAddrLarge = "2PnoneQAdaAz2DHYGFCwQXiYpPBp2dzqTQ" // 719 txs
	ehAddrNice  = "2Y3eFywAAFsap9uq9riBGAChN2hX5jnrhu" // 2 txs
	ehTx1       = "fcd4f2e45787a33571bc9b2ce939d6e8e51fa053296de9240f05455702bd9540"
	ehTx2       = "ef625505692d7410c5720bf9e5e3f94c70b3e74c635de7f29943403b2fc2e336"
	ehTx0       = "f03e801919e17a1ac1e0c621bdac0e35c3fb7c3b2e33216f422ae576fdd19bf6"
)

// Every call the wallet uses, and the others, against live proxy bodies.
func TestElectrumHTTPLiveFixtures(t *testing.T) {
	e := newEHEnv(t)
	e.loadHistory(t, ehAddrUtxo, "entirehistory_2PBEc.json")
	e.loadHistory(t, ehAddrLarge, "entirehistory_2Pnone.json")
	e.loadHistory(t, ehAddrNice, "entirehistory_2Y3e.json")
	e.loadUtxos(t, ehAddrUtxo, "utxo_2PBEc.json")
	e.worker.addresses[ehAddrUtxo] = &api.Address{AddrStr: ehAddrUtxo, BalanceSat: (*api.Amount)(big.NewInt(923600000000)), UnconfirmedBalanceSat: (*api.Amount)(big.NewInt(0))}
	for _, txid := range []string{ehTx0, ehTx1, ehTx2} {
		e.loadTx(t, txid)
	}
	e.addWorkerTx(t, ehTx1)
	e.addWorkerTx(t, ehTx2)
	for _, tc := range []struct{ name, call, param, fixture string }{
		{"balance", "balance", ehAddrUtxo, "balance_2PBEc.json"},
		{"utxo", "utxo", ehAddrUtxo, "utxo_2PBEc.json"},
		{"niceutxo", "niceutxo", ehAddrUtxo, "niceutxo_2PBEc.json"},
		{"history", "history", ehAddrUtxo, "history_2PBEc.json"},
		{"entirehistory", "entirehistory", ehAddrUtxo, "entirehistory_2PBEc.json"},
		{"history truncated to the last 100 of 719", "history", ehAddrLarge, "history_2Pnone.json"},
		{"entirehistory not truncated", "entirehistory", ehAddrLarge, "entirehistory_2Pnone.json"},
		{"nicehistory", "nicehistory", ehAddrNice, "nicehistory_2Y3e.json"},
		{"niceentirehistory", "niceentirehistory", ehAddrNice, "nicehistory_2Y3e.json"},
		{"transaction", "transaction", ehTx1, "transaction_" + ehTx1 + ".json"},
		{"transactionnonverbose", "transactionnonverbose", ehTx2, "transaction_" + ehTx2 + ".json"},
		{"transactionverbose", "transactionverbose", ehTx1, "transactionverbose_" + ehTx1 + ".json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ehExpect(t, e.get(ehProxyPrefix+tc.call+"&param="+tc.param), string(ehFixture(t, tc.fixture)))
		})
	}
	// nicehistory took its input values from the index: no raw previous
	// transaction was loaded from the daemon.
	before := e.rawTxCalls()
	e.get(ehProxyPrefix + "niceentirehistory&param=" + ehAddrNice)
	if n := e.rawTxCalls() - before; n != 0 {
		t.Fatalf("nicehistory loaded %d raw transactions", n)
	}
}

// The live proxy's error bodies for bad input.
func TestElectrumHTTPErrors(t *testing.T) {
	e := newEHEnv(t)
	for _, tc := range []struct{ target, want string }{
		{ehProxyPrefix + "balance", "Error: undefined has no matching Script"},
		{ehProxyPrefix + "balance&param=xyz", "Error: xyz has no matching Script"},
		{ehProxyPrefix + "balance&param=", "Error:  has no matching Script"},
		// A Bitcoin address is not a BTX address.
		{ehProxyPrefix + "utxo&param=1BoatSLRHtKNngkdXEeobR76b53LETtpyT", "Error: 1BoatSLRHtKNngkdXEeobR76b53LETtpyT has no matching Script"},
		{ehProxyPrefix + "niceutxo&param=xyz", "Error: xyz has no matching Script"},
		{ehProxyPrefix + "nicehistory&param=xyz", "Error: xyz has no matching Script"},
		{ehProxyPrefix + "transaction&param=zz", "Error: zz should be a transaction hash"},
		{ehProxyPrefix + "transactionverbose&param=00", "Error: 00 should be a transaction hash"},
		{ehProxyPrefix + "transaction", "Error: None should be a transaction hash"},
		{ehProxyPrefix + "transactionverbose&param=" + strings.Repeat("ab", 32), "Error: daemon error: DaemonError({'code': -5, 'message': 'No such mempool or blockchain transaction. Use gettransaction for wallet transactions.'})"},
		{ehProxyPrefix + "header&param=abc", "Error: abc should be a non-negative integer"},
		{ehProxyPrefix + "header&param=-1", "Error: -1 should be a non-negative integer"},
		{ehProxyPrefix + "header", "Error: None should be a non-negative integer"},
		{ehProxyPrefix + "header&param=12345678", "Error: height 12,345,678 out of range"},
		{ehProxyPrefix + "foo&param=1", "Error: Unknown call"},
		{"/?call=height", "Error: Server is undefined"},
		{"/?server=127.0.0.1", "Error: Call is undefined"},
		{"/", "Error: Call is undefined"},
		{"/?server=127.0.0.1&call=", "Error: Call is undefined"},
		// An empty server sends the URL through the compat parsing, which loses the call.
		{"/?server=&call=height", "Error: Call is undefined"},
		{"/?server=&call=height?server=x", "Error: Call is undefined"},
	} {
		t.Run(tc.target, func(t *testing.T) {
			w := e.get(tc.target)
			ehExpect(t, w, tc.want)
			if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
				t.Fatalf("content type %q", ct)
			}
		})
	}
	// A transport failure is not echoed.
	e.s.rpc = func(string, []interface{}, interface{}) error {
		return errors.New(`Post "http://user:secret@127.0.0.1:8332": dial tcp: connection refused`)
	}
	w := e.get(ehProxyPrefix + "transaction&param=" + ehTx1)
	ehExpect(t, w, "Error: internal error")
	// Malformed percent-encoding fails like decodeURIComponent.
	if w := e.get("/?server=a&call=balance&param=%zz"); w.Code != 400 || w.Body.String() != "Error: URI malformed" {
		t.Fatalf("malformed URI: %d %q", w.Code, w.Body.String())
	}
}

func TestElectrumHTTPQueryParsing(t *testing.T) {
	for _, tc := range []struct {
		target               string
		call, param, server  string
		callSet, paramSet, _ bool
	}{
		{target: ehProxyPrefix + "balance&param=ADDR", call: "balance", param: "ADDR", server: "127.0.0.1", callSet: true, paramSet: true},
		// The whole URL is decoded first, so an encoded '&' splits parameters.
		{target: "/?server=s%26call%3Dutxo%26param%3DADDR", call: "utxo", param: "ADDR", server: "s", callSet: true, paramSet: true},
		// qs then turns '+' into a space.
		{target: "/?server=s&call=balance&param=a+b", call: "balance", param: "a b", server: "s", callSet: true, paramSet: true},
		// Backward-compatible form: /?server=X?call=Y?param=Z (raw, undecoded).
		{target: "/?server=127.0.0.1?call=balance?param=ADDR", call: "balance", param: "ADDR", server: "127.0.0.1", callSet: true, paramSet: true},
		{target: "/?server=127.0.0.1?call=height", call: "height", server: "127.0.0.1", callSet: true},
		// Mobile mistake: ...&call=balance?param=ADDR.
		{target: "/?server=127.0.0.1&coin=bitcore&call=balance?param=ADDR", call: "balance", param: "ADDR", server: "127.0.0.1", callSet: true, paramSet: true},
		// The mobile fix keeps what follows "&call=" up to "?param=".
		{target: "/?server=s&call=balance&coin=bitcore?param=ADDR", call: "balance&coin=bitcore", param: "ADDR", server: "s", callSet: true, paramSet: true},
		// height takes no param, so no rewrite.
		{target: "/?server=s&call=height", call: "height", server: "s", callSet: true},
		// A call without param and not after '&' is lost by the mobile fix.
		{target: "/?call=balance&server=s", server: "s"},
		// Only the part between the first and second "/?" is parsed.
		{target: "/?server=s&call=balance&param=A/?param=B", call: "balance", param: "A", server: "s", callSet: true, paramSet: true},
	} {
		t.Run(tc.target, func(t *testing.T) {
			q, err := parseElectrumHTTPQuery(tc.target)
			if err != nil {
				t.Fatal(err)
			}
			if q.call.s != tc.call || q.call.set != tc.callSet || q.param.s != tc.param || q.param.set != tc.paramSet || q.server.s != tc.server {
				t.Fatalf("got call=%+v param=%+v server=%+v", q.call, q.param, q.server)
			}
		})
	}
	// Repeated keys make an array: never a known call.
	q, _ := parseElectrumHTTPQuery("/?server=s&call=balance&call=utxo&param=x")
	if !q.call.multi || q.call.s != "balance,utxo" {
		t.Fatalf("repeated call: %+v", q.call)
	}
	if _, err := parseElectrumHTTPQuery("/?server=s&call=%E0%A4%A"); err == nil {
		t.Fatal("truncated escape accepted")
	}
	if _, err := parseElectrumHTTPQuery("/?server=s&call=%C3"); err == nil {
		t.Fatal("invalid UTF-8 escape accepted")
	}
}

// Both backward-compatible forms answer, using the chain's own network
// (the proxy needs coin= for that).
func TestElectrumHTTPCompatForms(t *testing.T) {
	e := newEHEnv(t)
	e.worker.addresses[ehAddrUtxo] = &api.Address{AddrStr: ehAddrUtxo, BalanceSat: (*api.Amount)(big.NewInt(923600000000)), UnconfirmedBalanceSat: (*api.Amount)(big.NewInt(-5))}
	want := `{"confirmed":923600000000,"unconfirmed":-5}`
	ehExpect(t, e.get("/?server=127.0.0.1?call=balance?param="+ehAddrUtxo), want)
	ehExpect(t, e.get("/?server=127.0.0.1&call=balance?param="+ehAddrUtxo), want)
}

func TestElectrumHTTPMempool(t *testing.T) {
	e := newEHEnv(t)
	addr := ehAddrNice
	desc := ehDescHex(t, e.parser, addr)
	e.index.history[desc] = []ehStubHistory{{ehTx1, 326137}, {"c1", 400000}, {"c2", 400000}, {ehTx2, 400001}}
	// m1 spends a confirmed output; m2 spends m1's output; ehTx2 is reported
	// by the mempool but already confirmed and stays a confirmed entry.
	e.mempool.outpoints[desc] = []bchain.Outpoint{{Txid: "m2", Vout: 0}, {Txid: "m1", Vout: 1}, {Txid: "m1", Vout: ^0}, {Txid: ehTx2, Vout: 0}}
	e.mempool.times = map[string]uint32{"m1": 1, "m2": 2}
	e.worker.txs["m1"] = &api.Tx{Txid: "m1", Vin: []api.Vin{{Txid: "c2"}}, FeesSat: (*api.Amount)(big.NewInt(2260))}
	e.worker.txs["m2"] = &api.Tx{Txid: "m2", Vin: []api.Vin{{Txid: "m1"}}, FeesSat: (*api.Amount)(big.NewInt(1130))}
	ehExpect(t, e.get(ehProxyPrefix+"entirehistory&param="+addr),
		`[{"tx_hash":"`+ehTx1+`","height":326137},{"tx_hash":"c1","height":400000},{"tx_hash":"c2","height":400000},{"tx_hash":"`+ehTx2+`","height":400001},`+
			`{"tx_hash":"m1","height":0,"fee":2260},{"tx_hash":"m2","height":-1,"fee":1130}]`)

	// listunspent: confirmed in chain order, then mempool outputs at height 0;
	// niceutxo keeps the confirmed ones.
	e.worker.utxos[addr] = api.Utxos{
		{Txid: "m2", Vout: 0, AmountSat: (*api.Amount)(big.NewInt(7))},
		{Txid: "c2", Vout: 1, AmountSat: (*api.Amount)(big.NewInt(30)), Height: 400000, Confirmations: 10},
		{Txid: "c2", Vout: 0, AmountSat: (*api.Amount)(big.NewInt(20)), Height: 400000, Confirmations: 10},
		{Txid: "c1", Vout: 0, AmountSat: (*api.Amount)(big.NewInt(10)), Height: 300000, Confirmations: 100010},
	}
	ehExpect(t, e.get(ehProxyPrefix+"utxo&param="+addr),
		`[{"tx_hash":"c1","tx_pos":0,"height":300000,"value":10},{"tx_hash":"c2","tx_pos":0,"height":400000,"value":20},{"tx_hash":"c2","tx_pos":1,"height":400000,"value":30},{"tx_hash":"m2","tx_pos":0,"height":0,"value":7}]`)
	script := "76a914d0c320db3fbd0abe2b6fe31a3bca4fed8ce8669588ac"
	ehExpect(t, e.get(ehProxyPrefix+"niceutxo&param="+addr),
		`[{"txid":"c1","vout":0,"scriptPubKey":"`+script+`","satoshis":10,"height":300000},{"txid":"c2","vout":0,"scriptPubKey":"`+script+`","satoshis":20,"height":400000},{"txid":"c2","vout":1,"scriptPubKey":"`+script+`","satoshis":30,"height":400000}]`)
}

func TestElectrumHTTPLimits(t *testing.T) {
	e := newEHEnv(t)
	addr := ehAddrNice
	desc := ehDescHex(t, e.parser, addr)
	// 201 entries: history answers the last 100; 200 are answered whole.
	var h []ehStubHistory
	for i := 0; i < 201; i++ {
		h = append(h, ehStubHistory{fmt.Sprintf("t%03d", i), uint32(1000 + i)})
	}
	e.index.history[desc] = h
	var got []map[string]interface{}
	if err := json.Unmarshal(e.get(ehProxyPrefix+"history&param="+addr).Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 100 || got[0]["tx_hash"] != "t101" || got[99]["tx_hash"] != "t200" {
		t.Fatalf("truncated history: %d entries, first %v", len(got), got[0])
	}
	e.index.history[desc] = h[:200]
	if err := json.Unmarshal(e.get(ehProxyPrefix+"history&param="+addr).Body.Bytes(), &got); err != nil || len(got) != 200 {
		t.Fatalf("200 entries: %d %v", len(got), err)
	}
	// ElectrumX's history limit.
	h = h[:0]
	for i := 0; i < ehHistoryLimit; i++ {
		h = append(h, ehStubHistory{fmt.Sprintf("t%05d", i), uint32(i)})
	}
	e.index.history[desc] = h
	ehExpect(t, e.get(ehProxyPrefix+"entirehistory&param="+addr), "Error: history too large")
	e.index.history[desc] = h[:ehHistoryLimit-1]
	if w := e.get(ehProxyPrefix + "entirehistory&param=" + addr); !strings.HasPrefix(w.Body.String(), "[") {
		t.Fatalf("history under the limit: %.100s", w.Body.String())
	}

	// utxo and niceutxo keep the first (oldest) 600 outputs.
	var u api.Utxos
	for i := 700; i > 0; i-- {
		u = append(u, api.Utxo{Txid: fmt.Sprintf("u%03d", i), AmountSat: (*api.Amount)(big.NewInt(1)), Height: i, Confirmations: 1000})
	}
	e.worker.utxos[addr] = u
	for _, call := range []string{"utxo", "niceutxo"} {
		var rows []map[string]interface{}
		if err := json.Unmarshal(e.get(ehProxyPrefix+call+"&param="+addr).Body.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) != 600 || rows[0]["height"] != float64(1) || rows[599]["height"] != float64(600) {
			t.Fatalf("%s: %d rows", call, len(rows))
		}
	}

	// nicehistory loads only the newest 31 transactions.
	e.index.history[desc] = nil
	for i := 0; i < 40; i++ {
		e.index.history[desc] = append(e.index.history[desc], ehStubHistory{ehTx1, uint32(326137 + i)})
	}
	e.loadTx(t, ehTx0)
	e.loadTx(t, ehTx1)
	e.addWorkerTx(t, ehTx1)
	var nice []map[string]interface{}
	if err := json.Unmarshal(e.get(ehProxyPrefix+"nicehistory&param="+addr).Body.Bytes(), &nice); err != nil {
		t.Fatal(err)
	}
	if len(nice) != 31 || nice[0]["height"] != float64(326137+39) || nice[30]["height"] != float64(326137+9) {
		t.Fatalf("nicehistory: %d entries", len(nice))
	}
}

// A mempool transaction (no time, no confirmations), a coinbase input, a
// P2SH input and a witness input in nicehistory.
func TestElectrumHTTPNiceHistoryShapes(t *testing.T) {
	e := newEHEnv(t)
	p := e.parser
	addr := ehAddrNice
	desc := ehDescHex(t, p, addr)
	p2sh := "a914b7fcbed4f9a2e2bf6ac64ba79ae0a4ab0ab0d6a187"
	p2wpkh := "0014751e76e8199196d454941c45d1b3a323f1433bd6"
	// prev pays 0.29 to P2SH (n=0) and 1.5 to P2WPKH (n=1).
	prev := "0100000001" + strings.Repeat("11", 32) + "00000000" + "00" + "ffffffff" + "02" +
		"4081ba01" + "00000000" + "17" + p2sh +
		"80d1f008" + "00000000" + "16" + p2wpkh + "00000000"
	prevTx, err := p.ParseTx(mustHex(t, prev))
	if err != nil {
		t.Fatal(err)
	}
	e.rpc.raw[prevTx.Txid] = prev
	sig := "47" + "30440220" + strings.Repeat("aa", 32) + "0220" + strings.Repeat("bb", 32) + "01"
	verbose := `{"txid":"aaaa","version":1,"locktime":0,"vin":[` +
		`{"coinbase":"03abcdef","sequence":4294967295},` +
		`{"txid":"` + prevTx.Txid + `","vout":0,"scriptSig":{"asm":"x","hex":"` + sig + `"},"sequence":4294967294},` +
		`{"txid":"` + prevTx.Txid + `","vout":1,"scriptSig":{"asm":"","hex":""},"txinwitness":["00"],"sequence":4294967295}` +
		`],"vout":[{"value":0.29,"n":0,"scriptPubKey":{"hex":"` + p2sh + `","addresses":["x"]}},{"value":1.49990000,"n":1,"scriptPubKey":{"asm":"OP_RETURN 6869","hex":"6a026869"}}],"hex":"00"}`
	e.rpc.verbose["aaaa"] = json.RawMessage(verbose)
	e.mempool.outpoints[desc] = []bchain.Outpoint{{Txid: "aaaa", Vout: 0}}
	// The P2SH input comes resolved from the index; the witness input does
	// not, so its previous transaction is read from the daemon.
	e.worker.txs["aaaa"] = &api.Tx{Txid: "aaaa", FeesSat: (*api.Amount)(big.NewInt(10000)), Vin: []api.Vin{
		{N: 0, Coinbase: "03abcdef", Sequence: 4294967295},
		{N: 1, Txid: prevTx.Txid, Vout: 0, Hex: sig, Sequence: 4294967294, ValueSat: (*api.Amount)(big.NewInt(29000000)), AddrDesc: mustHex(t, p2sh)},
		{N: 2, Txid: prevTx.Txid, Vout: 1, Hex: "", Sequence: 4294967295},
	}}
	p2shAddr := ehScriptAddr(t, p, p2sh)
	asm := "30440220" + strings.Repeat("aa", 32) + "0220" + strings.Repeat("bb", 32) + "01"
	want := `[{"txid":"aaaa","version":1,"locktime":0,"vin":[` +
		`{"txid":"` + prevTx.Txid + `","n":0,"script":"` + asm + `","sequence":4294967294,"scriptSig":{"hex":"` + sig + `","asm":"` + asm + `"},"addr":"` + p2shAddr + `","value":0.29,"valueSat":29000000,"satoshis":29000000},` +
		`{"txid":"` + prevTx.Txid + `","n":1,"script":"","sequence":4294967295,"scriptSig":{"hex":"","asm":""},"addr":"","value":1.5,"valueSat":150000000,"satoshis":150000000}],` +
		`"vout":[{"value":0.29,"n":0,"scriptPubKey":{"hex":"` + p2sh + `","addresses":["x"]},"satoshi":28999999.999999996,"valueSat":28999999.999999996},` +
		`{"value":1.4999,"n":1,"scriptPubKey":{"asm":"OP_RETURN 6869","hex":"6a026869"},"satoshi":149990000,"valueSat":149990000}],` +
		`"time":1793530000,"confirmations":0,"valueInSat":179000000,"valueOutSat":178990000,"fees":10000,"height":0,"hex":"00"}]`
	ehExpect(t, e.get(ehProxyPrefix+"nicehistory&param="+addr), want)
	if n := e.rawTxCalls(); n != 1 {
		t.Fatalf("%d raw transaction loads, want 1 (the unresolved input)", n)
	}
}

func ehScriptAddr(t *testing.T, p bchain.BlockChainParser, scriptHex string) string {
	t.Helper()
	a, _, err := p.GetAddressesFromAddrDesc(mustHex(t, scriptHex))
	if err != nil || len(a) != 1 {
		t.Fatal(a, err)
	}
	return a[0]
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestElectrumHTTPHeightAndHeader(t *testing.T) {
	e := newEHEnv(t)
	// Bitcoin's genesis header, served as the tip and as height 0.
	genesis := "0100000000000000000000000000000000000000000000000000000000000000000000003ba3edfd7a7b12b27ac72c3e67768f617fc81bc3888a51323a9fb8aa4b1e5e4a29ab5f49ffff001d1dac2b7c"
	e.index.hashes[ehBest] = "tiphash"
	e.index.hashes[0] = "genesishash"
	e.rpc.headers["tiphash"] = genesis
	e.rpc.headers["genesishash"] = genesis
	ehExpect(t, e.get(ehProxyPrefix+"height"), `{"hex":"`+genesis+`","height":1890700}`)
	ehExpect(t, e.get(ehProxyPrefix+"header&param=0"),
		`{"block_height":0,"version":1,"prev_block_hash":"0000000000000000000000000000000000000000000000000000000000000000","merkle_root":"4a5e1e4baab89f3a32518a88c31bc87f618f76673e2cc77ab2127b7afdeda33b","timestamp":1231006505,"bits":486604799,"nonce":2083236893}`)
	ehExpect(t, e.get(ehProxyPrefix+"header&param=1890701"), "Error: height 1,890,701 out of range")
}

func TestElectrumHTTPBroadcast(t *testing.T) {
	e := newEHEnv(t)
	var sent []string
	e.rpc.broadcast = func(raw string) (string, error) {
		sent = append(sent, raw)
		if raw == "0100bad" {
			return "", &bchain.RPCError{Code: -26, Message: "min relay fee not met"}
		}
		return ehTx1, nil
	}
	ehExpect(t, e.get(ehProxyPrefix+"broadcast&param=0100abcd"), `"`+ehTx1+`"`)
	ehExpect(t, e.get(ehProxyPrefix+"broadcast&param=0100bad"), "Error: the transaction was rejected by network rules.\n\nmin relay fee not met\n[0100bad]")
	if len(sent) != 2 || sent[0] != "0100abcd" {
		t.Fatalf("sent %v", sent)
	}
}

func TestElectrumHTTPHTTP(t *testing.T) {
	e := newEHEnv(t)
	w := e.get(ehProxyPrefix + "balance&param=" + ehAddrUtxo)
	for k, v := range map[string]string{
		"Access-Control-Allow-Origin":   "*",
		"Access-Control-Request-Method": "*",
		"Access-Control-Allow-Methods":  "OPTIONS, GET",
		"Access-Control-Allow-Headers":  "*",
		"Content-Type":                  "application/json; charset=utf-8",
		"X-Content-Type-Options":        "nosniff",
	} {
		if got := w.Header().Get(k); got != v {
			t.Fatalf("%s = %q, want %q", k, got, v)
		}
	}
	r := httptest.NewRequest("OPTIONS", ehProxyPrefix+"broadcast&param=00", nil)
	rec := httptest.NewRecorder()
	called := false
	e.rpc.broadcast = func(string) (string, error) { called = true; return "", nil }
	e.s.ServeHTTP(rec, r)
	if rec.Code != 204 || called || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("OPTIONS: %d called=%v", rec.Code, called)
	}
	r = httptest.NewRequest("POST", ehProxyPrefix+"height", nil)
	rec = httptest.NewRecorder()
	e.s.ServeHTTP(rec, r)
	if rec.Code != 405 {
		t.Fatalf("POST: %d", rec.Code)
	}
	// Not ready: 503 before any backend access.
	e.s.ready.Store(false)
	calls := e.index.calls
	if w := e.get(ehProxyPrefix + "history&param=" + ehAddrUtxo); w.Code != 503 || e.index.calls != calls {
		t.Fatalf("not ready: %d", w.Code)
	}
}

func TestElectrumHTTPConstructorAndLifecycle(t *testing.T) {
	p := ehParser()
	idx, mp, wk := &ehStubIndex{}, &ehStubMempool{}, &ehStubWorker{}
	if _, err := NewElectrumHTTPServer(":9999", p, wk, idx, mp, nil, nil); err == nil {
		t.Fatal("empty host accepted")
	}
	if _, err := NewElectrumHTTPServer("127.0.0.1:0", p, nil, idx, mp, nil, nil); err == nil {
		t.Fatal("nil worker accepted")
	}
	s, err := NewElectrumHTTPServer("127.0.0.1:0", p, wk, idx, mp, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableRateLimit("btx"); err == nil {
		t.Fatal("rate limit after Start accepted")
	}
	if err := s.Start(); err == nil {
		t.Fatal("second Start accepted")
	}
	if err := s.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Without the backend RPC, raw calls fail without leaking anything.
	e := newEHEnv(t)
	e.s.rpc = nil
	ehExpect(t, e.get(ehProxyPrefix+"transaction&param="+ehTx1), "Error: internal error")
}

func TestElectrumHTTPRateLimit(t *testing.T) {
	t.Setenv("BTXTEST_ELECTRUM_HTTP_RATE_LIMIT", "60")
	t.Setenv("BTXTEST_ELECTRUM_HTTP_BURST", "3")
	e := newEHEnv(t)
	if err := e.s.EnableRateLimit("btxtest"); err != nil {
		t.Fatal(err)
	}
	codes := []int{}
	for i := 0; i < 5; i++ {
		r := httptest.NewRequest("GET", ehProxyPrefix+"balance&param="+ehAddrUtxo, nil)
		r.RemoteAddr = "203.0.113.7:4000"
		rec := httptest.NewRecorder()
		e.s.serve(rec, r)
		codes = append(codes, rec.Code)
	}
	if fmt.Sprint(codes) != "[200 200 200 429 429]" {
		t.Fatalf("codes %v", codes)
	}
	t.Setenv("BTXTEST_ELECTRUM_HTTP_RATE_LIMIT", "0")
	if l, err := newElectrumHTTPRateLimiter("btxtest"); err != nil || l != nil {
		t.Fatalf("disabled limiter: %v %v", l, err)
	}
	t.Setenv("BTXTEST_ELECTRUM_HTTP_RATE_LIMIT", "x")
	if _, err := newElectrumHTTPRateLimiter("btxtest"); err == nil {
		t.Fatal("bad rate accepted")
	}
}

func TestElectrumHTTPJSNumbers(t *testing.T) {
	// Variables, so the products are computed in float64 at run time (Go
	// folds constant expressions exactly).
	v029, v73, sat73, sat1e8, satExp := 0.29, 73.00346792, 7300346792.0, 1e8, 1e-8
	for _, tc := range []struct {
		f    float64
		want string
	}{
		{v029 * sat1e8, "28999999.999999996"},
		{v73 * sat1e8, "7300346792.000001"},
		{satExp * sat73, "73.00346792"},
		{1e-8, "1e-8"},
		{1.5e-7, "1.5e-7"},
		{0.000001, "0.000001"},
		{1e21, "1e+21"},
		{123456789012345680000, "123456789012345680000"},
		{-0.0, "0"},
		{100, "100"},
	} {
		var b bytes.Buffer
		jsNumber(&b, tc.f)
		if b.String() != tc.want {
			t.Errorf("jsNumber(%v) = %s, want %s", tc.f, b.String(), tc.want)
		}
	}
	var b bytes.Buffer
	jsString(&b, "a\"b\\c\n\x01<>& ")
	if b.String() != `"a\"b\\c\n\u0001<>&`+" "+`"` {
		t.Errorf("jsString = %s", b.String())
	}
}

func TestElectrumHTTPBitgoASM(t *testing.T) {
	for _, tc := range []struct{ hex, want string }{
		{"483045022100f37a58f721558f6da63d2d94ef741e451e2fa708f3dd53b82791627cdede168f022032f39e9e81676074f9e4b3f31fbd4637451f88ec407c10dc3039140499b925f60121025b649691fecb362080ca5581d7d893a98c2f58466bbff197a9b679ba5f195ba2",
			"3045022100f37a58f721558f6da63d2d94ef741e451e2fa708f3dd53b82791627cdede168f022032f39e9e81676074f9e4b3f31fbd4637451f88ec407c10dc3039140499b925f601 025b649691fecb362080ca5581d7d893a98c2f58466bbff197a9b679ba5f195ba2"},
		{"", ""},
		{"00", "OP_0"},
		{"0151", "51"},
		{"0101", "OP_1"},
		{"0181", "OP_1NEGATE"},
		{"4c00", "OP_0"},
		{"4c0110", "OP_16"},
		{"4c02abcd", "abcd"},
		{"76a914d0c320db3fbd0abe2b6fe31a3bca4fed8ce8669588ac", "OP_DUP OP_HASH160 d0c320db3fbd0abe2b6fe31a3bca4fed8ce86695 OP_EQUALVERIFY OP_CHECKSIG"},
		{"b1b2b3", "OP_CHECKLOCKTIMEVERIFY OP_CHECKSEQUENCEVERIFY OP_NOP4"},
		{"51ba52", "OP_1  OP_2"},
		{"0201", ""},
		{"4c", ""},
		{"4d0100", ""},
	} {
		if got := bitgoASM(mustHex(t, tc.hex)); got != tc.want {
			t.Errorf("bitgoASM(%s) = %q, want %q", tc.hex, got, tc.want)
		}
	}
}

func TestElectrumHTTPVinAddr(t *testing.T) {
	e := newEHEnv(t)
	p2pkh := mustHex(t, "76a914d0c320db3fbd0abe2b6fe31a3bca4fed8ce8669588ac")
	if got := e.s.ehVinAddr(p2pkh, "sig pub"); got != ehAddrNice {
		t.Fatalf("p2pkh: %q", got)
	}
	pub := "025b649691fecb362080ca5581d7d893a98c2f58466bbff197a9b679ba5f195ba2"
	p2pk := mustHex(t, "21"+pub+"ac")
	// The standard P2PK spend's first push is a signature: no address.
	if got := e.s.ehVinAddr(p2pk, "3045022100aa01"); got != "" {
		t.Fatalf("p2pk with signature: %q", got)
	}
	if got := e.s.ehVinAddr(p2pk, pub); got == "" || got[0] != '2' {
		t.Fatalf("p2pk with a key-shaped push: %q", got)
	}
	if got := e.s.ehVinAddr(mustHex(t, "0014751e76e8199196d454941c45d1b3a323f1433bd6"), ""); got != "" {
		t.Fatalf("witness: %q", got)
	}
}

func TestElectrumHTTPPyHelpers(t *testing.T) {
	if got := pyRepr("it's"); got != `"it's"` {
		t.Fatal(got)
	}
	if got := pyRepr(`a'b"c`); got != `'a\'b"c'` {
		t.Fatal(got)
	}
	if got := pyThousands("1234567"); got != "1,234,567" {
		t.Fatal(got)
	}
	if got := pyThousands("123"); got != "123" {
		t.Fatal(got)
	}
	_ = http.StatusOK
}

// Only the root path is served: the proxy's API is "/?..." in every form, and
// other paths must not reach the query parser (nor skip the rate limiter's
// path exemptions).
func TestElectrumHTTPRootPathOnly(t *testing.T) {
	t.Setenv("BTXPATH_ELECTRUM_HTTP_RATE_LIMIT", "60")
	t.Setenv("BTXPATH_ELECTRUM_HTTP_BURST", "1")
	e := newEHEnv(t)
	if err := e.s.EnableRateLimit("btxpath"); err != nil {
		t.Fatal(err)
	}
	calls := e.index.calls
	for _, target := range []string{
		"/static/x?server=s&call=entirehistory&param=" + ehAddrNice,
		"/favicon.ico?server=s&call=entirehistory&param=" + ehAddrNice,
		"/api/?server=s&call=entirehistory&param=" + ehAddrNice,
		"//?server=s&call=entirehistory&param=" + ehAddrNice,
	} {
		for i := 0; i < 3; i++ {
			r := httptest.NewRequest("GET", "http://proxy.example"+target, nil)
			r.RequestURI = target
			r.RemoteAddr = "203.0.113.9:4000"
			rec := httptest.NewRecorder()
			e.s.serve(rec, r)
			if rec.Code != 404 || rec.Body.String() != "Error: Not found" || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
				t.Fatalf("%s: %d %q", target, rec.Code, rec.Body.String())
			}
		}
		// ServeHTTP refuses it too when called directly.
		r := httptest.NewRequest("GET", "http://proxy.example"+target, nil)
		r.RequestURI = target
		rec := httptest.NewRecorder()
		e.s.ServeHTTP(rec, r)
		if rec.Code != 404 {
			t.Fatalf("%s via ServeHTTP: %d", target, rec.Code)
		}
	}
	if e.index.calls != calls {
		t.Fatal("a non-root request reached the index")
	}
	// The root path, in the backward-compatible form too, is served and limited.
	codes := []int{}
	for _, target := range []string{"/?server=127.0.0.1?call=height", "/?server=127.0.0.1?call=height"} {
		r := httptest.NewRequest("GET", "http://proxy.example"+target, nil)
		r.RequestURI = target
		r.RemoteAddr = "203.0.113.9:4000"
		rec := httptest.NewRecorder()
		e.s.serve(rec, r)
		codes = append(codes, rec.Code)
	}
	if fmt.Sprint(codes) != "[200 429]" {
		t.Fatalf("root codes %v", codes)
	}
}

func TestElectrumHTTPBudget(t *testing.T) {
	setup := func(t *testing.T) *ehEnv {
		e := newEHEnv(t)
		e.loadHistory(t, ehAddrNice, "entirehistory_2Y3e.json")
		for _, txid := range []string{ehTx0, ehTx1, ehTx2} {
			e.loadTx(t, txid)
		}
		e.addWorkerTx(t, ehTx1)
		e.addWorkerTx(t, ehTx2)
		return e
	}
	target := ehProxyPrefix + "niceentirehistory&param=" + ehAddrNice
	// The default budget answers in full.
	e := setup(t)
	ehExpect(t, e.get(target), string(ehFixture(t, "nicehistory_2Y3e.json")))

	// More transactions than the per-request cap are refused before any load.
	e = setup(t)
	e.s.niceMaxTxs = 1
	ehExpect(t, e.get(target), "Error: history too large: 2 transactions, at most 1 per request")
	if len(e.rpc.calls) != 0 {
		t.Fatalf("backend called: %v", e.rpc.calls)
	}

	// Two transactions need four backend lookups.
	e = setup(t)
	e.s.maxBackendCalls = 3
	ehExpect(t, e.get(target), "Error: request too large")
	e.s.maxBackendCalls = 4
	ehExpect(t, e.get(target), string(ehFixture(t, "nicehistory_2Y3e.json")))

	// Mempool lookups in the history count too.
	e = setup(t)
	desc := ehDescHex(t, e.parser, ehAddrNice)
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("m%d", i)
		e.mempool.outpoints[desc] = append(e.mempool.outpoints[desc], bchain.Outpoint{Txid: id})
		e.worker.txs[id] = &api.Tx{Txid: id}
	}
	e.s.maxBackendCalls = 4
	ehExpect(t, e.get(ehProxyPrefix+"entirehistory&param="+ehAddrNice), "Error: request too large")

	// The request deadline is checked inside the loops.
	e = setup(t)
	e.s.requestTimeout = time.Nanosecond
	ehExpect(t, e.get(target), "Error: request timed out")
}

func TestElectrumHTTPPrevTxFallback(t *testing.T) {
	e := newEHEnv(t)
	e.loadTx(t, ehTx0)
	e.loadTx(t, ehTx1)
	b := &ehBudget{ctx: t.Context(), calls: 10}
	cache := map[string]*bchain.Tx{}
	p2pkh := mustHex(t, "76a914d0c320db3fbd0abe2b6fe31a3bca4fed8ce8669588ac")
	twoPush := mustHex(t, "0201ab0201cd")
	vin := &api.Vin{Txid: ehTx1, Vout: 0, ValueSat: (*api.Amount)(big.NewInt(5)), AddrDesc: p2pkh}
	// A resolved P2PKH input with a two-push scriptSig needs no daemon call.
	if v, script, err := e.s.ehNiceInput(b, vin, twoPush, cache); err != nil || v != 5 || !bytes.Equal(script, p2pkh) || e.rawTxCalls() != 0 {
		t.Fatalf("resolved: %v %x %v calls=%d", v, script, err, e.rawTxCalls())
	}
	// A single-push scriptSig may be a P2PK spend the index folded into P2PKH:
	// the real previous output is read.
	if v, _, err := e.s.ehNiceInput(b, vin, mustHex(t, "0201ab"), cache); err != nil || v != 7300346792 || e.rawTxCalls() != 1 {
		t.Fatalf("single push: %v %v calls=%d", v, err, e.rawTxCalls())
	}
	// Cached for the request.
	if _, _, err := e.s.ehNiceInput(b, vin, mustHex(t, "0201ab"), cache); err != nil || e.rawTxCalls() != 1 {
		t.Fatalf("cache: %v calls=%d", err, e.rawTxCalls())
	}
	// The cache stops growing at its cap.
	full := map[string]*bchain.Tx{}
	for i := 0; i < ehPrevCacheMax; i++ {
		full[fmt.Sprint(i)] = &bchain.Tx{}
	}
	if _, err := e.s.ehPrevTx(b, ehTx0, full); err != nil || len(full) != ehPrevCacheMax {
		t.Fatalf("cache cap: %v %d", err, len(full))
	}
	// An exhausted budget refuses the load.
	if _, err := e.s.ehPrevTx(&ehBudget{ctx: t.Context()}, ehTx0, map[string]*bchain.Tx{}); err == nil || err.Error() != "request too large" {
		t.Fatalf("exhausted budget: %v", err)
	}
}

func TestElectrumHTTPHistoryLimitFor(t *testing.T) {
	for _, tc := range []struct{ maxSend, want int }{
		{0, 10101},            // ElectrumX's DEFAULT_MAX_SEND 1,000,000 // 99
		{1000000, 10101},      // explicit default
		{20000000, 202020},    // Raptoreum production MAX_SEND
		{1000, 350000 / 99},   // raised to SessionManager's 350,000 floor
		{350000, 350000 / 99}, // at the floor
		{350099, 350099 / 99}, // above the floor
		{-5, ehHistoryLimit},  // never reached (SetMaxSend refuses it)
	} {
		if got := ehHistoryLimitFor(tc.maxSend); got != tc.want {
			t.Errorf("ehHistoryLimitFor(%d) = %d, want %d", tc.maxSend, got, tc.want)
		}
	}
	if ehHistoryLimit != 10101 {
		t.Fatalf("default limit %d", ehHistoryLimit)
	}
	e := newEHEnv(t)
	if err := e.s.SetMaxSend(-1); err == nil {
		t.Fatal("negative max send accepted")
	}
}

// ElectrumX refuses an address with limit or more confirmed transactions
// (len(limited_history(limit)) >= limit); mempool entries do not count.
func TestElectrumHTTPHistoryLimit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		maxSend int
		limit   int
	}{
		{"default", 0, 10101},
		{"raptoreum", 20000000, 202020},
		{"floor", 1000, 3535},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEHEnv(t)
			if err := e.s.SetMaxSend(tc.maxSend); err != nil {
				t.Fatal(err)
			}
			addr := ehAddrNice
			desc := ehDescHex(t, e.parser, addr)
			h := make([]ehStubHistory, tc.limit+1)
			for i := range h {
				h[i] = ehStubHistory{fmt.Sprintf("t%07d", i), uint32(i)}
			}
			for _, n := range []struct {
				count    int
				tooLarge bool
			}{{tc.limit - 1, false}, {tc.limit, true}, {tc.limit + 1, true}} {
				e.index.history[desc] = h[:n.count]
				for _, call := range []string{"history", "entirehistory", "nicehistory"} {
					w := e.get(ehProxyPrefix + call + "&param=" + addr)
					if n.tooLarge {
						ehExpect(t, w, "Error: history too large")
					} else if call != "nicehistory" && !strings.HasPrefix(w.Body.String(), "[") {
						t.Fatalf("%s with %d txs (limit %d): %.100s", call, n.count, tc.limit, w.Body.String())
					}
				}
			}
			// limit-1 confirmed plus mempool entries still answers.
			e.index.history[desc] = h[:tc.limit-1]
			e.mempool.outpoints[desc] = []bchain.Outpoint{{Txid: "m1", Vout: 0}, {Txid: "m2", Vout: 0}}
			e.worker.txs["m1"] = &api.Tx{Txid: "m1", FeesSat: (*api.Amount)(big.NewInt(1))}
			e.worker.txs["m2"] = &api.Tx{Txid: "m2", FeesSat: (*api.Amount)(big.NewInt(1))}
			var got []map[string]interface{}
			if err := json.Unmarshal(e.get(ehProxyPrefix+"entirehistory&param="+addr).Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if len(got) != tc.limit+1 {
				t.Fatalf("limit-1 confirmed + 2 mempool: %d entries", len(got))
			}
		})
	}
}

// Same-block transactions come out in block order, as ElectrumX lists them
// (tx_num order), although RocksDB stores them reversed and in two passes.
func TestElectrumHTTPBlockOrder(t *testing.T) {
	ids := func(t *testing.T, w *httptest.ResponseRecorder) []string {
		t.Helper()
		var got []map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("%v: %s", err, w.Body.String())
		}
		out := make([]string, len(got))
		for i, g := range got {
			out[i] = g["tx_hash"].(string)
		}
		return out
	}
	getblocks := func(e *ehEnv) int {
		n := 0
		for _, c := range e.rpc.calls {
			if strings.HasPrefix(c, "getblock[") {
				n++
			}
		}
		return n
	}
	for _, tc := range []struct {
		name      string
		block     []string // block order
		inputOnly []string
		getblock  int
	}{
		{"outputs only", []string{"a1", "a2", "a3"}, nil, 0},
		{"inputs only", []string{"a1", "a2", "a3"}, []string{"a1", "a2", "a3"}, 0},
		{"one tx", []string{"a1"}, nil, 0},
		{"mixed", []string{"a1", "a2", "a3", "a4"}, []string{"a1", "a3"}, 1},
		{"mixed, input last", []string{"a1", "a2", "a3"}, []string{"a3"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEHEnv(t)
			addr := ehAddrNice
			desc := ehDescHex(t, e.parser, addr)
			h := []ehStubHistory{{"p0", 99}}
			for _, txid := range tc.block {
				h = append(h, ehStubHistory{txid, 100})
			}
			h = append(h, ehStubHistory{"n0", 101}, ehStubHistory{"n1", 101})
			e.index.history[desc] = h
			for _, txid := range tc.inputOnly {
				e.index.inputOnly[txid] = true
			}
			e.index.hashes[100] = "h100"
			e.rpc.blocks["h100"] = append([]string{"coinbase", "other"}, tc.block...)
			want := append(append([]string{"p0"}, tc.block...), "n0", "n1")
			for _, call := range []string{"entirehistory", "history"} {
				if got := ids(t, e.get(ehProxyPrefix+call+"&param="+addr)); !slices.Equal(got, want) {
					t.Fatalf("%s: %v, want %v", call, got, want)
				}
			}
			if n := getblocks(e); n != 2*tc.getblock {
				t.Fatalf("%d getblock calls, want %d", n, 2*tc.getblock)
			}
		})
	}

	t.Run("outside the history window", func(t *testing.T) {
		e := newEHEnv(t)
		addr := ehAddrNice
		desc := ehDescHex(t, e.parser, addr)
		h := []ehStubHistory{{"m-in", 100}, {"m-out", 100}}
		for i := 0; i < 248; i++ {
			h = append(h, ehStubHistory{fmt.Sprintf("t%03d", i), uint32(200 + i)})
		}
		e.index.history[desc] = h
		e.index.inputOnly["m-in"] = true
		e.index.hashes[100] = "h100"
		e.rpc.blocks["h100"] = []string{"cb", "m-in", "m-out"}
		got := ids(t, e.get(ehProxyPrefix+"history&param="+addr))
		if len(got) != 100 || got[0] != "t148" || got[99] != "t247" || getblocks(e) != 0 {
			t.Fatalf("history: %d entries %v..., %d getblock", len(got), got[:1], getblocks(e))
		}
		got = ids(t, e.get(ehProxyPrefix+"entirehistory&param="+addr))
		if got[0] != "m-in" || got[1] != "m-out" || getblocks(e) != 1 {
			t.Fatalf("entirehistory: %v..., %d getblock", got[:2], getblocks(e))
		}
	})

	t.Run("block changed", func(t *testing.T) {
		e := newEHEnv(t)
		addr := ehAddrNice
		desc := ehDescHex(t, e.parser, addr)
		e.index.history[desc] = []ehStubHistory{{"x-in", 100}, {"x-out", 100}}
		e.index.inputOnly["x-in"] = true
		e.index.hashes[100] = "h100"
		e.rpc.blocks["h100"] = []string{"cb", "x-out"}
		ehExpect(t, e.get(ehProxyPrefix+"entirehistory&param="+addr), "Error: internal error")
	})
}
