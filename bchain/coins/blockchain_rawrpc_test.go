//go:build unittest

package coins

import (
	"testing"

	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/bchain/coins/btc"
	"github.com/trezor/blockbook/bchain/coins/zec"
)

func TestRawRPC(t *testing.T) {
	for _, chain := range []bchain.BlockChain{&btc.BitcoinRPC{}, &zec.ZCashRPC{BitcoinRPC: &btc.BitcoinRPC{}}} {
		if call, ok := (&blockChainWithMetrics{b: chain}).RawRPC(); !ok || call == nil {
			t.Fatalf("%T: raw RPC not exposed", chain)
		}
	}
	if _, ok := (&blockChainWithMetrics{b: struct{ bchain.BlockChain }{}}).RawRPC(); ok {
		t.Fatal("chain without Call exposed raw RPC")
	}
}
