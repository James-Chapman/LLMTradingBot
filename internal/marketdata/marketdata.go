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
)

// Input is one market's raw data at a strategy tick.
type Input struct {
	Price        float64         // latest last-traded price
	Volume       *float64        // rolling 24-hour base volume; nil when unknown
	History      []float64       // price ticks, oldest first, newest last (includes Price)
	OHLC5        []domain.Candle // 5-minute candles, oldest first; may be empty
	OHLC15       []domain.Candle // 15-minute candles, oldest first; may be empty
	LLMSentiment float64         // latest briefing score for the market, -1..1
}

// Build returns the strategy context for one market. PreviousPrice is the
// tick momentumLookbackTicks ticks back (the current price while the history
// is shorter); price-history indicators run on the 30-second ticks with the
// 5-minute candles for ATR and Stochastic; the higher timeframe runs on the
// 15-minute candles.
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
	}
}
