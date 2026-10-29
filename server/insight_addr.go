package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/trezor/blockbook/api"
)

const (
	// insightMaxWindow is the largest to-from of one history window.
	insightMaxWindow = 1000
	// insightMaxHistoryPage bounds the worker page number so the worker's
	// page*size arithmetic cannot overflow. No address has that many pages,
	// so a larger page is simply past the end.
	insightMaxHistoryPage = 1 << 30
	// insightHistoryDefaultTo and insightAddrDefaultTo are the default window
	// ends of addrs/:a/txs and of the addr/:a transactions list.
	insightHistoryDefaultTo = 50
	insightAddrDefaultTo    = 1000
)

var insightHistoryFilter = &api.AddressFilter{Vout: api.AddressFilterVoutOff}

// insightWindow parses a from/to history window. As in stock, the window
// is used only when both are given; otherwise it is 0..defTo. Both are
// digits only, from <= to and to-from <= 1000; to has no upper bound (deep
// paging).
func insightWindow(fromStr, toStr string, defTo int) (from, to int, err error) {
	if fromStr == "" || toStr == "" {
		return 0, defTo, nil
	}
	if !insightDigits(fromStr) || !insightDigits(toStr) {
		return 0, 0, errors.New("invalid offset")
	}
	if from, err = strconv.Atoi(fromStr); err != nil {
		return 0, 0, err
	}
	if to, err = strconv.Atoi(toStr); err != nil {
		return 0, 0, err
	}
	if from > to || to-from > insightMaxWindow {
		return 0, 0, errors.New("invalid window")
	}
	return from, to, nil
}

// addressPages returns the deduplicated txids in [from,to) of the address's
// merged newest-first sequence (pending first, then confirmed: Blockbook's
// paged order, see computeAccountPaging), plus a worker answer for the
// summary and the total Txs+UnconfirmedTxs. The int is the HTTP status for
// an error.
//
// The worker page size is the window size, so the window lies in at most two
// worker pages and the worker reads about `to` txids, not a fixed 1000. A
// window that does not start on the first page is preceded by a cheap totals
// query, and a window at or past the end stops there, so a huge from never
// makes the worker scan the whole history. Offsets are positions in the
// worker's sequence, so consecutive windows tile it without gaps; a txid
// repeated inside one window is returned once.
func (s *InsightServer) addressPages(address string, from, to int) (*api.Address, []string, int, int, error) {
	size := to - from
	if size <= 0 || from >= size {
		a, err := s.worker.GetAddress(address, 1, 1, api.AccountDetailsTokens, insightHistoryFilter, "")
		if err != nil {
			return nil, nil, 0, insightAddressErrorCode(err), err
		}
		if a == nil {
			return nil, nil, 0, http.StatusInternalServerError, errors.New("missing address")
		}
		if total := a.Txs + a.UnconfirmedTxs; size <= 0 || from >= total {
			return a, []string{}, total, 0, nil
		}
	}
	first, last := from/size+1, (to-1)/size+1
	var paged *api.Address
	var ids []string
	for page := first; page <= last; page++ {
		a, err := s.worker.GetAddress(address, min(page, insightMaxHistoryPage), size, api.AccountDetailsTxidHistory, insightHistoryFilter, "")
		if err != nil {
			return nil, nil, 0, insightAddressErrorCode(err), err
		}
		if a == nil {
			return nil, nil, 0, http.StatusInternalServerError, errors.New("missing address")
		}
		if paged == nil {
			paged = a
		}
		// The worker clamps a page past the end to the last page.
		if a.Page != page {
			break
		}
		ids = append(ids, a.Txids...)
	}
	// The paged answer's totals match the sequence the offsets index.
	head := paged
	base := (first - 1) * size
	lo, hi := min(from-base, len(ids)), min(to-base, len(ids))
	out := make([]string, 0, hi-lo)
	seen := make(map[string]struct{}, hi-lo)
	for _, id := range ids[lo:hi] {
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return head, out, head.Txs + head.UnconfirmedTxs, 0, nil
}

// insightOwnTx returns a copy of tx with isOwn set on the inputs and outputs
// that pay or spend address alone, as Blockbook's address history does
// (setIsOwnAddress). The worker's tx is not modified.
func insightOwnTx(tx *api.Tx, address string) *api.Tx {
	if tx == nil {
		return nil
	}
	own := func(addresses []string) bool { return len(addresses) == 1 && addresses[0] == address }
	c := *tx
	if tx.Vin != nil {
		c.Vin = append([]api.Vin{}, tx.Vin...)
	}
	if tx.Vout != nil {
		c.Vout = append([]api.Vout{}, tx.Vout...)
	}
	for i := range c.Vin {
		if own(c.Vin[i].Addresses) {
			c.Vin[i].IsOwn = true
		}
	}
	for i := range c.Vout {
		if own(c.Vout[i].Addresses) {
			c.Vout[i].IsOwn = true
		}
	}
	return &c
}

// historyItems loads and transforms the txs of an address history window.
func (s *InsightServer) historyItems(address string, ids []string) ([]*insightTx, int, error) {
	items := make([]*insightTx, 0, len(ids))
	for _, id := range ids {
		tx, err := s.worker.GetTransaction(id, false, false)
		if err != nil {
			return nil, http.StatusInternalServerError, err
		}
		if tx == nil {
			return nil, http.StatusInternalServerError, errors.New("missing transaction")
		}
		item, err := insightTransaction(insightOwnTx(tx, address))
		if err != nil {
			return nil, http.StatusInternalServerError, err
		}
		items = append(items, item)
	}
	return items, 0, nil
}

// history serves addrs/:a/txs[/:from[/:to]] and addrs/txs?address=.
func (s *InsightServer) history(route insightRoute, addr string) (interface{}, int, error) {
	from, to, err := insightWindow(route.from, route.to, insightHistoryDefaultTo)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	head, ids, total, code, err := s.addressPages(addr, from, to)
	if err != nil {
		return nil, code, err
	}
	items, code, err := s.historyItems(head.AddrStr, ids)
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

// insightParseIntTruthy is JavaScript's Boolean(parseInt(value)): leading
// whitespace, an optional sign, then digits; true when they are not zero.
func insightParseIntTruthy(value string) bool {
	value = strings.TrimLeft(value, " \t\n\r\v\f")
	if value != "" && (value[0] == '+' || value[0] == '-') {
		value = value[1:]
	}
	nonzero := false
	for _, c := range value {
		if c < '0' || c > '9' {
			break
		}
		nonzero = nonzero || c != '0'
	}
	return nonzero
}

// summary serves addr/:a and addr?address=: the summary plus the stock
// transactions txid list for the from/to window (default 0..1000). noTxList
// (stock parseInt truthiness) or the converter's addr/:a/<x> path returns the
// summary alone, from a balance-only query as before.
func (s *InsightServer) summary(route insightRoute, addr string) (interface{}, int, error) {
	if route.arg != "" || insightParseIntTruthy(route.query.Get("noTxList")) {
		address, err := s.worker.GetAddress(addr, 1, 1, api.AccountDetailsTokens, insightHistoryFilter, "")
		if err != nil {
			return nil, insightAddressErrorCode(err), err
		}
		out, err := insightAddress(address)
		if err != nil {
			return nil, http.StatusInternalServerError, err
		}
		return out, 0, nil
	}
	from, to, err := insightWindow(route.from, route.to, insightAddrDefaultTo)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	head, ids, _, code, err := s.addressPages(addr, from, to)
	if err != nil {
		return nil, code, err
	}
	out, err := insightAddress(head)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	return struct {
		*insightSummary
		Transactions []string `json:"transactions"`
	}{out, ids}, 0, nil
}

// addrValue serves addr/:a/balance|totalReceived|totalSent|unconfirmedBalance
// as a bare JSON integer of satoshis (stock res.jsonp(data[param])).
func (s *InsightServer) addrValue(route insightRoute, addr string) (interface{}, int, error) {
	address, err := s.worker.GetAddress(addr, 1, 1, api.AccountDetailsTokens, insightHistoryFilter, "")
	if err != nil {
		return nil, insightAddressErrorCode(err), err
	}
	if address == nil {
		return nil, http.StatusInternalServerError, errors.New("missing address")
	}
	var amount *api.Amount
	switch route.arg {
	case "balance":
		amount = address.BalanceSat
	case "totalReceived":
		amount = address.TotalReceivedSat
	case "totalSent":
		amount = address.TotalSentSat
	case "unconfirmedBalance":
		amount = address.UnconfirmedBalanceSat
	default:
		return nil, http.StatusNotFound, errors.New("unknown address value")
	}
	_, sat, err := insightMoney(amount)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	return sat, 0, nil
}

// utxo serves addr/:a/utxo and addr/utxo?address=, pending included.
func (s *InsightServer) utxo(route insightRoute, addr string) (interface{}, int, error) {
	rows, err := s.worker.GetAddressUtxo(addr, false)
	if err != nil {
		return nil, insightAddressErrorCode(err), err
	}
	out := make([]insightUtxo, 0, len(rows))
	for _, u := range rows {
		amount, sat, err := insightMoney(u.AmountSat)
		if err != nil {
			return nil, http.StatusInternalServerError, err
		}
		address := u.Address
		if address == "" {
			address = route.addresses[0]
		}
		out = append(out, insightUtxo{address, u.Txid, u.Vout, amount, sat, u.Height, u.Confirmations, u.Coinbase})
	}
	return out, 0, nil
}

// insightPageNum parses the 0-based stock pageNum query (default 0). A value
// too large for an int, or above insightMaxPageNum, is insightMaxPageNum:
// past the end of any list.
func insightPageNum(route insightRoute) (int, error) {
	values, ok := route.query["pageNum"]
	if !ok {
		return 0, nil
	}
	value := ""
	if len(values) > 0 {
		value = values[0]
	}
	if !insightDigits(value) {
		return 0, errors.New("invalid pageNum")
	}
	n, err := strconv.Atoi(value)
	if err != nil || n > insightMaxPageNum {
		n = insightMaxPageNum
	}
	return n, nil
}

// txsByAddress serves txs?address=<a>&pageNum=N: stock pages of 10 over the
// address history, pagesTotal = ceil(total/10).
func (s *InsightServer) txsByAddress(route insightRoute) (interface{}, int, error) {
	addr := route.query.Get("address")
	if addr == "" || strings.Contains(addr, ",") {
		return nil, http.StatusBadRequest, errors.New("a single address is required")
	}
	pageNum, err := insightPageNum(route)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	from := pageNum * insightTxsPageSize
	head, ids, total, code, err := s.addressPages(addr, from, from+insightTxsPageSize)
	if err != nil {
		return nil, code, err
	}
	items, code, err := s.historyItems(head.AddrStr, ids)
	if err != nil {
		return nil, code, err
	}
	return struct {
		PagesTotal int          `json:"pagesTotal"`
		Txs        []*insightTx `json:"txs"`
	}{(total + insightTxsPageSize - 1) / insightTxsPageSize, items}, 0, nil
}
