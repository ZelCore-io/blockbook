package raptoreum

import (
	"github.com/martinboehm/btcd/txscript"
	"github.com/martinboehm/btcd/wire"
	"github.com/martinboehm/btcutil"
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
	baseOutputScriptToAddressesFunc btc.OutputScriptToAddressesFunc
}

// NewRaptoreumParser returns new RaptoreumParser instance
func NewRaptoreumParser(params *chaincfg.Params, c *btc.Configuration) *RaptoreumParser {
	p := &RaptoreumParser{DashParser: dash.NewDashParser(params, c)}
	// Hook the display of addresses only. The address descriptor stays the
	// whole output script (GetAddrDescFromVout is not touched), so the index
	// and the electrum scripthashes are the same as before.
	p.baseOutputScriptToAddressesFunc = p.OutputScriptToAddressesFunc
	p.OutputScriptToAddressesFunc = p.outputScriptToAddresses
	return p
}

const (
	// opAssetID is Raptoreum's OP_ASSET_ID (0xbc), which follows the P2PKH
	// part of an asset transfer output.
	opAssetID = 0xbc
	// p2pkhScriptLen is the length of OP_DUP OP_HASH160 <push 20> <hash> OP_EQUALVERIFY OP_CHECKSIG
	p2pkhScriptLen = 25
)

// assetTransferPubKeyHash returns the 20-byte public key hash of an asset
// transfer output script, or nil if the script is not one. Such a script is
// exactly a P2PKH script followed by OP_ASSET_ID and at least one byte of
// asset data (daemon type "transferasset").
func assetTransferPubKeyHash(script []byte) []byte {
	if len(script) <= p2pkhScriptLen+1 ||
		script[0] != txscript.OP_DUP ||
		script[1] != txscript.OP_HASH160 ||
		script[2] != txscript.OP_DATA_20 ||
		script[23] != txscript.OP_EQUALVERIFY ||
		script[24] != txscript.OP_CHECKSIG ||
		script[p2pkhScriptLen] != opAssetID {
		return nil
	}
	return script[3:23]
}

// outputScriptToAddresses shows the recipient of an asset transfer output as
// the P2PKH address in its script, as raptoreumd does. The address is not
// searchable, because the descriptor of the output is the whole script and
// not the P2PKH one. Every other script is handled as in Dash.
func (p *RaptoreumParser) outputScriptToAddresses(script []byte) ([]string, bool, error) {
	if hash := assetTransferPubKeyHash(script); hash != nil {
		a, err := btcutil.NewAddressPubKeyHash(hash, p.Params)
		if err != nil {
			return nil, false, err
		}
		return []string{a.EncodeAddress()}, false, nil
	}
	return p.baseOutputScriptToAddressesFunc(script)
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
