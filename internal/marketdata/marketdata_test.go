package marketdata

import (
	"reflect"
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

// TestBuildTrendCandles covers how the 4-hour candles reach the strategy input.
func TestBuildTrendCandles(t *testing.T) {
	t.Run("GIVEN 4-hour candles WHEN the strategy input is built THEN they are passed through unchanged, oldest first", func(t *testing.T) {
		candles := []domain.Candle{
			{T: 0, O: 1, H: 2, L: 0.5, C: 1.5, V: 10},
			{T: 4 * 3600, O: 1.5, H: 1.6, L: 1.4, C: 1.55, V: 3},
		}
		got := Build(Input{Price: 1.55, History: []float64{1.5, 1.55}, OHLC240: candles}, 1)
		if !reflect.DeepEqual(got.TrendCandles, candles) {
			t.Fatalf("trend candles = %+v, want %+v", got.TrendCandles, candles)
		}
	})

	t.Run("GIVEN no 4-hour candles WHEN the strategy input is built THEN the trend candles are empty", func(t *testing.T) {
		got := Build(Input{Price: 1, History: []float64{1}}, 1)
		if len(got.TrendCandles) != 0 {
			t.Fatalf("expected no trend candles, got %d", len(got.TrendCandles))
		}
	})

	t.Run("GIVEN the window constants WHEN read THEN the 4-hour window is Kraken's 240-minute interval and holds more rows than the short candles", func(t *testing.T) {
		if TrendCandleMinutes != 240 || TrendCandleLimit <= CandleLimit || TrendCandleLimit > 720 {
			t.Fatalf("TrendCandleMinutes=%d TrendCandleLimit=%d", TrendCandleMinutes, TrendCandleLimit)
		}
	})
}
