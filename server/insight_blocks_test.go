package server

import (
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trezor/blockbook/api"
	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/common"
	"github.com/trezor/blockbook/db"
)

func insightHashN(n int) string { return strings.Repeat(string(rune('a'+n)), 64) }

// insightChain6 is heights 0-5: three blocks on 2026-10-27, two on 10-28, one on 10-29.
func insightChain6() []db.BlockInfo {
	times := []time.Time{
		time.Date(2026, 10, 27, 0, 0, 10, 0, time.UTC), time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 10, 27, 23, 59, 59, 0, time.UTC), time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC), time.Date(2026, 10, 29, 1, 0, 0, 0, time.UTC),
	}
	chain := make([]db.BlockInfo, len(times))
	for i, t := range times {
		chain[i] = db.BlockInfo{Hash: insightHashN(i), Time: t.Unix(), Txs: uint32(i + 1), Size: uint32(100 + i), Height: uint32(i)}
	}
	return chain
}

func insightGetJSON(t *testing.T, s *InsightServer, path string, code int) map[string]interface{} {
	t.Helper()
	w := insightRequest(s, "GET", path, "")
	if w.Code != code {
		t.Fatalf("%s: %d %s", path, w.Code, w.Body)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s: %v %s", path, err, w.Body)
	}
	return out
}

func insightRowHeights(t *testing.T, out map[string]interface{}) []int {
	t.Helper()
	heights := []int{}
	for _, row := range out["blocks"].([]interface{}) {
		heights = append(heights, int(row.(map[string]interface{})["height"].(float64)))
	}
	return heights
}

func TestInsightBlocksByDate(t *testing.T) {
	f := insightFixture()
	f.chain = insightChain6()
	s := insightTestServer(t, f)
	out := insightGetJSON(t, s, "/api/blocks?blockDate=2026-10-27", 200)
	if got := insightRowHeights(t, out); !reflect.DeepEqual(got, []int{2, 1, 0}) {
		t.Fatalf("heights %v", got)
	}
	p := out["pagination"].(map[string]interface{})
	if out["length"].(float64) != 3 || p["current"] != "2026-10-27" || p["prev"] != "2026-10-26" || p["next"] != "2026-10-28" || p["more"] != false || p["isToday"] != false {
		t.Fatalf("pagination %v", out)
	}
	if p["currentTs"].(float64) != float64(time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC).Unix()-1) {
		t.Fatalf("currentTs %v", p["currentTs"])
	}
	if _, ok := p["moreTs"]; ok {
		t.Fatal("moreTs without more")
	}
	row := out["blocks"].([]interface{})[0].(map[string]interface{})
	if row["hash"] != insightHashN(2) || row["txlength"].(float64) != 3 || row["size"].(float64) != 102 || row["isMainChain"] != true || !reflect.DeepEqual(row["poolInfo"], map[string]interface{}{}) {
		t.Fatalf("row %v", row)
	}
	if _, ok := row["difficulty"]; ok {
		t.Fatal("row difficulty is not indexed")
	}
	out = insightGetJSON(t, s, "/api/blocks?blockDate=2026-10-27&limit=2", 200)
	p = out["pagination"].(map[string]interface{})
	if got := insightRowHeights(t, out); !reflect.DeepEqual(got, []int{2, 1}) || p["more"] != true || p["moreTs"].(float64) != float64(f.chain[1].Time) {
		t.Fatalf("limited %v %v", got, p)
	}
	// Both prefixes, and no blockDate means today (UTC), which has no blocks here.
	old := insightNow
	insightNow = func() time.Time { return time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC) }
	defer func() { insightNow = old }()
	out = insightGetJSON(t, s, "/insight-api/blocks", 200)
	if out["pagination"].(map[string]interface{})["isToday"] != true || out["length"].(float64) != 0 {
		t.Fatalf("today %v", out)
	}
}

func TestInsightBlocksByDateEdges(t *testing.T) {
	f := insightFixture()
	f.chain = insightChain6()
	s := insightTestServer(t, f)
	for _, date := range []string{"2030-01-01", "2000-01-01"} {
		out := insightGetJSON(t, s, "/api/blocks?blockDate="+date, 200)
		if blocks, ok := out["blocks"].([]interface{}); !ok || len(blocks) != 0 || out["length"].(float64) != 0 {
			t.Fatalf("%s: %v", date, out)
		}
		if out["pagination"].(map[string]interface{})["current"] != date {
			t.Fatalf("%s: %v", date, out)
		}
	}
	for _, q := range []string{"blockDate=2026-13-01", "blockDate=x", "blockDate=", "limit=0", "limit=201", "limit=a", "limit=", "limit=-1", "limit=%2B5"} {
		insightGetJSON(t, s, "/api/blocks?"+q, 400)
	}
	insightGetJSON(t, s, "/api/blocks?limit=200", 200)
	f.blocksCalls = 0
	out := insightGetJSON(t, s, "/api/blocks?blockDate=2026-10-28", 200)
	if got := insightRowHeights(t, out); !reflect.DeepEqual(got, []int{4, 3}) {
		t.Fatalf("heights %v", got)
	}
	if f.blocksCalls == 0 || f.blocksCalls > 40 {
		t.Fatalf("GetBlocks calls: %d", f.blocksCalls)
	}
	// The newest day holds a block too.
	old := insightNow
	insightNow = func() time.Time { return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC) }
	defer func() { insightNow = old }()
	out = insightGetJSON(t, s, "/api/blocks", 200)
	if got := insightRowHeights(t, out); !reflect.DeepEqual(got, []int{5}) || out["pagination"].(map[string]interface{})["isToday"] != true {
		t.Fatalf("today %v", out)
	}
	// A large chain stays logarithmic.
	big := make([]db.BlockInfo, 1000000)
	for i := range big {
		big[i] = db.BlockInfo{Hash: insightHashN(0), Time: int64(i) * 600, Height: uint32(i)}
	}
	f.chain, f.blocksCalls = big, 0
	insightGetJSON(t, s, "/api/blocks?blockDate=1970-03-01&limit=5", 200)
	if f.blocksCalls > 60 {
		t.Fatalf("GetBlocks calls on a long chain: %d", f.blocksCalls)
	}
}

func insightBlockFixture(f *insightFake, hash string, txCount int) {
	txs := []*api.Tx{{Txid: "cb", Vout: []api.Vout{{ValueSat: insightAmount(625000000)}, {ValueSat: insightAmount(1000)}}}}
	fees := []int64{600, 400}
	for i := 1; i < txCount; i++ {
		txs = append(txs, &api.Tx{Txid: string(rune('a' + i)), FeesSat: insightAmount(fees[(i-1)%2])})
	}
	f.blockByHash = map[string]*api.Block{hash: {
		BlockInfo: api.BlockInfo{Hash: hash, Prev: insightHashN(1), Next: insightHashN(2), Height: 7, Confirmations: 3, Size: 999, Time: 1700000000,
			Version: common.JSONNumber("536870912"), MerkleRoot: "mr", Nonce: "12345", Bits: "1d00ffff", Difficulty: "2.5"},
		TxCount: txCount, Transactions: txs,
	}}
}

func TestInsightBlockShape(t *testing.T) {
	hash := insightHashN(3)
	f := insightFixture()
	insightBlockFixture(f, hash, 3)
	s := insightTestServer(t, f)
	var header []interface{}
	s.SetBackend(func(method string, params []interface{}, result interface{}) error {
		header = append([]interface{}{method}, params...)
		return json.Unmarshal([]byte(`{"chainwork":"00ff"}`), result)
	}, "BTC")
	w := insightRequest(s, "GET", "/api/block/"+strings.ToUpper(hash), "")
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	body := w.Body.String()
	for _, v := range []string{`"reward":6.25,`, `"tx":["cb","b","c"]`, `"isMainChain":true`, `"poolInfo":{}`, `"chainwork":"00ff"`, `"size":999`, `"height":7`, `"version":536870912`, `"merkleroot":"mr"`, `"nonce":12345`, `"bits":"1d00ffff"`, `"difficulty":2.5`, `"confirmations":3`, `"previousblockhash":"` + insightHashN(1), `"nextblockhash":"` + insightHashN(2), `"time":1700000000`} {
		if !strings.Contains(body, v) {
			t.Errorf("missing %s in %s", v, body)
		}
	}
	if !reflect.DeepEqual(header, []interface{}{"getblockheader", hash, true}) {
		t.Fatalf("rpc %v", header)
	}
	// No RPC: chainwork is absent. Tip and genesis omit the missing neighbor.
	s.SetBackend(nil, "BTC")
	f.blockByHash[hash].Next, f.blockByHash[hash].Prev = "", ""
	body = insightRequest(s, "GET", "/insight-api/block/"+hash, "").Body.String()
	for _, v := range []string{`chainwork`, `nextblockhash`, `previousblockhash`} {
		if strings.Contains(body, v) {
			t.Errorf("unexpected %s in %s", v, body)
		}
	}
	// A failing header RPC also drops chainwork only.
	s.SetBackend(func(string, []interface{}, interface{}) error { return errors.New("boom") }, "BTC")
	w = insightRequest(s, "GET", "/api/block/"+hash, "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "chainwork") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestInsightBlockPagesThroughAllTxs(t *testing.T) {
	old := insightBlockPageSize
	insightBlockPageSize = 2
	defer func() { insightBlockPageSize = old }()
	hash := insightHashN(3)
	f := insightFixture()
	insightBlockFixture(f, hash, 5)
	s := insightTestServer(t, f)
	out := insightGetJSON(t, s, "/api/block/"+hash, 200)
	txids := out["tx"].([]interface{})
	if len(txids) != 5 || txids[4] != "e" {
		t.Fatalf("tx %v", txids)
	}
	// coinbase 625001000 minus fees 600+400+600+400
	if out["reward"].(float64) != 6.249990 {
		t.Fatalf("reward %v", out["reward"])
	}
}

func TestInsightRawBlockFallback(t *testing.T) {
	hash := insightHashN(3)
	f := insightFixture()
	f.rawBlock = &api.BlockRaw{Hex: "aa"}
	s := insightTestServer(t, f)
	if w := insightRequest(s, "GET", "/api/rawblock/"+hash, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `{"rawblock":"aa"}`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	f.rawBlock, f.rawErr = nil, errors.New("unsupported")
	var seen []interface{}
	s.SetBackend(func(method string, params []interface{}, result interface{}) error {
		seen = append([]interface{}{method}, params...)
		return json.Unmarshal([]byte(`"00ff"`), result)
	}, "ZEC")
	w := insightRequest(s, "GET", "/api/rawblock/"+hash, "")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"rawblock":"00ff"}` {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if !reflect.DeepEqual(seen, []interface{}{"getblock", hash, 0}) {
		t.Fatalf("rpc %v", seen)
	}
	// Both fail: generic 500, nothing leaks.
	s.SetBackend(func(string, []interface{}, interface{}) error { return errors.New("secret daemon text") }, "ZEC")
	w = insightRequest(s, "GET", "/api/rawblock/"+hash, "")
	if w.Code != 500 || !strings.Contains(w.Body.String(), "Backend request failed") || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	s.SetBackend(nil, "ZEC")
	if w = insightRequest(s, "GET", "/api/rawblock/"+hash, ""); w.Code != 500 || strings.Contains(w.Body.String(), "unsupported") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	// An API error (unknown block) is a 404 and never reaches the daemon.
	called := false
	s.SetBackend(func(string, []interface{}, interface{}) error { called = true; return nil }, "ZEC")
	f.rawErr = api.NewAPIError("Block not found", true)
	if w = insightRequest(s, "GET", "/api/rawblock/"+hash, ""); w.Code != 404 || called {
		t.Fatalf("%d %s called=%v", w.Code, w.Body, called)
	}
	// The daemon's own block-not-found is a 404 too.
	f.rawErr = errors.New("unsupported")
	s.SetBackend(func(string, []interface{}, interface{}) error {
		return &bchain.RPCError{Code: -5, Message: "Block not found"}
	}, "ZEC")
	if w = insightRequest(s, "GET", "/api/rawblock/"+hash, ""); w.Code != 404 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w = insightRequest(s, "GET", "/api/rawblock/zz", ""); w.Code != 400 {
		t.Fatalf("%d", w.Code)
	}
}

func TestInsightBlockIndex(t *testing.T) {
	f := insightFixture()
	f.chain = insightChain6()
	s := insightTestServer(t, f)
	out := insightGetJSON(t, s, "/api/block-index/3", 200)
	if out["blockHash"] != insightHashN(3) {
		t.Fatalf("%v", out)
	}
	if out = insightGetJSON(t, s, "/insight-api/block-index/0", 200); out["blockHash"] != insightHashN(0) {
		t.Fatalf("%v", out)
	}
	if out = insightGetJSON(t, s, "/api/block-index/5", 200); out["blockHash"] != insightHashN(5) {
		t.Fatalf("%v", out)
	}
	for _, h := range []string{"6", "-1", "x", "1.5", "99999999999999", "%2B1"} {
		insightGetJSON(t, s, "/api/block-index/"+h, 404)
	}
}

func TestInsightBlockNotFound(t *testing.T) {
	f := insightFixture()
	s := insightTestServer(t, f)
	insightGetJSON(t, s, "/api/block/"+insightHashN(4), 404)
	for _, p := range []string{"zz", strings.Repeat("g", 64), insightHashN(4) + "0", "0"} {
		insightGetJSON(t, s, "/api/block/"+p, 400)
	}
	// The router matches an empty argument; the handler must reject it.
	insightGetJSON(t, s, "/api/block//", 400)
	insightGetJSON(t, s, "/api/rawblock//", 400)
	// Non-API worker failures are generic 500s.
	insightBlockFixture(f, insightHashN(4), 1)
	f.err = errors.New("db secret")
	if w := insightRequest(s, "GET", "/api/block/"+insightHashN(4), ""); w.Code != 500 || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestInsightBlocksStartTimestamp(t *testing.T) {
	f := insightFixture()
	f.chain = insightChain6()
	s := insightTestServer(t, f)
	// Page through the 27th two rows at a time with moreTs. The end is
	// inclusive, as in stock, so the last row of a page opens the next one.
	out := insightGetJSON(t, s, "/api/blocks?blockDate=2026-10-27&limit=2", 200)
	moreTs := int64(out["pagination"].(map[string]interface{})["moreTs"].(float64))
	out = insightGetJSON(t, s, "/api/blocks?blockDate=2026-10-27&limit=2&startTimestamp="+strconv.FormatInt(moreTs, 10), 200)
	p := out["pagination"].(map[string]interface{})
	if got := insightRowHeights(t, out); !reflect.DeepEqual(got, []int{1, 0}) || p["more"] != false {
		t.Fatalf("second page %v %v", got, p)
	}
	if p["currentTs"].(float64) != float64(moreTs-1) || p["current"] != "2026-10-27" || p["next"] != time.Unix(moreTs, 0).UTC().Format("2006-01-02") {
		t.Fatalf("pagination %v", p)
	}
	// An end before the day, or a block exactly at the end, behave inclusively.
	out = insightGetJSON(t, s, "/api/blocks?blockDate=2026-10-27&startTimestamp="+strconv.FormatInt(f.chain[0].Time-1, 10), 200)
	if out["length"].(float64) != 0 {
		t.Fatalf("%v", out)
	}
	out = insightGetJSON(t, s, "/api/blocks?blockDate=2026-10-27&startTimestamp="+strconv.FormatInt(f.chain[0].Time, 10), 200)
	if got := insightRowHeights(t, out); !reflect.DeepEqual(got, []int{0}) {
		t.Fatalf("%v", got)
	}
	for _, q := range []string{"startTimestamp=", "startTimestamp=0", "startTimestamp=-1", "startTimestamp=x", "startTimestamp=1.5"} {
		insightGetJSON(t, s, "/api/blocks?blockDate=2026-10-27&"+q, 400)
	}
}

func TestInsightBlocksDefaultLimit(t *testing.T) {
	f := insightFixture()
	n := 250
	f.chain = make([]db.BlockInfo, n)
	for i := range f.chain {
		f.chain[i] = db.BlockInfo{Hash: insightHashN(0), Time: time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC).Unix() + int64(i)*60, Height: uint32(i)}
	}
	s := insightTestServer(t, f)
	out := insightGetJSON(t, s, "/api/blocks?blockDate=2026-10-27", 200)
	if out["length"].(float64) != 200 || out["pagination"].(map[string]interface{})["more"] != true {
		t.Fatalf("default limit: %v", out["length"])
	}
}

// Block times are only loosely ordered. With two swapped times at a day
// boundary the search may place a boundary block on the wrong side, but the
// answer stays a contiguous run of heights within the documented 2 h of the
// window, and never an error.
func TestInsightBlocksNonMonotonicTimes(t *testing.T) {
	f := insightFixture()
	f.chain = insightChain6()
	f.chain[2].Time = time.Date(2026, 10, 28, 0, 0, 5, 0, time.UTC).Unix()
	f.chain[3].Time = time.Date(2026, 10, 27, 23, 59, 58, 0, time.UTC).Unix()
	s := insightTestServer(t, f)
	out := insightGetJSON(t, s, "/api/blocks?blockDate=2026-10-27", 200)
	heights := insightRowHeights(t, out)
	if len(heights) == 0 || len(heights) > 4 {
		t.Fatalf("heights %v", heights)
	}
	for i := 1; i < len(heights); i++ {
		if heights[i] != heights[i-1]-1 {
			t.Fatalf("not contiguous: %v", heights)
		}
	}
	start := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC).Unix()
	for _, row := range out["blocks"].([]interface{}) {
		at := int64(row.(map[string]interface{})["time"].(float64))
		if at < start-7200 || at > start+86400+7200 {
			t.Fatalf("row outside the tolerance: %v", row)
		}
	}
}

func TestInsightBlocksTipMovedAndEmptyIndex(t *testing.T) {
	f := insightFixture()
	f.chain = insightChain6()
	s := insightTestServer(t, f)
	// One stale read is retried and the answer is right.
	f.shiftReads = 1
	out := insightGetJSON(t, s, "/api/blocks?blockDate=2026-10-27", 200)
	if got := insightRowHeights(t, out); !reflect.DeepEqual(got, []int{2, 1, 0}) {
		t.Fatalf("after retry: %v", got)
	}
	f.shiftReads = 1
	if out = insightGetJSON(t, s, "/api/block-index/3", 200); out["blockHash"] != insightHashN(3) {
		t.Fatalf("after retry: %v", out)
	}
	// A tip that never settles is a 503, for both routes.
	f.shiftReads = 1000
	insightGetJSON(t, s, "/api/blocks?blockDate=2026-10-27", 503)
	insightGetJSON(t, s, "/api/block-index/3", 503)
	// No index at all is a 503, for both routes.
	f.shiftReads = 0
	f.chain = []db.BlockInfo{}
	insightGetJSON(t, s, "/api/blocks?blockDate=2026-10-27", 503)
	insightGetJSON(t, s, "/api/block-index/0", 503)
}

func TestInsightBlockMainChainAndCache(t *testing.T) {
	old := insightBlockPageSize
	insightBlockPageSize = 2
	defer func() { insightBlockPageSize = old }()
	hash := insightHashN(3)
	f := insightFixture()
	insightBlockFixture(f, hash, 5)
	f.blockByHash[hash].Confirmations = -1
	s := insightTestServer(t, f)
	out := insightGetJSON(t, s, "/api/block/"+hash, 200)
	if out["isMainChain"] != false || out["confirmations"].(float64) != -1 {
		t.Fatalf("stale block: %v", out)
	}
	// 5 txs at 2 per page is 3 pages on the first request; a repeat reads one
	// header row only and still reports fresh confirmations.
	if f.blockCalls != 3 {
		t.Fatalf("first request GetBlock calls: %d", f.blockCalls)
	}
	f.blockByHash[hash].Confirmations = 4
	again := insightGetJSON(t, s, "/api/block/"+hash, 200)
	if f.blockCalls != 4 || again["isMainChain"] != true || again["confirmations"].(float64) != 4 || !reflect.DeepEqual(again["tx"], out["tx"]) || again["reward"] != out["reward"] {
		t.Fatalf("cached block: calls=%d %v", f.blockCalls, again)
	}
}

func TestInsightBlockCacheByteBound(t *testing.T) {
	data := func(n int) *insightBlockData { return &insightBlockData{Txids: make([]string, n)} }
	c := newInsightBlockCache()
	c.maxCost = insightBlockCost(data(10))*2 + 1
	c.put("a", data(10))
	c.put("b", data(10))
	if c.get("a") == nil || c.get("b") == nil {
		t.Fatal("two blocks within the cap were evicted")
	}
	c.put("c", data(10)) // pushes the total over the cap: the least recent (a, then b was touched later) goes
	if c.get("a") != nil || c.get("b") == nil || c.get("c") == nil {
		t.Fatalf("eviction order wrong: a=%v b=%v c=%v", c.get("a") != nil, c.get("b") != nil, c.get("c") != nil)
	}
	c.put("huge", data(1000)) // above the cap by itself: not cached, nothing evicted
	if c.get("huge") != nil || c.get("b") == nil || c.get("c") == nil {
		t.Fatal("an oversized block was cached or evicted the others")
	}
	c.put("b", data(5)) // replacing an entry re-accounts its cost
	if want := insightBlockCost(data(5)) + insightBlockCost(data(10)); c.bytes != want {
		t.Fatalf("bytes %d, want %d", c.bytes, want)
	}
}

// A missing path argument is an unknown route (404), an explicit empty one is
// a rejected request (400), and every route answers in the error shape.
// addr/ is 400 because ?address= is its other, valid form; block-index is 404
// for any bad height, empty included, as stock answers.
func TestInsightEmptyArgRoutes(t *testing.T) {
	s := insightTestServer(t, insightFixture())
	for _, tc := range []struct {
		path string
		code int
	}{
		{"/api/tx/", 404}, {"/api/rawtx/", 404}, {"/api/block/", 404}, {"/api/rawblock/", 404}, {"/api/block-index/", 404}, {"/api/block-index//", 404}, {"/api/addrs/", 404},
		{"/api/tx//", 400}, {"/api/rawtx//", 400}, {"/api/block//", 400}, {"/api/rawblock//", 400},
		{"/api/addr/", 400}, {"/api/addr//", 400}, {"/api/addr//utxo", 400}, {"/api/addr//balance", 400},
		{"/api/addrs//utxo", 400}, {"/api/addrs//balance", 400}, {"/api/addrs//txs", 400},
	} {
		w := insightRequest(s, "GET", tc.path, "")
		if w.Code != tc.code || !strings.Contains(w.Body.String(), `"status":"error"`) {
			t.Errorf("%s: %d %s", tc.path, w.Code, w.Body)
		}
	}
}
