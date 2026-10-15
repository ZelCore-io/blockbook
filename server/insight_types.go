package server

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"strings"

	"github.com/martinboehm/btcd/txscript"
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

type insightUtxo struct {
	Address       string      `json:"address"`
	Txid          string      `json:"txid"`
	Vout          int32       `json:"vout"`
	Amount        json.Number `json:"amount"`
	Satoshis      int64       `json:"satoshis"`
	Height        int         `json:"height"`
	Confirmations int         `json:"confirmations"`
	Coinbase      bool        `json:"coinbase,omitempty"`
}
type insightScript struct {
	Addresses []string `json:"addresses,omitempty"`
	Hex       string   `json:"hex,omitempty"`
	Asm       string   `json:"asm,omitempty"`
}
type insightVin struct {
	Txid      string        `json:"txid,omitempty"`
	Vout      uint32        `json:"vout"`
	Sequence  int64         `json:"sequence"`
	N         int           `json:"n"`
	Addr      *string       `json:"addr"`
	Value     json.Number   `json:"value"`
	ValueSat  int64         `json:"valueSat"`
	Coinbase  string        `json:"coinbase,omitempty"`
	ScriptSig insightScript `json:"scriptSig"`
}
type insightVout struct {
	Value        json.Number   `json:"value"`
	N            int           `json:"n"`
	ScriptPubKey insightScript `json:"scriptPubKey"`
}
type insightTx struct {
	Txid          string        `json:"txid"`
	Version       int32         `json:"version"`
	Locktime      uint32        `json:"locktime"`
	Time          int64         `json:"time"`
	Blocktime     int64         `json:"blocktime"`
	Blockheight   int           `json:"blockheight"`
	Blockhash     string        `json:"blockhash,omitempty"`
	Confirmations uint32        `json:"confirmations"`
	Fees          json.Number   `json:"fees"`
	Vin           []insightVin  `json:"vin"`
	Vout          []insightVout `json:"vout"`
	Hex           string        `json:"hex,omitempty"`
}

func insightTransaction(tx *api.Tx) (*insightTx, error) {
	if tx == nil {
		return nil, errors.New("missing transaction")
	}
	fees, _, err := insightMoney(tx.FeesSat)
	if err != nil {
		return nil, err
	}
	out := &insightTx{Txid: tx.Txid, Version: tx.Version, Locktime: tx.Locktime, Time: tx.Blocktime, Blocktime: tx.Blocktime, Blockheight: tx.Blockheight, Blockhash: tx.Blockhash, Confirmations: tx.Confirmations, Fees: fees, Hex: tx.Hex, Vin: make([]insightVin, 0, len(tx.Vin)), Vout: make([]insightVout, 0, len(tx.Vout))}
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
		out.Vin = append(out.Vin, insightVin{Txid: v.Txid, Vout: v.Vout, Sequence: v.Sequence, N: v.N, Addr: addr, Value: value, ValueSat: sat, Coinbase: v.Coinbase, ScriptSig: insightScript{Hex: v.Hex, Asm: v.Asm}})
	}
	for _, v := range tx.Vout {
		value, _, err := insightMoney(v.ValueSat)
		if err != nil {
			return nil, err
		}
		script := insightScript{Hex: v.Hex, Asm: v.Asm}
		if v.IsAddress {
			script.Addresses = v.Addresses
		}
		if v.Hex != "" {
			if raw, err := hex.DecodeString(v.Hex); err == nil {
				if asm, err := txscript.DisasmString(raw); err == nil {
					script.Asm = asm
				}
			}
		}
		out.Vout = append(out.Vout, insightVout{Value: value, N: v.N, ScriptPubKey: script})
	}
	return out, nil
}
