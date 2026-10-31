package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/trezor/blockbook/api"
	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/bchain/coins/anon"
	"github.com/trezor/blockbook/bchain/coins/btc"
)

// anond's getrawtransaction <txid> 1 for the version -3 (Groth16 Sprout)
// shielding tx 6caa4ad9... of block 1368074, cut to the keys the extension
// reads: two transparent inputs of AnMvf8Hn... (1.58402812 + 2.59171875), no
// transparent outputs and one JoinSplit moving 4.17564687 into the pool.
const insightAnonV3 = `{"txid":"6caa4ad9f46a5576318205be2a24654cb5f29037b7d1130b5d0e739f6ea8da1b","version":-3,"locktime":0,
	"vin":[{"txid":"84bf40449e751dc4e8aa3f2badb0180f2808d7e9b8a42121497b6723a9ec0614","vout":2,"value":1.58402812,"valueSat":158402812,"address":"AnMvf8HnX2DF4GTAWhiU1jjTyDsFsWRQRrq","sequence":4294967295},
	{"txid":"5aa86f01ce780b65ae5f9da70454a780fabb48da43cd6306a30e022edc7b8fe7","vout":1,"value":2.59171875,"valueSat":259171875,"address":"AnMvf8HnX2DF4GTAWhiU1jjTyDsFsWRQRrq","sequence":4294967295}],
	"vout":[],"vjoinsplit":[{"vpub_old":4.17564687,"vpub_new":0E-8,"vpub_oldZat":417564687,"vpub_newZat":0}],
	"blockhash":"000101faef080c654702d32c34e0e0ce6de68be32ebbc2985e05a81257f1bef6","height":1368074,"time":1793456775,"blocktime":1793456775}`

const insightAnonV3ID = "6caa4ad9f46a5576318205be2a24654cb5f29037b7d1130b5d0e739f6ea8da1b"

// TestInsightAnonV3 checks ANON's version -3 txs against the stock ANON
// Insight (bitcore-node-zelcash#anon): joinSplits and the shielded value
// count only for version >= 2, so a v-3 tx has no vjoinsplit and its fee is
// transparent in - out, the whole shielded amount (4.17574687 for this tx,
// as explorer.anon serves it). The extension is the one the ANON parser
// names, as blockbook.go enables it.
func TestInsightAnonV3(t *testing.T) {
	f := insightFixture()
	in := []api.Vin{
		{Txid: "84bf40449e751dc4e8aa3f2badb0180f2808d7e9b8a42121497b6723a9ec0614", Vout: 2, N: 0, Addresses: []string{"AnMvf8HnX2DF4GTAWhiU1jjTyDsFsWRQRrq"}, IsAddress: true, ValueSat: insightAmount(158402812)},
		{Txid: "5aa86f01ce780b65ae5f9da70454a780fabb48da43cd6306a30e022edc7b8fe7", Vout: 1, N: 1, Addresses: []string{"AnMvf8HnX2DF4GTAWhiU1jjTyDsFsWRQRrq"}, IsAddress: true, ValueSat: insightAmount(259171875)},
	}
	f.txs = map[string]*api.Tx{insightAnonV3ID: {Txid: insightAnonV3ID, Version: -3, Confirmations: 7, Blockheight: 1368074, Blocktime: 1793456775,
		ValueInSat: insightAmount(417574687), ValueOutSat: insightAmount(0), FeesSat: insightAmount(417574687), Vin: in}}
	s := insightTestServer(t, f)
	calls := 0
	s.SetBackend(func(method string, params []interface{}, result interface{}) error {
		if method != "getrawtransaction" || len(params) != 2 || params[0] != insightAnonV3ID || params[1] != 1 {
			return &bchain.RPCError{Code: -32601, Message: "Method not found"}
		}
		calls++
		return json.Unmarshal([]byte(insightAnonV3), result)
	}, "ANON")
	var parser interface{} = anon.NewAnonParser(anon.GetChainParams("main"), &btc.Configuration{})
	ext, ok := parser.(InsightExtensionProvider)
	if !ok {
		t.Fatal("the ANON parser names no Insight extension")
	}
	if err := s.EnableExtension(ext.InsightExtension()); err != nil {
		t.Fatal(err)
	}
	w := insightRequest(s, "GET", "/api/tx/"+insightAnonV3ID, "")
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	raw := w.Body.String()
	// valueIn is the listener's satoshi string for every coin (stock prints
	// 4.17574687); the wallet reads fees, vin value/addr and vout value.
	for _, want := range []string{`"version":-3`, `"valueIn":"417574687"`, `"valueOut":0`, `"value":1.58402812`, `"value":2.59171875`, `"fees":4.17574687`, `"addr":"AnMvf8HnX2DF4GTAWhiU1jjTyDsFsWRQRrq"`} {
		if !strings.Contains(raw, want) {
			t.Errorf("missing %s in %s", want, raw)
		}
	}
	for _, key := range []string{"vjoinsplit", "fOverwintered", "nVersionGroupId", "valueBalance"} {
		if strings.Contains(raw, key) {
			t.Errorf("v-3 tx has %s: %s", key, raw)
		}
	}
	if calls != 1 {
		t.Errorf("getrawtransaction calls = %d", calls)
	}
}
