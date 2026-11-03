package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang/glog"
	"github.com/trezor/blockbook/api"
	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/db"
)

// The electrum-http listener answers the query-string API of ZelCore's
// electrum-http proxy (ZelCore-io/electrum-http, index.js), which wallets of
// "electrum" coins call instead of the Electrum protocol. It serves the same
// calls with the same response shapes from Blockbook's index, mempool and
// backend RPC, so the proxy and its ElectrumX can be retired without a wallet
// change. See docs/electrum-http-compatibility.md.

type electrumHTTPWorker interface {
	GetAddress(string, int, int, api.AccountDetails, *api.AddressFilter, string) (*api.Address, error)
	GetAddressUtxo(string, bool) (api.Utxos, error)
	GetTransaction(string, bool, bool) (*api.Tx, error)
}

// electrumHTTPIndex is the part of the RocksDB index the listener reads.
type electrumHTTPIndex interface {
	GetAddrDescTransactions(addrDesc bchain.AddressDescriptor, lower uint32, higher uint32, fn db.GetTransactionsCallback) error
	GetBestBlock() (uint32, string, error)
	GetBlockHash(height uint32) (string, error)
}

// electrumHTTPMempool is the part of the mempool the listener reads.
type electrumHTTPMempool interface {
	GetAddrDescTransactions(addrDesc bchain.AddressDescriptor) ([]bchain.Outpoint, error)
	GetTransactionTime(txid string) uint32
}

// ElectrumHTTPServer is the optional electrum-http compatible listener. It
// shares the caller's worker, index, mempool and backend and owns none of them.
type ElectrumHTTPServer struct {
	http      *http.Server
	handler   http.Handler
	worker    electrumHTTPWorker
	index     electrumHTTPIndex
	mempool   electrumHTTPMempool
	parser    bchain.BlockChainParser
	rpc       InsightRPC
	broadcast func(string) (string, error)
	ready     atomic.Bool
	slots     chan struct{}
	mu        sync.Mutex
	closing   bool
	started   bool
	listener  net.Listener
	active    sync.WaitGroup
	// spendRule is the chain's optional spendability rule for utxo and
	// niceutxo (EnableChainRules); nil keeps ElectrumX's listunspent.
	spendRule ehSpendRule
	// now returns the current time (tests).
	now func() time.Time
	// Per-request budget (ehRequestTimeout, ehMaxBackendCalls, ehNiceMaxTxs
	// by default; tests lower them).
	requestTimeout  time.Duration
	maxBackendCalls int
	niceMaxTxs      int
}

// NewElectrumHTTPServer creates the listener. rpc may be nil: the calls that
// need raw daemon access then answer with an error.
func NewElectrumHTTPServer(binding string, parser bchain.BlockChainParser, worker electrumHTTPWorker, index electrumHTTPIndex, mempool electrumHTTPMempool, rpc InsightRPC, broadcast func(string) (string, error)) (*ElectrumHTTPServer, error) {
	if parser == nil || parser.GetChainType() != bchain.ChainBitcoinType || parser.AmountDecimals() != 8 {
		return nil, errors.New("electrum-http requires a Bitcoin-type chain with eight decimals")
	}
	host, _, err := net.SplitHostPort(binding)
	if err != nil || host == "" {
		return nil, errors.New("electrum-http requires an explicit bind host and port")
	}
	if worker == nil || index == nil || mempool == nil {
		return nil, errors.New("electrum-http requires the worker, index and mempool")
	}
	s := &ElectrumHTTPServer{
		worker:    worker,
		index:     index,
		mempool:   mempool,
		parser:    parser,
		rpc:       rpc,
		broadcast: broadcast,
		slots:     make(chan struct{}, 32),
		now:       time.Now,

		requestTimeout:  ehRequestTimeout,
		maxBackendCalls: ehMaxBackendCalls,
		niceMaxTxs:      ehNiceMaxTxs,
	}
	s.handler = s
	s.http = &http.Server{Addr: binding, Handler: http.HandlerFunc(s.serve), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 120 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	return s, nil
}

// serve is the listener's entry point. Every proxy request is on the root
// path ("/?..." including the backward-compatible query forms), so any other
// path is refused here, before the rate limiter and its path exemptions:
// ServeHTTP parses the query of the raw request target whatever the path is.
func (s *ElectrumHTTPServer) serve(w http.ResponseWriter, r *http.Request) {
	if !electrumHTTPRootPath(r) {
		electrumHTTPHeaders(w)
		electrumHTTPText(w, http.StatusNotFound, "Not found")
		return
	}
	s.handler.ServeHTTP(w, r)
}

func electrumHTTPRootPath(r *http.Request) bool {
	return r.URL != nil && r.URL.Path == "/" && strings.HasPrefix(r.RequestURI, "/")
}

// EnableRateLimit puts a per-client request-rate limit in front of the
// listener, comparable to the proxy's ddos module (burst 25, limit 100). It
// reuses the public server's REST limiter and its client-IP rules
// (<NETWORK>_TRUSTED_PROXIES, Cloudflare ranges, REST_UI_WHITELIST_IPS).
// <NETWORK>_ELECTRUM_HTTP_RATE_LIMIT (requests per minute, default 100, 0
// disables) and <NETWORK>_ELECTRUM_HTTP_BURST (default 25) tune it. Call it
// before Start.
func (s *ElectrumHTTPServer) EnableRateLimit(network string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return errors.New("electrum-http rate limit must be set before Start")
	}
	l, err := newElectrumHTTPRateLimiter(network)
	if err != nil {
		return err
	}
	if l != nil {
		s.handler = l.wrapPublic(s, "/")
	}
	return nil
}

func newElectrumHTTPRateLimiter(network string) (*restUIRateLimiter, error) {
	prefix := strings.ToUpper(network)
	rate, err := parseNonNegativeIntEnv(prefix+"_ELECTRUM_HTTP_RATE_LIMIT", 100)
	if err != nil {
		return nil, err
	}
	if rate == 0 {
		glog.Info("electrum-http rate limiter disabled")
		return nil, nil
	}
	burst, err := parseNonNegativeIntEnv(prefix+"_ELECTRUM_HTTP_BURST", 25)
	if err != nil {
		return nil, err
	}
	if burst == 0 {
		return nil, errors.New(prefix + "_ELECTRUM_HTTP_BURST: want a positive integer when the rate limit is enabled")
	}
	ipCfg, err := readClientIPConfig(network)
	if err != nil {
		return nil, err
	}
	l := &restUIRateLimiter{
		clients:            make(map[string]*restUIClientLimit),
		rateLimit:          rate,
		rateWindow:         time.Minute,
		burst:              burst,
		stateTTL:           defaultRestUIStateTTL,
		trustedProxies:     ipCfg.trustedProxies,
		cloudflarePrefixes: ipCfg.cloudflarePrefixes,
		trustPseudoIPv6:    ipCfg.trustPseudoIPv6,
	}
	for _, envName := range []string{prefix + "_REST_UI_WHITELIST_IPS", "REST_UI_WHITELIST_IPS"} {
		if v := os.Getenv(envName); v != "" {
			if l.whitelistPrefixes, err = parseCIDRList(envName, splitCIDRList(v)); err != nil {
				return nil, err
			}
			break
		}
	}
	glog.Infof("electrum-http rate limit: %d requests / minute; burst: %d", rate, burst)
	go l.runMaintenance(restUILimiterCleanupInterval)
	return l, nil
}

// Start binds before returning so a port collision is a startup failure.
func (s *ElectrumHTTPServer) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.closing {
		return errors.New("electrum-http listener already started or closed")
	}
	listener, err := net.Listen("tcp", s.http.Addr)
	if err != nil {
		return err
	}
	s.started = true
	s.listener = listener
	go func() {
		if err := s.http.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			glog.Error("electrum-http listener stopped unexpectedly")
		}
	}()
	return nil
}

// SetReady admits requests; until then every call answers 503.
func (s *ElectrumHTTPServer) SetReady() { s.ready.Store(true) }

// Shutdown closes admission before waiting. Worker calls cannot be cancelled,
// so the owner must not close the DB or chain before this returns.
func (s *ElectrumHTTPServer) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closing = true
	s.ready.Store(false)
	s.mu.Unlock()
	err := s.http.Shutdown(ctx)
	if err != nil {
		_ = s.http.Close()
	}
	s.active.Wait()
	return err
}

// electrumHTTPError is a failure answered as the proxy does: HTTP 200 and the
// plain-text body "Error: <message>".
type electrumHTTPError struct{ message string }

func (e *electrumHTTPError) Error() string { return e.message }

func ehError(message string) error { return &electrumHTTPError{message} }

// ehInternal is the message for failures whose text must not reach clients
// (transport errors can carry backend URLs).
const ehInternal = "internal error"

func electrumHTTPHeaders(w http.ResponseWriter) {
	// The proxy's CORS headers (index.js), verbatim.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Request-Method", "*")
	w.Header().Set("Access-Control-Allow-Methods", "OPTIONS, GET")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func electrumHTTPText(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte("Error: " + message))
}

func (s *ElectrumHTTPServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	electrumHTTPHeaders(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, OPTIONS")
		electrumHTTPText(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	if !electrumHTTPRootPath(r) {
		electrumHTTPText(w, http.StatusNotFound, "Not found")
		return
	}
	q, err := parseElectrumHTTPQuery(r.RequestURI)
	if err != nil {
		electrumHTTPText(w, http.StatusBadRequest, "URI malformed")
		return
	}
	if !q.call.truthy() {
		electrumHTTPText(w, http.StatusOK, "Call is undefined")
		return
	}
	if !q.server.truthy() {
		electrumHTTPText(w, http.StatusOK, "Server is undefined")
		return
	}
	s.mu.Lock()
	if s.closing || !s.ready.Load() {
		s.mu.Unlock()
		w.Header().Set("Retry-After", "1")
		electrumHTTPText(w, http.StatusServiceUnavailable, "Service unavailable")
		return
	}
	select {
	case s.slots <- struct{}{}:
		s.active.Add(1)
	default:
		s.mu.Unlock()
		w.Header().Set("Retry-After", "1")
		electrumHTTPText(w, http.StatusServiceUnavailable, "Service busy")
		return
	}
	s.mu.Unlock()
	defer func() { <-s.slots; s.active.Done() }()

	ctx, cancel := context.WithTimeout(r.Context(), s.requestTimeout)
	defer cancel()
	body, err := s.handle(ctx, q)
	if err != nil {
		var ee *electrumHTTPError
		if !errors.As(err, &ee) {
			glog.Warning("electrum-http ", q.call.s, ": ", err)
			ee = &electrumHTTPError{ehInternal}
		}
		electrumHTTPText(w, http.StatusOK, ee.message)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// handle dispatches a call. The body is the JSON answer.
func (s *ElectrumHTTPServer) handle(ctx context.Context, q electrumHTTPQuery) ([]byte, error) {
	if q.call.multi {
		return nil, ehError("Unknown call")
	}
	b := &ehBudget{ctx: ctx, calls: s.maxBackendCalls}
	var v interface{}
	var err error
	switch q.call.s {
	case "balance":
		v, err = s.callBalance(q.param)
	case "history":
		v, err = s.callHistory(b, q.param, true)
	case "entirehistory":
		v, err = s.callHistory(b, q.param, false)
	case "transaction", "transactionnonverbose":
		v, err = s.callRawTx(q.param)
	case "transactionverbose":
		v, err = s.callVerboseTx(q.param)
	case "utxo":
		v, err = s.callUtxo(b, q.param)
	case "broadcast":
		v, err = s.callBroadcast(q.param)
	case "height":
		v, err = s.callHeight()
	case "header":
		v, err = s.callHeader(q.param)
	case "nicehistory":
		v, err = s.callNiceHistory(b, q.param, 31)
	case "niceentirehistory":
		v, err = s.callNiceHistory(b, q.param, 30001)
	case "niceutxo":
		v, err = s.callNiceUtxo(b, q.param)
	default:
		// The proxy has no default case and never answers; answer instead.
		return nil, ehError("Unknown call")
	}
	if err != nil {
		return nil, err
	}
	return ehMarshal(v)
}
