package server

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"strconv"
	"strings"
	"sync"

	"github.com/trezor/blockbook/api"
)

// The Zcash-family stock Insight (bitcore-node getDetailedTransaction and
// insight-api's TxController.transformTransaction, on Zcash, Zclassic, Zero,
// BitcoinZ and Komodo alike) adds the shielded parts of a tx to every tx
// object and counts them in the fee. They come from the daemon's verbose tx
// JSON, which the worker's tx holds only for a pending tx: the tx cache
// stores the packed tx, not that JSON. The listener therefore reads
// getrawtransaction <txid> 1 for a confirmed tx, a page at a time with
// bounded concurrency, and keeps the extracted fields (they depend only on
// the tx bytes) in a size-bounded cache.

const (
	// insightZcashWorkers bounds concurrent getrawtransaction calls per page.
	insightZcashWorkers = 8
	// insightZcashCacheBytes bounds the cached fields (mostly the shielded
	// descriptions); entries past insightZcashMaxEntry are not kept.
	insightZcashCacheBytes = 32 << 20
	insightZcashMaxEntry   = 1 << 20
)

// insightJoinSplit is stock's vjoinsplit entry: vpub amounts as toFixed(8)
// strings.
type insightJoinSplit struct {
	VpubOld string `json:"vpub_old"`
	VpubNew string `json:"vpub_new"`
	N       int    `json:"n"`
}

// insightZcashRaw is the part of the daemon's verbose tx JSON stock reads.
type insightZcashRaw struct {
	Version         int32           `json:"version"`
	Overwintered    *bool           `json:"overwintered"`
	VersionGroupID  string          `json:"versiongroupid"`
	ExpiryHeight    json.Number     `json:"expiryheight"`
	ValueBalance    json.Number     `json:"valueBalance"`
	ValueBalanceZat json.Number     `json:"valueBalanceZat"`
	VShieldedSpend  json.RawMessage `json:"vShieldedSpend"`
	VShieldedOutput json.RawMessage `json:"vShieldedOutput"`
	BindingSig      string          `json:"bindingSig"`
	VJoinSplit      []struct {
		VpubOld    json.Number `json:"vpub_old"`
		VpubOldZat json.Number `json:"vpub_oldZat"`
		VpubNew    json.Number `json:"vpub_new"`
		VpubNewZat json.Number `json:"vpub_newZat"`
	} `json:"vjoinsplit"`
	Orchard *struct {
		ValueBalance    json.Number `json:"valueBalance"`
		ValueBalanceZat json.Number `json:"valueBalanceZat"`
	} `json:"orchard"`
}

// insightZcashFields are the stock fields of one tx and its net shielded
// value: sum(vpub_new - vpub_old) + valueBalance (+ the Orchard
// valueBalance on a v5 tx), what the shielded side adds to the inputs.
type insightZcashFields struct {
	joinSplits     *[]insightJoinSplit
	overwintered   *bool
	versionGroupID *uint32
	expiryHeight   json.Number
	sapling        bool
	valueBalance   json.Number
	spendDescs     json.RawMessage
	outputDescs    json.RawMessage
	bindingSig     string
	shielded       big.Int
	size           int
}

func insightZcashParse(data json.RawMessage) (*insightZcashFields, error) {
	var raw insightZcashRaw
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	f := &insightZcashFields{overwintered: raw.Overwintered, size: len(data)}
	// Stock: joinSplits for version >= 2 (empty when there are none).
	if raw.Version >= 2 {
		js := make([]insightJoinSplit, 0, len(raw.VJoinSplit))
		for i, j := range raw.VJoinSplit {
			old, err := insightZcashAmount(j.VpubOldZat, j.VpubOld)
			if err != nil {
				return nil, err
			}
			nw, err := insightZcashAmount(j.VpubNewZat, j.VpubNew)
			if err != nil {
				return nil, err
			}
			js = append(js, insightJoinSplit{insightFixed8(old), insightFixed8(nw), i})
			f.shielded.Add(&f.shielded, nw)
			f.shielded.Sub(&f.shielded, old)
		}
		f.joinSplits = &js
	}
	if raw.Overwintered == nil || !*raw.Overwintered {
		return f, nil
	}
	if id, err := strconv.ParseUint(raw.VersionGroupID, 16, 32); err == nil {
		v := uint32(id)
		f.versionGroupID = &v
	}
	f.expiryHeight = raw.ExpiryHeight
	if raw.Version >= 4 {
		vb, err := insightZcashAmount(raw.ValueBalanceZat, raw.ValueBalance)
		if err != nil {
			return nil, err
		}
		f.sapling = true
		f.valueBalance, _ = insightMoney((*api.Amount)(vb))
		f.spendDescs, f.outputDescs, f.bindingSig = raw.VShieldedSpend, raw.VShieldedOutput, raw.BindingSig
		f.shielded.Add(&f.shielded, vb)
		if raw.Orchard != nil {
			ob, err := insightZcashAmount(raw.Orchard.ValueBalanceZat, raw.Orchard.ValueBalance)
			if err != nil {
				return nil, err
			}
			f.shielded.Add(&f.shielded, ob)
		}
	}
	return f, nil
}

// insightZcashAmount returns an amount in zat from the daemon's integer zat
// field, or else from its coin value (eight decimals at most); both absent
// is 0.
func insightZcashAmount(zat, coins json.Number) (*big.Int, error) {
	if zat != "" {
		return insightZat(zat)
	}
	if coins == "" {
		return new(big.Int), nil
	}
	s := string(coins)
	negative := strings.HasPrefix(s, "-")
	n, err := insightCoinsToSat(strings.TrimPrefix(s, "-"))
	if err != nil {
		return nil, err
	}
	if negative {
		n.Neg(n)
	}
	return n, nil
}

// insightFixed8 renders zat as coins with exactly eight decimals, like
// JavaScript's (zat / 1e8).toFixed(8).
func insightFixed8(zat *big.Int) string {
	n := new(big.Int).Abs(zat)
	digits := n.String()
	for len(digits) <= 8 {
		digits = "0" + digits
	}
	s := digits[:len(digits)-8] + "." + digits[len(digits)-8:]
	if zat.Sign() < 0 {
		s = "-" + s
	}
	return s
}

// apply adds the stock fields to out and replaces the transparent fee with
// stock's: valueIn - valueOut + the net shielded value, so a t->z or z->t
// tx shows the fee it paid. A coinbase, or a tx whose input value is
// unknown, keeps the converter's fee.
func (f *insightZcashFields) apply(out *insightTx, tx *api.Tx) {
	out.VJoinSplit = f.joinSplits
	out.FOverwintered = f.overwintered
	if f.overwintered != nil && *f.overwintered {
		out.NVersionGroupID, out.NExpiryHeight = f.versionGroupID, f.expiryHeight
	}
	if f.sapling {
		out.ValueBalance, out.SpendDescs, out.OutputDescs, out.BindingSig = f.valueBalance, f.spendDescs, f.outputDescs, f.bindingSig
	}
	if out.IsCoinBase || tx.ValueInSat == nil {
		return
	}
	in, valueOut := tx.ValueInSat.AsBigInt(), tx.ValueOutSat.AsBigInt()
	fee := new(big.Int).Sub(&in, &valueOut)
	fee.Add(fee, &f.shielded)
	// An unknown input value can make it negative; the worker shows 0 then.
	if fee.Sign() < 0 {
		fee.SetInt64(0)
	}
	out.Fees, _ = insightMoney((*api.Amount)(fee))
}

type insightZcash struct {
	s     *InsightServer
	mu    sync.Mutex
	cache map[string]*insightZcashFields
	order []string
	bytes int
}

func newInsightZcash(s *InsightServer) insightExtension {
	return &insightZcash{s: s, cache: map[string]*insightZcashFields{}}
}

// The extension has no routes of its own.
func (z *insightZcash) match([]string) (string, string, []string, bool) { return "", "", nil, false }
func (z *insightZcash) handles(string) bool                             { return false }
func (z *insightZcash) handle(insightRoute) (interface{}, int, error) {
	return nil, 0, errors.New("no Zcash routes")
}

// decorateTx does nothing: the fields are added by loadTxs, which can fail.
func (z *insightZcash) decorateTx(*insightTx, *api.Tx) {}

// loadTxs reads the shielded fields of txs: from the daemon JSON the worker's
// tx carries (pending txs), the cache, or getrawtransaction, at most
// insightZcashWorkers calls at a time. Without daemon RPC the fields are
// left out.
func (z *insightZcash) loadTxs(ctx context.Context, txs []*api.Tx) ([]func(*insightTx), error) {
	fields := make([]*insightZcashFields, len(txs))
	var missing []int
	for i, tx := range txs {
		if tx == nil {
			continue
		}
		if len(tx.CoinSpecificData) > 0 {
			f, err := insightZcashParse(tx.CoinSpecificData)
			if err != nil {
				return nil, err
			}
			fields[i] = f
		} else if f := z.cached(tx.Txid); f != nil {
			fields[i] = f
		} else {
			missing = append(missing, i)
		}
	}
	if len(missing) > 0 && z.s.rpc != nil {
		if ctx == nil {
			ctx = context.Background()
		}
		errs := make([]error, len(txs))
		next := make(chan int)
		var wg sync.WaitGroup
		for w := 0; w < insightZcashWorkers && w < len(missing); w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range next {
					fields[i], errs[i] = z.fetch(txs[i].Txid)
				}
			}()
		}
	feed:
		for _, i := range missing {
			select {
			case next <- i:
			case <-ctx.Done():
				break feed
			}
		}
		close(next)
		wg.Wait()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, i := range missing {
			if errs[i] != nil {
				return nil, errs[i]
			}
		}
	}
	apply := make([]func(*insightTx), len(txs))
	for i := range txs {
		if f, tx := fields[i], txs[i]; f != nil {
			apply[i] = func(out *insightTx) { f.apply(out, tx) }
		}
	}
	return apply, nil
}

func (z *insightZcash) fetch(txid string) (*insightZcashFields, error) {
	var raw json.RawMessage
	if err := z.s.rpc("getrawtransaction", []interface{}{txid, 1}, &raw); err != nil {
		return nil, err
	}
	f, err := insightZcashParse(raw)
	if err != nil {
		return nil, err
	}
	z.store(txid, f)
	return f, nil
}

func (z *insightZcash) cached(txid string) *insightZcashFields {
	z.mu.Lock()
	defer z.mu.Unlock()
	return z.cache[txid]
}

// store keeps f, dropping the oldest entries past insightZcashCacheBytes.
func (z *insightZcash) store(txid string, f *insightZcashFields) {
	if f.size > insightZcashMaxEntry {
		return
	}
	z.mu.Lock()
	defer z.mu.Unlock()
	if _, ok := z.cache[txid]; ok {
		return
	}
	z.cache[txid] = f
	z.order = append(z.order, txid)
	z.bytes += f.size
	for z.bytes > insightZcashCacheBytes && len(z.order) > 0 {
		old := z.order[0]
		z.order = z.order[1:]
		z.bytes -= z.cache[old].size
		delete(z.cache, old)
	}
}
