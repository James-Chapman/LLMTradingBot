package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runCLI runs the command with args and returns its exit code and output.
func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(context.Background(), args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// fixtureDay is the day the CLI fixture trades on.
var fixtureDay = time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)

// importedCache writes a day of BTC/EUR trades as a Kraken archive CSV,
// imports it with `backtest data import` and returns the cache directory.
func importedCache(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "Kraken_Trades_2026Q3")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("timestamp,price,volume,type,order_type,misc,trade_id\n")
	price := 50000.0
	for i := range 8640 { // one trade every 10 s
		ts := fixtureDay.Add(time.Duration(i) * 10 * time.Second)
		price *= 1 + 0.0015*math.Sin(float64(i)/37) + 0.0004*math.Cos(float64(i)/5)
		side := "s"
		if i%2 == 0 {
			side = "b"
		}
		fmt.Fprintf(&b, "%d.25,%.1f,0.05,%s,m,,%d\n", ts.Unix(), price, side, i+1)
	}
	if err := os.WriteFile(filepath.Join(dir, "XBTEUR.csv"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(t.TempDir(), "cache")
	var out, errOut bytes.Buffer
	if code := runData(context.Background(), []string{"import", "--cache", cache, "--archive", dir}, &out, &errOut); code != 0 {
		t.Fatalf("import fixture: code %d\n%s\n%s", code, out.String(), errOut.String())
	}
	return cache
}

// runArgs returns `run` arguments over the fixture day.
func runArgs(cache, out string) []string {
	return []string{"run", "--markets", "BTC/EUR", "--start", "2026-07-09", "--end", "2026-07-09",
		"--data", cache, "--output", out, "--strategy", "basic_strategy"}
}

// TestCommand covers the backtest command line.
func TestCommand(t *testing.T) {
	t.Run("GIVEN no arguments WHEN run THEN usage is printed and the exit code is 2", func(t *testing.T) {
		code, _, stderr := runCLI(t)
		if code != 2 || !strings.Contains(stderr, "backtest run") || !strings.Contains(stderr, "backtest data") {
			t.Fatalf("code %d stderr %q", code, stderr)
		}
	})

	t.Run("GIVEN an unknown subcommand WHEN run THEN the exit code is 2", func(t *testing.T) {
		if code, _, _ := runCLI(t, "replay"); code != 2 {
			t.Fatalf("code %d", code)
		}
	})

	t.Run("GIVEN the data subcommand WHEN run without arguments THEN it is dispatched to the data command", func(t *testing.T) {
		code, _, stderr := runCLI(t, "data")
		if code != 2 || !strings.Contains(stderr, "import") {
			t.Fatalf("code %d stderr %q", code, stderr)
		}
	})

	for name, args := range map[string][]string{
		"no market list":            {"run", "--start", "2026-07-09", "--end", "2026-07-09"},
		"both markets and universe": {"run", "--markets", "BTC/EUR", "--universe", "top:5", "--start", "2026-07-09", "--end", "2026-07-09"},
		"a malformed date":          {"run", "--markets", "BTC/EUR", "--start", "09/07/2026", "--end", "2026-07-09"},
		"a bad fee":                 {"run", "--markets", "BTC/EUR", "--start", "2026-07-09", "--end", "2026-07-09", "--fees", "cheap"},
		"zero latency":              {"run", "--markets", "BTC/EUR", "--start", "2026-07-09", "--end", "2026-07-09", "--latency", "0s"},
		"a bad universe":            {"run", "--universe", "all", "--start", "2026-07-09", "--end", "2026-07-09"},
		"an old flag":               {"run", "--hours", "48"},
	} {
		t.Run("GIVEN "+name+" WHEN run THEN it is a usage error", func(t *testing.T) {
			if code, _, _ := runCLI(t, args...); code != 2 {
				t.Fatalf("code %d, want 2", code)
			}
		})
	}

	t.Run("GIVEN an LLM strategy WHEN a backtest is asked for THEN it fails with the reason and writes no result", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "res")
		args := append(runArgs(importedCache(t), out), "--strategy", "llm_only_strategy")
		code, _, stderr := runCLI(t, args...)
		if code == 0 || !strings.Contains(stderr, "LLM strategies cannot be backtested") {
			t.Fatalf("code %d stderr %q", code, stderr)
		}
		if _, err := os.Stat(out); !os.IsNotExist(err) {
			t.Fatalf("no result may be written for an LLM strategy: %v", err)
		}
	})

	t.Run("GIVEN a missing data directory WHEN a backtest runs THEN it fails without creating it", func(t *testing.T) {
		data := filepath.Join(t.TempDir(), "nope")
		code, _, _ := runCLI(t, runArgs(data, filepath.Join(t.TempDir(), "res"))...)
		if code != 1 {
			t.Fatalf("code %d, want 1", code)
		}
		if _, err := os.Stat(data); !os.IsNotExist(err) {
			t.Fatal("the data directory must not be created by a run")
		}
	})

	t.Run("GIVEN a cached day WHEN it is backtested twice THEN a summary is printed and both result files are byte-identical", func(t *testing.T) {
		cache := importedCache(t)
		var results [2][]byte
		for i := range results {
			out := filepath.Join(t.TempDir(), "res")
			code, stdout, stderr := runCLI(t, runArgs(cache, out)...)
			if code != 0 || !strings.Contains(stdout, "ending equity") {
				t.Fatalf("code %d\nstdout %s\nstderr %s", code, stdout, stderr)
			}
			raw, err := os.ReadFile(filepath.Join(out, "result.json"))
			if err != nil {
				t.Fatal(err)
			}
			results[i] = raw
		}
		if !bytes.Equal(results[0], results[1]) {
			t.Fatal("two runs of the same inputs differ")
		}
		if !bytes.Contains(results[0], []byte(`"fee_preset": "kraken-base"`)) {
			t.Fatal("the result must record the fee assumption")
		}
		var res struct {
			Provenance []struct {
				Market     string `json:"market"`
				FileSHA256 string `json:"file_sha256"`
				Sources    []struct {
					Source       string    `json:"source"`
					SourceSHA256 string    `json:"source_sha256"`
					ImportedAt   time.Time `json:"imported_at"`
				} `json:"sources"`
			} `json:"data_provenance"`
		}
		if err := json.Unmarshal(results[0], &res); err != nil {
			t.Fatal(err)
		}
		if len(res.Provenance) == 0 || res.Provenance[0].Market != "BTC/EUR" || res.Provenance[0].FileSHA256 == "" ||
			len(res.Provenance[0].Sources) == 0 || !strings.Contains(res.Provenance[0].Sources[0].Source, "XBTEUR.csv") ||
			res.Provenance[0].Sources[0].SourceSHA256 == "" || res.Provenance[0].Sources[0].ImportedAt.IsZero() {
			t.Fatalf("the result must cite the imported data: %+v", res.Provenance)
		}
	})

	t.Run("GIVEN a point-in-time universe WHEN a backtest runs THEN it selects from the cache", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "res")
		code, stdout, stderr := runCLI(t, "run", "--universe", "top:3", "--start", "2026-07-09", "--end", "2026-07-09",
			"--data", importedCache(t), "--output", out, "--universe-every", "6h")
		if code != 0 {
			t.Fatalf("code %d\nstdout %s\nstderr %s", code, stdout, stderr)
		}
	})
}
