package strategy

import (
	"context"
	"strings"
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/analysis"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

// evalBasic runs a fresh BasicStrategy over one market.
func evalBasic(market string, data MarketData) []domain.TradeIdea {
	return NewBasic(testSettings()).Evaluate(context.Background(), single(market, data))
}

// TestBasicStrategyGates covers the indicator-only strategy's signal gates.
func TestBasicStrategyGates(t *testing.T) {
	t.Run("GIVEN the basic strategy WHEN created THEN its id matches the UI label and it does not use the LLM", func(t *testing.T) {
		s := NewBasic(testSettings())
		if s.ID() != "basic_strategy" {
			t.Fatalf("id = %q", s.ID())
		}
		if s.UsesLLM() || s.RequiredPriceTicks() != 0 {
			t.Fatalf("basic strategy must not need LLM context")
		}
	})

	t.Run("GIVEN momentum below the noise threshold WHEN evaluated THEN no idea is emitted", func(t *testing.T) {
		ideas := evalBasic("BTC/EUR", MarketData{Price: 100.10, PreviousPrice: 100.0})
		if len(ideas) != 0 {
			t.Fatalf("expected no ideas, got %d", len(ideas))
		}
	})

	t.Run("GIVEN momentum below threshold with bullish indicators WHEN evaluated THEN no idea is emitted", func(t *testing.T) {
		ideas := evalBasic("BTC/EUR", MarketData{Price: 100.1, PreviousPrice: 100.0, Indicators: unitBullishIndicators()})
		if len(ideas) != 0 {
			t.Fatalf("expected no ideas, got %d", len(ideas))
		}
	})

	t.Run("GIVEN momentum above threshold with bullish indicators WHEN evaluated THEN a long idea is emitted", func(t *testing.T) {
		ideas := evalBasic("BTC/EUR", MarketData{Price: 100.5, PreviousPrice: 100.0, Indicators: unitBullishIndicators()})
		if len(ideas) != 1 || ideas[0].Direction != domain.Long {
			t.Fatalf("expected one long idea, got %+v", ideas)
		}
	})

	t.Run("GIVEN overbought RSI WHEN a long signal forms THEN the hard filter blocks it", func(t *testing.T) {
		ind := unitBullishIndicators()
		ind["rsi_14"] = 82.0
		if ideas := evalBasic("BTC/EUR", MarketData{Price: 100.5, PreviousPrice: 100.0, Indicators: ind}); len(ideas) != 0 {
			t.Fatalf("expected RSI hard block")
		}
		onlyRSI := analysis.Indicators{"rsi_14": 85.0}
		if ideas := evalBasic("BTC/EUR", MarketData{Price: 101.0, PreviousPrice: 100.0, Indicators: onlyRSI}); len(ideas) != 0 {
			t.Fatalf("expected RSI hard block")
		}
	})

	t.Run("GIVEN oversold RSI WHEN a short signal forms THEN the hard filter blocks it", func(t *testing.T) {
		ind := bearishIndicators()
		ind["rsi_14"] = 18.0
		if ideas := evalBasic("BTC/EUR", MarketData{Price: 99.5, PreviousPrice: 100.0, Indicators: ind}); len(ideas) != 0 {
			t.Fatalf("expected RSI short hard block")
		}
	})

	t.Run("GIVEN BB at the upper extreme WHEN a long signal forms THEN it is blocked", func(t *testing.T) {
		ind := unitBullishIndicators()
		ind["bb"] = map[string]any{"position": 97.0}
		if ideas := evalBasic("BTC/EUR", MarketData{Price: 100.5, PreviousPrice: 100.0, Indicators: ind}); len(ideas) != 0 {
			t.Fatalf("expected BB upper block")
		}
	})

	t.Run("GIVEN BB at the lower extreme WHEN a short signal forms THEN it is blocked", func(t *testing.T) {
		ind := bearishIndicators()
		ind["bb"] = map[string]any{"position": 3.0}
		if ideas := evalBasic("BTC/EUR", MarketData{Price: 99.5, PreviousPrice: 100.0, Indicators: ind}); len(ideas) != 0 {
			t.Fatalf("expected BB lower block")
		}
	})

	t.Run("GIVEN only four supporting indicators WHEN evaluated THEN no idea is emitted", func(t *testing.T) {
		ind := analysis.Indicators{
			"rsi_14":    30.0,
			"ema_cross": "bullish",
			"bb":        map[string]any{"position": 20.0},
			"macd":      map[string]any{"bias": "bullish"},
		}
		if ideas := evalBasic("BTC/EUR", MarketData{Price: 100.5, PreviousPrice: 100.0, Indicators: ind}); len(ideas) != 0 {
			t.Fatalf("expected support gate to block")
		}
	})

	t.Run("GIVEN exactly five supporting indicators WHEN evaluated THEN an idea is emitted", func(t *testing.T) {
		ind := analysis.Indicators{
			"rsi_14":    30.0,
			"ema_cross": "bullish",
			"bb":        map[string]any{"position": 20.0},
			"macd":      map[string]any{"bias": "bullish", "signal_bias": "bullish"},
			"stoch":     map[string]any{"k": 15.0},
		}
		if ideas := evalBasic("BTC/EUR", MarketData{Price: 100.5, PreviousPrice: 100.0, Indicators: ind}); len(ideas) != 1 {
			t.Fatalf("expected one idea, got %d", len(ideas))
		}
	})

	t.Run("GIVEN four indicators with a neutral BB WHEN evaluated THEN no idea is emitted", func(t *testing.T) {
		ind := analysis.Indicators{
			"rsi_14":    35.0,
			"ema_cross": "bullish",
			"bb":        map[string]any{"position": 50.0},
			"macd":      map[string]any{"bias": "bullish"},
			"stoch":     map[string]any{"k": 15.0, "d": 20.0},
		}
		if ideas := evalBasic("BTC/EUR", MarketData{Price: 102.0, PreviousPrice: 100.0, Indicators: ind}); len(ideas) != 0 {
			t.Fatalf("expected no ideas")
		}
	})

	t.Run("GIVEN five trend indicators in a trending market WHEN contrarian indicators oppose THEN a signal is still generated", func(t *testing.T) {
		ind := analysis.Indicators{
			"rsi_14":        76.6,
			"ema_cross":     "bullish",
			"bb":            map[string]any{"position": 80.0},
			"macd":          map[string]any{"bias": "bullish", "signal_bias": "bullish", "histogram": 0.5},
			"stoch":         map[string]any{"k": 93.2, "d": 90.0},
			"williams_r":    -0.0,
			"price_changes": map[string]any{"5m": 1.2, "15m": 2.4},
			"atr_pct":       0.5,
		}
		ideas := evalBasic("AXS/EUR", MarketData{Price: 102.0, PreviousPrice: 100.0, Indicators: ind})
		if len(ideas) != 1 {
			t.Fatalf("expected one idea, got %d", len(ideas))
		}
		if ideas[0].SupportingSignals["indicators_supporting"] != 5 || ideas[0].SupportingSignals["indicators_opposing"] != 4 {
			t.Fatalf("unexpected vote counts: %+v", ideas[0].SupportingSignals)
		}
	})

	t.Run("GIVEN exactly six supporting indicators WHEN more indicators oppose THEN the signal can pass", func(t *testing.T) {
		ind := analysis.Indicators{
			"rsi_14":        38.0,
			"ema_cross":     "bullish",
			"bb":            map[string]any{"position": 80.0},
			"macd":          map[string]any{"bias": "bullish", "signal_bias": "bullish", "histogram": 0.5},
			"stoch":         map[string]any{"k": 90.0, "d": 88.0},
			"williams_r":    -10.0,
			"price_changes": map[string]any{"5m": 1.2, "15m": 2.4},
			"atr_pct":       0.5,
		}
		ideas := evalBasic("AXS/EUR", MarketData{Price: 102.0, PreviousPrice: 100.0, Indicators: ind})
		if len(ideas) != 1 {
			t.Fatalf("expected one idea, got %d", len(ideas))
		}
		if ideas[0].SupportingSignals["indicators_supporting"] != 6 || ideas[0].SupportingSignals["indicators_opposing"] != 3 {
			t.Fatalf("unexpected vote counts: %+v", ideas[0].SupportingSignals)
		}
	})

	t.Run("GIVEN a supportive bullish context WHEN evaluated THEN a well-formed long idea is emitted", func(t *testing.T) {
		ideas := evalBasic("BTC/EUR", MarketData{Price: 102.0, PreviousPrice: 100.0, Indicators: bullishIndicators()})
		if len(ideas) != 1 {
			t.Fatalf("expected one idea, got %d", len(ideas))
		}
		idea := ideas[0]
		if idea.Market != "BTC/EUR" || idea.Direction != domain.Long || idea.StrategyID != BasicID {
			t.Fatalf("unexpected idea: %+v", idea)
		}
		if idea.ID == "" || len(idea.ModeEligibility) != 3 {
			t.Fatalf("idea missing id or mode eligibility")
		}
		if v, _ := idea.SupportingSignals["indicator_votes"].(int); v < 1 {
			t.Fatalf("expected positive net votes")
		}
		if v, _ := idea.SupportingSignals["indicators_supporting"].(int); v < 6 {
			t.Fatalf("expected at least six supporting")
		}
		if idea.Confidence < 0.20 || idea.Confidence > 0.95 {
			t.Fatalf("confidence out of range: %v", idea.Confidence)
		}
		if idea.PositionSizingProposal <= 0 {
			t.Fatalf("expected positive sizing")
		}
	})

	t.Run("GIVEN an opposing higher-timeframe EMA WHEN a long forms THEN it is blocked", func(t *testing.T) {
		data := MarketData{Price: 102.0, PreviousPrice: 100.0, Indicators: bullishIndicators(),
			HigherTimeframe: analysis.Indicators{"ema_cross": "bearish"}}
		if ideas := evalBasic("BTC/EUR", data); len(ideas) != 0 {
			t.Fatalf("expected HTF block")
		}
	})

	t.Run("GIVEN a bullish or neutral higher-timeframe EMA WHEN a long forms THEN it is not blocked", func(t *testing.T) {
		for _, cross := range []string{"bullish", "neutral"} {
			data := MarketData{Price: 100.5, PreviousPrice: 100.0, Indicators: unitBullishIndicators(),
				HigherTimeframe: analysis.Indicators{"ema_cross": cross}}
			if ideas := evalBasic("BTC/EUR", data); len(ideas) != 1 {
				t.Fatalf("HTF %s should not block", cross)
			}
		}
	})

	t.Run("GIVEN hostile LLM sentiment WHEN the basic strategy evaluates THEN it is ignored", func(t *testing.T) {
		data := MarketData{Price: 102.0, PreviousPrice: 100.0, LLMSentiment: -0.8, Indicators: bullishIndicators()}
		ideas := evalBasic("BTC/EUR", data)
		if len(ideas) != 1 || ideas[0].Direction != domain.Long {
			t.Fatalf("expected one long idea")
		}
		if v, _ := ideas[0].SupportingSignals["indicators_available"].(int); v < 6 {
			t.Fatalf("expected at least six available indicators")
		}
	})

	t.Run("GIVEN opposing consensus WHEN evaluated THEN no idea is emitted", func(t *testing.T) {
		ind := analysis.Indicators{
			"rsi_14":    65.0,
			"ema_cross": "bearish",
			"bb":        map[string]any{"position": 80.0},
			"macd":      map[string]any{"bias": "bearish", "signal_bias": "bearish"},
		}
		if ideas := evalBasic("ETH/EUR", MarketData{Price: 102.0, PreviousPrice: 100.0, Indicators: ind}); len(ideas) != 0 {
			t.Fatalf("expected no ideas")
		}
	})

	t.Run("GIVEN a short price history WHEN evaluated THEN no signal fires because MACD is absent", func(t *testing.T) {
		prices := make([]float64, 20)
		for i := range prices {
			prices[i] = 100.0 + float64(i)*0.5
		}
		ind := analysis.ComputeAll(prices, 30, nil)
		if _, ok := ind["macd"]; ok {
			t.Fatalf("MACD should be absent with only 20 ticks")
		}
		if ideas := evalBasic("BTC/EUR", MarketData{Price: prices[19], PreviousPrice: prices[0], Indicators: ind}); len(ideas) != 0 {
			t.Fatalf("expected no ideas")
		}
	})

	t.Run("GIVEN a zero previous price WHEN evaluated THEN no idea is emitted and nothing panics", func(t *testing.T) {
		if ideas := evalBasic("BTC/EUR", MarketData{Price: 50000.0, PreviousPrice: 0}); len(ideas) != 0 {
			t.Fatalf("expected no ideas")
		}
	})

	t.Run("GIVEN a large drop from the prior tick WHEN evaluated THEN a short idea is emitted", func(t *testing.T) {
		ind := analysis.Indicators{
			"rsi_14":        75.0,
			"ema_cross":     "bearish",
			"bb":            map[string]any{"position": 85.0},
			"macd":          map[string]any{"bias": "bearish", "signal_bias": "bearish", "histogram": -0.5},
			"stoch":         map[string]any{"k": 90.0, "d": 88.0},
			"williams_r":    -5.0,
			"price_changes": map[string]any{"5m": -3.0, "15m": -5.0},
			"atr_pct":       0.5,
		}
		ideas := evalBasic("BTC/EUR", MarketData{Price: 100.0, PreviousPrice: 110.0, Indicators: ind})
		if len(ideas) != 1 || ideas[0].Direction != domain.Short {
			t.Fatalf("expected one short idea, got %+v", ideas)
		}
	})

	t.Run("GIVEN bearish indicators and negative momentum WHEN evaluated THEN a short idea is emitted", func(t *testing.T) {
		ideas := evalBasic("BTC/EUR", MarketData{Price: 99.5, PreviousPrice: 100.0, Indicators: bearishIndicators()})
		if len(ideas) != 1 || ideas[0].Direction != domain.Short {
			t.Fatalf("expected one short idea")
		}
	})

	t.Run("GIVEN a neutral EMA cross WHEN evaluated THEN EMA does not vote and is not in the thesis", func(t *testing.T) {
		ind := bullishIndicators()
		ind["ema_cross"] = "neutral"
		ideas := evalBasic("BTC/EUR", MarketData{Price: 102.0, PreviousPrice: 100.0, Indicators: ind})
		if len(ideas) != 1 {
			t.Fatalf("expected one idea")
		}
		idea := ideas[0]
		if idea.SupportingSignals["indicators_supporting"] != 8 {
			t.Fatalf("expected 8 supporting, got %v", idea.SupportingSignals["indicators_supporting"])
		}
		if idea.SupportingSignals["ema_cross"] != "neutral" {
			t.Fatalf("ema_cross should be recorded as neutral")
		}
		if strings.Contains(idea.Thesis, "EMA neutral") {
			t.Fatalf("thesis should not mention neutral EMA: %s", idea.Thesis)
		}
	})

	t.Run("GIVEN indicators only WHEN evaluated THEN no news or LLM keys appear in supporting signals", func(t *testing.T) {
		ideas := evalBasic("BTC/EUR", MarketData{Price: 102.0, PreviousPrice: 100.0, Indicators: bullishIndicators()})
		if len(ideas) != 1 {
			t.Fatalf("expected one idea")
		}
		for _, key := range []string{"news_sentiment", "llm_sentiment", "uses_news_sentiment", "uses_llm_sentiment"} {
			if _, ok := ideas[0].SupportingSignals[key]; ok {
				t.Fatalf("unexpected key %s", key)
			}
		}
		for _, key := range []string{"momentum", "current_price", "previous_price", "rsi_14", "macd_histogram", "stoch_d", "atr_pct"} {
			if _, ok := ideas[0].SupportingSignals[key]; !ok {
				t.Fatalf("missing key %s", key)
			}
		}
	})

	t.Run("GIVEN a missing previous price WHEN evaluated THEN no idea is emitted", func(t *testing.T) {
		if ideas := evalBasic("BTC/EUR", MarketData{Price: 100.5}); len(ideas) != 0 {
			t.Fatalf("expected no ideas")
		}
	})

	t.Run("GIVEN several actionable markets WHEN evaluated THEN ideas come back sorted by market", func(t *testing.T) {
		in := Input{MarketData: map[string]MarketData{}}
		for _, m := range []string{"SOL/EUR", "ADA/EUR", "ETH/EUR", "BTC/EUR"} {
			in.MarketData[m] = MarketData{Price: 102.0, PreviousPrice: 100.0, Indicators: bullishIndicators()}
		}
		ideas := NewBasic(testSettings()).Evaluate(context.Background(), in)
		if len(ideas) != 4 {
			t.Fatalf("expected 4 ideas, got %d", len(ideas))
		}
		for i := 1; i < len(ideas); i++ {
			if ideas[i-1].Market > ideas[i].Market {
				t.Fatalf("ideas not sorted by market")
			}
		}
	})
}
