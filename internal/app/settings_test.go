package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/app/apptest"
	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/secret"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy"
)

// fieldView returns the named field from a settings view.
func fieldView(t *testing.T, view SettingsView, key string) SettingView {
	t.Helper()
	for _, f := range view.Fields {
		if f.Key == key {
			return f
		}
	}
	t.Fatalf("field %s not in settings view", key)
	return SettingView{}
}

// TestLoadSettings covers building the running settings from the database.
func TestLoadSettings(t *testing.T) {
	ctx := context.Background()
	box := secret.Reversible()

	t.Run("GIVEN an empty database WHEN settings load THEN defaults apply with the start-up flags", func(t *testing.T) {
		repo := newTestRepo(t)

		s, warnings, err := LoadSettings(ctx, repo, box, config.Bootstrap{Host: "0.0.0.0", Port: 9001, DatabasePath: "x.db", LogFile: "x.log"})

		if err != nil || len(warnings) != 0 {
			t.Fatalf("unexpected error/warnings: %v %v", err, warnings)
		}
		if s.MinSignalConfidence != config.Defaults().MinSignalConfidence {
			t.Errorf("expected default confidence, got %v", s.MinSignalConfidence)
		}
		if s.Host != "0.0.0.0" || s.Port != 9001 || s.DatabasePath != "x.db" || s.LogFile != "x.log" {
			t.Errorf("start-up flags not applied: %+v", s)
		}
	})

	t.Run("GIVEN a .env file and environment variables WHEN settings load THEN neither is read", func(t *testing.T) {
		repo := newTestRepo(t)
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, ".env"), []byte("MIN_SIGNAL_CONFIDENCE=0.99\nKRAKEN_API_KEY=from-file\n"), 0o600)
		t.Chdir(dir)
		t.Setenv("KRAKEN_API_KEY", "from-env")
		t.Setenv("MIN_SIGNAL_CONFIDENCE", "0.98")

		s, _, err := LoadSettings(ctx, repo, box, config.Bootstrap{})

		if err != nil {
			t.Fatal(err)
		}
		if s.KrakenAPIKey != "" || s.MinSignalConfidence != config.Defaults().MinSignalConfidence {
			t.Fatalf(".env and environment variables must be ignored, got key=%q confidence=%v", s.KrakenAPIKey, s.MinSignalConfidence)
		}
	})

	t.Run("GIVEN a stored value that no longer parses WHEN settings load THEN the default is used and a warning returned", func(t *testing.T) {
		repo := newTestRepo(t)
		_ = repo.SaveSettings(ctx, map[string]string{"MIN_SIGNAL_CONFIDENCE": "high", "MOMENTUM_LOOKBACK_TICKS": "15"})

		s, warnings, err := LoadSettings(ctx, repo, box, config.Bootstrap{})

		if err != nil {
			t.Fatal(err)
		}
		if s.MinSignalConfidence != config.Defaults().MinSignalConfidence || s.MomentumLookbackTicks != 15 {
			t.Fatalf("expected default for the bad value and the good value kept, got %v / %v", s.MinSignalConfidence, s.MomentumLookbackTicks)
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], "MIN_SIGNAL_CONFIDENCE") {
			t.Fatalf("expected one warning naming the key, got %v", warnings)
		}
	})
}

// TestSettingsFromWebInterface covers changing settings from the dashboard.
func TestSettingsFromWebInterface(t *testing.T) {
	ctx := context.Background()
	longIdea := func(strategy.Input) []domain.TradeIdea {
		return []domain.TradeIdea{apptest.Idea(strategy.BasicID, "BTC/EUR", domain.Long, 0.7)}
	}

	t.Run("GIVEN fully automated mode WHEN the signal threshold is raised THEN it applies to the next signal without a restart", func(t *testing.T) {
		env := newEnv(t, func(s *config.Settings) { s.TradingMode = "fully_automated" }, seedPrices(40))
		env.strategy.Ideas = longIdea
		env.boot(t)

		result, err := env.app.UpdateSettings(ctx, map[string]string{"MIN_SIGNAL_CONFIDENCE": "0.9"})
		if err != nil {
			t.Fatalf("UpdateSettings: %v", err)
		}
		env.tick(t)

		if len(env.app.Dashboard().Positions) != 0 {
			t.Fatal("a 70% signal must be skipped once the threshold is 90%")
		}
		if !slices.Contains(result.AppliedLive, "MIN_SIGNAL_CONFIDENCE") || len(result.RestartRequired) != 0 {
			t.Errorf("expected a live-applied change only, got %+v", result)
		}
	})

	t.Run("GIVEN a setting that needs a restart WHEN changed THEN it is saved, flagged and applied after a restart", func(t *testing.T) {
		env := newEnv(t, nil, nil)

		result, err := env.app.UpdateSettings(ctx, map[string]string{"FIXED_MARKETS": `["BTC/EUR","SOL/EUR"]`})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(result.RestartRequired, "FIXED_MARKETS") {
			t.Fatalf("expected FIXED_MARKETS to need a restart, got %+v", result)
		}
		view := env.app.SettingsView()
		if f := fieldView(t, view, "FIXED_MARKETS"); !f.RestartPending || f.Value != `["BTC/EUR","SOL/EUR"]` {
			t.Fatalf("expected the saved value with a pending restart, got %+v", f)
		}
		if slices.Contains(env.app.Settings().FixedMarkets, "SOL/EUR") {
			t.Fatal("the running bot must keep its markets until restarted")
		}

		env.restartFromDB(t)

		if !slices.Contains(env.app.Settings().FixedMarkets, "SOL/EUR") {
			t.Fatalf("expected the saved markets after restart, got %v", env.app.Settings().FixedMarkets)
		}
		if len(env.app.SettingsView().RestartRequired) != 0 {
			t.Errorf("nothing should be pending after the restart")
		}
	})

	t.Run("GIVEN an invalid or unknown setting WHEN saved THEN nothing is saved and a client error is returned", func(t *testing.T) {
		env := newEnv(t, nil, nil)

		for _, changes := range []map[string]string{
			{"MIN_SIGNAL_CONFIDENCE": "0.8", "MAX_DAILY_LOSS_PERCENT": "-5"},
			{"NOT_A_SETTING": "1"},
			{"PORT": "9000"},
		} {
			if _, err := env.app.UpdateSettings(ctx, changes); !isClientError(err) {
				t.Errorf("%v: expected a client error, got %v", changes, err)
			}
		}
		stored, _ := env.repo.LoadSettings(ctx)
		if len(stored) != 0 || env.app.Settings().MinSignalConfidence != testSettings().MinSignalConfidence {
			t.Fatalf("a refused save must change nothing, stored=%v", stored)
		}
	})

	t.Run("GIVEN a Kraken API key WHEN saved THEN it is stored sealed, never shown, and used after a restart", func(t *testing.T) {
		env := newEnv(t, nil, nil)

		if _, err := env.app.UpdateSettings(ctx, map[string]string{"KRAKEN_API_KEY": "key-123", "KRAKEN_API_SECRET": "secret-456"}); err != nil {
			t.Fatal(err)
		}

		stored, _ := env.repo.LoadSettings(ctx)
		if strings.Contains(stored["KRAKEN_API_KEY"], "key-123") || !strings.HasPrefix(stored["KRAKEN_API_KEY"], secret.SealedPrefix) {
			t.Fatalf("the key must be stored sealed, got %q", stored["KRAKEN_API_KEY"])
		}
		raw, _ := json.Marshal(env.app.SettingsView())
		if strings.Contains(string(raw), "key-123") || strings.Contains(string(raw), "secret-456") {
			t.Fatal("the settings view must never contain secret values")
		}
		if f := fieldView(t, env.app.SettingsView(), "KRAKEN_API_KEY"); !f.IsSet || f.Value != "" {
			t.Fatalf("expected is_set without a value, got %+v", f)
		}

		env.restartFromDB(t)

		if _, err := env.app.SetTradingEnvironment("live", LiveConfirmation); err != nil {
			t.Fatalf("saved keys should allow going live after a restart: %v", err)
		}
	})

	t.Run("GIVEN a saved secret WHEN cleared with an empty value THEN it is no longer set", func(t *testing.T) {
		env := newEnv(t, nil, nil)
		_, _ = env.app.UpdateSettings(ctx, map[string]string{"OPENAI_API_KEY": "sk-1"})

		if _, err := env.app.UpdateSettings(ctx, map[string]string{"OPENAI_API_KEY": ""}); err != nil {
			t.Fatal(err)
		}

		if fieldView(t, env.app.SettingsView(), "OPENAI_API_KEY").IsSet {
			t.Fatal("a cleared secret must report not set")
		}
	})
}
