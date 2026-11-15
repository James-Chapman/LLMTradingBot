package strategy

import (
	"context"
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/llm"
)

// stubPrompter is a base strategy that returns fixed ideas and a fixed prompt.
type stubPrompter struct {
	ideas []domain.TradeIdea
}

// ID returns a test id.
func (s *stubPrompter) ID() string { return "stub_strategy" }

// UsesLLM is false: the stub stands for a non-LLM base strategy.
func (s *stubPrompter) UsesLLM() bool { return false }

// RequiredPriceTicks is 0, as for the 4-hour strategies.
func (s *stubPrompter) RequiredPriceTicks() int { return 0 }

// Evaluate returns copies of the configured ideas.
func (s *stubPrompter) Evaluate(context.Context, Input) []domain.TradeIdea {
	return append([]domain.TradeIdea(nil), s.ideas...)
}

// Prompt describes the stub's signal.
func (s *stubPrompter) Prompt(idea domain.TradeIdea, _ MarketData) llm.StrategyPrompt {
	return llm.StrategyPrompt{Name: "Stub", Instructions: "Judge the stub.", Data: map[string]string{"market": idea.Market}}
}

// stubIdea returns a base idea for market in direction dir.
func stubIdea(market string, dir domain.Direction) domain.TradeIdea {
	return domain.TradeIdea{
		ID: "idea", StrategyID: "stub_strategy", Market: market, Direction: dir, Confidence: 0.9,
		Thesis: "stub thesis", SupportingSignals: map[string]any{}, ModeEligibility: allModes(),
	}
}

// llmInput returns an Input for ETH/EUR whose last closed 4-hour candle starts at closedT.
func llmInput(analyser Analyser, closedT int64, positions ...domain.PositionRecord) Input {
	candles := []domain.Candle{{T: closedT, C: 100}, {T: closedT + 4*3600, C: 101}} // closed, forming
	return Input{
		MarketData: map[string]MarketData{"ETH/EUR": {Price: 101, PreviousPrice: 100, TrendCandles: candles}},
		Analyser:   analyser,
		Portfolio:  Portfolio{Equity: 500, Cash: 500, OpenPositions: positions},
	}
}

// TestLLMVersion covers the LLM version of a strategy: the base strategy's
// entry is sent to the LLM with the strategy's own prompt, which scales its
// confidence or vetoes it; exits never wait for the LLM.
func TestLLMVersion(t *testing.T) {
	ctx := context.Background()
	newVersion := func(ideas ...domain.TradeIdea) *LLMVersion {
		return NewLLMVersion(&stubPrompter{ideas: ideas}, "stub_and_llm_strategy", testSettings())
	}

	t.Run("GIVEN an LLM version WHEN created THEN it has its own id and needs LLM context", func(t *testing.T) {
		v := newVersion()
		if v.ID() != "stub_and_llm_strategy" || !v.UsesLLM() {
			t.Fatalf("id %q, uses LLM %v", v.ID(), v.UsesLLM())
		}
		if v.Base().ID() != "stub_strategy" {
			t.Fatalf("base %q", v.Base().ID())
		}
	})

	t.Run("GIVEN a base entry WHEN the LLM supports it THEN its confidence is scaled and the prompt carries the strategy's own text", func(t *testing.T) {
		an := scaleAnalyser(1.05)
		ideas := newVersion(stubIdea("ETH/EUR", domain.Long)).Evaluate(ctx, llmInput(an, 0))
		if len(ideas) != 1 {
			t.Fatalf("got %d ideas, want 1", len(ideas))
		}
		got := ideas[0]
		if got.StrategyID != "stub_and_llm_strategy" {
			t.Fatalf("strategy id %q", got.StrategyID)
		}
		if want := 0.9 * 1.05; got.Confidence < want-1e-9 || got.Confidence > want+1e-9 {
			t.Fatalf("confidence %.4f, want %.4f", got.Confidence, want)
		}
		if got.SupportingSignals["llm_confidence_scale"] != 1.05 {
			t.Fatalf("llm fields %v", got.SupportingSignals)
		}
		if len(an.signalCalls) != 1 || an.signalCalls[0].Strategy == nil || an.signalCalls[0].Strategy.Name != "Stub" {
			t.Fatalf("LLM calls %+v", an.signalCalls)
		}
		if req := an.signalCalls[0]; req.Direction != "long" || req.Equity != 500 || req.BaseConfidence != 0.9 {
			t.Fatalf("request %+v", req)
		}
	})

	t.Run("GIVEN a strong LLM endorsement WHEN scaled THEN confidence is capped at the maximum", func(t *testing.T) {
		ideas := newVersion(stubIdea("ETH/EUR", domain.Long)).Evaluate(ctx, llmInput(scaleAnalyser(2.0), 0))
		if len(ideas) != 1 || ideas[0].Confidence > 0.95 {
			t.Fatalf("ideas %+v, want confidence capped at 0.95", ideas)
		}
	})

	t.Run("GIVEN a base entry WHEN the LLM scale is below the veto threshold THEN the entry is dropped", func(t *testing.T) {
		if ideas := newVersion(stubIdea("ETH/EUR", domain.Long)).Evaluate(ctx, llmInput(scaleAnalyser(0.6), 0)); len(ideas) != 0 {
			t.Fatalf("vetoed entry was kept: %+v", ideas)
		}
	})

	t.Run("GIVEN a base exit WHEN evaluated THEN it is passed on without asking the LLM", func(t *testing.T) {
		an := scaleAnalyser(0.5)
		held := domain.PositionRecord{Market: "ETH/EUR", Size: 1}
		ideas := newVersion(stubIdea("ETH/EUR", domain.Short)).Evaluate(ctx, llmInput(an, 0, held))
		if len(ideas) != 1 || ideas[0].Confidence != 0.9 || ideas[0].StrategyID != "stub_and_llm_strategy" {
			t.Fatalf("exit %+v, want it unchanged under the LLM version's id", ideas)
		}
		if len(an.signalCalls) != 0 {
			t.Fatal("an exit must not wait for the LLM")
		}
	})

	t.Run("GIVEN a long already held WHEN the base repeats its entry THEN the LLM is not asked", func(t *testing.T) {
		an := scaleAnalyser(1.0)
		held := domain.PositionRecord{Market: "ETH/EUR", Size: 0.5}
		ideas := newVersion(stubIdea("ETH/EUR", domain.Long)).Evaluate(ctx, llmInput(an, 0, held))
		if len(an.signalCalls) != 0 {
			t.Fatal("the LLM was asked about a market already held")
		}
		if len(ideas) != 1 {
			t.Fatalf("got %d ideas, want the repeated entry passed on", len(ideas))
		}
	})

	t.Run("GIVEN the same closed 4-hour candle WHEN evaluated again THEN the earlier verdict is reused, and a new candle asks again", func(t *testing.T) {
		an := scaleAnalyser(1.0)
		v := newVersion(stubIdea("ETH/EUR", domain.Long))
		v.Evaluate(ctx, llmInput(an, 0))
		v.Evaluate(ctx, llmInput(an, 0))
		if len(an.signalCalls) != 1 {
			t.Fatalf("%d LLM calls for one candle, want 1", len(an.signalCalls))
		}
		v.Evaluate(ctx, llmInput(an, 4*3600))
		if len(an.signalCalls) != 2 {
			t.Fatalf("%d LLM calls after a new candle, want 2", len(an.signalCalls))
		}
	})

	t.Run("GIVEN a cached veto WHEN the same candle is evaluated again THEN the entry stays vetoed without a new call", func(t *testing.T) {
		an := scaleAnalyser(0.5)
		v := newVersion(stubIdea("ETH/EUR", domain.Long))
		v.Evaluate(ctx, llmInput(an, 0))
		if ideas := v.Evaluate(ctx, llmInput(an, 0)); len(ideas) != 0 || len(an.signalCalls) != 1 {
			t.Fatalf("ideas %d, calls %d: want the veto reused", len(ideas), len(an.signalCalls))
		}
	})

	t.Run("GIVEN the LLM is unavailable WHEN evaluated THEN the entry keeps its base confidence and the LLM is asked again next time", func(t *testing.T) {
		an := &fakeAnalyser{signal: llm.SignalAnalysis{ConfidenceScale: 1.0, LLMUsed: false}}
		v := newVersion(stubIdea("ETH/EUR", domain.Long))
		ideas := v.Evaluate(ctx, llmInput(an, 0))
		if len(ideas) != 1 || ideas[0].Confidence != 0.9 || ideas[0].SupportingSignals["llm_confidence_scale"] != nil {
			t.Fatalf("ideas %+v, want the base entry unchanged", ideas)
		}
		v.Evaluate(ctx, llmInput(an, 0))
		if len(an.signalCalls) != 2 {
			t.Fatalf("%d calls, want an unavailable LLM retried", len(an.signalCalls))
		}
	})

	t.Run("GIVEN no analyser WHEN evaluated THEN the entry keeps its base confidence", func(t *testing.T) {
		ideas := newVersion(stubIdea("ETH/EUR", domain.Long)).Evaluate(ctx, llmInput(nil, 0))
		if len(ideas) != 1 || ideas[0].Confidence != 0.9 {
			t.Fatalf("ideas %+v", ideas)
		}
	})
}
