package explorercontent

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
	"time"
)

func TestExplorerContentRendering(t *testing.T) {
	tmpl, err := template.ParseFiles("../static/templates/base.html")
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err = tmpl.Parse(`{{define "specific"}}Blockchain data{{end}}`)
	if err != nil {
		t.Fatal(err)
	}
	render := func(s Snapshot) string {
		t.Helper()
		var out bytes.Buffer
		data := map[string]interface{}{"CoinLabel": "Groestlcoin", "ExplorerContent": s}
		if err := tmpl.ExecuteTemplate(&out, "base.html", data); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	disabled := render(Snapshot{})
	if strings.Contains(disabled, "Latest ecosystem articles") || strings.Contains(disabled, "Ecosystem promotion") {
		t.Fatal("disabled content visible")
	}
	s := sample(time.Now())
	s.Campaigns[0].Title = `<script>alert(1)</script>`
	s.Articles[0].Title = `<img onerror=alert(1)>`
	rendered := render(s)
	for _, want := range []string{"Blockchain data", "Ecosystem promotion", "Latest from Zelcore and SSP", "&lt;script&gt;", "&lt;img", `rel="sponsored noopener noreferrer"`, `referrerpolicy="no-referrer"`} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if strings.Contains(rendered, s.Campaigns[0].Title) || strings.Contains(rendered, s.Articles[0].Title) {
		t.Fatal("unescaped injected markup")
	}
}
