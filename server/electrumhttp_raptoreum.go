package server

import (
	"container/list"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/trezor/blockbook/api"
)

// ehSpendRule is an optional per-chain hook of the utxo and niceutxo calls:
// it reports whether an unspent output can be spent in the next block. The
// balance call does not use it.
type ehSpendRule interface {
	spendable(b *ehBudget, u *api.Utxo, now time.Time) (bool, error)
}

// EnableChainRules installs the spend rule of the coin (the coin_name of the
// blockchain config), if it has one; for every other coin it does nothing.
// blockTime returns the time of the indexed block at a height. Call it before
// Start.
func (s *ElectrumHTTPServer) EnableChainRules(coin string, blockTime func(height uint32) (int64, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return errors.New("electrum-http chain rules must be set before Start")
	}
	switch coin {
	case "Raptoreum":
		if s.rpc == nil || blockTime == nil {
			return errors.New("electrum-http: the Raptoreum spend rule needs the backend RPC and the block index")
		}
		s.spendRule = newRTMSpendRule(s.rpc, blockTime, rtmFutureCacheSize)
	}
	return nil
}

// Raptoreum consensus values (Raptor3um/raptoreum 2.0.3.01-mainnet).
const (
	// rtmCoinbaseMaturity is COINBASE_MATURITY (consensus/consensus.h).
	rtmCoinbaseMaturity = 100
	// rtmFutureForkHeight is mainnet consensus.nFutureForkBlock
	// (chainparams.cpp): no future transaction is mined below it.
	rtmFutureForkHeight = 420420
	// rtmTxFuture is TRANSACTION_FUTURE, the DIP2 type of a future.
	rtmTxFuture = 7
	// rtmTimeMargin delays the lockTime leg past the block time: the node
	// compares with its network-adjusted time, which may lag ours.
	rtmTimeMargin = 5 * time.Minute
	// rtmFutureCacheSize bounds the txid cache of parsed future payloads.
	rtmFutureCacheSize = 65536
)

// rtmFuture is what the spend rule needs of a transaction: whether it is a
// future and, if so, its CFutureTx lock (evo/providertx.h).
type rtmFuture struct {
	future   bool
	maturity int32  // confirmations; negative disables the leg
	lockTime int32  // seconds after the block time; negative disables the leg
	index    uint16 // lockOutputIndex, the only locked output
}

// rtmSpendRule leaves out the outputs raptoreumd would refuse to spend in the
// next block (consensus/tx_verify.cpp): coinbase outputs under
// COINBASE_MATURITY confirmations, and the locked output of a future that is
// still locked. A future output may be spent when
//
//	(maturity >= 0 && spendHeight-coinHeight >= maturity) ||
//	(lockTime >= 0 && adjustedTime-blockTime >= lockTime)
//
// with spendHeight the next block, so spendHeight-coinHeight is the output's
// confirmations. An unconfirmed future output is never spendable (the node has
// no block index for it).
type rtmSpendRule struct {
	rpc       InsightRPC
	blockTime func(height uint32) (int64, error)
	cache     *rtmFutureCache
}

func newRTMSpendRule(rpc InsightRPC, blockTime func(uint32) (int64, error), cacheSize int) *rtmSpendRule {
	return &rtmSpendRule{rpc: rpc, blockTime: blockTime, cache: newRTMFutureCache(cacheSize)}
}

func (r *rtmSpendRule) spendable(b *ehBudget, u *api.Utxo, now time.Time) (bool, error) {
	confirmed := u.Height > 0 && u.Confirmations > 0
	if u.Coinbase {
		// A coinbase is never a future (it is type 5, CbTx).
		return confirmed && u.Confirmations >= rtmCoinbaseMaturity, nil
	}
	if confirmed && u.Height < rtmFutureForkHeight {
		return true, nil
	}
	f, err := r.future(b, u.Txid)
	if err != nil {
		return false, err
	}
	if !f.future || u.Vout < 0 || uint32(u.Vout) != uint32(f.index) {
		return true, nil
	}
	if !confirmed {
		return false, nil
	}
	if f.maturity >= 0 && u.Confirmations >= int(f.maturity) {
		return true, nil
	}
	if f.lockTime >= 0 {
		t, err := r.blockTime(uint32(u.Height))
		if err != nil {
			return false, err
		}
		if now.Add(-rtmTimeMargin).Unix()-t >= int64(f.lockTime) {
			return true, nil
		}
	}
	return false, nil
}

// future returns the cached lock of a transaction, reading and parsing its raw
// hex from the daemon on a miss.
func (r *rtmSpendRule) future(b *ehBudget, txid string) (rtmFuture, error) {
	if f, ok := r.cache.get(txid); ok {
		return f, nil
	}
	if err := b.spend(); err != nil {
		return rtmFuture{}, err
	}
	var raw string
	if err := r.rpc("getrawtransaction", []interface{}{txid, 0}, &raw); err != nil {
		return rtmFuture{}, ehRPCErr(err)
	}
	tx, err := hex.DecodeString(raw)
	if err != nil {
		return rtmFuture{}, err
	}
	f, err := parseRTMFuture(tx)
	if err != nil {
		return rtmFuture{}, err
	}
	r.cache.add(txid, f)
	return f, nil
}

var errRTMShortTx = errors.New("raptoreum: truncated transaction")

// parseRTMFuture reads the DIP2 header of a raw Raptoreum transaction and, for
// a future (version 3, type 7), the start of its CFutureTx payload: nVersion
// u16, maturity i32, lockTime i32, lockOutputIndex u16, all little endian.
func parseRTMFuture(tx []byte) (rtmFuture, error) {
	if len(tx) < 4 {
		return rtmFuture{}, errRTMShortTx
	}
	v := binary.LittleEndian.Uint32(tx)
	if v&0xffff < 3 || v>>16 != rtmTxFuture {
		return rtmFuture{}, nil
	}
	p := rtmReader{b: tx, off: 4}
	nIn := p.varint()
	for i := uint64(0); i < nIn && p.err == nil; i++ {
		p.skip(36) // outpoint
		p.skip(p.varint())
		p.skip(4) // sequence
	}
	nOut := p.varint()
	for i := uint64(0); i < nOut && p.err == nil; i++ {
		p.skip(8) // value
		p.skip(p.varint())
	}
	p.skip(4) // locktime
	size := p.varint()
	payload := p.take(size)
	if p.err != nil {
		return rtmFuture{}, p.err
	}
	if p.off != len(tx) || len(payload) < 12 {
		return rtmFuture{}, errors.New("raptoreum: malformed future payload")
	}
	return rtmFuture{
		future:   true,
		maturity: int32(binary.LittleEndian.Uint32(payload[2:])),
		lockTime: int32(binary.LittleEndian.Uint32(payload[6:])),
		index:    binary.LittleEndian.Uint16(payload[10:]),
	}, nil
}

// rtmReader reads Bitcoin serialization; the first error sticks.
type rtmReader struct {
	b   []byte
	off int
	err error
}

func (r *rtmReader) take(n uint64) []byte {
	if r.err != nil {
		return nil
	}
	if n > uint64(len(r.b)-r.off) {
		r.err = errRTMShortTx
		return nil
	}
	s := r.b[r.off : r.off+int(n)]
	r.off += int(n)
	return s
}

func (r *rtmReader) skip(n uint64) { r.take(n) }

// varint reads a CompactSize.
func (r *rtmReader) varint() uint64 {
	b := r.take(1)
	if b == nil {
		return 0
	}
	switch b[0] {
	case 0xfd:
		if s := r.take(2); s != nil {
			return uint64(binary.LittleEndian.Uint16(s))
		}
	case 0xfe:
		if s := r.take(4); s != nil {
			return uint64(binary.LittleEndian.Uint32(s))
		}
	case 0xff:
		if s := r.take(8); s != nil {
			return binary.LittleEndian.Uint64(s)
		}
	default:
		return uint64(b[0])
	}
	return 0
}

// rtmFutureCache is a bounded LRU of parsed transactions by txid. A
// transaction's payload never changes, so entries need no expiry.
type rtmFutureCache = ehLRU[string, rtmFuture]

func newRTMFutureCache(max int) *rtmFutureCache {
	return newEHLRU[string, rtmFuture](max)
}

// ehLRU is a bounded, concurrency-safe LRU map.
type ehLRU[K comparable, V any] struct {
	mu    sync.Mutex
	max   int
	order *list.List // front = most recently used; values are *ehLRUEntry
	items map[K]*list.Element
}

type ehLRUEntry[K comparable, V any] struct {
	key K
	v   V
}

func newEHLRU[K comparable, V any](max int) *ehLRU[K, V] {
	return &ehLRU[K, V]{max: max, order: list.New(), items: make(map[K]*list.Element)}
}

func (c *ehLRU[K, V]) get(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[key]; ok {
		c.order.MoveToFront(e)
		return e.Value.(*ehLRUEntry[K, V]).v, true
	}
	var zero V
	return zero, false
}

func (c *ehLRU[K, V]) add(key K, v V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[key]; ok {
		e.Value.(*ehLRUEntry[K, V]).v = v
		c.order.MoveToFront(e)
		return
	}
	c.items[key] = c.order.PushFront(&ehLRUEntry[K, V]{key, v})
	for c.order.Len() > c.max {
		e := c.order.Back()
		c.order.Remove(e)
		delete(c.items, e.Value.(*ehLRUEntry[K, V]).key)
	}
}

func (c *ehLRU[K, V]) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}
