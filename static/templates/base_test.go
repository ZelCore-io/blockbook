package templates

import (
	"bytes"
	"html/template"
	"os"
	"strings"
	"testing"

	"github.com/trezor/blockbook/explorercontent"
)

// These standalone render tests need only Go's standard library and the explorercontent package:
// go test static/templates/base_test.go

const testSourceURL = "https://github.com/ZelCore-io/blockbook"

func readTemplateFile(t *testing.T, name string) string {
	t.Helper()
	source, err := os.ReadFile(name)
	if os.IsNotExist(err) {
		source, err = os.ReadFile("static/templates/" + name)
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(source)
}

func renderShell(t *testing.T, data map[string]any) string {
	t.Helper()
	tmpl, err := template.New("base").Parse(readTemplateFile(t, "base.html") + `{{define "specific"}}Test page{{end}}`)
	if err != nil {
		t.Fatal(err)
	}
	full := map[string]any{"CoinLabel": "Groestlcoin", "CoinShortcut": "GRS", "InternalExplorer": true, "SourceURL": testSourceURL, "SourceLabel": "Source", "TOSLink": "/terms", "Minified": "", "ExplorerContent": explorercontent.Snapshot{}}
	for k, v := range data {
		full[k] = v
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, full); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestNeutralExplorerBranding(t *testing.T) {
	for _, label := range []string{"Groestlcoin", "Bitcoin", "Coin <Test>"} {
		for _, explorer := range []bool{false, true} {
			html := renderShell(t, map[string]any{"CoinLabel": label, "InternalExplorer": explorer})
			escaped := template.HTMLEscapeString(label)
			for _, want := range []string{`<title>` + escaped + ` Explorer</title>`, `content="` + escaped + ` Explorer"`, `<span>` + escaped + ` Explorer</span>`, "https://docs.zelcore.io", `>Source code</a>`} {
				if !strings.Contains(html, want) {
					t.Errorf("label %q: missing %q", label, want)
				}
			}
			header := strings.Split(strings.Split(html, "<header")[1], "</header>")[0]
			if strings.Contains(header, "Zelcore") {
				t.Error("operator brand remains in explorer header")
			}
		}
	}
}

func TestShellLicenseFooter(t *testing.T) {
	for _, home := range []bool{false, true} {
		for _, explorer := range []bool{false, true} {
			html := renderShell(t, map[string]any{"HomePage": home, "InternalExplorer": explorer})
			for _, want := range []string{"Based on Blockbook by SatoshiLabs · AGPL-3.0 · ", `href="` + testSourceURL + `"`, ">Source code</a>", `href="/terms"`, ">Terms of use</a>", `href="/status"`, ">Documentation</a>", `<a class="hub-footer-brand" href="https://zelcore.io" target="_blank" rel="noopener noreferrer">Zelcore</a>`, `href="https://runonflux.com" target="_blank" rel="noopener noreferrer">Powered by Flux</a>`} {
				if !strings.Contains(html, want) {
					t.Errorf("home=%v explorer=%v: missing %q", home, explorer, want)
				}
			}
			if strings.Contains(html, "Created by SatoshiLabs") {
				t.Errorf("home=%v explorer=%v: old SatoshiLabs nav item remains", home, explorer)
			}
		}
	}
}

func TestShellSourceLinkTitle(t *testing.T) {
	html := renderShell(t, map[string]any{"SourceLabel": "Source (development build)"})
	if !strings.Contains(html, `<a href="`+testSourceURL+`" title="Source (development build)" target="_blank" rel="noopener noreferrer">Source code</a>`) {
		t.Error("source link must read Source code and carry the build label as its title")
	}
}

func TestShellDefaultTheme(t *testing.T) {
	for _, home := range []bool{false, true} {
		html := renderShell(t, map[string]any{"HomePage": home})
		for _, want := range []string{`--hub-band: #2B61D1`, `<span class="hub-mark" aria-hidden="true">GRS</span>`, `<body class="hub-page">`} {
			if !strings.Contains(html, want) {
				t.Errorf("home=%v: missing %q", home, want)
			}
		}
		if strings.Contains(html, "ZgotmplZ") {
			t.Errorf("home=%v: html/template rejected a value", home)
		}
		for _, absent := range []string{"Sponsored", "Flux ecosystem products", "hub-aside", "hub-campaign"} {
			if strings.Contains(html, absent) {
				t.Errorf("home=%v: empty content renders %q", home, absent)
			}
		}
	}
}

func TestShellThemeAndIcon(t *testing.T) {
	content := explorercontent.Snapshot{Theme: &explorercontent.Theme{Color: "#1B6E3A", IconURL: "https://cdn.example.com/grs.svg"}}
	html := renderShell(t, map[string]any{"ExplorerContent": content})
	for _, want := range []string{`--hub-band: #1B6E3A`, `<img src="https://cdn.example.com/grs.svg" alt="Groestlcoin"`} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(html, "ZgotmplZ") {
		t.Error("html/template rejected a value")
	}
}

func TestShellBandKind(t *testing.T) {
	inner := renderShell(t, map[string]any{"HomePage": false})
	home := renderShell(t, map[string]any{"HomePage": true})
	if !strings.Contains(inner, `class="hub-layout"`) || !strings.Contains(inner, `<div class="hub-main">Test page</div>`) {
		t.Error("inner page is not wrapped in the hub layout")
	}
	if strings.Contains(inner, "hub-band-home") || strings.Contains(inner, "<h1") {
		t.Error("inner page renders the home band")
	}
	if strings.Count(inner, `action="/search"`) != 1 || !strings.Contains(inner, `name="q"`) {
		t.Error("inner page needs exactly one search form with the existing action and field name")
	}
	if !strings.Contains(home, `class="hub-band hub-band-home"`) || !strings.Contains(home, "<h1>Explore Groestlcoin</h1>") {
		t.Error("home page lacks the home band")
	}
	if strings.Contains(home, "hub-layout") {
		t.Error("home page must lay out its own content")
	}
	if strings.Count(home, `action="/search"`) != 1 {
		t.Error("home page needs exactly one search form")
	}
	for _, want := range []string{`href="/blocks"`, `href="/mempool"`, `href="/sendtx"`, `href="/api-docs"`} {
		if !strings.Contains(inner, want) || !strings.Contains(home, want) {
			t.Errorf("nav link %q missing", want)
		}
	}
}

func TestShellSecondaryCurrencySwitch(t *testing.T) {
	html := renderShell(t, map[string]any{"SecondaryCoin": "usd", "SecondaryCurrencies": []string{"usd", "eur"}})
	// main.js binds the switch with "#header .bb-group>.btn-check".
	if !strings.Contains(html, `id="header"`) || !strings.Contains(html, `class="btn-check" name="btnradio" id="secondary-coin"`) || !strings.Contains(html, `href="?secondary=eur&use_secondary=true"`) {
		t.Error("secondary currency switch missing")
	}
}

func TestShellNoGoogleFonts(t *testing.T) {
	html := renderShell(t, map[string]any{"Minified": ".min.7"})
	if strings.Contains(html, "fonts.googleapis") || strings.Contains(html, "fonts.gstatic") {
		t.Error("shell loads Google Fonts")
	}
	main := strings.Index(html, `href="/static/css/main.min.7.css"`)
	hub := strings.Index(html, `href="/static/css/hub.min.7.css"`)
	if main < 0 || hub < 0 || hub < main {
		t.Error("hub stylesheet must load after main.css")
	}
	css := readTemplateFile(t, "../css/hub.css")
	if strings.Contains(css, "fonts.googleapis") || !strings.Contains(css, "../fonts/") {
		t.Error("hub.css must use self-hosted fonts")
	}
}

func TestShellFooterMoreExplorers(t *testing.T) {
	content := explorercontent.Snapshot{Explorers: explorercontent.ExplorerList{
		{Coin: "bitcoin", Name: "Bitcoin", URL: "https://blockbook.btc.zelcore.io/"},
		{Coin: "evil", Name: "Coin <b>", URL: "https://example.com/e?q=\"x"},
	}}
	for _, home := range []bool{false, true} {
		html := renderShell(t, map[string]any{"HomePage": home, "ExplorerContent": content})
		footer := strings.Split(html, "<footer")[1]
		for _, want := range []string{`<nav class="hub-footer-explorers-inner" aria-label="More explorers">`, `<span class="hub-footer-explorers-title">More explorers</span>`, `<a href="https://blockbook.btc.zelcore.io/" target="_blank" rel="noopener noreferrer">Bitcoin</a>`, `href="https://example.com/e?q=%22x"`, `>Coin &lt;b&gt;</a>`} {
			if !strings.Contains(footer, want) {
				t.Errorf("home=%v: footer missing %q", home, want)
			}
		}
		if strings.Contains(html, "Sponsored") {
			t.Errorf("home=%v: footer labels explorers Sponsored", home)
		}
		if html = renderShell(t, map[string]any{"HomePage": home}); strings.Contains(html, "More explorers") {
			t.Errorf("home=%v: empty list renders the footer row", home)
		}
	}
}
