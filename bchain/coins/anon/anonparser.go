package anon

import (
	"github.com/martinboehm/btcd/wire"
	"github.com/martinboehm/btcutil/chaincfg"
	"github.com/trezor/blockbook/bchain/coins/btc"
	"github.com/trezor/blockbook/bchain/coins/zec"
)

// Anon (ANON, Anonymous Bitcoin) is a Zclassic-lineage zcashd fork
// (anonymousbitcoin/anon). Its transactions are Zcash's pre-Overwinter ones:
// version 1 (transparent), version 2 (PHGR Sprout JoinSplits) and version -3
// (0xfffffffd, Groth16 Sprout JoinSplits without an Overwinter header). btcd
// cannot decode versions 2 and -3, so raw tx hex is never parsed: the Zcash
// parser stores txs with the BaseParser protobuf path and the RPC reads
// every tx as verbose JSON. Only the base58 address versions and the network
// magic differ from Zcash; the values come from src/chainparams.cpp of
// anonymousbitcoin/anon at tag v.2.2.0-62.
const (
	// MainnetMagic is mainnet network constant, pchMessageStart 83 d8 47 a7 read little endian
	MainnetMagic wire.BitcoinNet = 0xa747d883
)

var (
	// MainNetParams are parser parameters for mainnet
	MainNetParams chaincfg.Params
)

// Only mainnet is registered: ANON's testnet reuses Zcash's testnet magic
// 0xbff91afa, which the zec and snowgem packages already register.

func init() {
	MainNetParams = chaincfg.MainNetParams
	MainNetParams.Net = MainnetMagic

	// Address encoding magics (two bytes, like Zcash)
	MainNetParams.AddressMagicLen = 2
	MainNetParams.PubKeyHashAddrID = []byte{0x05, 0x82} // base58 prefix: An
	MainNetParams.ScriptHashAddrID = []byte{0x53, 0x89} // base58 prefix: 3Z
	MainNetParams.PrivateKeyID = []byte{0x80}

	// BIP32 versions are Bitcoin's xpub/xprv (0488b21e/0488ade4), inherited
	// from chaincfg.MainNetParams.
}

// AnonParser handle
type AnonParser struct {
	*zec.ZCashParser
}

// NewAnonParser returns new AnonParser instance
func NewAnonParser(params *chaincfg.Params, c *btc.Configuration) *AnonParser {
	return &AnonParser{ZCashParser: zec.NewZCashParser(params, c)}
}

// GetChainParams returns network parameters for the Anon main network,
// which is used for every chain name anond reports.
func GetChainParams(chain string) *chaincfg.Params {
	if !chaincfg.IsRegistered(&MainNetParams) {
		err := chaincfg.Register(&MainNetParams)
		if err != nil {
			panic(err)
		}
	}
	return &MainNetParams
}
