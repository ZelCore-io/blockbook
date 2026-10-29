package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func j(t *testing.T, s string) interface{} {
	t.Helper()
	v, ok := DecodeJSON([]byte(s))
	if !ok {
		t.Fatalf("bad test JSON: %s", s)
	}
	return v
}

func kinds(fs []Finding) map[string]int {
	m := map[string]int{}
	for _, f := range fs {
		m[f.Kind]++
	}
	return m
}

func failing(fs []Finding) int {
	n := 0
	for _, f := range fs {
		if f.Failing() {
			n++
		}
	}
	return n
}

func TestCompareMissingConverterField(t *testing.T) {
	fs := Compare(j(t, `{"fees":0.1,"blockHeight":5}`), j(t, `{"fees":0.1}`), "converter", "tx/abc")
	if len(fs) != 1 || fs[0].Kind != KindMissing || fs[0].Pointer != "/blockHeight" {
		t.Fatalf("want one missing /blockHeight, got %+v", fs)
	}
}

func TestCompareExtraIsNotFailure(t *testing.T) {
	fs := Compare(j(t, `{"fees":0.1}`), j(t, `{"fees":0.1,"valueOut":0.1}`), "converter", "tx/abc")
	if len(fs) != 1 || fs[0].Kind != KindExtra || fs[0].Pointer != "/valueOut" {
		t.Fatalf("want one extra /valueOut, got %+v", fs)
	}
	if failing(fs) != 0 {
		t.Fatal("extra must not fail")
	}
}

func TestCompareIgnoresConfirmations(t *testing.T) {
	fs := Compare(j(t, `{"txid":"a","confirmations":5,"vin":[{"confirmations":1}]}`), j(t, `{"txid":"a","confirmations":9,"vin":[{"confirmations":2}]}`), "converter", "tx/a")
	if len(fs) != 0 {
		t.Fatalf("want no findings, got %+v", fs)
	}
}

func TestCompareHistoryByTxid(t *testing.T) {
	ref := j(t, `{"totalItems":3,"from":0,"to":3,"items":[{"txid":"a","blockheight":9},{"txid":"b","blockheight":8,"fees":1},{"txid":"c","blockheight":7}]}`)
	got := j(t, `{"totalItems":2,"from":0,"to":2,"items":[{"txid":"b","blockheight":8,"fees":1},{"txid":"c","blockheight":7}]}`)
	fs := Compare(ref, got, "converter", "addrs/X/txs?from=0&to=50")
	if k := kinds(fs); k[KindChanged] != 0 || k[KindMissing] != 0 {
		t.Fatalf("no failure expected, got %+v", fs)
	}
	var countNote bool
	for _, f := range fs {
		if f.Kind == KindExpected && f.Pointer == "/items" && strings.HasPrefix(f.Note, "E3") {
			countNote = true
		}
	}
	if !countNote {
		t.Fatalf("want an expected count note on /items, got %+v", fs)
	}
	// A real difference inside a shared txid still fails.
	got2 := j(t, `{"totalItems":2,"from":0,"to":2,"items":[{"txid":"b","blockheight":8,"fees":2},{"txid":"c","blockheight":7}]}`)
	fs = Compare(ref, got2, "converter", "addrs/X/txs?from=0&to=50")
	if failing(fs) != 1 || fs[0].Pointer == "" {
		t.Fatalf("want one failing difference, got %+v", fs)
	}
}

func TestCompareHistoryNoOverlapAndDeepPaging(t *testing.T) {
	ref := j(t, `{"items":[{"txid":"a"}]}`)
	got := j(t, `{"items":[{"txid":"z"}]}`)
	if failing(Compare(ref, got, "converter", "addrs/X/txs?from=0&to=50")) != 1 {
		t.Fatal("disjoint first page must fail")
	}
	fs := Compare(ref, got, "converter", "addrs/X/txs?from=1500&to=1600")
	if failing(fs) != 0 || !strings.HasPrefix(fs[0].Note, "E8") {
		t.Fatalf("deep paging must be expected E8, got %+v", fs)
	}
}

func TestCompareHistoryOrder(t *testing.T) {
	ref := j(t, `{"items":[{"txid":"a","blockheight":9},{"txid":"b","blockheight":8}]}`)
	got := j(t, `{"items":[{"txid":"b","blockheight":8},{"txid":"a","blockheight":9}]}`)
	if failing(Compare(ref, got, "converter", "addrs/X/txs")) != 1 {
		t.Fatal("cross-block order swap must fail")
	}
	ref = j(t, `{"items":[{"txid":"a","blockheight":9},{"txid":"b","blockheight":9}]}`)
	got = j(t, `{"items":[{"txid":"b","blockheight":9},{"txid":"a","blockheight":9}]}`)
	if failing(Compare(ref, got, "converter", "addrs/X/txs")) != 0 {
		t.Fatal("same-block order swap must not fail")
	}
}

func TestCompareHistoryTotals(t *testing.T) {
	fs := Compare(j(t, `{"totalItems":10,"to":10}`), j(t, `{"totalItems":20,"to":20}`), "converter", "addrs/X/txs")
	if failing(fs) != 0 || kinds(fs)[KindExpected] != 2 {
		t.Fatalf("want two expected, got %+v", fs)
	}
	fs = Compare(j(t, `{"totalItems":100}`), j(t, `{"totalItems":40}`), "converter", "addrs/X/txs")
	if failing(fs) != 1 {
		t.Fatalf("smaller totalItems must fail, got %+v", fs)
	}
}

func TestCompareAsmExpected(t *testing.T) {
	fs := Compare(j(t, `{"asm":"t1abc"}`), j(t, `{"asm":"OP_DUP OP_HASH160 aa OP_EQUALVERIFY OP_CHECKSIG"}`), "converter", "tx/a")
	if len(fs) != 1 || fs[0].Kind != KindExpected {
		t.Fatalf("want expected, got %+v", fs)
	}
	fs = Compare(j(t, `{"asm":"OP_RETURN 6869"}`), j(t, `{"asm":"OP_RETURN 6870"}`), "converter", "tx/a")
	if len(fs) != 1 || fs[0].Kind != KindChanged {
		t.Fatalf("want changed, got %+v", fs)
	}
}

func TestCompareStatusCode(t *testing.T) {
	ok := &Response{Status: 200, JSON: j(t, `{"a":1}`), IsJSON: true}
	bad := &Response{Status: 500, JSON: j(t, `{"status":"error"}`), IsJSON: true}
	fs := CompareResponse(ok, bad, "converter", "tx/a", Options{})
	if len(fs) != 1 || fs[0].Kind != KindChanged {
		t.Fatalf("want changed, got %+v", fs)
	}
	// Error bodies compare by status code and status key only.
	a := &Response{Status: 400, JSON: j(t, `{"status":"error","error":"x"}`), IsJSON: true}
	b := &Response{Status: 400, JSON: j(t, `{"status":"error","error":"different text"}`), IsJSON: true}
	if fs := CompareResponse(a, b, "converter", "tx/a", Options{}); len(fs) != 0 {
		t.Fatalf("same status and key must match, got %+v", fs)
	}
	// Non-JSON against JSON.
	html := &Response{Status: 200, Body: []byte("<html>")}
	if fs := CompareResponse(ok, html, "converter", "tx/a", Options{}); failing(fs) != 1 {
		t.Fatalf("non-json must fail, got %+v", fs)
	}
	// 501 and routes absent on the converter are classified.
	nope := &Response{Status: 501, JSON: j(t, `{"status":"error"}`), IsJSON: true}
	if fs := CompareResponse(ok, nope, "converter", "messages/verify", Options{ZebraNew: true}); failing(fs) != 0 || fs[0].Kind != KindExpected {
		t.Fatalf("501 must be expected with -zebra-new, got %+v", fs)
	}
	if failing(CompareResponse(ok, nope, "converter", "messages/verify", Options{})) != 1 {
		t.Fatal("501 without -zebra-new must fail")
	}
	if failing(CompareResponse(ok, nope, "converter", "addr/X/balance", Options{ZebraNew: true})) != 1 {
		t.Fatal("501 outside messages/verify must fail even with -zebra-new")
	}
	absent := &Response{Status: 404, Body: []byte("Cannot GET")}
	if fs := CompareResponse(absent, ok, "converter", "currency", Options{RefLacks: true}); failing(fs) != 0 || fs[0].Kind != KindExtra {
		t.Fatalf("ref 404 vs listener 200 must be extra, got %+v", fs)
	}
}

func TestCompareTolerances(t *testing.T) {
	fs := Compare(j(t, `{"info":{"blocks":100}}`), j(t, `{"info":{"blocks":102}}`), "converter", "status")
	if len(fs) != 0 {
		t.Fatalf("blocks within 3 must match, got %+v", fs)
	}
	if failing(Compare(j(t, `{"info":{"blocks":100}}`), j(t, `{"info":{"blocks":105}}`), "converter", "status")) != 1 {
		t.Fatal("blocks beyond 3 must fail")
	}
	if failing(Compare(j(t, `{"height":100}`), j(t, `{"height":101}`), "converter", "block/abc")) != 1 {
		t.Fatal("block height outside sync/status stays exact")
	}
	if fs := Compare(j(t, `{"data":{"rate":10.5,"short":"ZEC"}}`), j(t, `{"data":{"rate":0,"short":"ZEC"}}`), "converter", "currency"); len(fs) != 0 {
		t.Fatalf("currency rate is presence only, got %+v", fs)
	}
	if fs := Compare(j(t, `{"data":{"short":"ZEC"}}`), j(t, `{"data":{"rate":0,"short":"ZEC"}}`), "converter", "currency"); kinds(fs)[KindExtra] != 1 {
		t.Fatalf("got %+v", fs)
	}
}

func TestCompareNumbers(t *testing.T) {
	// 8-decimal tokens equal the converter's shortest form, in both modes.
	if fs := Compare(j(t, `{"fees":0.1,"value":"100"}`), j(t, `{"fees":0.10000000,"value":"100"}`), "converter", "tx/a"); len(fs) != 0 {
		t.Fatalf("got %+v", fs)
	}
	// Numeric strings against numbers: only stock mode accepts them.
	r, g := j(t, `{"balance":"5"}`), j(t, `{"balance":5}`)
	if failing(Compare(r, g, "converter", "addr/a")) != 1 {
		t.Fatal("converter mode must flag type change")
	}
	if len(Compare(r, g, "stock", "addr/a")) != 0 {
		t.Fatal("stock mode must accept numeric strings")
	}
}

func TestCompareExpectedMissing(t *testing.T) {
	ref := j(t, `{"vin":[{"scriptPubKey":{"asm":"x y"},"vout":0,"sequence":0}],"minedBy":"p"}`)
	got := j(t, `{"vin":[{"scriptPubKey":{}}]}`)
	fs := Compare(ref, got, "converter", "tx/a")
	if failing(fs) != 0 || kinds(fs)[KindExpected] != 4 {
		t.Fatalf("want four expected (E2 E7 E7 E6), got %+v", fs)
	}
	fs = Compare(j(t, `{"balance":1,"immature":0}`), j(t, `{"balance":1}`), "converter", "addrs/a,b/balance")
	if failing(fs) != 0 || len(fs) != 1 {
		t.Fatalf("E5, got %+v", fs)
	}
	fs = Compare(j(t, `{"blocks":[{"hash":"h","height":1,"difficulty":2}]}`), j(t, `{"blocks":[{"hash":"h","height":1}]}`), "converter", "blocks?limit=1")
	if failing(fs) != 0 || len(fs) != 1 {
		t.Fatalf("E4, got %+v", fs)
	}
}

func TestCompareUtxoSet(t *testing.T) {
	ref := j(t, `[{"txid":"a","vout":0,"satoshis":5},{"txid":"b","vout":1,"satoshis":6}]`)
	got := j(t, `[{"txid":"b","vout":1,"satoshis":6},{"txid":"a","vout":0,"satoshis":5}]`)
	if fs := Compare(ref, got, "converter", "addr/x/utxo"); len(fs) != 0 {
		t.Fatalf("order must not matter, got %+v", fs)
	}
	got = j(t, `[{"txid":"a","vout":0,"satoshis":5}]`)
	if failing(Compare(ref, got, "converter", "addr/x/utxo")) != 1 {
		t.Fatal("missing utxo must fail")
	}
}

func TestEstimateFeeMinusOne(t *testing.T) {
	if failing(Compare(j(t, `{"2":0.0001}`), j(t, `{"2":-1}`), "stock", "utils/estimatefee?nbBlocks=2")) != 1 {
		t.Fatal("-1 against a real fee must fail without -zebra-new")
	}
	fs := compareWith(j(t, `{"2":0.0001}`), j(t, `{"2":-1}`), "stock", "utils/estimatefee?nbBlocks=2", Options{ZebraNew: true})
	if failing(fs) != 0 || len(fs) != 1 {
		t.Fatalf("got %+v", fs)
	}
	if failing(compareWith(j(t, `{"2":0.0001}`), j(t, `{"2":-1}`), "stock", "addr/a/balance", Options{ZebraNew: true})) != 1 {
		t.Fatal("-1 exception is estimatefee only")
	}
}

func TestSafetyChecks(t *testing.T) {
	specs, err := loadPaths(strings.NewReader("# c\nGET status\n\nPOST tx/send {\"rawtx\":\"zz\"}\nPOST tx/send @oversize:9437184\nPOST addrs/utxo {\"addrs\":\"a\"}\n"))
	if err != nil || len(specs) != 4 {
		t.Fatalf("load: %v %d", err, len(specs))
	}
	if err := validateSpecs(specs); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		`POST tx/send {"rawtx":"0100000001abcdef"}`,
		`POST tx/send rawtx=0100`,
		`POST block/abc {}`,
		`POST tx/send @oversize:5`,
	} {
		s, _ := loadPaths(strings.NewReader(line))
		if err := validateSpecs(s); err == nil {
			t.Errorf("must refuse %q", line)
		}
	}
	if _, err := loadPaths(strings.NewReader("DELETE x")); err == nil {
		t.Error("DELETE must be refused")
	}
	body, _ := expandBody("@oversize:9437184")
	if len(body) != 9437184 || unsafeBroadcast(body) != "" {
		t.Errorf("oversize body: len %d", len(body))
	}
}

func TestCORSFindings(t *testing.T) {
	with := &Response{Status: 204, Header: http.Header{"Access-Control-Allow-Origin": {"*"}}}
	without := &Response{Status: 204, Header: http.Header{}}
	if fs := corsFindings("p", with, with); len(fs) != 0 {
		t.Fatal(fs)
	}
	if fs := corsFindings("p", with, without); len(fs) != 1 || fs[0].Kind != KindMissing {
		t.Fatal(fs)
	}
	if fs := corsFindings("p", without, with); len(fs) != 1 || fs[0].Kind != KindExtra {
		t.Fatal(fs)
	}
}

func TestPercentile(t *testing.T) {
	s := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if percentile(s, 0.5) != 5 || percentile(s, 0.95) != 10 || percentile(nil, 0.5) != 0 {
		t.Fatal("percentile")
	}
}

// --- seed ---

const (
	addrCB   = "t1CoinbaseAddress000000001"
	addrOP   = "t1OpReturnAddress00000002"
	addrBusy = "t1BusyAddressManyTxs000003"
	addrOne  = "t1SingleTxAddress00000004"
	addrPend = "t1PendingAddress0000000005"
)

func bbTxs(h int) []map[string]interface{} {
	id := func(p string) string { return fmt.Sprintf("%s%d", p, h) }
	txs := []map[string]interface{}{
		{"txid": id("cb"), "vin": []interface{}{map[string]interface{}{"coinbase": "03"}}, "vout": []interface{}{map[string]interface{}{"hex": "76a9", "addresses": []string{addrCB}}}},
		{"txid": id("op"), "vin": []interface{}{map[string]interface{}{"addresses": []string{addrOP}}}, "vout": []interface{}{map[string]interface{}{"hex": "6a0568656c6c6f", "addresses": []string{"OP_RETURN (hello)"}}, map[string]interface{}{"addresses": []string{addrOP}}}},
		{"txid": id("p"), "vin": []interface{}{map[string]interface{}{"addresses": []string{addrBusy}}}, "vout": []interface{}{map[string]interface{}{"addresses": []string{addrBusy}}}},
	}
	if h == 101 {
		txs = append(txs, map[string]interface{}{"txid": "one", "vin": []interface{}{map[string]interface{}{"addresses": []string{addrBusy}}}, "vout": []interface{}{map[string]interface{}{"addresses": []string{addrOne}}}})
		for i := 0; i < 10; i++ {
			txs = append(txs, map[string]interface{}{"txid": fmt.Sprintf("f%d", i), "vin": []interface{}{map[string]interface{}{"addresses": []string{addrBusy}}}, "vout": []interface{}{map[string]interface{}{"addresses": []string{addrBusy}}}})
		}
	}
	return txs
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, mustMarshal(v))
}

func newBlockbookStub() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]interface{}{"blockbook": map[string]interface{}{"bestHeight": 102}})
	})
	mux.HandleFunc("/api/v2/block/", func(w http.ResponseWriter, r *http.Request) {
		var h int
		fmt.Sscanf(strings.TrimPrefix(r.URL.Path, "/api/v2/block/"), "%d", &h)
		if h < 100 || h > 102 {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, map[string]interface{}{"hash": fmt.Sprintf("hash%d", h), "height": h, "time": 1761696000 + h, "txs": bbTxs(h)})
	})
	mux.HandleFunc("/api/v2/address/", func(w http.ResponseWriter, r *http.Request) {
		n := 5
		switch strings.TrimPrefix(r.URL.Path, "/api/v2/address/") {
		case addrBusy:
			n = 1500
		case addrOne:
			n = 1
		}
		writeJSON(w, map[string]interface{}{"txs": n})
	})
	mux.HandleFunc("/api/v2/mempool", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]interface{}{"mempoolTxids": []string{"mp1"}})
	})
	mux.HandleFunc("/api/v2/tx/mp1", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]interface{}{"txid": "mp1", "vin": []interface{}{map[string]interface{}{"addresses": []string{addrPend}}}, "vout": []interface{}{map[string]interface{}{"addresses": []string{addrOne}}}})
	})
	return httptest.NewServer(mux)
}

var routeFamilies = map[string]string{
	"status": `^GET status$`, "status q": `^GET status\?q=getInfo$`, "sync": `^GET sync$`, "peer": `^GET peer$`, "version": `^GET version$`,
	"blocks": `^GET blocks\?limit=5$`, "blocks by date": `^GET blocks\?blockDate=\d{4}-\d\d-\d\d&limit=`,
	"block": `^GET block/hash101$`, "rawblock": `^GET rawblock/hash101$`, "block-index": `^GET block-index/102$`,
	"tx": `^GET tx/cb102$`, "rawtx": `^GET rawtx/op102$`, "txs block": `^GET txs\?block=hash101&pageNum=1$`, "txs address": `^GET txs\?address=` + addrBusy + `&pageNum=0$`,
	"tx/send": `^POST tx/send \{"rawtx":"zz"\}$`, "tx/send oversize": `^POST tx/send @oversize:`,
	"addr": `^GET addr/` + addrCB + `$`, "addr noTxList": `^GET addr/[^/?]+\?noTxList=1$`,
	"addr balance": `^GET addr/[^/]+/balance$`, "addr totalReceived": `^GET addr/[^/]+/totalReceived$`, "addr totalSent": `^GET addr/[^/]+/totalSent$`,
	"addr unconfirmedBalance": `^GET addr/[^/]+/unconfirmedBalance$`, "addr utxo": `^GET addr/[^/]+/utxo$`,
	"addrs utxo": `^GET addrs/[^/]+/utxo$`, "addrs unspent": `^GET addrs/[^/]+/unspent$`, "addrs balance": `^GET addrs/[^/]+/balance$`,
	"addrs txs": `^GET addrs/[^/]+/txs\?from=0&to=50$`, "addrs multi txs": `^GET addrs/[^/]+,[^/]+/txs\?from=0&to=50$`,
	"POST addrs/utxo": `^POST addrs/utxo \{"addrs":"[^"]+,[^"]+"\}$`, "POST addrs/txs": `^POST addrs/txs \{"addrs":"[^"]+,[^"]+","from":0,"to":50\}$`,
	"estimatefee": `^GET utils/estimatefee\?nbBlocks=2,3$`, "currency": `^GET currency$`,
	"messages GET": `^GET messages/verify\?address=`, "messages POST": `^POST messages/verify `,
	"paging 1000-1050": `^GET addrs/` + addrBusy + `/txs\?from=1000&to=1050$`, "paging 1500-1600": `^GET addrs/` + addrBusy + `/txs\?from=1500&to=1600$`,
	"bad address": `^GET addr/notanaddress$`, "bad txid": `^GET tx/zz$`,
	"tx-export 1": `^GET blocks\?limit=1$`, "tx-export 2": `^GET status\?q=getLastBlockHash$`, "tx-export 3": `^GET block/hash102$`,
	"empty address": `^GET addr/t1EmptyAddress0000000006$`, "pending address": `^GET addr/` + addrPend + `$`, "pending tx": `^GET tx/mp1$`,
}

func checkCoverage(t *testing.T, lines []string) {
	t.Helper()
	for name, pat := range routeFamilies {
		re := regexp.MustCompile(pat)
		found := false
		for _, l := range lines {
			if re.MatchString(l) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no line for %q (%s)", name, pat)
		}
	}
	specs, err := loadPaths(strings.NewReader(strings.Join(lines, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateSpecs(specs); err != nil {
		t.Fatalf("seeded paths fail the safety check: %v", err)
	}
}

func TestSeedPathsCoverRoutes(t *testing.T) {
	srv := newBlockbookStub()
	defer srv.Close()
	cl := newClient(0, 5*time.Second)
	lines, err := runSeed(cl, seedConfig{Base: srv.URL, Kind: "blockbook", Blocks: 3, TxsPerBlock: 25, Empty: "t1EmptyAddress0000000006"})
	if err != nil {
		t.Fatal(err)
	}
	checkCoverage(t, lines)
}

// The insight kind reads the target's own block-index, block and tx routes.
func TestSeedInsightKind(t *testing.T) {
	bb := newBlockbookStub()
	defer bb.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") == "getLastBlockHash" {
			writeJSON(w, map[string]interface{}{"syncTipHash": "hash102", "lastblockhash": "hash102"})
			return
		}
		writeJSON(w, map[string]interface{}{"info": map[string]interface{}{"blocks": 102}})
	})
	mux.HandleFunc("/api/block-index/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]interface{}{"blockHash": "hash" + strings.TrimPrefix(r.URL.Path, "/api/block-index/")})
	})
	mux.HandleFunc("/api/block/", func(w http.ResponseWriter, r *http.Request) {
		var h int
		fmt.Sscanf(strings.TrimPrefix(r.URL.Path, "/api/block/"), "hash%d", &h)
		var ids []string
		for _, tx := range bbTxs(h) {
			ids = append(ids, tx["txid"].(string))
		}
		writeJSON(w, map[string]interface{}{"hash": fmt.Sprintf("hash%d", h), "time": 1761696000 + h, "tx": ids})
	})
	mux.HandleFunc("/api/tx/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/tx/")
		for h := 100; h <= 102; h++ {
			for _, tx := range bbTxs(h) {
				if tx["txid"] != id {
					continue
				}
				// Insight shape: addr on vin, scriptPubKey on vout.
				var vin, vout []interface{}
				for _, e := range tx["vin"].([]interface{}) {
					m := e.(map[string]interface{})
					in := map[string]interface{}{}
					if c, ok := m["coinbase"]; ok {
						in["coinbase"] = c
					}
					if a, ok := m["addresses"].([]string); ok {
						in["addr"] = a[0]
					}
					vin = append(vin, in)
				}
				for _, e := range tx["vout"].([]interface{}) {
					m := e.(map[string]interface{})
					spk := map[string]interface{}{"addresses": m["addresses"]}
					if hx, ok := m["hex"]; ok {
						spk["hex"] = hx
					}
					vout = append(vout, map[string]interface{}{"scriptPubKey": spk})
				}
				writeJSON(w, map[string]interface{}{"txid": id, "vin": vin, "vout": vout})
				return
			}
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("/api/addr/", func(w http.ResponseWriter, r *http.Request) {
		n := 5
		switch strings.TrimPrefix(r.URL.Path, "/api/addr/") {
		case addrBusy:
			n = 1500
		case addrOne:
			n = 1
		}
		writeJSON(w, map[string]interface{}{"txApperances": n})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cl := newClient(0, 5*time.Second)
	lines, err := runSeed(cl, seedConfig{Base: srv.URL + "/api", Kind: "insight", Blocks: 3, TxsPerBlock: 25, Empty: "t1EmptyAddress0000000006", MempoolBase: bb.URL})
	if err != nil {
		t.Fatal(err)
	}
	checkCoverage(t, lines)
}

func TestSeedWithoutMempool(t *testing.T) {
	srv := newBlockbookStub()
	defer srv.Close()
	// No pending and no empty address: seed still works and says so.
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/mempool" {
			http.NotFound(w, r)
			return
		}
		resp, err := http.Get(srv.URL + r.URL.RequestURI())
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer resp.Body.Close()
		var buf bytes.Buffer
		buf.ReadFrom(resp.Body)
		w.WriteHeader(resp.StatusCode)
		w.Write(buf.Bytes())
	})
	p := httptest.NewServer(mux)
	defer p.Close()
	lines, err := runSeed(newClient(0, 5*time.Second), seedConfig{Base: p.URL, Kind: "blockbook", Blocks: 2})
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(lines, "\n")
	if !strings.Contains(text, "# note: no mempool route") || !strings.Contains(text, "no -empty address") {
		t.Fatalf("notes missing:\n%s", text)
	}
}

// --- end to end ---

func listener(body map[string]string, cors bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cors {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(204)
			return
		}
		b, ok := body[strings.TrimPrefix(r.URL.Path, "/api/")]
		if !ok {
			w.WriteHeader(404)
			fmt.Fprint(w, `{"status":"error"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, b)
	}))
}

func TestRunExitStatus(t *testing.T) {
	dir := t.TempDir()
	paths := filepath.Join(dir, "paths.txt")
	os.WriteFile(paths, []byte("GET version\nGET sync\n"), 0o644)
	ref := listener(map[string]string{"version": `{"version":"1"}`, "sync": `{"status":"finished"}`}, true)
	defer ref.Close()
	same := listener(map[string]string{"version": `{"version":"1","extra":true}`, "sync": `{"status":"finished"}`}, true)
	defer same.Close()
	diff := listener(map[string]string{"version": `{"version":"2"}`, "sync": `{"status":"finished"}`}, true)
	defer diff.Close()
	nocors := listener(map[string]string{"version": `{"version":"1"}`, "sync": `{"status":"finished"}`}, false)
	defer nocors.Close()

	runWith := func(newURL string, extra ...string) (int, string) {
		var out, errb bytes.Buffer
		args := append([]string{"-ref", ref.URL + "/api", "-new", newURL + "/api", "-paths", paths, "-out", filepath.Join(dir, "r.md"), "-rate", "0", "-load", "3", "-load-pause", "1ms"}, extra...)
		code := run(args, &out, &errb)
		return code, out.String() + errb.String()
	}
	if code, out := runWith(same.URL); code != 0 {
		t.Fatalf("extra field must exit 0, got %d\n%s", code, out)
	}
	if code, out := runWith(diff.URL); code != 1 || !strings.Contains(out, "FAIL") {
		t.Fatalf("changed value must exit 1, got %d\n%s", code, out)
	}
	if code, _ := runWith(nocors.URL); code != 1 {
		t.Fatalf("missing CORS must exit 1, got %d", code)
	}
	if code, out := runWith(diff.URL, "-ignore", "/version"); code != 0 {
		t.Fatalf("ignored pointer must exit 0, got %d\n%s", code, out)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "r.json"))
	if err != nil || !strings.Contains(string(raw), `"totals"`) || !strings.Contains(string(raw), `"load"`) {
		t.Fatalf("json report: %v\n%s", err, raw)
	}
}

// --- fix round 1 ---

func items(ids ...string) string {
	var parts []string
	for _, id := range ids {
		parts = append(parts, fmt.Sprintf(`{"txid":%q,"blockheight":5}`, id))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func ids(prefix string, from, to int) []string {
	var out []string
	for i := from; i < to; i++ {
		out = append(out, fmt.Sprintf("%s%d", prefix, i))
	}
	return out
}

func TestEmptyListenerPageFails(t *testing.T) {
	ref := j(t, `{"totalItems":2,"items":`+items("a", "b")+`}`)
	got := j(t, `{"totalItems":2,"items":[]}`)
	if failing(Compare(ref, got, "converter", "addrs/X/txs?from=0&to=50")) != 1 {
		t.Fatal("empty listener page against a full ref page must fail")
	}
	// Deep window: empty is fine only when the converter's totalItems is at or below from.
	deepRef := j(t, `{"totalItems":1200,"items":`+items(ids("t", 0, 50)...)+`}`)
	empty := j(t, `{"totalItems":1200,"items":[]}`)
	if failing(Compare(deepRef, empty, "converter", "addrs/X/txs?from=1000&to=1050")) != 1 {
		t.Fatal("empty deep window with ref totalItems 1200 > from must fail")
	}
	shortRef := j(t, `{"totalItems":1200,"items":`+items(ids("t", 0, 50)...)+`}`)
	if fs := Compare(shortRef, empty, "converter", "addrs/X/txs?from=1500&to=1600"); failing(fs) != 0 || fs[0].Kind != KindExpected {
		t.Fatalf("window past the end must be expected, got %+v", fs)
	}
	// Non-empty deep window is E8 expected.
	got = j(t, `{"totalItems":1200,"items":`+items("zz1", "zz2")+`}`)
	if fs := Compare(deepRef, got, "converter", "addrs/X/txs?from=1000&to=1050"); failing(fs) != 0 || !strings.HasPrefix(fs[0].Note, "E8") {
		t.Fatalf("got %+v", fs)
	}
}

func TestOneOverlapOfFullPageFails(t *testing.T) {
	ref := j(t, `{"totalItems":50,"items":`+items(ids("t", 0, 50)...)+`}`)
	got := j(t, `{"totalItems":50,"items":`+items(append([]string{"t0"}, ids("x", 0, 49)...)...)+`}`)
	if failing(Compare(ref, got, "converter", "addrs/X/txs?from=0&to=50")) != 1 {
		t.Fatal("one common txid of 50 must fail")
	}
	// A small drift (a new pending tx at the top, the tail falling off) is fine.
	got = j(t, `{"totalItems":51,"items":`+items(append([]string{"new"}, ids("t", 0, 49)...)...)+`}`)
	if failing(Compare(ref, got, "converter", "addrs/X/txs?from=0&to=50")) != 0 {
		t.Fatal("drift of one must pass")
	}
	// A narrower listener window inside the converter page is fine.
	got = j(t, `{"totalItems":50,"items":`+items(ids("t", 10, 20)...)+`}`)
	if failing(Compare(ref, got, "converter", "addrs/X/txs?from=10&to=20")) != 0 {
		t.Fatal("window inside the ref page must pass")
	}
}

func TestEmptyBlocksAndTransactionsFail(t *testing.T) {
	if failing(Compare(j(t, `{"blocks":[{"hash":"h","height":1}]}`), j(t, `{"blocks":[]}`), "converter", "blocks?limit=1")) != 1 {
		t.Fatal("empty blocks must fail")
	}
	if failing(Compare(j(t, `{"transactions":["a","b"]}`), j(t, `{"transactions":[]}`), "converter", "addr/X")) != 1 {
		t.Fatal("empty transactions must fail")
	}
	if failing(Compare(j(t, `{"transactions":["a","b"]}`), j(t, `{"transactions":["c"]}`), "converter", "addr/X")) != 1 {
		t.Fatal("disjoint transactions must fail")
	}
	if failing(Compare(j(t, `{"transactions":["a","b","c"]}`), j(t, `{"transactions":["b","c","d"]}`), "converter", "addr/X")) != 0 {
		t.Fatal("tail drift must pass")
	}
}

func resp(t *testing.T, status int, body string) *Response {
	v, ok := DecodeJSON([]byte(body))
	return &Response{Status: status, Body: []byte(body), JSON: v, IsJSON: ok}
}

func TestStatusClasses(t *testing.T) {
	ok500a, ok500b := resp(t, 500, `{"status":"error"}`), resp(t, 500, `{"status":"error"}`)
	if failing(CompareResponse(ok500a, ok500b, "converter", "status", Options{Expect: "ok"})) != 1 {
		t.Fatal("500 on both sides must fail on an ok case")
	}
	if failing(CompareResponse(ok500a, ok500b, "converter", "addr/zz", Options{Expect: "error"})) != 1 {
		t.Fatal("500 on both sides must fail on an error case too")
	}
	n1, n2 := resp(t, 404, `{"status":"error"}`), resp(t, 404, `{"status":"error"}`)
	if failing(CompareResponse(n1, n2, "converter", "status", Options{Expect: "ok"})) != 1 {
		t.Fatal("404 on both sides must fail on an ok case")
	}
	if fs := CompareResponse(n1, n2, "converter", "addr/zz", Options{Expect: "error"}); len(fs) != 0 {
		t.Fatalf("matching 404 on an error case is identical, got %+v", fs)
	}
	o1, o2 := resp(t, 200, `{}`), resp(t, 200, `{}`)
	if failing(CompareResponse(o1, o2, "converter", "addr/zz", Options{Expect: "error"})) != 1 {
		t.Fatal("malformed input accepted by both must fail")
	}
	if fs := CompareResponse(n1, n2, "converter", "x", Options{Expect: "any"}); len(fs) != 0 {
		t.Fatalf("any: matching 4xx passes, got %+v", fs)
	}
	if failing(CompareResponse(o1, resp(t, 503, `{}`), "converter", "x", Options{Expect: "any"})) != 1 {
		t.Fatal("a 5xx mismatch fails")
	}
}

func TestPathsClasses(t *testing.T) {
	specs, err := loadPaths(strings.NewReader("GET a\n#! error\nGET b\n#! any\nGET c\n#! ok\nGET d\n"))
	if err != nil || len(specs) != 4 {
		t.Fatal(err, specs)
	}
	got := []string{specs[0].Expect, specs[1].Expect, specs[2].Expect, specs[3].Expect}
	if strings.Join(got, ",") != "ok,error,any,ok" {
		t.Fatalf("classes %v", got)
	}
	if _, err := loadPaths(strings.NewReader("#! bogus\n")); err == nil {
		t.Fatal("unknown class must be refused")
	}
}

func TestSeedClassesAndDistinctLists(t *testing.T) {
	srv := newBlockbookStub()
	defer srv.Close()
	lines, err := runSeed(newClient(0, 5*time.Second), seedConfig{Base: srv.URL, Kind: "blockbook", Blocks: 3, Empty: "t1EmptyAddress0000000006"})
	if err != nil {
		t.Fatal(err)
	}
	specs, err := loadPaths(strings.NewReader(strings.Join(lines, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"status": "ok", "tx/zz": "error", "addr/notanaddress": "error", "block-index/abc": "error", "nosuchroute": "error", "utils/estimatefee?nbBlocks=2,3": "ok"}
	seen := map[string]bool{}
	for _, s := range specs {
		if w, ok := want[s.Path]; ok {
			seen[s.Path] = true
			if s.Expect != w {
				t.Errorf("%s: class %s, want %s", s.Path, s.Expect, w)
			}
		}
		if s.Method == "POST" && strings.HasPrefix(s.Path, "tx/send") && s.Expect != "error" {
			t.Errorf("tx/send must be an error case")
		}
		if strings.Contains(s.Path, "addrs/t1BusyAddressManyTxs000003,") && strings.Count(s.Path, ",") >= 20 {
			parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(strings.SplitN(s.Path, "?", 2)[0], "addrs/"), "/txs"), ",")
			uniq := map[string]bool{}
			for _, p := range parts {
				uniq[strings.TrimSuffix(p, "/utxo")] = true
			}
			if len(uniq) != len(parts) || s.Expect != "error" {
				t.Errorf("over-limit list must be distinct and an error case: %d/%d %s", len(uniq), len(parts), s.Expect)
			}
		}
	}
	for p := range want {
		if !seen[p] {
			t.Errorf("no line for %s", p)
		}
	}
	if got := strings.Count(distinctList("t1Abcdefghijklmnopqrstuvw", 101), ","); got != 100 {
		t.Fatalf("101 addresses expected, %d commas", got)
	}
}

func TestDefaultIgnores(t *testing.T) {
	if fs := Compare(j(t, `{"info":{"connections":8,"blocks":5}}`), j(t, `{"info":{"connections":9,"blocks":5}}`), "converter", "status"); len(fs) != 0 {
		t.Fatalf("info.connections is a default ignore, got %+v", fs)
	}
	pend := Compare(j(t, `{"txid":"a","confirmations":0,"time":100,"blocktime":100}`), j(t, `{"txid":"a","confirmations":0,"time":105,"blocktime":105}`), "converter", "tx/a")
	if len(pend) != 0 {
		t.Fatalf("pending tx times are ignored, got %+v", pend)
	}
	conf := Compare(j(t, `{"txid":"a","confirmations":3,"time":100}`), j(t, `{"txid":"a","confirmations":3,"time":105}`), "converter", "tx/a")
	if failing(conf) != 1 {
		t.Fatalf("confirmed tx time is compared, got %+v", conf)
	}
	// -ignore adds to the defaults.
	fs := compareWith(j(t, `{"info":{"connections":8,"errors":"a"}}`), j(t, `{"info":{"connections":9,"errors":"b"}}`), "converter", "status", Options{Ignores: []string{"/info/errors"}})
	if len(fs) != 0 {
		t.Fatalf("got %+v", fs)
	}
}

func TestAsmGarbageFails(t *testing.T) {
	fs := Compare(j(t, `{"asm":"t1abc"}`), j(t, `{"asm":"garbage!!"}`), "converter", "tx/a")
	if failing(fs) != 1 {
		t.Fatalf("got %+v", fs)
	}
}

func TestUnsafeBroadcastAllowList(t *testing.T) {
	for _, b := range []string{`{"RAWTX":"0100"}`, `{"rawTx":"aabb"}`, "rawtx=0100", "RawTx=0100&x=1", "0100aabb", `{"rawtx":"0100","rawtx2":"zz"}`} {
		if unsafeBroadcast(b) == "" {
			t.Errorf("must flag %q", b)
		}
	}
	for _, b := range []string{`{"rawtx":"zz"}`, `{"rawtx":"abc"}`, `{}`, `{"rawtx":`, `{"rawtx":1}`, "rawtx=zz", "hello"} {
		if unsafeBroadcast(b) != "" {
			t.Errorf("must allow %q", b)
		}
	}
}

func TestLoadBadBodyNotOK(t *testing.T) {
	good := &Response{Status: 200, IsJSON: true, Latency: 1000}
	bad := &Response{Status: 200, IsJSON: false, Latency: 1000}
	s := summarize([]*Response{good, bad})
	if s.OK != 1 || s.BadBody != 1 {
		t.Fatalf("%+v", s)
	}
}

func TestGetCORS(t *testing.T) {
	with := &Response{Status: 200, Header: http.Header{"Access-Control-Allow-Origin": {"*"}}}
	without := &Response{Status: 200, Header: http.Header{}}
	if fs := getCORS("p", with, without); len(fs) != 1 || fs[0].Kind != KindMissing {
		t.Fatal(fs)
	}
	if fs := getCORS("p", with, with); fs != nil {
		t.Fatal(fs)
	}
}

func TestRunRefusals(t *testing.T) {
	dir := t.TempDir()
	paths := filepath.Join(dir, "p.txt")
	os.WriteFile(paths, []byte("GET version\n"), 0o644)
	var out, errb bytes.Buffer
	if code := run([]string{"-ref", "http://h:1/api/", "-new", "HTTP://H:1/api", "-paths", paths}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "same URL") {
		t.Fatalf("same URL must exit 2, got %d %s", code, errb.String())
	}
	if code := run([]string{"-ref", "http://a/api", "-new", "http://b/api", "-paths", paths, "-load-paths", "999"}, &out, &errb); code != 2 {
		t.Fatalf("load-paths cap, got %d", code)
	}
}

func TestRunNewAltAndClasses(t *testing.T) {
	dir := t.TempDir()
	paths := filepath.Join(dir, "paths.txt")
	os.WriteFile(paths, []byte("GET version\n"), 0o644)
	ref := listener(map[string]string{"version": `{"version":"1"}`}, true)
	defer ref.Close()
	nw := listener(map[string]string{"version": `{"version":"1"}`}, true)
	defer nw.Close()
	altOK := listener(map[string]string{"version": `{"version":"1"}`}, true)
	defer altOK.Close()
	altBad := listener(map[string]string{"version": `{"version":"9"}`}, true)
	defer altBad.Close()
	args := func(alt string) []string {
		return []string{"-ref", ref.URL + "/api", "-new", nw.URL + "/api", "-new-alt", alt + "/api", "-paths", paths, "-out", filepath.Join(dir, "r.md"), "-rate", "0"}
	}
	var out, errb bytes.Buffer
	if code := run(args(altOK.URL), &out, &errb); code != 0 {
		t.Fatalf("matching alt prefix must pass, got %d\n%s%s", code, out.String(), errb.String())
	}
	out.Reset()
	if code := run(args(altBad.URL), &out, &errb); code != 1 || !strings.Contains(out.String(), "new-alt prefix") {
		t.Fatalf("differing alt prefix must fail, got %d\n%s", code, out.String())
	}
	// Both hosts answering 404 on an ok case is a failure, not "identical".
	os.WriteFile(paths, []byte("GET missing\n"), 0o644)
	out.Reset()
	if code := run([]string{"-ref", ref.URL + "/api", "-new", nw.URL + "/api", "-paths", paths, "-out", filepath.Join(dir, "r2.md"), "-rate", "0"}, &out, &errb); code != 1 {
		t.Fatalf("404 on both sides of an ok case must exit 1, got %d\n%s", code, out.String())
	}
	os.WriteFile(paths, []byte("#! error\nGET missing\n"), 0o644)
	if code := run([]string{"-ref", ref.URL + "/api", "-new", nw.URL + "/api", "-paths", paths, "-out", filepath.Join(dir, "r3.md"), "-rate", "0"}, &out, &errb); code != 0 {
		t.Fatalf("matching 404 on an error case is fine, got %d", code)
	}
}

// --- fix round 2 ---

func TestTruncatedListenerPageFails(t *testing.T) {
	ref := j(t, `{"totalItems":50,"from":0,"to":50,"items":`+items(ids("t", 0, 50)...)+`}`)
	for _, n := range []int{1, 3, 10, 47} {
		got := j(t, fmt.Sprintf(`{"totalItems":50,"from":0,"to":50,"items":%s}`, items(ids("t", 0, n)...)))
		if failing(Compare(ref, got, "converter", "addrs/X/txs?from=0&to=50")) != 1 {
			t.Errorf("%d common items of a 50 item window must fail", n)
		}
	}
	// One short at the newest end is drift, reported as expected.
	got := j(t, `{"totalItems":50,"from":0,"to":50,"items":`+items(ids("t", 0, 49)...)+`}`)
	fs := Compare(ref, got, "converter", "addrs/X/txs?from=0&to=50")
	if failing(fs) != 0 || kinds(fs)[KindExpected] == 0 {
		t.Fatalf("one short must be drift, got %+v", fs)
	}
	// A long page is also wrong.
	got = j(t, `{"totalItems":50,"from":0,"to":50,"items":`+items(ids("t", 0, 50)...)+`}`)
	long := j(t, `{"totalItems":50,"from":0,"to":50,"items":`+items(append(ids("t", 0, 50), "x1", "x2", "x3")...)+`}`)
	if failing(Compare(ref, got, "converter", "addrs/X/txs?from=0&to=50")) != 0 || failing(Compare(ref, long, "converter", "addrs/X/txs?from=0&to=50")) != 1 {
		t.Fatal("exact length passes, three too many fails")
	}
	// A narrower window answers its own size; the ref page is the whole page.
	narrow := j(t, `{"totalItems":50,"from":0,"to":10,"items":`+items(ids("t", 0, 10)...)+`}`)
	if failing(Compare(ref, narrow, "converter", "addrs/X/txs?from=0&to=10")) != 0 {
		t.Fatal("ten items for a ten item window must pass")
	}
	if failing(Compare(ref, j(t, `{"totalItems":50,"from":0,"to":4,"items":`+items(ids("t", 0, 4)...)+`}`), "converter", "addrs/X/txs?from=0&to=10")) != 1 {
		t.Fatal("four items for a ten item window must fail")
	}
	// A short history is not a short page: ref has 3 txs in total.
	small := j(t, `{"totalItems":3,"items":`+items("a", "b", "c")+`}`)
	if failing(Compare(small, small, "converter", "addrs/X/txs?from=0&to=50")) != 0 {
		t.Fatal("identical short history must pass")
	}
}

func TestRefNon2xxOnOkCaseFails(t *testing.T) {
	ok := resp(t, 200, `{"a":1}`)
	r404 := resp(t, 404, `{"status":"error"}`)
	fs := CompareResponse(r404, ok, "converter", "status", Options{Expect: "ok"})
	if failing(fs) != 1 || fs[0].Kind != KindChanged || !strings.Contains(fs[0].Note, "reference broken") {
		t.Fatalf("got %+v", fs)
	}
	if failing(CompareResponse(resp(t, 502, `x`), ok, "converter", "status", Options{Expect: "ok"})) != 1 {
		t.Fatal("ref 502 must fail")
	}
	// Declared absent routes stay extra, by class or by -ref-lacks.
	for _, o := range []Options{{Expect: "new"}, {Expect: "ok", RefLacks: true}} {
		fs = CompareResponse(r404, ok, "converter", "currency", o)
		if failing(fs) != 0 || fs[0].Kind != KindExtra {
			t.Fatalf("%+v: got %+v", o, fs)
		}
	}
	// Both sides 404 on a declared absent route is still a failure.
	if failing(CompareResponse(r404, resp(t, 404, `{}`), "converter", "currency", Options{Expect: "new"})) != 1 {
		t.Fatal("listener 404 on a new route must fail")
	}
}

func TestRefBrokenRunFailsLoudly(t *testing.T) {
	dir := t.TempDir()
	paths := filepath.Join(dir, "paths.txt")
	os.WriteFile(paths, []byte("GET version\nGET sync\nGET peer\n"), 0o644)
	ref := listener(map[string]string{}, true) // wrong prefix: every path 404
	defer ref.Close()
	nw := listener(map[string]string{"version": `{"v":1}`, "sync": `{"s":1}`, "peer": `{"p":1}`}, true)
	defer nw.Close()
	var out, errb bytes.Buffer
	code := run([]string{"-ref", ref.URL + "/api", "-new", nw.URL + "/api", "-paths", paths, "-out", filepath.Join(dir, "r.md"), "-rate", "0"}, &out, &errb)
	if code != 1 || !strings.Contains(out.String(), "REFERENCE BROKEN") {
		t.Fatalf("got %d\n%s", code, out.String())
	}
	// Declaring the routes absent turns them into extras and the run passes.
	out.Reset()
	code = run([]string{"-ref", ref.URL + "/api", "-new", nw.URL + "/api", "-paths", paths, "-out", filepath.Join(dir, "r.md"), "-rate", "0", "-ref-lacks", "version,sync,peer"}, &out, &errb)
	if code != 0 || strings.Contains(out.String(), "REFERENCE BROKEN") {
		t.Fatalf("got %d\n%s", code, out.String())
	}
}

func TestNewAltInheritsZebraAndIsRefused(t *testing.T) {
	dir := t.TempDir()
	paths := filepath.Join(dir, "paths.txt")
	os.WriteFile(paths, []byte("GET messages/verify\n"), 0o644)
	mk := func(status int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			if r.Method == http.MethodOptions {
				w.WriteHeader(204)
				return
			}
			w.WriteHeader(status)
			fmt.Fprint(w, `{"status":"error"}`)
		}))
	}
	ref, nw, alt := mk(200), mk(501), mk(501)
	defer ref.Close()
	defer nw.Close()
	defer alt.Close()
	var out, errb bytes.Buffer
	base := []string{"-ref", ref.URL + "/api", "-new", nw.URL + "/api", "-paths", paths, "-out", filepath.Join(dir, "r.md"), "-rate", "0"}
	if code := run(append(base, "-zebra-new", "-new-alt", alt.URL+"/api"), &out, &errb); code != 0 {
		t.Fatalf("501 on both listener prefixes with -zebra-new must pass, got %d\n%s", code, out.String())
	}
	for _, same := range []string{nw.URL + "/api/", ref.URL + "/api"} {
		errb.Reset()
		if code := run(append(base, "-new-alt", same), &out, &errb); code != 2 || !strings.Contains(errb.String(), "-new-alt") {
			t.Fatalf("-new-alt %s must be refused, got %d", same, code)
		}
	}
}

func TestPendingIsDecidedByTheReference(t *testing.T) {
	// Reference pending: first-seen times differ per node and are ignored.
	if fs := Compare(j(t, `{"confirmations":0,"time":100}`), j(t, `{"confirmations":0,"time":105}`), "converter", "tx/a"); len(fs) != 0 {
		t.Fatalf("got %+v", fs)
	}
	// Listener says 0, reference says 3: a difference, and the times compare.
	fs := Compare(j(t, `{"confirmations":3,"time":100}`), j(t, `{"confirmations":0,"time":105}`), "converter", "tx/a")
	if failing(fs) != 2 {
		t.Fatalf("want confirmations and time to fail, got %+v", fs)
	}
	// Reference pending, listener already confirmed (a block landed): not a difference.
	if fs := Compare(j(t, `{"confirmations":0,"time":100}`), j(t, `{"confirmations":1,"time":105}`), "converter", "tx/a"); failing(fs) != 0 {
		t.Fatalf("got %+v", fs)
	}
}

func TestNewClassInPathsFile(t *testing.T) {
	specs, err := loadPaths(strings.NewReader("#! new\nGET currency\n"))
	if err != nil || specs[0].Expect != "new" {
		t.Fatal(err, specs)
	}
}
