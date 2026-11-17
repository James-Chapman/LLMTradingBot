package app

import (
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

// TestOHLCIntervals covers the candle intervals the OHLC view serves: the
// 5- and 15-minute chart candles and the 1- and 4-hour strategy candles.
func TestOHLCIntervals(t *testing.T) {
	env := newEnv(t, nil, nil)
	candles := func(start int64, minutes, n int) []domain.Candle {
		out := make([]domain.Candle, n)
		for i := range out {
			out[i] = domain.Candle{T: start + int64(i*minutes*60), O: 1, H: 2, L: 0.5, C: 1.5}
		}
		return out
	}
	env.app.ohlc5["BTC/EUR"] = OHLCPayload{Symbol: "BTC/EUR", Interval: 5, Candles: candles(0, 5, 3)}
	env.app.ohlc60["BTC/EUR"] = OHLCPayload{Symbol: "BTC/EUR", Interval: 60, Candles: candles(0, 60, 4)}
	env.app.ohlc240["BTC/EUR"] = OHLCPayload{Symbol: "BTC/EUR", Interval: 240, Candles: candles(0, 240, 5)}

	t.Run("GIVEN cached strategy candles WHEN 1-hour or 4-hour candles are asked for THEN those candles are served", func(t *testing.T) {
		for minutes, want := range map[int]int{60: 4, 240: 5} {
			got := env.app.OHLC("BTC/EUR", minutes)
			if got.Interval != minutes || len(got.Candles) != want {
				t.Errorf("%d minutes: interval %d, %d candles, want %d", minutes, got.Interval, len(got.Candles), want)
			}
		}
	})

	t.Run("GIVEN no cached candles for an interval WHEN they are asked for THEN an empty payload echoes the interval", func(t *testing.T) {
		got := env.app.OHLC("ETH/EUR", 60)
		if got.Symbol != "ETH/EUR" || got.Interval != 60 || len(got.Candles) != 0 {
			t.Errorf("payload %+v, want an empty 60-minute payload", got)
		}
	})

	t.Run("GIVEN an interval the bot does not keep WHEN it is asked for THEN the 5-minute candles are served as before", func(t *testing.T) {
		if got := env.app.OHLC("BTC/EUR", 30); len(got.Candles) != 3 {
			t.Errorf("%d candles, want the three 5-minute candles", len(got.Candles))
		}
	})
}
