package server

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/martinboehm/btcutil/base58"
)

// insightRvnScript builds an asset script: owner + c0 + push(payload) + 75.
func insightRvnScript(t *testing.T, owner []byte, payload []byte) []byte {
	t.Helper()
	s := append([]byte{}, owner...)
	s = append(s, 0xc0)
	switch {
	case len(payload) <= 75:
		s = append(s, byte(len(payload)))
	case len(payload) <= 255:
		s = append(s, 0x4c, byte(len(payload)))
	default:
		t.Fatal("payload too long")
	}
	s = append(s, payload...)
	return append(s, 0x75)
}

func insightRvnPayload(kind byte, name string, parts ...[]byte) []byte {
	p := append([]byte("rvn"), kind, byte(len(name)))
	p = append(p, name...)
	for _, part := range parts {
		p = append(p, part...)
	}
	return p
}

func insightRvnLE(v int64) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, uint64(v))
	return b
}

func insightRvnHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestInsightRavencoinDecodeAsset(t *testing.T) {
	p2pkh := insightRvnHex(t, "76a914a0dfb26225355e9f0600a5839a92f99f3a98b70988ac")
	p2sh := insightRvnHex(t, "a914a0dfb26225355e9f0600a5839a92f99f3a98b70987")
	hash := bytes.Repeat([]byte{0xab}, 32)
	ipfs := base58.Encode(append([]byte{0x12, 0x20}, hash...))
	one, two, minus := 1, 2, -1
	zero := 0
	expire := int64(1700000000)
	longName := strings.Repeat("A", 30)
	tests := []struct {
		name      string
		script    []byte
		ok        bool
		assetType string
		owner     []byte
		ownerType string
		asset     insightRvnAsset
	}{
		{
			name:   "live FREN transfer",
			script: insightRvnHex(t, "76a914a0dfb26225355e9f0600a5839a92f99f3a98b70988acc01172766e74044652454e00e1f5050000000075"),
			ok:     true, assetType: "transfer_asset", owner: p2pkh, ownerType: "pubkeyhash",
			asset: insightRvnAsset{Name: "FREN", Amount: "1"},
		},
		{
			name:   "live #KYC qualifier issue",
			script: insightRvnHex(t, "76a914ce5229cff7e3c7d880e5308299c7bd3e854ba80488acc01472766e7104234b594300ca9a3b0000000000000075"),
			ok:     true, assetType: "new_asset", owner: insightRvnHex(t, "76a914ce5229cff7e3c7d880e5308299c7bd3e854ba80488ac"), ownerType: "pubkeyhash",
			asset: insightRvnAsset{Name: "#KYC", Amount: "10", Units: &zero, Reissuable: &zero},
		},
		{
			name:   "owner token",
			script: insightRvnScript(t, p2pkh, insightRvnPayload('o', "FREN!")),
			ok:     true, assetType: "new_asset", owner: p2pkh, ownerType: "pubkeyhash",
			asset: insightRvnAsset{Name: "FREN!", Amount: "1"},
		},
		{
			name:   "new asset with IPFS hash",
			script: insightRvnScript(t, p2pkh, insightRvnPayload('q', "TEST", insightRvnLE(100000000000), []byte{2, 1, 1, 0x12, 0x20}, hash)),
			ok:     true, assetType: "new_asset", owner: p2pkh, ownerType: "pubkeyhash",
			asset: insightRvnAsset{Name: "TEST", Amount: "1000", Units: &two, Reissuable: &one, IPFSHash: ipfs},
		},
		{
			name:   "reissue keeping units, no hash",
			script: insightRvnScript(t, p2pkh, insightRvnPayload('r', "TEST", insightRvnLE(50000000), []byte{0xff, 0})),
			ok:     true, assetType: "reissue_asset", owner: p2pkh, ownerType: "pubkeyhash",
			asset: insightRvnAsset{Name: "TEST", Amount: "0.5", Units: &minus, Reissuable: &zero},
		},
		{
			name:   "reissue with txid hash",
			script: insightRvnScript(t, p2pkh, insightRvnPayload('r', "TEST", insightRvnLE(1), []byte{8, 1, 0x54, 0x20}, hash)),
			ok:     true, assetType: "reissue_asset", owner: p2pkh, ownerType: "pubkeyhash",
			asset: insightRvnAsset{Name: "TEST", Amount: "0.00000001", Units: &[]int{8}[0], Reissuable: &one, IPFSHash: hex.EncodeToString(hash)},
		},
		{
			name:   "P2SH transfer with message and expiry (OP_PUSHDATA1)",
			script: insightRvnScript(t, p2sh, insightRvnPayload('t', longName, insightRvnLE(300000000), []byte{0x12, 0x20}, hash, insightRvnLE(expire))),
			ok:     true, assetType: "transfer_asset", owner: p2sh, ownerType: "scripthash",
			asset: insightRvnAsset{Name: longName, Amount: "3", Message: ipfs, ExpireTime: &expire},
		},
		{
			name:   "transfer with message, zero expiry omitted",
			script: insightRvnScript(t, p2pkh, insightRvnPayload('t', "FREN", insightRvnLE(100000000), []byte{0x54, 0x20}, hash, insightRvnLE(0))),
			ok:     true, assetType: "transfer_asset", owner: p2pkh, ownerType: "pubkeyhash",
			asset: insightRvnAsset{Name: "FREN", Amount: "1", Message: hex.EncodeToString(hash)},
		},
		{name: "null asset data (address tag)", script: insightRvnHex(t, "c014a0dfb26225355e9f0600a5839a92f99f3a98b7091272766e7104234b594300ca9a3b0000000075")},
		{name: "plain P2PKH", script: p2pkh},
		{name: "unknown asset type", script: insightRvnScript(t, p2pkh, insightRvnPayload('x', "FREN", insightRvnLE(1)))},
		{name: "not rvn", script: insightRvnScript(t, p2pkh, append([]byte("abc"), 't', 4, 'F', 'R', 'E', 'N'))},
		{name: "truncated amount", script: insightRvnScript(t, p2pkh, insightRvnPayload('t', "FREN", []byte{1, 2, 3}))},
		{name: "empty name", script: insightRvnScript(t, p2pkh, insightRvnPayload('t', "", insightRvnLE(1)))},
		{name: "name past the push", script: insightRvnScript(t, p2pkh, append([]byte("rvnt"), 40, 'F'))},
		{name: "missing OP_DROP", script: insightRvnHex(t, "76a914a0dfb26225355e9f0600a5839a92f99f3a98b70988acc01172766e74044652454e00e1f50500000000")},
		{name: "push past the end", script: insightRvnHex(t, "76a914a0dfb26225355e9f0600a5839a92f99f3a98b70988acc0ff72766e74")},
		{name: "new asset truncated IPFS hash", script: insightRvnScript(t, p2pkh, insightRvnPayload('q', "TEST", insightRvnLE(1), []byte{0, 0, 1, 0x12, 0x40}, hash))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := insightRvnDecodeAsset(tc.script)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (%+v)", ok, tc.ok, got)
			}
			if !ok {
				return
			}
			if got.assetType != tc.assetType || !bytes.Equal(got.owner, tc.owner) || got.ownerType != tc.ownerType {
				t.Fatalf("got %s %x %s, want %s %x %s", got.assetType, got.owner, got.ownerType, tc.assetType, tc.owner, tc.ownerType)
			}
			g, _ := json.Marshal(got.asset)
			w, _ := json.Marshal(tc.asset)
			if !bytes.Equal(g, w) {
				t.Fatalf("asset %s, want %s", g, w)
			}
		})
	}
}
