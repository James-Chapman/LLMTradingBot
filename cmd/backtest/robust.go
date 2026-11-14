package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/backtest"
	"github.com/James-Chapman/LLMTradingBot/internal/histdata"
)

// listFlag collects a repeatable string flag.
type listFlag []string

// String returns the values joined by spaces.
func (l *listFlag) String() string { return strings.Join(*l, " ") }

// Set appends one value.
func (l *listFlag) Set(v string) error {
	*l = append(*l, v)
	return nil
}

// robustFlags holds the flags of the multi-run commands besides the run flags.
type robustFlags struct {
	parallel int

	splitAt     string
	oosFraction float64

	train, test, step, tune string

	bucket     string
	bull, bear float64
	set        map[string]bool // flags given explicitly

	dims, grid                                                 string
	feesList, latencyList, spreadList, impactList, capitalList string
	fillModels                                                 string
	vary                                                       listFlag
}

// addRobustFlags defines the flags of one multi-run command on fs.
func addRobustFlags(fs *flag.FlagSet, kind string) *robustFlags {
	f := &robustFlags{set: map[string]bool{}}
	if kind != backtest.KindRegimes { // regimes segments one continuous run
		fs.IntVar(&f.parallel, "parallel", 1, "runs at the same time; the results do not depend on it")
	}
	switch kind {
	case backtest.KindSplit:
		fs.StringVar(&f.splitAt, "split", "", "first out-of-sample day, YYYY-MM-DD (UTC)")
		fs.Float64Var(&f.oosFraction, "oos-fraction", 0, "out-of-sample share of the period, e.g. 0.3 (instead of --split)")
	case backtest.KindWalkForward:
		fs.StringVar(&f.train, "train", "", "train window length: Nd (days) or a Go duration (required)")
		fs.StringVar(&f.test, "test", "", "test window length: Nd or a Go duration (required)")
		fs.StringVar(&f.step, "step", "", "window step (default --test; shorter than --test is refused)")
		fs.StringVar(&f.tune, "tune", "", "KEY=v1,v2,...: choose the value with the best train return per window")
	case backtest.KindRegimes:
		fs.StringVar(&f.bucket, "bucket", "month", "bucket length: day, week, month or quarter")
		fs.Float64Var(&f.bull, "bull", 0, "bucket market return above which it is bull, % (default by bucket)")
		fs.Float64Var(&f.bear, "bear", 0, "bucket market return below which it is bear, % (default by bucket)")
	case backtest.KindSensitivity:
		fs.StringVar(&f.dims, "dims", "", "dimensions to vary one at a time (default fees,latency,half-spread,impact)")
		fs.StringVar(&f.feesList, "fees-list", "", "fee values, bp or preset")
		fs.StringVar(&f.latencyList, "latency-list", "", "latency values, whole 30 s ticks")
		fs.StringVar(&f.spreadList, "half-spread-list", "", "half-spread values, bp")
		fs.StringVar(&f.impactList, "impact-list", "", "impact values, bp per unit of participation")
		fs.StringVar(&f.capitalList, "capital-list", "", "starting capital values")
		fs.StringVar(&f.fillModels, "fill-models", "", "fill models: spread,fixed")
		fs.Var(&f.vary, "vary", "KEY=v1,v2,...: vary a setting (repeatable)")
		fs.StringVar(&f.grid, "grid", "", "A,B: also run every combination of two varied dimensions")
	}
	return f
}

// parseSpan parses a window length: Nd for N days, or a Go duration.
func parseSpan(s string) (time.Duration, error) {
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil || days < 1 {
			return 0, fmt.Errorf("invalid length %q", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid length %q (use Nd or a duration such as 72h)", s)
	}
	return d, nil
}

// splitValues splits a comma-separated list, dropping empty items.
func splitValues(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// keyValues parses KEY=v1,v2,... and checks every value applies to cfg.
func keyValues(s string, cfg backtest.Config) (backtest.Dimension, error) {
	key, vals, ok := strings.Cut(s, "=")
	d := backtest.Dimension{Name: strings.TrimSpace(key), Values: splitValues(vals)}
	if !ok || d.Name == "" || len(d.Values) == 0 {
		return d, fmt.Errorf("want KEY=v1,v2,..., got %q", s)
	}
	if err := checkDimension(d, cfg); err != nil {
		return d, err
	}
	return d, nil
}

// checkDimension applies each value of d to a copy of cfg, so a bad value is
// a usage error before any run starts.
func checkDimension(d backtest.Dimension, cfg backtest.Config) error {
	for _, v := range d.Values {
		c := cfg
		if err := backtest.ApplyDimension(&c, d.Name, v); err != nil {
			return err
		}
	}
	return nil
}

// sensitivitySpec builds the sensitivity dimensions from the flags.
func (f *robustFlags) sensitivitySpec(cfg backtest.Config) (backtest.SensitivitySpec, error) {
	var spec backtest.SensitivitySpec
	lists := map[string]string{
		backtest.DimFees: f.feesList, backtest.DimLatency: f.latencyList, backtest.DimHalfSpread: f.spreadList,
		backtest.DimImpact: f.impactList, backtest.DimCapital: f.capitalList, backtest.DimFillModel: f.fillModels,
	}
	defaults := map[string][]string{}
	for _, d := range backtest.DefaultDimensions() {
		defaults[d.Name] = d.Values
	}
	names := splitValues(f.dims)
	if f.dims == "" && len(f.vary) == 0 {
		for _, d := range backtest.DefaultDimensions() {
			names = append(names, d.Name)
		}
	}
	chosen := map[string]bool{}
	for _, n := range names {
		list, known := lists[n]
		if !known {
			return spec, fmt.Errorf("--dims: unknown dimension %q (fees, latency, half-spread, impact, capital, fill-model; use --vary for settings)", n)
		}
		if chosen[n] {
			return spec, fmt.Errorf("--dims: %s given twice", n)
		}
		chosen[n] = true
		d := backtest.Dimension{Name: n, Values: defaults[n]}
		if list != "" {
			d.Values = splitValues(list)
		}
		if len(d.Values) == 0 {
			return spec, fmt.Errorf("dimension %s has no default values; give them with its list flag", n)
		}
		if err := checkDimension(d, cfg); err != nil {
			return spec, err
		}
		spec.Dimensions = append(spec.Dimensions, d)
	}
	for n, list := range lists {
		if list != "" && !chosen[n] {
			return spec, fmt.Errorf("a value list was given for %s, which is not in --dims", n)
		}
	}
	for _, v := range f.vary {
		d, err := keyValues(v, cfg)
		if err != nil {
			return spec, fmt.Errorf("--vary: %v", err)
		}
		if chosen[d.Name] {
			return spec, fmt.Errorf("--vary: %s given twice", d.Name)
		}
		chosen[d.Name] = true
		spec.Dimensions = append(spec.Dimensions, d)
	}
	if f.grid != "" {
		spec.Grid = splitValues(f.grid)
		if len(spec.Grid) != 2 || spec.Grid[0] == spec.Grid[1] || !chosen[spec.Grid[0]] || !chosen[spec.Grid[1]] {
			return spec, fmt.Errorf("--grid: want two different varied dimensions, got %q", f.grid)
		}
	}
	return spec, nil
}

// stripParallel removes --parallel from a command line: the results do not
// depend on it, so it is not recorded.
func stripParallel(cmdline []string) []string {
	out := make([]string, 0, len(cmdline))
	for i := 0; i < len(cmdline); i++ {
		a := cmdline[i]
		switch {
		case a == "--parallel" || a == "-parallel":
			i++ // skip its value
		case strings.HasPrefix(a, "--parallel=") || strings.HasPrefix(a, "-parallel="):
		default:
			out = append(out, a)
		}
	}
	return out
}

// robustJob is a parsed multi-run command ready to run.
type robustJob func(ctx context.Context, a runOptions, reader runReader, progress backtest.Progress) (summary any, markdown string, outs []backtest.RunOutcome, err error)

// runReader is the market data the runs read.
type runReader = histdata.Reader

// runRobust executes split, walkforward, regimes or sensitivity and returns
// the exit code.
func runRobust(ctx context.Context, kind string, args, cmdline []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("backtest "+kind, flag.ContinueOnError)
	fs.SetOutput(stderr)
	rf := addRunFlags(fs)
	f := addRobustFlags(fs, kind)
	usageErr := func(err error) int {
		if err != errUsage {
			fmt.Fprintln(stderr, "backtest:", err)
		}
		return 2
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		return usageErr(fmt.Errorf("unexpected argument %q", fs.Arg(0)))
	}
	fs.Visit(func(fl *flag.Flag) { f.set[fl.Name] = true })
	a, err := rf.options(kind)
	if err != nil {
		return usageErr(err)
	}
	if kind != backtest.KindRegimes && f.parallel < 1 {
		return usageErr(errors.New("--parallel must be at least 1"))
	}
	job, err := f.job(kind, a.cfg)
	if err != nil {
		return usageErr(err)
	}

	reader, err := prepare(&a)
	if err != nil {
		fmt.Fprintln(stderr, "backtest:", err)
		return 1
	}
	progress := func(done, total int, label string) { fmt.Fprintf(stderr, "[%d/%d] %s\n", done, total, label) }
	summary, markdown, outs, err := job(ctx, a, reader, progress)
	if err != nil {
		fmt.Fprintln(stderr, "backtest:", err)
		return 1
	}
	recorded := stripParallel(cmdline)
	if err := backtest.WriteMultiRun(a.output, summary, markdown, backtest.NewBaseInfo(kind, a.cfg, recorded), outs); err != nil {
		fmt.Fprintln(stderr, "backtest: write reports:", err)
		return 1
	}
	fmt.Fprint(stdout, markdown)
	fmt.Fprintf(stdout, "\nreports         %s\n", a.output)
	if err := reconcileError(backtest.AnyReconcileWarning(outs), a.allowReconcileWarning); err != nil {
		fmt.Fprintln(stderr, "backtest:", err)
		return 1
	}
	return 0
}

// job validates the command's own flags against cfg and returns the work to
// run; any error is a usage error.
func (f *robustFlags) job(kind string, cfg backtest.Config) (robustJob, error) {
	switch kind {
	case backtest.KindSplit:
		var spec backtest.SplitSpec
		if f.splitAt != "" {
			at, err := parseDay(f.splitAt)
			if err != nil {
				return nil, fmt.Errorf("--split: %v", err)
			}
			spec.At = at
		}
		spec.OOSFraction = f.oosFraction
		if spec.At.IsZero() && spec.OOSFraction == 0 {
			return nil, errors.New("give --split or --oos-fraction")
		}
		if _, _, err := backtest.SplitPoint(cfg.Start, cfg.End, spec); err != nil {
			return nil, err
		}
		return func(ctx context.Context, a runOptions, r runReader, p backtest.Progress) (any, string, []backtest.RunOutcome, error) {
			rep, outs, err := backtest.RunSplit(ctx, r, a.cfg, spec, f.parallel, p)
			if err != nil {
				return nil, "", nil, err
			}
			return rep, backtest.RenderSplit(rep), outs, nil
		}, nil

	case backtest.KindWalkForward:
		if f.train == "" || f.test == "" {
			return nil, errors.New("walkforward needs --train and --test")
		}
		var spec backtest.WalkSpec
		var err error
		if spec.Train, err = parseSpan(f.train); err != nil {
			return nil, fmt.Errorf("--train: %v", err)
		}
		if spec.Test, err = parseSpan(f.test); err != nil {
			return nil, fmt.Errorf("--test: %v", err)
		}
		spec.Step = spec.Test
		if f.step != "" {
			if spec.Step, err = parseSpan(f.step); err != nil {
				return nil, fmt.Errorf("--step: %v", err)
			}
		}
		if spec.Step < spec.Test {
			return nil, errors.New("--step shorter than --test would reuse test data")
		}
		if f.tune != "" {
			d, err := keyValues(f.tune, cfg)
			if err != nil {
				return nil, fmt.Errorf("--tune: %v", err)
			}
			spec.TuneKey, spec.TuneValues = d.Name, d.Values
		}
		return func(ctx context.Context, a runOptions, r runReader, p backtest.Progress) (any, string, []backtest.RunOutcome, error) {
			rep, outs, err := backtest.RunWalkForward(ctx, r, a.cfg, spec, f.parallel, p)
			if err != nil {
				return nil, "", nil, err
			}
			return rep, backtest.RenderWalkForward(rep), outs, nil
		}, nil

	case backtest.KindRegimes:
		spec, err := backtest.DefaultRegimeSpec(f.bucket)
		if err != nil {
			return nil, fmt.Errorf("--bucket: %v", err)
		}
		if f.set["bull"] {
			spec.BullAbovePct = f.bull
		}
		if f.set["bear"] {
			spec.BearBelowPct = f.bear
		}
		if spec.BearBelowPct >= spec.BullAbovePct {
			return nil, errors.New("--bear must be below --bull")
		}
		return func(ctx context.Context, a runOptions, r runReader, p backtest.Progress) (any, string, []backtest.RunOutcome, error) {
			rep, outs, err := backtest.RunRegimes(ctx, r, a.cfg, spec, p)
			if err != nil {
				return nil, "", nil, err
			}
			return rep, backtest.RenderRegimes(rep), outs, nil
		}, nil

	case backtest.KindSensitivity:
		spec, err := f.sensitivitySpec(cfg)
		if err != nil {
			return nil, err
		}
		return func(ctx context.Context, a runOptions, r runReader, p backtest.Progress) (any, string, []backtest.RunOutcome, error) {
			rep, outs, err := backtest.RunSensitivity(ctx, r, a.cfg, spec, f.parallel, p)
			if err != nil {
				return nil, "", nil, err
			}
			return rep, backtest.RenderSensitivity(rep), outs, nil
		}, nil
	}
	return nil, fmt.Errorf("unknown command %q", kind)
}

// runCompare executes `backtest compare` and returns the exit code.
func runCompare(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("backtest compare", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print the comparison as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 2 {
		fmt.Fprintln(stderr, "backtest: compare needs at least two result directories")
		return 2
	}
	rep, err := backtest.Compare(fs.Args())
	if err != nil {
		fmt.Fprintln(stderr, "backtest:", err)
		return 1
	}
	if *asJSON {
		data, err := json.MarshalIndent(rep, "", " ")
		if err != nil {
			fmt.Fprintln(stderr, "backtest:", err)
			return 1
		}
		fmt.Fprintln(stdout, string(data))
		return 0
	}
	fmt.Fprint(stdout, backtest.RenderCompare(rep))
	return 0
}
