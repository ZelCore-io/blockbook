//go:build unittest

package server

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trezor/blockbook/api"
	"github.com/trezor/blockbook/bchain/coins/btc"
)

// scriptPubKey.type is classified from the script with stock's names; a type
// the converter already set (coin labels) wins.
func TestInsightScriptType(t *testing.T) {
	for _, tc := range []struct{ hex, want string }{
		{"76a91477aae4cd256e39b37331f5704cb79e5f7c2ffe1788ac", "pubkeyhash"},
		{"2102b8d462740c99032a00083ac7028879acec244849e54ad0a04ea87f632f54b1d2ac", "pubkeyhash"},
		{"a914b7fcbed4f9a2e2bf6ac64ba79ae0a4ab0ab0d6a187", "scripthash"},
		{"0014751e76e8199196d454941c45d1b3a323f1433bd6", "witnesspubkeyhash"},
		{"00201863143c14c5166804bd19203356da136c985678cd4d27a1b8c6329604903262", "witnessscripthash"},
		{"5120a60869f0dbcf1dc659c9cecbaf8050135ea9e8cdc487053f1dc6880949dc684c", "taproot"},
		{"6a026869", "nulldata"},
		{"512102b8d462740c99032a00083ac7028879acec244849e54ad0a04ea87f632f54b1d251ae", "multisig"},
		{"d20160", ""},
		{"e076a914db14fb77c6517377c08ae36a8688d874eaa95a9f88ac", ""},
		{"", ""},
		{"zz", ""},
	} {
		tx := &api.Tx{Txid: "t", Vout: []api.Vout{{ValueSat: insightAmount(1), Hex: tc.hex}}}
		out, err := insightTransaction(tx)
		if err != nil {
			t.Fatal(err)
		}
		if got := out.Vout[0].ScriptPubKey.Type; got != tc.want {
			t.Errorf("%s: type %q, want %q", tc.hex, got, tc.want)
		}
		if out.Vout[0].Type != "" {
			t.Errorf("%s: converter type %q changed", tc.hex, out.Vout[0].Type)
		}
	}
	tx := &api.Tx{Txid: "t", Vout: []api.Vout{{ValueSat: insightAmount(1), Hex: "76a91477aae4cd256e39b37331f5704cb79e5f7c2ffe1788ac", Type: "label"}}}
	out, err := insightTransaction(tx)
	if err != nil || out.Vout[0].ScriptPubKey.Type != "label" || out.Vout[0].Type != "label" {
		t.Errorf("converter type not kept: %+v %v", out.Vout[0], err)
	}
	// an unknown type is omitted, not written as ""
	b, _ := json.Marshal(insightVoutScript{Value: "0", Hex: "d20160"})
	if strings.Contains(string(b), `"type"`) {
		t.Errorf("empty type written: %s", b)
	}
}

// utxo rows carry the address's output script when the listener has the
// chain parser, on the single and the multi-address routes.
func TestInsightUtxoScriptPubKey(t *testing.T) {
	const (
		addr   = "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2"
		script = "76a91477bff20c60e522dfaa3350c39b030a5d004e839a88ac"
	)
	f := insightFixture()
	f.utxos = api.Utxos{{Txid: "a", Vout: 2, AmountSat: insightAmount(5), Height: 7, Confirmations: 3}}
	s := insightTestServer(t, f)
	if w := insightRequest(s, "GET", "/api/addr/"+addr+"/utxo", ""); strings.Contains(w.Body.String(), "scriptPubKey") {
		t.Fatalf("scriptPubKey without a parser: %s", w.Body.String())
	}
	s.SetChainParser(btc.NewBitcoinParser(btc.GetChainParams("main"), &btc.Configuration{}))
	want := `[{"address":"` + addr + `","txid":"a","vout":2,"scriptPubKey":"` + script + `","amount":0.00000005,"satoshis":5,"height":7,"confirmations":3}]` + "\n"
	for _, path := range []string{"/api/addr/" + addr + "/utxo", "/api/addrs/" + addr + "/utxo"} {
		w := insightRequest(s, "GET", path, "")
		if w.Code != 200 || w.Body.String() != want {
			t.Errorf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	r := httptest.NewRequest("POST", "/api/addrs/utxo", strings.NewReader(`{"addrs":"`+addr+`"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 || w.Body.String() != want {
		t.Errorf("POST addrs/utxo: %d %s", w.Code, w.Body.String())
	}
	// an address the parser cannot read gets no scriptPubKey
	if w := insightRequest(s, "GET", "/api/addr/A/utxo", ""); w.Code != 200 || strings.Contains(w.Body.String(), "scriptPubKey") {
		t.Errorf("unparsable address: %d %s", w.Code, w.Body.String())
	}
}

// status reports stock's network, testnet and relayfee; the relay fee comes
// from getnetworkinfo, cached, and is left out when the daemon cannot give it.
func TestInsightStatusNetwork(t *testing.T) {
	calls := 0
	var fail error
	rpc := func(method string, params []interface{}, out interface{}) error {
		if method != "getnetworkinfo" {
			return errors.New("unexpected method " + method)
		}
		calls++
		if fail != nil {
			return fail
		}
		return json.Unmarshal([]byte(`{"version":140000,"relayfee":0.00001000,"incrementalfee":0.00001000}`), out)
	}
	f := insightFixture()
	f.info.Backend.Chain = "main"
	s := insightTestServer(t, f)
	s.SetBackend(rpc, "FIRO")
	want := `{"info":{"blocks":10,"difficulty":2.5,"errors":"","network":"livenet","protocolversion":456,"relayfee":0.00001,"testnet":false,"version":123}}` + "\n"
	for i := 0; i < 2; i++ {
		if w := insightRequest(s, "GET", "/api/status?q=getInfo", ""); w.Code != 200 || w.Body.String() != want {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
	if calls != 1 {
		t.Errorf("getnetworkinfo called %d times, want 1 (cached)", calls)
	}
	for chain, wantNet := range map[string]string{"test": `"network":"testnet"`, "regtest": `"network":"regtest"`} {
		f := insightFixture()
		f.info.Backend.Chain = chain
		w := insightRequest(insightTestServer(t, f), "GET", "/api/status", "")
		if !strings.Contains(w.Body.String(), wantNet) || !strings.Contains(w.Body.String(), `"testnet":true`) || strings.Contains(w.Body.String(), "relayfee") {
			t.Errorf("%s: %s", chain, w.Body.String())
		}
	}
	fail = errors.New("down")
	f = insightFixture()
	s = insightTestServer(t, f)
	s.SetBackend(rpc, "FIRO")
	w := insightRequest(s, "GET", "/api/status", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "relayfee") || strings.Contains(w.Body.String(), "network") {
		t.Errorf("daemon failure: %d %s", w.Code, w.Body.String())
	}
}
