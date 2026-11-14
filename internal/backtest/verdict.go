package backtest

import (
	"fmt"
	"strings"
)

// Verdict rule ids. The rules are fixed and documented in docs/14; the text
// is generated from them, never written freely. No automatic verdict says
// "edge" or "profitable strategy": only `backtest compare` may, when the
// documented out-of-sample, walk-forward and fee-stress conditions all hold.
const (
	RuleAccounting          = "accounting_warning"
	RuleNoTrades            = "no_round_trips"
	RuleNetNegative         = "net_negative_after_costs"
	RuleCostsConsume        = "costs_consume_gross"
	RuleFewTrades           = "positive_but_few_trades"
	RuleShortPeriod         = "positive_but_short_period"
	RulePositiveUnvalidated = "positive_not_validated"
	RuleBelowBuyHold        = "below_buy_and_hold"
	RuleAboveBuyHold        = "above_buy_and_hold"
	// RuleDataWarning: the data cache did not fully cover the run (a missing
	// month or an error finding accepted with --allow-data-gaps, or partial coverage).
	RuleDataWarning = "data_coverage_warning"
	// RuleStaleInconclusive: the result is positive only because positions in
	// markets that stopped trading are valued at their last trade.
	RuleStaleInconclusive = "inconclusive_stale_positions"
)

// VerdictLine is one fired rule and its generated sentence.
type VerdictLine struct {
	Rule string `json:"rule"`
	Text string `json:"text"`
}

// RunVerdict applies the single-run rules in a fixed order.
func RunVerdict(m Metrics, bench BenchmarkSet, rec Reconciliation) []VerdictLine {
	var out []VerdictLine
	add := func(rule, format string, args ...any) {
		out = append(out, VerdictLine{Rule: rule, Text: fmt.Sprintf(format, args...)})
	}
	if m.DataWarnings > 0 {
		add(RuleDataWarning, "The data cache does not fully cover this run (%d warning(s), listed first in the summary): trades and prices in the uncovered periods are missing, not quiet.", m.DataWarnings)
	}
	if rec.Warning {
		add(RuleAccounting, "Accounting reconciliation failed (%s): do not trust the numbers of this run.", strings.Join(rec.Problems, "; "))
	}
	switch {
	case m.RoundTrips == 0:
		add(RuleNoTrades, "No round trips: this run says nothing about profitability.")
	case m.NetPnL > 0 && m.StaleAtEnd > 0 && m.NetPnLWorstCase <= 0:
		add(RuleStaleInconclusive, "Inconclusive: net %+.2f with %d stale position(s) valued at their last trade, but %+.2f if they are worth nothing; the sign of the result depends on markets that stopped trading.",
			m.NetPnL, m.StaleAtEnd, m.NetPnLWorstCase)
	case m.NetPnL <= 0:
		add(RuleNetNegative, "Not net positive after costs: %+.2f (%+.2f %%) over %.0f days.", m.NetPnL, m.NetReturnPct, m.Days)
		if m.GrossPnL > 0 {
			add(RuleCostsConsume, "Positive before costs (gross %+.2f) but fees and execution costs turn it into a loss.", m.GrossPnL)
		}
	default:
		if m.RoundTrips < MeaningfulTrades {
			add(RuleFewTrades, "Net positive (%+.2f %%) but from %d round trips, fewer than %d: not statistically meaningful.", m.NetReturnPct, m.RoundTrips, MeaningfulTrades)
		}
		if m.Days < MeaningfulDays {
			add(RuleShortPeriod, "Net positive (%+.2f %%) over %.0f days, under %d days: too short to judge.", m.NetReturnPct, m.Days, MeaningfulDays)
		}
		if m.RoundTrips >= MeaningfulTrades && m.Days >= MeaningfulDays {
			add(RulePositiveUnvalidated, "Net positive (%+.2f %%) on this one historical path, which is not validation: run split, walkforward and sensitivity before relying on it.", m.NetReturnPct)
		}
	}
	if b, ok := bench.primary(); ok {
		diff := m.NetReturnPct - b.NetReturnPct
		if diff < 0 {
			add(RuleBelowBuyHold, "Underperforms %s by %.2f percentage points (%+.2f %% vs %+.2f %%).", b.Name, -diff, m.NetReturnPct, b.NetReturnPct)
		} else {
			add(RuleAboveBuyHold, "Beats %s by %.2f percentage points on this path (%+.2f %% vs %+.2f %%).", b.Name, diff, m.NetReturnPct, b.NetReturnPct)
		}
	}
	return out
}

// primary returns the primary benchmark.
func (s BenchmarkSet) primary() (Benchmark, bool) {
	for _, b := range s.Benchmarks {
		if b.Name == s.Primary {
			return b, true
		}
	}
	return Benchmark{}, false
}
