package backtest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/histdata"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy"
)

// randomPlan plans random long/short ideas on markets between ticks from and to.
func randomPlan(seed uint64, markets []string, from, to, every int) *scripted {
	rng := rand.New(rand.NewPCG(seed, 99))
	s := &scripted{}
	for i := from; i < to; i++ {
		for _, m := range markets {
			if rng.IntN(every) != 0 {
				continue
			}
			d := domain.Long
			if rng.IntN(2) == 0 {
				d = domain.Short
			}
			s.at(tick(i), m, d)
		}
	}
	return s
}

// twoMarkets returns a reader with two seeded random-walk markets.
func twoMarkets(n int) memReader {
	return memReader{bars: map[string][]histdata.Bar{
		"BTC/EUR": randomWalkBars(1, n, t0, 50000, 0.002),
		"ETH/EUR": randomWalkBars(2, n, t0, 3000, 0.003),
	}}
}

// TestReplayAccounting covers that the replay's money is the paper engine's money.
func TestReplayAccounting(t *testing.T) {
	t.Run("GIVEN a round trip at an unchanged price WHEN it completes THEN equity falls by exactly the fees plus the spread paid", func(t *testing.T) {
		cfg := testConfig([]string{"BTC/EUR"}, 79)
		cfg.Exchange.FillModel = FillFixed
		cfg.Exchange.FixedHalfSpreadBps = 10
		s := (&scripted{}).at(tick(entryTick), "BTC/EUR", domain.Long).at(tick(exitTick), "BTC/EUR", domain.Short)
		res := mustRun(t, oneMarket(flatSeries(80, 100)), withScript(cfg, s, nil))
		fees, spread := 0.0, 0.0
		for _, o := range ordersWhere(res, func(o OrderLogEntry) bool { return o.Status == StatusFilled }) {
			if o.LatencyDrift != 0 {
				t.Fatalf("price did not move, yet drift is %v", o.LatencyDrift)
			}
			fees += o.Fee
			spread += o.FilledQty * (o.SpreadCost + o.ImpactCost + o.RoundingCost)
		}
		if len(res.Trades) != 1 || fees <= 0 || spread <= 0 {
			t.Fatalf("want one trade with fees and spread, got trades=%d fees=%v spread=%v", len(res.Trades), fees, spread)
		}
		if change := res.EndingEquity - res.StartingCapital; !near(change, -fees-spread, 1e-9) {
			t.Fatalf("equity changed by %v, want -(fees %v + spread %v)", change, fees, spread)
		}
		if !near(res.Trades[0].PnL, -fees-spread, 1e-9) {
			t.Fatalf("trade pnl %v, want %v", res.Trades[0].PnL, -fees-spread)
		}
	})

	t.Run("GIVEN trading on two markets WHEN every tick is observed THEN equity equals cash plus positions marked at the tick price", func(t *testing.T) {
		cfg := testConfig([]string{"BTC/EUR", "ETH/EUR"}, 899)
		cfg.Settings.StartingCapital = 5000
		var obs []TickObservation
		res := mustRun(t, twoMarkets(900), withScript(cfg, randomPlan(5, cfg.Markets, 36, 890, 15), &obs))
		curve := map[time.Time]EquityPoint{}
		for _, p := range res.EquityCurve {
			curve[p.Time] = p
		}
		multi := false
		for _, o := range obs {
			want := o.Cash
			for _, p := range o.Positions {
				want += p.Size * o.Prices[p.Market]
			}
			if !near(o.Equity, want, 1e-9) {
				t.Fatalf("at %v equity %v, want cash+positions %v", o.Time, o.Equity, want)
			}
			if p, ok := curve[o.Time]; !ok || p.Equity != o.Equity || p.Cash != o.Cash {
				t.Fatalf("equity curve point at %v is %+v, want equity %v cash %v", o.Time, p, o.Equity, o.Cash)
			}
			multi = multi || len(o.Positions) > 1
		}
		if !multi {
			t.Fatal("the scenario never held two positions at once")
		}
	})
}

// TestReplayTime covers simulated time in the risk engine and the learner.
func TestReplayTime(t *testing.T) {
	t.Run("GIVEN a stop-loss that hits the daily loss limit WHEN simulated UTC midnight passes THEN new entries are allowed again", func(t *testing.T) {
		start := time.Date(2026, 1, 4, 22, 0, 0, 0, time.UTC)
		at := func(i int) time.Time { return start.Add(time.Duration(i) * histdata.BarInterval) }
		bars := make([]histdata.Bar, 300)
		for i := range bars {
			price := 100.0
			if i >= 125 {
				price = 90
			}
			bars[i] = flatBar(at(i), price, 0.001, 1000)
		}
		cfg := testConfig([]string{"BTC/EUR"}, 0)
		cfg.Start, cfg.End = start, at(299)
		cfg.Settings.MaxDailyLossPercent = 1
		s := (&scripted{}).
			at(at(120), "BTC/EUR", domain.Long). // 23:00, stopped out at 23:03
			at(at(180), "BTC/EUR", domain.Long). // 23:30, same UTC day
			at(at(260), "BTC/EUR", domain.Long)  // 00:10, next UTC day
		res := mustRun(t, oneMarket(bars), withScript(cfg, s, nil))

		byTime := map[time.Time]Decision{}
		for _, d := range res.Decisions {
			byTime[d.Time] = d
		}
		if d := byTime[at(120)]; d.Outcome != OutcomeSubmitted {
			t.Fatalf("first entry %+v, want submitted", d)
		}
		if d := byTime[at(180)]; d.Outcome != OutcomeRiskRejected || !strings.Contains(d.RiskReason, "Daily loss limit") {
			t.Fatalf("same-day entry %+v, want a daily loss rejection", d)
		}
		if d := byTime[at(260)]; d.Outcome != OutcomeSubmitted {
			t.Fatalf("next-day entry %+v, want submitted after the UTC reset", d)
		}
	})

	t.Run("GIVEN trades closing over time WHEN each tick is evaluated THEN the learner has seen exactly the trades closed by that tick", func(t *testing.T) {
		cfg := testConfig([]string{"BTC/EUR", "ETH/EUR"}, 899)
		cfg.Settings.StartingCapital = 5000
		var obs []TickObservation
		res := mustRun(t, twoMarkets(900), withScript(cfg, randomPlan(9, cfg.Markets, 36, 890, 10), &obs))
		if obs[0].LearnedOutcomes != 0 {
			t.Fatalf("the learner must start empty, saw %d outcomes", obs[0].LearnedOutcomes)
		}
		for _, o := range obs {
			closed := len(ordersWhere(res, func(x OrderLogEntry) bool {
				return x.Side == SideSell && x.Status == StatusFilled && x.Reason != ReasonEndOfRun && !x.FillTime.After(o.Time)
			}))
			if o.LearnedOutcomes != closed {
				t.Fatalf("at %v the learner saw %d outcomes, %d trades had closed", o.Time, o.LearnedOutcomes, closed)
			}
		}
		if obs[len(obs)-1].LearnedOutcomes < 5 {
			t.Fatalf("too few closed trades (%d) to exercise the learner", obs[len(obs)-1].LearnedOutcomes)
		}
	})

	t.Run("GIVEN the learner is switched off WHEN trades close THEN it learns nothing and confidence is never adjusted", func(t *testing.T) {
		cfg := testConfig([]string{"BTC/EUR", "ETH/EUR"}, 899)
		cfg.DisableLearner = true
		var obs []TickObservation
		res := mustRun(t, twoMarkets(900), withScript(cfg, randomPlan(9, cfg.Markets, 36, 890, 10), &obs))
		for _, o := range obs {
			if o.LearnedOutcomes != 0 {
				t.Fatalf("learner disabled but saw %d outcomes", o.LearnedOutcomes)
			}
		}
		for _, d := range res.Decisions {
			if d.Confidence != d.RawConfidence {
				t.Fatalf("confidence adjusted with the learner off: %+v", d)
			}
		}
	})
}

// TestReplayDeterminism covers byte-identical results across runs.
func TestReplayDeterminism(t *testing.T) {
	t.Run("GIVEN the same data, settings and seed WHEN replayed twice THEN the result files are byte-identical and ids are sequence numbers", func(t *testing.T) {
		dir := t.TempDir()
		var files [2][]byte
		for i := range files {
			cfg := testConfig([]string{"BTC/EUR", "ETH/EUR"}, 899)
			cfg.Settings.StartingCapital = 5000
			res := mustRun(t, twoMarkets(900), withScript(cfg, randomPlan(5, cfg.Markets, 36, 890, 12), nil))
			path := filepath.Join(dir, "run", "result.json")
			if err := WriteResult(res, path); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			files[i] = raw
			idPattern := regexp.MustCompile(`^[a-z]+-\d{6}$`)
			for _, o := range res.Orders {
				if !idPattern.MatchString(o.ID) || (o.PositionID != "" && !idPattern.MatchString(o.PositionID)) {
					t.Fatalf("non-sequential id in %+v", o)
				}
			}
			if len(res.Trades) < 3 {
				t.Fatalf("the scenario produced too few trades (%d)", len(res.Trades))
			}
		}
		if !bytes.Equal(files[0], files[1]) {
			t.Fatal("two identical runs produced different result files")
		}
	})
}

// TestReplayStrategies covers which strategies a backtest can run.
func TestReplayStrategies(t *testing.T) {
	for _, id := range []string{strategy.BasicAndLLM1hID, strategy.LLMOnlyID, "llm_only"} { // llm_only: legacy id
		t.Run("GIVEN the LLM strategy "+id+" WHEN a backtest is asked for THEN it refuses with a clear error and no result", func(t *testing.T) {
			cfg := testConfig([]string{"BTC/EUR"}, 79)
			cfg.StrategyID = id
			res, err := Run(context.Background(), oneMarket(flatSeries(80, 100)), cfg)
			if !errors.Is(err, ErrLLMStrategy) || res != nil {
				t.Fatalf("want ErrLLMStrategy and no result, got %v / %v", err, res)
			}
			if !strings.Contains(err.Error(), "news") || !strings.Contains(err.Error(), "reproducible") {
				t.Fatalf("the error must say why: %v", err)
			}
		})
	}

	t.Run("GIVEN an unknown strategy WHEN a backtest is asked for THEN it fails", func(t *testing.T) {
		cfg := testConfig([]string{"BTC/EUR"}, 79)
		cfg.StrategyID = "nope"
		if _, err := Run(context.Background(), oneMarket(flatSeries(80, 100)), cfg); err == nil {
			t.Fatal("want an error for an unknown strategy")
		}
	})
}

// observedJSON marshals the observations at or before limit for comparison.
func observedJSON(t *testing.T, obs []TickObservation, limit time.Time) []byte {
	t.Helper()
	var keep []TickObservation
	for _, o := range obs {
		if !o.Time.After(limit) {
			keep = append(keep, o)
		}
	}
	raw, err := json.Marshal(keep)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestReplayNoLookAhead covers that data after a time cannot affect anything before it.
func TestReplayNoLookAhead(t *testing.T) {
	t.Run("GIVEN two series identical up to T WHEN both are replayed with the real strategy THEN every input, decision and fill up to T is identical", func(t *testing.T) {
		const n, cut = warmBars + 4500, warmBars + 3000
		base := randomWalkBars(21, n, t0, 100, 0.004)
		alt := append([]histdata.Bar(nil), base...)
		for i := range alt {
			if !alt[i].Start.Before(tick(cut)) {
				alt[i].Open *= 1.5
				alt[i].High *= 1.6
				alt[i].Low *= 1.4
				alt[i].Close *= 1.5
				alt[i].BuyNotional *= 1.5
				alt[i].SellNotional *= 1.5
				alt[i].Volume *= 3
			}
		}
		limit := tick(cut)
		var results [2]*Result
		var observations [2][]TickObservation
		for i, bars := range [][]histdata.Bar{base, alt} {
			cfg := testConfig([]string{"BTC/EUR"}, n-1)
			cfg.observe = func(o TickObservation) {
				observations[i] = append(observations[i], copyObservation(o))
			}
			results[i] = mustRun(t, oneMarket(bars), cfg)
		}
		if !bytes.Equal(observedJSON(t, observations[0], limit), observedJSON(t, observations[1], limit)) {
			t.Fatal("strategy inputs before T differ: data after T leaked into them")
		}
		for i := range results {
			results[i].Decisions = decisionsUpTo(results[i].Decisions, limit)
			results[i].Orders = ordersWhere(results[i], func(o OrderLogEntry) bool { return !o.FillTime.After(limit) })
		}
		a, _ := json.Marshal([]any{results[0].Decisions, results[0].Orders})
		b, _ := json.Marshal([]any{results[1].Decisions, results[1].Orders})
		if !bytes.Equal(a, b) {
			t.Fatal("decisions or fills up to T differ")
		}
		if len(results[0].Decisions) == 0 {
			t.Fatal("the strategy made no decision before T, so the test proves nothing")
		}
	})

	t.Run("GIVEN scripted orders around T WHEN the series differ after T THEN fills up to T are identical", func(t *testing.T) {
		const n, cut = 600, 300
		base := randomWalkBars(4, n, t0, 100, 0.002)
		alt := append([]histdata.Bar(nil), base...)
		for i := range alt {
			if !alt[i].Start.Before(tick(cut)) {
				alt[i] = flatBar(alt[i].Start, 10, 0.01, 1)
			}
		}
		var logs [2][]byte
		for i, bars := range [][]histdata.Bar{base, alt} {
			res := mustRun(t, oneMarket(bars), withScript(testConfig([]string{"BTC/EUR"}, n-1), randomPlan(8, []string{"BTC/EUR"}, 36, n-5, 6), nil))
			raw, _ := json.Marshal(ordersWhere(res, func(o OrderLogEntry) bool { return !o.FillTime.After(tick(cut)) }))
			logs[i] = raw
		}
		if !bytes.Equal(logs[0], logs[1]) {
			t.Fatal("fills up to T depend on data after T")
		}
	})
}

// scaleVolume multiplies the traded amounts of the bars in [from, to) by k.
func scaleVolume(bars []histdata.Bar, from, to time.Time, k float64) {
	for i := range bars {
		if !bars[i].Start.Before(from) && bars[i].Start.Before(to) {
			bars[i].Volume *= k
			bars[i].BuyVolume *= k
			bars[i].BuyNotional *= k
			bars[i].SellVolume *= k
			bars[i].SellNotional *= k
		}
	}
}

// TestReplayUniverse covers point-in-time market selection.
func TestReplayUniverse(t *testing.T) {
	t.Run("GIVEN A is the biggest market before the start and B the biggest over the following day WHEN the universe is re-selected THEN the start picks A, the next day picks B, and the dropped A blocks new entries", func(t *testing.T) {
		const ticks = 2*2880 + 100
		from := t0.Add(-PreRoll)
		n := int(PreRoll/histdata.BarInterval) + ticks
		a := randomWalkBars(41, n, from, 100, 0.002)
		b := randomWalkBars(42, n, from, 100, 0.002)
		scaleVolume(a, from, t0, 3)                                 // A trades 3x more before the start
		scaleVolume(b, t0, t0.Add(ticks*histdata.BarInterval), 100) // B trades 100x more from the start on
		reader := memReader{bars: map[string][]histdata.Bar{"AAA/EUR": a, "BBB/EUR": b, "CCC/USD": a}}
		cfg := testConfig(nil, ticks)
		cfg.Universe = &UniverseRule{TopN: 1, Every: "24h"}
		day := 2880
		s := (&scripted{}).at(tick(100), "AAA/EUR", domain.Long).at(tick(day+50), "AAA/EUR", domain.Long)
		res := mustRun(t, reader, withScript(cfg, s, nil))

		if len(res.UniverseSelections) != 3 {
			t.Fatalf("selections %+v, want one at the start and one per day", res.UniverseSelections)
		}
		// A selection that could see the following 24 hours would pick B at the start.
		first, second := res.UniverseSelections[0], res.UniverseSelections[1]
		if !first.Time.Equal(t0) || len(first.Markets) != 1 || first.Markets[0] != "AAA/EUR" {
			t.Fatalf("first selection %+v, want AAA/EUR from the day before %v only", first, t0)
		}
		if !second.Time.Equal(t0.Add(24*time.Hour)) || len(second.Markets) != 1 || second.Markets[0] != "BBB/EUR" {
			t.Fatalf("second selection %+v, want BBB/EUR picked at %v", second, t0.Add(24*time.Hour))
		}
		for _, sel := range res.UniverseSelections {
			for _, m := range sel.Markets {
				if !strings.HasSuffix(m, "/EUR") {
					t.Fatalf("a market outside the quote currency was selected: %v", sel)
				}
			}
		}
		var late *Decision
		for i := range res.Decisions {
			if res.Decisions[i].Time.Equal(tick(day + 50)) {
				late = &res.Decisions[i]
			}
		}
		if late == nil || late.Outcome == OutcomeSubmitted {
			t.Fatalf("a long on a market dropped from the universe was handled as %+v", late)
		}
	})

	t.Run("GIVEN a market out of the universe for a day while its price moves from 100 to 150 WHEN it re-enters THEN its price history is the continuous last 200 ticks, not old prices spliced to new ones", func(t *testing.T) {
		const ticks = 3*2880 + 50
		from := t0.Add(-PreRoll)
		n := int(PreRoll/histdata.BarInterval) + ticks
		day := 24 * time.Hour
		a := make([]histdata.Bar, n)
		for i := range a {
			start := from.Add(time.Duration(i) * histdata.BarInterval)
			price := 100.0
			if !start.Before(t0.Add(day)) { // rises while A is out of the universe
				price = 100 + 50*math.Min(1, float64(start.Sub(t0.Add(day)))/float64(day))
			}
			if i%120 < 60 { // a 1 % swing every hour, so A is not taken for a pegged pair
				price *= 1.01
			}
			a[i] = flatBar(start, price, 0.001, 10)
		}
		b := randomWalkBars(43, n, from, 100, 0.002)
		scaleVolume(a, from, t0, 1000)                   // A is the biggest before the start
		scaleVolume(b, t0, t0.Add(day), 1000)            // B during the first day
		scaleVolume(a, t0.Add(day), t0.Add(2*day), 1000) // A again during the second
		reader := memReader{bars: map[string][]histdata.Bar{"AAA/EUR": a, "BBB/EUR": b}}
		cfg := testConfig(nil, ticks)
		cfg.Universe = &UniverseRule{TopN: 1, Every: "24h"}
		reentry := t0.Add(2 * day).Add(histdata.BarInterval)
		var seen *strategy.MarketData
		cfg.strategy = &scripted{}
		cfg.observe = func(o TickObservation) {
			if o.Time.Equal(reentry) {
				if md, ok := o.MarketData["AAA/EUR"]; ok {
					seen = &md
				}
			}
		}
		res := mustRun(t, reader, cfg)
		if got := res.UniverseSelections; len(got) < 3 || got[0].Markets[0] != "AAA/EUR" || got[1].Markets[0] != "BBB/EUR" || got[2].Markets[0] != "AAA/EUR" {
			t.Fatalf("selections %+v, want AAA, BBB, AAA", got)
		}
		if seen == nil {
			t.Fatal("AAA/EUR had no strategy input on re-entry")
		}
		hist := seen.PriceHistory
		if len(hist) != 200 {
			t.Fatalf("history of %d ticks on re-entry, want the full 200", len(hist))
		}
		for i := range hist {
			at := reentry.Add(-time.Duration(len(hist)-1-i) * histdata.BarInterval)
			want, _ := naiveLastPrice(a, at)
			if hist[i] != want {
				t.Fatalf("history[%d] = %v, want the price %v at %v", i, hist[i], want, at)
			}
		}
		if seen.PreviousPrice < 149 {
			t.Fatalf("previous price %v on re-entry, want the recent ~150, not the stale 100", seen.PreviousPrice)
		}
	})

	t.Run("GIVEN several markets in a universe WHEN one market's data changes after T THEN no decision, selection or fill at or before T changes", func(t *testing.T) {
		const n, cut = warmBars + 4000, warmBars + 2400
		markets := []string{"AAA/EUR", "BBB/EUR", "CCC/EUR"}
		build := func(mutate bool) memReader {
			r := memReader{bars: map[string][]histdata.Bar{}}
			for i, m := range markets {
				bars := randomWalkBars(uint64(60+i), n+int(PreRoll/histdata.BarInterval), t0.Add(-PreRoll), 100, 0.004)
				if mutate && m == "CCC/EUR" {
					for j := range bars {
						if !bars[j].Start.Before(tick(cut)) {
							bars[j] = flatBar(bars[j].Start, bars[j].Close*1.5, 0.001, bars[j].Volume*1000)
						}
					}
				}
				r.bars[m] = bars
			}
			return r
		}
		var out [2][]byte
		for i, mutate := range []bool{false, true} {
			cfg := testConfig(nil, n-1)
			cfg.Universe = &UniverseRule{TopN: 2, Every: "6h"}
			res := mustRun(t, build(mutate), cfg)
			var sels []UniverseSelection
			for _, s := range res.UniverseSelections {
				if !s.Time.After(tick(cut)) {
					sels = append(sels, s)
				}
			}
			decisions := decisionsUpTo(res.Decisions, tick(cut))
			if len(decisions) == 0 {
				t.Fatal("no decision before T: the test proves nothing")
			}
			raw, _ := json.Marshal([]any{sels, decisions, ordersWhere(res, func(o OrderLogEntry) bool { return !o.FillTime.After(tick(cut)) })})
			out[i] = raw
		}
		if !bytes.Equal(out[0], out[1]) {
			t.Fatal("changing one market after T changed selections, decisions or fills at or before T")
		}
	})
}

// decisionsUpTo keeps the decisions made at or before limit.
func decisionsUpTo(ds []Decision, limit time.Time) []Decision {
	var out []Decision
	for _, d := range ds {
		if !d.Time.After(limit) {
			out = append(out, d)
		}
	}
	return out
}

// copyObservation deep-copies an observation's market data through JSON so
// later ticks cannot change it.
func copyObservation(o TickObservation) TickObservation {
	raw, _ := json.Marshal(o.MarketData)
	o.MarketData = nil
	_ = json.Unmarshal(raw, &o.MarketData)
	o.Positions = append([]domain.PositionRecord(nil), o.Positions...)
	return o
}

// TestReplayPlumbing covers end-to-end trading with the real strategy.
func TestReplayPlumbing(t *testing.T) {
	// A gentle trend: in a steep one the hourly RSI and Bollinger position
	// stay pinned high and the basic hard filters block every long.
	t.Run("GIVEN a gently trending series WHEN the real basic strategy is replayed THEN it trades through the real risk and paper engines and pays fees", func(t *testing.T) {
		res := mustRun(t, oneMarket(trendBars(warmBars+6000, 0.00004)), testConfig([]string{"BTC/EUR"}, warmBars+5999))
		buys := ordersWhere(res, func(o OrderLogEntry) bool { return o.Side == SideBuy && o.Status == StatusFilled })
		exits := ordersWhere(res, func(o OrderLogEntry) bool {
			return o.Side == SideSell && o.Status == StatusFilled && o.Reason != ReasonEndOfRun
		})
		if len(buys) == 0 || len(exits) == 0 || res.FeesPaid <= 0 {
			t.Fatalf("want filled buys and exits with fees, got %d buys %d exits fees %v (decisions %v)", len(buys), len(exits), res.FeesPaid, res.DecisionCounts)
		}
		for _, tr := range res.Trades {
			if tr.EntryFee <= 0 || tr.ExitFee <= 0 {
				t.Fatalf("trade without fees: %+v", tr)
			}
		}
	})

	t.Run("GIVEN random-walk markets and realistic costs WHEN replayed over many seeds THEN the average result is not a profit", func(t *testing.T) {
		const seeds, n = 6, warmBars + 12000
		basicTotal, scriptTotal, basicTrades := 0.0, 0.0, 0
		for seed := uint64(1); seed <= seeds; seed++ {
			bars := randomWalkBars(100+seed, n, t0, 100, 0.0025)

			basic := mustRun(t, oneMarket(bars), testConfig([]string{"BTC/EUR"}, n-1))
			basicTotal += basic.EndingEquity - basic.StartingCapital
			basicTrades += len(basic.Trades)

			scriptRes := mustRun(t, oneMarket(bars), withScript(testConfig([]string{"BTC/EUR"}, n-1), randomPlan(seed, []string{"BTC/EUR"}, 36, n-5, 20), nil))
			scriptTotal += scriptRes.EndingEquity - scriptRes.StartingCapital
		}
		if basicTrades == 0 {
			t.Fatal("the basic strategy never traded on the random walks, so the guard proves nothing")
		}
		if basicTotal/seeds > 0 || scriptTotal/seeds >= 0 {
			t.Fatalf("random walks look profitable: basic mean %v (trades %d), random-entry mean %v", basicTotal/seeds, basicTrades, scriptTotal/seeds)
		}
	})
}

// trendBars returns n bars of a noisy uptrend (drift per bar) with one 6 %
// pullback in the middle third.
func trendBars(n int, drift float64) []histdata.Bar {
	bars := randomWalkBars(77, n, t0, 100, 0.0015)
	for i := range bars {
		k := int(bars[i].Start.Sub(t0) / histdata.BarInterval)
		f := math.Exp(float64(k) * math.Log1p(drift))
		if k >= n/2 && k < n/2+200 {
			f *= 0.94
		}
		b := &bars[i]
		b.Open, b.High, b.Low, b.Close = b.Open*f, b.High*f, b.Low*f, b.Close*f
		b.BuyNotional, b.SellNotional = b.BuyNotional*f, b.SellNotional*f
	}
	return bars
}

// TestReplayWarmUp covers the start of a run: a strategy waits only for the
// price ticks it says it needs, as live.
func TestReplayWarmUp(t *testing.T) {
	t.Run("GIVEN a strategy that needs no price ticks or one that needs the full lookback WHEN replayed THEN it is first evaluated on the first tick or once the lookback has filled", func(t *testing.T) {
		for _, c := range []struct{ ticks, first int }{{0, 0}, {strategy.LookbackTicks + 1, strategy.LookbackTicks}} {
			var obs []TickObservation
			s := &scripted{ticks: c.ticks}
			mustRun(t, oneMarket(flatSeries(100, 100)), withScript(testConfig([]string{"BTC/EUR"}, 99), s, &obs))
			first := -1
			for i, o := range obs {
				if o.MarketData != nil {
					first = i
					break
				}
			}
			if first != c.first {
				t.Fatalf("needs %d ticks: first evaluated at tick %d, want %d", c.ticks, first, c.first)
			}
		}
	})
}
