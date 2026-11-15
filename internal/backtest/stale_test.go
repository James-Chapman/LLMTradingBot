package backtest

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/histdata"
)

// staleScenario returns a reader where AAA/EUR is bought at tick 40, rises
// to 120 at tick 50 and stops trading after tick 60, while BBB/EUR keeps
// trading for ticks bars, and a script that buys AAA.
func staleScenario(ticks int) (memReader, *scripted) {
	aaa := flatSeries(61, 100)
	for i := 45; i < len(aaa); i++ {
		aaa[i] = flatBar(aaa[i].Start, 120, 0.001, 1000)
	}
	return memReader{bars: map[string][]histdata.Bar{"AAA/EUR": aaa, "BBB/EUR": flatSeries(ticks+1, 50)}},
		(&scripted{}).at(tick(entryTick), "AAA/EUR", domain.Long)
}

// TestStalePositions covers positions in markets whose data stops.
func TestStalePositions(t *testing.T) {
	day := int(24 * time.Hour / histdata.BarInterval)

	t.Run("GIVEN a held market whose trades stop for longer than the threshold WHEN the run ends THEN the position is reported stale, liquidated separately and valued at the last trade and at zero", func(t *testing.T) {
		ticks := day + 200
		reader, s := staleScenario(ticks)
		cfg := testConfig([]string{"AAA/EUR", "BBB/EUR"}, ticks)
		cfg.DisableLearner = true
		res := mustRun(t, reader, withScript(cfg, s, nil))

		if len(res.StalePositions) != 1 {
			t.Fatalf("stale positions %+v, want one", res.StalePositions)
		}
		sp := res.StalePositions[0]
		if sp.Market != "AAA/EUR" || !sp.LastTrade.Equal(tick(60)) || !sp.AtEnd || sp.Qty <= 0 || !near(sp.MarkedValue, sp.Qty*120, 1e-9) {
			t.Fatalf("stale position %+v, want AAA/EUR last traded at %v, still open at the end, marked at 120", sp, tick(60))
		}
		liq := ordersWhere(res, func(o OrderLogEntry) bool { return o.Reason == ReasonStaleLiquidation })
		if len(liq) != 1 || liq[0].Market != "AAA/EUR" || len(ordersWhere(res, func(o OrderLogEntry) bool { return o.Reason == ReasonEndOfRun })) != 0 {
			t.Fatalf("liquidations %+v, want one stale_liquidation and no plain end_of_run", liq)
		}
		proceeds := liq[0].Notional - liq[0].Fee
		if !near(sp.LiquidationValue, proceeds, 1e-9) || !near(res.EndingEquityWorstCase, res.EndingEquity-proceeds, 1e-9) {
			t.Fatalf("worst case %v with liquidation value %v, want ending equity %v less proceeds %v", res.EndingEquityWorstCase, sp.LiquidationValue, res.EndingEquity, proceeds)
		}
		flagged := 0
		for _, p := range res.EquityCurve {
			if p.StalePositions > 0 {
				flagged++
				if !near(p.StaleValue, sp.Qty*120, 1e-9) {
					t.Fatalf("equity point %+v, want the stale mark flagged", p)
				}
			}
		}
		if flagged == 0 {
			t.Fatal("no equity point flags the stale mark")
		}
		if got := assumptionBias(res, "end_of_run"); got != BiasUnknown {
			t.Fatalf("end_of_run bias %q, want unknown", got)
		}
	})

	t.Run("GIVEN a stale position that makes the run positive at its last trade but negative at zero WHEN the verdict is built THEN it is inconclusive, never positive, and the summary states the worst case", func(t *testing.T) {
		ticks := day + 200
		reader, s := staleScenario(ticks)
		cfg := testConfig([]string{"AAA/EUR", "BBB/EUR"}, ticks)
		cfg.DisableLearner = true
		res := mustRun(t, reader, withScript(cfg, s, nil))
		rep, err := BuildReport(reader, res)
		if err != nil {
			t.Fatal(err)
		}
		m := rep.Metrics
		if m.NetPnL <= 0 || m.NetPnLWorstCase >= 0 || m.StalePositions != 1 {
			t.Fatalf("metrics net %v worst %v stale %d, want positive, negative, one", m.NetPnL, m.NetPnLWorstCase, m.StalePositions)
		}
		rules := verdictRules(rep.Verdict)
		if !slices.Contains(rules, RuleStaleInconclusive) || slices.Contains(rules, RuleFewTrades) || slices.Contains(rules, RulePositiveUnvalidated) || slices.Contains(rules, RuleShortPeriod) {
			t.Fatalf("verdict %v, want inconclusive_stale_positions instead of any positive rule", rules)
		}
		if !containsText(rep.Honesty.Lines, "STALE") || !containsText([]string{RenderSummary(rep)}, "excluding stale positions") {
			t.Fatalf("honesty %v: the stale positions and the equity without them must be stated", rep.Honesty.Lines)
		}
	})

	t.Run("GIVEN a held market that stops trading for less than the threshold WHEN the run ends THEN nothing is stale", func(t *testing.T) {
		ticks := 400
		reader, s := staleScenario(ticks)
		cfg := testConfig([]string{"AAA/EUR", "BBB/EUR"}, ticks)
		res := mustRun(t, reader, withScript(cfg, s, nil))
		if len(res.StalePositions) != 0 || res.EndingEquityWorstCase != res.EndingEquity {
			t.Fatalf("stale %+v worst %v, want none and the worst case equal to %v", res.StalePositions, res.EndingEquityWorstCase, res.EndingEquity)
		}
		if len(ordersWhere(res, func(o OrderLogEntry) bool { return o.Reason == ReasonEndOfRun })) != 1 {
			t.Fatal("want a plain end_of_run valuation")
		}
	})

	t.Run("GIVEN a shorter stale threshold WHEN the data stops for longer THEN the threshold is honoured and recorded", func(t *testing.T) {
		ticks := 400
		reader, s := staleScenario(ticks)
		cfg := testConfig([]string{"AAA/EUR", "BBB/EUR"}, ticks)
		cfg.StaleAfter = time.Hour
		res := mustRun(t, reader, withScript(cfg, s, nil))
		if len(res.StalePositions) != 1 || res.StaleAfter != "1h0m0s" {
			t.Fatalf("stale %+v after %q, want one with a 1 h threshold", res.StalePositions, res.StaleAfter)
		}
	})
}

// assumptionBias returns the bias of the named assumption of res.
func assumptionBias(res *Result, name string) string {
	for _, a := range res.Assumptions {
		if a.Name == name {
			return a.Bias
		}
	}
	return ""
}

// verdictRules returns the rule ids of a verdict.
func verdictRules(v []VerdictLine) []string {
	out := []string{}
	for _, l := range v {
		out = append(out, l.Rule)
	}
	return out
}

// containsText reports whether any line contains sub.
func containsText(lines []string, sub string) bool {
	return slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, sub) })
}
