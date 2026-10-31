//go:build unittest

package zec

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"reflect"
	"strings"
	"testing"

	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/bchain/coins/btc"
)

// BitcoinZ (BTCZ) is served by the Zcash parser ("BitcoinZ" in
// bchain/coins/blockchain.go). BitcoinZ's chainparams.cpp keeps Zcash's
// mainnet pchMessageStart (24 e9 27 64) and base58 prefixes {0x1C,0xB8} (t1)
// and {0x1C,0xBD} (t3), and bitcoinzd reports the chain as "main", so
// GetChainParams returns the Zcash mainnet parameters unchanged.
//
// The vectors are real BitcoinZ mainnet transactions from block 1865000
// (bitcoinzd 2.3.1). The public BTCZ Insight (explorer.btcz.rocks) serves no
// raw hex, so each hex was rebuilt from its /api/tx fields (transparent-only
// v4 transactions: no JoinSplits, no Sapling spends or outputs) and is checked
// below against its txid by double SHA-256. The JSON is shaped like zcashd's
// getrawtransaction verbose output and carries the same fields Blockbook reads.

// btczTx is BitcoinZ tx e4c2a073... (block 1865000), a v4 Sapling-format transaction
// (overwintered, version group 892f2085) with two t1 inputs and five t1 outputs.
const btczTxHex = "0400008085202f8902ab06011b5a44307f85032fdb55529f142c9f8ae7738e1ba3410872357401c6ba020000006b483045022100a9b472ef668efbe9d69f3a18864076e049ecd7de234fd206bd7bfc1e629527e90220618c62f10dc0774459909cb2f42b56b30b38949533e2b32c15521d21b03060590121037cacad1414c87b3414ee7b6819b4bf3335ab7e372179968ec0a91e33d2daff01feffffff311a576a04fdbe040b9defe08207a50d6dddf191bffcb1798d8b1d7f893a63c5000000006b483045022100d2d349757d336832bd60a7d4da553a950181e7040aa028e55034ac5e6b5f8c27022022d1c533560e38df1e131b31f3f825fb474a9244ee531d9b85cb98b6a912701d0121037cacad1414c87b3414ee7b6819b4bf3335ab7e372179968ec0a91e33d2daff01feffffff057132a4aa1d0000001976a914440c1f07efa74fe4a7ee58267b23d8955819bce588ac6340583f110000001976a914d5775c5942e28f360ef006118682ac71edbeb2a188ac90531a5f090000001976a91442e4dc8a12fea448f5d811faa9f4dc8f9e143ce988ac46601b00000000001976a91472f12c92bb9a464622e8ab3d13b1855fed00343c88ac745a12ec010000001976a9148baf06eece2690376571af9030c91c2ca2f3417588ac05751c003c751c000000000000000000000000"

const btczTxTxid = "e4c2a073dab67f4b43252479aafd58bae4c6dfce74119e3419a33a8f10a6186e"

const btczTxJSON = `{"hex":"0400008085202f8902ab06011b5a44307f85032fdb55529f142c9f8ae7738e1ba3410872357401c6ba020000006b483045022100a9b472ef668efbe9d69f3a18864076e049ecd7de234fd206bd7bfc1e629527e90220618c62f10dc0774459909cb2f42b56b30b38949533e2b32c15521d21b03060590121037cacad1414c87b3414ee7b6819b4bf3335ab7e372179968ec0a91e33d2daff01feffffff311a576a04fdbe040b9defe08207a50d6dddf191bffcb1798d8b1d7f893a63c5000000006b483045022100d2d349757d336832bd60a7d4da553a950181e7040aa028e55034ac5e6b5f8c27022022d1c533560e38df1e131b31f3f825fb474a9244ee531d9b85cb98b6a912701d0121037cacad1414c87b3414ee7b6819b4bf3335ab7e372179968ec0a91e33d2daff01feffffff057132a4aa1d0000001976a914440c1f07efa74fe4a7ee58267b23d8955819bce588ac6340583f110000001976a914d5775c5942e28f360ef006118682ac71edbeb2a188ac90531a5f090000001976a91442e4dc8a12fea448f5d811faa9f4dc8f9e143ce988ac46601b00000000001976a91472f12c92bb9a464622e8ab3d13b1855fed00343c88ac745a12ec010000001976a9148baf06eece2690376571af9030c91c2ca2f3417588ac05751c003c751c000000000000000000000000","txid":"e4c2a073dab67f4b43252479aafd58bae4c6dfce74119e3419a33a8f10a6186e","overwintered":true,"version":4,"versiongroupid":"892f2085","locktime":1864965,"expiryheight":1865020,"vin":[{"txid":"bac6017435720841a31b8e73e78a9f2c149f5255db2f03857f30445a1b0106ab","vout":2,"scriptSig":{"hex":"483045022100a9b472ef668efbe9d69f3a18864076e049ecd7de234fd206bd7bfc1e629527e90220618c62f10dc0774459909cb2f42b56b30b38949533e2b32c15521d21b03060590121037cacad1414c87b3414ee7b6819b4bf3335ab7e372179968ec0a91e33d2daff01"},"sequence":4294967294},{"txid":"c5633a897f1d8b8d79b1fcbf91f1dd6d0da50782e0ef9d0b04befd046a571a31","vout":0,"scriptSig":{"hex":"483045022100d2d349757d336832bd60a7d4da553a950181e7040aa028e55034ac5e6b5f8c27022022d1c533560e38df1e131b31f3f825fb474a9244ee531d9b85cb98b6a912701d0121037cacad1414c87b3414ee7b6819b4bf3335ab7e372179968ec0a91e33d2daff01"},"sequence":4294967294}],"vout":[{"value":1274.16939121,"valueSat":127416939121,"n":0,"scriptPubKey":{"hex":"76a914440c1f07efa74fe4a7ee58267b23d8955819bce588ac","type":"pubkeyhash","addresses":["t1Q5QUaQfw9REizvQNvP1a4sFz9EjEtaRo1"]}},{"value":740.77192291,"valueSat":74077192291,"n":1,"scriptPubKey":{"hex":"76a914d5775c5942e28f360ef006118682ac71edbeb2a188ac","type":"pubkeyhash","addresses":["t1dLJvXPsdbkBNuSio8zWJti5MoQ7x8Jn8H"]}},{"value":402.50266512,"valueSat":40250266512,"n":2,"scriptPubKey":{"hex":"76a91442e4dc8a12fea448f5d811faa9f4dc8f9e143ce988ac","type":"pubkeyhash","addresses":["t1PyJma4pZBKTdZgqnUcErpzC6hdRM57zFS"]}},{"value":0.01794118,"valueSat":1794118,"n":3,"scriptPubKey":{"hex":"76a91472f12c92bb9a464622e8ab3d13b1855fed00343c88ac","type":"pubkeyhash","addresses":["t1UMMxGvtKPkSx693YfpNMiBEcEsX2Vy3K6"]}},{"value":82.55593076,"valueSat":8255593076,"n":4,"scriptPubKey":{"hex":"76a9148baf06eece2690376571af9030c91c2ca2f3417588ac","type":"pubkeyhash","addresses":["t1WcBc6TDyh8M4vQNQy4LxamTNuQ59iJvDo"]}}],"vjoinsplit":[],"valueBalance":0.0,"vShieldedSpend":[],"vShieldedOutput":[],"blockhash":"000003b72b31be8dfe8c4b874c915c3e39d631fda0272190c76d7dfa82afc37c","height":1865000,"time":1793396505,"blocktime":1793396505}`

// btczCoinbase is the coinbase 27a112a2... of block 1865000: three funding stream
// outputs to t3 (P2SH) addresses and the miner reward to a t1 address.
const btczCoinbaseHex = "0400008085202f89010000000000000000000000000000000000000000000000000000000000000000ffffffff220328751c04190fe56a0867e2ca1b000000002f7a706f6f6c2e63612f67e2ca1b2f00ffffffff04409452a30300000017a9141506747cbcb4a89e1c6d37da1d8523347629397c8700ba1dd20500000017a9146c34429047366517587a6311df11545b4fa38a5c87c002da170500000017a914c85f4794945c5a546be67c87c90120d77cbdb66287375d29353a0000001976a91441b79582732d06306d32dfb3183ce124b6f328ad88ac00000000000000000000000000000000000000"

const btczCoinbaseTxid = "27a112a240a86ac90b300b51cdd8f5dd9a0011755e5cf70aaa1e5a3b9bc67c54"

const btczCoinbaseJSON = `{"hex":"0400008085202f89010000000000000000000000000000000000000000000000000000000000000000ffffffff220328751c04190fe56a0867e2ca1b000000002f7a706f6f6c2e63612f67e2ca1b2f00ffffffff04409452a30300000017a9141506747cbcb4a89e1c6d37da1d8523347629397c8700ba1dd20500000017a9146c34429047366517587a6311df11545b4fa38a5c87c002da170500000017a914c85f4794945c5a546be67c87c90120d77cbdb66287375d29353a0000001976a91441b79582732d06306d32dfb3183ce124b6f328ad88ac00000000000000000000000000000000000000","txid":"27a112a240a86ac90b300b51cdd8f5dd9a0011755e5cf70aaa1e5a3b9bc67c54","overwintered":true,"version":4,"versiongroupid":"892f2085","locktime":0,"expiryheight":0,"vin":[{"coinbase":"0328751c04190fe56a0867e2ca1b000000002f7a706f6f6c2e63612f67e2ca1b2f00","sequence":4294967295}],"vout":[{"value":156.25,"valueSat":15625000000,"n":0,"scriptPubKey":{"hex":"a9141506747cbcb4a89e1c6d37da1d8523347629397c87","type":"scripthash","addresses":["t3LUnrQk7pHJTvTyTVzdDHNVamMGfvXAi4k"]}},{"value":250.0,"valueSat":25000000000,"n":1,"scriptPubKey":{"hex":"a9146c34429047366517587a6311df11545b4fa38a5c87","type":"scripthash","addresses":["t3URkWgiiYVNTAvKHy5HdPia3hft9byrsNz"]}},{"value":218.75,"valueSat":21875000000,"n":2,"scriptPubKey":{"hex":"a914c85f4794945c5a546be67c87c90120d77cbdb66287","type":"scripthash","addresses":["t3cq6DPTZbVShmY5hs4NLJyBhTfw5KNZNcf"]}},{"value":2500.00006455,"valueSat":250000006455,"n":3,"scriptPubKey":{"hex":"76a91441b79582732d06306d32dfb3183ce124b6f328ad88ac","type":"pubkeyhash","addresses":["t1Ps5rSpdGcvzkbf3hcBXq8apTdFZGjXRy7"]}}],"vjoinsplit":[],"valueBalance":0.0,"vShieldedSpend":[],"vShieldedOutput":[],"blockhash":"000003b72b31be8dfe8c4b874c915c3e39d631fda0272190c76d7dfa82afc37c","height":1865000,"time":1793396505,"blocktime":1793396505}`

func btczParser() *ZCashParser {
	return NewZCashParser(GetChainParams("main"), &btc.Configuration{})
}

func TestBitcoinZ_NetworkParams(t *testing.T) {
	p := GetChainParams("main")
	if p.Net != 0x6427e924 {
		t.Errorf("Net = %#x, want 0x6427e924 (pchMessageStart 24 e9 27 64, shared with Zcash)", uint32(p.Net))
	}
	if !bytes.Equal(p.PubKeyHashAddrID, []byte{0x1C, 0xB8}) || !bytes.Equal(p.ScriptHashAddrID, []byte{0x1C, 0xBD}) || p.AddressMagicLen != 2 {
		t.Errorf("address versions = %x/%x len %d, want 1cb8/1cbd len 2", p.PubKeyHashAddrID, p.ScriptHashAddrID, p.AddressMagicLen)
	}
}

// TestBitcoinZ_RawTxIsReal checks the fixture hex: v4 Sapling header, txid by
// double SHA-256, and that every output script in the JSON is in the hex.
func TestBitcoinZ_RawTxIsReal(t *testing.T) {
	for name, tt := range map[string]struct{ hex, txid, json string }{
		"tx":       {btczTxHex, btczTxTxid, btczTxJSON},
		"coinbase": {btczCoinbaseHex, btczCoinbaseTxid, btczCoinbaseJSON},
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := hex.DecodeString(tt.hex)
			if err != nil {
				t.Fatal(err)
			}
			if got := hex.EncodeToString(raw[:8]); got != "0400008085202f89" {
				t.Errorf("header+versiongroup = %s, want 0400008085202f89 (overwintered v4, Sapling)", got)
			}
			h1 := sha256.Sum256(raw)
			h2 := sha256.Sum256(h1[:])
			for i, j := 0, len(h2)-1; i < j; i, j = i+1, j-1 {
				h2[i], h2[j] = h2[j], h2[i]
			}
			if got := hex.EncodeToString(h2[:]); got != tt.txid {
				t.Errorf("txid = %s, want %s", got, tt.txid)
			}
			tx, err := btczParser().ParseTxFromJson([]byte(tt.json))
			if err != nil {
				t.Fatal(err)
			}
			if tx.Hex != tt.hex {
				t.Error("JSON hex differs from the fixture hex")
			}
			for i := range tx.Vout {
				if !strings.Contains(tt.hex, tx.Vout[i].ScriptPubKey.Hex) {
					t.Errorf("vout %d script %s not in the raw transaction", i, tx.Vout[i].ScriptPubKey.Hex)
				}
			}
		})
	}
}

func TestBitcoinZ_ParseTxFromJson(t *testing.T) {
	parser := btczParser()
	tests := []struct {
		name      string
		json      string
		txid      string
		vin       int
		values    []int64
		addresses []string
		coinbase  bool
	}{
		{"v4-sapling-t1", btczTxJSON, btczTxTxid, 2,
			[]int64{127416939121, 74077192291, 40250266512, 1794118, 8255593076},
			[]string{"t1Q5QUaQfw9REizvQNvP1a4sFz9EjEtaRo1", "t1dLJvXPsdbkBNuSio8zWJti5MoQ7x8Jn8H", "t1PyJma4pZBKTdZgqnUcErpzC6hdRM57zFS", "t1UMMxGvtKPkSx693YfpNMiBEcEsX2Vy3K6", "t1WcBc6TDyh8M4vQNQy4LxamTNuQ59iJvDo"}, false},
		{"v4-coinbase-funding-streams", btczCoinbaseJSON, btczCoinbaseTxid, 1,
			[]int64{15625000000, 25000000000, 21875000000, 250000006455},
			[]string{"t3LUnrQk7pHJTvTyTVzdDHNVamMGfvXAi4k", "t3URkWgiiYVNTAvKHy5HdPia3hft9byrsNz", "t3cq6DPTZbVShmY5hs4NLJyBhTfw5KNZNcf", "t1Ps5rSpdGcvzkbf3hcBXq8apTdFZGjXRy7"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx, err := parser.ParseTxFromJson([]byte(tt.json))
			if err != nil {
				t.Fatal(err)
			}
			if tx.Txid != tt.txid || tx.Version != 4 {
				t.Errorf("txid/version = %s/%d", tx.Txid, tx.Version)
			}
			if len(tx.Vin) != tt.vin || (tx.Vin[0].Coinbase != "") != tt.coinbase {
				t.Errorf("vin = %+v", tx.Vin)
			}
			if len(tx.Vout) != len(tt.values) {
				t.Fatalf("len(vout) = %d, want %d", len(tx.Vout), len(tt.values))
			}
			for i := range tx.Vout {
				if tx.Vout[i].ValueSat.Cmp(big.NewInt(tt.values[i])) != 0 {
					t.Errorf("vout %d value = %s, want %d", i, tx.Vout[i].ValueSat.String(), tt.values[i])
				}
				// The address is derived from the script, not taken from the JSON.
				desc, err := parser.GetAddrDescFromVout(&tx.Vout[i])
				if err != nil {
					t.Fatal(err)
				}
				addrs, searchable, err := parser.GetAddressesFromAddrDesc(desc)
				if err != nil || !searchable || !reflect.DeepEqual(addrs, []string{tt.addresses[i]}) {
					t.Errorf("vout %d addresses = %v (searchable %v, err %v), want %s", i, addrs, searchable, err, tt.addresses[i])
				}
			}
		})
	}
}

func TestBitcoinZ_PackUnpackTx(t *testing.T) {
	parser := btczParser()
	tx, err := parser.ParseTxFromJson([]byte(btczTxJSON))
	if err != nil {
		t.Fatal(err)
	}
	tx.Blocktime = tx.Time
	packed, err := parser.PackTx(tx, 1865000, 1793396505)
	if err != nil {
		t.Fatal(err)
	}
	got, height, err := parser.UnpackTx(packed)
	if err != nil {
		t.Fatal(err)
	}
	if height != 1865000 {
		t.Errorf("height = %d", height)
	}
	if got.Txid != btczTxTxid || got.Hex != btczTxHex || got.Blocktime != 1793396505 || len(got.Vin) != 2 || len(got.Vout) != 5 {
		t.Errorf("unpacked tx = %+v", got)
	}
	if got.Vin[1].Txid != tx.Vin[1].Txid || got.Vout[4].ValueSat.Cmp(&tx.Vout[4].ValueSat) != 0 || got.Vout[0].ScriptPubKey.Hex != tx.Vout[0].ScriptPubKey.Hex {
		t.Errorf("unpacked tx differs: %+v", got)
	}
}

// TestBitcoinZ_AddressRoundTrip checks a t1 (P2PKH) and a t3 (P2SH) address
// from the coinbase of block 1865000 both ways.
func TestBitcoinZ_AddressRoundTrip(t *testing.T) {
	parser := btczParser()
	tests := []struct {
		address string
		script  string
	}{
		{"t1Ps5rSpdGcvzkbf3hcBXq8apTdFZGjXRy7", "76a91441b79582732d06306d32dfb3183ce124b6f328ad88ac"},
		{"t3URkWgiiYVNTAvKHy5HdPia3hft9byrsNz", "a9146c34429047366517587a6311df11545b4fa38a5c87"},
	}
	for _, tt := range tests {
		t.Run(tt.address[:2], func(t *testing.T) {
			desc, err := parser.GetAddrDescFromAddress(tt.address)
			if err != nil {
				t.Fatal(err)
			}
			if got := hex.EncodeToString(desc); got != tt.script {
				t.Errorf("script = %s, want %s", got, tt.script)
			}
			script, _ := hex.DecodeString(tt.script)
			vout := bchain.Vout{ScriptPubKey: bchain.ScriptPubKey{Hex: tt.script}}
			fromVout, err := parser.GetAddrDescFromVout(&vout)
			if err != nil || !bytes.Equal(fromVout, script) {
				t.Errorf("GetAddrDescFromVout = %x, %v", fromVout, err)
			}
			addrs, searchable, err := parser.GetAddressesFromAddrDesc(script)
			if err != nil || !searchable || !reflect.DeepEqual(addrs, []string{tt.address}) {
				t.Errorf("addresses = %v (searchable %v, err %v)", addrs, searchable, err)
			}
		})
	}
}
