package server

import (
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/trezor/blockbook/api"
	"github.com/trezor/blockbook/bchain"
)

// insightSupplyTTL is how long the circulating supply is cached. The value
// moves by one block subsidy per block, and the fallback source
// (gettxoutsetinfo) scans the daemon's whole UTXO set.
const insightSupplyTTL = 10 * time.Minute

// insightSupplyErrTTL keeps a failed supply load for longer than the default
// error TTL: a gettxoutsetinfo that timed out keeps scanning in the daemon, so
// retrying it every few seconds would stack scans.
const insightSupplyErrTTL = 5 * time.Minute

// insightText is a handler result written as the bare body, the way stock's
// res.send(string) answers (Express: text/html; charset=utf-8).
type insightText string

type insightSupplyObject struct {
	CirculatingSupply string `json:"circulatingSupply"`
}

// insightValuePool is one entry of getblockchaininfo's valuePools (zcashd,
// zclassicd, fluxd, Zebra), or its chainSupply.
type insightValuePool struct {
	ID            string      `json:"id"`
	Monitored     bool        `json:"monitored"`
	ChainValueZat json.Number `json:"chainValueZat"`
}

// SetSupplyUTXOScan allows statistics/circulating-supply to fall back to
// gettxoutsetinfo (blockchaincfg "insight_supply_utxo_scan"). Call it before
// Start. Off by default.
func (s *InsightServer) SetSupplyUTXOScan(on bool) {
	s.supplyUTXOScan = on
}

// circulatingSupply serves statistics/circulating-supply: the amount of coins
// that exist on chain, in coins, as a bare number string, or as
// {"circulatingSupply":"<n>"} with ?format=object. Stock insight-api computes
// it from a per-coin emission formula; here it is read from the daemon (see
// insightLoadSupply). Without daemon RPC, or without a cheap source when the
// UTXO scan is not enabled, it is the unknown-route 404, as before the route
// existed; it answers 501 when the daemon reports a pool it does not track or
// has no gettxoutsetinfo.
func (s *InsightServer) circulatingSupply(route insightRoute) (interface{}, int, error) {
	notFound := &insightClientError{http.StatusNotFound, "Route not found"}
	if s.rpc == nil {
		return nil, http.StatusNotFound, notFound
	}
	v, err := s.supply.get(route.ctx, insightSupplyTTL, func() (interface{}, error) {
		return insightLoadSupply(s.rpc, s.supplyUTXOScan)
	})
	if err != nil {
		if errors.Is(err, errInsightSupplyNoSource) {
			return nil, http.StatusNotFound, notFound
		}
		var rpcErr *bchain.RPCError
		if errors.As(err, &rpcErr) && rpcErr.Code == insightRPCMethodNotFound {
			return nil, http.StatusNotImplemented, err
		}
		if errors.Is(err, errInsightSupplyUnknown) {
			return nil, http.StatusNotImplemented, err
		}
		if route.gone() != nil {
			return nil, insightClientGoneCode, err
		}
		return nil, http.StatusInternalServerError, err
	}
	supply, ok := v.(string)
	if !ok {
		return nil, http.StatusInternalServerError, errors.New("bad cached supply")
	}
	if route.query.Get("format") == "object" {
		return insightSupplyObject{supply}, 0, nil
	}
	return insightText(supply), 0, nil
}

// errInsightSupplyNoSource means the daemon has no cheap supply source and the
// UTXO scan is not enabled.
var errInsightSupplyNoSource = errors.New("no supply source")

// errInsightSupplyUnknown means the daemon reports a shielded pool whose value
// it does not track, so the total cannot be stated.
var errInsightSupplyUnknown = errors.New("value pool not monitored")

// insightLoadSupply returns the chain's coin supply in coins (trailing zeros
// trimmed), from the cheapest source the daemon offers:
//
//  1. getblockchaininfo chainSupply (zcashd 5+, Zebra), when monitored;
//  2. getblockchaininfo valuePools, when they include the transparent pool
//     and every pool is monitored;
//  3. only with utxoScan: gettxoutsetinfo total_amount (the transparent UTXO
//     set, a full scan) plus every shielded value pool from getblockchaininfo
//     (zcashd before 5, zclassicd, fluxd). Coins without valuePools (Bitcoin,
//     Firo, ...) get total_amount alone. Without utxoScan the result is
//     errInsightSupplyNoSource.
//
// A pool that is reported but not monitored makes the total unknown.
func insightLoadSupply(rpc InsightRPC, utxoScan bool) (interface{}, error) {
	var info struct {
		ChainSupply *insightValuePool  `json:"chainSupply"`
		ValuePools  []insightValuePool `json:"valuePools"`
	}
	if err := rpc("getblockchaininfo", nil, &info); err != nil {
		return nil, err
	}
	if info.ChainSupply != nil && info.ChainSupply.Monitored {
		total, err := insightZat(info.ChainSupply.ChainValueZat)
		if err != nil {
			return nil, err
		}
		return insightSupplyString(total)
	}
	total := new(big.Int)
	transparent := false
	for _, p := range info.ValuePools {
		if !p.Monitored {
			return nil, errInsightSupplyUnknown
		}
		v, err := insightZat(p.ChainValueZat)
		if err != nil {
			return nil, err
		}
		total.Add(total, v)
		if p.ID == "transparent" {
			transparent = true
		}
	}
	if !transparent {
		if !utxoScan {
			return nil, errInsightSupplyNoSource
		}
		var utxo struct {
			TotalAmount json.Number `json:"total_amount"`
		}
		if err := rpc("gettxoutsetinfo", nil, &utxo); err != nil {
			return nil, err
		}
		v, err := insightCoinsToSat(string(utxo.TotalAmount))
		if err != nil {
			return nil, err
		}
		total.Add(total, v)
	}
	return insightSupplyString(total)
}

func insightSupplyString(sat *big.Int) (interface{}, error) {
	if sat.Sign() < 0 {
		return nil, errors.New("negative supply")
	}
	value, _, err := insightMoney((*api.Amount)(sat))
	if err != nil {
		return nil, err
	}
	return string(value), nil
}

// insightZat parses an integer satoshi (zatoshi) amount.
func insightZat(n json.Number) (*big.Int, error) {
	v, ok := new(big.Int).SetString(string(n), 10)
	if !ok {
		return nil, errors.New("invalid pool value")
	}
	return v, nil
}

// insightCoinsToSat parses a non-negative decimal coin amount with at most
// eight decimals, exactly (no floating point).
func insightCoinsToSat(s string) (*big.Int, error) {
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" || strings.Trim(whole, "0123456789") != "" || strings.Trim(frac, "0123456789") != "" {
		return nil, errors.New("invalid coin amount")
	}
	frac = strings.TrimRight(frac, "0")
	if len(frac) > 8 {
		return nil, errors.New("coin amount below one satoshi")
	}
	frac += strings.Repeat("0", 8-len(frac))
	v, ok := new(big.Int).SetString(whole+frac, 10)
	if !ok {
		return nil, errors.New("invalid coin amount")
	}
	return v, nil
}
