package app

import (
	"context"
	"sync"
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/marketdata"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy"
)

// TestTrendCandlesReachStrategy covers the live path of the 4-hour candles:
// fetched by the OHLC loop and passed to the strategy in its market data.
func TestTrendCandlesReachStrategy(t *testing.T) {
	t.Run("GIVEN Kraken 4-hour candles WHEN an OHLC cycle and a strategy tick run THEN the strategy sees them as each market's trend candles", func(t *testing.T) {
		env := newEnv(t, nil, seedPrices(LookbackTicksForTest()+1))
		fourHour := []domain.Candle{{T: 0, O: 1, H: 2, L: 1, C: 2, V: 5}, {T: 14400, O: 2, H: 3, L: 2, C: 3, V: 1}}
		oneHour := []domain.Candle{{T: 0, O: 1, H: 2, L: 1, C: 2, V: 5}, {T: 3600, O: 2, H: 4, L: 2, C: 4, V: 2}}
		env.market.Candles[marketdata.TrendCandleMinutes] = fourHour
		env.market.Candles[marketdata.HourCandleMinutes] = oneHour

		var mu sync.Mutex
		seen := map[string][]domain.Candle{}
		seenHours := map[string][]domain.Candle{}
		env.strategy.Ideas = func(in strategy.Input) []domain.TradeIdea {
			mu.Lock()
			defer mu.Unlock()
			for m, md := range in.MarketData {
				seen[m] = md.TrendCandles
				seenHours[m] = md.HourCandles
			}
			return nil
		}
		env.boot(t)
		env.app.ohlcCycle(context.Background())
		env.tick(t)

		mu.Lock()
		defer mu.Unlock()
		for _, m := range []string{"BTC/EUR", "ETH/EUR"} {
			if len(seen[m]) != len(fourHour) || seen[m][1] != fourHour[1] {
				t.Fatalf("%s trend candles = %+v, want %+v", m, seen[m], fourHour)
			}
			if len(seenHours[m]) != len(oneHour) || seenHours[m][1] != oneHour[1] {
				t.Fatalf("%s hour candles = %+v, want %+v", m, seenHours[m], oneHour)
			}
		}
	})

	t.Run("GIVEN an OHLC cycle WHEN it fetches candles THEN the 4-hour interval asks for the longer trend window", func(t *testing.T) {
		env := newEnv(t, nil, nil)
		rec := &recordingMarket{flakyMarket: flakyMarket{Market: env.market}}
		env.app.market = rec
		env.boot(t)
		env.app.ohlcCycle(context.Background())

		limits := rec.limits()
		if limits[marketdata.TrendCandleMinutes] != marketdata.TrendCandleLimit {
			t.Fatalf("4-hour limit = %d, want %d (all: %v)", limits[marketdata.TrendCandleMinutes], marketdata.TrendCandleLimit, limits)
		}
		if limits[marketdata.HourCandleMinutes] != marketdata.HourCandleLimit {
			t.Fatalf("1-hour limit = %d, want %d (all: %v)", limits[marketdata.HourCandleMinutes], marketdata.HourCandleLimit, limits)
		}
		if limits[5] != ohlcCandleLimit || limits[15] != ohlcCandleLimit {
			t.Fatalf("5/15-minute limits changed: %v", limits)
		}
	})
}

// recordingMarket records the interval and limit of every OHLC request.
type recordingMarket struct {
	flakyMarket
	mu    sync.Mutex
	calls map[int]int // interval → last requested limit
	count map[int]int // interval → number of requests
}

// GetOHLC records the request and serves the fake's candles.
func (m *recordingMarket) GetOHLC(ctx context.Context, symbol string, interval, limit int) ([]domain.Candle, error) {
	m.mu.Lock()
	if m.calls == nil {
		m.calls = map[int]int{}
	}
	m.calls[interval] = limit
	if m.count == nil {
		m.count = map[int]int{}
	}
	m.count[interval]++
	m.mu.Unlock()
	return m.flakyMarket.GetOHLC(ctx, symbol, interval, limit)
}

// limits returns interval → requested limit.
func (m *recordingMarket) limits() map[int]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[int]int{}
	for k, v := range m.calls {
		out[k] = v
	}
	return out
}

// counts returns interval → number of OHLC requests.
func (m *recordingMarket) counts() map[int]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[int]int{}
	for k, v := range m.count {
		out[k] = v
	}
	return out
}
