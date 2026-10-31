package server

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/golang/glog"
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

// insightTxLoader is implemented by an extension whose tx fields need daemon
// data the worker's tx lacks. loadTxs fetches it for a batch of txs and
// returns one decorator per tx (nil for none); an error fails the request.
type insightTxLoader interface {
	loadTxs(ctx context.Context, txs []*api.Tx) ([]func(*insightTx), error)
}

// insightEscapedMatcher is implemented by an extension whose path arguments
// may hold an encoded "/" (%2F). matchEscaped is offered the escaped path
// segments (after the api prefix) before any other matching, so such an
// argument stays one segment; it unescapes the arguments it returns.
type insightEscapedMatcher interface {
	matchEscaped(parts []string) (insightRoute, []string, bool)
}

// insightTxidLister is implemented by an extension whose daemon lists the
// txids of addresses more completely than Blockbook's index (Ravencoin asset
// transfers, which the index files under no address). The address history
// routes page that list, in the order the extension gives it (pending first),
// instead of the worker's. The returned slice may be shared between requests
// and must not be modified. The int is the HTTP status for an error.
type insightTxidLister interface {
	addressTxids(ctx context.Context, addresses []string) ([]string, int, error)
}

var insightExtensions = map[string]func(*InsightServer) insightExtension{
	"firo":      newInsightFiro,
	"ravencoin": newInsightRavencoin,
	"zcash":     newInsightZcash,
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

// transaction converts one tx (see transactions).
func (s *InsightServer) transaction(ctx context.Context, tx *api.Tx) (*insightTx, error) {
	out, err := s.transactions(ctx, []*api.Tx{tx})
	if err != nil {
		return nil, err
	}
	return out[0], nil
}

// transactions converts txs and applies the extension's labels and loaded
// fields.
func (s *InsightServer) transactions(ctx context.Context, txs []*api.Tx) ([]*insightTx, error) {
	var loaded []func(*insightTx)
	if loader, ok := s.ext.(insightTxLoader); ok {
		var err error
		if loaded, err = loader.loadTxs(ctx, txs); err != nil {
			return nil, err
		}
	}
	out := make([]*insightTx, 0, len(txs))
	for i, tx := range txs {
		item, err := insightTransaction(tx)
		if err != nil {
			return nil, err
		}
		if s.ext != nil {
			s.ext.decorateTx(item, tx)
		}
		if loaded != nil && loaded[i] != nil {
			loaded[i](item)
		}
		out = append(out, item)
	}
	return out, nil
}

// insightCacheErrTTL is how long a failed load is remembered, so a hung or
// failing daemon is not called again by every queued request.
const insightCacheErrTTL = 5 * time.Second

// insightCached holds one value for ttl. Loads are single-flight: one load runs
// at a time, outside the mutex, and every caller that arrives meanwhile shares
// its result or error. When a refresh fails the last good value is served
// (stale-on-error); with no good value the error is served for
// insightCacheErrTTL. A caller whose context ends stops waiting; the load
// carries on and fills the cache for the next one. errTTL, when set, replaces
// insightCacheErrTTL for this cache.
type insightCached struct {
	errTTL time.Duration
	mu     sync.Mutex
	at     time.Time
	value  interface{}
	err    error
	errAt  time.Time
	call   *insightCacheCall
}

type insightCacheCall struct {
	done  chan struct{}
	value interface{}
	err   error
}

func (c *insightCached) get(ctx context.Context, ttl time.Duration, load func() (interface{}, error)) (interface{}, error) {
	c.mu.Lock()
	now := insightNow()
	if c.value != nil && now.Sub(c.at) < ttl {
		v := c.value
		c.mu.Unlock()
		return v, nil
	}
	errTTL := c.errTTL
	if errTTL <= 0 {
		errTTL = insightCacheErrTTL
	}
	if c.err != nil && now.Sub(c.errAt) < errTTL {
		v, err := c.value, c.err
		c.mu.Unlock()
		if v != nil {
			return v, nil
		}
		return nil, err
	}
	call := c.call
	if call == nil {
		call = &insightCacheCall{done: make(chan struct{})}
		c.call = call
		go c.run(call, load)
	}
	c.mu.Unlock()
	var cancelled <-chan struct{}
	if ctx != nil {
		cancelled = ctx.Done()
	}
	select {
	case <-call.done:
		return call.value, call.err
	case <-cancelled:
		return nil, ctx.Err()
	}
}

func (c *insightCached) run(call *insightCacheCall, load func() (interface{}, error)) {
	value, err := c.load(load)
	c.mu.Lock()
	if err == nil {
		c.value, c.at, c.err = value, insightNow(), nil
	} else {
		c.err, c.errAt = err, insightNow()
		if c.value != nil {
			value, err = c.value, nil
		}
	}
	call.value, call.err = value, err
	c.call = nil
	c.mu.Unlock()
	close(call.done)
}

// load runs the loader in the background goroutine, where a panic would end
// the process instead of one request; it becomes the waiters' error.
func (c *insightCached) load(load func() (interface{}, error)) (value interface{}, err error) {
	defer func() {
		if r := recover(); r != nil {
			glog.Error("insight: cache loader panic: ", r, "\n", string(debug.Stack()))
			value, err = nil, fmt.Errorf("internal error")
		}
	}()
	return load()
}
