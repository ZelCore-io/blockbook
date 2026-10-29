package server

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trezor/blockbook/api"
	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/common"
)

func TestInsightBothPrefixes(t *testing.T) {
	for _, path := range []string{"/api/status", "/api/addr/A/utxo", "/api/addr/A", "/api/addrs/A/txs?from=1&to=3", "/api/addr?address=A", "/api/addr/A/true", "/api/addr/utxo?address=A", "/api/addrs/txs?address=A&from=1&to=3", "/api/addrs/A/txs/1/3", "/api/addrs/A/txs?from=20&to=30", "/api/sync", "/api/peer", "/api/version"} {
		want := insightRequest(insightTestServer(t, insightFixture()), "GET", path, "")
		got := insightRequest(insightTestServer(t, insightFixture()), "GET", "/insight-api"+strings.TrimPrefix(path, "/api"), "")
		if want.Code != 200 || got.Code != 200 || got.Body.String() != want.Body.String() {
			t.Fatalf("%s: api %d %s, insight-api %d %s", path, want.Code, want.Body, got.Code, got.Body)
		}
	}
	s := insightTestServer(t, insightFixture())
	for _, path := range []string{"/insight-apix/status", "/API/status", "/insight-api", "/api"} {
		if w := insightRequest(s, "GET", path, ""); w.Code != 404 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
}

func TestInsightStatusVariants(t *testing.T) {
	info := `{"info":{"blocks":10,"difficulty":2.5,"errors":"","protocolversion":456,"version":123}}` + "\n"
	for _, tc := range []struct{ query, want string }{
		{"?q=getDifficulty", `{"difficulty":2.5}` + "\n"},
		{"?q=getBestBlockHash", `{"bestblockhash":"bb"}` + "\n"},
		{"?q=getLastBlockHash", `{"lastblockhash":"tip","syncTipHash":"tip"}` + "\n"},
		{"?q=getInfo", info},
		{"?q=unknown", info},
		{"", info},
	} {
		f := insightFixture()
		w := insightRequest(insightTestServer(t, f), "GET", "/api/status"+tc.query, "")
		if w.Code != 200 || w.Body.String() != tc.want {
			t.Fatalf("%s: %d %s", tc.query, w.Code, w.Body)
		}
		if tc.query == "?q=getLastBlockHash" && (f.blocksPage != 1 || f.blocksSize != 1) {
			t.Fatalf("GetBlocks(%d,%d)", f.blocksPage, f.blocksSize)
		}
	}
	f := insightFixture()
	f.blocks = &api.Blocks{}
	if w := insightRequest(insightTestServer(t, f), "GET", "/api/status?q=getLastBlockHash", ""); w.Code != 503 {
		t.Fatal(w.Code)
	}
	f = insightFixture()
	f.info.Backend.BackendError = "http://user:password@secret"
	if w := insightRequest(insightTestServer(t, f), "GET", "/api/status?q=getBestBlockHash", ""); w.Code != 503 || strings.Contains(w.Body.String(), "secret") {
		t.Fatal(w)
	}
}

func TestInsightSync(t *testing.T) {
	for _, tc := range []struct {
		inSync      bool
		best        uint32
		blocks      int
		status      string
		pct, height int
	}{
		{true, 10, 10, "finished", 100, 10},
		{false, 5, 10, "syncing", 50, 5},
		{false, 7, 9, "syncing", 78, 7},
		{false, 3, 0, "syncing", 0, 3},
		{true, 11, 10, "finished", 100, 11},
	} {
		f := insightFixture()
		f.info.Blockbook = &api.BlockbookInfo{InSync: tc.inSync, BestHeight: tc.best}
		f.info.Backend.Blocks = tc.blocks
		w := insightRequest(insightTestServer(t, f), "GET", "/api/sync", "")
		var out map[string]interface{}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatal(w)
		}
		if out["status"] != tc.status || out["syncPercentage"] != float64(tc.pct) || out["height"] != float64(tc.height) || out["blockChainHeight"] != float64(tc.blocks) || out["error"] != nil || out["type"] != "bitcore node" || len(out) != 6 {
			t.Fatalf("%+v: %s", tc, w.Body)
		}
	}
	want := `{"status":"finished","blockChainHeight":10,"syncPercentage":100,"height":10,"error":null,"type":"bitcore node"}` + "\n"
	if w := insightRequest(insightTestServer(t, insightFixture()), "GET", "/api/sync", ""); w.Body.String() != want {
		t.Fatal(w.Body)
	}
	f := insightFixture()
	f.info.Blockbook = nil
	if w := insightRequest(insightTestServer(t, f), "GET", "/api/sync", ""); w.Code != 503 {
		t.Fatal(w.Code)
	}
}

func TestInsightPeerVersion(t *testing.T) {
	s := insightTestServer(t, insightFixture())
	if w := insightRequest(s, "GET", "/api/peer", ""); w.Code != 200 || w.Body.String() != `{"connected":true,"host":"127.0.0.1","port":null}`+"\n" {
		t.Fatal(w)
	}
	version, _ := json.Marshal(common.GetVersionInfo().Version)
	if w := insightRequest(s, "GET", "/api/version", ""); w.Code != 200 || w.Body.String() != `{"version":`+string(version)+"}\n" {
		t.Fatal(w)
	}
}

func TestInsightRPCAdapter(t *testing.T) {
	var sent []byte
	rpc := NewInsightRPC(func(req interface{}, res interface{}) error {
		sent, _ = json.Marshal(req)
		return json.Unmarshal([]byte(`{"result":"00ff","error":null,"id":"1"}`), res)
	})
	var hex string
	if err := rpc("getblock", []interface{}{"h", 0}, &hex); err != nil || hex != "00ff" {
		t.Fatal(err, hex)
	}
	if string(sent) != `{"method":"getblock","params":["h",0]}` {
		t.Fatal(string(sent))
	}
	if err := rpc("getbestblockhash", nil, nil); err != nil || string(sent) != `{"method":"getbestblockhash","params":[]}` {
		t.Fatal(err, string(sent))
	}
	rpc = NewInsightRPC(func(req interface{}, res interface{}) error {
		return json.Unmarshal([]byte(`{"result":null,"error":{"code":-32601,"message":"x"}}`), res)
	})
	var rpcErr *bchain.RPCError
	if err := rpc("verifymessage", nil, &hex); !errors.As(err, &rpcErr) || rpcErr.Code != -32601 {
		t.Fatal(err)
	}
	transport := errors.New("dial")
	rpc = NewInsightRPC(func(interface{}, interface{}) error { return transport })
	if err := rpc("x", nil, &hex); !errors.Is(err, transport) {
		t.Fatal(err)
	}
}

func TestInsightRouteTable(t *testing.T) {
	for _, tc := range []struct {
		method, path, kind, arg string
		addresses               []string
		allow                   string
	}{
		{"GET", "/api/blocks", "blocks", "", nil, "GET"},
		{"GET", "/insight-api/block/h", "block", "h", nil, "GET"},
		{"GET", "/api/rawblock/h", "rawblock", "h", nil, "GET"},
		{"GET", "/api/block-index/5", "blockindex", "5", nil, "GET"},
		{"GET", "/api/tx/t", "tx", "t", nil, "GET"},
		{"POST", "/api/tx/send", "send", "", nil, "POST"},
		{"GET", "/api/rawtx/t", "rawtx", "t", nil, "GET"},
		{"GET", "/api/txs?block=h", "txs", "", nil, "GET"},
		{"GET", "/api/addr/A/balance", "addrvalue", "balance", []string{"A"}, "GET"},
		{"GET", "/api/addr/A/totalSent", "addrvalue", "totalSent", []string{"A"}, "GET"},
		{"GET", "/api/addr/A/true", "summary", "true", []string{"A"}, "GET"},
		{"GET", "/api/addrs/A,B/utxo", "multiutxo", "", []string{"A", "B"}, "GET"},
		{"POST", "/api/addrs/utxo", "multiutxo", "", nil, "POST"},
		{"GET", "/api/addrs/A,B/unspent", "multiunspent", "", []string{"A", "B"}, "GET"},
		{"GET", "/api/addrs/A,B/balance", "multibalance", "", []string{"A", "B"}, "GET"},
		{"POST", "/api/addrs/txs", "multitxs", "", nil, "GET,POST"},
		{"GET", "/api/addrs/txs?address=A", "history", "", []string{"A"}, "GET,POST"},
		{"GET", "/api/addrs/A,B/txs", "multitxs", "", []string{"A", "B"}, "GET"},
		{"GET", "/api/addrs/A/txs/1/2", "history", "", []string{"A"}, "GET"},
		{"GET", "/api/addrs/A,/txs/1/2", "multitxs", "", []string{"A", ""}, "GET"},
		{"GET", "/api/utils/estimatefee?nbBlocks=2", "estimatefee", "", nil, "GET"},
		{"GET", "/api/currency", "currency", "", nil, "GET"},
		{"POST", "/api/messages/verify", "verify", "", nil, "GET,POST"},
	} {
		route, methods, ok := insightMatch(httptest.NewRequest(tc.method, tc.path, nil))
		if !ok || route.kind != tc.kind || route.arg != tc.arg || strings.Join(route.addresses, "|") != strings.Join(tc.addresses, "|") || strings.Join(methods, ",") != tc.allow || route.query == nil {
			t.Fatalf("%s %s: %+v %v %v", tc.method, tc.path, route, methods, ok)
		}
	}
	for _, path := range []string{"/api/addrs", "/api/tx", "/api/block", "/api/utils", "/api/status/x"} {
		if _, _, ok := insightMatch(httptest.NewRequest("GET", path, nil)); ok {
			t.Fatal(path)
		}
	}
}

func TestInsightBodyAndUnsupported(t *testing.T) {
	s := insightTestServer(t, insightFixture())
	r := httptest.NewRequest("POST", "/api/tx/send", strings.NewReader("rawtx=aA00"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"txid":"sent"`) {
		t.Fatal(w)
	}
	r = httptest.NewRequest("POST", "/api/tx/send", strings.NewReader("rawtx=%zz"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal(w)
	}
	// A kind without a handler is 501, never a raw error.
	if _, code, err := s.handle(insightRoute{kind: "later"}); code != 501 || err == nil {
		t.Fatal(code, err)
	}
	for code, want := range map[int]struct {
		code    int
		message string
	}{400: {400, "Invalid request"}, 404: {404, "Not found"}, 413: {413, "Request body too large"}, 501: {501, "Not supported"}, 503: {503, "Service unavailable"}, 500: {500, "Backend request failed"}, 0: {500, "Backend request failed"}, 502: {500, "Backend request failed"}} {
		if got, message := insightErrorStatus(code); got != want.code || message != want.message {
			t.Fatal(code, got, message)
		}
	}
}
