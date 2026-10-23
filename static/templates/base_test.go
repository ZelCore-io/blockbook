package templates

import (
	"bytes"
	"html/template"
	"os"
	"strings"
	"testing"
)

// This standalone render test needs only Go's standard library:
// go test static/templates/base_test.go
func TestNeutralExplorerBranding(t *testing.T) {
	source, err := os.ReadFile("base.html")
	if os.IsNotExist(err) {
		source, err = os.ReadFile("static/templates/base.html")
	}
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := template.New("base").Parse(string(source) + `{{define "specific"}}Test page{{end}}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"Groestlcoin", "Bitcoin", "Coin <Test>"} {
		for _, explorer := range []bool{false, true} {
			var out bytes.Buffer
			err := tmpl.Execute(&out, map[string]any{"CoinLabel": label, "InternalExplorer": explorer, "SourceURL": "https://github.com/ZelCore-io/blockbook", "SourceLabel": "Source", "TOSLink": "/terms"})
			if err != nil {
				t.Fatal(err)
			}
			html := out.String()
			escaped := template.HTMLEscapeString(label)
			for _, want := range []string{`<title>` + escaped + ` Explorer</title>`, `content="` + escaped + ` Explorer"`, `<span>` + escaped + ` Explorer</span>`, "Created by SatoshiLabs", "https://zelcore.io", "https://docs.zelcore.io", "Powered by Flux", `>Source</a>`} {
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
