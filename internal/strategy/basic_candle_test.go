package strategy

import (
	"context"
	"log/slog"
	"math"
	"reflect"
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/analysis"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/llm"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy/calc"
)

// hourCandles returns n 1-hour candles whose closes follow step, the last one
// being the forming candle.
func hourCandles(n int, step func(i int) float64) []domain.Candle {
	out := make([]domain.Candle, n)
	for i := range out {
		c := step(i)
		out[i] = domain.Candle{T: int64(i) * 3600, O: c, H: c * 1.001, L: c * 0.999, C: c, V: 1}
	}
	return out
}

// flat is an unchanging price.
func flat(int) float64 { return 100 }

// candleMarket returns market data with 1-hour and 4-hour candles and a live price.
func candleMarket(price float64, hour, four []domain.Candle) MarketData {
	return MarketData{Price: price, PreviousPrice: price, HourCandles: hour, TrendCandles: four}
}

// longSignal is an entry signal as the shared gates would return it.
func longSignal(dir domain.Direction) *indicatorSignal {
	return &indicatorSignal{
		Momentum: 0.02, Direction: dir, ATRPct: 1.5,
		Votes:      calc.VoteSummary{Votes: 3, Available: 8, Supporting: 6, Opposing: 3, Values: map[string]any{}},
		Confidence: calc.ConfidenceBreakdown{Confidence: 0.8},
	}
}

// stubRead replaces the strategy's rules with a fixed reading and counts the
// reads; the view each read saw is kept in *seen when seen is not nil.
func stubRead(core *basicCore, r basicReading, calls *int, seen *MarketData) {
	core.read = func(_ *slog.Logger, _ string, view MarketData) basicReading {
		*calls++
		if seen != nil {
			*seen = view
		}
		out := r
		out.view = view
		return out
	}
}

// heldIn returns a portfolio holding size of market (negative: a paper short).
func heldIn(market string, size float64) Portfolio {
	return Portfolio{OpenPositions: []domain.PositionRecord{{Market: market, Size: size, AvgPrice: 100}}}
}

// closedOf returns the finished candles (all but the forming last one).
func closedOf(c []domain.Candle) []domain.Candle { return c[:len(c)-1] }

// closesOf returns the closes of candles.
func closesOf(c []domain.Candle) []float64 {
	out := make([]float64, len(c))
	for i, x := range c {
		out[i] = x.C
	}
	return out
}

// TestBasicCandleStrategies covers basic_strategy_1h and basic_strategy_4h:
// the basic indicator rules read on finished 1-hour or 4-hour candles.
func TestBasicCandleStrategies(t *testing.T) {
	ctx := context.Background()
	s := testSettings()
	enoughHours := hourCandles(BasicMinClosedCandles+30, flat)
	enoughFours := trendCandles(BasicMinClosedCandles+30, flat)

	t.Run("GIVEN the four basic strategies WHEN created THEN they have their own ids and only the combined ones use the LLM", func(t *testing.T) {
		cases := []struct {
			s   Strategy
			id  string
			llm bool
		}{
			{NewBasic1h(s), "basic_strategy_1h", false}, {NewBasic4h(s), "basic_strategy_4h", false},
			{NewBasicAndLLM1h(s), "basic_and_llm_strategy_1h", true}, {NewBasicAndLLM4h(s), "basic_and_llm_strategy_4h", true},
		}
		for _, c := range cases {
			if c.s.ID() != c.id || c.s.UsesLLM() != c.llm || c.s.RequiredPriceTicks() != 0 {
				t.Fatalf("%s: id %q, uses LLM %v, ticks %d", c.id, c.s.ID(), c.s.UsesLLM(), c.s.RequiredPriceTicks())
			}
		}
	})

	t.Run("GIVEN 1-hour and 4-hour candles WHEN the 1h strategy reads a market THEN the rules see the finished 1-hour candles with the 4-hour candles as the higher timeframe", func(t *testing.T) {
		hours := hourCandles(BasicMinClosedCandles+30, func(i int) float64 { return 100 + float64(i%7) })
		fours := trendCandles(60, func(i int) float64 { return 90 + float64(i%5) })
		b := NewBasic1h(s)
		var calls int
		var seen MarketData
		stubRead(&b.basicCore, basicReading{}, &calls, &seen)
		b.Evaluate(ctx, single("SOL/EUR", candleMarket(123, hours, fours)))

		closed := closedOf(hours)
		if calls != 1 || !reflect.DeepEqual(seen.PriceHistory, closesOf(closed)) || seen.Price != closed[len(closed)-1].C {
			t.Fatalf("calls %d, history %v, price %v", calls, seen.PriceHistory, seen.Price)
		}
		want := analysis.ComputeAll(closesOf(closed), 3600, closed)
		want["price_changes"] = analysis.CandleChanges(closesOf(closed)) // the 5m/15m changes mean nothing on hour bars
		if !reflect.DeepEqual(seen.Indicators, want) {
			t.Fatalf("indicators %+v not computed on the finished 1-hour candles", seen.Indicators)
		}
		four := closedOf(fours)
		if !reflect.DeepEqual(seen.HigherTimeframe, analysis.ComputeAll(closesOf(four), 14400, four)) {
			t.Fatalf("higher timeframe %+v not computed on the finished 4-hour candles", seen.HigherTimeframe)
		}
	})

	t.Run("GIVEN 4-hour candles WHEN the 4h strategy reads a market THEN the rules see the finished 4-hour candles with whole UTC days as the higher timeframe", func(t *testing.T) {
		// 30 days and 2 candles of 4-hour candles: the last day is unfinished.
		fours := trendCandles(30*6+3, func(i int) float64 { return 100 + float64(i%9) })
		b := NewBasic4h(s)
		var calls int
		var seen MarketData
		stubRead(&b.basicCore, basicReading{}, &calls, &seen)
		b.Evaluate(ctx, single("SOL/EUR", candleMarket(123, nil, fours)))

		closed := closedOf(fours)
		if !reflect.DeepEqual(seen.PriceHistory, closesOf(closed)) {
			t.Fatalf("history %v", seen.PriceHistory)
		}
		var days []domain.Candle
		for d := 0; d < 30; d++ {
			day := closed[d*6 : d*6+6]
			c := domain.Candle{T: day[0].T, O: day[0].O, H: day[0].H, L: day[0].L, C: day[5].C}
			for _, x := range day {
				c.H, c.L, c.V = max(c.H, x.H), min(c.L, x.L), c.V+x.V
			}
			days = append(days, c)
		}
		if !reflect.DeepEqual(seen.HigherTimeframe, analysis.ComputeAll(closesOf(days), 86400, days)) {
			t.Fatalf("higher timeframe %+v not computed on the 30 whole days", seen.HigherTimeframe)
		}
	})

	t.Run("GIVEN too few finished candles WHEN evaluated THEN the rules are not read and no idea is emitted", func(t *testing.T) {
		b := NewBasic1h(s)
		var calls int
		stubRead(&b.basicCore, basicReading{sig: longSignal(domain.Long)}, &calls, nil)
		short := hourCandles(BasicMinClosedCandles, flat) // one forming, so one short of enough
		if ideas := b.Evaluate(ctx, single("SOL/EUR", candleMarket(100, short, enoughFours))); len(ideas) != 0 || calls != 0 {
			t.Fatalf("ideas %+v, reads %d", ideas, calls)
		}
	})

	t.Run("GIVEN the same finished candles on later ticks WHEN evaluated THEN the rules are read once per candle", func(t *testing.T) {
		b := NewBasic1h(s)
		var calls int
		stubRead(&b.basicCore, basicReading{}, &calls, nil)
		b.Evaluate(ctx, single("SOL/EUR", candleMarket(100, enoughHours, enoughFours)))
		b.Evaluate(ctx, single("SOL/EUR", candleMarket(101, enoughHours, enoughFours)))
		if calls != 1 {
			t.Fatalf("read %d times on one candle, want 1", calls)
		}
		b.Evaluate(ctx, single("SOL/EUR", candleMarket(101, hourCandles(BasicMinClosedCandles+31, flat), enoughFours)))
		if calls != 2 {
			t.Fatalf("read %d times after a new candle, want 2", calls)
		}
	})

	// evalWith runs the 1h strategy with a fixed reading for SOL/EUR at price 105.
	evalWith := func(b *BasicStrategy, p Portfolio, hours []domain.Candle) []domain.TradeIdea {
		in := single("SOL/EUR", candleMarket(105, hours, enoughFours))
		in.Portfolio = p
		return b.Evaluate(ctx, in)
	}
	withReading := func(r basicReading) *BasicStrategy {
		b := NewBasic1h(s)
		var calls int
		stubRead(&b.basicCore, r, &calls, nil)
		return b
	}

	t.Run("GIVEN a long signal and nothing held WHEN evaluated THEN a long entry is emitted at the live price", func(t *testing.T) {
		ideas := evalWith(withReading(basicReading{sig: longSignal(domain.Long)}), Portfolio{}, enoughHours)
		if len(ideas) != 1 || ideas[0].Direction != domain.Long || ideas[0].Exit || ideas[0].StrategyID != Basic1hID {
			t.Fatalf("ideas %+v, want one long entry", ideas)
		}
		if ideas[0].Confidence != 0.8 || ideas[0].SupportingSignals["current_price"] != 105.0 || ideas[0].SupportingSignals["candle_interval"] != "1h" {
			t.Fatalf("idea %+v", ideas[0])
		}
	})

	t.Run("GIVEN a long signal and a long held WHEN evaluated THEN no idea is emitted", func(t *testing.T) {
		if ideas := evalWith(withReading(basicReading{sig: longSignal(domain.Long)}), heldIn("SOL/EUR", 1), enoughHours); len(ideas) != 0 {
			t.Fatalf("ideas %+v", ideas)
		}
	})

	t.Run("GIVEN a held long WHEN the consensus turns bearish THEN a rule exit is emitted that clears the confidence gate", func(t *testing.T) {
		for _, r := range []basicReading{{bearishTurn: true}, {sig: longSignal(domain.Short)}} {
			ideas := evalWith(withReading(r), heldIn("SOL/EUR", 1), enoughHours)
			if len(ideas) != 1 || ideas[0].Direction != domain.Short || !ideas[0].Exit || ideas[0].Confidence < s.MinSignalConfidence {
				t.Fatalf("reading %+v: ideas %+v, want one rule exit", r, ideas)
			}
		}
	})

	t.Run("GIVEN a held long WHEN the consensus has not turned THEN no idea is emitted", func(t *testing.T) {
		if ideas := evalWith(withReading(basicReading{}), heldIn("SOL/EUR", 1), enoughHours); len(ideas) != 0 {
			t.Fatalf("ideas %+v", ideas)
		}
	})

	t.Run("GIVEN an entry signal and nothing held WHEN evaluated on later ticks THEN one entry idea is emitted per candle, long or paper short", func(t *testing.T) {
		for _, dir := range []domain.Direction{domain.Long, domain.Short} {
			b := withReading(basicReading{sig: longSignal(dir)})
			first := evalWith(b, Portfolio{}, enoughHours)
			if len(first) != 1 || first[0].Direction != dir || first[0].Exit {
				t.Fatalf("first tick %+v, want one ordinary %s", first, dir)
			}
			if again := evalWith(b, Portfolio{}, enoughHours); len(again) != 0 {
				t.Fatalf("the same candle emitted the %s again: %+v", dir, again)
			}
			if next := evalWith(b, Portfolio{}, hourCandles(BasicMinClosedCandles+31, flat)); len(next) != 1 {
				t.Fatalf("a new candle emitted %d %s ideas, want 1", len(next), dir)
			}
		}
	})

	t.Run("GIVEN a held long WHEN the consensus stays bearish on later ticks THEN the rule exit is repeated until it is sold", func(t *testing.T) {
		b := withReading(basicReading{bearishTurn: true})
		for i := 0; i < 2; i++ {
			if ideas := evalWith(b, heldIn("SOL/EUR", 1), enoughHours); len(ideas) != 1 || !ideas[0].Exit {
				t.Fatalf("tick %d: %+v, want the rule exit", i, ideas)
			}
		}
	})

	t.Run("GIVEN a held paper short WHEN the consensus turns bullish THEN a long idea covers it; a short signal adds nothing", func(t *testing.T) {
		ideas := evalWith(withReading(basicReading{bullishTurn: true}), heldIn("SOL/EUR", -1), enoughHours)
		if len(ideas) != 1 || ideas[0].Direction != domain.Long || ideas[0].Exit || ideas[0].Confidence < s.MinSignalConfidence {
			t.Fatalf("ideas %+v, want one covering long", ideas)
		}
		if ideas := evalWith(withReading(basicReading{sig: longSignal(domain.Short)}), heldIn("SOL/EUR", -1), enoughHours); len(ideas) != 0 {
			t.Fatalf("ideas %+v, want none while short", ideas)
		}
	})

	t.Run("GIVEN a held long WHEN the real rules read steadily falling 1-hour candles THEN they sell it", func(t *testing.T) {
		falling := hourCandles(BasicMinClosedCandles+30, func(i int) float64 { return 1000 * (1 - 0.01*float64(i)) })
		in := single("SOL/EUR", candleMarket(falling[len(falling)-1].C, falling, enoughFours))
		in.Portfolio = heldIn("SOL/EUR", 1)
		ideas := NewBasic1h(s).Evaluate(ctx, in)
		if len(ideas) != 1 || !ideas[0].Exit {
			t.Fatalf("ideas %+v, want a rule exit", ideas)
		}
	})

	t.Run("GIVEN several markets with entries WHEN evaluated THEN ideas come back sorted by market", func(t *testing.T) {
		b := withReading(basicReading{sig: longSignal(domain.Long)})
		in := Input{MarketData: map[string]MarketData{}}
		for _, m := range []string{"SOL/EUR", "ADA/EUR", "ETH/EUR", "BTC/EUR"} {
			in.MarketData[m] = candleMarket(100, enoughHours, enoughFours)
		}
		ideas := b.Evaluate(ctx, in)
		if len(ideas) != 4 || ideas[0].Market != "ADA/EUR" || ideas[3].Market != "SOL/EUR" {
			t.Fatalf("ideas %+v", ideas)
		}
	})
}

// TestBasicAndLLMCandleStrategies covers basic_and_llm_strategy_1h and _4h:
// the candle rules, then the LLM validates each new entry once per candle.
func TestBasicAndLLMCandleStrategies(t *testing.T) {
	ctx := context.Background()
	s := testSettings()
	hours := hourCandles(BasicMinClosedCandles+30, flat)
	fours := trendCandles(BasicMinClosedCandles+30, flat)

	// run evaluates b on SOL/EUR at price 105 with analyser and portfolio p.
	run := func(b *BasicAndLLMStrategy, an Analyser, p Portfolio, hrs []domain.Candle) []domain.TradeIdea {
		in := single("SOL/EUR", candleMarket(105, hrs, fours))
		in.Analyser, in.Portfolio = an, p
		in.News = []llm.NewsArticle{{ID: "n1", Title: "Solana news"}}
		return b.Evaluate(ctx, in)
	}
	withReading := func(r basicReading) *BasicAndLLMStrategy {
		b := NewBasicAndLLM1h(s)
		var calls int
		stubRead(&b.basicCore, r, &calls, nil)
		return b
	}

	t.Run("GIVEN an entry WHEN the LLM supports it THEN its confidence is scaled and the prompt names the candle timescale and live price", func(t *testing.T) {
		an := scaleAnalyser(1.1)
		ideas := run(withReading(basicReading{sig: longSignal(domain.Long)}), an, Portfolio{}, hours)
		if len(ideas) != 1 || ideas[0].StrategyID != BasicAndLLM1hID || math.Abs(ideas[0].Confidence-0.88) > 1e-9 {
			t.Fatalf("ideas %+v", ideas)
		}
		req := an.signalCalls[0]
		if req.TickSeconds != 3600 || req.CurrentPrice != 105 || len(req.News) != 1 || req.Strategy != nil {
			t.Fatalf("request %+v", req)
		}
	})

	t.Run("GIVEN an entry WHEN later ticks bring the same candle THEN the LLM is asked once; a new candle asks again", func(t *testing.T) {
		an := scaleAnalyser(1.0)
		b := withReading(basicReading{sig: longSignal(domain.Long)})
		run(b, an, Portfolio{}, hours)
		if ideas := run(b, an, Portfolio{}, hours); len(ideas) != 0 || len(an.signalCalls) != 1 {
			t.Fatalf("ideas %d, LLM calls %d; want nothing more on that candle and one call", len(ideas), len(an.signalCalls))
		}
		run(b, an, Portfolio{}, hourCandles(BasicMinClosedCandles+31, flat))
		if len(an.signalCalls) != 2 {
			t.Fatalf("LLM calls %d after a new candle, want 2", len(an.signalCalls))
		}
	})

	t.Run("GIVEN an entry WHEN the LLM vetoes it THEN it is dropped on every tick of that candle", func(t *testing.T) {
		an := scaleAnalyser(0.5)
		b := withReading(basicReading{sig: longSignal(domain.Long)})
		if ideas := run(b, an, Portfolio{}, hours); len(ideas) != 0 {
			t.Fatalf("vetoed entry emitted: %+v", ideas)
		}
		if ideas := run(b, an, Portfolio{}, hours); len(ideas) != 0 || len(an.signalCalls) != 1 {
			t.Fatalf("ideas %+v, calls %d", ideas, len(an.signalCalls))
		}
	})

	t.Run("GIVEN the LLM cannot answer WHEN an entry is evaluated THEN it passes unscaled with empty LLM fields", func(t *testing.T) {
		down := &fakeAnalyser{signal: llm.SignalAnalysis{ConfidenceScale: 1, Reasoning: "unavailable"}}
		b := withReading(basicReading{sig: longSignal(domain.Long)})
		ideas := run(b, down, Portfolio{}, hours)
		if len(ideas) != 1 || ideas[0].Confidence != 0.8 || ideas[0].SupportingSignals["llm_confidence_scale"] != nil {
			t.Fatalf("ideas %+v", ideas)
		}
	})

	t.Run("GIVEN a held long WHEN the consensus turns bearish THEN the rule exit is emitted without asking the LLM", func(t *testing.T) {
		an := scaleAnalyser(0.5)
		ideas := run(withReading(basicReading{bearishTurn: true}), an, heldIn("SOL/EUR", 1), hours)
		if len(ideas) != 1 || !ideas[0].Exit || len(an.signalCalls) != 0 {
			t.Fatalf("ideas %+v, LLM calls %d", ideas, len(an.signalCalls))
		}
	})

	t.Run("GIVEN a short signal with nothing held WHEN the LLM vetoes it THEN no paper short is emitted", func(t *testing.T) {
		an := scaleAnalyser(0.5)
		if ideas := run(withReading(basicReading{sig: longSignal(domain.Short)}), an, Portfolio{}, hours); len(ideas) != 0 || len(an.signalCalls) != 1 {
			t.Fatalf("ideas %+v, LLM calls %d", ideas, len(an.signalCalls))
		}
	})

	t.Run("GIVEN the 4h combined strategy WHEN it asks the LLM THEN the prompt names 4-hour candles", func(t *testing.T) {
		b := NewBasicAndLLM4h(s)
		var calls int
		stubRead(&b.basicCore, basicReading{sig: longSignal(domain.Long)}, &calls, nil)
		an := scaleAnalyser(1.0)
		in := single("SOL/EUR", candleMarket(105, nil, fours))
		in.Analyser = an
		b.Evaluate(ctx, in)
		if len(an.signalCalls) != 1 || an.signalCalls[0].TickSeconds != 14400 {
			t.Fatalf("LLM calls %+v", an.signalCalls)
		}
	})
}

// TestBasicLongOnlyStrategies covers the long-only twins of the four basic
// strategies: the same rules, but a bearish signal never opens a short.
func TestBasicLongOnlyStrategies(t *testing.T) {
	ctx := context.Background()
	s := testSettings()
	hours := hourCandles(BasicMinClosedCandles+30, flat)
	fours := trendCandles(BasicMinClosedCandles+30, flat)

	// run evaluates st on SOL/EUR at price 105 with portfolio p and analyser an.
	run := func(st Strategy, p Portfolio, an Analyser) []domain.TradeIdea {
		in := single("SOL/EUR", candleMarket(105, hours, fours))
		in.Portfolio, in.Analyser = p, an
		return st.Evaluate(ctx, in)
	}
	// longOnly returns the four long-only strategies, each reading r.
	longOnly := func(r basicReading) []Strategy {
		var calls int
		b1, b4 := NewBasicLong1h(s), NewBasicLong4h(s)
		c1, c4 := NewBasicAndLLMLong1h(s), NewBasicAndLLMLong4h(s)
		for _, core := range []*basicCore{&b1.basicCore, &b4.basicCore, &c1.basicCore, &c4.basicCore} {
			stubRead(core, r, &calls, nil)
		}
		return []Strategy{b1, b4, c1, c4}
	}

	t.Run("GIVEN the four long-only strategies WHEN created THEN they have their own ids and only the combined ones use the LLM", func(t *testing.T) {
		want := map[string]bool{"basic_strategy_long_1h": false, "basic_strategy_long_4h": false,
			"basic_and_llm_strategy_long_1h": true, "basic_and_llm_strategy_long_4h": true}
		for _, st := range longOnly(basicReading{}) {
			usesLLM, ok := want[st.ID()]
			if !ok || st.UsesLLM() != usesLLM {
				t.Fatalf("unexpected strategy %q (uses LLM %v)", st.ID(), st.UsesLLM())
			}
			delete(want, st.ID())
		}
		if len(want) != 0 {
			t.Fatalf("missing %v", want)
		}
	})

	t.Run("GIVEN a short signal and nothing held WHEN a long-only strategy evaluates THEN no short is opened and the LLM is not asked", func(t *testing.T) {
		an := scaleAnalyser(1.2)
		for _, st := range longOnly(basicReading{sig: longSignal(domain.Short), bearishTurn: true}) {
			if ideas := run(st, Portfolio{}, an); len(ideas) != 0 {
				t.Fatalf("%s opened a short: %+v", st.ID(), ideas)
			}
		}
		if len(an.signalCalls) != 0 {
			t.Fatalf("LLM asked %d times about a short", len(an.signalCalls))
		}
	})

	t.Run("GIVEN a long signal and nothing held WHEN a long-only strategy evaluates THEN it enters long", func(t *testing.T) {
		for _, st := range longOnly(basicReading{sig: longSignal(domain.Long)}) {
			ideas := run(st, Portfolio{}, scaleAnalyser(1.0))
			if len(ideas) != 1 || ideas[0].Direction != domain.Long || ideas[0].StrategyID != st.ID() {
				t.Fatalf("%s: %+v", st.ID(), ideas)
			}
		}
	})

	t.Run("GIVEN a held long WHEN the consensus turns bearish THEN a long-only strategy still sells it as a rule exit", func(t *testing.T) {
		for _, st := range longOnly(basicReading{bearishTurn: true}) {
			ideas := run(st, heldIn("SOL/EUR", 1), scaleAnalyser(0.5))
			if len(ideas) != 1 || ideas[0].Direction != domain.Short || !ideas[0].Exit {
				t.Fatalf("%s: %+v, want a rule exit", st.ID(), ideas)
			}
		}
	})
}
