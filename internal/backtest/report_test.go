package backtest

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// reportRun replays two random-walk markets with a random plan and builds its report.
func reportRun(t *testing.T) (*Report, memReader) {
	t.Helper()
	reader := twoMarkets(900)
	cfg := testConfig([]string{"BTC/EUR", "ETH/EUR"}, 899)
	cfg.Settings.StartingCapital = 5000
	cfg.EquityEvery = 10 * time.Minute
	res := mustRun(t, reader, withScript(cfg, randomPlan(5, cfg.Markets, 36, 890, 12), nil))
	rep, err := BuildReport(reader, res)
	if err != nil {
		t.Fatal(err)
	}
	return rep, reader
}

// readCSV reads a CSV file into a header-keyed list of rows.
func readCSV(t *testing.T, path string) []map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatalf("%s does not parse as CSV: %v", path, err)
	}
	var out []map[string]string
	for _, r := range rows[1:] {
		m := map[string]string{}
		for i, h := range rows[0] {
			m[h] = r[i]
		}
		out = append(out, m)
	}
	return out
}

// num parses a CSV number.
func num(t *testing.T, s string) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatalf("not a number: %q", s)
	}
	return v
}

// TestReports covers the files `backtest run` writes.
func TestReports(t *testing.T) {
	t.Run("GIVEN the same run WHEN its reports are written twice THEN every file is byte-identical", func(t *testing.T) {
		var dirs [2]string
		for i := range dirs {
			rep, _ := reportRun(t)
			dirs[i] = filepath.Join(t.TempDir(), "out")
			if err := WriteReports(dirs[i], rep, []string{"backtest", "run"}); err != nil {
				t.Fatal(err)
			}
		}
		for _, name := range ReportFiles {
			a, errA := os.ReadFile(filepath.Join(dirs[0], name))
			b, errB := os.ReadFile(filepath.Join(dirs[1], name))
			if errA != nil || errB != nil {
				t.Fatalf("%s missing: %v %v", name, errA, errB)
			}
			if !bytes.Equal(a, b) {
				t.Fatalf("%s differs between two identical runs", name)
			}
		}
	})

	t.Run("GIVEN written reports WHEN the CSV files are parsed back THEN they match the JSON result", func(t *testing.T) {
		rep, _ := reportRun(t)
		dir := t.TempDir()
		if err := WriteReports(dir, rep, nil); err != nil {
			t.Fatal(err)
		}
		var res Result
		raw, _ := os.ReadFile(filepath.Join(dir, "result.json"))
		if err := json.Unmarshal(raw, &res); err != nil {
			t.Fatal(err)
		}

		trades := readCSV(t, filepath.Join(dir, "trades.csv"))
		if len(trades) != len(res.Trades) || len(trades) < 3 {
			t.Fatalf("%d trade rows for %d trades", len(trades), len(res.Trades))
		}
		for i, row := range trades {
			tr := res.Trades[i]
			if row["position_id"] != tr.PositionID || num(t, row["net_pnl"]) != tr.PnL || num(t, row["qty"]) != tr.Qty ||
				num(t, row["entry_fill_price"]) != tr.EntryPrice || num(t, row["exit_fill_price"]) != tr.ExitPrice || row["exit_reason"] != tr.ExitReason {
				t.Fatalf("trade row %v does not match %+v", row, tr)
			}
			bridge := num(t, row["gross_pnl"]) - num(t, row["execution_cost"]) - num(t, row["fees"])
			if !near(bridge, tr.PnL, 1e-9) {
				t.Fatalf("trade row gross - costs - fees = %v, net %v", bridge, tr.PnL)
			}
			parts := num(t, row["latency_drift_cost"]) + num(t, row["spread_cost"]) + num(t, row["impact_cost"]) + num(t, row["rounding_cost"])
			if !near(parts, num(t, row["execution_cost"]), 1e-9) {
				t.Fatalf("trade cost parts %v != execution %v", parts, row["execution_cost"])
			}
		}

		orders := readCSV(t, filepath.Join(dir, "orders.csv"))
		if len(orders) != len(res.Orders) {
			t.Fatalf("%d order rows for %d orders", len(orders), len(res.Orders))
		}
		rejected := 0
		for i, row := range orders {
			o := res.Orders[i]
			if row["id"] != o.ID || row["status"] != o.Status || row["reject_reason"] != o.RejectReason || num(t, row["fill_price"]) != o.FillPrice || num(t, row["fee"]) != o.Fee {
				t.Fatalf("order row %v does not match %+v", row, o)
			}
			if o.Status != StatusFilled {
				rejected++
			}
		}

		equity := readCSV(t, filepath.Join(dir, "equity.csv"))
		if len(equity) != len(res.EquityCurve) {
			t.Fatalf("%d equity rows for %d points", len(equity), len(res.EquityCurve))
		}
		for i, row := range equity {
			p := res.EquityCurve[i]
			if num(t, row["equity"]) != p.Equity || num(t, row["cash"]) != p.Cash || num(t, row["drawdown"]) != p.Peak-p.Equity {
				t.Fatalf("equity row %v does not match %+v", row, p)
			}
		}

		var summary map[string]json.RawMessage
		raw, _ = os.ReadFile(filepath.Join(dir, "summary.json"))
		if err := json.Unmarshal(raw, &summary); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"summary_schema_version", "kind", "honesty", "metrics", "bridge", "costs", "benchmarks", "reconciliation", "signals", "verdict"} {
			if _, ok := summary[key]; !ok {
				t.Fatalf("summary.json lacks %q", key)
			}
		}
		var metrics Metrics
		if err := json.Unmarshal(summary["metrics"], &metrics); err != nil || metrics.RoundTrips != len(trades) {
			t.Fatalf("summary metrics %+v (%v), want %d round trips", metrics, err, len(trades))
		}
		var run RunInfo
		raw, _ = os.ReadFile(filepath.Join(dir, "run.json"))
		if err := json.Unmarshal(raw, &run); err != nil || run.Settings["MIN_SIGNAL_CONFIDENCE"] == "" || run.Exchange.FeeBps != 80 || len(run.Assumptions) == 0 {
			t.Fatalf("run.json %+v (%v)", run, err)
		}
	})

	t.Run("GIVEN a report WHEN the human summary is rendered THEN the honesty block comes first and states the limits", func(t *testing.T) {
		rep, _ := reportRun(t)
		md := RenderSummary(rep)
		first := strings.Index(md, "## Read this first")
		results := strings.Index(md, "## Results")
		if first < 0 || results < first {
			t.Fatalf("the honesty block must precede the results:\n%s", md)
		}
		head := md[first:results]
		for _, want := range []string{"one historical path", "Survivorship", "fee tier", "in-sample", "not statistically meaningful", "under 90 days"} {
			if !strings.Contains(head, want) {
				t.Errorf("honesty block lacks %q:\n%s", want, head)
			}
		}
	})
}
