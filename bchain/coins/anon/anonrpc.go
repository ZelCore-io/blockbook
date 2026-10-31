package anon

import (
	"encoding/json"

	"github.com/golang/glog"
	"github.com/juju/errors"
	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/bchain/coins/btc"
	"github.com/trezor/blockbook/bchain/coins/zec"
)

// AnonRPC is an interface to JSON-RPC anond service.
//
// anond's getblock takes a boolean verbose argument only (true: JSON with
// txids, false: hex) and rejects the numeric verbosity that ZCashRPC sends
// ("JSON value is not a boolean as expected"). AnonRPC therefore marshals
// every call with btc.JSONMarshalerV1, which turns the getblock verbosity
// into true/false, reads a block as getblock <hash> true followed by
// getrawtransaction <txid> 1 for each tx (as SnowGem does), and never decodes
// raw tx or block hex. Transactions, mempool txs and chain info come from
// ZCashRPC, which reads verbose tx JSON and keeps it as CoinSpecificData for
// the Insight listener's zcash extension.
type AnonRPC struct {
	*zec.ZCashRPC
}

// NewAnonRPC returns new AnonRPC instance
func NewAnonRPC(config json.RawMessage, pushHandler func(bchain.NotificationType)) (bchain.BlockChain, error) {
	b, err := zec.NewZCashRPC(config, pushHandler)
	if err != nil {
		return nil, err
	}
	z := &AnonRPC{ZCashRPC: b.(*zec.ZCashRPC)}
	z.RPCMarshaler = btc.JSONMarshalerV1{}
	z.ChainConfig.SupportsEstimateSmartFee = false
	// The batched mempool resync decodes raw tx hex with btcd, which fails on
	// version 2 and -3 txs; mempool txs are read one by one as verbose JSON.
	z.ChainConfig.MempoolResyncBatchSize = 1
	return z, nil
}

// Initialize initializes AnonRPC instance.
func (z *AnonRPC) Initialize() error {
	ci, err := z.GetChainInfo()
	if err != nil {
		return err
	}

	params := GetChainParams(ci.Chain)

	// always create parser
	z.Parser = NewAnonParser(params, z.ChainConfig)

	// parameters for getInfo request
	z.Testnet = false
	z.Network = "livenet"

	glog.Info("rpc: block chain ", params.Name, " (anond chain ", ci.Chain, ")")

	return nil
}

// GetBlock returns block with given hash: getblock <hash> true for the header
// and txids, then getrawtransaction <txid> 1 for every tx.
func (z *AnonRPC) GetBlock(hash string, height uint32) (*bchain.Block, error) {
	var err error
	if hash == "" {
		hash, err = z.GetBlockHash(height)
		if err != nil {
			return nil, err
		}
	}

	glog.V(1).Info("rpc: getblock (verbose=true) ", hash)

	res := btc.ResGetBlockThin{}
	req := btc.CmdGetBlock{Method: "getblock"}
	req.Params.BlockHash = hash
	req.Params.Verbosity = 1 // marshalled as true by JSONMarshalerV1
	err = z.Call(&req, &res)
	if err != nil {
		return nil, errors.Annotatef(err, "hash %v", hash)
	}
	if res.Error != nil {
		if btc.IsErrBlockNotFound(res.Error) {
			return nil, bchain.ErrBlockNotFound
		}
		return nil, errors.Annotatef(res.Error, "hash %v", hash)
	}

	txs := make([]bchain.Tx, 0, len(res.Result.Txids))
	for _, txid := range res.Result.Txids {
		tx, err := z.GetTransaction(txid)
		if err != nil {
			// A block with a tx left out would corrupt the index: fail and
			// let the sync retry.
			return nil, errors.Annotatef(err, "tx %v in block %v", txid, hash)
		}
		txs = append(txs, *tx)
	}
	return &bchain.Block{
		BlockHeader: res.Result.BlockHeader,
		Txs:         txs,
	}, nil
}

// GetBlockFull returns block with given hash. anond has no verbosity 2, so
// it is the same as GetBlock.
func (z *AnonRPC) GetBlockFull(hash string) (*bchain.Block, error) {
	return z.GetBlock(hash, 0)
}

// GetBlockRaw returns block with given hash as hex string (getblock <hash>
// false). The hex is passed through to the API as it is and never decoded.
func (z *AnonRPC) GetBlockRaw(hash string) (string, error) {
	return z.BitcoinRPC.GetBlockRaw(hash)
}
