package server

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math/big"

	"github.com/martinboehm/btcutil/base58"
	"github.com/trezor/blockbook/api"
)

// Ravencoin asset outputs are a standard P2PKH or P2SH script followed by
// OP_RVN_ASSET (0xc0), one push of "rvn" + a type byte + the asset payload,
// and OP_DROP (0x75):
//
//	76a914{20}88ac | a914{20}87  c0  <push "rvn" t|q|o|r payload>  75
//
// The payload is Ravencoin core's serialization (CAssetTransfer, CNewAsset,
// the owner name, CReissueAsset); amounts are int64 in 1e8 units. A script
// that starts with c0 (null asset data: address tags, freezes, verifier
// strings) is not an asset output and is left alone, as is any script that
// does not decode.

const (
	insightRvnOpAsset = 0xc0
	insightRvnOpDrop  = 0x75
	// insightRvnOwnerAmount is Ravencoin core's OWNER_ASSET_AMOUNT (1 coin).
	insightRvnOwnerAmount = 100000000
)

// insightRvnAsset is ravend's scriptPubKey.asset object, in its key order
// (name, amount, message, expire_time, units, reissuable, ipfs_hash), which
// stock Insight passes through as vout.asset. A transfer has name, amount
// and, when the script carries them, message and expire_time; an owner
// token has name and amount; a new or reissued asset adds units, reissuable
// (a number, as ravend prints it) and ipfs_hash when present.
type insightRvnAsset struct {
	Name       string      `json:"name"`
	Amount     json.Number `json:"amount"`
	Message    string      `json:"message,omitempty"`
	ExpireTime *int64      `json:"expire_time,omitempty"`
	Units      *int        `json:"units,omitempty"`
	Reissuable *int        `json:"reissuable,omitempty"`
	IPFSHash   string      `json:"ipfs_hash,omitempty"`
}

// insightRvnAssetOut is one decoded asset output.
type insightRvnAssetOut struct {
	// assetType is ravend's scriptPubKey.type for the output (stock's
	// vout.assetType): transfer_asset, new_asset (also an owner token) or
	// reissue_asset.
	assetType string
	// owner is the standard script the asset is paid to (the script without
	// the asset part); ownerType is its stock scriptPubKey.type.
	owner     []byte
	ownerType string
	asset     insightRvnAsset
}

// insightRvnReader reads core's serialization from a byte slice; any read
// past the end sets bad.
type insightRvnReader struct {
	b   []byte
	bad bool
}

func (r *insightRvnReader) bytes(n int) []byte {
	if r.bad || n < 0 || n > len(r.b) {
		r.bad = true
		return nil
	}
	out := r.b[:n]
	r.b = r.b[n:]
	return out
}

func (r *insightRvnReader) byte() byte {
	b := r.bytes(1)
	if b == nil {
		return 0
	}
	return b[0]
}

// compactSize reads core's CompactSize length prefix.
func (r *insightRvnReader) compactSize() int {
	switch n := r.byte(); n {
	case 0xfd:
		if b := r.bytes(2); b != nil {
			return int(binary.LittleEndian.Uint16(b))
		}
	case 0xfe:
		if b := r.bytes(4); b != nil {
			v := binary.LittleEndian.Uint32(b)
			if v > 1<<20 {
				r.bad = true
				return 0
			}
			return int(v)
		}
	case 0xff:
		r.bad = true
	default:
		return int(n)
	}
	return 0
}

func (r *insightRvnReader) str() string {
	return string(r.bytes(r.compactSize()))
}

func (r *insightRvnReader) int64() int64 {
	if b := r.bytes(8); b != nil {
		return int64(binary.LittleEndian.Uint64(b))
	}
	return 0
}

// assetHash reads core's ReadWriteAssetHash: when at least 33 bytes remain,
// a type byte (0x12 for an IPFS sha2-256 hash, 0x54 for a txid) and a
// length-prefixed hash. It returns the hash as ravend's EncodeAssetData
// prints it (base58 for the 34-byte IPFS form, hex for 32 bytes, else ""),
// and whether a hash was read.
func (r *insightRvnReader) assetHash() (string, bool) {
	if len(r.b) < 33 {
		return "", false
	}
	kind := r.byte()
	hash := r.bytes(r.compactSize())
	if r.bad {
		return "", false
	}
	data := hash
	if kind == 0x12 {
		data = append([]byte{kind, byte(len(hash))}, hash...)
	}
	switch len(data) {
	case 34:
		return base58.Encode(data), true
	case 32:
		return hex.EncodeToString(data), true
	}
	return "", true
}

// insightRvnAmount renders an asset amount (1e8 units) as ravend's
// ValueFromAmount does once parsed as a JavaScript number.
func insightRvnAmount(v int64) json.Number {
	value, _ := insightMoney((*api.Amount)(big.NewInt(v)))
	return value
}

// insightRvnAssetPush returns the asset push data of an asset script and the
// standard script before it, ok false when script is not an asset output.
func insightRvnAssetPush(script []byte) (owner []byte, ownerType string, data []byte, ok bool) {
	var p int
	switch {
	case len(script) > 25 && script[0] == 0x76 && script[1] == 0xa9 && script[2] == 0x14 && script[23] == 0x88 && script[24] == 0xac:
		p, ownerType = 25, "pubkeyhash"
	case len(script) > 23 && script[0] == 0xa9 && script[1] == 0x14 && script[22] == 0x87:
		p, ownerType = 23, "scripthash"
	default:
		return nil, "", nil, false
	}
	if script[p] != insightRvnOpAsset || p+1 >= len(script) {
		return nil, "", nil, false
	}
	op := int(script[p+1])
	start := p + 2
	var n int
	switch {
	case op >= 1 && op <= 75:
		n = op
	case op == 0x4c && start < len(script):
		n, start = int(script[start]), start+1
	case op == 0x4d && start+1 < len(script):
		n, start = int(binary.LittleEndian.Uint16(script[start:start+2])), start+2
	default:
		return nil, "", nil, false
	}
	end := start + n
	if end >= len(script) || script[end] != insightRvnOpDrop {
		return nil, "", nil, false
	}
	return script[:p], ownerType, script[start:end], true
}

// insightRvnDecodeAsset decodes an asset output script; ok is false for any
// other script, including null asset data and anything malformed.
func insightRvnDecodeAsset(script []byte) (*insightRvnAssetOut, bool) {
	owner, ownerType, data, ok := insightRvnAssetPush(script)
	if !ok || len(data) < 4 || data[0] != 'r' || data[1] != 'v' || data[2] != 'n' {
		return nil, false
	}
	out := &insightRvnAssetOut{owner: owner, ownerType: ownerType}
	r := &insightRvnReader{b: data[4:]}
	a := &out.asset
	a.Name = r.str()
	switch data[3] {
	case 't':
		out.assetType = "transfer_asset"
		a.Amount = insightRvnAmount(r.int64())
		if r.bad {
			return nil, false
		}
		if message, read := r.assetHash(); read {
			if r.bad {
				return nil, false
			}
			a.Message = message
			if len(r.b) >= 8 {
				if expire := r.int64(); expire != 0 {
					a.ExpireTime = &expire
				}
			}
		}
	case 'q':
		out.assetType = "new_asset"
		a.Amount = insightRvnAmount(r.int64())
		units, reissuable := int(int8(r.byte())), int(int8(r.byte()))
		hasIPFS := r.byte()
		if r.bad {
			return nil, false
		}
		a.Units, a.Reissuable = &units, &reissuable
		if hasIPFS == 1 {
			ipfs, _ := r.assetHash()
			if r.bad {
				return nil, false
			}
			a.IPFSHash = ipfs
		}
	case 'o':
		out.assetType = "new_asset"
		a.Amount = insightRvnAmount(insightRvnOwnerAmount)
	case 'r':
		out.assetType = "reissue_asset"
		a.Amount = insightRvnAmount(r.int64())
		units, reissuable := int(int8(r.byte())), int(int8(r.byte()))
		if r.bad {
			return nil, false
		}
		a.Units, a.Reissuable = &units, &reissuable
		ipfs, _ := r.assetHash()
		if r.bad {
			return nil, false
		}
		a.IPFSHash = ipfs
	default:
		return nil, false
	}
	if r.bad || a.Name == "" {
		return nil, false
	}
	return out, true
}
