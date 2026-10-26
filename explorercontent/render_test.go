package explorercontent

import (
	"bytes"
	"html/template"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
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
