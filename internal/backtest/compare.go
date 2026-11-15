package backtest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// Evidence thresholds (docs/14): what split, walk-forward and sensitivity
// results must all show before any output may speak of an edge.
const (
	EvidenceMinOOSTrades        = 30  // out-of-sample round trips
	EvidenceMinWindows          = 3   // walk-forward test windows
	EvidenceMinWindowTrades     = 30  // walk-forward test round trips in total
	EvidenceMinProfitableWindow = 60  // percent of test windows net positive
	EvidenceFeeStress           = 1.5 // net positive at every tested fee up to this multiple of the baseline fee
)

// BaseInfo records a multi-run command's base inputs (run.json at the top
// of its output directory).
type BaseInfo struct {
	Kind            string            `json:"kind"`
	ToolVersion     string            `json:"tool_version"`
	BuildRevision   string            `json:"build_revision"`
	CommandLine     []string          `json:"command_line"`
	Seed            int64             `json:"seed"`
	StrategyID      string            `json:"strategy_id"`
	Start           time.Time         `json:"start"`
	End             time.Time         `json:"end"`
	StartingCapital float64           `json:"starting_capital"`
	Markets         []string          `json:"markets"`
	Universe        *UniverseRule     `json:"universe"`
	LearnerEnabled  bool              `json:"learner_enabled"`
	Settings        map[string]string `json:"settings"`
	Exchange        ExchangeConfig    `json:"exchange"`
}

// NewBaseInfo returns the base inputs of a multi-run command.
func NewBaseInfo(kind string, cfg Config, cmdline []string) BaseInfo {
	capital := cfg.StartingCapital
	if capital <= 0 {
		capital = cfg.Settings.StartingCapital
	}
	if cmdline == nil {
		cmdline = []string{}
	}
	return BaseInfo{
		Kind: kind, ToolVersion: ToolVersion, BuildRevision: buildRevision(), CommandLine: cmdline, Seed: cfg.Seed,
		StrategyID: cfg.StrategyID, Start: cfg.Start, End: cfg.End, StartingCapital: capital,
		Markets: append([]string{}, cfg.Markets...), Universe: cfg.Universe, LearnerEnabled: !cfg.DisableLearner,
		Settings: SettingsSnapshot(cfg.Settings), Exchange: cfg.Exchange,
	}
}

// CompareEntry is one compared directory.
type CompareEntry struct {
	Dir        string    `json:"dir"`
	Kind       string    `json:"kind"`
	StrategyID string    `json:"strategy_id"`
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	Row        *RunRow   `json:"row"`      // single runs
	Headline   string    `json:"headline"` // first verdict line
	inputs     map[string]string
}

// Difference is an input whose value differs between directories (one value per entry, "" when absent).
type Difference struct {
	Key    string   `json:"key"`
	Values []string `json:"values"`
}

// EvidenceCondition is one documented condition.
type EvidenceCondition struct {
	Name   string `json:"name"`
	Met    bool   `json:"met"`
	Detail string `json:"detail"`
}

// Evidence is the combined robustness check.
type Evidence struct {
	Met        bool                `json:"met"`
	Conditions []EvidenceCondition `json:"conditions"`
	Text       string              `json:"text"`
}

// CompareReport compares result directories.
type CompareReport struct {
	SchemaVersion int            `json:"summary_schema_version"`
	Kind          string         `json:"kind"`
	Entries       []CompareEntry `json:"entries"`
	Differences   []Difference   `json:"differences"`
	Evidence      *Evidence      `json:"evidence"` // only when split, walk-forward and sensitivity summaries are all given
	Notes         []string       `json:"notes"`
}

// readJSON decodes the JSON file at path into v.
func readJSON(path string, v any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

// flatten adds a JSON object's leaves to out as text, keyed by prefix.key.
func flatten(prefix string, v any, out map[string]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			flatten(prefix+"."+k, child, out)
		}
	case float64:
		out[prefix] = fmtNum(x)
	case nil:
		out[prefix] = ""
	default:
		raw, _ := json.Marshal(x)
		out[prefix] = strings.Trim(string(raw), `"`)
	}
}

// inputsOf reads the inputs recorded in dir/run.json.
func inputsOf(dir string) map[string]string {
	var run map[string]any
	out := map[string]string{}
	if readJSON(filepath.Join(dir, "run.json"), &run) != nil {
		return out
	}
	for _, key := range []string{"strategy_id", "start", "end", "starting_capital", "learner_enabled", "initial_markets", "markets", "universe", "seed"} {
		if v, ok := run[key]; ok {
			flatten(key, v, out)
		}
	}
	if s, ok := run["settings"]; ok {
		flatten("settings", s, out)
	}
	if ex, ok := run["exchange"].(map[string]any); ok {
		if rules, ok := ex["pair_rules"].(map[string]any); ok {
			n := 0
			if r, ok := rules["rules"].(map[string]any); ok {
				n = len(r)
			}
			ex["pair_rules"] = fmt.Sprintf("%v (%d pairs)", rules["source"], n)
		}
		flatten("exchange", ex, out)
	}
	return out
}

// Compare reads the summaries of several result directories.
func Compare(dirs []string) (*CompareReport, error) {
	if len(dirs) < 1 {
		return nil, errors.New("give at least one result directory")
	}
	rep := &CompareReport{SchemaVersion: SummarySchemaVersion, Kind: KindCompare, Entries: []CompareEntry{}, Differences: []Difference{}}
	var split *SplitReport
	var walk *WalkForwardReport
	var sens *SensitivityReport
	for _, dir := range dirs {
		path := filepath.Join(dir, "summary.json")
		var head struct {
			Kind       string        `json:"kind"`
			StrategyID string        `json:"strategy_id"`
			Start      time.Time     `json:"start"`
			End        time.Time     `json:"end"`
			Verdict    []VerdictLine `json:"verdict"`
		}
		if err := readJSON(path, &head); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		e := CompareEntry{Dir: dir, Kind: head.Kind, StrategyID: head.StrategyID, Start: head.Start, End: head.End, inputs: inputsOf(dir)}
		if len(head.Verdict) > 0 {
			e.Headline = head.Verdict[0].Text
		}
		var err error
		switch head.Kind {
		case KindRun:
			var r Report
			if err = readJSON(path, &r); err == nil {
				row := NewRunRow(filepath.Base(dir), &r)
				e.Row = &row
			}
		case KindSplit:
			if split == nil {
				split = &SplitReport{}
				err = readJSON(path, split)
			}
		case KindWalkForward:
			if walk == nil {
				walk = &WalkForwardReport{}
				err = readJSON(path, walk)
			}
		case KindSensitivity:
			if sens == nil {
				sens = &SensitivityReport{}
				err = readJSON(path, sens)
			}
		case KindRegimes:
		default:
			return nil, fmt.Errorf("%s: unknown summary kind %q", path, head.Kind)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		rep.Entries = append(rep.Entries, e)
	}
	rep.Differences = differences(rep.Entries)
	if split != nil && walk != nil && sens != nil {
		rep.Evidence = evaluateEvidence(split, walk, sens)
	} else {
		rep.Notes = append(rep.Notes, "The robustness evidence check needs a split, a walkforward and a sensitivity summary among the directories; it was not evaluated.")
	}
	return rep, nil
}

// differences lists the inputs whose values are not all equal.
func differences(entries []CompareEntry) []Difference {
	keys := map[string]bool{}
	for _, e := range entries {
		for k := range e.inputs {
			keys[k] = true
		}
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	out := []Difference{}
	for _, k := range sorted {
		d := Difference{Key: k}
		same := true
		for i, e := range entries {
			d.Values = append(d.Values, e.inputs[k])
			if i > 0 && e.inputs[k] != entries[0].inputs[k] {
				same = false
			}
		}
		if !same {
			out = append(out, d)
		}
	}
	return out
}

// evaluateEvidence applies the documented conditions to the three summaries.
func evaluateEvidence(split *SplitReport, walk *WalkForwardReport, sens *SensitivityReport) *Evidence {
	ev := &Evidence{Met: true}
	add := func(name string, met bool, format string, args ...any) {
		ev.Conditions = append(ev.Conditions, EvidenceCondition{Name: name, Met: met, Detail: fmt.Sprintf(format, args...)})
		ev.Met = ev.Met && met
	}
	is, oos := split.InSample, split.OutOfSample
	add("out_of_sample", is.NetPnL > 0 && oos.NetPnL > 0 && oos.RoundTrips >= EvidenceMinOOSTrades,
		"in-sample %+.2f %%, out-of-sample %+.2f %% over %d round trips (needs both positive and at least %d out-of-sample round trips)",
		is.NetReturnPct, oos.NetReturnPct, oos.RoundTrips, EvidenceMinOOSTrades)
	a := walk.Aggregate
	add("walk_forward", a.CompoundedReturnPct > 0 && a.ProfitablePct >= EvidenceMinProfitableWindow && a.Windows >= EvidenceMinWindows && a.RoundTrips >= EvidenceMinWindowTrades,
		"%d of %d test windows positive (%.0f %%), compounded %+.2f %%, %d round trips (needs a positive compounded return, at least %d %% of at least %d windows positive and %d round trips)",
		a.Profitable, a.Windows, a.ProfitablePct, a.CompoundedReturnPct, a.RoundTrips, EvidenceMinProfitableWindow, EvidenceMinWindows, EvidenceMinWindowTrades)
	limit := EvidenceFeeStress * sens.BaselineFeeBps
	stressed, allPositive := false, sens.Baseline.NetPnL > 0
	for _, r := range sens.Rows {
		if r.Dimension != DimFees {
			continue
		}
		if r.FeeBps >= limit {
			stressed = true
		}
		if r.FeeBps <= limit && r.Row.NetPnL <= 0 {
			allPositive = false
		}
	}
	add("fee_stress", stressed && allPositive,
		"net positive at every tested fee up to %s bp (%g × the baseline %s bp): %v; a fee of at least %s bp tested: %v",
		fmtNum(limit), EvidenceFeeStress, fmtNum(sens.BaselineFeeBps), allPositive, fmtNum(limit), stressed)
	rows := []RunRow{is, oos, sens.Baseline}
	for _, w := range walk.Windows {
		rows = append(rows, w.Test)
	}
	var staleSign, dataGaps []string
	for _, r := range rows {
		if r.StaleDecidesSign {
			staleSign = append(staleSign, r.Label)
		}
		if r.DataWarnings > 0 {
			dataGaps = append(dataGaps, r.Label)
		}
	}
	add("no_stale_sign", len(staleSign) == 0, "runs positive only because stale positions are valued at their last trade: %v", staleSign)
	add("data_coverage", len(dataGaps) == 0, "runs with data-coverage warnings: %v", dataGaps)
	same := split.StrategyID == walk.StrategyID && walk.StrategyID == sens.StrategyID
	add("same_strategy", same, "strategies: %s, %s, %s", split.StrategyID, walk.StrategyID, sens.StrategyID)

	if ev.Met {
		ev.Text = "All documented robustness conditions are met (out-of-sample, walk-forward and fee stress): over this history the evidence is consistent with an edge. " +
			"History does not guarantee future results; this is still one market history, with the assumptions listed in each run."
		return ev
	}
	var failed []string
	for _, c := range ev.Conditions {
		if !c.Met {
			failed = append(failed, c.Name)
		}
	}
	ev.Text = "Not all robustness conditions are met (" + strings.Join(failed, ", ") + "): do not treat these results as evidence of a lasting advantage."
	return ev
}

// RenderCompare renders the comparison as plain-text tables.
func RenderCompare(r *CompareReport) string {
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "dir\tkind\tstrategy\tperiod\tnet %\tround trips\twin rate\tprofit factor\tmax DD %\tbenchmark %\texcess pp")
	for _, e := range r.Entries {
		period := e.Start.Format(time.DateOnly) + ".." + e.End.Format(time.DateOnly)
		if e.Row == nil {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", e.Dir, e.Kind, e.StrategyID, period, e.Headline)
			continue
		}
		x := e.Row
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%+.2f\t%d\t%s\t%s\t%.2f\t%s\t%s\n", e.Dir, e.Kind, e.StrategyID, period, x.NetReturnPct, x.RoundTrips,
			opt(x.WinRatePct, "%.1f", ""), opt(x.ProfitFactor, "%.2f", ""), x.MaxDrawdownPct, opt(x.BenchmarkReturnPct, "%+.2f", ""), opt(x.ExcessReturnPct, "%+.2f", ""))
	}
	w.Flush()
	if len(r.Differences) > 0 {
		b.WriteString("\ninputs that differ:\n")
		w = tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
		for _, d := range r.Differences {
			fmt.Fprintf(w, "  %s\t%s\n", d.Key, strings.Join(d.Values, "\t"))
		}
		w.Flush()
	} else {
		b.WriteString("\nno recorded input differs between the directories\n")
	}
	if r.Evidence != nil {
		b.WriteString("\nrobustness evidence:\n")
		for _, c := range r.Evidence.Conditions {
			state := "NOT MET"
			if c.Met {
				state = "met"
			}
			fmt.Fprintf(&b, "  %-14s %-8s %s\n", c.Name, state, c.Detail)
		}
		fmt.Fprintf(&b, "  %s\n", r.Evidence.Text)
	}
	for _, n := range r.Notes {
		fmt.Fprintf(&b, "\nnote: %s\n", n)
	}
	return b.String()
}
