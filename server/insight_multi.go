package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/trezor/blockbook/api"
)

const (
	// insightMultiMax bounds the addresses of utxo, unspent and balance;
	// insightMultiTxsMax those of the merged history (spec Call 3).
	insightMultiMax    = 100
	insightMultiTxsMax = 20
	// insightMultiDefaultSpan is stock multitxs' default window length.
	insightMultiDefaultSpan = 10
	// insightMultiTotalBudget bounds the txids one multitxs request may read
	// to make totalItems exact (controller ruling). Above it totalItems is the
	// sum of the per-address totals, an upper bound.
	insightMultiTotalBudget = 50000
	// insightMultiUtxoMax bounds the rows of one multi-address utxo/unspent
	// answer; above it the request gets 413. The single-address route is
	// not capped (the wallet needs the full set).
	insightMultiUtxoMax = 20000
)

var errInsightTooManyUtxos = &insightClientError{http.StatusRequestEntityTooLarge, "Too many unspent outputs, query fewer addresses"}

// insightClientGoneCode ends a multi-address loop once the client has gone
// (nobody reads the answer).
const insightClientGoneCode = http.StatusServiceUnavailable

// insightMultiInput is a decoded multi-address request: the addresses from
// the path, or from the POST body's addrs (JSON or form), and the body's
// raw from/to (read only by multitxs).
type insightMultiInput struct {
	addresses []string
	from, to  json.RawMessage
}

// insightBodyValue reads one JSON body field as a string: a string as is, a
// number as its literal text, absent or null as "". Anything else is an error.
func insightBodyValue(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	if raw[0] == '"' {
		var v string
		err := json.Unmarshal(raw, &v)
		return v, err
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		return "", err
	}
	return n.String(), nil
}

// insightMultiParse decodes the addresses and the body window of a multi
// route. Addresses are trimmed; empties and duplicates are dropped, first
// occurrence kept. No address, or more than limit distinct ones, is an error.
func insightMultiParse(route insightRoute, limit int) (insightMultiInput, error) {
	var in insightMultiInput
	raw := route.addresses
	if len(route.body) > 0 || raw == nil {
		var body map[string]json.RawMessage
		if err := json.Unmarshal(route.body, &body); err != nil {
			return in, err
		}
		in.from, in.to = body["from"], body["to"]
		if raw == nil {
			addrs, ok := body["addrs"]
			var list string
			if !ok || json.Unmarshal(addrs, &list) != nil {
				return in, errors.New("addrs must be a string")
			}
			raw = strings.Split(list, ",")
		}
	}
	seen := make(map[string]struct{}, min(len(raw), limit+1))
	for _, a := range raw {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if _, ok := seen[a]; ok {
			continue
		}
		if len(in.addresses) == limit {
			return in, errors.New("too many addresses")
		}
		seen[a] = struct{}{}
		in.addresses = append(in.addresses, a)
	}
	if len(in.addresses) == 0 {
		return in, errors.New("an address is required")
	}
	return in, nil
}

// multiUtxo serves addrs/:addrs/utxo, POST addrs/utxo and (onlyConfirmed)
// addrs/:addrs/unspent: the stock utxo rows of every address, in address
// order. A rejected address fails the whole request.
func (s *InsightServer) multiUtxo(route insightRoute, onlyConfirmed bool) (interface{}, int, error) {
	in, err := insightMultiParse(route, insightMultiMax)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	out := make([]insightUtxo, 0)
	for _, a := range in.addresses {
		if err := route.gone(); err != nil {
			return nil, insightClientGoneCode, err
		}
		var code int
		if out, code, err = s.appendUtxos(out, a, onlyConfirmed); err != nil {
			return nil, code, err
		}
		if len(out) > insightMultiUtxoMax {
			return nil, errInsightTooManyUtxos.code, errInsightTooManyUtxos
		}
	}
	return out, 0, nil
}

// multiBalance serves addrs/:addrs/balance (RunOnFlux balancesum): confirmed
// and pending balances summed over the addresses, as integer satoshis.
// Stock's immature is omitted: Blockbook has no maturity data.
func (s *InsightServer) multiBalance(route insightRoute) (interface{}, int, error) {
	in, err := insightMultiParse(route, insightMultiMax)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	var balance, pending big.Int
	for _, a := range in.addresses {
		if err := route.gone(); err != nil {
			return nil, insightClientGoneCode, err
		}
		address, err := s.worker.GetAddress(a, 1, 1, api.AccountDetailsTokens, insightHistoryFilter, "")
		if err != nil {
			return nil, insightAddressErrorCode(err), err
		}
		if address == nil {
			return nil, http.StatusInternalServerError, errors.New("missing address")
		}
		confirmedSat, pendingSat := address.BalanceSat.AsBigInt(), address.UnconfirmedBalanceSat.AsBigInt()
		balance.Add(&balance, &confirmedSat)
		pending.Add(&pending, &pendingSat)
	}
	_, balanceSat, err := insightMoney((*api.Amount)(&balance))
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	_, pendingSat, err := insightMoney((*api.Amount)(&pending))
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	return struct {
		Balance            int64 `json:"balance"`
		UnconfirmedBalance int64 `json:"unconfirmedBalance"`
	}{balanceSat, pendingSat}, 0, nil
}

// insightMultiWindow resolves the multitxs window like stock: from and to
// come from the path or query, else the body; from defaults to 0 and to to
// from+10. The result must also satisfy insightWindow and to <= 1000.
func insightMultiWindow(route insightRoute, in insightMultiInput) (int, int, error) {
	fromStr, toStr := route.from, route.to
	var err error
	if fromStr == "" {
		if fromStr, err = insightBodyValue(in.from); err != nil {
			return 0, 0, err
		}
	}
	if toStr == "" {
		if toStr, err = insightBodyValue(in.to); err != nil {
			return 0, 0, err
		}
	}
	if fromStr == "" {
		fromStr = "0"
	}
	if toStr == "" {
		if !insightDigits(fromStr) {
			return 0, 0, errors.New("invalid offset")
		}
		from, err := strconv.Atoi(fromStr)
		if err != nil || from > insightMaxWindow {
			return 0, 0, errors.New("invalid window")
		}
		toStr = strconv.Itoa(from + insightMultiDefaultSpan)
	}
	from, to, err := insightWindow(fromStr, toStr, insightMultiDefaultSpan)
	if err != nil {
		return 0, 0, err
	}
	if to > insightMaxWindow {
		return 0, 0, errors.New("window beyond the multi-address bound")
	}
	return from, to, nil
}

// multiTxs serves addrs/:addrs/txs[/:from[/:to]] with two or more addresses
// in the path and POST addrs/txs: the addresses' histories merged newest
// first (pending first, then by block height), deduplicated by txid, sliced
// [from,to) and returned as {totalItems,from,to,items}.
//
// Each address is asked for its first `to` light txs (heights only), which
// is enough for an exact merge of the window. totalItems is the size of the
// deduplicated union: when every history fits in `to` it is the merged
// count; otherwise the full txid list of each longer history is read and the
// union counted (spec Call 3), provided those lists hold at most
// insightMultiTotalBudget txids in all. Above the budget no full list is read
// and totalItems is the sum of the per-address totals, an upper bound.
func (s *InsightServer) multiTxs(route insightRoute) (interface{}, int, error) {
	in, err := insightMultiParse(route, insightMultiTxsMax)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	from, to, err := insightMultiWindow(route, in)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	size := max(to, 1)
	var pending, confirmed []*api.Tx
	owned := make(map[string]bool, len(in.addresses))
	// long holds the addresses whose history is longer than the window,
	// with their totals.
	type history struct {
		address string
		total   int
	}
	var long []history
	sumTotal, longTotal := 0, 0
	for _, a := range in.addresses {
		if err := route.gone(); err != nil {
			return nil, insightClientGoneCode, err
		}
		address, err := s.worker.GetAddress(a, 1, size, api.AccountDetailsTxHistoryLight, insightHistoryFilter, "")
		if err != nil {
			return nil, insightAddressErrorCode(err), err
		}
		if address == nil {
			return nil, http.StatusInternalServerError, errors.New("missing address")
		}
		owned[address.AddrStr] = true
		n := address.Txs + address.UnconfirmedTxs
		sumTotal += n
		if n > size {
			long = append(long, history{a, n})
			longTotal += n
		}
		for _, tx := range address.Transactions {
			if tx == nil {
				continue
			}
			if tx.Blockheight <= 0 || tx.Confirmations == 0 {
				pending = append(pending, tx)
			} else {
				confirmed = append(confirmed, tx)
			}
		}
	}
	sort.SliceStable(confirmed, func(i, j int) bool { return confirmed[i].Blockheight > confirmed[j].Blockheight })
	seen := make(map[string]struct{}, len(pending)+len(confirmed))
	merged := make([]string, 0, len(pending)+len(confirmed))
	for _, tx := range append(pending, confirmed...) {
		if _, ok := seen[tx.Txid]; !ok {
			seen[tx.Txid] = struct{}{}
			merged = append(merged, tx.Txid)
		}
	}
	total := len(merged)
	switch {
	case len(long) == 0:
	case len(in.addresses) == 1:
		// One distinct address: its own count, as the single-address route.
		total = long[0].total
	case longTotal > insightMultiTotalBudget:
		// Too many txids to count the union exactly: report the sum of the
		// per-address totals, an upper bound, and read no full list.
		total = max(sumTotal, len(merged))
	default:
		for _, h := range long {
			if err := route.gone(); err != nil {
				return nil, insightClientGoneCode, err
			}
			full, err := s.worker.GetAddress(h.address, 1, h.total, api.AccountDetailsTxidHistory, insightHistoryFilter, "")
			if err != nil {
				return nil, insightAddressErrorCode(err), err
			}
			if full == nil {
				return nil, http.StatusInternalServerError, errors.New("missing address")
			}
			for _, id := range full.Txids {
				seen[id] = struct{}{}
			}
		}
		total = len(seen)
	}
	ids := merged[min(from, len(merged)):min(to, len(merged))]
	items, code, err := s.ownedItems(owned, ids)
	if err != nil {
		return nil, code, err
	}
	return struct {
		TotalItems int          `json:"totalItems"`
		From       int          `json:"from"`
		To         int          `json:"to"`
		Items      []*insightTx `json:"items"`
	}{total, from, from + len(items), items}, 0, nil
}
