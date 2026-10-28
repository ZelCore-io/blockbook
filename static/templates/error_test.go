package templates

import (
	"bytes"
	"html/template"
	"strings"
	"testing"

	"github.com/trezor/blockbook/explorercontent"
)

func renderErrorPage(t *testing.T, data map[string]any) string {
	t.Helper()
	tmpl, err := template.New("base").Funcs(template.FuncMap{"initials": testInitials}).Parse(readTemplateFile(t, "base.html") + readTemplateFile(t, "error.html"))
	if err != nil {
		t.Fatal(err)
	}
	full := map[string]any{"CoinLabel": "Groestlcoin", "CoinShortcut": "GRS", "InternalExplorer": true, "SourceURL": testSourceURL, "TOSLink": "/terms", "ExplorerContent": explorercontent.Snapshot{}, "Error": map[string]any{"Text": "Transaction not found"}}
	for k, v := range data {
		full[k] = v
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, full); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestErrorPage(t *testing.T) {
	for _, c := range []struct {
		name         string
		data         map[string]any
		want, absent []string
	}{
		{"internal error", map[string]any{"Robots": "noindex"}, []string{`<meta name="robots" content="noindex">`, `<h1>Error</h1>`, `id="error-q"`}, []string{"hub-error-hint", "autofocus"}},
		{"no robots", nil, []string{`<h1>Error</h1>`}, []string{`name="robots"`}},
		{"no results", map[string]any{"ErrorTitle": "No results", "ErrorHint": "Check the hash or address and try again.", "SearchQuery": `x"><b>`}, []string{`<h1>No results</h1>`, `<p class="hub-error-hint">Check the hash or address and try again.</p>`, `value="x&#34;&gt;&lt;b&gt;"`, ` autofocus>`}, []string{"<b>"}},
		{"external explorer", map[string]any{"InternalExplorer": false}, []string{`<h1>Error</h1>`}, []string{`id="error-q"`}},
	} {
		html := renderErrorPage(t, c.data)
		for _, w := range c.want {
			if !strings.Contains(html, w) {
				t.Errorf("%s: missing %q", c.name, w)
			}
		}
		for _, a := range c.absent {
			if strings.Contains(html, a) {
				t.Errorf("%s: must not contain %q", c.name, a)
			}
		}
	}
}
