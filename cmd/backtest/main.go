// Command backtest replays the bot's real strategy, risk engine, learner and
// paper engine over historical Kraken trades stored in a local cache, with a
// conservative simulated exchange, and writes the result and its reports.
//
//	backtest run         --markets BTC/EUR[,...] | --universe top:N  --start YYYY-MM-DD --end YYYY-MM-DD [flags]
//	backtest split       (run flags) --split YYYY-MM-DD | --oos-fraction F
//	backtest walkforward (run flags) --train 90d --test 30d [--step 30d] [--tune KEY=v1,v2,...]
//	backtest regimes     (run flags) [--bucket day|week|month|quarter] [--bull PCT] [--bear PCT]
//	backtest sensitivity (run flags) [--dims ...] [--vary KEY=v1,v2,...] [--grid A,B]
//	backtest compare     DIR DIR ... [--json]
//	backtest data <import|fetch|verify|list|universe> [flags]
//
// It never opens the bot's database, never reads secrets and cannot place an
// order: it does not link the live app, the Kraken client or the secret
// store (internal/backtest/deps_test.go enforces this). `backtest run` makes
// no network call.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/backtest"
	"github.com/James-Chapman/LLMTradingBot/internal/histdata"
	"github.com/James-Chapman/LLMTradingBot/internal/observability"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy"
)

// usage is printed for a missing or unknown subcommand.
const usage = `usage:
  backtest run  --markets BTC/EUR[,ETH/EUR...] | --universe top:N
                --start YYYY-MM-DD --end YYYY-MM-DD (UTC, end inclusive) [flags]
  backtest split       (run flags) --split YYYY-MM-DD | --oos-fraction 0.3 [--parallel N]
  backtest walkforward (run flags) --train 90d --test 30d [--step 30d] [--tune KEY=v1,v2,...] [--parallel N]
  backtest regimes     (run flags) [--bucket month] [--bull PCT] [--bear PCT]
  backtest sensitivity (run flags) [--dims fees,latency,half-spread,impact,capital,fill-model]
                       [--fees-list ...] [--vary KEY=v1,v2,...] [--grid A,B] [--parallel N]
  backtest compare     DIR DIR ... [--json]
  backtest data <import|fetch|verify|list|universe> [flags]

Run "backtest <command> -h" for the flags.`

// errUsage marks a command-line mistake (exit code 2).
var errUsage = errors.New("usage error")

// runOptions holds the parsed flags of `backtest run`.
type runOptions struct {
	markets, universe, universeEvery string
	start, end                       time.Time
	cfg                              backtest.Config
	settingsPath, pairRulesPath      string
	dataDir, output                  string
	allowReconcileWarning            bool
}

// runFlags holds the flag values `backtest run` shares with the multi-run commands.
type runFlags struct {
	a                runOptions
	ex               backtest.ExchangeConfig
	start, end, fees string
	latency          time.Duration
	noLearner        *bool
}

// parseRunArgs parses and validates the flags of `backtest run`.
func parseRunArgs(args []string, stderr io.Writer) (runOptions, error) {
	fs := flag.NewFlagSet("backtest run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	rf := addRunFlags(fs)
	if err := fs.Parse(args); err != nil {
		return runOptions{}, errUsage
	}
	if fs.NArg() > 0 {
		return runOptions{}, fmt.Errorf("%w: unexpected argument %q", errUsage, fs.Arg(0))
	}
	return rf.options("")
}

// addRunFlags defines the run flags on fs.
func addRunFlags(fs *flag.FlagSet) *runFlags {
	rf := &runFlags{ex: backtest.DefaultExchange()}
	a, ex := &rf.a, &rf.ex
	start, end, fees, latency := &rf.start, &rf.end, &rf.fees, &rf.latency
	fs.StringVar(&a.markets, "markets", "", "comma-separated markets, in the order the bot would list them")
	fs.StringVar(&a.universe, "universe", "", "point-in-time universe instead of --markets: top:N by 24 h traded value")
	fs.StringVar(&a.universeEvery, "universe-every", "month", "universe re-selection: month, week or a duration such as 72h")
	fs.StringVar(start, "start", "", "first day, YYYY-MM-DD (UTC)")
	fs.StringVar(end, "end", "", "last day, YYYY-MM-DD (UTC, inclusive)")
	fs.Float64Var(&a.cfg.StartingCapital, "capital", 0, "starting cash in the quote currency (0 = STARTING_CAPITAL from the settings)")
	fs.StringVar(&a.cfg.StrategyID, "strategy", strategy.Basic1hID, "strategy id (LLM strategies cannot be backtested)")
	fs.StringVar(fees, "fees", backtest.FeeKrakenBase, "taker fee: kraken-base (0.80 %), paper-default (0.26 %) or basis points")
	fs.StringVar(&ex.FillModel, "fill-model", ex.FillModel, "spread (taker-side averages) or fixed (close ± --half-spread-bps)")
	fs.DurationVar(latency, "latency", 30*time.Second, "decision-to-fill latency, rounded up to whole 30 s ticks (at least one)")
	fs.Float64Var(&ex.FixedHalfSpreadBps, "half-spread-bps", ex.FixedHalfSpreadBps, "fixed model half-spread, bp")
	fs.Float64Var(&ex.MinHalfSpreadBps, "min-half-spread-bps", ex.MinHalfSpreadBps, "spread model floor on the measured half-spread, bp")
	fs.Float64Var(&ex.ImpactThreshold, "impact-threshold", ex.ImpactThreshold, "participation in the fill bar's traded value above which impact applies")
	fs.Float64Var(&ex.ImpactBpsPerUnit, "impact-bps", ex.ImpactBpsPerUnit, "impact, bp per unit of participation above the threshold")
	fs.Float64Var(&ex.LiquidityCap, "liquidity-cap", ex.LiquidityCap, "participation above which an order is rejected")
	fs.StringVar(&a.pairRulesPath, "pair-rules", "", "saved Kraken AssetPairs JSON (ordermin, costmin, decimals); default: built-in fallback")
	fs.StringVar(&a.settingsPath, "settings", "", "JSON settings file (dashboard keys); default: built-in defaults")
	fs.StringVar(&a.dataDir, "data", histdata.DefaultCacheDir, "historical data cache directory")
	fs.Int64Var(&a.cfg.Seed, "seed", 1, "recorded in the result (the replay itself uses no randomness)")
	rf.noLearner = fs.Bool("no-learner", false, "never adjust confidence from closed trades (sensitivity runs)")
	fs.DurationVar(&a.cfg.EquityEvery, "equity-every", time.Hour, "equity curve sampling (each point keeps the lowest equity since the last)")
	fs.StringVar(&a.output, "output", "", "result directory (default results/<command>_<strategy>_<start>_<end>)")
	fs.BoolVar(&a.allowReconcileWarning, "allow-reconcile-warning", false, "exit 0 even when the exact reconciliation flags an accounting difference")
	fs.BoolVar(&a.cfg.AllowDataGaps, "allow-data-gaps", false, "run even when the cache misses a month inside a market's coverage or has an error-level finding (recorded as a warning)")
	fs.BoolVar(&a.cfg.FillEmptyCandles, "fill-empty-candles", false, "insert flat zero-volume candles for 5/15 min intervals without trades (sensitivity of an unverified assumption)")
	fs.DurationVar(&a.cfg.StaleAfter, "stale-after", backtest.DefaultStaleAfter, "report a held position as stale when its market has not traded for this long")
	return rf
}

// options validates the parsed run flags; kind prefixes the default output directory.
func (rf *runFlags) options(kind string) (runOptions, error) {
	a, ex := rf.a, rf.ex
	a.cfg.DisableLearner = *rf.noLearner

	var err error
	if a.start, err = parseDay(rf.start); err != nil {
		return a, fmt.Errorf("%w: --start: %v", errUsage, err)
	}
	if a.end, err = parseDay(rf.end); err != nil {
		return a, fmt.Errorf("%w: --end: %v", errUsage, err)
	}
	a.end = a.end.Add(24 * time.Hour) // inclusive last day
	if !a.end.After(a.start) {
		return a, fmt.Errorf("%w: --end is before --start", errUsage)
	}
	if (a.markets == "") == (a.universe == "") {
		return a, fmt.Errorf("%w: give exactly one of --markets and --universe", errUsage)
	}
	if a.markets != "" {
		for _, m := range strings.Split(a.markets, ",") {
			if m = strings.ToUpper(strings.TrimSpace(m)); m != "" {
				a.cfg.Markets = append(a.cfg.Markets, m)
			}
		}
	} else {
		n, ok := strings.CutPrefix(a.universe, "top:")
		top, err := strconv.Atoi(n)
		if !ok || err != nil || top < 1 {
			return a, fmt.Errorf("%w: --universe must be top:N, got %q", errUsage, a.universe)
		}
		a.cfg.Universe = &backtest.UniverseRule{TopN: top, Every: a.universeEvery}
	}
	if err := ex.SetFees(rf.fees); err != nil {
		return a, fmt.Errorf("%w: --fees: %v", errUsage, err)
	}
	if err := ex.SetLatency(rf.latency); err != nil {
		return a, fmt.Errorf("%w: --latency: %v", errUsage, err)
	}
	if err := ex.Validate(); err != nil {
		return a, fmt.Errorf("%w: %v", errUsage, err)
	}
	a.cfg.Exchange = ex
	a.cfg.Start, a.cfg.End = a.start, a.end
	if a.output == "" {
		name := fmt.Sprintf("%s_%s_%s", a.cfg.StrategyID, a.start.Format(time.DateOnly), a.end.Add(-24*time.Hour).Format(time.DateOnly))
		if kind != "" {
			name = kind + "_" + name
		}
		a.output = filepath.Join("results", name)
	}
	return a, nil
}

// parseDay parses a UTC calendar day.
func parseDay(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, errors.New("required")
	}
	return time.ParseInLocation(time.DateOnly, s, time.UTC)
}

// runBacktest executes `backtest run`: load settings and pair rules from
// files, replay the cached data, write the result and its reports into
// <output> and print a summary. cmdline is recorded in run.json. It returns
// the exit code.
func runBacktest(ctx context.Context, args, cmdline []string, stdout, stderr io.Writer) int {
	a, err := parseRunArgs(args, stderr)
	if err != nil {
		if err != errUsage {
			fmt.Fprintln(stderr, "backtest:", err)
		}
		return 2
	}
	if err := execute(ctx, a, cmdline, stdout); err != nil {
		fmt.Fprintln(stderr, "backtest:", err)
		return 1
	}
	return 0
}

// prepare loads the settings and pair rules into a.cfg, checks the data
// cache and returns its reader.
func prepare(a *runOptions) (histdata.Reader, error) {
	observability.SetLevel("ERROR") // the engines log every tick at INFO
	settings, keys, err := backtest.LoadSettingsKeys(a.settingsPath)
	if err != nil {
		return nil, err
	}
	a.cfg.Settings, a.cfg.ExplicitSettings = settings, keys
	a.cfg.SettingsSource = backtest.DefaultSettingsSource
	if a.settingsPath != "" {
		a.cfg.SettingsSource = a.settingsPath
	}
	if a.cfg.Exchange.PairRules, err = backtest.LoadPairRules(a.pairRulesPath); err != nil {
		return nil, err
	}
	if info, err := os.Stat(a.dataDir); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("data cache %s not found (import data with `backtest data import`)", a.dataDir)
	}
	dataDir := a.dataDir
	a.cfg.Provenance = func(market string, from, to time.Time) ([]histdata.Provenance, error) {
		return histdata.ProvenanceFor(dataDir, market, from, to)
	}
	// The multi-run commands check the same ranges many times (in parallel):
	// each distinct range is checked once.
	var mu sync.Mutex
	checked := map[string]histdata.RangeReport{}
	a.cfg.CheckData = func(markets []string, from, to time.Time) (histdata.RangeReport, error) {
		key := strings.Join(markets, ",") + "|" + from.String() + "|" + to.String()
		mu.Lock()
		defer mu.Unlock()
		if rep, ok := checked[key]; ok {
			return rep, nil
		}
		cache, err := histdata.OpenCache(dataDir) // the directory exists: checked above
		if err != nil {
			return histdata.RangeReport{}, err
		}
		rep, err := cache.CheckRange(markets, from, to)
		if err == nil {
			checked[key] = rep
		}
		return rep, err
	}
	return histdata.NewCacheReader(dataDir), nil
}

// execute loads the run inputs, replays, writes the reports and prints the
// summary. A reconciliation warning is an error unless explicitly allowed.
func execute(ctx context.Context, a runOptions, cmdline []string, stdout io.Writer) error {
	reader, err := prepare(&a)
	if err != nil {
		return err
	}
	res, err := backtest.Run(ctx, reader, a.cfg)
	if err != nil {
		return err
	}
	rep, err := backtest.BuildReport(reader, res)
	if err != nil {
		return err
	}
	if err := backtest.WriteReports(a.output, rep, cmdline); err != nil {
		return fmt.Errorf("write reports: %w", err)
	}
	printReport(stdout, rep, a.output)
	return reconcileError(rep.Reconciliation.Warning, a.allowReconcileWarning)
}

// reconcileError returns the error a reconciliation warning causes: none
// when there is no warning or it was explicitly allowed.
func reconcileError(warning, allow bool) error {
	if warning && !allow {
		return errors.New("the exact reconciliation found an accounting difference (see reconciliation in summary.json); rerun with --allow-reconcile-warning to accept it")
	}
	return nil
}

// printReport prints the honesty block, the verdict, the headline numbers
// and the reconciliation of a run.
func printReport(w io.Writer, rep *backtest.Report, dir string) {
	fmt.Fprintln(w, "READ THIS FIRST")
	for _, l := range rep.Honesty.Lines {
		fmt.Fprintln(w, "  - "+l)
	}
	fmt.Fprintln(w, "VERDICT")
	for _, l := range rep.Verdict {
		fmt.Fprintln(w, "  - "+l.Text)
	}
	fmt.Fprintln(w)
	printSummary(w, rep.Result(), filepath.Join(dir, "result.json"))
	rec := rep.Reconciliation
	if rec.Warning {
		fmt.Fprintf(w, "reconciliation  WARNING: %s\n", strings.Join(rec.Problems, "; "))
	} else {
		fmt.Fprintf(w, "reconciliation  exact, within %s (%d fills)\n", strconv.FormatFloat(rec.Tolerance, 'g', 3, 64), rec.Fills)
	}
	fmt.Fprintf(w, "reports         %s\n", filepath.Join(dir, "summary.md"))
}

// printSummary prints the headline numbers of a run.
func printSummary(w io.Writer, r *backtest.Result, path string) {
	filled, rejected := 0, 0
	for _, o := range r.Orders {
		switch o.Status {
		case backtest.StatusFilled:
			filled++
		case backtest.StatusRejected:
			rejected++
		}
	}
	net := r.EndingEquity - r.StartingCapital
	fmt.Fprintf(w, "strategy        %s\n", r.StrategyID)
	fmt.Fprintf(w, "period          %s .. %s (%d ticks, %d evaluated)\n", r.Start.Format(time.RFC3339), r.End.Format(time.RFC3339), r.Ticks, r.EvaluatedTicks)
	fmt.Fprintf(w, "markets         %s\n", strings.Join(r.Markets, ", "))
	fmt.Fprintf(w, "starting equity %.2f\n", r.StartingCapital)
	fmt.Fprintf(w, "ending equity   %.2f (net %+.2f, %+.2f%%)\n", r.EndingEquity, net, 100*net/r.StartingCapital)
	if len(r.StalePositions) > 0 {
		worst := r.EndingEquityWorstCase - r.StartingCapital
		fmt.Fprintf(w, "worst case      %.2f with stale positions at zero (net %+.2f, %d stale)\n", r.EndingEquityWorstCase, worst, len(r.StalePositions))
	}
	fmt.Fprintf(w, "fees paid       %.2f at %s bp (%s)\n", r.FeesPaid, strconv.FormatFloat(r.Exchange.FeeBps, 'g', -1, 64), r.Exchange.FeePreset)
	fmt.Fprintf(w, "trades          %d closed, %d orders filled, %d rejected\n", len(r.Trades), filled, rejected)
	fmt.Fprintf(w, "result          %s\n", path)
}

// run dispatches a subcommand and returns the exit code.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	cmdline := append([]string{"backtest"}, args...)
	switch args[0] {
	case "run":
		return runBacktest(ctx, args[1:], cmdline, stdout, stderr)
	case backtest.KindSplit, backtest.KindWalkForward, backtest.KindRegimes, backtest.KindSensitivity:
		return runRobust(ctx, args[0], args[1:], cmdline, stdout, stderr)
	case backtest.KindCompare:
		return runCompare(args[1:], stdout, stderr)
	case "data":
		return runData(ctx, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown subcommand %q\n%s\n", args[0], usage)
		return 2
	}
}

// main runs the command and exits with its code.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
