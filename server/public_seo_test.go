//go:build unittest

package server

import (
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang/glog"
	"github.com/trezor/blockbook/bchain/coins/eth"
	"github.com/trezor/blockbook/common"
	"github.com/trezor/blockbook/db"
	"github.com/trezor/blockbook/explorercontent"
	"github.com/trezor/blockbook/tests/dbtestdata"
)

type seoResponse struct {
	status int
	header http.Header
	body   string
}

func seoGet(t *testing.T, u string, header map[string]string) seoResponse {
	t.Helper()
	r := newGetRequest(u)
	for k, v := range header {
		r.Header.Set(k, v)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return seoResponse{status: resp.StatusCode, header: resp.Header, body: string(b)}
}

type statusCase struct {
	name, path  string
	status      int
	contentType string
	want        []string
	absent      []string
}

const noindexMeta = `<meta name="robots" content="noindex">`

func runStatusCases(t *testing.T, ts *httptest.Server, cases []statusCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := seoGet(t, ts.URL+c.path, nil)
			if got.status != c.status {
				t.Errorf("GET %s: status %d, want %d", c.path, got.status, c.status)
			}
			if ct := got.header.Get("Content-Type"); ct != c.contentType {
				t.Errorf("GET %s: Content-Type %q, want %q", c.path, ct, c.contentType)
			}
			checkHTML(t, c.path, got.body, c.want, c.absent)
		})
	}
}

func Test_PublicServer_SEO_StatusCodes_BitcoinType(t *testing.T) {
	parser, chain := setupChain(t)
	s, dbpath := setupPublicHTTPServer(parser, chain, t, false)
	defer closeAndDestroyPublicServer(t, s, dbpath)
	s.ConnectFullPublicInterface()
	ts := httptest.NewServer(s.https.Handler)
	defer ts.Close()

	html := "text/html; charset=utf-8"
	notFound := []string{noindexMeta, `<section class="hub-card hub-error"><h1>Not found</h1>`, `<p class="hub-error-hint">Check the hash or address and try again.</p>`, `<input id="error-q" name="q" type="text" value="" placeholder=`}
	runStatusCases(t, ts, []statusCase{
		{name: "home", path: "/", status: http.StatusOK, contentType: html, want: []string{`<h1>Explore Fake Coin</h1>`}, absent: []string{`name="robots"`}},
		{name: "valid tx", path: "/tx/" + dbtestdata.TxidB1T1, status: http.StatusOK, contentType: html, absent: []string{`name="robots"`}},
		{name: "valid block", path: "/block/225493", status: http.StatusOK, contentType: html, absent: []string{`name="robots"`}},
		{name: "blocks", path: "/blocks", status: http.StatusOK, contentType: html},
		{name: "unknown path", path: "/does-not-exist", status: http.StatusNotFound, contentType: html, want: []string{noindexMeta, `<h1>Page not found</h1>`, `There is no page at this address.`, `Search for a block, transaction or address instead.`, `id="error-q"`}, absent: []string{`hub-band-home`, `autofocus`}},
		{name: "unknown nested path", path: "/foo/bar.php", status: http.StatusNotFound, contentType: html, want: []string{`<h1>Page not found</h1>`}},
		{name: "blocks with slash", path: "/blocks/", status: http.StatusNotFound, contentType: html, want: []string{`<h1>Page not found</h1>`}},
		{name: "bad tx id", path: "/tx/00", status: http.StatusNotFound, contentType: html, want: notFound},
		{name: "unknown tx", path: "/tx/" + strings.Repeat("ab", 32), status: http.StatusNotFound, contentType: html, want: notFound},
		{name: "unknown address", path: "/address/notAnAddress", status: http.StatusNotFound, contentType: html, want: notFound},
		{name: "unknown block", path: "/block/99999999", status: http.StatusNotFound, contentType: html, want: notFound},
		{name: "bad xpub", path: "/xpub/xpubJunk", status: http.StatusNotFound, contentType: html, want: notFound},
		{name: "spending not found", path: "/spending/" + strings.Repeat("ab", 32) + "/0", status: http.StatusNotFound, contentType: html, want: notFound},
		{name: "search no results", path: "/search/?q=zzz%3Cb%3E", status: http.StatusNotFound, contentType: html, want: []string{`<meta name="robots" content="noindex,follow">`, `<h1>No results</h1>`, `No matching records found for &#39;zzz&lt;b&gt;&#39;`, `Check the hash or address and try again.`, `value="zzz&lt;b&gt;" placeholder="Search for block, transaction, address or xpub" autofocus>`}, absent: []string{"<b>"}},
	})
}

func Test_PublicServer_SEO_StatusCodes_EthereumType(t *testing.T) {
	parser := eth.NewEthereumParser(1, true)
	chain, err := dbtestdata.NewFakeBlockChainEthereumType(parser)
	if err != nil {
		glog.Fatal("fakechain: ", err)
	}
	s, dbpath := setupPublicHTTPServer(parser, chain, t, false)
	defer closeAndDestroyPublicServer(t, s, dbpath)
	s.ConnectFullPublicInterface()
	ts := httptest.NewServer(s.https.Handler)
	defer ts.Close()

	html := "text/html; charset=utf-8"
	runStatusCases(t, ts, []statusCase{
		{name: "home", path: "/", status: http.StatusOK, contentType: html, absent: []string{`name="robots"`}},
		{name: "unknown path", path: "/wp-login.php", status: http.StatusNotFound, contentType: html, want: []string{noindexMeta, `<h1>Page not found</h1>`}},
		{name: "bad tx id", path: "/tx/0x00", status: http.StatusNotFound, contentType: html, want: []string{noindexMeta, `<h1>Not found</h1>`}},
		{name: "unknown address", path: "/address/zzz", status: http.StatusNotFound, contentType: html, want: []string{noindexMeta, `<h1>Not found</h1>`}},
		{name: "unknown block", path: "/block/99999999", status: http.StatusNotFound, contentType: html, want: []string{noindexMeta, `<h1>Not found</h1>`}},
		{name: "search no results", path: "/search/?q=nothing", status: http.StatusNotFound, contentType: html, want: []string{`<h1>No results</h1>`}},
	})

	llms := seoGet(t, ts.URL+"/llms.txt", nil)
	checkHTML(t, "eth llms.txt", llms.body, []string{"Fakecoin (FAKE)", "/api/v2/address/{address}"}, []string{"/api/v2/xpub/", "/api/v2/utxo/"})
}

// During initial sync only the limited interface is connected: unknown paths stay 503 there.
func Test_PublicServer_SEO_UnknownPathDuringInitialSync(t *testing.T) {
	parser, chain := setupChain(t)
	s, dbpath := setupPublicHTTPServer(parser, chain, t, false)
	defer closeAndDestroyPublicServer(t, s, dbpath)
	ts := httptest.NewServer(s.https.Handler)
	defer ts.Close()
	if got := seoGet(t, ts.URL+"/does-not-exist", nil); got.status != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503", got.status)
	}
	// the crawl files are served from startup
	for _, p := range []string{"/robots.txt", "/sitemap.xml", "/llms.txt"} {
		if got := seoGet(t, ts.URL+p, nil); got.status != http.StatusOK {
			t.Errorf("%s during initial sync: status %d, want 200", p, got.status)
		}
	}
	// without the full interface there are no block pages to list
	sm := seoGet(t, ts.URL+"/sitemap.xml", nil)
	if strings.Contains(sm.body, "/block/") || strings.Contains(sm.body, "/blocks<") {
		t.Errorf("sitemap lists block pages before the full interface is connected:\n%s", sm.body)
	}
}

func Test_PublicServer_SEO_CrawlFiles(t *testing.T) {
	parser, chain := setupChain(t)
	s, dbpath := setupPublicHTTPServer(parser, chain, t, false)
	defer closeAndDestroyPublicServer(t, s, dbpath)
	s.ConnectFullPublicInterface()
	ts := httptest.NewServer(s.https.Handler)
	defer ts.Close()

	// robots.txt without public_url: rules, AI crawlers, no Sitemap line
	robots := seoGet(t, ts.URL+"/robots.txt", nil)
	if robots.status != http.StatusOK || robots.header.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("robots.txt: status %d, Content-Type %q", robots.status, robots.header.Get("Content-Type"))
	}
	rules := "Allow: /\nDisallow: /api/\nDisallow: /search\nDisallow: /sendtx\nDisallow: /xpub/\nDisallow: /*?*secondary=\nDisallow: /*?*use_secondary=\n"
	checkHTML(t, "robots.txt", robots.body, []string{"User-agent: *\n" + rules, "User-agent: GPTBot\n", "User-agent: ClaudeBot\n", "User-agent: CCBot\n" + rules, "User-agent: Google-Extended\n", "User-agent: PerplexityBot\n"}, []string{"Sitemap:", "Disallow: /\n", "Disallow: /tx", "Disallow: /address", "Disallow: /block"})
	for _, ua := range aiCrawlers {
		if !strings.Contains(robots.body, "User-agent: "+ua+"\n") {
			t.Errorf("robots.txt: missing crawler %s", ua)
		}
	}

	// sitemap.xml without public_url: request scheme and host, X-Forwarded-Proto honoured
	for _, c := range []struct {
		header map[string]string
		base   string
	}{
		{nil, ts.URL},
		{map[string]string{"X-Forwarded-Proto": "https"}, "https://" + strings.TrimPrefix(ts.URL, "http://")},
		{map[string]string{"X-Forwarded-Proto": "javascript"}, ts.URL},
	} {
		sm := seoGet(t, ts.URL+"/sitemap.xml", c.header)
		if sm.status != http.StatusOK || sm.header.Get("Content-Type") != "application/xml; charset=utf-8" {
			t.Fatalf("sitemap.xml: status %d, Content-Type %q", sm.status, sm.header.Get("Content-Type"))
		}
		var set struct {
			XMLName xml.Name `xml:"http://www.sitemaps.org/schemas/sitemap/0.9 urlset"`
			URLs    []struct {
				Loc     string `xml:"loc"`
				LastMod string `xml:"lastmod"`
			} `xml:"url"`
		}
		if err := xml.Unmarshal([]byte(sm.body), &set); err != nil {
			t.Fatalf("sitemap.xml is not valid XML: %v\n%s", err, sm.body)
		}
		// 4 stable pages + the 2 blocks of the test chain (fewer than sitemapBlocks)
		want := []string{c.base + "/", c.base + "/blocks", c.base + "/api-docs", c.base + "/status", c.base + "/block/225494", c.base + "/block/225493"}
		if len(set.URLs) != len(want) {
			t.Fatalf("sitemap.xml: %d urls, want %d\n%s", len(set.URLs), len(want), sm.body)
		}
		for i, u := range set.URLs {
			if u.Loc != want[i] {
				t.Errorf("sitemap url %d = %q, want %q", i, u.Loc, want[i])
			}
		}
		if set.URLs[4].LastMod == "" || set.URLs[0].LastMod != set.URLs[4].LastMod {
			t.Errorf("sitemap lastmod: home %q, newest block %q", set.URLs[0].LastMod, set.URLs[4].LastMod)
		}
		if cc := sm.header.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("sitemap.xml without public_url: Cache-Control %q, want no-store", cc)
		}
		if strings.Contains(sm.body, "/tx/") || strings.Contains(sm.body, "/address/") {
			t.Error("sitemap.xml must not list tx or address pages")
		}
	}

	// llms.txt: coin name, shortcut and label from the config, the request host as canonical host
	llms := seoGet(t, ts.URL+"/llms.txt", nil)
	if llms.status != http.StatusOK || llms.header.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("llms.txt: status %d, Content-Type %q", llms.status, llms.header.Get("Content-Type"))
	}
	if cc := llms.header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("llms.txt without public_url: Cache-Control %q, want no-store", cc)
	}
	if cc := robots.header.Get("Cache-Control"); cc != "public, max-age=3600" {
		t.Errorf("robots.txt: Cache-Control %q (its body has no request host)", cc)
	}
	checkHTML(t, "llms.txt", llms.body, []string{"# Fake Coin Explorer\n", "Blockchain explorer for Fakecoin (FAKE)", "Based on Blockbook by SatoshiLabs", "Canonical host: " + ts.URL + "\n", "[API documentation](" + ts.URL + "/api-docs)", ts.URL + "/api/v2/tx/{txid}", ts.URL + "/api/v2/xpub/{xpub}", ts.URL + "/block/{height or hash}"}, []string{"Operated by", "Powered by", "ZelCore", "Flux"})

	// with public_url: Sitemap line, absolute URLs and canonical host from the config, whatever the Host
	s.SetPublicURL("https://explorer.example.org/")
	defer s.SetPublicURL("")
	robots = seoGet(t, ts.URL+"/robots.txt", nil)
	checkHTML(t, "robots.txt public_url", robots.body, []string{"\nSitemap: https://explorer.example.org/sitemap.xml\n"}, nil)
	sm := seoGet(t, ts.URL+"/sitemap.xml", map[string]string{"X-Forwarded-Proto": "http"})
	checkHTML(t, "sitemap public_url", sm.body, []string{"<loc>https://explorer.example.org/</loc>", "<loc>https://explorer.example.org/block/225494</loc>"}, []string{ts.URL})
	llms = seoGet(t, ts.URL+"/llms.txt", nil)
	if cc := sm.header.Get("Cache-Control"); cc != "public, max-age=300" {
		t.Errorf("sitemap.xml with public_url: Cache-Control %q", cc)
	}
	if cc := llms.header.Get("Cache-Control"); cc != "public, max-age=3600" {
		t.Errorf("llms.txt with public_url: Cache-Control %q", cc)
	}
	checkHTML(t, "llms.txt public_url", llms.body, []string{"Canonical host: https://explorer.example.org\n"}, []string{ts.URL})

	// only GET and HEAD
	r, _ := http.NewRequest(http.MethodPost, ts.URL+"/robots.txt", nil)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST robots.txt: status %d, want 405", resp.StatusCode)
	}

	// favicon without a feed: 404 as before
	if got := seoGet(t, ts.URL+"/favicon.ico", nil); got.status != http.StatusNotFound {
		t.Errorf("favicon.ico without feed: status %d, want 404", got.status)
	}

	// a young tx and block page keep the default caching
	for _, p := range []string{"/tx/" + dbtestdata.TxidB2T1, "/block/225494", "/"} {
		if got := seoGet(t, ts.URL+p, nil); got.status != http.StatusOK || got.header.Get("Cache-Control") != "" {
			t.Errorf("%s: status %d, Cache-Control %q, want 200 and none", p, got.status, got.header.Get("Cache-Control"))
		}
	}
}

func Test_robotsTxtAndBaseURL_PathPrefix(t *testing.T) {
	s := &PublicServer{binding: "localhost:9130/explorer/"}
	robots := s.robotsTxt()
	checkHTML(t, "prefixed robots", robots, []string{"Allow: /explorer/\n", "Disallow: /explorer/api/\n", "Disallow: /explorer/*?*secondary=\n"}, []string{"Disallow: /api/", "Sitemap:"})
	r := httptest.NewRequest("GET", "http://example.com/explorer/sitemap.xml", nil)
	if got := s.baseURL(r); got != "http://example.com/explorer" {
		t.Errorf("baseURL = %q", got)
	}
	r.Host = "evil.com/<x>"
	if got := s.baseURL(r); got != "http://localhost/explorer" {
		t.Errorf("baseURL with a bad Host = %q", got)
	}
	s.SetPublicURL("https://blockbook.example.org/")
	if got := s.baseURL(r); got != "https://blockbook.example.org" {
		t.Errorf("baseURL with public_url = %q", got)
	}
	checkHTML(t, "prefixed robots public_url", s.robotsTxt(), []string{"Sitemap: https://blockbook.example.org/sitemap.xml\n"}, nil)
}

func Test_faviconTarget(t *testing.T) {
	product := func(icon string) explorercontent.Snapshot {
		return explorercontent.Snapshot{Products: []explorercontent.Product{{ID: "flux", IconURL: "https://cdn.example.org/flux.png"}, {ID: "zelcore", Name: "Zelcore", IconURL: icon}}}
	}
	for _, c := range []struct {
		name string
		snap explorercontent.Snapshot
		want string
	}{
		{"no feed", explorercontent.Snapshot{}, ""},
		{"no zelcore product", explorercontent.Snapshot{Products: []explorercontent.Product{{ID: "flux", IconURL: "https://cdn.example.org/flux.png"}}}, ""},
		{"no icon", product(""), ""},
		{"icon", product("https://cdn.example.org/zelcore.png"), "https://cdn.example.org/zelcore.png"},
		{"relative icon", product("/zelcore.png"), ""},
		{"javascript icon", product("javascript:alert(1)"), ""},
		{"plain http icon", product("http://cdn.example.org/zelcore.png"), ""},
	} {
		if got := faviconTarget(c.snap); got != c.want {
			t.Errorf("%s: faviconTarget = %q, want %q", c.name, got, c.want)
		}
	}
}

func Test_setDeepPageCacheControl(t *testing.T) {
	const public = "public, max-age=86400"
	const publicTx = "public, max-age=600, stale-while-revalidate=3600"
	plain := &TemplateData{}
	withFiat := &TemplateData{SecondaryCoin: "USD"}
	for _, c := range []struct {
		name          string
		confirmations int64
		url           string
		cookie        bool
		data          *TemplateData
		want          string
	}{
		{"unconfirmed", 0, "/tx/x", false, plain, ""},
		{"young", 1, "/tx/x", false, plain, ""},
		{"at threshold", 100, "/tx/x", false, plain, ""},
		{"deep tx", 101, "/tx/x", false, plain, publicTx},
		{"deep block", 101, "/block/1", false, plain, public},
		{"very deep block", 1000000, "/block/1?page=2", false, plain, public},
		{"secondary param", 1000, "/tx/x?secondary=eur", false, plain, ""},
		{"use_secondary param", 1000, "/tx/x?use_secondary=true", false, plain, ""},
		{"empty secondary param", 1000, "/tx/x?secondary=", false, plain, ""},
		{"secondary cookie", 1000, "/tx/x", true, plain, ""},
		{"fiat rates, default currency", 1000, "/tx/x", false, withFiat, publicTx},
		{"fiat rates, chosen currency", 1000, "/tx/x", true, withFiat, ""},
		{"fiat rates, currency param", 1000, "/tx/x?secondary=eur", false, withFiat, ""},
		{"no data", 1000, "/tx/x", false, nil, ""},
	} {
		r := httptest.NewRequest("GET", c.url, nil)
		if c.cookie {
			r.AddCookie(&http.Cookie{Name: secondaryCoinCookieName, Value: "EUR=true"})
		}
		w := httptest.NewRecorder()
		cc := deepTxCacheControl
		if strings.HasPrefix(c.url, "/block/") {
			cc = deepBlockCacheControl
		}
		setDeepPageCacheControl(w, r, c.data, c.confirmations, cc)
		if got := w.Header().Get("Cache-Control"); got != c.want {
			t.Errorf("%s: Cache-Control %q, want %q", c.name, got, c.want)
		}
		wantVary := ""
		if c.want != "" {
			wantVary = "Cookie"
		}
		if got := w.Header().Get("Vary"); got != wantVary {
			t.Errorf("%s: Vary %q, want %q", c.name, got, wantVary)
		}
	}
}

// A deep page is public exactly when its response sets no cookie: without a secondary_coin cookie
// and without currency parameters the server sends the default page and leaves the cookie to
// main.js; once the visitor chose a currency the cookie is (re)set and the page stays private.
func Test_setDeepPageCacheControl_PostHandlerCookie(t *testing.T) {
	s := &PublicServer{}
	for _, c := range []struct {
		name          string
		url, cookie   string
		data          *TemplateData
		public, isSet bool
	}{
		{"no fiat rates", "/tx/x", "", &TemplateData{}, true, false},
		{"fiat rates, first visit", "/tx/x", "", &TemplateData{SecondaryCoin: "USD"}, true, false},
		{"fiat rates, cookie", "/tx/x", "EUR=true", &TemplateData{SecondaryCoin: "EUR", UseSecondaryCoin: true}, false, true},
		{"fiat rates, secondary param", "/tx/x?secondary=eur", "", &TemplateData{SecondaryCoin: "EUR"}, false, true},
		{"fiat rates, use_secondary param", "/tx/x?use_secondary=true", "", &TemplateData{SecondaryCoin: "USD", UseSecondaryCoin: true}, false, true},
	} {
		r := httptest.NewRequest("GET", c.url, nil)
		if c.cookie != "" {
			r.AddCookie(&http.Cookie{Name: secondaryCoinCookieName, Value: c.cookie})
		}
		w := httptest.NewRecorder()
		setDeepPageCacheControl(w, r, c.data, 1000, deepTxCacheControl)
		s.postHtmlTemplateHandler(c.data, w, r)
		public := strings.HasPrefix(w.Header().Get("Cache-Control"), "public")
		setCookie := w.Header().Get("Set-Cookie")
		if public != c.public || (setCookie != "") != c.isSet {
			t.Errorf("%s: public %v, Set-Cookie %q; want public %v, cookie set %v", c.name, public, setCookie, c.public, c.isSet)
		}
		if c.isSet {
			want := secondaryCoinCookieName + "=" + c.data.SecondaryCoin + "=" + map[bool]string{true: "true", false: "false"}[c.data.UseSecondaryCoin] + "; Path=/"
			if setCookie != want {
				t.Errorf("%s: Set-Cookie %q, want %q", c.name, setCookie, want)
			}
		}
	}
}

// The same through the real template handler of a server with fiat rates: a deep page requested
// without cookie or currency parameters is public, has no Set-Cookie and tells main.js the
// default cookie value; with the visitor's cookie it is private and the cookie is refreshed.
func Test_PublicServer_DeepPageFiatCookie(t *testing.T) {
	parser, chain := setupChain(t)
	s, dbpath := setupPublicHTTPServerWithFiatFixture(parser, chain, t, false, func(d *db.RocksDB) error {
		ticker := []common.CurrencyRatesTicker{{Timestamp: fixedTimeNow(), Rates: map[string]float32{"usd": 0.0237, "eur": 0.0219}}}
		return d.FiatRatesStoreSpecialTickers("CurrentTickers", &ticker)
	})
	defer closeAndDestroyPublicServer(t, s, dbpath)
	// a settled page: any template will do, the headers are what is tested
	deep := s.htmlTemplateHandler(func(w http.ResponseWriter, r *http.Request) (tpl, *TemplateData, error) {
		data := s.newTemplateData(r)
		setDeepPageCacheControl(w, r, data, 1000, deepTxCacheControl)
		return sendTransactionTpl, data, nil
	})
	for _, c := range []struct {
		name, url, cookie, wantCache, wantSetCookie, wantScript string
	}{
		{"first visit", "/x", "", "public, max-age=600, stale-while-revalidate=3600", "", `var hasSecondary=true;var secondaryCoinDefault="USD=false";`},
		{"cookie", "/x", "EUR=true", "", "secondary_coin=EUR=true; Path=/", `var secondaryCoinDefault="EUR=true";`},
		{"param", "/x?secondary=eur", "", "", "secondary_coin=EUR=false; Path=/", `var secondaryCoinDefault="EUR=false";`},
	} {
		r := httptest.NewRequest("GET", c.url, nil)
		if c.cookie != "" {
			r.AddCookie(&http.Cookie{Name: secondaryCoinCookieName, Value: c.cookie})
		}
		w := httptest.NewRecorder()
		deep(w, r)
		if got := w.Header().Get("Cache-Control"); got != c.wantCache {
			t.Errorf("%s: Cache-Control %q, want %q", c.name, got, c.wantCache)
		}
		if got := w.Header().Get("Set-Cookie"); got != c.wantSetCookie {
			t.Errorf("%s: Set-Cookie %q, want %q", c.name, got, c.wantSetCookie)
		}
		if c.wantCache != "" && w.Header().Get("Vary") != "Cookie" {
			t.Errorf("%s: Vary %q, want Cookie", c.name, w.Header().Get("Vary"))
		}
		checkHTML(t, c.name, w.Body.String(), []string{c.wantScript}, nil)
	}
}

func Test_llmsTxt_OperatorFromFeed(t *testing.T) {
	parser, chain := setupChain(t)
	s, dbpath := setupPublicHTTPServer(parser, chain, t, false)
	defer closeAndDestroyPublicServer(t, s, dbpath)
	s.ConnectFullPublicInterface()
	const base = "https://explorer.example.org"
	zelcore := explorercontent.Product{ID: "zelcore", Name: "Zelcore", TargetURL: "https://zelcore.example/?utm=x"}
	flux := explorercontent.Product{ID: "flux", Name: "Flux", TargetURL: "https://flux.example/path"}
	for _, c := range []struct {
		name     string
		products []explorercontent.Product
		want     []string
		absent   []string
	}{
		{"no feed", nil, []string{"addresses. Based on Blockbook"}, []string{"Operated by", "Powered by", "ZelCore", "Flux"}},
		{"both", []explorercontent.Product{zelcore, flux}, []string{"addresses. Operated by Zelcore (https://zelcore.example). Powered by Flux (https://flux.example). Based on Blockbook"}, nil},
		{"operator only", []explorercontent.Product{zelcore}, []string{"Operated by Zelcore (https://zelcore.example). Based on Blockbook"}, []string{"Powered by"}},
		{"platform only", []explorercontent.Product{flux}, []string{"Powered by Flux (https://flux.example). Based on Blockbook"}, []string{"Operated by"}},
		{"http target", []explorercontent.Product{{ID: "zelcore", Name: "Zelcore", TargetURL: "http://zelcore.example"}, flux}, []string{"Powered by Flux"}, []string{"Operated by", "http://"}},
		{"no name", []explorercontent.Product{{ID: "zelcore", TargetURL: "https://zelcore.example"}}, nil, []string{"Operated by"}},
		{"name with newline", []explorercontent.Product{{ID: "zelcore", Name: "Zel\n# core", TargetURL: "https://zelcore.example"}}, []string{"Operated by Zel # core (https://zelcore.example)."}, []string{"\n# core"}},
	} {
		checkHTML(t, c.name, s.llmsTxtFor(base, explorercontent.Snapshot{Products: c.products}), c.want, c.absent)
	}
}

// The page patterns are listed only when the full internal explorer serves them.
func Test_llmsTxt_PagePatterns(t *testing.T) {
	parser, chain := setupChain(t)
	s, dbpath := setupPublicHTTPServer(parser, chain, t, false)
	defer closeAndDestroyPublicServer(t, s, dbpath)
	patterns := map[string]string{"Block": "/block/{height or hash}", "Transaction": "/tx/{txid}", "Address": "/address/{address}"}
	for _, c := range []struct {
		name                    string
		internal, fullInterface bool
		listed                  bool
	}{
		{"initial sync", true, false, false},
		{"external explorer", false, true, false},
		{"full internal explorer", true, true, true},
	} {
		s.internalExplorer, s.isFullInterface = c.internal, c.fullInterface
		out := s.llmsTxt("https://explorer.example.org")
		for label, p := range patterns {
			if got := strings.Contains(out, "- "+label+": https://explorer.example.org"+p+"\n"); got != c.listed {
				t.Errorf("%s: page pattern %s listed = %v, want %v", c.name, p, got, c.listed)
			}
		}
		if !strings.Contains(out, "/status") || !strings.Contains(out, "## API") {
			t.Errorf("%s: llms.txt lost its status link or API section", c.name)
		}
	}
}
