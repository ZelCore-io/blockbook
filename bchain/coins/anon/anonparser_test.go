//go:build unittest

package anon

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/bchain/coins/btc"
)

// The testdata files are anond v.2.2.0-62 mainnet output, captured from the
// daemon behind explorer.anon.zelcore.io:
//   - tx_f868ceb0: the version 1 coinbase of block 1368074 (three P2PKH outputs)
//   - tx_6caa4ad9: a version -3 (Groth16 Sprout) shielding tx of block 1368074,
//     two transparent inputs, no transparent outputs, one JoinSplit
//   - tx_c55dd0b5: an airdrop tx of block 10000, coinbase-style
//   - block_1368074: getblock <hash> true
// The .hex files are getrawtransaction <txid> 0 and hash back to their txids.

const (
	coinbaseTxid = "f868ceb07b83217b548c513362ebd25fd86dc3dd7959261bd056a5759fb89f25"
	sproutTxid   = "6caa4ad9f46a5576318205be2a24654cb5f29037b7d1130b5d0e739f6ea8da1b"
	airdropTxid  = "c55dd0b5f7729a99a71dd5c3f685f7e91e1f4252f50b4eb3d53f5ef209b23450"
	blockHash    = "000101faef080c654702d32c34e0e0ce6de68be32ebbc2985e05a81257f1bef6"
)

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.TrimSpace(b)
}

func anonParser() *AnonParser {
	return NewAnonParser(GetChainParams("main"), &btc.Configuration{})
}

func TestAnon_NetworkParams(t *testing.T) {
	p := GetChainParams("main")
	if p.Net != 0xa747d883 {
		t.Errorf("Net = %#x, want 0xa747d883 (pchMessageStart 83 d8 47 a7)", uint32(p.Net))
	}
	if p.AddressMagicLen != 2 || !bytes.Equal(p.PubKeyHashAddrID, []byte{0x05, 0x82}) || !bytes.Equal(p.ScriptHashAddrID, []byte{0x53, 0x89}) || !bytes.Equal(p.PrivateKeyID, []byte{0x80}) {
		t.Errorf("address versions = %x/%x (len %d) wif %x, want 0582/5389 wif 80", p.PubKeyHashAddrID, p.ScriptHashAddrID, p.AddressMagicLen, p.PrivateKeyID)
	}
	if !bytes.Equal(p.HDPublicKeyID[:], []byte{0x04, 0x88, 0xb2, 0x1e}) {
		t.Errorf("xpub version = %x, want 0488b21e", p.HDPublicKeyID)
	}
	if GetChainParams("test") != p || GetChainParams("regtest") != p {
		t.Error("every chain name must map to the Anon mainnet parameters")
	}
}

func TestAnon_InsightExtension(t *testing.T) {
	if got := anonParser().InsightExtension(); got != "zcash" {
		t.Errorf("InsightExtension() = %q, want zcash", got)
	}
}

func TestAnon_RawTxIsReal(t *testing.T) {
	for name, tt := range map[string]struct{ file, txid, header string }{
		"v1":  {"tx_f868ceb0", coinbaseTxid, "01000000"},
		"v-3": {"tx_6caa4ad9", sproutTxid, "fdffffff"},
	} {
		t.Run(name, func(t *testing.T) {
			hexTx := string(readTestdata(t, tt.file+".hex"))
			raw, err := hex.DecodeString(hexTx)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(hexTx, tt.header) {
				t.Errorf("header = %x, want %s", raw[:4], tt.header)
			}
			h1 := sha256.Sum256(raw)
			h2 := sha256.Sum256(h1[:])
			for i, j := 0, len(h2)-1; i < j; i, j = i+1, j-1 {
				h2[i], h2[j] = h2[j], h2[i]
			}
			if got := hex.EncodeToString(h2[:]); got != tt.txid {
				t.Errorf("txid = %s, want %s", got, tt.txid)
			}
			tx, err := anonParser().ParseTxFromJson(readTestdata(t, tt.file+".json"))
			if err != nil {
				t.Fatal(err)
			}
			if tx.Hex != hexTx {
				t.Error("JSON hex differs from the fixture hex")
			}
		})
	}
}

func TestAnon_ParseTxFromJson(t *testing.T) {
	parser := anonParser()
	tests := []struct {
		name     string
		file     string
		txid     string
		version  int32
		coinbase bool
		vin      int
		vout     []int64
		addrs    []string
	}{
		{"v1 coinbase", "tx_f868ceb0.json", coinbaseTxid, 1, true, 1,
			[]int64{29687500, 259181875, 8015625},
			[]string{"AnWUVqQGK6A51kJ7AC5sThvFwcLA1yhNpFx", "AnMvf8HnX2DF4GTAWhiU1jjTyDsFsWRQRrq", "AnY6ktdhwrHdXoJD12z2mq5Mb1GkLn8E4Xc"}},
		{"v-3 sprout", "tx_6caa4ad9.json", sproutTxid, -3, false, 2, []int64{}, []string{}},
		{"airdrop", "tx_c55dd0b5.json", airdropTxid, 1, true, 1, []int64{26235000}, []string{"AnePfTTXFsXumtG5kJZQa367RscaG7zAwUV"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx, err := parser.ParseTxFromJson(readTestdata(t, tt.file))
			if err != nil {
				t.Fatal(err)
			}
			if tx.Txid != tt.txid || tx.Version != tt.version || len(tx.Vin) != tt.vin || len(tx.Vout) != len(tt.vout) {
				t.Fatalf("tx = %s v%d vin %d vout %d", tx.Txid, tx.Version, len(tx.Vin), len(tx.Vout))
			}
			if got := tx.Vin[0].Coinbase != ""; got != tt.coinbase {
				t.Errorf("coinbase = %v, want %v", got, tt.coinbase)
			}
			for i, want := range tt.vout {
				if tx.Vout[i].ValueSat.Cmp(big.NewInt(want)) != 0 {
					t.Errorf("vout %d value = %s, want %d", i, tx.Vout[i].ValueSat.String(), want)
				}
				desc, err := parser.GetAddrDescFromVout(&tx.Vout[i])
				if err != nil {
					t.Fatal(err)
				}
				addrs, searchable, err := parser.GetAddressesFromAddrDesc(desc)
				if err != nil || !searchable || !reflect.DeepEqual(addrs, []string{tt.addrs[i]}) {
					t.Errorf("vout %d addresses = %v (searchable %v, err %v), want %s", i, addrs, searchable, err, tt.addrs[i])
				}
			}
		})
	}
	// The v-3 tx spends two outputs of AnMvf8Hn...; the inputs are kept.
	tx, _ := parser.ParseTxFromJson(readTestdata(t, "tx_6caa4ad9.json"))
	if tx.Vin[0].Txid != "84bf40449e751dc4e8aa3f2badb0180f2808d7e9b8a42121497b6723a9ec0614" || tx.Vin[0].Vout != 2 ||
		tx.Vin[1].Txid != "5aa86f01ce780b65ae5f9da70454a780fabb48da43cd6306a30e022edc7b8fe7" || tx.Vin[1].Vout != 1 {
		t.Errorf("v-3 inputs = %+v", tx.Vin)
	}
}

// TestAnon_RawHexNotDecoded documents why raw tx hex is never decoded: btcd
// rejects the v-3 tx, while the JSON path above reads it.
func TestAnon_RawHexNotDecoded(t *testing.T) {
	raw, _ := hex.DecodeString(string(readTestdata(t, "tx_6caa4ad9.hex")))
	if tx, err := anonParser().ParseTx(raw); err == nil && len(tx.Vin) == 2 && len(tx.Vout) == 0 && tx.Txid == sproutTxid {
		t.Error("btcd now decodes the v-3 tx; revisit the JSON-only design")
	}
}

// TestAnon_AddressRoundTrip checks the An (P2PKH, 0582) address of the
// coinbase output 0 and a 3Z (P2SH, 5389) address built independently from
// the script hash 6c344290... with base58check.
func TestAnon_AddressRoundTrip(t *testing.T) {
	parser := anonParser()
	tests := []struct {
		address string
		script  string
	}{
		{"AnWUVqQGK6A51kJ7AC5sThvFwcLA1yhNpFx", "76a91461c7794d7c2bb1f9569abc608789e0c0c9df310588ac"},
		{"3ZN3hCux4EXBhMXCAXJKkRran4jz6ebaWRjo", "a9146c34429047366517587a6311df11545b4fa38a5c87"},
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
			addrs, searchable, err := parser.GetAddressesFromAddrDesc(script)
			if err != nil || !searchable || !reflect.DeepEqual(addrs, []string{tt.address}) {
				t.Errorf("addresses = %v (searchable %v, err %v)", addrs, searchable, err)
			}
		})
	}
	// Zcash t1 addresses must not decode with Anon parameters.
	if _, err := parser.GetAddrDescFromAddress("t1Ps5rSpdGcvzkbf3hcBXq8apTdFZGjXRy7"); err == nil {
		t.Error("a Zcash t1 address decoded with Anon parameters")
	}
}

func TestAnon_PackUnpackTx(t *testing.T) {
	parser := anonParser()
	for name, file := range map[string]string{"v-3": "tx_6caa4ad9", "v1": "tx_f868ceb0"} {
		t.Run(name, func(t *testing.T) {
			tx, err := parser.ParseTxFromJson(readTestdata(t, file+".json"))
			if err != nil {
				t.Fatal(err)
			}
			tx.Blocktime = tx.Time
			packed, err := parser.PackTx(tx, 1368074, tx.Time)
			if err != nil {
				t.Fatal(err)
			}
			got, height, err := parser.UnpackTx(packed)
			if err != nil {
				t.Fatal(err)
			}
			if height != 1368074 || got.Txid != tx.Txid || got.Hex != tx.Hex || got.Version != tx.Version || got.Blocktime != tx.Blocktime {
				t.Errorf("unpacked tx = %s v%d height %d", got.Txid, got.Version, height)
			}
			if !reflect.DeepEqual(got.Vin, tx.Vin) || len(got.Vout) != len(tx.Vout) {
				t.Errorf("unpacked tx differs: %+v", got)
			}
			for i := range tx.Vout {
				if got.Vout[i].ValueSat.Cmp(&tx.Vout[i].ValueSat) != 0 || got.Vout[i].ScriptPubKey.Hex != tx.Vout[i].ScriptPubKey.Hex {
					t.Errorf("vout %d differs: %+v", i, got.Vout[i])
				}
			}
		})
	}
}

var _ bchain.BlockChainParser = (*AnonParser)(nil)
