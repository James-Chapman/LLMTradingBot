package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/secret"
	"github.com/James-Chapman/LLMTradingBot/internal/storage"
)

// TestBacktestSettings covers where the replay takes its strategy settings from.
func TestBacktestSettings(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN no database file WHEN settings load THEN the defaults are used and no file is created", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing.db")
		s, _, err := loadSettings(ctx, path, secret.Reversible())
		if err != nil {
			t.Fatal(err)
		}
		if s.MinSignalConfidence != config.Defaults().MinSignalConfidence {
			t.Errorf("expected default confidence, got %v", s.MinSignalConfidence)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("the backtest must not create a database, stat: %v", err)
		}
	})

	t.Run("GIVEN settings saved from the dashboard WHEN settings load THEN the replay uses them", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "bot.db")
		repo, err := storage.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.SaveSettings(ctx, map[string]string{"MIN_SIGNAL_CONFIDENCE": "0.9"}); err != nil {
			t.Fatal(err)
		}
		repo.Close()

		s, _, err := loadSettings(ctx, path, secret.Reversible())
		if err != nil {
			t.Fatal(err)
		}
		if s.MinSignalConfidence != 0.9 {
			t.Errorf("expected the saved confidence 0.9, got %v", s.MinSignalConfidence)
		}
	})
}
