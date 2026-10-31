package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/trezor/blockbook/api"
	"github.com/trezor/blockbook/bchain"
)

// insightMultiAddr is one address of the multi-address fake. txs is its
// merged newest-first history (pending first); a pending tx has Blockheight
// -1 and Confirmations 0.
type insightMultiAddr struct {
	utxos            api.Utxos
	txs              []*api.Tx
	balance, pending int64
}

// insightMultiFake answers per address. An address not in addrs is a valid
// empty address, except "bad", which the worker rejects publicly.
type insightMultiFake struct {
	*insightFake
	addrs map[string]*insightMultiAddr
	// utxoCalls and confirmed record GetAddressUtxo; lightSizes the page
	// size of each TxHistoryLight call; txidCalls counts TxidHistory calls.
	utxoCalls  []string
	confirmed  []bool
	lightSizes []int
	txidCalls  []string
}

func (f *insightMultiFake) lookup(a string) (*insightMultiAddr, error) {
	if a == "bad" {
		return nil, api.NewAPIError("Invalid address", true)
	}
	if m := f.addrs[a]; m != nil {
		return m, nil
	}
	return &insightMultiAddr{}, nil
}

func (f *insightMultiFake) GetAddress(a string, page, size int, d api.AccountDetails, filter *api.AddressFilter, _ string) (*api.Address, error) {
	if page != 1 || size < 1 || filter == nil || filter.Vout != api.AddressFilterVoutOff {
		panic("wrong worker query")
	}
	m, err := f.lookup(a)
	if err != nil {
		return nil, err
	}
	out := &api.Address{AddrStr: a, BalanceSat: insightAmount(m.balance), UnconfirmedBalanceSat: insightAmount(m.pending)}
	for _, tx := range m.txs {
		if tx.Confirmations == 0 {
			out.UnconfirmedTxs++
		} else {
			out.Txs++
		}
	}
	head := m.txs[:min(size, len(m.txs))]
	switch d {
	case api.AccountDetailsTxHistoryLight:
		f.lightSizes = append(f.lightSizes, size)
		out.Transactions = head
	case api.AccountDetailsTxidHistory:
		f.txidCalls = append(f.txidCalls, a)
		for _, tx := range head {
			out.Txids = append(out.Txids, tx.Txid)
		}
	}
	return out, nil
}

func (f *insightMultiFake) GetAddressUtxo(a string, onlyConfirmed bool) (api.Utxos, error) {
	f.utxoCalls = append(f.utxoCalls, a)
	f.confirmed = append(f.confirmed, onlyConfirmed)
	m, err := f.lookup(a)
	if err != nil {
		return nil, err
	}
	var out api.Utxos
	for _, u := range m.utxos {
		if !onlyConfirmed || u.Confirmations > 0 {
			out = append(out, u)
		}
	}
	return out, nil
}

// insightMultiTx returns a light tx at height h (pending when h < 0) that
// pays address.
func insightMultiTx(id string, h int, address string) *api.Tx {
	tx := &api.Tx{Txid: id, Blockheight: h, Vout: []api.Vout{{ValueSat: insightAmount(1), Addresses: []string{address}}}}
	if h > 0 {
		tx.Confirmations = uint32(20 - h)
	}
	return tx
}

func insightMultiServer(t *testing.T, addrs map[string]*insightMultiAddr) (*insightMultiFake, *InsightServer) {
	t.Helper()
	f := &insightMultiFake{insightFake: insightFixture(), addrs: addrs}
	f.txs = map[string]*api.Tx{}
	for _, m := range addrs {
		for _, tx := range m.txs {
			f.txs[tx.Txid] = tx
		}
	}
	s, err := NewInsightServer("127.0.0.1:0", bchain.ChainBitcoinType, 8, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.SetReady()
	return f, s
}

func insightMultiPost(s *InsightServer, path, contentType, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

const insightForm = "application/x-www-form-urlencoded"

func insightMultiUtxoAddrs() map[string]*insightMultiAddr {
	return map[string]*insightMultiAddr{
		"A": {utxos: api.Utxos{{Txid: "a1", AmountSat: insightAmount(1), Height: 5, Confirmations: 3}, {Txid: "a2", Vout: 1, AmountSat: insightAmount(2)}}},
		"B": {utxos: api.Utxos{{Txid: "b1", AmountSat: insightAmount(3), Height: 6, Confirmations: 2, Address: "B"}}},
	}
}

func insightUtxoKeys(t *testing.T, body []byte) []string {
	t.Helper()
	var rows []insightUtxo
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatal(err, string(body))
	}
	out := []string{}
	for _, r := range rows {
		out = append(out, r.Address+":"+r.Txid)
	}
	return out
}

func TestInsightMultiUtxo(t *testing.T) {
	f, s := insightMultiServer(t, insightMultiUtxoAddrs())
	w := insightRequest(s, "GET", "/api/addrs/A,B/utxo", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if got := insightUtxoKeys(t, w.Body.Bytes()); !reflect.DeepEqual(got, []string{"A:a1", "A:a2", "B:b1"}) {
		t.Fatal(got)
	}
	if !strings.Contains(w.Body.String(), `"satoshis":2`) || !strings.Contains(w.Body.String(), `"amount":0.00000003`) {
		t.Fatal(w.Body)
	}
	if !reflect.DeepEqual(f.confirmed, []bool{false, false}) {
		t.Fatal(f.confirmed)
	}

	f, s = insightMultiServer(t, insightMultiUtxoAddrs())
	w = insightRequest(s, "GET", "/insight-api/addrs/A,B/unspent", "")
	if got := insightUtxoKeys(t, w.Body.Bytes()); w.Code != 200 || !reflect.DeepEqual(got, []string{"A:a1", "B:b1"}) {
		t.Fatal(w.Code, got)
	}
	if !reflect.DeepEqual(f.confirmed, []bool{true, true}) {
		t.Fatal(f.confirmed)
	}

	for _, tc := range []struct{ contentType, body string }{
		{"application/json", `{"addrs":"A,B"}`},
		{"", `{"addrs":"A,B"}`},
		{insightForm, "addrs=A%2CB"},
		{insightForm, "addrs=A,B&addrs=C"},
	} {
		_, s = insightMultiServer(t, insightMultiUtxoAddrs())
		w = insightMultiPost(s, "/api/addrs/utxo", tc.contentType, tc.body)
		if got := insightUtxoKeys(t, w.Body.Bytes()); w.Code != 200 || !reflect.DeepEqual(got, []string{"A:a1", "A:a2", "B:b1"}) {
			t.Fatal(tc, w.Code, got)
		}
	}
}

func TestInsightMultiBalance(t *testing.T) {
	_, s := insightMultiServer(t, map[string]*insightMultiAddr{"A": {balance: 1, pending: -1}, "B": {balance: 2}})
	w := insightRequest(s, "GET", "/api/addrs/A,B/balance", "")
	if w.Code != 200 || w.Body.String() != "{\"balance\":3,\"unconfirmedBalance\":-1}\n" {
		t.Fatal(w.Code, w.Body)
	}
	_, s = insightMultiServer(t, map[string]*insightMultiAddr{"A": {balance: 9007199254740991}, "B": {balance: 1}})
	if w := insightRequest(s, "GET", "/api/addrs/A,B/balance", ""); w.Code != 200 || w.Body.String() != "{\"balance\":9007199254740992,\"unconfirmedBalance\":0}\n" {
		t.Fatal(w.Code, w.Body)
	}
}

// insightMultiMergeAddrs: A has [pA, h10a, h5shared], B has [h7b, h5shared].
func insightMultiMergeAddrs() map[string]*insightMultiAddr {
	shared := insightMultiTx("h5shared", 5, "A")
	return map[string]*insightMultiAddr{
		"A": {txs: []*api.Tx{insightMultiTx("pA", -1, "A"), insightMultiTx("h10a", 10, "A"), shared}},
		"B": {txs: []*api.Tx{insightMultiTx("h7b", 7, "B"), shared}},
	}
}

type insightMultiPage struct {
	TotalItems int `json:"totalItems"`
	From       int `json:"from"`
	To         int `json:"to"`
	Items      []struct {
		Txid string `json:"txid"`
		Vout []struct {
			IsOwn bool `json:"isOwn"`
		} `json:"vout"`
	} `json:"items"`
}

func insightMultiDecode(t *testing.T, w *httptest.ResponseRecorder) (insightMultiPage, []string) {
	t.Helper()
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var p insightMultiPage
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, it := range p.Items {
		ids = append(ids, it.Txid)
	}
	return p, ids
}

func TestInsightMultiTxsMerge(t *testing.T) {
	f, s := insightMultiServer(t, insightMultiMergeAddrs())
	p, ids := insightMultiDecode(t, insightRequest(s, "GET", "/api/addrs/A,B/txs", ""))
	if !reflect.DeepEqual(ids, []string{"pA", "h10a", "h7b", "h5shared"}) || p.TotalItems != 4 || p.From != 0 || p.To != 4 {
		t.Fatal(p, ids)
	}
	// Default window 0..10: each address is asked for its first 10 light txs.
	if !reflect.DeepEqual(f.lightSizes, []int{10, 10}) || len(f.txidCalls) != 0 || !reflect.DeepEqual(f.fetched, ids) {
		t.Fatal(f.lightSizes, f.txidCalls, f.fetched)
	}
	// isOwn marks outputs paying any requested address.
	for _, it := range p.Items {
		if len(it.Vout) != 1 || !it.Vout[0].IsOwn {
			t.Fatal("isOwn", p)
		}
	}

	f, s = insightMultiServer(t, insightMultiMergeAddrs())
	p, ids = insightMultiDecode(t, insightRequest(s, "GET", "/api/addrs/A,B/txs?from=1&to=3", ""))
	if !reflect.DeepEqual(ids, []string{"h10a", "h7b"}) || p.TotalItems != 4 || p.From != 1 || p.To != 3 || !reflect.DeepEqual(f.lightSizes, []int{3, 3}) {
		t.Fatal(p, ids, f.lightSizes)
	}
	_, s = insightMultiServer(t, insightMultiMergeAddrs())
	p, ids = insightMultiDecode(t, insightRequest(s, "GET", "/api/addrs/A,B/txs/1/3", ""))
	if !reflect.DeepEqual(ids, []string{"h10a", "h7b"}) {
		t.Fatal(p, ids)
	}

	for _, tc := range []struct{ path, contentType, body string }{
		{"/api/addrs/txs", insightForm, "addrs=A,B&from=0&to=2"},
		{"/api/addrs/txs", "application/json", `{"addrs":"A,B","from":0,"to":2}`},
		{"/api/addrs/txs", "application/json", `{"addrs":"A,B","from":"0","to":"2"}`},
		{"/api/addrs/txs?from=0&to=2", "application/json", `{"addrs":"A,B","from":5,"to":9}`},
	} {
		_, s = insightMultiServer(t, insightMultiMergeAddrs())
		p, ids = insightMultiDecode(t, insightMultiPost(s, tc.path, tc.contentType, tc.body))
		if !reflect.DeepEqual(ids, []string{"pA", "h10a"}) || p.TotalItems != 4 || p.To != 2 {
			t.Fatal(tc, p, ids)
		}
	}

	// Stock defaults: a lone from gives from..from+10; past the end is empty.
	_, s = insightMultiServer(t, insightMultiMergeAddrs())
	p, ids = insightMultiDecode(t, insightRequest(s, "GET", "/api/addrs/A,B/txs?from=3", ""))
	if !reflect.DeepEqual(ids, []string{"h5shared"}) || p.From != 3 || p.To != 4 {
		t.Fatal(p, ids)
	}
	_, s = insightMultiServer(t, insightMultiMergeAddrs())
	p, ids = insightMultiDecode(t, insightRequest(s, "GET", "/api/addrs/A,B/txs?from=8&to=9", ""))
	if len(ids) != 0 || p.TotalItems != 4 || p.From != 8 || p.To != 8 {
		t.Fatal(p, ids)
	}
}

func TestInsightMultiTxsTotalExact(t *testing.T) {
	// A: x9, x8, s5, x4; B: s5, y3. Union 5.
	shared := insightMultiTx("s5", 5, "A")
	addrs := func() map[string]*insightMultiAddr {
		return map[string]*insightMultiAddr{
			"A": {txs: []*api.Tx{insightMultiTx("x9", 9, "A"), insightMultiTx("x8", 8, "A"), shared, insightMultiTx("x4", 4, "A")}},
			"B": {txs: []*api.Tx{shared, insightMultiTx("y3", 3, "B")}},
			"C": {txs: []*api.Tx{insightMultiTx("z1", 1, "C")}},
		}
	}
	f, s := insightMultiServer(t, addrs())
	p, ids := insightMultiDecode(t, insightRequest(s, "GET", "/api/addrs/A,B/txs?from=0&to=1", ""))
	if !reflect.DeepEqual(ids, []string{"x9"}) || p.TotalItems != 5 || !reflect.DeepEqual(f.txidCalls, []string{"A", "B"}) {
		t.Fatal(p, ids, f.txidCalls)
	}
	// Only A's history is longer than the window: B and C come complete from
	// the light query, A's full txid list is fetched.
	f, s = insightMultiServer(t, addrs())
	p, ids = insightMultiDecode(t, insightRequest(s, "GET", "/api/addrs/A,B,C/txs?from=1&to=3", ""))
	if !reflect.DeepEqual(ids, []string{"x8", "s5"}) || p.TotalItems != 6 || !reflect.DeepEqual(f.txidCalls, []string{"A"}) {
		t.Fatal(p, ids, f.txidCalls)
	}
	// Every history fits the window: the merged count is exact, no extra query.
	f, s = insightMultiServer(t, addrs())
	p, _ = insightMultiDecode(t, insightRequest(s, "GET", "/api/addrs/A,B/txs?from=0&to=4", ""))
	if p.TotalItems != 5 || len(f.txidCalls) != 0 {
		t.Fatal(p, f.txidCalls)
	}
	// One distinct address: its own count, no full list.
	f, s = insightMultiServer(t, addrs())
	p, ids = insightMultiDecode(t, insightRequest(s, "GET", "/api/addrs/A,A/txs?from=0&to=1", ""))
	if !reflect.DeepEqual(ids, []string{"x9"}) || p.TotalItems != 4 || len(f.txidCalls) != 0 {
		t.Fatal(p, ids, f.txidCalls)
	}
}

func TestInsightMultiAddressInputs(t *testing.T) {
	f, s := insightMultiServer(t, insightMultiUtxoAddrs())
	w := insightRequest(s, "GET", "/api/addrs/A,A,B/utxo", "")
	if w.Code != 200 || !reflect.DeepEqual(f.utxoCalls, []string{"A", "B"}) {
		t.Fatal(w.Code, f.utxoCalls)
	}
	f, s = insightMultiServer(t, insightMultiUtxoAddrs())
	w = insightRequest(s, "GET", "/api/addrs/%20B,,A,%20A%20,B/utxo", "")
	if w.Code != 200 || !reflect.DeepEqual(f.utxoCalls, []string{"B", "A"}) {
		t.Fatal(w.Code, f.utxoCalls)
	}
	list := func(n int) string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("x%d", i)
		}
		return strings.Join(out, ",")
	}
	for _, tc := range []struct {
		method, path, contentType, body string
		code                            int
	}{
		{"GET", "/api/addrs/A,bad/utxo", "", "", 400},
		{"GET", "/api/addrs/A,bad/unspent", "", "", 400},
		{"GET", "/api/addrs/A,bad/balance", "", "", 400},
		{"GET", "/api/addrs/A,bad/txs", "", "", 400},
		{"POST", "/api/addrs/utxo", insightForm, "addrs=A,bad", 400},
		{"GET", "/api/addrs/" + list(100) + "/utxo", "", "", 200},
		{"GET", "/api/addrs/" + list(101) + "/utxo", "", "", 400},
		{"GET", "/api/addrs/" + list(100) + ",x0,x1/utxo", "", "", 200},
		{"GET", "/api/addrs/" + list(101) + "/unspent", "", "", 400},
		{"GET", "/api/addrs/" + list(101) + "/balance", "", "", 400},
		{"POST", "/api/addrs/utxo", insightForm, "addrs=" + list(101), 400},
		{"GET", "/api/addrs/" + list(21) + "/txs", "", "", 400},
		{"GET", "/api/addrs/" + list(20) + "/txs", "", "", 200},
		{"POST", "/api/addrs/txs", "application/json", `{"addrs":"` + list(21) + `"}`, 400},
		{"GET", "/api/addrs/,/utxo", "", "", 400},
		{"GET", "/api/addrs/%20,%20/balance", "", "", 400},
		{"POST", "/api/addrs/utxo", "application/json", `{"addrs":""}`, 400},
		{"POST", "/api/addrs/utxo", "application/json", `{"addrs":" , "}`, 400},
		{"POST", "/api/addrs/utxo", "application/json", `{}`, 400},
		{"POST", "/api/addrs/utxo", "application/json", ``, 400},
		{"POST", "/api/addrs/utxo", "application/json", `null`, 400},
		{"POST", "/api/addrs/utxo", "application/json", `{"addrs":["A"]}`, 400},
		{"POST", "/api/addrs/utxo", "application/json", `{"addrs":"A"`, 400},
		{"POST", "/api/addrs/utxo", insightForm, "addrs=", 400},
		{"POST", "/api/addrs/utxo", insightForm, "", 400},
		{"POST", "/api/addrs/txs", insightForm, "addrs=", 400},
		{"GET", "/api/addrs/A,B/txs?to=1001", "", "", 400},
		{"GET", "/api/addrs/A,B/txs?from=500&to=1001", "", "", 400},
		{"GET", "/api/addrs/A,B/txs?from=0&to=1000", "", "", 200},
		{"GET", "/api/addrs/A,B/txs?from=995", "", "", 400},
		{"GET", "/api/addrs/A,B/txs?from=2&to=1", "", "", 400},
		{"GET", "/api/addrs/A,B/txs?from=x&to=2", "", "", 400},
		{"POST", "/api/addrs/txs", "application/json", `{"addrs":"A,B","from":-1,"to":2}`, 400},
		{"POST", "/api/addrs/txs", "application/json", `{"addrs":"A,B","from":0.5,"to":2}`, 400},
		{"POST", "/api/addrs/txs", "application/json", `{"addrs":"A,B","to":true}`, 400},
		{"POST", "/api/addrs/utxo", "application/json", `{"addrs":"A","from":true}`, 200},
		{"POST", "/api/addrs/A,B/utxo", "application/json", `{"addrs":"A"}`, 405},
	} {
		t.Run(tc.method+tc.path[:min(len(tc.path), 40)]+tc.body[:min(len(tc.body), 30)], func(t *testing.T) {
			_, s := insightMultiServer(t, insightMultiUtxoAddrs())
			var w *httptest.ResponseRecorder
			if tc.method == "POST" {
				w = insightMultiPost(s, tc.path, tc.contentType, tc.body)
			} else {
				w = insightRequest(s, tc.method, tc.path, "")
			}
			if w.Code != tc.code {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			if tc.code == 400 && w.Body.String() != "{\"status\":\"error\",\"error\":\"Invalid request\"}\n" {
				t.Fatal(w.Body)
			}
		})
	}
	// A rejected address stops the request before any later address.
	f, s = insightMultiServer(t, insightMultiUtxoAddrs())
	if w := insightRequest(s, "GET", "/api/addrs/bad,A/utxo", ""); w.Code != 400 || !reflect.DeepEqual(f.utxoCalls, []string{"bad"}) {
		t.Fatal(w.Code, f.utxoCalls)
	}
}

// insightMultiHistory returns n confirmed light txs of address, newest first.
func insightMultiHistory(prefix string, n int, address string) []*api.Tx {
	out := make([]*api.Tx, n)
	for i := range out {
		out[i] = &api.Tx{Txid: fmt.Sprintf("%s%d", prefix, i), Blockheight: n - i, Confirmations: 1}
	}
	return out
}

func TestInsightMultiTxsTotalBudget(t *testing.T) {
	// Long histories within the budget: exact union (B repeats A's newest).
	a := insightMultiHistory("a", 25000, "A")
	b := append([]*api.Tx{a[0]}, insightMultiHistory("b", 24999, "B")...)
	f, s := insightMultiServer(t, map[string]*insightMultiAddr{"A": {txs: a}, "B": {txs: b}})
	p, ids := insightMultiDecode(t, insightRequest(s, "GET", "/api/addrs/A,B/txs?from=0&to=2", ""))
	if p.TotalItems != 49999 || !reflect.DeepEqual(f.txidCalls, []string{"A", "B"}) || len(ids) != 2 {
		t.Fatal(p.TotalItems, f.txidCalls, ids)
	}
	// Over the budget: no full list is read; totalItems is the sum of the
	// per-address totals (an upper bound), short addresses included.
	b = append([]*api.Tx{a[0]}, insightMultiHistory("b", 25000, "B")...)
	c := []*api.Tx{insightMultiTx("c1", 1, "C")}
	f, s = insightMultiServer(t, map[string]*insightMultiAddr{"A": {txs: a}, "B": {txs: b}, "C": {txs: c}})
	p, ids = insightMultiDecode(t, insightRequest(s, "GET", "/api/addrs/A,B,C/txs?from=0&to=2", ""))
	if p.TotalItems != 50002 || len(f.txidCalls) != 0 || !reflect.DeepEqual(ids, []string{"a0", "b0"}) {
		t.Fatal(p.TotalItems, f.txidCalls, ids)
	}
}

func TestInsightMultiUtxoCap(t *testing.T) {
	rows := func(prefix string, n int) api.Utxos {
		out := make(api.Utxos, n)
		for i := range out {
			out[i] = api.Utxo{Txid: fmt.Sprintf("%s%d", prefix, i), AmountSat: insightAmount(1), Confirmations: 1}
		}
		return out
	}
	addrs := map[string]*insightMultiAddr{"A": {utxos: rows("a", 15000)}, "B": {utxos: rows("b", 5000)}, "C": {utxos: rows("c", 1)}}
	_, s := insightMultiServer(t, addrs)
	if w := insightRequest(s, "GET", "/api/addrs/A,B/utxo", ""); w.Code != 200 || len(insightUtxoKeys(t, w.Body.Bytes())) != 20000 {
		t.Fatal(w.Code)
	}
	for _, path := range []string{"/api/addrs/A,B,C/utxo", "/api/addrs/A,B,C/unspent"} {
		f, s := insightMultiServer(t, addrs)
		w := insightRequest(s, "GET", path, "")
		if w.Code != 413 || w.Body.String() != "{\"status\":\"error\",\"error\":\"Too many unspent outputs, query fewer addresses\"}\n" || len(f.utxoCalls) != 3 {
			t.Fatal(path, w.Code, w.Body.Len())
		}
	}
	_, s = insightMultiServer(t, addrs)
	if w := insightMultiPost(s, "/api/addrs/utxo", insightForm, "addrs=A,B,C"); w.Code != 413 {
		t.Fatal(w.Code)
	}
	// One address over the cap is refused on its own, before the next is read.
	huge := map[string]*insightMultiAddr{"A": {utxos: rows("a", 20001)}, "B": {utxos: rows("b", 1)}}
	if f, s := insightMultiServer(t, huge); true {
		if w := insightRequest(s, "GET", "/api/addrs/A,B/utxo", ""); w.Code != 413 || len(f.utxoCalls) != 1 {
			t.Fatal(w.Code, f.utxoCalls)
		}
	}
	// The single-address route keeps the full set.
	f := insightFixture()
	f.utxos = rows("x", 20001)
	if w := insightRequest(insightTestServer(t, f), "GET", "/api/addr/A/utxo", ""); w.Code != 200 || len(insightUtxoKeys(t, w.Body.Bytes())) != 20001 {
		t.Fatal(w.Code)
	}
}

func TestInsightMultiClientGone(t *testing.T) {
	for _, path := range []string{"/api/addrs/A,B/utxo", "/api/addrs/A,B/unspent", "/api/addrs/A,B/balance", "/api/addrs/A,B/txs"} {
		f, s := insightMultiServer(t, insightMultiMergeAddrs())
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		r := httptest.NewRequest("GET", path, nil).WithContext(ctx)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code == 200 || len(f.utxoCalls) != 0 || len(f.lightSizes) != 0 || len(f.pages) != 0 {
			t.Fatal(path, w.Code, f.utxoCalls, f.lightSizes)
		}
	}
}

// POST precedence: the addresses come only from the body (a query addrs is
// ignored), and a path or query window beats the body's from/to.
func TestInsightMultiPostPrecedence(t *testing.T) {
	_, s := insightMultiServer(t, insightMultiUtxoAddrs())
	w := insightMultiPost(s, "/api/addrs/utxo?addrs=B", insightForm, "addrs=A")
	if keys := insightUtxoKeys(t, w.Body.Bytes()); w.Code != 200 || !reflect.DeepEqual(keys, insightUtxoKeys(t, insightMultiPost(s, "/api/addrs/utxo", insightForm, "addrs=A").Body.Bytes())) || len(keys) != 2 {
		t.Fatal(w.Code, w.Body)
	}
	_, s = insightMultiServer(t, insightMultiMergeAddrs())
	// merged order: pA, h10a, h7b, h5shared
	_, ids := insightMultiDecode(t, insightMultiPost(s, "/api/addrs/txs?addrs=B&from=1&to=2", "application/json", `{"addrs":"A,B","from":0,"to":1}`))
	if !reflect.DeepEqual(ids, []string{"h10a"}) {
		t.Fatalf("query window must beat the body: %v", ids)
	}
	_, ids = insightMultiDecode(t, insightMultiPost(s, "/api/addrs/txs", "application/json", `{"addrs":"A,B","from":2,"to":3}`))
	if !reflect.DeepEqual(ids, []string{"h7b"}) {
		t.Fatalf("body window: %v", ids)
	}
}
