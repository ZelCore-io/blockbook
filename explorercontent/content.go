// Package explorercontent caches optional, structured dashboard content independently of blockchain indexing.
package explorercontent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const maxBytes = 64 * 1024

// JSON encoding can expand a single input byte to a six-byte escape.
const maxCacheBytes = 6*maxBytes + 4096
const maxAge = 24 * time.Hour

// defaultThemeColor is Flux Blue Wave, the band color when the feed carries no theme and the
// accent of a product tile without a color.
const defaultThemeColor = "#2B61D1"

// maxProducts is the number of product tiles the feed may serve; the strip shows the first
// mainProducts and the rest go to the "More from Flux" row.
const maxProducts = 6
const mainProducts = 4

// minCTAContrast is the WCAG AA ratio white CTA text needs on a product color to be filled.
const minCTAContrast = 4.5

var themeColorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// maxExplorers is the number of "More explorers" links kept from the feed; maxExplorerName is
// the longest explorer name in runes.
const maxExplorers = 24
const maxExplorerName = 40

var coinSlugRe = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

// Campaign is a scheduled product promotion, never executable HTML.
type Campaign struct {
	ID          string   `json:"id"`
	Placement   string   `json:"placement"`
	Coins       []string `json:"coins"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	ImageURL    string   `json:"imageUrl"`
	TargetURL   string   `json:"targetUrl"`
	StartsAt    string   `json:"startsAt"`
	EndsAt      string   `json:"endsAt"`
}

// Article is a public article selected by the dashboard.
type Article struct {
	ID           string `json:"id"`
	Source       string `json:"source"`
	Title        string `json:"title"`
	URL          string `json:"url"`
	ThumbnailURL string `json:"thumbnailUrl"`
	PublishedAt  string `json:"publishedAt"`
	Pinned       bool   `json:"pinned"`
}

// Product is a Zelcore product tile shown under "Flux ecosystem products", never as an advertisement.
// Color is optional (older feeds omit it); an invalid color is dropped when the feed is loaded.
type Product struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Tagline   string `json:"tagline"`
	TargetURL string `json:"targetUrl"`
	IconURL   string `json:"iconUrl"`
	CTALabel  string `json:"ctaLabel"`
	Color     string `json:"color,omitempty"`
}

// Accent is the tile's accent color: the product color when it is a valid #RRGGBB value,
// else Flux Blue Wave. Templates may put it in a style attribute because nothing else is returned.
func (p Product) Accent() string {
	if themeColorRe.MatchString(p.Color) {
		return p.Color
	}
	return defaultThemeColor
}

// CTAFilled reports whether the call to action is a filled button with white text, which needs
// a WCAG contrast of at least 4.5:1 between white and the accent. Otherwise it is outlined in the
// accent with dark text.
func (p Product) CTAFilled() bool {
	return contrastWithWhite(p.Accent()) >= minCTAContrast
}

// contrastWithWhite returns the WCAG 2 contrast ratio between #FFFFFF and a #RRGGBB color.
func contrastWithWhite(hex string) float64 {
	var rgb [3]float64
	for i := range rgb {
		var v int
		fmt.Sscanf(hex[1+2*i:3+2*i], "%02x", &v)
		c := float64(v) / 255
		if c <= 0.04045 {
			rgb[i] = c / 12.92
		} else {
			rgb[i] = math.Pow((c+0.055)/1.055, 2.4)
		}
	}
	l := 0.2126*rgb[0] + 0.7152*rgb[1] + 0.0722*rgb[2]
	return 1.05 / (l + 0.05)
}

// Explorer is another Zelcore explorer listed under "More explorers". It is a plain link, never
// an advertisement.
type Explorer struct {
	Coin    string `json:"coin"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	IconURL string `json:"iconUrl"`
}

func (e Explorer) valid() bool {
	return coinSlugRe.MatchString(e.Coin) && strings.TrimSpace(e.Name) != "" && utf8.ValidString(e.Name) && utf8.RuneCountInString(e.Name) <= maxExplorerName &&
		safeURL(e.URL) && (e.IconURL == "" || safeURL(e.IconURL))
}

// ExplorerList is the feed's optional "explorers" field. It never fails the feed: a missing,
// null or malformed field decodes to an empty list, an invalid or duplicate entry is dropped,
// and at most maxExplorers entries are kept.
type ExplorerList []Explorer

// UnmarshalJSON decodes the list leniently, entry by entry.
func (l *ExplorerList) UnmarshalJSON(raw []byte) error {
	*l = nil
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	seen := map[string]bool{}
	for _, item := range items {
		var e Explorer
		if json.Unmarshal(item, &e) != nil || !e.valid() || seen[e.Coin] {
			continue
		}
		seen[e.Coin] = true
		*l = append(*l, e)
		if len(*l) == maxExplorers {
			break
		}
	}
	return nil
}

// Theme is the per-coin accent color and optional mark chosen in the dashboard.
type Theme struct {
	Color   string `json:"color"`
	IconURL string `json:"iconUrl"`
}

// Snapshot is the versioned public contract shared with the dashboard.
type Snapshot struct {
	Version     int        `json:"version"`
	GeneratedAt time.Time  `json:"generatedAt"`
	Campaigns   []Campaign `json:"campaigns"`
	Articles    []Article  `json:"articles"`
	Products    []Product  `json:"products"`
	Theme       *Theme     `json:"theme"`
	// Explorers is optional; older dashboards omit it.
	Explorers ExplorerList `json:"explorers,omitempty"`
}

// campaignFor returns the first campaign with the preferred placement, else the first with the other one.
func (s Snapshot) campaignFor(first, second string) *Campaign {
	for _, placement := range []string{first, second} {
		for i := range s.Campaigns {
			if s.Campaigns[i].Placement == placement {
				return &s.Campaigns[i]
			}
		}
	}
	return nil
}

// HomeCampaign is the first hero campaign, falling back to the first sidebar campaign.
func (s Snapshot) HomeCampaign() *Campaign { return s.campaignFor("hero", "sidebar") }

// InnerCampaign is the first sidebar campaign, falling back to the first hero campaign.
func (s Snapshot) InnerCampaign() *Campaign { return s.campaignFor("sidebar", "hero") }

// ThemeColor is the coin accent color, or the default band color when the feed carries no theme.
func (s Snapshot) ThemeColor() string {
	if s.Theme == nil {
		return defaultThemeColor
	}
	return s.Theme.Color
}

// ThemeIcon is the coin mark URL, or "" when there is none.
func (s Snapshot) ThemeIcon() string {
	if s.Theme == nil {
		return ""
	}
	return s.Theme.IconURL
}

// namedProducts are the products a page can show: a tile without a name is skipped.
func (s Snapshot) namedProducts() []Product {
	var out []Product
	for _, p := range s.Products {
		if p.Name != "" {
			out = append(out, p)
		}
	}
	return out
}

// ProductsMain are the first four named products, shown in the product strip.
func (s Snapshot) ProductsMain() []Product {
	named := s.namedProducts()
	if len(named) > mainProducts {
		return named[:mainProducts]
	}
	return named
}

// ProductsMore are the named products after the first four, shown under "More from Flux".
func (s Snapshot) ProductsMore() []Product {
	named := s.namedProducts()
	if len(named) > mainProducts {
		return named[mainProducts:]
	}
	return nil
}

// FluxSymbol is the icon URL of the product with id "flux", used as the Flux symbol next to
// "Powered by Flux", or "" when the feed has no such product or it has no icon.
func (s Snapshot) FluxSymbol() string {
	for _, p := range s.Products {
		if p.ID == "flux" {
			return p.IconURL
		}
	}
	return ""
}

// ZelcoreProduct is the tile that feeds the "Watch in Zelcore" prompt, or nil.
func (s Snapshot) ZelcoreProduct() *Product {
	for i := range s.Products {
		if s.Products[i].ID == "zelcore" {
			return &s.Products[i]
		}
	}
	return nil
}

// MoreExplorers are the valid explorers to link under "More explorers", at most 24, without the
// explorer for this page's coin. The dashboard already leaves that coin out; this also drops an
// entry whose coin matches the coin label as a slug ("Bitcoin Cash" -> "bitcoin-cash") or the
// shortcut, or whose name equals the label, so a misconfigured feed never links to itself.
func (s Snapshot) MoreExplorers(coinLabel, coinShortcut string) []Explorer {
	label := strings.TrimSpace(coinLabel)
	slug := strings.ToLower(strings.Join(strings.Fields(label), "-"))
	shortcut := strings.ToLower(strings.TrimSpace(coinShortcut))
	var out []Explorer
	for _, e := range s.Explorers {
		if !e.valid() || (slug != "" && e.Coin == slug) || (shortcut != "" && e.Coin == shortcut) || (label != "" && strings.EqualFold(strings.TrimSpace(e.Name), label)) {
			continue
		}
		out = append(out, e)
		if len(out) == maxExplorers {
			break
		}
	}
	return out
}

// Client owns a bounded in-memory snapshot and an optional restart cache.
type Client struct {
	mu        sync.RWMutex
	current   Snapshot
	endpoint  string
	coin      string // the endpoint's ?coin= value: this explorer's own coin
	cachePath string
	http      *http.Client
}

// New validates an operator-provided HTTPS endpoint and loads a valid cache if present.
// No network request occurs on startup or on the explorer request path.
func New(endpoint, cachePath string) (*Client, error) {
	if !safeURL(endpoint) {
		return nil, fmt.Errorf("explorer content endpoint must be HTTPS without credentials")
	}
	c := &Client{endpoint: endpoint, coin: endpointCoin(endpoint), cachePath: cachePath, http: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("content redirects are disabled") }}}
	if cachePath != "" {
		if f, err := os.Open(cachePath); err == nil {
			defer f.Close()
			var saved struct {
				Endpoint string   `json:"endpoint"`
				Snapshot Snapshot `json:"snapshot"`
			}
			raw, err := io.ReadAll(io.LimitReader(f, maxCacheBytes+1))
			if err == nil && len(raw) <= maxCacheBytes && json.Unmarshal(raw, &saved) == nil && saved.Endpoint == endpoint {
				dropInvalidProductColors(&saved.Snapshot)
				if validate(saved.Snapshot, time.Now()) == nil {
					c.current = saved.Snapshot
				}
			}
		}
	}
	return c, nil
}

// endpointCoin is the coin slug in the operator-configured endpoint's query, or "".
func endpointCoin(endpoint string) string {
	if u, err := url.Parse(endpoint); err == nil {
		return strings.ToLower(u.Query().Get("coin"))
	}
	return ""
}

func safeURL(raw string) bool {
	u, err := url.Parse(raw)
	return len(raw) <= 2048 && err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && !strings.ContainsAny(raw, "\r\n\t")
}

// dropInvalidProductColors clears a product color that is not #RRGGBB. The product stays: the
// color is optional and only styles the tile.
func dropInvalidProductColors(s *Snapshot) {
	for i := range s.Products {
		if s.Products[i].Color != "" && !themeColorRe.MatchString(s.Products[i].Color) {
			s.Products[i].Color = ""
		}
	}
}

func validate(s Snapshot, now time.Time) error {
	if s.Version != 1 || s.GeneratedAt.IsZero() || s.GeneratedAt.After(now.Add(5*time.Minute)) || now.Sub(s.GeneratedAt) > maxAge {
		return fmt.Errorf("invalid content version or generation time")
	}
	if len(s.Campaigns) > 8 || len(s.Articles) > 6 {
		return fmt.Errorf("content item limit exceeded")
	}
	for _, c := range s.Campaigns {
		if c.ID == "" || c.Title == "" || len(c.Title) > 480 || len(c.Description) > 960 || (c.Placement != "hero" && c.Placement != "sidebar") || !safeURL(c.ImageURL) || !safeURL(c.TargetURL) {
			return fmt.Errorf("invalid campaign")
		}
		var start, end time.Time
		var err error
		if c.StartsAt != "" {
			start, err = time.Parse(time.RFC3339, c.StartsAt)
			if err != nil {
				return fmt.Errorf("invalid campaign start")
			}
		}
		if c.EndsAt != "" {
			end, err = time.Parse(time.RFC3339, c.EndsAt)
			if err != nil {
				return fmt.Errorf("invalid campaign end")
			}
		}
		if !start.IsZero() && !end.IsZero() && !end.After(start) {
			return fmt.Errorf("invalid campaign interval")
		}
	}
	if len(s.Products) > maxProducts {
		return fmt.Errorf("content item limit exceeded")
	}
	for _, p := range s.Products {
		if p.ID == "" || len(p.Name) > 160 || len(p.Tagline) > 480 || len(p.CTALabel) > 96 || !safeURL(p.TargetURL) || (p.IconURL != "" && !safeURL(p.IconURL)) {
			return fmt.Errorf("invalid product")
		}
	}
	if s.Theme != nil && (!themeColorRe.MatchString(s.Theme.Color) || (s.Theme.IconURL != "" && !safeURL(s.Theme.IconURL))) {
		return fmt.Errorf("invalid theme")
	}
	for _, a := range s.Articles {
		date, err := time.Parse(time.RFC3339, a.PublishedAt)
		if a.ID == "" || a.Title == "" || len(a.Title) > 800 || (a.Source != "zelcore" && a.Source != "ssp") || !safeURL(a.URL) || (a.ThumbnailURL != "" && !safeURL(a.ThumbnailURL)) || err != nil || date.After(now.Add(5*time.Minute)) {
			return fmt.Errorf("invalid article")
		}
	}
	return nil
}

// Refresh replaces the snapshot only after a successful bounded and validated fetch.
// A failed fetch retains the last good snapshot, subject to its maximum age.
// A disk persistence error is reported but does not discard valid fresh in-memory content.
func (c *Client) Refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("content HTTP status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > maxBytes {
		return fmt.Errorf("content exceeds 64 KiB")
	}
	var next Snapshot
	if err = json.Unmarshal(raw, &next); err != nil {
		return err
	}
	dropInvalidProductColors(&next)
	if err = validate(next, time.Now()); err != nil {
		return err
	}
	c.mu.Lock()
	c.current = next
	c.mu.Unlock()
	if c.cachePath != "" {
		saved := struct {
			Endpoint string   `json:"endpoint"`
			Snapshot Snapshot `json:"snapshot"`
		}{c.endpoint, next}
		encoded, err := json.Marshal(saved)
		if err != nil {
			return err
		}
		// Reserve space for the endpoint wrapper in addition to the public payload.
		if len(encoded) > maxCacheBytes {
			return fmt.Errorf("encoded disk content cache exceeds limit")
		}
		f, err := os.CreateTemp(filepath.Dir(c.cachePath), ".explorer-content-*")
		if err != nil {
			return err
		}
		temp := f.Name()
		defer os.Remove(temp)
		if _, err = f.Write(encoded); err != nil {
			f.Close()
			return err
		}
		if err = f.Close(); err != nil {
			return err
		}
		if err = os.Rename(temp, c.cachePath); err != nil {
			return err
		}
	}
	return nil
}

// Snapshot returns fresh copies of currently eligible items. Expiry is enforced
// locally even while the dashboard is unavailable.
func (c *Client) Snapshot(now time.Time) Snapshot {
	if c == nil {
		return Snapshot{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	s := c.current
	if s.GeneratedAt.IsZero() || now.Sub(s.GeneratedAt) > maxAge {
		return Snapshot{}
	}
	out := Snapshot{Version: s.Version, GeneratedAt: s.GeneratedAt, Articles: append([]Article(nil), s.Articles...), Products: append([]Product(nil), s.Products...)}
	if s.Theme != nil {
		theme := *s.Theme
		out.Theme = &theme
	}
	for _, e := range s.Explorers {
		if c.coin == "" || e.Coin != c.coin {
			out.Explorers = append(out.Explorers, e)
		}
	}
	for _, item := range s.Campaigns {
		if item.StartsAt != "" {
			start, _ := time.Parse(time.RFC3339, item.StartsAt)
			if now.Before(start) {
				continue
			}
		}
		if item.EndsAt != "" {
			end, _ := time.Parse(time.RFC3339, item.EndsAt)
			if !now.Before(end) {
				continue
			}
		}
		item.Coins = append([]string(nil), item.Coins...)
		out.Campaigns = append(out.Campaigns, item)
	}
	return out
}

// Run refreshes immediately then every five minutes until shutdown.
func (c *Client) Run(ctx context.Context, onError func(error)) {
	tick := time.NewTicker(5 * time.Minute)
	defer tick.Stop()
	for {
		if err := c.Refresh(ctx); err != nil && ctx.Err() == nil && onError != nil {
			onError(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
