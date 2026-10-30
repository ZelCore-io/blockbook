package axe

import (
	"encoding/json"

	"github.com/golang/glog"
	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/bchain/coins/dash"
)

// firstBlockWithSpecialTransactions is consensus.DIP0003Height of Axe mainnet
// (chainparams.cpp, v1.6.1.1)
const firstBlockWithSpecialTransactions = 213696

// AxeRPC is an interface to JSON-RPC axed service. It behaves like DashRPC with
// Axe's network parameters and DIP3 activation height.
type AxeRPC struct {
	*dash.DashRPC
}

// NewAxeRPC returns new AxeRPC instance
func NewAxeRPC(config json.RawMessage, pushHandler func(bchain.NotificationType)) (bchain.BlockChain, error) {
	b, err := dash.NewDashRPC(config, pushHandler)
	if err != nil {
		return nil, err
	}

	s := &AxeRPC{
		DashRPC: b.(*dash.DashRPC),
	}
	s.FirstBlockWithSpecialTransactions = firstBlockWithSpecialTransactions

	return s, nil
}

// Initialize initializes AxeRPC instance.
func (b *AxeRPC) Initialize() error {
	ci, err := b.GetChainInfo()
	if err != nil {
		return err
	}

	params := GetChainParams(ci.Chain)

	// always create parser
	b.Parser = NewAxeParser(params, b.ChainConfig)

	// parameters for getInfo request
	if params.Net == MainnetMagic {
		b.Testnet = false
		b.Network = "livenet"
	} else {
		b.Testnet = true
		b.Network = "testnet"
	}

	glog.Info("rpc: block chain ", params.Name)

	return nil
}
