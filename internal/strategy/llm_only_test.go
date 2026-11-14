package strategy

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/analysis"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/llm"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy/calc"
)

// recommendation builds an LLM trade recommendation for tests.
func recommendation(action string, confidence float64, used bool) llm.LLMTradeRecommendation {
	return llm.LLMTradeRecommendation{Action: action, Confidence: confidence, Sentiment: 0.65, Reasoning: "fake reasoning", LLMUsed: used}
}

// manyMarkets returns n markets with simple price data.
func manyMarkets(n int) map[string]MarketData {
	markets := map[string]MarketData{}
	for i := 0; i < n; i++ {
		markets[fmt.Sprintf("COIN%d/EUR", i)] = MarketData{Price: 100.0, PreviousPrice: 99.0}
	}
	return markets
}

// TestLLMOnlyStrategy covers the pure LLM recommendation strategy.
func TestLLMOnlyStrategy(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN the strategy is created WHEN its dispatch flags are read THEN it needs the averaged-momentum history", func(t *testing.T) {
		s := NewLLMOnly(testSettings())
		if s.ID() != "llm_only_strategy" || !s.UsesLLM() {
			t.Fatalf("unexpected id/flags")
		}
		if s.RequiredPriceTicks() != calc.MomentumWindowPoints*2 {
			t.Fatalf("required ticks = %d", s.RequiredPriceTicks())
		}
	})

	t.Run("GIVEN an LLM long recommendation WHEN evaluated THEN a long idea is emitted with the LLM context", func(t *testing.T) {
		indicators := analysis.Indicators{"rsi_14": 72.0, "ema_cross": "bearish"}
		a := &fakeAnalyser{rec: recommendation("long", 0.81, true)}
		in := single("BTC/EUR", MarketData{Price: 100.0, PreviousPrice: 99.0, Indicators: indicators})
		in.Analyser = a
		in.Portfolio = Portfolio{Equity: 500, Cash: 500}

		ideas := NewLLMOnly(testSettings()).Evaluate(ctx, in)
		if len(ideas) != 1 {
			t.Fatalf("expected one idea, got %d", len(ideas))
		}
		idea := ideas[0]
		if idea.StrategyID != LLMOnlyID || idea.Direction != domain.Long || idea.Confidence != 0.81 {
			t.Fatalf("unexpected idea: %+v", idea)
		}
		if idea.SupportingSignals["llm_only"] != true || idea.SupportingSignals["llm_reasoning"] != "fake reasoning" {
			t.Fatalf("missing LLM context: %+v", idea.SupportingSignals)
		}
		if _, ok := idea.SupportingSignals["llm_sentiment"]; !ok {
			t.Fatalf("missing llm_sentiment")
		}
		if !reflect.DeepEqual(a.recCalls[0].Indicators, indicators) {
			t.Fatalf("analyser should receive the market indicators")
		}
		if a.recCalls[0].Equity != 500 || a.recCalls[0].Cash != 500 {
			t.Fatalf("analyser should receive portfolio context")
		}
	})

	t.Run("GIVEN an LLM short recommendation WHEN evaluated THEN a short idea is emitted", func(t *testing.T) {
		in := single("ETH/EUR", MarketData{Price: 100.0, PreviousPrice: 101.0})
		in.Analyser = &fakeAnalyser{rec: recommendation("short", 0.7, true)}
		ideas := NewLLMOnly(testSettings()).Evaluate(ctx, in)
		if len(ideas) != 1 || ideas[0].Direction != domain.Short {
			t.Fatalf("expected one short idea")
		}
	})

	t.Run("GIVEN an LLM hold recommendation WHEN evaluated THEN no idea is emitted", func(t *testing.T) {
		in := single("ETH/EUR", MarketData{Price: 100.0, PreviousPrice: 100.0})
		in.Analyser = &fakeAnalyser{rec: recommendation("hold", 0.9, true)}
		if ideas := NewLLMOnly(testSettings()).Evaluate(ctx, in); len(ideas) != 0 {
			t.Fatalf("expected no ideas")
		}
	})

	t.Run("GIVEN an invalid LLM action WHEN evaluated THEN no idea is emitted", func(t *testing.T) {
		in := single("ETH/EUR", MarketData{Price: 100.0, PreviousPrice: 100.0})
		in.Analyser = &fakeAnalyser{rec: recommendation("buy", 0.9, true)}
		if ideas := NewLLMOnly(testSettings()).Evaluate(ctx, in); len(ideas) != 0 {
			t.Fatalf("expected no ideas")
		}
	})

	t.Run("GIVEN the LLM is unavailable WHEN evaluated THEN no idea is emitted", func(t *testing.T) {
		in := single("SOL/EUR", MarketData{Price: 100.0, PreviousPrice: 98.0})
		in.Analyser = &fakeAnalyser{rec: recommendation("long", 0.9, false)}
		if ideas := NewLLMOnly(testSettings()).Evaluate(ctx, in); len(ideas) != 0 {
			t.Fatalf("expected no ideas")
		}
	})

	t.Run("GIVEN no analyser WHEN evaluated THEN no idea is emitted", func(t *testing.T) {
		in := single("SOL/EUR", MarketData{Price: 100.0, PreviousPrice: 98.0})
		if ideas := NewLLMOnly(testSettings()).Evaluate(ctx, in); len(ideas) != 0 {
			t.Fatalf("expected no ideas")
		}
	})

	t.Run("GIVEN multiple markets WHEN evaluated THEN they are processed concurrently and returned sorted", func(t *testing.T) {
		a := &fakeAnalyser{rec: recommendation("long", 0.7, true), delay: 50 * time.Millisecond}
		in := Input{MarketData: manyMarkets(5), Analyser: a}

		start := time.Now()
		ideas := NewLLMOnly(testSettings()).Evaluate(ctx, in)
		elapsed := time.Since(start)

		if len(ideas) != 5 {
			t.Fatalf("expected 5 ideas, got %d", len(ideas))
		}
		// Sequential would take 5 × 50 ms = 250 ms.
		if elapsed >= 200*time.Millisecond {
			t.Fatalf("evaluation took %v — likely sequential", elapsed)
		}
		for i := 1; i < len(ideas); i++ {
			if ideas[i-1].Market > ideas[i].Market {
				t.Fatalf("ideas not sorted by market")
			}
		}
	})

	t.Run("GIVEN many markets and a concurrency limit WHEN evaluated THEN LLM calls in flight never exceed it", func(t *testing.T) {
		s := testSettings()
		s.LLMOnlyMaxConcurrency = 3
		a := &fakeAnalyser{rec: recommendation("hold", 0.7, true), delay: 20 * time.Millisecond}
		NewLLMOnly(s).Evaluate(ctx, Input{MarketData: manyMarkets(8), Analyser: a})
		if a.maxInFlight > 3 || a.maxInFlight < 1 {
			t.Fatalf("max in flight = %d", a.maxInFlight)
		}
		if len(a.recCalls) != 8 {
			t.Fatalf("expected 8 calls, got %d", len(a.recCalls))
		}
	})

	t.Run("GIVEN a non-positive concurrency setting WHEN the strategy is created THEN at least one call may run", func(t *testing.T) {
		s := testSettings()
		s.LLMOnlyMaxConcurrency = 0
		if NewLLMOnly(s).MaxConcurrency < 1 {
			t.Fatalf("concurrency must be at least 1")
		}
	})
}
