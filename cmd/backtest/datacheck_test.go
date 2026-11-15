package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// importDay imports one day of BTC/EUR trades (a trade a minute) into cache.
func importDay(t *testing.T, cache string, day time.Time) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "day")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("timestamp,price,volume,type,order_type,misc,trade_id\n")
	for i := range 1440 {
		side := "s"
		if i%2 == 0 {
			side = "b"
		}
		fmt.Fprintf(&b, "%d,%.1f,0.05,%s,m,,%d\n", day.Add(time.Duration(i)*time.Minute).Unix(), 50000+float64(i%7), side, day.Unix()+int64(i))
	}
	if err := os.WriteFile(filepath.Join(dir, "XBTEUR.csv"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := runData(context.Background(), []string{"import", "--cache", cache, "--archive", dir}, &out, &errOut); code != 0 {
		t.Fatalf("import: code %d\n%s\n%s", code, out.String(), errOut.String())
	}
}

// readResult decodes a run's result.json into v.
func readResult(t *testing.T, dir string, v any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "result.json"))
	if err != nil || json.Unmarshal(raw, v) != nil {
		t.Fatalf("read result: %v", err)
	}
}

// TestRunDataChecks covers the data-cache checks and the new run flags.
func TestRunDataChecks(t *testing.T) {
	t.Run("GIVEN a cache with July and September but no August WHEN a run spans August THEN it refuses naming the month, and --allow-data-gaps runs it with the gap in the summary", func(t *testing.T) {
		cache := filepath.Join(t.TempDir(), "cache")
		importDay(t, cache, time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC))
		importDay(t, cache, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
		args := []string{"run", "--markets", "BTC/EUR", "--start", "2026-07-31", "--end", "2026-09-01", "--data", cache, "--strategy", "basic_strategy"}

		out := filepath.Join(t.TempDir(), "refused")
		code, _, stderr := runCLI(t, append(args, "--output", out)...)
		if code == 0 || !strings.Contains(stderr, "BTC/EUR") || !strings.Contains(stderr, "2026-08") {
			t.Fatalf("code %d stderr %q, want a refusal naming BTC/EUR 2026-08", code, stderr)
		}
		if _, err := os.Stat(out); !os.IsNotExist(err) {
			t.Fatal("a refused run must write no result")
		}

		out = filepath.Join(t.TempDir(), "allowed")
		code, stdout, stderr := runCLI(t, append(args, "--output", out, "--allow-data-gaps")...)
		if code != 0 {
			t.Fatalf("code %d\n%s\n%s", code, stdout, stderr)
		}
		summary, _ := os.ReadFile(filepath.Join(out, "summary.md"))
		if !strings.Contains(string(summary), "2026-08") || !strings.Contains(stdout, "2026-08") {
			t.Fatalf("the summary and the printed report must show the missing month:\n%s", summary)
		}
	})

	t.Run("GIVEN --fill-empty-candles, --stale-after and a settings file setting FEE_AND_SLIPPAGE WHEN a run completes THEN the result records them and where the settings came from", func(t *testing.T) {
		cache := importedCache(t)
		settings := filepath.Join(t.TempDir(), "settings.json")
		if err := os.WriteFile(settings, []byte(`{"FEE_AND_SLIPPAGE": 0.02}`), 0o644); err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(t.TempDir(), "res")
		code, stdout, stderr := runCLI(t, append(runArgs(cache, out), "--fill-empty-candles", "--stale-after", "6h", "--settings", settings)...)
		if code != 0 {
			t.Fatalf("code %d\n%s\n%s", code, stdout, stderr)
		}
		var res struct {
			FillEmptyCandles bool   `json:"fill_empty_candles"`
			StaleAfter       string `json:"stale_after"`
			SettingsSource   string `json:"settings_source"`
			RiskFee          struct {
				Explicit       bool    `json:"explicit"`
				FeeAndSlippage float64 `json:"fee_and_slippage"`
			} `json:"risk_fee"`
			DataCheck *struct{} `json:"data_check"`
		}
		readResult(t, out, &res)
		if !res.FillEmptyCandles || res.StaleAfter != "6h0m0s" || res.SettingsSource != settings || !res.RiskFee.Explicit || res.RiskFee.FeeAndSlippage != 0.02 || res.DataCheck == nil {
			t.Fatalf("result %+v, want the flags, the settings file, the explicit risk fee and the data check recorded", res)
		}
	})

	t.Run("GIVEN no settings file WHEN a run completes THEN the result says the built-in defaults were used", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "res")
		if code, stdout, stderr := runCLI(t, runArgs(importedCache(t), out)...); code != 0 {
			t.Fatalf("code %d\n%s\n%s", code, stdout, stderr)
		}
		var res struct {
			SettingsSource string `json:"settings_source"`
		}
		readResult(t, out, &res)
		if res.SettingsSource != "built-in defaults" {
			t.Fatalf("settings source %q", res.SettingsSource)
		}
	})
}
