// Command insightcompare requests the same Insight paths from a reference
// (the Node converter or stock Insight) and from the native Blockbook Insight
// listener, and reports every difference. It only issues GET, OPTIONS and
// deliberately malformed POSTs and refuses to run anything that could
// broadcast a transaction.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type pathSpec struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Body   string `json:"body,omitempty"`
	Expect string `json:"expect"` // ok, error or any; set by `#! <class>` lines
	Line   int    `json:"line"`
}

func (p pathSpec) String() string {
	if p.Method == "POST" {
		return "POST " + p.Path + " " + truncate(p.Body, 60)
	}
	return "GET " + p.Path
}

// loadPaths reads one request per line: `GET path` or `POST path <json>`.
// Blank lines and lines starting with # are skipped, except `#! ok|error|any`,
// which sets the status class (default ok) for the lines that follow:
// ok must answer 2xx on both sides, new is ok for a route the reference lacks
// (a ref non-2xx is then reported as extra), error must answer 4xx (malformed
// input), any only needs both sides to agree. A 5xx fails in every class.
func loadPaths(r io.Reader) ([]pathSpec, error) {
	var out []pathSpec
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	n := 0
	expect := "ok"
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if class, ok := strings.CutPrefix(line, "#!"); ok {
			class = strings.TrimSpace(class)
			if class != "ok" && class != "error" && class != "any" && class != "new" {
				return nil, fmt.Errorf("line %d: unknown class %q (want ok, error, any or new)", n, class)
			}
			expect = class
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.SplitN(line, " ", 3)
		if len(f) < 2 {
			return nil, fmt.Errorf("line %d: want `GET path` or `POST path <json>`", n)
		}
		spec := pathSpec{Method: strings.ToUpper(f[0]), Path: strings.TrimPrefix(f[1], "/"), Expect: expect, Line: n}
		switch spec.Method {
		case "GET":
			if len(f) > 2 {
				return nil, fmt.Errorf("line %d: GET takes no body", n)
			}
		case "POST":
			if len(f) > 2 {
				spec.Body = strings.TrimSpace(f[2])
			}
		default:
			return nil, fmt.Errorf("line %d: method %q is not allowed (GET and POST only)", n, f[0])
		}
		out = append(out, spec)
	}
	return out, sc.Err()
}

// postAllow lists the only POST routes the tool will send. tx/send is further
// restricted to bodies that cannot be a transaction.
var postAllow = map[string]bool{"tx/send": true, "addrs/utxo": true, "addrs/txs": true, "messages/verify": true}

// oversizeMax bounds the generated oversized body.
const oversizeMax = 64 << 20

// expandBody turns the `@oversize:N` directive into a body of N bytes whose
// rawtx is not hex. Other bodies are used verbatim.
func expandBody(body string) (string, error) {
	rest, ok := strings.CutPrefix(body, "@oversize:")
	if !ok {
		return body, nil
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < 16 || n > oversizeMax {
		return "", fmt.Errorf("bad @oversize size %q", rest)
	}
	const pre, post = `{"rawtx":"`, `"}`
	return pre + strings.Repeat("z", n-len(pre)-len(post)) + post, nil
}

// unsafeBroadcast reports why a tx/send body could be a real transaction.
// It is an allow-list: a body is safe only when it is not decodable as JSON
// object or form data and is not bare hex, or when every key that matches
// rawtx (any case) is missing, not a string, odd-length or not hexadecimal.
func unsafeBroadcast(body string) string {
	const why = "rawtx is even-length hex and could be a real transaction"
	trim := strings.TrimSpace(body)
	if reEvenHexTok.MatchString(trim) {
		return "body is bare even-length hex"
	}
	var cands []string
	var m map[string]interface{}
	if json.Unmarshal([]byte(trim), &m) == nil {
		for k, v := range m {
			if strings.EqualFold(k, "rawtx") {
				s, _ := v.(string)
				cands = append(cands, s)
			}
		}
	} else if vals, err := url.ParseQuery(trim); err == nil {
		for k, vs := range vals {
			if strings.EqualFold(k, "rawtx") {
				cands = append(cands, vs...)
			}
		}
	}
	for _, raw := range cands {
		if reEvenHexTok.MatchString(strings.TrimSpace(raw)) {
			return why
		}
	}
	return ""
}

func validateSpecs(specs []pathSpec) error {
	for _, s := range specs {
		route, _ := splitPath(s.Path)
		if s.Method != "POST" {
			continue
		}
		if !postAllow[route] {
			return fmt.Errorf("line %d: POST %s is not an allowed route", s.Line, route)
		}
		body, err := expandBody(s.Body)
		if err != nil {
			return fmt.Errorf("line %d: %v", s.Line, err)
		}
		if route == "tx/send" {
			if why := unsafeBroadcast(body); why != "" {
				return fmt.Errorf("line %d: refusing tx/send: %s", s.Line, why)
			}
		}
	}
	return nil
}

type limiter struct{ tick <-chan time.Time }

func newLimiter(rate float64) *limiter {
	if rate <= 0 {
		return &limiter{}
	}
	return &limiter{tick: time.NewTicker(time.Duration(float64(time.Second) / rate)).C}
}

func (l *limiter) wait() {
	if l != nil && l.tick != nil {
		<-l.tick
	}
}

type client struct {
	http *http.Client
	lim  *limiter
	ua   string
}

func newClient(rate float64, timeout time.Duration) *client {
	tr := &http.Transport{MaxIdleConnsPerHost: 64, IdleConnTimeout: 30 * time.Second}
	return &client{
		http: &http.Client{Timeout: timeout, Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		lim:  newLimiter(rate),
		ua:   "insightcompare/1",
	}
}

const maxBody = 32 << 20

func (c *client) do(method, base, path, body string, hdr map[string]string, limited bool) *Response {
	if limited {
		c.lim.wait()
	}
	u := strings.TrimRight(base, "/") + "/" + strings.TrimPrefix(path, "/")
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, u, rd)
	if err != nil {
		return &Response{Err: err.Error()}
	}
	req.Header.Set("User-Agent", c.ua)
	if method == "POST" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return &Response{Err: scrub(err.Error()), Latency: time.Since(start).Microseconds()}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	r := &Response{Status: resp.StatusCode, Header: resp.Header, Body: data, Latency: time.Since(start).Microseconds()}
	if err != nil {
		r.Err = scrub(err.Error())
		return r
	}
	r.JSON, r.IsJSON = DecodeJSON(data)
	return r
}

// scrub removes the request URL from transport errors so reports do not carry
// addresses of internal hosts twice; the host is already in the report header.
func scrub(s string) string { return truncate(s, 200) }

func getJSON(c *client, base, path string, v *interface{}) (*Response, error) {
	r := c.do("GET", base, path, "", nil, true)
	if r.Err != "" {
		return r, fmt.Errorf("GET %s: %s", path, r.Err)
	}
	if r.Status != 200 || !r.IsJSON {
		return r, fmt.Errorf("GET %s: status %d, json=%v", path, r.Status, r.IsJSON)
	}
	*v = r.JSON
	return r, nil
}

// Result is the outcome for one path.
type Result struct {
	Spec      pathSpec  `json:"spec"`
	RefStatus int       `json:"refStatus"`
	NewStatus int       `json:"newStatus"`
	RefMicros int64     `json:"refMicros"`
	NewMicros int64     `json:"newMicros"`
	CORSRef   string    `json:"corsRef"`
	CORSNew   string    `json:"corsNew"`
	OptRef    int       `json:"optionsRefStatus"`
	OptNew    int       `json:"optionsNewStatus"`
	LinkNew   string    `json:"linkNew,omitempty"`
	Findings  []Finding `json:"findings"`
}

func headerOf(r *Response, k string) string {
	if v := r.Header[http.CanonicalHeaderKey(k)]; len(v) > 0 {
		return v[0]
	}
	return ""
}

type config struct {
	Ref, New    string
	Mode        string
	Concurrency int
	Ignores     []string
	Origin      string
	ZebraNew    bool
	RefLacks    []string
	NewAlt      string
	NoOversize  bool
}

// refLacks reports whether the path's route is declared as missing on the reference.
func (cfg *config) refLacks(path string) bool {
	route, _ := splitPath(path)
	for _, p := range cfg.RefLacks {
		if route == p || strings.HasPrefix(route, p+"/") {
			return true
		}
	}
	return false
}

func (cfg *config) runOne(c *client, spec pathSpec) Result {
	body, _ := expandBody(spec.Body)
	var ref, got, oref, onew, alt *Response
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); ref = c.do(spec.Method, cfg.Ref, spec.Path, body, nil, true) }()
	go func() { defer wg.Done(); got = c.do(spec.Method, cfg.New, spec.Path, body, nil, true) }()
	wg.Wait()
	if cfg.NewAlt != "" {
		alt = c.do(spec.Method, cfg.NewAlt, spec.Path, body, nil, true)
	}
	oh := map[string]string{"Origin": cfg.Origin, "Access-Control-Request-Method": spec.Method}
	wg.Add(2)
	go func() { defer wg.Done(); oref = c.do("OPTIONS", cfg.Ref, spec.Path, "", oh, true) }()
	go func() { defer wg.Done(); onew = c.do("OPTIONS", cfg.New, spec.Path, "", oh, true) }()
	wg.Wait()

	res := Result{Spec: spec, RefStatus: ref.Status, NewStatus: got.Status, RefMicros: ref.Latency, NewMicros: got.Latency,
		OptRef: oref.Status, OptNew: onew.Status,
		CORSRef: headerOf(oref, "Access-Control-Allow-Origin"), CORSNew: headerOf(onew, "Access-Control-Allow-Origin"),
		LinkNew: headerOf(got, "Link")}
	opts := Options{Expect: spec.Expect, Ignores: cfg.Ignores, ZebraNew: cfg.ZebraNew, RefLacks: cfg.refLacks(spec.Path)}
	res.Findings = CompareResponse(ref, got, cfg.Mode, spec.Path, opts)
	res.Findings = append(res.Findings, corsFindings(spec.Path, oref, onew)...)
	if ref.Err == "" && got.Err == "" && ref.Status/100 == 2 && got.Status/100 == 2 {
		if fs := getCORS(spec.Path, ref, got); fs != nil {
			res.Findings = append(res.Findings, fs...)
		}
	}
	if alt != nil {
		for _, f := range CompareResponse(got, alt, "converter", spec.Path, Options{Expect: spec.Expect, Ignores: cfg.Ignores, ZebraNew: cfg.ZebraNew}) {
			f.Pointer = "[new-alt prefix] " + f.Pointer
			if f.Kind == KindExtra || f.Kind == KindExpected {
				continue
			}
			res.Findings = append(res.Findings, f)
		}
	}
	return res
}

func corsFindings(path string, oref, onew *Response) []Finding {
	if oref.Err != "" || onew.Err != "" {
		return []Finding{{Path: path, Pointer: "OPTIONS", Kind: KindChanged, Ref: errOr(oref), Got: errOr(onew), Note: "OPTIONS request failed"}}
	}
	r, g := headerOf(oref, "Access-Control-Allow-Origin"), headerOf(onew, "Access-Control-Allow-Origin")
	switch {
	case r != "" && g == "":
		return []Finding{{Path: path, Pointer: "OPTIONS Access-Control-Allow-Origin", Kind: KindMissing, Ref: r, Got: "(absent)"}}
	case r == "" && g != "":
		return []Finding{{Path: path, Pointer: "OPTIONS Access-Control-Allow-Origin", Kind: KindExtra, Ref: "(absent)", Got: g}}
	case r == "" && g == "":
		return []Finding{{Path: path, Pointer: "OPTIONS Access-Control-Allow-Origin", Kind: KindChanged, Ref: "(absent)", Got: "(absent)", Note: "no CORS header on either side"}}
	}
	return nil
}

// getCORS requires the allow-origin header on the actual GET/POST response, which
// is what a browser reads, whenever the reference sets it.
func getCORS(path string, ref, got *Response) []Finding {
	r, g := headerOf(ref, "Access-Control-Allow-Origin"), headerOf(got, "Access-Control-Allow-Origin")
	if r != "" && g == "" {
		return []Finding{{Path: path, Pointer: "response Access-Control-Allow-Origin", Kind: KindMissing, Ref: r, Got: "(absent)"}}
	}
	return nil
}

func errOr(r *Response) string {
	if r.Err != "" {
		return r.Err
	}
	return strconv.Itoa(r.Status)
}

func (cfg *config) runAll(c *client, specs []pathSpec) []Result {
	results := make([]Result, len(specs))
	idx := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < cfg.Concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				results[i] = cfg.runOne(c, specs[i])
			}
		}()
	}
	for i := range specs {
		idx <- i
	}
	close(idx)
	wg.Wait()
	return results
}

// LoadSide holds latency statistics for one side of one path.
type LoadSide struct {
	Requests int     `json:"requests"`
	OK       int     `json:"ok"`
	Non2xx   int     `json:"non2xx"`
	BadBody  int     `json:"badBody"`
	Errors   int     `json:"errors"`
	P50ms    float64 `json:"p50ms"`
	P95ms    float64 `json:"p95ms"`
	Maxms    float64 `json:"maxms"`
}

// LoadResult compares both sides for one path under N parallel repeats.
type LoadResult struct {
	Path string   `json:"path"`
	Ref  LoadSide `json:"ref"`
	New  LoadSide `json:"new"`
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(float64(len(sorted))*p+0.9999999) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}

func summarize(rs []*Response) LoadSide {
	s := LoadSide{Requests: len(rs)}
	var lat []float64
	for _, r := range rs {
		switch {
		case r.Err != "":
			s.Errors++
		case r.Status/100 == 2 && !r.IsJSON:
			s.BadBody++
		case r.Status/100 == 2:
			s.OK++
		default:
			s.Non2xx++
		}
		lat = append(lat, float64(r.Latency)/1000)
	}
	sort.Float64s(lat)
	s.P50ms, s.P95ms = percentile(lat, 0.5), percentile(lat, 0.95)
	if len(lat) > 0 {
		s.Maxms = lat[len(lat)-1]
	}
	return s
}

// pickLoadPaths takes GET paths that answered 200 on both sides, at most one
// per first path segment first, then more up to max.
func pickLoadPaths(results []Result, max int) []string {
	var picked []string
	seen := map[string]bool{}
	used := map[string]bool{}
	for pass := 0; pass < 2; pass++ {
		for _, r := range results {
			p := r.Spec.Path
			if r.Spec.Method != "GET" || r.RefStatus != 200 || r.NewStatus != 200 || used[p] || len(picked) >= max {
				continue
			}
			seg, _, _ := strings.Cut(p, "/")
			seg, _, _ = strings.Cut(seg, "?")
			if pass == 0 && seen[seg] {
				continue
			}
			seen[seg] = true
			used[p] = true
			picked = append(picked, p)
		}
	}
	return picked
}

// runLoad fires n parallel GETs at each side for every picked path, both sides
// at once, with a pause between paths. It deliberately bypasses the per-request
// rate limiter; -load-pause is the throttle.
func (cfg *config) runLoad(c *client, paths []string, n int, pause time.Duration) []LoadResult {
	var out []LoadResult
	for _, p := range paths {
		refs, gots := make([]*Response, n), make([]*Response, n)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(2)
			go func(i int) { defer wg.Done(); refs[i] = c.do("GET", cfg.Ref, p, "", nil, false) }(i)
			go func(i int) { defer wg.Done(); gots[i] = c.do("GET", cfg.New, p, "", nil, false) }(i)
		}
		wg.Wait()
		out = append(out, LoadResult{Path: p, Ref: summarize(refs), New: summarize(gots)})
		time.Sleep(pause)
	}
	return out
}

// normBase makes two spellings of one base URL comparable.
func normBase(u string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(u), "/"))
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("insightcompare", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		ref         = fs.String("ref", "", "reference base URL, e.g. http://10.100.0.x:9876/api")
		newBase     = fs.String("new", "", "base URL of the new native listener")
		paths       = fs.String("paths", "", "file with one request per line: `GET path` or `POST path <json>`")
		mode        = fs.String("mode", "converter", "converter or stock")
		out         = fs.String("out", "insightcompare-report.md", "short human summary (markdown)")
		jsonOut     = fs.String("json", "", "machine-readable report (default: -out with .json)")
		conc        = fs.Int("concurrency", 4, "paths compared in parallel")
		rate        = fs.Float64("rate", 8, "global request limit per second across both sides (0 = unlimited)")
		timeout     = fs.Duration("timeout", 30*time.Second, "per request timeout")
		load        = fs.Int("load", 0, "parallel repeats per path for the latency phase (0 = off, at most 64)")
		loadMax     = fs.Int("load-paths", 12, "paths used by the latency phase")
		loadPause   = fs.Duration("load-pause", time.Second, "pause between latency waves")
		ignore      = fs.String("ignore", "", "comma-separated JSON pointers to ignore; trailing * is a prefix, **/name matches a suffix")
		zebraNew    = fs.Bool("zebra-new", false, "the listener's coin is Zebra-backed (ZEC): messages/verify 501 and estimatefee -1 are expected; without it they fail")
		newAlt      = fs.String("new-alt", "", "second base URL of the same listener (the other prefix, /api vs /insight-api); every path must answer the same there")
		refLacks    = fs.String("ref-lacks", "", "comma-separated routes the reference does not have (for example utils/estimatefee,currency,messages/verify): a ref non-2xx there is extra instead of changed")
		noOversize  = fs.Bool("no-oversize", false, "drop @oversize POSTs (9 MiB bodies) from the run")
		origin      = fs.String("origin", "https://insightcompare.invalid", "Origin header sent with OPTIONS")
		seedBase    = fs.String("seed", "", "base URL used to discover sample paths (see -seed-kind)")
		seedKind    = fs.String("seed-kind", "insight", "insight: -seed is an Insight base (block-index, block, tx); blockbook: -seed is a Blockbook root (/api/v2)")
		seedBlocks  = fs.Int("blocks", 3, "recent blocks to walk when seeding")
		seedTxs     = fs.Int("seed-txs", 25, "transactions fetched per block when seeding through Insight")
		seedOut     = fs.String("seed-out", "", "write the seeded paths file here")
		emptyAddr   = fs.String("empty", "", "valid address that never received funds")
		pending     = fs.String("pending", "", "comma-separated addresses with a pending (mempool) transaction")
		mempoolBase = fs.String("mempool-url", "", "Blockbook root used to find pending addresses when -seed-kind is insight")
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *mode != "converter" && *mode != "stock" {
		fmt.Fprintln(stderr, "-mode must be converter or stock")
		return 2
	}
	if normBase(*ref) != "" && normBase(*ref) == normBase(*newBase) {
		fmt.Fprintln(stderr, "-ref and -new are the same URL: that compares a host with itself")
		return 2
	}
	if *newAlt != "" && (normBase(*newAlt) == normBase(*newBase) || normBase(*newAlt) == normBase(*ref)) {
		fmt.Fprintln(stderr, "-new-alt must differ from -new and -ref")
		return 2
	}
	if *loadMax < 1 || *loadMax > 30 {
		fmt.Fprintln(stderr, "-load-paths must be between 1 and 30")
		return 2
	}
	if *load < 0 || *load > 64 {
		fmt.Fprintln(stderr, "-load must be between 0 and 64")
		return 2
	}
	cl := newClient(*rate, *timeout)

	var specs []pathSpec
	if *seedBase != "" {
		sc := seedConfig{Base: *seedBase, Kind: *seedKind, Blocks: *seedBlocks, TxsPerBlock: *seedTxs, Empty: *emptyAddr,
			MempoolBase: *mempoolBase, Log: stderr}
		for _, a := range strings.Split(*pending, ",") {
			if a = strings.TrimSpace(a); a != "" {
				sc.Pending = append(sc.Pending, a)
			}
		}
		lines, err := runSeed(cl, sc)
		if err != nil {
			fmt.Fprintln(stderr, "seed:", err)
			return 2
		}
		text := strings.Join(lines, "\n") + "\n"
		if *seedOut != "" {
			if err := os.WriteFile(*seedOut, []byte(text), 0o644); err != nil {
				fmt.Fprintln(stderr, err)
				return 2
			}
			fmt.Fprintf(stderr, "wrote %s\n", *seedOut)
		}
		if *paths == "" && *ref != "" && *newBase != "" {
			var err error
			if specs, err = loadPaths(strings.NewReader(text)); err != nil {
				fmt.Fprintln(stderr, err)
				return 2
			}
		} else if *paths == "" {
			if *seedOut == "" {
				fmt.Fprint(stdout, text)
			}
			return 0
		}
	}
	if *paths != "" {
		f, err := os.Open(*paths)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		specs, err = loadPaths(f)
		f.Close()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
	}
	if len(specs) == 0 || *ref == "" || *newBase == "" {
		fmt.Fprintln(stderr, "need -ref, -new and -paths (or -seed with -ref and -new)")
		fs.Usage()
		return 2
	}
	if err := validateSpecs(specs); err != nil {
		fmt.Fprintln(stderr, "refusing to run:", err)
		return 2
	}
	cfg := &config{Ref: *ref, New: *newBase, Mode: *mode, Concurrency: max(1, *conc), Origin: *origin, ZebraNew: *zebraNew, NewAlt: *newAlt, NoOversize: *noOversize}
	for _, p := range strings.Split(*refLacks, ",") {
		if p = strings.Trim(strings.TrimSpace(p), "/"); p != "" {
			cfg.RefLacks = append(cfg.RefLacks, p)
		}
	}
	if cfg.NoOversize {
		kept := specs[:0]
		for _, sp := range specs {
			if !strings.HasPrefix(sp.Body, "@oversize:") {
				kept = append(kept, sp)
			}
		}
		specs = kept
	}
	for _, p := range strings.Split(*ignore, ",") {
		if p = strings.TrimSpace(p); p != "" {
			cfg.Ignores = append(cfg.Ignores, p)
		}
	}
	started := time.Now()
	fmt.Fprintf(stderr, "comparing %d paths, rate %.1f req/s, concurrency %d\n", len(specs), *rate, cfg.Concurrency)
	results := cfg.runAll(cl, specs)
	var loads []LoadResult
	if *load > 0 {
		lp := pickLoadPaths(results, *loadMax)
		fmt.Fprintf(stderr, "latency phase: %d paths x %d parallel x 2 sides\n", len(lp), *load)
		loads = cfg.runLoad(cl, lp, *load, *loadPause)
	}
	rep := buildReport(cfg, results, loads, started)
	jp := *jsonOut
	if jp == "" {
		jp = strings.TrimSuffix(*out, ".md") + ".json"
	}
	if err := writeReport(rep, *out, jp); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	fmt.Fprint(stdout, rep.Summary())
	fmt.Fprintf(stdout, "\nreport: %s, %s\n", *out, jp)
	if rep.Totals.Failing > 0 {
		return 1
	}
	return 0
}

func mustMarshal(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
