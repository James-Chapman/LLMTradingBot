package strategy

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/llm"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy/calc"
)

// indicatorSignal is a market signal that passed the shared indicator gates
// (momentum, higher timeframe, hard filters, support, consensus, confidence).
type indicatorSignal struct {
	Momentum   float64
	Direction  domain.Direction
	ATRPct     float64
	Votes      calc.VoteSummary
	Confidence calc.ConfidenceBreakdown
}

// evaluateIndicatorGates runs gates 1-6 shared by basic_strategy and
// basic_and_llm_strategy. It returns nil when any gate rejects the market.
func evaluateIndicatorGates(log *slog.Logger, symbol string, data MarketData) *indicatorSignal {
	// Python checks for the "price"/"previous_price" keys; a zero value is the Go equivalent.
	if data.Price <= 0 || data.PreviousPrice == 0 {
		log.Debug("skip: missing price or previous_price", "market", symbol)
		return nil
	}

	previous := data.PreviousPrice
	momentum, ok := calc.CalculateMomentum(data.Price, &previous, data.PriceHistory)
	if !ok {
		log.Debug("skip: no usable momentum baseline", "market", symbol)
		return nil
	}

	// atr_pct is in percentage units (0.5 = 0.5%); momentum is a decimal fraction.
	atrPct, _ := data.Indicators.Num("atr_pct")
	gate := calc.EvaluateMomentumGate(momentum, atrPct)
	if !gate.Passes {
		log.Debug("skip: momentum below threshold", "market", symbol,
			"momentum_pct", momentum*100, "threshold_pct", gate.EffectiveThreshold*100)
		return nil
	}

	isLong := momentum > 0
	direction := domain.Short
	if isLong {
		direction = domain.Long
	}

	if alignment := calc.CheckHigherTimeframeAlignment(isLong, data.HigherTimeframe); !alignment.Allowed {
		log.Debug("skip: higher timeframe opposes", "market", symbol, "direction", direction, "reason", alignment.Reason)
		return nil
	}

	if filter := calc.CheckHardFilters(isLong, data.Indicators); filter.Blocked {
		log.Debug("skip: hard filter", "market", symbol, "direction", direction, "reason", filter.Reason)
		return nil
	}

	votes := calc.VoteIndicators(isLong, data.Indicators)
	if votes.Supporting < calc.MinIndicatorsForSignal {
		log.Debug("skip: too few supporting indicators", "market", symbol,
			"supporting", votes.Supporting, "required", calc.MinIndicatorsForSignal)
		return nil
	}
	if votes.Votes < calc.MinConsensusVotes {
		log.Debug("skip: indicator consensus too weak", "market", symbol,
			"votes", votes.Votes, "available", votes.Available)
		return nil
	}

	confidence := calc.CalculateConfidence(momentum, votes.Votes, votes.Available, atrPct)
	if confidence.Confidence < calc.MinActionableConfidence {
		log.Debug("skip: confidence below actionable threshold", "market", symbol,
			"confidence", confidence.Confidence, "threshold", calc.MinActionableConfidence)
		return nil
	}

	return &indicatorSignal{
		Momentum:   momentum,
		Direction:  direction,
		ATRPct:     atrPct,
		Votes:      votes,
		Confidence: confidence,
	}
}

// thesisParts returns the human-readable indicator summary used as the idea thesis.
func (sig *indicatorSignal) thesisParts() []string {
	v := sig.Votes.Values
	parts := []string{fmt.Sprintf("Momentum %.2f%%", sig.Momentum*100)}
	if v["rsi_14"] != nil {
		parts = append(parts, "RSI "+pyStr(v["rsi_14"]))
	}
	if s, _ := v["ema_cross"].(string); s != "" && s != "neutral" {
		parts = append(parts, "EMA "+s)
	}
	if s, _ := v["macd_bias"].(string); s != "" && s != "neutral" {
		parts = append(parts, "MACD "+s)
	}
	if s, _ := v["macd_signal_bias"].(string); s != "" && s != "neutral" {
		parts = append(parts, "MACD-X "+s)
	}
	if v["stoch_k"] != nil {
		parts = append(parts, "Stoch "+pyStr(v["stoch_k"]))
	}
	if v["williams_r"] != nil {
		parts = append(parts, "WR "+pyStr(v["williams_r"]))
	}
	parts = append(parts, fmt.Sprintf("support %d/%d (net %+d)", sig.Votes.Supporting, sig.Votes.Available, sig.Votes.Votes))
	return parts
}

// supportingSignals returns the indicator context stored on the trade idea
// (same keys as the Python strategies).
func (sig *indicatorSignal) supportingSignals(data MarketData) map[string]any {
	v := sig.Votes.Values
	var atr any
	if sig.ATRPct != 0 {
		atr = pyRound(sig.ATRPct, 3)
	}
	out := map[string]any{
		"momentum":              pyRound(sig.Momentum, 6),
		"current_price":         data.Price,
		"previous_price":        data.PreviousPrice,
		"indicator_votes":       sig.Votes.Votes,
		"indicators_supporting": sig.Votes.Supporting,
		"indicators_opposing":   sig.Votes.Opposing,
		"indicators_available":  sig.Votes.Available,
		"rsi_14":                v["rsi_14"],
		"ema_cross":             v["ema_cross"],
		"bb_position":           v["bb_position"],
		"macd_bias":             v["macd_bias"],
		"macd_signal_bias":      v["macd_signal_bias"],
		"macd_histogram":        v["macd_histogram"],
		"stoch_k":               v["stoch_k"],
		"stoch_d":               v["stoch_d"],
		"williams_r":            v["williams_r"],
		"atr_pct":               atr,
		"price_change_5m":       v["price_change_5m"],
		"price_change_15m":      v["price_change_15m"],
	}
	// Candle versions only: the changes over 10 and 30 candles.
	for _, key := range []string{"price_change_10_candles", "price_change_30_candles"} {
		if c, ok := v[key]; ok {
			out[key] = c
		}
	}
	return out
}

// stopLossPercent returns the configured stop loss as a whole percentage.
// It rounds rather than truncates so float error (0.29*100 = 28.999...) is not lost.
func stopLossPercent(stopLossPct float64) int {
	return int(math.Round(stopLossPct * 100))
}

// allModes is the mode eligibility every strategy idea carries.
func allModes() []domain.TradingMode {
	return []domain.TradingMode{domain.ModeManual, domain.ModeSemiAutomated, domain.ModeFullyAutomated}
}

// sortedMarkets returns the market symbols in sorted order so evaluation is deterministic.
func sortedMarkets(data map[string]MarketData) []string {
	markets := make([]string, 0, len(data))
	for m := range data {
		markets = append(markets, m)
	}
	sort.Strings(markets)
	return markets
}

// pyRound rounds v to the given number of decimal places (Python round()).
func pyRound(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

// pyStr formats a value the way a Python f-string would (floats keep a ".0").
func pyStr(v any) string {
	switch x := v.(type) {
	case float64:
		s := strconv.FormatFloat(x, 'f', -1, 64)
		if !strings.ContainsAny(s, ".eEnN") {
			s += ".0"
		}
		return s
	case string:
		return x
	default:
		return fmt.Sprint(x)
	}
}

// NeutralAnalyser is an Analyser that never adjusts or recommends a trade,
// matching an unavailable LLM. Used when no real analyser is supplied (e.g. replay).
type NeutralAnalyser struct{}

// AnalyseSignal returns a neutral, unused LLM analysis (scale 1.0).
func (NeutralAnalyser) AnalyseSignal(_ context.Context, _ llm.SignalRequest) llm.SignalAnalysis {
	return llm.SignalAnalysis{ConfidenceScale: 1.0, Reasoning: "LLM unavailable — no adjustment"}
}

// RecommendTrade returns an unused "hold" recommendation.
func (NeutralAnalyser) RecommendTrade(_ context.Context, _ llm.RecommendationRequest) llm.LLMTradeRecommendation {
	return llm.LLMTradeRecommendation{Action: "hold", Reasoning: "LLM unavailable — no recommendation"}
}
