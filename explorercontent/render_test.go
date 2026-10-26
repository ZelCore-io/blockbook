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
		for _, absent := range []string{"Sponsored", "Zelcore products", "hub-campaign", "hub-aside", "hub-zelcore-prompt", "Latest from Zelcore and SSP"} {
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
		`<section class="hub-products-grid" aria-labelledby="hub-products-title">`, `<h2 class="hub-products-title" id="hub-products-title">Zelcore products</h2>`,
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
	for _, absent := range []string{"Zelcore products", "hub-products", "Sponsored", "hub-campaign", "News from Zelcore and SSP", "hub-articles", "<dt>Price</dt>"} {
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
