//go:build unittest

package fiat

import (
	"sync/atomic"
	"testing"

	"github.com/trezor/blockbook/common"
)

// TestRunHistoricalCycle_InternalStateTimestampsRace runs a real historical cycle, which
// records the historical fiat timestamp in the internal state, while another goroutine reads
// it the way system info does. Run with -race: the cycle must write through the locked setter.
func TestRunHistoricalCycle_InternalStateTimestampsRace(t *testing.T) {
	d, tmp, fullDays := setupBackfillDB(t)
	defer closeAndDestroyRocksDB(t, d, tmp)
	mock := newBackfillMock(t, fullDays, backfillRate)
	defer mock.server.Close()
	fr := newBackfillFiatRates(d, newBackfillCoingecko(d, mock.server.URL), false)
	is := &common.InternalState{}

	var stop atomic.Bool
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for !stop.Load() {
			is.GetHistoricalFiatRatesTimes()
			if _, err := is.Pack(); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	done, _ := fr.runHistoricalCycle(is)
	stop.Store(true)
	<-readerDone
	if !done {
		t.Fatal("historical cycle not done")
	}
	if got, _ := is.GetHistoricalFiatRatesTimes(); got.IsZero() {
		t.Fatal("historical fiat rates time not recorded")
	}
}
