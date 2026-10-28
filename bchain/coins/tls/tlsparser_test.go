//go:build unittest

package tls

import (
	"encoding/hex"
	"math/big"
	"os"
	"reflect"
	"testing"

	"github.com/martinboehm/btcutil/chaincfg"
	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/bchain/coins/btc"
)

func TestMain(m *testing.M) {
	c := m.Run()
	chaincfg.ResetParams()
	os.Exit(c)
}

func Test_GetAddrDescFromAddress_Mainnet(t *testing.T) {
	type args struct {
		address string
	}
	tests := []struct {
		name    string
		args    args
		want    string
		wantErr bool
	}{
		{
			name:    "P2PKH1",
			args:    args{address: "ToqGr48ZCNz9tm5YDesWQFWL5QhiyjCPGF"},
			want:    "76a9149f54a959f7a885b13b9c454f14da59d0c3000c1288ac",
			wantErr: false,
		},
		{
			name:    "P2PKH2",
			args:    args{address: "Tg4xc77QGXxRykhscqJp2dPQFFjrLBVfac"},
			want:    "76a9144a2a40987c74578ee517d426aa2c43fc568f7e0888ac",
			wantErr: false,
		},
		{
			name:    "P2SH1",
			args:    args{address: "tDgkfjHyaYDssCDA7RdFcGzPe2TPhkRGRn"},
			want:    "a9144a2a40987c74578ee517d426aa2c43fc568f7e0887",
			wantErr: false,
		},
		{
			name:    "P2SH2",
			args:    args{address: "tEgHaK4CU3ZyJPPWx2BGGjpT2smVuM8xQC"},
			want:    "a914550bc2fcc1992afade4d298326ee6a03ab975a9387",
			wantErr: false,
		},
	}
	parser := NewTLSParser(GetChainParams("main"), &btc.Configuration{})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parser.GetAddrDescFromAddress(tt.args.address)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetAddrDescFromAddress() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			h := hex.EncodeToString(got)
			if !reflect.DeepEqual(h, tt.want) {
				t.Errorf("GetAddrDescFromAddress() = %v, want %v", h, tt.want)
			}
		})
	}
}

// testTx1 is mainnet transaction 099af7cd53f890530e7d55d31118b9f36a2b3ed29505527d6930132f8902fa78 (block 1166741).
var (
	testTx1       bchain.Tx
	testTxPacked1 = "0a20099af7cd53f890530e7d55d31118b9f36a2b3ed29505527d6930132f8902fa7812c00102000000010970ada6087ace57946719479f1bfdd16172fea01bc9c38f3994013c855275c3000000006b483045022100c364415890511ff6766d9ce7896b3130ac3fee88295edcd2671cdd074228acb902202d95913a214d80fdfbe6a5c9ce0ddc1cefe867e02e0ae2a0ea43afd803553ccd0121025a410c199512aab1b77bf3c118a304dfc22827b402f113e0651400c612514a33ffffffff01a106121f5d0000001976a9149f54a959f7a885b13b9c454f14da59d0c3000c1288ac00000000189fdd87d70628959b473295011220c37552853c0194398fc3c91ba0fe7261d1fd1b9f4719679457ce7a08a6ad7009226b483045022100c364415890511ff6766d9ce7896b3130ac3fee88295edcd2671cdd074228acb902202d95913a214d80fdfbe6a5c9ce0ddc1cefe867e02e0ae2a0ea43afd803553ccd0121025a410c199512aab1b77bf3c118a304dfc22827b402f113e0651400c612514a3328ffffffff0f3a460a055d1f1206a11a1976a9149f54a959f7a885b13b9c454f14da59d0c3000c1288ac2222546f71477234385a434e7a39746d3559446573575146574c35516869796a435047464002"
)

func init() {
	testTx1 = bchain.Tx{
		Hex:       "02000000010970ada6087ace57946719479f1bfdd16172fea01bc9c38f3994013c855275c3000000006b483045022100c364415890511ff6766d9ce7896b3130ac3fee88295edcd2671cdd074228acb902202d95913a214d80fdfbe6a5c9ce0ddc1cefe867e02e0ae2a0ea43afd803553ccd0121025a410c199512aab1b77bf3c118a304dfc22827b402f113e0651400c612514a33ffffffff01a106121f5d0000001976a9149f54a959f7a885b13b9c454f14da59d0c3000c1288ac00000000",
		Blocktime: 1793191583,
		Time:      1793191583,
		Txid:      "099af7cd53f890530e7d55d31118b9f36a2b3ed29505527d6930132f8902fa78",
		LockTime:  0,
		Version:   2,
		Vin: []bchain.Vin{
			{
				ScriptSig: bchain.ScriptSig{
					Hex: "483045022100c364415890511ff6766d9ce7896b3130ac3fee88295edcd2671cdd074228acb902202d95913a214d80fdfbe6a5c9ce0ddc1cefe867e02e0ae2a0ea43afd803553ccd0121025a410c199512aab1b77bf3c118a304dfc22827b402f113e0651400c612514a33",
				},
				Txid:     "c37552853c0194398fc3c91ba0fe7261d1fd1b9f4719679457ce7a08a6ad7009",
				Vout:     0,
				Sequence: 4294967295,
			},
		},
		Vout: []bchain.Vout{
			{
				ValueSat: *big.NewInt(399953233569),
				N:        0,
				ScriptPubKey: bchain.ScriptPubKey{
					Hex: "76a9149f54a959f7a885b13b9c454f14da59d0c3000c1288ac",
					Addresses: []string{
						"ToqGr48ZCNz9tm5YDesWQFWL5QhiyjCPGF",
					},
				},
			},
		},
	}
}

func Test_ParseTx(t *testing.T) {
	parser := NewTLSParser(GetChainParams("main"), &btc.Configuration{})
	b, _ := hex.DecodeString(testTx1.Hex)
	got, err := parser.ParseTx(b)
	if err != nil {
		t.Fatalf("ParseTx() error = %v", err)
	}
	if got.Txid != testTx1.Txid {
		t.Errorf("ParseTx() txid = %v, want %v", got.Txid, testTx1.Txid)
	}
	if len(got.Vout) != len(testTx1.Vout) {
		t.Fatalf("ParseTx() vouts = %d, want %d", len(got.Vout), len(testTx1.Vout))
	}
	for i, vout := range got.Vout {
		want := testTx1.Vout[i]
		if vout.ValueSat.Cmp(&want.ValueSat) != 0 || vout.ScriptPubKey.Hex != want.ScriptPubKey.Hex {
			t.Errorf("ParseTx() vout %d = %v %v, want %v %v", i, vout.ValueSat.String(), vout.ScriptPubKey.Hex, want.ValueSat.String(), want.ScriptPubKey.Hex)
		}
		addrs, _, err := parser.GetAddressesFromAddrDesc(mustDecodeHex(t, vout.ScriptPubKey.Hex))
		if err != nil || !reflect.DeepEqual(addrs, want.ScriptPubKey.Addresses) {
			t.Errorf("GetAddressesFromAddrDesc() vout %d = %v (%v), want %v", i, addrs, err, want.ScriptPubKey.Addresses)
		}
	}
}

func mustDecodeHex(t *testing.T, s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func Test_PackTx(t *testing.T) {
	type args struct {
		tx        bchain.Tx
		height    uint32
		blockTime int64
		parser    *TLSParser
	}
	tests := []struct {
		name    string
		args    args
		want    string
		wantErr bool
	}{
		{
			name: "tls-1",
			args: args{
				tx:        testTx1,
				height:    1166741,
				blockTime: 1793191583,
				parser:    NewTLSParser(GetChainParams("main"), &btc.Configuration{}),
			},
			want:    testTxPacked1,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.args.parser.PackTx(&tt.args.tx, tt.args.height, tt.args.blockTime)
			if (err != nil) != tt.wantErr {
				t.Errorf("packTx() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			h := hex.EncodeToString(got)
			if !reflect.DeepEqual(h, tt.want) {
				t.Errorf("packTx() = %v, want %v", h, tt.want)
			}
		})
	}
}

func Test_UnpackTx(t *testing.T) {
	type args struct {
		packedTx string
		parser   *TLSParser
	}
	tests := []struct {
		name    string
		args    args
		want    *bchain.Tx
		want1   uint32
		wantErr bool
	}{
		{
			name: "tls-1",
			args: args{
				packedTx: testTxPacked1,
				parser:   NewTLSParser(GetChainParams("main"), &btc.Configuration{}),
			},
			want:    &testTx1,
			want1:   1166741,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, _ := hex.DecodeString(tt.args.packedTx)
			got, got1, err := tt.args.parser.UnpackTx(b)
			if (err != nil) != tt.wantErr {
				t.Errorf("unpackTx() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("unpackTx() got = %v, want %v", got, tt.want)
			}
			if got1 != tt.want1 {
				t.Errorf("unpackTx() got1 = %v, want %v", got1, tt.want1)
			}
		})
	}
}
