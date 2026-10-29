//go:build unittest

package api

import (
	"sync"
	"testing"
	"time"

	"github.com/trezor/blockbook/fiat"
)

// TestGetSystemInfo_FiatTimestampsRace runs system info (served by /api, status
// and sync) in a loop while the historical fiat timestamps are updated the way
// the fiat downloader updates them. Run with -race: the reads in GetSystemInfo
// and the downloader's writes must both go through InternalState's mutex.
func TestGetSystemInfo_FiatTimestampsRace(t *testing.T) {
	w := setupXpubRaceWorker(t)
	w.fiatRates = &fiat.FiatRates{}

	const rounds = 200
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		base := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
		for i := 0; i < rounds; i++ {
			// the same updates fiat.runHistoricalCycle makes after a successful cycle
			w.is.SetHistoricalFiatRatesTime(base.Add(time.Duration(i) * time.Hour))
			w.is.SetHistoricalTokenFiatRatesTime(base.Add(time.Duration(i) * time.Minute))
		}
	}()
	errs := make(chan error, 1)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			if _, err := w.GetSystemInfo(false); err != nil {
				select {
				case errs <- err:
				default:
				}
				return
			}
		}
	}()
	wg.Wait()
	close(errs)
	if err := <-errs; err != nil {
		t.Fatal(err)
	}

	si, err := w.GetSystemInfo(false)
	if err != nil {
		t.Fatal(err)
	}
	last := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	if si.Blockbook.HistoricalFiatRatesTime == nil || !si.Blockbook.HistoricalFiatRatesTime.Equal(last.Add((rounds-1)*time.Hour)) {
		t.Fatalf("historicalFiatRatesTime = %v", si.Blockbook.HistoricalFiatRatesTime)
	}
	if si.Blockbook.HistoricalTokenFiatRatesTime == nil || !si.Blockbook.HistoricalTokenFiatRatesTime.Equal(last.Add((rounds-1)*time.Minute)) {
		t.Fatalf("historicalTokenFiatRatesTime = %v", si.Blockbook.HistoricalTokenFiatRatesTime)
	}
}
