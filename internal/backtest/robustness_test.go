package backtest

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/histdata"
)

// outcomeJSON marshals the reports and results of outcomes for comparison.
func outcomeJSON(t *testing.T, outs []RunOutcome) []byte {
	t.Helper()
	var all []any
	for _, o := range outs {
		all = append(all, o.Label, o.Report, o.Report.Result())
	}
	raw, err := json.Marshal(all)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// outcomeNamed returns the outcome labelled label.
func outcomeNamed(t *testing.T, outs []RunOutcome, label string) RunOutcome {
	t.Helper()
	for _, o := range outs {
		if o.Label == label {
			return o
		}
	}
	t.Fatalf("no run %q", label)
	return RunOutcome{}
}

// walkReader returns one random-walk market from a pre-roll before t0 for n ticks.
func walkReader(seed uint64, n int, sigma float64) memReader {
	from := t0.Add(-PreRoll)
	pre := int(PreRoll / histdata.BarInterval)
	return oneMarket(randomWalkBars(seed, pre+n, from, 100, sigma))
}

// TestRunAll covers the multi-run executor.
func TestRunAll(t *testing.T) {
	t.Run("GIVEN independent runs of the real strategy WHEN executed with 1 and 3 workers THEN the results are identical and in spec order", func(t *testing.T) {
		reader := walkReader(31, 3000, 0.004)
		var specs []RunSpec
		for i, fee := range []string{"26", "80", "120", "40"} {
			cfg := testConfig([]string{"BTC/EUR"}, 2999)
			if err := cfg.Exchange.SetFees(fee); err != nil {
				t.Fatal(err)
			}
			specs = append(specs, RunSpec{Label: "fees=" + fee, Config: cfg})
			_ = i
		}
		var got [2][]byte
		for i, n := range []int{1, 3} {
			calls := 0
			outs, err := RunAll(context.Background(), reader, specs, n, func(done, total int, _ string) {
				calls++
				if total != len(specs) || done < 1 || done > total {
					t.Errorf("progress %d/%d", done, total)
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			if calls != len(specs) || outs[2].Label != "fees=120" {
				t.Fatalf("progress calls %d, labels %v", calls, outs[2].Label)
			}
			got[i] = outcomeJSON(t, outs)
		}
		if !bytes.Equal(got[0], got[1]) {
			t.Fatal("results depend on the number of workers")
		}
	})
}

// TestSplit covers the in-sample / out-of-sample split.
func TestSplit(t *testing.T) {
	const n = 8000
	split := tick(5000)

	t.Run("GIVEN a split date WHEN both periods run THEN the out-of-sample run is a fresh run from the split: empty learner, no earlier trade", func(t *testing.T) {
		reader := walkReader(3, n, 0.003)
		s := randomPlan(4, []string{"BTC/EUR"}, 36, n-5, 25)
		var obs []TickObservation
		base := withScript(testConfig([]string{"BTC/EUR"}, n-1), s, &obs)
		rep, outs, err := RunSplit(context.Background(), reader, base, SplitSpec{At: split}, 1, nil)
		if err != nil {
			t.Fatal(err)
		}
		is, oos := outcomeNamed(t, outs, LabelInSample).Report.Result(), outcomeNamed(t, outs, LabelOutOfSample).Report.Result()
		if len(is.LearnedOutcomes) == 0 || !is.End.Equal(split) || !oos.Start.Equal(split) {
			t.Fatalf("in-sample learned %d, periods %v-%v / %v-%v", len(is.LearnedOutcomes), is.Start, is.End, oos.Start, oos.End)
		}
		if oos.LearnerSeedOutcomes != 0 {
			t.Fatalf("the out-of-sample learner was seeded with %d outcomes", oos.LearnerSeedOutcomes)
		}
		for _, o := range obs {
			if o.Time.Equal(split.Add(histdata.BarInterval)) && o.LearnedOutcomes != 0 {
				t.Fatalf("the out-of-sample run's first tick saw %d learned outcomes", o.LearnedOutcomes)
			}
		}
		for _, o := range oos.Orders {
			if !o.DecisionTime.After(split) {
				t.Fatalf("out-of-sample order decided at %v, not after the split", o.DecisionTime)
			}
		}
		alone := withScript(testConfig([]string{"BTC/EUR"}, n-1), s, nil)
		alone.Start = split
		standalone, _ := json.Marshal(mustRun(t, reader, alone))
		got, _ := json.Marshal(oos)
		if !bytes.Equal(standalone, got) {
			t.Fatal("the out-of-sample run differs from a standalone run from the split date")
		}
		if rep.InSample.Label != LabelInSample || rep.OutOfSample.RoundTrips != len(oos.Trades) || !strings.Contains(strings.Join(rep.Notes, " "), "in-sample") {
			t.Fatalf("report %+v", rep)
		}
	})

	t.Run("GIVEN data before the out-of-sample pre-roll changes WHEN the split runs THEN the out-of-sample result is unchanged: its warm-up uses only data just before its start", func(t *testing.T) {
		base := walkReader(21, n, 0.004)
		alt := memReader{bars: map[string][]histdata.Bar{"BTC/EUR": append([]histdata.Bar(nil), base.bars["BTC/EUR"]...)}}
		for i, b := range alt.bars["BTC/EUR"] {
			if b.Start.Before(split.Add(-PreRoll)) {
				b.Open, b.High, b.Low, b.Close = b.Open*1.5, b.High*1.5, b.Low*1.5, b.Close*1.5
				b.BuyNotional, b.SellNotional = b.BuyNotional*1.5, b.SellNotional*1.5
				alt.bars["BTC/EUR"][i] = b
			}
		}
		var oos [2][]byte
		var decisions int
		for i, r := range []memReader{base, alt} {
			_, outs, err := RunSplit(context.Background(), r, testConfig([]string{"BTC/EUR"}, n-1), SplitSpec{At: split}, 2, nil)
			if err != nil {
				t.Fatal(err)
			}
			res := outcomeNamed(t, outs, LabelOutOfSample).Report.Result()
			oos[i], _ = json.Marshal(res)
			decisions = len(res.Decisions)
		}
		if !bytes.Equal(oos[0], oos[1]) {
			t.Fatal("the out-of-sample run saw data from before its pre-roll")
		}
		if decisions == 0 {
			t.Fatal("the out-of-sample run made no decision, so the test proves nothing")
		}
	})

	t.Run("GIVEN an out-of-sample fraction WHEN the split point is computed THEN it falls on a UTC day boundary inside the period", func(t *testing.T) {
		start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		at, _, err := SplitPoint(start, start.AddDate(0, 0, 100), SplitSpec{OOSFraction: 0.3})
		if err != nil || !at.Equal(start.AddDate(0, 0, 70)) {
			t.Fatalf("split %v (%v)", at, err)
		}
		if _, _, err := SplitPoint(start, start.AddDate(0, 0, 10), SplitSpec{At: start}); err == nil {
			t.Fatal("a split at the start must be refused")
		}
	})
}

// TestWalkForward covers sequential train/test windows.
func TestWalkForward(t *testing.T) {
	const n = 6 * 120 // six hours
	spec := WalkSpec{Train: 2 * time.Hour, Test: time.Hour, Step: time.Hour, TuneKey: "STOP_LOSS_PCT", TuneValues: []string{"0.005", "0.05"}}
	plan := func() *scripted { return randomPlan(12, []string{"BTC/EUR"}, 1, n-5, 8) }
	run := func(t *testing.T, r histdata.Reader) (*WalkForwardReport, []RunOutcome) {
		t.Helper()
		rep, outs, err := RunWalkForward(context.Background(), r, withScript(testConfig([]string{"BTC/EUR"}, n), plan(), nil), spec, 1, nil)
		if err != nil {
			t.Fatal(err)
		}
		return rep, outs
	}

	t.Run("GIVEN tuning over train windows WHEN the test window's data changes THEN the value chosen for it does not", func(t *testing.T) {
		base := walkReader(8, n+10, 0.003)
		rep, _ := run(t, base)
		if len(rep.Windows) != 4 {
			t.Fatalf("%d windows, want 4", len(rep.Windows))
		}
		w := rep.Windows[0]
		alt := memReader{bars: map[string][]histdata.Bar{"BTC/EUR": append([]histdata.Bar(nil), base.bars["BTC/EUR"]...)}}
		for i, b := range alt.bars["BTC/EUR"] {
			if !b.Start.Before(w.TrainEnd) {
				alt.bars["BTC/EUR"][i] = flatBar(b.Start, b.Close*3, 0.01, 1)
			}
		}
		altRep, _ := run(t, alt)
		a, _ := json.Marshal(w.Train)
		b, _ := json.Marshal(altRep.Windows[0].Train)
		if altRep.Windows[0].Chosen != w.Chosen || !bytes.Equal(a, b) {
			t.Fatalf("the choice for window 0 changed with its test data: %q vs %q", w.Chosen, altRep.Windows[0].Chosen)
		}
		if altRep.Windows[0].Test.NetReturnPct == w.Test.NetReturnPct {
			t.Fatal("the test window's own result should change with its data")
		}
	})

	t.Run("GIVEN walk-forward windows WHEN aggregated THEN only test windows count, compounded, and the learner carries from train to test", func(t *testing.T) {
		rep, outs := run(t, walkReader(8, n+10, 0.003))
		product, profitable, trips := 1.0, 0, 0
		for i, w := range rep.Windows {
			product *= 1 + w.Test.NetReturnPct/100
			trips += w.Test.RoundTrips
			if w.Test.NetPnL > 0 {
				profitable++
			}
			if !w.TestStart().Equal(w.TrainEnd) || !w.TestEnd.Equal(w.TrainEnd.Add(spec.Test)) {
				t.Fatalf("window %d periods %+v", i, w)
			}
			train := outcomeNamed(t, outs, w.TrainLabel(w.Chosen)).Report.Result()
			test := outcomeNamed(t, outs, w.TestLabel()).Report.Result()
			if test.LearnerSeedOutcomes != len(train.LearnedOutcomes) || w.SeededOutcomes != len(train.LearnedOutcomes) {
				t.Fatalf("window %d: test seeded with %d outcomes, its train run learned %d", i, test.LearnerSeedOutcomes, len(train.LearnedOutcomes))
			}
		}
		a := rep.Aggregate
		if a.Windows != 4 || a.Profitable != profitable || a.RoundTrips != trips || !near(a.CompoundedReturnPct, 100*(product-1), 1e-9) {
			t.Fatalf("aggregate %+v, want compounded %v of the test windows", a, 100*(product-1))
		}
		if rep.Tuning == nil || len(rep.Tuning.Chosen) != 4 {
			t.Fatalf("tuning %+v", rep.Tuning)
		}
		notes := strings.Join(rep.Notes, " ")
		if !strings.Contains(notes, "learner") || !strings.Contains(notes, "train window") {
			t.Fatalf("notes must state what the train window is used for: %s", notes)
		}
	})

	t.Run("GIVEN a step shorter than the test window WHEN planned THEN it is refused", func(t *testing.T) {
		bad := spec
		bad.Step = 30 * time.Minute
		if _, _, err := RunWalkForward(context.Background(), walkReader(8, n+10, 0.003), testConfig([]string{"BTC/EUR"}, n), bad, 1, nil); err == nil {
			t.Fatal("overlapping test windows must be refused")
		}
	})
}

// TestSensitivity covers one-at-a-time stresses.
func TestSensitivity(t *testing.T) {
	const n = 2000
	base := func() Config {
		cfg := withScript(testConfig([]string{"BTC/EUR"}, n-1), randomPlan(6, []string{"BTC/EUR"}, 36, n-5, 15), nil)
		cfg.DisableLearner = true
		cfg.Exchange.FillModel = FillFixed
		cfg.Settings.MaxDailyLossPercent = 100 // the daily loss limit would otherwise stop trading sooner at higher fees
		return cfg
	}

	t.Run("GIVEN the same trades WHEN fees rise THEN net return never increases", func(t *testing.T) {
		rep, _, err := RunSensitivity(context.Background(), walkReader(2, n, 0.002), base(), SensitivitySpec{
			Dimensions: []Dimension{{Name: DimFees, Values: []string{"10", "40", "80", "160"}}}}, 1, nil)
		if err != nil {
			t.Fatal(err)
		}
		var prev *SensitivityRow
		for i := range rep.Rows {
			row := &rep.Rows[i]
			if prev != nil {
				if row.Row.NetReturnPct > prev.Row.NetReturnPct || row.Row.RoundTrips != prev.Row.RoundTrips {
					t.Fatalf("fees %s -> %s: net %v -> %v, trips %d -> %d", prev.Value, row.Value, prev.Row.NetReturnPct, row.Row.NetReturnPct, prev.Row.RoundTrips, row.Row.RoundTrips)
				}
			}
			prev = row
		}
		if len(rep.Rows) != 4 || !rep.Rows[2].Baseline || rep.Rows[0].Baseline {
			t.Fatalf("rows %+v: the 80 bp row must be the baseline", rep.Rows)
		}
		if rep.Rows[0].Row.RoundTrips == 0 {
			t.Fatal("no trades: the test proves nothing")
		}
	})

	t.Run("GIVEN fee levels bracketing zero WHEN the break-even fee is estimated THEN it is the linear interpolation between them", func(t *testing.T) {
		be := BreakEvenFee([]FeePoint{{26, 2}, {40, 1}, {80, -1}, {120, -3}})
		if be.Status != BreakEvenInterpolated || be.FeeBps == nil || !near(*be.FeeBps, 60, 1e-12) || !strings.Contains(be.Note, "estimate") {
			t.Fatalf("break-even %+v", be)
		}
		if be := BreakEvenFee([]FeePoint{{26, 2}, {120, 1}}); be.Status != BreakEvenAboveTested || *be.FeeBps != 120 {
			t.Fatalf("all positive: %+v", be)
		}
		if be := BreakEvenFee([]FeePoint{{26, -2}, {120, -4}}); be.Status != BreakEvenBelowTested || *be.FeeBps != 26 {
			t.Fatalf("all negative: %+v", be)
		}
	})

	t.Run("GIVEN a flat baseline without round trips WHEN the sensitivity verdict is given THEN it says the run shows nothing and never calls it negative", func(t *testing.T) {
		rep := &SensitivityReport{Baseline: RunRow{}, Rows: []SensitivityRow{{Dimension: DimFees, Value: "26"}},
			BreakEvenFee: BreakEvenFee([]FeePoint{{26, 0}, {120, 0}})}
		rep.markSurvival()
		v := sensitivityVerdict(rep, false)
		var text strings.Builder
		for _, l := range v {
			text.WriteString(l.Text + "\n")
		}
		if v[0].Rule != RuleSensNoTrades || strings.Contains(text.String(), "negative") || strings.Contains(rep.BreakEvenFee.Note, "negative") {
			t.Fatalf("verdict %+v, break-even %+v", v, rep.BreakEvenFee)
		}
	})

	t.Run("GIVEN stresses WHEN the sign of a variant differs from the baseline THEN it is marked as not surviving", func(t *testing.T) {
		rep := &SensitivityReport{Baseline: RunRow{NetPnL: 5}, Rows: []SensitivityRow{
			{Dimension: DimFees, Value: "40", Row: RunRow{NetPnL: 8}}, {Dimension: DimFees, Value: "120", Row: RunRow{NetPnL: -1}},
			{Dimension: DimLatency, Value: "4", Row: RunRow{NetPnL: 1}},
		}}
		rep.markSurvival()
		if !rep.Rows[0].SignSurvives || rep.Rows[1].SignSurvives || !rep.Rows[2].SignSurvives {
			t.Fatalf("rows %+v", rep.Rows)
		}
		if len(rep.Survival) != 2 || rep.Survival[0].Survives || !rep.Survival[1].Survives || rep.Survival[0].Failing[0] != "120" {
			t.Fatalf("survival %+v", rep.Survival)
		}
	})

	for name, c := range map[string][2]string{
		"an unknown setting": {"NOT_A_SETTING", "1"}, "a secret": {"KRAKEN_API_SECRET", "x"}, "a bad fee": {DimFees, "cheap"},
		"zero latency": {DimLatency, "0"}, "a negative capital": {DimCapital, "-5"},
	} {
		t.Run("GIVEN "+name+" WHEN a variation is applied THEN it is refused", func(t *testing.T) {
			cfg := base()
			if err := ApplyDimension(&cfg, c[0], c[1]); err == nil {
				t.Fatalf("%s=%s accepted", c[0], c[1])
			}
		})
	}

	t.Run("GIVEN a setting key WHEN varied THEN it is applied through the settings registry", func(t *testing.T) {
		cfg := base()
		if err := ApplyDimension(&cfg, "MIN_SIGNAL_CONFIDENCE", "0.8"); err != nil || cfg.Settings.MinSignalConfidence != 0.8 {
			t.Fatalf("%v %v", err, cfg.Settings.MinSignalConfidence)
		}
	})

	t.Run("GIVEN a two-dimension grid WHEN planned THEN every combination runs once", func(t *testing.T) {
		rep, outs, err := RunSensitivity(context.Background(), walkReader(2, 400, 0.002), base(), SensitivitySpec{
			Dimensions: []Dimension{{Name: DimFees, Values: []string{"40", "120"}}, {Name: DimLatency, Values: []string{"1", "4"}}},
			Grid:       []string{DimFees, DimLatency}}, 1, nil) // one worker: the scripted strategy is shared
		if err != nil {
			t.Fatal(err)
		}
		grid := 0
		for _, r := range rep.Rows {
			if r.Dimension == DimFees+"×"+DimLatency {
				grid++
			}
		}
		if grid != 4 || len(outs) != 6 { // baseline, fees 40/120, latency 4, fees 40/120 at latency 4; the latency-1 cells reuse the fee runs
			t.Fatalf("grid rows %d, runs %d", grid, len(outs))
		}
	})
}

// dayPath returns bars for days of price moves: each day goes from
// prices[d] to prices[d+1] geometrically; osc[d] adds ±1 % every 5 minutes.
func dayPath(prices []float64, osc []bool) []histdata.Bar {
	from := t0.Add(-PreRoll)
	var bars []histdata.Bar
	for at := from; at.Before(t0.Add(time.Duration(len(prices)-1) * 24 * time.Hour)); at = at.Add(histdata.BarInterval) {
		p := prices[0]
		if !at.Before(t0) {
			d := int(at.Sub(t0) / (24 * time.Hour))
			frac := float64(at.Sub(t0.Add(time.Duration(d)*24*time.Hour))) / float64(24*time.Hour)
			p = prices[d] * math.Pow(prices[d+1]/prices[d], frac)
			if osc[d] && (at.Unix()/300)%2 == 1 {
				p *= 1.01
			}
		}
		bars = append(bars, flatBar(at, p, 0.0005, 1000))
	}
	return bars
}

// TestRegimes covers labelling buckets and attributing the run's P&L.
func TestRegimes(t *testing.T) {
	prices := []float64{100, 110, 99, 99, 99}
	osc := []bool{false, false, false, true}
	reader := oneMarket(dayPath(prices, osc))
	ticks := 4 * 2880
	cfg := withScript(testConfig([]string{"BTC/EUR"}, ticks), randomPlan(3, []string{"BTC/EUR"}, 36, ticks-5, 60), nil)
	res := mustRun(t, reader, cfg)
	rep, err := BuildReport(reader, res)
	if err != nil {
		t.Fatal(err)
	}
	spec := RegimeSpec{Bucket: BucketDay, BullAbovePct: 5, BearBelowPct: -5}

	t.Run("GIVEN one continuous run WHEN segmented by day THEN trend and volatility labels follow the market and are deterministic", func(t *testing.T) {
		a, err := AnalyseRegimes(rep, spec)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := AnalyseRegimes(rep, spec)
		ja, _ := json.Marshal(a)
		jb, _ := json.Marshal(b)
		if !bytes.Equal(ja, jb) {
			t.Fatal("regime labels are not deterministic")
		}
		want := []string{TrendBull, TrendBear, TrendSideways, TrendSideways}
		if len(a.Buckets) != 4 {
			t.Fatalf("%d buckets", len(a.Buckets))
		}
		for i, bk := range a.Buckets {
			if bk.Trend != want[i] {
				t.Fatalf("bucket %d trend %s, want %s (market %v)", i, bk.Trend, want[i], bk.MarketReturnPct)
			}
		}
		if a.Buckets[2].Volatility != VolLow || a.Buckets[3].Volatility != VolHigh {
			t.Fatalf("volatility labels %s / %s, want low for the flat day and high for the oscillating one", a.Buckets[2].Volatility, a.Buckets[3].Volatility)
		}
		counted := 0
		for _, c := range a.Cells {
			counted += c.Buckets
			if c.Thin != (c.Buckets < MinBucketsPerCell) {
				t.Fatalf("cell %+v thin flag", c)
			}
		}
		if counted != 4 {
			t.Fatalf("cells count %d buckets", counted)
		}
	})

	t.Run("GIVEN one continuous run WHEN segmented THEN per-bucket P&L and round trips add up to the run's totals", func(t *testing.T) {
		a, _ := AnalyseRegimes(rep, spec)
		pnl, trips := 0.0, 0
		for _, bk := range a.Buckets {
			pnl += bk.PnL
			trips += bk.RoundTrips
		}
		if !near(pnl, rep.Metrics.NetPnL, 1e-9) || trips != rep.Metrics.RoundTrips || trips == 0 {
			t.Fatalf("buckets sum to %v / %d trips, run %v / %d", pnl, trips, rep.Metrics.NetPnL, rep.Metrics.RoundTrips)
		}
		cellPnL := 0.0
		for _, c := range a.Cells {
			cellPnL += c.PnL
		}
		if !near(cellPnL, rep.Metrics.NetPnL, 1e-9) {
			t.Fatalf("cells sum to %v, run %v", cellPnL, rep.Metrics.NetPnL)
		}
	})
}

// TestRobustnessFlowThrough covers the H4 inputs in the multi-run commands.
func TestRobustnessFlowThrough(t *testing.T) {
	t.Run("GIVEN the default sensitivity presets WHEN listed THEN they include empty candles off and on, and applying it sets the run option", func(t *testing.T) {
		found := false
		for _, d := range DefaultDimensions() {
			if d.Name == DimEmptyCandles && strings.Join(d.Values, ",") == "false,true" {
				found = true
			}
		}
		if !found {
			t.Fatalf("presets %+v lack %s", DefaultDimensions(), DimEmptyCandles)
		}
		cfg := testConfig([]string{"BTC/EUR"}, 79)
		if err := ApplyDimension(&cfg, DimEmptyCandles, "true"); err != nil || !cfg.FillEmptyCandles {
			t.Fatalf("apply: %v, fill %v", err, cfg.FillEmptyCandles)
		}
		if fingerprint(cfg) == fingerprint(testConfig([]string{"BTC/EUR"}, 79)) {
			t.Fatal("the empty-candle option must change the run's fingerprint")
		}
	})

	t.Run("GIVEN a variation of FEE_AND_SLIPPAGE WHEN applied THEN the run keeps it as an explicit setting", func(t *testing.T) {
		cfg := testConfig([]string{"BTC/EUR"}, 79)
		if err := ApplyDimension(&cfg, "FEE_AND_SLIPPAGE", "0.02"); err != nil {
			t.Fatal(err)
		}
		res := mustRun(t, oneMarket(flatSeries(80, 100)), cfg)
		if !res.RiskFee.Explicit || res.RiskFee.FeeAndSlippage != 0.02 {
			t.Fatalf("risk fee %+v, want the varied 0.02 kept", res.RiskFee)
		}
	})

	t.Run("GIVEN a report whose sign depends on stale positions WHEN it becomes a multi-run row THEN the row says so and carries the worst case", func(t *testing.T) {
		ticks := 2880 + 200
		reader, s := staleScenario(ticks)
		cfg := testConfig([]string{"AAA/EUR", "BBB/EUR"}, ticks)
		cfg.DisableLearner = true
		rep, err := BuildReport(reader, mustRun(t, reader, withScript(cfg, s, nil)))
		if err != nil {
			t.Fatal(err)
		}
		row := NewRunRow("x", rep)
		if !row.StaleDecidesSign || row.NetPnLWorstCase >= 0 || row.StalePositions != 1 {
			t.Fatalf("row %+v, want the stale sign flagged with a negative worst case", row)
		}
	})
}
