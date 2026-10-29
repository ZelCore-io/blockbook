package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// seedConfig drives path discovery. No address is hardcoded: everything comes
// from recent blocks served by the target itself (kind insight: status,
// block-index, block, tx, addr) or by a Blockbook public API (kind blockbook).
type seedConfig struct {
	Base        string
	Kind        string
	Blocks      int
	TxsPerBlock int
	Empty       string
	Pending     []string
	MempoolBase string
	Log         io.Writer
}

type txInfo struct {
	Txid      string
	Coinbase  bool
	OpReturn  bool
	VinAddrs  []string
	VoutAddrs []string
}

func (t *txInfo) all() []string { return append(append([]string{}, t.VinAddrs...), t.VoutAddrs...) }

type blockInfo struct {
	Height int
	Hash   string
	Time   int64
	Txids  []string
	Txs    []*txInfo // set when the source returns full transactions with the block
}

var errNoMempool = errors.New("no mempool route")

type source interface {
	tip() (int, error)
	block(height int) (*blockInfo, error)
	tx(txid string) (*txInfo, error)
	addrCount(addr string) (int, error)
	bestHash() (string, error)
	mempool(max int) ([]*txInfo, error)
}

func asInt(v interface{}) int {
	switch n := v.(type) {
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
		if f, err := n.Float64(); err == nil {
			return int(f)
		}
	case float64:
		return int(n)
	}
	return 0
}

func asObj(v interface{}) map[string]interface{} {
	m, _ := v.(map[string]interface{})
	return m
}

func strList(v interface{}) []string {
	var out []string
	if arr, ok := v.([]interface{}); ok {
		for _, e := range arr {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func looksLikeAddress(s string) bool {
	return len(s) >= 20 && len(s) <= 120 && !strings.ContainsAny(s, " \t") && !strings.HasPrefix(s, "OP_")
}

func addrsOf(m map[string]interface{}, keys ...string) []string {
	var out []string
	for _, k := range keys {
		if s, ok := m[k].(string); ok && looksLikeAddress(s) {
			out = append(out, s)
		}
		for _, s := range strList(m[k]) {
			if looksLikeAddress(s) {
				out = append(out, s)
			}
		}
	}
	return out
}

// parseTx reads either the Insight or the Blockbook transaction shape.
func parseTx(m map[string]interface{}) *txInfo {
	t := &txInfo{}
	t.Txid, _ = m["txid"].(string)
	if b, _ := m["isCoinBase"].(bool); b {
		t.Coinbase = true
	}
	if vin, ok := m["vin"].([]interface{}); ok {
		for _, e := range vin {
			in := asObj(e)
			if s, _ := in["coinbase"].(string); s != "" {
				t.Coinbase = true
			}
			t.VinAddrs = append(t.VinAddrs, addrsOf(in, "addr", "addresses")...)
		}
	}
	if vout, ok := m["vout"].([]interface{}); ok {
		for _, e := range vout {
			out := asObj(e)
			spk := asObj(out["scriptPubKey"])
			addrs := addrsOf(out, "addresses")
			if spk != nil {
				addrs = append(addrs, addrsOf(spk, "addresses")...)
			}
			t.VoutAddrs = append(t.VoutAddrs, addrs...)
			hex, _ := out["hex"].(string)
			if spk != nil && hex == "" {
				hex, _ = spk["hex"].(string)
			}
			asm, _ := spk["asm"].(string)
			typ, _ := out["type"].(string)
			if typ == "" {
				typ, _ = spk["type"].(string)
			}
			isRet := typ == "nulldata" || strings.HasPrefix(hex, "6a") || strings.HasPrefix(asm, "OP_RETURN")
			for _, s := range strList(out["addresses"]) {
				if strings.HasPrefix(s, "OP_RETURN") {
					isRet = true
				}
			}
			if isRet {
				t.OpReturn = true
			}
		}
	}
	return t
}

func fetchObj(c *client, base, path string) (map[string]interface{}, error) {
	var v interface{}
	if _, err := getJSON(c, base, path, &v); err != nil {
		return nil, err
	}
	m := asObj(v)
	if m == nil {
		return nil, fmt.Errorf("GET %s: not a JSON object", path)
	}
	return m, nil
}

// insightSource reads the target's own Insight routes.
type insightSource struct {
	c    *client
	base string
	mem  source
}

func (s *insightSource) tip() (int, error) {
	m, err := fetchObj(s.c, s.base, "status")
	if err != nil {
		return 0, err
	}
	if n := asInt(asObj(m["info"])["blocks"]); n > 0 {
		return n, nil
	}
	m, err = fetchObj(s.c, s.base, "sync")
	if err != nil {
		return 0, err
	}
	if n := asInt(m["height"]); n > 0 {
		return n, nil
	}
	return 0, errors.New("no block height in status or sync")
}

func (s *insightSource) block(h int) (*blockInfo, error) {
	m, err := fetchObj(s.c, s.base, "block-index/"+strconv.Itoa(h))
	if err != nil {
		return nil, err
	}
	hash, _ := m["blockHash"].(string)
	if hash == "" {
		return nil, fmt.Errorf("block-index/%d: no blockHash", h)
	}
	b, err := fetchObj(s.c, s.base, "block/"+hash)
	if err != nil {
		return nil, err
	}
	return &blockInfo{Height: h, Hash: hash, Time: int64(asInt(b["time"])), Txids: strList(b["tx"])}, nil
}

func (s *insightSource) tx(id string) (*txInfo, error) {
	m, err := fetchObj(s.c, s.base, "tx/"+id)
	if err != nil {
		return nil, err
	}
	t := parseTx(m)
	if t.Txid == "" {
		t.Txid = id
	}
	return t, nil
}

func (s *insightSource) addrCount(a string) (int, error) {
	m, err := fetchObj(s.c, s.base, "addr/"+a+"?noTxList=1")
	if err != nil {
		return 0, err
	}
	return asInt(m["txApperances"]), nil
}

func (s *insightSource) bestHash() (string, error) {
	m, err := fetchObj(s.c, s.base, "status?q=getLastBlockHash")
	if err != nil {
		return "", err
	}
	h, _ := m["lastblockhash"].(string)
	if h == "" {
		return "", errors.New("status?q=getLastBlockHash: no lastblockhash")
	}
	return h, nil
}

func (s *insightSource) mempool(max int) ([]*txInfo, error) {
	if s.mem == nil {
		return nil, errNoMempool
	}
	return s.mem.mempool(max)
}

// blockbookSource reads a Blockbook public API.
type blockbookSource struct {
	c    *client
	root string
}

func blockbookRoot(u string) string {
	u = strings.TrimRight(u, "/")
	u = strings.TrimSuffix(u, "/v2")
	u = strings.TrimSuffix(u, "/api")
	return u
}

func (s *blockbookSource) get(path string) (map[string]interface{}, error) {
	return fetchObj(s.c, s.root, strings.TrimSuffix("api/v2/"+path, "/"))
}

func (s *blockbookSource) tip() (int, error) {
	m, err := s.get("")
	if err != nil {
		return 0, err
	}
	if n := asInt(asObj(m["blockbook"])["bestHeight"]); n > 0 {
		return n, nil
	}
	return 0, errors.New("api/v2: no blockbook.bestHeight")
}

func (s *blockbookSource) block(h int) (*blockInfo, error) {
	m, err := s.get("block/" + strconv.Itoa(h))
	if err != nil {
		return nil, err
	}
	b := &blockInfo{Height: h, Time: int64(asInt(m["time"]))}
	b.Hash, _ = m["hash"].(string)
	if txs, ok := m["txs"].([]interface{}); ok {
		for _, e := range txs {
			t := parseTx(asObj(e))
			b.Txids = append(b.Txids, t.Txid)
			b.Txs = append(b.Txs, t)
		}
	}
	if b.Hash == "" {
		return nil, fmt.Errorf("block/%d: no hash", h)
	}
	return b, nil
}

func (s *blockbookSource) tx(id string) (*txInfo, error) {
	m, err := s.get("tx/" + id)
	if err != nil {
		return nil, err
	}
	t := parseTx(m)
	if t.Txid == "" {
		t.Txid = id
	}
	return t, nil
}

func (s *blockbookSource) addrCount(a string) (int, error) {
	m, err := s.get("address/" + a + "?details=basic")
	if err != nil {
		return 0, err
	}
	return asInt(m["txs"]), nil
}

func (s *blockbookSource) bestHash() (string, error) {
	h, err := s.tip()
	if err != nil {
		return "", err
	}
	b, err := s.block(h)
	if err != nil {
		return "", err
	}
	return b.Hash, nil
}

func (s *blockbookSource) mempool(max int) ([]*txInfo, error) {
	m, err := s.get("mempool")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNoMempool, err)
	}
	var ids []string
	for _, k := range []string{"mempoolTxids", "txids", "txs"} {
		arr, _ := m[k].([]interface{})
		for _, e := range arr {
			switch v := e.(type) {
			case string:
				ids = append(ids, v)
			case map[string]interface{}:
				if id, ok := v["txid"].(string); ok {
					ids = append(ids, id)
				}
			}
		}
	}
	var out []*txInfo
	for _, id := range ids {
		if len(out) >= max {
			break
		}
		if t, err := s.tx(id); err == nil && len(t.all()) > 0 {
			out = append(out, t)
		}
	}
	return out, nil
}

// seedData is everything buildPaths needs.
type seedData struct {
	Tip                             int
	TipHash                         string
	PrevHash                        string // block tip-1: stable under reorg-free operation
	OldHash                         string
	OldHeight                       int
	BestHash                        string // status?q=getLastBlockHash answer
	BigHash                         string // block with the most transactions
	BigTxs                          int
	Date                            string // UTC day of block tip-1
	OldDate                         string
	Addrs                           []labeled
	BusyCount                       int
	Busy, Single                    string
	CoinbaseTx, OpReturnTx, PlainTx string
	PendingTxs                      []string
	Notes                           []string
}

type labeled struct{ Label, Addr string }

func (d *seedData) note(format string, args ...interface{}) {
	d.Notes = append(d.Notes, fmt.Sprintf(format, args...))
}

func runSeed(c *client, sc seedConfig) ([]string, error) {
	if sc.Blocks < 1 {
		sc.Blocks = 3
	}
	if sc.TxsPerBlock < 1 {
		sc.TxsPerBlock = 25
	}
	var src source
	switch sc.Kind {
	case "insight", "":
		is := &insightSource{c: c, base: sc.Base}
		if sc.MempoolBase != "" {
			is.mem = &blockbookSource{c: c, root: blockbookRoot(sc.MempoolBase)}
		}
		src = is
	case "blockbook":
		src = &blockbookSource{c: c, root: blockbookRoot(sc.Base)}
	default:
		return nil, fmt.Errorf("unknown -seed-kind %q", sc.Kind)
	}
	d, err := discover(src, sc)
	if err != nil {
		return nil, err
	}
	if sc.Log != nil {
		for _, n := range d.Notes {
			fmt.Fprintln(sc.Log, "seed note:", n)
		}
	}
	return buildPaths(d), nil
}

func discover(src source, sc seedConfig) (*seedData, error) {
	d := &seedData{}
	tip, err := src.tip()
	if err != nil {
		return nil, fmt.Errorf("tip height: %w", err)
	}
	d.Tip = tip
	tally := map[string]int{}
	var order []string
	var coinbaseAddr, opretAddr string
	bump := func(a string) {
		if tally[a] == 0 {
			order = append(order, a)
		}
		tally[a]++
	}
	for i := 0; i < sc.Blocks; i++ {
		h := tip - i
		if h < 1 {
			break
		}
		b, err := src.block(h)
		if err != nil {
			if i == 0 {
				return nil, fmt.Errorf("block %d: %w", h, err)
			}
			d.note("block %d skipped: %v", h, err)
			continue
		}
		switch i {
		case 0:
			d.TipHash = b.Hash
		case 1:
			d.PrevHash = b.Hash
			d.Date = time.Unix(b.Time, 0).UTC().Format("2006-01-02")
		}
		d.OldHash, d.OldHeight = b.Hash, h
		d.OldDate = time.Unix(b.Time, 0).UTC().Format("2006-01-02")
		if len(b.Txids) > d.BigTxs {
			d.BigTxs, d.BigHash = len(b.Txids), b.Hash
		}
		txs := b.Txs
		if txs == nil {
			for j, id := range b.Txids {
				if j >= sc.TxsPerBlock {
					break
				}
				t, err := src.tx(id)
				if err != nil {
					d.note("tx %s skipped: %v", id, err)
					continue
				}
				txs = append(txs, t)
			}
		}
		for _, t := range txs {
			switch {
			case t.Coinbase:
				if d.CoinbaseTx == "" && len(t.VoutAddrs) > 0 {
					d.CoinbaseTx, coinbaseAddr = t.Txid, t.VoutAddrs[0]
				}
				continue
			case t.OpReturn:
				if d.OpReturnTx == "" && len(t.all()) > 0 {
					d.OpReturnTx = t.Txid
					opretAddr = t.all()[0]
				}
			case d.PlainTx == "":
				d.PlainTx = t.Txid
			}
			for _, a := range t.all() {
				bump(a)
			}
		}
	}
	if d.PrevHash == "" {
		d.PrevHash = d.TipHash
		d.Date = d.OldDate
	}
	if bh, err := src.bestHash(); err != nil {
		d.note("best hash: %v (using tip block hash)", err)
		d.BestHash = d.TipHash
	} else {
		d.BestHash = bh
	}
	if d.CoinbaseTx == "" {
		d.note("no coinbase receiver found in %d blocks", sc.Blocks)
	}
	if d.OpReturnTx == "" {
		d.note("no OP_RETURN transaction in the sampled blocks; raise -blocks or -seed-txs")
	}

	// Busy address: confirm counts for the most-seen addresses and the coinbase receiver.
	sort.SliceStable(order, func(i, j int) bool { return tally[order[i]] > tally[order[j]] })
	cands := append([]string{}, order[:min(5, len(order))]...)
	if coinbaseAddr != "" {
		cands = append(cands, coinbaseAddr)
	}
	busy, busyN := "", -1
	for _, a := range dedupe(cands) {
		n, err := src.addrCount(a)
		if err != nil {
			d.note("count for candidate failed: %v", err)
			continue
		}
		if n > busyN {
			busy, busyN = a, n
		}
		if n > 1000 && busyN == n {
			break
		}
	}
	d.BusyCount = busyN
	if busy != "" && busyN <= 1000 {
		d.note("busiest sampled address has %d txs, not past 1000: the deep-paging edges will run on a short history", busyN)
	}
	// Single-tx address.
	single := ""
	tried := 0
	for _, a := range order {
		if a == busy || a == coinbaseAddr || tried >= 8 {
			continue
		}
		tried++
		if n, err := src.addrCount(a); err == nil && n == 1 {
			single = a
			break
		}
	}
	if single == "" {
		d.note("no single-tx address found among the sampled addresses")
	}
	// Pending.
	pending := append([]string{}, sc.Pending...)
	mem, err := src.mempool(3)
	switch {
	case errors.Is(err, errNoMempool):
		d.note("no mempool route (%v): pass -pending or -mempool-url for pending coverage", err)
	case err != nil:
		d.note("mempool: %v", err)
	default:
		for _, t := range mem {
			d.PendingTxs = append(d.PendingTxs, t.Txid)
			pending = append(pending, t.all()...)
		}
		if len(mem) == 0 {
			d.note("mempool is empty right now")
		}
	}
	pending = dedupe(pending)
	if len(pending) > 3 {
		pending = pending[:3]
	}
	if len(pending) == 0 {
		d.note("no pending address: pending coverage skipped")
	}
	if sc.Empty == "" {
		d.note("no -empty address given: empty-address coverage skipped")
	}

	add := func(label, a string) {
		if a == "" {
			return
		}
		for _, l := range d.Addrs {
			if l.Addr == a {
				return
			}
		}
		d.Addrs = append(d.Addrs, labeled{label, a})
	}
	add("coinbase", coinbaseAddr)
	add("opreturn", opretAddr)
	add("busy", busy)
	add("single", single)
	add("empty", sc.Empty)
	for i, p := range pending {
		add("pending"+strconv.Itoa(i+1), p)
	}
	d.Busy, d.Single = busy, single
	if len(d.Addrs) == 0 {
		return nil, errors.New("no address could be discovered")
	}
	return d, nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

const zeroTxid = "0000000000000000000000000000000000000000000000000000000000000000"

// buildPaths writes a line for every route family in the spec table plus the
// error cases, paging edges and the tx-export dependencies (Call 7).
func buildPaths(d *seedData) []string {
	var out []string
	g := func(p string, a ...interface{}) { out = append(out, "GET "+fmt.Sprintf(p, a...)) }
	post := func(p, body string) { out = append(out, "POST "+p+" "+body) }
	c := func(s string, a ...interface{}) {
		out = append(out, "# "+strings.NewReplacer("\n", " ", "\r", " ").Replace(fmt.Sprintf(s, a...)))
	}

	class := func(k string) { out = append(out, "#! "+k) }

	c("generated by insightcompare seed at tip %d", d.Tip)
	c("status classes: ok must answer 2xx, error must answer 4xx, any only needs both sides to agree")
	class("ok")
	for _, n := range d.Notes {
		c("note: %s", n)
	}
	for _, l := range d.Addrs {
		c("address %s: %s", l.Label, l.Addr)
	}

	c("status, sync, peer, version")
	g("status")
	for _, q := range []string{"getInfo", "getDifficulty", "getBestBlockHash", "getLastBlockHash"} {
		g("status?q=%s", q)
	}
	g("sync")
	g("peer")
	g("version")

	c("blocks and block routes; the next three are the tx-export dependencies (Call 7)")
	g("blocks?limit=1")
	g("status?q=getLastBlockHash")
	g("block/%s", d.BestHash)
	g("blocks?limit=5")
	if d.Date != "" {
		g("blocks?blockDate=%s&limit=10", d.Date)
	}
	if d.OldDate != "" && d.OldDate != d.Date {
		g("blocks?blockDate=%s&limit=200", d.OldDate)
	}
	for _, h := range dedupe([]string{d.PrevHash, d.OldHash, d.BigHash}) {
		g("block/%s", h)
	}
	g("rawblock/%s", d.PrevHash)
	g("block-index/%d", d.Tip)
	g("block-index/%d", d.Tip-1)
	if d.OldHeight > 0 {
		g("block-index/%d", d.OldHeight)
	}
	g("txs?block=%s&pageNum=0", d.BigHash)
	g("txs?block=%s&pageNum=1", d.BigHash)

	c("transactions")
	for _, id := range dedupe(append([]string{d.CoinbaseTx, d.OpReturnTx, d.PlainTx}, d.PendingTxs...)) {
		g("tx/%s", id)
		g("rawtx/%s", id)
	}

	busy, single := d.Busy, d.Single
	c("per-address routes")
	for _, l := range d.Addrs {
		a := l.Addr
		c("%s", l.Label)
		g("addr/%s", a)
		g("addr/%s?noTxList=1", a)
		for _, r := range []string{"balance", "totalReceived", "totalSent", "unconfirmedBalance", "utxo"} {
			g("addr/%s/%s", a, r)
		}
		g("addrs/%s/utxo", a)
		g("addrs/%s/unspent", a)
		g("addrs/%s/balance", a)
		g("addrs/%s/txs", a)
		g("addrs/%s/txs?from=0&to=50", a)
		g("txs?address=%s&pageNum=0", a)
	}

	if busy != "" {
		c("paging edges on the busiest address")
		for _, w := range [][2]int{{0, 1}, {0, 10}, {0, 1000}, {950, 1000}, {1000, 1050}, {1500, 1600}, {2000, 2050}, {10, 10}} {
			g("addrs/%s/txs?from=%d&to=%d", busy, w[0], w[1])
		}
		g("addr/%s?from=0&to=10", busy)
		g("addr/%s?from=1000&to=1010", busy)
		g("txs?address=%s&pageNum=1", busy)
		class("any")
		c("windows the two sides may legitimately refuse or answer differently")
		for _, w := range [][2]int{{0, 1500}, {5, 2}, {-1, 5}} {
			g("addrs/%s/txs?from=%d&to=%d", busy, w[0], w[1])
		}
		g("addrs/%s/txs?from=abc&to=5", busy)
		g("addrs/%s/txs?from=0", busy)
		g("txs?address=%s&pageNum=100", busy)
		class("ok")
	}
	if single != "" {
		c("paging past the end of a one-tx history")
		g("addrs/%s/txs?from=100&to=150", single)
	}

	var all []string
	for _, l := range d.Addrs {
		all = append(all, l.Addr)
	}
	if len(all) >= 2 {
		c("multi-address routes")
		for _, set := range multiSets(all) {
			list := strings.Join(set, ",")
			g("addrs/%s/utxo", list)
			g("addrs/%s/unspent", list)
			g("addrs/%s/balance", list)
			g("addrs/%s/txs?from=0&to=50", list)
			post("addrs/utxo", fmt.Sprintf(`{"addrs":%q}`, list))
			post("addrs/txs", fmt.Sprintf(`{"addrs":%q,"from":0,"to":50}`, list))
		}
	} else if len(all) == 1 {
		c("multi-address routes (one address only)")
		post("addrs/utxo", fmt.Sprintf(`{"addrs":%q}`, all[0]))
		post("addrs/txs", fmt.Sprintf(`{"addrs":%q,"from":0,"to":50}`, all[0]))
	}
	first := all[0]
	g("addrs/%s,%s/txs?from=0&to=50", first, first)
	class("error")
	c("over-limit lists with distinct addresses (checksums may be invalid; either way a 4xx is the answer)")
	g("addrs/%s/txs", distinctList(first, 21))
	g("addrs/%s/utxo", distinctList(first, 101))
	class("ok")

	c("fees, currency, messages")
	g("utils/estimatefee")
	g("utils/estimatefee?nbBlocks=2")
	g("utils/estimatefee?nbBlocks=2,3")
	class("any")
	g("utils/estimatefee?nbBlocks=0")
	g("utils/estimatefee?nbBlocks=abc")
	class("ok")
	g("currency")
	sig := strings.Repeat("A", 86) + "=="
	g("messages/verify?address=%s&signature=%s&message=insightcompare", first, url.QueryEscape(sig))
	post("messages/verify", fmt.Sprintf(`{"address":%q,"signature":%q,"message":"insightcompare"}`, first, sig))
	class("any")
	g("messages/verify")

	class("error")
	c("error cases; tx/send gets only payloads that cannot be a transaction and is never broadcast")
	g("addr/notanaddress")
	g("addr/notanaddress/utxo")
	g("addrs/notanaddress/txs")
	g("addr/%s", corrupt(first))
	g("tx/zz")
	g("tx/%s", zeroTxid)
	g("rawtx/zz")
	g("rawtx/%s", zeroTxid)
	g("block/zz")
	g("block/%s", zeroTxid)
	g("rawblock/%s", zeroTxid)
	g("block-index/99999999")
	g("block-index/abc")
	g("block-index/-1")
	g("nosuchroute")
	post("tx/send", `{"rawtx":"zz"}`)
	post("tx/send", `{"rawtx":"abc"}`)
	post("tx/send", `{}`)
	post("tx/send", `{"rawtx":`)
	post("tx/send", `@oversize:9437184`)
	return out
}

// multiSets picks address groups for the multi routes: all addresses (up to 6)
// and, when there are enough, the first two.
func multiSets(all []string) [][]string {
	top := all[:min(6, len(all))]
	sets := [][]string{top}
	if len(top) > 2 {
		sets = append(sets, top[:2])
	}
	return sets
}

// corrupt changes the last character so the address checksum is wrong.
func corrupt(a string) string {
	if a == "" {
		return "x"
	}
	last := a[len(a)-1]
	r := byte('2')
	if last == '2' {
		r = '3'
	}
	return a[:len(a)-1] + string(r)
}

// distinctList joins n distinct strings derived from a: the first is a itself,
// the rest replace the last characters so a listener that deduplicates before
// counting still sees n addresses.
func distinctList(a string, n int) string {
	const alpha = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"
	base := a
	if len(base) > 2 {
		base = base[:len(base)-2]
	}
	list := []string{a}
	for i := 1; len(list) < n; i++ {
		list = append(list, base+string(alpha[i/len(alpha)%len(alpha)])+string(alpha[i%len(alpha)]))
	}
	return strings.Join(list, ",")
}
