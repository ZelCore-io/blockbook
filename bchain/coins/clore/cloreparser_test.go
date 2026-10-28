//go:build unittest

package clore

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
			args:    args{address: "AcYSzztTWRZ8CkSMQGjgALvWvJpcoMLkQ6"},
			want:    "76a914e3d0a8f999fd15c9bb3881609a41976e9ba942b388ac",
			wantErr: false,
		},
		{
			name:    "P2PKH2",
			args:    args{address: "APL8d2iXurTyoGe3bkDHuVhkdKwiSSbeuC"},
			want:    "76a91452e2b1068d430f1e624dbc5c1f8230e8c7af69d588ac",
			wantErr: false,
		},
		{
			name:    "P2PKH3",
			args:    args{address: "ANY2GSJ1jn2kp7i9Znz7BEgaBZfHkexqdy"},
			want:    "76a9144a2a40987c74578ee517d426aa2c43fc568f7e0888ac",
			wantErr: false,
		},
		{
			name:    "P2SH1",
			args:    args{address: "rCzjkBoY2duVn2WizKxfBedTVWAg6UhfLZ"},
			want:    "a9144a2a40987c74578ee517d426aa2c43fc568f7e0887",
			wantErr: false,
		},
		{
			name:    "P2SH2",
			args:    args{address: "rDzGemZkv9FbDDh5pvWfr7TWtMUnNRRE7T"},
			want:    "a914550bc2fcc1992afade4d298326ee6a03ab975a9387",
			wantErr: false,
		},
	}
	parser := NewCloreParser(GetChainParams("main"), &btc.Configuration{})

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

// testTx1 is mainnet transaction bca9c29fbc961f06018820bd975845156cc26030632d0b8cbaa415c6c0c2e591 (block 1611988).
var (
	testTx1       bchain.Tx
	testTxPacked1 = "0a20bca9c29fbc961f06018820bd975845156cc26030632d0b8cbaa415c6c0c2e59112f50202000000028b36a5a2838762c3b123dca9b65b510ca777ebaa3e1307131680bb3b57ac8513020000006b4830450221009b59aa0537d9cf092e9cad673e37eff2aae2cbaf581ee88d0a87da4cea9f9bf20220481275069f98bf7d264e6f4478bd8f0165dda18b7d14567747f40fa99e01ede701210361e6a7a9d79d19a9450d3f9c078d946162b0c29fd03a35431b3ba799608b41b7feffffffa842a7d9b51b7dec8f904d73617546dd82f33f558139facebc62f29fdc846eed020000006a473044022010dde1e79ea9b0da430c41d956c71f878c34371457180be054c50053a0e4658e0220795dab51bfccfb127d7146f53c6bca2fb549d9b2ceff7bb41d3d5d2a8833647201210361e6a7a9d79d19a9450d3f9c078d946162b0c29fd03a35431b3ba799608b41b7feffffff02d0913661060000001976a914e3d0a8f999fd15c9bb3881609a41976e9ba942b388ac47a08b10000000001976a91452e2b1068d430f1e624dbc5c1f8230e8c7af69d588acd398180018fb9dd0cb0620d3b16228d4b16232970112201385ac573bbb80161307133eaaeb77a70c515bb6a9dc23b1c3628783a2a5368b1802226b4830450221009b59aa0537d9cf092e9cad673e37eff2aae2cbaf581ee88d0a87da4cea9f9bf20220481275069f98bf7d264e6f4478bd8f0165dda18b7d14567747f40fa99e01ede701210361e6a7a9d79d19a9450d3f9c078d946162b0c29fd03a35431b3ba799608b41b728feffffff0f3296011220ed6e84dc9ff262bccefa3981553ff382dd467561734d908fec7d1bb5d9a742a81802226a473044022010dde1e79ea9b0da430c41d956c71f878c34371457180be054c50053a0e4658e0220795dab51bfccfb127d7146f53c6bca2fb549d9b2ceff7bb41d3d5d2a8833647201210361e6a7a9d79d19a9450d3f9c078d946162b0c29fd03a35431b3ba799608b41b728feffffff0f3a460a0506613691d01a1976a914e3d0a8f999fd15c9bb3881609a41976e9ba942b388ac2222416359537a7a745457525a38436b534d51476a67414c7657764a70636f4d4c6b51363a470a04108ba04710011a1976a91452e2b1068d430f1e624dbc5c1f8230e8c7af69d588ac222241504c3864326958757254796f476533626b44487556686b644b77695353626575434002"
)

func init() {
	testTx1 = bchain.Tx{
		Hex:       "02000000028b36a5a2838762c3b123dca9b65b510ca777ebaa3e1307131680bb3b57ac8513020000006b4830450221009b59aa0537d9cf092e9cad673e37eff2aae2cbaf581ee88d0a87da4cea9f9bf20220481275069f98bf7d264e6f4478bd8f0165dda18b7d14567747f40fa99e01ede701210361e6a7a9d79d19a9450d3f9c078d946162b0c29fd03a35431b3ba799608b41b7feffffffa842a7d9b51b7dec8f904d73617546dd82f33f558139facebc62f29fdc846eed020000006a473044022010dde1e79ea9b0da430c41d956c71f878c34371457180be054c50053a0e4658e0220795dab51bfccfb127d7146f53c6bca2fb549d9b2ceff7bb41d3d5d2a8833647201210361e6a7a9d79d19a9450d3f9c078d946162b0c29fd03a35431b3ba799608b41b7feffffff02d0913661060000001976a914e3d0a8f999fd15c9bb3881609a41976e9ba942b388ac47a08b10000000001976a91452e2b1068d430f1e624dbc5c1f8230e8c7af69d588acd3981800",
		Blocktime: 1769213691,
		Time:      1769213691,
		Txid:      "bca9c29fbc961f06018820bd975845156cc26030632d0b8cbaa415c6c0c2e591",
		LockTime:  1611987,
		Version:   2,
		Vin: []bchain.Vin{
			{
				ScriptSig: bchain.ScriptSig{
					Hex: "4830450221009b59aa0537d9cf092e9cad673e37eff2aae2cbaf581ee88d0a87da4cea9f9bf20220481275069f98bf7d264e6f4478bd8f0165dda18b7d14567747f40fa99e01ede701210361e6a7a9d79d19a9450d3f9c078d946162b0c29fd03a35431b3ba799608b41b7",
				},
				Txid:     "1385ac573bbb80161307133eaaeb77a70c515bb6a9dc23b1c3628783a2a5368b",
				Vout:     2,
				Sequence: 4294967294,
			},
			{
				ScriptSig: bchain.ScriptSig{
					Hex: "473044022010dde1e79ea9b0da430c41d956c71f878c34371457180be054c50053a0e4658e0220795dab51bfccfb127d7146f53c6bca2fb549d9b2ceff7bb41d3d5d2a8833647201210361e6a7a9d79d19a9450d3f9c078d946162b0c29fd03a35431b3ba799608b41b7",
				},
				Txid:     "ed6e84dc9ff262bccefa3981553ff382dd467561734d908fec7d1bb5d9a742a8",
				Vout:     2,
				Sequence: 4294967294,
			},
		},
		Vout: []bchain.Vout{
			{
				ValueSat: *big.NewInt(27400770000),
				N:        0,
				ScriptPubKey: bchain.ScriptPubKey{
					Hex: "76a914e3d0a8f999fd15c9bb3881609a41976e9ba942b388ac",
					Addresses: []string{
						"AcYSzztTWRZ8CkSMQGjgALvWvJpcoMLkQ6",
					},
				},
			},
			{
				ValueSat: *big.NewInt(277585991),
				N:        1,
				ScriptPubKey: bchain.ScriptPubKey{
					Hex: "76a91452e2b1068d430f1e624dbc5c1f8230e8c7af69d588ac",
					Addresses: []string{
						"APL8d2iXurTyoGe3bkDHuVhkdKwiSSbeuC",
					},
				},
			},
		},
	}
}

func Test_ParseTx(t *testing.T) {
	parser := NewCloreParser(GetChainParams("main"), &btc.Configuration{})
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
		parser    *CloreParser
	}
	tests := []struct {
		name    string
		args    args
		want    string
		wantErr bool
	}{
		{
			name: "clore-1",
			args: args{
				tx:        testTx1,
				height:    1611988,
				blockTime: 1769213691,
				parser:    NewCloreParser(GetChainParams("main"), &btc.Configuration{}),
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
		parser   *CloreParser
	}
	tests := []struct {
		name    string
		args    args
		want    *bchain.Tx
		want1   uint32
		wantErr bool
	}{
		{
			name: "clore-1",
			args: args{
				packedTx: testTxPacked1,
				parser:   NewCloreParser(GetChainParams("main"), &btc.Configuration{}),
			},
			want:    &testTx1,
			want1:   1611988,
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
