//go:build unittest

package anon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/trezor/blockbook/bchain"
)

// fakeAnond answers like anond v.2.2.0-62: getblock takes only a boolean
// verbose and rejects a number with UniValue's get_bool error.
type fakeAnond struct {
	t        *testing.T
	mu       sync.Mutex
	verboses []interface{} // getblock verbose arguments, in order
	methods  []string
}

func (f *fakeAnond) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	var result string
	var rpcErr string
	switch req.Method {
	case "getblockchaininfo":
		result = `{"chain":"main","blocks":1368080,"headers":1368080,"bestblockhash":"` + blockHash + `","difficulty":50659.97}`
	case "getnetworkinfo":
		result = `{"version":2020050,"subversion":"/MagicBean:2.2.0/","protocolversion":170009}`
	case "getblockhash":
		result = `"` + blockHash + `"`
	case "getblock":
		var verbose interface{}
		if len(req.Params) > 1 {
			_ = json.Unmarshal(req.Params[1], &verbose)
		}
		f.mu.Lock()
		f.verboses = append(f.verboses, verbose)
		f.mu.Unlock()
		switch verbose {
		case true:
			result = string(readTestdata(f.t, "block_1368074.json"))
		case false:
			result = `"` + string(readTestdata(f.t, "block_1368074.hex")) + `"`
		default:
			rpcErr = `{"code":-1,"message":"JSON value is not a boolean as expected"}`
		}
	case "getrawtransaction":
		var txid string
		var verbose int
		_ = json.Unmarshal(req.Params[0], &txid)
		_ = json.Unmarshal(req.Params[1], &verbose)
		file := map[string]string{coinbaseTxid: "tx_f868ceb0", sproutTxid: "tx_6caa4ad9"}[txid]
		if verbose != 1 || file == "" {
			rpcErr = `{"code":-5,"message":"No information available about transaction"}`
		} else {
			result = string(readTestdata(f.t, file+".json"))
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

func newTestAnonRPC(t *testing.T) (*AnonRPC, *fakeAnond) {
	t.Helper()
	f := &fakeAnond{t: t}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	cfg := fmt.Sprintf(`{"coin_name":"Anon","rpc_url":%q,"rpc_user":"u","rpc_pass":"p","rpc_timeout":5,"parse":true,"mempool_resync_batch_size":50}`, srv.URL)
	chain, err := NewAnonRPC(json.RawMessage(cfg), nil)
	if err != nil {
		t.Fatal(err)
	}
	z := chain.(*AnonRPC)
	if err := z.Initialize(); err != nil {
		t.Fatal(err)
	}
	return z, f
}

func TestAnonRPC_GetBlock(t *testing.T) {
	z, f := newTestAnonRPC(t)
	if z.Testnet || z.Network != "livenet" || z.Parser.(*AnonParser) == nil {
		t.Fatalf("initialized as %v/%s", z.Testnet, z.Network)
	}
	if z.ChainConfig.MempoolResyncBatchSize != 1 || z.ChainConfig.SupportsEstimateSmartFee {
		t.Errorf("batch size %d, estimatesmartfee %v", z.ChainConfig.MempoolResyncBatchSize, z.ChainConfig.SupportsEstimateSmartFee)
	}
	for _, tt := range []struct {
		hash   string
		height uint32
	}{{blockHash, 0}, {"", 1368074}, {blockHash, 1368074}} {
		block, err := z.GetBlock(tt.hash, tt.height)
		if err != nil {
			t.Fatal(err)
		}
		if block.Hash != blockHash || block.Height != 1368074 || block.Time != 1793456775 || block.Prev != "00012460699a8d8635903bcd952061d6035b023d66a8290d1f57efe1afbeab36" {
			t.Errorf("header = %+v", block.BlockHeader)
		}
		if len(block.Txs) != 2 || block.Txs[0].Txid != coinbaseTxid || block.Txs[1].Txid != sproutTxid {
			t.Fatalf("txs = %d", len(block.Txs))
		}
		cb, sp := block.Txs[0], block.Txs[1]
		if cb.Version != 1 || sp.Version != -3 || len(cb.Vout) != 3 || len(sp.Vin) != 2 || len(sp.Vout) != 0 {
			t.Errorf("tx shapes: cb v%d vout %d, sprout v%d vin %d vout %d", cb.Version, len(cb.Vout), sp.Version, len(sp.Vin), len(sp.Vout))
		}
		// The verbose JSON is kept for the Insight zcash extension.
		if !strings.Contains(string(sp.CoinSpecificData.(json.RawMessage)), `"vjoinsplit"`) {
			t.Error("sprout tx lost its verbose JSON")
		}
		if sp.Blocktime != 1793456775 {
			t.Errorf("blocktime = %d", sp.Blocktime)
		}
	}
	bi, err := z.GetBlockInfo(blockHash)
	if err != nil || len(bi.Txids) != 2 {
		t.Fatalf("GetBlockInfo = %+v, %v", bi, err)
	}
	full, err := z.GetBlockFull(blockHash)
	if err != nil || len(full.Txs) != 2 {
		t.Fatalf("GetBlockFull = %v", err)
	}
	raw, err := z.GetBlockRaw(blockHash)
	if err != nil || raw != string(readTestdata(t, "block_1368074.hex")) {
		t.Fatalf("GetBlockRaw = %.16s..., %v", raw, err)
	}
	tx, err := z.GetTransactionForMempool(sproutTxid)
	if err != nil || tx.Version != -3 || len(tx.Vin) != 2 {
		t.Fatalf("GetTransactionForMempool = %+v, %v", tx, err)
	}
	// anond rejects a numeric verbosity: none may ever be sent.
	for i, v := range f.verboses {
		if _, ok := v.(bool); !ok {
			t.Errorf("getblock call %d sent verbose %v (%T)", i, v, v)
		}
	}
	if len(f.verboses) == 0 || f.verboses[len(f.verboses)-1] != false {
		t.Errorf("verboses = %v", f.verboses)
	}
}

func TestAnonRPC_UnknownTx(t *testing.T) {
	z, _ := newTestAnonRPC(t)
	if _, err := z.GetTransaction("00" + coinbaseTxid[2:]); err != bchain.ErrTxNotFound {
		t.Fatalf("GetTransaction(unknown) = %v", err)
	}
}
