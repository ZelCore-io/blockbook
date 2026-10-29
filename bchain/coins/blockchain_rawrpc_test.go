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

type namedRawRequest string

func (n namedRawRequest) RPCMethod() string { return string(n) }

func TestRawRPCLabel(t *testing.T) {
	if got := rawRPCLabel(namedRawRequest("getblock")); got != "RawRPC:getblock" {
		t.Fatalf("named request label %q", got)
	}
	if got := rawRPCLabel(namedRawRequest("")); got != "RawRPC" {
		t.Fatalf("empty method label %q", got)
	}
	if got := rawRPCLabel(struct{}{}); got != "RawRPC" {
		t.Fatalf("unnamed request label %q", got)
	}
}
