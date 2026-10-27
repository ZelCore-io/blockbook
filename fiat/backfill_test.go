//go:build unittest

package fiat

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trezor/blockbook/common"
	"github.com/trezor/blockbook/db"
)

func TestNewFiatRates_ParsesBackfillHistory(t *testing.T) {
	tests := []struct {
		name   string
		params string
		want   bool
	}{
		{name: "backfillHistory true", params: `{"coin":"bitcoin","periodSeconds":60,"backfillHistory":true}`, want: true},
		{name: "backfillHistory false", params: `{"coin":"bitcoin","periodSeconds":60,"backfillHistory":false}`, want: false},
		{name: "backfillHistory absent", params: `{"coin":"bitcoin","periodSeconds":60}`, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := common.Config{
				CoinName:        "fakecoin",
				FiatRates:       "coingecko",
				FiatRatesParams: tt.params,
			}
			d, is, tmp := setupRocksDB(t, &testBitcoinParser{
				BitcoinParser: bitcoinTestnetParser(),
			}, &config)
			defer closeAndDestroyRocksDB(t, d, tmp)

			envNames := append([]string{coingeckoAPIKeyEnv}, coinGeckoScopedAPIKeyEnvNames(is.GetNetwork(), is.CoinShortcut)...)
			for _, envName := range envNames {
				if v, ok := os.LookupEnv(envName); ok {
					value := v
					defer os.Setenv(envName, value)
				}
				_ = os.Unsetenv(envName)
			}

			fiatRates, err := NewFiatRates(d, &config, nil, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if fiatRates.backfillHistory != tt.want {
				t.Fatalf("unexpected backfillHistory: got %v, want %v", fiatRates.backfillHistory, tt.want)
			}
		})
	}
}

// backfillMock serves supported_vs_currencies and market_chart. days=max returns the full daily
// series (one point per day in fullDays, plus a non-whole-day "current" point that must be
// ignored); any other range returns no points. failMax makes days=max fail for that currency.
type backfillMock struct {
	server      *httptest.Server
	maxRequests atomic.Int32
	failMax     atomic.Value // string
}

func newBackfillMock(t *testing.T, fullDays []time.Time, rate func(currency string, day time.Time) float64) *backfillMock {
	m := &backfillMock{}
	m.failMax.Store("")
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/simple/supported_vs_currencies":
			_, _ = w.Write([]byte(`["usd","eur"]`))
		case "/coins/ethereum/market_chart":
			currency := r.URL.Query().Get("vs_currency")
			if r.URL.Query().Get("days") != "max" {
				_, _ = w.Write([]byte(`{"prices":[]}`))
				return
			}
			if r.URL.Query().Get("interval") != "daily" {
				http.Error(w, "expected interval=daily", http.StatusBadRequest)
				return
			}
			m.maxRequests.Add(1)
			if currency == m.failMax.Load().(string) {
				http.Error(w, "upstream error", http.StatusInternalServerError)
				return
			}
			points := make([]string, 0, len(fullDays)+1)
			for _, day := range fullDays {
				points = append(points, fmt.Sprintf("[%d,%v]", day.Unix()*1000, rate(currency, day)))
			}
			points = append(points, fmt.Sprintf("[%d,1]", time.Now().Unix()*1000+1))
			_, _ = w.Write([]byte(`{"prices":[` + strings.Join(points, ",") + `]}`))
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	return m
}

func newBackfillCoingecko(d *db.RocksDB, url string) *Coingecko {
	return &Coingecko{
		coin:         "ethereum",
		bootstrapURL: url,
		tipURL:       url,
		httpClient:   &http.Client{Timeout: 5 * time.Second},
		db:           d,
		plan:         coingeckoPlanFree,
	}
}

func backfillRate(currency string, day time.Time) float64 {
	base := 1.0
	if currency == "eur" {
		base = 2.0
	}
	return base + float64(day.Unix()/secondsInDay%1000)
}

// setupBackfillDB seeds d3, d2 and d1 (the most recent days) with existing rates.
func setupBackfillDB(t *testing.T) (*db.RocksDB, string, []time.Time) {
	config := common.Config{CoinName: "fakecoin"}
	d, _, tmp := setupRocksDB(t, &testBitcoinParser{BitcoinParser: bitcoinTestnetParser()}, &config)
	if err := d.FiatRatesSetHistoricalBootstrapComplete(true); err != nil {
		t.Fatalf("FiatRatesSetHistoricalBootstrapComplete failed: %v", err)
	}
	for i := 3; i >= 1; i-- {
		seedDailyTicker(t, d, midnightDaysAgo(i), map[string]float32{"usd": 1000, "eur": 2000}, nil)
	}
	fullDays := make([]time.Time, 0, 10)
	for i := 10; i >= 1; i-- {
		fullDays = append(fullDays, midnightDaysAgo(i))
	}
	return d, tmp, fullDays
}

func TestBackfillHistoricalTickers_FillsOnlyMissingDays(t *testing.T) {
	d, tmp, fullDays := setupBackfillDB(t)
	defer closeAndDestroyRocksDB(t, d, tmp)
	mock := newBackfillMock(t, fullDays, backfillRate)
	defer mock.server.Close()

	filled, err := newBackfillCoingecko(d, mock.server.URL).BackfillHistoricalTickers()
	if err != nil {
		t.Fatalf("BackfillHistoricalTickers failed: %v", err)
	}
	if filled["usd"] != 7 || filled["eur"] != 7 {
		t.Fatalf("unexpected filled counts: %v, want 7 per currency", filled)
	}
	if got := mock.maxRequests.Load(); got != 2 {
		t.Fatalf("unexpected days=max request count: got %d, want 2", got)
	}
	for i := 10; i >= 4; i-- {
		day := midnightDaysAgo(i)
		ticker, err := d.FiatRatesGetTicker(&day)
		if err != nil {
			t.Fatalf("FiatRatesGetTicker failed: %v", err)
		}
		if ticker == nil || ticker.Rates["usd"] != float32(backfillRate("usd", day)) || ticker.Rates["eur"] != float32(backfillRate("eur", day)) {
			t.Fatalf("day %v not backfilled: %+v", day, ticker)
		}
	}
	for i := 3; i >= 1; i-- {
		day := midnightDaysAgo(i)
		ticker, err := d.FiatRatesGetTicker(&day)
		if err != nil {
			t.Fatalf("FiatRatesGetTicker failed: %v", err)
		}
		if ticker == nil || ticker.Rates["usd"] != 1000 || ticker.Rates["eur"] != 2000 {
			t.Fatalf("existing day %v was modified: %+v", day, ticker)
		}
	}
}

func newBackfillFiatRates(d *db.RocksDB, cg *Coingecko, backfill bool) *FiatRates {
	return &FiatRates{db: d, downloader: cg, backfillHistory: backfill}
}

func TestRunHistoricalCycle_BackfillSetsMarkerAndRunsOnce(t *testing.T) {
	d, tmp, fullDays := setupBackfillDB(t)
	defer closeAndDestroyRocksDB(t, d, tmp)
	mock := newBackfillMock(t, fullDays, backfillRate)
	defer mock.server.Close()
	fr := newBackfillFiatRates(d, newBackfillCoingecko(d, mock.server.URL), true)

	if done, _ := fr.runHistoricalCycle(nil); !done {
		t.Fatal("historical cycle not done")
	}
	done, err := d.FiatRatesGetHistoricalBackfillDone()
	if err != nil || !done {
		t.Fatalf("backfill marker not set: done=%v err=%v", done, err)
	}
	// the in-memory daily cache must include the backfilled days
	oldest := midnightDaysAgo(10)
	fr.mux.RLock()
	from := fr.dailyTickersFrom
	cached := fr.dailyTickers[oldest.Unix()]
	fr.mux.RUnlock()
	if from != oldest.Unix() || cached == nil || cached.Rates["usd"] != float32(backfillRate("usd", oldest)) {
		t.Fatalf("daily cache not refreshed with backfilled days: from=%d cached=%+v", from, cached)
	}

	if done, _ := fr.runHistoricalCycle(nil); !done {
		t.Fatal("second historical cycle not done")
	}
	if got := mock.maxRequests.Load(); got != 2 {
		t.Fatalf("backfill ran again despite the marker: %d days=max requests, want 2", got)
	}
}

func TestRunHistoricalCycle_NoBackfillParamMakesNoMaxRequest(t *testing.T) {
	d, tmp, fullDays := setupBackfillDB(t)
	defer closeAndDestroyRocksDB(t, d, tmp)
	mock := newBackfillMock(t, fullDays, backfillRate)
	defer mock.server.Close()
	fr := newBackfillFiatRates(d, newBackfillCoingecko(d, mock.server.URL), false)

	if done, _ := fr.runHistoricalCycle(nil); !done {
		t.Fatal("historical cycle not done")
	}
	if got := mock.maxRequests.Load(); got != 0 {
		t.Fatalf("unexpected days=max requests without backfillHistory: %d", got)
	}
	if done, _ := d.FiatRatesGetHistoricalBackfillDone(); done {
		t.Fatal("backfill marker set without backfillHistory")
	}
	day := midnightDaysAgo(10)
	if ticker, _ := d.FiatRatesGetTicker(&day); ticker != nil {
		t.Fatalf("old day stored without backfillHistory: %+v", ticker)
	}
}

func TestRunHistoricalCycle_BackfillFailureCountsAttemptsAndGivesUp(t *testing.T) {
	d, tmp, fullDays := setupBackfillDB(t)
	defer closeAndDestroyRocksDB(t, d, tmp)
	mock := newBackfillMock(t, fullDays, backfillRate)
	defer mock.server.Close()
	mock.failMax.Store("eur")
	fr := newBackfillFiatRates(d, newBackfillCoingecko(d, mock.server.URL), true)

	for attempt := 1; attempt <= maxHistoricalBackfillAttempts; attempt++ {
		if done, _ := fr.runHistoricalCycle(nil); !done {
			t.Fatalf("attempt %d: a backfill failure must not fail the daily historical cycle", attempt)
		}
		attempts, err := d.FiatRatesGetHistoricalBackfillAttempts()
		if err != nil || attempts != attempt {
			t.Fatalf("attempt %d: unexpected attempt count %d (err %v)", attempt, attempts, err)
		}
		done, _ := d.FiatRatesGetHistoricalBackfillDone()
		if attempt < maxHistoricalBackfillAttempts && done {
			t.Fatalf("attempt %d: marker set after a failed backfill", attempt)
		}
		if attempt == maxHistoricalBackfillAttempts && !done {
			t.Fatal("marker not set after exhausting backfill attempts")
		}
		// all-or-nothing: the usd series that did succeed must not be persisted
		day := midnightDaysAgo(10)
		if ticker, _ := d.FiatRatesGetTicker(&day); ticker != nil {
			t.Fatalf("attempt %d: partial backfill was persisted: %+v", attempt, ticker)
		}
	}
	requests := mock.maxRequests.Load()
	fr.runHistoricalCycle(nil)
	if got := mock.maxRequests.Load(); got != requests {
		t.Fatalf("backfill retried after giving up: %d -> %d days=max requests", requests, got)
	}
}
