package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"strings"
	"testing"

	"github.com/trezor/blockbook/api"
	"github.com/trezor/blockbook/bchain"
)

func insightTxJSON(t *testing.T, tx *api.Tx) (string, map[string]interface{}) {
	t.Helper()
	out, err := insightTransaction(tx)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return string(data), m
}

func insightObj(t *testing.T, v interface{}, path ...interface{}) interface{} {
	t.Helper()
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, ok := v.(map[string]interface{})
			if !ok {
				t.Fatalf("not an object at %v", p)
			}
			v = m[k]
		case int:
			a, ok := v.([]interface{})
			if !ok || k >= len(a) {
				t.Fatalf("not an array at %v", p)
			}
			v = a[k]
		}
	}
	return v
}

func TestInsightTxSuperset(t *testing.T) {
	p2pkh := "76a914" + strings.Repeat("00", 20) + "88ac"
	tx := &api.Tx{
		Txid: "t1", Version: 2, Locktime: 7, Blockhash: "bh", Blockheight: 100, Blocktime: 1700000000, Confirmations: 5,
		Size: 225, VSize: 200, ValueInSat: insightAmount(3000), ValueOutSat: insightAmount(2500), FeesSat: insightAmount(500), Hex: "0100",
		Vin: []api.Vin{{Txid: "prev", Vout: 1, Sequence: 4294967295, N: 0, Addresses: []string{"A"}, IsAddress: true, ValueSat: insightAmount(3000), Hex: "4830"}},
		Vout: []api.Vout{
			{N: 0, ValueSat: insightAmount(2000), Hex: p2pkh, Addresses: []string{"B"}, IsAddress: true, Type: "P2PKH"},
			{N: 1, Hex: "6a026869", Addresses: []string{"OP_RETURN (hi)"}},
		},
	}
	raw, m := insightTxJSON(t, tx)
	for _, field := range []string{`"value":"2500"`, `"valueIn":"3000"`, `"valueOut":0.000025`, `"fees":0.000005`, `"lockTime":7`, `"locktime":7`, `"blockHash":"bh"`, `"blockhash":"bh"`, `"size":225`, `"vsize":200`, `"hex":"0100"`, `"doubleSpentTxID":null`} {
		if !strings.Contains(raw, field) {
			t.Errorf("missing %s in %s", field, raw)
		}
	}
	for _, key := range []string{"blockHeight", "blockheight"} {
		if m[key] != float64(100) {
			t.Errorf("%s = %v", key, m[key])
		}
	}
	for _, key := range []string{"blockTime", "blocktime", "time"} {
		if m[key] != float64(1700000000) {
			t.Errorf("%s = %v", key, m[key])
		}
	}
	if _, ok := m["isCoinBase"]; ok {
		t.Error("isCoinBase on a non-coinbase tx")
	}
	if insightObj(t, m, "vin", 0, "addr") != "A" || insightObj(t, m, "vin", 0, "valueSat") != float64(3000) || insightObj(t, m, "vin", 0, "value") != 0.00003 {
		t.Errorf("vin: %s", raw)
	}
	spk := insightObj(t, m, "vin", 0, "scriptPubKey").(map[string]interface{})
	if spk["valueSat"] != float64(3000) || spk["txid"] != "prev" || spk["value"] != 0.00003 || !reflect.DeepEqual(spk["addresses"], []interface{}{"A"}) || spk["isAddress"] != true {
		t.Errorf("vin scriptPubKey: %v", spk)
	}
	for _, key := range []string{"asm", "addr", "scriptSig", "doubleSpentTxID", "scriptPubKey"} {
		if _, ok := spk[key]; ok {
			t.Errorf("vin scriptPubKey has %s", key)
		}
	}
	if insightObj(t, m, "vin", 0, "scriptSig", "hex") != "4830" {
		t.Errorf("scriptSig: %s", raw)
	}
	if insightObj(t, m, "vout", 1, "scriptPubKey", "asm") != "OP_RETURN 6869" || !reflect.DeepEqual(insightObj(t, m, "vout", 1, "scriptPubKey", "addresses"), []interface{}{"OP_RETURN (hi)"}) {
		t.Errorf("OP_RETURN vout: %s", raw)
	}
	if !reflect.DeepEqual(insightObj(t, m, "vout", 1, "addresses"), []interface{}{"OP_RETURN (hi)"}) || insightObj(t, m, "vout", 1, "isAddress") != false {
		t.Errorf("OP_RETURN vout keys: %s", raw)
	}
	if asm, _ := insightObj(t, m, "vout", 0, "scriptPubKey", "asm").(string); !strings.HasPrefix(asm, "OP_DUP OP_HASH160") {
		t.Errorf("P2PKH asm %q", asm)
	}
	vout0 := insightObj(t, m, "vout", 0).(map[string]interface{})
	if vout0["value"] != 0.00002 || vout0["type"] != "P2PKH" || vout0["hex"] != p2pkh || !reflect.DeepEqual(vout0["addresses"], []interface{}{"B"}) {
		t.Errorf("vout 0: %v", vout0)
	}
	spk0 := vout0["scriptPubKey"].(map[string]interface{})
	if spk0["value"] != 0.00002 || spk0["n"] != float64(0) || spk0["type"] != "P2PKH" || spk0["hex"] != p2pkh || spk0["isAddress"] != true {
		t.Errorf("vout 0 scriptPubKey: %v", spk0)
	}
	if _, ok := vout0["spent"]; ok {
		t.Error("spent:false must be omitted")
	}
	// Unspent: stock's spentTxId/spentIndex/spentHeight are null at the top
	// level; the converter's scriptPubKey clone omits them (Blockbook did).
	for _, key := range []string{"spentTxId", "spentIndex", "spentHeight"} {
		if v, ok := vout0[key]; !ok || v != nil {
			t.Errorf("unspent %s must be null: %s", key, raw)
		}
		if _, ok := spk0[key]; ok {
			t.Errorf("unspent scriptPubKey has %s: %s", key, raw)
		}
	}
	// Spending info, when the worker filled it, is passed through to the
	// vout and its scriptPubKey clone; spentIndex 0 is a real index.
	tx.Vout[0].Spent, tx.Vout[0].SpentTxID, tx.Vout[0].SpentIndex, tx.Vout[0].SpentHeight = true, "next", 0, 101
	raw, m = insightTxJSON(t, tx)
	for _, field := range []string{`"spent":true`, `"spentTxId":"next"`, `"spentIndex":0`, `"spentHeight":101`} {
		if !strings.Contains(raw, field) {
			t.Errorf("missing %s in %s", field, raw)
		}
	}
	spk0 = insightObj(t, m, "vout", 0, "scriptPubKey").(map[string]interface{})
	// The clone follows Blockbook's vout JSON, which omits spentIndex 0.
	if _, ok := spk0["spentIndex"]; ok || spk0["spent"] != true || spk0["spentTxId"] != "next" || spk0["spentHeight"] != float64(101) {
		t.Errorf("spent scriptPubKey clone: %v", spk0)
	}
	// Satoshi strings carry any size; only JSON numbers keep the safe-int 500.
	large := &api.Tx{Txid: "large", ValueInSat: (*api.Amount)(new(big.Int).Lsh(big.NewInt(1), 60)), ValueOutSat: insightAmount(1)}
	raw, _ = insightTxJSON(t, large)
	if !strings.Contains(raw, `"valueIn":"1152921504606846976"`) {
		t.Errorf("large valueIn: %s", raw)
	}
	large.ValueOutSat = insightAmount(9007199254740992)
	if _, err := insightTransaction(large); err == nil {
		t.Error("valueOut is a JSON number and must keep the safe-int bound")
	}
	// A pending tx keeps blockheight -1 under both names and omits the hash.
	pending := &api.Tx{Txid: "p", Blockheight: -1, ValueOutSat: insightAmount(1), ConfirmationETABlocks: 2, ConfirmationETASeconds: 1200, Rbf: true}
	raw, m = insightTxJSON(t, pending)
	if m["blockheight"] != float64(-1) || m["blockHeight"] != float64(-1) || strings.Contains(raw, "blockhash") || strings.Contains(raw, "blockHash") || m["fees"] != float64(0) {
		t.Errorf("pending: %s", raw)
	}
	for _, field := range []string{`"confirmationETABlocks":2`, `"confirmationETASeconds":1200`, `"rbf":true`, `"locktime":0`} {
		if !strings.Contains(raw, field) {
			t.Errorf("missing %s in %s", field, raw)
		}
	}
	for _, key := range []string{"lockTime", "size", "vsize", "hex", "valueIn"} {
		if _, ok := m[key]; ok {
			t.Errorf("%s must be omitted when empty: %s", key, raw)
		}
	}
}

func TestInsightTxCoinbaseShape(t *testing.T) {
	tx := &api.Tx{
		Txid: "cb", Blockheight: 5, Blocktime: 10, Confirmations: 1, ValueOutSat: insightAmount(625000000),
		Vin:  []api.Vin{{Coinbase: "03abcd", Sequence: 4294967295}},
		Vout: []api.Vout{{N: 0, ValueSat: insightAmount(625000000), Hex: "51", Addresses: []string{"M"}, IsAddress: true}},
	}
	raw, m := insightTxJSON(t, tx)
	if m["isCoinBase"] != true {
		t.Errorf("isCoinBase: %s", raw)
	}
	if _, ok := m["valueIn"]; ok {
		t.Errorf("valueIn on coinbase: %s", raw)
	}
	if !strings.Contains(raw, `"fees":0`) || m["fees"] != float64(0) {
		t.Errorf("fees: %s", raw)
	}
	vin := insightObj(t, m, "vin", 0).(map[string]interface{})
	if v, ok := vin["addr"]; !ok || v != nil {
		t.Errorf("vin addr must be null: %s", raw)
	}
	if vin["coinbase"] != "03abcd" || vin["valueSat"] != float64(0) || vin["value"] != float64(0) {
		t.Errorf("coinbase vin: %s", raw)
	}
	if spk := vin["scriptPubKey"].(map[string]interface{}); spk["coinbase"] != "03abcd" {
		t.Errorf("coinbase scriptPubKey clone: %v", spk)
	}
	if m["value"] != "625000000" || m["valueOut"] != 6.25 {
		t.Errorf("value/valueOut: %s", raw)
	}
}

const (
	insightTestTxid    = "abababababababababababababababababababababababababababababababab"
	insightTestUnknown = "cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"
)

func TestInsightTxRoutes(t *testing.T) {
	f := insightFixture()
	f.txs[insightTestTxid] = &api.Tx{Txid: insightTestTxid, Hex: "0100", ValueOutSat: insightAmount(1), Vout: []api.Vout{{N: 0, ValueSat: insightAmount(1), Spent: true, SpentTxID: "next"}}}
	s := insightTestServer(t, f)
	for _, prefix := range []string{"/api", "/insight-api"} {
		f.spending = nil
		w := insightRequest(s, "GET", prefix+"/tx/"+insightTestTxid, "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"txid":"`+insightTestTxid+`"`) || !strings.Contains(w.Body.String(), `"spentTxId":"next"`) {
			t.Fatalf("%s tx: %d %s", prefix, w.Code, w.Body)
		}
		if !reflect.DeepEqual(f.spending, []bool{false}) {
			t.Fatalf("tx must not request the per-output spending scan: %v", f.spending)
		}
		w = insightRequest(s, "GET", prefix+"/rawtx/"+insightTestTxid, "")
		if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"rawtx":"0100"}` {
			t.Fatalf("%s rawtx: %d %s", prefix, w.Code, w.Body)
		}
	}
	// Upper-case txids are accepted and looked up lower-cased.
	if w := insightRequest(s, "GET", "/api/tx/"+strings.ToUpper(insightTestTxid), ""); w.Code != 200 {
		t.Fatalf("upper-case txid: %d", w.Code)
	}
	for _, route := range []string{"tx", "rawtx"} {
		for _, bad := range []string{"xyz", insightTestTxid[:63], insightTestTxid + "a", strings.Repeat("zz", 32), ""} {
			if w := insightRequest(s, "GET", "/api/"+route+"/"+bad+"/", ""); w.Code != 400 && !(bad == "" && w.Code == 404) {
				t.Errorf("%s/%q: %d %s", route, bad, w.Code, w.Body)
			} else if !strings.Contains(w.Body.String(), `"status":"error"`) {
				t.Errorf("%s/%q error shape: %s", route, bad, w.Body)
			}
		}
		if w := insightRequest(s, "GET", "/api/"+route+"//", ""); w.Code != 400 {
			t.Errorf("%s empty: %d %s", route, w.Code, w.Body)
		}
		w := insightRequest(s, "GET", "/api/"+route+"/"+insightTestUnknown, "")
		if w.Code != 404 || !strings.Contains(w.Body.String(), `"error":"Not found"`) {
			t.Errorf("%s unknown: %d %s", route, w.Code, w.Body)
		}
	}
	// The real worker wraps every tx-cache failure in a public APIError: only
	// its exact not-found text is 404; "not found (<cause>)" (a backend
	// failure), other public errors and plain errors are a generic 500.
	for _, tc := range []struct {
		err  error
		code int
	}{
		{api.NewAPIError("Transaction '"+insightTestTxid+"' not found", true), 404},
		{api.NewAPIError("Transaction '"+insightTestTxid+"' not found (connection refused http://user:password@secret/)", true), 500},
		{api.NewAPIError("Transaction '"+insightTestUnknown+"' not found", true), 500},
		{api.NewAPIError("Transaction '"+insightTestTxid+"' not found", false), 500},
		{bchain.ErrTxNotFound, 500},
		{errors.New("http://user:password@secret/"), 500},
	} {
		f := insightFixture()
		f.err = tc.err
		for _, route := range []string{"tx", "rawtx"} {
			w := insightRequest(insightTestServer(t, f), "GET", "/api/"+route+"/"+insightTestTxid, "")
			if w.Code != tc.code || strings.Contains(w.Body.String(), "secret") {
				t.Errorf("%s %v: %d %s", route, tc.err, w.Code, w.Body)
			}
		}
	}
}

func TestInsightRawTxDaemonFallback(t *testing.T) {
	f := insightFixture()
	f.txs[insightTestTxid] = &api.Tx{Txid: insightTestTxid}
	s := insightTestServer(t, f)
	if w := insightRequest(s, "GET", "/api/rawtx/"+insightTestTxid, ""); w.Code != 500 {
		t.Fatalf("no hex, no rpc: %d %s", w.Code, w.Body)
	}
	var seen []interface{}
	s.SetBackend(func(method string, params []interface{}, result interface{}) error {
		if method != "getrawtransaction" {
			return errors.New("unexpected " + method)
		}
		seen = params
		*(result.(*string)) = "0200"
		return nil
	}, "BTC")
	w := insightRequest(s, "GET", "/api/rawtx/"+insightTestTxid, "")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"rawtx":"0200"}` || !reflect.DeepEqual(seen, []interface{}{insightTestTxid, 0}) {
		t.Fatalf("fallback: %d %s %v", w.Code, w.Body, seen)
	}
	for _, tc := range []struct {
		err  error
		hex  string
		code int
	}{{&bchain.RPCError{Code: -5, Message: "No such mempool or blockchain transaction"}, "", 404}, {errors.New("secret daemon text"), "", 500}, {nil, "zz", 500}, {nil, "", 500}} {
		s.SetBackend(func(_ string, _ []interface{}, result interface{}) error {
			*(result.(*string)) = tc.hex
			return tc.err
		}, "BTC")
		w := insightRequest(s, "GET", "/api/rawtx/"+insightTestTxid, "")
		if w.Code != tc.code || strings.Contains(w.Body.String(), "secret") {
			t.Errorf("%v %q: %d %s", tc.err, tc.hex, w.Code, w.Body)
		}
	}
}

func insightBlockWithTxs(hash string, n int, f *insightFake) {
	b := &api.Block{TxCount: n, BlockInfo: api.BlockInfo{Hash: hash, Height: 7}}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("t%02d", i)
		b.Transactions = append(b.Transactions, &api.Tx{Txid: id})
		f.txs[id] = &api.Tx{Txid: id, Blockhash: hash, Blockheight: 7, ValueOutSat: insightAmount(int64(i))}
	}
	if f.blockByHash == nil {
		f.blockByHash = map[string]*api.Block{}
	}
	f.blockByHash[hash] = b
}

func TestInsightTxsByBlock(t *testing.T) {
	hash := strings.Repeat("1a", 32)
	type page struct {
		PagesTotal int               `json:"pagesTotal"`
		Txs        []json.RawMessage `json:"txs"`
	}
	for _, tc := range []struct {
		query string
		clamp bool
		ids   []string
		total int
	}{
		{"&pageNum=2", false, []string{"t20", "t21", "t22"}, 3},
		{"", false, []string{"t00", "t01", "t02", "t03", "t04", "t05", "t06", "t07", "t08", "t09"}, 3},
		{"&pageNum=1", false, []string{"t10", "t11", "t12", "t13", "t14", "t15", "t16", "t17", "t18", "t19"}, 3},
		{"&pageNum=3", false, nil, 3},
		{"&pageNum=3", true, nil, 3},
		{"&pageNum=99999999999999999999", true, nil, 3},
	} {
		t.Run(tc.query, func(t *testing.T) {
			f := insightFixture()
			f.clampPages = tc.clamp
			insightBlockWithTxs(hash, 23, f)
			w := insightRequest(insightTestServer(t, f), "GET", "/insight-api/txs?block="+hash+tc.query, "")
			var out page
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Txs == nil {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			if out.PagesTotal != tc.total || len(out.Txs) != len(tc.ids) || !reflect.DeepEqual(f.fetched, tc.ids) {
				t.Fatalf("pagesTotal=%d txs=%d fetched=%v", out.PagesTotal, len(out.Txs), f.fetched)
			}
			for i, raw := range out.Txs {
				if !strings.Contains(string(raw), `"txid":"`+tc.ids[i]+`"`) || !strings.Contains(string(raw), `"blockHeight":7`) || !strings.Contains(string(raw), `"blockheight":7`) {
					t.Fatalf("tx %d: %s", i, raw)
				}
			}
			for _, sp := range f.spending {
				if sp {
					t.Fatal("txs by block must not request spending txs")
				}
			}
		})
	}
	f := insightFixture()
	insightBlockWithTxs(hash, 0, f)
	w := insightRequest(insightTestServer(t, f), "GET", "/api/txs?block="+hash, "")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"pagesTotal":0,"txs":[]}` {
		t.Fatalf("empty block: %d %s", w.Code, w.Body)
	}
	for _, tc := range []struct {
		path string
		code int
	}{
		{"/api/txs?block=" + hash + "&pageNum=-1", 400}, {"/api/txs?block=" + hash + "&pageNum=a", 400},
		{"/api/txs?block=" + hash + "&pageNum=1.5", 400}, {"/api/txs?block=" + hash + "&pageNum=", 400},
		{"/api/txs", 400}, {"/api/txs?block=", 400}, {"/api/txs?block=xyz", 400},
		{"/api/txs?block=" + strings.Repeat("22", 32), 404},
	} {
		f := insightFixture()
		insightBlockWithTxs(hash, 23, f)
		w := insightRequest(insightTestServer(t, f), "GET", tc.path, "")
		if w.Code != tc.code || !strings.Contains(w.Body.String(), `"status":"error"`) {
			t.Errorf("%s: %d %s", tc.path, w.Code, w.Body)
		}
	}
	// A tx of the block that fails to load is a generic 500.
	f = insightFixture()
	insightBlockWithTxs(hash, 23, f)
	f.txErr = map[string]error{"t21": errors.New("http://user:password@secret/")}
	w = insightRequest(insightTestServer(t, f), "GET", "/api/txs?block="+hash+"&pageNum=2", "")
	if w.Code != 500 || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("tx failure: %d %s", w.Code, w.Body)
	}
}

// TestInsightBlockErrorShapes uses the real worker's error shapes: GetBlock
// and GetBlockRaw answer an unknown block with exactly "Block not found" and
// wrap backend failures as "Block not found, <cause>". Only the former is 404.
func TestInsightBlockErrorShapes(t *testing.T) {
	hash := strings.Repeat("1a", 32)
	for _, tc := range []struct {
		err  error
		code int
	}{
		{api.NewAPIError("Block not found", true), 404},
		{api.NewAPIError("Block not found, connection refused http://user:password@secret/", true), 500},
		{api.NewAPIError("Block not found", false), 500},
		{errors.New("http://user:password@secret/"), 500},
	} {
		for _, path := range []string{"/api/block/" + hash, "/api/txs?block=" + hash} {
			f := insightFixture()
			insightBlockWithTxs(hash, 3, f)
			f.err = tc.err
			w := insightRequest(insightTestServer(t, f), "GET", path, "")
			if w.Code != tc.code || strings.Contains(w.Body.String(), "secret") {
				t.Errorf("%s %v: %d %s", path, tc.err, w.Code, w.Body)
			}
		}
		f := insightFixture()
		f.rawErr = tc.err
		w := insightRequest(insightTestServer(t, f), "GET", "/api/rawblock/"+hash, "")
		if w.Code != tc.code || strings.Contains(w.Body.String(), "secret") {
			t.Errorf("rawblock %v: %d %s", tc.err, w.Code, w.Body)
		}
	}
}
