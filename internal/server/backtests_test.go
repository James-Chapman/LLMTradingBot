package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// The fixtures below write small files in the formats of internal/backtest's
// writers (report.go, render_multi.go); the field names and CSV columns are
// copied from there, the package itself is not imported.

// equityHeader, tradeHeader and orderHeader are the CSV columns of
// equity.csv, trades.csv and orders.csv.
const (
	equityHeader = "time,equity,cash,positions_value,peak,drawdown,drawdown_pct,min_equity,open_positions"
	tradeHeader  = "position_id,market,entry_time,exit_time,holding_seconds,qty,entry_reference_price,entry_fill_price,exit_reference_price,exit_fill_price,gross_pnl,latency_drift_cost,spread_cost,impact_cost,rounding_cost,execution_cost,entry_fee,exit_fee,fees,net_pnl,return_pct,exit_reason"
	orderHeader  = "id,market,side,reason,strategy_id,idea_id,position_id,decision_time,fill_time,requested_qty,filled_qty,reference_price,fill_bar_close,fill_price,price_source,half_spread,latency_drift,spread_cost,impact_cost,rounding_cost,participation,notional,fee,fee_rate,realised_pnl,status,reject_reason"
)

// outsideMarker is written outside the results root; no response may contain it.
const outsideMarker = "OUTSIDE-SECRET-7f3a"

// writeFile writes content to root/rel, creating parent directories.
func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// touch sets the modification time of root/rel.
func touch(t *testing.T, root, rel string, when time.Time) {
	t.Helper()
	if err := os.Chtimes(filepath.Join(root, filepath.FromSlash(rel)), when, when); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
}

// runFixture describes one single-run directory.
type runFixture struct {
	strategy   string
	netPct     float64
	netPnL     float64
	roundTrips int
	warning    bool   // reconciliation warning (and an ACCOUNTING WARNING honesty line)
	noCosts    bool   // leave costs.json out
	equityCSV  string // equity.csv body after the header; default two points
	modified   time.Time
}

// writeRun writes a single run's report set into root/rel.
func writeRun(t *testing.T, root, rel string, f runFixture) {
	t.Helper()
	honesty := []string{"NOT THE LIVE STRATEGY: test fixture.", "Period: 2025-01-01 to 2025-03-31."}
	if f.warning {
		honesty = append([]string{"ACCOUNTING WARNING: the exact reconciliation of the order log failed; the numbers below are not trustworthy."}, honesty...)
	}
	summary := map[string]any{
		"summary_schema_version": 1, "kind": "run", "tool_version": "backtest/1.0.0", "strategy_id": f.strategy,
		"start": "2025-01-01T00:00:00Z", "end": "2025-03-31T00:00:00Z", "markets": []string{"BTC/EUR", "ETH/EUR"},
		"honesty":        map[string]any{"lines": honesty, "survivorship": "explicit_list_warning", "statistically_meaningful": false},
		"verdict":        []map[string]string{{"rule": "too_few_trades", "text": "Too few round trips to judge."}, {"rule": "second", "text": "Second line."}},
		"metrics":        map[string]any{"net_pnl": f.netPnL, "net_return_pct": f.netPct, "round_trips": f.roundTrips, "future_metric": 7},
		"reconciliation": map[string]any{"warning": f.warning},
	}
	writeJSONFile(t, root, rel+"/summary.json", summary)
	writeJSONFile(t, root, rel+"/run.json", map[string]any{"tool_version": "backtest/1.0.0", "seed": 1, "strategy_id": f.strategy, "initial_markets": []string{"BTC/EUR"}})
	if !f.noCosts {
		writeJSONFile(t, root, rel+"/costs.json", map[string]any{"definition": "test", "gross_pnl": 12.5, "fees": 1.25})
	}
	writeFile(t, root, rel+"/summary.md", "# Backtest\n\n## Read this first\n\n- <b>not html</b>\n")
	writeFile(t, root, rel+"/result.json", "{}\n")
	equity := f.equityCSV
	if equity == "" {
		equity = "2025-01-01T00:00:00Z,10000,10000,0,10000,0,0,10000,0\n2025-03-31T00:00:00Z,10100,10100,0,10100,0,0,10050,0\n"
	}
	writeFile(t, root, rel+"/equity.csv", equityHeader+"\n"+equity)
	writeFile(t, root, rel+"/trades.csv", tradeHeader+"\n"+
		"p1,BTC/EUR,2025-01-02T00:00:00Z,2025-01-03T00:00:00Z,86400,0.01,50000,50010,51000,50990,10,0.1,0.2,0,0.01,0.31,0.13,0.13,0.26,9.43,1.88,signal\n"+
		"p2,ETH/EUR,2025-01-04T00:00:00Z,2025-01-05T00:00:00Z,86400,1,3000,3001,2900,2899,-100,0,1,0,0,1,0.78,0.75,1.53,-102.53,-3.41,stop_loss\n"+
		"p3,BTC/EUR,2025-02-01T00:00:00Z,2025-03-31T00:00:00Z,5011200,0.02,52000,52010,53000,52990,20,0,0.2,0,0,0.2,0.27,0.27,0.54,19.26,1.85,end_of_run\n")
	writeFile(t, root, rel+"/orders.csv", orderHeader+"\n"+
		"o1,BTC/EUR,buy,signal,basic,i1,p1,2025-01-02T00:00:00Z,2025-01-02T00:00:30Z,0.01,0.01,50000,50005,50010,taker_side_average,0,5,5,0,0,0.01,500.1,0.13,0.0026,,filled,\n"+
		"o2,BTC/EUR,sell,signal,basic,i2,p1,2025-01-03T00:00:00Z,2025-01-03T00:00:30Z,0.01,0.01,51000,50995,50990,taker_side_average,0,5,5,0,0,0.01,509.9,0.13,0.0026,9.43,filled,\n"+
		"o3,ETH/EUR,buy,signal,basic,i3,,2025-01-03T12:00:00Z,2025-01-03T12:00:30Z,5,0,3000,3000,0,,0,0,0,0,0,0,0,0,0.0026,,rejected,insufficient_funds\n"+
		"o4,ETH/EUR,buy,signal,basic,i4,p2,2025-01-04T00:00:00Z,2025-01-04T00:00:30Z,1,1,3000,3000.5,3001,taker_side_average,0,0.5,0.5,0,0,0.02,3001,0.78,0.0026,,filled,\n")
	if !f.modified.IsZero() {
		touch(t, root, rel+"/summary.json", f.modified)
	}
}

// writeMulti writes a multi-run parent of kind with one child run per label.
func writeMulti(t *testing.T, root, rel, kind string, labels []string, modified time.Time) {
	t.Helper()
	row := map[string]any{"label": "headline", "net_pnl": 42.0, "net_return_pct": 0.42, "round_trips": 31, "reconcile_warning": false}
	summary := map[string]any{
		"summary_schema_version": 1, "kind": kind, "strategy_id": "basic",
		"start": "2024-01-01T00:00:00Z", "end": "2025-12-31T00:00:00Z",
		"verdict":   []map[string]string{{"rule": "oos_holds", "text": "The out-of-sample result keeps its sign."}},
		"in_sample": row, "out_of_sample": row, "run": row, "baseline": row,
		"aggregate": map[string]any{"windows": 2, "compounded_return_pct": 1.5, "round_trips": 12},
	}
	writeJSONFile(t, root, rel+"/summary.json", summary)
	writeFile(t, root, rel+"/summary.md", "# Multi\n")
	writeJSONFile(t, root, rel+"/run.json", map[string]any{"kind": kind, "strategy_id": "basic", "markets": []string{"BTC/EUR"}})
	for i, label := range labels {
		writeRun(t, root, fmt.Sprintf("%s/runs/%02d_%s", rel, i+1, label), runFixture{strategy: "basic", netPct: 1, netPnL: 1, roundTrips: 1})
	}
	touch(t, root, rel+"/summary.json", modified)
}

// writeJSONFile writes v as JSON to root/rel.
func writeJSONFile(t *testing.T, root, rel string, v any) {
	t.Helper()
	data, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	writeFile(t, root, rel, string(data)+"\n")
}

// backtestHarness is a loopback harness serving backtest results from dir.
func backtestHarness(t *testing.T, dir string) *harness {
	t.Helper()
	h := newHarness(t, harnessConfig{})
	h.handler = New(h.app, Options{
		FrontendDir:        frontendDir,
		AllowedHosts:       []string{"example.com"},
		FailedLoginDelay:   time.Millisecond,
		BacktestResultsDir: dir,
	})
	return h
}

// listEntries returns the "backtests" array of a list response.
func listEntries(t *testing.T, rec *httptest.ResponseRecorder) (entries []map[string]any, skipped float64) {
	t.Helper()
	var body struct {
		Backtests []map[string]any `json:"backtests"`
		Skipped   float64          `json:"skipped"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Backtests == nil {
		t.Fatalf("expected {backtests: [...]}, got %d %q: %v", rec.Code, rec.Body.String(), err)
	}
	return body.Backtests, body.Skipped
}

// ids returns the id of every entry, in order.
func ids(entries []map[string]any) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, fmt.Sprint(e["id"]))
	}
	return out
}

// TestBacktestList covers GET /api/backtests.
func TestBacktestList(t *testing.T) {
	t.Run("GIVEN single runs, a multi-run parent and a broken directory WHEN listing THEN runs come newest first, children are nested and the broken one is counted", func(t *testing.T) {
		root := t.TempDir()
		base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		writeRun(t, root, "old_run", runFixture{strategy: "basic", netPct: -1.5, netPnL: -150, roundTrips: 4, modified: base})
		writeRun(t, root, "2026/new_run", runFixture{strategy: "trend", netPct: 2.5, netPnL: 250, roundTrips: 40, warning: true, modified: base.Add(2 * time.Hour)})
		writeMulti(t, root, "split_x", "split", []string{"in_sample", "out_of_sample"}, base.Add(time.Hour))
		writeFile(t, root, "broken/run.json", "{}")
		writeFile(t, root, "broken/summary.json", "{not json")
		h := backtestHarness(t, root)

		rec := h.do(http.MethodGet, "/api/backtests", nil)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d %s", rec.Code, rec.Body)
		}
		entries, skipped := listEntries(t, rec)
		if got := strings.Join(ids(entries), ","); got != "2026/new_run,split_x,old_run" {
			t.Fatalf("expected newest first without children or the broken run, got %s", got)
		}
		if skipped != 1 {
			t.Errorf("expected the broken directory to be counted as skipped, got %v", skipped)
		}
		newest := entries[0]
		requireKeys(t, newest, "id", "kind", "strategy", "markets", "start", "end", "net_return_pct", "net_pnl", "round_trips", "verdict", "has_warnings", "modified", "children")
		if newest["kind"] != "run" || newest["strategy"] != "trend" || newest["net_return_pct"] != 2.5 || newest["round_trips"] != 40.0 {
			t.Errorf("single-run entry not taken from summary.json: %v", newest)
		}
		if newest["has_warnings"] != true || entries[2]["has_warnings"] != false {
			t.Errorf("has_warnings must follow the reconciliation warning: %v / %v", newest["has_warnings"], entries[2]["has_warnings"])
		}
		if newest["verdict"] != "Too few round trips to judge." {
			t.Errorf("verdict must be the first verdict line, got %v", newest["verdict"])
		}
		parent := entries[1]
		if parent["kind"] != "split" || parent["net_return_pct"] != 0.42 {
			t.Errorf("split entry must carry its out-of-sample headline: %v", parent)
		}
		children, _ := parent["children"].([]any)
		if len(children) != 2 || children[0] != "split_x/runs/01_in_sample" || children[1] != "split_x/runs/02_out_of_sample" {
			t.Errorf("expected the parent's children in order, got %v", parent["children"])
		}
		if markets, _ := parent["markets"].([]any); len(markets) != 1 || markets[0] != "BTC/EUR" {
			t.Errorf("a multi-run parent's markets come from run.json, got %v", parent["markets"])
		}
	})

	t.Run("GIVEN a results root that does not exist WHEN listing THEN the list is empty and the directory is not created", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "missing")
		h := backtestHarness(t, root)

		rec := h.do(http.MethodGet, "/api/backtests", nil)

		entries, skipped := listEntries(t, rec)
		if rec.Code != http.StatusOK || len(entries) != 0 || skipped != 0 {
			t.Fatalf("expected an empty 200 list, got %d %s", rec.Code, rec.Body)
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Errorf("the results root must not be created")
		}
	})

	t.Run("GIVEN two runs with the same modification time WHEN listing twice THEN the order is the same, by id", func(t *testing.T) {
		root := t.TempDir()
		when := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		writeRun(t, root, "b", runFixture{strategy: "basic", modified: when})
		writeRun(t, root, "a", runFixture{strategy: "basic", modified: when})
		h := backtestHarness(t, root)

		first, _ := listEntries(t, h.do(http.MethodGet, "/api/backtests", nil))
		second, _ := listEntries(t, h.do(http.MethodGet, "/api/backtests", nil))

		if strings.Join(ids(first), ",") != "a,b" || strings.Join(ids(second), ",") != "a,b" {
			t.Fatalf("expected a deterministic order, got %v then %v", ids(first), ids(second))
		}
	})
}

// TestBacktestDetail covers GET /api/backtests/{id...} without a part.
func TestBacktestDetail(t *testing.T) {
	root := t.TempDir()
	writeRun(t, root, "r1", runFixture{strategy: "basic", netPct: 1, roundTrips: 2})
	writeRun(t, root, "nocosts", runFixture{strategy: "basic", noCosts: true})
	writeMulti(t, root, "wf", "walkforward", []string{"w01_test"}, time.Now())
	h := backtestHarness(t, root)

	t.Run("GIVEN a stored run WHEN its detail is requested THEN summary, run and costs are returned as stored", func(t *testing.T) {
		rec := h.do(http.MethodGet, "/api/backtests/r1", nil)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d %s", rec.Code, rec.Body)
		}
		body := decodeObject(t, rec)
		requireKeys(t, body, "id", "kind", "summary", "run", "costs", "children")
		summary, _ := body["summary"].(map[string]any)
		metrics, _ := summary["metrics"].(map[string]any)
		if body["id"] != "r1" || body["kind"] != "run" || metrics["future_metric"] != 7.0 {
			t.Errorf("summary.json must pass through as-is (unknown fields included): %v", body)
		}
		if run, _ := body["run"].(map[string]any); run["seed"] != 1.0 {
			t.Errorf("run.json must pass through, got %v", body["run"])
		}
		if costs, _ := body["costs"].(map[string]any); costs["gross_pnl"] != 12.5 {
			t.Errorf("costs.json must pass through, got %v", body["costs"])
		}
	})

	t.Run("GIVEN a run without costs.json WHEN its detail is requested THEN costs is null", func(t *testing.T) {
		body := decodeObject(t, h.do(http.MethodGet, "/api/backtests/nocosts", nil))

		if v, ok := body["costs"]; !ok || v != nil {
			t.Fatalf("expected costs: null, got %v", body)
		}
	})

	t.Run("GIVEN a multi-run parent WHEN its detail and a child's are requested THEN the parent lists its children and the child is addressable", func(t *testing.T) {
		parent := decodeObject(t, h.do(http.MethodGet, "/api/backtests/wf", nil))
		children, _ := parent["children"].([]any)
		if parent["kind"] != "walkforward" || len(children) != 1 || children[0] != "wf/runs/01_w01_test" {
			t.Fatalf("expected the walk-forward parent with its child, got %v", parent)
		}

		rec := h.do(http.MethodGet, "/api/backtests/wf/runs/01_w01_test", nil)

		if rec.Code != http.StatusOK || decodeObject(t, rec)["kind"] != "run" {
			t.Fatalf("expected the child run, got %d %s", rec.Code, rec.Body)
		}
	})

	t.Run("GIVEN a stored run WHEN summary_md is requested THEN the Markdown is sent as text, not HTML", func(t *testing.T) {
		rec := h.do(http.MethodGet, "/api/backtests/r1?part=summary_md", nil)

		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/markdown") {
			t.Fatalf("expected 200 text/markdown, got %d %q", rec.Code, rec.Header().Get("Content-Type"))
		}
		if !strings.Contains(rec.Body.String(), "## Read this first") || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("expected the stored Markdown with nosniff, got %q", rec.Body.String())
		}
	})

	t.Run("GIVEN a run WHEN an unknown id or a missing file is requested THEN 404", func(t *testing.T) {
		if rec := h.do(http.MethodGet, "/api/backtests/nope", nil); rec.Code != http.StatusNotFound {
			t.Errorf("unknown run: expected 404, got %d", rec.Code)
		}
		if err := os.Remove(filepath.Join(root, "nocosts", "trades.csv")); err != nil {
			t.Fatal(err)
		}
		if rec := h.do(http.MethodGet, "/api/backtests/nocosts?part=trades", nil); rec.Code != http.StatusNotFound {
			t.Errorf("missing trades.csv: expected 404, got %d", rec.Code)
		}
	})
}

// equityRows builds n equity.csv rows at a flat 10000 with overrides by row index.
func equityRows(n int, overrides map[int]float64) string {
	var b strings.Builder
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		equity := 10000.0 + float64(i%3) // a little noise
		if v, ok := overrides[i]; ok {
			equity = v
		}
		fmt.Fprintf(&b, "%s,%g,%g,0,10002,0,0,%g,0\n", start.Add(time.Duration(i)*time.Minute).Format(time.RFC3339), equity, equity, equity)
	}
	return b.String()
}

// equityBody is the equity part's response.
type equityBody struct {
	Columns        []string `json:"columns"`
	Points         [][]any  `json:"points"`
	Downsampled    bool     `json:"downsampled"`
	OriginalPoints int      `json:"original_points"`
	MaxPoints      int      `json:"max_points"`
}

// decodeEquity decodes an equity response.
func decodeEquity(t *testing.T, rec *httptest.ResponseRecorder) equityBody {
	t.Helper()
	var body equityBody
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

// TestBacktestEquity covers ?part=equity.
func TestBacktestEquity(t *testing.T) {
	root := t.TempDir()
	writeRun(t, root, "small", runFixture{strategy: "basic", equityCSV: equityRows(30, nil)})
	writeRun(t, root, "big", runFixture{strategy: "basic", equityCSV: equityRows(1000, map[int]float64{500: 5000, 700: 20000})})
	h := backtestHarness(t, root)

	t.Run("GIVEN a short equity curve WHEN requested THEN every point is returned with typed columns", func(t *testing.T) {
		body := decodeEquity(t, h.do(http.MethodGet, "/api/backtests/small?part=equity", nil))

		if strings.Join(body.Columns, ",") != equityHeader {
			t.Errorf("expected the CSV columns, got %v", body.Columns)
		}
		if len(body.Points) != 30 || body.Downsampled || body.OriginalPoints != 30 {
			t.Fatalf("expected all 30 points, not downsampled, got %d %v %d", len(body.Points), body.Downsampled, body.OriginalPoints)
		}
		if _, ok := body.Points[0][0].(string); !ok {
			t.Errorf("time must stay a string, got %T", body.Points[0][0])
		}
		if _, ok := body.Points[0][1].(float64); !ok {
			t.Errorf("equity must be a number, got %T", body.Points[0][1])
		}
	})

	t.Run("GIVEN a long curve with a spike down and up inside buckets WHEN downsampled THEN first, last and both spikes survive within max_points", func(t *testing.T) {
		body := decodeEquity(t, h.do(http.MethodGet, "/api/backtests/big?part=equity&max_points=50", nil))

		if !body.Downsampled || body.OriginalPoints != 1000 || len(body.Points) > 50 || len(body.Points) < 10 {
			t.Fatalf("expected at most 50 points from 1000, got %d (downsampled %v, original %d)", len(body.Points), body.Downsampled, body.OriginalPoints)
		}
		first, last := body.Points[0][0], body.Points[len(body.Points)-1][0]
		if first != "2025-01-01T00:00:00Z" || last != "2025-01-01T16:39:00Z" {
			t.Errorf("first and last points must be kept, got %v .. %v", first, last)
		}
		var sawLow, sawHigh bool
		previous := ""
		for _, p := range body.Points {
			ts := p[0].(string)
			if ts <= previous {
				t.Fatalf("points must stay in time order: %s after %s", ts, previous)
			}
			previous = ts
			sawLow = sawLow || p[1] == 5000.0
			sawHigh = sawHigh || p[1] == 20000.0
		}
		if !sawLow || !sawHigh {
			t.Errorf("a bucket's minimum and maximum must survive (low %v, high %v)", sawLow, sawHigh)
		}
	})

	t.Run("GIVEN max_points above the cap WHEN requested THEN the cap applies; a bad value is 400", func(t *testing.T) {
		body := decodeEquity(t, h.do(http.MethodGet, "/api/backtests/big?part=equity&max_points=999999", nil))
		if body.MaxPoints != 20000 || len(body.Points) != 1000 {
			t.Errorf("expected max_points capped at 20000 (all 1000 points), got %d and %d points", body.MaxPoints, len(body.Points))
		}
		if body := decodeEquity(t, h.do(http.MethodGet, "/api/backtests/big?part=equity", nil)); body.MaxPoints != 2000 {
			t.Errorf("expected the default max_points 2000, got %d", body.MaxPoints)
		}
		for _, bad := range []string{"abc", "1", "-5"} {
			if rec := h.do(http.MethodGet, "/api/backtests/big?part=equity&max_points="+bad, nil); rec.Code != http.StatusBadRequest {
				t.Errorf("max_points=%s: expected 400, got %d", bad, rec.Code)
			}
		}
	})

	t.Run("GIVEN a CSV above the size cap WHEN requested THEN 413", func(t *testing.T) {
		saved := maxBacktestCSVBytes
		maxBacktestCSVBytes = 100
		t.Cleanup(func() { maxBacktestCSVBytes = saved })

		rec := h.do(http.MethodGet, "/api/backtests/big?part=equity", nil)

		if rec.Code != http.StatusRequestEntityTooLarge || decodeObject(t, rec)["detail"] == "" {
			t.Fatalf("expected 413 with a detail, got %d %s", rec.Code, rec.Body)
		}
	})
}

// rowsBody is the trades/orders part's response.
type rowsBody struct {
	Columns []string         `json:"columns"`
	Rows    []map[string]any `json:"rows"`
	Total   int              `json:"total"`
	Limit   int              `json:"limit"`
	Offset  int              `json:"offset"`
}

// decodeRows decodes a trades or orders response.
func decodeRows(t *testing.T, rec *httptest.ResponseRecorder) rowsBody {
	t.Helper()
	var body rowsBody
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Rows == nil {
		t.Fatalf("decode: %v %s", err, rec.Body)
	}
	return body
}

// TestBacktestTradesAndOrders covers ?part=trades and ?part=orders.
func TestBacktestTradesAndOrders(t *testing.T) {
	root := t.TempDir()
	writeRun(t, root, "r1", runFixture{strategy: "basic"})
	h := backtestHarness(t, root)

	t.Run("GIVEN three trades WHEN a page is requested THEN that page is returned with the total and typed values", func(t *testing.T) {
		body := decodeRows(t, h.do(http.MethodGet, "/api/backtests/r1?part=trades&limit=2&offset=1", nil))

		if body.Total != 3 || len(body.Rows) != 2 || body.Limit != 2 || body.Offset != 1 {
			t.Fatalf("expected rows 2-3 of 3, got %d rows of %d (limit %d offset %d)", len(body.Rows), body.Total, body.Limit, body.Offset)
		}
		row := body.Rows[0]
		if row["position_id"] != "p2" || row["market"] != "ETH/EUR" || row["exit_reason"] != "stop_loss" {
			t.Errorf("text columns must stay strings: %v", row)
		}
		if row["net_pnl"] != -102.53 || row["holding_seconds"] != 86400.0 {
			t.Errorf("numeric columns must be numbers: %v", row)
		}
		if strings.Join(body.Columns, ",") != tradeHeader {
			t.Errorf("expected the CSV columns, got %v", body.Columns)
		}
	})

	t.Run("GIVEN trades WHEN no limit is given THEN the default 500 applies and a large limit is capped", func(t *testing.T) {
		if body := decodeRows(t, h.do(http.MethodGet, "/api/backtests/r1?part=trades", nil)); body.Limit != 500 || len(body.Rows) != 3 {
			t.Errorf("expected the default limit 500 and all rows, got %d / %d", body.Limit, len(body.Rows))
		}
		if body := decodeRows(t, h.do(http.MethodGet, "/api/backtests/r1?part=trades&limit=9999999", nil)); body.Limit != 5000 {
			t.Errorf("expected limit capped at 5000, got %d", body.Limit)
		}
		if body := decodeRows(t, h.do(http.MethodGet, "/api/backtests/r1?part=trades&offset=10", nil)); body.Total != 3 || len(body.Rows) != 0 {
			t.Errorf("an offset past the end must give no rows and the total, got %d / %d", len(body.Rows), body.Total)
		}
		for _, bad := range []string{"limit=abc", "limit=0", "offset=-1", "offset=x"} {
			if rec := h.do(http.MethodGet, "/api/backtests/r1?part=trades&"+bad, nil); rec.Code != http.StatusBadRequest {
				t.Errorf("%s: expected 400, got %d", bad, rec.Code)
			}
		}
	})

	t.Run("GIVEN orders with one rejection WHEN filtered by status THEN only matching orders count, empty cells are null", func(t *testing.T) {
		all := decodeRows(t, h.do(http.MethodGet, "/api/backtests/r1?part=orders", nil))
		rejected := decodeRows(t, h.do(http.MethodGet, "/api/backtests/r1?part=orders&status=rejected", nil))

		if all.Total != 4 || rejected.Total != 1 || len(rejected.Rows) != 1 {
			t.Fatalf("expected 4 orders and 1 rejected, got %d and %d", all.Total, rejected.Total)
		}
		row := rejected.Rows[0]
		if row["id"] != "o3" || row["reject_reason"] != "insufficient_funds" || row["requested_qty"] != 5.0 {
			t.Errorf("unexpected rejected order %v", row)
		}
		if v, ok := row["realised_pnl"]; !ok || v != nil {
			t.Errorf("an empty numeric cell must be null, got %v", row["realised_pnl"])
		}
		if row["position_id"] != "" {
			t.Errorf("an empty text cell must stay an empty string, got %v", row["position_id"])
		}
	})
}

// TestBacktestPathSafety covers ids and parts that try to leave the results root.
func TestBacktestPathSafety(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "results")
	writeRun(t, root, "r1", runFixture{strategy: "basic"})
	// A complete run directory outside the root: reaching it would be a leak.
	writeRun(t, base, "outside", runFixture{strategy: outsideMarker})
	writeFile(t, base, "outside/summary.md", outsideMarker)
	h := backtestHarness(t, root)

	// noLeak fails when a response names the outside run or a filesystem path.
	noLeak := func(t *testing.T, target string, rec *httptest.ResponseRecorder) {
		t.Helper()
		body := rec.Body.String()
		if strings.Contains(body, outsideMarker) || strings.Contains(body, base) || strings.Contains(body, filepath.ToSlash(base)) {
			t.Errorf("%s: response leaks outside data or a path: %s", target, body)
		}
	}

	t.Run("GIVEN ids that try to escape the root WHEN requested THEN each is refused without leaking anything", func(t *testing.T) {
		targets := []string{
			"/api/backtests/..%2Foutside",
			"/api/backtests/r1%2F..%2F..%2Foutside",
			"/api/backtests/%2e%2e%2Foutside",
			"/api/backtests/%2e%2e",
			"/api/backtests/r1/%2e%2e/%2e%2e/outside",
			"/api/backtests/..%5Coutside",
			"/api/backtests/r1%5C..%5C..%5Coutside",
			"/api/backtests/%2F" + strings.TrimPrefix(filepath.ToSlash(filepath.Join(base, "outside")), "/"),
			"/api/backtests/" + strings.ReplaceAll(filepath.ToSlash(filepath.Join(base, "outside")), ":", "%3A"),
			"/api/backtests/C:%2Fwindows",
			"/api/backtests/C:",
			"/api/backtests/r1%00",
			"/api/backtests/r1%00/x",
			// "/api/backtests/." is cleaned by the mux to the list itself, which is harmless.
			"/api/backtests/r1/./",
			"/api/backtests/NUL",
		}
		for _, target := range targets {
			for _, part := range []string{"", "?part=summary_md", "?part=equity"} {
				// ServeMux redirects a non-canonical path (".", "..", "//")
				// to its cleaned form; follow it so the final answer is checked.
				next := target + part
				var rec *httptest.ResponseRecorder
				for hop := 0; hop < 3; hop++ {
					rec = h.do(http.MethodGet, next, nil)
					noLeak(t, next, rec)
					if rec.Code != http.StatusMovedPermanently && rec.Code != http.StatusTemporaryRedirect && rec.Code != http.StatusPermanentRedirect {
						break
					}
					next = rec.Header().Get("Location")
				}
				if rec.Code != http.StatusNotFound {
					t.Errorf("%s%s: expected 404 in the end, got %d %s", target, part, rec.Code, rec.Body)
				}
			}
		}
	})

	t.Run("GIVEN an id that the mux would clean or redirect WHEN checked by the handler itself THEN it is still refused", func(t *testing.T) {
		// The handler must not rely on ServeMux path cleaning.
		refused := []string{"", ".", "..", "../outside", "r1/../../outside", "r1/..", "./r1", "r1/", "/r1", "//r1",
			`..\outside`, `r1\..\..\outside`, "C:", "C:/windows", "c:outside", "r1\x00", "r1\x00/x", "r1/\x01", "NUL", "r1/CON", strings.Repeat("a", 600)}
		for _, id := range refused {
			if validBacktestID(id) {
				t.Errorf("id %q must be refused", id)
			}
		}
		for _, id := range []string{"r1", "2026/new_run", "split_x/runs/01_in_sample", "a.b-c=d_e"} {
			if !validBacktestID(id) {
				t.Errorf("id %q must be accepted", id)
			}
		}
	})

	t.Run("GIVEN a symlink inside the root to a run outside WHEN requested THEN 404 and the list ignores it", func(t *testing.T) {
		link := filepath.Join(root, "link")
		if err := os.Symlink(filepath.Join(base, "outside"), link); err != nil {
			t.Skipf("cannot create a symlink here: %v", err)
		}
		t.Cleanup(func() { _ = os.Remove(link) })

		for _, target := range []string{"/api/backtests/link", "/api/backtests/link?part=summary_md", "/api/backtests/link?part=trades"} {
			rec := h.do(http.MethodGet, target, nil)
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s: expected 404, got %d", target, rec.Code)
			}
			noLeak(t, target, rec)
		}
		rec := h.do(http.MethodGet, "/api/backtests", nil)
		noLeak(t, "/api/backtests", rec)
		if entries, _ := listEntries(t, rec); strings.Join(ids(entries), ",") != "r1" {
			t.Errorf("the list must not follow the symlink, got %v", ids(entries))
		}
	})

	t.Run("GIVEN a Windows directory junction inside the root to a run outside WHEN requested THEN 404 and the list ignores it", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			t.Skip("directory junctions are Windows-only")
		}
		link := filepath.Join(root, "junction")
		// mklink /J needs no special privilege, unlike a symlink.
		if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, filepath.Join(base, "outside")).CombinedOutput(); err != nil {
			t.Skipf("cannot create a junction here: %v %s", err, out)
		}
		t.Cleanup(func() { _ = os.Remove(link) })

		for _, target := range []string{"/api/backtests/junction", "/api/backtests/junction?part=summary_md", "/api/backtests/junction?part=equity"} {
			rec := h.do(http.MethodGet, target, nil)
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s: expected 404, got %d", target, rec.Code)
			}
			noLeak(t, target, rec)
		}
		rec := h.do(http.MethodGet, "/api/backtests", nil)
		noLeak(t, "/api/backtests", rec)
		if entries, _ := listEntries(t, rec); slices.Contains(ids(entries), "junction") {
			t.Errorf("the list must not follow the junction, got %v", ids(entries))
		}
	})

	t.Run("GIVEN a run whose summary.md is a symlink to a file outside WHEN summary_md is requested THEN 404", func(t *testing.T) {
		writeRun(t, root, "r2", runFixture{strategy: "basic"})
		md := filepath.Join(root, "r2", "summary.md")
		if err := os.Remove(md); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(base, "outside", "summary.md"), md); err != nil {
			t.Skipf("cannot create a symlink here: %v", err)
		}

		rec := h.do(http.MethodGet, "/api/backtests/r2?part=summary_md", nil)

		if rec.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d", rec.Code)
		}
		noLeak(t, "r2 summary_md", rec)
	})

	t.Run("GIVEN an id naming a file instead of a directory WHEN requested THEN 404", func(t *testing.T) {
		for _, target := range []string{"/api/backtests/r1/summary.json", "/api/backtests/r1/equity.csv?part=equity", "/api/backtests/r1/result.json?part=summary_md"} {
			if rec := h.do(http.MethodGet, target, nil); rec.Code != http.StatusNotFound {
				t.Errorf("%s: expected 404, got %d", target, rec.Code)
			}
		}
	})

	t.Run("GIVEN a run WHEN a part outside the allow-list is requested THEN 400 without reading anything", func(t *testing.T) {
		for _, part := range []string{"result", "result.json", "run", "../../outside/summary.md", "summary.md", "", "EQUITY"} {
			rec := h.do(http.MethodGet, "/api/backtests/r1?part="+part, nil)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("part=%q: expected 400, got %d", part, rec.Code)
			}
			noLeak(t, "part="+part, rec)
		}
	})
}

// TestBacktestAccess covers the session check on the backtest routes.
func TestBacktestAccess(t *testing.T) {
	root := t.TempDir()
	writeRun(t, root, "r1", runFixture{strategy: "basic"})

	t.Run("GIVEN a bot bound to 0.0.0.0 WHEN the backtest API is called without a session THEN 401; with the token THEN 200", func(t *testing.T) {
		h, token := lanHarness(t)
		h.handler = New(h.app, Options{FrontendDir: frontendDir, AllowedHosts: []string{"example.com"}, Access: token, FailedLoginDelay: time.Millisecond, BacktestResultsDir: root})

		for _, target := range []string{"/api/backtests", "/api/backtests/r1", "/api/backtests/r1?part=equity"} {
			if rec := h.send(http.MethodGet, target, "", nil); rec.Code != http.StatusUnauthorized {
				t.Errorf("%s without a session: expected 401, got %d", target, rec.Code)
			}
			if rec := h.send(http.MethodGet, target, "", map[string]string{"Authorization": "Bearer " + token.Token()}); rec.Code != http.StatusOK {
				t.Errorf("%s with the token: expected 200, got %d", target, rec.Code)
			}
		}
	})

	t.Run("GIVEN a loopback-bound bot WHEN the backtest API is called without a session THEN 200", func(t *testing.T) {
		h := backtestHarness(t, root)

		if rec := h.do(http.MethodGet, "/api/backtests/r1", nil); rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
	})
}
