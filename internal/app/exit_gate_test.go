package app

import (
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/app/apptest"
	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy"
)

// TestExitGate covers fully automated exits and the MIN_SIGNAL_CONFIDENCE
// gate: a rule exit of a held long is never dropped for low confidence.
func TestExitGate(t *testing.T) {
	// openThenSell buys BTC/EUR on the first tick and sends a 0.3-confidence
	// sell, marked as a rule exit or not, on every later tick.
	openThenSell := func(exit bool) func(strategy.Input) []domain.TradeIdea {
		first := true
		return func(strategy.Input) []domain.TradeIdea {
			if first {
				first = false
				return []domain.TradeIdea{apptest.Idea(strategy.TrendID, "BTC/EUR", domain.Long, 0.9)}
			}
			sell := apptest.Idea(strategy.TrendID, "BTC/EUR", domain.Short, 0.3)
			sell.Exit = exit
			return []domain.TradeIdea{sell}
		}
	}

	t.Run("GIVEN a held paper long WHEN a rule exit arrives below the confidence gate THEN the long is sold", func(t *testing.T) {
		env := newEnv(t, func(s *config.Settings) { s.TradingMode = "fully_automated" }, seedPrices(40))
		env.strategy.Ideas = openThenSell(true)
		env.boot(t)
		env.tick(t)
		if len(env.app.paper.OpenPositions()) != 1 {
			t.Fatal("expected the long to open")
		}
		env.tick(t)
		if n := len(env.app.paper.OpenPositions()); n != 0 {
			t.Fatalf("%d position(s) still open: the rule exit was blocked by the confidence gate", n)
		}
	})

	t.Run("GIVEN a held paper long WHEN an ordinary sell arrives below the gate THEN it is skipped as before", func(t *testing.T) {
		env := newEnv(t, func(s *config.Settings) { s.TradingMode = "fully_automated" }, seedPrices(40))
		env.strategy.Ideas = openThenSell(false)
		env.boot(t)
		env.tick(t)
		env.tick(t)
		if len(env.app.paper.OpenPositions()) != 1 || !env.activityContains("below threshold") {
			t.Fatal("an ordinary low-confidence sell must still be skipped")
		}
	})

	t.Run("GIVEN nothing held WHEN a rule exit arrives below the gate THEN no paper short is opened", func(t *testing.T) {
		env := newEnv(t, func(s *config.Settings) { s.TradingMode = "fully_automated" }, seedPrices(40))
		env.strategy.Ideas = func(strategy.Input) []domain.TradeIdea {
			sell := apptest.Idea(strategy.TrendID, "BTC/EUR", domain.Short, 0.3)
			sell.Exit = true
			return []domain.TradeIdea{sell}
		}
		env.boot(t)
		env.tick(t)
		if n := len(env.app.paper.OpenPositions()); n != 0 {
			t.Fatalf("a rule exit with nothing held opened %d position(s)", n)
		}
	})
}
