//go:build unittest

package server

import (
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/common"
	"github.com/trezor/blockbook/db"
)

// TestElectrumHTTPBlockOrderRealIndex indexes a mixed block with the real
// indexer (ConnectBlock): it pins ehStubIndex's model of what
// GetAddrDescTransactions yields, and checks that the listener turns the real
// index's yield back into block order.
func TestElectrumHTTPBlockOrderRealIndex(t *testing.T) {
	parser := ehParser()
	d, err := db.NewRocksDB(t.TempDir(), 100000, -1, parser, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	is, err := d.LoadInternalState(&common.Config{CoinName: "coin-unittest"})
	if err != nil {
		t.Fatal(err)
	}
	d.SetInternalState(is)

	scriptA := "76a914" + strings.Repeat("11", 20) + "88ac"
	scriptB := "76a914" + strings.Repeat("22", 20) + "88ac"
	txid := func(name string) string { return fmt.Sprintf("%064x", []byte(name)) }
	vout := func(n uint32, script string) bchain.Vout {
		return bchain.Vout{N: n, ValueSat: *big.NewInt(1000), ScriptPubKey: bchain.ScriptPubKey{Hex: script}}
	}
	tx := func(name string, vin []bchain.Vin, vouts ...bchain.Vout) bchain.Tx {
		return bchain.Tx{Txid: txid(name), Vin: vin, Vout: vouts}
	}
	spend := func(name string, n uint32) bchain.Vin { return bchain.Vin{Txid: txid(name), Vout: n} }

	block1 := &bchain.Block{
		BlockHeader: bchain.BlockHeader{Hash: strings.Repeat("a1", 32), Height: 1, Time: 1},
		Txs: []bchain.Tx{
			tx("f1", nil, vout(0, scriptA), vout(1, scriptA)),
			tx("f2", nil, vout(0, scriptA), vout(1, scriptB), vout(2, scriptB)),
		},
	}
	// Block order for A: s1 spends only, p1 pays, b1 spends (input 1) and
	// pays (output 0), s2 spends only. The index adds p1, b1, then s1, s2.
	block2 := &bchain.Block{
		BlockHeader: bchain.BlockHeader{Hash: strings.Repeat("a2", 32), Prev: block1.Hash, Height: 2, Time: 2},
		Txs: []bchain.Tx{
			tx("cb", []bchain.Vin{{Coinbase: "01"}}, vout(0, scriptB)),
			tx("s1", []bchain.Vin{spend("f1", 0)}, vout(0, scriptB)),
			tx("p1", []bchain.Vin{spend("f2", 1)}, vout(0, scriptA)),
			tx("b1", []bchain.Vin{spend("f2", 2), spend("f1", 1)}, vout(0, scriptA)),
			tx("s2", []bchain.Vin{spend("f2", 0)}, vout(0, scriptB)),
		},
	}
	for _, b := range []*bchain.Block{block1, block2} {
		if err := d.ConnectBlock(b); err != nil {
			t.Fatal(err)
		}
	}

	type yield struct {
		txid      string
		height    uint32
		inputOnly bool
	}
	var b1Indexes []int32
	collect := func(index electrumHTTPIndex, desc bchain.AddressDescriptor) []yield {
		var got []yield
		if err := index.GetAddrDescTransactions(desc, 0, ^uint32(0), func(id string, height uint32, indexes []int32) error {
			if id == txid("b1") {
				b1Indexes = slices.Clone(indexes)
			}
			txid := id
			// The stub gives one index per kind; the listener only reads
			// whether a transaction is spend-only.
			got = append(got, yield{txid, height, ehInputOnly(indexes)})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return got
	}
	desc := bchain.AddressDescriptor(mustHex(t, scriptA))
	fromDB := collect(d, desc)
	if !slices.Equal(b1Indexes, []int32{0, ^int32(1)}) {
		t.Fatalf("b1 indexes %v, want [0 ^1]", b1Indexes)
	}

	// The stub fed the same history yields the same.
	e := newEHEnv(t)
	e.index.history[scriptA] = []ehStubHistory{
		{txid("f1"), 1}, {txid("f2"), 1},
		{txid("s1"), 2}, {txid("p1"), 2}, {txid("b1"), 2}, {txid("s2"), 2},
	}
	e.index.inputOnly[txid("s1")] = true
	e.index.inputOnly[txid("s2")] = true
	e.index.both[txid("b1")] = true
	if stub := collect(e.index, desc); !reflect.DeepEqual(fromDB, stub) {
		t.Fatalf("real index yields %v,\nstub yields %v", fromDB, stub)
	}

	// The listener over the real index answers block order.
	s, err := NewElectrumHTTPServer("127.0.0.1:0", parser, e.worker, d, e.mempool, e.rpc.call, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.SetReady()
	e.s = s
	var blockTxs []string
	for _, x := range block2.Txs {
		blockTxs = append(blockTxs, x.Txid)
	}
	e.rpc.blocks[block2.Hash] = blockTxs
	w := e.get(ehProxyPrefix + "entirehistory&param=" + ehScriptAddr(t, parser, scriptA))
	var got []struct {
		TxHash string `json:"tx_hash"`
		Height int    `json:"height"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("%v: %s", err, w.Body.String())
	}
	var gotIDs []string
	for _, g := range got {
		gotIDs = append(gotIDs, g.TxHash)
	}
	want := []string{txid("f1"), txid("f2"), txid("s1"), txid("p1"), txid("b1"), txid("s2")}
	if !slices.Equal(gotIDs, want) {
		t.Fatalf("entirehistory %v, want %v", gotIDs, want)
	}
}
