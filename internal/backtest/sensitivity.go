package backtest

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/histdata"
)

// Sensitivity dimensions besides setting keys.
const (
	DimFees         = "fees"          // basis points or a fee preset
	DimLatency      = "latency"       // whole 30 s ticks
	DimHalfSpread   = "half-spread"   // bp: the spread model's floor, or the fixed model's half-spread
	DimImpact       = "impact"        // bp per unit of participation above the threshold
	DimCapital      = "capital"       // starting capital
	DimFillModel    = "fill-model"    // spread | fixed
	DimEmptyCandles = "empty-candles" // false | true: flat candles for intervals without trades
)

// DefaultDimensions are the stresses run when none are named.
func DefaultDimensions() []Dimension {
	return []Dimension{
		{Name: DimFees, Values: []string{"26", "40", "80", "120"}},
		{Name: DimLatency, Values: []string{"1", "2", "4", "10"}},
		{Name: DimHalfSpread, Values: []string{"2", "5", "10", "20"}},
		{Name: DimImpact, Values: []string{"50", "100", "200", "400"}},
		{Name: DimEmptyCandles, Values: []string{"false", "true"}},
	}
}

// Dimension is one input and the values to try.
type Dimension struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`
}

// SensitivitySpec lists the dimensions varied one at a time and, optionally,
// two of them to combine as a grid.
type SensitivitySpec struct {
	Dimensions []Dimension
	Grid       []string
}

// ApplyDimension sets one input of cfg. A setting key is parsed and
// validated by the dashboard's settings registry; secrets and settings that
// do not affect a backtest are refused.
func ApplyDimension(cfg *Config, name, value string) error {
	number := func(min float64, exclusive bool) (float64, error) {
		v, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < min || (exclusive && v == min) {
			return 0, fmt.Errorf("%s: invalid value %q", name, value)
		}
		return v, nil
	}
	switch name {
	case DimFees:
		if err := cfg.Exchange.SetFees(value); err != nil {
			return err
		}
	case DimLatency:
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || n < 1 {
			return fmt.Errorf("latency: whole ticks of 30 s, at least 1, got %q", value)
		}
		cfg.Exchange.LatencyTicks = n
	case DimHalfSpread:
		v, err := number(0, false)
		if err != nil {
			return err
		}
		if cfg.Exchange.FillModel == FillFixed {
			cfg.Exchange.FixedHalfSpreadBps = v
		} else {
			cfg.Exchange.MinHalfSpreadBps = v
		}
	case DimImpact:
		v, err := number(0, false)
		if err != nil {
			return err
		}
		cfg.Exchange.ImpactBpsPerUnit = v
	case DimCapital:
		v, err := number(0, true)
		if err != nil {
			return err
		}
		cfg.StartingCapital = v
	case DimFillModel:
		cfg.Exchange.FillModel = value
	case DimEmptyCandles:
		v, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("%s: true or false, got %q", name, value)
		}
		cfg.FillEmptyCandles = v
	default:
		field, ok := config.FieldByKey(name)
		switch {
		case !ok:
			return fmt.Errorf("unknown dimension or setting %q", name)
		case field.Secret:
			return fmt.Errorf("%s is a secret; a backtest never reads secrets", name)
		case !settingsAllowed(field):
			return fmt.Errorf("%s does not affect a backtest", name)
		}
		if err := field.Apply(&cfg.Settings, value); err != nil {
			return err
		}
		if !slices.Contains(cfg.ExplicitSettings, name) {
			cfg.ExplicitSettings = append(slices.Clone(cfg.ExplicitSettings), name)
		}
	}
	return cfg.Exchange.Validate()
}

// fingerprint identifies the inputs of a run that variations change, so a
// variant equal to another reuses its run.
func fingerprint(cfg Config) string {
	capital := cfg.StartingCapital
	if capital <= 0 {
		capital = cfg.Settings.StartingCapital
	}
	ex := cfg.Exchange
	ex.FeePreset, ex.FeeSource = "", "" // labels only: 80 and kraken-base charge the same
	raw, _ := json.Marshal(struct {
		Exchange         ExchangeConfig
		Settings         map[string]string
		Capital          float64
		FillEmptyCandles bool
		RiskFeeExplicit  bool // the only explicit key that changes a run (alignRiskFee)
	}{ex, SettingsSnapshot(cfg.Settings), capital, cfg.FillEmptyCandles, slices.Contains(cfg.ExplicitSettings, "FEE_AND_SLIPPAGE")})
	return string(raw)
}

// SensitivityRow is one variant.
type SensitivityRow struct {
	Dimension    string  `json:"dimension"`
	Value        string  `json:"value"`
	Baseline     bool    `json:"baseline"` // this variant is the baseline run
	Run          string  `json:"run"`      // label of the run it used
	FeeBps       float64 `json:"fee_bps"`  // the fee the run charged
	Row          RunRow  `json:"result"`
	SignSurvives bool    `json:"sign_survives"` // net P&L > 0 exactly when the baseline's is
}

// DimensionSurvival says whether the sign of the result survives every value of a dimension.
type DimensionSurvival struct {
	Dimension string   `json:"dimension"`
	Survives  bool     `json:"survives"`
	Failing   []string `json:"failing"`
}

// Break-even statuses.
const (
	BreakEvenInterpolated = "interpolated"
	BreakEvenAboveTested  = "above_highest_tested_fee"
	BreakEvenBelowTested  = "below_lowest_tested_fee"
	BreakEvenNotMonotonic = "not_monotonic"
	BreakEvenNotTested    = "not_tested"
)

// FeePoint is a fee level and the net return at it.
type FeePoint struct {
	FeeBps       float64
	NetReturnPct float64
}

// BreakEven is the fee at which the net return reaches zero.
type BreakEven struct {
	Status string   `json:"status"`
	FeeBps *float64 `json:"fee_bps"`
	Note   string   `json:"note"`
}

// BreakEvenFee estimates the break-even fee by linear interpolation between
// the bracketing tested fee levels: the first level with a positive net
// return followed by one without.
func BreakEvenFee(points []FeePoint) BreakEven {
	if len(points) == 0 {
		return BreakEven{Status: BreakEvenNotTested, Note: "no fee levels were tested"}
	}
	p := append([]FeePoint(nil), points...)
	sort.Slice(p, func(i, j int) bool { return p[i].FeeBps < p[j].FeeBps })
	positive := 0
	for _, x := range p {
		if x.NetReturnPct > 0 {
			positive++
		}
	}
	switch positive {
	case len(p):
		v := p[len(p)-1].FeeBps
		return BreakEven{Status: BreakEvenAboveTested, FeeBps: &v, Note: fmt.Sprintf("net positive at every tested fee up to %s bp; the break-even is higher (not measured)", fmtNum(v))}
	case 0:
		v := p[0].FeeBps
		return BreakEven{Status: BreakEvenBelowTested, FeeBps: &v, Note: fmt.Sprintf("not net positive at any tested fee down to %s bp; the break-even is lower, if it exists", fmtNum(v))}
	}
	for i := 0; i+1 < len(p); i++ {
		a, b := p[i], p[i+1]
		if a.NetReturnPct > 0 && b.NetReturnPct <= 0 {
			v := a.FeeBps + (0-a.NetReturnPct)*(b.FeeBps-a.FeeBps)/(b.NetReturnPct-a.NetReturnPct)
			note := fmt.Sprintf("estimate: linear interpolation between %s bp (%+.3f %%) and %s bp (%+.3f %%); the true curve between them was not measured",
				fmtNum(a.FeeBps), a.NetReturnPct, fmtNum(b.FeeBps), b.NetReturnPct)
			if positive != i+1 {
				note += "; the net return is not monotonic in the fee"
			}
			return BreakEven{Status: BreakEvenInterpolated, FeeBps: &v, Note: note}
		}
	}
	return BreakEven{Status: BreakEvenNotMonotonic, Note: "the net return rises with the fee somewhere in the tested range: no break-even estimate"}
}

// SensitivityReport is the sensitivity summary.
type SensitivityReport struct {
	SchemaVersion  int                 `json:"summary_schema_version"`
	Kind           string              `json:"kind"`
	StrategyID     string              `json:"strategy_id"`
	Start          time.Time           `json:"start"`
	End            time.Time           `json:"end"`
	Baseline       RunRow              `json:"baseline"`
	BaselineFeeBps float64             `json:"baseline_fee_bps"`
	BaselineValues map[string]string   `json:"baseline_values"`
	Rows           []SensitivityRow    `json:"rows"`
	BreakEvenFee   BreakEven           `json:"break_even_fee"`
	Survival       []DimensionSurvival `json:"survival"`
	Notes          []string            `json:"notes"`
	Verdict        []VerdictLine       `json:"verdict"`
}

// baselineValue returns cfg's current value of a dimension.
func baselineValue(cfg Config, name string) string {
	switch name {
	case DimFees:
		return fmtNum(cfg.Exchange.FeeBps)
	case DimLatency:
		return strconv.Itoa(cfg.Exchange.LatencyTicks)
	case DimHalfSpread:
		if cfg.Exchange.FillModel == FillFixed {
			return fmtNum(cfg.Exchange.FixedHalfSpreadBps)
		}
		return fmtNum(cfg.Exchange.MinHalfSpreadBps)
	case DimImpact:
		return fmtNum(cfg.Exchange.ImpactBpsPerUnit)
	case DimCapital:
		if cfg.StartingCapital > 0 {
			return fmtNum(cfg.StartingCapital)
		}
		return fmtNum(cfg.Settings.StartingCapital)
	case DimFillModel:
		return cfg.Exchange.FillModel
	case DimEmptyCandles:
		return strconv.FormatBool(cfg.FillEmptyCandles)
	}
	if f, ok := config.FieldByKey(name); ok {
		return f.Value(cfg.Settings)
	}
	return ""
}

// plannedRow is a row waiting for its run.
type plannedRow struct {
	dimension, value string
	run              int
}

// RunSensitivity re-runs base varying one dimension at a time (and the grid,
// if given). A variant equal to another (the baseline included) reuses its run.
func RunSensitivity(ctx context.Context, reader histdata.Reader, base Config, spec SensitivitySpec, parallel int, progress Progress) (*SensitivityReport, []RunOutcome, error) {
	specs := []RunSpec{{Label: "baseline", Config: base}}
	index := map[string]int{fingerprint(base): 0}
	var planned []plannedRow
	plan := func(dimension, value, label string, cfg Config) {
		fp := fingerprint(cfg)
		i, ok := index[fp]
		if !ok {
			i = len(specs)
			index[fp] = i
			specs = append(specs, RunSpec{Label: label, Config: cfg})
		}
		planned = append(planned, plannedRow{dimension: dimension, value: value, run: i})
	}
	values := map[string][]string{}
	baseline := map[string]string{}
	for _, d := range spec.Dimensions {
		if _, dup := values[d.Name]; dup || len(d.Values) == 0 {
			return nil, nil, fmt.Errorf("dimension %q is empty or given twice", d.Name)
		}
		values[d.Name] = d.Values
		baseline[d.Name] = baselineValue(base, d.Name)
		for _, v := range d.Values {
			cfg := base
			if err := ApplyDimension(&cfg, d.Name, v); err != nil {
				return nil, nil, fmt.Errorf("%s=%s: %w", d.Name, v, err)
			}
			plan(d.Name, v, d.Name+"="+v, cfg)
		}
	}
	if len(spec.Grid) > 0 {
		if len(spec.Grid) != 2 || spec.Grid[0] == spec.Grid[1] || values[spec.Grid[0]] == nil || values[spec.Grid[1]] == nil {
			return nil, nil, fmt.Errorf("the grid needs two different dimensions that are also varied, got %v", spec.Grid)
		}
		a, b := spec.Grid[0], spec.Grid[1]
		for _, va := range values[a] {
			for _, vb := range values[b] {
				cfg := base
				if err := ApplyDimension(&cfg, a, va); err != nil {
					return nil, nil, err
				}
				if err := ApplyDimension(&cfg, b, vb); err != nil {
					return nil, nil, err
				}
				plan(a+"×"+b, va+","+vb, a+"="+va+","+b+"="+vb, cfg)
			}
		}
	}
	outs, err := RunAll(ctx, reader, specs, parallel, progress)
	if err != nil {
		return nil, nil, err
	}
	rep := &SensitivityReport{
		SchemaVersion: SummarySchemaVersion, Kind: KindSensitivity, StrategyID: outs[0].Report.StrategyID,
		Start: base.Start, End: base.End, Baseline: NewRunRow("baseline", outs[0].Report),
		BaselineFeeBps: outs[0].Report.Result().Exchange.FeeBps, BaselineValues: baseline,
	}
	points := []FeePoint{{FeeBps: rep.BaselineFeeBps, NetReturnPct: rep.Baseline.NetReturnPct}}
	seenFee := map[float64]bool{rep.BaselineFeeBps: true}
	for _, p := range planned {
		o := outs[p.run]
		fee := o.Report.Result().Exchange.FeeBps
		rep.Rows = append(rep.Rows, SensitivityRow{Dimension: p.dimension, Value: p.value, Baseline: p.run == 0, Run: o.Label, FeeBps: fee, Row: NewRunRow(o.Label, o.Report)})
		if p.dimension == DimFees && !seenFee[fee] {
			seenFee[fee] = true
			points = append(points, FeePoint{FeeBps: fee, NetReturnPct: o.Report.Metrics.NetReturnPct})
		}
	}
	rep.BreakEvenFee = BreakEven{Status: BreakEvenNotTested, Note: "fees were not varied"}
	if values[DimFees] != nil {
		rep.BreakEvenFee = BreakEvenFee(points)
	}
	rep.markSurvival()
	rep.Notes = []string{
		"Each variant re-runs the same period with one input changed (two in the grid); everything else is the baseline's. Trades can differ between variants because sizing, the cash check and the learner react to the changed costs.",
		"The baseline is marked; a variant whose inputs equal another's reuses that run.",
		"A sign survives when the variant's net P&L is positive exactly when the baseline's is.",
	}
	rep.Verdict = sensitivityVerdict(rep, AnyReconcileWarning(outs))
	return rep, outs, nil
}

// markSurvival marks each row's sign survival and summarises it per dimension.
func (r *SensitivityReport) markSurvival() {
	basePositive := r.Baseline.NetPnL > 0
	r.Survival = nil
	at := map[string]int{}
	for i := range r.Rows {
		row := &r.Rows[i]
		row.SignSurvives = (row.Row.NetPnL > 0) == basePositive
		j, ok := at[row.Dimension]
		if !ok {
			j = len(r.Survival)
			at[row.Dimension] = j
			r.Survival = append(r.Survival, DimensionSurvival{Dimension: row.Dimension, Survives: true, Failing: []string{}})
		}
		if !row.SignSurvives {
			r.Survival[j].Survives = false
			r.Survival[j].Failing = append(r.Survival[j].Failing, row.Value)
		}
	}
}

// Sensitivity verdict rules.
const (
	RuleSensAccounting       = "accounting_warning"
	RuleSensNoTrades         = "baseline_no_round_trips"
	RuleSensBaselineNegative = "baseline_not_positive"
	RuleSensBreakEven        = "positive_only_below_fee"
	RuleSensFeeRobust        = "positive_at_every_tested_fee"
	RuleSensNegativeAllFees  = "not_positive_at_any_tested_fee"
	RuleSensSignFlips        = "sign_does_not_survive"
	RuleSensSignSurvives     = "sign_survives_every_stress"
)

// sensitivityVerdict applies the sensitivity rules.
func sensitivityVerdict(r *SensitivityReport, accounting bool) []VerdictLine {
	var out []VerdictLine
	add := func(rule, format string, args ...any) {
		out = append(out, VerdictLine{Rule: rule, Text: fmt.Sprintf(format, args...)})
	}
	if accounting {
		add(RuleSensAccounting, "A run failed its accounting reconciliation: do not trust these numbers.")
	}
	switch {
	case r.Baseline.RoundTrips == 0:
		add(RuleSensNoTrades, "The baseline made no round trips: the stresses say nothing about profitability.")
	case r.Baseline.NetPnL <= 0:
		add(RuleSensBaselineNegative, "The baseline is not net positive after costs (%+.2f %%); the stresses can only show how much better or worse it gets.", r.Baseline.NetReturnPct)
	}
	be := r.BreakEvenFee
	switch be.Status {
	case BreakEvenInterpolated:
		add(RuleSensBreakEven, "Net positive only below about %.0f bp fees (an estimate by linear interpolation; the baseline fee is %s bp).", *be.FeeBps, fmtNum(r.BaselineFeeBps))
	case BreakEvenAboveTested:
		add(RuleSensFeeRobust, "Net positive at every tested fee up to %s bp.", fmtNum(*be.FeeBps))
	case BreakEvenBelowTested:
		add(RuleSensNegativeAllFees, "Not net positive at any tested fee, down to %s bp.", fmtNum(*be.FeeBps))
	}
	var failing []string
	for _, s := range r.Survival {
		for _, v := range s.Failing {
			failing = append(failing, s.Dimension+"="+v)
		}
	}
	if len(failing) > 0 {
		add(RuleSensSignFlips, "The sign of the result does not survive: %s.", strings.Join(failing, ", "))
	} else if len(r.Rows) > 0 {
		add(RuleSensSignSurvives, "The sign of the result survives every tested variation.")
	}
	return out
}
