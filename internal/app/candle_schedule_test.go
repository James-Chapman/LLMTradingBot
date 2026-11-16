package app

import (
	"context"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/marketdata"
)

// candlesEndingAt returns two candles of interval minutes, the last starting at last.
func candlesEndingAt(last time.Time, interval int) []domain.Candle {
	step := int64(interval * 60)
	return []domain.Candle{
		{T: last.Unix() - step, O: 1, H: 1, L: 1, C: 1, V: 1},
		{T: last.Unix(), O: 1, H: 1, L: 1, C: 1, V: 1},
	}
}

// at is a fixed UTC time on 2026-11-16.
func at(hour, minute, second int) time.Time {
	return time.Date(2026, 11, 16, hour, minute, second, 0, time.UTC)
}

// TestCandleFetchSchedule covers fetching the 1-hour and 4-hour candles by
// the clock: once their next interval has begun, not on every OHLC cycle.
func TestCandleFetchSchedule(t *testing.T) {
	// cycle runs one OHLC cycle at now with cached 1h/4h candles whose last
	// rows start at hour and four, and Kraken serving served (interval →
	// candles); it returns the number of requests per interval.
	cycle := func(t *testing.T, now, hour, four time.Time, served map[int][]domain.Candle, runs int) map[int]int {
		t.Helper()
		env := newEnv(t, nil, nil)
		for interval, c := range served {
			env.market.Candles[interval] = c
		}
		rec := &recordingMarket{flakyMarket: flakyMarket{Market: env.market}}
		env.app.market = rec
		env.boot(t)
		env.app.now = func() time.Time { return now }
		env.app.intervals.OHLCIntervalGap, env.app.intervals.OHLCMarketGap = time.Millisecond, time.Millisecond
		for _, m := range env.app.ActiveMarkets() {
			if !hour.IsZero() {
				env.app.ohlc60[m] = OHLCPayload{Symbol: m, Interval: 60, Candles: candlesEndingAt(hour, 60)}
			}
			if !four.IsZero() {
				env.app.ohlc240[m] = OHLCPayload{Symbol: m, Interval: 240, Candles: candlesEndingAt(four, 240)}
			}
		}
		for i := 0; i < runs; i++ {
			env.app.ohlcCycle(context.Background())
		}
		return rec.counts()
	}
	markets := 2 // BTC/EUR and ETH/EUR

	t.Run("GIVEN cached 1-hour and 4-hour candles that hold the current interval's row WHEN an OHLC cycle runs THEN only the 5- and 15-minute candles are fetched", func(t *testing.T) {
		got := cycle(t, at(10, 30, 0), at(10, 0, 0), at(8, 0, 0), nil, 1)
		if got[60] != 0 || got[240] != 0 || got[5] != markets || got[15] != markets {
			t.Fatalf("requests per interval %v", got)
		}
	})

	t.Run("GIVEN a new hour began more than the fetch delay ago WHEN a cycle runs THEN the 1-hour candles are fetched, and the 4-hour ones only after a 4-hour boundary", func(t *testing.T) {
		served := map[int][]domain.Candle{60: candlesEndingAt(at(11, 0, 0), 60)}
		got := cycle(t, at(11, 0, 30), at(10, 0, 0), at(8, 0, 0), served, 1)
		if got[60] != markets || got[240] != 0 {
			t.Fatalf("requests per interval %v", got)
		}
		served[240] = candlesEndingAt(at(12, 0, 0), 240)
		if got := cycle(t, at(12, 0, 30), at(11, 0, 0), at(8, 0, 0), served, 1); got[240] != markets {
			t.Fatalf("after the 12:00 boundary: requests per interval %v", got)
		}
	})

	t.Run("GIVEN the hour began less than the fetch delay ago WHEN a cycle runs THEN the 1-hour candles wait for Kraken to open the new candle", func(t *testing.T) {
		if got := cycle(t, at(11, 0, 5), at(10, 0, 0), at(8, 0, 0), nil, 1); got[60] != 0 {
			t.Fatalf("requests per interval %v", got)
		}
	})

	t.Run("GIVEN Kraken's answer has no row for the current hour yet WHEN the next cycle runs THEN the 1-hour candles are fetched again", func(t *testing.T) {
		served := map[int][]domain.Candle{60: candlesEndingAt(at(10, 0, 0), 60)} // no 11:00 row: no trade yet
		if got := cycle(t, at(11, 5, 0), at(10, 0, 0), at(8, 0, 0), served, 2); got[60] != 2*markets {
			t.Fatalf("requests per interval %v, want two 1-hour fetches per market", got)
		}
	})

	t.Run("GIVEN no cached candles WHEN a cycle runs THEN every interval is fetched with its window", func(t *testing.T) {
		got := cycle(t, at(11, 0, 5), time.Time{}, time.Time{}, nil, 1)
		if got[60] != markets || got[240] != markets {
			t.Fatalf("requests per interval %v", got)
		}
	})

	t.Run("GIVEN the time WHEN the OHLC loop picks its wait THEN it wakes just after the next hour boundary, or after the refresh interval if that is sooner", func(t *testing.T) {
		refresh := 120 * time.Second
		if got := ohlcWait(at(10, 59, 50), refresh); got != 10*time.Second+candleFetchDelay {
			t.Fatalf("wait %v before the boundary", got)
		}
		if got := ohlcWait(at(10, 30, 0), refresh); got != refresh {
			t.Fatalf("wait %v mid-hour", got)
		}
	})

	t.Run("GIVEN the constants WHEN read THEN Kraken is given a short delay after the boundary", func(t *testing.T) {
		if candleFetchDelay <= 0 || candleFetchDelay > time.Minute {
			t.Fatalf("candleFetchDelay = %v", candleFetchDelay)
		}
		if marketdata.HourCandleMinutes != 60 {
			t.Fatalf("hour candles are %d minutes", marketdata.HourCandleMinutes)
		}
	})
}

// TestWarmUpByStrategyNeed covers the start-up wait following what the
// selected strategy needs: price ticks, or nothing for candle strategies.
func TestWarmUpByStrategyNeed(t *testing.T) {
	t.Run("GIVEN a strategy that reads candles, not ticks WHEN the first tick runs with no price history THEN it evaluates without a tick warm-up", func(t *testing.T) {
		env := newEnv(t, nil, nil)
		env.strategy.Ticks = 0
		env.boot(t)
		env.tick(t)
		if env.strategy.Calls() != 1 || env.activityContains("warming up") {
			t.Fatalf("calls %d; the candle strategy must not wait for price ticks", env.strategy.Calls())
		}
	})
}
