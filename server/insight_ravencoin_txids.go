package server

import (
	"container/list"
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// ravend's getaddresstxids returns an address's whole txid list in one call,
// and its height range cannot bound that: with assets included (which the
// history routes need) ravend seeks to the address's first index key and
// ignores start, and its keys are ordered by asset before height, so end
// cuts the list off inside the first asset. The listener therefore reads the
// full list, but at most once per address set and chain tip. The asset utxo
// routes get the same treatment for ravend's getaddressutxos, which also
// answers with everything at once (several MB for "*" on an airdrop
// address):
//
//   - the confirmed answer is cached under the tip block hash and the sorted
//     address set (and the asset name for utxos); a new tip (or a reorg)
//     starts a fresh cache, so an answer is never served for a tip other
//     than the one it was read at. The tip is Blockbook's, which can trail
//     ravend by the time it takes to index a block: in that gap a tx that
//     left the mempool for the new block is in neither part of the answer.
//   - each cache is an LRU bounded by entry count and by total size (txids
//     or utxo rows); an answer over the size bound is served but not kept.
//   - concurrent requests for the same key share one ravend call.
//   - at most insightRvnTxidsSlots of these calls (history and utxos
//     together) run at once; a request that cannot start one within
//     insightRvnTxidsWait gets 503.
//
// The mempool is not cached: getaddressmempool is cheap and is read on every
// request, so pending txs show up (and confirmed ones leave the pending part)
// as fast as without the cache.
const (
	insightRvnTxidsMaxEntries = 256
	// insightRvnTxidsMaxIds bounds the txids held in all (~100 bytes each
	// with the slice and string headers, so ~100 MB).
	insightRvnTxidsMaxIds     = 1000000
	insightRvnUtxosMaxEntries = 256
	// insightRvnUtxosMaxRows bounds the utxo rows held in all (~400 bytes
	// each with the script, so ~80 MB).
	insightRvnUtxosMaxRows = 200000
	insightRvnTxidsSlots   = 3
	insightRvnTxidsWait    = 2 * time.Second
)

// errInsightRvnBusy is returned when no ravend slot frees up in time.
var errInsightRvnBusy = errors.New("ravend address index busy")

type insightRvnTipEntry[T any] struct {
	key  string
	val  T
	size int
}

// insightRvnTipCache holds ravend answers for one chain tip, see above.
type insightRvnTipCache[T any] struct {
	mu      sync.Mutex
	tip     string
	lru     *list.List // *insightRvnTipEntry[T], most recently used first
	entries map[string]*list.Element
	size    int

	maxEntries, maxSize int
	sizeOf              func(T) int
	slots               chan struct{}
	wait                time.Duration
	group               singleflight.Group
	// joined, when set (tests), is called once a request is attached to the
	// call for its key.
	joined func()
}

// insightRvnTxidCache holds getaddresstxids lists.
type insightRvnTxidCache = insightRvnTipCache[[]string]

// insightRvnUtxoSet is ravend's getaddressutxos answer with chaininfo.
type insightRvnUtxoSet struct {
	Utxos  []insightRvnUtxo `json:"utxos"`
	Height int              `json:"height"`
}

// newInsightRvnTipCache makes a cache whose ravend calls take a slot from
// slots, which caches may share.
func newInsightRvnTipCache[T any](maxEntries, maxSize int, sizeOf func(T) int, slots chan struct{}, wait time.Duration) *insightRvnTipCache[T] {
	return &insightRvnTipCache[T]{
		lru:        list.New(),
		entries:    map[string]*list.Element{},
		maxEntries: maxEntries,
		maxSize:    maxSize,
		sizeOf:     sizeOf,
		slots:      slots,
		wait:       wait,
	}
}

func newInsightRvnTxidCache(maxEntries, maxIds, slots int, wait time.Duration) *insightRvnTxidCache {
	return newInsightRvnTipCache(maxEntries, maxIds, func(ids []string) int { return len(ids) }, make(chan struct{}, slots), wait)
}

func newInsightRvnUtxoCache(maxEntries, maxRows int, slots chan struct{}, wait time.Duration) *insightRvnTipCache[*insightRvnUtxoSet] {
	return newInsightRvnTipCache(maxEntries, maxRows, func(u *insightRvnUtxoSet) int { return len(u.Utxos) }, slots, wait)
}

// insightRvnTxidKey keys an address set at a tip. The addresses are sorted:
// ravend's answer for several addresses does not depend on their order.
func insightRvnTxidKey(tip string, addresses []string) string {
	sorted := append([]string(nil), addresses...)
	sort.Strings(sorted)
	return tip + "|" + strings.Join(sorted, ",")
}

// insightRvnUtxoKey keys an address set and an asset name at a tip. The
// asset comes last: the addresses are validated and hold no "|".
func insightRvnUtxoKey(tip string, addresses []string, asset string) string {
	return insightRvnTxidKey(tip, addresses) + "|" + asset
}

// get returns the entry for key at tip. A tip other than the cache's drops
// every entry and makes tip the cache's: get runs right after a request has
// read the tip, so a different tip is a new block (or a reorg).
func (c *insightRvnTipCache[T]) get(tip, key string) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var zero T
	if tip != c.tip {
		c.tip = tip
		c.lru.Init()
		c.entries = map[string]*list.Element{}
		c.size = 0
		return zero, false
	}
	el, ok := c.entries[key]
	if !ok {
		return zero, false
	}
	c.lru.MoveToFront(el)
	return el.Value.(*insightRvnTipEntry[T]).val, true
}

// put keeps val for key at tip. It never moves the cache's tip: a call that
// started on the previous tip and ends after the tip moved drops its answer
// instead of wiping the new tip's entries.
func (c *insightRvnTipCache[T]) put(tip, key string, val T) {
	c.mu.Lock()
	defer c.mu.Unlock()
	size := c.sizeOf(val)
	if tip != c.tip || size > c.maxSize {
		return
	}
	if el, ok := c.entries[key]; ok {
		c.size -= el.Value.(*insightRvnTipEntry[T]).size
		c.lru.Remove(el)
	}
	c.entries[key] = c.lru.PushFront(&insightRvnTipEntry[T]{key: key, val: val, size: size})
	c.size += size
	for c.lru.Len() > c.maxEntries || c.size > c.maxSize {
		el := c.lru.Back()
		e := el.Value.(*insightRvnTipEntry[T])
		c.lru.Remove(el)
		delete(c.entries, e.key)
		c.size -= e.size
	}
}

// acquire takes a ravend slot, waiting at most c.wait. It does not follow
// any request's context: the call it guards is shared by every request
// waiting on the key, and each of them stops waiting on its own.
func (c *insightRvnTipCache[T]) acquire() bool {
	select {
	case c.slots <- struct{}{}:
		return true
	default:
	}
	t := time.NewTimer(c.wait)
	defer t.Stop()
	select {
	case c.slots <- struct{}{}:
		return true
	case <-t.C:
		return false
	}
}

func (c *insightRvnTipCache[T]) release() { <-c.slots }

// load returns the cached answer for key at tip (uncached without a tip),
// or runs fetch in a slot, shared by the concurrent requests for key. The
// answer may be shared with other requests and must not be modified. The
// int is the HTTP status for an error.
func (c *insightRvnTipCache[T]) load(ctx context.Context, tip string, cacheable bool, key string, fetch func() (T, error)) (T, int, error) {
	var zero T
	if cacheable {
		if v, ok := c.get(tip, key); ok {
			return v, 0, nil
		}
	}
	ch := c.group.DoChan(key, func() (interface{}, error) {
		if cacheable {
			if v, ok := c.get(tip, key); ok {
				return v, nil
			}
		}
		if !c.acquire() {
			return nil, errInsightRvnBusy
		}
		defer c.release()
		v, err := fetch()
		if err != nil {
			return nil, err
		}
		if cacheable {
			c.put(tip, key, v)
		}
		return v, nil
	})
	if c.joined != nil {
		c.joined()
	}
	var done <-chan struct{}
	if ctx != nil {
		done = ctx.Done()
	}
	select {
	case r := <-ch:
		if errors.Is(r.Err, errInsightRvnBusy) {
			return zero, http.StatusServiceUnavailable, r.Err
		}
		if r.Err != nil {
			return zero, http.StatusInternalServerError, r.Err
		}
		return r.Val.(T), 0, nil
	case <-done:
		return zero, insightClientGoneCode, ctx.Err()
	}
}

// insightRvnTipHash is Blockbook's best block hash, or false on an empty
// index or an error (the list is then read uncached).
func (x *insightRavencoin) insightRvnTipHash() (string, bool) {
	blocks, err := x.s.worker.GetBlocks(1, 1)
	if err != nil || blocks == nil || len(blocks.Blocks) == 0 || blocks.Blocks[0].Hash == "" {
		return "", false
	}
	return blocks.Blocks[0].Hash, true
}

// confirmedTxids returns ravend's getaddresstxids list (with assets) for the
// addresses in history order, each txid once: the reverse of ravend's order.
// ravend lists one address in its index order, which groups the txids by
// asset and orders each group by height, and several addresses by height,
// then txid. The slice may be shared with other requests and must not be
// modified. The int is the HTTP status for an error.
func (x *insightRavencoin) confirmedTxids(ctx context.Context, addresses []string) ([]string, int, error) {
	tip, cacheable := x.tipHash()
	return x.txids.load(ctx, tip, cacheable, insightRvnTxidKey(tip, addresses), func() ([]string, error) {
		var raw []string
		if err := x.s.rpc("getaddresstxids", []interface{}{map[string]interface{}{"addresses": addresses}, true}, &raw); err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(raw))
		listed := make(map[string]bool, len(raw))
		for i := len(raw) - 1; i >= 0; i-- {
			if !listed[raw[i]] {
				listed[raw[i]] = true
				ids = append(ids, raw[i])
			}
		}
		return ids, nil
	})
}

// confirmedUtxos returns ravend's getaddressutxos answer for the addresses
// and asset, cached per tip like confirmedTxids. The rows may be shared with
// other requests and must not be modified.
func (x *insightRavencoin) confirmedUtxos(ctx context.Context, addresses []string, asset string) (*insightRvnUtxoSet, int, error) {
	tip, cacheable := x.tipHash()
	return x.utxos.load(ctx, tip, cacheable, insightRvnUtxoKey(tip, addresses, asset), func() (*insightRvnUtxoSet, error) {
		set := &insightRvnUtxoSet{}
		if err := x.s.rpc("getaddressutxos", []interface{}{map[string]interface{}{"addresses": addresses, "chaininfo": true, "assetName": asset}}, set); err != nil {
			return nil, err
		}
		return set, nil
	})
}
