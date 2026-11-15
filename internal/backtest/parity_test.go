package backtest

import (
	"fmt"
	"math"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/histdata"
	"github.com/James-Chapman/LLMTradingBot/internal/marketdata"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy"
)

// naiveCandles builds clock-aligned candles of interval from every bar that
// started before tick, the way live reads Kraken's OHLC rows: one candle per
// interval that had trades (empty intervals have no row) and, last, the
// candle of the interval holding tick. Kraken always returns that forming
// row, so when the interval has had no trade yet it is flat at the last
// close with zero volume; synthetic reports whether that happened. Only the
// newest limit are returned.
func naiveCandles(bars []histdata.Bar, tick time.Time, interval time.Duration, limit int) (out []domain.Candle, synthetic bool) {
	for _, b := range bars {
		if !b.Start.Before(tick) {
			break
		}
		bucket := b.Start.Truncate(interval).Unix()
		if n := len(out); n > 0 && out[n-1].T == bucket {
			c := &out[n-1]
			c.H, c.L, c.C = math.Max(c.H, b.High), math.Min(c.L, b.Low), b.Close
			c.V += b.Volume
			continue
		}
		out = append(out, domain.Candle{T: bucket, O: b.Open, H: b.High, L: b.Low, C: b.Close, V: b.Volume})
	}
	if len(out) == 0 {
		return nil, false
	}
	if current := tick.Truncate(interval).Unix(); out[len(out)-1].T < current {
		last := out[len(out)-1].C
		out = append(out, domain.Candle{T: current, O: last, H: last, L: last, C: last})
		synthetic = true
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, synthetic
}

// barsFrom returns the bars that start at or after from.
func barsFrom(bars []histdata.Bar, from time.Time) []histdata.Bar {
	for i, b := range bars {
		if !b.Start.Before(from) {
			return bars[i:]
		}
	}
	return nil
}

// naiveVolume sums the base volume of bars that started in the 24 hours before tick.
func naiveVolume(bars []histdata.Bar, tick time.Time) float64 {
	v := 0.0
	for _, b := range bars {
		if !b.Start.Before(tick.Add(-24*time.Hour)) && b.Start.Before(tick) {
			v += b.Volume
		}
	}
	return v
}

// naiveLastPrice returns the close of the newest bar that started before tick.
func naiveLastPrice(bars []histdata.Bar, tick time.Time) (float64, bool) {
	price, ok := 0.0, false
	for _, b := range bars {
		if b.Start.Before(tick) {
			price, ok = b.Close, true
		}
	}
	return price, ok
}

// gappyBars returns a random walk from start with every bar in [gapFrom, gapTo) removed.
func gappyBars(seed uint64, n int, start time.Time, price float64, gapFrom, gapTo time.Time) []histdata.Bar {
	var out []histdata.Bar
	for _, b := range randomWalkBars(seed, n, start, price, 0.002) {
		if !b.Start.Before(gapFrom) && b.Start.Before(gapTo) {
			continue
		}
		out = append(out, b)
	}
	return out
}

// TestReplayInputParity covers that the replay builds the strategy input the
// live loop builds from the same ticks and candles.
func TestReplayInputParity(t *testing.T) {
	for _, interval := range []int{30, 120} {
		t.Run(fmt.Sprintf("GIVEN markets with pre-start history, gaps and a late listing and a %d s strategy interval WHEN replayed THEN candles omit untraded intervals but always end with the forming one, and each strategy tick's input equals the live builder fed with naive live-equivalent data", interval), func(t *testing.T) {
			pre := 26 * time.Hour
			const ticks = 1400
			reader := memReader{bars: map[string][]histdata.Bar{
				// Trades from 26 h before the start, with a 40-minute gap after it.
				"BTC/EUR": gappyBars(31, int((pre+ticks*histdata.BarInterval)/histdata.BarInterval), t0.Add(-pre), 50000, tick(300), tick(380)),
				// Trades from 10 h before the start.
				"ETH/EUR": randomWalkBars(32, int((10*time.Hour+ticks*histdata.BarInterval)/histdata.BarInterval), t0.Add(-10*time.Hour), 3000, 0.003),
				// Listed one hour after the start.
				"SOL/EUR": randomWalkBars(33, ticks-120, tick(120), 140, 0.003),
			}}
			settings := testSettings()
			settings.MomentumLookbackTicks = 7
			cfg := testConfig([]string{"BTC/EUR", "ETH/EUR", "SOL/EUR"}, ticks)
			cfg.Settings = settings
			cfg.Settings.TargetTradeAmount = 0 // let ordinary signals size themselves
			cfg = withInterval(cfg, interval)
			stride := interval / 30
			// The naive history gets one price per strategy tick, as live appends one per tick.
			history := map[string][]float64{}
			checked, synthetic := 0, 0
			cfg.observe = func(o TickObservation) {
				for _, m := range cfg.Markets {
					bars := reader.bars[m]
					price, priced := naiveLastPrice(bars, o.Time)
					if priced {
						history[m] = append(history[m], price)
					}
					got, has := o.MarketData[m]
					if o.MarketData == nil {
						continue // warming up: nothing is built
					}
					if has != priced {
						t.Fatalf("%s at %v: replay has data %v, naive priced %v", m, o.Time, has, priced)
					}
					if !priced {
						continue
					}
					hist := history[m]
					if len(hist) > marketdata.PriceHistoryTicks {
						hist = hist[len(hist)-marketdata.PriceHistoryTicks:]
					}
					vol := naiveVolume(bars, o.Time)
					c5, synth5 := naiveCandles(bars, o.Time, 5*time.Minute, marketdata.CandleLimit)
					c15, synth15 := naiveCandles(bars, o.Time, 15*time.Minute, marketdata.CandleLimit)
					// The replay reads nothing before the pre-roll, so its 4-hour window
					// (longer than the pre-roll) starts there; live Kraken rows go further back.
					c240, _ := naiveCandles(barsFrom(bars, cfg.Start.Add(-PreRoll)), o.Time, 4*time.Hour, marketdata.TrendCandleLimit)
					if synth5 || synth15 {
						synthetic++
					}
					want := marketdata.Build(marketdata.Input{
						Price:   price,
						Volume:  &vol,
						History: append([]float64(nil), hist...),
						OHLC5:   c5,
						OHLC15:  c15,
						OHLC240: c240,
					}, settings.MomentumLookbackTicks)
					if got.Volume == nil || !near(*got.Volume, vol, 1e-9*math.Max(1, vol)) {
						t.Fatalf("%s at %v: 24h volume %v, want %v", m, o.Time, got.Volume, vol)
					}
					got.Volume, want.Volume = nil, nil
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("%s at %v: strategy input differs from the live builder's\n got %+v\nwant %+v", m, o.Time, summarise(got), summarise(want))
					}
					checked++
				}
			}
			mustRun(t, reader, cfg)
			if checked < 2*ticks/stride {
				t.Fatalf("only %d market-ticks were compared", checked)
			}
			if synthetic == 0 {
				t.Fatal("no tick fell in an interval without trades, so the forming-row rule is untested")
			}
		})
	}
}

// krakenRows returns the rows Kraken's REST OHLC endpoint would return at
// tick for interval, built from bars: [time, open, high, low, close, vwap,
// volume, count] as strings, oldest first, ending with the forming row that
// Kraken always sends. With emptyRows, intervals without trades get a flat
// zero-volume row (whether Kraken does this is not verified); without, they
// have no row, as in Kraken's downloadable OHLCVT files.
func krakenRows(bars []histdata.Bar, tick time.Time, interval time.Duration, emptyRows bool) [][]any {
	type agg struct {
		start                   time.Time
		o, h, l, c, notional, v float64
		n                       int
	}
	var rows []agg
	for _, b := range bars {
		if !b.Start.Before(tick) {
			break
		}
		bucket := b.Start.Truncate(interval)
		if n := len(rows); n > 0 && rows[n-1].start.Equal(bucket) {
			r := &rows[n-1]
			r.h, r.l, r.c = math.Max(r.h, b.High), math.Min(r.l, b.Low), b.Close
			r.v += b.Volume
			r.notional += b.Close * b.Volume
			r.n += b.Trades
			continue
		}
		if n := len(rows); emptyRows && n > 0 {
			for gap := rows[n-1].start.Add(interval); gap.Before(bucket); gap = gap.Add(interval) {
				last := rows[len(rows)-1].c
				rows = append(rows, agg{start: gap, o: last, h: last, l: last, c: last})
			}
		}
		rows = append(rows, agg{start: bucket, o: b.Open, h: b.High, l: b.Low, c: b.Close, v: b.Volume, notional: b.Close * b.Volume, n: b.Trades})
	}
	if len(rows) == 0 {
		return nil
	}
	current := tick.Truncate(interval)
	for next := rows[len(rows)-1].start.Add(interval); !next.After(current); next = next.Add(interval) {
		if !emptyRows && !next.Equal(current) {
			continue // only the forming row is always present
		}
		last := rows[len(rows)-1].c
		rows = append(rows, agg{start: next, o: last, h: last, l: last, c: last})
	}
	out := make([][]any, 0, len(rows))
	num := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	for _, r := range rows {
		vwap := r.c
		if r.v > 0 {
			vwap = r.notional / r.v
		}
		out = append(out, []any{float64(r.start.Unix()), num(r.o), num(r.h), num(r.l), num(r.c), num(vwap), num(r.v), float64(r.n)})
	}
	return out
}

// liveCandles converts Kraken OHLC rows to candles the way the live market
// adapter does (internal/kraken parseOHLCResult, then GetOHLC's limit of the
// newest marketdata.CandleLimit rows).
func liveCandles(t *testing.T, rows [][]any, limit int) []domain.Candle {
	t.Helper()
	field := func(v any) float64 {
		switch x := v.(type) {
		case float64:
			return x
		case string:
			f, err := strconv.ParseFloat(x, 64)
			if err != nil {
				t.Fatalf("bad OHLC field %q", x)
			}
			return f
		}
		t.Fatalf("bad OHLC field %v", v)
		return 0
	}
	candles := make([]domain.Candle, 0, len(rows))
	for _, r := range rows {
		candles = append(candles, domain.Candle{T: int64(field(r[0])), O: field(r[1]), H: field(r[2]), L: field(r[3]), C: field(r[4]), V: field(r[6])})
	}
	if len(candles) > limit {
		candles = candles[len(candles)-limit:]
	}
	return candles
}

// sparseBars returns an illiquid market: a trade bar every few minutes at random.
func sparseBars(seed uint64, n int, start time.Time) []histdata.Bar {
	var out []histdata.Bar
	for i, b := range randomWalkBars(seed, n, start, 10, 0.004) {
		if i%9 == 0 || i%13 == 0 {
			out = append(out, b)
		}
	}
	return out
}

// TestLiveInputParity covers that the replay's strategy input equals the one
// the live path builds from Kraken-style OHLC rows and a price history through
// the shared builder, with and without rows for empty intervals.
func TestLiveInputParity(t *testing.T) {
	const ticks = 1200
	pre := 10 * time.Hour
	bars := sparseBars(77, int((pre+ticks*histdata.BarInterval)/histdata.BarInterval), t0.Add(-pre))
	reader := memReader{bars: map[string][]histdata.Bar{"ILL/EUR": bars}}
	inputs := map[bool]map[time.Time]strategy.MarketData{}
	for _, fill := range []bool{false, true} {
		name := "GIVEN an illiquid market and live OHLC rows without empty intervals WHEN replayed with empty candles off THEN each tick's input equals the live builder's"
		if fill {
			name = "GIVEN an illiquid market and live OHLC rows with flat rows for empty intervals WHEN replayed with --fill-empty-candles THEN each tick's input equals the live builder's"
		}
		t.Run(name, func(t *testing.T) {
			cfg := testConfig([]string{"ILL/EUR"}, ticks)
			cfg.FillEmptyCandles = fill
			cfg.strategy = &scripted{}
			var history []float64
			inputs[fill] = map[time.Time]strategy.MarketData{}
			checked := 0
			cfg.observe = func(o TickObservation) {
				price, ok := naiveLastPrice(bars, o.Time)
				if ok {
					history = append(history, price)
				}
				got, has := o.MarketData["ILL/EUR"]
				if !has {
					return
				}
				hist := history
				if len(hist) > marketdata.PriceHistoryTicks {
					hist = hist[len(hist)-marketdata.PriceHistoryTicks:]
				}
				want := marketdata.Build(marketdata.Input{
					Price:   price,
					Volume:  got.Volume, // 24 h volume is covered by TestReplayInputParity
					History: append([]float64(nil), hist...),
					OHLC5:   liveCandles(t, krakenRows(bars, o.Time, 5*time.Minute, fill), marketdata.CandleLimit),
					OHLC15:  liveCandles(t, krakenRows(bars, o.Time, 15*time.Minute, fill), marketdata.CandleLimit),
					OHLC240: liveCandles(t, krakenRows(bars, o.Time, 4*time.Hour, fill), marketdata.TrendCandleLimit),
				}, cfg.Settings.MomentumLookbackTicks)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("at %v the replay input differs from the live builder's\n got %+v\nwant %+v", o.Time, summarise(got), summarise(want))
				}
				inputs[fill][o.Time] = got
				checked++
			}
			mustRun(t, reader, cfg)
			if checked < ticks/2 {
				t.Fatalf("only %d ticks compared", checked)
			}
		})
	}
	t.Run("GIVEN the same illiquid market WHEN empty intervals have rows or not THEN the indicators differ, so the unverified assumption matters", func(t *testing.T) {
		differs := false
		for at, a := range inputs[false] {
			if b, ok := inputs[true][at]; ok && !reflect.DeepEqual(a.Indicators, b.Indicators) {
				differs = true
				break
			}
		}
		if !differs {
			t.Fatal("filling empty candles changed nothing on an illiquid market")
		}
	})
}

// summarise returns the short fields of market data for a failure message.
func summarise(md strategy.MarketData) map[string]any {
	return map[string]any{
		"price": md.Price, "previous": md.PreviousPrice, "history_len": len(md.PriceHistory),
		"indicators": md.Indicators, "htf": md.HigherTimeframe, "trend_candles": len(md.TrendCandles),
	}
}
