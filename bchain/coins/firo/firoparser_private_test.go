//go:build unittest

package firo

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"strings"
	"testing"

	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/bchain/coins/btc"
)

// mainnetTx is a mainnet transaction from firod v0.14.18.0: the raw hex of
// getrawtransaction and, for private spends, its verbose JSON
type mainnetTx struct {
	Txid string          `json:"txid"`
	Hex  string          `json:"hex"`
	JSON json.RawMessage `json:"json"`
}

// loadMainnetTxs reads testdata/mainnet_special_txs.json:
//   - coinbase 74d65d9f... (block 988)
//   - lelantus 4d36244e... (type 8 JoinSplit, block 450000, pays 6.15 FIRO to a3tuU5MW...)
//   - sparkv1  3c667f00... (type 9 Spark spend, block 850000, nFees 0.00006351)
//   - sparkv2  7842d789... (type 11 Spark V2 spend, block 1380973, pays 3000 FIRO to a3yX5UKV..., nFees 0.00003115)
//   - quorum   9d673607... (type 6 quorum commitment, block 279373)
//   - proreg   26d0d872... (type 1 ProRegTx, block 281963)
//   - exchange a8156ab6... (vout 3 pays EXXaSB8H9NUcFscQFuEMeKYTAKpq2BYjxdVS, block 941274)
func loadMainnetTxs(t *testing.T) map[string]mainnetTx {
	t.Helper()
	b, err := os.ReadFile("testdata/mainnet_special_txs.json")
	if err != nil {
		t.Fatal(err)
	}
	var txs map[string]mainnetTx
	if err := json.Unmarshal(b, &txs); err != nil {
		t.Fatal(err)
	}
	return txs
}

func mainnetParser() *FiroParser {
	return NewFiroParser(GetChainParams("main"), &btc.Configuration{})
}

func parseRaw(t *testing.T, p *FiroParser, tx mainnetTx) *bchain.Tx {
	t.Helper()
	b, err := hex.DecodeString(tx.Hex)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.ParseTx(b)
	if err != nil {
		t.Fatalf("ParseTx %s: %v", tx.Txid, err)
	}
	return got
}

func parseJSON(t *testing.T, p *FiroParser, tx mainnetTx) *bchain.Tx {
	t.Helper()
	got, err := p.ParseTxFromJson(tx.JSON)
	if err != nil {
		t.Fatalf("ParseTxFromJson %s: %v", tx.Txid, err)
	}
	// as FiroRPC.GetTransaction does
	got.CoinSpecificData = tx.JSON
	return got
}

// TestParseTxSpecialTransactions covers the mempool path (FiroRPC.GetTransactionForMempool),
// which used the bitcoin parser: special transactions got a txid without the extra
// payload and a quorum commitment failed to parse.
func TestParseTxSpecialTransactions(t *testing.T) {
	p := mainnetParser()
	txs := loadMainnetTxs(t)
	tests := []struct {
		name        string
		spendScript string // first byte of the scriptSig of a private spend input
	}{
		{name: "coinbase"},
		{name: "quorum"},
		{name: "proreg"},
		{name: "exchange"},
		{name: "lelantus", spendScript: "c9"},
		{name: "sparkv1", spendScript: "d3"},
		{name: "sparkv2", spendScript: "d3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := txs[tt.name]
			got := parseRaw(t, p, tx)
			if got.Txid != tx.Txid {
				t.Errorf("Txid = %s, want %s", got.Txid, tx.Txid)
			}
			if got.Hex != tx.Hex {
				t.Error("Hex is not the raw transaction")
			}
			if tt.spendScript == "" {
				return
			}
			if len(got.Vin) != 1 {
				t.Fatalf("len(Vin) = %d, want 1", len(got.Vin))
			}
			vin := got.Vin[0]
			if vin.Txid != "" || vin.Coinbase != "" || !strings.HasPrefix(vin.ScriptSig.Hex, tt.spendScript) {
				t.Errorf("spend input = %+v, want no txid, no coinbase and scriptSig %s...", vin, tt.spendScript)
			}
		})
	}
}

// TestParseTxBitcoinParserWasWrong documents the bug fixed by FiroParser.ParseTx.
func TestParseTxBitcoinParserWasWrong(t *testing.T) {
	p := mainnetParser()
	txs := loadMainnetTxs(t)
	b, _ := hex.DecodeString(txs["proreg"].Hex)
	got, err := p.BitcoinLikeParser.ParseTx(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.Txid == txs["proreg"].Txid {
		t.Error("the bitcoin parser now hashes the extra payload, FiroParser.ParseTx may be obsolete")
	}
	b, _ = hex.DecodeString(txs["quorum"].Hex)
	if _, err = p.BitcoinLikeParser.ParseTx(b); err == nil {
		t.Error("the bitcoin parser now parses a quorum commitment")
	}
}

func TestIsCoinbaseTx(t *testing.T) {
	p := mainnetParser()
	txs := loadMainnetTxs(t)
	tests := []struct {
		name string
		tx   *bchain.Tx
		want bool
	}{
		{name: "coinbase raw", tx: parseRaw(t, p, txs["coinbase"]), want: true},
		{name: "exchange raw", tx: parseRaw(t, p, txs["exchange"]), want: false},
		{name: "quorum raw", tx: parseRaw(t, p, txs["quorum"]), want: false},
		{name: "lelantus raw", tx: parseRaw(t, p, txs["lelantus"]), want: false},
		{name: "lelantus json", tx: parseJSON(t, p, txs["lelantus"]), want: false},
		{name: "spark v1 raw", tx: parseRaw(t, p, txs["sparkv1"]), want: false},
		{name: "spark v1 json", tx: parseJSON(t, p, txs["sparkv1"]), want: false},
		{name: "spark v2 raw", tx: parseRaw(t, p, txs["sparkv2"]), want: false},
		{name: "spark v2 json", tx: parseJSON(t, p, txs["sparkv2"]), want: false},
		{name: "zerocoin spend raw", tx: func() *bchain.Tx {
			b, _ := hex.DecodeString(rawTestTx2)
			tx, err := p.ParseTx(b)
			if err != nil {
				t.Fatal(err)
			}
			return tx
		}(), want: false},
		{name: "zerocoin spend json", tx: func() *bchain.Tx {
			tx, err := p.ParseTxFromJson(jsonTx)
			if err != nil {
				t.Fatal(err)
			}
			return tx
		}(), want: false},
		// transactions stored in the tx cache by earlier versions of parseFiroTx
		{name: "stored lelantus spend", tx: &bchain.Tx{Vin: []bchain.Vin{{Coinbase: "c9"}}}, want: false},
		{name: "stored spark spend", tx: &bchain.Tx{Vin: []bchain.Vin{{Coinbase: "d3"}}}, want: false},
		{name: "stored sigma spend", tx: &bchain.Tx{Vin: []bchain.Vin{{Coinbase: SpendTxID, ScriptSig: bchain.ScriptSig{Hex: "c4"}}}}, want: false},
		{name: "two inputs", tx: &bchain.Tx{Vin: []bchain.Vin{{Coinbase: "03a1"}, {Coinbase: "03a1"}}}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := p.IsCoinbaseTx(tt.tx); got != tt.want {
				t.Errorf("IsCoinbaseTx = %v, want %v (vin %+v)", got, tt.want, tt.tx.Vin)
			}
		})
	}
}

// nFees returns the fee firod shows in the verbose JSON of a Spark spend, in satoshis
func nFees(t *testing.T, txJSON json.RawMessage) int64 {
	t.Helper()
	var j struct {
		Vin []struct {
			NFees json.Number `json:"nFees"`
		} `json:"vin"`
	}
	if err := json.Unmarshal(txJSON, &j); err != nil {
		t.Fatal(err)
	}
	f, ok := new(big.Float).SetString(j.Vin[0].NFees.String())
	if !ok {
		t.Fatalf("nFees %q", j.Vin[0].NFees)
	}
	sat, _ := new(big.Float).Mul(f, big.NewFloat(1e8)).Float64()
	return int64(sat + 0.5)
}

func TestPrivateSpendFee(t *testing.T) {
	p := mainnetParser()
	txs := loadMainnetTxs(t)
	if got := nFees(t, txs["sparkv1"].JSON); got != 6351 {
		t.Fatalf("nFees of sparkv1 = %d", got)
	}
	if got := nFees(t, txs["sparkv2"].JSON); got != 3115 {
		t.Fatalf("nFees of sparkv2 = %d", got)
	}
	stored := func(tx *bchain.Tx) *bchain.Tx {
		// a tx cache record: no hex, no backend JSON
		c := *tx
		c.Hex = ""
		c.CoinSpecificData = nil
		return &c
	}
	tests := []struct {
		name         string
		tx           *bchain.Tx
		wantFee      int64 // -1 = nil
		wantReadable bool
	}{
		{name: "spark v1 raw", tx: parseRaw(t, p, txs["sparkv1"]), wantFee: 6351, wantReadable: true},
		{name: "spark v1 json", tx: parseJSON(t, p, txs["sparkv1"]), wantFee: 6351, wantReadable: true},
		{name: "spark v2 raw", tx: parseRaw(t, p, txs["sparkv2"]), wantFee: 3115, wantReadable: true},
		{name: "spark v2 json", tx: parseJSON(t, p, txs["sparkv2"]), wantFee: 3115, wantReadable: true},
		{name: "spark v2 stored", tx: stored(parseRaw(t, p, txs["sparkv2"])), wantFee: -1, wantReadable: true},
		{name: "spark legacy stored", tx: &bchain.Tx{Vin: []bchain.Vin{{Coinbase: "d3"}}}, wantFee: -1, wantReadable: true},
		{name: "lelantus raw", tx: parseRaw(t, p, txs["lelantus"]), wantFee: -1, wantReadable: false},
		{name: "lelantus json", tx: parseJSON(t, p, txs["lelantus"]), wantFee: -1, wantReadable: false},
		{name: "coinbase", tx: parseRaw(t, p, txs["coinbase"]), wantFee: -1, wantReadable: false},
		{name: "transparent", tx: parseRaw(t, p, txs["exchange"]), wantFee: -1, wantReadable: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fee, readable := p.PrivateSpendFee(tt.tx)
			if readable != tt.wantReadable {
				t.Errorf("readable = %v, want %v", readable, tt.wantReadable)
			}
			switch {
			case tt.wantFee < 0 && fee != nil:
				t.Errorf("fee = %v, want nil", fee)
			case tt.wantFee >= 0 && (fee == nil || fee.Int64() != tt.wantFee):
				t.Errorf("fee = %v, want %d", fee, tt.wantFee)
			}
		})
	}
}

func TestSparkSpendFeeInvalidPayload(t *testing.T) {
	txs := loadMainnetTxs(t)
	b, _ := hex.DecodeString(txs["sparkv1"].Hex)
	mtx, err := decodeFiroMsgTx(strings.NewReader(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	if f, err := sparkSpendFee(TransactionSparkType, mtx.Extra); err != nil || f != 6351 {
		t.Fatalf("sparkSpendFee = %d, %v", f, err)
	}
	tests := []struct {
		name    string
		txType  uint32
		payload []byte
	}{
		{name: "empty", txType: TransactionSparkType, payload: nil},
		{name: "truncated", txType: TransactionSparkType, payload: mtx.Extra[:60]},
		{name: "too many inputs", txType: TransactionSparkType, payload: []byte{0xfd, 0xff, 0xff}},
		{name: "v2 wrong version", txType: TransactionSparkV2Type, payload: []byte{1, 1, 1}},
		{name: "not a spark type", txType: 8, payload: mtx.Extra},
		{name: "fee above max money", txType: TransactionSparkType, payload: []byte{0, 0, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if f, err := sparkSpendFee(tt.txType, tt.payload); err == nil {
				t.Errorf("sparkSpendFee = %d, want error", f)
			}
		})
	}
}

// TestUnpackTxNormalizesStoredSpends: tx cache records written before this fix
// carry private spend inputs as coinbase inputs; they are normalized on read,
// so no reindex or cache flush is needed.
func TestUnpackTxNormalizesStoredSpends(t *testing.T) {
	p := mainnetParser()
	tests := []struct {
		name string
		vin  bchain.Vin
		want bchain.Vin
	}{
		{
			name: "lelantus",
			vin:  bchain.Vin{Coinbase: "c9", Sequence: 4294967295},
			want: bchain.Vin{ScriptSig: bchain.ScriptSig{Hex: "c9"}, Sequence: 4294967295},
		},
		{
			name: "sigma",
			vin:  bchain.Vin{Coinbase: SpendTxID, ScriptSig: bchain.ScriptSig{Hex: "c40102"}},
			want: bchain.Vin{ScriptSig: bchain.ScriptSig{Hex: "c40102"}},
		},
		{
			name: "coinbase",
			vin:  bchain.Vin{Coinbase: "03a1860104", Sequence: 4294967295},
			want: bchain.Vin{Coinbase: "03a1860104", Sequence: 4294967295},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := &bchain.Tx{Txid: "74d65d9f673220566ce09972daae22bb7d7637aba72750d95abcf2aad051f990", Vin: []bchain.Vin{tt.vin}}
			packed, err := p.BaseParser.PackTx(tx, 1, 1)
			if err != nil {
				t.Fatal(err)
			}
			got, _, err := p.UnpackTx(packed)
			if err != nil {
				t.Fatal(err)
			}
			if g := got.Vin[0]; g.Coinbase != tt.want.Coinbase || g.Txid != "" || g.ScriptSig.Hex != tt.want.ScriptSig.Hex || g.Sequence != tt.want.Sequence {
				t.Errorf("vin = %+v, want %+v", g, tt.want)
			}
		})
	}
}

func TestExchangeAddress(t *testing.T) {
	p := mainnetParser()
	txs := loadMainnetTxs(t)
	const (
		exAddr   = "EXXaSB8H9NUcFscQFuEMeKYTAKpq2BYjxdVS"
		exScript = "e076a914db14fb77c6517377c08ae36a8688d874eaa95a9f88ac"
	)
	script, _ := hex.DecodeString(exScript)

	addrs, searchable, err := p.GetAddressesFromAddrDesc(script)
	if err != nil || !searchable || len(addrs) != 1 || addrs[0] != exAddr {
		t.Errorf("GetAddressesFromAddrDesc = %v, %v, %v; want [%s], true", addrs, searchable, err, exAddr)
	}

	desc, err := p.GetAddrDescFromAddress(exAddr)
	if err != nil || hex.EncodeToString(desc) != exScript {
		t.Errorf("GetAddrDescFromAddress = %x, %v; want %s", desc, err, exScript)
	}

	// vout 3 of a mainnet tx, decoded the way firod shows it
	tx := parseRaw(t, p, txs["exchange"])
	if got := tx.Vout[3].ScriptPubKey.Addresses; len(got) != 1 || got[0] != exAddr {
		t.Errorf("vout 3 addresses = %v, want [%s]", got, exAddr)
	}
	vdesc, err := p.GetAddrDescFromVout(&tx.Vout[3])
	if err != nil || hex.EncodeToString(vdesc) != exScript {
		t.Errorf("GetAddrDescFromVout = %x, %v; the index keys by the script", vdesc, err)
	}

	// other addresses are not affected
	desc, err = p.GetAddrDescFromAddress("a3yX5UKVnoHdf4iN93HgENmwmpSY7CPvN4")
	if err != nil || hex.EncodeToString(desc) != "76a91423c1671516726a8bddfab69291e47ceea569994e88ac" {
		t.Errorf("P2PKH GetAddrDescFromAddress = %x, %v", desc, err)
	}

	// a wrong checksum, a testnet exchange address and a non-exchange script are rejected
	bad := exAddr[:len(exAddr)-1] + "T"
	if _, err := p.GetAddrDescFromAddress(bad); err == nil {
		t.Errorf("GetAddrDescFromAddress(%s) with a bad checksum succeeded", bad)
	}
	tp := NewFiroParser(GetChainParams("test"), &btc.Configuration{})
	tAddrs, _, _ := tp.GetAddressesFromAddrDesc(script)
	if len(tAddrs) != 1 || !strings.HasPrefix(tAddrs[0], "EXT") {
		t.Errorf("testnet exchange address = %v, want EXT...", tAddrs)
	}
	if _, err := p.GetAddrDescFromAddress(tAddrs[0]); err == nil {
		t.Errorf("mainnet parser accepted testnet exchange address %s", tAddrs[0])
	}
	if d, err := tp.GetAddrDescFromAddress(tAddrs[0]); err != nil || hex.EncodeToString(d) != exScript {
		t.Errorf("testnet GetAddrDescFromAddress = %x, %v", d, err)
	}
	notEx := append([]byte{OpExchangeAddr}, script[1:25]...) // no OP_CHECKSIG
	if a, s, _ := p.GetAddressesFromAddrDesc(notEx); len(a) != 0 || s {
		t.Errorf("truncated exchange script decoded to %v, %v", a, s)
	}
}
