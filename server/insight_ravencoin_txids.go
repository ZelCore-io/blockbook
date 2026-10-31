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
// full list, but at most once per address set and chain tip:
//
//   - the confirmed list is cached under the tip block hash and the sorted
//     address set; a new tip (or a reorg) starts a fresh cache, so a list is
//     never served for a tip other than the one it was read at. The tip is
//     Blockbook's, which can trail ravend by the time it takes to index a
//     block: in that gap a tx that left the mempool for the new block is in
//     neither part of the list.
//   - the cache is an LRU bounded by entry count and by total txids; a list
//     longer than the txid bound is served but not kept.
//   - concurrent requests for the same key share one ravend call.
//   - at most insightRvnTxidsSlots calls run at once; a request that cannot
//     start one within insightRvnTxidsWait gets 503.
//
// The mempool is not cached: getaddressmempool is cheap and is read on every
// request, so pending txs show up (and confirmed ones leave the pending part)
// as fast as without the cache.
const (
	insightRvnTxidsMaxEntries = 256
	// insightRvnTxidsMaxIds bounds the txids held in all (~100 bytes each
	// with the slice and string headers, so ~100 MB).
	insightRvnTxidsMaxIds = 1000000
	insightRvnTxidsSlots  = 3
	insightRvnTxidsWait   = 2 * time.Second
)

// errInsightRvnBusy is returned when no getaddresstxids slot frees up in time.
var errInsightRvnBusy = errors.New("ravend getaddresstxids busy")

type insightRvnTxidEntry struct {
	key string
	ids []string
}

type insightRvnTxidCache struct {
	mu      sync.Mutex
	tip     string
	lru     *list.List // *insightRvnTxidEntry, most recently used first
	entries map[string]*list.Element
	ids     int

	maxEntries, maxIds int
	slots              chan struct{}
	wait               time.Duration
	group              singleflight.Group
}

func newInsightRvnTxidCache(maxEntries, maxIds, slots int, wait time.Duration) *insightRvnTxidCache {
	return &insightRvnTxidCache{
		lru:        list.New(),
		entries:    map[string]*list.Element{},
		maxEntries: maxEntries,
		maxIds:     maxIds,
		slots:      make(chan struct{}, slots),
		wait:       wait,
	}
}

// insightRvnTxidKey keys an address set at a tip. The addresses are sorted:
// ravend's answer for several addresses does not depend on their order.
func insightRvnTxidKey(tip string, addresses []string) string {
	sorted := append([]string(nil), addresses...)
	sort.Strings(sorted)
	return tip + "|" + strings.Join(sorted, ",")
}

// reset drops every entry once the tip differs. The caller holds mu.
func (c *insightRvnTxidCache) reset(tip string) {
	if tip == c.tip {
		return
	}
	c.tip = tip
	c.lru.Init()
	c.entries = map[string]*list.Element{}
	c.ids = 0
}

func (c *insightRvnTxidCache) get(tip, key string) ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reset(tip)
	el, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	c.lru.MoveToFront(el)
	return el.Value.(*insightRvnTxidEntry).ids, true
}

func (c *insightRvnTxidCache) put(tip, key string, ids []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reset(tip)
	if len(ids) > c.maxIds {
		return
	}
	if el, ok := c.entries[key]; ok {
		c.ids -= len(el.Value.(*insightRvnTxidEntry).ids)
		c.lru.Remove(el)
	}
	c.entries[key] = c.lru.PushFront(&insightRvnTxidEntry{key: key, ids: ids})
	c.ids += len(ids)
	for c.lru.Len() > c.maxEntries || c.ids > c.maxIds {
		el := c.lru.Back()
		e := el.Value.(*insightRvnTxidEntry)
		c.lru.Remove(el)
		delete(c.entries, e.key)
		c.ids -= len(e.ids)
	}
}

// acquire takes a getaddresstxids slot, waiting at most c.wait. It does not
// follow any request's context: the call it guards is shared by every
// request waiting on the key, and each of them stops waiting on its own.
func (c *insightRvnTxidCache) acquire() bool {
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

func (c *insightRvnTxidCache) release() { <-c.slots }

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
	c := x.txids
	tip, cacheable := x.tipHash()
	key := insightRvnTxidKey(tip, addresses)
	if cacheable {
		if ids, ok := c.get(tip, key); ok {
			return ids, 0, nil
		}
	}
	ch := c.group.DoChan(key, func() (interface{}, error) {
		if cacheable {
			if ids, ok := c.get(tip, key); ok {
				return ids, nil
			}
		}
		if !c.acquire() {
			return nil, errInsightRvnBusy
		}
		defer c.release()
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
		if cacheable {
			c.put(tip, key, ids)
		}
		return ids, nil
	})
	var done <-chan struct{}
	if ctx != nil {
		done = ctx.Done()
	}
	select {
	case r := <-ch:
		if errors.Is(r.Err, errInsightRvnBusy) {
			return nil, http.StatusServiceUnavailable, r.Err
		}
		if r.Err != nil {
			return nil, http.StatusInternalServerError, r.Err
		}
		return r.Val.([]string), 0, nil
	case <-done:
		return nil, insightClientGoneCode, ctx.Err()
	}
}
