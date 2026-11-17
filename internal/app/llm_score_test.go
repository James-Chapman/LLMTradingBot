package app

import (
	"context"
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/app/apptest"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/storage"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy"
)

// storedSignal returns the stored signal detail of one trade idea.
func storedSignal(t *testing.T, repo *storage.Repository, ideaID string) *storage.SignalDetail {
	t.Helper()
	detail, err := repo.GetSignalDetail(context.Background(), ideaID)
	if err != nil || detail == nil {
		t.Fatalf("signal %s: %v", ideaID, err)
	}
	return detail
}

// TestSignalScoreSteps covers the confidence steps stored with a signal, which
// the signal card uses to show what the LLM and the learner each changed.
func TestSignalScoreSteps(t *testing.T) {
	t.Run("GIVEN an LLM strategy that scaled its entry WHEN the signal is stored THEN its pre-LLM confidence is the strategy's, not the final score", func(t *testing.T) {
		env := newEnv(t, nil, seedPrices(40))
		idea := apptest.Idea(strategy.BasicAndLLM1hID, "BTC/EUR", domain.Long, 0.6)
		idea.SupportingSignals["pre_llm_confidence"] = 0.5
		idea.SupportingSignals["llm_confidence_scale"] = 1.2
		env.strategy.Ideas = ideasOnce(idea)
		env.boot(t)
		env.tick(t)

		ss := storedSignal(t, env.repo, idea.ID).SupportingSignals
		if ss["pre_llm_confidence"] != 0.5 {
			t.Fatalf("pre_llm_confidence %v, want the strategy's 0.5 kept", ss["pre_llm_confidence"])
		}
	})

	t.Run("GIVEN a learner with history for the strategy WHEN a signal is stored THEN the confidence before the learner is recorded", func(t *testing.T) {
		env := newEnv(t, nil, seedPrices(40))
		idea := apptest.Idea(strategy.Basic1hID, "BTC/EUR", domain.Long, 0.6)
		env.strategy.Ideas = ideasOnce(idea)
		env.boot(t)
		for range 6 {
			env.app.learner.RecordOutcomeWithNotional(strategy.Basic1hID, "BTC/EUR", "long", 50, 1000)
		}
		env.tick(t)

		detail := storedSignal(t, env.repo, idea.ID)
		ss := detail.SupportingSignals
		if ss["pre_learner_confidence"] != 0.6 {
			t.Fatalf("pre_learner_confidence %v, want the strategy's 0.6", ss["pre_learner_confidence"])
		}
		if detail.Confidence == 0.6 {
			t.Fatal("test setup: the learner did not adjust the score")
		}
	})

	t.Run("GIVEN a strategy without the LLM WHEN a signal is stored THEN its pre-LLM confidence equals its pre-learner confidence", func(t *testing.T) {
		env := newEnv(t, nil, seedPrices(40))
		idea := apptest.Idea(strategy.Basic1hID, "BTC/EUR", domain.Long, 0.6)
		env.strategy.Ideas = ideasOnce(idea)
		env.boot(t)
		env.tick(t)

		ss := storedSignal(t, env.repo, idea.ID).SupportingSignals
		if ss["pre_llm_confidence"] == nil || ss["pre_llm_confidence"] != ss["pre_learner_confidence"] {
			t.Fatalf("pre_llm %v, pre_learner %v: want both set and equal", ss["pre_llm_confidence"], ss["pre_learner_confidence"])
		}
	})
}
