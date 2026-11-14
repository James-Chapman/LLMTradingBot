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

// importedDays imports days of BTC/EUR trades (one every 10 s, alternating
// sides) starting on fixtureDay and returns the cache directory.
func importedDays(t *testing.T, days int) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "Kraken_Trades_2026Q3")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("timestamp,price,volume,type,order_type,misc,trade_id\n")
	price := 50000.0
	for i := range days * 8640 {
		ts := fixtureDay.Add(time.Duration(i) * 10 * time.Second)
		price *= 1 + 0.0015*math.Sin(float64(i)/37) + 0.0004*math.Cos(float64(i)/5) + 0.00001*math.Sin(float64(i)/2000)
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

// readTree returns every file under dir by relative path.
func readTree(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		rel, _ := filepath.Rel(dir, path)
		files[filepath.ToSlash(rel)] = raw
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// summaryKind reads the kind of dir/summary.json.
func summaryKind(t *testing.T, dir string) string {
	t.Helper()
	var s struct {
		Kind string `json:"kind"`
	}
	raw, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil || json.Unmarshal(raw, &s) != nil {
		t.Fatalf("summary.json in %s: %v", dir, err)
	}
	return s.Kind
}

// threeDays are the common arguments over a three-day fixture.
func threeDays(cache, out string) []string {
	return []string{"--markets", "BTC/EUR", "--start", "2026-07-09", "--end", "2026-07-11", "--data", cache, "--output", out}
}

// TestRunReports covers the report files of `backtest run`.
func TestRunReports(t *testing.T) {
	t.Run("GIVEN a cached day WHEN backtested THEN every report file is written, the reconciliation passes and the summary starts with the honesty block", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "res")
		code, stdout, stderr := runCLI(t, runArgs(importedCache(t), out)...)
		if code != 0 {
			t.Fatalf("code %d\n%s\n%s", code, stdout, stderr)
		}
		for _, name := range []string{"result.json", "summary.json", "summary.md", "trades.csv", "orders.csv", "equity.csv", "costs.json", "run.json"} {
			if _, err := os.Stat(filepath.Join(out, name)); err != nil {
				t.Fatalf("%s not written: %v", name, err)
			}
		}
		md, _ := os.ReadFile(filepath.Join(out, "summary.md"))
		if i := bytes.Index(md, []byte("## Read this first")); i < 0 || i > bytes.Index(md, []byte("## Results")) {
			t.Fatalf("summary.md must open with the honesty block:\n%s", md)
		}
		var run struct {
			CommandLine []string `json:"command_line"`
		}
		raw, _ := os.ReadFile(filepath.Join(out, "run.json"))
		if json.Unmarshal(raw, &run) != nil || len(run.CommandLine) < 3 || run.CommandLine[1] != "run" {
			t.Fatalf("run.json command line %v", run.CommandLine)
		}
		if !strings.Contains(stdout, "one historical path") || !strings.Contains(stdout, "reconciliation") {
			t.Fatalf("the printed summary must carry the honesty block and the reconciliation:\n%s", stdout)
		}
	})

	t.Run("GIVEN a reconciliation warning WHEN deciding the exit THEN it fails unless explicitly allowed", func(t *testing.T) {
		if reconcileError(true, false) == nil || reconcileError(true, true) != nil || reconcileError(false, false) != nil {
			t.Fatal("a reconciliation warning must fail the command unless --allow-reconcile-warning")
		}
	})
}

// TestRobustnessCommands covers split, walkforward, regimes, sensitivity and compare.
func TestRobustnessCommands(t *testing.T) {
	cache := importedDays(t, 3)

	t.Run("GIVEN a split date WHEN split runs THEN both periods are reported side by side with their runs", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "split")
		code, stdout, stderr := runCLI(t, append([]string{"split", "--split", "2026-07-11"}, threeDays(cache, out)...)...)
		if code != 0 {
			t.Fatalf("code %d\n%s\n%s", code, stdout, stderr)
		}
		if summaryKind(t, out) != "split" {
			t.Fatal("wrong summary kind")
		}
		for _, d := range []string{"01_in_sample", "02_out_of_sample"} {
			if summaryKind(t, filepath.Join(out, "runs", d)) != "run" {
				t.Fatalf("run %s missing", d)
			}
		}
		md, _ := os.ReadFile(filepath.Join(out, "summary.md"))
		if !bytes.Contains(md, []byte("may only be chosen using the in-sample period")) {
			t.Fatalf("the split summary must say parameters come from the in-sample period:\n%s", md)
		}
		if !strings.Contains(stderr, "[2/2]") {
			t.Fatalf("progress not shown: %q", stderr)
		}
	})

	t.Run("GIVEN train and test lengths WHEN walkforward runs THEN each window's test is reported", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "wf")
		code, stdout, stderr := runCLI(t, append([]string{"walkforward", "--train", "1d", "--test", "1d"}, threeDays(cache, out)...)...)
		if code != 0 {
			t.Fatalf("code %d\n%s\n%s", code, stdout, stderr)
		}
		var s struct {
			Kind    string `json:"kind"`
			Windows []any  `json:"windows"`
		}
		raw, _ := os.ReadFile(filepath.Join(out, "summary.json"))
		if json.Unmarshal(raw, &s) != nil || s.Kind != "walkforward" || len(s.Windows) != 2 {
			t.Fatalf("summary %s", raw)
		}
	})

	t.Run("GIVEN day buckets WHEN regimes runs THEN one run is segmented", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "rg")
		code, stdout, stderr := runCLI(t, append([]string{"regimes", "--bucket", "day"}, threeDays(cache, out)...)...)
		if code != 0 || summaryKind(t, out) != "regimes" {
			t.Fatalf("code %d\n%s\n%s", code, stdout, stderr)
		}
		entries, _ := os.ReadDir(filepath.Join(out, "runs"))
		if len(entries) != 1 {
			t.Fatalf("regimes must use one continuous run, found %d", len(entries))
		}
	})

	t.Run("GIVEN a sensitivity sweep WHEN run with 1 and 3 workers THEN every output file is byte-identical", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "sens")
		var trees [2]map[string][]byte
		for i, n := range []string{"1", "3"} {
			args := append([]string{"sensitivity", "--dims", "fees,latency", "--fees-list", "26,120", "--latency-list", "2", "--parallel", n}, threeDays(cache, out)...)
			code, stdout, stderr := runCLI(t, args...)
			if code != 0 {
				t.Fatalf("code %d\n%s\n%s", code, stdout, stderr)
			}
			trees[i] = readTree(t, out)
			if err := os.RemoveAll(out); err != nil {
				t.Fatal(err)
			}
		}
		if len(trees[0]) < 3*8+3 {
			t.Fatalf("only %d files written", len(trees[0]))
		}
		for name, a := range trees[0] {
			if !bytes.Equal(a, trees[1][name]) {
				t.Fatalf("%s differs with the number of workers", name)
			}
		}
	})

	t.Run("GIVEN two result directories WHEN compared THEN a table and JSON are printed", func(t *testing.T) {
		a, b := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
		for dir, fee := range map[string]string{a: "26", b: "80"} {
			if code, _, stderr := runCLI(t, append([]string{"run", "--fees", fee}, threeDays(cache, dir)...)...); code != 0 {
				t.Fatalf("run: %s", stderr)
			}
		}
		code, stdout, _ := runCLI(t, "compare", a, b)
		if code != 0 || !strings.Contains(stdout, "exchange.fee_bps") {
			t.Fatalf("code %d\n%s", code, stdout)
		}
		code, stdout, _ = runCLI(t, "compare", "--json", a, b)
		var rep struct {
			Kind    string `json:"kind"`
			Entries []any  `json:"entries"`
		}
		if code != 0 || json.Unmarshal([]byte(stdout), &rep) != nil || rep.Kind != "compare" || len(rep.Entries) != 2 {
			t.Fatalf("code %d json %s", code, stdout)
		}
	})

	for name, args := range map[string][]string{
		"walkforward without --train":      {"walkforward", "--test", "1d"},
		"split with a date and a fraction": {"split", "--split", "2026-07-10", "--oos-fraction", "0.3"},
		"sensitivity with a bad --vary":    {"sensitivity", "--vary", "NOPE"},
		"regimes with a bad bucket":        {"regimes", "--bucket", "year"},
		"compare without directories":      {"compare"},
	} {
		t.Run("GIVEN "+name+" WHEN run THEN it is a usage error", func(t *testing.T) {
			full := args
			if args[0] != "compare" {
				full = append(append([]string{}, args...), threeDays(cache, filepath.Join(t.TempDir(), "x"))...)
			}
			if code, _, _ := runCLI(t, full...); code != 2 {
				t.Fatalf("code %d, want 2", code)
			}
		})
	}
}
