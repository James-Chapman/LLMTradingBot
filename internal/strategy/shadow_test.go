package strategy

import (
	"context"
	"reflect"
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/llm"
)

// TestShadowReviewer covers shadow reviews: the LLM is asked about a non-LLM
// 4-hour strategy's entries exactly as its LLM version would ask, and the
// answer is only recorded, never applied to the idea.
func TestShadowReviewer(t *testing.T) {
	ctx := context.Background()
	s := testSettings()
	base := &stubPrompter{}

	t.Run("GIVEN a new entry of a 4-hour strategy WHEN claimed and reviewed THEN the LLM gets the same request as the LLM version sends and the idea is unchanged", func(t *testing.T) {
		idea := stubIdea("ETH/EUR", domain.Long)
		version := scaleAnalyser(0.6)
		NewLLMVersion(&stubPrompter{ideas: []domain.TradeIdea{idea}}, "stub_and_llm_strategy", s).Evaluate(ctx, llmInput(version, 0))

		shadow := scaleAnalyser(0.6)
		in := llmInput(shadow, 0)
		r := NewShadowReviewer()
		entries := r.Claim(base, []domain.TradeIdea{idea}, in)
		if len(entries) != 1 {
			t.Fatalf("claimed %d entries, want 1", len(entries))
		}
		v := r.Review(ctx, shadow, entries[0], s)
		if len(shadow.signalCalls) != 1 || !reflect.DeepEqual(shadow.signalCalls[0], version.signalCalls[0]) {
			t.Fatalf("shadow request %+v\nLLM version request %+v", shadow.signalCalls, version.signalCalls)
		}
		if !v.Analysis.LLMUsed || !v.WouldVeto || v.StrategyID != "stub_strategy" || v.Market != "ETH/EUR" || v.Candle != 0 {
			t.Fatalf("verdict %+v, want a used LLM veto of stub_strategy ETH/EUR at candle 0", v)
		}
		if v.TradeIdeaID != idea.ID || v.BaseConfidence != idea.Confidence || v.VetoThreshold != s.LLMVetoThreshold {
			t.Fatalf("verdict %+v does not record the reviewed idea", v)
		}
		if entries[0].Idea.Confidence != idea.Confidence || entries[0].Idea.Thesis != idea.Thesis {
			t.Fatalf("the shadow review changed the idea: %+v", entries[0].Idea)
		}
	})

	t.Run("GIVEN a scale at the veto threshold WHEN reviewed THEN it is not a veto", func(t *testing.T) {
		an := scaleAnalyser(s.LLMVetoThreshold)
		in := llmInput(an, 0)
		r := NewShadowReviewer()
		entries := r.Claim(base, []domain.TradeIdea{stubIdea("ETH/EUR", domain.Long)}, in)
		if v := r.Review(ctx, an, entries[0], s); v.WouldVeto {
			t.Fatalf("scale %.2f at the threshold counted as a veto", v.Analysis.ConfidenceScale)
		}
	})

	t.Run("GIVEN an entry claimed for a candle WHEN the same entry comes again THEN it is claimed only once per closed candle", func(t *testing.T) {
		r := NewShadowReviewer()
		idea := stubIdea("ETH/EUR", domain.Long)
		an := scaleAnalyser(1.0)
		first := r.Claim(base, []domain.TradeIdea{idea}, llmInput(an, 0))
		r.Review(ctx, an, first[0], s)
		if again := r.Claim(base, []domain.TradeIdea{idea}, llmInput(an, 0)); len(again) != 0 {
			t.Fatalf("the same candle was claimed again: %+v", again)
		}
		if next := r.Claim(base, []domain.TradeIdea{idea}, llmInput(an, 4*3600)); len(next) != 1 {
			t.Fatalf("a new closed candle claimed %d entries, want 1", len(next))
		}
	})

	t.Run("GIVEN exits, held markets and entries without a closed candle WHEN claimed THEN none is reviewed", func(t *testing.T) {
		r := NewShadowReviewer()
		exit := stubIdea("ETH/EUR", domain.Short)
		exit.Exit = true
		if got := r.Claim(base, []domain.TradeIdea{exit}, llmInput(nil, 0)); len(got) != 0 {
			t.Fatalf("an exit was claimed: %+v", got)
		}
		held := llmInput(nil, 0, domain.PositionRecord{Market: "ETH/EUR", Size: 1})
		if got := r.Claim(base, []domain.TradeIdea{stubIdea("ETH/EUR", domain.Long)}, held); len(got) != 0 {
			t.Fatalf("an entry for a held market was claimed: %+v", got)
		}
		noCandle := llmInput(nil, 0)
		noCandle.MarketData["ETH/EUR"] = MarketData{Price: 101}
		if got := r.Claim(base, []domain.TradeIdea{stubIdea("ETH/EUR", domain.Long)}, noCandle); len(got) != 0 {
			t.Fatalf("an entry without a closed 4-hour candle was claimed: %+v", got)
		}
	})

	t.Run("GIVEN a strategy without an LLM prompt or one that already uses the LLM WHEN claimed THEN nothing is reviewed", func(t *testing.T) {
		r := NewShadowReviewer()
		ideas := []domain.TradeIdea{stubIdea("ETH/EUR", domain.Long)}
		if got := r.Claim(NewBasic1h(s), ideas, llmInput(nil, 0)); len(got) != 0 {
			t.Fatalf("basic_strategy was claimed: %+v", got)
		}
		version := NewLLMVersion(base, "stub_and_llm_strategy", s)
		if got := r.Claim(version, ideas, llmInput(nil, 0)); len(got) != 0 {
			t.Fatalf("an LLM version was claimed: %+v", got)
		}
	})

	t.Run("GIVEN the LLM cannot answer WHEN reviewed THEN the verdict says so and the entry can be claimed again", func(t *testing.T) {
		r := NewShadowReviewer()
		idea := stubIdea("ETH/EUR", domain.Long)
		down := &fakeAnalyser{signal: llm.SignalAnalysis{ConfidenceScale: 1, Reasoning: "LLM unavailable — no adjustment"}}
		entries := r.Claim(base, []domain.TradeIdea{idea}, llmInput(down, 0))
		if v := r.Review(ctx, down, entries[0], s); v.Analysis.LLMUsed || v.WouldVeto {
			t.Fatalf("verdict %+v, want an unused LLM and no veto", v)
		}
		if again := r.Claim(base, []domain.TradeIdea{idea}, llmInput(down, 0)); len(again) != 1 {
			t.Fatalf("an unanswered entry was not claimable again: %+v", again)
		}
	})
}
