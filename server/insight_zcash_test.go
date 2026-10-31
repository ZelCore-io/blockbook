package server

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/trezor/blockbook/api"
	"github.com/trezor/blockbook/bchain"
)

// Daemon verbose JSON (getrawtransaction <txid> 1) as zcashd and its forks
// print it, cut to the keys the extension reads plus a few others.
const (
	// t->z: 1.0001 transparent in, nothing transparent out, 1 ZEC shielded.
	insightZcashSaplingTZ = `{"txid":"tz","overwintered":true,"version":4,"versiongroupid":"892f2085","locktime":0,"expiryheight":1000,
		"vin":[{"txid":"prev","vout":0}],"vout":[],"vjoinsplit":[],"valueBalance":-1.00000000,"valueBalanceZat":-100000000,
		"vShieldedSpend":[],"vShieldedOutput":[{"cv":"aa","cmu":"bb","ephemeralKey":"cc","encCiphertext":"dd","outCiphertext":"ee","proof":"ff"}],
		"bindingSig":"0102"}`
	// z->t: 0.9999 transparent out from 1 ZEC shielded; valueBalanceZat
	// absent, so the coin value is read.
	insightZcashSaplingZT = `{"txid":"zt","overwintered":true,"version":4,"versiongroupid":"892f2085","locktime":0,"expiryheight":2000,
		"vin":[],"vout":[{"value":0.9999,"valueZat":99990000,"n":0}],"vjoinsplit":[],"valueBalance":1.00000000,
		"vShieldedSpend":[{"cv":"11","anchor":"22","nullifier":"33","rk":"44","proof":"55","spendAuthSig":"66"}],"vShieldedOutput":[],
		"bindingSig":"0304"}`
	// Sprout: 0.5001 transparent in, 0.5 shielded by one JoinSplit, a second
	// JoinSplit moving nothing in or out (vpub_oldZat absent there).
	insightZcashSprout = `{"txid":"js","version":2,"locktime":0,"vin":[{"txid":"prev","vout":1}],"vout":[],
		"vjoinsplit":[{"vpub_old":0.50000000,"vpub_oldZat":50000000,"vpub_new":0.00000000,"vpub_newZat":0},{"vpub_old":0.00000000,"vpub_new":0.00000000,"vpub_newZat":0}]}`
	insightZcashV1 = `{"txid":"v1","overwintered":false,"version":1,"locktime":0,"vin":[],"vout":[]}`
	// Pending t->z; the worker hands its JSON over as coinSpecificData.
	insightZcashPending = `{"txid":"pend","overwintered":true,"version":4,"versiongroupid":"892f2085","locktime":0,"expiryheight":3000,
		"vin":[],"vout":[],"vjoinsplit":[],"valueBalance":-0.50000000,"valueBalanceZat":-50000000,"vShieldedSpend":[],"vShieldedOutput":[]}`
)

// The txids are 64 hex characters, as the tx route requires.
const (
	tzID   = "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1"
	ztID   = "b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2"
	jsID   = "c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3"
	v1ID   = "d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4"
	cbID   = "e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5"
	pendID = "f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6"
)

type insightZcashRPC struct {
	mu    sync.Mutex
	txs   map[string]string
	calls map[string]int
	fail  bool
}

func (r *insightZcashRPC) call(method string, params []interface{}, result interface{}) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if method != "getrawtransaction" || len(params) != 2 || params[1] != 1 {
		return &bchain.RPCError{Code: -32601, Message: "Method not found"}
	}
	txid := params[0].(string)
	r.calls[txid]++
	raw, ok := r.txs[txid]
	if r.fail || !ok {
		return &bchain.RPCError{Code: -5, Message: "secret daemon text"}
	}
	return json.Unmarshal([]byte(raw), result)
}

func insightZcashSetup(t *testing.T, ext bool) (*InsightServer, *insightFake, *insightZcashRPC) {
	t.Helper()
	f := insightFixture()
	f.txs = map[string]*api.Tx{
		tzID: {Txid: tzID, Version: 4, Confirmations: 2, ValueInSat: insightAmount(100010000), ValueOutSat: insightAmount(0), FeesSat: insightAmount(100010000),
			Vin: []api.Vin{{Txid: "prev", Addresses: []string{"A"}, IsAddress: true, ValueSat: insightAmount(100010000)}}},
		ztID: {Txid: ztID, Version: 4, Confirmations: 2, ValueInSat: insightAmount(0), ValueOutSat: insightAmount(99990000), FeesSat: insightAmount(0),
			Vout: []api.Vout{{ValueSat: insightAmount(99990000), Addresses: []string{"A"}, IsAddress: true}}},
		jsID: {Txid: jsID, Version: 2, Confirmations: 2, ValueInSat: insightAmount(50010000), ValueOutSat: insightAmount(0), FeesSat: insightAmount(50010000),
			Vin: []api.Vin{{Txid: "prev", Vout: 1, Addresses: []string{"A"}, IsAddress: true, ValueSat: insightAmount(50010000)}}},
		v1ID: {Txid: v1ID, Version: 1, Confirmations: 2, ValueInSat: insightAmount(20000), ValueOutSat: insightAmount(10000), FeesSat: insightAmount(10000)},
		cbID: {Txid: cbID, Version: 4, Confirmations: 2, ValueOutSat: insightAmount(625000000), FeesSat: insightAmount(0), Vin: []api.Vin{{Coinbase: "03"}}},
		pendID: {Txid: pendID, Version: 4, Blockheight: -1, ValueInSat: insightAmount(50010000), ValueOutSat: insightAmount(0), FeesSat: insightAmount(50010000),
			CoinSpecificData: json.RawMessage(insightZcashPending)},
	}
	f.address.Txids = []string{pendID, tzID, ztID, jsID, v1ID}
	f.address.Txs, f.address.UnconfirmedTxs = 4, 1
	s := insightTestServer(t, f)
	rpc := &insightZcashRPC{calls: map[string]int{}, txs: map[string]string{
		tzID: insightZcashSaplingTZ, ztID: insightZcashSaplingZT, jsID: insightZcashSprout, v1ID: insightZcashV1,
		cbID: `{"txid":"cb","overwintered":true,"version":4,"versiongroupid":"892f2085","expiryheight":0,"vin":[{"coinbase":"03"}],"vjoinsplit":[],"valueBalance":0.00000000,"valueBalanceZat":0,"vShieldedSpend":[],"vShieldedOutput":[]}`,
	}}
	s.SetBackend(rpc.call, "ZEC")
	if ext {
		if err := s.EnableExtension("zcash"); err != nil {
			t.Fatal(err)
		}
	}
	return s, f, rpc
}

func insightZcashGet(t *testing.T, s *InsightServer, path string) string {
	t.Helper()
	w := insightRequest(s, "GET", path, "")
	if w.Code != 200 {
		t.Fatalf("%s: %d %s", path, w.Code, w.Body)
	}
	return w.Body.String()
}

func insightZcashHas(t *testing.T, raw string, fields ...string) {
	t.Helper()
	for _, field := range fields {
		if !strings.Contains(raw, field) {
			t.Errorf("missing %s in %s", field, raw)
		}
	}
}

func TestInsightZcashSapling(t *testing.T) {
	s, _, rpc := insightZcashSetup(t, true)
	// t->z: the fee counts the shielded value, 1.0001 - 0 - 1 (stock's
	// feeSatoshis + valueBalanceZat); the converter's was the whole 1.0001.
	raw := insightZcashGet(t, s, "/api/tx/"+tzID)
	insightZcashHas(t, raw, `"fees":0.0001,`,
		`"vjoinsplit":[],"fOverwintered":true,"nVersionGroupId":2301567109,"nExpiryHeight":1000,"valueBalance":-1,`+
			`"spendDescs":[],"outputDescs":[{"cv":"aa","cmu":"bb","ephemeralKey":"cc","encCiphertext":"dd","outCiphertext":"ee","proof":"ff"}],"bindingSig":"0102"}`)
	// z->t: transparent in 0, out 0.9999, 1 ZEC unshielded; the converter's
	// clamped fee was 0.
	raw = insightZcashGet(t, s, "/api/tx/"+ztID)
	insightZcashHas(t, raw, `"fees":0.0001,`, `"nExpiryHeight":2000,"valueBalance":1,"spendDescs":[{"cv":"11","anchor":"22","nullifier":"33","rk":"44","proof":"55","spendAuthSig":"66"}],"outputDescs":[],"bindingSig":"0304"`)
	// A coinbase keeps the converter's fee and still gets the fields.
	raw = insightZcashGet(t, s, "/api/tx/"+cbID)
	insightZcashHas(t, raw, `"fees":0,`, `"valueBalance":0,"spendDescs":[],"outputDescs":[]}`)
	if strings.Contains(raw, "bindingSig") {
		t.Errorf("empty bindingSig emitted: %s", raw)
	}
	if rpc.calls[tzID] != 1 || rpc.calls[ztID] != 1 {
		t.Errorf("rpc calls %v", rpc.calls)
	}
}

func TestInsightZcashSproutAndV1(t *testing.T) {
	s, _, _ := insightZcashSetup(t, true)
	// JoinSplit: vpub amounts are toFixed(8) strings; fee 0.5001 - 0 - 0.5.
	raw := insightZcashGet(t, s, "/api/tx/"+jsID)
	insightZcashHas(t, raw, `"fees":0.0001,`, `"vjoinsplit":[{"vpub_old":"0.50000000","vpub_new":"0.00000000","n":0},{"vpub_old":"0.00000000","vpub_new":"0.00000000","n":1}]}`)
	for _, key := range []string{"fOverwintered", "nVersionGroupId", "valueBalance", "spendDescs"} {
		if strings.Contains(raw, key) {
			t.Errorf("sprout tx has %s: %s", key, raw)
		}
	}
	// A v1 tx: stock's vjoinsplit is for version >= 2 only; fOverwintered
	// is the daemon's false; the transparent fee stands.
	raw = insightZcashGet(t, s, "/api/tx/"+v1ID)
	insightZcashHas(t, raw, `"fees":0.0001,`, `"fOverwintered":false}`)
	for _, key := range []string{"vjoinsplit", "nVersionGroupId", "nExpiryHeight", "valueBalance"} {
		if strings.Contains(raw, key) {
			t.Errorf("v1 tx has %s: %s", key, raw)
		}
	}
}

// History reads the daemon JSON once per confirmed tx, uses a pending tx's
// coinSpecificData without a call, and serves a repeat from the cache.
func TestInsightZcashHistory(t *testing.T) {
	s, _, rpc := insightZcashSetup(t, true)
	for i := 0; i < 2; i++ {
		var out struct {
			Items []map[string]interface{} `json:"items"`
		}
		w := insightRequest(s, "GET", "/api/addrs/A/txs?from=0&to=5", "")
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Items) != 5 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		fees := map[string]interface{}{}
		for _, item := range out.Items {
			fees[item["txid"].(string)] = item["fees"]
		}
		want := map[string]interface{}{pendID: 0.0001, tzID: 0.0001, ztID: 0.0001, jsID: 0.0001, v1ID: 0.0001}
		for id, fee := range want {
			if fees[id] != fee {
				t.Errorf("pass %d: %s fee %v, want %v", i, id, fees[id], fee)
			}
		}
		if out.Items[0]["valueBalance"] != -0.5 || out.Items[0]["nExpiryHeight"] != float64(3000) {
			t.Errorf("pending fields: %v", out.Items[0])
		}
	}
	if rpc.calls[pendID] != 0 || rpc.calls[tzID] != 1 || rpc.calls[ztID] != 1 || rpc.calls[jsID] != 1 || rpc.calls[v1ID] != 1 {
		t.Errorf("rpc calls %v", rpc.calls)
	}
}

func TestInsightZcashRPCFailure(t *testing.T) {
	s, _, rpc := insightZcashSetup(t, true)
	rpc.fail = true
	for _, path := range []string{"/api/tx/" + tzID, "/api/addrs/A/txs"} {
		w := insightRequest(s, "GET", path, "")
		if w.Code != 500 || strings.Contains(w.Body.String(), "secret") {
			t.Errorf("%s: %d %s", path, w.Code, w.Body)
		}
	}
}

// Without the extension (Bitcoin, Firo, Axe) nothing is added or fetched,
// even when the tx carries daemon JSON with the Zcash keys.
func TestInsightZcashNotOtherCoins(t *testing.T) {
	s, _, rpc := insightZcashSetup(t, false)
	// A pending tx's own coinSpecificData passthrough keeps the daemon keys;
	// only the tx object's keys are checked.
	for _, path := range []string{"/api/tx/" + tzID, "/api/tx/" + pendID, "/api/addrs/A/txs"} {
		var out struct {
			Items []map[string]json.RawMessage `json:"items"`
		}
		var one map[string]json.RawMessage
		raw := insightZcashGet(t, s, path)
		if json.Unmarshal([]byte(raw), &out) != nil || json.Unmarshal([]byte(raw), &one) != nil {
			t.Fatal(raw)
		}
		for _, tx := range append(out.Items, one) {
			for _, key := range []string{"vjoinsplit", "fOverwintered", "nVersionGroupId", "nExpiryHeight", "valueBalance", "spendDescs", "outputDescs", "bindingSig"} {
				if _, ok := tx[key]; ok {
					t.Errorf("%s has %s: %s", path, key, raw)
				}
			}
		}
	}
	if raw := insightZcashGet(t, s, "/api/tx/"+tzID); !strings.Contains(raw, `"fees":1.0001,`) {
		t.Errorf("converter fee changed: %s", raw)
	}
	if len(rpc.calls) != 0 {
		t.Errorf("rpc calls %v", rpc.calls)
	}
}

func TestInsightZcashAmounts(t *testing.T) {
	for _, tc := range []struct {
		zat, coins string
		want       int64
		fixed      string
		bad        bool
	}{
		{"", "", 0, "0.00000000", false},
		{"-100000000", "", -100000000, "-1.00000000", false},
		{"", "-1.00000000", -100000000, "-1.00000000", false},
		{"", "0.5", 50000000, "0.50000000", false},
		{"", "32600000.00000001", 3260000000000001, "32600000.00000001", false},
		{"", "0.000000001", 0, "", true},
		{"1.5", "", 0, "", true},
	} {
		n, err := insightZcashAmount(json.Number(tc.zat), json.Number(tc.coins))
		if tc.bad {
			if err == nil {
				t.Errorf("%q/%q: no error", tc.zat, tc.coins)
			}
			continue
		}
		if err != nil || n.Int64() != tc.want || insightFixed8(n) != tc.fixed {
			t.Errorf("%q/%q: %v %v %s", tc.zat, tc.coins, n, err, insightFixed8(n))
		}
	}
}
