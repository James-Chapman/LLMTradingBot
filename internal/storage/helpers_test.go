package storage

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

// newTestRepo opens a fresh repository backed by a DB file in a temp dir.
func newTestRepo(t *testing.T) *Repository {
	t.Helper()
	return openTestRepo(t, filepath.Join(t.TempDir(), "trading_bot.db"))
}

// openTestRepo opens a repository at path and closes it when the test ends.
func openTestRepo(t *testing.T, path string) *Repository {
	t.Helper()
	repo, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q) failed: %v", path, err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

// makeTradeIdea mirrors tests/bdd_helpers.make_trade_idea.
func makeTradeIdea() domain.TradeIdea {
	return domain.TradeIdea{
		ID:                     domain.NewID(),
		StrategyID:             "basic_and_llm_strategy",
		Market:                 "BTC/EUR",
		Direction:              domain.Long,
		Thesis:                 "BDD test signal",
		SupportingSignals:      map[string]any{"source": "test"},
		Confidence:             0.7,
		EntryPlan:              "Enter at market",
		ExitPlan:               "Exit on invalidation",
		StopOrInvalidation:     "Stop at configured loss",
		PositionSizingProposal: 0.20,
		ModeEligibility: []domain.TradingMode{
			domain.ModeManual, domain.ModeSemiAutomated, domain.ModeFullyAutomated,
		},
	}
}

// at returns a UTC time on 2026-04-24 at the given hour/minute/second.
func at(hour, minute, second int) time.Time {
	return time.Date(2026, 4, 24, hour, minute, second, 0, time.UTC)
}

// mustNoErr fails the test immediately when err is non-nil.
func mustNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
