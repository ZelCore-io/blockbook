package kmd

import (
	"encoding/json"

	"github.com/golang/glog"
	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/bchain/coins/zec"
)

// KomodoRPC is an interface to JSON-RPC komodod service. It behaves like
// ZCashRPC with Komodo's network parameters.
type KomodoRPC struct {
	*zec.ZCashRPC
}

// NewKomodoRPC returns new KomodoRPC instance
func NewKomodoRPC(config json.RawMessage, pushHandler func(bchain.NotificationType)) (bchain.BlockChain, error) {
	b, err := zec.NewZCashRPC(config, pushHandler)
	if err != nil {
		return nil, err
	}
	return &KomodoRPC{ZCashRPC: b.(*zec.ZCashRPC)}, nil
}

// Initialize initializes KomodoRPC instance.
func (b *KomodoRPC) Initialize() error {
	ci, err := b.GetChainInfo()
	if err != nil {
		return err
	}

	params := GetChainParams(ci.Chain)

	// always create parser
	b.Parser = NewKomodoParser(params, b.ChainConfig)

	// parameters for getInfo request
	b.Testnet = false
	b.Network = "livenet"

	glog.Info("rpc: block chain ", params.Name, " (komodod chain ", ci.Chain, ")")

	return nil
}
