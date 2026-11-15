package backtest

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/control"
	"github.com/James-Chapman/LLMTradingBot/internal/histdata"
)

// ToolVersion identifies the report format and code in run.json.
const ToolVersion = "backtest/1.0.0"

// SummarySchemaVersion is bumped whenever a summary file's JSON shape changes.
const SummarySchemaVersion = 1

// KindRun marks a single run's summary.json.
const KindRun = "run"

// ReportFiles are the files WriteReports writes into a run directory.
var ReportFiles = []string{"result.json", "summary.json", "summary.md", "trades.csv", "orders.csv", "equity.csv", "costs.json", "run.json"}

// Honesty is the block printed before any number: what the run can and
// cannot show.
type Honesty struct {
	Lines        []string `json:"lines"`
	Survivorship string   `json:"survivorship"` // explicit_list_warning | point_in_time_partial
	Meaningful   bool     `json:"statistically_meaningful"`
}

// Report is everything derived from one run (summary.json).
type Report struct {
	SchemaVersion  int            `json:"summary_schema_version"`
	Kind           string         `json:"kind"`
	ToolVersion    string         `json:"tool_version"`
	StrategyID     string         `json:"strategy_id"`
	Start          time.Time      `json:"start"`
	End            time.Time      `json:"end"`
	Markets        []string       `json:"markets"`
	Honesty        Honesty        `json:"honesty"`
	Verdict        []VerdictLine  `json:"verdict"`
	Metrics        Metrics        `json:"metrics"`
	Bridge         Bridge         `json:"bridge"`
	Costs          Costs          `json:"costs"`
	Benchmarks     BenchmarkSet   `json:"benchmarks"`
	Reconciliation Reconciliation `json:"reconciliation"`
	Signals        SignalStats    `json:"signals"`

	result *Result
}

// Result returns the raw result the report was built from.
func (r *Report) Result() *Result { return r.result }

// BuildReport derives a run's report; reader supplies the market data for
// the benchmarks (the same cache the run read).
func BuildReport(reader histdata.Reader, res *Result) (*Report, error) {
	bench, err := ComputeBenchmarks(reader, res)
	if err != nil {
		return nil, fmt.Errorf("benchmarks: %w", err)
	}
	m := ComputeMetrics(res)
	rec := Reconcile(res)
	r := &Report{
		SchemaVersion: SummarySchemaVersion, Kind: KindRun, ToolVersion: ToolVersion, StrategyID: res.StrategyID,
		Start: res.Start, End: res.End, Markets: res.Markets, Metrics: m, Bridge: ComputeBridge(res),
		Costs: ComputeCosts(res), Benchmarks: bench, Reconciliation: rec, Signals: ComputeSignals(res), result: res,
	}
	r.Honesty = honesty(res, m, rec)
	r.Verdict = RunVerdict(m, bench, rec)
	return r, nil
}

// honesty writes the statements every human-readable summary starts with.
func honesty(res *Result, m Metrics, rec Reconciliation) Honesty {
	h := Honesty{Meaningful: m.RoundTrips >= MeaningfulTrades && m.Days >= MeaningfulDays}
	add := func(format string, args ...any) { h.Lines = append(h.Lines, fmt.Sprintf(format, args...)) }
	if live := control.DefaultSelectedStrategy; res.StrategyID != live {
		add("NOT THE LIVE STRATEGY: the bot's live default is %s, whose LLM can scale confidence and veto entries. This %s run measures the indicator core only; it does not answer whether the bot as you run it is profitable.", live, res.StrategyID)
	}
	if rec.Warning {
		add("ACCOUNTING WARNING: the exact reconciliation of the order log failed; the numbers below are not trustworthy.")
	}
	if res.DataCheck == nil {
		add("DATA: the data cache's coverage was not checked for this run, so missing data cannot be told apart from periods without trades.")
	}
	for _, w := range res.DataWarnings {
		add("DATA WARNING: %s.", w)
	}
	if res.DataCheck != nil {
		if n := res.DataCheck.Counts[histdata.KindDuplicateID]; n > 0 {
			add("DATA: %d duplicate trade(s) were flagged at import in the range read and are kept in the bars, inflating volume and the taker-side notionals (flattering participation, the universe ranking and the minimum-volume filter).", n)
		}
	}
	if m.StalePositions > 0 {
		add("STALE POSITIONS: %d position(s) were held in markets that had no trade for more than %s; %d still held at the end were valued at their last trade. Ending equity %.2f (net %+.2f); excluding the stale positions (valued at zero) it would be %.2f (net %+.2f).",
			m.StalePositions, res.StaleAfter, m.StaleAtEnd, res.EndingEquity, m.NetPnL, res.EndingEquityWorstCase, m.NetPnLWorstCase)
	}
	if res.RiskFee.Warning != "" {
		add("RISK SIZING: %s.", res.RiskFee.Warning)
	}
	add("Period: %s to %s (%.1f days, UTC). Strategy %s on %s.", res.Start.Format(time.RFC3339), res.End.Format(time.RFC3339),
		m.Days, res.StrategyID, strings.Join(res.InitialMarkets, ", "))
	add("Round trips: %d (%d of them valued at the end of the run, not closed by the strategy).", m.RoundTrips, m.EndOfRunCloses)
	if m.RoundTrips < MeaningfulTrades {
		add("Fewer than %d round trips: the result is not statistically meaningful; luck alone can explain it.", MeaningfulTrades)
	}
	if m.Days < MeaningfulDays {
		add("The period is under %d days: too short to judge a strategy across market conditions.", MeaningfulDays)
	}
	add("This is one historical path. The same rules on another period or other markets can give a very different result.")
	add("Not the live set-up: live trades in %s mode by default (every idea waits for approval); this simulates the fully automated path. Settings: %s (the bot's database is never read).",
		config.Defaults().TradingMode, res.SettingsSource)
	emptyRows := "no candle for an interval without trades, as in Kraken's downloadable files"
	if res.FillEmptyCandles {
		emptyRows = "a flat zero-volume candle for every interval without trades (--fill-empty-candles)"
	}
	add("Candles: %s. Whether Kraken's REST OHLC, which the live bot uses, returns rows for empty intervals is not verified; on illiquid pairs it changes ATR, the stochastic and the higher-timeframe filter (compare with --fill-empty-candles).", emptyRows)
	add("The price history starts empty at the start, whereas live restores saved ticks after a restart (a small pessimistic difference in warm-up).")
	if len(res.UniverseSelections) > 0 {
		h.Survivorship = "point_in_time_partial"
		add("Universe: the point-in-time top-N re-selection is a different policy from live, which trades its fixed markets plus dynamic coins chosen once at start-up.")
		add("Survivorship: partial. The universe was chosen point in time from the data cache, but markets missing from the cache (for example delisted pairs absent from Kraken's archive) could not be chosen, which biases results upwards.")
	} else {
		h.Survivorship = "explicit_list_warning"
		add("Survivorship: the market list was given explicitly, chosen today knowing which coins survived; markets that were delisted or collapsed are missing, which biases results upwards.")
	}
	add("Fees: %s = %s bp taker on every fill. It must match your actual Kraken fee tier and the pair's discount; a higher rate makes every number here worse.",
		res.Exchange.FeePreset, fmtNum(res.Exchange.FeeBps))
	add("If any setting (defaults included) was chosen by looking at this period, the result is in-sample and overstates what to expect.")
	return h
}

// RunInfo is everything needed to reproduce a run (run.json).
type RunInfo struct {
	ToolVersion         string                `json:"tool_version"`
	BuildRevision       string                `json:"build_revision"`
	ResultSchemaVersion int                   `json:"result_schema_version"`
	CommandLine         []string              `json:"command_line"`
	Seed                int64                 `json:"seed"`
	StrategyID          string                `json:"strategy_id"`
	Start               time.Time             `json:"start"`
	End                 time.Time             `json:"end"`
	StartingCapital     float64               `json:"starting_capital"`
	InitialMarkets      []string              `json:"initial_markets"`
	LearnerEnabled      bool                  `json:"learner_enabled"`
	LearnerSeedOutcomes int                   `json:"learner_seed_outcomes"`
	Settings            map[string]string     `json:"settings"`
	Exchange            ExchangeConfig        `json:"exchange"`
	Assumptions         []Assumption          `json:"assumptions"`
	UniverseSelections  []UniverseSelection   `json:"universe_selections"`
	DataProvenance      []histdata.Provenance `json:"data_provenance"`
}

// buildRevision returns the VCS revision the binary was built from, if known.
func buildRevision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	rev, dirty := "", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev != "" && dirty {
		rev += "+modified"
	}
	return rev
}

// NewRunInfo returns the reproduction record of res run by cmdline.
func NewRunInfo(res *Result, cmdline []string) RunInfo {
	if cmdline == nil {
		cmdline = []string{}
	}
	return RunInfo{
		ToolVersion: ToolVersion, BuildRevision: buildRevision(), ResultSchemaVersion: res.SchemaVersion, CommandLine: cmdline,
		Seed: res.Seed, StrategyID: res.StrategyID, Start: res.Start, End: res.End, StartingCapital: res.StartingCapital,
		InitialMarkets: res.InitialMarkets, LearnerEnabled: res.LearnerEnabled, LearnerSeedOutcomes: res.LearnerSeedOutcomes,
		Settings: res.Settings, Exchange: res.Exchange, Assumptions: res.Assumptions,
		UniverseSelections: res.UniverseSelections, DataProvenance: res.DataProvenance,
	}
}

// WriteJSON writes v as indented JSON to path, creating parent directories.
func WriteJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// WriteReports writes a run's report files (ReportFiles) into dir.
func WriteReports(dir string, rep *Report, cmdline []string) error {
	res := rep.result
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	writers := []func() error{
		func() error { return WriteResult(res, filepath.Join(dir, "result.json")) },
		func() error { return WriteJSON(filepath.Join(dir, "summary.json"), rep) },
		func() error { return os.WriteFile(filepath.Join(dir, "summary.md"), []byte(RenderSummary(rep)), 0o644) },
		func() error { return writeCSV(filepath.Join(dir, "trades.csv"), tradeRows(res)) },
		func() error { return writeCSV(filepath.Join(dir, "orders.csv"), orderRows(res)) },
		func() error { return writeCSV(filepath.Join(dir, "equity.csv"), equityRows(res)) },
		func() error { return WriteJSON(filepath.Join(dir, "costs.json"), rep.Costs) },
		func() error { return WriteJSON(filepath.Join(dir, "run.json"), NewRunInfo(res, cmdline)) },
	}
	for _, w := range writers {
		if err := w(); err != nil {
			return err
		}
	}
	return nil
}

// writeCSV writes rows (header first) to path.
func writeCSV(path string, rows [][]string) error {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.WriteAll(rows); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// f formats a float so it parses back to the same value.
func f(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

// ts formats a time for the CSV files.
func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// orderCost returns an order's execution cost components times its quantity.
func orderCost(o OrderLogEntry) (drift, spread, impact, rounding float64) {
	q := o.FilledQty
	return q * o.LatencyDrift, q * o.SpreadCost, q * o.ImpactCost, q * o.RoundingCost
}

// tradeRows returns trades.csv: one row per closed round trip.
func tradeRows(res *Result) [][]string {
	orders := map[string]OrderLogEntry{}
	for _, o := range res.Orders {
		orders[o.ID] = o
	}
	rows := [][]string{{"position_id", "market", "entry_time", "exit_time", "holding_seconds", "qty",
		"entry_reference_price", "entry_fill_price", "exit_reference_price", "exit_fill_price", "gross_pnl",
		"latency_drift_cost", "spread_cost", "impact_cost", "rounding_cost", "execution_cost", "entry_fee", "exit_fee", "fees",
		"net_pnl", "return_pct", "exit_reason"}}
	for _, t := range res.Trades {
		entry, exit := orders[t.EntryOrder], orders[t.ExitOrder]
		d1, s1, i1, r1 := orderCost(entry)
		d2, s2, i2, r2 := orderCost(exit)
		drift, spread, impact, rounding := d1+d2, s1+s2, i1+i2, r1+r2
		cost := t.Qty*t.EntryPrice + t.EntryFee
		ret := 0.0
		if cost > 0 {
			ret = 100 * t.PnL / cost
		}
		rows = append(rows, []string{t.PositionID, t.Market, ts(t.EntryTime), ts(t.ExitTime),
			strconv.FormatInt(int64(t.ExitTime.Sub(t.EntryTime)/time.Second), 10), f(t.Qty),
			f(entry.ReferencePrice), f(t.EntryPrice), f(exit.ReferencePrice), f(t.ExitPrice),
			f(t.Qty * (exit.ReferencePrice - entry.ReferencePrice)),
			f(drift), f(spread), f(impact), f(rounding), f(drift + spread + impact + rounding),
			f(t.EntryFee), f(t.ExitFee), f(t.EntryFee + t.ExitFee), f(t.PnL), f(ret), t.ExitReason})
	}
	return rows
}

// orderRows returns orders.csv: every order request, rejections included.
func orderRows(res *Result) [][]string {
	rows := [][]string{{"id", "market", "side", "reason", "strategy_id", "idea_id", "position_id", "decision_time", "fill_time",
		"requested_qty", "filled_qty", "reference_price", "fill_bar_close", "fill_price", "price_source", "half_spread",
		"latency_drift", "spread_cost", "impact_cost", "rounding_cost", "participation", "notional", "fee", "fee_rate",
		"realised_pnl", "status", "reject_reason"}}
	for _, o := range res.Orders {
		pnl := ""
		if o.RealisedPnL != nil {
			pnl = f(*o.RealisedPnL)
		}
		rows = append(rows, []string{o.ID, o.Market, o.Side, o.Reason, o.StrategyID, o.IdeaID, o.PositionID,
			ts(o.DecisionTime), ts(o.FillTime), f(o.RequestedQty), f(o.FilledQty), f(o.ReferencePrice), f(o.FillBarClose),
			f(o.FillPrice), o.PriceSource, f(o.HalfSpread), f(o.LatencyDrift), f(o.SpreadCost), f(o.ImpactCost),
			f(o.RoundingCost), f(o.Participation), f(o.Notional), f(o.Fee), f(o.FeeRate), pnl, o.Status, o.RejectReason})
	}
	return rows
}

// equityRows returns equity.csv: the sampled equity curve with its drawdown
// from the per-tick running peak.
func equityRows(res *Result) [][]string {
	rows := [][]string{{"time", "equity", "cash", "positions_value", "peak", "drawdown", "drawdown_pct", "min_equity", "open_positions"}}
	for _, p := range res.EquityCurve {
		pct := 0.0
		if p.Peak > 0 {
			pct = 100 * (p.Peak - p.Equity) / p.Peak
		}
		rows = append(rows, []string{ts(p.Time), f(p.Equity), f(p.Cash), f(p.PositionsValue), f(p.Peak), f(p.Peak - p.Equity),
			f(pct), f(p.MinEquity), strconv.Itoa(p.OpenPositions)})
	}
	return rows
}
