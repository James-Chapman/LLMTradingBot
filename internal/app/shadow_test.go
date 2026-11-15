package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/app/apptest"
	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/llm"
	"github.com/James-Chapman/LLMTradingBot/internal/marketdata"
	"github.com/James-Chapman/LLMTradingBot/internal/storage"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy"
)

// promptingStrategy is a fake non-LLM 4-hour strategy with its own LLM prompt.
type promptingStrategy struct{ *apptest.Strategy }

// Prompt returns a fixed prompt that the test can find in the LLM request.
func (p promptingStrategy) Prompt(domain.TradeIdea, strategy.MarketData) llm.StrategyPrompt {
	return llm.StrategyPrompt{Name: "Fake 4-hour breakout", Instructions: "Judge the fake breakout."}
}

// shadowEnv returns a fully automated paper bot whose selected strategy is a
// prompting 4-hour strategy, with news, 4-hour candles and an LLM that
// answers with scale. The strategy buys BTC/EUR on the first tick and sends
// a rule exit on every later one.
func shadowEnv(t *testing.T, enabled bool, scale float64) *testEnv {
	t.Helper()
	env := newEnv(t, func(s *config.Settings) {
		s.TradingMode = "fully_automated"
		s.LLMShadowReview = enabled
	}, seedPrices(40))
	env.strategy.StrategyID = strategy.DonchianID
	first := true
	env.strategy.Ideas = func(strategy.Input) []domain.TradeIdea {
		if first {
			first = false
			return []domain.TradeIdea{apptest.Idea(strategy.DonchianID, "BTC/EUR", domain.Long, 0.95)}
		}
		exit := apptest.Idea(strategy.DonchianID, "BTC/EUR", domain.Short, 0.95)
		exit.Exit = true
		return []domain.TradeIdea{exit}
	}
	// A closed 4-hour candle that ended at the last 4-hour boundary, then the forming one.
	// They go straight into the candle cache: a real OHLC cycle paces its
	// Kraken requests and would add seconds to every test.
	forming := time.Now().UTC().Truncate(4 * time.Hour).Unix()
	candles := []domain.Candle{
		{T: forming - 4*3600, O: 50000, H: 50100, L: 49900, C: 50000, V: 1},
		{T: forming, O: 50000, H: 50000, L: 50000, C: 50000, V: 0},
	}
	env.boot(t)
	// The LLM answers from here on, so boot's own LLM work stays offline.
	env.llm.Answer = map[string]any{"sentiment": -0.5, "confidence_scale": scale, "reasoning": "looks like hype"}
	p := promptingStrategy{env.strategy}
	env.app.mu.Lock()
	env.app.ohlc240["BTC/EUR"] = OHLCPayload{Symbol: "BTC/EUR", Interval: marketdata.TrendCandleMinutes, Candles: candles}
	env.app.strategies = []strategy.Strategy{p}
	env.app.latestNews = []storage.NewsEntry{{ID: "n1", Source: "Fake", Title: "Bitcoin rallies", Summary: "BTC up", PublishedAt: time.Now().UTC().Format(time.RFC3339)}}
	env.app.mu.Unlock()
	env.app.control.SelectStrategy(p.ID())
	return env
}

// TestShadowReview covers shadow mode in the strategy loop: the LLM's verdict
// on a non-LLM 4-hour strategy's entry is recorded and later scored against
// the trade, while the trade itself goes ahead unchanged.
func TestShadowReview(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN shadow mode on and an LLM that would veto WHEN a 4-hour strategy enters THEN the trade still opens and the veto is recorded with the strategy's own prompt", func(t *testing.T) {
		env := shadowEnv(t, true, 0.5)
		env.tick(t)
		env.app.Wait()
		if n := len(env.app.paper.OpenPositions()); n != 1 {
			t.Fatalf("%d open positions, want the entry traded as without shadow mode", n)
		}
		prompts := env.llm.Prompts()
		if len(prompts) != 1 || !strings.Contains(prompts[0], "Fake 4-hour breakout") {
			t.Fatalf("LLM prompts %q, want one with the strategy's own prompt", prompts)
		}
		report, err := env.app.ShadowReviews(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !report.Enabled || len(report.Recent) != 1 || !report.Recent[0].WouldVeto || report.Recent[0].Market != "BTC/EUR" {
			t.Fatalf("report %+v, want one recorded BTC/EUR veto", report)
		}
	})

	t.Run("GIVEN a recorded veto WHEN the trade it reviewed closes THEN the veto is scored against the trade's P&L", func(t *testing.T) {
		env := shadowEnv(t, true, 0.5)
		env.tick(t) // entry
		env.app.Wait()
		env.tick(t) // rule exit
		env.app.Wait()
		if n := len(env.app.paper.OpenPositions()); n != 0 {
			t.Fatalf("%d positions still open", n)
		}
		report, err := env.app.ShadowReviews(ctx)
		if err != nil || len(report.Strategies) != 1 {
			t.Fatalf("report %+v, %v", report, err)
		}
		got := report.Strategies[0]
		// Flat prices: the round trip loses its fees, so the veto would have saved them.
		if got.StrategyID != strategy.DonchianID || got.Scored != 1 || got.ScoredVetoes != 1 || got.VetoedLosers != 1 || got.VetoedPnL >= 0 {
			t.Fatalf("score %+v, want one vetoed losing trade", got)
		}
		if len(env.llm.Prompts()) != 1 {
			t.Fatalf("the exit was sent to the LLM: %d prompts", len(env.llm.Prompts()))
		}
	})

	t.Run("GIVEN shadow mode off WHEN a 4-hour strategy enters THEN the LLM is not asked and nothing is recorded", func(t *testing.T) {
		env := shadowEnv(t, false, 0.5)
		env.tick(t)
		env.app.Wait()
		report, _ := env.app.ShadowReviews(ctx)
		if len(env.llm.Prompts()) != 0 || len(report.Recent) != 0 || report.Enabled {
			t.Fatalf("prompts %d, report %+v; want no shadow review", len(env.llm.Prompts()), report)
		}
		if n := len(env.app.paper.OpenPositions()); n != 1 {
			t.Fatalf("%d open positions, want the entry traded", n)
		}
	})

	t.Run("GIVEN shadow mode on and no news yet WHEN a 4-hour strategy enters THEN no review is made, as the LLM version would still wait", func(t *testing.T) {
		env := shadowEnv(t, true, 0.5)
		env.app.mu.Lock()
		env.app.latestNews = nil
		env.app.mu.Unlock()
		env.tick(t)
		env.app.Wait()
		if len(env.llm.Prompts()) != 0 {
			t.Fatalf("the LLM was asked without news: %d prompts", len(env.llm.Prompts()))
		}
	})
}
