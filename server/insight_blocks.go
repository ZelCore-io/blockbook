package server

import (
	"encoding/hex"
	"errors"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/trezor/blockbook/api"
	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/db"
)

const (
	insightBlocksDefaultLimit = 100
	insightBlocksMaxLimit     = 200
	insightRPCBlockNotFound   = -5
	insightTipRetries         = 3
)

// insightBlockPageSize is how many txs one GetBlock call loads. A var so tests
// can force paging.
var insightBlockPageSize = 1000

// insightNow is the clock for "today"; a var so tests can pin it.
var insightNow = time.Now

var errInsightTipMoved = errors.New("chain tip moved during the request")

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

// insightBlockErrorCode maps a worker error: a public API error is an unknown
// block (404), anything else is a backend failure.
func insightBlockErrorCode(err error) int {
	var apiErr *api.APIError
	if errors.As(err, &apiErr) && apiErr.Public {
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
			return nil, http.StatusServiceUnavailable, errors.New("no indexed block")
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

func (s *InsightServer) rawBlock(route insightRoute) (interface{}, int, error) {
	hash, ok := insightHash64(route.arg)
	if !ok {
		return nil, http.StatusBadRequest, errors.New("invalid block hash")
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
		return nil, insightBlockErrorCode(err), err
	}
	// The index cannot serve the raw block (for example Zebra): ask the daemon.
	var rawHex string
	if rpcErr := s.rpc("getblock", []interface{}{hash, 0}, &rawHex); rpcErr != nil {
		var re *bchain.RPCError
		if errors.As(rpcErr, &re) && re.Code == insightRPCBlockNotFound {
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

// loadBlock fetches a block with all of its transactions, paging as needed.
func (s *InsightServer) loadBlock(hash string) (*api.Block, []*api.Tx, error) {
	first, err := s.worker.GetBlock(hash, 1, insightBlockPageSize)
	if err != nil {
		return nil, nil, err
	}
	if first == nil {
		return nil, nil, errors.New("missing block")
	}
	txs := append(make([]*api.Tx, 0, first.TxCount), first.Transactions...)
	for page := 2; len(txs) < first.TxCount; page++ {
		if page > first.TotalPages {
			return nil, nil, errors.New("block transactions missing")
		}
		next, err := s.worker.GetBlock(hash, page, insightBlockPageSize)
		if err != nil {
			return nil, nil, err
		}
		if next == nil || next.Page != page || len(next.Transactions) == 0 {
			return nil, nil, errors.New("block page missing")
		}
		txs = append(txs, next.Transactions...)
	}
	return first, txs, nil
}

// insightBlockReward is the coinbase output total minus the fees of every tx
// in the block, as an integer.
func insightBlockReward(txs []*api.Tx) *api.Amount {
	total := new(big.Int)
	if len(txs) > 0 && txs[0] != nil {
		for _, out := range txs[0].Vout {
			v := out.ValueSat.AsBigInt()
			total.Add(total, &v)
		}
	}
	for _, tx := range txs {
		if tx != nil {
			fee := tx.FeesSat.AsBigInt()
			total.Sub(total, &fee)
		}
	}
	return (*api.Amount)(total)
}

func (s *InsightServer) block(route insightRoute) (interface{}, int, error) {
	hash, ok := insightHash64(route.arg)
	if !ok {
		return nil, http.StatusBadRequest, errors.New("invalid block hash")
	}
	b, txs, err := s.loadBlock(hash)
	if err != nil {
		return nil, insightBlockErrorCode(err), err
	}
	reward, _, err := insightMoney(insightBlockReward(txs))
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	txids := make([]string, 0, len(txs))
	for _, tx := range txs {
		if tx == nil {
			return nil, http.StatusInternalServerError, errors.New("missing block transaction")
		}
		txids = append(txids, tx.Txid)
	}
	out := &insightBlock{
		Hash: b.Hash, Size: b.Size, Height: b.Height, Version: insightNumberOrString(string(b.Version)),
		MerkleRoot: b.MerkleRoot, Tx: txids, Time: b.Time, Nonce: insightNumberOrString(b.Nonce),
		Bits: b.Bits, Difficulty: insightNumberOrString(b.Difficulty), Confirmations: b.Confirmations,
		Prev: b.Prev, Next: b.Next, Reward: reward, IsMainChain: true,
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

// insightBlocksQuery validates limit and blockDate. A missing blockDate is
// today in UTC.
func insightBlocksQuery(route insightRoute, now time.Time) (limit int, day time.Time, isToday bool, ok bool) {
	limit = insightBlocksDefaultLimit
	if raw := route.query.Get("limit"); route.query.Has("limit") {
		n, err := strconv.Atoi(raw)
		if !insightDigits(raw) || err != nil || n < 1 || n > insightBlocksMaxLimit {
			return 0, time.Time{}, false, false
		}
		limit = n
	}
	today := now.UTC().Truncate(24 * time.Hour)
	if !route.query.Has("blockDate") {
		return limit, today, true, true
	}
	day, err := time.Parse("2006-01-02", route.query.Get("blockDate"))
	if err != nil {
		return 0, time.Time{}, false, false
	}
	return limit, day, day.Equal(today), true
}

func (s *InsightServer) blocks(route insightRoute) (interface{}, int, error) {
	limit, day, isToday, ok := insightBlocksQuery(route, insightNow())
	if !ok {
		return nil, http.StatusBadRequest, errors.New("invalid blocks query")
	}
	start, end := day.Unix(), day.Unix()+86400
	list := insightBlockList{Blocks: []insightBlockRow{}, Pagination: insightBlockPagination{
		Next: day.AddDate(0, 0, 1).Format("2006-01-02"), Prev: day.AddDate(0, 0, -1).Format("2006-01-02"),
		CurrentTs: end - 1, Current: day.Format("2006-01-02"), IsToday: isToday,
	}}
	for try := 0; try < insightTipRetries; try++ {
		rows, more, err := s.blocksInRange(start, end, limit)
		if err == errInsightTipMoved {
			continue
		}
		if err != nil {
			return nil, http.StatusInternalServerError, err
		}
		for _, r := range rows {
			list.Blocks = append(list.Blocks, insightBlockRow{Height: r.Height, Size: r.Size, Hash: r.Hash, Time: r.Time, TxLength: r.Txs, IsMainChain: true})
		}
		list.Length = len(list.Blocks)
		list.Pagination.More = more
		if more {
			t := rows[len(rows)-1].Time
			list.Pagination.MoreTs = &t
		}
		return list, 0, nil
	}
	return nil, http.StatusServiceUnavailable, errInsightTipMoved
}

// blocksInRange returns up to limit rows with start <= time < end, newest
// first, and whether older rows in the range were cut off. Heights are found
// by binary search over single-row index reads, never by scanning.
func (s *InsightServer) blocksInRange(start, end int64, limit int) ([]db.BlockInfo, bool, error) {
	best, ok, err := s.insightTip()
	if err != nil || !ok {
		return nil, false, err
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
