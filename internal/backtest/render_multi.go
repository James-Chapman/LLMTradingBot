package backtest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// rowHeader and rowCells format RunRows as a Markdown table.
const rowHeader = "| Run | Period | Net P&L | Net % | Round trips | Win rate | Profit factor | Max DD % | Benchmark % | Excess pp |\n|---|---|---|---|---|---|---|---|---|---|\n"

// rowCells returns one RunRow's table line.
func rowCells(r RunRow) string {
	warn := ""
	if r.ReconcileWarning {
		warn = " (ACCOUNTING WARNING)"
	}
	return fmt.Sprintf("| %s%s | %s..%s | %s | %+.2f | %d | %s | %s | %.2f | %s | %s |\n", r.Label, warn,
		r.Start.Format(time.DateOnly), r.End.Format(time.DateOnly), money(r.NetPnL), r.NetReturnPct, r.RoundTrips,
		opt(r.WinRatePct, "%.1f %%", ""), opt(r.ProfitFactor, "%.2f", ""), r.MaxDrawdownPct,
		opt(r.BenchmarkReturnPct, "%+.2f", ""), opt(r.ExcessReturnPct, "%+.2f", ""))
}

// header writes a multi-run summary's title, honesty block and verdict.
func header(b *strings.Builder, title, strategy string, start, end time.Time, notes []string, verdict []VerdictLine) {
	fmt.Fprintf(b, "# %s: %s, %s to %s\n\n## Read this first\n\n", title, strategy, start.Format(time.DateOnly), end.Format(time.DateOnly))
	lines := append([]string{
		"Every run here replays one historical path; robustness tests reduce, but do not remove, the chance that a result is luck or fitted to that path.",
		"Survivorship, fee tier and in-sample warnings of each run are in its own summary.md under runs/.",
	}, notes...)
	bullets(b, lines)
	b.WriteString("## Verdict\n\n")
	var v []string
	for _, l := range verdict {
		v = append(v, l.Text+" ["+l.Rule+"]")
	}
	bullets(b, v)
}

// RenderSplit renders the split summary.
func RenderSplit(r *SplitReport) string {
	var b strings.Builder
	header(&b, "In-sample / out-of-sample", r.StrategyID, r.Start, r.End, r.Notes, r.Verdict)
	fmt.Fprintf(&b, "## Results\n\nSplit: %s.\n\n%s", r.Method, rowHeader)
	b.WriteString(rowCells(r.InSample))
	b.WriteString(rowCells(r.OutOfSample))
	return b.String()
}

// RenderWalkForward renders the walk-forward summary.
func RenderWalkForward(r *WalkForwardReport) string {
	var b strings.Builder
	header(&b, "Walk-forward", r.StrategyID, r.Start, r.End, r.Notes, r.Verdict)
	a := r.Aggregate
	fmt.Fprintf(&b, "## Test windows only\n\nTrain %s, test %s, step %s. %d windows, %d net positive (%.0f %%). Compounded test return %+.2f %%; min %+.2f %%, median %+.2f %%, max %+.2f %%; %d round trips.\n\n",
		r.Train, r.Test, r.Step, a.Windows, a.Profitable, a.ProfitablePct, a.CompoundedReturnPct, a.MinReturnPct, a.MedianReturnPct, a.MaxReturnPct, a.RoundTrips)
	b.WriteString(rowHeader)
	for _, w := range r.Windows {
		b.WriteString(rowCells(w.Test))
	}
	b.WriteString("\n## Train windows\n\n")
	if r.Tuning != nil {
		fmt.Fprintf(&b, "Tuned %s over %s. Chosen per window: %s (%d changes). Chosen values: mean train return %+.2f %%, mean test return %+.2f %%.\n\n",
			r.Tuning.Key, strings.Join(r.Tuning.Values, ", "), strings.Join(r.Tuning.Chosen, ", "), r.Tuning.Changes, r.Tuning.MeanTrainReturnPct, r.Tuning.MeanTestReturnPct)
	}
	b.WriteString(rowHeader)
	for _, w := range r.Windows {
		for _, t := range w.Train {
			b.WriteString(rowCells(t))
		}
	}
	return b.String()
}

// RenderSensitivity renders the sensitivity summary.
func RenderSensitivity(r *SensitivityReport) string {
	var b strings.Builder
	header(&b, "Sensitivity", r.StrategyID, r.Start, r.End, r.Notes, r.Verdict)
	b.WriteString("## Variants\n\n| Dimension | Value | Baseline | Net % | Round trips | Max DD % | Profit factor | Sign survives |\n|---|---|---|---|---|---|---|---|\n")
	fmt.Fprintf(&b, "| baseline | | yes | %+.2f | %d | %.2f | %s | |\n", r.Baseline.NetReturnPct, r.Baseline.RoundTrips, r.Baseline.MaxDrawdownPct, opt(r.Baseline.ProfitFactor, "%.2f", ""))
	for _, x := range r.Rows {
		base, survives := "", "yes"
		if x.Baseline {
			base = "yes"
		}
		if !x.SignSurvives {
			survives = "NO"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %+.2f | %d | %.2f | %s | %s |\n", x.Dimension, x.Value, base, x.Row.NetReturnPct, x.Row.RoundTrips,
			x.Row.MaxDrawdownPct, opt(x.Row.ProfitFactor, "%.2f", ""), survives)
	}
	be := r.BreakEvenFee
	fmt.Fprintf(&b, "\n## Break-even fee\n\n%s: %s (%s).\n", be.Status, opt(be.FeeBps, "%.1f bp", ""), be.Note)
	return b.String()
}

// cellTable renders regime cells.
func cellTable(b *strings.Builder, cells []RegimeCell) {
	b.WriteString("| Trend | Volatility | Buckets | P&L | Compounded % | Benchmark % | Buckets below benchmark | Round trips | Win rate |\n|---|---|---|---|---|---|---|---|---|\n")
	for _, c := range cells {
		thin := ""
		if c.Thin {
			thin = " (thin)"
		}
		fmt.Fprintf(b, "| %s | %s | %d%s | %s | %+.2f | %+.2f | %d | %d | %s |\n", c.Trend, c.Volatility, c.Buckets, thin, money(c.PnL),
			c.CompoundedReturnPct, c.BenchmarkCompoundedPct, c.BucketsBelowBenchmark, c.RoundTrips, opt(c.WinRatePct, "%.1f %%", ""))
	}
	b.WriteString("\n")
}

// RenderRegimes renders the regime summary.
func RenderRegimes(r *RegimeReport) string {
	var b strings.Builder
	header(&b, "Regimes", r.StrategyID, r.Start, r.End, r.Notes, r.Verdict)
	b.WriteString("## Whole run\n\n" + rowHeader + rowCells(r.Run) + "\n## By regime\n\n")
	cellTable(&b, r.Cells)
	b.WriteString("## By trend\n\n")
	cellTable(&b, r.ByTrend)
	b.WriteString("## By volatility\n\n")
	cellTable(&b, r.ByVolatility)
	fmt.Fprintf(&b, "## Buckets (%s)\n\n| Start | Trend | Volatility | Market %% | P&L | Return %% | Benchmark %% | Round trips | Max DD %% |\n|---|---|---|---|---|---|---|---|---|\n", r.Spec.Bucket)
	for _, x := range r.Buckets {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %+.2f | %+.2f | %d | %.2f |\n", x.Start.Format(time.DateOnly), x.Trend, x.Volatility,
			opt(x.MarketReturnPct, "%+.2f", ""), money(x.PnL), x.ReturnPct, x.BenchmarkReturnPct, x.RoundTrips, x.MaxDrawdownPct)
	}
	return b.String()
}

// RunDirName returns the directory of the i-th run of a multi-run command.
func RunDirName(i int, label string) string {
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_', r == '=':
			return r
		}
		return '_'
	}, label)
	return fmt.Sprintf("%02d_%s", i+1, clean)
}

// WriteMultiRun writes a multi-run command's directory: summary.json and
// summary.md, run.json with the base inputs, and every run's report set
// under runs/.
func WriteMultiRun(dir string, summary any, markdown string, base BaseInfo, outs []RunOutcome) error {
	if err := WriteJSON(filepath.Join(dir, "summary.json"), summary); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.md"), []byte(markdown), 0o644); err != nil {
		return err
	}
	if err := WriteJSON(filepath.Join(dir, "run.json"), base); err != nil {
		return err
	}
	for i, o := range outs {
		if err := WriteReports(filepath.Join(dir, "runs", RunDirName(i, o.Label)), o.Report, base.CommandLine); err != nil {
			return err
		}
	}
	return nil
}
