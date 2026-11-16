package backtest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/histdata"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy"
)

// fakeCheck returns a data check that reports cov for its markets and
// records the range it was asked about.
func fakeCheck(cov map[string]histdata.RangeCoverage, counts map[string]int, asked *[2]time.Time) func([]string, time.Time, time.Time) (histdata.RangeReport, error) {
	return func(markets []string, from, to time.Time) (histdata.RangeReport, error) {
		if asked != nil {
			*asked = [2]time.Time{from, to}
		}
		rep := histdata.RangeReport{From: from, To: to, Counts: counts, Findings: []histdata.Finding{}}
		for _, m := range markets {
			c, ok := cov[m]
			if !ok {
				c = histdata.RangeCoverage{Market: m, HasData: true}
			}
			rep.Markets = append(rep.Markets, c)
		}
		return rep, nil
	}
}

// TestDataCoverageCheck covers the check of the data cache before a run.
func TestDataCoverageCheck(t *testing.T) {
	t.Run("GIVEN a market missing a month inside its coverage WHEN a run is asked for THEN it refuses, naming the market and month, over the pre-roll and the run", func(t *testing.T) {
		cfg := testConfig([]string{"BTC/EUR"}, 79)
		var asked [2]time.Time
		cfg.CheckData = fakeCheck(map[string]histdata.RangeCoverage{"BTC/EUR": {Market: "BTC/EUR", HasData: true, MissingMonths: []string{"2025-12"}}}, nil, &asked)
		res, err := Run(context.Background(), oneMarket(flatSeries(80, 100)), cfg)
		if err == nil || res != nil || !strings.Contains(err.Error(), "BTC/EUR") || !strings.Contains(err.Error(), "2025-12") || !strings.Contains(err.Error(), "--allow-data-gaps") {
			t.Fatalf("want a refusal naming BTC/EUR 2025-12 and the override, got %v", err)
		}
		if !asked[0].Equal(cfg.Start.Add(-PreRoll)) || !asked[1].Equal(cfg.End) {
			t.Fatalf("checked %v, want [start - pre-roll, end]", asked)
		}
	})

	t.Run("GIVEN a market-month with an error-level finding WHEN a run is asked for THEN it refuses by default", func(t *testing.T) {
		cfg := testConfig([]string{"BTC/EUR"}, 79)
		cfg.CheckData = fakeCheck(map[string]histdata.RangeCoverage{"BTC/EUR": {Market: "BTC/EUR", HasData: true, ErrorMonths: []string{"2026-01"}}}, nil, nil)
		if _, err := Run(context.Background(), oneMarket(flatSeries(80, 100)), cfg); err == nil || !strings.Contains(err.Error(), "2026-01") {
			t.Fatalf("want a refusal naming 2026-01, got %v", err)
		}
	})

	t.Run("GIVEN gaps allowed WHEN the run goes ahead THEN the missing month and the error month are recorded as warnings and shown first in the summary", func(t *testing.T) {
		cfg := testConfig([]string{"BTC/EUR"}, 79)
		cfg.AllowDataGaps = true
		cfg.CheckData = fakeCheck(map[string]histdata.RangeCoverage{"BTC/EUR": {Market: "BTC/EUR", HasData: true, MissingMonths: []string{"2025-12"}, ErrorMonths: []string{"2026-01"}}}, nil, nil)
		reader := oneMarket(flatSeries(80, 100))
		res := mustRun(t, reader, cfg)
		if res.DataCheck == nil || len(res.DataWarnings) != 2 {
			t.Fatalf("data check %+v warnings %v, want the check recorded with two warnings", res.DataCheck, res.DataWarnings)
		}
		rep, err := BuildReport(reader, res)
		if err != nil {
			t.Fatal(err)
		}
		if !containsText(rep.Honesty.Lines, "2025-12") || !containsText(rep.Honesty.Lines, "2026-01") {
			t.Fatalf("honesty %v must show both warnings", rep.Honesty.Lines)
		}
		if !strings.Contains(verdictRules(rep.Verdict)[0], RuleDataWarning) {
			t.Fatalf("verdict %v, want the data warning first", rep.Verdict)
		}
	})

	t.Run("GIVEN a pair listed after the start and data with duplicate trades WHEN replayed THEN the late coverage and the duplicates are visible but do not stop the run", func(t *testing.T) {
		cfg := testConfig([]string{"BTC/EUR"}, 79)
		cfg.CheckData = fakeCheck(map[string]histdata.RangeCoverage{"BTC/EUR": {Market: "BTC/EUR", HasData: true, StartsLate: true, First: t0}},
			map[string]int{histdata.KindDuplicateID: 7}, nil)
		reader := oneMarket(flatSeries(80, 100))
		res := mustRun(t, reader, cfg)
		rep, err := BuildReport(reader, res)
		if err != nil {
			t.Fatal(err)
		}
		if !containsText(rep.Honesty.Lines, "starts") || !containsText(rep.Honesty.Lines, "7 duplicate") {
			t.Fatalf("honesty %v must show the late start and the duplicates", rep.Honesty.Lines)
		}
	})

	t.Run("GIVEN no data check WHEN replayed THEN the summary says the coverage was not checked", func(t *testing.T) {
		reader := oneMarket(flatSeries(80, 100))
		res := mustRun(t, reader, testConfig([]string{"BTC/EUR"}, 79))
		rep, _ := BuildReport(reader, res)
		if res.DataCheck != nil || !containsText(rep.Honesty.Lines, "not checked") {
			t.Fatalf("honesty %v must say the coverage was not checked", rep.Honesty.Lines)
		}
	})
}

// TestRiskFeeAlignment covers the risk engine's fee assumption in a backtest.
func TestRiskFeeAlignment(t *testing.T) {
	t.Run("GIVEN settings that do not set FEE_AND_SLIPPAGE WHEN a run starts THEN the risk engine sizes with the exchange fee plus the fill model's expected half-spread, recorded in the settings and assumptions", func(t *testing.T) {
		cfg := testConfig([]string{"BTC/EUR"}, 79)
		res := mustRun(t, oneMarket(flatSeries(80, 100)), cfg)
		want := 0.008 + DefaultExchange().NoDataHalfSpreadBps/1e4
		if res.RiskFee.Explicit || !near(res.RiskFee.FeeAndSlippage, want, 1e-12) || res.Settings["FEE_AND_SLIPPAGE"] != fmtNum(want) {
			t.Fatalf("risk fee %+v, settings %q, want %v set for the run", res.RiskFee, res.Settings["FEE_AND_SLIPPAGE"], want)
		}
		if assumptionBias(res, "risk_fee") == "" {
			t.Fatal("the risk fee alignment must be an assumption")
		}
	})

	t.Run("GIVEN cash for one near all-in buy WHEN FEE_AND_SLIPPAGE is aligned THEN the risk engine leaves room for the fee and the buy fills instead of failing for funds", func(t *testing.T) {
		cfg := testConfig([]string{"BTC/EUR"}, 79)
		cfg.Settings.StartingCapital = 100
		cfg.Settings.TargetTradeAmount = 100
		cfg.Settings.MinTradeSize = 10
		s := (&scripted{}).at(tick(entryTick), "BTC/EUR", "long")
		res := mustRun(t, oneMarket(flatSeries(80, 100)), withScript(cfg, s, nil))
		if res.RejectionCounts[RejectFunds] != 0 || len(ordersWhere(res, func(o OrderLogEntry) bool { return o.Status == StatusFilled && o.Side == SideBuy })) != 1 {
			t.Fatalf("orders %+v, want the buy filled with no insufficient_funds rejection", res.Orders)
		}
	})

	t.Run("GIVEN a settings file that sets FEE_AND_SLIPPAGE below the exchange fee WHEN a run starts THEN it is left as set and the summary warns", func(t *testing.T) {
		cfg := testConfig([]string{"BTC/EUR"}, 79)
		cfg.Settings.FeeAndSlippage = 0.0036
		cfg.ExplicitSettings = []string{"FEE_AND_SLIPPAGE"}
		reader := oneMarket(flatSeries(80, 100))
		res := mustRun(t, reader, cfg)
		if !res.RiskFee.Explicit || res.RiskFee.FeeAndSlippage != 0.0036 || res.RiskFee.Warning == "" {
			t.Fatalf("risk fee %+v, want the explicit 0.0036 kept with a warning", res.RiskFee)
		}
		rep, _ := BuildReport(reader, res)
		if !containsText(rep.Honesty.Lines, "FEE_AND_SLIPPAGE") {
			t.Fatalf("honesty %v must warn about FEE_AND_SLIPPAGE", rep.Honesty.Lines)
		}
	})
}

// TestDisclosures covers the differences from the live bot that every summary states.
func TestDisclosures(t *testing.T) {
	t.Run("GIVEN a basic_strategy run on built-in defaults WHEN summarised THEN the first line says it is not the live default strategy, and the mode, settings source, universe policy and candle assumption are stated", func(t *testing.T) {
		reader := oneMarket(flatSeries(80, 100))
		res := mustRun(t, reader, testConfig([]string{"BTC/EUR"}, 79))
		rep, _ := BuildReport(reader, res)
		lines := rep.Honesty.Lines
		if len(lines) == 0 || !strings.Contains(lines[0], strategy.BasicAndLLM1hID) || !strings.Contains(lines[0], "indicator core") {
			t.Fatalf("first honesty line %q, want the live-default strategy disclosure", lines)
		}
		for _, want := range []string{"manual", "built-in defaults", "empty", "restores saved ticks"} {
			if !containsText(lines, want) {
				t.Errorf("honesty %v does not mention %q", lines, want)
			}
		}
		for _, name := range []string{"strategy_scope", "trading_mode", "settings_source", "empty_candles", "duplicate_trades", "price_history_start"} {
			if assumptionBias(res, name) == "" {
				t.Errorf("assumption %s missing", name)
			}
		}
	})

	t.Run("GIVEN any run WHEN its assumptions are listed THEN the shorter 4-hour trend window and its warm-up are stated as pessimistic", func(t *testing.T) {
		res := mustRun(t, oneMarket(flatSeries(80, 100)), testConfig([]string{"BTC/EUR"}, 79))
		if got := assumptionBias(res, "trend_candles"); got != BiasPessimistic {
			t.Fatalf("trend_candles bias = %q, want %q", got, BiasPessimistic)
		}
		for _, a := range res.Assumptions {
			if a.Name == "candles" && !strings.Contains(a.Value, "4 h") {
				t.Fatalf("candles assumption %q does not list the 4-hour candles", a.Value)
			}
		}
	})

	t.Run("GIVEN a run with a settings file WHEN summarised THEN the file is named", func(t *testing.T) {
		reader := oneMarket(flatSeries(80, 100))
		cfg := testConfig([]string{"BTC/EUR"}, 79)
		cfg.SettingsSource = "tuned.json"
		res := mustRun(t, reader, cfg)
		rep, _ := BuildReport(reader, res)
		if res.SettingsSource != "tuned.json" || !containsText(rep.Honesty.Lines, "tuned.json") {
			t.Fatalf("settings source %q honesty %v, want tuned.json named", res.SettingsSource, rep.Honesty.Lines)
		}
	})

	t.Run("GIVEN a universe run WHEN summarised THEN it says the point-in-time re-selection is not the live start-up policy", func(t *testing.T) {
		const ticks = 200
		from := t0.Add(-PreRoll)
		n := int(PreRoll/histdata.BarInterval) + ticks
		reader := memReader{bars: map[string][]histdata.Bar{"AAA/EUR": randomWalkBars(5, n, from, 100, 0.002)}}
		cfg := testConfig(nil, ticks)
		cfg.Universe = &UniverseRule{TopN: 1}
		res := mustRun(t, reader, cfg)
		rep, _ := BuildReport(reader, res)
		if !containsText(rep.Honesty.Lines, "different policy") {
			t.Fatalf("honesty %v must state the universe policy differs from live", rep.Honesty.Lines)
		}
	})
}
