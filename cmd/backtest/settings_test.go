package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunSettings covers where `backtest run` takes its settings from.
func TestRunSettings(t *testing.T) {
	t.Run("GIVEN a settings file WHEN a backtest runs THEN the result records the settings used and no database is created", func(t *testing.T) {
		cache := importedCache(t)
		workDir := t.TempDir()
		t.Chdir(workDir)
		settings := filepath.Join(workDir, "settings.json")
		if err := os.WriteFile(settings, []byte(`{"MIN_SIGNAL_CONFIDENCE": 0.9, "STOP_LOSS_PCT": 0.04}`), 0o644); err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(workDir, "res")
		code, stdout, stderr := runCLI(t, append(runArgs(cache, out), "--settings", settings)...)
		if code != 0 {
			t.Fatalf("code %d\nstdout %s\nstderr %s", code, stdout, stderr)
		}
		var res struct {
			Settings map[string]string `json:"settings"`
		}
		raw, err := os.ReadFile(filepath.Join(out, "result.json"))
		if err != nil || json.Unmarshal(raw, &res) != nil {
			t.Fatalf("read result: %v", err)
		}
		if res.Settings["MIN_SIGNAL_CONFIDENCE"] != "0.9" || res.Settings["STOP_LOSS_PCT"] != "0.04" {
			t.Fatalf("settings snapshot %v", res.Settings)
		}
		if _, err := os.Stat(filepath.Join(workDir, "trading_bot.db")); !os.IsNotExist(err) {
			t.Fatalf("the backtest must never create or open the bot database: %v", err)
		}
	})

	t.Run("GIVEN a settings file holding a secret WHEN a backtest runs THEN it refuses to start", func(t *testing.T) {
		settings := filepath.Join(t.TempDir(), "settings.json")
		if err := os.WriteFile(settings, []byte(`{"KRAKEN_API_SECRET": "x"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		code, _, stderr := runCLI(t, append(runArgs(importedCache(t), filepath.Join(t.TempDir(), "res")), "--settings", settings)...)
		if code == 0 || !strings.Contains(stderr, "secret") {
			t.Fatalf("code %d stderr %q, want a refusal naming secrets", code, stderr)
		}
	})
}
