//go:build unittest

package raptoreum

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/trezor/blockbook/bchain/coins/btc"
	"github.com/trezor/blockbook/bchain/coins/dash"
)

// fakeRaptoreumd answers the calls RaptoreumRPC makes from the testdata and
// records every getblock verbosity.
type fakeRaptoreumd struct {
	t         *testing.T
	mu        sync.Mutex
	methods   []string
	verbosity []int
}

func (f *fakeRaptoreumd) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		f.t.Errorf("bad request: %v", err)
		return
	}
	f.mu.Lock()
	f.methods = append(f.methods, req.Method)
	f.mu.Unlock()
	var result, rpcErr string
	switch req.Method {
	case "getblockchaininfo":
		result = `{"chain":"main","blocks":1464827,"headers":1464827,"bestblockhash":"` + blockHash + `","difficulty":0.19}`
	case "getnetworkinfo":
		result = `{"version":2000301,"subversion":"/Raptoreum Core:2.0.3.1/","protocolversion":70230}`
	case "getblockhash":
		var h uint32
		_ = json.Unmarshal(req.Params[0], &h)
		if h == 1420444 {
			result = `"` + blockHash + `"`
		} else {
			rpcErr = `{"code":-8,"message":"Block height out of range"}`
		}
	case "getblock":
		var hash string
		var v int
		_ = json.Unmarshal(req.Params[0], &hash)
		_ = json.Unmarshal(req.Params[1], &v)
		f.mu.Lock()
		f.verbosity = append(f.verbosity, v)
		f.mu.Unlock()
		if hash == blockHash && v == 2 {
			result = string(readTestdata(f.t, "block_1420444.json"))
		} else {
			rpcErr = `{"code":-5,"message":"Block not found"}`
		}
	default:
		rpcErr = `{"code":-32601,"message":"Method not found"}`
	}
	if rpcErr != "" {
		fmt.Fprintf(w, `{"result":null,"error":%s,"id":"1"}`, rpcErr)
		return
	}
	fmt.Fprintf(w, `{"result":%s,"error":null,"id":"1"}`, result)
}

func newTestRaptoreumRPC(t *testing.T) (*RaptoreumRPC, *fakeRaptoreumd) {
	t.Helper()
	f := &fakeRaptoreumd{t: t}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	cfg := fmt.Sprintf(`{"coin_name":"Raptoreum","rpc_url":%q,"rpc_user":"u","rpc_pass":"p","rpc_timeout":5,"parse":false}`, srv.URL)
	chain, err := NewRaptoreumRPC(json.RawMessage(cfg), nil)
	if err != nil {
		t.Fatal(err)
	}
	r := chain.(*RaptoreumRPC)
	if err := r.Initialize(); err != nil {
		t.Fatal(err)
	}
	return r, f
}

func TestRaptoreumRPC_Initialize(t *testing.T) {
	r, _ := newTestRaptoreumRPC(t)
	if r.Testnet || r.Network != "livenet" {
		t.Errorf("initialized as %v/%s", r.Testnet, r.Network)
	}
	if _, ok := r.Parser.(*RaptoreumParser); !ok {
		t.Errorf("parser = %T", r.Parser)
	}
	if r.ChainConfig.SupportsEstimateFee {
		t.Error("SupportsEstimateFee = true; raptoreumd has no estimatefee")
	}
	if !r.ChainConfig.SupportsEstimateSmartFee {
		t.Error("SupportsEstimateSmartFee = false")
	}
	if r.FirstBlockWithSpecialTransactions != 0 {
		t.Errorf("FirstBlockWithSpecialTransactions = %d, want 0", r.FirstBlockWithSpecialTransactions)
	}
}

// TestRaptoreumRPC_DashUnchanged guards against the Raptoreum override leaking
// into DashRPC's own activation height.
func TestRaptoreumRPC_DashUnchanged(t *testing.T) {
	newTestRaptoreumRPC(t)
	b, err := dash.NewDashRPC(json.RawMessage(`{"coin_name":"Dash","rpc_url":"http://127.0.0.1:1","rpc_user":"u","rpc_pass":"p","rpc_timeout":1}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := b.(*dash.DashRPC).FirstBlockWithSpecialTransactions; got != 1028160 {
		t.Errorf("Dash FirstBlockWithSpecialTransactions = %d, want 1028160", got)
	}
}

func TestRaptoreumRPC_GetBlock(t *testing.T) {
	r, f := newTestRaptoreumRPC(t)
	for _, tt := range []struct {
		hash   string
		height uint32
	}{{blockHash, 0}, {"", 1420444}, {blockHash, 1420444}} {
		block, err := r.GetBlock(tt.hash, tt.height)
		if err != nil {
			t.Fatal(err)
		}
		if block.Hash != blockHash || block.Height != 1420444 || block.Time != 1788268218 || block.Prev != blockPrev || block.Size != 1394 {
			t.Errorf("header = %+v", block.BlockHeader)
		}
		if len(block.Txs) != 3 || block.Txs[0].Txid != cbTxid || block.Txs[1].Txid != futureTxid || block.Txs[2].Txid != plainTxid {
			t.Fatalf("txs = %+v", block.Txs)
		}
		cb, fut := block.Txs[0], block.Txs[1]
		if cb.Version != 3 || cb.Vin[0].Coinbase == "" || len(cb.Vout) != 3 || cb.Vout[0].ValueSat.Int64() != 99800000236 {
			t.Errorf("coinbase = %+v", cb)
		}
		if fut.Version != 3 || len(fut.Vin) != 2 || len(fut.Vout) != 1 || fut.Vout[0].ValueSat.Int64() != 108359256859490 || fut.Vout[0].ScriptPubKey.Addresses[0] != "RHMagXBeMGna229N3sNuLY3Uh8WumEKe1X" {
			t.Errorf("future = %+v", fut)
		}
		if fut.Hex != testTxHex(t, "tx_1366dc99.json") {
			t.Error("future hex differs from getrawtransaction")
		}
	}
	// One getblock per block, always verbosity 2: no per-transaction calls.
	for _, m := range f.methods {
		if m == "getrawtransaction" {
			t.Error("GetBlock fetched a transaction on its own")
		}
	}
	for i, v := range f.verbosity {
		if v != 2 {
			t.Errorf("getblock call %d used verbosity %d", i, v)
		}
	}
	if len(f.verbosity) != 3 {
		t.Errorf("getblock calls = %d, want 3", len(f.verbosity))
	}
	if _, err := r.GetBlock("", 5); err == nil {
		t.Error("GetBlock of an unknown height succeeded")
	}
}

func TestRaptoreumRPC_Marshaler(t *testing.T) {
	m := jsonMarshaler{}
	for _, v := range []int{0, 1, 2} {
		req := btc.CmdGetBlock{Method: "getblock"}
		req.Params.BlockHash = blockHash
		req.Params.Verbosity = v
		got, err := m.Marshal(&req)
		if err != nil {
			t.Fatal(err)
		}
		if want := fmt.Sprintf(`{"method":"getblock","params":["%s",%d]}`, blockHash, v); string(got) != want {
			t.Errorf("getblock = %s, want %s", got, want)
		}
	}
	// Every other call is encoded as DashRPC encodes it.
	tx := btc.CmdGetRawTransaction{Method: "getrawtransaction"}
	tx.Params.Txid = futureTxid
	tx.Params.Verbose = true
	got, err := m.Marshal(&tx)
	want, _ := btc.JSONMarshalerV1{}.Marshal(&tx)
	if err != nil || string(got) != string(want) {
		t.Errorf("getrawtransaction = %s, want %s", got, want)
	}
}
