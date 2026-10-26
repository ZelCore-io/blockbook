package explorercontent

import (
	"bytes"
	"fmt"
	"html/template"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func renderShell(t *testing.T, s Snapshot, extra map[string]interface{}) string {
	t.Helper()
	tmpl, err := template.ParseFiles("../static/templates/base.html")
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err = tmpl.Parse(`{{define "specific"}}Blockchain data{{end}}`)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	data := map[string]interface{}{"CoinLabel": "Groestlcoin", "CoinShortcut": "GRS", "InternalExplorer": true, "SourceURL": "https://github.com/ZelCore-io/blockbook", "TOSLink": "/terms", "ExplorerContent": s}
	for k, v := range extra {
		data[k] = v
	}
	if err := tmpl.ExecuteTemplate(&out, "base.html", data); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// within reports whether value appears inside an element whose class attribute starts with cls,
// before that element's closeTag.
func within(html, cls, closeTag, value string) bool {
	marker := `class="` + cls
	for rest := html; ; {
		open := strings.Index(rest, marker)
		if open < 0 {
			return false
		}
		rest = rest[open+len(marker):]
		if end := strings.Index(rest, closeTag); end >= 0 && strings.Contains(rest[:end], value) {
			return true
		}
	}
}

func TestExplorerContentRendering(t *testing.T) {
	for _, home := range []bool{false, true} {
		disabled := renderShell(t, Snapshot{}, map[string]interface{}{"HomePage": home})
		for _, absent := range []string{"Sponsored", "Flux ecosystem products", "hub-campaign", "hub-aside", "hub-zelcore-prompt", "Latest from Zelcore and SSP"} {
			if strings.Contains(disabled, absent) {
				t.Fatalf("home=%v: disabled content renders %q", home, absent)
			}
		}
	}
	s := sample(time.Now())
	s.Campaigns[0].Title = `<script>alert(1)</script>`
	s.Articles[0].Title = `<script>alert(2)</script>`
	s.Products = []Product{{ID: "zelcore", Name: "Zelcore", Tagline: "Wallet", TargetURL: "https://zelcore.io"}, {ID: "ssp", Name: `<script>alert(3)</script>`, TargetURL: "https://sspwallet.io"}}
	rendered := renderShell(t, s, nil)
	for _, want := range []string{"Blockchain data", "Sponsored", "&lt;script&gt;alert(1)", "&lt;script&gt;alert(3)", `rel="sponsored noopener noreferrer"`, `referrerpolicy="no-referrer"`, `loading="lazy"`} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if strings.Contains(rendered, "<script>alert") {
		t.Fatal("unescaped injected markup")
	}
	// Articles render on the home page only (index.html), never from the shell.
	if strings.Contains(rendered, "alert(2)") {
		t.Fatal("shell renders articles")
	}
}

func TestHubAsideFallbackAndPrompt(t *testing.T) {
	heroOnly := Snapshot{Campaigns: []Campaign{{ID: "hero", Placement: "hero", Title: "Hero promo", ImageURL: "https://example.com/h.png", TargetURL: "https://example.com/hero"}}}
	html := renderShell(t, heroOnly, nil)
	if !within(html, "hub-aside", "</aside>", "Hero promo") || !strings.Contains(html, `href="https://example.com/hero"`) {
		t.Error("inner aside does not fall back to the hero campaign")
	}
	if strings.Contains(html, "hub-zelcore-prompt") {
		t.Error("prompt renders without a zelcore product")
	}

	withSidebar := heroOnly
	withSidebar.Campaigns = append([]Campaign{}, heroOnly.Campaigns...)
	withSidebar.Campaigns = append(withSidebar.Campaigns, Campaign{ID: "side", Placement: "sidebar", Title: "Side promo", ImageURL: "https://example.com/s.png", TargetURL: "https://example.com/side"})
	html = renderShell(t, withSidebar, nil)
	if !strings.Contains(html, "Side promo") || strings.Contains(html, "Hero promo") {
		t.Error("inner aside must prefer the sidebar campaign")
	}

	prompt := Snapshot{Products: []Product{{ID: "ssp", Name: "SSP Wallet", TargetURL: "https://sspwallet.io"}, {ID: "zelcore", Name: "Zelcore", Tagline: "Hold, send and swap GRS.", TargetURL: "https://zelcore.io/download"}}}
	html = renderShell(t, prompt, nil)
	if !within(html, "hub-zelcore-prompt", "</a>", "Watch in Zelcore") || !regexp.MustCompile(`<a class="hub-zelcore-prompt" href="https://zelcore.io/download"`).MatchString(html) {
		t.Error("zelcore prompt missing or not linked to the product target")
	}
	if strings.Contains(html, "Sponsored") {
		t.Error("products must never be labelled Sponsored")
	}
	if !within(html, "hub-footer", "</footer>", ">SSP Wallet</a>") || !within(html, "hub-footer", "</footer>", `href="https://zelcore.io" target="_blank" rel="noopener noreferrer">Zelcore</a>`) {
		t.Error("footer product links missing")
	}
	if strings.Contains(html, `href="https://zelcore.io/download" target="_blank" rel="noopener noreferrer">Zelcore</a>`) || strings.Count(html, ">Zelcore</a>") != 1 {
		t.Error("footer must show the fixed Zelcore link once and skip the zelcore product")
	}
	if strings.Index(html, ">Zelcore</a>") > strings.Index(html, ">SSP Wallet</a>") {
		t.Error("fixed Zelcore link must come first in the footer")
	}

	home := renderShell(t, withSidebar, map[string]interface{}{"HomePage": true})
	if strings.Contains(home, "hub-aside") {
		t.Error("home page must not render the inner aside")
	}

	unnamed := Snapshot{Products: []Product{{ID: "x", TargetURL: "https://example.com/unnamed"}}}
	if html = renderShell(t, unnamed, nil); strings.Contains(html, "https://example.com/unnamed") {
		t.Error("footer renders a product without a name")
	}

	if html = renderShell(t, Snapshot{}, nil); strings.Contains(html, "<aside") {
		t.Error("aside renders without promotions")
	}
	// An older dashboard feed has no products: the footer still shows Zelcore and Flux.
	if !within(html, "hub-footer", "</footer>", ">Zelcore</a>") || !within(html, "hub-footer", "</footer>", ">Powered by Flux</a>") {
		t.Error("footer lacks the fixed Zelcore and Flux links")
	}
}

func TestLongValuesRender(t *testing.T) {
	name := strings.Repeat("N", 40)
	tagline := strings.Repeat("T", 120)
	slug := strings.Repeat("c", 64)
	s := Snapshot{
		Campaigns: []Campaign{{ID: "side", Placement: "sidebar", Title: strings.Repeat("C", 80), ImageURL: "https://example.com/s.png", TargetURL: "https://example.com"}},
		Products:  []Product{{ID: "zelcore", Name: "Zelcore", Tagline: tagline, TargetURL: "https://zelcore.io"}, {ID: "long", Name: name, TargetURL: "https://example.com/long"}},
	}
	html := renderShell(t, s, map[string]interface{}{"CoinLabel": slug})
	checks := []struct{ cls, closeTag, value string }{
		{"hub-zelcore-prompt", "</a>", tagline},
		{"hub-campaign", "</a>", strings.Repeat("C", 80)},
		{"hub-brand", "</a>", slug},
		{"hub-footer", "</footer>", name},
	}
	for _, c := range checks {
		if !within(html, c.cls, c.closeTag, c.value) {
			t.Errorf("%d-char value not inside .%s", len(c.value), c.cls)
		}
	}
	// The containers above must wrap long unbroken strings instead of widening the page.
	css, err := os.ReadFile("../static/css/hub.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, sel := range []string{".hub-zelcore-prompt", ".hub-campaign", ".hub-brand", ".hub-footer a"} {
		rule := regexp.MustCompile(`(?s)\n` + regexp.QuoteMeta(sel) + ` \{([^}]*)\}`).FindStringSubmatch(string(css))
		if rule == nil {
			t.Errorf("%s rule missing", sel)
			continue
		}
		if !strings.Contains(rule[1], "overflow-wrap: anywhere") {
			t.Errorf("%s does not wrap long words", sel)
		}
	}
}

// renderHome renders base.html with index.html as the home page. The template functions are
// stand-ins for the server's formatters, which live in a package that needs RocksDB.
func renderHome(t *testing.T, s Snapshot, extra map[string]interface{}) string {
	t.Helper()
	format := func(v interface{}) string { return fmt.Sprint(v) }
	initials := func(name string) string {
		for _, r := range strings.TrimSpace(name) {
			return strings.ToUpper(string(r))
		}
		return ""
	}
	funcs := template.FuncMap{"formatUint32": format, "formatInt": format, "unixTimeSpan": format, "timeSpan": format, "initials": initials}
	tmpl, err := template.New("base.html").Funcs(funcs).ParseFiles("../static/templates/base.html", "../static/templates/index.html")
	if err != nil {
		t.Fatal(err)
	}
	info := map[string]interface{}{
		"Blockbook": map[string]interface{}{"BestHeight": uint32(6315496), "MempoolSize": 0, "InSync": true},
		"Backend":   map[string]interface{}{"Difficulty": "60257.33", "Subversion": "/Groestlcoin:31.0.0/"},
	}
	blocks := map[string]interface{}{"Blocks": []map[string]interface{}{{"Height": uint32(6315496), "Hash": strings.Repeat("0", 64), "Time": int64(1), "Txs": 1, "Size": 249}}}
	data := map[string]interface{}{"CoinLabel": "Groestlcoin", "CoinShortcut": "GRS", "InternalExplorer": true, "SourceURL": "https://github.com/ZelCore-io/blockbook", "TOSLink": "/terms", "ExplorerContent": s, "HomePage": true, "HomeTab": "blocks", "Info": info, "Blocks": blocks}
	for k, v := range extra {
		data[k] = v
	}
	var out bytes.Buffer
	if err := tmpl.ExecuteTemplate(&out, "base.html", data); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestHomeContentRendering(t *testing.T) {
	now := time.Now().UTC()
	s := sample(now)
	s.Campaigns[0].Title = `<script>alert(1)</script>`
	s.Articles = []Article{
		{ID: "1", Source: "zelcore", Title: `<script>alert(2)</script>`, URL: "https://example.com/1", ThumbnailURL: "https://example.com/1.png", PublishedAt: "2026-10-15T08:00:00Z"},
		{ID: "2", Source: "ssp", Title: "Kaspa is coming to SSP", URL: "https://example.com/2", PublishedAt: "2026-09-29T08:00:00Z"},
		{ID: "3", Source: "zelcore", Title: "Third", URL: "https://example.com/3", PublishedAt: "2026-09-28T08:00:00Z"},
		{ID: "4", Source: "zelcore", Title: "Fourth article", URL: "https://example.com/4", PublishedAt: "2026-09-27T08:00:00Z"},
	}
	s.Products = []Product{
		{ID: "zelcore", Name: "Zelcore", Tagline: "Hold, send and swap GRS.", TargetURL: "https://zelcore.io", CTALabel: "Get the wallet"},
		{ID: "ssp", Name: `<script>alert(3)</script>`, TargetURL: "https://sspwallet.io"},
		{ID: "blank", TargetURL: "https://example.com/blank"},
	}
	html := renderHome(t, s, map[string]interface{}{"HomePrice": "$0.0237"})
	for _, want := range []string{
		`<div><dt>Price</dt><dd title="Exchange rates provided by Coingecko">$0.0237</dd></div>`, `<div><dt>Height</dt><dd>6315496</dd></div>`, `<div><dt>Mempool</dt><dd>0 tx</dd></div>`,
		`<section class="hub-products-grid" aria-labelledby="hub-products-title">`, `<h2 class="hub-products-title" id="hub-products-title">Flux ecosystem products</h2>`,
		"&lt;script&gt;alert(1)", "&lt;script&gt;alert(2)", "&lt;script&gt;alert(3)",
		`rel="sponsored noopener noreferrer"`, `referrerpolicy="no-referrer"`, `loading="lazy"`,
		"Get the wallet", "Learn more",
		"News from Zelcore and SSP", "Zelcore · ", `<time datetime="2026-10-15T08:00:00Z">2026-10-15</time>`, "SSP · ", "Kaspa is coming to SSP", "Third",
		"<h2", "Network", "60257.33", "/Groestlcoin:31.0.0/", "Synced",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("home: missing %q", want)
		}
	}
	if strings.Contains(html, "<script>alert") {
		t.Error("home: unescaped injected markup")
	}
	if strings.Contains(html, "Fourth article") {
		t.Error("home: more than 3 articles")
	}
	if strings.Contains(html, "https://example.com/blank") {
		t.Error("home: product without a name renders")
	}
	if !within(html, "hub-campaign", "</a>", "Sponsored") || within(html, "hub-products", "</section>", "Sponsored") {
		t.Error("home: the campaign, and only the campaign, is labelled Sponsored")
	}
	if strings.Count(html, `action="/search"`) != 1 {
		t.Error("home: needs exactly one search form")
	}
	if strings.Index(html, "hub-campaign") > strings.Index(html, ">Network</h2>") {
		t.Error("home: the campaign card comes before the Network card")
	}
}

func TestHomeEmptyContent(t *testing.T) {
	html := renderHome(t, Snapshot{}, nil)
	for _, absent := range []string{"Flux ecosystem products", "hub-products", "Sponsored", "hub-campaign", "News from Zelcore and SSP", "hub-articles", "<dt>Price</dt>"} {
		if strings.Contains(html, absent) {
			t.Errorf("empty content renders %q", absent)
		}
	}
	unnamed := renderHome(t, Snapshot{Products: []Product{{ID: "x", TargetURL: "https://example.com/x"}}}, nil)
	if strings.Contains(unnamed, "hub-products") {
		t.Error("product strip renders with only unnamed products")
	}
	for _, want := range []string{"<h1>Explore Groestlcoin</h1>", ">Network</h2>", `href="/block/6315496"`} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestHomeProductInitials(t *testing.T) {
	s := Snapshot{Products: []Product{
		{ID: "elan", Name: "Élan wallet", TargetURL: "https://example.com/elan"},
		{ID: "icon", Name: "Iconic", IconURL: "https://example.com/i.png", TargetURL: "https://example.com/icon"},
	}}
	html := renderHome(t, s, nil)
	if !strings.Contains(html, `<span class="hub-product-icon" aria-hidden="true">É</span>`) {
		t.Error("product without an icon must show its multibyte initial")
	}
	if !strings.Contains(html, `<span class="hub-product-icon" aria-hidden="true"><img src="https://example.com/i.png"`) {
		t.Error("product with an icon must show the image")
	}
	if !utf8.ValidString(html) {
		t.Error("rendered page is not valid UTF-8")
	}
}

func TestHomeInitialSyncNotice(t *testing.T) {
	notice := "Application is now in initial synchronization and does not provide any data."
	info := map[string]interface{}{
		"Blockbook": map[string]interface{}{"BestHeight": uint32(1), "MempoolSize": 0, "InSync": false, "InitialSync": true},
		"Backend":   map[string]interface{}{"Version": "1"},
	}
	if html := renderHome(t, Snapshot{}, map[string]interface{}{"Info": info}); !strings.Contains(html, notice) {
		t.Error("initial sync notice missing")
	}
	if html := renderHome(t, Snapshot{}, nil); strings.Contains(html, notice) {
		t.Error("initial sync notice shown while synced")
	}
}

func TestHomeTxsViewAll(t *testing.T) {
	block := map[string]interface{}{"Height": uint32(42), "Transactions": []map[string]interface{}{{"Txid": "ab", "Blocktime": int64(1)}}}
	html := renderHome(t, Snapshot{}, map[string]interface{}{"HomeTab": "txs", "Block": block})
	if !strings.Contains(html, `<a class="hub-tabs-end" href="/block/42">View all</a>`) {
		t.Error("View all must point at the block the list comes from")
	}
	mempool := map[string]interface{}{"Mempool": []map[string]interface{}{{"Txid": "cd", "Time": int64(1)}}}
	html = renderHome(t, Snapshot{}, map[string]interface{}{"HomeTab": "txs", "MempoolTxids": mempool})
	if !strings.Contains(html, `<a class="hub-tabs-end" href="/mempool">View all</a>`) || !strings.Contains(html, `href="/tx/cd"`) {
		t.Error("View all must point at the mempool when the list comes from it")
	}
}

func TestHomeLongProductValues(t *testing.T) {
	name := strings.Repeat("N", 40)
	tagline := strings.Repeat("T", 120)
	html := renderHome(t, Snapshot{Products: []Product{{ID: "long", Name: name, Tagline: tagline, TargetURL: "https://example.com/long"}}}, nil)
	for _, v := range []string{name, tagline} {
		if !within(html, "hub-product\"", "</a>", v) {
			t.Errorf("%d-char value not inside .hub-product", len(v))
		}
	}
	css, err := os.ReadFile("../static/css/hub.css")
	if err != nil {
		t.Fatal(err)
	}
	rule := regexp.MustCompile(`(?s)\n\.hub-product \{([^}]*)\}`).FindStringSubmatch(string(css))
	if rule == nil || !strings.Contains(rule[1], "overflow-wrap: anywhere") || !strings.Contains(rule[1], "min-width: 0") {
		t.Error(".hub-product must wrap long words and be allowed to shrink")
	}
}

func sixProducts() []Product {
	return []Product{
		{ID: "zelcore", Name: "Zelcore", TargetURL: "https://zelcore.io", Color: "#1B63EF"},
		{ID: "ssp", Name: "SSP Wallet", TargetURL: "https://sspwallet.io", Color: "#F59E0B"},
		{ID: "flux", Name: "Flux", TargetURL: "https://runonflux.com", IconURL: "https://cdn.example.com/flux.svg"},
		{ID: "vpn", Name: "CumulusVPN", TargetURL: "https://example.com/vpn", Color: "#0FB9AE"},
		{ID: "os", Name: "FluxOS", TargetURL: "https://example.com/os", Color: "#36C9A5"},
		{ID: "nodes", Name: "FluxNodes", TargetURL: "https://example.com/nodes", Color: "#36C9A5"},
	}
}

func TestHomeMoreFromFluxRow(t *testing.T) {
	html := renderHome(t, Snapshot{Products: sixProducts()}, nil)
	more := strings.Index(html, `<h3 class="hub-products-more-title">More from Flux</h3>`)
	if more < 0 || !within(html, "hub-products-grid", "</section>", "More from Flux") {
		t.Fatal("More from Flux row missing inside the product strip")
	}
	for _, url := range []string{"https://zelcore.io", "https://sspwallet.io", "https://runonflux.com", "https://example.com/vpn"} {
		tile := strings.Index(html, `<a class="hub-product`)
		if i := strings.Index(html[tile:], `href="`+url+`"`); i < 0 || tile+i > more {
			t.Errorf("%s must render in the strip, before the More row", url)
		}
	}
	for _, url := range []string{"https://example.com/os", "https://example.com/nodes"} {
		if i := strings.Index(html, `href="`+url+`"`); i < more {
			t.Errorf("%s must render in the More row", url)
		}
	}
	if strings.Count(html, `<a class="hub-product`) != 6 {
		t.Errorf("want 6 tiles, got %d", strings.Count(html, `<a class="hub-product`))
	}

	legacy := renderHome(t, Snapshot{Products: sixProducts()[:4]}, nil)
	if strings.Contains(legacy, "More from Flux") || strings.Contains(legacy, "hub-products-more") {
		t.Error("4 products must not render the More row")
	}
	if strings.Count(legacy, `<a class="hub-product`) != 4 || !strings.Contains(legacy, "Flux ecosystem products") {
		t.Error("4 products must render in the strip")
	}
	// A legacy feed without colors renders every tile in Flux Blue Wave with a filled CTA.
	plain := sixProducts()[:4]
	for i := range plain {
		plain[i].Color = ""
	}
	html = renderHome(t, Snapshot{Products: plain}, nil)
	if strings.Count(html, `style="--tile-accent: #2B61D1"`) != 4 || strings.Contains(html, "hub-product-outline") {
		t.Error("tiles without a color must use Flux Blue Wave and a filled CTA")
	}
}

func TestHomeProductAccentAttribute(t *testing.T) {
	html := renderHome(t, Snapshot{Products: sixProducts()}, nil)
	for _, want := range []string{
		`<a class="hub-product" href="https://zelcore.io" target="_blank" rel="noopener noreferrer" style="--tile-accent: #1B63EF">`,
		`<a class="hub-product hub-product-outline" href="https://sspwallet.io" target="_blank" rel="noopener noreferrer" style="--tile-accent: #F59E0B">`,
		`<a class="hub-product" href="https://runonflux.com" target="_blank" rel="noopener noreferrer" style="--tile-accent: #2B61D1">`,
		`<a class="hub-product hub-product-outline" href="https://example.com/vpn" target="_blank" rel="noopener noreferrer" style="--tile-accent: #0FB9AE">`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %s", want)
		}
	}
	// A Snapshot built without the feed's validation (or a future code path that skips it)
	// still never puts an invalid color into the attribute.
	for _, bad := range []string{`red;background:url(//x)`, `#1B63EF" onmouseover="alert(1)`, `expression(alert(1))`, `#12345`} {
		p := Product{ID: "x", Name: "X", TargetURL: "https://example.com/x", Color: bad}
		html := renderHome(t, Snapshot{Products: []Product{p}}, nil)
		if !strings.Contains(html, `style="--tile-accent: #2B61D1"`) {
			t.Errorf("%q: tile must fall back to the default accent", bad)
		}
		for _, leak := range []string{"background:url", "onmouseover", "expression(", "#12345\"", "ZgotmplZ"} {
			if strings.Contains(html, leak) {
				t.Errorf("%q: rendered %q", bad, leak)
			}
		}
	}
	prompt := renderShell(t, Snapshot{Products: sixProducts()}, nil)
	if !strings.Contains(prompt, `<a class="hub-zelcore-prompt" href="https://zelcore.io" target="_blank" rel="noopener noreferrer" style="--tile-accent: #1B63EF">`) {
		t.Error("Zelcore prompt must carry the zelcore product accent")
	}
	amber := sixProducts()
	amber[0].Color = "#F59E0B"
	if prompt = renderShell(t, Snapshot{Products: amber}, nil); !strings.Contains(prompt, `<a class="hub-zelcore-prompt hub-product-outline" href="https://zelcore.io"`) {
		t.Error("a prompt accent that fails 4.5:1 with white must use dark initials")
	}
}

func TestFluxSymbolInBandAndFooter(t *testing.T) {
	for _, home := range []bool{false, true} {
		html := renderShell(t, Snapshot{Products: sixProducts()}, map[string]interface{}{"HomePage": home})
		band := `<a class="hub-powered" href="https://runonflux.com" target="_blank" rel="noopener noreferrer"><span class="hub-powered-mark"><img src="https://cdn.example.com/flux.svg" alt="Flux" width="22" height="22" referrerpolicy="no-referrer"></span>Powered by Flux</a>`
		if !within(html, "hub-band", "</header>", band) {
			t.Errorf("home=%v: band lacks the Flux symbol link", home)
		}
		if !within(html, "hub-footer", "</footer>", `<a href="https://runonflux.com" target="_blank" rel="noopener noreferrer"><img src="https://cdn.example.com/flux.svg" alt="Flux" width="24" height="24" loading="lazy" referrerpolicy="no-referrer">Powered by Flux</a>`) {
			t.Errorf("home=%v: footer lacks the Flux symbol", home)
		}

		noIcon := sixProducts()
		noIcon[2].IconURL = ""
		for name, s := range map[string]Snapshot{"no icon": {Products: noIcon}, "no flux product": {Products: sixProducts()[:2]}, "empty": {}} {
			html := renderShell(t, s, map[string]interface{}{"HomePage": home})
			if !within(html, "hub-band", "</header>", `<a class="hub-powered" href="https://runonflux.com" target="_blank" rel="noopener noreferrer">Powered by Flux</a>`) {
				t.Errorf("home=%v %s: band must show the text-only Flux link", home, name)
			}
			if strings.Contains(html, `alt="Flux"`) {
				t.Errorf("home=%v %s: renders a Flux symbol without a URL", home, name)
			}
			if !within(html, "hub-footer", "</footer>", `<a href="https://runonflux.com" target="_blank" rel="noopener noreferrer">Powered by Flux</a>`) {
				t.Errorf("home=%v %s: footer Flux link changed", home, name)
			}
		}
	}
}

func fixtureExplorers() ExplorerList {
	return ExplorerList{
		{Coin: "bitcoin", Name: "Bitcoin", URL: "https://blockbook.btc.zelcore.io/", IconURL: "https://cdn.example.com/btc.svg"},
		{Coin: "litecoin", Name: "Litecoin", URL: "https://blockbook.ltc.zelcore.io/"},
		{Coin: "hostile", Name: `<script>alert(4)</script>`, URL: `https://example.com/x?a=1"onmouseover=alert(5)`, IconURL: `https://cdn.example.com/i.svg"onerror=alert(6)`},
	}
}

func TestHomeMoreExplorers(t *testing.T) {
	html := renderHome(t, Snapshot{Explorers: fixtureExplorers()}, nil)
	for _, want := range []string{
		`<section class="hub-explorers" aria-labelledby="hub-explorers-title">`, `<h2 id="hub-explorers-title">More explorers</h2>`,
		`<a class="hub-explorer" href="https://blockbook.btc.zelcore.io/" target="_blank" rel="noopener noreferrer">`,
		`<span class="hub-explorer-icon" aria-hidden="true"><img src="https://cdn.example.com/btc.svg" alt="" width="32" height="32" loading="lazy" referrerpolicy="no-referrer"></span>`,
		`<span class="hub-explorer-icon" aria-hidden="true">L</span>`, `<span class="hub-explorer-name">Litecoin</span>`,
		"&lt;script&gt;alert(4)", `href="https://example.com/x?a=1%22onmouseover=alert%285%29"`, `src="https://cdn.example.com/i.svg%22onerror=alert%286%29"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %s", want)
		}
	}
	for _, leak := range []string{"<script>alert", `"onmouseover`, `"onerror`, "Sponsored"} {
		if strings.Contains(html, leak) {
			t.Errorf("rendered %q", leak)
		}
	}
	if !within(html, "hub-explorers\"", "</section>", "Bitcoin") || !within(html, "hub-footer-explorers\"", "</div>", ">Litecoin</a>") {
		t.Error("explorers missing from the home section or the footer row")
	}
	section, news, blocks := strings.Index(html, `class="hub-explorers"`), strings.Index(html, "News from Zelcore and SSP"), strings.Index(html, `class="hub-tabs"`)
	if section < blocks {
		t.Error("More explorers must come after the Blocks/Transactions panel")
	}
	withNews := renderHome(t, Snapshot{Explorers: fixtureExplorers(), Articles: sample(time.Now()).Articles}, nil)
	if section, news = strings.Index(withNews, `class="hub-explorers"`), strings.Index(withNews, "News from Zelcore and SSP"); news < 0 || section > news {
		t.Error("More explorers must come before the news")
	}

	empty := renderHome(t, Snapshot{}, nil)
	invalidOnly := renderHome(t, Snapshot{Explorers: ExplorerList{{Coin: "x", Name: "X", URL: "javascript:alert(1)"}}}, nil)
	for name, html := range map[string]string{"empty": empty, "invalid only": invalidOnly} {
		if strings.Contains(html, "More explorers") || strings.Contains(html, "hub-explorer") {
			t.Errorf("%s: renders the More explorers section or footer row", name)
		}
	}
}

func TestFooterMoreExplorers(t *testing.T) {
	for _, home := range []bool{false, true} {
		html := renderShell(t, Snapshot{Explorers: fixtureExplorers()}, map[string]interface{}{"HomePage": home})
		row := `<nav class="hub-footer-explorers-inner" aria-label="More explorers">`
		if !within(html, "hub-footer\"", "</footer>", row) || !within(html, "hub-footer-explorers\"", "</div>", `<a href="https://blockbook.btc.zelcore.io/" target="_blank" rel="noopener noreferrer">Bitcoin</a>`) {
			t.Errorf("home=%v: footer explorer row missing", home)
		}
		if strings.Index(html, row) > strings.Index(html, `class="hub-footer-inner"`) {
			t.Errorf("home=%v: explorer row must sit above the footer links", home)
		}
		if strings.Contains(html, "<script>alert") || strings.Contains(html, `"onmouseover`) {
			t.Errorf("home=%v: unescaped value in the footer", home)
		}
		if html = renderShell(t, Snapshot{}, map[string]interface{}{"HomePage": home}); strings.Contains(html, "More explorers") || strings.Contains(html, "hub-footer-explorers") {
			t.Errorf("home=%v: empty list renders the footer row", home)
		}
	}
	// Hostile values built without the feed's validation never reach the page.
	bad := Snapshot{Explorers: ExplorerList{{Coin: "x", Name: "X", URL: "javascript:alert(1)"}, {Coin: "y", Name: strings.Repeat("Y", 41), URL: "https://example.com/y"}}}
	if html := renderShell(t, bad, nil); strings.Contains(html, "More explorers") || strings.Contains(html, "javascript:") {
		t.Error("invalid explorers rendered")
	}
}

func TestLongExplorerNameWraps(t *testing.T) {
	name := strings.Repeat("W", 40)
	html := renderHome(t, Snapshot{Explorers: ExplorerList{{Coin: "w", Name: name, URL: "https://example.com/w"}}}, nil)
	if !within(html, "hub-explorer\"", "</a>", name) || !within(html, "hub-footer-explorers\"", "</div>", name) {
		t.Fatal("long name missing")
	}
	css, err := os.ReadFile("../static/css/hub.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, sel := range []string{".hub-explorer", ".hub-footer a"} {
		rule := regexp.MustCompile(`(?s)\n` + regexp.QuoteMeta(sel) + ` \{([^}]*)\}`).FindStringSubmatch(string(css))
		if rule == nil || !strings.Contains(rule[1], "overflow-wrap: anywhere") || !strings.Contains(rule[1], "min-height: 44px") {
			t.Errorf("%s must wrap long words and be at least 44px tall", sel)
		}
	}
	min, err := os.ReadFile("../static/css/hub.min.7.css")
	if err != nil || !strings.Contains(string(min), ".hub-explorer{") || !strings.Contains(string(min), ".hub-footer-explorers-inner{") {
		t.Error("hub.min.7.css is not rebuilt")
	}
}
