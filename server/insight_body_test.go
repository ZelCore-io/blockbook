//go:build unittest

package server

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// blockingBody is a request body that blocks in Read until released, as a
// client that sends headers and then trickles.
type blockingBody struct {
	reading chan struct{}
	release chan struct{}
}

func (b *blockingBody) Read([]byte) (int, error) {
	select {
	case b.reading <- struct{}{}:
	default:
	}
	<-b.release
	return 0, io.EOF
}

// A POST body that is still being read holds no concurrency slot.
func TestInsightSlowBodyHoldsNoSlot(t *testing.T) {
	s := insightTestServer(t, insightFixture())
	body := &blockingBody{reading: make(chan struct{}, 1), release: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		r := httptest.NewRequest("POST", "/api/tx/send", body)
		s.ServeHTTP(httptest.NewRecorder(), r)
	}()
	select {
	case <-body.reading:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never read the body")
	}
	if n := len(s.slots); n != 0 {
		t.Fatalf("a slow body holds %d slot(s)", n)
	}
	close(body.release)
	<-done
	if n := len(s.slots); n != 0 {
		t.Fatalf("slot leaked: %d", n)
	}
}

// 40 connections that send POST headers and never the body cannot starve
// other requests, and the body read deadline ends them.
func TestInsightTrickledBodiesDoNotStarveSlots(t *testing.T) {
	f := insightFixture()
	s := insightTestServer(t, f)
	s.bodyTimeout = 300 * time.Millisecond
	srv := httptest.NewServer(s)
	defer srv.Close()
	conns := make([]net.Conn, 0, 40)
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()
	for i := 0; i < 40; i++ {
		c, err := net.Dial("tcp", srv.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
		fmt.Fprintf(c, "POST /api/tx/send HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 1000\r\n\r\n{")
	}
	// All of them are parked in the body read; a normal request still gets a slot.
	time.Sleep(100 * time.Millisecond)
	resp, err := http.Get(srv.URL + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status while bodies trickle: %d", resp.StatusCode)
	}
	// The deadline ends the stalled bodies with a 400 and frees them.
	_ = conns[0].SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(conns[0]).ReadString('\n')
	if err != nil || !strings.Contains(line, "400") {
		t.Fatalf("stalled body not ended by the read deadline: %q %v", line, err)
	}
	if n := len(s.slots); n != 0 {
		t.Fatalf("slots held after the deadline: %d", n)
	}
}
