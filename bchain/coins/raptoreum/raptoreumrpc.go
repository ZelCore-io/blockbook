package raptoreum

import (
	"encoding/json"

	"github.com/golang/glog"
	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/bchain/coins/btc"
	"github.com/trezor/blockbook/bchain/coins/dash"
)

// firstBlockWithSpecialTransactions is 0: DIP3 is active from Raptoreum's
// genesis (consensus.DIP0003Enabled = true in chainparams.cpp, no activation
// height), so any block may carry special transactions.
const firstBlockWithSpecialTransactions = 0

// RaptoreumRPC is an interface to JSON-RPC raptoreumd service. It behaves like
// DashRPC with Raptoreum's network parameters, except that blocks are read
// in one getblock call with verbosity 2.
type RaptoreumRPC struct {
	*dash.DashRPC
}

// NewRaptoreumRPC returns new RaptoreumRPC instance
func NewRaptoreumRPC(config json.RawMessage, pushHandler func(bchain.NotificationType)) (bchain.BlockChain, error) {
	b, err := dash.NewDashRPC(config, pushHandler)
	if err != nil {
		return nil, err
	}

	s := &RaptoreumRPC{
		DashRPC: b.(*dash.DashRPC),
	}
	s.FirstBlockWithSpecialTransactions = firstBlockWithSpecialTransactions
	s.RPCMarshaler = jsonMarshaler{}

	return s, nil
}

// jsonMarshaler is DashRPC's positional JSONMarshalerV1, except that getblock
// sends its verbosity as a number: V1 sends a boolean, which raptoreumd reads
// as verbosity 1 (txids only), so GetBlockFull could not get verbosity 2.
type jsonMarshaler struct {
	btc.JSONMarshalerV1
}

// Marshal converts struct passed by parameter to JSON
func (m jsonMarshaler) Marshal(v interface{}) ([]byte, error) {
	if c, ok := v.(*btc.CmdGetBlock); ok {
		return json.Marshal(struct {
			Method string        `json:"method"`
			Params []interface{} `json:"params"`
		}{c.Method, []interface{}{c.Params.BlockHash, c.Params.Verbosity}})
	}
	return m.JSONMarshalerV1.Marshal(v)
}

// Initialize initializes RaptoreumRPC instance.
func (b *RaptoreumRPC) Initialize() error {
	ci, err := b.GetChainInfo()
	if err != nil {
		return err
	}

	params := GetChainParams(ci.Chain)

	// always create parser
	b.Parser = NewRaptoreumParser(params, b.ChainConfig)

	// raptoreumd has no estimatefee RPC, only estimatesmartfee
	b.ChainConfig.SupportsEstimateFee = false

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

// GetBlock returns block with given hash or height. raptoreumd answers
// getblock with verbosity 2 (every transaction decoded, special transactions
// included), so the block takes one call instead of DashRPC's call per
// transaction.
func (b *RaptoreumRPC) GetBlock(hash string, height uint32) (*bchain.Block, error) {
	var err error
	if hash == "" {
		hash, err = b.GetBlockHash(height)
		if err != nil {
			return nil, err
		}
	}
	return b.BitcoinRPC.GetBlockFull(hash)
}
