package explorercontent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
