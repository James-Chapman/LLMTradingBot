package backtest

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/histdata"
)

// Regime buckets.
const (
	BucketDay     = "day"
	BucketWeek    = "week" // ISO weeks, Monday 00:00 UTC
	BucketMonth   = "month"
	BucketQuarter = "quarter"
)

// Regime labels.
const (
	TrendBull     = "bull"
	TrendSideways = "sideways"
	TrendBear     = "bear"
	VolHigh       = "high"
	VolLow        = "low"
	LabelUnknown  = "unknown"
	labelAll      = "all"
)

// MinBucketsPerCell is the fewest buckets a regime cell needs before it is
// anything but thin.
const MinBucketsPerCell = 3

// RegimeSpec chooses the buckets and the trend thresholds (percent
// buy-and-hold price return over a bucket).
type RegimeSpec struct {
	Bucket       string  `json:"bucket"`
	BullAbovePct float64 `json:"bull_above_pct"`
	BearBelowPct float64 `json:"bear_below_pct"`
}

// DefaultRegimeSpec returns the documented thresholds for a bucket length:
// ±1 % a day, ±2 % a week, ±5 % a month, ±10 % a quarter.
func DefaultRegimeSpec(bucket string) (RegimeSpec, error) {
	t, ok := map[string]float64{BucketDay: 1, BucketWeek: 2, BucketMonth: 5, BucketQuarter: 10}[bucket]
	if !ok {
		return RegimeSpec{}, fmt.Errorf("bucket must be day, week, month or quarter, got %q", bucket)
	}
	return RegimeSpec{Bucket: bucket, BullAbovePct: t, BearBelowPct: -t}, nil
}

// RegimeBucket is the strategy and the market over one bucket.
type RegimeBucket struct {
	Start              time.Time `json:"start"`
	End                time.Time `json:"end"`
	Trend              string    `json:"trend"`
	Volatility         string    `json:"volatility"`
	MarketReturnPct    *float64  `json:"market_return_pct"`   // mean price return of the initial markets
	RealisedVolatility *float64  `json:"realised_volatility"` // median daily realised volatility in the bucket
	PnL                float64   `json:"pnl"`                 // equity at the end - equity at the start
	ReturnPct          float64   `json:"return_pct"`
	BenchmarkReturnPct float64   `json:"benchmark_return_pct"`
	ExcessReturnPct    float64   `json:"excess_return_pct"`
	RoundTrips         int       `json:"round_trips"` // closed in the bucket
	Wins               int       `json:"wins"`
	WinRatePct         *float64  `json:"win_rate_pct"`
	MaxDrawdownPct     float64   `json:"max_drawdown_pct"`
}

// RegimeCell aggregates the buckets of one regime.
type RegimeCell struct {
	Trend                  string   `json:"trend"`
	Volatility             string   `json:"volatility"`
	Buckets                int      `json:"buckets"`
	Thin                   bool     `json:"thin"` // fewer than MinBucketsPerCell buckets
	PnL                    float64  `json:"pnl"`
	CompoundedReturnPct    float64  `json:"compounded_return_pct"`
	BenchmarkCompoundedPct float64  `json:"benchmark_compounded_pct"`
	BucketsBelowBenchmark  int      `json:"buckets_below_benchmark"`
	RoundTrips             int      `json:"round_trips"`
	WinRatePct             *float64 `json:"win_rate_pct"`
}

// RegimeReport is the regime summary of one continuous run.
type RegimeReport struct {
	SchemaVersion       int            `json:"summary_schema_version"`
	Kind                string         `json:"kind"`
	StrategyID          string         `json:"strategy_id"`
	Start               time.Time      `json:"start"`
	End                 time.Time      `json:"end"`
	Spec                RegimeSpec     `json:"spec"`
	Benchmark           string         `json:"benchmark"`
	VolatilityThreshold *float64       `json:"volatility_threshold"` // median daily realised volatility over the whole period
	Run                 RunRow         `json:"run"`
	Buckets             []RegimeBucket `json:"buckets"`
	Cells               []RegimeCell   `json:"cells"`
	ByTrend             []RegimeCell   `json:"by_trend"`
	ByVolatility        []RegimeCell   `json:"by_volatility"`
	Notes               []string       `json:"notes"`
	Verdict             []VerdictLine  `json:"verdict"`
}

// bucketStart returns the start of the bucket holding t.
func bucketStart(t time.Time, bucket string) time.Time {
	y, m, d := t.UTC().Date()
	switch bucket {
	case BucketWeek:
		day := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
		return day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))
	case BucketMonth:
		return time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
	case BucketQuarter:
		return time.Date(y, (m-1)/3*3+1, 1, 0, 0, 0, 0, time.UTC)
	}
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// nextBucket returns the start of the bucket after the one starting at s.
func nextBucket(s time.Time, bucket string) time.Time {
	switch bucket {
	case BucketWeek:
		return s.AddDate(0, 0, 7)
	case BucketMonth:
		return s.AddDate(0, 1, 0)
	case BucketQuarter:
		return s.AddDate(0, 3, 0)
	}
	return s.AddDate(0, 0, 1)
}

// valueAt returns a daily series' value at t (start for the run start).
func valueAt(daily []DailyPoint, start time.Time, startValue float64, t time.Time) (float64, bool) {
	if t.Equal(start) {
		return startValue, true
	}
	i := sort.Search(len(daily), func(i int) bool { return !daily[i].Time.Before(t) })
	if i < len(daily) && daily[i].Time.Equal(t) {
		return daily[i].Value, true
	}
	return 0, false
}

// AnalyseRegimes segments one continuous run into calendar buckets, labels
// each by the initial markets' own behaviour, and attributes the run's P&L.
func AnalyseRegimes(rep *Report, spec RegimeSpec) (*RegimeReport, error) {
	if _, err := DefaultRegimeSpec(spec.Bucket); err != nil {
		return nil, err
	}
	if spec.BearBelowPct > spec.BullAbovePct {
		return nil, fmt.Errorf("the bear threshold %v %% is above the bull threshold %v %%", spec.BearBelowPct, spec.BullAbovePct)
	}
	res := rep.Result()
	bench, ok := rep.Benchmarks.primary()
	if !ok {
		return nil, fmt.Errorf("the run has no buy-and-hold benchmark to compare with")
	}
	out := &RegimeReport{
		SchemaVersion: SummarySchemaVersion, Kind: KindRegimes, StrategyID: res.StrategyID, Start: res.Start, End: res.End,
		Spec: spec, Benchmark: bench.Name, Run: NewRunRow("run", rep),
	}
	vols := combinedVolatility(rep.Benchmarks.Paths)
	var threshold float64
	if len(vols) > 0 {
		all := make([]float64, 0, len(vols))
		for _, v := range vols {
			all = append(all, v.Value)
		}
		threshold = median(all)
		out.VolatilityThreshold = &threshold
	}

	for s := res.Start; s.Before(res.End); {
		e := nextBucket(bucketStart(s, spec.Bucket), spec.Bucket)
		if e.After(res.End) {
			e = res.End
		}
		b, err := regimeBucket(res, rep.Benchmarks.Paths, bench, vols, threshold, spec, s, e)
		if err != nil {
			return nil, err
		}
		out.Buckets = append(out.Buckets, b)
		s = e
	}
	out.Cells = regimeCells(out.Buckets, func(b RegimeBucket) (string, string) { return b.Trend, b.Volatility })
	out.ByTrend = regimeCells(out.Buckets, func(b RegimeBucket) (string, string) { return b.Trend, labelAll })
	out.ByVolatility = regimeCells(out.Buckets, func(b RegimeBucket) (string, string) { return labelAll, b.Volatility })
	out.Notes = []string{
		"One continuous run segmented afterwards, not re-run per bucket: open positions and the learner carry across buckets as they would live. A round trip belongs to the bucket in which it closed; a bucket's P&L is the change in equity over it, so the buckets add up to the run's net P&L.",
		fmt.Sprintf("Trend: the mean buy-and-hold price return of the initial markets over the bucket; bull above %+.2f %%, bear below %+.2f %%, sideways between.", spec.BullAbovePct, spec.BearBelowPct),
		"Volatility: a day's realised volatility is the square root of the sum of its squared 5-minute log returns of the 30 s tick price (averaged over the initial markets); a bucket is high when its median day is above the median day of the whole period. That threshold uses the whole period, known only afterwards: the labels are for analysis only, never for trading decisions.",
		fmt.Sprintf("The benchmark per bucket is %s's own equity change over the bucket (its entry cost falls in the first bucket, its exit cost in the last).", bench.Name),
		"Bucket drawdowns use the sampled equity curve (each point keeps the lowest tick equity since the previous one); a peak between samples can be missed.",
	}
	out.Verdict = regimeVerdict(out)
	return out, nil
}

// combinedVolatility averages the markets' daily realised volatility per day.
func combinedVolatility(paths []MarketPath) []DailyPoint {
	sum, n := map[time.Time]float64{}, map[time.Time]int{}
	for _, p := range paths {
		for _, v := range p.DailyVol {
			sum[v.Time] += v.Value
			n[v.Time]++
		}
	}
	out := make([]DailyPoint, 0, len(sum))
	for t, s := range sum {
		out = append(out, DailyPoint{Time: t, Value: s / float64(n[t])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out
}

// regimeBucket measures and labels the bucket [s, e].
func regimeBucket(res *Result, paths []MarketPath, bench Benchmark, vols []DailyPoint, threshold float64, spec RegimeSpec, s, e time.Time) (RegimeBucket, error) {
	b := RegimeBucket{Start: s, End: e, Trend: LabelUnknown, Volatility: LabelUnknown}
	e0, ok0 := valueAt(res.TickStats.Daily, res.Start, res.StartingCapital, s)
	e1, ok1 := valueAt(res.TickStats.Daily, res.Start, res.StartingCapital, e)
	b0, okb0 := valueAt(bench.Daily, res.Start, bench.InitialCapital, s)
	b1, okb1 := valueAt(bench.Daily, res.Start, bench.InitialCapital, e)
	if !ok0 || !ok1 || !okb0 || !okb1 {
		return b, fmt.Errorf("no equity at the bucket bounds %s / %s", s.Format(time.RFC3339), e.Format(time.RFC3339))
	}
	b.PnL = e1 - e0
	if e0 > 0 {
		b.ReturnPct = 100 * (e1/e0 - 1)
	}
	if b0 > 0 {
		b.BenchmarkReturnPct = 100 * (b1/b0 - 1)
	}
	b.ExcessReturnPct = b.ReturnPct - b.BenchmarkReturnPct

	sum, n := 0.0, 0
	for _, p := range paths {
		p0, ok0 := valueAt(p.Closes, res.Start, p.StartPrice, s)
		p1, ok1 := valueAt(p.Closes, res.Start, p.StartPrice, e)
		if ok0 && ok1 && p0 > 0 && p1 > 0 && containsMarket(res.InitialMarkets, p.Market) {
			sum += 100 * (p1/p0 - 1)
			n++
		}
	}
	if n > 0 {
		r := sum / float64(n)
		b.MarketReturnPct = &r
		switch {
		case r > spec.BullAbovePct:
			b.Trend = TrendBull
		case r < spec.BearBelowPct:
			b.Trend = TrendBear
		default:
			b.Trend = TrendSideways
		}
	}
	var inBucket []float64
	for _, v := range vols {
		if v.Time.After(s) && !v.Time.After(e) {
			inBucket = append(inBucket, v.Value)
		}
	}
	if len(inBucket) > 0 {
		m := median(inBucket)
		b.RealisedVolatility = &m
		b.Volatility = VolLow
		if m > threshold {
			b.Volatility = VolHigh
		}
	}

	for _, t := range res.Trades {
		if t.ExitTime.After(s) && !t.ExitTime.After(e) {
			b.RoundTrips++
			if t.PnL > BreakevenTolerance {
				b.Wins++
			}
		}
	}
	if b.RoundTrips > 0 {
		b.WinRatePct = ptr(100 * float64(b.Wins) / float64(b.RoundTrips))
	}
	peak := e0
	for _, p := range res.EquityCurve {
		if !p.Time.After(s) || p.Time.After(e) {
			continue
		}
		if peak > 0 {
			b.MaxDrawdownPct = math.Max(b.MaxDrawdownPct, 100*(peak-math.Min(p.MinEquity, p.Equity))/peak)
		}
		peak = math.Max(peak, p.Equity)
	}
	if peak > 0 && e.Equal(res.End) {
		b.MaxDrawdownPct = math.Max(b.MaxDrawdownPct, 100*(peak-e1)/peak) // the end-of-run valuation
	}
	return b, nil
}

// containsMarket reports whether markets holds m.
func containsMarket(markets []string, m string) bool {
	for _, x := range markets {
		if x == m {
			return true
		}
	}
	return false
}

// trendOrder and volOrder fix the order of the cells.
var (
	trendOrder = []string{TrendBull, TrendSideways, TrendBear, LabelUnknown, labelAll}
	volOrder   = []string{VolHigh, VolLow, LabelUnknown, labelAll}
)

// orderOf returns v's position in order.
func orderOf(order []string, v string) int {
	for i, x := range order {
		if x == v {
			return i
		}
	}
	return len(order)
}

// regimeCells aggregates buckets by the key, in the fixed label order.
func regimeCells(buckets []RegimeBucket, key func(RegimeBucket) (string, string)) []RegimeCell {
	type acc struct {
		cell           RegimeCell
		product, bench float64
		wins           int
	}
	cells := map[[2]string]*acc{}
	for _, b := range buckets {
		t, v := key(b)
		a := cells[[2]string{t, v}]
		if a == nil {
			a = &acc{cell: RegimeCell{Trend: t, Volatility: v}, product: 1, bench: 1}
			cells[[2]string{t, v}] = a
		}
		a.cell.Buckets++
		a.cell.PnL += b.PnL
		a.product *= 1 + b.ReturnPct/100
		a.bench *= 1 + b.BenchmarkReturnPct/100
		a.cell.RoundTrips += b.RoundTrips
		a.wins += b.Wins
		if b.ReturnPct < b.BenchmarkReturnPct {
			a.cell.BucketsBelowBenchmark++
		}
	}
	out := make([]RegimeCell, 0, len(cells))
	for _, a := range cells {
		c := a.cell
		c.Thin = c.Buckets < MinBucketsPerCell
		c.CompoundedReturnPct, c.BenchmarkCompoundedPct = 100*(a.product-1), 100*(a.bench-1)
		if c.RoundTrips > 0 {
			c.WinRatePct = ptr(100 * float64(a.wins) / float64(c.RoundTrips))
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if ti, tj := orderOf(trendOrder, out[i].Trend), orderOf(trendOrder, out[j].Trend); ti != tj {
			return ti < tj
		}
		return orderOf(volOrder, out[i].Volatility) < orderOf(volOrder, out[j].Volatility)
	})
	return out
}

// Regime verdict rules.
const (
	RuleRegimeBelowBuyHold = "below_buy_and_hold_in_buckets"
	RuleRegimePnLByTrend   = "pnl_by_trend"
	RuleRegimeThinCells    = "thin_cells"
)

// regimeVerdict applies the regime rules.
func regimeVerdict(r *RegimeReport) []VerdictLine {
	below := 0
	for _, b := range r.Buckets {
		if b.ReturnPct < b.BenchmarkReturnPct {
			below++
		}
	}
	out := []VerdictLine{{Rule: RuleRegimeBelowBuyHold,
		Text: fmt.Sprintf("Underperforms %s in %d of %d %s buckets.", r.Benchmark, below, len(r.Buckets), r.Spec.Bucket)}}
	var parts []string
	for _, c := range r.ByTrend {
		parts = append(parts, fmt.Sprintf("%s %+.2f over %d bucket(s)", c.Trend, c.PnL, c.Buckets))
	}
	out = append(out, VerdictLine{Rule: RuleRegimePnLByTrend, Text: "Net P&L by trend: " + strings.Join(parts, "; ") + "."})
	thin := 0
	for _, c := range r.Cells {
		if c.Thin {
			thin++
		}
	}
	if thin > 0 {
		out = append(out, VerdictLine{Rule: RuleRegimeThinCells,
			Text: fmt.Sprintf("%d of %d trend×volatility cells hold fewer than %d buckets: too thin to conclude anything about them.", thin, len(r.Cells), MinBucketsPerCell)})
	}
	return out
}

// RunRegimes runs base once and segments it.
func RunRegimes(ctx context.Context, reader histdata.Reader, base Config, spec RegimeSpec, progress Progress) (*RegimeReport, []RunOutcome, error) {
	if _, err := DefaultRegimeSpec(spec.Bucket); err != nil {
		return nil, nil, err
	}
	outs, err := RunAll(ctx, reader, []RunSpec{{Label: "run", Config: base}}, 1, progress)
	if err != nil {
		return nil, nil, err
	}
	rep, err := AnalyseRegimes(outs[0].Report, spec)
	return rep, outs, err
}
