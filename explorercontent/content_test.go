package explorercontent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sample(now time.Time) Snapshot {
	return Snapshot{Version: 1, GeneratedAt: now, Campaigns: []Campaign{{ID: "flux", Placement: "hero", Title: "Flux", ImageURL: "https://example.com/a.png", TargetURL: "https://example.com", EndsAt: now.Add(time.Hour).Format(time.RFC3339)}}, Articles: []Article{{ID: "1", Source: "zelcore", Title: "News", URL: "https://example.com/news", PublishedAt: now.Format(time.RFC3339)}}}
}
func TestRefreshCacheAndOutage(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	payload := sample(now)
	fail := false
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			http.Error(w, "offline", 503)
			return
		}
		json.NewEncoder(w).Encode(payload)
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "content.json")
	c, err := New(srv.URL, path)
	if err != nil {
		t.Fatal(err)
	}
	c.http = srv.Client()
	if err = c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(c.Snapshot(now).Campaigns) != 1 {
		t.Fatal("campaign missing")
	}
	fail = true
	if err = c.Refresh(context.Background()); err == nil {
		t.Fatal("expected offline error")
	}
	if len(c.Snapshot(now).Articles) != 1 {
		t.Fatal("outage discarded cached article")
	}
	restored, err := New(srv.URL, path)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Snapshot(now).Articles) != 1 {
		t.Fatal("restart did not restore cache")
	}
	if len(restored.Snapshot(now.Add(2*time.Hour)).Campaigns) != 0 {
		t.Fatal("expired campaign served")
	}
	if len(restored.Snapshot(now.Add(25*time.Hour)).Articles) != 0 {
		t.Fatal("stale snapshot served")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("cache permissions")
	}
}
func TestRejectUnsafeOrMalformedSnapshots(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name   string
		mutate func(*Snapshot)
	}{
		{"version", func(s *Snapshot) { s.Version = 2 }},
		{"javascript", func(s *Snapshot) { s.Campaigns[0].TargetURL = "javascript:alert(1)" }},
		{"http", func(s *Snapshot) { s.Articles[0].URL = "http://example.com" }},
		{"credential", func(s *Snapshot) { s.Articles[0].URL = "https://secret@example.com" }},
		{"source", func(s *Snapshot) { s.Articles[0].Source = "unknown" }},
		{"future", func(s *Snapshot) { s.GeneratedAt = now.Add(time.Hour) }},
		{"date", func(s *Snapshot) { s.Campaigns[0].EndsAt = "bad" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := sample(now)
			tc.mutate(&s)
			if err := validate(s, now); err == nil {
				t.Fatal("accepted unsafe snapshot")
			}
		})
	}
}
func TestDisabledAndInvalidEndpoint(t *testing.T) {
	var c *Client
	if len(c.Snapshot(time.Now()).Campaigns) != 0 {
		t.Fatal("nil client should be disabled")
	}
	if _, err := New("http://example.com", ""); err == nil {
		t.Fatal("HTTP accepted")
	}
}

func TestInvalidRefreshRetainsSnapshot(t *testing.T) {
	now := time.Now().UTC()
	payload, _ := json.Marshal(sample(now))
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(payload) }))
	defer srv.Close()
	c, _ := New(srv.URL, "")
	c.http = srv.Client()
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{[]byte("not JSON"), []byte(`{"version":2}`), make([]byte, maxBytes+1)} {
		payload = bad
		if err := c.Refresh(context.Background()); err == nil {
			t.Fatal("invalid response accepted")
		}
		if len(c.Snapshot(now).Campaigns) != 1 {
			t.Fatal("last good snapshot lost")
		}
	}
}
func TestLimitsScheduleAndIsolation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	s := sample(now)
	c := &Client{current: s}
	s.Campaigns[0].StartsAt = now.Add(time.Minute).Format(time.RFC3339)
	if len(c.Snapshot(now).Campaigns) != 0 {
		t.Fatal("future campaign shown")
	}
	if len(c.Snapshot(now.Add(time.Minute)).Campaigns) != 1 {
		t.Fatal("start boundary excluded")
	}
	if len(c.Snapshot(now.Add(time.Hour)).Campaigns) != 0 {
		t.Fatal("end boundary included")
	}
	s.Campaigns[0].Coins = []string{"grs"}
	got := c.Snapshot(now.Add(time.Minute))
	got.Campaigns[0].Coins[0] = "btc"
	got.Articles[0].Title = "changed"
	if c.current.Campaigns[0].Coins[0] != "grs" || c.current.Articles[0].Title == "changed" {
		t.Fatal("snapshot exposes mutable cached data")
	}
	s.Campaigns = make([]Campaign, 9)
	if validate(s, now) == nil {
		t.Fatal("campaign limit not enforced")
	}
	s = sample(now)
	s.Articles = make([]Article, 7)
	if validate(s, now) == nil {
		t.Fatal("article limit not enforced")
	}
}
func TestRedirectRejected(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "https://example.com", 302) }))
	defer srv.Close()
	c, _ := New(srv.URL, "")
	c.http.Transport = srv.Client().Transport
	if err := c.Refresh(context.Background()); err == nil {
		t.Fatal("redirect accepted")
	}
}
func TestCacheBoundToEndpoint(t *testing.T) {
	now := time.Now().UTC()
	path := filepath.Join(t.TempDir(), "cache.json")
	saved := struct {
		Endpoint string   `json:"endpoint"`
		Snapshot Snapshot `json:"snapshot"`
	}{"https://old.example", sample(now)}
	raw, _ := json.Marshal(saved)
	os.WriteFile(path, raw, 0600)
	c, err := New("https://new.example", path)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Snapshot(now).Articles) != 0 {
		t.Fatal("wrong coin endpoint cache reused")
	}
	saved.Endpoint = "https://new.example"
	saved.Snapshot.GeneratedAt = now.Add(-25 * time.Hour)
	raw, _ = json.Marshal(saved)
	os.WriteFile(path, raw, 0600)
	c, _ = New(saved.Endpoint, path)
	if len(c.Snapshot(now).Articles) != 0 {
		t.Fatal("stale disk cache reused")
	}
}

func TestCacheAllowsEndpointWrapperBeyondPayloadLimit(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	s := sample(now)
	// Build a valid payload close to the public limit; the endpoint wrapper is additional.
	s.Campaigns[0].Coins = []string{strings.Repeat("x", maxBytes-1000)}
	raw, _ := json.Marshal(s)
	s.Campaigns[0].Coins[0] += strings.Repeat("x", maxBytes-len(raw))
	raw, _ = json.Marshal(s)
	if len(raw) != maxBytes {
		t.Fatalf("fixture size %d", len(raw))
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(raw) }))
	defer srv.Close()
	cache := filepath.Join(t.TempDir(), "cache.json")
	c, _ := New(srv.URL, cache)
	c.http = srv.Client()
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	restored, err := New(srv.URL, cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Snapshot(now).Articles) != 1 {
		t.Fatal("maximum payload did not survive restart")
	}
}

func product(id string) Product {
	return Product{ID: id, Name: "Name " + id, Tagline: "Tagline", TargetURL: "https://example.com/" + id, IconURL: "https://example.com/" + id + ".png", CTALabel: "Learn more"}
}
func TestLegacyFeedWithoutProductsOrTheme(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	raw := `{"version":1,"generatedAt":"` + now.Format(time.RFC3339) + `","campaigns":[],"articles":[]}`
	var s Snapshot
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	if err := validate(s, now); err != nil {
		t.Fatal(err)
	}
	if s.ThemeColor() != "#2B61D1" || s.ThemeIcon() != "" || len(s.Products) != 0 || s.ZelcoreProduct() != nil || s.ProductsMain() != nil || s.ProductsMore() != nil || s.FluxSymbol() != "" {
		t.Fatal("legacy feed did not fall back to defaults")
	}
}
func TestNullIconAndThemeDecode(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	raw := `{"version":1,"generatedAt":"` + now.Format(time.RFC3339) + `","campaigns":[],"articles":[],"products":[{"id":"zelcore","name":"Zelcore","tagline":"Wallet","targetUrl":"https://zelcore.io","iconUrl":null,"ctaLabel":"Get it"}],"theme":null}`
	var s Snapshot
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	if err := validate(s, now); err != nil {
		t.Fatal(err)
	}
	if len(s.Products) != 1 || s.Products[0].IconURL != "" || s.Theme != nil || s.ThemeColor() != "#2B61D1" {
		t.Fatalf("null handling: %+v", s)
	}
	if p := s.ZelcoreProduct(); p == nil || p.ID != "zelcore" {
		t.Fatal("zelcore product missing")
	}
	raw = `{"version":1,"generatedAt":"` + now.Format(time.RFC3339) + `","theme":{"color":"#112233","iconUrl":null}}`
	s = Snapshot{}
	if err := json.Unmarshal([]byte(raw), &s); err != nil || validate(s, now) != nil || s.ThemeColor() != "#112233" || s.ThemeIcon() != "" {
		t.Fatalf("theme with null icon: %+v %v", s, err)
	}
}
func TestProductAndThemeValidation(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name   string
		mutate func(*Snapshot)
	}{
		{"seven products", func(s *Snapshot) {
			s.Products = []Product{product("a"), product("b"), product("c"), product("d"), product("e"), product("f"), product("g")}
		}},
		{"icon javascript", func(s *Snapshot) { s.Products[0].IconURL = "javascript:alert(1)" }},
		{"target http", func(s *Snapshot) { s.Products[0].TargetURL = "http://x.io" }},
		{"empty id", func(s *Snapshot) { s.Products[0].ID = "" }},
		{"long name", func(s *Snapshot) { s.Products[0].Name = strings.Repeat("n", 161) }},
		{"long tagline", func(s *Snapshot) { s.Products[0].Tagline = strings.Repeat("t", 481) }},
		{"long cta", func(s *Snapshot) { s.Products[0].CTALabel = strings.Repeat("c", 97) }},
		{"theme injection", func(s *Snapshot) { s.Theme.Color = "red;background:url(//x)" }},
		{"theme short", func(s *Snapshot) { s.Theme.Color = "#0A5A8" }},
		{"theme icon http", func(s *Snapshot) { s.Theme.IconURL = "http://x.io/i.png" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := sample(now)
			s.Products = []Product{product("a")}
			s.Theme = &Theme{Color: "#1B63EF", IconURL: "https://example.com/t.png"}
			tc.mutate(&s)
			if validate(s, now) == nil {
				t.Fatal("expected rejection")
			}
		})
	}
	s := sample(now)
	s.Products = []Product{product("a"), product("b"), product("c"), product("d")}
	s.Theme = &Theme{Color: "#0a5A8c", IconURL: "https://example.com/t.png"}
	if err := validate(s, now); err != nil {
		t.Fatal(err)
	}
	s.Products = append(s.Products, product("e"), product("f"))
	if err := validate(s, now); err != nil {
		t.Fatalf("six products: %v", err)
	}
}
func TestSlotFallback(t *testing.T) {
	hero := Campaign{ID: "h", Placement: "hero"}
	side := Campaign{ID: "s", Placement: "sidebar"}
	cases := []struct {
		name        string
		campaigns   []Campaign
		home, inner string
	}{
		{"only sidebar", []Campaign{side}, "s", "s"},
		{"only hero", []Campaign{hero}, "h", "h"},
		{"none", nil, "", ""},
		{"both", []Campaign{side, hero}, "h", "s"},
	}
	id := func(c *Campaign) string {
		if c == nil {
			return ""
		}
		return c.ID
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := Snapshot{Campaigns: tc.campaigns}
			if id(s.HomeCampaign()) != tc.home || id(s.InnerCampaign()) != tc.inner {
				t.Fatalf("home=%q inner=%q", id(s.HomeCampaign()), id(s.InnerCampaign()))
			}
		})
	}
}
func TestSnapshotCopiesProductsAndTheme(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	s := sample(now)
	s.Products = []Product{product("a")}
	s.Theme = &Theme{Color: "#1B63EF"}
	c := &Client{current: s}
	got := c.Snapshot(now)
	got.Products[0].Name = "changed"
	got.Theme.Color = "#000000"
	again := c.Snapshot(now)
	if again.Products[0].Name != "Name a" || again.Theme.Color != "#1B63EF" {
		t.Fatal("snapshot shares products or theme with the client")
	}
}

// A feed from the updated dashboard carries a color per product and up to 6 products; an
// invalid color is dropped, never the product, and survives neither refresh nor restart.
func TestProductColorFromFeed(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	raw := `{"version":1,"generatedAt":"` + now.Format(time.RFC3339) + `","products":[` +
		`{"id":"zelcore","name":"Zelcore","targetUrl":"https://zelcore.io","color":"#1B63EF"},` +
		`{"id":"ssp","name":"SSP Wallet","targetUrl":"https://sspwallet.io","color":null},` +
		`{"id":"flux","name":"Flux","targetUrl":"https://runonflux.com","iconUrl":"https://cdn.example.com/flux.svg"},` +
		`{"id":"vpn","name":"CumulusVPN","targetUrl":"https://example.com/vpn","color":"red;background:url(//x)"},` +
		`{"id":"os","name":"FluxOS","targetUrl":"https://example.com/os","color":"#36c9a5"},` +
		`{"id":"nodes","name":"FluxNodes","targetUrl":"https://example.com/nodes","color":"#36C9A"}]}`
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(raw)) }))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "content.json")
	c, _ := New(srv.URL, path)
	c.http = srv.Client()
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	restored, err := New(srv.URL, path)
	if err != nil {
		t.Fatal(err)
	}
	for name, client := range map[string]*Client{"refresh": c, "restart": restored} {
		s := client.Snapshot(now)
		got := []string{}
		for _, p := range s.Products {
			got = append(got, p.ID+"="+p.Color)
		}
		want := "zelcore=#1B63EF ssp= flux= vpn= os=#36c9a5 nodes="
		if strings.Join(got, " ") != want {
			t.Errorf("%s: colors %q, want %q", name, strings.Join(got, " "), want)
		}
		if len(s.ProductsMain()) != 4 || len(s.ProductsMore()) != 2 || s.ProductsMore()[0].ID != "os" {
			t.Errorf("%s: split %d+%d", name, len(s.ProductsMain()), len(s.ProductsMore()))
		}
		if s.FluxSymbol() != "https://cdn.example.com/flux.svg" {
			t.Errorf("%s: flux symbol %q", name, s.FluxSymbol())
		}
	}
	// A cache file written by hand with an invalid color keeps the product and drops the color.
	cached := `{"endpoint":"` + srv.URL + `","snapshot":{"version":1,"generatedAt":"` + now.Format(time.RFC3339) + `","products":[{"id":"a","name":"A","targetUrl":"https://example.com/a","color":"#12345G"}]}}`
	if err := os.WriteFile(path, []byte(cached), 0o600); err != nil {
		t.Fatal(err)
	}
	fromCache, _ := New(srv.URL, path)
	if s := fromCache.Snapshot(now); len(s.Products) != 1 || s.Products[0].Color != "" {
		t.Fatalf("cache with invalid color: %+v", s.Products)
	}
}

func TestProductAccentAndCTA(t *testing.T) {
	cases := []struct {
		color, accent string
		filled        bool
	}{
		{"", "#2B61D1", true},         // missing: Flux Blue Wave, 5.63:1
		{"#2B61D1", "#2B61D1", true},  // Flux
		{"#1B63EF", "#1B63EF", true},  // Zelcore, 5.15:1
		{"#F59E0B", "#F59E0B", false}, // SSP, 2.15:1
		{"#0FB9AE", "#0FB9AE", false}, // CumulusVPN, 2.45:1
		{"#36C9A5", "#36C9A5", false}, // FluxOS / FluxNodes, 2.09:1
		{"#767676", "#767676", true},  // 4.54:1, just above the line
		{"#777777", "#777777", false}, // 4.48:1, just below
		{"#000000", "#000000", true},
		{"#FFFFFF", "#FFFFFF", false},
		{"red", "#2B61D1", true}, // never reaches a template unvalidated
		{"#2B61D1;x:y", "#2B61D1", true},
	}
	for _, tc := range cases {
		p := Product{Color: tc.color}
		if p.Accent() != tc.accent || p.CTAFilled() != tc.filled {
			t.Errorf("%q: accent %q filled %v, want %q %v", tc.color, p.Accent(), p.CTAFilled(), tc.accent, tc.filled)
		}
	}
	for hex, want := range map[string]float64{"#2B61D1": 5.63, "#1B63EF": 5.15, "#F59E0B": 2.15, "#0FB9AE": 2.45, "#36C9A5": 2.09, "#000000": 21, "#FFFFFF": 1} {
		if got := contrastWithWhite(hex); got < want-0.01 || got > want+0.01 {
			t.Errorf("contrast %s = %.3f, want %.2f", hex, got, want)
		}
	}
}

func TestProductsMainAndMore(t *testing.T) {
	four := Snapshot{Products: []Product{product("a"), product("b"), product("c"), product("d")}}
	if len(four.ProductsMain()) != 4 || four.ProductsMore() != nil {
		t.Error("a legacy 4-product feed has no More row")
	}
	six := Snapshot{Products: []Product{product("a"), product("b"), product("c"), product("d"), product("e"), product("f")}}
	if m, more := six.ProductsMain(), six.ProductsMore(); len(m) != 4 || m[3].ID != "d" || len(more) != 2 || more[0].ID != "e" || more[1].ID != "f" {
		t.Errorf("6 products split into %d + %d", len(m), len(more))
	}
	unnamed := product("b")
	unnamed.Name = ""
	gap := Snapshot{Products: []Product{product("a"), unnamed, product("c"), product("d"), product("e")}}
	if m := gap.ProductsMain(); len(m) != 4 || m[1].ID != "c" || gap.ProductsMore() != nil {
		t.Error("an unnamed product must not take a slot")
	}
	if (Snapshot{}).ProductsMain() != nil {
		t.Error("empty snapshot has products")
	}
}

func TestFluxSymbol(t *testing.T) {
	flux := product("flux")
	if got := (Snapshot{Products: []Product{product("zelcore"), flux}}).FluxSymbol(); got != "https://example.com/flux.png" {
		t.Errorf("flux symbol %q", got)
	}
	flux.IconURL = ""
	if got := (Snapshot{Products: []Product{flux}}).FluxSymbol(); got != "" {
		t.Errorf("flux without icon: %q", got)
	}
	if got := (Snapshot{Products: []Product{product("zelcore")}}).FluxSymbol(); got != "" {
		t.Errorf("no flux product: %q", got)
	}
}
