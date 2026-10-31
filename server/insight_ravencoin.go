package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/url"
	"strings"

	"github.com/trezor/blockbook/api"
)

// The Ravencoin extension serves what the RVN stock Insight (RavenDevKit
// insight-api on ravencore-node) adds for assets, from ravend's address and
// asset indexes (addressindex, assetindex, spentindex, timestampindex), so
// Blockbook's own index needs no asset support:
//
//   - addr/:addr/asset/:asset/utxo and addrs/:addrs/asset/:asset/utxo: the
//     asset utxos (getaddressutxos with assetName, "*" = every asset), merged
//     with the mempool as stock does.
//   - address history (addrs/:addr/txs, txs?address=, multi-address
//     history): the txid list comes from ravend getaddresstxids with assets,
//     so asset-only txs, which Blockbook files under no address, are listed.
//     The list is cached per chain tip (insight_ravencoin_txids.go).
//   - every tx: asset outputs get stock's assetType, asset and owner address;
//     inputs spending an asset output get the owner as addr.
//   - assets?asset=&verbose=: stock always failed it (its controller calls a
//     method the node lacks); it stays a 500, as a JSON error.
//
// The other RVN-only stock routes (addr/:addr/balances, tags, frozen, the
// assets/* and asset/:asset/* lists) never worked on stock and stay 404.

const (
	insightRvnPrefix = "ravencoin/"
	// insightRvnAssetNameMax bounds an asset path argument; ravend names are
	// at most 32 characters (sub-asset and unique-tag paths included).
	insightRvnAssetNameMax = 128
)

// insightRvnUnserved are the addr/:addr/<x> stock routes that never worked.
var insightRvnUnserved = map[string]bool{"balances": true, "tags": true, "frozen": true}

type insightRavencoin struct {
	s *InsightServer
	// txids caches and rations ravend's getaddresstxids lists
	// (insight_ravencoin_txids.go); tipHash keys them, Blockbook's best
	// block hash.
	txids   *insightRvnTxidCache
	tipHash func() (string, bool)
}

func newInsightRavencoin(s *InsightServer) insightExtension {
	x := &insightRavencoin{s: s, txids: newInsightRvnTxidCache(insightRvnTxidsMaxEntries, insightRvnTxidsMaxIds, insightRvnTxidsSlots, insightRvnTxidsWait)}
	x.tipHash = x.insightRvnTipHash
	return x
}

func (x *insightRavencoin) match(parts []string) (string, string, []string, bool) {
	if len(parts) == 1 && parts[0] == "assets" {
		return insightRvnPrefix + "assets", "", insightGet, true
	}
	return "", "", nil, false
}

// matchEscaped matches the asset utxo routes on the escaped path, so an
// asset name sent with an encoded "/" (sub-assets, WXRAVEN%2FAIRDROP) or "#"
// (qualifiers, %23KYC) stays one path argument.
//
// addr/:addr/balances, tags and frozen, which stock answered with an empty
// body or a 500, are claimed here only to answer the Insight 404 instead of
// the generic converter form addr/:addr/<x> (the summary).
func (x *insightRavencoin) matchEscaped(parts []string) (insightRoute, []string, bool) {
	if len(parts) == 3 && parts[0] == "addr" && insightRvnUnserved[parts[2]] {
		return insightRoute{kind: insightRvnPrefix + "unserved"}, insightGet, true
	}
	if len(parts) != 5 || parts[2] != "asset" || parts[4] != "utxo" || (parts[0] != "addr" && parts[0] != "addrs") {
		return insightRoute{}, nil, false
	}
	addr, err := url.PathUnescape(parts[1])
	if err != nil {
		return insightRoute{}, nil, false
	}
	asset, err := url.PathUnescape(parts[3])
	if err != nil {
		return insightRoute{}, nil, false
	}
	route := insightRoute{kind: insightRvnPrefix + "assetutxo", addresses: []string{addr}, arg: asset}
	if parts[0] == "addrs" {
		route.kind, route.addresses = insightRvnPrefix+"multiassetutxo", strings.Split(addr, ",")
	}
	return route, insightGet, true
}

func (x *insightRavencoin) handles(kind string) bool {
	return strings.HasPrefix(kind, insightRvnPrefix)
}

func (x *insightRavencoin) handle(route insightRoute) (interface{}, int, error) {
	switch strings.TrimPrefix(route.kind, insightRvnPrefix) {
	case "unserved":
		return nil, http.StatusNotFound, &insightClientError{http.StatusNotFound, "Route not found"}
	case "assets":
		// Stock: TypeError (this.node.listAssets is not a function), 500.
		return nil, http.StatusInternalServerError, errors.New("asset list not supported")
	case "assetutxo":
		addr, ok := insightSingleAddress(route)
		if !ok {
			return nil, http.StatusBadRequest, errors.New("a single address is required")
		}
		return x.assetUtxos(route, []string{addr}, 0)
	case "multiassetutxo":
		in, err := insightMultiParse(route, insightMultiMax)
		if err != nil {
			return nil, http.StatusBadRequest, err
		}
		return x.assetUtxos(route, in.addresses, insightMultiUtxoMax)
	}
	return nil, http.StatusNotImplemented, errors.New("route not implemented")
}

// checkAddresses returns 400 for an address the chain parser rejects.
func (x *insightRavencoin) checkAddresses(addresses []string) (int, error) {
	if x.s.parser == nil {
		return 0, nil
	}
	for _, a := range addresses {
		if _, err := x.s.parser.GetAddrDescFromAddress(a); err != nil {
			return http.StatusBadRequest, &insightClientError{http.StatusBadRequest, "Invalid address"}
		}
	}
	return 0, nil
}

// insightRvnUtxo is a ravend getaddressutxos entry.
type insightRvnUtxo struct {
	Address     string      `json:"address"`
	AssetName   string      `json:"assetName"`
	Txid        string      `json:"txid"`
	OutputIndex int         `json:"outputIndex"`
	Script      string      `json:"script"`
	Satoshis    json.Number `json:"satoshis"`
	Height      int         `json:"height"`
}

// insightRvnDelta is a ravend getaddressmempool entry.
type insightRvnDelta struct {
	Address   string      `json:"address"`
	AssetName string      `json:"assetName"`
	Txid      string      `json:"txid"`
	Index     int         `json:"index"`
	Satoshis  json.Number `json:"satoshis"`
	Timestamp int64       `json:"timestamp"`
	PrevTxid  string      `json:"prevtxid"`
	PrevOut   int         `json:"prevout"`
}

// insightRvnAssetUtxo is stock's asset utxo row (transformUtxo): height only
// for a confirmed row, ts only for a mempool row.
type insightRvnAssetUtxo struct {
	Address       string      `json:"address"`
	Txid          string      `json:"txid"`
	Vout          int         `json:"vout"`
	ScriptPubKey  string      `json:"scriptPubKey"`
	AssetName     string      `json:"assetName"`
	Amount        json.Number `json:"amount"`
	Satoshis      json.Number `json:"satoshis"`
	Height        int         `json:"height,omitempty"`
	Confirmations int         `json:"confirmations"`
	Ts            int64       `json:"ts,omitempty"`
}

func insightRvnSatoshis(n json.Number) (json.Number, json.Number, error) {
	v, ok := new(big.Int).SetString(n.String(), 10)
	if !ok {
		return "", "", errors.New("invalid satoshis")
	}
	amount, sat := insightMoney((*api.Amount)(v))
	return amount, sat, nil
}

// assetUtxos is stock's getAddressUnspentOutputs with an assetName and the
// mempool: ravend's confirmed utxos newest first, preceded by the matching
// mempool outputs (newest first, those already confirmed dropped), minus the
// outputs a mempool tx spends. scriptPubKey is ravend's output script, which
// includes the asset part the wallet signs over. A mempool row has the real
// output script too, where stock put the address's plain P2PKH script. A
// limit above 0 caps the rows (413).
func (x *insightRavencoin) assetUtxos(route insightRoute, addresses []string, limit int) (interface{}, int, error) {
	asset := route.arg
	if asset == "" || len(asset) > insightRvnAssetNameMax {
		return nil, http.StatusBadRequest, errors.New("invalid asset name")
	}
	if code, err := x.checkAddresses(addresses); err != nil {
		return nil, code, err
	}
	if x.s.rpc == nil {
		return nil, http.StatusNotImplemented, errors.New("no daemon RPC")
	}
	var deltas []insightRvnDelta
	if err := x.s.rpc("getaddressmempool", []interface{}{map[string]interface{}{"addresses": addresses}, true}, &deltas); err != nil {
		return nil, http.StatusInternalServerError, err
	}
	var confirmed struct {
		Utxos  []insightRvnUtxo `json:"utxos"`
		Height int              `json:"height"`
	}
	if err := x.s.rpc("getaddressutxos", []interface{}{map[string]interface{}{"addresses": addresses, "chaininfo": true, "assetName": asset}}, &confirmed); err != nil {
		return nil, http.StatusInternalServerError, err
	}
	if err := route.gone(); err != nil {
		return nil, insightClientGoneCode, err
	}
	type outpoint struct {
		txid string
		n    int
	}
	spent := map[outpoint]bool{}
	known := make(map[outpoint]bool, len(confirmed.Utxos))
	for _, u := range confirmed.Utxos {
		known[outpoint{u.Txid, u.OutputIndex}] = true
	}
	var pending []insightRvnDelta
	for _, d := range deltas {
		// stock: delta.prevtxid && delta.satoshis <= 0 marks a spend
		if d.PrevTxid != "" && insightRvnNonPositive(d.Satoshis) {
			spent[outpoint{d.PrevTxid, d.PrevOut}] = true
			continue
		}
		if (asset == "*" && d.AssetName != "RVN") || asset == d.AssetName {
			if !known[outpoint{d.Txid, d.Index}] {
				pending = append(pending, d)
			}
		}
	}
	rows := len(pending) + len(confirmed.Utxos)
	if limit > 0 && rows > limit {
		return nil, errInsightTooManyUtxos.code, errInsightTooManyUtxos
	}
	out := make([]insightRvnAssetUtxo, 0, rows)
	scripts := map[string]*api.Tx{}
	for i := len(pending) - 1; i >= 0; i-- {
		d := pending[i]
		if spent[outpoint{d.Txid, d.Index}] {
			continue
		}
		amount, sat, err := insightRvnSatoshis(d.Satoshis)
		if err != nil {
			return nil, http.StatusInternalServerError, err
		}
		out = append(out, insightRvnAssetUtxo{Address: d.Address, Txid: d.Txid, Vout: d.Index, ScriptPubKey: x.mempoolScript(scripts, d), AssetName: d.AssetName, Amount: amount, Satoshis: sat, Ts: d.Timestamp})
	}
	for i := len(confirmed.Utxos) - 1; i >= 0; i-- {
		u := confirmed.Utxos[i]
		if spent[outpoint{u.Txid, u.OutputIndex}] {
			continue
		}
		amount, sat, err := insightRvnSatoshis(u.Satoshis)
		if err != nil {
			return nil, http.StatusInternalServerError, err
		}
		row := insightRvnAssetUtxo{Address: u.Address, Txid: u.Txid, Vout: u.OutputIndex, ScriptPubKey: u.Script, AssetName: u.AssetName, Amount: amount, Satoshis: sat}
		if u.Height > 0 {
			row.Height, row.Confirmations = u.Height, max(confirmed.Height-u.Height+1, 0)
		}
		out = append(out, row)
	}
	return out, 0, nil
}

func insightRvnNonPositive(n json.Number) bool {
	v, ok := new(big.Int).SetString(n.String(), 10)
	return ok && v.Sign() <= 0
}

// mempoolScript returns the output script of a mempool asset output from the
// worker's tx, or stock's plain address script when the tx cannot be read.
func (x *insightRavencoin) mempoolScript(txs map[string]*api.Tx, d insightRvnDelta) string {
	tx, ok := txs[d.Txid]
	if !ok {
		tx, _ = x.s.worker.GetTransaction(d.Txid, false, false)
		txs[d.Txid] = tx
	}
	if tx != nil {
		for _, v := range tx.Vout {
			if v.N == d.Index && v.Hex != "" {
				return v.Hex
			}
		}
	}
	return x.s.insightAddressScript(d.Address)
}

// addressTxids is stock's getAddressTxids for the history routes: the
// addresses' mempool txids (newest first, those already confirmed dropped),
// then ravend's getaddresstxids list with assets reversed (confirmedTxids:
// for one address that is ravend's index order, grouped by asset, reversed,
// as stock; for several, height then txid, descending). Stock asked the
// mempool without assets, so a pending asset-only tx was missing from its
// history; here it is listed. A txid is listed once. The mempool is read on
// every call; the confirmed list is cached per chain tip. The returned slice
// may be shared and must not be modified.
func (x *insightRavencoin) addressTxids(ctx context.Context, addresses []string) ([]string, int, error) {
	if code, err := x.checkAddresses(addresses); err != nil {
		return nil, code, err
	}
	if x.s.rpc == nil {
		return nil, http.StatusNotImplemented, errors.New("no daemon RPC")
	}
	var deltas []insightRvnDelta
	if err := x.s.rpc("getaddressmempool", []interface{}{map[string]interface{}{"addresses": addresses}, true}, &deltas); err != nil {
		return nil, http.StatusInternalServerError, err
	}
	listed, code, err := x.confirmedTxids(ctx, addresses)
	if err != nil {
		return nil, code, err
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, insightClientGoneCode, err
		}
	}
	if len(deltas) == 0 {
		return listed, 0, nil
	}
	seen := make(map[string]bool, len(deltas))
	for _, d := range deltas {
		seen[d.Txid] = false
	}
	for _, id := range listed {
		if _, ok := seen[id]; ok {
			seen[id] = true
		}
	}
	var pending []string
	for _, d := range deltas {
		if !seen[d.Txid] {
			seen[d.Txid] = true
			pending = append(pending, d.Txid)
		}
	}
	if len(pending) == 0 {
		return listed, 0, nil
	}
	out := make([]string, 0, len(pending)+len(listed))
	for i := len(pending) - 1; i >= 0; i-- {
		out = append(out, pending[i])
	}
	return append(out, listed...), 0, nil
}

// decorateTx adds stock's asset fields: an asset output gets assetType,
// asset, its owner as scriptPubKey.addresses (and addresses, isAddress) and
// the owner's scriptPubKey.type; an input that spends an asset output gets
// the owner as addr. Values stay 0, the RVN carried by an asset output.
func (x *insightRavencoin) decorateTx(out *insightTx, tx *api.Tx) {
	if out == nil || tx == nil {
		return
	}
	for i := range out.Vout {
		vout := &out.Vout[i]
		raw, err := hex.DecodeString(vout.Hex)
		if err != nil {
			continue
		}
		a, ok := insightRvnDecodeAsset(raw)
		if !ok {
			continue
		}
		asset := a.asset
		vout.AssetType, vout.Asset = a.assetType, &asset
		vout.ScriptPubKey.Type = a.ownerType
		if owner := x.ownerAddress(a.owner); owner != "" {
			vout.Addresses, vout.IsAddress = []string{owner}, true
			vout.ScriptPubKey.Addresses, vout.ScriptPubKey.IsAddress = []string{owner}, true
		}
	}
	for i := range out.Vin {
		if i >= len(tx.Vin) || out.Vin[i].Addr != nil || len(tx.Vin[i].AddrDesc) == 0 {
			continue
		}
		a, ok := insightRvnDecodeAsset(tx.Vin[i].AddrDesc)
		if !ok {
			continue
		}
		if owner := x.ownerAddress(a.owner); owner != "" {
			out.Vin[i].Addr = &owner
		}
	}
}

// ownerAddress is the address of a standard output script, "" without a
// parser or for a script it does not map to one address.
func (x *insightRavencoin) ownerAddress(script []byte) string {
	if x.s.parser == nil {
		return ""
	}
	addresses, isAddress, err := x.s.parser.GetAddressesFromAddrDesc(script)
	if err != nil || !isAddress || len(addresses) != 1 {
		return ""
	}
	return addresses[0]
}
