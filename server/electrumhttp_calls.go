package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/martinboehm/btcutil"
	"github.com/trezor/blockbook/api"
	"github.com/trezor/blockbook/bchain"
)

const (
	// ehHistoryLimit is ElectrumX's history DoS limit with its default
	// MAX_SEND (1,000,000 // 99): an address with this many confirmed
	// transactions answers "history too large", as the proxy then does.
	ehHistoryLimit = 1000000 / 99
	// ehUtxoLimit is the proxy's slice of listunspent.
	ehUtxoLimit = 600
)

var errEHHistoryTooLarge = errors.New("history too large")

// ehZeroTxid is the substring index.js uses to skip coinbase inputs.
const ehZeroTxid = "00000000000000000000000000000"

// ehAddrDesc parses param as the proxy's bitgo toOutputScript does for the
// coin's network: an address of another network or no address at all is
// "<param> has no matching Script". The string is the address in the
// parser's canonical form.
func (s *ElectrumHTTPServer) ehAddrDesc(param ehValue) (bchain.AddressDescriptor, string, error) {
	addr := param.jsParam()
	noMatch := ehError(addr + " has no matching Script")
	if !param.set || addr == "" {
		return nil, "", noMatch
	}
	desc, err := s.parser.GetAddrDescFromAddress(addr)
	if err != nil || len(desc) == 0 {
		return nil, "", noMatch
	}
	// The decoder accepts any registered network's prefix; the address must
	// round-trip to itself on this network (bech32 may be all upper case).
	addrs, _, err := s.parser.GetAddressesFromAddrDesc(desc)
	if err != nil || len(addrs) != 1 || addrs[0] != addr && strings.ToUpper(addrs[0]) != addr {
		return nil, "", noMatch
	}
	return desc, addrs[0], nil
}

var ehTxidPattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// ehTxid validates a transaction hash as ElectrumX's assert_tx_hash does.
func ehTxid(param ehValue) (string, error) {
	if !param.set || param.multi || !ehTxidPattern.MatchString(param.s) {
		return "", ehError(param.pyParam() + " should be a transaction hash")
	}
	return strings.ToLower(param.s), nil
}

// ehRPCErr maps a daemon failure as ElectrumX reports it. Errors that are not
// daemon RPC errors stay internal.
func ehRPCErr(err error) error {
	var re *bchain.RPCError
	if errors.As(err, &re) {
		return ehError(fmt.Sprintf("daemon error: DaemonError({'code': %d, 'message': %s})", re.Code, pyRepr(re.Message)))
	}
	return err
}

func (s *ElectrumHTTPServer) rpcCall(method string, params []interface{}, result interface{}) error {
	if s.rpc == nil {
		return errors.New("backend RPC unavailable")
	}
	if err := s.rpc(method, params, result); err != nil {
		return ehRPCErr(err)
	}
	return nil
}

// balance: blockchain.scripthash.get_balance.
func (s *ElectrumHTTPServer) callBalance(param ehValue) (interface{}, error) {
	_, addr, err := s.ehAddrDesc(param)
	if err != nil {
		return nil, err
	}
	a, err := s.worker.GetAddress(addr, 1, 1, api.AccountDetailsBasic, &api.AddressFilter{Vout: api.AddressFilterVoutOff}, "")
	if err != nil {
		return nil, err
	}
	if a == nil {
		return nil, errors.New("missing address")
	}
	return ehObject{{"confirmed", a.BalanceSat.AsInt64()}, {"unconfirmed", a.UnconfirmedBalanceSat.AsInt64()}}, nil
}

type ehHistoryEntry struct {
	txid    string
	height  int
	fee     int64
	mempool bool
}

func (e ehHistoryEntry) object() ehObject {
	o := ehObject{{"tx_hash", e.txid}, {"height", e.height}}
	if e.mempool {
		o = append(o, ehField{"fee", e.fee})
	}
	return o
}

// ehHistory is blockchain.scripthash.get_history: confirmed transactions in
// blockchain order (ascending height, block position within a block), then
// the mempool transactions with height 0, or -1 when one of their inputs is
// unconfirmed, and their fee. Mempool entries are ordered height 0 first, then
// by txid (ElectrumX leaves that order unspecified).
func (s *ElectrumHTTPServer) ehHistory(desc bchain.AddressDescriptor) ([]ehHistoryEntry, error) {
	// The index yields blocks newest first and each block's txs in block order.
	type group struct {
		height uint32
		txids  []string
	}
	var groups []group
	count := 0
	err := s.index.GetAddrDescTransactions(desc, 0, math.MaxUint32, func(txid string, height uint32, _ []int32) error {
		count++
		if count >= ehHistoryLimit {
			return errEHHistoryTooLarge
		}
		if n := len(groups); n == 0 || groups[n-1].height != height {
			groups = append(groups, group{height: height})
		}
		groups[len(groups)-1].txids = append(groups[len(groups)-1].txids, txid)
		return nil
	})
	if errors.Is(err, errEHHistoryTooLarge) {
		return nil, ehError("history too large")
	}
	if err != nil {
		return nil, err
	}
	entries := make([]ehHistoryEntry, 0, count)
	confirmed := make(map[string]struct{}, count)
	for i := len(groups) - 1; i >= 0; i-- {
		for _, txid := range groups[i].txids {
			entries = append(entries, ehHistoryEntry{txid: txid, height: int(groups[i].height)})
			confirmed[txid] = struct{}{}
		}
	}
	outpoints, err := s.mempool.GetAddrDescTransactions(desc)
	if err != nil {
		return nil, err
	}
	var pending []ehHistoryEntry
	seen := make(map[string]struct{}, len(outpoints))
	for _, o := range outpoints {
		if _, ok := seen[o.Txid]; ok {
			continue
		}
		seen[o.Txid] = struct{}{}
		if _, ok := confirmed[o.Txid]; ok {
			continue
		}
		tx, err := s.worker.GetTransaction(o.Txid, false, false)
		if err != nil || tx == nil || tx.Confirmations > 0 {
			// Left the mempool (mined or evicted) since the lookup.
			continue
		}
		e := ehHistoryEntry{txid: o.Txid, fee: tx.FeesSat.AsInt64(), mempool: true}
		for _, vin := range tx.Vin {
			if vin.Txid != "" && s.mempool.GetTransactionTime(vin.Txid) != 0 {
				e.height = -1
				break
			}
		}
		pending = append(pending, e)
	}
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].height != pending[j].height {
			return pending[i].height > pending[j].height
		}
		return pending[i].txid < pending[j].txid
	})
	return append(entries, pending...), nil
}

// history / entirehistory. history keeps the proxy's truncation: more than
// 200 entries answer the last 100.
func (s *ElectrumHTTPServer) callHistory(param ehValue, limited bool) (interface{}, error) {
	desc, _, err := s.ehAddrDesc(param)
	if err != nil {
		return nil, err
	}
	entries, err := s.ehHistory(desc)
	if err != nil {
		return nil, err
	}
	if limited && len(entries) > 200 {
		entries = entries[len(entries)-100:]
	}
	out := make([]ehObject, len(entries))
	for i, e := range entries {
		out[i] = e.object()
	}
	return out, nil
}

// transaction / transactionnonverbose: blockchain.transaction.get, the raw
// hex as a JSON string.
func (s *ElectrumHTTPServer) callRawTx(param ehValue) (interface{}, error) {
	txid, err := ehTxid(param)
	if err != nil {
		return nil, err
	}
	var raw string
	if err := s.rpcCall("getrawtransaction", []interface{}{txid, 0}, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// ehVerboseTx is the daemon's verbose transaction as ElectrumX passes it
// through: key order kept, numbers re-encoded as JavaScript doubles.
func (s *ElectrumHTTPServer) ehVerboseTx(txid string) (interface{}, error) {
	var raw json.RawMessage
	if err := s.rpcCall("getrawtransaction", []interface{}{txid, 1}, &raw); err != nil {
		return nil, err
	}
	return ehDecode(raw)
}

// transactionverbose: blockchain.transaction.get(txid, true).
func (s *ElectrumHTTPServer) callVerboseTx(param ehValue) (interface{}, error) {
	txid, err := ehTxid(param)
	if err != nil {
		return nil, err
	}
	return s.ehVerboseTx(txid)
}

// ehListUnspent is blockchain.scripthash.listunspent: confirmed outputs in
// blockchain order, then mempool outputs with height 0. Outputs spent in the
// mempool are left out, as in ElectrumX.
func (s *ElectrumHTTPServer) ehListUnspent(addr string) (api.Utxos, error) {
	utxos, err := s.worker.GetAddressUtxo(addr, false)
	if err != nil {
		return nil, err
	}
	// The worker answers the mempool outputs first, then confirmed newest first.
	var confirmed, pending api.Utxos
	for _, u := range utxos {
		if u.Height <= 0 || u.Confirmations <= 0 {
			u.Height = 0
			pending = append(pending, u)
		} else {
			confirmed = append(confirmed, u)
		}
	}
	out := make(api.Utxos, 0, len(utxos))
	for i := len(confirmed) - 1; i >= 0; i-- {
		out = append(out, confirmed[i])
	}
	return append(out, pending...), nil
}

// utxo: listunspent, first 600.
func (s *ElectrumHTTPServer) callUtxo(param ehValue) (interface{}, error) {
	_, addr, err := s.ehAddrDesc(param)
	if err != nil {
		return nil, err
	}
	utxos, err := s.ehListUnspent(addr)
	if err != nil {
		return nil, err
	}
	if len(utxos) > ehUtxoLimit {
		utxos = utxos[:ehUtxoLimit]
	}
	out := make([]ehObject, len(utxos))
	for i, u := range utxos {
		out[i] = ehObject{{"tx_hash", u.Txid}, {"tx_pos", int64(u.Vout)}, {"height", u.Height}, {"value", u.AmountSat.AsInt64()}}
	}
	return out, nil
}

// niceutxo: the first 600 listunspent outputs that are confirmed, with the
// address's output script.
func (s *ElectrumHTTPServer) callNiceUtxo(param ehValue) (interface{}, error) {
	desc, addr, err := s.ehAddrDesc(param)
	if err != nil {
		return nil, err
	}
	script, err := s.parser.GetScriptFromAddrDesc(desc)
	if err != nil {
		return nil, err
	}
	scriptHex := hex.EncodeToString(script)
	utxos, err := s.ehListUnspent(addr)
	if err != nil {
		return nil, err
	}
	if len(utxos) > ehUtxoLimit {
		utxos = utxos[:ehUtxoLimit]
	}
	out := make([]ehObject, 0, len(utxos))
	for _, u := range utxos {
		if u.Height == 0 {
			continue
		}
		out = append(out, ehObject{{"txid", u.Txid}, {"vout", int64(u.Vout)}, {"scriptPubKey", scriptHex}, {"satoshis", u.AmountSat.AsInt64()}, {"height", u.Height}})
	}
	return out, nil
}

// broadcast: blockchain.transaction.broadcast. A daemon refusal is reported
// with ElectrumX's text, which the wallet splits at "\n[".
func (s *ElectrumHTTPServer) callBroadcast(param ehValue) (interface{}, error) {
	if s.broadcast == nil {
		return nil, errors.New("broadcast unavailable")
	}
	raw := ""
	if param.set {
		raw = param.s
	}
	txid, err := s.broadcast(raw)
	if err != nil {
		var re *bchain.RPCError
		if errors.As(err, &re) {
			return nil, ehError("the transaction was rejected by network rules.\n\n" + re.Message + "\n[" + param.pyParam() + "]")
		}
		return nil, err
	}
	return txid, nil
}

func (s *ElectrumHTTPServer) ehRawHeader(hash string) ([]byte, error) {
	var headerHex string
	if err := s.rpcCall("getblockheader", []interface{}{hash, false}, &headerHex); err != nil {
		return nil, err
	}
	header, err := hex.DecodeString(headerHex)
	if err != nil {
		return nil, err
	}
	if len(header) < 80 {
		return nil, errors.New("short block header")
	}
	return header, nil
}

// height: blockchain.headers.subscribe, {"hex", "height"} of the index tip.
func (s *ElectrumHTTPServer) callHeight() (interface{}, error) {
	height, hash, err := s.index.GetBestBlock()
	if err != nil {
		return nil, err
	}
	header, err := s.ehRawHeader(hash)
	if err != nil {
		return nil, err
	}
	return ehObject{{"hex", hex.EncodeToString(header)}, {"height", int64(height)}}, nil
}

var ehHeightPattern = regexp.MustCompile(`^\s*\+?([0-9]+)\s*$`)

// header: the legacy blockchain.block.get_header, the decoded 80-byte header.
func (s *ElectrumHTTPServer) callHeader(param ehValue) (interface{}, error) {
	m := ehHeightPattern.FindStringSubmatch(param.s)
	if !param.set || param.multi || m == nil {
		return nil, ehError(param.pyParam() + " should be a non-negative integer")
	}
	digits := strings.TrimLeft(m[1], "0")
	if digits == "" {
		digits = "0"
	}
	best, _, err := s.index.GetBestBlock()
	if err != nil {
		return nil, err
	}
	var height uint64
	if len(digits) > 10 {
		height = math.MaxUint64
	} else {
		height, _ = strconv.ParseUint(digits, 10, 64)
	}
	if height > uint64(best) {
		return nil, ehError("height " + pyThousands(digits) + " out of range")
	}
	hash, err := s.index.GetBlockHash(uint32(height))
	if err != nil {
		return nil, err
	}
	if hash == "" {
		return nil, errors.New("missing block hash")
	}
	h, err := s.ehRawHeader(hash)
	if err != nil {
		return nil, err
	}
	le32 := func(b []byte) int64 {
		return int64(uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24)
	}
	return ehObject{
		{"block_height", int64(height)},
		{"version", le32(h[0:4])},
		{"prev_block_hash", ehReversedHex(h[4:36])},
		{"merkle_root", ehReversedHex(h[36:68])},
		{"timestamp", le32(h[68:72])},
		{"bits", le32(h[72:76])},
		{"nonce", le32(h[76:80])},
	}, nil
}

func ehReversedHex(b []byte) string {
	r := make([]byte, len(b))
	for i := range b {
		r[len(b)-1-i] = b[i]
	}
	return hex.EncodeToString(r)
}

// jsTruthy is JavaScript truthiness for a decoded JSON value.
func jsTruthy(v interface{}) bool {
	switch v := v.(type) {
	case nil:
		return false
	case bool:
		return v
	case float64:
		return v != 0 && !math.IsNaN(v)
	case string:
		return v != ""
	}
	return true
}

func ehNumber(v interface{}) float64 {
	if f, ok := v.(float64); ok {
		return f
	}
	return math.NaN()
}

// ehVinAddr is the proxy's input address: the previous output's address for
// P2PKH and P2SH outputs; for a P2PK output, the P2PKH address of the first
// scriptSig push when that push has a public key's shape (it is the signature
// in a standard spend, so "" in practice); "" for every other output type,
// witness outputs included.
func (s *ElectrumHTTPServer) ehVinAddr(prevScript []byte, scriptSigASM string) string {
	n := len(prevScript)
	isP2PKH := n == 25 && prevScript[0] == 0x76 && prevScript[1] == 0xa9 && prevScript[2] == 0x14 && prevScript[23] == 0x88 && prevScript[24] == 0xac
	isP2SH := n == 23 && prevScript[0] == 0xa9 && prevScript[1] == 0x14 && prevScript[22] == 0x87
	if isP2PKH || isP2SH {
		return s.ehScriptAddress(prevScript)
	}
	isP2PK := n == 35 && prevScript[0] == 33 && (prevScript[1] == 2 || prevScript[1] == 3) && prevScript[34] == 0xac ||
		n == 67 && prevScript[0] == 65 && prevScript[1] == 4 && prevScript[66] == 0xac
	if !isP2PK {
		return ""
	}
	pub, err := hex.DecodeString(strings.Split(scriptSigASM, " ")[0])
	if err != nil || !(len(pub) == 33 && (pub[0] == 2 || pub[0] == 3) || len(pub) == 65 && pub[0] == 4) {
		return ""
	}
	script := append(append([]byte{0x76, 0xa9, 0x14}, btcutil.Hash160(pub)...), 0x88, 0xac)
	return s.ehScriptAddress(script)
}

func (s *ElectrumHTTPServer) ehScriptAddress(script []byte) string {
	addrs, _, err := s.parser.GetAddressesFromAddrDesc(script)
	if err != nil || len(addrs) != 1 {
		return ""
	}
	return addrs[0]
}

// ehPrevTx loads and parses a raw transaction, cached for the request.
func (s *ElectrumHTTPServer) ehPrevTx(txid string, cache map[string]*bchain.Tx) (*bchain.Tx, error) {
	if tx, ok := cache[txid]; ok {
		return tx, nil
	}
	var raw string
	if err := s.rpcCall("getrawtransaction", []interface{}{txid, 0}, &raw); err != nil {
		return nil, err
	}
	b, err := hex.DecodeString(raw)
	if err != nil {
		return nil, err
	}
	tx, err := s.parser.ParseTx(b)
	if err != nil {
		return nil, err
	}
	cache[txid] = tx
	return tx, nil
}

// nicehistory / niceentirehistory: the newest amountOfTxs history entries,
// newest first, each the verbose transaction reshaped as index.js does,
// including its floating-point satoshi arithmetic.
func (s *ElectrumHTTPServer) callNiceHistory(ctx context.Context, param ehValue, amountOfTxs int) (interface{}, error) {
	desc, _, err := s.ehAddrDesc(param)
	if err != nil {
		return nil, err
	}
	entries, err := s.ehHistory(desc)
	if err != nil {
		return nil, err
	}
	limit := min(len(entries), amountOfTxs)
	now := float64(s.now().Unix())
	cache := make(map[string]*bchain.Tx)
	out := make([]ehObject, 0, limit)
	for j := 0; j < limit; j++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entry := entries[len(entries)-1-j]
		v, err := s.ehVerboseTx(entry.txid)
		if err != nil {
			return nil, err
		}
		tx, ok := v.(ehObject)
		if !ok {
			return nil, errors.New("verbose transaction is not an object")
		}
		result, err := s.ehNiceTx(tx, entry.height, now, cache)
		if err != nil {
			return nil, err
		}
		out = append(out, result)
	}
	return out, nil
}

func (s *ElectrumHTTPServer) ehNiceTx(tx ehObject, height int, now float64, cache map[string]*bchain.Tx) (ehObject, error) {
	result := ehObject{}
	// JSON.stringify drops keys whose value is undefined.
	copyKey := func(key string) {
		if v, ok := tx.get(key); ok {
			result = append(result, ehField{key, v})
		}
	}
	copyKey("txid")
	copyKey("version")
	copyKey("locktime")
	var valueIn, valueOut, fees float64
	vins := []interface{}{}
	rawVins, _ := tx.get("vin")
	vinList, _ := rawVins.([]interface{})
	for _, rv := range vinList {
		in, ok := rv.(ehObject)
		if !ok {
			continue
		}
		t, _ := in.get("txid")
		txid, _ := t.(string)
		if txid == "" || strings.Contains(txid, ehZeroTxid) {
			// Coinbase input: the proxy adds no vin for it.
			continue
		}
		nv, _ := in.get("vout")
		n := ehNumber(nv)
		seq, _ := in.get("sequence")
		var scriptHex string
		if ss, ok := in.get("scriptSig"); ok {
			if so, ok := ss.(ehObject); ok {
				h, _ := so.get("hex")
				scriptHex, _ = h.(string)
			}
		}
		script, err := hex.DecodeString(scriptHex)
		if err != nil {
			return nil, err
		}
		prev, err := s.ehPrevTx(txid, cache)
		if err != nil {
			return nil, err
		}
		if math.IsNaN(n) || n < 0 || int(n) >= len(prev.Vout) {
			return nil, fmt.Errorf("input %s:%v not found", txid, n)
		}
		prevOut := &prev.Vout[int(n)]
		prevScript, err := hex.DecodeString(prevOut.ScriptPubKey.Hex)
		if err != nil {
			return nil, err
		}
		sats := float64(prevOut.ValueSat.Int64())
		asm := bitgoASM(script)
		vins = append(vins, ehObject{
			{"txid", txid},
			{"n", n},
			{"script", asm},
			{"sequence", seq},
			{"scriptSig", ehObject{{"hex", hex.EncodeToString(script)}, {"asm", asm}}},
			{"addr", s.ehVinAddr(prevScript, asm)},
			{"value", 1e-8 * sats},
			{"valueSat", sats},
			{"satoshis", sats},
		})
		valueIn += sats
		fees += sats
	}
	vouts := []interface{}{}
	rawVouts, _ := tx.get("vout")
	voutList, _ := rawVouts.([]interface{})
	for _, rv := range voutList {
		o, ok := rv.(ehObject)
		if !ok {
			vouts = append(vouts, rv)
			continue
		}
		value, _ := o.get("value")
		sat := ehNumber(value) * 1e8
		o.set("satoshi", sat)
		o.set("valueSat", ehNumber(value)*1e8)
		valueOut += sat
		fees -= sat
		vouts = append(vouts, o)
	}
	result = append(result, ehField{"vin", vins}, ehField{"vout", vouts})
	if t, ok := tx.get("time"); ok && jsTruthy(t) {
		result = append(result, ehField{"time", t})
	} else {
		result = append(result, ehField{"time", now})
	}
	if c, ok := tx.get("confirmations"); ok && jsTruthy(c) {
		result = append(result, ehField{"confirmations", c})
	} else {
		result = append(result, ehField{"confirmations", 0})
	}
	result = append(result,
		ehField{"valueInSat", valueIn},
		ehField{"valueOutSat", valueOut},
		ehField{"fees", fees},
		ehField{"height", height},
	)
	if h, ok := tx.get("hex"); ok && jsTruthy(h) {
		result = append(result, ehField{"hex", h})
	}
	return result, nil
}
