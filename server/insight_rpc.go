package server

import (
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

// SetBackend gives the listener raw daemon access and the coin shortcut.
// Call it before Start. A nil rpc is allowed: routes that need it return 501.
func (s *InsightServer) SetBackend(rpc InsightRPC, coinShortcut string) {
	s.rpc = rpc
	s.coinShortcut = coinShortcut
}
