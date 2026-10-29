package server

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/trezor/blockbook/api"
)

// insightDeepFixture is an address with 2 pending and 2500 confirmed txs:
// the merged newest-first sequence is p0,p1,c0..c2499.
func insightDeepFixture() *insightFake {
	f := insightFixture()
	ids := []string{"p0", "p1"}
	for i := 0; i < 2500; i++ {
		ids = append(ids, fmt.Sprintf("c%d", i))
	}
	f.address.Txids = ids
	f.address.Txs, f.address.UnconfirmedTxs = 2500, 2
	f.txs = make(map[string]*api.Tx, len(ids))
	for _, id := range ids {
		f.txs[id] = &api.Tx{Txid: id}
	}
	return f
}

func insightSeq(prefix string, from, to int) []string {
	out := []string{}
	for i := from; i < to; i++ {
		out = append(out, fmt.Sprintf("%s%d", prefix, i))
	}
	return out
}

type insightHistoryOut struct {
	TotalItems int `json:"totalItems"`
	From       int `json:"from"`
	To         int `json:"to"`
	Items      []struct {
		Txid string `json:"txid"`
	} `json:"items"`
}

func insightHistoryIDs(out insightHistoryOut) []string {
	ids := []string{}
	for _, item := range out.Items {
		ids = append(ids, item.Txid)
	}
	return ids
}

func TestInsightHistoryDeepPaging(t *testing.T) {
	for _, tc := range []struct {
		query    string
		code     int
		pages    []int
		from, to int
		ids      []string
	}{
		{"?from=1500&to=1510", 200, []int{2}, 1500, 1510, insightSeq("c", 1498, 1508)},
		{"?from=990&to=1010", 200, []int{1, 2}, 990, 1010, insightSeq("c", 988, 1008)},
		{"?from=2500&to=2600", 200, []int{3}, 2500, 2502, []string{"c2498", "c2499"}},
		{"?from=3000&to=3001", 200, []int{4}, 3000, 3000, []string{}},
		// to-from <= 1000 (spec/plan rule; the brief's 0..1001 example conflicts with it).
		{"?from=0&to=1000", 200, []int{1}, 0, 1000, append([]string{"p0", "p1"}, insightSeq("c", 0, 998)...)},
		{"?from=5&to=1005", 200, []int{1, 2}, 5, 1005, insightSeq("c", 3, 1003)},
		{"?from=0&to=1001", 400, nil, 0, 0, nil},
		{"?from=5&to=1006", 400, nil, 0, 0, nil},
		{"?from=999999999999999&to=1000000000000000", 200, []int{insightMaxHistoryPage}, 999999999999999, 999999999999999, []string{}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			f := insightDeepFixture()
			w := insightRequest(insightTestServer(t, f), "GET", "/api/addrs/A/txs"+tc.query, "")
			if w.Code != tc.code {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			if tc.code != 200 {
				if len(f.pages) != 0 {
					t.Fatalf("rejected window queried the worker: %v", f.pages)
				}
				return
			}
			var out insightHistoryOut
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			ids := insightHistoryIDs(out)
			if out.TotalItems != 2502 || out.From != tc.from || out.To != tc.to || !reflect.DeepEqual(ids, tc.ids) {
				t.Fatalf("total=%d from=%d to=%d ids=%v", out.TotalItems, out.From, out.To, ids)
			}
			if !reflect.DeepEqual(f.pages, tc.pages) || f.prefix != 1000 || f.detail != api.AccountDetailsTxidHistory {
				t.Fatalf("worker pages %v size %d detail %d", f.pages, f.prefix, f.detail)
			}
			if !reflect.DeepEqual(f.fetched, append([]string(nil), tc.ids...)) && !(len(f.fetched) == 0 && len(tc.ids) == 0) {
				t.Fatalf("enriched %v", f.fetched)
			}
		})
	}
}

// Consecutive 50-row windows, as tx-export walks them, cover the list once.
func TestInsightHistoryWindowsTile(t *testing.T) {
	f := insightDeepFixture()
	s := insightTestServer(t, f)
	seen := []string{}
	for from := 0; ; from += 50 {
		w := insightRequest(s, "GET", fmt.Sprintf("/api/addrs/A/txs?from=%d&to=%d", from, from+50), "")
		var out insightHistoryOut
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.TotalItems != 2502 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		if len(out.Items) == 0 {
			break
		}
		seen = append(seen, insightHistoryIDs(out)...)
	}
	if !reflect.DeepEqual(seen, f.address.Txids) {
		t.Fatalf("windows returned %d ids, want %d in order", len(seen), len(f.address.Txids))
	}
}

func TestInsightHistoryDeepPagingAcrossPages(t *testing.T) {
	f := insightDeepFixture()
	// The worker repeats page 1's last id as page 2's first.
	f.pageHook = func(page int, ids []string) []string {
		if page == 2 {
			return append([]string{f.address.Txids[999]}, ids...)
		}
		return ids
	}
	w := insightRequest(insightTestServer(t, f), "GET", "/api/addrs/A/txs?from=995&to=1005", "")
	var out insightHistoryOut
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	ids := insightHistoryIDs(out)
	want := []string{"c993", "c994", "c995", "c996", "c997", "c998", "c999", "c1000", "c1001"}
	if !reflect.DeepEqual(ids, want) || out.To != 995+len(want) {
		t.Fatalf("to=%d ids=%v", out.To, ids)
	}
}

// History items carry isOwn on the address's own inputs and outputs, as the
// converter's Blockbook history does, without changing the worker's tx.
func TestInsightHistoryIsOwn(t *testing.T) {
	for _, path := range []string{"/api/addrs/A/txs?from=0&to=1", "/api/txs?address=A"} {
		f := insightFixture()
		f.address.Txids = []string{"own"}
		f.address.Txs, f.address.UnconfirmedTxs = 1, 0
		f.txs["own"] = &api.Tx{Txid: "own",
			Vin:  []api.Vin{{Addresses: []string{"A"}, IsAddress: true, ValueSat: insightAmount(2)}, {Addresses: []string{"B"}, IsAddress: true}},
			Vout: []api.Vout{{N: 0, Addresses: []string{"B"}, IsAddress: true}, {N: 1, Addresses: []string{"A"}, IsAddress: true}},
		}
		w := insightRequest(insightTestServer(t, f), "GET", path, "")
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		var out struct {
			Items []insightTx `json:"items"`
			Txs   []insightTx `json:"txs"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		items := append(out.Items, out.Txs...)
		if len(items) != 1 {
			t.Fatalf("%s: %s", path, w.Body)
		}
		tx := items[0]
		if !tx.Vin[0].IsOwn || !tx.Vin[0].ScriptPubKey.IsOwn || tx.Vin[1].IsOwn || tx.Vout[0].IsOwn || !tx.Vout[1].IsOwn {
			t.Fatalf("%s: isOwn %s", path, w.Body)
		}
		if f.txs["own"].Vin[0].IsOwn || f.txs["own"].Vout[1].IsOwn {
			t.Fatal("worker transaction was mutated")
		}
	}
}

const insightMasterSummary = `{"addrStr":"A","balance":0.00000001,"balanceSat":"1","totalReceived":0,"totalReceivedSat":0,"totalSent":0,"totalSentSat":0,"unconfirmedBalance":-0.00000001,"unconfirmedBalanceSat":-1,"unconfirmedTxApperances":1,"txApperances":3}` + "\n"

func TestInsightAddrTransactions(t *testing.T) {
	f := insightDeepFixture()
	f.address.Txs, f.address.UnconfirmedTxs = 3, 1
	w := insightRequest(insightTestServer(t, f), "GET", "/api/addr/A", "")
	var out struct {
		Transactions []string `json:"transactions"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if len(out.Transactions) != 1000 || out.Transactions[0] != "p0" || out.Transactions[1] != "p1" || out.Transactions[999] != "c997" {
		t.Fatalf("%d transactions", len(out.Transactions))
	}
	// The summary keys are master's, in master's order, followed by the list.
	if !strings.HasPrefix(w.Body.String(), strings.TrimSuffix(insightMasterSummary, "}\n")+`,"transactions":["p0","p1",`) {
		t.Fatalf("summary prefix: %.300s", w.Body)
	}
	if len(f.fetched) != 0 || !reflect.DeepEqual(f.pages, []int{1}) {
		t.Fatalf("fetched %d txs, pages %v", len(f.fetched), f.pages)
	}

	for _, tc := range []struct {
		path string
		ids  []string
	}{
		{"/api/addr/A?from=0&to=2", []string{"p0", "p1"}},
		{"/api/addr?address=A&from=1500&to=1502", []string{"c1498", "c1499"}},
		{"/api/addr/A?from=5&to=5", []string{}},
		{"/api/addr/A?noTxList=0&from=0&to=1", []string{"p0"}},
	} {
		f := insightDeepFixture()
		w := insightRequest(insightTestServer(t, f), "GET", tc.path, "")
		var out struct {
			Transactions []string `json:"transactions"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || !reflect.DeepEqual(out.Transactions, tc.ids) || !strings.Contains(w.Body.String(), `"transactions":[`) {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body)
		}
	}

	for _, path := range []string{"/api/addr/A?noTxList=1", "/api/addr/A/true", "/insight-api/addr/A/1", "/api/addr?address=A&noTxList=1"} {
		f := insightFixture()
		w := insightRequest(insightTestServer(t, f), "GET", path, "")
		if w.Code != 200 || w.Body.String() != insightMasterSummary {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		if f.detail != api.AccountDetailsTokens || f.prefix != 1 {
			t.Fatalf("%s: summary query detail %d size %d", path, f.detail, f.prefix)
		}
	}
}

func TestInsightAddrValues(t *testing.T) {
	for _, tc := range []struct{ value, body string }{
		{"balance", "1\n"}, {"unconfirmedBalance", "-1\n"}, {"totalReceived", "0\n"}, {"totalSent", "0\n"},
	} {
		f := insightFixture()
		w := insightRequest(insightTestServer(t, f), "GET", "/api/addr/A/"+tc.value, "")
		if w.Code != 200 || w.Body.String() != tc.body {
			t.Fatalf("%s: %d %q", tc.value, w.Code, w.Body)
		}
		if f.detail != api.AccountDetailsTokens || f.prefix != 1 || !reflect.DeepEqual(f.pages, []int{1}) {
			t.Fatalf("%s: query detail %d size %d pages %v", tc.value, f.detail, f.prefix, f.pages)
		}
	}
	f := insightFixture()
	f.address.TotalReceivedSat = insightAmount(9007199254740992)
	if w := insightRequest(insightTestServer(t, f), "GET", "/api/addr/A/totalReceived", ""); w.Code != 500 {
		t.Fatalf("unsafe amount: %d %s", w.Code, w.Body)
	}
	f = insightFixture()
	f.err = api.NewAPIError("Invalid address", true)
	if w := insightRequest(insightTestServer(t, f), "GET", "/api/addr/x/balance", ""); w.Code != 400 {
		t.Fatalf("invalid address: %d %s", w.Code, w.Body)
	}
}

func TestInsightTxsByAddress(t *testing.T) {
	f := insightDeepFixture()
	w := insightRequest(insightTestServer(t, f), "GET", "/api/txs?address=A&pageNum=1", "")
	var out struct {
		PagesTotal int `json:"pagesTotal"`
		Txs        []struct {
			Txid string `json:"txid"`
		} `json:"txs"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	ids := []string{}
	for _, tx := range out.Txs {
		ids = append(ids, tx.Txid)
	}
	if out.PagesTotal != 251 || !reflect.DeepEqual(ids, insightSeq("c", 8, 18)) {
		t.Fatalf("pagesTotal %d ids %v", out.PagesTotal, ids)
	}
	for _, tc := range []struct {
		query string
		code  int
		ids   int
	}{
		{"?address=A", 200, 10}, {"?address=A&pageNum=250", 200, 2}, {"?address=A&pageNum=251", 200, 0},
		{"?address=A&pageNum=99999999999999999999", 200, 0},
		{"?address=A&pageNum=-1", 400, 0}, {"?address=A&pageNum=", 400, 0}, {"?address=A&pageNum=x", 400, 0},
		{"?address=", 400, 0}, {"?address=A,B", 400, 0},
	} {
		f := insightDeepFixture()
		w := insightRequest(insightTestServer(t, f), "GET", "/api/txs"+tc.query, "")
		out.Txs = nil
		if w.Code != tc.code || (tc.code == 200 && (json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Txs) != tc.ids || out.PagesTotal != 251 || !strings.Contains(w.Body.String(), `"txs":[`))) {
			t.Fatalf("%s: %d %s", tc.query, w.Code, w.Body)
		}
	}
}
