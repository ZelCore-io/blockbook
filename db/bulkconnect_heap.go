package db

import (
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"time"

	"github.com/golang/glog"
)

// The Bitcoin-type bulk connect caches address balances (each with its whole
// UTXO list) and tx addresses, and stores them only when the maps reach
// maxBulkBalances / maxBulkTxAddresses entries. Those caps count entries, not
// bytes. On chains where few addresses carry many UTXOs (Komodo notaries,
// mining pools), with about 400k cached balances the process reached 7.5 GB and was killed
// before the count cap was ever reached, leaving the DB in inconsistent state.
// The heap guard below stores everything once the live Go heap passes a byte
// limit, so memory stays bounded whatever the chain's address shape.

const (
	// bulkHeapAutoFraction of physical memory is the default live-heap limit.
	// The Go runtime may hold up to about twice the live heap before the next
	// GC (GOGC=100), so a third of RAM keeps the process near two thirds.
	bulkHeapAutoFraction = 3
	// bulkHeapCheckEvery blocks between live-heap reads; the read is cheap
	// (no stop-the-world), this only keeps it off the per-block hot path.
	bulkHeapCheckEvery = 100
	// bulkHeapMinLimit protects tiny hosts from a limit that would flush
	// every check.
	bulkHeapMinLimit = 256 << 20
)

const liveHeapMetric = "/gc/heap/live:bytes"

// bulkHeapGuard decides when the bulk connect must store its caches.
type bulkHeapGuard struct {
	limit   uint64 // configured live-heap limit, 0 = disabled
	trigger uint64 // current trigger, >= limit, raised when a flush cannot get under limit
	blocks  int
	sample  []metrics.Sample
	read    func() uint64 // live heap reader, replaceable in tests
}

func newBulkHeapGuard(limit uint64) *bulkHeapGuard {
	g := &bulkHeapGuard{limit: limit, trigger: limit}
	g.sample = []metrics.Sample{{Name: liveHeapMetric}}
	g.read = g.readLiveHeap
	return g
}

func (g *bulkHeapGuard) readLiveHeap() uint64 {
	metrics.Read(g.sample)
	if g.sample[0].Value.Kind() != metrics.KindUint64 {
		return 0
	}
	return g.sample[0].Value.Uint64()
}

// due reports whether the caches must be stored now; it reads the heap only
// every bulkHeapCheckEvery calls.
func (g *bulkHeapGuard) due() (bool, uint64) {
	if g == nil || g.limit == 0 {
		return false, 0
	}
	g.blocks++
	if g.blocks < bulkHeapCheckEvery {
		return false, 0
	}
	g.blocks = 0
	live := g.read()
	return live >= g.trigger, live
}

// flushed records the live heap after a store. If the working set alone is
// still above the limit, the next trigger is raised by a quarter of the limit
// so the bulk connect keeps making progress instead of flushing every check.
func (g *bulkHeapGuard) flushed(after uint64) {
	if after+g.limit/4 > g.limit {
		g.trigger = after + g.limit/4
	} else {
		g.trigger = g.limit
	}
}

// resolveBulkHeapLimit turns the -bulkmaxheap value (MiB) into bytes:
// negative disables the guard, 0 picks a third of physical memory (disabled
// when the host memory is unknown), positive is taken as given.
func resolveBulkHeapLimit(mib int64, physical uint64) uint64 {
	if mib < 0 {
		return 0
	}
	var limit uint64
	if mib > 0 {
		limit = uint64(mib) << 20
	} else {
		if physical == 0 {
			return 0
		}
		limit = physical / bulkHeapAutoFraction
	}
	if limit < bulkHeapMinLimit {
		limit = bulkHeapMinLimit
	}
	return limit
}

// SetBulkMaxHeap sets the live-heap limit of the bulk connect in MiB
// (-bulkmaxheap): negative disables it, 0 uses a third of physical memory.
func (d *RocksDB) SetBulkMaxHeap(mib int64) {
	d.bulkMaxHeapBytes = resolveBulkHeapLimit(mib, physicalMemory())
	if d.bulkMaxHeapBytes == 0 {
		glog.Info("rocksdb: bulk connect heap limit disabled")
	} else {
		glog.Info("rocksdb: bulk connect heap limit ", d.bulkMaxHeapBytes>>20, " MiB")
	}
}

// releaseHeap returns freed memory to the OS after a store and reports the
// live heap left.
func releaseHeap(g *bulkHeapGuard) uint64 {
	runtime.GC()
	debug.FreeOSMemory()
	return g.read()
}

func logHeapFlush(height uint32, before, after uint64, g *bulkHeapGuard, d time.Duration) {
	glog.Info("rocksdb: height ", height, ", heap limit flush: live heap ", before>>20, " MiB -> ", after>>20,
		" MiB (limit ", g.limit>>20, " MiB, next trigger ", g.trigger>>20, " MiB), done in ", d)
	if g.trigger > g.limit {
		glog.Warning("rocksdb: bulk connect working set ", after>>20, " MiB is above -bulkmaxheap ", g.limit>>20,
			" MiB; raise -bulkmaxheap or add memory")
	}
}
