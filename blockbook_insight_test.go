package main

import (
	"context"
	"net/http/httptest"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/trezor/blockbook/api"
	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/common"
	"github.com/trezor/blockbook/server"
)

type shutdownInsightWorker struct {
	*api.Worker
	entered chan struct{}
	release chan struct{}
}

func (w *shutdownInsightWorker) GetSystemInfo(bool) (*api.SystemInfo, error) {
	close(w.entered)
	<-w.release
	return &api.SystemInfo{Backend: &common.BackendInfo{}}, nil
}

type shutdownInsightChain struct {
	bchain.BlockChain
	closed     chan struct{}
	contextErr error
}

func (c *shutdownInsightChain) Shutdown(ctx context.Context) error {
	c.contextErr = ctx.Err()
	close(c.closed)
	return nil
}

func TestSignalShutdownDrainsInsightBeforeDaemon(t *testing.T) {
	w := &shutdownInsightWorker{entered: make(chan struct{}), release: make(chan struct{})}
	s, err := server.NewInsightServer("127.0.0.1:0", bchain.ChainBitcoinType, 8, w, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.SetReady()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(w.release) }) }
	defer release()
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		s.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/status", nil))
	}()
	select {
	case <-w.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	shutdownSig := make(chan os.Signal, 1)
	chain := &shutdownInsightChain{closed: make(chan struct{})}
	done := make(chan struct{})
	go func() { waitForSignalAndShutdown(nil, nil, s, chain, shutdownSig, 10*time.Millisecond); close(done) }()
	select {
	case <-done:
		t.Fatal("shutdown did not wait for its dedicated signal")
	case <-time.After(20 * time.Millisecond):
	}
	shutdownSig <- syscall.SIGTERM
	select {
	case <-chain.closed:
		t.Fatal("daemon closed while query remained active")
	case <-time.After(30 * time.Millisecond):
	}
	release()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not finish")
	}
	select {
	case <-chain.closed:
	default:
		t.Fatal("daemon did not close after drain")
	}
	<-requestDone
	if chain.contextErr != nil {
		t.Fatalf("Insight drain consumed the existing daemon shutdown deadline: %v", chain.contextErr)
	}
}
