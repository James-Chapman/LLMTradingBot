package strategy

import (
	"context"
	"math"
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/llm"
)

// basicAndLLMWeighted returns basic_and_llm_strategy_1h with LLM_WEIGHT set to weight.
func basicAndLLMWeighted(weight float64) *BasicAndLLMStrategy {
	s := rulesOff()
	s.LLMWeight = weight
	b := NewBasicAndLLM1h(s)
	asView(&b.basicCore)
	return b
}

// llmVersionWeighted returns an LLM version of a stub strategy with LLM_WEIGHT set to weight.
func llmVersionWeighted(weight float64, ideas ...domain.TradeIdea) *LLMVersion {
	s := testSettings()
	s.LLMWeight = weight
	return NewLLMVersion(&stubPrompter{ideas: ideas}, "stub_and_llm_strategy", s)
}

// floatSignal reads a numeric supporting signal, failing the test when it is missing.
func floatSignal(t *testing.T, idea domain.TradeIdea, key string) float64 {
	t.Helper()
	v, ok := idea.SupportingSignals[key].(float64)
	if !ok {
		t.Fatalf("supporting signal %s = %v, want a number", key, idea.SupportingSignals[key])
	}
	return v
}

// llmShift is how far the LLM moved an idea's confidence from its pre-LLM value.
func llmShift(t *testing.T, idea domain.TradeIdea) float64 {
	t.Helper()
	return idea.Confidence - floatSignal(t, idea, "pre_llm_confidence")
}

// TestLLMWeight covers LLM_WEIGHT: how much of the LLM's confidence scale is
// applied to an entry's score. The veto always uses the LLM's raw scale.
func TestLLMWeight(t *testing.T) {
	ctx := context.Background()
	bullish := MarketData{Price: 102.0, PreviousPrice: 100.0, Indicators: bullishIndicators()}

	t.Run("GIVEN the default settings WHEN the LLM weight is read THEN the LLM has its full effect", func(t *testing.T) {
		if w := testSettings().LLMWeight; w != 1 {
			t.Fatalf("default LLM weight %v, want 1 (full effect)", w)
		}
	})

	t.Run("GIVEN half LLM weight WHEN the LLM lowers a basic entry THEN the score moves half as far as at full weight", func(t *testing.T) {
		evaluate := func(weight float64) domain.TradeIdea {
			in := single("BTC/EUR", bullish)
			in.Analyser = scaleAnalyser(0.8)
			ideas := basicAndLLMWeighted(weight).Evaluate(ctx, in)
			if len(ideas) != 1 {
				t.Fatalf("weight %v: got %d ideas, want 1", weight, len(ideas))
			}
			return ideas[0]
		}
		full, half := evaluate(1), evaluate(0.5)
		if llmShift(t, full) >= 0 {
			t.Fatalf("full weight: the LLM's 0.8 scale did not lower the score (shift %v)", llmShift(t, full))
		}
		if math.Abs(llmShift(t, half)-llmShift(t, full)/2) > 1e-9 {
			t.Fatalf("half weight shift %v, want half of the full-weight shift %v", llmShift(t, half), llmShift(t, full))
		}
	})

	t.Run("GIVEN an LLM weight WHEN an entry is scored THEN the raw scale, the weight and the applied scale are recorded", func(t *testing.T) {
		in := single("BTC/EUR", bullish)
		in.Analyser = scaleAnalyser(0.8)
		idea := basicAndLLMWeighted(0.5).Evaluate(ctx, in)[0]
		raw, weight, applied := floatSignal(t, idea, "llm_confidence_scale"), floatSignal(t, idea, "llm_weight"), floatSignal(t, idea, "llm_applied_scale")
		if raw != 0.8 || weight != 0.5 {
			t.Fatalf("raw scale %v, weight %v: want the LLM's 0.8 and the configured 0.5", raw, weight)
		}
		if !(applied > raw && applied < 1) {
			t.Fatalf("applied scale %v, want between the raw scale %v and neutral 1", applied, raw)
		}
		if math.Abs(idea.Confidence-floatSignal(t, idea, "pre_llm_confidence")*applied) > 1e-9 {
			t.Fatalf("final %v is not the pre-LLM confidence times the applied scale", idea.Confidence)
		}
	})

	t.Run("GIVEN zero LLM weight WHEN the LLM supports an entry THEN the score is unchanged by the LLM", func(t *testing.T) {
		in := single("BTC/EUR", bullish)
		in.Analyser = scaleAnalyser(1.5)
		ideas := basicAndLLMWeighted(0).Evaluate(ctx, in)
		if len(ideas) != 1 || llmShift(t, ideas[0]) != 0 {
			t.Fatalf("ideas %+v, want the pre-LLM score kept", ideas)
		}
	})

	t.Run("GIVEN zero LLM weight WHEN the LLM's raw scale is below the veto threshold THEN the entry is still vetoed", func(t *testing.T) {
		in := single("BTC/EUR", bullish)
		in.Analyser = scaleAnalyser(0.5)
		if ideas := basicAndLLMWeighted(0).Evaluate(ctx, in); len(ideas) != 0 {
			t.Fatalf("a raw-scale veto was ignored at zero weight: %+v", ideas)
		}
	})

	t.Run("GIVEN the LLM was not used WHEN a basic entry is scored THEN its pre-LLM confidence equals the final score", func(t *testing.T) {
		in := single("BTC/EUR", bullish)
		in.Analyser = &fakeAnalyser{signal: llm.SignalAnalysis{ConfidenceScale: 1.0, LLMUsed: false}}
		ideas := basicAndLLMWeighted(1).Evaluate(ctx, in)
		if len(ideas) != 1 || llmShift(t, ideas[0]) != 0 || ideas[0].SupportingSignals["llm_applied_scale"] != nil {
			t.Fatalf("ideas %+v, want an unchanged score with no applied scale", ideas)
		}
	})

	t.Run("GIVEN half LLM weight WHEN the LLM supports an LLM-version entry THEN the score moves half as far as at full weight", func(t *testing.T) {
		evaluate := func(weight float64) domain.TradeIdea {
			idea := stubIdea("ETH/EUR", domain.Long)
			idea.Confidence = 0.5 // room to scale up without reaching the cap
			ideas := llmVersionWeighted(weight, idea).Evaluate(ctx, llmInput(scaleAnalyser(1.4), 0))
			if len(ideas) != 1 {
				t.Fatalf("weight %v: got %d ideas, want 1", weight, len(ideas))
			}
			return ideas[0]
		}
		full, half := evaluate(1), evaluate(0.5)
		if llmShift(t, full) <= 0 {
			t.Fatalf("full weight: the LLM's 1.4 scale did not raise the score (shift %v)", llmShift(t, full))
		}
		if math.Abs(llmShift(t, half)-llmShift(t, full)/2) > 1e-9 {
			t.Fatalf("half weight shift %v, want half of the full-weight shift %v", llmShift(t, half), llmShift(t, full))
		}
	})

	t.Run("GIVEN zero LLM weight WHEN the LLM's raw scale is below the veto threshold THEN the LLM-version entry is still vetoed", func(t *testing.T) {
		if ideas := llmVersionWeighted(0, stubIdea("ETH/EUR", domain.Long)).Evaluate(ctx, llmInput(scaleAnalyser(0.6), 0)); len(ideas) != 0 {
			t.Fatalf("a raw-scale veto was ignored at zero weight: %+v", ideas)
		}
	})
}
