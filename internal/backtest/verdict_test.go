package backtest

import (
	"strings"
	"testing"
)

// verdictInput is a neutral run for the verdict rules: positive, long
// enough, enough trades, beats its benchmark, reconciles.
func verdictInput() (Metrics, BenchmarkSet, Reconciliation) {
	m := Metrics{Days: 365, NetPnL: 100, NetReturnPct: 10, GrossPnL: 150, RoundTrips: 40}
	bench := BenchmarkSet{Primary: "buy_and_hold:BTC/EUR", Benchmarks: []Benchmark{{Name: "buy_and_hold:BTC/EUR", NetReturnPct: 5}}}
	return m, bench, Reconciliation{}
}

// rules returns the ids of the verdict lines.
func rules(lines []VerdictLine) []string {
	var out []string
	for _, l := range lines {
		out = append(out, l.Rule)
	}
	return out
}

// has reports whether the verdict includes rule.
func has(lines []VerdictLine, rule string) bool {
	for _, l := range lines {
		if l.Rule == rule {
			return true
		}
	}
	return false
}

// forbiddenClaims are words no automatic verdict may use (docs/14).
var forbiddenClaims = []string{"edge", "profitable strategy"}

// assertNoClaims fails when a verdict line makes a forbidden claim.
func assertNoClaims(t *testing.T, lines []VerdictLine) {
	t.Helper()
	for _, l := range lines {
		for _, w := range forbiddenClaims {
			if strings.Contains(strings.ToLower(l.Text), w) {
				t.Fatalf("verdict %q uses the forbidden claim %q", l.Text, w)
			}
		}
	}
}

// TestRunVerdict covers each single-run verdict rule.
func TestRunVerdict(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Metrics, *BenchmarkSet, *Reconciliation)
		want   string
		absent string
	}{
		{"an accounting warning", func(_ *Metrics, _ *BenchmarkSet, r *Reconciliation) { r.Warning = true }, RuleAccounting, ""},
		{"no round trips", func(m *Metrics, _ *BenchmarkSet, _ *Reconciliation) {
			m.RoundTrips = 0
			m.NetPnL, m.NetReturnPct = 0, 0
		}, RuleNoTrades, RuleNetNegative},
		{"a net loss", func(m *Metrics, _ *BenchmarkSet, _ *Reconciliation) {
			m.NetPnL, m.NetReturnPct, m.GrossPnL = -10, -1, -5
		}, RuleNetNegative, RuleCostsConsume},
		{"a gross gain eaten by costs", func(m *Metrics, _ *BenchmarkSet, _ *Reconciliation) { m.NetPnL, m.NetReturnPct = -10, -1 }, RuleCostsConsume, RulePositiveUnvalidated},
		{"few trades", func(m *Metrics, _ *BenchmarkSet, _ *Reconciliation) { m.RoundTrips = 29 }, RuleFewTrades, RulePositiveUnvalidated},
		{"a short period", func(m *Metrics, _ *BenchmarkSet, _ *Reconciliation) { m.Days = 89 }, RuleShortPeriod, RulePositiveUnvalidated},
		{"a long positive run", func(*Metrics, *BenchmarkSet, *Reconciliation) {}, RulePositiveUnvalidated, RuleFewTrades},
		{"a worse result than buy-and-hold", func(_ *Metrics, b *BenchmarkSet, _ *Reconciliation) { b.Benchmarks[0].NetReturnPct = 20 }, RuleBelowBuyHold, RuleAboveBuyHold},
		{"a better result than buy-and-hold", func(*Metrics, *BenchmarkSet, *Reconciliation) {}, RuleAboveBuyHold, RuleBelowBuyHold},
	}
	for _, c := range cases {
		t.Run("GIVEN "+c.name+" WHEN the verdict is formed THEN rule "+c.want+" fires", func(t *testing.T) {
			m, b, r := verdictInput()
			c.mutate(&m, &b, &r)
			lines := RunVerdict(m, b, r)
			if !has(lines, c.want) || (c.absent != "" && has(lines, c.absent)) {
				t.Fatalf("rules %v, want %s without %q", rules(lines), c.want, c.absent)
			}
			assertNoClaims(t, lines)
		})
	}
}
