package strategy

import (
	"context"
	"strings"
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy/calc"
)

// signalWith returns an entry signal in dir with net votes for it and atrPct.
func signalWith(dir domain.Direction, votes int, atrPct float64) *indicatorSignal {
	sig := longSignal(dir)
	sig.Votes = calc.VoteSummary{Votes: votes, Available: 11, Supporting: votes, Values: map[string]any{}}
	sig.ATRPct = atrPct
	return sig
}

// candleRunner evaluates a strategy built from s whose reading is *cur, on the
// first BasicMinClosedCandles+extra flat 1-hour candles at price.
type candleRunner struct {
	b   Strategy
	cur *basicReading
}

// newRunner returns a basic_strategy_1h with settings s and a scripted reading.
func newRunner(s config.Settings) candleRunner {
	b := NewBasic1h(s)
	cur := &basicReading{}
	scriptedRead(&b.basicCore, cur)
	return candleRunner{b: b, cur: cur}
}

// run evaluates the runner with reading r on candle extra at price.
func (c candleRunner) run(r basicReading, p Portfolio, price float64, extra int) []domain.TradeIdea {
	*c.cur = r
	in := single("SOL/EUR", candleMarket(price, hourCandles(BasicMinClosedCandles+extra, flat), trendCandles(70, flat)))
	in.Portfolio = p
	return c.b.Evaluate(context.Background(), in)
}

// TestBasicStricterEntries covers BASIC_ENTRY_VOTES and BASIC_FRESH_SIGNAL:
// entries need a clear consensus, and optionally only a newly appeared one.
func TestBasicStricterEntries(t *testing.T) {
	t.Run("GIVEN entry votes of 4 WHEN a signal has 3 net votes THEN no entry; 4 THEN it enters, long or short", func(t *testing.T) {
		s := rulesOff()
		s.BasicEntryVotes = 4
		for _, dir := range []domain.Direction{domain.Long, domain.Short} {
			if ideas := newRunner(s).run(basicReading{sig: signalWith(dir, 3, 1.5)}, Portfolio{}, 100, 30); len(ideas) != 0 {
				t.Fatalf("%s with 3 votes: %+v", dir, ideas)
			}
			if ideas := newRunner(s).run(basicReading{sig: signalWith(dir, 4, 1.5)}, Portfolio{}, 100, 30); len(ideas) != 1 || ideas[0].Direction != dir {
				t.Fatalf("%s with 4 votes: %+v, want an entry", dir, ideas)
			}
		}
	})

	t.Run("GIVEN fresh signals only WHEN a long signal appears, then holds on the next candle THEN it enters on the first candle only", func(t *testing.T) {
		s := rulesOff()
		s.BasicFreshSignal = true
		r := newRunner(s)
		if ideas := r.run(basicReading{}, Portfolio{}, 100, 30); len(ideas) != 0 {
			t.Fatalf("no signal: %+v", ideas)
		}
		long := basicReading{sig: signalWith(domain.Long, 3, 1.5)}
		if ideas := r.run(long, Portfolio{}, 100, 31); len(ideas) != 1 {
			t.Fatalf("new signal: %+v, want an entry", ideas)
		}
		if ideas := r.run(long, Portfolio{}, 100, 32); len(ideas) != 0 {
			t.Fatalf("the same signal a candle later entered again: %+v", ideas)
		}
		r.run(basicReading{}, Portfolio{}, 100, 33)
		if ideas := r.run(long, Portfolio{}, 100, 34); len(ideas) != 1 {
			t.Fatalf("a signal after a gap: %+v, want an entry", ideas)
		}
	})

	t.Run("GIVEN fresh signals and entry votes of 4 WHEN a weak signal grows strong on the next candle THEN it counts as new and enters", func(t *testing.T) {
		s := rulesOff()
		s.BasicFreshSignal, s.BasicEntryVotes = true, 4
		r := newRunner(s)
		r.run(basicReading{sig: signalWith(domain.Long, 2, 1.5)}, Portfolio{}, 100, 30)
		if ideas := r.run(basicReading{sig: signalWith(domain.Long, 4, 1.5)}, Portfolio{}, 100, 31); len(ideas) != 1 {
			t.Fatalf("ideas %+v, want an entry", ideas)
		}
	})

	t.Run("GIVEN fresh signals off WHEN a signal holds on the next candle THEN it is followed on each new candle", func(t *testing.T) {
		r := newRunner(rulesOff())
		long := basicReading{sig: signalWith(domain.Long, 3, 1.5)}
		r.run(long, Portfolio{}, 100, 30)
		if ideas := r.run(long, Portfolio{}, 100, 31); len(ideas) != 1 {
			t.Fatalf("ideas %+v, want an entry", ideas)
		}
	})
}

// TestBasicVolatilityFilter covers BASIC_MIN_ATR_COST: no entry when the
// candle ATR is too small for a typical move to pay the trading costs.
func TestBasicVolatilityFilter(t *testing.T) {
	s := rulesOff()
	s.FeeAndSlippage = 0.01

	t.Run("GIVEN a minimum of 2 costs WHEN the candle ATR is below twice FEE_AND_SLIPPAGE THEN no entry; at or above it THEN it enters", func(t *testing.T) {
		s.BasicMinATRCost = 2
		if ideas := newRunner(s).run(basicReading{sig: signalWith(domain.Long, 3, 1.5)}, Portfolio{}, 100, 30); len(ideas) != 0 {
			t.Fatalf("ATR 1.5%% against 2%%: %+v", ideas)
		}
		if ideas := newRunner(s).run(basicReading{sig: signalWith(domain.Short, 3, 2.5)}, Portfolio{}, 100, 30); len(ideas) != 1 {
			t.Fatalf("ATR 2.5%% against 2%%: %+v, want an entry", ideas)
		}
	})

	t.Run("GIVEN the filter switched off WHEN the candle ATR is tiny THEN it enters", func(t *testing.T) {
		s.BasicMinATRCost = 0
		if ideas := newRunner(s).run(basicReading{sig: signalWith(domain.Long, 3, 0.01)}, Portfolio{}, 100, 30); len(ideas) != 1 {
			t.Fatalf("ideas %+v, want an entry", ideas)
		}
	})

	t.Run("GIVEN the filter WHEN a long is held and the ATR is tiny THEN its exits are unaffected", func(t *testing.T) {
		s.BasicMinATRCost = 2
		ideas := newRunner(s).run(basicReading{bearishTurn: true, sig: signalWith(domain.Short, 3, 0.01)}, heldIn("SOL/EUR", 1), 100, 30)
		if len(ideas) != 1 || !ideas[0].Exit {
			t.Fatalf("ideas %+v, want a rule exit", ideas)
		}
	})
}

// TestBasicProfitTarget covers BASIC_TAKE_PROFIT_ATR: a held position is
// closed once the live price has moved that many candle ATRs its way from
// the entry price. The flat test candles have an ATR of 0.2 at 100.
func TestBasicProfitTarget(t *testing.T) {
	s := rulesOff()
	s.BasicTakeProfitATR = 3

	t.Run("GIVEN a target of 3 ATR and a long held at 100 WHEN the price reaches 100.7 THEN it is sold; at 100.4 THEN it is held", func(t *testing.T) {
		ideas := newRunner(s).run(basicReading{}, heldAt("SOL/EUR", 1, 100, 45), 100.7, 30)
		if len(ideas) != 1 || !ideas[0].Exit || ideas[0].Direction != domain.Short || !strings.Contains(ideas[0].Thesis, "Profit target") {
			t.Fatalf("ideas %+v, want a profit-target exit", ideas)
		}
		if ideas := newRunner(s).run(basicReading{}, heldAt("SOL/EUR", 1, 100, 45), 100.4, 30); len(ideas) != 0 {
			t.Fatalf("ideas %+v, want the long held", ideas)
		}
	})

	t.Run("GIVEN a target of 3 ATR and a paper short held at 100 WHEN the price falls to 99.3 THEN it is covered; at 99.6 THEN it is held", func(t *testing.T) {
		ideas := newRunner(s).run(basicReading{}, heldAt("SOL/EUR", -1, 100, 45), 99.3, 30)
		if len(ideas) != 1 || ideas[0].Direction != domain.Long || !strings.Contains(ideas[0].Thesis, "Profit target") {
			t.Fatalf("ideas %+v, want a covering long", ideas)
		}
		if ideas := newRunner(s).run(basicReading{}, heldAt("SOL/EUR", -1, 100, 45), 99.6, 30); len(ideas) != 0 {
			t.Fatalf("ideas %+v, want the short held", ideas)
		}
	})

	t.Run("GIVEN the target switched off WHEN the price is far above the entry THEN the long is held", func(t *testing.T) {
		if ideas := newRunner(rulesOff()).run(basicReading{}, heldAt("SOL/EUR", 1, 100, 45), 110, 30); len(ideas) != 0 {
			t.Fatalf("ideas %+v", ideas)
		}
	})
}
