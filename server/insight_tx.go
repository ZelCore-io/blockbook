package server

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/trezor/blockbook/bchain"
)

// insightTxsPageSize is stock's page size for txs?block=.
const insightTxsPageSize = 10

// insightMaxPageNum bounds pageNum so page+1 cannot overflow; no block has
// that many pages, so a larger value is simply past the end.
const insightMaxPageNum = 1 << 30

// tx serves tx/:txid with spending info (spentTxId and friends) filled.
func (s *InsightServer) tx(route insightRoute) (interface{}, int, error) {
	txid, ok := insightHash64(route.arg)
	if !ok {
		return nil, http.StatusBadRequest, errors.New("invalid txid")
	}
	tx, err := s.worker.GetTransaction(txid, true, false)
	if err != nil {
		return nil, insightNotFoundCode(err, insightTxNotFound(txid)), err
	}
	out, err := insightTransaction(tx)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	return out, 0, nil
}

// rawTx serves rawtx/:txid. The worker's own GetRawTransaction is
// Ethereum-only, so the hex comes from the indexed tx, falling back to the
// daemon's getrawtransaction <txid> 0 when the tx carries no hex.
func (s *InsightServer) rawTx(route insightRoute) (interface{}, int, error) {
	txid, ok := insightHash64(route.arg)
	if !ok {
		return nil, http.StatusBadRequest, errors.New("invalid txid")
	}
	tx, err := s.worker.GetTransaction(txid, false, false)
	if err != nil {
		return nil, insightNotFoundCode(err, insightTxNotFound(txid)), err
	}
	if tx == nil {
		return nil, http.StatusInternalServerError, errors.New("missing transaction")
	}
	rawHex := tx.Hex
	if rawHex == "" {
		if s.rpc == nil {
			return nil, http.StatusInternalServerError, errors.New("transaction hex unavailable")
		}
		if rpcErr := s.rpc("getrawtransaction", []interface{}{txid, 0}, &rawHex); rpcErr != nil {
			var re *bchain.RPCError
			if errors.As(rpcErr, &re) && re.Code == insightRPCNotFound {
				return nil, http.StatusNotFound, rpcErr
			}
			return nil, http.StatusInternalServerError, rpcErr
		}
	}
	if rawHex == "" {
		return nil, http.StatusInternalServerError, errors.New("empty raw transaction")
	}
	if _, err := hex.DecodeString(rawHex); err != nil {
		return nil, http.StatusInternalServerError, err
	}
	return struct {
		Rawtx string `json:"rawtx"`
	}{rawHex}, 0, nil
}

// txs serves txs?block=<hash>&pageNum=N: stock pages of 10, pageNum 0-based,
// pagesTotal = ceil(TxCount/10). A page past the end has no txs.
// txs?address= is served by txsByAddress; neither selector is 400.
func (s *InsightServer) txs(route insightRoute) (interface{}, int, error) {
	_, hasBlock := route.query["block"]
	if !hasBlock {
		if _, hasAddress := route.query["address"]; hasAddress {
			return s.txsByAddress(route)
		}
		return nil, http.StatusBadRequest, errors.New("block or address required")
	}
	hash, ok := insightHash64(route.query.Get("block"))
	if !ok {
		return nil, http.StatusBadRequest, errors.New("invalid block hash")
	}
	pageNum, err := insightPageNum(route)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	block, err := s.worker.GetBlock(hash, pageNum+1, insightTxsPageSize)
	if err != nil {
		return nil, insightNotFoundCode(err, insightBlockNotFound), err
	}
	if block == nil {
		return nil, http.StatusInternalServerError, errors.New("missing block")
	}
	out := struct {
		PagesTotal int          `json:"pagesTotal"`
		Txs        []*insightTx `json:"txs"`
	}{(block.TxCount + insightTxsPageSize - 1) / insightTxsPageSize, make([]*insightTx, 0, insightTxsPageSize)}
	// The worker clamps a page past the end to the last page.
	if block.Page != pageNum+1 {
		return out, 0, nil
	}
	for _, light := range block.Transactions {
		if light == nil {
			return nil, http.StatusInternalServerError, errors.New("missing block transaction")
		}
		tx, err := s.worker.GetTransaction(light.Txid, false, false)
		if err != nil {
			return nil, http.StatusInternalServerError, err
		}
		item, err := insightTransaction(tx)
		if err != nil {
			return nil, http.StatusInternalServerError, err
		}
		out.Txs = append(out.Txs, item)
	}
	return out, 0, nil
}

// send serves POST tx/send with a JSON or form body {rawtx}.
func (s *InsightServer) send(route insightRoute) (interface{}, int, error) {
	var input struct {
		Rawtx string `json:"rawtx"`
	}
	if err := json.Unmarshal(route.body, &input); err != nil {
		return nil, 400, err
	}
	if input.Rawtx == "" {
		return nil, 400, errors.New("empty rawtx")
	}
	if _, err := hex.DecodeString(input.Rawtx); err != nil {
		return nil, 400, err
	}
	if s.broadcast == nil {
		return nil, 503, errors.New("broadcast unavailable")
	}
	txid, err := s.broadcast(input.Rawtx)
	if err != nil {
		return nil, 500, err
	}
	return struct {
		Txid string `json:"txid"`
	}{txid}, 0, nil
}
