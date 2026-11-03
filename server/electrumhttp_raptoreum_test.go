//go:build unittest

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/trezor/blockbook/api"
	"github.com/trezor/blockbook/bchain/coins/btc"
	"github.com/trezor/blockbook/bchain/coins/raptoreum"
)

// Real Raptoreum mainnet data (raptoreumd 2.0.3.1), shared with
// bchain/coins/raptoreum/testdata: the future 1366dc99... of block 1420444
// (maturity 100, lockTime 100, lockOutputIndex 0, block time 1788268218) and
// the asset mint ccc31c7a... (type 10).
const (
	rtmFutureTxid      = "1366dc991927541013509184d3ad5b554e2789c60b8456d05357a2d50e297d3f"
	rtmFutureHeight    = 1420444
	rtmFutureBlockTime = 1788268218
	rtmMintTxid        = "ccc31c7aac752dee7481cae08454d67fb7a57061cc14fbbe707b666cb61db10c"
	rtmFutureAddr      = "RHMagXBeMGna229N3sNuLY3Uh8WumEKe1X"
)

func rtmTestTxHex(t *testing.T, short string) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	b, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "bchain", "coins", "raptoreum", "testdata", "tx_"+short+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Hex string `json:"hex"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v.Hex
}

func TestRTMParseFuture(t *testing.T) {
	future := mustHex(t, rtmTestTxHex(t, "1366dc99"))
	f, err := parseRTMFuture(future)
	if err != nil {
		t.Fatal(err)
	}
	if f != (rtmFuture{future: true, maturity: 100, lockTime: 100, index: 0}) {
		t.Errorf("future = %+v", f)
	}
	mint := mustHex(t, rtmTestTxHex(t, "ccc31c7a"))
	if f, err := parseRTMFuture(mint); err != nil || f.future {
		t.Errorf("mint (type 10) = %+v, %v", f, err)
	}
	// An ordinary version 1 transaction and a version 2 one claiming type 7.
	if f, err := parseRTMFuture([]byte{1, 0, 0, 0, 0}); err != nil || f.future {
		t.Errorf("v1 = %+v, %v", f, err)
	}
	if f, err := parseRTMFuture([]byte{2, 0, 7, 0, 0}); err != nil || f.future {
		t.Errorf("v2 type 7 = %+v, %v", f, err)
	}
	for name, b := range map[string][]byte{
		"empty":     nil,
		"truncated": future[:len(future)-1],
		"trailing":  append(append([]byte{}, future...), 0),
		"header":    future[:4],
	} {
		if _, err := parseRTMFuture(b); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	// A payload shorter than the lock fields.
	short := append(append([]byte{}, future[:len(future)-85]...), 2, 1, 0)
	if _, err := parseRTMFuture(short); err == nil {
		t.Error("short payload: no error")
	}
}

type rtmStubRPC struct {
	raw   map[string]string
	calls int
}

func (r *rtmStubRPC) call(method string, params []interface{}, result interface{}) error {
	r.calls++
	if method != "getrawtransaction" || params[1] != 0 {
		return fmt.Errorf("unexpected call %s %v", method, params)
	}
	raw, ok := r.raw[params[0].(string)]
	if !ok {
		return errors.New("not found")
	}
	*(result.(*string)) = raw
	return nil
}

func rtmBudget(calls int) *ehBudget {
	return &ehBudget{ctx: context.Background(), calls: calls}
}

func TestRTMSpendRule(t *testing.T) {
	rpc := &rtmStubRPC{raw: map[string]string{
		rtmFutureTxid: rtmTestTxHex(t, "1366dc99"),
		rtmMintTxid:   rtmTestTxHex(t, "ccc31c7a"),
	}}
	blockTimes := map[uint32]int64{rtmFutureHeight: rtmFutureBlockTime, 1500000: 1800000000}
	r := newRTMSpendRule(rpc.call, func(h uint32) (int64, error) {
		if t, ok := blockTimes[h]; ok {
			return t, nil
		}
		return 0, errors.New("no block")
	}, 16)
	// Synthetic locks on synthetic txids, to cover the disabled legs.
	r.cache.add("noMaturity", rtmFuture{future: true, maturity: -1, lockTime: 3600, index: 1})
	r.cache.add("noTime", rtmFuture{future: true, maturity: 10, lockTime: -1, index: 0})
	r.cache.add("neither", rtmFuture{future: true, maturity: -1, lockTime: -1, index: 0})
	r.cache.add("plain", rtmFuture{})

	unlocked := time.Unix(rtmFutureBlockTime+100, 0).Add(rtmTimeMargin)
	tests := []struct {
		name string
		u    api.Utxo
		now  time.Time
		want bool
	}{
		{"coinbase 99 confirmations", api.Utxo{Txid: "cb", Height: 1500000, Confirmations: 99, Coinbase: true}, unlocked, false},
		{"coinbase 100 confirmations", api.Utxo{Txid: "cb", Height: 1500000, Confirmations: 100, Coinbase: true}, unlocked, true},
		{"before the future fork", api.Utxo{Txid: "unknown", Height: rtmFutureForkHeight - 1, Confirmations: 5}, unlocked, true},
		{"not a future", api.Utxo{Txid: rtmMintTxid, Vout: 1, Height: 1101722, Confirmations: 1}, unlocked, true},
		{"pending not a future", api.Utxo{Txid: "plain", Height: 0, Confirmations: 0}, unlocked, true},
		{"future, both legs locked", api.Utxo{Txid: rtmFutureTxid, Height: rtmFutureHeight, Confirmations: 99}, unlocked.Add(-time.Second), false},
		{"future, maturity reached", api.Utxo{Txid: rtmFutureTxid, Height: rtmFutureHeight, Confirmations: 100}, unlocked.Add(-time.Hour), true},
		{"future, lockTime reached with margin", api.Utxo{Txid: rtmFutureTxid, Height: rtmFutureHeight, Confirmations: 2}, unlocked, true},
		{"future, lockTime reached inside margin", api.Utxo{Txid: rtmFutureTxid, Height: rtmFutureHeight, Confirmations: 2}, time.Unix(rtmFutureBlockTime+100, 0), false},
		{"future, another output", api.Utxo{Txid: rtmFutureTxid, Vout: 1, Height: rtmFutureHeight, Confirmations: 1}, unlocked.Add(-time.Hour), true},
		{"future, pending", api.Utxo{Txid: rtmFutureTxid, Height: 0, Confirmations: 0}, unlocked.Add(time.Hour), false},
		{"maturity disabled, time locked", api.Utxo{Txid: "noMaturity", Vout: 1, Height: 1500000, Confirmations: 1000000}, time.Unix(1800000000+3599, 0).Add(rtmTimeMargin), false},
		{"maturity disabled, time reached", api.Utxo{Txid: "noMaturity", Vout: 1, Height: 1500000, Confirmations: 1}, time.Unix(1800000000+3600, 0).Add(rtmTimeMargin), true},
		{"time disabled, 9 confirmations", api.Utxo{Txid: "noTime", Height: 1500000, Confirmations: 9}, time.Unix(1900000000, 0), false},
		{"time disabled, 10 confirmations", api.Utxo{Txid: "noTime", Height: 1500000, Confirmations: 10}, time.Unix(1900000000, 0), true},
		{"both legs disabled", api.Utxo{Txid: "neither", Height: 1500000, Confirmations: 1000000}, time.Unix(1900000000, 0), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := r.spendable(rtmBudget(10), &tt.u, tt.now)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("spendable = %v, want %v", got, tt.want)
			}
		})
	}
	// Each real transaction was read from the daemon once; coinbase and
	// pre-fork outputs needed no lookup.
	if rpc.calls != 2 {
		t.Errorf("daemon calls = %d, want 2", rpc.calls)
	}
	// A lookup takes one call from the request budget.
	r.cache = newRTMFutureCache(16)
	if _, err := r.spendable(rtmBudget(0), &api.Utxo{Txid: rtmFutureTxid, Height: rtmFutureHeight, Confirmations: 1}, unlocked); err == nil || err.Error() != "request too large" {
		t.Errorf("empty budget: %v", err)
	}
	if _, err := r.spendable(rtmBudget(1), &api.Utxo{Txid: "missing", Height: rtmFutureHeight, Confirmations: 1}, unlocked); err == nil {
		t.Error("a failed lookup was not reported")
	}
}

func TestRTMFutureCacheBounded(t *testing.T) {
	c := newRTMFutureCache(3)
	for i := 0; i < 5; i++ {
		c.add(fmt.Sprint(i), rtmFuture{index: uint16(i)})
	}
	if c.len() != 3 {
		t.Fatalf("len = %d, want 3", c.len())
	}
	for i, want := range []bool{false, false, true, true, true} {
		if _, ok := c.get(fmt.Sprint(i)); ok != want {
			t.Errorf("entry %d present = %v, want %v", i, ok, want)
		}
	}
	// get refreshes: 2 is now the most recently used, so 3 goes next.
	c.get("2")
	c.add("5", rtmFuture{})
	if _, ok := c.get("3"); ok {
		t.Error("least recently used entry kept")
	}
	if _, ok := c.get("2"); !ok {
		t.Error("refreshed entry evicted")
	}
}

// newRTMEnv is the listener on Raptoreum's parser with the Raptoreum rule.
func newRTMEnv(t *testing.T, now time.Time) *ehEnv {
	t.Helper()
	e := newEHEnv(t)
	e.parser = raptoreum.NewRaptoreumParser(raptoreum.GetChainParams("main"), &btc.Configuration{})
	s, err := NewElectrumHTTPServer("127.0.0.1:0", e.parser, e.worker, e.index, e.mempool, e.rpc.call, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnableChainRules("Raptoreum", func(h uint32) (int64, error) {
		if h == rtmFutureHeight {
			return rtmFutureBlockTime, nil
		}
		return 1700000000, nil
	}); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now }
	s.SetReady()
	e.s = s
	e.rpc.raw[rtmFutureTxid] = rtmTestTxHex(t, "1366dc99")
	return e
}

func rtmUtxo(txid string, vout int32, height, confirmations int, value int64, coinbase bool) api.Utxo {
	return api.Utxo{Txid: txid, Vout: vout, AmountSat: (*api.Amount)(big.NewInt(value)), Height: height, Confirmations: confirmations, Coinbase: coinbase}
}

func TestElectrumHTTPRaptoreumUtxo(t *testing.T) {
	const best = rtmFutureHeight + 49 // the future has 50 confirmations
	cb := func(n int) string { return fmt.Sprintf("%064x", n) }
	e := newRTMEnv(t, time.Unix(rtmFutureBlockTime+100, 0)) // lockTime reached, but not past the margin
	e.worker.addresses[rtmFutureAddr] = &api.Address{AddrStr: rtmFutureAddr, BalanceSat: (*api.Amount)(big.NewInt(108359256859490 + 3*100)), UnconfirmedBalanceSat: (*api.Amount)(big.NewInt(7))}
	// Worker order: mempool first, then confirmed newest first.
	e.worker.utxos[rtmFutureAddr] = api.Utxos{
		rtmUtxo(cb(9), 0, 0, 0, 7, false),                                      // pending, not a future
		rtmUtxo(cb(1), 0, best, 1, 100, true),                                  // immature coinbase
		rtmUtxo(rtmFutureTxid, 0, rtmFutureHeight, 50, 108359256859490, false), // locked future
		rtmUtxo(cb(2), 0, best-200, 201, 100, true),                            // mature coinbase
		rtmUtxo(cb(3), 1, 400000, best-400000+1, 100, false),                   // pre-fork
	}
	e.rpc.raw[cb(9)] = "0100000000000000000000" // v1, no inputs or outputs
	prefix := "/?server=127.0.0.1&port=10002&contype=tls&coin=raptoreum&call="
	addr := "&param=" + rtmFutureAddr

	ehExpect(t, e.get(prefix+"balance"+addr), `{"confirmed":108359256859790,"unconfirmed":7}`)
	ehExpect(t, e.get(prefix+"utxo"+addr), `[`+
		`{"tx_hash":"`+cb(3)+`","tx_pos":1,"height":400000,"value":100},`+
		`{"tx_hash":"`+cb(2)+`","tx_pos":0,"height":`+fmt.Sprint(best-200)+`,"value":100},`+
		`{"tx_hash":"`+cb(9)+`","tx_pos":0,"height":0,"value":7}]`)
	script := "76a914589589e1b1755aa9e9a6ef16e171fba7e3211f5288ac"
	ehExpect(t, e.get(prefix+"niceutxo"+addr), `[`+
		`{"txid":"`+cb(3)+`","vout":1,"scriptPubKey":"`+script+`","satoshis":100,"height":400000},`+
		`{"txid":"`+cb(2)+`","vout":0,"scriptPubKey":"`+script+`","satoshis":100,"height":`+fmt.Sprint(best-200)+`}]`)

	// Past the margin the future's lockTime leg opens.
	e.s.now = func() time.Time { return time.Unix(rtmFutureBlockTime+100, 0).Add(rtmTimeMargin) }
	if body := e.get(prefix + "niceutxo" + addr).Body.String(); !strings.Contains(body, rtmFutureTxid) {
		t.Errorf("unlocked future missing: %s", body)
	}
	// The future's raw transaction was read once and then cached.
	if n := e.rawTxCalls(); n != 2 {
		t.Errorf("raw tx calls = %d, want 2 (the future and the pending tx)", n)
	}
}

// TestElectrumHTTPRaptoreumUtxoLimit: left-out outputs do not count toward the
// first 600.
func TestElectrumHTTPRaptoreumUtxoLimit(t *testing.T) {
	e := newRTMEnv(t, time.Unix(1793530000, 0))
	var u api.Utxos
	for i := ehUtxoLimit + 10; i > 0; i-- {
		u = append(u, rtmUtxo(fmt.Sprintf("%064x", i), 0, i, 1000000, 1, i <= 10 && i%2 == 0))
	}
	// The ten oldest include five coinbase outputs with too few confirmations.
	for i := range u {
		if u[i].Coinbase {
			u[i].Confirmations = 5
		}
	}
	e.worker.utxos[rtmFutureAddr] = u
	var got []struct {
		TxHash string `json:"tx_hash"`
	}
	w := e.get("/?server=x&call=utxo&param=" + rtmFutureAddr)
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(w.Body.String())
	}
	if len(got) != ehUtxoLimit {
		t.Fatalf("utxo = %d entries, want %d", len(got), ehUtxoLimit)
	}
	if got[0].TxHash != fmt.Sprintf("%064x", 1) || got[1].TxHash != fmt.Sprintf("%064x", 3) {
		t.Errorf("first entries = %v", got[:2])
	}
	if n := e.rawTxCalls(); n != 0 {
		t.Errorf("raw tx calls = %d for pre-fork outputs", n)
	}
}

func TestElectrumHTTPEnableChainRules(t *testing.T) {
	e := newEHEnv(t)
	if err := e.s.EnableChainRules("Bitcore", nil); err != nil || e.s.spendRule != nil {
		t.Errorf("Bitcore: rule %v, err %v", e.s.spendRule, err)
	}
	s, err := NewElectrumHTTPServer("127.0.0.1:0", e.parser, e.worker, e.index, e.mempool, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnableChainRules("Raptoreum", func(uint32) (int64, error) { return 0, nil }); err == nil {
		t.Error("Raptoreum rule installed without the backend RPC")
	}
	if err := e.s.EnableChainRules("Raptoreum", nil); err == nil {
		t.Error("Raptoreum rule installed without block times")
	}
	if err := e.s.Start(); err != nil {
		t.Fatal(err)
	}
	defer e.s.Shutdown(context.Background())
	if err := e.s.EnableChainRules("Raptoreum", func(uint32) (int64, error) { return 0, nil }); err == nil {
		t.Error("chain rules changed after Start")
	}
}
