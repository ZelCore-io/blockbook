package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/trezor/blockbook/api"
	"github.com/trezor/blockbook/common"
)

// insightBackend returns the system info with a usable backend, or a 503.
func (s *InsightServer) insightBackend() (*api.SystemInfo, int, error) {
	info, err := s.worker.GetSystemInfo(false)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	if info == nil || info.Backend == nil || info.Backend.BackendError != "" {
		return nil, http.StatusServiceUnavailable, errors.New("backend unavailable")
	}
	return info, 0, nil
}

// insightLastBlockHash is a struct, not a map, so the key order is stock's
// (syncTipHash, then lastblockhash).
type insightLastBlockHash struct {
	SyncTipHash   string `json:"syncTipHash"`
	LastBlockHash string `json:"lastblockhash"`
}

// status serves status and its stock q variants; unknown q values and getInfo
// fall through to the getInfo shape, as stock's default branch does.
func (s *InsightServer) status(route insightRoute) (interface{}, int, error) {
	q := route.query.Get("q")
	if q == "getLastBlockHash" {
		blocks, err := s.worker.GetBlocks(1, 1)
		if err != nil {
			return nil, http.StatusInternalServerError, err
		}
		if blocks == nil || len(blocks.Blocks) == 0 {
			return nil, http.StatusServiceUnavailable, errors.New("no indexed block")
		}
		hash := blocks.Blocks[0].Hash
		return insightLastBlockHash{SyncTipHash: hash, LastBlockHash: hash}, 0, nil
	}
	info, code, err := s.insightBackend()
	if err != nil {
		return nil, code, err
	}
	b := info.Backend
	switch q {
	case "getDifficulty":
		return map[string]interface{}{"difficulty": insightStatusNumber(b.Difficulty)}, 0, nil
	case "getBestBlockHash":
		return map[string]string{"bestblockhash": b.BestBlockHash}, 0, nil
	}
	fields := map[string]interface{}{"blocks": b.Blocks, "version": insightStatusNumber(b.Version), "protocolversion": insightStatusNumber(b.ProtocolVersion), "difficulty": insightStatusNumber(b.Difficulty), "errors": ""}
	if network, testnet, ok := insightNetwork(b.Chain); ok {
		fields["network"], fields["testnet"] = network, testnet
	}
	if fee, ok := s.insightRelayFee(route); ok {
		fields["relayfee"] = fee
	}
	return map[string]interface{}{"info": fields}, 0, nil
}

// insightNetwork maps the daemon's chain name to bitcore's network name and
// stock's testnet flag. ok is false when the chain name is unknown (empty).
func insightNetwork(chain string) (network string, testnet bool, ok bool) {
	switch chain {
	case "":
		return "", false, false
	case "main":
		return "livenet", false, true
	case "test":
		return "testnet", true, true
	}
	return chain, true, true
}

// insightRelayFeeTTL is how long the daemon's relay fee is cached; it only
// changes with the daemon's configuration.
const insightRelayFeeTTL = 10 * time.Minute

// insightRelayFee returns the daemon's relayfee (coins/kB) from
// getnetworkinfo, cached for insightRelayFeeTTL. ok is false without daemon
// RPC or when the call fails, and status then leaves relayfee out instead of
// failing.
func (s *InsightServer) insightRelayFee(route insightRoute) (json.Number, bool) {
	if s.rpc == nil {
		return "", false
	}
	v, err := s.relayFee.get(route.ctx, insightRelayFeeTTL, func() (interface{}, error) {
		var res struct {
			RelayFee json.Number `json:"relayfee"`
		}
		if err := s.rpc("getnetworkinfo", nil, &res); err != nil {
			return nil, err
		}
		fee := string(res.RelayFee)
		if fee == "" || !json.Valid([]byte(fee)) {
			return nil, errors.New("getnetworkinfo without relayfee")
		}
		// 0.00001000 as stock's 0.00001
		if strings.Contains(fee, ".") && !strings.ContainsAny(fee, "eE") {
			fee = strings.TrimRight(strings.TrimRight(fee, "0"), ".")
		}
		return json.Number(fee), nil
	})
	if err != nil {
		return "", false
	}
	fee, ok := v.(json.Number)
	return fee, ok
}

type insightSyncInfo struct {
	Status           string      `json:"status"`
	BlockChainHeight int         `json:"blockChainHeight"`
	SyncPercentage   int64       `json:"syncPercentage"`
	Height           uint32      `json:"height"`
	Error            interface{} `json:"error"`
	Type             string      `json:"type"`
}

// sync reports the index height against the daemon's height. syncPercentage
// is rounded half up in integer arithmetic and capped at 100.
func (s *InsightServer) sync() (interface{}, int, error) {
	info, code, err := s.insightBackend()
	if err != nil {
		return nil, code, err
	}
	if info.Blockbook == nil {
		return nil, http.StatusServiceUnavailable, errors.New("index state unavailable")
	}
	bb, blocks := info.Blockbook, int64(info.Backend.Blocks)
	var pct int64
	if blocks > 0 {
		pct = (int64(bb.BestHeight)*200 + blocks) / (2 * blocks)
		if pct > 100 {
			pct = 100
		}
	}
	status := "syncing"
	if bb.InSync {
		status = "finished"
	}
	return insightSyncInfo{status, info.Backend.Blocks, pct, bb.BestHeight, nil, "bitcore node"}, 0, nil
}

// peer never exposes topology: stock's fixed local-peer shape.
func insightPeer() interface{} {
	return struct {
		Connected bool        `json:"connected"`
		Host      string      `json:"host"`
		Port      interface{} `json:"port"`
	}{true, "127.0.0.1", nil}
}

func insightVersion() interface{} {
	return map[string]string{"version": common.GetVersionInfo().Version}
}
