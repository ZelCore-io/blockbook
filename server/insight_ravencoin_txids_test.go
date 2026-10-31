package server

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trezor/blockbook/api"
)

const insightRvnA, insightRvnB = "RPwpAwECnjm2FZVduEE8M5VjWc2sSHs6Lo", "RU67hqkZMRNqWp5Sa4HcKnKUQyC77PKBB4"

func insightRvnHistoryIds(t *testing.T, s *InsightServer, path string) (int, []string) {
	t.Helper()
	w := insightRequest(s, "GET", path, "")
	var got struct {
		TotalItems int `json:"totalItems"`
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
	return got.TotalItems, out
}

func insightRvnTxidSetup(t *testing.T, ids ...string) (*InsightServer, *insightRvnRPC, *insightRavencoin, *insightFake) {
	t.Helper()
	f := insightFixture()
	for _, id := range ids {
		f.txs[id] = &api.Tx{Txid: id}
	}
	s, rpc := insightRvnSetup(t, f)
	return s, rpc, s.ext.(*insightRavencoin), f
}

// Paging the same history on one tip reads ravend's list once, for every
// history route and either address order; the mempool is read every time;
// a new tip reads the list again.
func TestInsightRavencoinTxidsCachedPerTip(t *testing.T) {
	a, b := insightRvnA, insightRvnB
	s, rpc, x, _ := insightRvnTxidSetup(t, "t1", "t2", "t3", "t4", "u1", "m1")
	tip := "h1"
	var tipMu sync.Mutex
	x.tipHash = func() (string, bool) {
		tipMu.Lock()
		defer tipMu.Unlock()
		return tip, true
	}
	rpc.txids[a] = []string{"t1", "t2", "t3"}
	rpc.txids[b] = []string{"u1"}

	for _, path := range []string{"/api/addrs/" + a + "/txs", "/api/addrs/" + a + "/txs/1/3", "/api/addrs/txs?address=" + a, "/api/addrs/" + a + "/txs?from=2&to=3"} {
		insightRvnHistoryIds(t, s, path)
	}
	if w := insightRequest(s, "GET", "/api/txs?address="+a, ""); w.Code != 200 {
		t.Fatalf("txs?address=: %d", w.Code)
	}
	if n := rpc.count("getaddresstxids"); n != 1 {
		t.Fatalf("single address: %d getaddresstxids calls", n)
	}
	if n := rpc.count("getaddressmempool"); n != 5 {
		t.Fatalf("mempool reads %d", n)
	}
	if total, got := insightRvnHistoryIds(t, s, "/api/addrs/"+a+","+b+"/txs?from=0&to=10"); total != 4 || !reflect.DeepEqual(got, []string{"u1", "t3", "t2", "t1"}) {
		t.Fatalf("multi: %d %v", total, got)
	}
	insightRvnHistoryIds(t, s, "/api/addrs/"+b+","+a+"/txs?from=1&to=3")
	if n := rpc.count("getaddresstxids"); n != 2 {
		t.Fatalf("multi address: %d getaddresstxids calls", n)
	}

	// a mempool change on the same tip is seen without a new list read
	rpc.mempool = []insightRvnDelta{{Txid: "m1"}, {Txid: "t3"}}
	if total, got := insightRvnHistoryIds(t, s, "/api/addrs/"+a+"/txs"); total != 4 || !reflect.DeepEqual(got, []string{"m1", "t3", "t2", "t1"}) || rpc.count("getaddresstxids") != 2 {
		t.Fatalf("mempool: %d %v %d", total, got, rpc.count("getaddresstxids"))
	}

	// a new tip reads the list again
	rpc.mempool = nil
	rpc.txids[a] = []string{"t1", "t2", "t3", "t4"}
	if _, got := insightRvnHistoryIds(t, s, "/api/addrs/"+a+"/txs"); !reflect.DeepEqual(got, []string{"t3", "t2", "t1"}) {
		t.Fatalf("same tip served the cached list: %v", got)
	}
	tipMu.Lock()
	tip = "h2"
	tipMu.Unlock()
	if total, got := insightRvnHistoryIds(t, s, "/api/addrs/"+a+"/txs"); total != 4 || !reflect.DeepEqual(got, []string{"t4", "t3", "t2", "t1"}) || rpc.count("getaddresstxids") != 3 {
		t.Fatalf("new tip: %d %v %d", total, got, rpc.count("getaddresstxids"))
	}
	if x.txids.lru.Len() != 1 {
		t.Fatalf("old tip entries kept: %d", x.txids.lru.Len())
	}

	// without a tip (empty index) every request reads the list
	x.tipHash = func() (string, bool) { return "", false }
	insightRvnHistoryIds(t, s, "/api/addrs/"+a+"/txs")
	insightRvnHistoryIds(t, s, "/api/addrs/"+a+"/txs")
	if n := rpc.count("getaddresstxids"); n != 5 {
		t.Fatalf("no tip: %d getaddresstxids calls", n)
	}
}

// The default tip is Blockbook's best block hash.
func TestInsightRavencoinTxidsTipHash(t *testing.T) {
	_, _, x, f := insightRvnTxidSetup(t)
	if tip, ok := x.tipHash(); !ok || tip != "tip" {
		t.Fatalf("tip %q %v", tip, ok)
	}
	f.blocks = &api.Blocks{}
	if _, ok := x.tipHash(); ok {
		t.Fatal("empty index has a tip")
	}
}

// Concurrent requests for one address set share one getaddresstxids call.
func TestInsightRavencoinTxidsSharedCall(t *testing.T) {
	a := insightRvnA
	_, rpc, x, _ := insightRvnTxidSetup(t)
	x.tipHash = func() (string, bool) { return "h1", true }
	rpc.txids[a] = []string{"t1", "t2"}
	rpc.txidsEntered, rpc.txidsGate = make(chan struct{}, 16), make(chan struct{})
	const n = 8
	joined := make(chan struct{}, n)
	x.txids.joined = func() { joined <- struct{}{} }

	results := make([][]string, n)
	var wg sync.WaitGroup
	run := func(i int) {
		defer wg.Done()
		ids, _, err := x.addressTxids(context.Background(), []string{a})
		if err != nil {
			t.Error(err)
		}
		results[i] = ids
	}
	wg.Add(1)
	go run(0)
	<-rpc.txidsEntered
	<-joined
	for i := 1; i < n; i++ {
		wg.Add(1)
		go run(i)
	}
	for i := 1; i < n; i++ { // every caller is attached to the held call
		<-joined
	}
	close(rpc.txidsGate)
	wg.Wait()
	if c := rpc.count("getaddresstxids"); c != 1 {
		t.Fatalf("%d getaddresstxids calls", c)
	}
	for i, ids := range results {
		if !reflect.DeepEqual(ids, []string{"t2", "t1"}) {
			t.Fatalf("result %d: %v", i, ids)
		}
	}
}

// Beyond the getaddresstxids slots a request waits briefly, then gets the
// listener's 503; a waiter whose client leaves stops waiting.
func TestInsightRavencoinTxidsConcurrencyCap(t *testing.T) {
	a, b := insightRvnA, insightRvnB
	s, rpc, x, _ := insightRvnTxidSetup(t, "t1", "u1")
	x.tipHash = func() (string, bool) { return "h1", true }
	x.txids = newInsightRvnTxidCache(insightRvnTxidsMaxEntries, insightRvnTxidsMaxIds, 1, 50*time.Millisecond)
	rpc.txids[a] = []string{"t1"}
	rpc.txids[b] = []string{"u1"}
	rpc.txidsEntered, rpc.txidsGate = make(chan struct{}, 16), make(chan struct{})

	done := make(chan []string)
	go func() {
		ids, _, err := x.addressTxids(context.Background(), []string{a})
		if err != nil {
			t.Error(err)
		}
		done <- ids
	}()
	<-rpc.txidsEntered // a holds the only slot

	start := time.Now()
	w := insightRequest(s, "GET", "/api/addrs/"+b+"/txs", "")
	if w.Code != http.StatusServiceUnavailable || strings.TrimSpace(w.Body.String()) != `{"status":"error","error":"Service unavailable"}` {
		t.Fatalf("over the cap: %d %s", w.Code, w.Body.String())
	}
	if d := time.Since(start); d < 50*time.Millisecond || d > 2*time.Second {
		t.Fatalf("waited %v", d)
	}

	// a request for a's key waits on a's call and leaves with its client
	ctx, cancel := context.WithCancel(context.Background())
	gone := make(chan int)
	go func() {
		_, code, _ := x.addressTxids(ctx, []string{a})
		gone <- code
	}()
	cancel()
	if code := <-gone; code != insightClientGoneCode {
		t.Fatalf("client gone: %d", code)
	}

	close(rpc.txidsGate)
	if ids := <-done; !reflect.DeepEqual(ids, []string{"t1"}) {
		t.Fatalf("holder: %v", ids)
	}
	// the slot is free again
	if _, got := insightRvnHistoryIds(t, s, "/api/addrs/"+b+"/txs"); !reflect.DeepEqual(got, []string{"u1"}) {
		t.Fatalf("after release: %v", got)
	}
}

// The cache is an LRU bounded by entries and by total txids; a list over
// the txid bound is not kept, and a new tip empties it.
func TestInsightRavencoinTxidsCacheBound(t *testing.T) {
	c := newInsightRvnTxidCache(3, 5, 1, 0)
	ids := func(n int) []string { return make([]string, n) }
	keys := func() []string {
		var out []string
		for el := c.lru.Front(); el != nil; el = el.Next() {
			out = append(out, el.Value.(*insightRvnTipEntry[[]string]).key)
		}
		return out
	}
	c.get("h", "") // the cache's tip
	c.put("h", "k1", ids(2))
	c.put("h", "k2", ids(2))
	if _, ok := c.get("h", "k1"); !ok {
		t.Fatal("k1 missing")
	}
	c.put("h", "k3", ids(2)) // 6 txids > 5: the least recently used (k2) goes
	if got := keys(); !reflect.DeepEqual(got, []string{"k3", "k1"}) || c.size != 4 {
		t.Fatalf("txid bound: %v %d", got, c.size)
	}
	c.put("h", "big", ids(6)) // over the bound on its own: not kept
	if got := keys(); !reflect.DeepEqual(got, []string{"k3", "k1"}) {
		t.Fatalf("oversized list: %v", got)
	}
	c.put("h", "k4", ids(0))
	c.put("h", "k5", ids(0)) // 4 entries > 3: k1 goes
	if got := keys(); !reflect.DeepEqual(got, []string{"k5", "k4", "k3"}) || len(c.entries) != 3 {
		t.Fatalf("entry bound: %v", got)
	}
	c.put("h", "k3", ids(1)) // replacing an entry recounts it
	if c.size != 1 || c.lru.Len() != 3 {
		t.Fatalf("replace: %d %d", c.size, c.lru.Len())
	}
	if _, ok := c.get("h2", "k3"); ok || c.lru.Len() != 0 || c.size != 0 {
		t.Fatalf("new tip kept %d entries", c.lru.Len())
	}
}

// A call that read the previous tip and ends after the tip moved does not
// wipe the new tip's entries: put drops its list, only get moves the tip.
func TestInsightRavencoinTxidsLatePut(t *testing.T) {
	c := newInsightRvnTxidCache(8, 100, 1, 0)
	c.get("h1", "k")
	c.put("h1", "k", []string{"a"})
	if _, ok := c.get("h2", "k"); ok { // a request on the new tip
		t.Fatal("old tip served")
	}
	c.put("h2", "k2", []string{"b"})
	c.put("h1", "k3", []string{"c"}) // late put from the old tip
	if c.tip != "h2" || c.lru.Len() != 1 || c.size != 1 {
		t.Fatalf("late put: tip %s, %d entries, %d ids", c.tip, c.lru.Len(), c.size)
	}
	if ids, ok := c.get("h2", "k2"); !ok || !reflect.DeepEqual(ids, []string{"b"}) {
		t.Fatalf("new tip entry lost: %v %v", ids, ok)
	}
	if _, ok := c.get("h2", "k3"); ok {
		t.Fatal("old tip list kept")
	}
}

// The key ignores the address order and includes the tip.
func TestInsightRavencoinTxidsKey(t *testing.T) {
	if insightRvnTxidKey("h", []string{"b", "a"}) != insightRvnTxidKey("h", []string{"a", "b"}) {
		t.Fatal("order matters")
	}
	if insightRvnTxidKey("h", []string{"a"}) == insightRvnTxidKey("g", []string{"a"}) {
		t.Fatal("tip ignored")
	}
	in := []string{"b", "a"}
	insightRvnTxidKey("h", in)
	if in[0] != "b" {
		t.Fatal("input sorted in place")
	}
}
