package strategy

import (
	"context"
	"strings"
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/llm"
)

// TestBasicAndLLMStrategy covers indicator consensus plus LLM validation.
func TestBasicAndLLMStrategy(t *testing.T) {
	ctx := context.Background()
	bullish := MarketData{Price: 102.0, PreviousPrice: 100.0, Indicators: bullishIndicators()}

	t.Run("GIVEN the strategy is created WHEN its id and dispatch flags are read THEN it is an LLM strategy", func(t *testing.T) {
		s := NewBasicAndLLM1h(testSettings())
		if s.ID() != "basic_and_llm_strategy_1h" {
			t.Fatalf("id = %q", s.ID())
		}
		if !s.UsesLLM() {
			t.Fatalf("expected UsesLLM")
		}
		if s.RequiredPriceTicks() != 0 { // it waits for its own candle warm-up
			t.Fatalf("required ticks = %d", s.RequiredPriceTicks())
		}
	})

	t.Run("GIVEN momentum below threshold WHEN evaluated THEN no signal and the LLM is not called", func(t *testing.T) {
		a := scaleAnalyser(1.0)
		in := single("BTC/EUR", MarketData{Price: 100.10, PreviousPrice: 100.0})
		in.Analyser = a
		if ideas := rawBasicAndLLM().Evaluate(ctx, in); len(ideas) != 0 {
			t.Fatalf("expected no ideas")
		}
		if len(a.signalCalls) != 0 {
			t.Fatalf("LLM should not be called for gated signals")
		}
	})

	t.Run("GIVEN an LLM scale below the veto threshold WHEN evaluated THEN the signal is blocked inside the strategy", func(t *testing.T) {
		in := single("BTC/EUR", bullish)
		in.Analyser = scaleAnalyser(0.5)
		if ideas := rawBasicAndLLM().Evaluate(ctx, in); len(ideas) != 0 {
			t.Fatalf("expected LLM veto")
		}
	})

	t.Run("GIVEN a supportive LLM WHEN evaluated THEN confidence is scaled and LLM context is recorded", func(t *testing.T) {
		a := scaleAnalyser(1.2)
		a.signal.Sentiment = 0.4
		a.signal.Reasoning = "news supports"
		in := single("BTC/EUR", bullish)
		in.Analyser = a
		in.Portfolio = Portfolio{Equity: 600, Cash: 400}
		in.News = []llm.NewsArticle{{ID: "n1", Title: "Bitcoin rallies"}}

		base := rawBasic().Evaluate(ctx, single("BTC/EUR", bullish))
		ideas := rawBasicAndLLM().Evaluate(ctx, in)
		if len(ideas) != 1 || len(base) != 1 {
			t.Fatalf("expected one idea from each strategy")
		}
		idea := ideas[0]
		if idea.StrategyID != BasicAndLLM1hID || idea.Direction != domain.Long {
			t.Fatalf("unexpected idea: %+v", idea)
		}
		if idea.Confidence <= base[0].Confidence || idea.Confidence > 0.95 {
			t.Fatalf("expected scaled-up, capped confidence: base %v final %v", base[0].Confidence, idea.Confidence)
		}
		if idea.SupportingSignals["llm_confidence_scale"] == nil || idea.SupportingSignals["llm_sentiment"] == nil {
			t.Fatalf("expected LLM context in supporting signals: %+v", idea.SupportingSignals)
		}
		if idea.SupportingSignals["llm_reasoning"] != "news supports" {
			t.Fatalf("expected reasoning recorded")
		}
		if !strings.Contains(idea.Thesis, "LLM: news supports") {
			t.Fatalf("thesis should carry LLM reasoning: %s", idea.Thesis)
		}

		req := a.signalCalls[0]
		if req.Market != "BTC/EUR" || req.Direction != "long" || req.Equity != 600 || req.Cash != 400 || len(req.News) != 1 {
			t.Fatalf("analyser received wrong context: %+v", req)
		}
		if req.MomentumPct <= 0 || req.BaseConfidence <= 0 || req.CurrentPrice != 102.0 {
			t.Fatalf("analyser received wrong signal values: %+v", req)
		}
	})

	t.Run("GIVEN the LLM was not used WHEN evaluated THEN the idea passes with nil LLM fields", func(t *testing.T) {
		in := single("BTC/EUR", bullish)
		in.Analyser = &fakeAnalyser{signal: llm.SignalAnalysis{ConfidenceScale: 1.0, LLMUsed: false, Reasoning: "unavailable"}}
		ideas := rawBasicAndLLM().Evaluate(ctx, in)
		if len(ideas) != 1 {
			t.Fatalf("expected one idea")
		}
		for _, key := range []string{"llm_sentiment", "llm_confidence_scale", "llm_reasoning"} {
			v, ok := ideas[0].SupportingSignals[key]
			if !ok || v != nil {
				t.Fatalf("%s should be present and nil, got %v (present=%v)", key, v, ok)
			}
		}
		if strings.Contains(ideas[0].Thesis, "LLM:") {
			t.Fatalf("thesis should not mention the LLM when unused")
		}
	})

	t.Run("GIVEN no analyser WHEN evaluated THEN the strategy behaves as with an unavailable LLM", func(t *testing.T) {
		ideas := rawBasicAndLLM().Evaluate(ctx, single("BTC/EUR", bullish))
		if len(ideas) != 1 || ideas[0].SupportingSignals["llm_confidence_scale"] != nil {
			t.Fatalf("expected neutral behaviour without an analyser")
		}
	})
}
