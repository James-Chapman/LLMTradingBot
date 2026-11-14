package backtest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/histdata"
)

// Summary kinds of the multi-run commands.
const (
	KindSplit       = "split"
	KindWalkForward = "walkforward"
	KindRegimes     = "regimes"
	KindSensitivity = "sensitivity"
	KindCompare     = "compare"
)

// RunSpec is one run of a multi-run command.
type RunSpec struct {
	Label  string
	Config Config
}

// RunOutcome is a finished run and its report.
type RunOutcome struct {
	Label  string
	Report *Report
}

// Progress is told each time a run finishes.
type Progress func(done, total int, label string)

// RunAll runs specs on up to parallel workers. Each run is independent and
// deterministic, and outcomes are returned in spec order, so the results do
// not depend on parallel. The reader is only read.
func RunAll(ctx context.Context, reader histdata.Reader, specs []RunSpec, parallel int, progress Progress) ([]RunOutcome, error) {
	parallel = max(1, min(parallel, len(specs)))
	out := make([]RunOutcome, len(specs))
	errs := make([]error, len(specs))
	jobs := make(chan int)
	var wg sync.WaitGroup
	var mu sync.Mutex
	done := 0
	for range parallel {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				res, err := Run(ctx, reader, specs[i].Config)
				var rep *Report
				if err == nil {
					rep, err = BuildReport(reader, res)
				}
				out[i], errs[i] = RunOutcome{Label: specs[i].Label, Report: rep}, err
				mu.Lock()
				done++
				if progress != nil {
					progress(done, len(specs), specs[i].Label)
				}
				mu.Unlock()
			}
		}()
	}
	for i := range specs {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	for i, err := range errs { // the first failure in spec order, whatever finished first
		if err != nil {
			return nil, fmt.Errorf("run %s: %w", specs[i].Label, err)
		}
	}
	return out, nil
}

// RunRow is the key metrics of one run, for the multi-run tables.
type RunRow struct {
	Label              string    `json:"label"`
	Start              time.Time `json:"start"`
	End                time.Time `json:"end"`
	Days               float64   `json:"days"`
	FinalValue         float64   `json:"final_value"`
	NetPnL             float64   `json:"net_pnl"`
	NetReturnPct       float64   `json:"net_return_pct"`
	GrossPnL           float64   `json:"gross_pnl"`
	Fees               float64   `json:"fees"`
	ExecutionCost      float64   `json:"execution_cost"`
	RoundTrips         int       `json:"round_trips"`
	WinRatePct         *float64  `json:"win_rate_pct"`
	ProfitFactor       *float64  `json:"profit_factor"`
	MaxDrawdownPct     float64   `json:"max_drawdown_pct"`
	Benchmark          string    `json:"benchmark"`
	BenchmarkReturnPct *float64  `json:"benchmark_return_pct"`
	ExcessReturnPct    *float64  `json:"excess_return_pct"` // net return - benchmark return, percentage points
	ReconcileWarning   bool      `json:"reconcile_warning"`
	// StaleDecidesSign: positive only with stale positions valued at their
	// last trade (negative at zero); NetPnLWorstCase is that zero valuation.
	StalePositions   int     `json:"stale_positions"`
	NetPnLWorstCase  float64 `json:"net_pnl_worst_case"`
	StaleDecidesSign bool    `json:"stale_decides_sign"`
	DataWarnings     int     `json:"data_warnings"`
}

// NewRunRow extracts the key metrics of a report.
func NewRunRow(label string, rep *Report) RunRow {
	m := rep.Metrics
	row := RunRow{
		Label: label, Start: rep.Start, End: rep.End, Days: m.Days, FinalValue: m.FinalValue, NetPnL: m.NetPnL,
		NetReturnPct: m.NetReturnPct, GrossPnL: m.GrossPnL, Fees: m.TotalFees, ExecutionCost: m.TotalExecutionCost,
		RoundTrips: m.RoundTrips, WinRatePct: m.WinRatePct, ProfitFactor: m.ProfitFactor, ReconcileWarning: rep.Reconciliation.Warning,
		StalePositions: m.StalePositions, NetPnLWorstCase: m.NetPnLWorstCase, DataWarnings: m.DataWarnings,
		StaleDecidesSign: m.NetPnL > 0 && m.StaleAtEnd > 0 && m.NetPnLWorstCase <= 0,
	}
	if m.MaxDrawdown != nil {
		row.MaxDrawdownPct = m.MaxDrawdown.Pct
	}
	if b, ok := rep.Benchmarks.primary(); ok {
		row.Benchmark = b.Name
		row.BenchmarkReturnPct = ptr(b.NetReturnPct)
		row.ExcessReturnPct = ptr(m.NetReturnPct - b.NetReturnPct)
	}
	return row
}

// AnyReconcileWarning reports whether any outcome failed its reconciliation.
func AnyReconcileWarning(outs []RunOutcome) bool {
	return slices.ContainsFunc(outs, func(o RunOutcome) bool { return o.Report.Reconciliation.Warning })
}

// Split labels.
const (
	LabelInSample    = "in_sample"
	LabelOutOfSample = "out_of_sample"
)

// SplitSpec chooses the split: a date, or the out-of-sample share of the period.
type SplitSpec struct {
	At          time.Time
	OOSFraction float64
}

// SplitPoint returns the split time and how it was chosen. A fraction is
// rounded down to a UTC day boundary when one falls inside the period.
func SplitPoint(start, end time.Time, spec SplitSpec) (time.Time, string, error) {
	var at time.Time
	var method string
	switch {
	case !spec.At.IsZero() && spec.OOSFraction != 0:
		return time.Time{}, "", errors.New("give a split date or an out-of-sample fraction, not both")
	case !spec.At.IsZero():
		at, method = spec.At.UTC(), "split date "+spec.At.UTC().Format(time.RFC3339)
	case spec.OOSFraction > 0 && spec.OOSFraction < 1:
		raw := start.Add(time.Duration(float64(end.Sub(start)) * (1 - spec.OOSFraction)))
		at = raw.Truncate(24 * time.Hour)
		if !at.After(start) {
			at = raw.Truncate(histdata.BarInterval)
		}
		method = fmt.Sprintf("last %g of the period out of sample, split rounded down to %s", spec.OOSFraction, at.Format(time.RFC3339))
	default:
		return time.Time{}, "", errors.New("the out-of-sample fraction must be between 0 and 1")
	}
	if at.UnixNano()%int64(histdata.BarInterval) != 0 {
		return time.Time{}, "", errors.New("the split must be a multiple of 30 seconds")
	}
	if !at.After(start) || !at.Before(end) {
		return time.Time{}, "", fmt.Errorf("the split %s must fall strictly inside the period %s to %s", at.Format(time.RFC3339), start.Format(time.RFC3339), end.Format(time.RFC3339))
	}
	return at, method, nil
}

// SplitReport is the in-sample / out-of-sample summary.
type SplitReport struct {
	SchemaVersion int           `json:"summary_schema_version"`
	Kind          string        `json:"kind"`
	StrategyID    string        `json:"strategy_id"`
	Start         time.Time     `json:"start"`
	End           time.Time     `json:"end"`
	Split         time.Time     `json:"split"`
	Method        string        `json:"method"`
	InSample      RunRow        `json:"in_sample"`
	OutOfSample   RunRow        `json:"out_of_sample"`
	Notes         []string      `json:"notes"`
	Verdict       []VerdictLine `json:"verdict"`
}

// RunSplit runs the two periods of base as separate runs: each has its own
// pre-roll from data before its start, starts with the starting capital and
// an empty learner, and values its open positions at its end.
func RunSplit(ctx context.Context, reader histdata.Reader, base Config, spec SplitSpec, parallel int, progress Progress) (*SplitReport, []RunOutcome, error) {
	at, method, err := SplitPoint(base.Start, base.End, spec)
	if err != nil {
		return nil, nil, err
	}
	is, oos := base, base
	is.End, oos.Start = at, at
	is.LearnerSeed, oos.LearnerSeed = nil, nil
	outs, err := RunAll(ctx, reader, []RunSpec{{LabelInSample, is}, {LabelOutOfSample, oos}}, parallel, progress)
	if err != nil {
		return nil, nil, err
	}
	rep := &SplitReport{
		SchemaVersion: SummarySchemaVersion, Kind: KindSplit, StrategyID: outs[0].Report.StrategyID, Start: base.Start, End: base.End,
		Split: at, Method: method, InSample: NewRunRow(LabelInSample, outs[0].Report), OutOfSample: NewRunRow(LabelOutOfSample, outs[1].Report),
		Notes: []string{
			"Parameters (settings, thresholds, the choice of strategy or markets) may only be chosen using the in-sample period. Look at the out-of-sample result once: anything changed after seeing it makes it in-sample too.",
			fmt.Sprintf("Each period is a separate run: it starts with the starting capital and an empty learner, and its %s pre-roll before the start feeds candles, 24 h volume and the last price without trading (for the out-of-sample run that pre-roll is the end of the in-sample period, used as data only).", PreRoll),
			"Open positions are valued at the end of each period; nothing carries from the in-sample run into the out-of-sample run.",
		},
	}
	rep.Verdict = splitVerdict(rep, AnyReconcileWarning(outs))
	return rep, outs, nil
}

// Split verdict rules.
const (
	RuleSplitAccounting   = "accounting_warning"
	RuleSplitISNegative   = "in_sample_negative"
	RuleSplitOOSNegative  = "positive_in_sample_negative_out_of_sample"
	RuleSplitBothPositive = "positive_in_and_out_of_sample"
	RuleSplitOOSFewTrades = "out_of_sample_few_trades"
)

// splitVerdict applies the split rules.
func splitVerdict(r *SplitReport, accounting bool) []VerdictLine {
	var out []VerdictLine
	add := func(rule, format string, args ...any) {
		out = append(out, VerdictLine{Rule: rule, Text: fmt.Sprintf(format, args...)})
	}
	is, oos := r.InSample, r.OutOfSample
	if accounting {
		add(RuleSplitAccounting, "A run failed its accounting reconciliation: do not trust these numbers.")
	}
	switch {
	case is.NetPnL <= 0:
		add(RuleSplitISNegative, "Not positive in-sample (%+.2f %%, %d round trips): there is nothing to validate out of sample.", is.NetReturnPct, is.RoundTrips)
	case oos.NetPnL <= 0:
		add(RuleSplitOOSNegative, "Positive in-sample (%+.2f %%), not positive out-of-sample (%+.2f %%).", is.NetReturnPct, oos.NetReturnPct)
	default:
		add(RuleSplitBothPositive, "Positive in-sample (%+.2f %%) and out-of-sample (%+.2f %%).", is.NetReturnPct, oos.NetReturnPct)
	}
	if oos.RoundTrips < MeaningfulTrades {
		add(RuleSplitOOSFewTrades, "The out-of-sample period has %d round trips, fewer than %d: not statistically meaningful.", oos.RoundTrips, MeaningfulTrades)
	}
	return out
}

// WalkSpec is a walk-forward plan: sequential train windows each followed by
// a test window, moved by Step; optionally one setting tuned per train window.
type WalkSpec struct {
	Train, Test, Step time.Duration
	TuneKey           string
	TuneValues        []string
}

// WalkWindow is one train/test pair.
type WalkWindow struct {
	Index          int       `json:"index"`
	TrainStart     time.Time `json:"train_start"`
	TrainEnd       time.Time `json:"train_end"` // = test start
	TestEnd        time.Time `json:"test_end"`
	Chosen         string    `json:"chosen,omitempty"` // tuned value applied to the test window
	Train          []RunRow  `json:"train"`            // one per tuned value (or one)
	Test           RunRow    `json:"test"`
	SeededOutcomes int       `json:"seeded_outcomes"` // learner outcomes carried from the chosen train run
}

// TestStart returns the start of the window's test period.
func (w WalkWindow) TestStart() time.Time { return w.TrainEnd }

// TrainLabel names the train run of the window for a tuned value ("" untuned).
func (w WalkWindow) TrainLabel(value string) string {
	if value == "" {
		return fmt.Sprintf("w%02d_train", w.Index+1)
	}
	return fmt.Sprintf("w%02d_train_%s", w.Index+1, value)
}

// TestLabel names the test run of the window.
func (w WalkWindow) TestLabel() string { return fmt.Sprintf("w%02d_test", w.Index+1) }

// WalkAggregate summarises the test windows only.
type WalkAggregate struct {
	Windows             int     `json:"windows"`
	Profitable          int     `json:"profitable"`
	ProfitablePct       float64 `json:"profitable_pct"`
	CompoundedReturnPct float64 `json:"compounded_return_pct"` // Π(1 + test return) - 1
	MinReturnPct        float64 `json:"min_return_pct"`
	MedianReturnPct     float64 `json:"median_return_pct"`
	MaxReturnPct        float64 `json:"max_return_pct"`
	RoundTrips          int     `json:"round_trips"`
}

// WalkTuning reports how the tuned value behaved.
type WalkTuning struct {
	Key                string   `json:"key"`
	Values             []string `json:"values"`
	Chosen             []string `json:"chosen"` // per window
	Changes            int      `json:"changes"`
	MeanTrainReturnPct float64  `json:"mean_train_return_pct"` // chosen value, in-sample
	MeanTestReturnPct  float64  `json:"mean_test_return_pct"`  // same value, out of sample
}

// WalkForwardReport is the walk-forward summary.
type WalkForwardReport struct {
	SchemaVersion int           `json:"summary_schema_version"`
	Kind          string        `json:"kind"`
	StrategyID    string        `json:"strategy_id"`
	Start         time.Time     `json:"start"`
	End           time.Time     `json:"end"`
	Train         string        `json:"train"`
	Test          string        `json:"test"`
	Step          string        `json:"step"`
	Windows       []WalkWindow  `json:"windows"`
	Aggregate     WalkAggregate `json:"aggregate"`
	Tuning        *WalkTuning   `json:"tuning"`
	Notes         []string      `json:"notes"`
	Verdict       []VerdictLine `json:"verdict"`
}

// walkWindows plans the windows of spec over [start, end].
func walkWindows(start, end time.Time, spec WalkSpec) ([]WalkWindow, error) {
	for name, d := range map[string]time.Duration{"train": spec.Train, "test": spec.Test, "step": spec.Step} {
		if d <= 0 || d%histdata.BarInterval != 0 {
			return nil, fmt.Errorf("the %s length must be a positive multiple of 30 seconds, got %s", name, d)
		}
	}
	if spec.Step < spec.Test {
		return nil, fmt.Errorf("the step (%s) must be at least the test length (%s): overlapping test windows would count the same days twice", spec.Step, spec.Test)
	}
	var out []WalkWindow
	for s := start; !s.Add(spec.Train + spec.Test).After(end); s = s.Add(spec.Step) {
		out = append(out, WalkWindow{Index: len(out), TrainStart: s, TrainEnd: s.Add(spec.Train), TestEnd: s.Add(spec.Train + spec.Test)})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the period %s to %s is shorter than one train plus test window", start.Format(time.RFC3339), end.Format(time.RFC3339))
	}
	return out, nil
}

// withValue returns cfg with the tuned value applied ("" leaves it).
func withValue(cfg Config, key, value string) (Config, error) {
	if value == "" {
		return cfg, nil
	}
	err := ApplyDimension(&cfg, key, value)
	return cfg, err
}

// RunWalkForward runs every train window (once per tuned value), chooses the
// value with the best train net return (ties: the first listed), then runs
// each test window with that value and with the learner seeded from the
// chosen train run's outcomes.
func RunWalkForward(ctx context.Context, reader histdata.Reader, base Config, spec WalkSpec, parallel int, progress Progress) (*WalkForwardReport, []RunOutcome, error) {
	windows, err := walkWindows(base.Start, base.End, spec)
	if err != nil {
		return nil, nil, err
	}
	values := []string{""}
	if spec.TuneKey != "" {
		if len(spec.TuneValues) < 2 {
			return nil, nil, errors.New("tuning needs at least two values")
		}
		values = spec.TuneValues
	}
	var trainSpecs []RunSpec
	for _, w := range windows {
		for _, v := range values {
			cfg, err := withValue(base, spec.TuneKey, v)
			if err != nil {
				return nil, nil, fmt.Errorf("--tune %s=%s: %w", spec.TuneKey, v, err)
			}
			cfg.Start, cfg.End, cfg.LearnerSeed = w.TrainStart, w.TrainEnd, nil
			trainSpecs = append(trainSpecs, RunSpec{Label: w.TrainLabel(v), Config: cfg})
		}
	}
	total := len(trainSpecs) + len(windows)
	wrap := func(offset int) Progress {
		if progress == nil {
			return nil
		}
		return func(done, _ int, label string) { progress(offset+done, total, label) }
	}
	trainOuts, err := RunAll(ctx, reader, trainSpecs, parallel, wrap(0))
	if err != nil {
		return nil, nil, err
	}

	var testSpecs []RunSpec
	chosenOut := make([]*Report, len(windows))
	for i := range windows {
		w := &windows[i]
		best := -1
		for j := range values {
			rep := trainOuts[i*len(values)+j].Report
			w.Train = append(w.Train, NewRunRow(w.TrainLabel(values[j]), rep))
			if best < 0 || rep.Metrics.NetReturnPct > chosenOut[i].Metrics.NetReturnPct {
				best, chosenOut[i] = j, rep
			}
		}
		w.Chosen = values[best]
		cfg, _ := withValue(base, spec.TuneKey, w.Chosen) // validated above
		cfg.Start, cfg.End = w.TrainEnd, w.TestEnd
		cfg.LearnerSeed = nil
		if !base.DisableLearner {
			cfg.LearnerSeed = append([]LearnedOutcome{}, chosenOut[i].Result().LearnedOutcomes...)
		}
		w.SeededOutcomes = len(cfg.LearnerSeed)
		testSpecs = append(testSpecs, RunSpec{Label: w.TestLabel(), Config: cfg})
	}
	testOuts, err := RunAll(ctx, reader, testSpecs, parallel, wrap(len(trainSpecs)))
	if err != nil {
		return nil, nil, err
	}
	for i := range windows {
		windows[i].Test = NewRunRow(windows[i].TestLabel(), testOuts[i].Report)
	}

	rep := &WalkForwardReport{
		SchemaVersion: SummarySchemaVersion, Kind: KindWalkForward, StrategyID: testOuts[0].Report.StrategyID,
		Start: base.Start, End: base.End, Train: spec.Train.String(), Test: spec.Test.String(), Step: spec.Step.String(),
		Windows: windows, Aggregate: walkAggregate(windows),
	}
	if spec.TuneKey != "" {
		rep.Tuning = walkTuning(spec, windows)
	}
	rep.Notes = walkNotes(spec, base, rep)
	all := append(trainOuts, testOuts...)
	rep.Verdict = walkVerdict(rep, AnyReconcileWarning(all))
	return rep, all, nil
}

// walkAggregate summarises the test windows.
func walkAggregate(windows []WalkWindow) WalkAggregate {
	a := WalkAggregate{Windows: len(windows)}
	product := 1.0
	returns := make([]float64, 0, len(windows))
	for _, w := range windows {
		product *= 1 + w.Test.NetReturnPct/100
		returns = append(returns, w.Test.NetReturnPct)
		a.RoundTrips += w.Test.RoundTrips
		if w.Test.NetPnL > 0 {
			a.Profitable++
		}
	}
	a.CompoundedReturnPct = 100 * (product - 1)
	a.ProfitablePct = 100 * float64(a.Profitable) / float64(len(windows))
	sort.Float64s(returns)
	a.MinReturnPct, a.MaxReturnPct = returns[0], returns[len(returns)-1]
	a.MedianReturnPct = median(returns)
	return a
}

// walkTuning reports the tuned value per window and in- vs out-of-sample.
func walkTuning(spec WalkSpec, windows []WalkWindow) *WalkTuning {
	t := &WalkTuning{Key: spec.TuneKey, Values: spec.TuneValues}
	for i, w := range windows {
		t.Chosen = append(t.Chosen, w.Chosen)
		if i > 0 && w.Chosen != windows[i-1].Chosen {
			t.Changes++
		}
		for _, row := range w.Train {
			if row.Label == w.TrainLabel(w.Chosen) {
				t.MeanTrainReturnPct += row.NetReturnPct
			}
		}
		t.MeanTestReturnPct += w.Test.NetReturnPct
	}
	t.MeanTrainReturnPct /= float64(len(windows))
	t.MeanTestReturnPct /= float64(len(windows))
	return t
}

// walkNotes states what the train windows are used for and what the aggregate means.
func walkNotes(spec WalkSpec, base Config, r *WalkForwardReport) []string {
	notes := []string{
		"There is no optimiser in this bot. The train window is used for exactly two things: (1) warming the learner: the outcomes it learns in the train window are recorded into the test window's learner before its first tick; (2) only with --tune, choosing the value with the best train-window net return (ties: the first value listed), which is then applied unchanged to the following test window.",
		"Every window starts with the starting capital and values its open positions at its end; only the learner's outcomes carry from train to test. The aggregate compounds the test windows' returns only (as if reinvested between windows); train results never enter it.",
	}
	if base.DisableLearner {
		notes = append(notes, "The learner is off in this run, so the train window only serves the tuning choice.")
	}
	if spec.TuneKey == "" && base.DisableLearner {
		notes = append(notes, "With neither tuning nor the learner, the train windows have no effect on the test windows.")
	}
	if spec.Step > spec.Test {
		notes = append(notes, fmt.Sprintf("The step is longer than the test window: %s between test windows is not tested.", spec.Step-spec.Test))
	}
	last := r.Windows[len(r.Windows)-1].TestEnd
	if last.Before(r.End) {
		notes = append(notes, fmt.Sprintf("The period after %s is shorter than a window and is not tested.", last.Format(time.RFC3339)))
	}
	return notes
}

// Walk-forward verdict rules.
const (
	RuleWalkAccounting     = "accounting_warning"
	RuleWalkWindows        = "test_windows_profitable"
	RuleWalkMostlyLosing   = "most_test_windows_lose"
	RuleWalkFewTrades      = "test_windows_few_trades"
	RuleWalkInVsOut        = "tuned_in_sample_vs_out_of_sample"
	RuleWalkUnstableChoice = "tuned_value_unstable"
)

// walkVerdict applies the walk-forward rules.
func walkVerdict(r *WalkForwardReport, accounting bool) []VerdictLine {
	var out []VerdictLine
	add := func(rule, format string, args ...any) {
		out = append(out, VerdictLine{Rule: rule, Text: fmt.Sprintf(format, args...)})
	}
	a := r.Aggregate
	if accounting {
		add(RuleWalkAccounting, "A run failed its accounting reconciliation: do not trust these numbers.")
	}
	add(RuleWalkWindows, "%d of %d test windows were net positive (%.0f %%); the compounded test-window return is %+.2f %% (min %+.2f %%, median %+.2f %%, max %+.2f %%).",
		a.Profitable, a.Windows, a.ProfitablePct, a.CompoundedReturnPct, a.MinReturnPct, a.MedianReturnPct, a.MaxReturnPct)
	if a.ProfitablePct < 50 {
		add(RuleWalkMostlyLosing, "Most test windows lost money.")
	}
	if a.RoundTrips < MeaningfulTrades {
		add(RuleWalkFewTrades, "The test windows hold %d round trips in total, fewer than %d: not statistically meaningful.", a.RoundTrips, MeaningfulTrades)
	}
	if t := r.Tuning; t != nil {
		add(RuleWalkInVsOut, "The chosen %s values averaged %+.2f %% in their train windows (in-sample) and %+.2f %% in the following test windows (out of sample).",
			t.Key, t.MeanTrainReturnPct, t.MeanTestReturnPct)
		if t.Changes > 0 {
			add(RuleWalkUnstableChoice, "The chosen value changed %d times over %d windows: the best setting is not stable.", t.Changes, len(t.Chosen))
		}
	}
	return out
}
