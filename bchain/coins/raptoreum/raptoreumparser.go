package raptoreum

import (
	"github.com/martinboehm/btcd/wire"
	"github.com/martinboehm/btcutil/chaincfg"
	"github.com/trezor/blockbook/bchain/coins/btc"
	"github.com/trezor/blockbook/bchain/coins/dash"
)

// Raptoreum (RTM) is a Dash Core 0.17 fork (Raptor3um/raptoreum, 2.0.3.01).
// It keeps Dash's DIP2 transaction format (nVersion | nType<<16 and an extra
// payload after the locktime) and adds its own special types: 7 FUTURE,
// 8 NEW_ASSET, 9 UPDATE_ASSET and 10 MINT_ASSET. Those numbers clash with
// later Dash types, so Blockbook reads Raptoreum transactions from the
// daemon's JSON (parse false) and never decodes the payloads as Dash ones.
// The values below come from src/chainparams.cpp at tag 2.0.3.01-mainnet.
const (
	// MainnetMagic is mainnet network constant, pchMessageStart 72 74 6d 2e ("rtm.") read little endian
	MainnetMagic wire.BitcoinNet = 0x2e6d7472
	// TestnetMagic is testnet network constant, pchMessageStart 74 72 74 6d ("trtm") read little endian
	TestnetMagic wire.BitcoinNet = 0x6d747274
)

var (
	// MainNetParams are parser parameters for mainnet
	MainNetParams chaincfg.Params
	// TestNetParams are parser parameters for testnet
	TestNetParams chaincfg.Params
)

// Raptoreum regtest is not supported: its pchMessageStart (fc c1 b7 dc) equals
// Dash's, and chaincfg refuses two registered networks with the same magic.

func init() {
	MainNetParams = chaincfg.MainNetParams
	MainNetParams.Net = MainnetMagic

	// Address encoding magics
	MainNetParams.PubKeyHashAddrID = []byte{60} // base58 prefix: R
	MainNetParams.ScriptHashAddrID = []byte{16} // base58 prefix: 7
	// Raptoreum has no segwit addresses; an HRP of its own keeps bech32
	// decoding from accepting Bitcoin's.
	MainNetParams.Bech32HRPSegwit = "rtm"

	TestNetParams = chaincfg.TestNet3Params
	TestNetParams.Net = TestnetMagic

	// Address encoding magics
	TestNetParams.PubKeyHashAddrID = []byte{123} // base58 prefix: r
	TestNetParams.ScriptHashAddrID = []byte{19}  // base58 prefix: 8 or 9
	TestNetParams.Bech32HRPSegwit = "trtm"
}

// RaptoreumParser handle. Transactions are packed with protobuf, as Dash's
// are (DashParser.PackTx and UnpackTx).
type RaptoreumParser struct {
	*dash.DashParser
}

// NewRaptoreumParser returns new RaptoreumParser instance
func NewRaptoreumParser(params *chaincfg.Params, c *btc.Configuration) *RaptoreumParser {
	return &RaptoreumParser{DashParser: dash.NewDashParser(params, c)}
}

// GetChainParams contains network parameters for the main Raptoreum network
// and the test Raptoreum network
func GetChainParams(chain string) *chaincfg.Params {
	if !chaincfg.IsRegistered(&MainNetParams) {
		err := chaincfg.Register(&MainNetParams)
		if err == nil {
			err = chaincfg.Register(&TestNetParams)
		}
		if err != nil {
			panic(err)
		}
	}
	switch chain {
	case "test":
		return &TestNetParams
	default:
		return &MainNetParams
	}
}
