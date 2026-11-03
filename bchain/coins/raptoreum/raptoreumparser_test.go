//go:build unittest

package raptoreum

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/martinboehm/btcutil/chaincfg"
	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/bchain/coins/btc"
)

// Raptoreum (RTM) is served by the Dash parser with Raptoreum's own network
// parameters (chainparams.cpp of Raptor3um/raptoreum, tag 2.0.3.01-mainnet).
// The testdata are real mainnet answers of raptoreumd 2.0.3.1, with the
// changing "confirmations" fields removed:
//
//   - tx_ccc31c7a.json: getrawtransaction ccc31c7a... 1, a MINT_ASSET (type 10)
//     transaction of block 1101722 with an asset transfer output.
//   - tx_1366dc99.json: getrawtransaction 1366dc99... 1, a FUTURE (type 7)
//     transaction of block 1420444 (maturity 100, lockTime 100, output 0).
//   - block_1420444.json: getblock 24ed891b... 2, the block of that future:
//     a CbTx coinbase (type 5), the future and an ordinary transaction.

const (
	mintTxid   = "ccc31c7aac752dee7481cae08454d67fb7a57061cc14fbbe707b666cb61db10c"
	futureTxid = "1366dc991927541013509184d3ad5b554e2789c60b8456d05357a2d50e297d3f"
	blockHash  = "24ed891bb4c3196f9bda735a0fc686dc2e2431926529d6cfa5422c5688683dbe"
	blockPrev  = "144a0fd08ee6e7c67f85b1897c83eaa0edaeaa1c47164f402d81fd089be55dce"
	cbTxid     = "1a4a1a7f8ada29dfed341eb1dd4e959786a77a534d27a54d3e60e899795e9b2c"
	plainTxid  = "db620a703e89bcb9c5a1691bc88915e31ca65ebac43db6ff5ad72ed585d7882d"
)

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func testTxHex(t *testing.T, name string) string {
	t.Helper()
	var v struct {
		Hex string `json:"hex"`
	}
	if err := json.Unmarshal(readTestdata(t, name), &v); err != nil {
		t.Fatal(err)
	}
	return v.Hex
}

func rtmParser() *RaptoreumParser {
	return NewRaptoreumParser(GetChainParams("main"), &btc.Configuration{})
}

func binaryBE(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func TestRaptoreum_NetworkParams(t *testing.T) {
	m := GetChainParams("main")
	// pchMessageStart 72 74 6d 2e, read little endian
	if m.Net != 0x2e6d7472 {
		t.Errorf("mainnet Net = %#x, want 0x2e6d7472", uint32(m.Net))
	}
	// base58Prefixes PUBKEY_ADDRESS 60 ('R'), SCRIPT_ADDRESS 16 ('7')
	if !bytes.Equal(m.PubKeyHashAddrID, []byte{60}) || !bytes.Equal(m.ScriptHashAddrID, []byte{16}) {
		t.Errorf("mainnet address versions = %v/%v, want 60/16", m.PubKeyHashAddrID, m.ScriptHashAddrID)
	}
	// EXT_PUBLIC_KEY 04 88 B2 1E (xpub), EXT_SECRET_KEY 04 88 AD E4 (xprv)
	if !bytes.Equal(m.HDPublicKeyID[:], []byte{0x04, 0x88, 0xB2, 0x1E}) || !bytes.Equal(m.HDPrivateKeyID[:], []byte{0x04, 0x88, 0xAD, 0xE4}) {
		t.Errorf("mainnet HD versions = %x/%x", m.HDPublicKeyID, m.HDPrivateKeyID)
	}
	if binaryBE(m.HDPublicKeyID[:]) != 76067358 {
		t.Errorf("xpub magic = %d, want 76067358 (configs/coins/raptoreum.json xpub_magic)", binaryBE(m.HDPublicKeyID[:]))
	}
	tn := GetChainParams("test")
	// pchMessageStart 74 72 74 6d; PUBKEY_ADDRESS 123, SCRIPT_ADDRESS 19
	if tn.Net != 0x6d747274 || !bytes.Equal(tn.PubKeyHashAddrID, []byte{123}) || !bytes.Equal(tn.ScriptHashAddrID, []byte{19}) {
		t.Errorf("testnet params = %#x %v %v", uint32(tn.Net), tn.PubKeyHashAddrID, tn.ScriptHashAddrID)
	}
	if !chaincfg.IsRegistered(m) || !chaincfg.IsRegistered(tn) {
		t.Error("Raptoreum networks are not registered")
	}
}

// TestRaptoreum_RawTxIsReal checks that the fixture hex hashes back to the txid
// (sha256d over the whole transaction, extra payload included) and carries the
// DIP2 header of its special type.
func TestRaptoreum_RawTxIsReal(t *testing.T) {
	for name, tt := range map[string]struct {
		file, txid string
		header     []byte
	}{
		"mint":   {"tx_ccc31c7a.json", mintTxid, []byte{0x03, 0x00, 0x0a, 0x00}},
		"future": {"tx_1366dc99.json", futureTxid, []byte{0x03, 0x00, 0x07, 0x00}},
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := hex.DecodeString(testTxHex(t, tt.file))
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
			if !bytes.HasPrefix(raw, tt.header) {
				t.Errorf("header = %x, want %x", raw[:4], tt.header)
			}
		})
	}
}

func TestRaptoreum_ParseTxFromJson(t *testing.T) {
	parser := rtmParser()
	tests := []struct {
		name       string
		file       string
		txid       string
		vin        int
		values     []int64
		addresses  [][]string
		searchable []bool
	}{
		// Output 0 is a P2PKH script followed by OP_ASSET_ID (0xbc) and the
		// asset transfer; its RTM value is 0. Its descriptor is the whole
		// script, as ElectrumX indexes it, so it is not an address of the owner.
		{"mint-asset-type10", "tx_ccc31c7a.json", mintTxid, 1, []int64{0, 499999929000},
			[][]string{{}, {"RCFEV1U8e4QsM3dvcjmiL9zBjha387Q6uo"}}, []bool{false, true}},
		{"future-type7", "tx_1366dc99.json", futureTxid, 2, []int64{108359256859490},
			[][]string{{"RHMagXBeMGna229N3sNuLY3Uh8WumEKe1X"}}, []bool{true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx, err := parser.ParseTxFromJson(readTestdata(t, tt.file))
			if err != nil {
				t.Fatal(err)
			}
			if tx.Txid != tt.txid || tx.Version != 3 || len(tx.Vin) != tt.vin {
				t.Errorf("txid/version/vin = %s/%d/%d", tx.Txid, tx.Version, len(tx.Vin))
			}
			if len(tx.Vout) != len(tt.values) {
				t.Fatalf("len(vout) = %d, want %d", len(tx.Vout), len(tt.values))
			}
			for i := range tx.Vout {
				if tx.Vout[i].ValueSat.Cmp(big.NewInt(tt.values[i])) != 0 {
					t.Errorf("vout %d value = %s, want %d", i, tx.Vout[i].ValueSat.String(), tt.values[i])
				}
				desc, err := parser.GetAddrDescFromVout(&tx.Vout[i])
				if err != nil {
					t.Fatal(err)
				}
				if hex.EncodeToString(desc) != tx.Vout[i].ScriptPubKey.Hex {
					t.Errorf("vout %d descriptor is not the output script", i)
				}
				addrs, searchable, err := parser.GetAddressesFromAddrDesc(desc)
				if err != nil {
					t.Fatal(err)
				}
				if len(addrs) == 0 {
					addrs = []string{}
				}
				if !reflect.DeepEqual(addrs, tt.addresses[i]) || searchable != tt.searchable[i] {
					t.Errorf("vout %d addresses = %v (searchable %v), want %v (%v)", i, addrs, searchable, tt.addresses[i], tt.searchable[i])
				}
			}
		})
	}
}

func TestRaptoreum_PackUnpackTx(t *testing.T) {
	parser := rtmParser()
	for name, tt := range map[string]struct {
		file, txid string
		height     uint32
		time       int64
	}{
		"mint":   {"tx_ccc31c7a.json", mintTxid, 1101722, 1749233400},
		"future": {"tx_1366dc99.json", futureTxid, 1420444, 1788268218},
	} {
		t.Run(name, func(t *testing.T) {
			tx, err := parser.ParseTxFromJson(readTestdata(t, tt.file))
			if err != nil {
				t.Fatal(err)
			}
			if tx.Hex != testTxHex(t, tt.file) {
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
			if height != tt.height || got.Txid != tt.txid || got.Hex != tx.Hex || got.Blocktime != tt.time || got.Version != 3 || len(got.Vin) != len(tx.Vin) || len(got.Vout) != len(tx.Vout) {
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

// TestRaptoreum_AddressRoundTrip checks an R (P2PKH) address both ways.
func TestRaptoreum_AddressRoundTrip(t *testing.T) {
	parser := rtmParser()
	const addr, script = "RHMagXBeMGna229N3sNuLY3Uh8WumEKe1X", "76a914589589e1b1755aa9e9a6ef16e171fba7e3211f5288ac"
	desc, err := parser.GetAddrDescFromAddress(addr)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(desc); got != script {
		t.Errorf("script = %s, want %s", got, script)
	}
	addrs, searchable, err := parser.GetAddressesFromAddrDesc(desc)
	if err != nil || !searchable || !reflect.DeepEqual(addrs, []string{addr}) {
		t.Errorf("addresses = %v (searchable %v, err %v)", addrs, searchable, err)
	}
	// A Dash address must not decode with Raptoreum's version bytes.
	if _, err := parser.GetAddrDescFromAddress("XkycBX1ykVXXs92pAi6ZQwZPEre9kSHHKH"); err == nil {
		t.Error("a Dash X... address was accepted as a Raptoreum address")
	}
	var _ bchain.BlockChainParser = parser
}
