//go:build unittest

package zec

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"reflect"
	"testing"

	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/bchain/coins/btc"
)

// Zero (ZER) is served by the Zcash parser ("Zero" in
// bchain/coins/blockchain.go). Zero's chainparams.cpp uses Zcash's mainnet
// base58 prefixes {0x1C,0xB8} (t1) and {0x1C,0xBD} (t3). Its pchMessageStart
// is "ZERO" (5a 45 52 4f), not Zcash's, but Blockbook reads blocks over RPC
// and only registers the Zcash parameters, so the network magic is not used.
// The vectors below are real Zero mainnet data (zerod 4.0.0, block 2576000).

// zerTxHex is Zero mainnet tx 6945f87c... (block 2576000), a v4 Sapling
// transaction (overwintered, version group 892f2085) with one transparent
// input and two t1 outputs, from zerod's getrawtransaction.
const zerTxHex = "0400008085202f89012d9e1eb6781c9caf5b1b8ca818f6b44edbb6a5a9f5e56258341e7762a4b580f8010000006a47304402202899eecb47fdd503b80aa22a6c63df99a68179ce60460249e1b5ae46d9f9c7f7022037ad24baa3ab9e17a25c9c206b314709f589ec6dece965fe1fd24113e6b9c95001210375bec77e87f7115d7c09a86020894a2baff0504adfc35058b666cb63023f6a37ffffffff02e07b0300000000001976a9140e84a48b21e593b8719df256e8431474611de4af88ac4b6d8fd6020000001976a914787d44274fe03e0bed87657d2f2fbd8ea6983fa188ac50c30000000000000000000000000000000000"

const zerTxid = "6945f87c51124cf8777cb38dc3b4b783868a3aecd59829b148f930fc81eb84ce"

// zerTxJSON is the same transaction in zerod's verbatim getrawtransaction
// verbose output, which is what Blockbook parses (it never decodes v4 bytes
// itself). zerod runs with insightexplorer, so vin carries value and address.
const zerTxJSON = `{"hex":"0400008085202f89012d9e1eb6781c9caf5b1b8ca818f6b44edbb6a5a9f5e56258341e7762a4b580f8010000006a47304402202899eecb47fdd503b80aa22a6c63df99a68179ce60460249e1b5ae46d9f9c7f7022037ad24baa3ab9e17a25c9c206b314709f589ec6dece965fe1fd24113e6b9c95001210375bec77e87f7115d7c09a86020894a2baff0504adfc35058b666cb63023f6a37ffffffff02e07b0300000000001976a9140e84a48b21e593b8719df256e8431474611de4af88ac4b6d8fd6020000001976a914787d44274fe03e0bed87657d2f2fbd8ea6983fa188ac50c30000000000000000000000000000000000","txid":"6945f87c51124cf8777cb38dc3b4b783868a3aecd59829b148f930fc81eb84ce","size":244,"overwintered":true,"version":4,"versiongroupid":"892f2085","locktime":50000,"expiryheight":0,"vin":[{"txid":"f880b5a462771e345862e5f5a9a5b6db4eb4f618a88c1b5baf9c1c78b61e9e2d","vout":1,"scriptSig":{"asm":"304402202899eecb47fdd503b80aa22a6c63df99a68179ce60460249e1b5ae46d9f9c7f7022037ad24baa3ab9e17a25c9c206b314709f589ec6dece965fe1fd24113e6b9c950[ALL] 0375bec77e87f7115d7c09a86020894a2baff0504adfc35058b666cb63023f6a37","hex":"47304402202899eecb47fdd503b80aa22a6c63df99a68179ce60460249e1b5ae46d9f9c7f7022037ad24baa3ab9e17a25c9c206b314709f589ec6dece965fe1fd24113e6b9c95001210375bec77e87f7115d7c09a86020894a2baff0504adfc35058b666cb63023f6a37"},"value":121.89896763,"valueZat":12189896763,"address":"t1UrhA1FTxFtz9V4ckBH6w2QnaHZwMXbpuC","sequence":4294967295}],"vout":[{"value":0.00228320,"valueZat":228320,"valueSat":228320,"n":0,"scriptPubKey":{"asm":"OP_DUP OP_HASH160 0e84a48b21e593b8719df256e8431474611de4af OP_EQUALVERIFY OP_CHECKSIG","hex":"76a9140e84a48b21e593b8719df256e8431474611de4af88ac","reqSigs":1,"type":"pubkeyhash","addresses":["t1KCNNCc6d9K8qmAVXKcTgDcsEF9VE1VwTR"]}},{"value":121.89658443,"valueZat":12189658443,"valueSat":12189658443,"n":1,"scriptPubKey":{"asm":"OP_DUP OP_HASH160 787d44274fe03e0bed87657d2f2fbd8ea6983fa1 OP_EQUALVERIFY OP_CHECKSIG","hex":"76a914787d44274fe03e0bed87657d2f2fbd8ea6983fa188ac","reqSigs":1,"type":"pubkeyhash","addresses":["t1UrhA1FTxFtz9V4ckBH6w2QnaHZwMXbpuC"]},"spentTxId":"ec04558e1b9c0e722e2c50affedca18f8437a0c223f077d53e3a691c0f3e9632","spentIndex":0,"spentHeight":2576005}],"vjoinsplit":[],"valueBalance":0.00000000,"valueBalanceZat":0,"vShieldedSpend":[],"vShieldedOutput":[],"blockhash":"00004377d7786716d24c278e88ee0335bb38736bbf850448ec388f4705798f15","height":2576000,"confirmations":2014,"time":1793172156,"blocktime":1793172156}`

// zerCoinbaseHex and zerCoinbaseJSON are the coinbase 7a912f35... of block
// 2576000, with a t3 (P2SH) output and two t1 outputs.
const zerCoinbaseHex = "0400008085202f89010000000000000000000000000000000000000000000000000000000000000000ffffffff2203804e2704bda2e16a08c88acad1000000002f7a706f6f6c2e63612fc88acad12f00ffffffff03c87e9a000000000017a914ffb6b76395048bba796296293e691732763cd8ea8750fad002000000001976a914ddc01d62957b72b1d9c4df973f68775ce444574588acb89da004000000001976a91426e11afd1615894a82a337e34a426071a50e7bbe88ac00000000000000000000000000000000000000"

const zerCoinbaseTxid = "7a912f358a2ea0bbf4d712ab6c84452c566abb9544a49352ec2373a06e692e1d"

const zerCoinbaseJSON = `{"hex":"0400008085202f89010000000000000000000000000000000000000000000000000000000000000000ffffffff2203804e2704bda2e16a08c88acad1000000002f7a706f6f6c2e63612fc88acad12f00ffffffff03c87e9a000000000017a914ffb6b76395048bba796296293e691732763cd8ea8750fad002000000001976a914ddc01d62957b72b1d9c4df973f68775ce444574588acb89da004000000001976a91426e11afd1615894a82a337e34a426071a50e7bbe88ac00000000000000000000000000000000000000","txid":"7a912f358a2ea0bbf4d712ab6c84452c566abb9544a49352ec2373a06e692e1d","size":204,"overwintered":true,"version":4,"versiongroupid":"892f2085","locktime":0,"expiryheight":0,"vin":[{"coinbase":"03804e2704bda2e16a08c88acad1000000002f7a706f6f6c2e63612fc88acad12f00","sequence":4294967295}],"vout":[{"value":0.10125000,"valueZat":10125000,"valueSat":10125000,"n":0,"scriptPubKey":{"asm":"OP_HASH160 ffb6b76395048bba796296293e691732763cd8ea OP_EQUAL","hex":"a914ffb6b76395048bba796296293e691732763cd8ea87","reqSigs":1,"type":"scripthash","addresses":["t3hsi89hPsZzmnbs3pny6cfAxMxV5TJLErj"]}},{"value":0.47250000,"valueZat":47250000,"valueSat":47250000,"n":1,"scriptPubKey":{"asm":"OP_DUP OP_HASH160 ddc01d62957b72b1d9c4df973f68775ce4445745 OP_EQUALVERIFY OP_CHECKSIG","hex":"76a914ddc01d62957b72b1d9c4df973f68775ce444574588ac","reqSigs":1,"type":"pubkeyhash","addresses":["t1e67V6ig2kcuUBrUjLQdkwj7acGhNKTmEq"]}},{"value":0.77635000,"valueZat":77635000,"valueSat":77635000,"n":2,"scriptPubKey":{"asm":"OP_DUP OP_HASH160 26e11afd1615894a82a337e34a426071a50e7bbe OP_EQUALVERIFY OP_CHECKSIG","hex":"76a91426e11afd1615894a82a337e34a426071a50e7bbe88ac","reqSigs":1,"type":"pubkeyhash","addresses":["t1MRBMDxW5QTbMteKprtjExPwWZuyitZ13o"]},"spentTxId":"d4763074e481ccb956f99735725844f82c99638e78cbcb7a5d326a1564ee8895","spentIndex":2,"spentHeight":2576724}],"vjoinsplit":[],"valueBalance":0.00000000,"valueBalanceZat":0,"vShieldedSpend":[],"vShieldedOutput":[],"blockhash":"00004377d7786716d24c278e88ee0335bb38736bbf850448ec388f4705798f15","height":2576000,"confirmations":2014,"time":1793172156,"blocktime":1793172156}`

func zerParser() *ZCashParser {
	return NewZCashParser(GetChainParams("main"), &btc.Configuration{})
}

func TestZero_NetworkParams(t *testing.T) {
	p := GetChainParams("main")
	// pchMessageStart 24 e9 27 64, read little endian
	if p.Net != 0x6427e924 {
		t.Errorf("Net = %#x, want Zcash mainnet magic (the parameters Zero is parsed with) 0x6427e924", uint32(p.Net))
	}
	if !bytes.Equal(p.PubKeyHashAddrID, []byte{0x1C, 0xB8}) || !bytes.Equal(p.ScriptHashAddrID, []byte{0x1C, 0xBD}) || p.AddressMagicLen != 2 {
		t.Errorf("address versions = %x/%x len %d, want 1cb8/1cbd len 2", p.PubKeyHashAddrID, p.ScriptHashAddrID, p.AddressMagicLen)
	}
}

// TestZero_RawTxIsReal checks that the fixture hex is the Zero v4 Sapling
// transaction it claims to be: header, version group and txid, for both the
// ordinary transaction and the coinbase.
func TestZero_RawTxIsReal(t *testing.T) {
	for name, tt := range map[string]struct{ hex, txid string }{
		"tx":       {zerTxHex, zerTxid},
		"coinbase": {zerCoinbaseHex, zerCoinbaseTxid},
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
		})
	}
}

func TestZero_ParseTxFromJson(t *testing.T) {
	parser := zerParser()
	tests := []struct {
		name      string
		json      string
		txid      string
		values    []int64
		addresses []string
		coinbase  bool
	}{
		{"v4-sapling-t1", zerTxJSON, zerTxid, []int64{228320, 12189658443}, []string{"t1KCNNCc6d9K8qmAVXKcTgDcsEF9VE1VwTR", "t1UrhA1FTxFtz9V4ckBH6w2QnaHZwMXbpuC"}, false},
		{"v4-coinbase", zerCoinbaseJSON, zerCoinbaseTxid, []int64{10125000, 47250000, 77635000}, []string{"t3hsi89hPsZzmnbs3pny6cfAxMxV5TJLErj", "t1e67V6ig2kcuUBrUjLQdkwj7acGhNKTmEq", "t1MRBMDxW5QTbMteKprtjExPwWZuyitZ13o"}, true},
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
			if len(tx.Vin) != 1 || (tx.Vin[0].Coinbase != "") != tt.coinbase {
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

func TestZero_PackUnpackTx(t *testing.T) {
	parser := zerParser()
	tx, err := parser.ParseTxFromJson([]byte(zerTxJSON))
	if err != nil {
		t.Fatal(err)
	}
	tx.Hex = zerTxHex
	tx.Blocktime = tx.Time
	packed, err := parser.PackTx(tx, 2576000, 1793172156)
	if err != nil {
		t.Fatal(err)
	}
	got, height, err := parser.UnpackTx(packed)
	if err != nil {
		t.Fatal(err)
	}
	if height != 2576000 {
		t.Errorf("height = %d", height)
	}
	if got.Txid != zerTxid || got.Hex != zerTxHex || got.Blocktime != 1793172156 || len(got.Vin) != 1 || len(got.Vout) != 2 {
		t.Errorf("unpacked tx = %+v", got)
	}
	if got.Vin[0].Txid != tx.Vin[0].Txid || got.Vout[0].ValueSat.Cmp(&tx.Vout[0].ValueSat) != 0 || got.Vout[1].ScriptPubKey.Hex != tx.Vout[1].ScriptPubKey.Hex {
		t.Errorf("unpacked tx differs: %+v", got)
	}
}

// TestZero_AddressRoundTrip checks a t1 (P2PKH) and a t3 (P2SH) address
// both ways: address -> output script and script -> address. Both come from
// the coinbase of block 2576000 (the t1 is its second output).
func TestZero_AddressRoundTrip(t *testing.T) {
	parser := zerParser()
	tests := []struct {
		address string
		script  string
	}{
		{"t1e67V6ig2kcuUBrUjLQdkwj7acGhNKTmEq", "76a914ddc01d62957b72b1d9c4df973f68775ce444574588ac"},
		{"t3hsi89hPsZzmnbs3pny6cfAxMxV5TJLErj", "a914ffb6b76395048bba796296293e691732763cd8ea87"},
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
