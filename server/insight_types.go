package server

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"strings"

	"github.com/martinboehm/btcd/txscript"
	btxscript "github.com/martinboehm/btcutil/txscript"
	"github.com/trezor/blockbook/api"
)

// Amounts remain integer-backed until their final JSON representation. Insight
// clients spend numeric satoshi fields in JavaScript, so unsafe integers fail.
func insightMoney(a *api.Amount) (json.Number, int64, error) {
	n := a.AsBigInt()
	if n.Cmp(big.NewInt(9007199254740991)) > 0 || n.Cmp(big.NewInt(-9007199254740991)) < 0 {
		return "", 0, errors.New("unsafe Insight amount")
	}
	sat := n.Int64()
	negative := n.Sign() < 0
	n.Abs(&n)
	digits := n.String()
	for len(digits) <= 8 {
		digits = "0" + digits
	}
	value := strings.TrimRight(strings.TrimRight(digits[:len(digits)-8]+"."+digits[len(digits)-8:], "0"), ".")
	if negative {
		value = "-" + value
	}
	return json.Number(value), sat, nil
}

type insightSummary struct {
	Address               string      `json:"addrStr"`
	Balance               json.Number `json:"balance"`
	BalanceSat            string      `json:"balanceSat"`
	TotalReceived         json.Number `json:"totalReceived"`
	TotalReceivedSat      int64       `json:"totalReceivedSat"`
	TotalSent             json.Number `json:"totalSent"`
	TotalSentSat          int64       `json:"totalSentSat"`
	UnconfirmedBalance    json.Number `json:"unconfirmedBalance"`
	UnconfirmedBalanceSat int64       `json:"unconfirmedBalanceSat"`
	UnconfirmedTxs        int         `json:"unconfirmedTxApperances"`
	Txs                   int         `json:"txApperances"`
}

func insightAddress(a *api.Address) (*insightSummary, error) {
	if a == nil {
		return nil, errors.New("missing address")
	}
	balance, sat, err := insightMoney(a.BalanceSat)
	if err != nil {
		return nil, err
	}
	received, rs, err := insightMoney(a.TotalReceivedSat)
	if err != nil {
		return nil, err
	}
	sent, ss, err := insightMoney(a.TotalSentSat)
	if err != nil {
		return nil, err
	}
	pending, ps, err := insightMoney(a.UnconfirmedBalanceSat)
	if err != nil {
		return nil, err
	}
	return &insightSummary{a.AddrStr, balance, big.NewInt(sat).String(), received, rs, sent, ss, pending, ps, a.UnconfirmedTxs, a.Txs}, nil
}

// insightUtxo is a stock utxo row. scriptPubKey is the output script of the
// address (see insightAddressScript), omitted when it cannot be derived.
type insightUtxo struct {
	Address       string      `json:"address"`
	Txid          string      `json:"txid"`
	Vout          int32       `json:"vout"`
	ScriptPubKey  string      `json:"scriptPubKey,omitempty"`
	Amount        json.Number `json:"amount"`
	Satoshis      int64       `json:"satoshis"`
	Height        int         `json:"height"`
	Confirmations int         `json:"confirmations"`
	Coinbase      bool        `json:"coinbase,omitempty"`
}

// insightScript is stock's vin scriptSig.
type insightScript struct {
	Hex string `json:"hex,omitempty"`
	Asm string `json:"asm,omitempty"`
}

// The transaction shape is the converter's (Blockbook v2 tx JSON plus its
// edits) with stock Insight fields added alongside. Where both use a key with
// different meanings, the converter's wins (vout value in coins, valueIn as
// a satoshi string). asm always comes from script bytes, never from address
// text (controller ruling).

// insightVinScript is the converter's vin.scriptPubKey: a clone of the vin
// without addr, scriptSig, doubleSpentTxID and asm.
type insightVinScript struct {
	Txid      string      `json:"txid,omitempty"`
	Vout      uint32      `json:"vout"`
	Sequence  int64       `json:"sequence"`
	N         int         `json:"n"`
	Addresses []string    `json:"addresses,omitempty"`
	IsAddress bool        `json:"isAddress"`
	IsOwn     bool        `json:"isOwn,omitempty"`
	Value     json.Number `json:"value"`
	ValueSat  int64       `json:"valueSat"`
	Hex       string      `json:"hex,omitempty"`
	Coinbase  string      `json:"coinbase,omitempty"`
}
type insightVin struct {
	insightVinScript
	Addr            *string          `json:"addr"`
	ScriptSig       insightScript    `json:"scriptSig"`
	DoubleSpentTxID *string          `json:"doubleSpentTxID"`
	ScriptPubKey    insightVinScript `json:"scriptPubKey"`
}

// insightVoutScript is the converter's vout.scriptPubKey (a clone of the
// Blockbook vout, so spending info is omitted when absent, as Blockbook
// omits it) with asm disassembled from the script hex and type classified
// from it (insightScriptType). Stock's scriptPubKey keys (hex, asm,
// addresses, type) are present when the script gives them.
type insightVoutScript struct {
	Value       json.Number `json:"value"`
	N           int         `json:"n"`
	Spent       bool        `json:"spent,omitempty"`
	SpentTxID   string      `json:"spentTxId,omitempty"`
	SpentIndex  int         `json:"spentIndex,omitempty"`
	SpentHeight int         `json:"spentHeight,omitempty"`
	Hex         string      `json:"hex,omitempty"`
	Addresses   []string    `json:"addresses"`
	IsAddress   bool        `json:"isAddress"`
	IsOwn       bool        `json:"isOwn,omitempty"`
	Type        string      `json:"type,omitempty"`
	Asm         string      `json:"asm,omitempty"`
}

// insightVout: spentTxId/spentIndex/spentHeight are null for an unspent (or
// not looked up) output, as in stock; the converter never had these keys.
type insightVout struct {
	Value        json.Number       `json:"value"`
	N            int               `json:"n"`
	Spent        bool              `json:"spent,omitempty"`
	SpentTxID    *string           `json:"spentTxId"`
	SpentIndex   *int              `json:"spentIndex"`
	SpentHeight  *int              `json:"spentHeight"`
	Hex          string            `json:"hex,omitempty"`
	Addresses    []string          `json:"addresses"`
	IsAddress    bool              `json:"isAddress"`
	IsOwn        bool              `json:"isOwn,omitempty"`
	Type         string            `json:"type,omitempty"`
	ScriptPubKey insightVoutScript `json:"scriptPubKey"`
}
type insightTx struct {
	Txid                   string                `json:"txid"`
	Version                int32                 `json:"version"`
	Locktime               uint32                `json:"locktime"`
	LockTime               uint32                `json:"lockTime,omitempty"`
	Blockhash              string                `json:"blockhash,omitempty"`
	BlockHash              string                `json:"blockHash,omitempty"`
	Blockheight            int                   `json:"blockheight"`
	BlockHeight            int                   `json:"blockHeight"`
	Blocktime              int64                 `json:"blocktime"`
	BlockTime              int64                 `json:"blockTime"`
	Time                   int64                 `json:"time"`
	Confirmations          uint32                `json:"confirmations"`
	ConfirmationETABlocks  uint32                `json:"confirmationETABlocks,omitempty"`
	ConfirmationETASeconds int64                 `json:"confirmationETASeconds,omitempty"`
	Size                   int                   `json:"size,omitempty"`
	VSize                  int                   `json:"vsize,omitempty"`
	Value                  string                `json:"value"`
	ValueIn                string                `json:"valueIn,omitempty"`
	ValueOut               json.Number           `json:"valueOut"`
	Fees                   json.Number           `json:"fees"`
	IsCoinBase             bool                  `json:"isCoinBase,omitempty"`
	Rbf                    bool                  `json:"rbf,omitempty"`
	Hex                    string                `json:"hex,omitempty"`
	CoinSpecificData       json.RawMessage       `json:"coinSpecificData,omitempty"`
	ChainExtraData         *api.TxChainExtraData `json:"chainExtraData,omitempty"`
	Vin                    []insightVin          `json:"vin"`
	Vout                   []insightVout         `json:"vout"`
	// SparkData is set by the Firo extension on a Spark name registration.
	SparkData *insightSparkData `json:"sparkData,omitempty"`
}

// insightSatString renders an amount as a decimal satoshi string. Strings
// carry any size, so there is no safe-integer bound (only JSON numbers have
// one, in insightMoney).
func insightSatString(a *api.Amount) string {
	n := a.AsBigInt()
	return n.String()
}

// insightTransaction is the one transaction transform shared by history,
// tx/:txid, txs?block= and multi-address history.
func insightTransaction(tx *api.Tx) (*insightTx, error) {
	if tx == nil {
		return nil, errors.New("missing transaction")
	}
	fees, _, err := insightMoney(tx.FeesSat)
	if err != nil {
		return nil, err
	}
	valueOut, _, err := insightMoney(tx.ValueOutSat)
	if err != nil {
		return nil, err
	}
	out := &insightTx{
		Txid: tx.Txid, Version: tx.Version, Locktime: tx.Locktime, LockTime: tx.Locktime,
		Blockhash: tx.Blockhash, BlockHash: tx.Blockhash, Blockheight: tx.Blockheight, BlockHeight: tx.Blockheight,
		Blocktime: tx.Blocktime, BlockTime: tx.Blocktime, Time: tx.Blocktime, Confirmations: tx.Confirmations,
		ConfirmationETABlocks: tx.ConfirmationETABlocks, ConfirmationETASeconds: tx.ConfirmationETASeconds,
		Size: tx.Size, VSize: tx.VSize, Value: insightSatString(tx.ValueOutSat), ValueOut: valueOut, Fees: fees,
		Rbf: tx.Rbf, Hex: tx.Hex, CoinSpecificData: tx.CoinSpecificData, ChainExtraData: tx.ChainExtraData,
		Vin: make([]insightVin, 0, len(tx.Vin)), Vout: make([]insightVout, 0, len(tx.Vout)),
	}
	if tx.ValueInSat != nil {
		out.ValueIn = insightSatString(tx.ValueInSat)
	}
	out.IsCoinBase = len(tx.Vin) > 0 && tx.Vin[0].Coinbase != ""
	for _, v := range tx.Vin {
		value, sat, err := insightMoney(v.ValueSat)
		if err != nil {
			return nil, err
		}
		var addr *string
		if v.IsAddress && len(v.Addresses) > 0 {
			a := v.Addresses[0]
			addr = &a
		}
		clone := insightVinScript{Txid: v.Txid, Vout: v.Vout, Sequence: v.Sequence, N: v.N, Addresses: v.Addresses, IsAddress: v.IsAddress, IsOwn: v.IsOwn, Value: value, ValueSat: sat, Hex: v.Hex, Coinbase: v.Coinbase}
		out.Vin = append(out.Vin, insightVin{insightVinScript: clone, Addr: addr, ScriptSig: insightScript{Hex: v.Hex, Asm: v.Asm}, ScriptPubKey: clone})
	}
	for _, v := range tx.Vout {
		value, _, err := insightMoney(v.ValueSat)
		if err != nil {
			return nil, err
		}
		asm, scriptType := v.Asm, v.Type
		if v.Hex != "" {
			if raw, err := hex.DecodeString(v.Hex); err == nil {
				if disasm, err := txscript.DisasmString(raw); err == nil {
					asm = disasm
				}
				if scriptType == "" {
					scriptType = insightScriptType(raw)
				}
			}
		}
		vout := insightVout{
			Value: value, N: v.N, Spent: v.Spent, Hex: v.Hex, Addresses: v.Addresses, IsAddress: v.IsAddress, IsOwn: v.IsOwn, Type: v.Type,
			ScriptPubKey: insightVoutScript{Value: value, N: v.N, Spent: v.Spent, SpentTxID: v.SpentTxID, SpentIndex: v.SpentIndex, SpentHeight: v.SpentHeight, Hex: v.Hex, Addresses: v.Addresses, IsAddress: v.IsAddress, IsOwn: v.IsOwn, Type: scriptType, Asm: asm},
		}
		if v.SpentTxID != "" {
			spentTxID, spentIndex, spentHeight := v.SpentTxID, v.SpentIndex, v.SpentHeight
			vout.SpentTxID, vout.SpentIndex, vout.SpentHeight = &spentTxID, &spentIndex, &spentHeight
		}
		out.Vout = append(out.Vout, vout)
	}
	return out, nil
}

// insightScriptType names a standard output script the way stock Insight's
// scriptPubKey.type does: the bitcore address type (pubkeyhash, scripthash,
// witnesspubkeyhash, witnessscripthash, taproot; a P2PK output is indexed
// under its P2PKH address, so it is pubkeyhash too), and bitcoind's names for
// the address-less standard forms (nulldata, multisig), which stock leaves
// out. Anything else is "" (omitted); a coin extension may label it.
func insightScriptType(script []byte) string {
	switch btxscript.GetScriptClass(script) {
	case btxscript.PubKeyHashTy, btxscript.PubKeyTy:
		return "pubkeyhash"
	case btxscript.ScriptHashTy:
		return "scripthash"
	case btxscript.WitnessV0PubKeyHashTy:
		return "witnesspubkeyhash"
	case btxscript.WitnessV0ScriptHashTy:
		return "witnessscripthash"
	case btxscript.WitnessV1TaprootTy:
		return "taproot"
	case btxscript.NullDataTy:
		return "nulldata"
	case btxscript.MultiSigTy:
		return "multisig"
	}
	return ""
}

// insightBlock is the stock block/:hash shape. chainwork, previousblockhash
// and nextblockhash are omitted when unknown (no RPC, genesis, tip).
type insightBlock struct {
	Hash          string      `json:"hash"`
	Size          int         `json:"size"`
	Height        uint32      `json:"height"`
	Version       interface{} `json:"version"`
	MerkleRoot    string      `json:"merkleroot"`
	Tx            []string    `json:"tx"`
	Time          int64       `json:"time"`
	Nonce         interface{} `json:"nonce"`
	Bits          string      `json:"bits"`
	Difficulty    interface{} `json:"difficulty"`
	Chainwork     string      `json:"chainwork,omitempty"`
	Confirmations int         `json:"confirmations"`
	Prev          string      `json:"previousblockhash,omitempty"`
	Next          string      `json:"nextblockhash,omitempty"`
	Reward        json.Number `json:"reward"`
	IsMainChain   bool        `json:"isMainChain"`
	PoolInfo      struct{}    `json:"poolInfo"`
}

// insightBlockRow is one row of the blocks-by-date list. Stock's per-row
// difficulty is omitted: the block index does not store it.
type insightBlockRow struct {
	Height      uint32   `json:"height"`
	Size        uint32   `json:"size"`
	Hash        string   `json:"hash"`
	Time        int64    `json:"time"`
	TxLength    uint32   `json:"txlength"`
	PoolInfo    struct{} `json:"poolInfo"`
	IsMainChain bool     `json:"isMainChain"`
}

type insightBlockPagination struct {
	Next      string `json:"next"`
	Prev      string `json:"prev"`
	CurrentTs int64  `json:"currentTs"`
	Current   string `json:"current"`
	IsToday   bool   `json:"isToday"`
	More      bool   `json:"more"`
	MoreTs    *int64 `json:"moreTs,omitempty"`
}

type insightBlockList struct {
	Blocks     []insightBlockRow      `json:"blocks"`
	Length     int                    `json:"length"`
	Pagination insightBlockPagination `json:"pagination"`
}

// insightNumberOrString renders a numeric daemon string as a JSON number, as
// stock does for nonce and difficulty, and keeps anything else as a string.
func insightNumberOrString(value string) interface{} {
	if value != "" && (value[0] == '-' || value[0] >= '0' && value[0] <= '9') && json.Valid([]byte(value)) {
		return json.Number(value)
	}
	return value
}
