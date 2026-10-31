package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trezor/blockbook/api"
	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/bchain/coins/btc"
	"github.com/trezor/blockbook/bchain/coins/ravencoin"
)

// insightRvnFixture holds stock RVN Insight responses (ravencore insight-api)
// and the Blockbook tx of the FREN transfer, recorded on FX-RVN-01.
type insightRvnFixture struct {
	Tip       int `json:"tip"`
	AssetUtxo []struct {
		Path    string                `json:"path"`
		Address string                `json:"address"`
		Asset   string                `json:"asset"`
		Expect  []insightRvnAssetUtxo `json:"expect"`
	} `json:"assetUtxo"`
	BlockbookTx  json.RawMessage `json:"blockbookTx"`
	StockHistory json.RawMessage `json:"stockHistory"`
}

func insightRvnLoad(t *testing.T) (*insightRvnFixture, map[string]json.RawMessage) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	b, err := os.ReadFile(filepath.Join(filepath.Dir(file), "testdata", "insight_ravencoin.json"))
	if err != nil {
		t.Fatal(err)
	}
	fix := &insightRvnFixture{}
	if err := json.Unmarshal(b, fix); err != nil {
		t.Fatal(err)
	}
	// the stock bodies verbatim, for byte comparison
	var raw struct {
		AssetUtxo []struct {
			Path   string          `json:"path"`
			Expect json.RawMessage `json:"expect"`
		} `json:"assetUtxo"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	bodies := map[string]json.RawMessage{}
	for _, c := range raw.AssetUtxo {
		bodies[c.Path] = c.Expect
	}
	return fix, bodies
}

type insightRvnRPC struct {
	mu sync.Mutex
	// utxos is keyed by address + "|" + assetName; height is the tip.
	utxos   map[string][]insightRvnUtxo
	height  int
	mempool []insightRvnDelta
	txids   map[string][]string
	calls   []string
	params  [][]interface{}
	fail    bool
	// txidsEntered, when set, is signalled by each getaddresstxids call,
	// which then waits for txidsGate.
	txidsEntered chan struct{}
	txidsGate    chan struct{}
	// utxosEntered and utxosGate do the same for getaddressutxos.
	utxosEntered chan struct{}
	utxosGate    chan struct{}
}

// count returns how many times method was called.
func (r *insightRvnRPC) count(method string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, m := range r.calls {
		if m == method {
			n++
		}
	}
	return n
}

func (r *insightRvnRPC) call(method string, params []interface{}, result interface{}) error {
	r.mu.Lock()
	r.calls = append(r.calls, method)
	r.params = append(r.params, params)
	r.mu.Unlock()
	if method == "getaddresstxids" && r.txidsEntered != nil {
		r.txidsEntered <- struct{}{}
		<-r.txidsGate
	}
	if method == "getaddressutxos" && r.utxosEntered != nil {
		r.utxosEntered <- struct{}{}
		<-r.utxosGate
	}
	if r.fail {
		return &bchain.RPCError{Code: -1, Message: "secret daemon text"}
	}
	opts := params[0].(map[string]interface{})
	addresses := opts["addresses"].([]string)
	var value interface{}
	switch method {
	case "getaddressutxos":
		var rows []insightRvnUtxo
		for _, a := range addresses {
			rows = append(rows, r.utxos[a+"|"+opts["assetName"].(string)]...)
		}
		value = map[string]interface{}{"utxos": rows, "hash": "tiphash", "height": r.height}
	case "getaddressmempool":
		value = r.mempool
		if r.mempool == nil {
			value = []interface{}{}
		}
	case "getaddresstxids":
		var ids []string
		for _, a := range addresses {
			ids = append(ids, r.txids[a]...)
		}
		value = ids
		if ids == nil {
			value = []interface{}{}
		}
	default:
		return &bchain.RPCError{Code: -32601, Message: "Method not found"}
	}
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, result)
}

func insightRvnParser() bchain.BlockChainParser {
	return ravencoin.NewRavencoinParser(ravencoin.GetChainParams("main"), &btc.Configuration{})
}

func insightRvnSetup(t *testing.T, f *insightFake) (*InsightServer, *insightRvnRPC) {
	t.Helper()
	s := insightTestServer(t, f)
	s.SetChainParser(insightRvnParser())
	rpc := &insightRvnRPC{utxos: map[string][]insightRvnUtxo{}, txids: map[string][]string{}}
	s.SetBackend(rpc.call, "RVN")
	if err := s.EnableExtension("ravencoin"); err != nil {
		t.Fatal(err)
	}
	return s, rpc
}

// Every asset script in the stock utxo responses decodes to the row's asset
// name and amount, paid to the row's address.
func TestInsightRavencoinDecodeStockScripts(t *testing.T) {
	fix, _ := insightRvnLoad(t)
	parser := insightRvnParser()
	n := 0
	for _, c := range fix.AssetUtxo {
		for _, row := range c.Expect {
			a, ok := insightRvnDecodeAsset(insightRvnHex(t, row.ScriptPubKey))
			if !ok {
				t.Fatalf("%s %s: not decoded", row.Txid, row.ScriptPubKey)
			}
			addresses, _, err := parser.GetAddressesFromAddrDesc(a.owner)
			if err != nil || len(addresses) != 1 || addresses[0] != row.Address {
				t.Fatalf("%s: owner %v %v, want %s", row.Txid, addresses, err, row.Address)
			}
			if a.asset.Name != row.AssetName || a.asset.Amount != row.Amount {
				t.Fatalf("%s: %s %s, want %s %s", row.Txid, a.asset.Name, a.asset.Amount, row.AssetName, row.Amount)
			}
			n++
		}
	}
	if n < 50 {
		t.Fatalf("only %d scripts checked", n)
	}
}

// The asset utxo routes answer the stock responses byte for byte, given
// ravend's getaddressutxos for the same capture (stock reverses ravend's
// height-ascending list, and its confirmations are tip - height + 1).
func TestInsightRavencoinAssetUtxoMatchesStock(t *testing.T) {
	fix, bodies := insightRvnLoad(t)
	s, rpc := insightRvnSetup(t, insightFixture())
	rpc.height = fix.Tip
	for _, c := range fix.AssetUtxo {
		var rows []insightRvnUtxo
		for i := len(c.Expect) - 1; i >= 0; i-- {
			e := c.Expect[i]
			rows = append(rows, insightRvnUtxo{Address: e.Address, AssetName: e.AssetName, Txid: e.Txid, OutputIndex: e.Vout, Script: e.ScriptPubKey, Satoshis: e.Satoshis, Height: e.Height})
		}
		rpc.utxos[c.Address+"|"+c.Asset] = rows
	}
	for _, c := range fix.AssetUtxo {
		t.Run(c.Path, func(t *testing.T) {
			rpc.calls, rpc.params = nil, nil
			w := insightRequest(s, "GET", c.Path, "")
			if w.Code != 200 {
				t.Fatalf("got %d %s", w.Code, w.Body.String())
			}
			if got, want := strings.TrimSuffix(w.Body.String(), "\n"), insightCompact(t, bodies[c.Path]); got != want {
				t.Fatalf("differs from stock\n got %.600s\nwant %.600s", got, want)
			}
			// the asset name reaches ravend decoded (%2F, %23)
			want := map[string]interface{}{"addresses": []string{c.Address}, "chaininfo": true, "assetName": c.Asset}
			if !reflect.DeepEqual(rpc.calls, []string{"getaddressmempool", "getaddressutxos"}) || !reflect.DeepEqual(rpc.params[1][0], want) {
				t.Fatalf("calls %v %v", rpc.calls, rpc.params)
			}
			if !reflect.DeepEqual(rpc.params[0], []interface{}{map[string]interface{}{"addresses": []string{c.Address}}, true}) {
				t.Fatalf("mempool params %v", rpc.params[0])
			}
		})
	}
}

// Mempool outputs of the asset come first with ts and no height, carrying the
// real output script; outputs a mempool tx spends are dropped; RVN and other
// assets' deltas are left out; an output whose tx the worker cannot load is
// left out (stock gave it a plain P2PKH script the wallet's signature would
// not match).
func TestInsightRavencoinAssetUtxoMempool(t *testing.T) {
	const addr = "RPwpAwECnjm2FZVduEE8M5VjWc2sSHs6Lo"
	f := insightFixture()
	fren := "76a914a0dfb26225355e9f0600a5839a92f99f3a98b70988acc01172766e74044652454e00e1f5050000000075"
	f.txs["p1"] = &api.Tx{Txid: "p1", Vout: []api.Vout{{N: 0, Hex: "76a914a0dfb26225355e9f0600a5839a92f99f3a98b70988ac"}, {N: 1, Hex: fren}}}
	cate := "76a914a0dfb26225355e9f0600a5839a92f99f3a98b70988acc01172766e740443415445070000000000000075"
	f.txs["p2"] = &api.Tx{Txid: "p2", Vout: []api.Vout{{N: 0, Hex: cate}}}
	s, rpc := insightRvnSetup(t, f)
	rpc.height = 100
	rpc.utxos[addr+"|FREN"] = []insightRvnUtxo{
		{Address: addr, AssetName: "FREN", Txid: "c1", OutputIndex: 1, Script: fren, Satoshis: "100000000", Height: 90},
		{Address: addr, AssetName: "FREN", Txid: "c2", OutputIndex: 0, Script: fren, Satoshis: "200000000", Height: 95},
	}
	rpc.utxos[addr+"|*"] = rpc.utxos[addr+"|FREN"]
	rpc.mempool = []insightRvnDelta{
		{Address: addr, AssetName: "FREN", Txid: "p1", Index: 1, Satoshis: "500000000", Timestamp: 1700000001},
		{Address: addr, AssetName: "RVN", Txid: "p1", Index: 0, Satoshis: "1000", Timestamp: 1700000001},
		{Address: addr, AssetName: "CATE", Txid: "p2", Index: 0, Satoshis: "7", Timestamp: 1700000002},
		{Address: addr, AssetName: "FREN", Txid: "p3", Index: 0, Satoshis: "-200000000", Timestamp: 1700000003, PrevTxid: "c2", PrevOut: 0},
		// already confirmed: listed once, from the confirmed set
		{Address: addr, AssetName: "FREN", Txid: "c1", Index: 1, Satoshis: "100000000", Timestamp: 1700000000},
		// no worker tx: left out
		{Address: addr, AssetName: "FREN", Txid: "p4", Index: 3, Satoshis: "100", Timestamp: 1700000004},
	}
	w := insightRequest(s, "GET", "/api/addr/"+addr+"/asset/FREN/utxo", "")
	want := `[{"address":"` + addr + `","txid":"p1","vout":1,"scriptPubKey":"` + fren + `","assetName":"FREN","amount":5,"satoshis":500000000,"confirmations":0,"ts":1700000001},` +
		`{"address":"` + addr + `","txid":"c1","vout":1,"scriptPubKey":"` + fren + `","assetName":"FREN","amount":1,"satoshis":100000000,"height":90,"confirmations":11}]`
	if got := strings.TrimSuffix(w.Body.String(), "\n"); w.Code != 200 || got != want {
		t.Fatalf("got %d\n%s\nwant\n%s", w.Code, got, want)
	}
	// "*": every non-RVN asset in the mempool
	w = insightRequest(s, "GET", "/api/addr/"+addr+"/asset/*/utxo", "")
	var rows []insightRvnAssetUtxo
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil || len(rows) != 3 || rows[0].AssetName != "CATE" || rows[0].ScriptPubKey != cate || rows[1].Txid != "p1" || rows[2].Txid != "c1" {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}

// The confirmed asset utxos are read once per tip, address set (in any
// order) and asset; the mempool is read on every request; a new tip reads
// them again; the answer is the same from the cache.
func TestInsightRavencoinAssetUtxoCachedPerTip(t *testing.T) {
	const a, b = "RPwpAwECnjm2FZVduEE8M5VjWc2sSHs6Lo", "RU67hqkZMRNqWp5Sa4HcKnKUQyC77PKBB4"
	f := insightFixture()
	f.txs["p1"] = &api.Tx{Txid: "p1", Vout: []api.Vout{{N: 0, Hex: "01"}}}
	s, rpc := insightRvnSetup(t, f)
	x := s.ext.(*insightRavencoin)
	tip := "h1"
	var tipMu sync.Mutex
	x.tipHash = func() (string, bool) {
		tipMu.Lock()
		defer tipMu.Unlock()
		return tip, true
	}
	rpc.height = 10
	rpc.utxos[a+"|FREN"] = []insightRvnUtxo{{Address: a, AssetName: "FREN", Txid: "x", OutputIndex: 0, Script: "00", Satoshis: "1", Height: 5}}
	rpc.utxos[b+"|FREN"] = []insightRvnUtxo{{Address: b, AssetName: "FREN", Txid: "y", OutputIndex: 1, Script: "00", Satoshis: "2", Height: 6}}
	path := "/api/addr/" + a + "/asset/FREN/utxo"
	first := insightRequest(s, "GET", path, "").Body.String()
	if again := insightRequest(s, "GET", path, "").Body.String(); again != first || !strings.Contains(first, `"confirmations":6`) {
		t.Fatalf("cached answer differs:\n%s\n%s", first, again)
	}
	insightRequest(s, "GET", "/api/addrs/"+a+"/asset/FREN/utxo", "")
	if rpc.count("getaddressutxos") != 1 || rpc.count("getaddressmempool") != 3 {
		t.Fatalf("calls %v", rpc.calls)
	}
	// another asset is another key
	insightRequest(s, "GET", "/api/addr/"+a+"/asset/*/utxo", "")
	insightRequest(s, "GET", "/api/addrs/"+a+","+b+"/asset/FREN/utxo", "")
	insightRequest(s, "GET", "/api/addrs/"+b+","+a+"/asset/FREN/utxo", "")
	if n := rpc.count("getaddressutxos"); n != 3 {
		t.Fatalf("%d getaddressutxos calls", n)
	}
	// the mempool is seen on the same tip
	rpc.mempool = []insightRvnDelta{
		{Address: a, AssetName: "FREN", Txid: "p1", Index: 0, Satoshis: "3", Timestamp: 7},
		{Address: a, AssetName: "FREN", Txid: "p1", Index: 1, Satoshis: "-1", PrevTxid: "x", PrevOut: 0},
	}
	w := insightRequest(s, "GET", path, "")
	if got := strings.TrimSuffix(w.Body.String(), "\n"); got != `[{"address":"`+a+`","txid":"p1","vout":0,"scriptPubKey":"01","assetName":"FREN","amount":0.00000003,"satoshis":3,"confirmations":0,"ts":7}]` || rpc.count("getaddressutxos") != 3 {
		t.Fatalf("mempool: %s %v", got, rpc.calls)
	}
	// a new tip reads them again
	rpc.mempool = nil
	rpc.height = 11
	tipMu.Lock()
	tip = "h2"
	tipMu.Unlock()
	if w := insightRequest(s, "GET", path, ""); !strings.Contains(w.Body.String(), `"confirmations":7`) || rpc.count("getaddressutxos") != 4 {
		t.Fatalf("new tip: %s %v", w.Body.String(), rpc.calls)
	}
}

// The asset utxo reads share the history's ravend slots: over the cap a
// request waits briefly, then gets the listener's 503. Concurrent requests
// for one key share one getaddressutxos call.
func TestInsightRavencoinAssetUtxoSlots(t *testing.T) {
	const a, b = "RPwpAwECnjm2FZVduEE8M5VjWc2sSHs6Lo", "RU67hqkZMRNqWp5Sa4HcKnKUQyC77PKBB4"
	s, rpc := insightRvnSetup(t, insightFixture())
	x := s.ext.(*insightRavencoin)
	x.tipHash = func() (string, bool) { return "h1", true }
	x.txids = newInsightRvnTxidCache(insightRvnTxidsMaxEntries, insightRvnTxidsMaxIds, 1, 50*time.Millisecond)
	x.utxos = newInsightRvnUtxoCache(insightRvnUtxosMaxEntries, insightRvnUtxosMaxRows, x.txids.slots, 50*time.Millisecond)
	rpc.height = 10
	rpc.utxos[a+"|FREN"] = []insightRvnUtxo{{Address: a, AssetName: "FREN", Txid: "x", OutputIndex: 0, Script: "00", Satoshis: "1", Height: 5}}
	rpc.txids[b] = []string{}
	rpc.utxosEntered, rpc.utxosGate = make(chan struct{}, 16), make(chan struct{})
	const n = 4
	joined := make(chan struct{}, n)
	x.utxos.joined = func() { joined <- struct{}{} }

	bodies := make(chan string, n)
	route := insightRoute{kind: insightRvnPrefix + "assetutxo", addresses: []string{a}, arg: "FREN"}
	run := func() {
		v, _, err := x.handle(route)
		if err != nil {
			t.Error(err)
		}
		b, _ := json.Marshal(v)
		bodies <- string(b)
	}
	go run()
	<-rpc.utxosEntered // the utxo read holds the only slot
	<-joined
	for i := 1; i < n; i++ {
		go run()
	}
	for i := 1; i < n; i++ {
		<-joined
	}
	// history needs the same slot
	w := insightRequest(s, "GET", "/api/addrs/"+b+"/txs", "")
	if w.Code != http.StatusServiceUnavailable || strings.TrimSpace(w.Body.String()) != `{"status":"error","error":"Service unavailable"}` {
		t.Fatalf("over the cap: %d %s", w.Code, w.Body.String())
	}
	close(rpc.utxosGate)
	for i := 0; i < n; i++ {
		if got := <-bodies; !strings.Contains(got, `"txid":"x"`) {
			t.Fatalf("result %d: %s", i, got)
		}
	}
	if c := rpc.count("getaddressutxos"); c != 1 {
		t.Fatalf("%d getaddressutxos calls", c)
	}
	// and an asset utxo read waits for a history call's slot the same way
	rpc.utxos[b+"|FREN"] = nil
	rpc.txidsEntered, rpc.txidsGate = make(chan struct{}, 16), make(chan struct{})
	go func() { _, _, _ = x.addressTxids(context.Background(), []string{b}) }()
	<-rpc.txidsEntered
	if w := insightRequest(s, "GET", "/api/addr/"+b+"/asset/FREN/utxo", ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("utxo over the cap: %d %s", w.Code, w.Body.String())
	}
	close(rpc.txidsGate)
}

func TestInsightRavencoinAssetRoutes(t *testing.T) {
	const a, b = "RPwpAwECnjm2FZVduEE8M5VjWc2sSHs6Lo", "RU67hqkZMRNqWp5Sa4HcKnKUQyC77PKBB4"
	f := insightFixture()
	s, rpc := insightRvnSetup(t, f)
	rpc.height = 10
	rpc.utxos[a+"|*"] = []insightRvnUtxo{{Address: a, AssetName: "FREN", Txid: "x", OutputIndex: 0, Script: "00", Satoshis: "1", Height: 5}}
	rpc.utxos[b+"|*"] = []insightRvnUtxo{{Address: b, AssetName: "CATE", Txid: "y", OutputIndex: 0, Script: "00", Satoshis: "2", Height: 6}}

	// multi-address: one ravend call for all the addresses
	w := insightRequest(s, "GET", "/api/addrs/"+a+","+b+","+a+"/asset/%2A/utxo", "")
	if w.Code != 200 || strings.Count(w.Body.String(), `"txid"`) != 2 || len(rpc.calls) != 2 {
		t.Fatalf("multi: %d %s %v", w.Code, w.Body.String(), rpc.calls)
	}
	if got := rpc.params[1][0].(map[string]interface{})["addresses"]; !reflect.DeepEqual(got, []string{a, b}) {
		t.Fatalf("multi addresses %v", got)
	}
	// insight-api prefix and a trailing slash route the same way
	if w := insightRequest(s, "GET", "/insight-api/addr/"+a+"/asset/*/utxo/", ""); w.Code != 200 {
		t.Fatalf("insight-api prefix: %d", w.Code)
	}

	for _, tc := range []struct {
		method, path string
		code         int
		body         string
	}{
		{"GET", "/api/addr/notanaddress/asset/FREN/utxo", 400, `{"status":"error","error":"Invalid address"}`},
		{"GET", "/api/addr/" + a + "," + b + "/asset/FREN/utxo", 400, ""},
		{"GET", "/api/addrs/" + a + ",bad/asset/FREN/utxo", 400, `{"status":"error","error":"Invalid address"}`},
		{"GET", "/api/addr/" + a + "/asset//utxo", 400, ""},
		{"GET", "/api/addr/" + a + "/asset/" + strings.Repeat("A", 129) + "/utxo", 400, ""},
		{"POST", "/api/addr/" + a + "/asset/FREN/utxo", 405, ""},
		// W8: stock's failure, as a JSON 500 without the stack trace
		{"GET", "/api/assets?asset=FREN&verbose=true", 500, `{"status":"error","error":"Backend request failed"}`},
		{"GET", "/api/assets?asset=WXRAVEN%2FAIRDROP&verbose=true", 500, `{"status":"error","error":"Backend request failed"}`},
		// RVN-only stock routes that never worked stay 404
		{"GET", "/api/addr/" + a + "/balances", 404, `{"status":"error","error":"Route not found"}`},
		{"GET", "/api/addr/" + a + "/tags", 404, `{"status":"error","error":"Route not found"}`},
		{"GET", "/api/addr/" + a + "/frozen", 404, `{"status":"error","error":"Route not found"}`},
		{"GET", "/api/addr/" + a + "/checkTag/asset/KYC", 404, `{"status":"error","error":"Route not found"}`},
		{"GET", "/api/assets/restricted", 404, `{"status":"error","error":"Route not found"}`},
		{"GET", "/api/asset/FREN/verifier", 404, ""},
		{"GET", "/api/utils/estimatesmartfee?nbBlocks=2", 404, ""},
	} {
		w := insightRequest(s, tc.method, tc.path, "")
		if w.Code != tc.code || tc.body != "" && strings.TrimSuffix(w.Body.String(), "\n") != tc.body {
			t.Errorf("%s %s: got %d %s, want %d %s", tc.method, tc.path, w.Code, w.Body.String(), tc.code, tc.body)
		}
	}

	// daemon failure: generic 500, no daemon text
	rpc.fail = true
	if w := insightRequest(s, "GET", "/api/addr/"+a+"/asset/FREN/utxo", ""); w.Code != 500 || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("daemon failure: %d %s", w.Code, w.Body.String())
	}
	// no daemon RPC: 501
	s.SetBackend(nil, "RVN")
	if w := insightRequest(s, "GET", "/api/addr/"+a+"/asset/FREN/utxo", ""); w.Code != 501 {
		t.Fatalf("no rpc: %d", w.Code)
	}
}

func TestInsightRavencoinMultiUtxoCap(t *testing.T) {
	const a = "RPwpAwECnjm2FZVduEE8M5VjWc2sSHs6Lo"
	s, rpc := insightRvnSetup(t, insightFixture())
	rows := make([]insightRvnUtxo, insightMultiUtxoMax+1)
	for i := range rows {
		rows[i] = insightRvnUtxo{Address: a, AssetName: "FREN", Txid: "x", OutputIndex: i, Script: "00", Satoshis: "1", Height: 1}
	}
	rpc.utxos[a+"|FREN"] = rows
	if w := insightRequest(s, "GET", "/api/addrs/"+a+"/asset/FREN/utxo", ""); w.Code != 413 {
		t.Fatalf("multi cap: %d", w.Code)
	}
	// the single-address route is not capped
	if w := insightRequest(s, "GET", "/api/addr/"+a+"/asset/FREN/utxo", ""); w.Code != 200 {
		t.Fatalf("single: %d", w.Code)
	}
}

// insightRvnFrenTx is the Blockbook tx of the FREN transfer, with the input
// that spends an asset output given its asset script as AddrDesc (the
// worker keeps the spent output's script there).
func insightRvnFrenTx(t *testing.T, fix *insightRvnFixture) *api.Tx {
	t.Helper()
	tx := &api.Tx{}
	if err := json.Unmarshal(fix.BlockbookTx, tx); err != nil {
		t.Fatal(err)
	}
	desc, err := insightRvnParser().GetAddrDescFromAddress("REZHVHDyK3mozmrvDN3a6hAxG2X7kM5w5S")
	if err != nil {
		t.Fatal(err)
	}
	tx.Vin[2].AddrDesc = insightRvnScript(t, desc, insightRvnPayload('t', "FREN", insightRvnLE(100000000)))
	for i := range tx.Vin[:2] {
		tx.Vin[i].AddrDesc, _ = insightRvnParser().GetAddrDescFromAddress(tx.Vin[i].Addresses[0])
	}
	return tx
}

// An asset-only tx, which Blockbook's index files under no address, is in the
// address history (ravend getaddresstxids), with stock's asset fields.
func TestInsightRavencoinHistoryAssetOnlyTx(t *testing.T) {
	const addr = "RPwpAwECnjm2FZVduEE8M5VjWc2sSHs6Lo"
	fix, _ := insightRvnLoad(t)
	f := insightFixture()
	f.address = &api.Address{AddrStr: addr} // Blockbook: txs 0
	tx := insightRvnFrenTx(t, fix)
	f.txs[tx.Txid] = tx
	s, rpc := insightRvnSetup(t, f)
	rpc.txids[addr] = []string{tx.Txid}

	type vin struct {
		Addr *string `json:"addr"`
	}
	type vout struct {
		ScriptPubKey struct {
			Hex       string   `json:"hex"`
			Addresses []string `json:"addresses"`
			Type      string   `json:"type"`
		} `json:"scriptPubKey"`
		AssetType string                 `json:"assetType,omitempty"`
		Asset     map[string]interface{} `json:"asset,omitempty"`
	}
	type history struct {
		TotalItems int `json:"totalItems"`
		From       int `json:"from"`
		To         int `json:"to"`
		Items      []struct {
			Txid string `json:"txid"`
			Vin  []vin  `json:"vin"`
			Vout []vout `json:"vout"`
		} `json:"items"`
	}
	var want history
	if err := json.Unmarshal(fix.StockHistory, &want); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/addrs/" + addr + "/txs?from=0&to=50", "/api/addrs/" + addr + "/txs", "/api/addrs/txs?address=" + addr} {
		w := insightRequest(s, "GET", path, "")
		var got history
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		if !reflect.DeepEqual(got, want) {
			g, _ := json.Marshal(got)
			e, _ := json.Marshal(want)
			t.Fatalf("%s: stock fields differ\n got %s\nwant %s", path, g, e)
		}
	}
	// the tx route carries the same fields
	w := insightRequest(s, "GET", "/api/tx/"+tx.Txid, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"assetType":"transfer_asset","asset":{"name":"FREN","amount":1}`) || !strings.Contains(w.Body.String(), `"addr":"REZHVHDyK3mozmrvDN3a6hAxG2X7kM5w5S"`) {
		t.Fatalf("tx: %d %s", w.Code, w.Body.String())
	}
	// txs?address= pages of 10 over the same list
	w = insightRequest(s, "GET", "/api/txs?address="+addr, "")
	if w.Code != 200 || !strings.HasPrefix(w.Body.String(), `{"pagesTotal":1,"txs":[{"txid":"`+tx.Txid) {
		t.Fatalf("txs?address=: %d %.200s", w.Code, w.Body.String())
	}
	// Blockbook's own address index is not read for history
	if len(f.historyPages) != 0 {
		t.Fatalf("worker history read: %v", f.historyPages)
	}
}

// History order and paging follow stock: mempool txids first (newest first,
// confirmed ones dropped), then ravend's confirmed txids newest first;
// totalItems counts the whole list.
func TestInsightRavencoinHistoryOrder(t *testing.T) {
	const a, b = "RPwpAwECnjm2FZVduEE8M5VjWc2sSHs6Lo", "RU67hqkZMRNqWp5Sa4HcKnKUQyC77PKBB4"
	f := insightFixture()
	for _, id := range []string{"t1", "t2", "t3", "m1", "m2", "u1"} {
		f.txs[id] = &api.Tx{Txid: id}
	}
	s, rpc := insightRvnSetup(t, f)
	rpc.txids[a] = []string{"t1", "t2", "t3"} // ravend: oldest first
	rpc.txids[b] = []string{"u1"}
	rpc.mempool = []insightRvnDelta{{Txid: "m1"}, {Txid: "m2"}, {Txid: "m1"}, {Txid: "t2"}}
	ids := func(path string) (int, []string, int, int) {
		t.Helper()
		w := insightRequest(s, "GET", path, "")
		var got struct {
			TotalItems int `json:"totalItems"`
			From, To   int
			Items      []struct {
				Txid string `json:"txid"`
			} `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		out := []string{}
		for _, it := range got.Items {
			out = append(out, it.Txid)
		}
		return got.TotalItems, out, got.From, got.To
	}
	if total, got, _, _ := ids("/api/addrs/" + a + "/txs"); total != 5 || !reflect.DeepEqual(got, []string{"m2", "m1", "t3", "t2", "t1"}) {
		t.Fatalf("order: %d %v", total, got)
	}
	if total, got, from, to := ids("/api/addrs/" + a + "/txs/1/3"); total != 5 || !reflect.DeepEqual(got, []string{"m1", "t3"}) || from != 1 || to != 3 {
		t.Fatalf("window: %d %v %d %d", total, got, from, to)
	}
	if total, got, _, _ := ids("/api/addrs/" + a + "/txs?from=4&to=10"); total != 5 || !reflect.DeepEqual(got, []string{"t1"}) {
		t.Fatalf("tail: %d %v", total, got)
	}
	if total, got, _, _ := ids("/api/addrs/" + a + "/txs?from=9&to=10"); total != 5 || len(got) != 0 {
		t.Fatalf("past the end: %d %v", total, got)
	}
	// multi-address history: one ravend list for all the addresses
	if total, got, _, _ := ids("/api/addrs/" + a + "," + b + "/txs?from=0&to=10"); total != 6 || !reflect.DeepEqual(got, []string{"m2", "m1", "u1", "t3", "t2", "t1"}) {
		t.Fatalf("multi: %d %v", total, got)
	}
	// windows are still checked like the generic route
	if w := insightRequest(s, "GET", "/api/addrs/"+a+"/txs?from=0&to=1001", ""); w.Code != 400 {
		t.Fatalf("oversized window: %d", w.Code)
	}
	if w := insightRequest(s, "GET", "/api/addrs/notanaddress/txs", ""); w.Code != 400 {
		t.Fatalf("invalid address: %d", w.Code)
	}
	rpc.fail = true
	if w := insightRequest(s, "GET", "/api/addrs/"+a+"/txs", ""); w.Code != 500 || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("daemon failure: %d %s", w.Code, w.Body.String())
	}
}

// Without the extension the asset routes are unknown, and other coins keep
// the generic addr/:addr/<x> summary form.
func TestInsightRavencoinOnlyWithExtension(t *testing.T) {
	s := insightTestServer(t, insightFixture())
	if w := insightRequest(s, "GET", "/api/addr/A/asset/FREN/utxo", ""); w.Code != 404 {
		t.Fatalf("asset route without extension: %d", w.Code)
	}
	if w := insightRequest(s, "GET", "/api/assets?asset=FREN", ""); w.Code != 404 {
		t.Fatalf("assets without extension: %d", w.Code)
	}
	if w := insightRequest(s, "GET", "/api/addr/A/balances", ""); w.Code != 200 {
		t.Fatalf("summary form: %d", w.Code)
	}
}
