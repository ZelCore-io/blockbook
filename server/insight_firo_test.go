package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trezor/blockbook/api"
	"github.com/trezor/blockbook/bchain"
)

// insightFiroFixture holds firod results and the stock insight-api-zcoin 0.4.5
// responses recorded from FIRO-01 at the same time (masternode responses are
// cut to the nodes in the recorded evoznodelist subset; stock's JSON-RPC id is
// dropped since it is random).
type insightFiroFixture struct {
	RPC struct {
		Evoznodelist          json.RawMessage            `json:"evoznodelist"`
		EvoznodeCount         json.RawMessage            `json:"evoznode_count"`
		Gettotalsupply        json.RawMessage            `json:"gettotalsupply"`
		Getaddressbalance     map[string]json.RawMessage `json:"getaddressbalance"`
		Getaddresstxids       map[string]json.RawMessage `json:"getaddresstxids"`
		Getsparknames         json.RawMessage            `json:"getsparknames"`
		Getsparknametxdetails map[string]json.RawMessage `json:"getsparknametxdetails"`
	} `json:"rpc"`
	MasternodeCases []struct {
		Method      string          `json:"method"`
		Path        string          `json:"path"`
		ContentType string          `json:"contentType"`
		Body        string          `json:"body"`
		Expect      json.RawMessage `json:"expect"`
	} `json:"masternodeCases"`
	Simple map[string]json.RawMessage `json:"simple"`
}

type insightFiroRPC struct {
	fix   *insightFiroFixture
	mu    sync.Mutex
	calls map[string]int
	fail  bool
}

func (r *insightFiroRPC) call(method string, params []interface{}, result interface{}) error {
	r.mu.Lock()
	r.calls[method]++
	r.mu.Unlock()
	if r.fail {
		return &bchain.RPCError{Code: -1, Message: "secret daemon text"}
	}
	var raw json.RawMessage
	arg := func() string {
		if m, ok := params[0].(map[string][]string); ok {
			return m["addresses"][0]
		}
		return params[0].(string)
	}
	switch method {
	case "evoznodelist":
		raw = r.fix.RPC.Evoznodelist
	case "evoznode":
		raw = r.fix.RPC.EvoznodeCount
	case "gettotalsupply":
		raw = r.fix.RPC.Gettotalsupply
	case "getaddressbalance":
		raw = r.fix.RPC.Getaddressbalance[arg()]
	case "getaddresstxids":
		raw = r.fix.RPC.Getaddresstxids[arg()]
	case "getsparknames":
		raw = r.fix.RPC.Getsparknames
	case "getsparknametxdetails":
		raw = r.fix.RPC.Getsparknametxdetails[arg()]
	}
	if raw == nil {
		return &bchain.RPCError{Code: -25, Message: "Unknown transaction."}
	}
	return json.Unmarshal(raw, result)
}

func insightFiroSetup(t *testing.T) (*InsightServer, *insightFake, *insightFiroRPC, *insightFiroFixture) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	// absolute: other tests in this package change the working directory
	b, err := os.ReadFile(filepath.Join(filepath.Dir(file), "testdata", "insight_firo.json"))
	if err != nil {
		t.Fatal(err)
	}
	fix := &insightFiroFixture{}
	if err := json.Unmarshal(b, fix); err != nil {
		t.Fatal(err)
	}
	f := insightFixture()
	s := insightTestServer(t, f)
	rpc := &insightFiroRPC{fix: fix, calls: map[string]int{}}
	s.SetBackend(rpc.call, "FIRO")
	if err := s.EnableExtension("firo"); err != nil {
		t.Fatal(err)
	}
	return s, f, rpc, fix
}

var insightFiroID = regexp.MustCompile(`,"id":[0-9]+}$`)

func insightCompact(t *testing.T, raw []byte) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestInsightFiroMasternodesMatchStock(t *testing.T) {
	s, _, rpc, fix := insightFiroSetup(t)
	for _, tc := range fix.MasternodeCases {
		t.Run(tc.Method+" "+tc.Path, func(t *testing.T) {
			r := httptest.NewRequest(tc.Method, tc.Path, strings.NewReader(tc.Body))
			if tc.ContentType != "" {
				r.Header.Set("Content-Type", tc.ContentType)
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			body := strings.TrimSuffix(w.Body.String(), "\n")
			if w.Code != 200 || !insightFiroID.MatchString(body) {
				t.Fatalf("got %d %.200s", w.Code, body)
			}
			if got, want := insightFiroID.ReplaceAllString(body, "}"), insightCompact(t, tc.Expect); got != want {
				t.Fatalf("differs from stock\n got %.400s\nwant %.400s", got, want)
			}
		})
	}
	if rpc.calls["evoznodelist"] != 1 {
		t.Fatalf("evoznodelist called %d times, want 1 (cached)", rpc.calls["evoznodelist"])
	}
}

func TestInsightFiroSimpleRoutesMatchStock(t *testing.T) {
	s, _, rpc, fix := insightFiroSetup(t)
	for path, want := range fix.Simple {
		w := insightRequest(s, "GET", "/api/"+path, "")
		if got := strings.TrimSuffix(w.Body.String(), "\n"); w.Code != 200 || got != insightCompact(t, want) {
			t.Errorf("%s: got %d %.300s, want %.300s", path, w.Code, got, insightCompact(t, want))
		}
	}
	// a second round is served from the cache
	for path := range fix.Simple {
		insightRequest(s, "GET", "/insight-api/"+path, "")
	}
	for method, n := range map[string]int{"gettotalsupply": 1, "getaddressbalance": 4, "getaddresstxids": 2, "getsparknames": 1, "evoznode": 1} {
		if rpc.calls[method] != n {
			t.Errorf("%s called %d times, want %d", method, rpc.calls[method], n)
		}
	}
}

func TestInsightFiroCacheExpires(t *testing.T) {
	s, _, rpc, _ := insightFiroSetup(t)
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	old := insightNow
	insightNow = func() time.Time { return now }
	defer func() { insightNow = old }()
	insightRequest(s, "GET", "/api/masternode/masternodecount", "")
	now = now.Add(29 * time.Second)
	insightRequest(s, "GET", "/api/masternode/masternodecount", "")
	if rpc.calls["evoznode"] != 1 {
		t.Fatalf("evoznode called %d times within the TTL", rpc.calls["evoznode"])
	}
	now = now.Add(2 * time.Second)
	insightRequest(s, "GET", "/api/masternode/masternodecount", "")
	if rpc.calls["evoznode"] != 2 {
		t.Fatalf("evoznode called %d times after the TTL", rpc.calls["evoznode"])
	}
}

func TestInsightFiroErrorsAndMethods(t *testing.T) {
	s, _, rpc, _ := insightFiroSetup(t)
	for _, tc := range []struct {
		method, path, body string
		code               int
	}{
		{"POST", "/api/znodes/listznodesarray", "", 404}, // stock 404s it too
		{"POST", "/api/masternode/listmasternodesarray/abc", "", 405},
		{"POST", "/api/masternode/listmasternodes", "", 405},
		{"GET", "/api/sigmastatus", "", 404},
		{"GET", "/api/richlist", "", 404},
		{"GET", "/api/masternode/listmasternodesarray/a/b", "", 404},
	} {
		if w := insightRequest(s, tc.method, tc.path, tc.body); w.Code != tc.code {
			t.Errorf("%s %s: got %d, want %d", tc.method, tc.path, w.Code, tc.code)
		}
	}
	r := httptest.NewRequest("POST", "/api/masternode/listmasternodesarray", strings.NewReader(`{"filter":[1]}`))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Errorf("non-string filter: got %d", w.Code)
	}
	rpc.fail = true
	w = insightRequest(s, "GET", "/api/sparknames", "")
	if w.Code != 500 || strings.Contains(w.Body.String(), "secret") || !strings.Contains(w.Body.String(), `"status":"error"`) {
		t.Errorf("RPC failure: got %d %s", w.Code, w.Body.String())
	}
	s.SetBackend(nil, "FIRO")
	if w = insightRequest(s, "GET", "/api/ext/getmoneysupply", ""); w.Code != 501 {
		t.Errorf("no RPC: got %d", w.Code)
	}
}

func TestInsightFiroCurrency(t *testing.T) {
	s, f, _, _ := insightFiroSetup(t)
	f.fiat = &api.FiatTicker{Rates: map[string]float32{"usd": 0.841331}}
	w := insightRequest(s, "GET", "/api/currency", "")
	want := `{"status":200,"data":{"zcoin_usd":0.841331,"btc_usd":0,"btc_zcoin":0,"bitstamp":0.841331}}` + "\n"
	if w.Code != 200 || w.Body.String() != want {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	f.fiat = nil
	w = insightRequest(s, "GET", "/api/currency", "")
	if want = `{"status":200,"data":{"zcoin_usd":0,"btc_usd":0,"btc_zcoin":0,"bitstamp":0}}` + "\n"; w.Body.String() != want {
		t.Fatalf("no rate: got %s", w.Body.String())
	}
}

// Other coins keep the generic table: no Firo routes, generic currency shape,
// no Firo labels.
func TestInsightFiroRoutesOnlyForFiro(t *testing.T) {
	f := insightFixture()
	s := insightTestServer(t, f)
	rpc := &insightFiroRPC{fix: &insightFiroFixture{}, calls: map[string]int{}}
	s.SetBackend(rpc.call, "BTC")
	for _, path := range []string{"/api/masternode/listmasternodes", "/api/masternode/listmasternodesarray", "/api/masternode/masternodecount", "/api/sparknames", "/api/ext/getmoneysupply", "/api/zerocoin/getrealsupply", "/api/sigma/gettotalsigmamint"} {
		if w := insightRequest(s, "GET", path, ""); w.Code != 404 {
			t.Errorf("%s: got %d, want 404", path, w.Code)
		}
	}
	if w := insightRequest(s, "GET", "/api/currency", ""); !strings.Contains(w.Body.String(), `"short":"BTC"`) {
		t.Errorf("generic currency changed: %s", w.Body.String())
	}
	out, err := s.transaction(nil, insightFiroTx())
	if err != nil {
		t.Fatal(err)
	}
	if out.Vin[0].Addr != nil || out.Vout[0].Type != "" || out.SparkData != nil || len(rpc.calls) != 0 {
		t.Errorf("labels applied without the extension: %+v", out)
	}
	if err := s.EnableExtension("nope"); err == nil {
		t.Error("unknown extension accepted")
	}
}

const insightFiroSparkNameTx = "db857913ed2fe72910d6e0a2463a4dfb4b01e3a361c3f5d02128322bec5f3146"

// insightFiroSparkNameFeeScript is vout 0 of mainnet tx 01676aed...: P2PKH of
// the dev fund aFA2TbqG... + OP_SPARKNAMEID "connect" OP_DROP <Spark address> OP_DROP.
const insightFiroSparkNameFeeScript = "76a9149e6778ee1011af76f6f800873032ea8e15ada4ca88ace107636f6e6e656374754c90736d3168646770337932793579376b636a68653068726a6e6674706e6c617a777067397932393567777636736d3766656b7a736677703773656839783771346a67306e306d616b6379343270773534326668706e7337307130776c3936787a3230386d6439746763723675347a636872646a6a64383866346763353375737867636668366334326636716e327778686775"

func insightFiroTx() *api.Tx {
	return &api.Tx{
		Txid: insightFiroSparkNameTx, ValueOutSat: insightAmount(100000000), FeesSat: insightAmount(3618),
		Vin: []api.Vin{{N: 0, Hex: "d3", ValueSat: insightAmount(0), Addresses: []string{"SparkSpend"}}},
		Vout: []api.Vout{
			{N: 0, ValueSat: insightAmount(100000000), Hex: insightFiroSparkNameFeeScript, Addresses: []string{"aFA2TbqG9cnhhzX5Yny2pBJRK5EaEqLCH7"}, IsAddress: true},
			{N: 1, ValueSat: insightAmount(0), Hex: "d20160", Addresses: []string{"SparkSMint"}},
		},
	}
}

func TestInsightFiroTxLabels(t *testing.T) {
	s, f, rpc, fix := insightFiroSetup(t)
	f.txs[insightFiroSparkNameTx] = insightFiroTx()
	w := insightRequest(s, "GET", "/api/tx/"+insightFiroSparkNameTx, "")
	var out struct {
		Vin []struct {
			Addr string `json:"addr"`
		} `json:"vin"`
		Vout []struct {
			N            int `json:"n"`
			ScriptPubKey struct {
				N         int      `json:"n"`
				Type      string   `json:"type"`
				Addresses []string `json:"addresses"`
			} `json:"scriptPubKey"`
		} `json:"vout"`
		SparkData json.RawMessage `json:"sparkData"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); w.Code != 200 || err != nil {
		t.Fatalf("got %d %v", w.Code, err)
	}
	if out.Vin[0].Addr != "Sparkspend" {
		t.Errorf("vin addr %q", out.Vin[0].Addr)
	}
	if string(out.SparkData) != insightCompact(t, fix.RPC.Getsparknametxdetails[insightFiroSparkNameTx]) {
		t.Errorf("sparkData %s", out.SparkData)
	}
	// no synthetic "sparkname" output: n is the on-chain output index
	if len(out.Vout) != 2 {
		t.Fatalf("got %d outputs, want the 2 on-chain ones: %s", len(out.Vout), w.Body.String())
	}
	for i, v := range out.Vout {
		if v.N != i || v.ScriptPubKey.N != i {
			t.Errorf("vout %d numbered %d/%d", i, v.N, v.ScriptPubKey.N)
		}
	}
	if v := out.Vout[1].ScriptPubKey; v.Type != "sparksmint" || len(v.Addresses) != 1 || v.Addresses[0] != "Sparksmint" {
		t.Errorf("sparksmint output %+v", v)
	}
	// the Spark name fee output keeps its address, with stock's type
	if v := out.Vout[0].ScriptPubKey; v.Type != "pubkeyhash" || len(v.Addresses) != 1 || v.Addresses[0] != "aFA2TbqG9cnhhzX5Yny2pBJRK5EaEqLCH7" {
		t.Errorf("Spark name fee output %+v", out.Vout[0])
	}
	insightRequest(s, "GET", "/api/tx/"+insightFiroSparkNameTx, "")
	if rpc.calls["getsparknametxdetails"] != 1 {
		t.Errorf("getsparknametxdetails called %d times, want 1 (cached)", rpc.calls["getsparknametxdetails"])
	}
}

func TestInsightFiroLabelTable(t *testing.T) {
	s, _, rpc, _ := insightFiroSetup(t)
	tx := &api.Tx{
		Txid: "other", ValueOutSat: insightAmount(250000000), FeesSat: insightAmount(0),
		Vin:  []api.Vin{{Hex: "c2aa"}, {Hex: "c4aa"}, {Hex: "c7aa"}, {Hex: "c9"}, {Hex: "c8aa", ValueSat: insightAmount(0)}, {Hex: "d3"}, {Hex: "4830"}, {Coinbase: "c2"}},
		Vout: []api.Vout{{Hex: "c1aa"}, {Hex: "c3aa"}, {Hex: "c5aa"}, {Hex: "C6aa"}, {Hex: "d1aa"}, {Hex: "d2aa"}, {Hex: "6a00"}},
	}
	out, err := s.transaction(nil, tx)
	if err != nil {
		t.Fatal(err)
	}
	wantVin := []string{"Zerospend", "Sigmaspend", "Lelantusjsplit", "Lelantusjsplit", "Remint", "Sparkspend", "", ""}
	for i, want := range wantVin {
		got := ""
		if out.Vin[i].Addr != nil {
			got = *out.Vin[i].Addr
		}
		if got != want {
			t.Errorf("vin %d: %q, want %q", i, got, want)
		}
	}
	if r := out.Vin[4]; r.Value != "2.5" || r.ValueSat != "250000000" {
		t.Errorf("remint value %s %s", r.Value, r.ValueSat)
	}
	wantVout := [][2]string{{"zerocoinmint", "Zeromint"}, {"sigmamint", "Sigmamint"}, {"lelantusmint", "Lelantusmint"}, {"lelantusjmint", "Lelantusjmint"}, {"sparkmint", "Sparkmint"}, {"sparksmint", "Sparksmint"}}
	for i, want := range wantVout {
		v := out.Vout[i]
		if v.Type != want[0] || v.ScriptPubKey.Type != want[0] || len(v.ScriptPubKey.Addresses) != 1 || v.ScriptPubKey.Addresses[0] != want[1] || v.Addresses[0] != want[1] {
			t.Errorf("vout %d: %+v, want %v", i, v.ScriptPubKey, want)
		}
	}
	if out.Vout[6].Type != "" || len(out.Vout) != 7 || out.SparkData != nil {
		t.Errorf("unexpected extras: %+v", out.Vout[6])
	}
	// a Spark spend that registers no name gets no sparkname output
	if rpc.calls["getsparknametxdetails"] != 1 {
		t.Errorf("getsparknametxdetails called %d times", rpc.calls["getsparknametxdetails"])
	}
}

// insightFiroManyNodes repeats the fixture's evoznodelist entries under new
// collateral keys up to n nodes, the size of the mainnet list.
func insightFiroManyNodes(t *testing.T, fix *insightFiroFixture, n int) json.RawMessage {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(fix.RPC.Evoznodelist))
	if _, err := dec.Token(); err != nil {
		t.Fatal(err)
	}
	var values []json.RawMessage
	for dec.More() {
		if _, err := dec.Token(); err != nil {
			t.Fatal(err)
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			t.Fatal(err)
		}
		values = append(values, v)
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i := 0; i < n; i++ {
		if i > 0 {
			buf.WriteByte(',')
		}
		fmt.Fprintf(&buf, `"COutPoint(%064x, %d)":`, i, i%2)
		buf.Write(values[i%len(values)])
	}
	buf.WriteByte('}')
	return buf.Bytes()
}

func TestInsightFiroFilterCaps(t *testing.T) {
	s, _, rpc, fix := insightFiroSetup(t)
	fix.RPC.Evoznodelist = insightFiroManyNodes(t, fix, 7300)
	long := strings.Repeat("a", insightFiroFilterMax+1)
	many := make([]string, insightFiroFilterMaxItems+1)
	for i := range many {
		many[i] = strconv.Itoa(i)
	}
	tooMany := strings.Join(many, ",")
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/masternode/listmasternodesarray/" + long, ""},
		{"GET", "/api/masternode/listmasternodesarray?filter=" + long, ""},
		{"GET", "/api/masternode/listmasternodesarray/" + tooMany, ""},
		{"GET", "/api/masternode/listmasternodesarray?filter=" + tooMany, ""},
		{"POST", "/api/masternode/listmasternodesarray", `{"filter":"` + long + `"}`},
		{"POST", "/api/masternode/listmasternodesarray", `{"filter":"` + tooMany + `"}`},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		if tc.body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 400 || !strings.Contains(w.Body.String(), `"status":"error"`) {
			t.Errorf("%s %.60s: got %d %.200s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
	if rpc.calls["evoznodelist"] != 0 {
		t.Errorf("an over-cap filter loaded the list")
	}
	// repeated items count once
	if w := insightRequest(s, "GET", "/api/masternode/listmasternodesarray/"+strings.Repeat("x,", 200)+"x", ""); w.Code != 200 {
		t.Errorf("repeated items: got %d", w.Code)
	}
	// 100 distinct items that match nothing scan the whole 7.3k list per item
	start := time.Now()
	w := insightRequest(s, "GET", "/api/masternode/listmasternodesarray/"+strings.Join(many[:insightFiroFilterMaxItems], "z,")+"z", "")
	if elapsed := time.Since(start); w.Code != 200 || elapsed > 2*time.Second {
		t.Errorf("100-item filter: got %d in %v", w.Code, elapsed)
	}
	if w := insightRequest(s, "GET", "/api/masternode/listmasternodesarray/"+strings.Repeat("0", 63)+"5-1", ""); w.Code != 200 || strings.Count(w.Body.String(), `"collateral"`) != 1 {
		t.Errorf("exact match: got %d %.300s", w.Code, w.Body.String())
	}
}

func TestInsightFiroFilterStopsOnCancel(t *testing.T) {
	s, _, _, _ := insightFiroSetup(t)
	f := s.ext.(*insightFiro)
	if _, err := f.listMasternodesArray(context.Background(), []string{"a"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.listMasternodesArray(ctx, []string{"a", "b"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled filter: %v", err)
	}
}

func TestInsightCachedSingleFlight(t *testing.T) {
	var c insightCached
	var calls atomic.Int32
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := c.get(context.Background(), time.Minute, func() (interface{}, error) {
				calls.Add(1)
				<-release
				return 7, nil
			})
			if err != nil || v != 7 {
				t.Errorf("got %v %v", v, err)
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("%d loads for concurrent callers, want 1", calls.Load())
	}
}

func TestInsightCachedStaleAndErrors(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	old := insightNow
	insightNow = func() time.Time { return now }
	defer func() { insightNow = old }()
	var calls int
	fail := func() (interface{}, error) { calls++; return nil, errors.New("down") }
	// no good value: the error is served and cached briefly
	var c insightCached
	if _, err := c.get(nil, time.Minute, fail); err == nil {
		t.Fatal("want error")
	}
	if _, err := c.get(nil, time.Minute, fail); err == nil || calls != 1 {
		t.Fatalf("error not cached: %v, %d calls", err, calls)
	}
	now = now.Add(insightCacheErrTTL + time.Second)
	c.get(nil, time.Minute, fail)
	if calls != 2 {
		t.Fatalf("error cached past its TTL: %d calls", calls)
	}
	// a good value that went stale is served when the refresh fails
	var d insightCached
	d.get(nil, time.Minute, func() (interface{}, error) { return "good", nil })
	now = now.Add(2 * time.Minute)
	calls = 0
	for i := 0; i < 3; i++ {
		if v, err := d.get(nil, time.Minute, fail); err != nil || v != "good" {
			t.Fatalf("stale-on-error: got %v %v", v, err)
		}
	}
	if calls != 1 {
		t.Errorf("failed refresh retried %d times within the error TTL", calls)
	}
}

func TestInsightCachedWaiterCancel(t *testing.T) {
	var c insightCached
	hang := make(chan struct{})
	defer close(hang)
	go c.get(context.Background(), time.Minute, func() (interface{}, error) { <-hang; return 1, nil })
	time.Sleep(20 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.get(ctx, time.Minute, func() (interface{}, error) { t.Error("second load"); return nil, nil })
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("waiter did not give up: %v after %v", err, time.Since(start))
	}
}

func TestInsightFiroSparkNameCache(t *testing.T) {
	s, _, _, _ := insightFiroSetup(t)
	f := s.ext.(*insightFiro)
	var calls int
	var answer error
	s.SetBackend(func(method string, params []interface{}, result interface{}) error {
		calls++
		return answer
	}, "FIRO")
	answer = errors.New("timeout")
	f.sparkName("t1")
	f.sparkName("t1")
	if calls != 2 {
		t.Errorf("transport failure cached: %d calls", calls)
	}
	answer = &bchain.RPCError{Code: -28, Message: "Loading block index"}
	f.sparkName("t2")
	f.sparkName("t2")
	if calls != 4 {
		t.Errorf("warm-up error cached: %d calls", calls)
	}
	// firod 0.14.18 answers -25 both for a tx it cannot find yet and for a
	// known tx that registers no name; only the second is cached
	answer = &bchain.RPCError{Code: -25, Message: "Unknown transaction."}
	f.sparkName("t3")
	f.sparkName("t3")
	if calls != 6 {
		t.Errorf("unknown tx cached: %d calls", calls)
	}
	answer = &bchain.RPCError{Code: -25, Message: insightFiroNotSparkName}
	f.sparkName("t4")
	f.sparkName("t4")
	if calls != 7 {
		t.Errorf("daemon answer not cached: %d calls", calls)
	}
	// the bound keeps at most two generations
	for i := 0; i < insightFiroNameCacheSize*2; i++ {
		f.sparkName(strconv.Itoa(i))
	}
	if n := len(f.names) + len(f.oldNames); n > insightFiroNameCacheSize {
		t.Errorf("%d cached names", n)
	}
	if _, ok := f.oldNames[strconv.Itoa(insightFiroNameCacheSize*2-insightFiroNameCacheSize/2-1)]; !ok {
		t.Errorf("recent generation dropped")
	}
}

// The Spark name fee output keeps its full script; history items of the
// address number outputs as tx/:txid does (the on-chain index).
func TestInsightFiroSparkNameOutputJSON(t *testing.T) {
	s, f, _, _ := insightFiroSetup(t)
	f.txs[insightFiroSparkNameTx] = insightFiroTx()
	f.address = &api.Address{AddrStr: "aFA2TbqG9cnhhzX5Yny2pBJRK5EaEqLCH7", BalanceSat: insightAmount(0), UnconfirmedBalanceSat: insightAmount(0), Txs: 1, Txids: []string{insightFiroSparkNameTx}}
	for _, path := range []string{"/api/tx/" + insightFiroSparkNameTx, "/api/addrs/aFA2TbqG9cnhhzX5Yny2pBJRK5EaEqLCH7/txs?from=0&to=1"} {
		body := insightRequest(s, "GET", path, "").Body.String()
		var tx struct {
			Vout []struct {
				N            int                        `json:"n"`
				ScriptPubKey map[string]json.RawMessage `json:"scriptPubKey"`
			} `json:"vout"`
			SparkData json.RawMessage `json:"sparkData"`
		}
		var hist struct {
			Items []json.RawMessage `json:"items"`
		}
		raw := []byte(body)
		if json.Unmarshal(raw, &hist) == nil && len(hist.Items) == 1 {
			raw = hist.Items[0]
		}
		if err := json.Unmarshal(raw, &tx); err != nil || len(tx.Vout) != 2 {
			t.Fatalf("%s: %v %.300s", path, err, body)
		}
		if tx.Vout[0].N != 0 || string(tx.Vout[0].ScriptPubKey["hex"]) != `"`+insightFiroSparkNameFeeScript+`"` {
			t.Errorf("%s: Spark name fee output %.400s", path, body)
		}
		if !strings.HasPrefix(string(tx.Vout[0].ScriptPubKey["asm"]), `"OP_DUP OP_HASH160 9e6778ee1011af76f6f800873032ea8e15ada4ca OP_EQUALVERIFY OP_CHECKSIG OP_UNKNOWN225 636f6e6e656374 OP_DROP`) {
			t.Errorf("%s: asm %s", path, tx.Vout[0].ScriptPubKey["asm"])
		}
		if len(tx.SparkData) == 0 {
			t.Errorf("%s: no sparkData", path)
		}
	}
}

func TestInsightCachedLoaderPanic(t *testing.T) {
	var c insightCached
	_, err := c.get(context.Background(), time.Minute, func() (interface{}, error) {
		panic("boom")
	})
	if err == nil {
		t.Fatal("loader panic did not become an error")
	}
	v, err := c.get(context.Background(), time.Minute, func() (interface{}, error) { return 1, nil })
	if err == nil && v != nil {
		t.Fatal("error was not remembered for insightCacheErrTTL")
	}
}

// A JSON array filter does what stock's code makes of it: the elements joined
// by "," are one substring of each entry's JSON (200, possibly []). An array
// stock's code throws on, or with non-string elements, is refused.
func TestInsightFiroArrayFilter(t *testing.T) {
	s, _, _, _ := insightFiroSetup(t)
	post := func(body string) (int, []json.RawMessage) {
		r := httptest.NewRequest("POST", "/api/masternode/listmasternodesarray", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		var out struct {
			Result []json.RawMessage `json:"result"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out.Result
	}
	_, all := post(`{}`)
	if len(all) < 3 {
		t.Fatalf("fixture has %d nodes", len(all))
	}
	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"filter":["aKs8csbHpTWYwzfyRhFiZN4in83qLweTRD"]}`, 1},
		{`{"filter":["aKs8csbHpTWYwzfyRhFiZN4in83qLweTRD","23.88.21.154"]}`, 0},
		{`{"filter":["33a48424836ba49c9eefbafe174b12ef156c1587cf3b9636ac348133f367b852-1"]}`, 0},
		{`{"filter":[]}`, len(all)},
		{`{"filter":[""]}`, len(all)},
	} {
		code, got := post(tc.body)
		if code != 200 || len(got) != tc.want {
			t.Errorf("%s: %d, %d rows, want 200 and %d", tc.body, code, len(got), tc.want)
		}
	}
	for _, body := range []string{`{"filter":[","]}`, `{"filter":["a","-"]}`, `{"filter":[1]}`, `{"filter":[["a"]]}`, `{"filter":{"a":1}}`, `{"filter":["` + strings.Repeat("a", insightFiroFilterMax+1) + `"]}`} {
		if code, _ := post(body); code != 400 {
			t.Errorf("%.60s: got %d, want 400", body, code)
		}
	}
}

// Exchange outputs and Spark name fee outputs get bitcore's address type.
func TestInsightFiroAddressTypes(t *testing.T) {
	s, _, _, _ := insightFiroSetup(t)
	tx := &api.Tx{
		Txid: "x", ValueOutSat: insightAmount(3), FeesSat: insightAmount(0),
		Vout: []api.Vout{
			{ValueSat: insightAmount(1), Hex: "e076a914db14fb77c6517377c08ae36a8688d874eaa95a9f88ac", Addresses: []string{"EXXaSB8H9NUcFscQFuEMeKYTAKpq2BYjxdVS"}, IsAddress: true},
			{N: 1, ValueSat: insightAmount(1), Hex: insightFiroSparkNameFeeScript, Addresses: []string{"aFA2TbqG9cnhhzX5Yny2pBJRK5EaEqLCH7"}, IsAddress: true},
			{N: 2, ValueSat: insightAmount(1), Hex: "76a9149e6778ee1011af76f6f800873032ea8e15ada4ca88ace1", Addresses: []string{}},
		},
	}
	out, err := s.transaction(nil, tx)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"expubkeyhash", "pubkeyhash", ""} {
		if got := out.Vout[i].ScriptPubKey.Type; got != want {
			t.Errorf("vout %d: type %q, want %q", i, got, want)
		}
		if out.Vout[i].Type != "" {
			t.Errorf("vout %d: converter type %q", i, out.Vout[i].Type)
		}
	}
}
