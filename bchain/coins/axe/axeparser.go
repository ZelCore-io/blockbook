package axe

import (
	"github.com/martinboehm/btcd/wire"
	"github.com/martinboehm/btcutil/chaincfg"
	"github.com/trezor/blockbook/bchain/coins/btc"
	"github.com/trezor/blockbook/bchain/coins/dash"
)

// Axe (AXE) is a Dash Core fork (AXErunners/axe, axed 1.6.1.1). It keeps Dash's
// transaction format, including the DIP2 special transactions, so the Dash
// parser is reused with Axe's own network parameters. The values below come
// from src/chainparams.cpp of AXErunners/axe at tag v1.6.1.1.
const (
	// MainnetMagic is mainnet network constant, pchMessageStart b5 ce 6b 04 read little endian
	MainnetMagic wire.BitcoinNet = 0x046bceb5
	// TestnetMagic is testnet network constant, pchMessageStart ce e2 ca f1 read little endian
	TestnetMagic wire.BitcoinNet = 0xf1cae2ce
)

var (
	// MainNetParams are parser parameters for mainnet
	MainNetParams chaincfg.Params
	// TestNetParams are parser parameters for testnet
	TestNetParams chaincfg.Params
)

// Axe regtest is not supported: its pchMessageStart (fc c1 b7 dc) equals Dash's,
// and chaincfg refuses two registered networks with the same magic.

func init() {
	MainNetParams = chaincfg.MainNetParams
	MainNetParams.Net = MainnetMagic

	// Address encoding magics
	MainNetParams.PubKeyHashAddrID = []byte{55} // base58 prefix: P
	MainNetParams.ScriptHashAddrID = []byte{16} // base58 prefix: 7

	TestNetParams = chaincfg.TestNet3Params
	TestNetParams.Net = TestnetMagic

	// Address encoding magics
	TestNetParams.PubKeyHashAddrID = []byte{140} // base58 prefix: y
	TestNetParams.ScriptHashAddrID = []byte{19}  // base58 prefix: 8 or 9
}

// AxeParser handle
type AxeParser struct {
	*dash.DashParser
}

// NewAxeParser returns new AxeParser instance
func NewAxeParser(params *chaincfg.Params, c *btc.Configuration) *AxeParser {
	return &AxeParser{DashParser: dash.NewDashParser(params, c)}
}

// GetChainParams contains network parameters for the main Axe network and
// the test Axe network
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
