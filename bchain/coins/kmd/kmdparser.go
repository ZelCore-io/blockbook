package kmd

import (
	"github.com/martinboehm/btcd/wire"
	"github.com/martinboehm/btcutil/chaincfg"
	"github.com/trezor/blockbook/bchain/coins/btc"
	"github.com/trezor/blockbook/bchain/coins/zec"
)

// Komodo (KMD) is a zcashd fork (KomodoPlatform/komodo). Its transactions are
// Zcash's (v1/v2 before Overwinter, v4 Sapling after), so the Zcash parser and
// RPC are reused. Only the base58 address versions and the network magic
// differ from Zcash; the values below come from src/chainparams.cpp of
// KomodoPlatform/komodo.
const (
	// MainnetMagic is mainnet network constant, pchMessageStart f9 ee e4 8d read little endian
	MainnetMagic wire.BitcoinNet = 0x8de4eef9
)

var (
	// MainNetParams are parser parameters for mainnet
	MainNetParams chaincfg.Params
)

// Komodo has no public testnet with its own magic, so only mainnet is registered.

func init() {
	MainNetParams = chaincfg.MainNetParams
	MainNetParams.Net = MainnetMagic

	// Address encoding magics (one byte, unlike Zcash's two-byte t1/t3)
	MainNetParams.PubKeyHashAddrID = []byte{60} // base58 prefix: R
	MainNetParams.ScriptHashAddrID = []byte{85} // base58 prefix: b
	MainNetParams.PrivateKeyID = []byte{188}

	// BIP32 versions are Bitcoin's xpub/xprv (0488b21e/0488ade4), inherited
	// from chaincfg.MainNetParams.
}

// KomodoParser handle
type KomodoParser struct {
	*zec.ZCashParser
}

// NewKomodoParser returns new KomodoParser instance
func NewKomodoParser(params *chaincfg.Params, c *btc.Configuration) *KomodoParser {
	return &KomodoParser{ZCashParser: zec.NewZCashParser(params, c)}
}

// GetChainParams returns network parameters for the Komodo main network,
// which is used for every chain name komodod reports.
func GetChainParams(chain string) *chaincfg.Params {
	if !chaincfg.IsRegistered(&MainNetParams) {
		err := chaincfg.Register(&MainNetParams)
		if err != nil {
			panic(err)
		}
	}
	return &MainNetParams
}
