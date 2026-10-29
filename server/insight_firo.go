package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trezor/blockbook/api"
	"github.com/trezor/blockbook/bchain"
)

// The Firo extension serves the Firo-specific routes of the stock Firo Insight
// (insight-api-zcoin 0.4.5) and its transaction labels. Each route makes the
// firod call the stock controller makes (via bitcore-node-zcoin) and keeps its
// response shape; results are cached briefly so traffic does not reach firod
// per request.

const (
	insightFiroMasternodeTTL = 30 * time.Second
	insightFiroSupplyTTL     = 60 * time.Second
	insightFiroNameTTL       = 10 * time.Minute
	insightFiroNameCacheSize = 4096 // across both generations
	insightFiroPrefix        = "firo/"
	// insightFiroFilterMax and insightFiroFilterMaxItems cap a listmasternodesarray
	// filter (bytes, distinct comma-separated items); larger filters get 400.
	insightFiroFilterMax      = 4096
	insightFiroFilterMaxItems = 100
)

// insightFiroRoutes are the stock GET routes without a path argument.
var insightFiroRoutes = map[string]bool{
	"masternode/listmasternodes": true,
	"masternode/masternodecount": true,
	"ext/getmoneysupply":         true,
	"zerocoin/gettotalzeromint":  true,
	"zerocoin/gettotalzerospend": true,
	"zerocoin/getrealsupply":     true,
	"zerocoin/getzerominttxs":    true,
	"zerocoin/getzerospendtxs":   true,
	"sigma/gettotalsigmamint":    true,
	"sigma/gettotalsigmaspend":   true,
	"sigma/getrealsupply":        true,
	"sparknames":                 true,
}

// insightFiroPseudo are the pseudo-addresses firod's address index files
// private mints and spends under.
var insightFiroPseudo = []string{"Zeromint", "Zerospend", "Sigmamint", "Sigmaspend"}

// insightFiroVinLabels and insightFiroVoutLabels are the stock labels, keyed by
// the script's first opcode (stock matches the asm prefix of that opcode).
var insightFiroVinLabels = map[string]string{
	"c2": "Zerospend",      // OP_ZEROCOINSPEND
	"c4": "Sigmaspend",     // OP_SIGMASPEND
	"c7": "Lelantusjsplit", // OP_LELANTUSJOINSPLIT
	"c9": "Lelantusjsplit", // OP_LELANTUSJOINSPLITPAYLOAD
	"d3": "Sparkspend",     // OP_SPARKSPEND
	"c8": "Remint",         // OP_ZEROCOINTOSIGMAREMINT
}

var insightFiroVoutLabels = map[string][2]string{
	"c1": {"zerocoinmint", "Zeromint"},     // OP_ZEROCOINMINT
	"c3": {"sigmamint", "Sigmamint"},       // OP_SIGMAMINT
	"c5": {"lelantusmint", "Lelantusmint"}, // OP_LELANTUSMINT
	"c6": {"lelantusjmint", "Lelantusjmint"},
	"d1": {"sparkmint", "Sparkmint"},
	"d2": {"sparksmint", "Sparksmint"},
}

type insightFiro struct {
	s          *InsightServer
	znodes     insightCached
	count      insightCached
	supply     insightCached
	sparknames insightCached
	balances   map[string]*insightCached
	txids      map[string]*insightCached
	rpcID      atomic.Int64
	namesMu    sync.Mutex
	names      map[string]insightFiroName // current generation
	oldNames   map[string]insightFiroName // previous generation
}

type insightFiroName struct {
	data *insightSparkData
	at   time.Time
}

func newInsightFiro(s *InsightServer) insightExtension {
	f := &insightFiro{s: s, balances: map[string]*insightCached{}, txids: map[string]*insightCached{}, names: map[string]insightFiroName{}}
	for _, a := range insightFiroPseudo {
		f.balances[a] = &insightCached{}
		f.txids[a] = &insightCached{}
	}
	return f
}

func (f *insightFiro) match(parts []string) (string, string, []string, bool) {
	path := strings.Join(parts, "/")
	switch {
	case path == "masternode/listmasternodesarray":
		return insightFiroPrefix + path, "", insightGetPost, true
	case len(parts) == 3 && parts[0] == "masternode" && parts[1] == "listmasternodesarray":
		return insightFiroPrefix + "masternode/listmasternodesarray", parts[2], insightGet, true
	case insightFiroRoutes[path]:
		return insightFiroPrefix + path, "", insightGet, true
	}
	return "", "", nil, false
}

func (f *insightFiro) handles(kind string) bool {
	return kind == "currency" || strings.HasPrefix(kind, insightFiroPrefix)
}

func (f *insightFiro) handle(route insightRoute) (interface{}, int, error) {
	if route.kind == "currency" {
		return f.currency(), 0, nil
	}
	if f.s.rpc == nil {
		return nil, http.StatusNotImplemented, errors.New("no daemon RPC")
	}
	ctx := route.ctx
	var value interface{}
	var err error
	switch strings.TrimPrefix(route.kind, insightFiroPrefix) {
	case "masternode/listmasternodes":
		value, err = f.listMasternodes(ctx)
	case "masternode/listmasternodesarray":
		filter, ok := insightFiroFilter(route)
		if !ok {
			return nil, http.StatusBadRequest, errors.New("invalid filter")
		}
		items, ferr := insightFiroFilterItems(filter)
		if ferr != nil {
			return nil, http.StatusBadRequest, ferr
		}
		value, err = f.listMasternodesArray(ctx, items)
	case "masternode/masternodecount":
		value, err = f.count.get(ctx, insightFiroMasternodeTTL, func() (interface{}, error) {
			var count struct {
				Total   json.Number `json:"total"`
				Enabled json.Number `json:"enabled"`
			}
			err := f.s.rpc("evoznode", []interface{}{"count"}, &count)
			return count, err
		})
	case "ext/getmoneysupply":
		value, err = f.totals(ctx, func(get func(string) float64) float64 { return get("supply") })
	case "zerocoin/gettotalzeromint":
		value, err = f.totals(ctx, func(get func(string) float64) float64 { return get("Zeromint") / 1e+8 })
	case "zerocoin/gettotalzerospend":
		value, err = f.totals(ctx, func(get func(string) float64) float64 { return get("Zerospend") / -1e+8 })
	case "sigma/gettotalsigmamint":
		value, err = f.totals(ctx, func(get func(string) float64) float64 { return get("Sigmamint") / 1e+8 })
	case "sigma/gettotalsigmaspend":
		value, err = f.totals(ctx, func(get func(string) float64) float64 { return get("Sigmaspend") / -1e+8 })
	case "zerocoin/getrealsupply":
		// stock: results[2] - results[0] + results[1], in that float order
		value, err = f.totals(ctx, func(get func(string) float64) float64 {
			return get("supply") - get("Zeromint")/1e+8 + get("Zerospend")/1e+8
		})
	case "sigma/getrealsupply":
		value, err = f.totals(ctx, func(get func(string) float64) float64 {
			return get("supply") - get("Sigmamint")/1e+8 + get("Sigmaspend")/-1e+8
		})
	case "zerocoin/getzerominttxs":
		value, err = f.pseudoTxs(ctx, "Zeromint")
	case "zerocoin/getzerospendtxs":
		value, err = f.pseudoTxs(ctx, "Zerospend")
	case "sparknames":
		value, err = f.sparknames.get(ctx, insightFiroSupplyTTL, func() (interface{}, error) {
			var raw json.RawMessage
			if err := f.s.rpc("getsparknames", nil, &raw); err != nil {
				return nil, err
			}
			// stock: Array.isArray(result) ? result : []
			if t := bytes.TrimSpace(raw); len(t) == 0 || t[0] != '[' {
				raw = json.RawMessage("[]")
			}
			return raw, nil
		})
	default:
		return nil, http.StatusNotImplemented, errors.New("route not implemented")
	}
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	return value, 0, nil
}

// insightFiroCurrency is the stock currency shape. Stock only ever fills
// zcoin_usd (and bitstamp, its backward-compatible copy); btc_usd and
// btc_zcoin stay 0 there, and here.
type insightFiroCurrency struct {
	Status int `json:"status"`
	Data   struct {
		ZcoinUSD json.Number `json:"zcoin_usd"`
		BtcUSD   json.Number `json:"btc_usd"`
		BtcZcoin json.Number `json:"btc_zcoin"`
		Bitstamp json.Number `json:"bitstamp"`
	} `json:"data"`
}

func (f *insightFiro) currency() insightFiroCurrency {
	var c insightFiroCurrency
	c.Status = http.StatusOK
	rate := f.s.usdRate()
	c.Data.ZcoinUSD, c.Data.BtcUSD, c.Data.BtcZcoin, c.Data.Bitstamp = rate, "0", "0", rate
	return c
}

// insightFiroNumber formats a float as JavaScript's JSON.stringify does for
// the values these routes produce (no exponent, -0 as 0).
func insightFiroNumber(v float64) json.Number {
	if v == 0 {
		v = 0
	}
	return json.Number(strconv.FormatFloat(v, 'f', -1, 64))
}

type insightFiroTotal struct {
	Total json.Number `json:"total"`
}

// totals evaluates compute over get, which returns firod's gettotalsupply in
// coins ("supply") or a pseudo-address's getaddressbalance in satoshis, both
// as float64 like stock, so the arithmetic rounds as stock's does.
func (f *insightFiro) totals(ctx context.Context, compute func(get func(string) float64) float64) (interface{}, error) {
	var firstErr error
	get := func(name string) float64 {
		var v float64
		var err error
		if name == "supply" {
			v, err = f.supplyValue(ctx)
		} else {
			v, err = f.balance(ctx, name)
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
		return v
	}
	v := compute(get)
	if firstErr != nil {
		return nil, firstErr
	}
	return insightFiroTotal{insightFiroNumber(v)}, nil
}

func (f *insightFiro) supplyValue(ctx context.Context) (float64, error) {
	v, err := f.supply.get(ctx, insightFiroSupplyTTL, func() (interface{}, error) {
		var res struct {
			Total float64 `json:"total"`
		}
		if err := f.s.rpc("gettotalsupply", nil, &res); err != nil {
			return nil, err
		}
		return res.Total / 1e+8, nil
	})
	if err != nil {
		return 0, err
	}
	return v.(float64), nil
}

func (f *insightFiro) balance(ctx context.Context, address string) (float64, error) {
	v, err := f.balances[address].get(ctx, insightFiroSupplyTTL, func() (interface{}, error) {
		var res struct {
			Balance float64 `json:"balance"`
		}
		if err := f.s.rpc("getaddressbalance", []interface{}{map[string][]string{"addresses": {address}}}, &res); err != nil {
			return nil, err
		}
		return res.Balance, nil
	})
	if err != nil {
		return 0, err
	}
	return v.(float64), nil
}

// insightFiroPseudoTxs is the stock getzerominttxs/getzerospendtxs shape. Stock
// also sets address from an undefined field, so JSON never carries it.
type insightFiroPseudoTxs struct {
	Balance json.Number     `json:"balance"`
	Txs     json.RawMessage `json:"txs"`
}

func (f *insightFiro) pseudoTxs(ctx context.Context, address string) (interface{}, error) {
	balance, err := f.balance(ctx, address)
	if err != nil {
		return nil, err
	}
	txs, err := f.txids[address].get(ctx, insightFiroSupplyTTL, func() (interface{}, error) {
		var raw json.RawMessage
		if err := f.s.rpc("getaddresstxids", []interface{}{map[string][]string{"addresses": {address}}}, &raw); err != nil {
			return nil, err
		}
		return raw, nil
	})
	if err != nil {
		return nil, err
	}
	return insightFiroPseudoTxs{insightFiroNumber(balance / 1e+8), txs.(json.RawMessage)}, nil
}

// insightFiroZnode is one entry of stock listmasternodesarray, in stock field
// order. Daemon values are kept verbatim; a field firod omits is omitted, as
// JSON.stringify drops undefined.
type insightFiroZnode struct {
	Collateral        string          `json:"collateral"`
	Txhash            string          `json:"txhash"`
	Outidx            string          `json:"outidx"`
	IP                string          `json:"ip"`
	ProTxHash         json.RawMessage `json:"proTxHash,omitempty"`
	Address           json.RawMessage `json:"address,omitempty"`
	Payee             json.RawMessage `json:"payee,omitempty"`
	Status            json.RawMessage `json:"status,omitempty"`
	Lastpaidtime      json.RawMessage `json:"lastpaidtime,omitempty"`
	Lastpaidblock     json.RawMessage `json:"lastpaidblock,omitempty"`
	Owneraddress      json.RawMessage `json:"owneraddress,omitempty"`
	Votingaddress     json.RawMessage `json:"votingaddress,omitempty"`
	Collateraladdress json.RawMessage `json:"collateraladdress,omitempty"`
	Pubkeyoperator    json.RawMessage `json:"pubkeyoperator,omitempty"`
}

// insightFiroZnodes is one evoznodelist snapshot: the raw daemon object for
// listmasternodes and the converted entries, each with its JSON text (what
// stock's substring filter searches).
type insightFiroZnodes struct {
	raw    json.RawMessage
	id     int64
	nodes  []json.RawMessage
	text   []string // nodes as strings, searched by the substring filter
	txhash []string
	outidx []string
}

// insightFiroRPCResult is stock's {result, error, id} envelope. id is this
// listener's own counter, not firod's JSON-RPC id.
type insightFiroRPCResult struct {
	Result interface{}      `json:"result"`
	Error  *json.RawMessage `json:"error"`
	ID     int64            `json:"id"`
}

func (f *insightFiro) loadZnodes(ctx context.Context) (*insightFiroZnodes, error) {
	v, err := f.znodes.get(ctx, insightFiroMasternodeTTL, func() (interface{}, error) {
		var raw json.RawMessage
		if err := f.s.rpc("evoznodelist", nil, &raw); err != nil {
			return nil, err
		}
		return insightFiroParseZnodes(raw, f.rpcID.Add(1))
	})
	if err != nil {
		return nil, err
	}
	return v.(*insightFiroZnodes), nil
}

// insightFiroParseZnodes converts evoznodelist's object in daemon key order,
// as stock's Object.keys does.
func insightFiroParseZnodes(raw json.RawMessage, id int64) (*insightFiroZnodes, error) {
	z := &insightFiroZnodes{raw: raw, id: id}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, errors.New("evoznodelist result is not an object")
	}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := t.(string)
		var node insightFiroZnode
		if err := dec.Decode(&node); err != nil {
			return nil, err
		}
		// stock: key.split(",")[0].substr(10), key.split(", ")[1].slice(0, -1)
		head, _, _ := strings.Cut(key, ",")
		_, tail, found := strings.Cut(key, ", ")
		if !found {
			return nil, errors.New("unexpected evoznodelist key")
		}
		if i := strings.Index(tail, ", "); i >= 0 {
			tail = tail[:i]
		}
		node.Collateral = key
		if len(head) > 10 {
			node.Txhash = head[10:]
		}
		if tail != "" {
			node.Outidx = tail[:len(tail)-1]
		}
		var address string
		if err := json.Unmarshal(node.Address, &address); err != nil {
			return nil, errors.New("unexpected evoznodelist address")
		}
		// stock: address.split(":").slice(0, -1).join('')
		if seg := strings.Split(address, ":"); len(seg) > 1 {
			node.IP = strings.Join(seg[:len(seg)-1], "")
		}
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(&node); err != nil {
			return nil, err
		}
		text := strings.TrimSuffix(buf.String(), "\n")
		z.nodes = append(z.nodes, json.RawMessage(text))
		z.text = append(z.text, text)
		z.txhash = append(z.txhash, node.Txhash)
		z.outidx = append(z.outidx, node.Outidx)
	}
	return z, nil
}

func (f *insightFiro) listMasternodes(ctx context.Context) (interface{}, error) {
	z, err := f.loadZnodes(ctx)
	if err != nil {
		return nil, err
	}
	return insightFiroRPCResult{Result: z.raw, ID: z.id}, nil
}

// insightFiroFilterItems splits a filter as stock does (comma-separated when
// it has a comma), dropping repeated items, which match nothing new. A filter
// over insightFiroFilterMax bytes or insightFiroFilterMaxItems distinct items is
// refused, so one request cannot scan the node list without bound.
func insightFiroFilterItems(filter string) ([]string, error) {
	if filter == "" {
		return nil, nil
	}
	if len(filter) > insightFiroFilterMax {
		return nil, fmt.Errorf("filter longer than %d bytes", insightFiroFilterMax)
	}
	parts := []string{filter}
	if strings.Contains(filter, ",") {
		parts = strings.Split(filter, ",")
	}
	seen := make(map[string]bool, len(parts))
	items := make([]string, 0, len(parts))
	for _, p := range parts {
		if !seen[p] {
			seen[p] = true
			items = append(items, p)
		}
	}
	if len(items) > insightFiroFilterMaxItems {
		return nil, fmt.Errorf("filter has more than %d items", insightFiroFilterMaxItems)
	}
	return items, nil
}

// listMasternodesArray applies stock's filter: no items returns all; each
// item with "-" is txhash-outidx, exact; anything else is a substring of the
// entry's JSON. Matches keep list order per item, duplicates dropped. It stops
// when the request context ends.
func (f *insightFiro) listMasternodesArray(ctx context.Context, items []string) (interface{}, error) {
	z, err := f.loadZnodes(ctx)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return insightFiroRPCResult{Result: z.nodes, ID: z.id}, nil
	}
	seen := make(map[int]bool)
	result := []json.RawMessage{}
	for _, item := range items {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		exact := strings.Contains(item, "-")
		var txhash, outidx string
		if exact {
			p := strings.Split(item, "-")
			txhash, outidx = p[0], p[1]
		}
		for i, text := range z.text {
			var ok bool
			if exact {
				ok = z.txhash[i] == txhash && z.outidx[i] == outidx
			} else {
				ok = strings.Contains(text, item)
			}
			if ok && !seen[i] {
				seen[i] = true
				result = append(result, z.nodes[i])
			}
		}
	}
	return insightFiroRPCResult{Result: result, ID: z.id}, nil
}

// insightFiroFilter returns stock's req.body.filter || req.params.filter ||
// req.query.filter. A body that is not a JSON (or form) object is ignored, as
// stock's body parser leaves req.body empty for it; a non-string filter is
// refused.
func insightFiroFilter(route insightRoute) (string, bool) {
	var body map[string]interface{}
	if len(route.body) > 0 && json.Unmarshal(route.body, &body) == nil {
		if v, ok := body["filter"]; ok && v != nil {
			str, isString := v.(string)
			if !isString {
				return "", false
			}
			if str != "" {
				return str, true
			}
		}
	}
	if route.arg != "" {
		return route.arg, true
	}
	return route.query.Get("filter"), true
}

// insightSparkData is stock's sparkData for a Spark name registration.
type insightSparkData struct {
	Name           string          `json:"name"`
	Address        string          `json:"address"`
	ValidUntil     json.RawMessage `json:"validUntil,omitempty"`
	AdditionalInfo json.RawMessage `json:"additionalInfo,omitempty"`
}

// sparkName returns firod's getsparknametxdetails for txid, nil when the tx
// registers no Spark name or the call fails (as stock). Only daemon answers are
// cached: a name, or a JSON-RPC error saying the tx registers none (except
// warm-up). A transport failure or timeout is not cached, so the next request
// asks again. The cache keeps two generations of insightFiroNameCacheSize/2
// entries and drops the older one when the current fills.
func (f *insightFiro) sparkName(txid string) *insightSparkData {
	if f.s.rpc == nil {
		return nil
	}
	f.namesMu.Lock()
	e, ok := f.names[txid]
	if !ok {
		e, ok = f.oldNames[txid]
	}
	f.namesMu.Unlock()
	if ok && insightNow().Sub(e.at) < insightFiroNameTTL {
		return e.data
	}
	var data *insightSparkData
	var res insightSparkData
	err := f.s.rpc("getsparknametxdetails", []interface{}{txid}, &res)
	if err == nil && res.Name != "" && res.Address != "" {
		data = &res
	}
	var rpcErr *bchain.RPCError
	if err != nil && (!errors.As(err, &rpcErr) || rpcErr.Code == insightFiroRPCInWarmup) {
		return nil
	}
	f.namesMu.Lock()
	if len(f.names) >= insightFiroNameCacheSize/2 {
		f.oldNames, f.names = f.names, make(map[string]insightFiroName, insightFiroNameCacheSize/2)
	}
	f.names[txid] = insightFiroName{data, insightNow()}
	f.namesMu.Unlock()
	return data
}

// insightFiroRPCInWarmup is bitcoind's RPC_IN_WARMUP, an error that says
// nothing about the tx.
const insightFiroRPCInWarmup = -28

func insightFiroOpcode(script string) string {
	if len(script) < 2 {
		return ""
	}
	return strings.ToLower(script[:2])
}

// decorateTx applies stock's labels: private-spend inputs get an addr label
// (a Remint input the tx's output value), private-mint outputs a type and
// address label. A Spark spend that registers a Spark name gets sparkData and
// a synthetic first "sparkname" output, and outputs are renumbered, as stock.
func (f *insightFiro) decorateTx(out *insightTx, tx *api.Tx) {
	if out == nil {
		return
	}
	sparkSpend := false
	for i := range out.Vin {
		vin := &out.Vin[i]
		if vin.Coinbase != "" {
			continue
		}
		label, ok := insightFiroVinLabels[insightFiroOpcode(vin.Hex)]
		if !ok {
			continue
		}
		vin.Addr = &label
		if label == "Sparkspend" {
			sparkSpend = true
		}
		if label == "Remint" {
			value, sat, err := insightMoney(tx.ValueOutSat)
			if err == nil {
				vin.Value, vin.ValueSat = value, sat
				vin.ScriptPubKey.Value, vin.ScriptPubKey.ValueSat = value, sat
			}
		}
	}
	for i := range out.Vout {
		vout := &out.Vout[i]
		label, ok := insightFiroVoutLabels[insightFiroOpcode(vout.Hex)]
		if !ok {
			continue
		}
		vout.Type, vout.ScriptPubKey.Type = label[0], label[0]
		vout.Addresses, vout.ScriptPubKey.Addresses = []string{label[1]}, []string{label[1]}
	}
	if !sparkSpend {
		return
	}
	data := f.sparkName(out.Txid)
	if data == nil {
		return
	}
	out.SparkData = data
	name := insightVout{
		Value: "0", Addresses: []string{"Sparkname"}, Type: "sparkname",
		ScriptPubKey: insightVoutScript{Value: "0", Addresses: []string{"Sparkname"}, Type: "sparkname", emptyScript: true},
	}
	out.Vout = append([]insightVout{name}, out.Vout...)
	for i := range out.Vout {
		out.Vout[i].N, out.Vout[i].ScriptPubKey.N = i, i
	}
}
