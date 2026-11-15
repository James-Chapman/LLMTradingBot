package strategy

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/James-Chapman/LLMTradingBot/internal/analysis"
	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/llm"
	"github.com/James-Chapman/LLMTradingBot/internal/observability"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy/calc"
)

// Trend strategy parameters. The strategy trades the 4-hour trend: it holds
// a market while the fast EMA of the closed 4-hour candles is above the slow
// one and exits when it falls below. Few, long trades keep the fee (paid on
// every fill) small against the size of the moves it tries to catch.
const (
	// TrendFastEMA is the fast EMA period, in 4-hour candles (about 8 days).
	TrendFastEMA = 50
	// TrendSlowEMA is the slow EMA period, in 4-hour candles (25 days).
	// Both were chosen on January-August 2026 BTC/EUR and ETH/EUR data; see
	// docs/06-strategy-and-learning.md for the backtest results.
	TrendSlowEMA = 150
	// TrendMinClosedCandles is the warm-up: closed 4-hour candles needed
	// before the strategy decides anything (the slow EMA plus time to settle).
	TrendMinClosedCandles = TrendSlowEMA + 10
	// TrendConfidence is the confidence of every idea. It is high on purpose:
	// the learner can scale a confidence down to half, and an exit dropped by
	// the MIN_SIGNAL_CONFIDENCE gate would leave a position open in a downtrend.
	TrendConfidence = 0.95
)

// TrendStrategy is the 4-hour EMA trend-following strategy (no LLM).
type TrendStrategy struct {
	settings config.Settings
	log      *slog.Logger
}

// NewTrend creates the 4-hour trend strategy.
func NewTrend(s config.Settings) *TrendStrategy {
	return &TrendStrategy{settings: s, log: observability.Logger("trend_strategy")}
}

// ID returns "trend_strategy".
func (t *TrendStrategy) ID() string { return TrendID }

// UsesLLM is false: the trend strategy needs no news or analyser.
func (t *TrendStrategy) UsesLLM() bool { return false }

// RequiredPriceTicks is 0: the strategy waits for its own 4-hour candle warm-up.
func (t *TrendStrategy) RequiredPriceTicks() int { return 0 }

// Evaluate returns at most one idea per market with enough closed 4-hour
// candles: a long while the fast EMA is above the slow one and the market is
// not held, a rule exit while it is below and a long is held.
func (t *TrendStrategy) Evaluate(_ context.Context, in Input) []domain.TradeIdea {
	ideas := []domain.TradeIdea{}
	for _, symbol := range sortedMarkets(in.MarketData) {
		if idea := t.evaluateMarket(symbol, in.MarketData[symbol]); idea != nil && actionable(idea, in.Portfolio) {
			ideas = append(ideas, *idea)
		}
	}
	return ideas
}

// evaluateMarket returns the idea for one market, or nil during warm-up or
// when the two EMAs are equal.
func (t *TrendStrategy) evaluateMarket(symbol string, data MarketData) *domain.TradeIdea {
	closes := closedCloses(data.TrendCandles)
	if len(closes) < TrendMinClosedCandles {
		t.log.Debug("skip: 4h candle warm-up", "market", symbol, "closed", len(closes), "required", TrendMinClosedCandles)
		return nil
	}
	fast, okFast := analysis.EMA(closes, TrendFastEMA)
	slow, okSlow := analysis.EMA(closes, TrendSlowEMA)
	if !okFast || !okSlow || slow <= 0 || fast == slow {
		return nil
	}

	direction, exitPlan := domain.Long, "Exit when the 4h fast EMA closes below the slow EMA, or on the stop-loss / trailing stop"
	if fast < slow {
		direction, exitPlan = domain.Short, "Sell the held position: the 4h trend has turned down"
	}
	gap := (fast - slow) / slow
	slPct := stopLossPercent(t.settings.StopLossPct)
	idea := &domain.TradeIdea{
		ID:         domain.NewID(),
		StrategyID: TrendID,
		Market:     symbol,
		Direction:  direction,
		Thesis: fmt.Sprintf("4h EMA%d %s %s EMA%d %s (%+.2f%%) on %d closed candles",
			TrendFastEMA, fmtPrice(fast), aboveOrBelow(fast, slow), TrendSlowEMA, fmtPrice(slow), gap*100, len(closes)),
		SupportingSignals: map[string]any{
			"ema_fast_4h":    pyRound(fast, 8),
			"ema_slow_4h":    pyRound(slow, 8),
			"ema_gap":        pyRound(gap, 6),
			"closed_candles": len(closes),
			"current_price":  data.Price,
		},
		Confidence:             TrendConfidence,
		EntryPlan:              "Enter at market while the 4h fast EMA is above the slow EMA",
		ExitPlan:               exitPlan,
		StopOrInvalidation:     fmt.Sprintf("Abandon if unrealised loss reaches %d%%", slPct),
		PositionSizingProposal: calc.PositionSizingProposal,
		ModeEligibility:        allModes(),
	}
	t.log.Debug("Generated trade idea", "strategy", TrendID, "market", symbol,
		"direction", string(direction), "ema_gap_pct", pyRound(gap*100, 3))
	return idea
}

// closedCloses returns the close of every 4-hour candle except the newest,
// which is still forming (Kraken's OHLC response always ends with it, and the
// backtest replay reproduces that), so a decision only uses finished candles.
func closedCloses(candles []domain.Candle) []float64 {
	if len(candles) < 2 {
		return nil
	}
	closes := make([]float64, 0, len(candles)-1)
	for _, c := range candles[:len(candles)-1] {
		closes = append(closes, c.C)
	}
	return closes
}

// aboveOrBelow describes fast relative to slow.
func aboveOrBelow(fast, slow float64) string {
	if fast > slow {
		return "above"
	}
	return "below"
}

// fmtPrice formats a price with enough digits for sub-cent coins.
func fmtPrice(p float64) string {
	if p >= 1 {
		return fmt.Sprintf("%.2f", p)
	}
	return fmt.Sprintf("%.6g", p)
}

// trendInstructions is the trend strategy's own prompt to the LLM.
const trendInstructions = "This is a slow 4-hour trend-following entry: the 50-candle EMA (about 8 days) of finished " +
	"4-hour candles is above the 150-candle EMA (25 days). Trend entries arrive after a rise has started; most " +
	"lose a little and a few large multi-week moves pay for them. The position is held for days to weeks and " +
	"exits when the EMAs cross back or on the trailing stop, so do not judge it on the next few hours. " +
	"Use a scale above 1.0 when the news or briefing points to a lasting shift (adoption, regulation, fund flows) " +
	"behind the rise; below 1.0 when the rise looks driven by a one-off headline likely to reverse, or a known " +
	"risk event is close; below 0.7 only when you expect the trend to fail."

// Prompt returns the trend strategy's own prompt for an entry.
func (t *TrendStrategy) Prompt(idea domain.TradeIdea, _ MarketData) llm.StrategyPrompt {
	return llm.StrategyPrompt{
		Name:         "4-hour EMA trend following (EMA50 / EMA150)",
		Instructions: trendInstructions,
		Data:         promptData(idea.SupportingSignals),
	}
}

// NewTrendAndLLM creates the trend strategy's LLM version.
func NewTrendAndLLM(s config.Settings) *LLMVersion {
	return NewLLMVersion(NewTrend(s), TrendAndLLMID, s)
}
