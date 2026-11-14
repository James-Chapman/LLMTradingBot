package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/histdata"
)

// dataRESTSleep, when set, replaces the REST fetcher's sleep (tests only).
var dataRESTSleep func(ctx context.Context, d time.Duration) error

// errDataUsage marks a command-line mistake (exit code 2).
var errDataUsage = errors.New("usage")

// dataUsage is printed for `backtest data` without a valid subcommand.
const dataUsage = `usage: backtest data <subcommand> [flags]

subcommands:
  import   --archive <dir|zip|csv>... [--markets BTC/EUR,...] [--from DATE] [--to DATE] [--map FILE=BASE/QUOTE]... [--dry-run]
  fetch    --markets BTC/EUR,... --from DATE --to DATE [--base-url URL]
  verify   [--markets ...] [--gap 1h] [--jump 1.25] [--ohlcvt FILE --ohlcvt-market M --ohlcvt-minutes N] [--json]
  list     [--json]
  universe --date DATE [--quote EUR] [--top 50] [--rank 24h|7d] [--json]

every subcommand takes --cache DIR (default data/history). DATE is UTC,
YYYY-MM-DD or RFC 3339. Nothing here can place an order or read the bot's
database or secrets.
`

// runData runs `backtest data <subcommand>` and returns the exit code:
// 0 success, 1 failure (or verify errors), 2 usage error.
func runData(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, dataUsage)
		return 2
	}
	cmds := map[string]func(context.Context, []string, io.Writer, io.Writer) error{
		"import": dataImport, "fetch": dataFetch, "verify": dataVerify, "list": dataList, "universe": dataUniverse,
	}
	cmd, ok := cmds[args[0]]
	if !ok {
		if args[0] != "help" && args[0] != "-h" && args[0] != "--help" {
			fmt.Fprintf(stderr, "unknown data subcommand %q\n", args[0])
		}
		fmt.Fprint(stderr, dataUsage)
		return 2
	}
	err := cmd(ctx, args[1:], stdout, stderr)
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errDataUsage), errors.Is(err, flag.ErrHelp):
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(stderr, "backtest data:", err)
		}
		return 2
	default:
		fmt.Fprintln(stderr, "backtest data:", err)
		return 1
	}
}

// newDataFlags returns a flag set with the shared --cache flag.
func newDataFlags(name string, stderr io.Writer) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet("backtest data "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	cache := fs.String("cache", histdata.DefaultCacheDir, "cache directory")
	return fs, cache
}

// parseDataFlags parses args, turning flag errors into usage errors.
func parseDataFlags(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return fmt.Errorf("%w: %v", errDataUsage, err)
	}
	return nil
}

// dataListFlag collects a repeatable string flag.
type dataListFlag []string

// String implements flag.Value.
func (l *dataListFlag) String() string { return strings.Join(*l, ",") }

// Set implements flag.Value.
func (l *dataListFlag) Set(v string) error {
	*l = append(*l, v)
	return nil
}

// parseDataDate accepts YYYY-MM-DD (midnight UTC) or RFC 3339, returned in UTC.
func parseDataDate(s string) (time.Time, error) {
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("%w: date %q must be YYYY-MM-DD or RFC 3339 (UTC)", errDataUsage, s)
}

// optionalDataDate parses s when non-empty.
func optionalDataDate(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return parseDataDate(s)
}

// splitDataMarkets parses a comma-separated market list.
func splitDataMarkets(s string) []string {
	var out []string
	for _, m := range strings.Split(s, ",") {
		if m = strings.ToUpper(strings.TrimSpace(m)); m != "" {
			out = append(out, m)
		}
	}
	return out
}

// printDataJSON writes v as indented JSON.
func printDataJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// dataProgress returns a progress callback printing to w.
func dataProgress(w io.Writer) func(string) {
	return func(msg string) { fmt.Fprintln(w, msg) }
}

// dataImport imports trade archives into the cache.
func dataImport(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs, cache := newDataFlags("import", stderr)
	var archives, maps dataListFlag
	fs.Var(&archives, "archive", "archive directory, zip or CSV file (repeatable)")
	fs.Var(&maps, "map", "file-name override FILE=BASE/QUOTE (repeatable)")
	markets := fs.String("markets", "", "only these markets (comma-separated)")
	fromS := fs.String("from", "", "keep trades from this date")
	toS := fs.String("to", "", "keep trades before this date")
	gap := fs.Duration("gap", 0, "report no-trade runs at least this long (default 1h)")
	jump := fs.Float64("jump", 0, "flag consecutive prices differing by more than this factor (default 1.25)")
	dry := fs.Bool("dry-run", false, "validate and summarise without writing")
	if err := parseDataFlags(fs, args); err != nil {
		return err
	}
	archives = append(archives, fs.Args()...)
	if len(archives) == 0 {
		return fmt.Errorf("%w: import needs at least one --archive", errDataUsage)
	}
	from, err := optionalDataDate(*fromS)
	if err != nil {
		return err
	}
	to, err := optionalDataDate(*toS)
	if err != nil {
		return err
	}
	opts := histdata.ImportOptions{Markets: splitDataMarkets(*markets), From: from, To: to, GapThreshold: *gap, JumpFactor: *jump,
		DryRun: *dry, Progress: dataProgress(stderr), Mapper: histdata.MarketMapper{Overrides: map[string]string{}}}
	for _, m := range maps {
		file, market, ok := strings.Cut(m, "=")
		if !ok {
			return fmt.Errorf("%w: --map %q must be FILE=BASE/QUOTE", errDataUsage, m)
		}
		opts.Mapper.Overrides[strings.TrimSuffix(strings.ToUpper(strings.TrimSpace(file)), ".CSV")] = strings.ToUpper(strings.TrimSpace(market))
	}
	c, err := histdata.OpenCache(*cache)
	if err != nil {
		return err
	}
	failed := false
	for _, a := range archives {
		fmt.Fprintf(stderr, "importing %s ...\n", a)
		sum, err := c.ImportArchive(ctx, a, opts)
		printDataImport(stdout, sum)
		if err != nil {
			fmt.Fprintf(stderr, "backtest data: %s: %v\n", a, err)
			failed = true
		}
	}
	if failed {
		return errors.New("one or more archives failed to import")
	}
	return nil
}

// printDataImport prints one archive's import result.
func printDataImport(w io.Writer, sum histdata.ImportSummary) {
	for _, f := range sum.Files {
		fmt.Fprintf(w, "%s -> %s: %d trades, %d bars, months %s, %s .. %s\n", f.Source, f.Market, f.Trades, f.Bars,
			strings.Join(f.Months, ","), f.First.Format(time.RFC3339), f.Last.Format(time.RFC3339))
		fmt.Fprintf(w, "  buy volume %g, sell volume %g", f.BuyVolume, f.SellVolume)
		if f.UnknownSideTrades > 0 {
			fmt.Fprintf(w, ", %d trades (%g volume) without a side: counted in volume, in neither side total", f.UnknownSideTrades, f.UnknownSideVolume)
		}
		fmt.Fprintf(w, "; rejected rows %d, outside range %d\n", f.Rejected, f.OutsideRange)
		printDataFindings(w, "  ", f.FindingCounts, f.Findings)
	}
	for _, u := range sum.Unmapped {
		fmt.Fprintf(w, "UNMAPPED %s (not imported; add --map FILE=BASE/QUOTE)\n", u)
	}
	for _, s := range sum.Skipped {
		fmt.Fprintf(w, "skipped %s\n", s)
	}
}

// printDataFindings prints finding counts by kind and the listed findings.
func printDataFindings(w io.Writer, indent string, counts map[string]int, list []histdata.Finding) {
	kinds := make([]string, 0, len(counts))
	for k := range counts {
		kinds = append(kinds, k)
	}
	slices.Sort(kinds)
	for _, k := range kinds {
		fmt.Fprintf(w, "%sfinding %s: %d\n", indent, k, counts[k])
	}
	for _, f := range list {
		when := ""
		if !f.Time.IsZero() {
			when = " " + f.Time.Format(time.RFC3339Nano)
			if !f.End.IsZero() {
				when += ".." + f.End.Format(time.RFC3339Nano)
			}
		}
		market := f.Market
		if market == "" {
			market = "*"
		}
		fmt.Fprintf(w, "%s  [%s] %s %s%s: %s\n", indent, f.Severity, f.Kind, market, when, f.Detail)
	}
}

// dataFetch pages Kraken's public REST trades into the cache.
func dataFetch(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs, cache := newDataFlags("fetch", stderr)
	markets := fs.String("markets", "", "markets to fetch (comma-separated, BASE/QUOTE)")
	fromS := fs.String("from", "", "start date (required)")
	toS := fs.String("to", "", "end date, exclusive (required)")
	baseURL := fs.String("base-url", histdata.DefaultRESTURL, "Kraken REST base URL")
	if err := parseDataFlags(fs, args); err != nil {
		return err
	}
	list := splitDataMarkets(*markets)
	if len(list) == 0 || *fromS == "" || *toS == "" {
		return fmt.Errorf("%w: fetch needs --markets, --from and --to", errDataUsage)
	}
	from, err := parseDataDate(*fromS)
	if err != nil {
		return err
	}
	to, err := parseDataDate(*toS)
	if err != nil {
		return err
	}
	c, err := histdata.OpenCache(*cache)
	if err != nil {
		return err
	}
	f := &histdata.RESTFetcher{BaseURL: *baseURL, Timeout: 60 * time.Second, Sleep: dataRESTSleep, Progress: dataProgress(stderr)}
	for _, m := range list {
		fmt.Fprintf(stderr, "fetching %s %s .. %s (about 1 call per 1.1 s) ...\n", m, from.Format(time.RFC3339), to.Format(time.RFC3339))
		res, sum, err := c.FetchREST(ctx, f, m, from, to, histdata.ImportOptions{Progress: dataProgress(stderr)})
		if err != nil {
			return fmt.Errorf("%s: %w (run the same command again to resume)", m, err)
		}
		state := "reached the end time"
		if res.CaughtUp {
			state = "caught up with the present before the end time"
		}
		fmt.Fprintf(stdout, "%s: %d pages, %d new trades, %d in spool, %s, resumed=%v\n", m, res.Pages, res.NewTrades, res.TotalTrades, state, res.Resumed)
		printDataImport(stdout, histdata.ImportSummary{Files: []histdata.FileSummary{sum}})
	}
	return nil
}

// dataVerify re-checks the cache and prints the findings.
func dataVerify(_ context.Context, args []string, stdout, stderr io.Writer) error {
	fs, cache := newDataFlags("verify", stderr)
	markets := fs.String("markets", "", "only these markets (comma-separated)")
	gap := fs.Duration("gap", 0, "report no-trade runs at least this long (default 1h)")
	jump := fs.Float64("jump", 0, "flag bar-to-bar price jumps beyond this factor (default 1.25)")
	ohlcvt := fs.String("ohlcvt", "", "Kraken OHLCVT CSV file to cross-check against")
	ohlcvtMarket := fs.String("ohlcvt-market", "", "market of the OHLCVT file")
	ohlcvtMinutes := fs.Int("ohlcvt-minutes", 0, "candle interval of the OHLCVT file in minutes")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	if err := parseDataFlags(fs, args); err != nil {
		return err
	}
	opts := histdata.VerifyOptions{Markets: splitDataMarkets(*markets), GapThreshold: *gap, JumpFactor: *jump}
	if *ohlcvt != "" {
		if *ohlcvtMarket == "" || *ohlcvtMinutes <= 0 {
			return fmt.Errorf("%w: --ohlcvt needs --ohlcvt-market and --ohlcvt-minutes", errDataUsage)
		}
		opts.OHLCVT = []histdata.OHLCVTCheck{{Market: strings.ToUpper(*ohlcvtMarket), Minutes: *ohlcvtMinutes, Path: *ohlcvt}}
	}
	if _, err := os.Stat(*cache); err != nil {
		return err
	}
	c, err := histdata.OpenCache(*cache)
	if err != nil {
		return err
	}
	rep, err := c.Verify(opts)
	if err != nil {
		return err
	}
	if *asJSON {
		if err := printDataJSON(stdout, rep); err != nil {
			return err
		}
	} else {
		tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "MARKET\tFIRST\tLAST\tBARS\tCOVERAGE\tGAPS\tLONGEST GAP")
		for _, m := range rep.Markets {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%.1f%%\t%d\t%v\n", m.Market, m.First.Format(time.RFC3339), m.Last.Format(time.RFC3339),
				m.Bars, 100*m.Coverage, m.Gaps, m.LongestGap)
		}
		_ = tw.Flush()
		for _, cc := range rep.CrossChecks {
			fmt.Fprintf(stdout, "OHLCVT %s %dm (%s): compared %d, matched %d, mismatched %d, missing %d, timestamp = candle %s time\n",
				cc.Market, cc.Minutes, cc.Path, cc.Compared, cc.Matched, cc.Mismatched, cc.Missing, cc.Convention)
		}
		printDataFindings(stdout, "", rep.Counts, rep.Findings)
	}
	if rep.HasErrors() {
		return errors.New("verification found errors")
	}
	return nil
}

// dataList prints the cached markets with coverage and sizes.
func dataList(_ context.Context, args []string, stdout, stderr io.Writer) error {
	fs, cache := newDataFlags("list", stderr)
	asJSON := fs.Bool("json", false, "print as JSON")
	if err := parseDataFlags(fs, args); err != nil {
		return err
	}
	if _, err := os.Stat(*cache); err != nil {
		return err
	}
	c, err := histdata.OpenCache(*cache)
	if err != nil {
		return err
	}
	list, err := c.List()
	if err != nil {
		return err
	}
	if *asJSON {
		return printDataJSON(stdout, list)
	}
	tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "MARKET\tFIRST\tLAST\tMONTHS\tBARS\tBYTES")
	for _, m := range list {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\n", m.Market, m.First.Format(time.RFC3339), m.Last.Format(time.RFC3339), m.Months, m.Bars, m.Bytes)
	}
	return tw.Flush()
}

// dataUniverse prints the point-in-time top-N markets.
func dataUniverse(_ context.Context, args []string, stdout, stderr io.Writer) error {
	fs, cache := newDataFlags("universe", stderr)
	dateS := fs.String("date", "", "the date (required); only data before it is used")
	quote := fs.String("quote", "EUR", "quote currency")
	top := fs.Int("top", 50, "number of markets")
	rank := fs.String("rank", "24h", "ranking window: 24h or 7d")
	asJSON := fs.Bool("json", false, "print as JSON")
	if err := parseDataFlags(fs, args); err != nil {
		return err
	}
	if *dateS == "" {
		return fmt.Errorf("%w: universe needs --date", errDataUsage)
	}
	at, err := parseDataDate(*dateS)
	if err != nil {
		return err
	}
	if *rank != "24h" && *rank != "7d" {
		return fmt.Errorf("%w: --rank must be 24h or 7d", errDataUsage)
	}
	u, err := histdata.Universe(histdata.NewCacheReader(*cache), at, histdata.UniverseOptions{Quote: *quote, Top: *top, RankBy7d: *rank == "7d"})
	if err != nil {
		return err
	}
	if *asJSON {
		return printDataJSON(stdout, u)
	}
	fmt.Fprintf(stdout, "top %d %s markets by traded value over the %s before %s (cached data only)\n", *top, u.Quote, u.RankedBy, at.Format(time.RFC3339))
	tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "#\tMARKET\tVALUE 24H\tVALUE 7D\tRANGE")
	for i, e := range u.Selected {
		fmt.Fprintf(tw, "%d\t%s\t%.2f\t%.2f\t%.2f%%\n", i+1, e.Market, e.Value24h, e.Value7d, 100*e.Range24h)
	}
	_ = tw.Flush()
	for _, e := range u.Pegged {
		fmt.Fprintf(stdout, "excluded as pegged (range %.3f%% < 0.5%%): %s\n", 100*e.Range24h, e.Market)
	}
	if len(u.NoData) > 0 {
		fmt.Fprintf(stdout, "no trades in the window: %s\n", strings.Join(u.NoData, ", "))
	}
	return nil
}
