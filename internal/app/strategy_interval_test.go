package app

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
)

// TestStrategyInterval covers the strategy tick length set from the dashboard.
func TestStrategyInterval(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN default settings WHEN the strategy loop waits THEN ticks are 60 seconds apart and prices go stale after 120 seconds", func(t *testing.T) {
		env := newEnv(t, nil, nil)

		if got := env.app.strategyInterval(); got != 60*time.Second {
			t.Errorf("interval = %v, want 60s", got)
		}
		if got := env.app.staleAfter(); got != 120*time.Second {
			t.Errorf("stale after = %v, want two intervals", got)
		}
	})

	t.Run("GIVEN a running bot WHEN the interval is changed in the dashboard THEN it applies at once", func(t *testing.T) {
		env := newEnv(t, nil, nil)

		result, err := env.app.UpdateSettings(ctx, map[string]string{"STRATEGY_INTERVAL_SECONDS": "300"})

		if err != nil {
			t.Fatalf("UpdateSettings: %v", err)
		}
		if !slices.Contains(result.AppliedLive, "STRATEGY_INTERVAL_SECONDS") {
			t.Errorf("applied live = %v, want the interval in use at once", result.AppliedLive)
		}
		if got := env.app.strategyInterval(); got != 5*time.Minute {
			t.Errorf("interval = %v, want 5m", got)
		}
	})

	t.Run("GIVEN a saved interval WHEN the bot restarts THEN the saved interval is used", func(t *testing.T) {
		env := newEnv(t, nil, nil)
		if _, err := env.app.UpdateSettings(ctx, map[string]string{"STRATEGY_INTERVAL_SECONDS": "45"}); err != nil {
			t.Fatalf("UpdateSettings: %v", err)
		}

		env.restartFromDB(t)

		if got := env.app.strategyInterval(); got != 45*time.Second {
			t.Errorf("interval after restart = %v, want 45s", got)
		}
	})

	t.Run("GIVEN an interval fixed by the start-up options WHEN the setting differs THEN the options win", func(t *testing.T) {
		env := newEnv(t, func(s *config.Settings) { s.StrategyIntervalSeconds = 90 }, nil)
		env.app.intervals.Strategy = time.Hour

		if got := env.app.strategyInterval(); got != time.Hour {
			t.Errorf("interval = %v, want the fixed hour", got)
		}
	})
}
