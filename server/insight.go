package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang/glog"
	"github.com/trezor/blockbook/api"
	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/common"
)

const insightBodyLimit = 8 << 20

// insightBodyTimeout bounds reading a POST body, which happens before a
// concurrency slot is taken.
const insightBodyTimeout = 10 * time.Second

// insightBodyReaders caps POST bodies read at once (with insightBodyLimit,
// at most 128 MiB buffered), since body reads hold no concurrency slot.
const insightBodyReaders = 16

type insightWorker interface {
	GetSystemInfo(bool) (*api.SystemInfo, error)
	GetAddress(string, int, int, api.AccountDetails, *api.AddressFilter, string) (*api.Address, error)
	GetAddressUtxo(string, bool) (api.Utxos, error)
	GetTransaction(string, bool, bool) (*api.Tx, error)
	GetBlocks(page int, blocksOnPage int) (*api.Blocks, error)
	GetBlock(bid string, page int, txsOnPage int) (*api.Block, error)
	GetBlockRaw(bid string) (*api.BlockRaw, error)
	EstimateFee(blocks int, conservative bool) (big.Int, error)
	GetCurrentFiatRates(currencies []string, token string) (*api.FiatTicker, error)
}

// InsightServer is the optional eight-decimal Bitcoin compatibility listener.
// It shares the caller's worker and owns no database or chain resources.
type InsightServer struct {
	http      *http.Server
	worker    insightWorker
	broadcast func(string) (string, error)
	ready     atomic.Bool
	slots     chan struct{}
	readers   chan struct{}
	mu        sync.Mutex
	closing   bool
	started   bool
	listener  net.Listener
	active    sync.WaitGroup
	// rpc and coinShortcut are set by SetBackend before Start; rpc may be nil.
	rpc          InsightRPC
	coinShortcut string
	blockCache   *insightBlockCache
	// bodyTimeout overrides insightBodyTimeout (tests).
	bodyTimeout time.Duration
	feeCache    insightFeeCache
	// ext holds the coin's own stock routes and tx labels (EnableExtension).
	ext insightExtension
}

func NewInsightServer(binding string, chainType bchain.ChainType, decimals int, worker insightWorker, broadcast func(string) (string, error)) (*InsightServer, error) {
	if chainType != bchain.ChainBitcoinType || decimals != 8 {
		return nil, errors.New("Insight requires a Bitcoin-type chain with eight decimals")
	}
	host, _, err := net.SplitHostPort(binding)
	if err != nil || host == "" {
		return nil, errors.New("Insight requires an explicit bind host and port")
	}
	if worker == nil {
		return nil, errors.New("Insight requires a query worker")
	}
	s := &InsightServer{worker: worker, broadcast: broadcast, slots: make(chan struct{}, 32), readers: make(chan struct{}, insightBodyReaders), blockCache: newInsightBlockCache()}
	s.http = &http.Server{Addr: binding, Handler: s, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	return s, nil
}

// Start binds before returning so a port collision is a startup failure.
func (s *InsightServer) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.closing {
		return errors.New("Insight listener already started or closed")
	}
	listener, err := net.Listen("tcp", s.http.Addr)
	if err != nil {
		return err
	}
	s.started = true
	s.listener = listener
	go func() {
		if err := s.http.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			glog.Error("Insight listener stopped unexpectedly")
		}
	}()
	return nil
}
func (s *InsightServer) SetReady() { s.ready.Store(true) }

// Shutdown closes admission before waiting. Legacy worker calls cannot cancel:
// even when HTTP's deadline expires, the owner must not close their DB or chain.
func (s *InsightServer) Shutdown(ctx context.Context) error {
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

func insightJSON(w http.ResponseWriter, code int, value interface{}) {
	body, err := json.Marshal(value)
	if err != nil {
		code = 500
		body = []byte(`{"status":"error","error":"Backend request failed"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(append(body, '\n'))
}
func insightError(w http.ResponseWriter, code int, message string) {
	insightJSON(w, code, struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}{"error", message})
}

func (s *InsightServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sourceURL, sourceLabel := sourceLink(common.GetVersionInfo().GitCommit)
	w.Header().Set("Link", fmt.Sprintf("<%s>; rel=\"source\"; title=\"%s\"", sourceURL, sourceLabel))
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Expose-Headers", "Link")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	route, methods, ok := s.match(r)
	if !ok {
		insightError(w, 404, "Route not found")
		return
	}
	if r.Method == http.MethodOptions {
		w.WriteHeader(204)
		return
	}
	if !insightAllowed(methods, r.Method) {
		w.Header().Set("Allow", strings.Join(methods, ", ")+", OPTIONS")
		insightError(w, 405, "Method not allowed")
		return
	}
	if !s.admissible() {
		w.Header().Set("Retry-After", "1")
		insightError(w, 503, "Service unavailable")
		return
	}
	// The body is read, size-limited and parsed before a concurrency slot is
	// taken, so clients that trickle a body cannot hold the 32 slots.
	if r.Method == http.MethodPost {
		// Refusals close the connection: net/http would otherwise drain the
		// unread body before answering, which a trickling client can stall.
		if r.ContentLength > insightBodyLimit {
			w.Header().Set("Connection", "close")
			insightError(w, 413, "Request body too large")
			return
		}
		select {
		case s.readers <- struct{}{}:
		default:
			w.Header().Set("Connection", "close")
			w.Header().Set("Retry-After", "1")
			insightError(w, 503, "Service busy")
			return
		}
		timeout := s.bodyTimeout
		if timeout <= 0 {
			timeout = insightBodyTimeout
		}
		// Not every ResponseWriter supports deadlines (test recorders); the
		// server-wide ReadTimeout still applies then.
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(timeout))
		body, code, err := insightReadBody(w, r)
		<-s.readers
		if err != nil {
			code, message := insightErrorStatus(code)
			insightError(w, code, message)
			return
		}
		route.body = body
	}
	s.mu.Lock()
	if s.closing || !s.ready.Load() {
		s.mu.Unlock()
		w.Header().Set("Retry-After", "1")
		insightError(w, 503, "Service unavailable")
		return
	}
	select {
	case s.slots <- struct{}{}:
		s.active.Add(1)
	default:
		s.mu.Unlock()
		w.Header().Set("Retry-After", "1")
		insightError(w, 503, "Service busy")
		return
	}
	s.mu.Unlock()
	defer func() { <-s.slots; s.active.Done() }()
	if insightSingleAddressKinds[route.kind] {
		if _, ok := insightSingleAddress(route); !ok {
			insightError(w, 400, "A single address is required")
			return
		}
	}
	route.ctx = r.Context()
	value, code, err := s.handle(route)
	if err != nil {
		code, message := insightErrorStatus(code)
		var clientErr *insightClientError
		if errors.As(err, &clientErr) && clientErr.code == code {
			message = clientErr.message
		}
		insightError(w, code, message)
		return
	}
	insightJSON(w, 200, value)
}

// insightClientError is a handler error whose fixed, client-safe message
// replaces the generic one for its status code.
type insightClientError struct {
	code    int
	message string
}

func (e *insightClientError) Error() string { return e.message }

// insightErrorStatus maps a handler status to the generic public message.
// Unknown statuses, including 0, become 500.
func insightErrorStatus(code int) (int, string) {
	switch code {
	case http.StatusBadRequest:
		return code, "Invalid request"
	case http.StatusNotFound:
		return code, "Not found"
	case http.StatusRequestEntityTooLarge:
		return code, "Request body too large"
	case http.StatusNotImplemented:
		return code, "Not supported"
	case http.StatusServiceUnavailable:
		return code, "Service unavailable"
	}
	return http.StatusInternalServerError, "Backend request failed"
}

func insightStatusNumber(value string) json.Number {
	value = strings.TrimSpace(value)
	if value != "" && (value[0] == '-' || value[0] >= '0' && value[0] <= '9') && json.Valid([]byte(value)) {
		return json.Number(value)
	}
	return "0"
}

// handle dispatches an admitted route. Kinds without a handler yet are 501.
func (s *InsightServer) handle(route insightRoute) (interface{}, int, error) {
	if s.ext != nil && s.ext.handles(route.kind) {
		return s.ext.handle(route)
	}
	address, _ := insightSingleAddress(route)
	switch route.kind {
	case "status":
		return s.status(route)
	case "sync":
		return s.sync()
	case "blocks":
		return s.blocks(route)
	case "block":
		return s.block(route)
	case "rawblock":
		return s.rawBlock(route)
	case "blockindex":
		return s.blockIndex(route)
	case "peer":
		return insightPeer(), 0, nil
	case "version":
		return insightVersion(), 0, nil
	case "summary":
		return s.summary(route, address)
	case "addrvalue":
		return s.addrValue(route, address)
	case "utxo":
		return s.utxo(route, address)
	case "history":
		return s.history(route, address)
	case "multiutxo":
		return s.multiUtxo(route, false)
	case "multiunspent":
		return s.multiUtxo(route, true)
	case "multibalance":
		return s.multiBalance(route)
	case "multitxs":
		return s.multiTxs(route)
	case "send":
		return s.send(route)
	case "tx":
		return s.tx(route)
	case "rawtx":
		return s.rawTx(route)
	case "txs":
		return s.txs(route)
	case "estimatefee":
		return s.estimateFee(route)
	case "currency":
		return s.currency()
	case "verify":
		return s.verifyMessage(route)
	}
	return nil, http.StatusNotImplemented, errors.New("route not implemented")
}

func insightAddressErrorCode(err error) int {
	var clientError *api.APIError
	if errors.As(err, &clientError) && clientError.Public {
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

// admissible reports whether the listener is ready and not shutting down.
func (s *InsightServer) admissible() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closing && s.ready.Load()
}
