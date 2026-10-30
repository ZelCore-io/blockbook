//go:build unittest

package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trezor/blockbook/bchain"
)

// insightSupplyRPC answers getblockchaininfo and gettxoutsetinfo with canned
// JSON and counts the calls. A nil answer is the given error.
type insightSupplyRPC struct {
	mu        sync.Mutex
	calls     map[string]int
	info      string
	utxo      string
	utxoError error
}

func (r *insightSupplyRPC) call(method string, params []interface{}, result interface{}) error {
	r.mu.Lock()
	r.calls[method]++
	r.mu.Unlock()
	switch method {
	case "getblockchaininfo":
		return json.Unmarshal([]byte(r.info), result)
	case "gettxoutsetinfo":
		if r.utxoError != nil {
			return r.utxoError
		}
		return json.Unmarshal([]byte(r.utxo), result)
	}
	return &bchain.RPCError{Code: insightRPCMethodNotFound, Message: "Method not found"}
}

// zcashd before 5.0 (zclassicd 2.1, fluxd): shielded pools only, no chainSupply.
const insightOldZcashdInfo = `{"chain":"main","blocks":3302466,"valuePools":[` +
	`{"id":"sprout","monitored":true,"chainValue":12345.67890123,"chainValueZat":1234567890123,"valueDelta":0.0,"valueDeltaZat":0},` +
	`{"id":"sapling","monitored":true,"chainValue":100.00000001,"chainValueZat":10000000001,"valueDelta":0.0,"valueDeltaZat":0}]}`

// gettxoutsetinfo of the same daemon (transparent UTXO set).
const insightOldZcashdUtxo = `{"height":3302466,"bestblock":"00","transactions":1,"txouts":2,"bytes_serialized":3,"hash_serialized":"ab","total_amount":9876543.21000000}`

// insightSupplySetup returns a listener with the UTXO-scan opt-in on, as
// configs/coins/zclassic.json sets it.
func insightSupplySetup(t *testing.T, rpc *insightSupplyRPC) *InsightServer {
	t.Helper()
	rpc.calls = map[string]int{}
	s := insightTestServer(t, insightFixture())
	s.SetBackend(rpc.call, "ZCL")
	s.SetSupplyUTXOScan(true)
	return s
}

// Without the opt-in, a daemon with no cheap source gets the unknown-route
// 404 (as before the route existed) and is never asked for a UTXO scan; the
// cheap sources still answer.
func TestInsightCirculatingSupplyUTXOScanOptIn(t *testing.T) {
	for _, info := range []string{insightOldZcashdInfo, `{"chain":"main","blocks":10}`} {
		rpc := &insightSupplyRPC{info: info, utxo: insightOldZcashdUtxo, calls: map[string]int{}}
		s := insightTestServer(t, insightFixture())
		s.SetBackend(rpc.call, "BTC")
		for _, path := range []string{"/api/statistics/circulating-supply", "/api/statistics/circulating-supply?format=object"} {
			w := insightRequest(s, "GET", path, "")
			if w.Code != 404 || strings.TrimSpace(w.Body.String()) != `{"status":"error","error":"Route not found"}` {
				t.Fatalf("default off %s: %d %s", path, w.Code, w.Body.String())
			}
		}
		if rpc.calls["gettxoutsetinfo"] != 0 {
			t.Fatalf("default off scanned the UTXO set: %v", rpc.calls)
		}
	}
	rpc := &insightSupplyRPC{info: `{"chainSupply":{"monitored":true,"chainValueZat":150000000}}`, calls: map[string]int{}}
	s := insightTestServer(t, insightFixture())
	s.SetBackend(rpc.call, "ZEC")
	if w := insightRequest(s, "GET", "/api/statistics/circulating-supply", ""); w.Code != 200 || w.Body.String() != "1.5" {
		t.Fatalf("cheap source without opt-in: %d %s", w.Code, w.Body.String())
	}
	// opt-in on: the scan is used
	rpc = &insightSupplyRPC{info: insightOldZcashdInfo, utxo: insightOldZcashdUtxo}
	s = insightSupplySetup(t, rpc)
	if w := insightRequest(s, "GET", "/api/statistics/circulating-supply", ""); w.Code != 200 || w.Body.String() != "9888988.88890124" || rpc.calls["gettxoutsetinfo"] != 1 {
		t.Fatalf("opt-in: %d %s %v", w.Code, w.Body.String(), rpc.calls)
	}
}

func TestInsightCirculatingSupplyOldZcashd(t *testing.T) {
	rpc := &insightSupplyRPC{info: insightOldZcashdInfo, utxo: insightOldZcashdUtxo}
	s := insightSupplySetup(t, rpc)
	// 9876543.21 + 12345.67890123 + 100.00000001
	const want = "9888988.88890124"
	for _, prefix := range []string{"/api", "/insight-api", "/api/"} {
		w := insightRequest(s, "GET", prefix+"/statistics/circulating-supply", "")
		if w.Code != 200 || w.Body.String() != want {
			t.Fatalf("%s: %d %q", prefix, w.Code, w.Body.String())
		}
		// stock: Express res.send(string)
		if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
			t.Fatalf("content type %q", ct)
		}
		if w.Header().Get("Access-Control-Allow-Origin") != "*" || w.Header().Get("Link") == "" {
			t.Fatalf("missing Insight headers: %v", w.Header())
		}
	}
	w := insightRequest(s, "GET", "/api/statistics/circulating-supply?format=object", "")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"circulatingSupply":"`+want+`"}` || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("object: %d %q %v", w.Code, w.Body.String(), w.Header())
	}
	// one load, cached for every request above
	if rpc.calls["getblockchaininfo"] != 1 || rpc.calls["gettxoutsetinfo"] != 1 {
		t.Fatalf("calls %v", rpc.calls)
	}
	if w := insightRequest(s, "POST", "/api/statistics/circulating-supply", ""); w.Code != 405 {
		t.Fatalf("POST: %d", w.Code)
	}
	if w := insightRequest(s, "OPTIONS", "/api/statistics/circulating-supply", ""); w.Code != 204 {
		t.Fatalf("OPTIONS: %d", w.Code)
	}
}

func TestInsightCirculatingSupplySources(t *testing.T) {
	for _, tc := range []struct {
		name, info, utxo, want string
		utxoCalls              int
	}{
		// zcashd 5+ / Zebra: chainSupply wins, no UTXO scan
		{"chainSupply", `{"chainSupply":{"monitored":true,"chainValue":1.5,"chainValueZat":150000000},"valuePools":[{"id":"transparent","monitored":true,"chainValueZat":1}]}`, "", "1.5", 0},
		// valuePools with the transparent pool: summed, no UTXO scan
		{"transparent pool", `{"valuePools":[{"id":"transparent","monitored":true,"chainValueZat":100000000},{"id":"sapling","monitored":true,"chainValueZat":50000000}]}`, "", "1.5", 0},
		// an unmonitored chainSupply falls back to the pools
		{"chainSupply unmonitored", `{"chainSupply":{"monitored":false,"chainValueZat":0},"valuePools":[{"id":"transparent","monitored":true,"chainValueZat":200000000}]}`, "", "2", 0},
		// Bitcoin, Firo, ...: no valuePools, the UTXO set total alone
		{"no pools", `{"chain":"main","blocks":10}`, `{"total_amount":19876543.12345678}`, "19876543.12345678", 1},
		{"integer total", `{"chain":"main"}`, `{"total_amount":21}`, "21", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rpc := &insightSupplyRPC{info: tc.info, utxo: tc.utxo}
			s := insightSupplySetup(t, rpc)
			w := insightRequest(s, "GET", "/api/statistics/circulating-supply", "")
			if w.Code != 200 || w.Body.String() != tc.want {
				t.Fatalf("%d %q, want %q", w.Code, w.Body.String(), tc.want)
			}
			if rpc.calls["gettxoutsetinfo"] != tc.utxoCalls {
				t.Fatalf("gettxoutsetinfo calls %d, want %d", rpc.calls["gettxoutsetinfo"], tc.utxoCalls)
			}
		})
	}
}

func TestInsightCirculatingSupplyErrors(t *testing.T) {
	// no daemon RPC
	s := insightTestServer(t, insightFixture())
	if w := insightRequest(s, "GET", "/api/statistics/circulating-supply", ""); w.Code != 404 || !strings.Contains(w.Body.String(), `"error":"Route not found"`) {
		t.Fatalf("no rpc: %d %s", w.Code, w.Body.String())
	}
	// a shielded pool the daemon does not track makes the total unknown
	rpc := &insightSupplyRPC{info: `{"valuePools":[{"id":"sprout","monitored":false,"chainValueZat":0}]}`, utxo: insightOldZcashdUtxo}
	s = insightSupplySetup(t, rpc)
	if w := insightRequest(s, "GET", "/api/statistics/circulating-supply", ""); w.Code != 501 {
		t.Fatalf("unmonitored: %d %s", w.Code, w.Body.String())
	}
	// a daemon without gettxoutsetinfo
	rpc = &insightSupplyRPC{info: `{}`, utxoError: &bchain.RPCError{Code: insightRPCMethodNotFound, Message: "Method not found"}}
	s = insightSupplySetup(t, rpc)
	if w := insightRequest(s, "GET", "/api/statistics/circulating-supply", ""); w.Code != 501 {
		t.Fatalf("method not found: %d %s", w.Code, w.Body.String())
	}
	// any other failure is the generic 500 without daemon text, and is not
	// retried for insightSupplyErrTTL (a timed-out scan keeps running)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	old := insightNow
	insightNow = func() time.Time { return now }
	defer func() { insightNow = old }()
	rpc = &insightSupplyRPC{info: `{}`, utxoError: &bchain.RPCError{Code: -1, Message: "secret daemon text"}}
	s = insightSupplySetup(t, rpc)
	for i := 0; i < 3; i++ {
		w := insightRequest(s, "GET", "/api/statistics/circulating-supply", "")
		if w.Code != 500 || strings.Contains(w.Body.String(), "secret") {
			t.Fatalf("failure: %d %s", w.Code, w.Body.String())
		}
		now = now.Add(time.Minute)
	}
	if rpc.calls["gettxoutsetinfo"] != 1 {
		t.Fatalf("failed scan retried within the error TTL: %v", rpc.calls)
	}
	now = now.Add(insightSupplyErrTTL)
	rpc.utxoError, rpc.utxo = nil, `{"total_amount":1}`
	if w := insightRequest(s, "GET", "/api/statistics/circulating-supply", ""); w.Code != 200 || w.Body.String() != "1" {
		t.Fatalf("after error TTL: %d %s", w.Code, w.Body.String())
	}
	// malformed amounts are errors, never a wrong number
	for _, utxo := range []string{`{"total_amount":1.000000001}`, `{"total_amount":-1}`, `{"total_amount":1e3}`, `{}`} {
		rpc = &insightSupplyRPC{info: `{}`, utxo: utxo}
		s = insightSupplySetup(t, rpc)
		if w := insightRequest(s, "GET", "/api/statistics/circulating-supply", ""); w.Code != 500 {
			t.Fatalf("%s: %d %s", utxo, w.Code, w.Body.String())
		}
	}
}

func TestInsightCoinsToSat(t *testing.T) {
	for in, want := range map[string]string{"0": "0", "1": "100000000", "0.00000001": "1", "21000000.00000000": "2100000000000000", "1.10": "110000000", "1.": "100000000"} {
		got, err := insightCoinsToSat(in)
		if err != nil || got.String() != want {
			t.Errorf("%q: %v %v, want %s", in, got, err, want)
		}
	}
	for _, in := range []string{"", ".5", "-1", "1e8", "1.000000001", "0x10", "1.2.3", " 1"} {
		if _, err := insightCoinsToSat(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestInsightOtherStatisticsUnsupported(t *testing.T) {
	rpc := &insightSupplyRPC{info: insightOldZcashdInfo, utxo: insightOldZcashdUtxo}
	s := insightSupplySetup(t, rpc)
	for _, path := range []string{"/api/statistics/total-supply", "/api/statistics/supply", "/api/statistics/circulating-supply/x", "/api/statistics", "/api/markets/info", "/api/supply"} {
		if w := insightRequest(s, "GET", path, ""); w.Code != 404 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	if len(rpc.calls) != 0 {
		t.Fatalf("unsupported routes called the daemon: %v", rpc.calls)
	}
}

func TestInsightRepeatedSlashes(t *testing.T) {
	for _, tc := range []struct {
		path, kind, arg string
		addresses       []string
	}{
		{"/api//addr/A", "summary", "", []string{"A"}},
		{"/api//addr/A/", "summary", "", []string{"A"}},
		{"/api///addr/A", "summary", "", []string{"A"}},
		{"/insight-api//addr/A/utxo", "utxo", "", []string{"A"}},
		{"/api//tx/t", "tx", "t", nil},
		{"/api//addr/A/balance", "addrvalue", "balance", []string{"A"}},
		{"/api//statistics/circulating-supply", "circulatingsupply", "", nil},
		{"/api//addrs/A,B/txs", "multitxs", "", []string{"A", "B"}},
		{"/api//status?q=getInfo", "status", "", nil},
	} {
		route, _, ok := insightMatch(httptest.NewRequest("GET", tc.path, nil))
		if !ok || route.kind != tc.kind || route.arg != tc.arg || strings.Join(route.addresses, "|") != strings.Join(tc.addresses, "|") {
			t.Fatalf("%s: %+v %v", tc.path, route, ok)
		}
	}
	// Only the empty segments right after the prefix go. An empty segment
	// elsewhere stays an empty argument (never shifts the next one into its
	// place), dot segments are ordinary text, and a doubled slash before the
	// prefix is not the prefix.
	for _, path := range []string{"/api//./status", "/api//../api/status", "/api/./addr/A", "//api/addr/A", "/x//api/status", "/api/statistics//circulating-supply", "/api/tx//t"} {
		if _, _, ok := insightMatch(httptest.NewRequest("GET", path, nil)); ok {
			t.Fatalf("%s matched", path)
		}
	}
	// /api//addr//balance is /api/addr//balance: no address (a 400 below)
	if route, _, ok := insightMatch(httptest.NewRequest("GET", "/api//addr//balance", nil)); !ok || route.kind != "addrvalue" || len(route.addresses) != 0 || route.arg != "balance" {
		t.Fatalf("empty address after the prefix: %+v %v", route, ok)
	}

	// end to end, and the Insight prefixes stay Insight-owned with -insightui
	s := insightTestServer(t, insightFixture())
	if err := s.SetFallback(insightUIStub); err != nil {
		t.Fatal(err)
	}
	w := insightRequest(s, "GET", "/api//addr/A", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"addrStr":"A"`) {
		t.Fatalf("double slash summary: %d %s", w.Code, w.Body.String())
	}
	for path, code := range map[string]int{"/api//addr//balance": 400, "/api/addr//balance": 400, "/api//nosuchroute": 404, "/insight-api//nosuchroute": 404} {
		w = insightRequest(s, "GET", path, "")
		if w.Code != code || w.Header().Get("X-Public") != "" || !strings.Contains(w.Body.String(), `"status":"error"`) {
			t.Fatalf("%s: %d %v %s", path, w.Code, w.Header(), w.Body.String())
		}
	}
	// Blockbook's versioned API after a doubled slash goes to the public
	// handler like /api/v2/, with the path as sent.
	w = insightRequest(s, "GET", "/api//v2/tx/t", "")
	if w.Header().Get("X-Public") != "1" || w.Body.String() != "public GET /api//v2/tx/t" {
		t.Fatalf("double slash v2: %d %v %s", w.Code, w.Header(), w.Body.String())
	}
}
