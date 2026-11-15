package strategy

import (
	"context"
	"strings"
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

// flatThen returns n flat 4-hour candles at 100 (high 101, low 99) followed
// by the given closes as full candles, plus a forming candle at the last close.
func flatThen(n int, closes ...float64) []domain.Candle {
	out := make([]domain.Candle, 0, n+len(closes)+1)
	add := func(o, h, l, c float64) {
		out = append(out, domain.Candle{T: int64(len(out)) * 4 * 3600, O: o, H: h, L: l, C: c, V: 1})
	}
	for range n {
		add(100, 101, 99, 100)
	}
	prev := 100.0
	for _, c := range closes {
		add(prev, max(prev, c)*1.002, min(prev, c)*0.998, c)
		prev = c
	}
	add(prev, prev, prev, prev) // forming
	return out
}

// evalOne runs s over one market's 4-hour candles, with nothing held, and
// returns its ideas.
func evalOne(s Strategy, candles []domain.Candle) []domain.TradeIdea {
	price := candles[len(candles)-1].C
	return s.Evaluate(context.Background(), single("SOL/EUR", MarketData{Price: price, PreviousPrice: price, TrendCandles: candles}))
}

// evalHeld runs s like evalOne while a SOL/EUR long is held.
func evalHeld(s Strategy, candles []domain.Candle) []domain.TradeIdea {
	price := candles[len(candles)-1].C
	in := single("SOL/EUR", MarketData{Price: price, PreviousPrice: price, TrendCandles: candles})
	in.Portfolio = Portfolio{OpenPositions: []domain.PositionRecord{{Market: "SOL/EUR", Size: 1, AvgPrice: price}}}
	return s.Evaluate(context.Background(), in)
}

// direction returns the one idea's direction, or "" when there is no idea.
func direction(t *testing.T, ideas []domain.TradeIdea) domain.Direction {
	t.Helper()
	switch len(ideas) {
	case 0:
		return ""
	case 1:
		if ideas[0].Confidence < testSettings().MinSignalConfidence {
			t.Fatalf("confidence %.2f is below the automated gate", ideas[0].Confidence)
		}
		if (ideas[0].Direction == domain.Short) != ideas[0].Exit {
			t.Fatalf("a %s idea with Exit=%v: every sell, and only a sell, is a rule exit", ideas[0].Direction, ideas[0].Exit)
		}
		return ideas[0].Direction
	default:
		t.Fatalf("got %d ideas for one market", len(ideas))
		return ""
	}
}

// repeat returns n copies of v.
func repeat(v float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// TestDonchianStrategy covers the 4-hour channel breakout strategy.
func TestDonchianStrategy(t *testing.T) {
	s := NewDonchian(testSettings())
	enough := DonchianEntryCandles + 5

	t.Run("GIVEN fewer closed candles than the entry channel WHEN evaluated THEN no idea is emitted", func(t *testing.T) {
		if d := direction(t, evalOne(s, flatThen(DonchianEntryCandles-1, 120))); d != "" {
			t.Fatalf("got %s during warm-up", d)
		}
	})

	t.Run("GIVEN a close above the highest high of the entry channel WHEN evaluated THEN a long is emitted", func(t *testing.T) {
		if d := direction(t, evalOne(s, flatThen(enough, 103))); d != domain.Long {
			t.Fatalf("direction %q, want long", d)
		}
	})

	t.Run("GIVEN a breakout followed by quiet candles inside the channel WHEN evaluated THEN it stays long", func(t *testing.T) {
		if d := direction(t, evalOne(s, flatThen(enough, append([]float64{103}, repeat(102, 5)...)...))); d != domain.Long {
			t.Fatalf("direction %q, want long held", d)
		}
	})

	t.Run("GIVEN a held long after a breakout WHEN a close falls below the lowest low of the exit channel THEN an exit is emitted", func(t *testing.T) {
		if d := direction(t, evalHeld(s, flatThen(enough, 103, 104, 95))); d != domain.Short {
			t.Fatalf("direction %q, want short", d)
		}
	})

	t.Run("GIVEN a held long and no breakout in force WHEN evaluated THEN an exit is emitted", func(t *testing.T) {
		if d := direction(t, evalHeld(s, flatThen(enough))); d != domain.Short {
			t.Fatalf("direction %q, want short", d)
		}
	})

	t.Run("GIVEN nothing held and no breakout WHEN evaluated THEN no idea is emitted (nothing to sell)", func(t *testing.T) {
		if d := direction(t, evalOne(s, flatThen(enough))); d != "" {
			t.Fatalf("direction %q, want no idea", d)
		}
	})

	t.Run("GIVEN a held long WHEN the breakout is still in force THEN no idea is emitted (already held)", func(t *testing.T) {
		if d := direction(t, evalHeld(s, flatThen(enough, 103))); d != "" {
			t.Fatalf("direction %q, want no idea", d)
		}
	})

	t.Run("GIVEN a breakout only in the forming candle WHEN evaluated THEN it is ignored", func(t *testing.T) {
		c := flatThen(enough)
		c[len(c)-1].C, c[len(c)-1].H = 130, 130
		if d := direction(t, evalOne(s, c)); d != "" {
			t.Fatalf("direction %q: the forming candle must not trigger an entry", d)
		}
	})

	t.Run("GIVEN an entry WHEN its prompt is built THEN it names the channel and lists its levels", func(t *testing.T) {
		c := flatThen(enough, 103)
		ideas := evalOne(s, c)
		p := s.Prompt(ideas[0], MarketData{TrendCandles: c})
		if !strings.Contains(p.Name, "Donchian") || p.Data["channel_high"] == "" || p.Data["exit_level"] == "" {
			t.Fatalf("prompt %+v", p)
		}
	})
}

// TestRSIDipStrategy covers buying a deep 4-hour dip and selling the bounce.
func TestRSIDipStrategy(t *testing.T) {
	s := NewRSIDip(testSettings())
	crash := []float64{97, 93, 89, 85, 81, 78} // RSI collapses and price is far below its 20-candle EMA

	t.Run("GIVEN too few closed candles WHEN evaluated THEN no idea is emitted", func(t *testing.T) {
		if d := direction(t, evalOne(s, flatThen(5, crash...))); d != "" {
			t.Fatalf("got %s during warm-up", d)
		}
	})

	t.Run("GIVEN a crash that drives RSI below the entry level far under the EMA WHEN evaluated THEN a long is emitted", func(t *testing.T) {
		if d := direction(t, evalOne(s, flatThen(60, crash...))); d != domain.Long {
			t.Fatalf("direction %q, want long", d)
		}
	})

	t.Run("GIVEN a dip bought and a bounce that lifts RSI over the exit level WHEN evaluated THEN an exit is emitted", func(t *testing.T) {
		if d := direction(t, evalHeld(s, flatThen(60, append(crash, 84, 89, 94, 98)...))); d != domain.Short {
			t.Fatalf("direction %q, want short", d)
		}
	})

	t.Run("GIVEN a dip held for the maximum number of candles without a bounce WHEN evaluated THEN an exit is emitted", func(t *testing.T) {
		if d := direction(t, evalHeld(s, flatThen(60, append(crash, repeat(77.5, RSIDipMaxHoldCandles)...)...))); d != domain.Short {
			t.Fatalf("direction %q, want short after the time stop", d)
		}
	})

	t.Run("GIVEN a gentle decline WHEN evaluated THEN no entry is made", func(t *testing.T) {
		if d := direction(t, evalOne(s, flatThen(60, 99.5, 99, 98.5, 98, 97.5))); d == domain.Long {
			t.Fatal("a shallow decline must not trigger a dip entry")
		}
	})

	t.Run("GIVEN an entry WHEN its prompt is built THEN it explains the dip and lists RSI and the EMA gap", func(t *testing.T) {
		c := flatThen(60, crash...)
		p := s.Prompt(evalOne(s, c)[0], MarketData{TrendCandles: c})
		if !strings.Contains(p.Instructions, "RSI") || p.Data["rsi_4h"] == "" || p.Data["ema20_gap"] == "" {
			t.Fatalf("prompt %+v", p)
		}
	})
}

// TestMACDStrategy covers the slow 4-hour MACD momentum strategy.
func TestMACDStrategy(t *testing.T) {
	s := NewMACD(testSettings())
	n := MACDMinClosedCandles + 20

	t.Run("GIVEN too few closed candles WHEN evaluated THEN no idea is emitted", func(t *testing.T) {
		if d := direction(t, evalOne(s, trendCandles(MACDMinClosedCandles, rising))); d != "" {
			t.Fatalf("got %s during warm-up", d)
		}
	})

	t.Run("GIVEN an accelerating 4-hour rise WHEN evaluated THEN a long is emitted", func(t *testing.T) {
		accel := func(i int) float64 { return 1000 * (1 + 0.00005*float64(i*i)) }
		if d := direction(t, evalOne(s, trendCandles(n, accel))); d != domain.Long {
			t.Fatalf("direction %q, want long", d)
		}
	})

	t.Run("GIVEN a rise that turns into a fall WHEN evaluated THEN an exit is emitted", func(t *testing.T) {
		turn := func(i int) float64 {
			if i < n-30 {
				return rising(i)
			}
			return rising(n-30) * (1 - 0.01*float64(i-(n-30)))
		}
		if d := direction(t, evalHeld(s, trendCandles(n, turn))); d != domain.Short {
			t.Fatalf("direction %q, want short", d)
		}
	})

	t.Run("GIVEN an entry WHEN its prompt is built THEN it lists the MACD line and histogram", func(t *testing.T) {
		accel := func(i int) float64 { return 1000 * (1 + 0.00005*float64(i*i)) }
		c := trendCandles(n, accel)
		p := s.Prompt(evalOne(s, c)[0], MarketData{TrendCandles: c})
		if !strings.Contains(p.Name, "MACD") || p.Data["macd_4h"] == "" || p.Data["histogram_4h"] == "" {
			t.Fatalf("prompt %+v", p)
		}
	})
}

// TestSqueezeStrategy covers the Bollinger squeeze breakout strategy.
func TestSqueezeStrategy(t *testing.T) {
	s := NewSqueeze(testSettings())
	// Wide swings, then a tight range (the squeeze), then a close above the upper band.
	swings := make([]float64, 0, SqueezeMinClosedCandles)
	for i := range SqueezeMinClosedCandles - 20 {
		swings = append(swings, 100+6*float64(i%2*2-1)+0.05*float64(i))
	}
	base := swings[len(swings)-1]
	tight := repeat(base, SqueezeBandCandles+5) // a whole band window of ever-smaller swings
	for i := range tight {
		tight[i] += 0.2 * float64(i%2) * (1 - float64(i)/float64(len(tight)))
	}
	breakout := append(append(append([]float64{}, swings...), tight...), base*1.04)

	t.Run("GIVEN too few closed candles WHEN evaluated THEN no idea is emitted", func(t *testing.T) {
		if d := direction(t, evalOne(s, flatThen(10, base*1.04))); d != "" {
			t.Fatalf("got %s during warm-up", d)
		}
	})

	t.Run("GIVEN a squeeze followed by a close above the upper band in an uptrend WHEN evaluated THEN a long is emitted", func(t *testing.T) {
		if d := direction(t, evalOne(s, flatThen(0, breakout...))); d != domain.Long {
			t.Fatalf("direction %q, want long", d)
		}
	})

	t.Run("GIVEN a breakout that falls back below the middle band WHEN evaluated THEN an exit is emitted", func(t *testing.T) {
		if d := direction(t, evalHeld(s, flatThen(0, append(breakout, base*1.05, base*0.97)...))); d != domain.Short {
			t.Fatalf("direction %q, want short", d)
		}
	})

	t.Run("GIVEN wide swings without a squeeze WHEN a close tops the upper band THEN no entry is made", func(t *testing.T) {
		noSqueeze := append(append([]float64{}, swings...), swings[len(swings)-1]*1.15)
		if d := direction(t, evalOne(s, flatThen(0, noSqueeze...))); d == domain.Long {
			t.Fatal("a breakout without a squeeze must not trigger an entry")
		}
	})

	t.Run("GIVEN an entry WHEN its prompt is built THEN it lists the bands and the squeeze", func(t *testing.T) {
		c := flatThen(0, breakout...)
		p := s.Prompt(evalOne(s, c)[0], MarketData{TrendCandles: c})
		if !strings.Contains(p.Name, "Bollinger") || p.Data["upper_band"] == "" || p.Data["bandwidth_pct"] == "" {
			t.Fatalf("prompt %+v", p)
		}
	})
}

// TestCandleStrategyReuse covers reusing a decision between candle closes.
func TestCandleStrategyReuse(t *testing.T) {
	t.Run("GIVEN one strategy evaluated on every tick WHEN a new 4-hour candle closes THEN the decision follows the new candle", func(t *testing.T) {
		s := NewDonchian(testSettings())
		quiet := flatThen(DonchianEntryCandles + 5)
		if d := direction(t, evalHeld(s, quiet)); d != domain.Short {
			t.Fatalf("before the breakout: %q", d)
		}
		if d := direction(t, evalHeld(s, quiet)); d != domain.Short {
			t.Fatalf("same candles again: %q", d)
		}
		if d := direction(t, evalOne(s, flatThen(DonchianEntryCandles+5, 103))); d != domain.Long {
			t.Fatalf("after the breakout candle closed: %q, want long", d)
		}
	})

	t.Run("GIVEN a reused decision WHEN the price changes between closes THEN each idea reports the current price", func(t *testing.T) {
		s := NewDonchian(testSettings())
		c := flatThen(DonchianEntryCandles+5, 103)
		in := func(price float64) Input {
			return single("SOL/EUR", MarketData{Price: price, PreviousPrice: price, TrendCandles: c})
		}
		a := s.Evaluate(context.Background(), in(103))
		b := s.Evaluate(context.Background(), in(104))
		if a[0].SupportingSignals["current_price"] != 103.0 || b[0].SupportingSignals["current_price"] != 104.0 {
			t.Fatalf("prices %v and %v", a[0].SupportingSignals["current_price"], b[0].SupportingSignals["current_price"])
		}
	})
}
