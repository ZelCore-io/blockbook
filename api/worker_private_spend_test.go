//go:build unittest

package api

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/bchain/coins/btc"
	"github.com/trezor/blockbook/bchain/coins/firo"
	"github.com/trezor/blockbook/db"
	"github.com/trezor/blockbook/tests/dbtestdata"
)

// privateSpendChain serves transactions and their verbose JSON by txid and
// counts the calls, standing in for firod
type privateSpendChain struct {
	bchain.BlockChain
	txs         map[string]*bchain.Tx
	specific    map[string]json.RawMessage
	getTx       int
	getSpecific int
}

func (c *privateSpendChain) GetTransaction(txid string) (*bchain.Tx, error) {
	c.getTx++
	if tx, found := c.txs[txid]; found {
		return tx, nil
	}
	return nil, bchain.ErrTxNotFound
}

func (c *privateSpendChain) GetTransactionSpecific(tx *bchain.Tx) (json.RawMessage, error) {
	c.getSpecific++
	if j, found := c.specific[tx.Txid]; found {
		return j, nil
	}
	return nil, bchain.ErrTxNotFound
}

type firoMainnetTx struct {
	Txid string          `json:"txid"`
	Hex  string          `json:"hex"`
	JSON json.RawMessage `json:"json"`
}

// buildFiroWorker indexes, in one block, a coinbase, a Lelantus JoinSplit
// paying 6.15 FIRO to a3tuU5MW... and a Spark V2 spend paying 3000 FIRO to
// a3yX5UKV... (mainnet transactions, see bchain/coins/firo/testdata), so all
// their outputs have 1 confirmation.
func buildFiroWorker(t *testing.T) (*Worker, *privateSpendChain, map[string]*bchain.Tx, map[string]firoMainnetTx) {
	t.Helper()
	b, err := os.ReadFile("../bchain/coins/firo/testdata/mainnet_special_txs.json")
	require.NoError(t, err)
	var fixtures map[string]firoMainnetTx
	require.NoError(t, json.Unmarshal(b, &fixtures))

	parser := firo.NewFiroParser(firo.GetChainParams("main"), &btc.Configuration{BlockAddressesToKeep: 1, MinimumCoinbaseConfirmations: 100})
	parsed := make(map[string]*bchain.Tx)
	block := &bchain.Block{BlockHeader: bchain.BlockHeader{Hash: fanInHash(1_000_001), Height: 1, Time: 1_700_000_001}}
	chain := &privateSpendChain{txs: make(map[string]*bchain.Tx), specific: make(map[string]json.RawMessage)}
	for _, name := range []string{"coinbase", "lelantus", "sparkv2"} {
		raw, err := hex.DecodeString(fixtures[name].Hex)
		require.NoError(t, err)
		tx, err := parser.ParseTx(raw)
		require.NoError(t, err)
		tx.BlockHeight, tx.Confirmations, tx.Blocktime, tx.Time = 1, 1, 1_700_000_001, 1_700_000_001
		parsed[name] = tx
		block.Txs = append(block.Txs, *tx)
		// firod serves confirmed transactions as verbose JSON
		jtx := *tx
		jtx.Hex = ""
		jtx.CoinSpecificData = fixtures[name].JSON
		chain.txs[tx.Txid] = &jtx
		chain.specific[tx.Txid] = fixtures[name].JSON
	}
	base, err := dbtestdata.NewFakeBlockChain(parser)
	require.NoError(t, err)
	chain.BlockChain = base

	database, is := newIndexedWorkerDB(t, parser, []*bchain.Block{block})
	txCache, err := db.NewTxCache(database, chain, newRefreshTestMetrics(t), is, false)
	require.NoError(t, err)
	w := &Worker{db: database, chain: chain, chainParser: parser, chainType: bchain.ChainBitcoinType, txCache: txCache, is: is}
	return w, chain, parsed, fixtures
}

func firstAddress(t *testing.T, w *Worker, vout *bchain.Vout) string {
	t.Helper()
	_, addrs, _, err := w.getAddressesFromVout(vout)
	require.NoError(t, err)
	require.Len(t, addrs, 1)
	return addrs[0]
}

// TestGetAddressUtxoFiroPrivateSpendIsNotCoinbase: an output paid by a
// Lelantus or Spark spend has no transparent input, like a coinbase output.
// Before the fix it was flagged coinbase until 100 confirmations, and ZelCore
// refuses to spend coinbase UTXOs below 100 confirmations.
func TestGetAddressUtxoFiroPrivateSpendIsNotCoinbase(t *testing.T) {
	w, chain, parsed, _ := buildFiroWorker(t)
	tests := []struct {
		name         string
		addr         string
		wantCoinbase bool
	}{
		{name: "coinbase", addr: firstAddress(t, w, &parsed["coinbase"].Vout[0]), wantCoinbase: true},
		{name: "lelantus", addr: "a3tuU5MWz9CHVN4afWFr83izoz93Er94eq", wantCoinbase: false},
		{name: "spark v2", addr: "a3yX5UKVnoHdf4iN93HgENmwmpSY7CPvN4", wantCoinbase: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			utxos, err := w.GetAddressUtxo(tt.addr, true)
			require.NoError(t, err)
			require.Len(t, utxos, 1)
			require.Equal(t, 1, utxos[0].Confirmations)
			require.Equal(t, tt.wantCoinbase, utxos[0].Coinbase)
		})
	}
	require.Equal(t, len(tests), chain.getTx, "one backend lookup per candidate UTXO under 100 confirmations")
}

// TestGetAddressUtxoCoinbaseOtherCoinsUnchanged: for a parser without
// bchain.PrivateSpendParser the index heuristic is final, the tx is not loaded.
func TestGetAddressUtxoCoinbaseOtherCoinsUnchanged(t *testing.T) {
	parser := btc.NewBitcoinParser(btc.GetChainParams("test"), &btc.Configuration{BlockAddressesToKeep: 1, MinimumCoinbaseConfirmations: 100})
	_, ok := interface{}(parser).(bchain.PrivateSpendParser)
	require.False(t, ok)
	script := dbtestdata.AddressToPubKeyHex(dbtestdata.Addr1, parser)
	tx := fanInTx(fanInHash(1), 1, []bchain.Vin{{Coinbase: "01", Sequence: 0xffffffff}}, []bchain.Vout{fanInVout(0, script)})
	database, is := newIndexedWorkerDB(t, parser, []*bchain.Block{
		{BlockHeader: bchain.BlockHeader{Hash: fanInHash(1_000_001), Height: 1, Time: 1_700_000_001}, Txs: []bchain.Tx{*tx}},
	})
	// no chain and no tx cache: any attempt to load the transaction panics
	w := &Worker{db: database, chainParser: parser, chainType: bchain.ChainBitcoinType, is: is}
	utxos, err := w.GetAddressUtxo(dbtestdata.Addr1, true)
	require.NoError(t, err)
	require.Len(t, utxos, 1)
	require.True(t, utxos[0].Coinbase)
}

func TestGetTransactionFiroSparkFee(t *testing.T) {
	w, chain, parsed, fixtures := buildFiroWorker(t)
	addresses := map[string]struct{}{}
	spark := parsed["sparkv2"]

	// raw transaction (mempool parse): the fee is read from the extra payload
	tx, err := w.GetTransactionFromBchainTx(spark, 1, false, false, addresses)
	require.NoError(t, err)
	require.Equal(t, big.NewInt(3115), (*big.Int)(tx.FeesSat))
	require.Equal(t, 0, chain.getSpecific)

	// backend JSON (FiroRPC.GetTransaction): read from sparkData
	tx, err = w.GetTransactionFromBchainTx(chain.txs[spark.Txid], 1, false, false, addresses)
	require.NoError(t, err)
	require.Equal(t, big.NewInt(3115), (*big.Int)(tx.FeesSat))
	require.Equal(t, 0, chain.getSpecific)

	// tx cache record (no hex, no JSON): the JSON is requested from the backend
	stored := *spark
	stored.Hex = ""
	tx, err = w.GetTransactionFromBchainTx(&stored, 1, false, false, addresses)
	require.NoError(t, err)
	require.Equal(t, big.NewInt(3115), (*big.Int)(tx.FeesSat))
	require.Equal(t, 1, chain.getSpecific)
	require.Empty(t, tx.Vin[0].Coinbase, "a Spark spend input is not coinbase")
	require.Equal(t, "d3", tx.Vin[0].Hex)

	// Lelantus: the fee is not derivable, it stays 0 and the backend is not asked
	tx, err = w.GetTransactionFromBchainTx(parsed["lelantus"], 1, false, false, addresses)
	require.NoError(t, err)
	require.Equal(t, 0, (*big.Int)(tx.FeesSat).Sign())
	require.Equal(t, 1, chain.getSpecific)

	// mempool notification path
	mtx := &bchain.MempoolTx{Hex: fixtures["sparkv2"].Hex, Txid: spark.Txid, Version: spark.Version, Vout: spark.Vout}
	for _, v := range spark.Vin {
		mtx.Vin = append(mtx.Vin, bchain.MempoolVin{Vin: v})
	}
	tx, err = w.GetTransactionFromMempoolTx(mtx)
	require.NoError(t, err)
	require.Equal(t, big.NewInt(3115), (*big.Int)(tx.FeesSat))
	require.Equal(t, 1, chain.getSpecific)
}
