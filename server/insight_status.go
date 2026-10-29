package server

import (
	"errors"
	"net/http"

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
	return map[string]interface{}{"info": map[string]interface{}{"blocks": b.Blocks, "version": insightStatusNumber(b.Version), "protocolversion": insightStatusNumber(b.ProtocolVersion), "difficulty": insightStatusNumber(b.Difficulty), "errors": ""}}, 0, nil
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
