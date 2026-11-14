// Package strategy turns per-market price, indicator and news context into
// trade ideas. Three strategies exist: basic_strategy (indicators only),
// basic_and_llm_strategy (indicator consensus + LLM signal analysis) and
// llm_only_strategy (pure LLM trade recommendation).
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
	BasicID       = "basic_strategy"
	BasicAndLLMID = "basic_and_llm_strategy"
	LLMOnlyID     = "llm_only_strategy"
	DefaultID     = BasicAndLLMID
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
	// RequiredPriceTicks is the price-history depth an LLM strategy needs
	// before it can evaluate (main.py's _llm_price_history_ticks_for_strategy);
	// 0 for non-LLM strategies, which instead wait for indicator warm-up.
	RequiredPriceTicks() int
	// Evaluate returns zero or more trade ideas for the given input.
	Evaluate(ctx context.Context, in Input) []domain.TradeIdea
}

// LookbackTicks is the price-history depth needed before every indicator
// (MACD needs slow_ema + signal - 1 = 34 bars) is computable (main.py _LOOKBACK_TICKS).
const LookbackTicks = 34

// All returns one instance of every strategy, in display order:
// basic_strategy, basic_and_llm_strategy, llm_only_strategy.
func All(s config.Settings) []Strategy {
	return []Strategy{NewBasic(s), NewBasicAndLLM(s), NewLLMOnly(s)}
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
