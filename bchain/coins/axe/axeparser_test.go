//go:build unittest

package axe

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"reflect"
	"testing"

	"github.com/martinboehm/btcutil/chaincfg"
	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/bchain/coins/btc"
)

// Axe (AXE) is served by the Dash parser with Axe's own network parameters
// (chainparams.cpp of AXErunners/axe, tag v1.6.1.1). The transactions below are
// real Axe mainnet data from axed 1.6.1.1 (getrawtransaction verbose), which is
// what Blockbook parses.

// axeTxHex is Axe mainnet tx 525fddae... (block 5005718): two P2PKH inputs, a
// P2SH output, an OP_RETURN output and a P2PKH change output.
const axeTxHex = "0100000002c3432539f3aadbc5b52e472883e1978c68e227e4f18bfeb7c939726f5f4744d3000000006a47304402207229fe2d3fc9fbf4fe47ce0ce0d774c6a2461decc11ac19824b3af554c917ffa0220503a48783cbfe80f8590e7abfdbd2be19ecc5a00ced2644e8fd75ea0dc04d8f4012102c24472bb6d3cd3fb245881eddd3638b5ae5303563ef6d80d324d3e449df70e15ffffffff6b0ef581acf2ca798b895eb2753ea85355125b4882b552bedab45cc25f32ffaf020000006a473044022038f7c6c513d1c98b23346766cb575ea01931f755a9cd375f9944868461824daf0220208c362465c3e5170d872d8356ea4676c4de808e043366f2240c16776ffacfcc012102c24472bb6d3cd3fb245881eddd3638b5ae5303563ef6d80d324d3e449df70e15ffffffff0320ebaf190000000017a9143c51458e81c81fe68dc2fe8ef3e60b06d696c812870000000000000000166a14157a960a0f4fe4fa84696d23af164cb5f6d1d3506ce762115a0000001976a914f76629c972e4e80b44f7dbe0dcbf19539a2c57bc88ac4359df6a"

const axeTxid = "525fddaecabc5a696f68d36af2d24d5f514aaa49b0a785c0fc51cc641b3dd025"

const axeTxJSON = `{"txid":"525fddaecabc5a696f68d36af2d24d5f514aaa49b0a785c0fc51cc641b3dd025","version":1,"type":0,"size":401,"locktime":1793022275,"vin":[{"txid":"d344475f6f7239c9b7fe8bf1e427e2688c97e18328472eb5c5dbaaf3392543c3","vout":0,"scriptSig":{"asm":"304402207229fe2d3fc9fbf4fe47ce0ce0d774c6a2461decc11ac19824b3af554c917ffa0220503a48783cbfe80f8590e7abfdbd2be19ecc5a00ced2644e8fd75ea0dc04d8f4[ALL] 02c24472bb6d3cd3fb245881eddd3638b5ae5303563ef6d80d324d3e449df70e15","hex":"47304402207229fe2d3fc9fbf4fe47ce0ce0d774c6a2461decc11ac19824b3af554c917ffa0220503a48783cbfe80f8590e7abfdbd2be19ecc5a00ced2644e8fd75ea0dc04d8f4012102c24472bb6d3cd3fb245881eddd3638b5ae5303563ef6d80d324d3e449df70e15"},"value":4.2561844,"valueSat":425618440,"address":"PX9JSnFEkacFiSeEkj53MatEMfwUvnSQtz","sequence":4294967295},{"txid":"afff325fc25cb4dabe52b582485b125553a83e75b25e898b79caf2ac81f50e6b","vout":2,"scriptSig":{"asm":"3044022038f7c6c513d1c98b23346766cb575ea01931f755a9cd375f9944868461824daf0220208c362465c3e5170d872d8356ea4676c4de808e043366f2240c16776ffacfcc[ALL] 02c24472bb6d3cd3fb245881eddd3638b5ae5303563ef6d80d324d3e449df70e15","hex":"473044022038f7c6c513d1c98b23346766cb575ea01931f755a9cd375f9944868461824daf0220208c362465c3e5170d872d8356ea4676c4de808e043366f2240c16776ffacfcc012102c24472bb6d3cd3fb245881eddd3638b5ae5303563ef6d80d324d3e449df70e15"},"value":3868.44096066,"valueSat":386844096066,"address":"PX9JSnFEkacFiSeEkj53MatEMfwUvnSQtz","sequence":4294967295}],"vout":[{"value":4.30959392,"valueSat":430959392,"n":0,"scriptPubKey":{"asm":"OP_HASH160 3c51458e81c81fe68dc2fe8ef3e60b06d696c812 OP_EQUAL","hex":"a9143c51458e81c81fe68dc2fe8ef3e60b06d696c81287","reqSigs":1,"type":"scripthash","addresses":["7Xuaf73yuDF4qpnWwboS5S9nioXqMAg7tM"]},"spentTxId":"72045561f5527934c33e4df3326c95d26daacf40d8c5308d600afa82c7fa95e5","spentIndex":0,"spentHeight":5005745},{"value":0.0,"valueSat":0,"n":1,"scriptPubKey":{"asm":"OP_RETURN 157a960a0f4fe4fa84696d23af164cb5f6d1d350","hex":"6a14157a960a0f4fe4fa84696d23af164cb5f6d1d350","type":"nulldata"}},{"value":3868.38751084,"valueSat":386838751084,"n":2,"scriptPubKey":{"asm":"OP_DUP OP_HASH160 f76629c972e4e80b44f7dbe0dcbf19539a2c57bc OP_EQUALVERIFY OP_CHECKSIG","hex":"76a914f76629c972e4e80b44f7dbe0dcbf19539a2c57bc88ac","reqSigs":1,"type":"pubkeyhash","addresses":["PX9JSnFEkacFiSeEkj53MatEMfwUvnSQtz"]}}],"hex":"0100000002c3432539f3aadbc5b52e472883e1978c68e227e4f18bfeb7c939726f5f4744d3000000006a47304402207229fe2d3fc9fbf4fe47ce0ce0d774c6a2461decc11ac19824b3af554c917ffa0220503a48783cbfe80f8590e7abfdbd2be19ecc5a00ced2644e8fd75ea0dc04d8f4012102c24472bb6d3cd3fb245881eddd3638b5ae5303563ef6d80d324d3e449df70e15ffffffff6b0ef581acf2ca798b895eb2753ea85355125b4882b552bedab45cc25f32ffaf020000006a473044022038f7c6c513d1c98b23346766cb575ea01931f755a9cd375f9944868461824daf0220208c362465c3e5170d872d8356ea4676c4de808e043366f2240c16776ffacfcc012102c24472bb6d3cd3fb245881eddd3638b5ae5303563ef6d80d324d3e449df70e15ffffffff0320ebaf190000000017a9143c51458e81c81fe68dc2fe8ef3e60b06d696c812870000000000000000166a14157a960a0f4fe4fa84696d23af164cb5f6d1d3506ce762115a0000001976a914f76629c972e4e80b44f7dbe0dcbf19539a2c57bc88ac4359df6a","blockhash":"0000000009e10805320e2d2c65af3b2ef56b928d5d09a2764f590e28491e3b4a","height":5005718,"confirmations":2343,"time":1793023546,"blocktime":1793023546,"instantlock":false,"instantlock_internal":false,"chainlock":false}`

// axeCoinbaseHex is the coinbase 'f47321c0...' of block 5008000: a version 3
// special transaction of type 5 (CbTx, DIP4) with a 70 byte extra payload.
const axeCoinbaseHex = "03000500010000000000000000000000000000000000000000000000000000000000000000ffffffff1c03806a4c04b9c2e46a08810000a75d0a0000626f777365726c616200000000000269c5e00c000000001976a91411b30353d8bf69350dc498b89569ab9f1335b00088acf1d89508000000001976a9141025bc511a73ecf9bf023830edf6be47bdd085be88ac00000000460200806a4c00ce7938294bd0fb1321b8f0fc83f6c9575fe790f99bd52897931c4c1351f16c0931ed962a18564be031d8fc6b84eaf62c135c08c6edb74e42d50b04c7bb944d23"

const axeCoinbaseTxid = "f47321c0964044a8b46f41ed9d0127052d8c0e671be63ff69dfc51b233ce6d08"

const axeCoinbaseJSON = `{"txid":"f47321c0964044a8b46f41ed9d0127052d8c0e671be63ff69dfc51b233ce6d08","version":3,"type":5,"size":218,"locktime":0,"vin":[{"coinbase":"03806a4c04b9c2e46a08810000a75d0a0000626f777365726c616200","sequence":0}],"vout":[{"value":2.16057193,"valueSat":216057193,"n":0,"scriptPubKey":{"asm":"OP_DUP OP_HASH160 11b30353d8bf69350dc498b89569ab9f1335b000 OP_EQUALVERIFY OP_CHECKSIG","hex":"76a91411b30353d8bf69350dc498b89569ab9f1335b00088ac","reqSigs":1,"type":"pubkeyhash","addresses":["PACm6Gviu6sPfbqK62M6T95AyknzY3mPkq"]}},{"value":1.44038129,"valueSat":144038129,"n":1,"scriptPubKey":{"asm":"OP_DUP OP_HASH160 1025bc511a73ecf9bf023830edf6be47bdd085be OP_EQUALVERIFY OP_CHECKSIG","hex":"76a9141025bc511a73ecf9bf023830edf6be47bdd085be88ac","reqSigs":1,"type":"pubkeyhash","addresses":["PA4ZAxZSctGCM3znU1Y6oAvKRdK38TjsdJ"]}}],"extraPayloadSize":70,"extraPayload":"0200806a4c00ce7938294bd0fb1321b8f0fc83f6c9575fe790f99bd52897931c4c1351f16c0931ed962a18564be031d8fc6b84eaf62c135c08c6edb74e42d50b04c7bb944d23","cbTx":{"version":2,"height":5008000,"merkleRootMNList":"096cf151134c1c939728d59bf990e75f57c9f683fcf0b82113fbd04b293879ce","merkleRootQuorums":"234d94bbc7040bd5424eb7edc6085c132cf6ea846bfcd831e04b56182a96ed31"},"hex":"03000500010000000000000000000000000000000000000000000000000000000000000000ffffffff1c03806a4c04b9c2e46a08810000a75d0a0000626f777365726c616200000000000269c5e00c000000001976a91411b30353d8bf69350dc498b89569ab9f1335b00088acf1d89508000000001976a9141025bc511a73ecf9bf023830edf6be47bdd085be88ac00000000460200806a4c00ce7938294bd0fb1321b8f0fc83f6c9575fe790f99bd52897931c4c1351f16c0931ed962a18564be031d8fc6b84eaf62c135c08c6edb74e42d50b04c7bb944d23","blockhash":"0000000000a8f1a5b0604750eb3f7fa1a883533fbe111a82e3c33a0ea4dee638","height":5008000,"confirmations":61,"time":1793376953,"blocktime":1793376953,"instantlock":false,"instantlock_internal":false,"chainlock":false}`

func axeParser() *AxeParser {
	return NewAxeParser(GetChainParams("main"), &btc.Configuration{})
}

func TestAxe_NetworkParams(t *testing.T) {
	m := GetChainParams("main")
	// pchMessageStart b5 ce 6b 04, read little endian
	if m.Net != 0x046bceb5 {
		t.Errorf("mainnet Net = %#x, want 0x046bceb5", uint32(m.Net))
	}
	// base58Prefixes PUBKEY_ADDRESS 55 ('P'), SCRIPT_ADDRESS 16 ('7')
	if !bytes.Equal(m.PubKeyHashAddrID, []byte{55}) || !bytes.Equal(m.ScriptHashAddrID, []byte{16}) {
		t.Errorf("mainnet address versions = %v/%v, want 55/16", m.PubKeyHashAddrID, m.ScriptHashAddrID)
	}
	// EXT_PUBLIC_KEY 04 88 B2 1E (xpub), EXT_SECRET_KEY 04 88 AD E4 (xprv)
	if !bytes.Equal(m.HDPublicKeyID[:], []byte{0x04, 0x88, 0xB2, 0x1E}) || !bytes.Equal(m.HDPrivateKeyID[:], []byte{0x04, 0x88, 0xAD, 0xE4}) {
		t.Errorf("mainnet HD versions = %x/%x", m.HDPublicKeyID, m.HDPrivateKeyID)
	}
	if uint32(binaryBE(m.HDPublicKeyID[:])) != 76067358 {
		t.Errorf("xpub magic = %d, want 76067358 (configs/coins/axe.json xpub_magic)", binaryBE(m.HDPublicKeyID[:]))
	}
	tn := GetChainParams("test")
	if tn.Net != 0xf1cae2ce || !bytes.Equal(tn.PubKeyHashAddrID, []byte{140}) || !bytes.Equal(tn.ScriptHashAddrID, []byte{19}) {
		t.Errorf("testnet params = %#x %v %v", uint32(tn.Net), tn.PubKeyHashAddrID, tn.ScriptHashAddrID)
	}
	if !chaincfg.IsRegistered(m) || !chaincfg.IsRegistered(tn) {
		t.Error("Axe networks are not registered")
	}
}

func binaryBE(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// TestAxe_RawTxIsReal checks that the fixture hex hashes back to the txid, for
// the ordinary transaction and for the special coinbase (extra payload included).
func TestAxe_RawTxIsReal(t *testing.T) {
	for name, tt := range map[string]struct{ hex, txid string }{
		"tx":       {axeTxHex, axeTxid},
		"coinbase": {axeCoinbaseHex, axeCoinbaseTxid},
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := hex.DecodeString(tt.hex)
			if err != nil {
				t.Fatal(err)
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
	// version 3, type 5 (CbTx): little endian 4 byte header 03000500
	if cb, _ := hex.DecodeString(axeCoinbaseHex); !bytes.HasPrefix(cb, []byte{0x03, 0x00, 0x05, 0x00}) {
		t.Errorf("coinbase header = %x, want version 3 type 5", cb[:4])
	}
}

func TestAxe_ParseTxFromJson(t *testing.T) {
	parser := axeParser()
	tests := []struct {
		name      string
		json      string
		txid      string
		version   int32
		vin       int
		coinbase  bool
		values    []int64
		addresses [][]string
	}{
		{"p2pkh-p2sh-opreturn", axeTxJSON, axeTxid, 1, 2, false, []int64{430959392, 0, 386838751084},
			[][]string{{"7Xuaf73yuDF4qpnWwboS5S9nioXqMAg7tM"}, {"OP_RETURN 157a960a0f4fe4fa84696d23af164cb5f6d1d350"}, {"PX9JSnFEkacFiSeEkj53MatEMfwUvnSQtz"}}},
		{"cbtx-v3-type5", axeCoinbaseJSON, axeCoinbaseTxid, 3, 1, true, []int64{216057193, 144038129},
			[][]string{{"PACm6Gviu6sPfbqK62M6T95AyknzY3mPkq"}, {"PA4ZAxZSctGCM3znU1Y6oAvKRdK38TjsdJ"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx, err := parser.ParseTxFromJson([]byte(tt.json))
			if err != nil {
				t.Fatal(err)
			}
			if tx.Txid != tt.txid || tx.Version != tt.version {
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
				addrs, _, err := parser.GetAddressesFromAddrDesc(desc)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(addrs, tt.addresses[i]) {
					t.Errorf("vout %d addresses = %v, want %v", i, addrs, tt.addresses[i])
				}
			}
		})
	}
}

func TestAxe_ParseTxFromJsonInputs(t *testing.T) {
	tx, err := axeParser().ParseTxFromJson([]byte(axeTxJSON))
	if err != nil {
		t.Fatal(err)
	}
	if tx.LockTime != 1793022275 || tx.Blocktime != 1793023546 {
		t.Errorf("locktime/blocktime = %d/%d", tx.LockTime, tx.Blocktime)
	}
	if tx.Vin[0].Txid != "d344475f6f7239c9b7fe8bf1e427e2688c97e18328472eb5c5dbaaf3392543c3" || tx.Vin[0].Vout != 0 ||
		tx.Vin[1].Txid != "afff325fc25cb4dabe52b582485b125553a83e75b25e898b79caf2ac81f50e6b" || tx.Vin[1].Vout != 2 {
		t.Errorf("vin outpoints = %+v", tx.Vin)
	}
}

func TestAxe_PackUnpackTx(t *testing.T) {
	parser := axeParser()
	for name, tt := range map[string]struct {
		json, hex, txid string
		height          uint32
		time            int64
	}{
		"tx":       {axeTxJSON, axeTxHex, axeTxid, 5005718, 1793023546},
		"coinbase": {axeCoinbaseJSON, axeCoinbaseHex, axeCoinbaseTxid, 5008000, 1793376953},
	} {
		t.Run(name, func(t *testing.T) {
			tx, err := parser.ParseTxFromJson([]byte(tt.json))
			if err != nil {
				t.Fatal(err)
			}
			if tx.Hex != tt.hex {
				t.Errorf("hex differs from the fixture")
			}
			packed, err := parser.PackTx(tx, tt.height, tt.time)
			if err != nil {
				t.Fatal(err)
			}
			got, height, err := parser.UnpackTx(packed)
			if err != nil {
				t.Fatal(err)
			}
			if height != tt.height || got.Txid != tt.txid || got.Hex != tt.hex || got.Blocktime != tt.time || len(got.Vin) != len(tx.Vin) || len(got.Vout) != len(tx.Vout) {
				t.Errorf("unpacked tx = %+v (height %d)", got, height)
			}
			for i := range tx.Vout {
				if got.Vout[i].ValueSat.Cmp(&tx.Vout[i].ValueSat) != 0 || got.Vout[i].ScriptPubKey.Hex != tx.Vout[i].ScriptPubKey.Hex {
					t.Errorf("vout %d differs: %+v", i, got.Vout[i])
				}
			}
		})
	}
}

// TestAxe_AddressRoundTrip checks a P (P2PKH) and a 7 (P2SH) address both
// ways: address -> output script and script -> address. Both are outputs of
// tx 525fddae... (block 5005718).
func TestAxe_AddressRoundTrip(t *testing.T) {
	parser := axeParser()
	tests := []struct {
		address string
		script  string
	}{
		{"PX9JSnFEkacFiSeEkj53MatEMfwUvnSQtz", "76a914f76629c972e4e80b44f7dbe0dcbf19539a2c57bc88ac"},
		{"7Xuaf73yuDF4qpnWwboS5S9nioXqMAg7tM", "a9143c51458e81c81fe68dc2fe8ef3e60b06d696c81287"},
	}
	for _, tt := range tests {
		t.Run(tt.address[:1], func(t *testing.T) {
			desc, err := parser.GetAddrDescFromAddress(tt.address)
			if err != nil {
				t.Fatal(err)
			}
			if got := hex.EncodeToString(desc); got != tt.script {
				t.Errorf("script = %s, want %s", got, tt.script)
			}
			script, _ := hex.DecodeString(tt.script)
			addrs, searchable, err := parser.GetAddressesFromAddrDesc(script)
			if err != nil || !searchable || !reflect.DeepEqual(addrs, []string{tt.address}) {
				t.Errorf("addresses = %v (searchable %v, err %v)", addrs, searchable, err)
			}
		})
	}
	// A Dash address must not decode with Axe's version bytes.
	if _, err := parser.GetAddrDescFromAddress("XkycBX1ykVXXs92pAi6ZQwZPEre9kSHHKH"); err == nil {
		t.Error("a Dash X... address was accepted as an Axe address")
	}
}

func TestAxe_FirstBlockWithSpecialTransactions(t *testing.T) {
	if firstBlockWithSpecialTransactions != 213696 {
		t.Errorf("firstBlockWithSpecialTransactions = %d, want DIP0003Height 213696", firstBlockWithSpecialTransactions)
	}
	var _ bchain.BlockChainParser = axeParser()
}
