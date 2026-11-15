package backtest

import (
	"path/filepath"
	"strings"
	"testing"
)

// writeRunDir runs the report scenario at fee bp and writes its reports.
func writeRunDir(t *testing.T, fee string) string {
	t.Helper()
	reader := twoMarkets(900)
	cfg := testConfig([]string{"BTC/EUR", "ETH/EUR"}, 899)
	cfg.Settings.StartingCapital = 5000
	if err := cfg.Exchange.SetFees(fee); err != nil {
		t.Fatal(err)
	}
	res := mustRun(t, reader, withScript(cfg, randomPlan(5, cfg.Markets, 36, 890, 12), nil))
	rep, err := BuildReport(reader, res)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "fee"+fee)
	if err := WriteReports(dir, rep, []string{"backtest", "run", "--fees", fee}); err != nil {
		t.Fatal(err)
	}
	return dir
}

// writeSummary writes a multi-run summary into a new directory.
func writeSummary(t *testing.T, v any) string {
	t.Helper()
	dir := t.TempDir()
	if err := WriteJSON(filepath.Join(dir, "summary.json"), v); err != nil {
		t.Fatal(err)
	}
	return dir
}

// evidenceDirs writes split, walk-forward and sensitivity summaries; good
// decides whether they meet the documented conditions.
func evidenceDirs(t *testing.T, good bool) []string {
	oos := RunRow{NetPnL: 50, NetReturnPct: 5, RoundTrips: 40}
	if !good {
		oos.NetPnL, oos.NetReturnPct = -5, -0.5
	}
	split := &SplitReport{SchemaVersion: SummarySchemaVersion, Kind: KindSplit, StrategyID: "basic_strategy",
		InSample: RunRow{NetPnL: 80, NetReturnPct: 8, RoundTrips: 60}, OutOfSample: oos}
	walk := &WalkForwardReport{SchemaVersion: SummarySchemaVersion, Kind: KindWalkForward, StrategyID: "basic_strategy",
		Aggregate: WalkAggregate{Windows: 5, Profitable: 4, ProfitablePct: 80, CompoundedReturnPct: 6, RoundTrips: 50}}
	sens := &SensitivityReport{SchemaVersion: SummarySchemaVersion, Kind: KindSensitivity, StrategyID: "basic_strategy",
		Baseline: RunRow{NetPnL: 30}, BaselineFeeBps: 80, Rows: []SensitivityRow{
			{Dimension: DimFees, Value: "80", FeeBps: 80, Row: RunRow{NetPnL: 30}},
			{Dimension: DimFees, Value: "120", FeeBps: 120, Row: RunRow{NetPnL: 2}},
		}}
	return []string{writeSummary(t, split), writeSummary(t, walk), writeSummary(t, sens)}
}

// TestCompare covers `backtest compare`.
func TestCompare(t *testing.T) {
	t.Run("GIVEN two run directories with different fees WHEN compared THEN both rows and only the differing inputs are listed", func(t *testing.T) {
		a, b := writeRunDir(t, "40"), writeRunDir(t, "120")
		rep, err := Compare([]string{a, b})
		if err != nil {
			t.Fatal(err)
		}
		if len(rep.Entries) != 2 || rep.Entries[0].Row == nil || rep.Entries[0].Row.NetReturnPct <= rep.Entries[1].Row.NetReturnPct {
			t.Fatalf("entries %+v", rep.Entries)
		}
		keys := map[string]Difference{}
		for _, d := range rep.Differences {
			keys[d.Key] = d
		}
		if d, ok := keys["exchange.fee_bps"]; !ok || d.Values[0] != "40" || d.Values[1] != "120" {
			t.Fatalf("differences %+v lack the fee", rep.Differences)
		}
		if _, ok := keys["settings.MIN_SIGNAL_CONFIDENCE"]; ok {
			t.Fatal("an equal setting is listed as a difference")
		}
		if rep.Evidence != nil {
			t.Fatal("no robustness summaries were given, so no evidence may be evaluated")
		}
		if !strings.Contains(RenderCompare(rep), "fee_bps") {
			t.Fatal("the table must show the differences")
		}
	})

	t.Run("GIVEN split, walk-forward and fee-stress summaries meeting every condition WHEN compared THEN the evidence statement is made, with the caveat", func(t *testing.T) {
		rep, err := Compare(evidenceDirs(t, true))
		if err != nil {
			t.Fatal(err)
		}
		if rep.Evidence == nil || !rep.Evidence.Met || !strings.Contains(rep.Evidence.Text, "edge") || !strings.Contains(rep.Evidence.Text, "does not guarantee") {
			t.Fatalf("evidence %+v", rep.Evidence)
		}
	})

	t.Run("GIVEN a negative out-of-sample result WHEN compared THEN the conditions are not met and no claim is made", func(t *testing.T) {
		rep, err := Compare(evidenceDirs(t, false))
		if err != nil {
			t.Fatal(err)
		}
		if rep.Evidence == nil || rep.Evidence.Met {
			t.Fatalf("evidence %+v", rep.Evidence)
		}
		for _, w := range forbiddenClaims {
			if strings.Contains(strings.ToLower(rep.Evidence.Text), w) {
				t.Fatalf("an unmet evidence check says %q: %s", w, rep.Evidence.Text)
			}
		}
		if rep.Evidence.Conditions[0].Met {
			t.Fatalf("the out-of-sample condition must fail: %+v", rep.Evidence.Conditions)
		}
	})

	t.Run("GIVEN a directory without a summary WHEN compared THEN it fails", func(t *testing.T) {
		if _, err := Compare([]string{t.TempDir(), t.TempDir()}); err == nil {
			t.Fatal("want an error")
		}
	})
}
