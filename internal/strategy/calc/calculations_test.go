package calc

import (
	"math"
	"strings"
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/analysis"
)

// ptr returns a pointer to v.
func ptr(v float64) *float64 {
	return &v
}

// bullishIndicators returns a snapshot where every indicator supports a long.
func bullishIndicators() analysis.Indicators {
	return analysis.Indicators{
		"rsi_14":        35.0,
		"ema_cross":     "bullish",
		"bb":            map[string]any{"position": 25.0},
		"macd":          map[string]any{"bias": "bullish", "signal_bias": "bullish", "histogram": 0.5},
		"stoch":         map[string]any{"k": 15.0, "d": 20.0},
		"williams_r":    -85.0,
		"price_changes": map[string]any{"5m": 1.2, "15m": 2.4},
	}
}

// TestCalculateMomentum covers two-point and averaged-window momentum.
func TestCalculateMomentum(t *testing.T) {
	t.Run("GIVEN current and previous prices WHEN momentum is calculated THEN the decimal change is returned", func(t *testing.T) {
		if got, ok := CalculateMomentum(102.0, ptr(100.0), nil); !ok || got != 0.02 {
			t.Fatalf("expected 0.02, got %v (ok=%v)", got, ok)
		}
		if got, ok := CalculateMomentum(100.0, ptr(110.0), nil); !ok || math.Abs(got+0.090909) > 1e-6 {
			t.Fatalf("expected ~-0.090909, got %v", got)
		}
	})

	t.Run("GIVEN an unusable previous price WHEN momentum is calculated THEN it is unavailable", func(t *testing.T) {
		for _, prev := range []*float64{nil, ptr(0), ptr(-1), ptr(math.NaN()), ptr(math.Inf(1))} {
			if _, ok := CalculateMomentum(100.0, prev, nil); ok {
				t.Fatalf("expected unavailable for previous=%v", prev)
			}
		}
	})

	t.Run("GIVEN an unusable current price WHEN momentum is calculated THEN it is unavailable", func(t *testing.T) {
		if _, ok := CalculateMomentum(0, ptr(100), nil); ok {
			t.Fatal("expected unavailable")
		}
	})

	t.Run("GIVEN enough history WHEN momentum is calculated THEN averaged windows are used", func(t *testing.T) {
		history := []float64{100.0, 101.0, 99.0, 100.5, 99.5, 101.0, 102.0, 100.0, 101.5, 100.5}
		got, ok := CalculateMomentum(103.0, ptr(100.0), history)
		if !ok || math.Abs(got-0.01) > 1e-6 {
			t.Fatalf("expected ~0.01, got %v", got)
		}
	})

	t.Run("GIVEN history with invalid points WHEN momentum is calculated THEN they are ignored", func(t *testing.T) {
		history := []float64{0, math.NaN(), 100.0, 101.0, 99.0, 100.5, 99.5, -5, 101.0, 102.0, 100.0, 101.5, 100.5}
		got, ok := CalculateMomentum(103.0, nil, history)
		if !ok || math.Abs(got-0.01) > 1e-6 {
			t.Fatalf("expected ~0.01, got %v (ok=%v)", got, ok)
		}
	})

	t.Run("GIVEN short history WHEN momentum is calculated THEN the two-point change is returned", func(t *testing.T) {
		got, ok := CalculateMomentum(103.0, ptr(100.0), []float64{100.0, 101.0, 103.0})
		if !ok || got != 0.03 {
			t.Fatalf("expected 0.03, got %v", got)
		}
	})

	t.Run("GIVEN the window constant WHEN read THEN it is five points", func(t *testing.T) {
		if MomentumWindowPoints != 5 {
			t.Fatalf("expected 5, got %d", MomentumWindowPoints)
		}
	})
}

// TestEvaluateMomentumGate covers the fixed and ATR-derived threshold.
func TestEvaluateMomentumGate(t *testing.T) {
	t.Run("GIVEN ATR volatility WHEN the gate is evaluated THEN the larger threshold applies", func(t *testing.T) {
		low := EvaluateMomentumGate(0.003, 0.05)
		high := EvaluateMomentumGate(0.003, 0.5)
		if !low.Passes || low.EffectiveThreshold != 0.002 {
			t.Fatalf("unexpected low-ATR gate: %+v", low)
		}
		if high.Passes || high.EffectiveThreshold != 0.005 {
			t.Fatalf("unexpected high-ATR gate: %+v", high)
		}
	})

	t.Run("GIVEN a 0.1% move WHEN the gate is evaluated THEN it does not pass", func(t *testing.T) {
		if EvaluateMomentumGate(0.001, 0).Passes {
			t.Fatal("expected gate to block")
		}
	})

	t.Run("GIVEN negative momentum beyond the floor WHEN the gate is evaluated THEN it passes", func(t *testing.T) {
		if !EvaluateMomentumGate(-0.005, 0).Passes {
			t.Fatal("expected gate to pass")
		}
	})
}

// TestCheckHardFilters covers RSI and BB reversal blocks.
func TestCheckHardFilters(t *testing.T) {
	t.Run("GIVEN extreme readings WHEN filters are checked THEN the trade is blocked with a reason", func(t *testing.T) {
		longBlock := CheckHardFilters(true, analysis.Indicators{"rsi_14": 82.0})
		shortRSI := CheckHardFilters(false, analysis.Indicators{"rsi_14": 15.0})
		shortBB := CheckHardFilters(false, analysis.Indicators{"bb": map[string]any{"position": 3.0}})
		longBB := CheckHardFilters(true, analysis.Indicators{"bb": map[string]any{"position": 97.0}})

		if !longBlock.Blocked || !strings.Contains(longBlock.Reason, "RSI") {
			t.Fatalf("unexpected: %+v", longBlock)
		}
		if !shortRSI.Blocked || !strings.Contains(shortRSI.Reason, "oversold") {
			t.Fatalf("unexpected: %+v", shortRSI)
		}
		if !shortBB.Blocked || !strings.Contains(shortBB.Reason, "BB") {
			t.Fatalf("unexpected: %+v", shortBB)
		}
		if !longBB.Blocked || !strings.Contains(longBB.Reason, "upper") {
			t.Fatalf("unexpected: %+v", longBB)
		}
	})

	t.Run("GIVEN moderate readings WHEN filters are checked THEN the trade is allowed", func(t *testing.T) {
		clean := CheckHardFilters(true, analysis.Indicators{"rsi_14": 35.0, "bb": map[string]any{"position": 25.0}})
		if clean.Blocked || clean.Reason != "" {
			t.Fatalf("unexpected: %+v", clean)
		}
	})

	t.Run("GIVEN no indicators WHEN filters are checked THEN the trade is allowed", func(t *testing.T) {
		if CheckHardFilters(true, nil).Blocked {
			t.Fatal("expected not blocked")
		}
	})
}

// TestCheckHigherTimeframeAlignment covers the HTF EMA filter.
func TestCheckHigherTimeframeAlignment(t *testing.T) {
	t.Run("GIVEN a higher-timeframe EMA WHEN alignment is checked THEN only opposition blocks", func(t *testing.T) {
		blocked := CheckHigherTimeframeAlignment(true, analysis.Indicators{"ema_cross": "bearish"})
		if blocked.Allowed || !strings.Contains(blocked.Reason, "bearish") {
			t.Fatalf("unexpected: %+v", blocked)
		}
		if !CheckHigherTimeframeAlignment(true, analysis.Indicators{"ema_cross": "neutral"}).Allowed {
			t.Fatal("neutral should be allowed")
		}
		if !CheckHigherTimeframeAlignment(true, analysis.Indicators{"ema_cross": "bullish"}).Allowed {
			t.Fatal("bullish long should be allowed")
		}
		if !CheckHigherTimeframeAlignment(false, analysis.Indicators{"ema_cross": "bearish"}).Allowed {
			t.Fatal("bearish short should be allowed")
		}
		if !CheckHigherTimeframeAlignment(true, nil).Allowed {
			t.Fatal("missing HTF data should be allowed")
		}
	})
}

// TestVoteIndicators covers indicator vote counting.
func TestVoteIndicators(t *testing.T) {
	t.Run("GIVEN all bullish indicators WHEN votes are counted for a long THEN all nine support", func(t *testing.T) {
		s := VoteIndicators(true, bullishIndicators())
		if s.Votes != 9 || s.Available != 9 || s.Supporting != 9 || s.Opposing != 0 {
			t.Fatalf("unexpected summary: %+v", s)
		}
		if s.Values["macd_signal_bias"] != "bullish" {
			t.Fatalf("unexpected values: %v", s.Values)
		}
	})

	t.Run("GIVEN all bullish indicators WHEN votes are counted for a short THEN all nine oppose", func(t *testing.T) {
		s := VoteIndicators(false, bullishIndicators())
		if s.Votes != -9 || s.Available != 9 || s.Supporting != 0 || s.Opposing != 9 {
			t.Fatalf("unexpected summary: %+v", s)
		}
	})

	t.Run("GIVEN only four bullish indicators WHEN votes are counted THEN fewer than the signal minimum support", func(t *testing.T) {
		ind := analysis.Indicators{
			"rsi_14":    30.0,
			"ema_cross": "bullish",
			"bb":        map[string]any{"position": 20.0},
			"macd":      map[string]any{"bias": "bullish"},
		}
		s := VoteIndicators(true, ind)
		if s.Supporting >= MinIndicatorsForSignal {
			t.Fatalf("expected fewer than %d supporting, got %+v", MinIndicatorsForSignal, s)
		}
	})

	t.Run("GIVEN neutral indicators WHEN votes are counted THEN they are not available", func(t *testing.T) {
		ind := analysis.Indicators{"rsi_14": 50.0, "ema_cross": "neutral", "price_changes": map[string]any{"5m": 0.0}}
		s := VoteIndicators(true, ind)
		if s.Available != 0 || s.Votes != 0 {
			t.Fatalf("unexpected summary: %+v", s)
		}
	})

	t.Run("GIVEN missing indicators WHEN votes are counted THEN every value key is present and nil", func(t *testing.T) {
		s := VoteIndicators(true, analysis.Indicators{})
		for _, key := range []string{"rsi_14", "ema_cross", "bb_position", "macd_bias", "macd_signal_bias",
			"macd_histogram", "stoch_k", "stoch_d", "williams_r", "price_change_5m", "price_change_15m"} {
			v, present := s.Values[key]
			if !present || v != nil {
				t.Fatalf("expected %q present and nil, got %v (present=%v)", key, v, present)
			}
		}
	})
}

// TestCalculateConfidence covers capped confidence components.
func TestCalculateConfidence(t *testing.T) {
	t.Run("GIVEN momentum, votes and ATR WHEN confidence is calculated THEN shared caps apply", func(t *testing.T) {
		b := CalculateConfidence(0.02, 9, 9, 1.5)
		if b.Base != 0.50 || b.IndicatorBonus != 0.40 || b.ATRPenalty != -0.025 || b.Confidence != 0.875 {
			t.Fatalf("unexpected breakdown: %+v", b)
		}
	})

	t.Run("GIVEN no available indicators WHEN confidence is calculated THEN no bonus applies", func(t *testing.T) {
		b := CalculateConfidence(0.001, -9, 0, 0)
		if b.IndicatorBonus != 0 || b.Confidence < MinConfidence {
			t.Fatalf("unexpected breakdown: %+v", b)
		}
	})

	t.Run("GIVEN extreme inputs WHEN confidence is calculated THEN it stays within bounds", func(t *testing.T) {
		low := CalculateConfidence(0, -20, 20, 50)
		high := CalculateConfidence(1, 20, 20, 0)
		if low.Confidence != MinConfidence || high.Confidence > MaxConfidence {
			t.Fatalf("unexpected bounds: low=%+v high=%+v", low, high)
		}
		if low.ATRPenalty != -MaxATRPenalty || low.IndicatorBonus != MinIndicatorBonus {
			t.Fatalf("unexpected caps: %+v", low)
		}
	})
}

// TestCalculatePositionSizing covers ATR-adjusted sizing bounds.
func TestCalculatePositionSizing(t *testing.T) {
	t.Run("GIVEN volatility changes WHEN sizing is calculated THEN the size is bounded", func(t *testing.T) {
		if got := CalculatePositionSizing(0.5); got != 0.20 {
			t.Fatalf("expected 0.20, got %v", got)
		}
		if got := CalculatePositionSizing(2.0); got != 0.10 {
			t.Fatalf("expected 0.10, got %v", got)
		}
		if got := CalculatePositionSizing(20.0); got != 0.05 {
			t.Fatalf("expected 0.05, got %v", got)
		}
	})
}
