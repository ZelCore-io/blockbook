//go:build unittest

package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/trezor/blockbook/common"
	"github.com/trezor/blockbook/db"
	"github.com/trezor/blockbook/tests/dbtestdata"
)

func getHTML(t *testing.T, u string) string {
	t.Helper()
	resp, err := http.DefaultClient.Do(newGetRequest(u))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", u, resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func checkHTML(t *testing.T, name, html string, want, absent []string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(html, w) {
			t.Errorf("%s: missing %q\ngot\n%s", name, w, html)
		}
	}
	for _, a := range absent {
		if strings.Contains(html, a) {
			t.Errorf("%s: must not contain %q", name, a)
		}
	}
}

func Test_PublicServer_HomeAndStatus(t *testing.T) {
	parser, chain := setupChain(t)
	s, dbpath := setupPublicHTTPServer(parser, chain, t, false)
	defer closeAndDestroyPublicServer(t, s, dbpath)
	s.ConnectFullPublicInterface()
	ts := httptest.NewServer(s.https.Handler)
	defer ts.Close()

	blocksTab := `<nav class="hub-tabs" aria-label="Latest activity"><a href="/?tab=blocks" class="active" aria-current="page">Blocks</a><a href="/?tab=txs">Transactions</a><a class="hub-tabs-end" href="/blocks">View all</a></nav>`
	newestBlockRow := `<tr><td class="text-nowrap"><a class="hub-mono" href="/block/225494">225<span class="ns">494</span></a><div class="hub-muted">`
	newestBlockHash := `<td class="hub-mono hub-ellipsis d-none d-sm-table-cell">00000000eb0443fd7dc4a1ed5c686a8e995057805f9a161d9a5a77a95e72b7b6</td><td class="text-nowrap">4 tx · 2<span class="ns">345</span><span class="ns">678</span> B</td></tr>`

	home := getHTML(t, ts.URL+"/")
	checkHTML(t, "home", home, []string{
		`<header class="hub-band hub-band-home" style="--hub-band: #2B61D1">`,
		`<h1>Explore Fake Coin</h1>`,
		`<dl class="hub-figures"><div><dt>Height</dt><dd>225<span class="ns">494</span></dd></div><div><dt>Mempool</dt><dd>0 tx</dd></div></dl></div></header><main><div class="hub-layout"><div class="hub-main">`,
		blocksTab,
		newestBlockRow,
		newestBlockHash,
		`<a class="hub-mono" href="/block/225493">225<span class="ns">493</span></a>`,
		`<h2 id="hub-network-title">Network</h2><dl class="hub-stat-grid"><div class="hub-stat-wide"><dt>Node</dt><dd class="hub-mono hub-stat-node">/Fakecoin:0.0.1/</dd></div><div><dt>Status</dt><dd><span class="hub-badge hub-badge-ok">Synced</span></dd></div></dl></section></div></div></main><footer class="hub-footer">`,
	}, []string{"Application status", "<dt>Price</dt>", "Flux ecosystem products", "Sponsored", "News from Zelcore and SSP", "<dt>Difficulty</dt>", "More explorers", "hub-explorer"})
	if n := strings.Count(home, `action="/search"`); n != 1 {
		t.Errorf("home: %d search forms, want 1", n)
	}

	status := getHTML(t, ts.URL+"/status")
	checkHTML(t, "status", status, []string{
		`<header class="hub-band" style="--hub-band: #2B61D1">`,
		`<main><div class="hub-layout"><div class="hub-main"><section class="hub-card"><h1>Application status</h1></section>`,
		`<div class="hub-status-grid"><section class="hub-card"><h2>Blockbook</h2><table class="table data-table info-table"><tbody><tr><td>Coin</td>`,
		`</td></tr><tr><td>Mempool in Sync</td><td><h6 class="badge bg-danger">false</h6></td></tr><tr><td>Last Mempool Update</td><td></td></tr><tr><td>Transactions in Mempool</td><td><a href="/mempool">0</a></td></tr><tr><td>Current Fiat rates</td>`,
		`</td></tr></tbody></table></section><section class="hub-card"><h2>Backend</h2><table class="table data-table info-table"><tbody><tr><td>Chain</td><td>fakecoin</td></tr><tr><td>Version</td><td>001001</td></tr><tr><td>Subversion</td><td>/Fakecoin:0.0.1/</td></tr><tr><td>Last Block</td><td>2</td></tr><tr><td>Difficulty</td><td></td></tr></tbody></table></section></div></div></div></main><footer class="hub-footer">`,
		`unknown (development build)</a>`,
	}, []string{"hub-band-home", "Explore Fake Coin", `class="hub-tabs"`})

	txs := getHTML(t, ts.URL+"/?tab=txs")
	checkHTML(t, "txs tab", txs, []string{
		`<nav class="hub-tabs" aria-label="Latest activity"><a href="/?tab=blocks">Blocks</a><a href="/?tab=txs" class="active" aria-current="page">Transactions</a><a class="hub-tabs-end" href="/block/225494">View all</a></nav>`,
		`The mempool is empty. Showing transactions from block <a href="/block/225494">225<span class="ns">494</span></a>.`,
		`<a class="hub-mono" href="/tx/` + dbtestdata.TxidB2T1 + `">` + dbtestdata.TxidB2T1 + `</a>`,
		`<a class="hub-mono" href="/tx/` + dbtestdata.TxidB2T2 + `">` + dbtestdata.TxidB2T2 + `</a>`,
	}, []string{newestBlockHash, `href="/mempool">View all`})

	// Each tab loads only its own list: the blocks tab never reads the mempool txids.
	for _, c := range []struct {
		url                    string
		blocks, mempool, block bool
	}{
		{url: "/", blocks: true},
		{url: "/?tab=blocks", blocks: true},
		{url: "/?tab=txs", mempool: true, block: true},
	} {
		_, data, err := s.explorerIndex(httptest.NewRecorder(), httptest.NewRequest("GET", c.url, nil))
		if err != nil {
			t.Fatal(err)
		}
		if (data.Blocks != nil) != c.blocks || (data.MempoolTxids != nil) != c.mempool || (data.Block != nil) != c.block {
			t.Errorf("%s: loaded blocks=%v mempool=%v block=%v, want %v %v %v", c.url, data.Blocks != nil, data.MempoolTxids != nil, data.Block != nil, c.blocks, c.mempool, c.block)
		}
	}

	bad := getHTML(t, ts.URL+"/?tab="+url.QueryEscape(`<script>alert(1)</script>`))
	checkHTML(t, "bad tab", bad, []string{blocksTab, newestBlockRow}, []string{"alert(1)", "alert%281%29", "%3Cscript"})
}

func Test_PublicServer_HomePriceFreshness(t *testing.T) {
	cases := []struct {
		name  string
		age   time.Duration
		price bool
	}{
		{name: "fresh", age: 10 * time.Minute, price: true},
		{name: "stale", age: 3 * time.Hour, price: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			parser, chain := setupChain(t)
			s, dbpath := setupPublicHTTPServerWithFiatFixture(parser, chain, t, false, func(d *db.RocksDB) error {
				ticker := []common.CurrencyRatesTicker{{Timestamp: fixedTimeNow().Add(-c.age), Rates: map[string]float32{"usd": 0.0237, "eur": 0.0219}}}
				return d.FiatRatesStoreSpecialTickers("CurrentTickers", &ticker)
			})
			defer closeAndDestroyPublicServer(t, s, dbpath)
			s.ConnectFullPublicInterface()
			ts := httptest.NewServer(s.https.Handler)
			defer ts.Close()

			// The price is always quoted in USD, even when the visitor picked another secondary currency.
			home := getHTML(t, ts.URL+"/?secondary=eur")
			price := `<dl class="hub-figures"><div><dt>Price</dt><dd title="Exchange rates provided by Coingecko">$0.0237</dd></div><div><dt>Height</dt>`
			if c.price {
				checkHTML(t, c.name, home, []string{price}, nil)
			} else {
				checkHTML(t, c.name, home, []string{`<dl class="hub-figures"><div><dt>Height</dt>`}, []string{"<dt>Price</dt>", "$0.0237"})
			}
		})
	}
}

func Test_homePrice(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	ticker := func(age time.Duration, usd float32) *common.CurrencyRatesTicker {
		return &common.CurrencyRatesTicker{Timestamp: now.Add(-age), Rates: map[string]float32{"usd": usd}}
	}
	tests := []struct {
		name   string
		ticker *common.CurrencyRatesTicker
		want   string
	}{
		{"fresh GRS", ticker(10*time.Minute, 0.0237), "$0.0237"},
		{"just within two hours", ticker(2*time.Hour, 0.0237), "$0.0237"},
		{"stale", ticker(2*time.Hour+time.Second, 0.0237), ""},
		{"missing ticker", nil, ""},
		{"no usd rate", &common.CurrencyRatesTicker{Timestamp: now, Rates: map[string]float32{"eur": 1}}, ""},
		{"zero", ticker(0, 0), ""},
		{"cents", ticker(0, 0.5), "$0.50"},
		{"dollars", ticker(0, 1.5), "$1.50"},
		{"thousands", ticker(0, 64123.45), "$64,123.45"},
		{"tiny", ticker(0, 0.000012345), "$0.0000123"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := homePrice(tt.ticker, now); got != tt.want {
				t.Errorf("homePrice() = %q, want %q", got, tt.want)
			}
		})
	}
}

func Test_initials(t *testing.T) {
	for name, want := range map[string]string{"Zelcore": "Z", "  ssp": "S", "élan": "É", "日本": "日", "": "", "   ": ""} {
		if got := initials(name); got != want {
			t.Errorf("initials(%q) = %q, want %q", name, got, want)
		}
	}
}

// getStatus returns the status code and body of GET u.
func getStatus(t *testing.T, u string) (int, string) {
	t.Helper()
	resp, err := http.DefaultClient.Do(newGetRequest(u))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

// During initial sync only the limited interface is connected; / and /status must both work then.
func Test_PublicServer_StatusDuringInitialSync(t *testing.T) {
	parser, chain := setupChain(t)
	s, dbpath := setupPublicHTTPServer(parser, chain, t, false)
	defer closeAndDestroyPublicServer(t, s, dbpath)
	s.is.SetInitialSync(true)
	defer s.is.SetInitialSync(false)
	ts := httptest.NewServer(s.https.Handler)
	defer ts.Close()

	banner := "Application is now in initial synchronization and does not provide any data."
	status := getHTML(t, ts.URL+"/status")
	checkHTML(t, "status in initial sync", status, []string{"<h1>Application status</h1>", banner}, nil)
	home := getHTML(t, ts.URL+"/")
	checkHTML(t, "home in initial sync", home, []string{`<div class="hub-card hub-muted" role="status">` + banner + `</div>`}, nil)
	if code, _ := getStatus(t, ts.URL+"/blocks"); code != http.StatusServiceUnavailable {
		t.Errorf("/blocks in initial sync: status %d, want 503", code)
	}
}

func Test_PublicServer_HomeSyncDisabledWarning(t *testing.T) {
	parser, chain := setupChain(t)
	s, dbpath := setupPublicHTTPServer(parser, chain, t, false)
	defer closeAndDestroyPublicServer(t, s, dbpath)
	s.ConnectFullPublicInterface()
	ts := httptest.NewServer(s.https.Handler)
	defer ts.Close()

	warning := `<div class="hub-card hub-muted" role="status">Synchronization with backend is disabled, the state of index is not up to date.</div>`
	s.is.SyncMode = false
	checkHTML(t, "home sync disabled", getHTML(t, ts.URL+"/"), []string{warning}, nil)
	s.is.SyncMode = true
	defer func() { s.is.SyncMode = false }()
	checkHTML(t, "home sync enabled", getHTML(t, ts.URL+"/"), nil, []string{"Synchronization with backend is disabled"})
}

// With an external explorer configured, / and /status still render and link to no internal explorer pages.
func Test_PublicServer_HomeAndStatusExternalExplorer(t *testing.T) {
	parser, chain := setupChain(t)
	s, dbpath := setupPublicHTTPServerWithOptions(parser, chain, t, false, nil, "https://explorer.example.com/")
	defer closeAndDestroyPublicServer(t, s, dbpath)
	if s.internalExplorer {
		t.Fatal("internal explorer must be off with a non-empty explorerURL")
	}
	internalLinks := []string{`href="/block/`, `href="/mempool"`, `action="/search"`, `href="/blocks"`, `href="/tx/`}
	check := func(t *testing.T, ts *httptest.Server) {
		home := getHTML(t, ts.URL+"/")
		checkHTML(t, "home external explorer", home, []string{`<h1>Explore Fake Coin</h1>`, `<a href="/?tab=txs">Transactions</a>`}, internalLinks)
		txs := getHTML(t, ts.URL+"/?tab=txs")
		checkHTML(t, "txs tab external explorer", txs, []string{`<a href="/?tab=txs" class="active" aria-current="page">Transactions</a>`}, internalLinks)
		status := getHTML(t, ts.URL+"/status")
		checkHTML(t, "status external explorer", status, []string{"<h1>Application status</h1>", "<td>Transactions in Mempool</td>"}, internalLinks)
	}
	t.Run("limited", func(t *testing.T) {
		ts := httptest.NewServer(s.https.Handler)
		defer ts.Close()
		check(t, ts)
	})
	t.Run("full", func(t *testing.T) {
		s.ConnectFullPublicInterface()
		ts := httptest.NewServer(s.https.Handler)
		defer ts.Close()
		check(t, ts)
	})
}
