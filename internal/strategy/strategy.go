// Package strategy turns per-market price, indicator and news context into
// trade ideas. Eight basic strategies read the basic indicator rules on
// finished 1-hour or 4-hour candles: basic_strategy_* (indicators only) and
// basic_and_llm_strategy_* (indicators + LLM signal analysis), each in a
// version that may open paper shorts and a long-only version (_long_).
// llm_only_strategy is a pure LLM trade recommendation. Six 4-hour candle
// strategies follow: trend, donchian, rsi_dip, macd, squeeze and rotation,
// each with an LLM version (LLMVersion) that sends its entries to the LLM
// with the strategy's own prompt.
package strategy

import (
	"context"

	"github.com/James-Chapman/LLMTradingBot/internal/analysis"
	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/control"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/llm"
)

// Strategy IDs.
const (
	Basic1hID           = "basic_strategy_1h"
	Basic4hID           = "basic_strategy_4h"
	BasicLong1hID       = "basic_strategy_long_1h"
	BasicLong4hID       = "basic_strategy_long_4h"
	BasicAndLLM1hID     = "basic_and_llm_strategy_1h"
	BasicAndLLM4hID     = "basic_and_llm_strategy_4h"
	BasicAndLLMLong1hID = "basic_and_llm_strategy_long_1h"
	BasicAndLLMLong4hID = "basic_and_llm_strategy_long_4h"
	LLMOnlyID           = "llm_only_strategy"
	TrendID             = "trend_strategy"
	TrendAndLLMID       = "trend_and_llm_strategy"

	DonchianID       = "donchian_strategy"
	DonchianAndLLMID = "donchian_and_llm_strategy"
	RSIDipID         = "rsi_dip_strategy"
	RSIDipAndLLMID   = "rsi_dip_and_llm_strategy"
	MACDID           = "macd_strategy"
	MACDAndLLMID     = "macd_and_llm_strategy"
	SqueezeID        = "squeeze_strategy"
	SqueezeAndLLMID  = "squeeze_and_llm_strategy"
	RotationID       = "rotation_strategy"
	RotationAndLLMID = "rotation_and_llm_strategy"
	DefaultID        = BasicAndLLM1hID
)

// MarketData is the per-market context built by the strategy loop each tick
// (the Go form of main.py's market_data[sym] dict).
type MarketData struct {
	Price           float64             `json:"price"`
	PreviousPrice   float64             `json:"previous_price"`
	PriceHistory    []float64           `json:"price_history"` // oldest first
	Volume          *float64            `json:"volume,omitempty"`
	LLMSentiment    float64             `json:"llm_sentiment"`
	HigherTimeframe analysis.Indicators `json:"higher_timeframe"` // 15-minute indicators, may be empty
	Indicators      analysis.Indicators `json:"indicators"`
	// TrendCandles are the 4-hour candles, oldest first; the newest is the
	// forming one (Kraken always returns it). May be empty.
	TrendCandles []domain.Candle `json:"trend_candles,omitempty"`
	// HourCandles are the 1-hour candles, oldest first; the newest is the
	// forming one. May be empty.
	HourCandles []domain.Candle `json:"hour_candles,omitempty"`
}

// Portfolio is the account context passed to LLM-backed strategies.
type Portfolio struct {
	Equity        float64
	Cash          float64
	OpenPositions []domain.PositionRecord
}

// Analyser is the subset of *llm.LLMAnalyser the strategies call.
// Declared here so tests can substitute a fake.
type Analyser interface {
	AnalyseSignal(ctx context.Context, req llm.SignalRequest) llm.SignalAnalysis
	RecommendTrade(ctx context.Context, req llm.RecommendationRequest) llm.LLMTradeRecommendation
}

// Input is everything a strategy may use for one evaluation.
type Input struct {
	MarketData map[string]MarketData
	News       []llm.NewsArticle   // latest cached news, newest first
	Analyser   Analyser            // nil for strategies that do not use the LLM
	Portfolio  Portfolio           // used by LLM-backed strategies
	Briefing   *llm.MarketBriefing // latest market briefing, may be nil
}

// Strategy is implemented by every trading strategy.
type Strategy interface {
	// ID returns the canonical strategy id (one of the *ID constants).
	ID() string
	// UsesLLM reports whether evaluation needs LLM context (news + analyser).
	UsesLLM() bool
	// RequiredPriceTicks is the price-tick history the strategy needs before
	// it can evaluate (10 for llm_only); 0 for the candle strategies, which
	// wait for their own finished candles instead. The live loop and the
	// backtest replay both wait for it at start-up.
	RequiredPriceTicks() int
	// Evaluate returns zero or more trade ideas for the given input.
	Evaluate(ctx context.Context, in Input) []domain.TradeIdea
}

// LookbackTicks is the price-history depth needed before every indicator
// (MACD needs slow_ema + signal - 1 = 34 bars) is computable (main.py _LOOKBACK_TICKS).
const LookbackTicks = 34

// All returns one instance of every strategy, in display order: the eight
// basic strategies, llm_only and trend, then each 4-hour strategy followed
// by its LLM version.
func All(s config.Settings) []Strategy {
	return []Strategy{
		NewBasic1h(s), NewBasicLong1h(s), NewBasic4h(s), NewBasicLong4h(s),
		NewBasicAndLLM1h(s), NewBasicAndLLMLong1h(s), NewBasicAndLLM4h(s), NewBasicAndLLMLong4h(s),
		NewLLMOnly(s), NewTrend(s), NewTrendAndLLM(s),
		NewDonchian(s), NewDonchianAndLLM(s), NewRSIDip(s), NewRSIDipAndLLM(s),
		NewMACD(s), NewMACDAndLLM(s), NewSqueeze(s), NewSqueezeAndLLM(s),
		NewRotation(s), NewRotationAndLLM(s),
	}
}

// IsBasicAndLLM reports whether id is one of the four basic_and_llm strategies,
// whose LLM verdict is stored in the idea's supporting signals.
func IsBasicAndLLM(id string) bool {
	switch id {
	case BasicAndLLM1hID, BasicAndLLM4hID, BasicAndLLMLong1hID, BasicAndLLMLong4hID:
		return true
	}
	return false
}

// CandleMinutes returns the length, in minutes, of the candles strategy id
// reads its signals from: 60 for the 1-hour basic strategies and All LLM, 240 for the
// 4-hour ones (the basic 4h, trend, Donchian, RSI dip, MACD, squeeze and
// rotation strategies and their LLM versions). Unknown ids get 15,
// the closest candles the bot keeps.
func CandleMinutes(id string) int {
	switch control.CanonicalStrategyID(id) {
	case Basic1hID, BasicLong1hID, BasicAndLLM1hID, BasicAndLLMLong1hID, LLMOnlyID:
		return 60
	case Basic4hID, BasicLong4hID, BasicAndLLM4hID, BasicAndLLMLong4hID,
		TrendID, TrendAndLLMID, DonchianID, DonchianAndLLMID, RSIDipID, RSIDipAndLLMID,
		MACDID, MACDAndLLMID, SqueezeID, SqueezeAndLLMID, RotationID, RotationAndLLMID:
		return 240
	}
	return 15
}

// ByID returns the strategy with the given id (legacy ids are mapped to their
// canonical form), or nil when none matches.
func ByID(list []Strategy, id string) Strategy {
	canonical := control.CanonicalStrategyID(id)
	for _, s := range list {
		if s != nil && s.ID() == canonical {
			return s
		}
	}
	return nil
}
