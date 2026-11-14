package backtest

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// money formats an amount in the quote currency.
func money(v float64) string { return fmt.Sprintf("%.2f", v) }

// pct formats a percentage.
func pct(v float64) string { return fmt.Sprintf("%.2f %%", v) }

// opt formats an optional value, or "n/a" with a reason.
func opt(v *float64, format, reason string) string {
	if v == nil {
		if reason == "" {
			return "n/a"
		}
		return "n/a (" + reason + ")"
	}
	return fmt.Sprintf(format, *v)
}

// duration formats seconds as days and hours.
func duration(seconds int64) string {
	d := time.Duration(seconds) * time.Second
	days := int(d / (24 * time.Hour))
	return fmt.Sprintf("%dd %dh", days, int((d-time.Duration(days)*24*time.Hour)/time.Hour))
}

// episode formats a drawdown episode.
func episode(e *DrawdownEpisode) string {
	if e == nil {
		return "none"
	}
	rec := "not recovered by the end"
	if e.RecoveryTime != nil {
		rec = "recovered " + e.RecoveryTime.Format(time.RFC3339)
	}
	return fmt.Sprintf("%s (%s) from %s at %s to %s at %s, %s", money(e.Amount), pct(e.Pct), money(e.Peak),
		e.PeakTime.Format(time.RFC3339), money(e.Trough), e.TroughTime.Format(time.RFC3339), rec)
}

// bullets writes lines as a Markdown list.
func bullets(b *strings.Builder, lines []string) {
	for _, l := range lines {
		fmt.Fprintf(b, "- %s\n", l)
	}
	b.WriteString("\n")
}

// sortedCounts returns "key: n" lines sorted by key.
func sortedCounts(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, fmt.Sprintf("%s: %d", k, m[k]))
	}
	return out
}

// RenderSummary renders a run's human-readable summary (summary.md). The
// honesty block always comes first.
func RenderSummary(r *Report) string {
	var b strings.Builder
	m := r.Metrics
	fmt.Fprintf(&b, "# Backtest: %s, %s to %s\n\n", r.StrategyID, r.Start.Format(time.DateOnly), r.End.Format(time.DateOnly))
	b.WriteString("## Read this first\n\n")
	bullets(&b, r.Honesty.Lines)

	b.WriteString("## Verdict\n\n")
	var verdict []string
	for _, v := range r.Verdict {
		verdict = append(verdict, v.Text+" ["+v.Rule+"]")
	}
	bullets(&b, verdict)

	b.WriteString("## Results\n\n| Metric | Value |\n|---|---|\n")
	rows := [][2]string{
		{"Initial capital", money(m.InitialCapital)},
		{"Final portfolio value", money(m.FinalValue)},
		{"Final value excluding stale positions (valued at zero)", fmt.Sprintf("%s (%d stale at the end)", money(m.InitialCapital+m.NetPnLWorstCase), m.StaleAtEnd)},
		{"Net P&L", fmt.Sprintf("%s (%s)", money(m.NetPnL), pct(m.NetReturnPct))},
		{"Net P&L, stale positions at zero (worst case)", fmt.Sprintf("%s (%s)", money(m.NetPnLWorstCase), pct(m.NetReturnPctWorstCase))},
		{"Annualised return", opt(m.AnnualisedReturnPct, "%.2f %%", m.AnnualisedNote)},
		{"Gross P&L (before fees and execution costs)", money(m.GrossPnL)},
		{"Total fees", money(m.TotalFees)},
		{"Total execution cost (drift + spread + impact + rounding)", money(m.TotalExecutionCost)},
		{"Round trips (end-of-run valuations)", fmt.Sprintf("%d (%d)", m.RoundTrips, m.EndOfRunCloses)},
		{"Wins / losses / breakeven", fmt.Sprintf("%d / %d / %d", m.Wins, m.Losses, m.Breakeven)},
		{"Win rate", opt(m.WinRatePct, "%.2f %%", "")},
		{"Average / median net P&L per round trip", opt(m.AvgNetPnL, "%.2f", "") + " / " + opt(m.MedianNetPnL, "%.2f", "")},
		{"Average win / average loss", opt(m.AvgWin, "%.2f", "") + " / " + opt(m.AvgLoss, "%.2f", "")},
		{"Payoff ratio", opt(m.PayoffRatio, "%.3f", "")},
		{"Profit factor", opt(m.ProfitFactor, "%.3f", m.ProfitFactorNote)},
		{"Max drawdown (by %)", episode(m.MaxDrawdown)},
		{"Max drawdown (by amount)", episode(m.MaxDrawdownAmount)},
		{"Longest drawdown", longest(m)},
		{"Sharpe (daily, annualised √365)", opt(m.Sharpe, "%.3f", m.RatioNote)},
		{"Sortino", opt(m.Sortino, "%.3f", m.RatioNote)},
		{"Exposure (time with a position)", pct(m.ExposurePct)},
		{"Capital utilisation average / max", pct(m.AvgUtilisationPct) + " / " + pct(m.MaxUtilisationPct)},
		{"Traded value / turnover", money(m.TradedValue) + " / " + opt(m.Turnover, "%.2f×", "")},
	}
	for _, row := range rows {
		fmt.Fprintf(&b, "| %s | %s |\n", row[0], row[1])
	}
	fmt.Fprintf(&b, "\nDrawdowns are measured %s.\n\n", m.DrawdownResolution)

	br := r.Bridge
	b.WriteString("## Gross to net\n\n| Step | Amount |\n|---|---|\n")
	for _, row := range [][2]string{
		{"Gross P&L", money(br.GrossPnL)}, {"− latency drift", money(br.LatencyDrift)}, {"− spread", money(br.SpreadCost)},
		{"− size impact", money(br.ImpactCost)}, {"− price rounding", money(br.RoundingCost)}, {"− unexplained residual", fmt.Sprintf("%.3g", br.Residual)},
		{"− fees", money(br.Fees)}, {"= net P&L (order log)", money(br.NetPnL)}, {"net P&L (engine)", fmt.Sprintf("%s (difference %.3g)", money(br.EngineNetPnL), br.Difference)},
	} {
		fmt.Fprintf(&b, "| %s | %s |\n", row[0], row[1])
	}
	fmt.Fprintf(&b, "\n%s\n\n", br.Definition)

	b.WriteString("## Costs\n\n| Cost | Amount | % of traded value | % of starting capital |\n|---|---|---|---|\n")
	c := r.Costs.Total
	for _, row := range []struct {
		name string
		line CostLine
	}{{"Fees", c.Fees}, {"Spread", c.Spread}, {"Size impact", c.Impact}, {"Latency drift", c.LatencyDrift}, {"Rounding", c.Rounding}, {"Execution total", c.Execution}, {"All costs", c.All}} {
		fmt.Fprintf(&b, "| %s | %s | %.4f %% | %.4f %% |\n", row.name, money(row.line.Amount), row.line.PctOfTradedValue, row.line.PctOfStartingCapital)
	}
	b.WriteString("\nPer market: see costs.json.\n\n")

	b.WriteString("## Benchmarks\n\n| Benchmark | Final value | Net return | Max drawdown | Strategy minus benchmark |\n|---|---|---|---|---|\n")
	for _, bm := range r.Benchmarks.Benchmarks {
		dd := "none"
		if bm.MaxDrawdown != nil {
			dd = pct(bm.MaxDrawdown.Pct)
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %+.2f pp |\n", bm.Name, money(bm.FinalValue), pct(bm.NetReturnPct), dd, bm.ExcessReturnPct)
	}
	b.WriteString("\n")
	bullets(&b, r.Benchmarks.Notes)

	s := r.Signals
	b.WriteString("## Signals\n\n")
	fmt.Fprintf(&b, "%d ideas, %d sent as orders, %d of those filled.\n\n", s.Ideas, s.Submitted, s.Filled)
	if len(s.NotTraded) > 0 {
		b.WriteString("Not traded, by reason:\n\n")
		bullets(&b, sortedCounts(s.NotTraded))
	}
	if len(s.RiskReasons) > 0 {
		b.WriteString("Risk rejections:\n\n")
		bullets(&b, sortedCounts(s.RiskReasons))
	}
	if len(s.OrderRejections) > 0 {
		b.WriteString("Orders not filled (signals and stops), by reason:\n\n")
		bullets(&b, sortedCounts(s.OrderRejections))
	}

	rec := r.Reconciliation
	b.WriteString("## Reconciliation\n\n")
	status := "passed"
	if rec.Warning {
		status = "FAILED: " + strings.Join(rec.Problems, "; ")
	}
	fmt.Fprintf(&b, "Exact (%s): %s. Final cash exact %s vs engine %s (difference %.3g); final equity exact %s vs engine %s (difference %.3g); fees exact %s vs engine %s; tolerance %.3g (%s).\n",
		rec.Method, status, rec.ExactFinalCash, f(rec.EngineFinalCash), rec.CashDifference, rec.ExactFinalEquity, f(rec.EngineFinalEquity),
		rec.EquityDifference, rec.ExactFees, f(rec.EngineFees), rec.Tolerance, rec.ToleranceRule)
	return b.String()
}

// longest formats the longest drawdown.
func longest(m Metrics) string {
	if m.LongestDrawdownSeconds == 0 || m.LongestDrawdownStart == nil {
		return "none"
	}
	state := "recovered"
	if !m.LongestDrawdownRecovered {
		state = "not recovered by the end"
	}
	return fmt.Sprintf("%s from %s, %s", duration(m.LongestDrawdownSeconds), m.LongestDrawdownStart.Format(time.RFC3339), state)
}
