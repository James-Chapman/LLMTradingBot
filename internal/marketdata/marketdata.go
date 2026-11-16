// Package marketdata builds the per-market strategy input of one strategy
// tick. The live strategy loop (internal/app) and the historical replay
// (internal/backtest) both call Build, so a backtest feeds the strategy
// exactly what the live bot computes from the same ticks and candles.
//
// It is pure: no clock, no I/O, no state.
package marketdata

import (
	"github.com/James-Chapman/LLMTradingBot/internal/analysis"
	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy"
)

// Window sizes and timescales the live loop uses.
const (
	// PriceHistoryTicks is how many 30-second price ticks per market the
	// strategy sees (app.priceHistoryMax).
	PriceHistoryTicks = config.PriceHistoryTicks
	// CandleLimit is how many 5- and 15-minute candles the live loop keeps
	// per market (app.ohlcCandleLimit), the newest being the forming candle.
	CandleLimit = 100
	// TickSeconds is the strategy tick the price-history indicators assume.
	TickSeconds = analysis.DefaultTickSecs
	// HigherTimeframeSeconds is the tick the 15-minute indicators assume.
	HigherTimeframeSeconds = 900
	// TrendCandleMinutes is the Kraken OHLC interval of the trend candles (4 hours).
	TrendCandleMinutes = 240
	// TrendCandleLimit is how many 4-hour candles the live loop keeps per
	// market (120 days, Kraken's maximum of 720 rows), the newest being the
	// forming candle, so the trend strategy's 150-candle EMA has settled.
	TrendCandleLimit = 720
	// HourCandleMinutes is the Kraken OHLC interval of the hour candles.
	HourCandleMinutes = 60
	// HourCandleLimit is how many 1-hour candles the live loop keeps per
	// market (30 days, Kraken's maximum of 720 rows), the newest being the
	// forming candle.
	HourCandleLimit = 720
)

// Input is one market's raw data at a strategy tick.
type Input struct {
	Price        float64         // latest last-traded price
	Volume       *float64        // rolling 24-hour base volume; nil when unknown
	History      []float64       // price ticks, oldest first, newest last (includes Price)
	OHLC5        []domain.Candle // 5-minute candles, oldest first; may be empty
	OHLC15       []domain.Candle // 15-minute candles, oldest first; may be empty
	OHLC60       []domain.Candle // 1-hour candles, oldest first, newest forming; may be empty
	OHLC240      []domain.Candle // 4-hour candles, oldest first, newest forming; may be empty
	LLMSentiment float64         // latest briefing score for the market, -1..1
}

// Build returns the strategy context for one market. PreviousPrice is the
// tick momentumLookbackTicks ticks back (the current price while the history
// is shorter); price-history indicators run on the 30-second ticks with the
// 5-minute candles for ATR and Stochastic; the higher timeframe runs on the
// 15-minute candles. The 1-hour and 4-hour candles are passed through as they are.
func Build(in Input, momentumLookbackTicks int) strategy.MarketData {
	lookback := momentumLookbackTicks + 1
	prev := in.Price
	if len(in.History) >= lookback {
		prev = in.History[len(in.History)-lookback]
	}
	htf := analysis.Indicators{}
	if len(in.OHLC15) > 0 {
		closes := make([]float64, 0, len(in.OHLC15))
		for _, c := range in.OHLC15 {
			closes = append(closes, c.C)
		}
		htf = analysis.ComputeAll(closes, HigherTimeframeSeconds, in.OHLC15)
	}
	return strategy.MarketData{
		Price:           in.Price,
		PreviousPrice:   prev,
		PriceHistory:    in.History,
		Volume:          in.Volume,
		LLMSentiment:    in.LLMSentiment,
		HigherTimeframe: htf,
		Indicators:      analysis.ComputeAll(in.History, TickSeconds, in.OHLC5),
		HourCandles:     in.OHLC60,
		TrendCandles:    in.OHLC240,
	}
}
