package calc

import (
	"fmt"
	"math"
	"strconv"

	"github.com/James-Chapman/LLMTradingBot/internal/analysis"
)

// MomentumGate is the result of applying the fixed and ATR-adjusted momentum threshold.
type MomentumGate struct {
	Passes             bool    `json:"passes"`
	DynamicThreshold   float64 `json:"dynamic_threshold"`
	EffectiveThreshold float64 `json:"effective_threshold"`
}

// HardFilterResult is the result of the reversal-risk hard filter checks.
type HardFilterResult struct {
	Blocked bool   `json:"blocked"`
	Reason  string `json:"reason"`
}

// HigherTimeframeAlignment is the result of checking higher-timeframe trend alignment.
type HigherTimeframeAlignment struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason"`
}

// VoteSummary holds indicator vote totals and the extracted values used by strategy outputs.
// Values always contains every key; missing indicators are nil (JSON null), as in Python.
type VoteSummary struct {
	Votes      int            `json:"votes"`
	Available  int            `json:"available"`
	Supporting int            `json:"supporting"`
	Opposing   int            `json:"opposing"`
	Values     map[string]any `json:"values"`
}

// ConfidenceBreakdown holds confidence components before learner or LLM adjustment.
type ConfidenceBreakdown struct {
	Base           float64 `json:"base"`
	IndicatorBonus float64 `json:"indicator_bonus"`
	ATRPenalty     float64 `json:"atr_penalty"`
	Confidence     float64 `json:"confidence"`
}

// usablePrice reports whether a price is finite and positive.
func usablePrice(price float64) bool {
	return price > 0 && !math.IsInf(price, 0) && !math.IsNaN(price)
}

// cleanPriceHistory returns the finite positive prices from a history sample.
func cleanPriceHistory(history []float64) []float64 {
	clean := make([]float64, 0, len(history))
	for _, price := range history {
		if usablePrice(price) {
			clean = append(clean, price)
		}
	}
	return clean
}

// windowAverage returns the arithmetic mean of a non-empty price window.
func windowAverage(prices []float64) float64 {
	total := 0.0
	for _, p := range prices {
		total += p
	}
	return total / float64(len(prices))
}

// CalculateMomentum returns decimal price momentum, preferring averaged windows
// of recent history and falling back to current vs previous price.
// ok is false when no usable baseline exists (Python returns None).
func CalculateMomentum(currentPrice float64, previousPrice *float64, priceHistory []float64) (float64, bool) {
	clean := cleanPriceHistory(priceHistory)
	required := MomentumWindowPoints * 2
	if len(clean) >= required {
		baseline := clean[len(clean)-required : len(clean)-MomentumWindowPoints]
		recent := clean[len(clean)-MomentumWindowPoints:]
		baselineAvg := windowAverage(baseline)
		if baselineAvg <= 0 {
			return 0, false
		}
		return (windowAverage(recent) - baselineAvg) / baselineAvg, true
	}

	if previousPrice == nil || !usablePrice(currentPrice) || !usablePrice(*previousPrice) {
		return 0, false
	}
	return (currentPrice - *previousPrice) / *previousPrice, true
}

// EvaluateMomentumGate reports whether momentum exceeds the larger of the
// fixed floor and the ATR-derived noise threshold. Pass atrPct=0 when unknown.
func EvaluateMomentumGate(momentum, atrPct float64) MomentumGate {
	dynamic := (atrPct / 100) * MomentumATRMultiplier
	effective := math.Max(dynamic, MomentumThreshold)
	return MomentumGate{
		Passes:             math.Abs(momentum) >= effective,
		DynamicThreshold:   dynamic,
		EffectiveThreshold: effective,
	}
}

// groupNum reads a numeric value from a nested indicator group such as bb.position.
func groupNum(indicators analysis.Indicators, group, key string) (float64, bool) {
	g, ok := indicators.Group(group)
	if !ok {
		return 0, false
	}
	return analysis.Num(g, key)
}

// groupStr reads a string value from a nested indicator group such as macd.bias.
func groupStr(indicators analysis.Indicators, group, key string) (string, bool) {
	g, ok := indicators.Group(group)
	if !ok {
		return "", false
	}
	v, ok := g[key].(string)
	return v, ok
}

// CheckHardFilters blocks trades into reversal-prone RSI or Bollinger extremes.
func CheckHardFilters(isLong bool, indicators analysis.Indicators) HardFilterResult {
	if rsi, ok := indicators.Num("rsi_14"); ok {
		if isLong && rsi >= RSILongBlock {
			return HardFilterResult{Blocked: true, Reason: fmt.Sprintf("RSI %.1f overbought", rsi)}
		}
		if !isLong && rsi <= RSIShortBlock {
			return HardFilterResult{Blocked: true, Reason: fmt.Sprintf("RSI %.1f oversold", rsi)}
		}
	}

	if bbPos, ok := groupNum(indicators, "bb", "position"); ok {
		if isLong && bbPos >= BBLongBlock {
			return HardFilterResult{Blocked: true, Reason: fmt.Sprintf("BB %.1f%% upper extreme", bbPos)}
		}
		if !isLong && bbPos <= BBShortBlock {
			return HardFilterResult{Blocked: true, Reason: fmt.Sprintf("BB %.1f%% lower extreme", bbPos)}
		}
	}

	return HardFilterResult{}
}

// CheckHigherTimeframeAlignment blocks only when the higher-timeframe EMA
// cross opposes the proposed direction; missing or neutral data is allowed.
func CheckHigherTimeframeAlignment(isLong bool, higherTimeframe analysis.Indicators) HigherTimeframeAlignment {
	cross, _ := higherTimeframe.Str("ema_cross")
	if cross == "" || cross == "neutral" {
		return HigherTimeframeAlignment{Allowed: true}
	}
	if (isLong && cross == "bullish") || (!isLong && cross == "bearish") {
		return HigherTimeframeAlignment{Allowed: true}
	}
	return HigherTimeframeAlignment{Allowed: false, Reason: "higher-timeframe EMA " + cross}
}

// castVote counts one vote when an indicator has a directional opinion.
func castVote(s *VoteSummary, isLong, longCondition, shortCondition bool) {
	if !longCondition && !shortCondition {
		return
	}
	s.Available++
	if (isLong && longCondition) || (!isLong && shortCondition) {
		s.Votes++
		s.Supporting++
	} else {
		s.Votes--
		s.Opposing++
	}
}

// optionalNum returns v as any when ok, else nil (JSON null).
func optionalNum(v float64, ok bool) any {
	if !ok {
		return nil
	}
	return v
}

// optionalStr returns v as any when ok, else nil (JSON null).
func optionalStr(v string, ok bool) any {
	if !ok {
		return nil
	}
	return v
}

// VoteIndicators counts indicator votes for the proposed direction using the
// shared thresholds, and extracts the values shown in strategy outputs.
func VoteIndicators(isLong bool, indicators analysis.Indicators) VoteSummary {
	s := VoteSummary{}

	rsi, okRSI := indicators.Num("rsi_14")
	if okRSI {
		castVote(&s, isLong, rsi < RSIBullishThreshold, rsi > RSIBearishThreshold)
	}

	emaCross, okEMA := indicators.Str("ema_cross")
	if emaCross != "" {
		castVote(&s, isLong, emaCross == "bullish", emaCross == "bearish")
	}

	bbPos, okBB := groupNum(indicators, "bb", "position")
	if okBB {
		castVote(&s, isLong, bbPos < BBBullishThreshold, bbPos > BBBearishThreshold)
	}

	macdBias, okMACDBias := groupStr(indicators, "macd", "bias")
	if macdBias != "" {
		castVote(&s, isLong, macdBias == "bullish", macdBias == "bearish")
	}

	sigBias, okSigBias := groupStr(indicators, "macd", "signal_bias")
	if sigBias != "" {
		castVote(&s, isLong, sigBias == "bullish", sigBias == "bearish")
	}

	stochK, okK := groupNum(indicators, "stoch", "k")
	if okK {
		castVote(&s, isLong, stochK < StochBullishThreshold, stochK > StochBearishThreshold)
	}

	wr, okWR := indicators.Num("williams_r")
	if okWR {
		castVote(&s, isLong, wr <= WilliamsBullishThreshold, wr >= WilliamsBearishThreshold)
	}

	chg5m, ok5m := groupNum(indicators, "price_changes", "5m")
	if ok5m {
		castVote(&s, isLong, chg5m > 0, chg5m < 0)
	}

	chg15m, ok15m := groupNum(indicators, "price_changes", "15m")
	if ok15m {
		castVote(&s, isLong, chg15m > 0, chg15m < 0)
	}

	// The candle-based basic strategies replace the 5m/15m changes with
	// changes over 10 and 30 candles (analysis.CandleChanges).
	candleChanges := map[string]any{}
	for _, n := range analysis.CandleChangeSpans {
		key := strconv.Itoa(n) + "_candles"
		if chg, ok := groupNum(indicators, "price_changes", key); ok {
			castVote(&s, isLong, chg > 0, chg < 0)
			candleChanges["price_change_"+key] = chg
		}
	}

	histogram, okHist := groupNum(indicators, "macd", "histogram")
	stochD, okD := groupNum(indicators, "stoch", "d")
	s.Values = map[string]any{
		"rsi_14":           optionalNum(rsi, okRSI),
		"ema_cross":        optionalStr(emaCross, okEMA),
		"bb_position":      optionalNum(bbPos, okBB),
		"macd_bias":        optionalStr(macdBias, okMACDBias),
		"macd_signal_bias": optionalStr(sigBias, okSigBias),
		"macd_histogram":   optionalNum(histogram, okHist),
		"stoch_k":          optionalNum(stochK, okK),
		"stoch_d":          optionalNum(stochD, okD),
		"williams_r":       optionalNum(wr, okWR),
		"price_change_5m":  optionalNum(chg5m, ok5m),
		"price_change_15m": optionalNum(chg15m, ok15m),
	}
	for k, v := range candleChanges {
		s.Values[k] = v
	}
	return s
}

// CalculateConfidence returns capped base confidence and its components
// before learner or LLM adjustment. Pass atrPct=0 when unknown.
func CalculateConfidence(momentum float64, votes, available int, atrPct float64) ConfidenceBreakdown {
	base := math.Min(math.Abs(momentum)*100, BaseConfidenceCap)

	indicatorBonus := 0.0
	if available > 0 {
		indicatorBonus = math.Max(MinIndicatorBonus, math.Min(MaxIndicatorBonus, float64(votes)*IndicatorVoteWeight))
	}

	atrPenalty := 0.0
	if atrPct > ATRVolatilityThresholdPct {
		extra := (atrPct - ATRVolatilityThresholdPct) * ATRPenaltyPerExtraPct
		atrPenalty = -math.Min(MaxATRPenalty, math.Max(0.0, extra))
	}

	confidence := math.Max(MinConfidence, math.Min(MaxConfidence, base+indicatorBonus+atrPenalty))
	return ConfidenceBreakdown{
		Base:           base,
		IndicatorBonus: indicatorBonus,
		ATRPenalty:     atrPenalty,
		Confidence:     confidence,
	}
}

// CalculatePositionSizing returns the bounded position size after ATR volatility adjustment.
func CalculatePositionSizing(atrPct float64) float64 {
	size := PositionSizingProposal
	if atrPct > 1.0 {
		size *= math.Max(0.25, 1.0/atrPct)
	}
	return math.Max(MinDynamicPositionSize, math.Min(MaxDynamicPositionSize, size))
}
