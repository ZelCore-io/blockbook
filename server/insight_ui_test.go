//go:build unittest

package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/trezor/blockbook/tests/dbtestdata"
)

// insightUIStub stands in for the public server's handler.
var insightUIStub = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Public", "1")
	if r.URL.Path == "/nope" {
		http.NotFound(w, r)
		return
	}
	_, _ = io.WriteString(w, "public "+r.Method+" "+r.URL.Path)
})

// Without -insightui an unknown path is still the listener's own JSON 404.
func TestInsightUIOffKeepsInsight404(t *testing.T) {
	s := insightTestServer(t, insightFixture())
	for _, path := range []string{"/", "/tx/a", "/api/v2/tx/a", "/static/css/main.css", "/websocket", "/nope"} {
		w := insightRequest(s, "GET", path, "")
		if w.Code != 404 || w.Body.String() != "{\"status\":\"error\",\"error\":\"Route not found\"}\n" || w.Header().Get("Access-Control-Allow-Origin") != "*" || w.Header().Get("X-Public") != "" {
			t.Fatalf("GET %s: %d %q %v", path, w.Code, w.Body.String(), w.Header())
		}
	}
}

func TestInsightUIFallbackRouting(t *testing.T) {
	s := insightTestServer(t, insightFixture())
	if err := s.SetFallback(insightUIStub); err != nil {
		t.Fatal(err)
	}
	// Insight routes, including paths the public server also serves, stay Insight.
	for _, path := range []string{"/api/status", "/api/tx/a", "/insight-api/tx/a", "/api/addr/A", "/api/blocks", "/api/block-index/1"} {
		w := insightRequest(s, "GET", path, "")
		if w.Header().Get("X-Public") != "" || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") || !strings.Contains(w.Header().Get("Link"), `rel="source"`) {
			t.Fatalf("GET %s left Insight: %d %q", path, w.Code, w.Body.String())
		}
	}
	// Wrong methods and preflights on Insight routes are still answered by Insight.
	if w := insightRequest(s, "DELETE", "/api/status", ""); w.Code != 405 || w.Header().Get("X-Public") != "" {
		t.Fatalf("DELETE /api/status: %d %v", w.Code, w.Header())
	}
	if w := insightRequest(s, "OPTIONS", "/api/tx/a", ""); w.Code != 204 || w.Header().Get("X-Public") != "" {
		t.Fatalf("OPTIONS /api/tx/a: %d %v", w.Code, w.Header())
	}
	for _, tc := range []struct{ method, path string }{
		{"GET", "/"},
		{"GET", "/tx/a"},
		{"GET", "/block/1"},
		{"GET", "/api/v2/tx/a"},
		{"GET", "/api/v2/address/A"},
		{"POST", "/api/v2/sendtx/"},
		{"GET", "/static/css/main.css"},
		{"GET", "/websocket"},
		{"GET", "/robots.txt"},
		{"GET", "/api/v1/tx/a"},
		{"GET", "/api/v2"},
	} {
		w := insightRequest(s, tc.method, tc.path, "")
		if w.Code != 200 || w.Body.String() != "public "+tc.method+" "+tc.path || w.Header().Get("X-Public") != "1" {
			t.Fatalf("%s %s: %d %q", tc.method, tc.path, w.Code, w.Body.String())
		}
		// Insight's CORS and source headers are not forced onto public responses.
		if w.Header().Get("Access-Control-Allow-Origin") != "" || w.Header().Get("Link") != "" {
			t.Fatalf("%s %s carries Insight headers: %v", tc.method, tc.path, w.Header())
		}
	}
	// Unknown paths under the Insight prefixes (other than Blockbook's /api/v1/
	// and /api/v2/) are the Insight JSON 404 with CORS headers, as on stock,
	// not the public server's status page or its unversioned API.
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/nosuchroute"},
		{"GET", "/api/tx/a/b"},
		{"GET", "/api"},
		{"GET", "/api/"},
		{"GET", "/api/xpub/x"},
		{"POST", "/api/sendtx/"},
		{"GET", "/insight-api"},
		{"GET", "/insight-api/v2/tx/a"},
		{"OPTIONS", "/api/nosuchroute"},
	} {
		w := insightRequest(s, tc.method, tc.path, "")
		if w.Code != 404 || w.Body.String() != "{\"status\":\"error\",\"error\":\"Route not found\"}\n" || w.Header().Get("X-Public") != "" || w.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Fatalf("%s %s: %d %q %v", tc.method, tc.path, w.Code, w.Body.String(), w.Header())
		}
	}
	// A truly unknown path gets the public server's 404, not the Insight JSON.
	if w := insightRequest(s, "GET", "/nope", ""); w.Code != 404 || w.Header().Get("X-Public") != "1" || strings.Contains(w.Body.String(), "Route not found") {
		t.Fatalf("GET /nope: %d %q", w.Code, w.Body.String())
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown(context.Background())
	if err := s.SetFallback(insightUIStub); err == nil {
		t.Fatal("fallback replaced after Start")
	}
}

// Fallthrough requests take neither concurrency nor body-reader slots and do
// not wait for Insight readiness.
func TestInsightUIFallbackTakesNoSlots(t *testing.T) {
	s := insightTestServer(t, insightFixture())
	if err := s.SetFallback(insightUIStub); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < cap(s.slots); i++ {
		s.slots <- struct{}{}
	}
	for i := 0; i < cap(s.readers); i++ {
		s.readers <- struct{}{}
	}
	if w := insightRequest(s, "GET", "/api/status", ""); w.Code != 503 {
		t.Fatalf("Insight route with full slots: %d", w.Code)
	}
	if w := insightRequest(s, "POST", "/api/tx/send", `{"rawtx":"00"}`); w.Code != 503 {
		t.Fatalf("Insight POST with full readers: %d", w.Code)
	}
	for _, tc := range []struct{ method, path string }{{"GET", "/tx/a"}, {"POST", "/api/v2/sendtx/"}} {
		if w := insightRequest(s, tc.method, tc.path, "00"); w.Code != 200 || w.Header().Get("X-Public") != "1" {
			t.Fatalf("%s %s with full slots: %d %q", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
	s.ready.Store(false)
	if w := insightRequest(s, "GET", "/tx/a", ""); w.Code != 200 {
		t.Fatalf("fallthrough while Insight unready: %d", w.Code)
	}
	if len(s.slots) != cap(s.slots) || len(s.readers) != cap(s.readers) {
		t.Fatal("slot accounting changed")
	}
}

// Over a real listener, a fallthrough response may take longer than the
// listener's WriteTimeout; an Insight route may not.
func TestInsightUIFallbackLiftsWriteTimeout(t *testing.T) {
	f := insightFixture()
	s := insightTestServer(t, f)
	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(600 * time.Millisecond)
		_, _ = io.WriteString(w, "slow page")
	})
	if err := s.SetFallback(slow); err != nil {
		t.Fatal(err)
	}
	s.http.WriteTimeout = 200 * time.Millisecond
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown(context.Background())
	base := "http://" + s.listener.Addr().String()
	resp, err := http.Get(base + "/tx/a")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 || string(body) != "slow page" {
		t.Fatalf("slow fallthrough: %v %d %q", err, resp.StatusCode, body)
	}
	// Control: the same delay on an Insight route still hits the listener's
	// deadline. A fresh connection, as net/http retries a GET on a reused one.
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	f.enter = make(chan struct{}, 1)
	f.release = make(chan struct{})
	go func() { <-f.enter; time.Sleep(600 * time.Millisecond); close(f.release) }()
	if resp, err = client.Get(base + "/api/status"); err == nil {
		_, err = io.ReadAll(resp.Body)
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("Insight route outlived the listener WriteTimeout")
	}
}

// The real public handler behind the listener: explorer pages with the
// public_url canonical, robots.txt, /api/v2, static assets and the websocket,
// all through the Insight port with short listener timeouts. Its name avoids
// "Insight": the -race -run Insight pass would otherwise trip on the existing
// setupRocksDB fixture race (LoadInternalState's goroutine), not on this code.
func TestListenerFallbackServesPublicExplorer(t *testing.T) {
	parser, chain := setupChain(t)
	public, dbpath := setupPublicHTTPServer(parser, chain, t, false)
	defer closeAndDestroyPublicServer(t, public, dbpath)
	public.SetPublicURL("https://blockbook.example.org")
	defer public.SetPublicURL("")
	public.ConnectFullPublicInterface()

	s := insightTestServer(t, insightFixture())
	if err := s.SetFallback(public.Handler()); err != nil {
		t.Fatal(err)
	}
	s.http.ReadTimeout = 300 * time.Millisecond
	s.http.WriteTimeout = 300 * time.Millisecond
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown(context.Background())
	addr := s.listener.Addr().String()
	get := func(path string) (int, http.Header, string) {
		t.Helper()
		req, err := http.NewRequest("GET", "http://"+addr+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		// The second hostname HAProxy forwards to the listener.
		req.Host = "explorer.example.net"
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, resp.Header, string(b)
	}

	code, _, page := get("/block/225494")
	checkHTML(t, "block via listener", page, []string{`<link rel="canonical" href="https://blockbook.example.org/block/225494">`}, []string{"explorer.example.net"})
	if code != 200 {
		t.Fatalf("block page: %d", code)
	}
	code, _, page = get("/")
	checkHTML(t, "index via listener", page, []string{`<link rel="canonical" href="https://blockbook.example.org/">`, `<meta property="og:url" content="https://blockbook.example.org/">`}, []string{"explorer.example.net"})
	if code != 200 {
		t.Fatalf("index page: %d", code)
	}
	_, _, robots := get("/robots.txt")
	checkHTML(t, "robots via listener", robots, []string{"Sitemap: https://blockbook.example.org/sitemap.xml\n"}, []string{"explorer.example.net"})
	_, _, sitemap := get("/sitemap.xml")
	checkHTML(t, "sitemap via listener", sitemap, []string{"<loc>https://blockbook.example.org/</loc>"}, []string{"explorer.example.net"})
	_, _, llms := get("/llms.txt")
	checkHTML(t, "llms.txt via listener", llms, []string{"Canonical host: https://blockbook.example.org\n"}, []string{"explorer.example.net"})

	code, _, body := get("/api/v2/tx/" + dbtestdata.TxidB1T1)
	if code != 200 || !strings.Contains(body, `"txid":"`+dbtestdata.TxidB1T1+`"`) || !strings.Contains(body, `"vin"`) {
		t.Fatalf("api/v2 tx: %d %s", code, body)
	}
	// Insight still owns /api/tx/<id> (its fake worker has no such tx).
	code, hdr, body := get("/api/tx/" + dbtestdata.TxidB1T1)
	if !strings.Contains(hdr.Get("Link"), `rel="source"`) || strings.Contains(body, `"vin"`) {
		t.Fatalf("api/tx left Insight: %d %s", code, body)
	}
	code, hdr, _ = get("/static/css/main.css")
	if code != 200 || !strings.HasPrefix(hdr.Get("Content-Type"), "text/css") {
		t.Fatalf("static asset: %d %v", code, hdr)
	}
	code, _, body = get("/no-such-page")
	if code != 404 || strings.Contains(body, "Route not found") {
		t.Fatalf("unknown path: %d %s", code, body)
	}

	// The websocket upgrades through the listener and outlives its read and
	// write timeouts.
	conn, resp, err := websocket.DefaultDialer.Dial("ws://"+addr+"/websocket", http.Header{"Host": {"explorer.example.net"}})
	if err != nil {
		t.Fatalf("websocket dial: %v %v", err, resp)
	}
	defer conn.Close()
	for i := 0; i < 2; i++ {
		time.Sleep(500 * time.Millisecond)
		if err := conn.WriteJSON(map[string]interface{}{"id": "1", "method": "ping", "params": map[string]interface{}{}}); err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		var res struct {
			ID   string          `json:"id"`
			Data json.RawMessage `json:"data"`
		}
		if err := conn.ReadJSON(&res); err != nil || res.ID != "1" {
			t.Fatalf("websocket ping %d: %v %+v", i, err, res)
		}
	}
}
