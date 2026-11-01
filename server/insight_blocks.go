package server

import (
	"container/list"
	"encoding/hex"
	"errors"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/trezor/blockbook/api"
	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/db"
)

const (
	insightBlocksDefaultLimit = 200 // stock BLOCK_LIMIT
	insightBlocksMaxLimit     = 200
	insightRPCNotFound        = -5
	// insightRPCInvalidParam is what Zebra answers getblock for an unknown hash.
	insightRPCInvalidParam = -8
	insightTipRetries      = 3
	insightBlockCacheSize  = 32
	// insightBlockCacheBytes caps the cached txid lists in total, so a run of
	// very large blocks cannot grow the cache by entry count alone. A block
	// whose own cost exceeds the cap is not cached.
	insightBlockCacheBytes = 16 << 20
	insightTxidCost        = 64 + 16 + 16 // hex txid + string header + slice slot slack
)

// insightBlockPageSize is how many txs one GetBlock call loads. A var so tests
// can force paging.
var insightBlockPageSize = 1000

// insightNow is the clock for "today"; a var so tests can pin it.
var insightNow = time.Now

var (
	errInsightTipMoved   = errors.New("chain tip moved during the request")
	errInsightEmptyIndex = errors.New("no indexed block")
)

// insightHash64 reports whether s is a 64-character hex hash, and returns it
// lower-cased.
func insightHash64(s string) (string, bool) {
	if len(s) != 64 {
		return "", false
	}
	if _, err := hex.DecodeString(s); err != nil {
		return "", false
	}
	return strings.ToLower(s), true
}

// insightBlockNotFound is the worker's exact public text for an unknown
// block. A backend failure reads "Block not found, <cause>" instead.
const insightBlockNotFound = "Block not found"

// insightTxNotFound is the worker's exact public text for an unknown tx. A
// backend failure reads "Transaction '<txid>' not found (<cause>)" instead.
func insightTxNotFound(txid string) string {
	return "Transaction '" + txid + "' not found"
}

// insightNotFoundCode maps a worker error. The worker wraps both "unknown"
// and backend failures in a public APIError, so only the exact not-found
// text is 404; every other error is a generic 500.
func insightNotFoundCode(err error, notFound string) int {
	var apiErr *api.APIError
	if errors.As(err, &apiErr) && apiErr.Public && apiErr.Text == notFound {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

// insightRowAt reads the index row at a height. Page p of one-row pages is
// height best-(p-1), so a row with another height means the tip moved.
func (s *InsightServer) insightRowAt(best, height uint32) (db.BlockInfo, error) {
	blocks, err := s.worker.GetBlocks(int(best-height)+1, 1)
	if err != nil {
		return db.BlockInfo{}, err
	}
	if blocks == nil || len(blocks.Blocks) != 1 || blocks.Blocks[0].Height != height {
		return db.BlockInfo{}, errInsightTipMoved
	}
	return blocks.Blocks[0], nil
}

// insightTip returns the best indexed height, or false on an empty index.
func (s *InsightServer) insightTip() (uint32, bool, error) {
	blocks, err := s.worker.GetBlocks(1, 1)
	if err != nil {
		return 0, false, err
	}
	if blocks == nil || len(blocks.Blocks) == 0 {
		return 0, false, nil
	}
	return blocks.Blocks[0].Height, true, nil
}

func insightDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func (s *InsightServer) blockIndex(route insightRoute) (interface{}, int, error) {
	if !insightDigits(route.arg) {
		return nil, http.StatusNotFound, errors.New("invalid height")
	}
	height, err := strconv.ParseUint(route.arg, 10, 32)
	if err != nil {
		return nil, http.StatusNotFound, err
	}
	for try := 0; try < insightTipRetries; try++ {
		best, ok, err := s.insightTip()
		if err != nil {
			return nil, http.StatusInternalServerError, err
		}
		if !ok {
			return nil, http.StatusServiceUnavailable, errInsightEmptyIndex
		}
		if uint32(height) > best {
			return nil, http.StatusNotFound, errors.New("height out of range")
		}
		row, err := s.insightRowAt(best, uint32(height))
		if err == errInsightTipMoved {
			continue
		}
		if err != nil {
			return nil, http.StatusInternalServerError, err
		}
		return struct {
			BlockHash string `json:"blockHash"`
		}{row.Hash}, 0, nil
	}
	return nil, http.StatusServiceUnavailable, errInsightTipMoved
}

// insightDigitsOnly reports a 64-character argument with no a-f letter. The
// worker reads such a string as a block height (64 zeros is the genesis
// block), and no real block hash has this form in practice.
func insightDigitsOnly(hash string) bool {
	for i := 0; i < len(hash); i++ {
		if hash[i] < '0' || hash[i] > '9' {
			return false
		}
	}
	return true
}

func (s *InsightServer) rawBlock(route insightRoute) (interface{}, int, error) {
	hash, ok := insightHash64(route.arg)
	if !ok {
		return nil, http.StatusBadRequest, errors.New("invalid block hash")
	}
	if insightDigitsOnly(hash) {
		return nil, http.StatusNotFound, errors.New(insightBlockNotFound)
	}
	raw, err := s.worker.GetBlockRaw(hash)
	if err == nil && raw != nil {
		return struct {
			Rawblock string `json:"rawblock"`
		}{raw.Hex}, 0, nil
	}
	if err == nil {
		err = errors.New("missing raw block")
	}
	var apiErr *api.APIError
	if errors.As(err, &apiErr) || s.rpc == nil {
		return nil, insightNotFoundCode(err, insightBlockNotFound), err
	}
	// The index cannot serve the raw block (for example Zebra): ask the daemon.
	var rawHex string
	rpcErr := s.rpc("getblock", []interface{}{hash, 0}, &rawHex)
	if insightRPCWantsBool(rpcErr) {
		// Older zcashd-lineage daemons (anond) take only a boolean verbose.
		rpcErr = s.rpc("getblock", []interface{}{hash, false}, &rawHex)
	}
	if rpcErr != nil {
		var re *bchain.RPCError
		if errors.As(rpcErr, &re) && (re.Code == insightRPCNotFound || re.Code == insightRPCInvalidParam) {
			return nil, http.StatusNotFound, rpcErr
		}
		return nil, http.StatusInternalServerError, rpcErr
	}
	if rawHex == "" {
		return nil, http.StatusInternalServerError, errors.New("empty raw block")
	}
	if _, err := hex.DecodeString(rawHex); err != nil {
		return nil, http.StatusInternalServerError, err
	}
	return struct {
		Rawblock string `json:"rawblock"`
	}{rawHex}, 0, nil
}

// insightRPCWantsBool reports the error a daemon whose getblock verbose
// argument is a boolean (anond, bitcoind before 0.15) returns for a number:
// UniValue's get_bool runtime error, which the RPC server reports as
// RPC_MISC_ERROR (-1) or RPC_TYPE_ERROR (-3).
func insightRPCWantsBool(err error) bool {
	var re *bchain.RPCError
	return errors.As(err, &re) && (re.Code == -1 || re.Code == -3) && strings.Contains(re.Message, "not a boolean")
}

// insightBlockData is what a block response needs beyond its header. Both
// fields are immutable for a given hash, so they are cached by hash; the
// header (confirmations, neighbours) is always read fresh.
type insightBlockData struct {
	Txids  []string
	Reward *api.Amount // coinbase outputs minus all fees, in satoshi
}

// insightBlockCache is a small LRU of insightBlockData keyed by block hash.
type insightBlockCache struct {
	mu      sync.Mutex
	order   *list.List // front = most recent; values are *insightBlockCacheEntry
	entries map[string]*list.Element
	bytes   int // sum of entry costs
	maxSize int // entry count bound
	maxCost int // total cost bound
}
type insightBlockCacheEntry struct {
	hash string
	data *insightBlockData
	cost int
}

func insightBlockCost(data *insightBlockData) int {
	return len(data.Txids)*insightTxidCost + 256
}

func newInsightBlockCache() *insightBlockCache {
	return &insightBlockCache{order: list.New(), entries: map[string]*list.Element{}, maxSize: insightBlockCacheSize, maxCost: insightBlockCacheBytes}
}

func (c *insightBlockCache) get(hash string) *insightBlockData {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[hash]; ok {
		c.order.MoveToFront(el)
		return el.Value.(*insightBlockCacheEntry).data
	}
	return nil
}

func (c *insightBlockCache) put(hash string, data *insightBlockData) {
	cost := insightBlockCost(data)
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[hash]; ok {
		c.remove(el)
	}
	if cost > c.maxCost {
		return
	}
	c.entries[hash] = c.order.PushFront(&insightBlockCacheEntry{hash, data, cost})
	c.bytes += cost
	for c.order.Len() > c.maxSize || c.bytes > c.maxCost {
		c.remove(c.order.Back())
	}
}

// remove drops an element; the caller holds c.mu.
func (c *insightBlockCache) remove(el *list.Element) {
	entry := c.order.Remove(el).(*insightBlockCacheEntry)
	delete(c.entries, entry.hash)
	c.bytes -= entry.cost
}

// loadBlock returns the block header (api.Block with no Transactions), the
// txids in block order and the reward. A cache miss pages through every tx in
// insightBlockPageSize chunks and keeps only txids and the running fee sum, so
// memory stays at one page of transactions. A hit still reads one fresh
// header row (one tx) so confirmations and neighbours are current.
func (s *InsightServer) loadBlock(hash string) (*api.Block, *insightBlockData, error) {
	pageSize := insightBlockPageSize
	cached := s.blockCache.get(hash)
	if cached != nil {
		pageSize = 1
	}
	head, err := s.worker.GetBlock(hash, 1, pageSize)
	if err != nil {
		return nil, nil, err
	}
	if head == nil {
		return nil, nil, errors.New("missing block")
	}
	if !strings.EqualFold(head.Hash, hash) {
		return nil, nil, api.NewAPIError(insightBlockNotFound, true)
	}
	if cached != nil && len(cached.Txids) == head.TxCount {
		head.Transactions = nil
		return head, cached, nil
	}
	data := &insightBlockData{Txids: make([]string, 0, head.TxCount)}
	total := new(big.Int)
	collect := func(txs []*api.Tx) error {
		for _, tx := range txs {
			if tx == nil {
				return errors.New("missing block transaction")
			}
			if len(data.Txids) == 0 { // the coinbase is the first tx of the block
				for _, out := range tx.Vout {
					v := out.ValueSat.AsBigInt()
					total.Add(total, &v)
				}
			}
			fee := tx.FeesSat.AsBigInt()
			total.Sub(total, &fee)
			data.Txids = append(data.Txids, tx.Txid)
		}
		return nil
	}
	page, got := head, 1
	for {
		if err := collect(page.Transactions); err != nil {
			return nil, nil, err
		}
		if len(data.Txids) >= head.TxCount {
			break
		}
		got++
		if got > head.TotalPages {
			return nil, nil, errors.New("block transactions missing")
		}
		if page, err = s.worker.GetBlock(hash, got, pageSize); err != nil {
			return nil, nil, err
		}
		if page == nil || page.Page != got || len(page.Transactions) == 0 {
			return nil, nil, errors.New("block page missing")
		}
	}
	head.Transactions = nil
	data.Reward = (*api.Amount)(total)
	s.blockCache.put(hash, data)
	return head, data, nil
}

func (s *InsightServer) block(route insightRoute) (interface{}, int, error) {
	hash, ok := insightHash64(route.arg)
	if !ok {
		return nil, http.StatusBadRequest, errors.New("invalid block hash")
	}
	if insightDigitsOnly(hash) {
		return nil, http.StatusNotFound, errors.New(insightBlockNotFound)
	}
	b, data, err := s.loadBlock(hash)
	if err != nil {
		return nil, insightNotFoundCode(err, insightBlockNotFound), err
	}
	reward, _ := insightMoney(data.Reward)
	out := &insightBlock{
		Hash: b.Hash, Size: b.Size, Height: b.Height, Version: insightNumberOrString(string(b.Version)),
		MerkleRoot: b.MerkleRoot, Tx: data.Txids, Time: b.Time, Nonce: insightNumberOrString(b.Nonce),
		Bits: b.Bits, Difficulty: insightNumberOrString(b.Difficulty), Confirmations: b.Confirmations,
		Prev: b.Prev, Next: b.Next, Reward: reward, IsMainChain: b.Confirmations != -1,
	}
	if s.rpc != nil {
		var header struct {
			Chainwork string `json:"chainwork"`
		}
		if err := s.rpc("getblockheader", []interface{}{b.Hash, true}, &header); err == nil {
			out.Chainwork = header.Chainwork
		}
	}
	return out, 0, nil
}

// insightBlocksQuery validates limit, blockDate and startTimestamp. A missing
// blockDate is today in UTC. end is the exclusive upper time bound: the next
// midnight, or startTimestamp+1 when startTimestamp is given (stock treats an
// explicit end as inclusive, so a page opened with the previous moreTs
// repeats that last row and skips none).
func insightBlocksQuery(route insightRoute, now time.Time) (limit int, day time.Time, end int64, isToday bool, ok bool) {
	limit = insightBlocksDefaultLimit
	if raw := route.query.Get("limit"); route.query.Has("limit") {
		n, err := strconv.Atoi(raw)
		if !insightDigits(raw) || err != nil || n < 1 || n > insightBlocksMaxLimit {
			return
		}
		limit = n
	}
	today := now.UTC().Truncate(24 * time.Hour)
	day, isToday = today, true
	if route.query.Has("blockDate") {
		var err error
		if day, err = time.Parse("2006-01-02", route.query.Get("blockDate")); err != nil {
			return
		}
		isToday = day.Equal(today)
	}
	end = day.Unix() + 86400
	if route.query.Has("startTimestamp") {
		raw := route.query.Get("startTimestamp")
		ts, err := strconv.ParseInt(raw, 10, 64)
		if !insightDigits(raw) || err != nil || ts < 1 || ts > 1<<40 {
			return
		}
		end = ts + 1
	}
	return limit, day, end, isToday, true
}

func (s *InsightServer) blocks(route insightRoute) (interface{}, int, error) {
	limit, day, end, isToday, ok := insightBlocksQuery(route, insightNow())
	if !ok {
		return nil, http.StatusBadRequest, errors.New("invalid blocks query")
	}
	// Stock: lte is startTimestamp or the next midnight; currentTs = lte-1 and
	// next is the date of lte.
	lte := end
	if route.query.Has("startTimestamp") {
		lte = end - 1
	}
	result := insightBlockList{Blocks: []insightBlockRow{}, Pagination: insightBlockPagination{
		Next: time.Unix(lte, 0).UTC().Format("2006-01-02"), Prev: day.AddDate(0, 0, -1).Format("2006-01-02"),
		CurrentTs: lte - 1, Current: day.Format("2006-01-02"), IsToday: isToday,
	}}
	for try := 0; try < insightTipRetries; try++ {
		rows, more, err := s.blocksInRange(day.Unix(), end, limit)
		if err == errInsightTipMoved {
			continue
		}
		if err == errInsightEmptyIndex {
			return nil, http.StatusServiceUnavailable, err
		}
		if err != nil {
			return nil, http.StatusInternalServerError, err
		}
		for _, r := range rows {
			result.Blocks = append(result.Blocks, insightBlockRow{Height: r.Height, Size: r.Size, Hash: r.Hash, Time: r.Time, TxLength: r.Txs, IsMainChain: true})
		}
		result.Length = len(result.Blocks)
		result.Pagination.More = more
		if more {
			t := rows[len(rows)-1].Time
			result.Pagination.MoreTs = &t
		}
		return result, 0, nil
	}
	return nil, http.StatusServiceUnavailable, errInsightTipMoved
}

// Block times are only loosely ordered (consensus allows a block to be up to
// about 2 h out of order), so the search can place a block within roughly 2 h
// of a boundary on the wrong side. It never errors on that, and the result is
// always a contiguous run of heights.
//
// blocksInRange returns up to limit rows with start <= time < end, newest
// first, and whether older rows in the range were cut off. Heights are found
// by binary search over single-row index reads, never by scanning.
func (s *InsightServer) blocksInRange(start, end int64, limit int) ([]db.BlockInfo, bool, error) {
	best, ok, err := s.insightTip()
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, errInsightEmptyIndex
	}
	cache := map[uint32]db.BlockInfo{}
	at := func(h uint32) (db.BlockInfo, error) {
		if row, ok := cache[h]; ok {
			return row, nil
		}
		row, err := s.insightRowAt(best, h)
		if err == nil {
			cache[h] = row
		}
		return row, err
	}
	// firstAtOrAfter is the lowest height in [lo, best+1) with time >= t.
	firstAtOrAfter := func(lo uint32, t int64) (uint32, error) {
		hi := best + 1
		for lo < hi {
			mid := lo + (hi-lo)/2
			row, err := at(mid)
			if err != nil {
				return 0, err
			}
			if row.Time >= t {
				hi = mid
			} else {
				lo = mid + 1
			}
		}
		return lo, nil
	}
	low, err := firstAtOrAfter(0, start)
	if err != nil {
		return nil, false, err
	}
	high, err := firstAtOrAfter(low, end)
	if err != nil {
		return nil, false, err
	}
	count := int(high - low)
	n := count
	if n > limit {
		n = limit
	}
	rows := make([]db.BlockInfo, 0, n)
	for i := 0; i < n; i++ {
		row, err := at(high - 1 - uint32(i))
		if err != nil {
			return nil, false, err
		}
		rows = append(rows, row)
	}
	return rows, count > limit, nil
}
