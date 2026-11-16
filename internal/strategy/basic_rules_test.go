package strategy

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/analysis"
	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

// rulesOff returns settings with the four basic exit and entry rules at their
// plainest: an exit on one vote on one candle, no trend filter, no trailing
// stop and no cooldown. Each test then switches on the rule it covers.
func rulesOff() config.Settings {
	s := testSettings()
	s.BasicExitVotes, s.BasicExitConfirmCandles = 1, 1
	s.BasicTrendEMA, s.BasicTrailATR, s.BasicCooldownCandles = 0, 0, 0
	s.BasicEntryVotes, s.BasicFreshSignal, s.BasicMinATRCost, s.BasicTakeProfitATR = 1, false, 0, 0
	return s
}

// scriptedRead replaces the strategy's rules with whatever reading *cur holds
// when a new set of candles is read.
func scriptedRead(core *basicCore, cur *basicReading) {
	core.read = func(_ *slog.Logger, _ string, view MarketData) basicReading {
		out := *cur
		out.view = view
		return out
	}
}

// heldAt returns a portfolio holding size of market, opened at avg on the
// hour candle with index openedHour.
func heldAt(market string, size, avg float64, openedHour int) Portfolio {
	opened := time.Unix(int64(openedHour)*3600, 0).UTC()
	return Portfolio{OpenPositions: []domain.PositionRecord{{Market: market, Size: size, AvgPrice: avg, Timestamp: opened}}}
}

// TestBasicExitRules covers the rule exit of the basic strategies: a clear
// vote against the position, seen on several closed candles in a row.
func TestBasicExitRules(t *testing.T) {
	ctx := context.Background()
	fours := trendCandles(70, flat)
	// eval runs b at price on the first BasicMinClosedCandles+extra 1-hour candles.
	eval := func(b Strategy, p Portfolio, price float64, extra int) []domain.TradeIdea {
		in := single("SOL/EUR", candleMarket(price, hourCandles(BasicMinClosedCandles+extra, flat), fours))
		in.Portfolio = p
		return b.Evaluate(ctx, in)
	}

	t.Run("GIVEN exit votes of 2 WHEN the net indicator vote is one against THEN the consensus has not turned; two against THEN it has", func(t *testing.T) {
		cases := []struct {
			momentum, votes  int
			bearish, bullish bool
		}{
			{0, -1, false, false}, {0, -2, true, false}, {0, -3, true, false},
			{0, 1, false, false}, {0, 2, false, true},
			{-1, 0, true, false}, {1, 0, false, true}, // a momentum move past the gate turns it on its own
		}
		for _, c := range cases {
			if bear, bull := consensusTurns(c.momentum, c.votes, 2); bear != c.bearish || bull != c.bullish {
				t.Fatalf("momentum %d, votes %+d: bearish %v bullish %v, want %v %v", c.momentum, c.votes, bear, bull, c.bearish, c.bullish)
			}
		}
	})

	t.Run("GIVEN 2 confirming candles and a held long WHEN the consensus turns bearish on one closed candle THEN it is held, however many ticks run; on the next candle too THEN it is sold", func(t *testing.T) {
		s := rulesOff()
		s.BasicExitConfirmCandles = 2
		b := NewBasic1h(s)
		cur := basicReading{bearishTurn: true}
		scriptedRead(&b.basicCore, &cur)
		for tick := 0; tick < 3; tick++ {
			if ideas := eval(b, heldIn("SOL/EUR", 1), 100, 30); len(ideas) != 0 {
				t.Fatalf("tick %d on the first bearish candle: %+v", tick, ideas)
			}
		}
		ideas := eval(b, heldIn("SOL/EUR", 1), 100, 31)
		if len(ideas) != 1 || !ideas[0].Exit || ideas[0].Direction != domain.Short {
			t.Fatalf("second bearish candle: %+v, want a rule exit", ideas)
		}
	})

	t.Run("GIVEN 2 confirming candles WHEN a bearish candle is followed by one that is not THEN the count starts again", func(t *testing.T) {
		s := rulesOff()
		s.BasicExitConfirmCandles = 2
		b := NewBasic1h(s)
		cur := basicReading{bearishTurn: true}
		scriptedRead(&b.basicCore, &cur)
		eval(b, heldIn("SOL/EUR", 1), 100, 30)
		cur = basicReading{}
		eval(b, heldIn("SOL/EUR", 1), 100, 31)
		cur = basicReading{bearishTurn: true}
		if ideas := eval(b, heldIn("SOL/EUR", 1), 100, 32); len(ideas) != 0 {
			t.Fatalf("one bearish candle after a break sold: %+v", ideas)
		}
		if ideas := eval(b, heldIn("SOL/EUR", 1), 100, 33); len(ideas) != 1 || !ideas[0].Exit {
			t.Fatalf("two bearish candles in a row: %+v, want a rule exit", ideas)
		}
	})

	t.Run("GIVEN 2 confirming candles WHEN a held paper short sees two bullish candles THEN it is covered after the second", func(t *testing.T) {
		s := rulesOff()
		s.BasicExitConfirmCandles = 2
		b := NewBasic1h(s)
		cur := basicReading{bullishTurn: true}
		scriptedRead(&b.basicCore, &cur)
		if ideas := eval(b, heldIn("SOL/EUR", -1), 100, 30); len(ideas) != 0 {
			t.Fatalf("first bullish candle: %+v", ideas)
		}
		if ideas := eval(b, heldIn("SOL/EUR", -1), 100, 31); len(ideas) != 1 || ideas[0].Direction != domain.Long {
			t.Fatalf("second bullish candle: %+v, want a cover", ideas)
		}
	})

	t.Run("GIVEN 2 confirming candles and a held long WHEN a full short signal appears THEN it is sold at once", func(t *testing.T) {
		s := rulesOff()
		s.BasicExitConfirmCandles = 2
		b := NewBasic1h(s)
		cur := basicReading{sig: longSignal(domain.Short)}
		scriptedRead(&b.basicCore, &cur)
		if ideas := eval(b, heldIn("SOL/EUR", 1), 100, 30); len(ideas) != 1 || !ideas[0].Exit {
			t.Fatalf("ideas %+v, want an immediate rule exit", ideas)
		}
	})
}

// TestBasicTrendFilter covers the entry trend filter: no long below the
// higher-timeframe trend EMA, no short above it.
func TestBasicTrendFilter(t *testing.T) {
	ctx := context.Background()
	hours := hourCandles(BasicMinClosedCandles+30, flat)
	fours := trendCandles(70, func(i int) float64 { return 50 + float64(i) })
	eval := func(s config.Settings, r basicReading) []domain.TradeIdea {
		b := NewBasic1h(s)
		scriptedRead(&b.basicCore, &r)
		return b.Evaluate(ctx, single("SOL/EUR", candleMarket(100, hours, fours)))
	}
	s := rulesOff()
	s.BasicTrendEMA = 20

	t.Run("GIVEN the trend filter WHEN a signal agrees with the trend, or the trend is unknown THEN the entry is emitted", func(t *testing.T) {
		for _, c := range []struct {
			dir   domain.Direction
			trend int
		}{{domain.Long, 1}, {domain.Long, 0}, {domain.Short, -1}, {domain.Short, 0}} {
			if ideas := eval(s, basicReading{sig: longSignal(c.dir), trend: c.trend}); len(ideas) != 1 || ideas[0].Direction != c.dir {
				t.Fatalf("%s with trend %+d: %+v, want an entry", c.dir, c.trend, ideas)
			}
		}
	})

	t.Run("GIVEN the trend filter WHEN a signal goes against the trend THEN no entry is emitted", func(t *testing.T) {
		for _, c := range []struct {
			dir   domain.Direction
			trend int
		}{{domain.Long, -1}, {domain.Short, 1}} {
			if ideas := eval(s, basicReading{sig: longSignal(c.dir), trend: c.trend}); len(ideas) != 0 {
				t.Fatalf("%s against trend %+d: %+v", c.dir, c.trend, ideas)
			}
		}
	})

	t.Run("GIVEN the trend filter switched off WHEN a long signal goes against the trend THEN it is emitted", func(t *testing.T) {
		if ideas := eval(rulesOff(), basicReading{sig: longSignal(domain.Long), trend: -1}); len(ideas) != 1 {
			t.Fatalf("ideas %+v, want an entry", ideas)
		}
	})

	t.Run("GIVEN a trend EMA period WHEN the 1h strategy reads a market THEN the trend EMA of the finished 4-hour candles is in the higher timeframe; off THEN it is not", func(t *testing.T) {
		b := NewBasic1h(s)
		var calls int
		var seen MarketData
		stubRead(&b.basicCore, basicReading{}, &calls, &seen)
		b.Evaluate(ctx, single("SOL/EUR", candleMarket(100, hours, fours)))
		want, _ := analysis.EMA(closesOf(closedOf(fours)), 20)
		if got, ok := seen.HigherTimeframe.Num("trend_ema"); !ok || got != want {
			t.Fatalf("trend_ema %v (%v), want %v", got, ok, want)
		}
		off := NewBasic1h(rulesOff())
		stubRead(&off.basicCore, basicReading{}, &calls, &seen)
		off.Evaluate(ctx, single("SOL/EUR", candleMarket(100, hours, fours)))
		if _, ok := seen.HigherTimeframe["trend_ema"]; ok {
			t.Fatal("trend_ema computed with the filter off")
		}
	})

	t.Run("GIVEN a candle view WHEN the rules read it THEN the trend is up above the trend EMA, down below it and unknown without one", func(t *testing.T) {
		for _, c := range []struct {
			ema   any
			trend int
		}{{80.0, 1}, {120.0, -1}, {nil, 0}} {
			view := MarketData{Price: 100, PreviousPrice: 100, HigherTimeframe: analysis.Indicators{}}
			if c.ema != nil {
				view.HigherTimeframe["trend_ema"] = c.ema
			}
			if r := readBasic(slog.Default(), "SOL/EUR", view, 2); r.trend != c.trend {
				t.Fatalf("EMA %v: trend %+d, want %+d", c.ema, r.trend, c.trend)
			}
		}
	})
}

// TestBasicTrailingStop covers the ATR trailing stop: a held position is
// closed once the live price gives back a multiple of the candle ATR from
// its best price since entry.
func TestBasicTrailingStop(t *testing.T) {
	ctx := context.Background()
	fours := trendCandles(70, flat)
	n := BasicMinClosedCandles + 30
	// eval runs a neutral-reading strategy with settings s at price.
	eval := func(s config.Settings, p Portfolio, price float64, hours []domain.Candle) []domain.TradeIdea {
		b := NewBasic1h(s)
		cur := basicReading{}
		scriptedRead(&b.basicCore, &cur)
		in := single("SOL/EUR", candleMarket(price, hours, fours))
		in.Portfolio = p
		return b.Evaluate(ctx, in)
	}
	s := rulesOff()
	s.BasicTrailATR = 3
	// The price steps from 100 to 110 on candle 50, then stays: the ATR settles
	// back towards 0.22, so the stop sits a little below 110.
	rose := hourCandles(n, func(i int) float64 {
		if i >= 50 {
			return 110
		}
		return 100
	})

	t.Run("GIVEN a long held since before a rise WHEN the live price falls three ATRs below the high since entry THEN it is sold; above that THEN it is held", func(t *testing.T) {
		ideas := eval(s, heldAt("SOL/EUR", 1, 100, 45), 105, rose)
		if len(ideas) != 1 || !ideas[0].Exit || ideas[0].Direction != domain.Short || !strings.Contains(ideas[0].Thesis, "ATR") {
			t.Fatalf("ideas %+v, want a trailing-stop exit", ideas)
		}
		if ideas := eval(s, heldAt("SOL/EUR", 1, 100, 45), 109.9, rose); len(ideas) != 0 {
			t.Fatalf("ideas %+v, want the long held", ideas)
		}
	})

	t.Run("GIVEN a high before the entry WHEN the price stays near the entry THEN the stop trails from the entry, not from the earlier high", func(t *testing.T) {
		spike := hourCandles(n, func(i int) float64 {
			if i == 5 {
				return 130
			}
			return 100
		})
		if ideas := eval(s, heldAt("SOL/EUR", 1, 100, 50), 99.75, spike); len(ideas) != 0 {
			t.Fatalf("ideas %+v, want the long held", ideas)
		}
		if ideas := eval(s, heldAt("SOL/EUR", 1, 100, 50), 98.5, spike); len(ideas) != 1 || !ideas[0].Exit {
			t.Fatalf("ideas %+v, want a trailing-stop exit", ideas)
		}
	})

	t.Run("GIVEN a paper short held since before a fall WHEN the live price rises three ATRs above the low since entry THEN it is covered", func(t *testing.T) {
		fell := hourCandles(n, func(i int) float64 {
			if i >= 50 {
				return 90
			}
			return 100
		})
		ideas := eval(s, heldAt("SOL/EUR", -1, 100, 45), 95, fell)
		if len(ideas) != 1 || ideas[0].Direction != domain.Long || ideas[0].Exit {
			t.Fatalf("ideas %+v, want a covering long", ideas)
		}
		if ideas := eval(s, heldAt("SOL/EUR", -1, 100, 45), 90.1, fell); len(ideas) != 0 {
			t.Fatalf("ideas %+v, want the short held", ideas)
		}
	})

	t.Run("GIVEN the trailing stop switched off WHEN the price falls far below the high THEN the long is held", func(t *testing.T) {
		if ideas := eval(rulesOff(), heldAt("SOL/EUR", 1, 100, 45), 101, rose); len(ideas) != 0 {
			t.Fatalf("ideas %+v", ideas)
		}
	})
}

// TestBasicCooldown covers the re-entry cooldown: after a position in a
// market closes, no new entry there until enough candles have closed.
func TestBasicCooldown(t *testing.T) {
	ctx := context.Background()
	fours := trendCandles(70, flat)
	// run evaluates b on the first BasicMinClosedCandles+extra 1-hour candles.
	run := func(b Strategy, p Portfolio, extra int) []domain.TradeIdea {
		in := single("SOL/EUR", candleMarket(100, hourCandles(BasicMinClosedCandles+extra, flat), fours))
		in.Portfolio = p
		return b.Evaluate(ctx, in)
	}
	strategyWith := func(cooldown int) *BasicStrategy {
		s := rulesOff()
		s.BasicCooldownCandles = cooldown
		b := NewBasic1h(s)
		cur := basicReading{sig: longSignal(domain.Long)}
		scriptedRead(&b.basicCore, &cur)
		return b
	}

	t.Run("GIVEN a cooldown of 3 candles WHEN a held long closes THEN a long signal is ignored until 3 more candles have closed", func(t *testing.T) {
		b := strategyWith(3)
		run(b, heldIn("SOL/EUR", 1), 30) // held on candle 30, then closed
		for extra := 30; extra < 33; extra++ {
			if ideas := run(b, Portfolio{}, extra); len(ideas) != 0 {
				t.Fatalf("candle +%d after the exit: %+v", extra-30, ideas)
			}
		}
		if ideas := run(b, Portfolio{}, 33); len(ideas) != 1 || ideas[0].Direction != domain.Long {
			t.Fatalf("3 candles after the exit: %+v, want an entry", ideas)
		}
	})

	t.Run("GIVEN no cooldown WHEN a held long closes THEN a long signal on the same candle is followed", func(t *testing.T) {
		b := strategyWith(0)
		run(b, heldIn("SOL/EUR", 1), 30)
		if ideas := run(b, Portfolio{}, 30); len(ideas) != 1 {
			t.Fatalf("ideas %+v, want an entry", ideas)
		}
	})

	t.Run("GIVEN a cooldown WHEN the market was never held THEN a long signal is followed at once", func(t *testing.T) {
		if ideas := run(strategyWith(3), Portfolio{}, 30); len(ideas) != 1 {
			t.Fatalf("ideas %+v, want an entry", ideas)
		}
	})

	t.Run("GIVEN a cooldown WHEN the combined LLM strategy sees a long signal during it THEN the LLM is not asked", func(t *testing.T) {
		s := rulesOff()
		s.BasicCooldownCandles = 3
		b := NewBasicAndLLM1h(s)
		cur := basicReading{sig: longSignal(domain.Long)}
		scriptedRead(&b.basicCore, &cur)
		an := scaleAnalyser(1.0)
		for _, p := range []Portfolio{heldIn("SOL/EUR", 1), {}} {
			in := single("SOL/EUR", candleMarket(100, hourCandles(BasicMinClosedCandles+30, flat), fours))
			in.Portfolio, in.Analyser = p, an
			if ideas := b.Evaluate(ctx, in); len(ideas) != 0 {
				t.Fatalf("ideas %+v", ideas)
			}
		}
		if len(an.signalCalls) != 0 {
			t.Fatalf("LLM asked %d times during the cooldown", len(an.signalCalls))
		}
	})
}
