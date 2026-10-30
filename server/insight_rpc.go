package server

import (
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/trezor/blockbook/bchain"
)

// InsightRPC calls one daemon JSON-RPC method and decodes its result into
// result. A daemon error is returned as *bchain.RPCError; a JSON-RPC "method
// not found" error has Code -32601. Handlers must never echo the error text.
type InsightRPC func(method string, params []interface{}, result interface{}) error

type insightRPCRequest struct {
	Method string        `json:"method"`
	Params []interface{} `json:"params"`
}

// RPCMethod names the daemon method for the raw-call latency metric label.
func (r *insightRPCRequest) RPCMethod() string { return r.Method }

type insightRPCResponse struct {
	Result json.RawMessage  `json:"result"`
	Error  *bchain.RPCError `json:"error"`
}

// NewInsightRPC adapts a raw daemon caller (BitcoinRPC.Call) to InsightRPC.
func NewInsightRPC(call func(req interface{}, res interface{}) error) InsightRPC {
	return func(method string, params []interface{}, result interface{}) error {
		if params == nil {
			params = []interface{}{}
		}
		var res insightRPCResponse
		if err := call(&insightRPCRequest{Method: method, Params: params}, &res); err != nil {
			return err
		}
		if res.Error != nil {
			return res.Error
		}
		if result == nil {
			return nil
		}
		if len(res.Result) == 0 {
			return errors.New("missing RPC result")
		}
		return json.Unmarshal(res.Result, result)
	}
}

// SetChainParser gives the listener the chain parser, which maps an address
// to its output script for the scriptPubKey of utxo rows. Call it before
// Start. Without it utxo rows have no scriptPubKey.
func (s *InsightServer) SetChainParser(parser bchain.BlockChainParser) {
	s.parser = parser
}

// insightAddressScript returns the hex output script of addr: the script of
// its address descriptor, which is what the index keys the address's outputs
// by. For a P2PKH, P2SH or witness address that is the output script itself.
// Outputs Blockbook folds into another address's descriptor are shown with
// that descriptor's script: a P2PK output with its P2PKH script (stock shows
// the P2PK script), and any output a coin parser maps onto a standard
// descriptor (Firo's Spark name fee outputs) with the standard script. ""
// when there is no parser or the address does not parse.
func (s *InsightServer) insightAddressScript(addr string) string {
	if s.parser == nil {
		return ""
	}
	desc, err := s.parser.GetAddrDescFromAddress(addr)
	if err != nil || len(desc) == 0 {
		return ""
	}
	script, err := s.parser.GetScriptFromAddrDesc(desc)
	if err != nil {
		return ""
	}
	return hex.EncodeToString(script)
}

// SetBackend gives the listener raw daemon access and the coin shortcut.
// Call it before Start. A nil rpc is allowed: routes that need it return 501.
func (s *InsightServer) SetBackend(rpc InsightRPC, coinShortcut string) {
	s.rpc = rpc
	s.coinShortcut = coinShortcut
}
