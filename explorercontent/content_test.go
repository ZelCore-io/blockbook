package explorercontent

import (
	"context"
	"encoding/json"
	"fmt"
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
	if s.ThemeColor() != "#2B61D1" || s.ThemeIcon() != "" || len(s.Products) != 0 || s.ZelcoreProduct() != nil || s.ProductTiles() != nil || s.ProductsScroll() || s.FluxSymbol() != "" {
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
		if tiles := s.ProductTiles(); len(tiles) != 6 || tiles[4].ID != "os" || !s.ProductsScroll() {
			t.Errorf("%s: %d tiles, scroll %v", name, len(tiles), s.ProductsScroll())
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

func TestProductTilesAndScroll(t *testing.T) {
	four := Snapshot{Products: []Product{product("a"), product("b"), product("c"), product("d")}}
	if len(four.ProductTiles()) != 4 || four.ProductsScroll() {
		t.Error("a 4-product feed fits without scrolling")
	}
	six := Snapshot{Products: []Product{product("a"), product("b"), product("c"), product("d"), product("e"), product("f")}}
	if tiles := six.ProductTiles(); len(tiles) != 6 || tiles[5].ID != "f" || !six.ProductsScroll() {
		t.Errorf("6 products: %d tiles, scroll %v", len(tiles), six.ProductsScroll())
	}
	unnamed := product("b")
	unnamed.Name = ""
	gap := Snapshot{Products: []Product{product("a"), unnamed, product("c"), product("d"), product("e")}}
	if tiles := gap.ProductTiles(); len(tiles) != 4 || tiles[1].ID != "c" || gap.ProductsScroll() {
		t.Error("an unnamed product must not take a slot")
	}
	if (Snapshot{}).ProductTiles() != nil || (Snapshot{}).ProductsScroll() {
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

// The "explorers" field never fails the feed: a legacy feed has none, a malformed field is an
// empty list, and bad entries are dropped one by one.
func TestExplorersFromFeed(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	head := `{"version":1,"generatedAt":"` + now.Format(time.RFC3339) + `"`
	decode := func(t *testing.T, raw string) Snapshot {
		t.Helper()
		var s Snapshot
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			t.Fatalf("feed rejected: %v", err)
		}
		if err := validate(s, now); err != nil {
			t.Fatalf("feed invalid: %v", err)
		}
		return s
	}
	for name, raw := range map[string]string{
		"legacy": head + `}`, "null": head + `,"explorers":null}`, "object": head + `,"explorers":{"coin":"bitcoin"}}`,
		"string": head + `,"explorers":"https://example.com"}`, "number": head + `,"explorers":7}`,
	} {
		if s := decode(t, raw); len(s.Explorers) != 0 || s.MoreExplorers() != nil {
			t.Errorf("%s: want no explorers, got %+v", name, s.Explorers)
		}
	}

	raw := head + `,"explorers":[` +
		`{"coin":"bitcoin","name":"Bitcoin","url":"https://blockbook.btc.zelcore.io/","iconUrl":null},` +
		`{"coin":"evil","name":"Evil","url":"javascript:alert(1)","iconUrl":null},` +
		`{"coin":"plain","name":"Plain","url":"http://example.com/"},` +
		`{"coin":"long","name":"` + strings.Repeat("L", 41) + `","url":"https://example.com/long"},` +
		`{"coin":"blank","name":"   ","url":"https://example.com/blank"},` +
		`{"coin":"Upper","name":"Upper","url":"https://example.com/upper"},` +
		`{"coin":"badicon","name":"Bad icon","url":"https://example.com/bi","iconUrl":"javascript:alert(2)"},` +
		`{"coin":"creds","name":"Creds","url":"https://user:pw@example.com/"},` +
		`{"coin":"typed","name":5,"url":"https://example.com/typed"},` +
		`"not an object",null,` +
		`{"coin":"bitcoin","name":"Bitcoin again","url":"https://example.com/dupe"},` +
		`{"coin":"kaspa","name":"` + strings.Repeat("é", 40) + `","url":"https://example.com/k?a=1\"onmouseover=alert(3)","iconUrl":"https://cdn.example.com/kas.svg","order":3,"enabled":true}` +
		`],"products":[{"id":"zelcore","name":"Zelcore","targetUrl":"https://zelcore.io"}]}`
	s := decode(t, raw)
	if len(s.Explorers) != 2 || s.Explorers[0].Name != "Bitcoin" || s.Explorers[0].IconURL != "" || s.Explorers[1].Coin != "kaspa" || s.Explorers[1].IconURL != "https://cdn.example.com/kas.svg" {
		t.Fatalf("want bitcoin and kaspa only, got %+v", s.Explorers)
	}
	if len(s.Products) != 1 {
		t.Fatal("a bad explorer must not affect the rest of the feed")
	}

	var many []string
	for i := 0; i < 30; i++ {
		many = append(many, fmt.Sprintf(`{"coin":"c%d","name":"Coin %d","url":"https://example.com/%d"}`, i, i, i))
	}
	s = decode(t, head+`,"explorers":[`+strings.Join(many, ",")+`]}`)
	if len(s.Explorers) != maxExplorers || s.Explorers[23].Coin != "c23" || len(s.MoreExplorers()) != maxExplorers {
		t.Fatalf("want the first %d explorers, got %d", maxExplorers, len(s.Explorers))
	}
}

// MoreExplorers re-checks entries but does not guess the page's coin: only the client drops it,
// from the endpoint's ?coin= value.
func TestMoreExplorersSkipsInvalidOnly(t *testing.T) {
	s := Snapshot{Explorers: ExplorerList{
		{Coin: "groestlcoin", Name: "Groestlcoin", URL: "https://example.com/grs"},
		{Coin: "bitcoin-cash", Name: "Bitcoin Cash", URL: "https://example.com/bch"},
		{Coin: "bad", Name: "Bad", URL: "javascript:alert(1)"},
		{Coin: "Upper", Name: "Upper", URL: "https://example.com/upper"},
		{Coin: "bitcoin", Name: "Bitcoin", URL: "https://example.com/btc", IconURL: "http://example.com/i.png"},
	}}
	got := s.MoreExplorers()
	if len(got) != 2 || got[0].Coin != "groestlcoin" || got[1].Coin != "bitcoin-cash" {
		t.Fatalf("got %+v", got)
	}
}

func TestClientSnapshotExplorers(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	s := sample(now)
	s.Explorers = ExplorerList{{Coin: "groestlcoin", Name: "Groestlcoin", URL: "https://example.com/grs"}, {Coin: "bitcoin", Name: "Bitcoin", URL: "https://example.com/btc"}}
	payload, _ := json.Marshal(s)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(payload) }))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "content.json")
	c, err := New(srv.URL+"/api/public/explorer/v1/config?coin=groestlcoin", path)
	if err != nil {
		t.Fatal(err)
	}
	c.http = srv.Client()
	if err = c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := c.Snapshot(now)
	if len(got.Explorers) != 1 || got.Explorers[0].Coin != "bitcoin" {
		t.Fatalf("the endpoint's own coin must be dropped: %+v", got.Explorers)
	}
	got.Explorers[0].Name = "changed"
	if c.Snapshot(now).Explorers[0].Name != "Bitcoin" {
		t.Fatal("snapshot shares explorers with the client")
	}
	restored, err := New(srv.URL+"/api/public/explorer/v1/config?coin=groestlcoin", path)
	if err != nil {
		t.Fatal(err)
	}
	if e := restored.Snapshot(now).Explorers; len(e) != 1 || e[0].URL != "https://example.com/btc" {
		t.Fatalf("restart cache lost explorers: %+v", e)
	}
}

// Own-coin exclusion uses only the endpoint's ?coin= value, compared in lower case. Without it,
// nothing is excluded: the dashboard's own filter is then the only one.
func TestClientOwnCoinFromEndpoint(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	s := sample(now)
	s.Explorers = ExplorerList{{Coin: "groestlcoin", Name: "Groestlcoin", URL: "https://example.com/grs"}, {Coin: "bitcoin", Name: "Bitcoin", URL: "https://example.com/btc"}}
	payload, _ := json.Marshal(s)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(payload) }))
	defer srv.Close()
	for query, want := range map[string][]string{
		"":                   {"groestlcoin", "bitcoin"},
		"?coin=GROESTLCOIN":  {"bitcoin"},
		"?coin=Groestlcoin":  {"bitcoin"},
		"?other=groestlcoin": {"groestlcoin", "bitcoin"},
	} {
		c, err := New(srv.URL+"/api/public/explorer/v1/config"+query, "")
		if err != nil {
			t.Fatal(err)
		}
		c.http = srv.Client()
		if err = c.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, e := range c.Snapshot(now).Explorers {
			got = append(got, e.Coin)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%q: got %v, want %v", query, got, want)
		}
	}
}

// Invisible and bidi format characters (category Cf) are stripped from names before the checks,
// so a name made only of them is dropped and a 40-letter name padded with them still fits.
func TestExplorerNameFormatCharacters(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	entry := func(coin, name string) string {
		b, _ := json.Marshal(map[string]string{"coin": coin, "name": name, "url": "https://example.com/" + coin})
		return string(b)
	}
	raw := `{"version":1,"generatedAt":"` + now.Format(time.RFC3339) + `","explorers":[` + strings.Join([]string{
		entry("zwsp", "\u200b\u200b\u200b"),
		entry("bidi", "\u202e\u2066 \u2069"),
		entry("bom", "\ufeff"),
		entry("rlo", "Bit\u202ecoin"),
		entry("padded", strings.Repeat("P", 40)+"\u200b\u200d"),
		entry("toolong", strings.Repeat("Q", 41)+"\u200b"),
	}, ",") + `]}`
	var s Snapshot
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Explorers) != 2 || s.Explorers[0].Name != "Bitcoin" || s.Explorers[1].Name != strings.Repeat("P", 40) {
		t.Fatalf("got %+v", s.Explorers)
	}
	// A hand-built Snapshot goes through the same cleaning at render time.
	hand := Snapshot{Explorers: ExplorerList{{Coin: "a", Name: "\u200b", URL: "https://example.com/a"}, {Coin: "b", Name: "B\u202e", URL: "https://example.com/b"}}}
	if got := hand.MoreExplorers(); len(got) != 1 || got[0].Name != "B" || hand.Explorers[1].Name != "B\u202e" {
		t.Fatalf("render-time cleaning: %+v", got)
	}
}
