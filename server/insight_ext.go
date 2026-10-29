package server

import (
	"fmt"
	"sync"
	"time"

	"github.com/trezor/blockbook/api"
)

// insightExtension adds the routes and transaction fields of one coin's stock
// Insight API. Only a coin whose chain parser names an extension gets them, so
// other coins keep the generic route table unchanged.
type insightExtension interface {
	// match maps path segments (after the api prefix) that the generic
	// router does not know to a kind, its path argument and allowed methods.
	match(parts []string) (kind, arg string, methods []string, ok bool)
	// handles reports whether the extension serves kind instead of the
	// generic handler.
	handles(kind string) bool
	// handle serves an admitted route of a kind it handles.
	handle(route insightRoute) (interface{}, int, error)
	// decorateTx adds the coin's labels to a converted transaction.
	decorateTx(out *insightTx, tx *api.Tx)
}

// InsightExtensionProvider is implemented by chain parsers whose stock Insight
// API has coin-specific routes. InsightExtension names the extension.
type InsightExtensionProvider interface {
	InsightExtension() string
}

var insightExtensions = map[string]func(*InsightServer) insightExtension{
	"firo": newInsightFiro,
}

// EnableExtension turns on a coin's extension by name. Call it after
// SetBackend and before Start.
func (s *InsightServer) EnableExtension(name string) error {
	ctor, ok := insightExtensions[name]
	if !ok {
		return fmt.Errorf("unknown Insight extension %q", name)
	}
	s.ext = ctor(s)
	return nil
}

// transaction converts tx and applies the extension's labels.
func (s *InsightServer) transaction(tx *api.Tx) (*insightTx, error) {
	out, err := insightTransaction(tx)
	if err == nil && s.ext != nil {
		s.ext.decorateTx(out, tx)
	}
	return out, err
}

// insightCached holds one value for ttl. Loads are serialized, so concurrent
// requests for an expired value make one daemon call; a failed load is not
// cached.
type insightCached struct {
	mu    sync.Mutex
	at    time.Time
	value interface{}
}

func (c *insightCached) get(ttl time.Duration, load func() (interface{}, error)) (interface{}, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.value != nil && insightNow().Sub(c.at) < ttl {
		return c.value, nil
	}
	value, err := load()
	if err != nil {
		return nil, err
	}
	c.value, c.at = value, insightNow()
	return value, nil
}
