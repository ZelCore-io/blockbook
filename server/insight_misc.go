package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/trezor/blockbook/api"
	"github.com/trezor/blockbook/bchain"
)

const (
	// insightFeeTargetsMax caps the comma list of utils/estimatefee.
	insightFeeTargetsMax = 10
	// insightFeeTargetMax is the largest block target bitcoind's estimatesmartfee accepts.
	insightFeeTargetMax = 1008
)

const (
	// insightFeeCacheFrom is where the worker's own 10 s fee cache ends (its
	// estimatedFeeCacheSize); larger targets go to the daemon on every call,
	// so the listener caches them itself for the same time.
	insightFeeCacheFrom = 300
	insightFeeCacheTTL  = 10 * time.Second
)

// insightFeeCache holds the fee value of targets the worker does not cache.
// Targets are at most insightFeeTargetMax, so it stays small.
type insightFeeCache struct {
	mu      sync.Mutex
	entries map[int]insightFeeCacheEntry
}

type insightFeeCacheEntry struct {
	value json.Number
	at    time.Time
}

func (c *insightFeeCache) get(target int) (json.Number, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[target]
	if !ok || insightNow().Sub(e.at) >= insightFeeCacheTTL {
		return "", false
	}
	return e.value, true
}

func (c *insightFeeCache) put(target int, value json.Number) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[int]insightFeeCacheEntry{}
	}
	c.entries[target] = insightFeeCacheEntry{value, insightNow()}
}

// RPC error codes verifymessage returns for input the daemon cannot use:
// RPC_TYPE_ERROR (address is not a key / malformed base64), RPC_INVALID_ADDRESS_OR_KEY
// and RPC_INVALID_PARAMETER. Stock answers such input with result false.
var insightVerifyInvalidCodes = map[int]bool{-3: true, -5: true, -8: true}

const insightRPCMethodNotFound = -32601

// insightFeeEntry is one key of the estimatefee object; insightFees keeps the
// request order, as stock's zipObject does.
type insightFeeEntry struct {
	key   string
	value json.Number
}

type insightFees []insightFeeEntry

func (f insightFees) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, e := range f {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := json.Marshal(e.key)
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		b.WriteString(string(e.value))
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// estimateFee serves utils/estimatefee?nbBlocks=2,3 as {"2":<coins/kB>,...}.
// A target the backend cannot estimate (error or non-positive fee) is -1.
func (s *InsightServer) estimateFee(route insightRoute) (interface{}, int, error) {
	arg := route.query.Get("nbBlocks")
	if arg == "" {
		arg = "2"
	}
	parts := strings.Split(arg, ",")
	if len(parts) > insightFeeTargetsMax {
		return nil, http.StatusBadRequest, errors.New("too many fee targets")
	}
	targets := make([]int, 0, len(parts))
	for _, p := range parts {
		if p == "" || len(p) > 4 || strings.Trim(p, "0123456789") != "" {
			return nil, http.StatusBadRequest, errors.New("invalid fee target")
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > insightFeeTargetMax {
			return nil, http.StatusBadRequest, errors.New("invalid fee target")
		}
		targets = append(targets, n)
	}
	// Repeated targets collapse to one key at their first position, as the
	// object keys of stock's zipObject do, and cost one backend call.
	out := make(insightFees, 0, len(targets))
	seen := make(map[int]struct{}, len(targets))
	for _, n := range targets {
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		if err := route.gone(); err != nil {
			return nil, insightClientGoneCode, err
		}
		value, ok := json.Number(""), false
		if n >= insightFeeCacheFrom {
			value, ok = s.feeCache.get(n)
		}
		if !ok {
			value = json.Number("-1")
			fee, err := s.worker.EstimateFee(n, true)
			if err == nil && fee.Sign() > 0 {
				money, _, err := insightMoney((*api.Amount)(&fee))
				if err != nil {
					return nil, http.StatusInternalServerError, err
				}
				value = money
				if n >= insightFeeCacheFrom {
					s.feeCache.put(n, value)
				}
			}
		}
		out = append(out, insightFeeEntry{key: strconv.Itoa(n), value: value})
	}
	return out, 0, nil
}

type insightCurrencyData struct {
	Rate  json.Number `json:"rate"`
	Short string      `json:"short"`
}

type insightCurrency struct {
	Status int                 `json:"status"`
	Data   insightCurrencyData `json:"data"`
}

// currency serves the USD rate from Blockbook's own fiat rates (no network call
// here). The rate is 0 when the coin has no usable USD rate.
func (s *InsightServer) currency() (interface{}, int, error) {
	rate := json.Number("0")
	ticker, err := s.worker.GetCurrentFiatRates([]string{"usd"}, "")
	if err == nil && ticker != nil {
		if v, ok := ticker.Rates["usd"]; ok && v >= 0 && !math.IsInf(float64(v), 0) && !math.IsNaN(float64(v)) {
			// float32 precision, so 0.1 stays 0.1 instead of a widened float64
			rate = json.Number(strconv.FormatFloat(float64(v), 'f', -1, 32))
		}
	}
	return insightCurrency{Status: http.StatusOK, Data: insightCurrencyData{Rate: rate, Short: s.coinShortcut}}, 0, nil
}

type insightVerifyResult struct {
	Result bool `json:"result"`
}

// verifyMessage checks a signed message with the daemon's verifymessage, so
// each coin's own message magic applies. Daemon error text is never echoed.
func (s *InsightServer) verifyMessage(route insightRoute) (interface{}, int, error) {
	fields := []string{"address", "signature", "message"}
	var body map[string]interface{}
	if len(route.body) > 0 {
		if err := json.Unmarshal(route.body, &body); err != nil || body == nil {
			return nil, http.StatusBadRequest, errors.New("invalid body")
		}
	}
	params := make([]interface{}, 0, len(fields))
	for _, name := range fields {
		// as stock: req.body.x || req.query.x
		if v, ok := body[name]; ok {
			str, isString := v.(string)
			if !isString {
				return nil, http.StatusBadRequest, errors.New("invalid parameter")
			}
			if str != "" {
				params = append(params, str)
				continue
			}
		}
		values, ok := route.query[name]
		if !ok || len(values) == 0 {
			return nil, http.StatusBadRequest, errors.New("missing parameter")
		}
		params = append(params, values[0])
	}
	if s.rpc == nil {
		return nil, http.StatusNotImplemented, errors.New("no daemon RPC")
	}
	var valid bool
	if err := s.rpc("verifymessage", params, &valid); err != nil {
		var rpcErr *bchain.RPCError
		if errors.As(err, &rpcErr) {
			if rpcErr.Code == insightRPCMethodNotFound {
				return nil, http.StatusNotImplemented, err
			}
			if insightVerifyInvalidCodes[rpcErr.Code] {
				return insightVerifyResult{Result: false}, 0, nil
			}
		}
		return nil, http.StatusInternalServerError, err
	}
	return insightVerifyResult{Result: valid}, 0, nil
}
