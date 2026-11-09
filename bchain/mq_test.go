//go:build unittest

package bchain

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	zmq "github.com/pebbe/zmq4"
)

var testMQTopics = SubscriptionTopics{
	BlockSubscribe: "hashblock",
	BlockReceive:   "hashblock",
	TxSubscribe:    "hashtx",
	TxReceive:      "hashtx",
}

// newTestPublisher binds a PUB socket on a free loopback port and returns it with its endpoint.
func newTestPublisher(t *testing.T) (*zmq.Context, *zmq.Socket, string) {
	t.Helper()
	pubCtx, err := zmq.NewContext()
	if err != nil {
		t.Fatal(err)
	}
	pub, err := pubCtx.NewSocket(zmq.PUB)
	if err != nil {
		t.Fatal(err)
	}
	if err := pub.SetLinger(0); err != nil {
		t.Fatal(err)
	}
	if err := pub.Bind("tcp://127.0.0.1:*"); err != nil {
		t.Fatal(err)
	}
	endpoint, err := pub.GetLastEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	return pubCtx, pub, endpoint
}

// TestMQShutdownLoop starts and stops the MQ listener many times. Shutdown must not touch the
// socket from another goroutine: before the fix this produced "MQ loop recovered from nil
// pointer", "context deadline exceeded" hangs and, rarely, a libzmq assertion abort.
func TestMQShutdownLoop(t *testing.T) {
	pubCtx, pub, endpoint := newTestPublisher(t)
	defer func() {
		pub.Close()
		pubCtx.Term()
	}()

	const iterations = 200
	var totalBlocks, totalTxs int64
	for i := 0; i < iterations; i++ {
		var blocks, txs int64
		mq, err := NewMQ(endpoint, nil, func(nt NotificationType) {
			switch nt {
			case NotificationNewBlock:
				atomic.AddInt64(&blocks, 1)
			case NotificationNewTx:
				atomic.AddInt64(&txs, 1)
			}
		}, testMQTopics)
		if err != nil {
			t.Fatalf("iteration %d: NewMQ: %v", i, err)
		}

		// Every other iteration publishes until both notification types arrive (PUB/SUB
		// subscriptions propagate asynchronously). The other iterations shut down while the
		// listener is blocked in recv with nothing received, the usual production stop.
		if i%2 == 0 {
			deadline := time.Now().Add(5 * time.Second)
			for atomic.LoadInt64(&blocks) == 0 || atomic.LoadInt64(&txs) == 0 {
				if time.Now().After(deadline) {
					t.Fatalf("iteration %d: no notifications received (blocks=%d txs=%d)", i, atomic.LoadInt64(&blocks), atomic.LoadInt64(&txs))
				}
				if _, err := pub.SendMessage("hashblock", "00", []byte{1, 0, 0, 0}); err != nil {
					t.Fatal(err)
				}
				if _, err := pub.SendMessage("hashtx", "00", []byte{1, 0, 0, 0}); err != nil {
					t.Fatal(err)
				}
				time.Sleep(2 * time.Millisecond)
			}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		start := time.Now()
		err = mq.Shutdown(ctx)
		cancel()
		if err != nil {
			t.Fatalf("iteration %d: Shutdown: %v (after %v)", i, err, time.Since(start))
		}
		if mq.isRunning.Load() {
			t.Fatalf("iteration %d: MQ still running after Shutdown", i)
		}
		// A second Shutdown is a no-op.
		if err := mq.Shutdown(context.Background()); err != nil {
			t.Fatalf("iteration %d: second Shutdown: %v", i, err)
		}
		totalBlocks += atomic.LoadInt64(&blocks)
		totalTxs += atomic.LoadInt64(&txs)
	}
	if totalBlocks < iterations/2 || totalTxs < iterations/2 {
		t.Fatalf("too few notifications: blocks=%d txs=%d", totalBlocks, totalTxs)
	}
	t.Logf("%d start/stop cycles, %d block and %d tx notifications", iterations, totalBlocks, totalTxs)
}

// TestMQShutdownWithoutPublisher stops a listener whose endpoint has nobody bound to it. The
// socket still holds queued subscription messages; Shutdown must not hang on them.
func TestMQShutdownWithoutPublisher(t *testing.T) {
	pubCtx, pub, endpoint := newTestPublisher(t)
	pub.Close()
	pubCtx.Term()

	for i := 0; i < 20; i++ {
		mq, err := NewMQ(endpoint, nil, func(NotificationType) {}, testMQTopics)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = mq.Shutdown(ctx)
		cancel()
		if err != nil {
			t.Fatalf("iteration %d: Shutdown: %v", i, err)
		}
	}
}
