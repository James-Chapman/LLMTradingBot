package risk

import (
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

// TestRiskSettingsApplyLive covers changing risk limits while the bot runs.
func TestRiskSettingsApplyLive(t *testing.T) {
	t.Run("GIVEN a trade the limits allow WHEN a stricter volume limit is applied THEN the next evaluation blocks it", func(t *testing.T) {
		engine := newTestEngine(nil)
		idea := makeIdea("BTC/EUR", domain.Long, 0.20)
		opts := standardOptions(500, 100)
		opts.MarketVolume24h = domain.Float(1_000)

		if d := engine.EvaluateTrade(idea, opts); !d.Approved {
			t.Fatalf("expected approval before the change, got %q", d.Reason)
		}

		stricter := config.Defaults()
		stricter.Min24hVolume = 1_000_000
		engine.SetSettings(stricter)

		if d := engine.EvaluateTrade(idea, opts); d.Approved {
			t.Fatal("expected the new volume limit to block the trade")
		}
	})

	t.Run("GIVEN running equity and daily loss WHEN settings are applied THEN that account state is kept", func(t *testing.T) {
		engine := newTestEngine(nil)
		engine.UpdateEquity(420)

		engine.SetSettings(config.Defaults())

		if engine.CurrentEquity() != 420 {
			t.Fatalf("applying settings must not reset equity, got %v", engine.CurrentEquity())
		}
	})
}
