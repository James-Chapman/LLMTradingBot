package app

import (
	"errors"
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/app/apptest"
	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy"
)

// withKrakenKeys configures (fake) Kraken credentials so live mode is allowed.
func withKrakenKeys(s *config.Settings) {
	s.KrakenAPIKey = "test-key"
	s.KrakenAPISecret = "test-secret"
}

// isClientError reports whether err is a 4xx StatusError.
func isClientError(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Status >= 400 && se.Status < 500
}

// TestTradingSettingsFromWebInterface covers changing the trading mode and the
// paper/live environment at runtime, as the dashboard does.
func TestTradingSettingsFromWebInterface(t *testing.T) {
	longIdea := func(strategy.Input) []domain.TradeIdea {
		return []domain.TradeIdea{apptest.Idea(strategy.BasicID, "BTC/EUR", domain.Long, 0.9)}
	}

	t.Run("GIVEN manual mode WHEN switched to fully automated THEN the next signal executes without approval", func(t *testing.T) {
		env := newEnv(t, nil, seedPrices(40))
		env.strategy.Ideas = longIdea
		env.boot(t)

		if _, err := env.app.SetTradingMode("fully_automated"); err != nil {
			t.Fatalf("SetTradingMode: %v", err)
		}
		env.tick(t)

		if len(env.app.Dashboard().Positions) != 1 {
			t.Fatalf("expected the signal to open a position in fully automated mode")
		}
		if env.app.Dashboard().Mode != "fully_automated" {
			t.Errorf("dashboard should show the new mode, got %q", env.app.Dashboard().Mode)
		}
	})

	t.Run("GIVEN an unknown trading mode WHEN set THEN it is refused and the mode is unchanged", func(t *testing.T) {
		env := newEnv(t, nil, nil)

		_, err := env.app.SetTradingMode("yolo")

		if !isClientError(err) {
			t.Fatalf("expected a client error, got %v", err)
		}
		if env.app.Dashboard().Mode != "manual" {
			t.Errorf("mode must stay manual, got %q", env.app.Dashboard().Mode)
		}
	})

	t.Run("GIVEN paper WHEN switching to live without typing the confirmation THEN it is refused and stays paper", func(t *testing.T) {
		env := newEnv(t, withKrakenKeys, nil)

		for _, confirmation := range []string{"", "yes", "live"} {
			if _, err := env.app.SetTradingEnvironment("live", confirmation); !isClientError(err) {
				t.Errorf("confirmation %q: expected a client error, got %v", confirmation, err)
			}
		}
		if env.app.Dashboard().Environment != "paper" {
			t.Fatalf("environment must stay paper, got %q", env.app.Dashboard().Environment)
		}
	})

	t.Run("GIVEN no Kraken API keys WHEN switching to live with the confirmation THEN it is refused and stays paper", func(t *testing.T) {
		env := newEnv(t, nil, nil)

		_, err := env.app.SetTradingEnvironment("live", LiveConfirmation)

		if !isClientError(err) {
			t.Fatalf("expected a client error, got %v", err)
		}
		if env.app.Dashboard().Environment != "paper" {
			t.Fatalf("environment must stay paper, got %q", env.app.Dashboard().Environment)
		}
	})

	t.Run("GIVEN Kraken keys and the typed confirmation WHEN switching to live THEN live-toggled markets send real orders", func(t *testing.T) {
		env := newEnv(t, func(s *config.Settings) { withKrakenKeys(s); s.TradingMode = "fully_automated" }, seedPrices(40))
		env.strategy.Ideas = longIdea
		env.boot(t)
		env.app.control.SetMarketLive("BTC/EUR", true)

		if _, err := env.app.SetTradingEnvironment("live", LiveConfirmation); err != nil {
			t.Fatalf("SetTradingEnvironment: %v", err)
		}
		env.tick(t)

		if env.app.Dashboard().Environment != "live" {
			t.Errorf("dashboard should show live, got %q", env.app.Dashboard().Environment)
		}
		if env.live.ExecutedCount() != 1 {
			t.Fatalf("expected the live-toggled market to route to Kraken, got %d orders", env.live.ExecutedCount())
		}
	})

	t.Run("GIVEN live WHEN switching back to paper THEN no typed confirmation is needed and orders stay on paper", func(t *testing.T) {
		env := newEnv(t, func(s *config.Settings) {
			withKrakenKeys(s)
			s.TradingMode = "fully_automated"
			s.TradingEnvironment = string(domain.EnvLive)
		}, seedPrices(40))
		env.strategy.Ideas = longIdea
		env.boot(t)
		env.app.control.SetMarketLive("BTC/EUR", true)

		if _, err := env.app.SetTradingEnvironment("paper", ""); err != nil {
			t.Fatalf("switching to paper should not need a confirmation: %v", err)
		}
		env.tick(t)

		if env.live.ExecutedCount() != 0 {
			t.Fatalf("paper environment must not send real orders, got %d", env.live.ExecutedCount())
		}
	})

	t.Run("GIVEN settings chosen in the web interface WHEN the bot restarts with different starting values THEN the web choices win", func(t *testing.T) {
		env := newEnv(t, withKrakenKeys, nil)
		if _, err := env.app.SetTradingMode("semi_automated"); err != nil {
			t.Fatal(err)
		}
		if _, err := env.app.SetTradingEnvironment("live", LiveConfirmation); err != nil {
			t.Fatal(err)
		}

		env.restart(t, func(s *config.Settings) { withKrakenKeys(s); s.TradingMode = "manual"; s.TradingEnvironment = "paper" })

		d := env.app.Dashboard()
		if d.Mode != "semi_automated" || d.Environment != "live" {
			t.Fatalf("web choices must survive a restart, got mode=%q env=%q", d.Mode, d.Environment)
		}
	})

	t.Run("GIVEN a fresh database WHEN the bot starts THEN the starting values are used", func(t *testing.T) {
		env := newEnv(t, func(s *config.Settings) { s.TradingMode = "semi_automated" }, nil)

		d := env.app.Dashboard()
		if d.Mode != "semi_automated" || d.Environment != "paper" {
			t.Fatalf("expected the starting values, got mode=%q env=%q", d.Mode, d.Environment)
		}
	})
}
