//go:build unittest

package server

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trezor/blockbook/api"
	"github.com/trezor/blockbook/common"
)

func TestSourceLink(t *testing.T) {
	const revision = "b14641d979241b93108025f2a72b4517ab4db260"
	for _, tt := range []struct{ name, revision, url, label string }{
		{"release", revision, "https://github.com/ZelCore-io/blockbook/tree/" + revision, "Source"},
		{"unknown", "unknown", "https://github.com/ZelCore-io/blockbook", "Source (development build)"},
		{"empty", "", "https://github.com/ZelCore-io/blockbook", "Source (development build)"},
		{"dirty", revision + "-dirty", "https://github.com/ZelCore-io/blockbook", "Source (development build)"},
		{"short", "b14641d9", "https://github.com/ZelCore-io/blockbook", "Source (development build)"},
		{"not hex", strings.Repeat("z", 40), "https://github.com/ZelCore-io/blockbook", "Source (development build)"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			url, label := sourceLink(tt.revision)
			if url != tt.url || label != tt.label {
				t.Fatalf("sourceLink(%q) = (%q, %q), want (%q, %q)", tt.revision, url, label, tt.url, tt.label)
			}
		})
	}
}

func TestExplorerSourceRendering(t *testing.T) {
	parser, _ := setupChain(t)
	s := &PublicServer{chainParser: parser, internalExplorer: true, is: &common.InternalState{Coin: "Fakecoin", CoinLabel: "Fake Coin"}}
	for _, debug := range []bool{false, true} {
		s.debug = debug
		templates := s.parseTemplates()
		for _, revision := range []string{"b14641d979241b93108025f2a72b4517ab4db260", "unknown", "b14641d979241b93108025f2a72b4517ab4db260-dirty"} {
			t.Run(fmt.Sprintf("debug=%v/%s", debug, revision), func(t *testing.T) {
				data := s.newTemplateData(httptest.NewRequest("GET", "/", nil))
				wantURL, wantLabel := sourceLink(common.GetVersionInfo().GitCommit)
				if data.SourceURL != wantURL || data.SourceLabel != wantLabel {
					t.Fatal("template data does not use running binary revision")
				}
				data.SourceURL, data.SourceLabel = sourceLink(revision)
				data.Error = &api.APIError{Text: "Test error"}
				data.Info = &api.SystemInfo{Blockbook: &api.BlockbookInfo{GitCommit: revision}, Backend: &common.BackendInfo{}}
				for _, page := range []tpl{indexTpl, errorTpl, sendTransactionTpl} {
					data.HomePage = page == indexTpl
					var out bytes.Buffer
					if err := templates[page].ExecuteTemplate(&out, "base.html", data); err != nil {
						t.Fatal(err)
					}
					html := out.String()
					expected := `href="` + data.SourceURL + `" target="_blank" rel="noopener noreferrer">Source code</a>`
					if !strings.Contains(html, expected) {
						t.Fatalf("page %d lacks source link %s", page, expected)
					}
					if page == indexTpl && strings.Count(html, `href="`+data.SourceURL+`"`) != 2 {
						t.Fatal("status commit must use the same source target as footer")
					}
					if (data.SourceLabel != "Source" && strings.Contains(html, "/tree/")) || strings.Contains(html, "github.com/trezor/blockbook/commit/") {
						t.Fatal("broken or upstream revision link")
					}
					if revision == "unknown" && page == indexTpl && !strings.Contains(html, "unknown (development build)</a>") {
						t.Fatal("development revision must be labelled")
					}
				}
			})
		}
	}
}
