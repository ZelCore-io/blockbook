//go:build unittest

package db

import (
	"testing"

	"github.com/trezor/blockbook/common"
	"github.com/trezor/blockbook/tests/dbtestdata"
)

func Test_resolveBulkHeapLimit(t *testing.T) {
	const gib = uint64(1) << 30
	tests := []struct {
		name     string
		mib      int64
		physical uint64
		want     uint64
	}{
		{"disabled", -1, 8 * gib, 0},
		{"auto third of RAM", 0, 9 * gib, 3 * gib},
		{"auto unknown RAM", 0, 0, 0},
		{"explicit", 3072, 8 * gib, 3 * gib},
		{"explicit below floor", 10, 8 * gib, bulkHeapMinLimit},
		{"auto below floor", 0, 300 << 20, bulkHeapMinLimit},
	}
	for _, tt := range tests {
		if got := resolveBulkHeapLimit(tt.mib, tt.physical); got != tt.want {
			t.Errorf("%s: resolveBulkHeapLimit(%d, %d) = %d, want %d", tt.name, tt.mib, tt.physical, got, tt.want)
		}
	}
}

func Test_bulkHeapGuard(t *testing.T) {
	var nilGuard *bulkHeapGuard
	if due, _ := nilGuard.due(); due {
		t.Fatal("nil guard must never be due")
	}
	live := uint64(0)
	g := newBulkHeapGuard(1000)
	g.read = func() uint64 { return live }

	live = 5000
	for i := 1; i < bulkHeapCheckEvery; i++ {
		if due, _ := g.due(); due {
			t.Fatalf("due after %d blocks, want only every %d", i, bulkHeapCheckEvery)
		}
	}
	if due, got := g.due(); !due || got != 5000 {
		t.Fatalf("due() = %v, %d at check block over the limit, want true, 5000", due, got)
	}

	// A flush that gets well under the limit keeps the trigger at the limit.
	g.flushed(100)
	if g.trigger != 1000 {
		t.Fatalf("trigger after a good flush = %d, want 1000", g.trigger)
	}
	// A working set still near the limit raises the trigger by a quarter of
	// the limit, so the next flush waits for real growth.
	g.flushed(900)
	if g.trigger != 1150 {
		t.Fatalf("trigger after a flush ending at 900 = %d, want 1150", g.trigger)
	}
	live = 1100
	g.blocks = bulkHeapCheckEvery - 1
	if due, _ := g.due(); due {
		t.Fatal("due below the raised trigger")
	}
	live = 1150
	g.blocks = bulkHeapCheckEvery - 1
	if due, _ := g.due(); !due {
		t.Fatal("not due at the raised trigger")
	}
	// The trigger drops back once a flush gets far enough under the limit.
	g.flushed(200)
	if g.trigger != 1000 {
		t.Fatalf("trigger after recovery = %d, want 1000", g.trigger)
	}
}

// A heap-limit flush in the middle of a bulk sync must leave the same index
// as a sync that only stores at Close.
func Test_BulkConnect_BitcoinType_HeapFlush(t *testing.T) {
	d := setupRocksDB(t, &testBitcoinParser{
		BitcoinParser: bitcoinTestnetParser(),
	})
	defer closeAndDestroyRocksDB(t, d)
	d.bulkMaxHeapBytes = bulkHeapMinLimit

	bc, err := d.InitBulkConnect()
	if err != nil {
		t.Fatal(err)
	}
	if bc.heapGuard == nil {
		t.Fatal("InitBulkConnect() did not set the heap guard")
	}
	// Odd reads are the due() checks (over the limit), even reads come after
	// the store (heap released), as a real flush would see it.
	flushes := 0
	bc.heapGuard.read = func() uint64 {
		flushes++
		if flushes%2 == 1 {
			return 1 << 40
		}
		return 0
	}

	bc.heapGuard.blocks = bulkHeapCheckEvery - 1
	if err := bc.ConnectBlock(dbtestdata.GetTestBitcoinTypeBlock1(d.chainParser), false); err != nil {
		t.Fatal(err)
	}
	if len(bc.balances) != 0 || len(bc.txAddressesMap) != 0 || bc.bulkAddressesCount != 0 {
		t.Fatalf("after heap flush: %d balances, %d tx addresses, %d bulk addresses cached, want all 0",
			len(bc.balances), len(bc.txAddressesMap), bc.bulkAddressesCount)
	}
	addrDesc, err := d.chainParser.GetAddrDescFromAddress(dbtestdata.Addr1)
	if err != nil {
		t.Fatal(err)
	}
	if bal, err := d.GetAddrDescBalance(addrDesc, AddressBalanceDetailUTXO); err != nil || bal == nil || len(bal.Utxos) == 0 {
		t.Fatalf("balance of block 1 address not stored by the heap flush: %+v, %v", bal, err)
	}
	if d.is.DbState != common.DbStateInconsistent {
		t.Fatal("heap flush must keep the DB in DbStateInconsistent until Close")
	}

	bc.heapGuard.blocks = bulkHeapCheckEvery - 1
	if err := bc.ConnectBlock(dbtestdata.GetTestBitcoinTypeBlock2(d.chainParser), true); err != nil {
		t.Fatal(err)
	}
	if err := bc.Close(); err != nil {
		t.Fatal(err)
	}
	// 2 due() reads + 2 releaseHeap() reads
	if flushes != 4 {
		t.Fatalf("heap reads = %d, want 4", flushes)
	}
	if d.is.DbState != common.DbStateOpen {
		t.Fatal("DB not in DbStateOpen")
	}
	verifyAfterBitcoinTypeBlock2(t, d)
}
