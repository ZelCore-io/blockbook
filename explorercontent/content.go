// Package explorercontent caches optional, structured dashboard content independently of blockchain indexing.
package explorercontent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const maxBytes = 64 * 1024
const maxCacheBytes = maxBytes + 4096
const maxAge = 24 * time.Hour

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

// Snapshot is the versioned public contract shared with the dashboard.
type Snapshot struct {
	Version     int        `json:"version"`
	GeneratedAt time.Time  `json:"generatedAt"`
	Campaigns   []Campaign `json:"campaigns"`
	Articles    []Article  `json:"articles"`
}

// Client owns a bounded in-memory snapshot and an optional restart cache.
type Client struct {
	mu        sync.RWMutex
	current   Snapshot
	endpoint  string
	cachePath string
	http      *http.Client
}

// New validates an operator-provided HTTPS endpoint and loads a valid cache if present.
// No network request occurs on startup or on the explorer request path.
func New(endpoint, cachePath string) (*Client, error) {
	if !safeURL(endpoint) {
		return nil, fmt.Errorf("explorer content endpoint must be HTTPS without credentials")
	}
	c := &Client{endpoint: endpoint, cachePath: cachePath, http: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("content redirects are disabled") }}}
	if cachePath != "" {
		if f, err := os.Open(cachePath); err == nil {
			defer f.Close()
			var saved struct {
				Endpoint string   `json:"endpoint"`
				Snapshot Snapshot `json:"snapshot"`
			}
			raw, err := io.ReadAll(io.LimitReader(f, maxCacheBytes+1))
			if err == nil && len(raw) <= maxCacheBytes && json.Unmarshal(raw, &saved) == nil && saved.Endpoint == endpoint && validate(saved.Snapshot, time.Now()) == nil {
				c.current = saved.Snapshot
			}
		}
	}
	return c, nil
}

func safeURL(raw string) bool {
	u, err := url.Parse(raw)
	return len(raw) <= 2048 && err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && !strings.ContainsAny(raw, "\r\n\t")
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
			return fmt.Errorf("disk content cache exceeds 68 KiB")
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
	out := Snapshot{Version: s.Version, GeneratedAt: s.GeneratedAt, Articles: append([]Article(nil), s.Articles...)}
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
