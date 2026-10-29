package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Finding kinds. Only missing and changed fail a run.
const (
	KindMissing  = "missing"
	KindChanged  = "changed"
	KindExtra    = "extra"
	KindExpected = "expected"
)

// Finding is one difference between the reference and the new listener.
type Finding struct {
	Path    string `json:"path"`
	Pointer string `json:"pointer"`
	Kind    string `json:"kind"`
	Ref     string `json:"ref"`
	Got     string `json:"got"`
	Note    string `json:"note,omitempty"`
}

// Failing reports whether the finding makes the run exit with status 1.
func (f Finding) Failing() bool { return f.Kind == KindMissing || f.Kind == KindChanged }

// Rule is one declared, non-failing difference. The table is the single
// source for the Note text of expected findings; Cite points back to the
// Task 8 brief (rules 1-7) or to the design spec (Calls).
type Rule struct {
	ID   string
	Cite string
	Text string
}

var expectedRules = map[string]Rule{
	"E1":  {"E1", "Task 8 rule 1; Task 3 ASM decision", "scriptPubKey.asm: the reference holds address text, the listener holds the script disassembly"},
	"E2":  {"E2", "Task 8 rule 2", "vin scriptPubKey.asm is absent on the listener"},
	"E3":  {"E3", "Task 8 rule 3; spec Call 2 and Task 4", "history window: item count, to and totalItems differ (converter returns the whole page, counts confirmed txs only, ignores from beyond 1000)"},
	"E4":  {"E4", "Task 8 rule 4", "blocks list rows lack difficulty"},
	"E5":  {"E5", "Task 8 rule 5", "addrs/*/balance lacks immature"},
	"E6":  {"E6", "Task 8 rule 6", "minedBy is not emitted"},
	"E7":  {"E7", "Task 8 rule 7", "vin vout/sequence zero omitted by Blockbook"},
	"E8":  {"E8", "spec Call 2 (deep paging)", "from >= 1000: the converter returns the first 1000, the listener serves the real window"},
	"E9":  {"E9", "spec Verification (ZEC)", "estimatefee -1 / messages/verify 501 are the normal ZEC answers"},
	"E10": {"E10", "Task 8 drift tolerance", "list membership differs by drift between the two requests (new block or pending tx)"},
}

// driftTolerances are numeric tolerances by pointer, applied per route family.
// Task 8: info.blocks, blockChainHeight, height and syncPercentage allow +-3
// (status and sync routes only, so block heights elsewhere stay exact);
// counters that move while a busy address is being served allow +-2.
func tolerance(route, ptr string) float64 {
	statusish := route == "status" || route == "sync"
	switch ptr {
	case "/info/blocks", "/blockChainHeight", "/height", "/syncPercentage":
		if statusish {
			return 3
		}
	case "/totalItems", "/txApperances", "/unconfirmedTxApperances":
		return 2
	}
	return 0
}

// ignoredKeys are dropped wherever they appear (time-sensitive).
var ignoredKeys = map[string]bool{
	"confirmations":          true,
	"confirmationETABlocks":  true,
	"confirmationETASeconds": true,
}

var (
	reVinAsm     = regexp.MustCompile(`/vin/\d+/scriptPubKey/asm$`)
	reBlockDiff  = regexp.MustCompile(`^/blocks/[^/]+/difficulty$`)
	reVinZero    = regexp.MustCompile(`/vin/\d+/(vout|sequence)$`)
	reAddrToken  = regexp.MustCompile(`^[A-Za-z0-9]+$`)
	reEvenHexTok = regexp.MustCompile(`^([0-9a-fA-F]{2})+$`)
)

type comparer struct {
	mode    string
	path    string
	route   string
	query   url.Values
	out     []Finding
	ignores []string
}

// Compare walks ref and got (decoded JSON, numbers as json.Number or float64)
// and returns the differences for the request path. mode is "converter" or
// "stock".
func Compare(ref, got interface{}, mode string, path string) []Finding {
	return compareWith(ref, got, mode, path, nil)
}

func compareWith(ref, got interface{}, mode, path string, ignores []string) []Finding {
	route, q := splitPath(path)
	c := &comparer{mode: mode, path: path, route: route, query: q, ignores: ignores}
	c.walk("", ref, got, nil)
	return c.out
}

func splitPath(p string) (string, url.Values) {
	p = strings.TrimPrefix(p, "/")
	route, rawq, _ := strings.Cut(p, "?")
	q, _ := url.ParseQuery(rawq)
	return strings.TrimSuffix(route, "/"), q
}

func (c *comparer) add(kind, ptr string, ref, got interface{}, rule string) {
	f := Finding{Path: c.path, Pointer: ptr, Kind: kind, Ref: show(ref), Got: show(got)}
	if r, ok := expectedRules[rule]; ok {
		f.Note = r.ID + ": " + r.Text
	} else if rule != "" {
		f.Note = rule
	}
	c.out = append(c.out, f)
}

func show(v interface{}) string {
	if v == nil {
		return "null"
	}
	if s, ok := v.(string); ok {
		return truncate(strconv.Quote(s), 140)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return truncate(string(b), 140)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

func esc(k string) string {
	return strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1")
}

func lastKey(ptr string) string {
	i := strings.LastIndex(ptr, "/")
	return ptr[i+1:]
}

func (c *comparer) isIgnoredPtr(ptr string) bool {
	for _, ig := range c.ignores {
		if ig == ptr || (strings.HasSuffix(ig, "*") && strings.HasPrefix(ptr, strings.TrimSuffix(ig, "*"))) {
			return true
		}
		if strings.HasPrefix(ig, "**/") && strings.HasSuffix(ptr, strings.TrimPrefix(ig, "**")) {
			return true
		}
	}
	return false
}

func (c *comparer) isHistory() bool {
	return strings.HasPrefix(c.route, "addrs/") && (strings.HasSuffix(c.route, "/txs") || strings.Contains(c.route, "/txs/")) || c.route == "addrs/txs"
}

// deepPaging reports a from offset at or beyond 1000 (spec Call 2).
func (c *comparer) deepPaging() bool {
	if v := c.query.Get("from"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1000 {
			return true
		}
	}
	parts := strings.Split(c.route, "/")
	for i, p := range parts {
		if p == "txs" && i+1 < len(parts) {
			if n, err := strconv.Atoi(parts[i+1]); err == nil && n >= 1000 {
				return true
			}
		}
	}
	return false
}

func (c *comparer) walk(ptr string, r, g interface{}, rparent map[string]interface{}) {
	if c.isIgnoredPtr(ptr) {
		return
	}
	switch rv := r.(type) {
	case map[string]interface{}:
		gv, ok := g.(map[string]interface{})
		if !ok {
			c.add(KindChanged, ptr, typeName(r), typeName(g), "type differs")
			return
		}
		c.walkMap(ptr, rv, gv)
	case []interface{}:
		gv, ok := g.([]interface{})
		if !ok {
			c.add(KindChanged, ptr, typeName(r), typeName(g), "type differs")
			return
		}
		c.walkArray(ptr, rv, gv)
	default:
		c.scalar(ptr, r, g, rparent)
	}
}

func typeName(v interface{}) string {
	switch v.(type) {
	case nil:
		return "null"
	case map[string]interface{}:
		return "object"
	case []interface{}:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	default:
		return "number"
	}
}

func sortedKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (c *comparer) walkMap(ptr string, r, g map[string]interface{}) {
	for _, k := range sortedKeys(r) {
		if ignoredKeys[k] {
			continue
		}
		cp := ptr + "/" + esc(k)
		if c.isIgnoredPtr(cp) {
			continue
		}
		gv, ok := g[k]
		if !ok {
			if rule := c.expectedMissing(cp, r[k]); rule != "" {
				c.add(KindExpected, cp, r[k], "(absent)", rule)
			} else {
				c.add(KindMissing, cp, r[k], "(absent)", "")
			}
			continue
		}
		c.walk(cp, r[k], gv, r)
	}
	for _, k := range sortedKeys(g) {
		if ignoredKeys[k] {
			continue
		}
		if _, ok := r[k]; !ok {
			cp := ptr + "/" + esc(k)
			if c.isIgnoredPtr(cp) {
				continue
			}
			c.add(KindExtra, cp, "(absent)", g[k], "")
		}
	}
}

// expectedMissing returns the rule id when a key present in ref and absent in
// got is a declared difference.
func (c *comparer) expectedMissing(ptr string, refVal interface{}) string {
	switch {
	case reVinAsm.MatchString(ptr):
		return "E2"
	case reBlockDiff.MatchString(ptr):
		return "E4"
	case lastKey(ptr) == "minedBy":
		return "E6"
	case ptr == "/immature" && strings.HasPrefix(c.route, "addrs/") && strings.HasSuffix(c.route, "/balance"):
		return "E5"
	case reVinZero.MatchString(ptr):
		if rat, ok := toRat(refVal); ok && rat.Sign() == 0 {
			return "E7"
		}
	}
	return ""
}

func (c *comparer) walkArray(ptr string, r, g []interface{}) {
	switch {
	case (ptr == "/items" || ptr == "/txs") && keyedBy(r, g, "txid"):
		c.historyArray(ptr, r, g)
	case ptr == "/blocks" && keyedBy(r, g, "hash"):
		c.blocksArray(ptr, r, g)
	case ptr == "" && keyedBy(r, g, "txid") && hasKey(r, g, "vout"):
		c.utxoArray(ptr, r, g)
	case ptr == "/transactions":
		c.txidList(ptr, r, g)
	default:
		for i := range r {
			cp := ptr + "/" + strconv.Itoa(i)
			if i >= len(g) {
				c.add(KindMissing, cp, r[i], "(absent)", "")
				continue
			}
			c.walk(cp, r[i], g[i], nil)
		}
		for i := len(r); i < len(g); i++ {
			c.add(KindExtra, ptr+"/"+strconv.Itoa(i), "(absent)", g[i], "")
		}
	}
}

// keyedBy is true when both arrays are non-trivially objects carrying key.
func keyedBy(r, g []interface{}, key string) bool {
	if len(r) == 0 && len(g) == 0 {
		return false
	}
	for _, arr := range [][]interface{}{r, g} {
		for _, e := range arr {
			m, ok := e.(map[string]interface{})
			if !ok {
				return false
			}
			if _, ok := m[key].(string); !ok {
				return false
			}
		}
	}
	return true
}

func hasKey(r, g []interface{}, key string) bool {
	for _, arr := range [][]interface{}{r, g} {
		for _, e := range arr {
			if _, ok := e.(map[string]interface{})[key]; !ok {
				return false
			}
		}
	}
	return true
}

type keyed struct {
	order []string
	byKey map[string]map[string]interface{}
	dups  []string
}

func indexBy(arr []interface{}, key func(map[string]interface{}) string) keyed {
	k := keyed{byKey: map[string]map[string]interface{}{}}
	for _, e := range arr {
		m := e.(map[string]interface{})
		id := key(m)
		if _, dup := k.byKey[id]; dup {
			k.dups = append(k.dups, id)
			continue
		}
		k.byKey[id] = m
		k.order = append(k.order, id)
	}
	return k
}

func strKey(name string) func(map[string]interface{}) string {
	return func(m map[string]interface{}) string { return m[name].(string) }
}

// historyArray matches items/txs elements by txid and compares only the txids
// present on both sides (Task 8). Count and window differences are expected.
func (c *comparer) historyArray(ptr string, r, g []interface{}) {
	rk, gk := indexBy(r, strKey("txid")), indexBy(g, strKey("txid"))
	for _, d := range gk.dups {
		c.add(KindChanged, ptr, "unique txids", "duplicate txid "+d, "listener returned the same txid twice in one page")
	}
	var common []string
	for _, id := range rk.order {
		if gm, ok := gk.byKey[id]; ok {
			common = append(common, id)
			c.walk(ptr+"/"+id, rk.byKey[id], gm, nil)
		}
	}
	rOnly, gOnly := len(rk.order)-len(common), len(gk.order)-len(common)
	rule := "E3"
	if c.deepPaging() {
		rule = "E8"
	}
	if len(common) == 0 && len(rk.order) > 0 && len(gk.order) > 0 && !c.deepPaging() {
		c.add(KindChanged, ptr, fmt.Sprintf("%d items", len(rk.order)), fmt.Sprintf("%d items", len(gk.order)), "no txid in common between the two pages")
		return
	}
	if rOnly > 0 || gOnly > 0 {
		c.add(KindExpected, ptr, fmt.Sprintf("%d items", len(rk.order)), fmt.Sprintf("%d items", len(gk.order)),
			fmt.Sprintf("%s: %s (common %d, ref-only %d, got-only %d)", rule, expectedRules[rule].Text, len(common), rOnly, gOnly))
	}
	// Relative order of the common txids must agree, except between txs of the
	// same block where either side may order them freely.
	pos := map[string]int{}
	for i, id := range gk.order {
		pos[id] = i
	}
	hard := 0
	soft := 0
	for i := 0; i < len(common); i++ {
		for j := i + 1; j < len(common); j++ {
			if pos[common[i]] > pos[common[j]] {
				a, b := rk.byKey[common[i]], rk.byKey[common[j]]
				if sameBlock(a, b) {
					soft++
				} else {
					hard++
				}
			}
		}
	}
	if hard > 0 {
		c.add(KindChanged, ptr, "newest-first order", fmt.Sprintf("%d out-of-order pairs across blocks", hard), "order of common txids differs")
	} else if soft > 0 {
		c.add(KindExpected, ptr, "tx order within a block", fmt.Sprintf("%d reordered pairs inside one block", soft), "ordering inside one block is not defined")
	}
}

func sameBlock(a, b map[string]interface{}) bool {
	for _, k := range []string{"blockheight", "blockHeight"} {
		x, ok1 := toRat(a[k])
		y, ok2 := toRat(b[k])
		if ok1 && ok2 {
			return x.Cmp(y) == 0
		}
	}
	return false
}

func (c *comparer) blocksArray(ptr string, r, g []interface{}) {
	rk, gk := indexBy(r, strKey("hash")), indexBy(g, strKey("hash"))
	common := 0
	for _, id := range rk.order {
		if gm, ok := gk.byKey[id]; ok {
			common++
			c.walk(ptr+"/"+id, rk.byKey[id], gm, nil)
		}
	}
	if common == 0 && len(rk.order) > 0 && len(gk.order) > 0 {
		rh, ok1 := toRat(rk.byKey[rk.order[0]]["height"])
		gh, ok2 := toRat(gk.byKey[gk.order[0]]["height"])
		near := false
		if ok1 && ok2 {
			d := new(big.Rat).Sub(rh, gh)
			near = d.Abs(d).Cmp(big.NewRat(3, 1)) <= 0
		}
		if near {
			c.add(KindExpected, ptr, "heights "+rh.RatString(), "heights "+gh.RatString(), expectedRules["E10"].ID+": "+expectedRules["E10"].Text)
		} else {
			c.add(KindChanged, ptr, fmt.Sprintf("%d blocks", len(rk.order)), fmt.Sprintf("%d blocks", len(gk.order)), "no block hash in common")
		}
		return
	}
	if len(rk.order) != common || len(gk.order) != common {
		c.add(KindExpected, ptr, fmt.Sprintf("%d blocks", len(rk.order)), fmt.Sprintf("%d blocks", len(gk.order)), expectedRules["E10"].ID+": "+expectedRules["E10"].Text)
	}
}

func (c *comparer) utxoArray(ptr string, r, g []interface{}) {
	key := func(m map[string]interface{}) string {
		return m["txid"].(string) + ":" + show(m["vout"])
	}
	rk, gk := indexBy(r, key), indexBy(g, key)
	for _, id := range rk.order {
		gm, ok := gk.byKey[id]
		if !ok {
			c.add(KindMissing, "/"+id, "utxo present", "(absent)", "utxo only on the reference")
			continue
		}
		c.walk("/"+id, rk.byKey[id], gm, nil)
	}
	for _, id := range gk.order {
		if _, ok := rk.byKey[id]; !ok {
			c.add(KindChanged, "/"+id, "(absent)", "utxo present", "utxo only on the listener")
		}
	}
}

// txidList compares the addr "transactions" list as a set. A few ref-only
// entries are window drift; more than that is a real difference.
func (c *comparer) txidList(ptr string, r, g []interface{}) {
	gset := map[string]bool{}
	for _, e := range g {
		if s, ok := e.(string); ok {
			gset[s] = true
		}
	}
	rset := map[string]bool{}
	var rOnly []string
	for _, e := range r {
		s, ok := e.(string)
		if !ok {
			c.add(KindChanged, ptr, show(e), "string expected", "transactions entry is not a txid")
			continue
		}
		rset[s] = true
		if !gset[s] {
			rOnly = append(rOnly, s)
		}
	}
	var gOnly []string
	for _, e := range g {
		if s, ok := e.(string); ok && !rset[s] {
			gOnly = append(gOnly, s)
		}
	}
	switch {
	case len(rOnly) > 2:
		c.add(KindChanged, ptr, fmt.Sprintf("%d txids not on listener", len(rOnly)), fmt.Sprintf("%d txids", len(g)), "transactions list differs, first missing "+rOnly[0])
	case len(rOnly) > 0 || len(gOnly) > 0:
		c.add(KindExpected, ptr, fmt.Sprintf("%d txids", len(r)), fmt.Sprintf("%d txids", len(g)), expectedRules["E10"].ID+": "+expectedRules["E10"].Text)
	}
}

func (c *comparer) scalar(ptr string, r, g interface{}, rparent map[string]interface{}) {
	key := lastKey(ptr)
	if c.route == "currency" && ptr == "/data/rate" {
		return // presence only (Task 8)
	}
	if key == "asm" {
		if rs, ok := r.(string); ok {
			if gs, ok := g.(string); ok && rs != gs && isAddressText(rs, rparent) {
				c.add(KindExpected, ptr, r, g, "E1")
				return
			}
		}
	}
	if strings.HasPrefix(c.route, "utils/estimatefee") {
		if rat, ok := toRat(g); ok && rat.Cmp(big.NewRat(-1, 1)) == 0 {
			if rr, ok := toRat(r); !ok || rr.Cmp(rat) != 0 {
				c.add(KindExpected, ptr, r, g, "E9")
				return
			}
		}
	}
	if tol := tolerance(c.route, ptr); tol > 0 {
		rr, ok1 := toRat(r)
		gg, ok2 := toRat(g)
		if ok1 && ok2 {
			d := new(big.Rat).Sub(rr, gg)
			d.Abs(d)
			if d.Cmp(new(big.Rat).SetFloat64(tol)) <= 0 {
				return
			}
		}
	}
	if c.isHistory() && (ptr == "/to" || ptr == "/totalItems") && !c.equal(r, g) {
		rule := "E3"
		if c.deepPaging() {
			rule = "E8"
		}
		if ptr == "/totalItems" {
			// The listener counts confirmed plus unconfirmed, so it can only be
			// larger than the converter's confirmed-only count.
			rr, ok1 := toRat(r)
			gg, ok2 := toRat(g)
			if ok1 && ok2 && gg.Cmp(rr) < 0 {
				c.add(KindChanged, ptr, r, g, "listener totalItems is smaller than the converter's")
				return
			}
		}
		c.add(KindExpected, ptr, r, g, rule)
		return
	}
	if !c.equal(r, g) {
		c.add(KindChanged, ptr, r, g, "")
	}
}

// isAddressText reports whether a reference asm value is address text rather
// than a disassembly: a single token that is not an opcode and, when the sibling
// addresses are known, equals one of them.
func isAddressText(s string, parent map[string]interface{}) bool {
	if parent != nil {
		if addrs, ok := parent["addresses"].([]interface{}); ok {
			for _, a := range addrs {
				if as, ok := a.(string); ok && as == s {
					return true
				}
			}
		}
	}
	return s != "" && !strings.HasPrefix(s, "OP_") && reAddrToken.MatchString(s) && !reEvenHexTok.MatchString(s)
}

func toRat(v interface{}) (*big.Rat, bool) {
	switch n := v.(type) {
	case json.Number:
		return new(big.Rat).SetString(n.String())
	case float64:
		if n != n { // NaN
			return nil, false
		}
		return new(big.Rat).SetFloat64(n), true
	case int:
		return big.NewRat(int64(n), 1), true
	case int64:
		return big.NewRat(n, 1), true
	}
	return nil, false
}

// equal compares scalars. Numbers compare by value in both modes (the listener
// writes 8-decimal tokens where the converter writes shortest form); stock mode
// also equates numeric strings with numbers.
func (c *comparer) equal(r, g interface{}) bool {
	if r == nil || g == nil {
		return r == nil && g == nil
	}
	if rr, ok := toRat(r); ok {
		if gg, ok := toRat(g); ok {
			return rr.Cmp(gg) == 0
		}
		if gs, ok := g.(string); ok && c.mode == "stock" {
			if gg, ok := new(big.Rat).SetString(strings.TrimSpace(gs)); ok {
				return rr.Cmp(gg) == 0
			}
		}
		return false
	}
	switch rv := r.(type) {
	case string:
		switch gv := g.(type) {
		case string:
			if rv == gv {
				return true
			}
			if c.mode == "stock" {
				a, ok1 := new(big.Rat).SetString(strings.TrimSpace(rv))
				b, ok2 := new(big.Rat).SetString(strings.TrimSpace(gv))
				return ok1 && ok2 && a.Cmp(b) == 0
			}
			return false
		default:
			if gg, ok := toRat(g); ok && c.mode == "stock" {
				if rr, ok := new(big.Rat).SetString(strings.TrimSpace(rv)); ok {
					return rr.Cmp(gg) == 0
				}
			}
			return false
		}
	case bool:
		gv, ok := g.(bool)
		return ok && rv == gv
	}
	return false
}

// Response is one HTTP exchange as seen by the tool.
type Response struct {
	Status  int
	Header  map[string][]string
	Body    []byte
	JSON    interface{}
	IsJSON  bool
	Err     string
	Latency int64 // microseconds
}

// DecodeJSON parses a whole body with numbers kept exact.
func DecodeJSON(body []byte) (interface{}, bool) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v interface{}
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	if dec.More() {
		return nil, false
	}
	return v, true
}

// CompareResponse applies the status, error-body and JSON rules for one path.
func CompareResponse(ref, got *Response, mode, path string, ignores []string) []Finding {
	var out []Finding
	mk := func(kind, ptr, r, g, note string) {
		out = append(out, Finding{Path: path, Pointer: ptr, Kind: kind, Ref: r, Got: g, Note: note})
	}
	if ref.Err != "" || got.Err != "" {
		if ref.Err != "" {
			mk(KindChanged, "transport", ref.Err, statusText(got), "reference request failed")
		}
		if got.Err != "" {
			mk(KindChanged, "transport", statusText(ref), got.Err, "listener request failed")
		}
		return out
	}
	if ref.Status != got.Status {
		switch {
		case got.Status == 501:
			mk(KindExpected, "http status", statusText(ref), statusText(got), "E9: "+expectedRules["E9"].Text)
		case ref.Status == 404 && got.Status/100 == 2:
			mk(KindExtra, "http status", statusText(ref), statusText(got), "route absent on the reference; check it against stock Insight")
		default:
			mk(KindChanged, "http status", statusText(ref), statusText(got), "")
		}
		return out
	}
	if ref.Status >= 400 {
		rm, ok1 := ref.JSON.(map[string]interface{})
		gm, ok2 := got.JSON.(map[string]interface{})
		if ref.IsJSON != got.IsJSON {
			mk(KindChanged, "body", bodyKind(ref), bodyKind(got), "error body type differs")
		} else if ok1 && ok2 && show(rm["status"]) != show(gm["status"]) {
			mk(KindChanged, "/status", show(rm["status"]), show(gm["status"]), "error status key differs")
		}
		return out
	}
	switch {
	case ref.IsJSON && got.IsJSON:
		out = append(out, compareWith(ref.JSON, got.JSON, mode, path, ignores)...)
	case ref.IsJSON != got.IsJSON:
		mk(KindChanged, "body", bodyKind(ref), bodyKind(got), "one side is not JSON")
	default:
		if strings.TrimSpace(string(ref.Body)) != strings.TrimSpace(string(got.Body)) {
			mk(KindChanged, "body", truncate(string(ref.Body), 140), truncate(string(got.Body), 140), "non-JSON bodies differ")
		}
	}
	return out
}

func statusText(r *Response) string {
	if r.Err != "" {
		return "error"
	}
	return strconv.Itoa(r.Status)
}

func bodyKind(r *Response) string {
	if r.IsJSON {
		return "json"
	}
	return "non-json: " + truncate(strings.TrimSpace(string(r.Body)), 60)
}
