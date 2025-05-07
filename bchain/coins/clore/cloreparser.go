package clore

import (
	"github.com/martinboehm/btcd/wire"
	"github.com/martinboehm/btcutil/chaincfg"
	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/bchain/coins/btc"
)

// magic numbers
const (
	MainnetMagic wire.BitcoinNet = 0x49414941
	TestnetMagic wire.BitcoinNet = 0x65566360
)

// chain parameters
var (
	MainNetParams chaincfg.Params
	TestNetParams chaincfg.Params
)

func init() {
	MainNetParams = chaincfg.MainNetParams
	MainNetParams.Net = MainnetMagic
	MainNetParams.PubKeyHashAddrID = []byte{23}
	MainNetParams.ScriptHashAddrID = []byte{122}

	TestNetParams = chaincfg.TestNet3Params
	TestNetParams.Net = TestnetMagic
	TestNetParams.PubKeyHashAddrID = []byte{42}
	TestNetParams.ScriptHashAddrID = []byte{124}
}

// CloreParser handle
type CloreParser struct {
	*btc.BitcoinLikeParser
	baseparser *bchain.BaseParser
}

// NewCloreParser returns new CloreParser instance
func NewCloreParser(params *chaincfg.Params, c *btc.Configuration) *CloreParser {
	return &CloreParser{
		BitcoinLikeParser: btc.NewBitcoinLikeParser(params, c),
		baseparser:        &bchain.BaseParser{},
	}
}

// GetChainParams contains network parameters
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

// PackTx packs transaction to byte array using protobuf
func (p *CloreParser) PackTx(tx *bchain.Tx, height uint32, blockTime int64) ([]byte, error) {
	return p.baseparser.PackTx(tx, height, blockTime)
}

// UnpackTx unpacks transaction from protobuf byte array
func (p *CloreParser) UnpackTx(buf []byte) (*bchain.Tx, uint32, error) {
	return p.baseparser.UnpackTx(buf)
}
