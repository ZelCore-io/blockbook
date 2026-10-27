package templates

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
)

func renderPaging(t *testing.T, data map[string]any) string {
	t.Helper()
	tmpl, err := template.New("paging").Parse(readTemplateFile(t, "paging.html"))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := tmpl.ExecuteTemplate(&out, "paging", data); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestPagingNoPagesRendersNothing(t *testing.T) {
	if html := renderPaging(t, map[string]any{"Page": 1, "PrevPage": 0, "NextPage": 0, "PageParams": ""}); strings.TrimSpace(html) != "" {
		t.Errorf("single page rendered %q", html)
	}
}

func TestPagingCompactMarkup(t *testing.T) {
	// page 5 of 38980, as getPagingRange builds it
	html := renderPaging(t, map[string]any{"Page": 5, "PrevPage": 4, "NextPage": 6, "PageParams": template.URL("&x=1"), "PagingRange": []int{1, 0, 4, 5, 6, 0, 38980}})
	for _, want := range []string{
		`aria-label="Pagination"`,
		`aria-label="Previous page"`, `aria-label="Next page"`,
		`<span class="hub-pg-text">Previous</span>`, `<span class="hub-pg-arrow" aria-hidden="true">&lsaquo;</span>`,
		`<span class="hub-pg-text">Next</span>`, `<span class="hub-pg-arrow" aria-hidden="true">&rsaquo;</span>`,
		`class="bb-group hub-pg-wide"`,
		`class="btn hub-pg-far" href="?page=1&amp;x=1"`,
		`class="btn hub-pg-near" href="?page=4&amp;x=1"`,
		`class="btn active" href="?page=5&amp;x=1" aria-current="page"`,
		`class="btn hub-pg-near" href="?page=6&amp;x=1"`,
		`class="btn hub-pg-far" href="?page=38980&amp;x=1"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %q in %s", want, html)
		}
	}
	if n := strings.Count(html, `class="hub-pg-gap"`); n != 2 {
		t.Errorf("want 2 gaps, got %d", n)
	}
}

func TestPagingShortRangeIsNotWide(t *testing.T) {
	html := renderPaging(t, map[string]any{"Page": 2, "PrevPage": 1, "NextPage": 3, "PageParams": "", "PagingRange": []int{1, 2, 3, 4, 0, 50}})
	if strings.Contains(html, "hub-pg-wide") {
		t.Error("a 2 digit last page must keep its neighbours on phones")
	}
	if !strings.Contains(html, `class="btn hub-pg-far" href="?page=4"`) {
		t.Error("page 4 is two away from page 2 and must be marked far")
	}
}

func TestPagingUnknownTotalKeepsStepLinks(t *testing.T) {
	html := renderPaging(t, map[string]any{"Page": 3, "PrevPage": 2, "NextPage": 4, "PageParams": ""})
	if !strings.Contains(html, `href="?page=2"`) || !strings.Contains(html, `href="?page=4"`) || strings.Contains(html, "bb-group") {
		t.Errorf("unexpected markup %s", html)
	}
}

func TestMobileTemplateHooks(t *testing.T) {
	for file, wants := range map[string][]string{
		"blocks.html":         {`hub-table-fit`, `hub-th-short">Txs<`, `hub-th-short">Time<`, `hub-col-hash`},
		"index.html":          {`<table class="hub-linkrows">`},
		"tx_bitcointype.html": {`<span class="hub-stat-num">{{formatInt $tx.Size}} B</span>`},
	} {
		src := readTemplateFile(t, file)
		for _, want := range wants {
			if !strings.Contains(src, want) {
				t.Errorf("%s: missing %q", file, want)
			}
		}
	}
	if strings.Contains(readTemplateFile(t, "block.html"), "max-width: 280px") {
		t.Error("block.html: the Merkle root cell must wrap, not be cut at a fixed width")
	}
}
