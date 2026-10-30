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

// Zclassic (ZCL) is served by the Zcash parser ("Zclassic" in
// bchain/coins/blockchain.go). zclassicd's chainparams.cpp uses Zcash's
// mainnet pchMessageStart (24 e9 27 64) and base58 prefixes {0x1C,0xB8} (t1)
// and {0x1C,0xBD} (t3), so the Zcash mainnet parameters are ZCL's as well.
// The vectors below are real ZCL mainnet data.

// zclTxHex is ZCL mainnet tx be2ecc14... (block 3302439), a v4 Sapling
// transaction (overwintered, version group 892f2085) with one transparent
// input and two t1 outputs, as returned by the stock explorer's rawtx route.
const zclTxHex = "0400008085202f8901d71d9240af02a693cfcca013468ce7c5b1445a6d156b442f774093077cd9a830000000006a47304402203aeaad02dbee3c03a81322187695cde0cfb97d8b3b025a956193a6a3d140debe022057bca44d8ac444ada343cac10dfebbd0df542512f6f80b314e066bdc1e4db6160121023ff5997ade1914bc1dbc10ce2a76597696e4fe9cf5f2dd609bfda9dc606044d9ffffffff02981cbb43ba0000001976a914b504aa96846cedef567326dbb7d838be06cfbdc388ac00c817a8040000001976a914cffa2d3a89826fec10eaa51e9de24a88e9bb266188ac00000000000000000000000000000000000000"

const zclTxid = "be2ecc141d9fd0d787e9a54f4173ae79dd5f3e0b57ef17c98fb2e444614ef939"

// zclTxJSON is the same transaction in zclassicd's getrawtransaction verbose
// shape, which is what Blockbook parses (it never decodes v4 bytes itself).
const zclTxJSON = `{"hex":"` + zclTxHex + `","txid":"` + zclTxid + `","overwintered":true,"version":4,"versiongroupid":"892f2085","locktime":0,"expiryheight":0,` +
	`"vin":[{"txid":"30a8d97c079340772f446b156d5a44b1c5e78c4613a0cccf93a602af40921dd7","vout":0,"scriptSig":{"hex":"47304402203aeaad02dbee3c03a81322187695cde0cfb97d8b3b025a956193a6a3d140debe022057bca44d8ac444ada343cac10dfebbd0df542512f6f80b314e066bdc1e4db6160121023ff5997ade1914bc1dbc10ce2a76597696e4fe9cf5f2dd609bfda9dc606044d9"},"sequence":4294967295}],` +
	`"vout":[{"value":8000.0025308,"valueSat":800000253080,"n":0,"scriptPubKey":{"hex":"76a914b504aa96846cedef567326dbb7d838be06cfbdc388ac","reqSigs":1,"type":"pubkeyhash","addresses":["t1aNjuHGELsKZUd7b5vfKMTXN2xoAuYoDMc"]}},` +
	`{"value":200.0,"valueSat":20000000000,"n":1,"scriptPubKey":{"hex":"76a914cffa2d3a89826fec10eaa51e9de24a88e9bb266188ac","reqSigs":1,"type":"pubkeyhash","addresses":["t1cqHadBFVctM63bhfRiPbLrGccjD9ckE3d"]}}],` +
	`"vjoinsplit":[],"valueBalance":0.0,"valueBalanceZat":0,"vShieldedSpend":[],"vShieldedOutput":[],"time":1793346557,"blocktime":1793346557}`

// zclCoinbaseJSON is zclassicd's verbatim getrawtransaction output for the
// coinbase cdb49509... of block 3302447.
const zclCoinbaseJSON = `{"hex":"0400008085202f89010000000000000000000000000000000000000000000000000000000000000000ffffffff22032f6432045750e46a08cf9fc5e0000000002f7a706f6f6c2e63612fcf9fc5e02f00ffffffff01e40b5402000000001976a914c97ed8ae27a2c553d19761066e99a53b376e7a2188ac00000000000000000000000000000000000000","txid":"cdb495094a7f9a4cef514ef7e95378c258c03a2b6b4cbdab934d0346ddfef61e","overwintered":true,"version":4,"versiongroupid":"892f2085","locktime":0,"expiryheight":0,"vin":[{"coinbase":"032f6432045750e46a08cf9fc5e0000000002f7a706f6f6c2e63612fcf9fc5e02f00","sequence":4294967295}],"vout":[{"value":0.390625,"valueSat":39062500,"n":0,"scriptPubKey":{"asm":"OP_DUP OP_HASH160 c97ed8ae27a2c553d19761066e99a53b376e7a21 OP_EQUALVERIFY OP_CHECKSIG","hex":"76a914c97ed8ae27a2c553d19761066e99a53b376e7a2188ac","reqSigs":1,"type":"pubkeyhash","addresses":["t1cF1nLTs1Em1xMHn6FrLeUFsWt3aferGs5"]}}],"vjoinsplit":[],"valueBalance":0.0,"valueBalanceZat":0,"vShieldedSpend":[],"vShieldedOutput":[],"blockhash":"000009d3aad365224a7f4343419a990daf5e1f7b4539751b443d4fc80cc6e077","height":3302447,"confirmations":6,"time":1793347671,"blocktime":1793347671}`

func zclParser() *ZCashParser {
	return NewZCashParser(GetChainParams("main"), &btc.Configuration{})
}

func TestZclassic_NetworkParams(t *testing.T) {
	p := GetChainParams("main")
	// pchMessageStart 24 e9 27 64, read little endian
	if p.Net != 0x6427e924 {
		t.Errorf("Net = %#x, want ZCL/ZEC mainnet magic 0x6427e924", uint32(p.Net))
	}
	if !bytes.Equal(p.PubKeyHashAddrID, []byte{0x1C, 0xB8}) || !bytes.Equal(p.ScriptHashAddrID, []byte{0x1C, 0xBD}) || p.AddressMagicLen != 2 {
		t.Errorf("address versions = %x/%x len %d, want 1cb8/1cbd len 2", p.PubKeyHashAddrID, p.ScriptHashAddrID, p.AddressMagicLen)
	}
}

// TestZclassic_RawTxIsReal checks that the fixture hex is the ZCL v4 Sapling
// transaction it claims to be: header, version group and txid.
func TestZclassic_RawTxIsReal(t *testing.T) {
	raw, err := hex.DecodeString(zclTxHex)
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
	if got := hex.EncodeToString(h2[:]); got != zclTxid {
		t.Errorf("txid = %s, want %s", got, zclTxid)
	}
}

func TestZclassic_ParseTxFromJson(t *testing.T) {
	parser := zclParser()
	tests := []struct {
		name      string
		json      string
		txid      string
		values    []int64
		addresses []string
		coinbase  bool
	}{
		{"v4-sapling-t1", zclTxJSON, zclTxid, []int64{800000253080, 20000000000}, []string{"t1aNjuHGELsKZUd7b5vfKMTXN2xoAuYoDMc", "t1cqHadBFVctM63bhfRiPbLrGccjD9ckE3d"}, false},
		{"v4-coinbase", zclCoinbaseJSON, "cdb495094a7f9a4cef514ef7e95378c258c03a2b6b4cbdab934d0346ddfef61e", []int64{39062500}, []string{"t1cF1nLTs1Em1xMHn6FrLeUFsWt3aferGs5"}, true},
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

func TestZclassic_PackUnpackTx(t *testing.T) {
	parser := zclParser()
	tx, err := parser.ParseTxFromJson([]byte(zclTxJSON))
	if err != nil {
		t.Fatal(err)
	}
	tx.Hex = zclTxHex
	tx.Blocktime = tx.Time
	packed, err := parser.PackTx(tx, 3302439, 1793346557)
	if err != nil {
		t.Fatal(err)
	}
	got, height, err := parser.UnpackTx(packed)
	if err != nil {
		t.Fatal(err)
	}
	if height != 3302439 {
		t.Errorf("height = %d", height)
	}
	if got.Txid != zclTxid || got.Hex != zclTxHex || got.Blocktime != 1793346557 || len(got.Vin) != 1 || len(got.Vout) != 2 {
		t.Errorf("unpacked tx = %+v", got)
	}
	if got.Vin[0].Txid != tx.Vin[0].Txid || got.Vout[0].ValueSat.Cmp(&tx.Vout[0].ValueSat) != 0 || got.Vout[1].ScriptPubKey.Hex != tx.Vout[1].ScriptPubKey.Hex {
		t.Errorf("unpacked tx differs: %+v", got)
	}
}

// TestZclassic_AddressRoundTrip checks a t1 (P2PKH) and a t3 (P2SH) address
// both ways: address -> output script and script -> address. The t1 is the
// first output of be2ecc14...; the t3 was encoded independently (base58check
// of 1cbd || hash160) for an arbitrary script hash.
func TestZclassic_AddressRoundTrip(t *testing.T) {
	parser := zclParser()
	tests := []struct {
		address string
		script  string
	}{
		{"t1aNjuHGELsKZUd7b5vfKMTXN2xoAuYoDMc", "76a914b504aa96846cedef567326dbb7d838be06cfbdc388ac"},
		{"t3XakuaGrVm964pUtu4Pt4kGn5dSTbkhrtv", "a9148ed0b5dfd4b4cdb0a5a9f8dbe02b1b03e4b2ccbe87"},
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
