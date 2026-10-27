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
		{name: "search no results", path: "/search/?q=zzz%3Cb%3E", status: http.StatusNotFound, contentType: html, want: []string{noindexMeta, `<h1>No results</h1>`, `No matching records found for &#39;zzz&lt;b&gt;&#39;`, `Check the hash or address and try again.`, `value="zzz&lt;b&gt;" placeholder="Search for block, transaction, address or xpub" autofocus>`}, absent: []string{"<b>"}},
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
	checkHTML(t, "llms.txt", llms.body, []string{"# Fake Coin Explorer\n", "Blockchain explorer for Fakecoin (FAKE)", "ZelCore", "Flux", "Canonical host: " + ts.URL + "\n", "[API documentation](" + ts.URL + "/api-docs)", ts.URL + "/api/v2/tx/{txid}", ts.URL + "/api/v2/xpub/{xpub}", ts.URL + "/block/{height or hash}"}, nil)

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
	} {
		if got := faviconTarget(c.snap); got != c.want {
			t.Errorf("%s: faviconTarget = %q, want %q", c.name, got, c.want)
		}
	}
}

func Test_setDeepPageCacheControl(t *testing.T) {
	const public = "public, max-age=86400"
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
		{"deep", 101, "/tx/x", false, plain, public},
		{"very deep", 1000000, "/block/1?page=2", false, plain, public},
		{"secondary param", 1000, "/tx/x?secondary=eur", false, plain, ""},
		{"use_secondary param", 1000, "/tx/x?use_secondary=true", false, plain, ""},
		{"empty secondary param", 1000, "/tx/x?secondary=", false, plain, ""},
		{"secondary cookie", 1000, "/tx/x", true, plain, ""},
		{"response sets the cookie", 1000, "/tx/x", false, withFiat, ""},
		{"no data", 1000, "/tx/x", false, nil, ""},
	} {
		r := httptest.NewRequest("GET", c.url, nil)
		if c.cookie {
			r.AddCookie(&http.Cookie{Name: secondaryCoinCookieName, Value: "EUR=true"})
		}
		w := httptest.NewRecorder()
		setDeepPageCacheControl(w, r, c.data, c.confirmations)
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

// The fiat-rates path end to end: a deep page whose response sets secondary_coin is never public.
func Test_setDeepPageCacheControl_PostHandlerCookie(t *testing.T) {
	s := &PublicServer{}
	for _, data := range []*TemplateData{{}, {SecondaryCoin: "USD"}} {
		w := httptest.NewRecorder()
		setDeepPageCacheControl(w, httptest.NewRequest("GET", "/tx/x", nil), data, 1000)
		s.postHtmlTemplateHandler(data, w, httptest.NewRequest("GET", "/tx/x", nil))
		public := strings.HasPrefix(w.Header().Get("Cache-Control"), "public")
		if setCookie := w.Header().Get("Set-Cookie") != ""; public && setCookie {
			t.Errorf("SecondaryCoin %q: public Cache-Control together with Set-Cookie", data.SecondaryCoin)
		} else if !public && !setCookie {
			t.Errorf("SecondaryCoin %q: an identical-for-everyone deep page should be public", data.SecondaryCoin)
		}
	}
}
