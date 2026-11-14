package histdata

import (
	"errors"
	"fmt"
	"iter"
	"slices"
	"time"
)

// Tick is the market state at one 30-second strategy boundary.
type Tick struct {
	// Time is the boundary: the instant the live bot would sample prices.
	// Every price is the last trade strictly before Time.
	Time time.Time
	// Prices holds every requested market that has traded at least once in
	// the merged range, sorted by market.
	Prices []MarketPrice
}

// MarketPrice is one market's price at a tick.
type MarketPrice struct {
	Market string
	Price  float64 // the bar's close, or the last close carried forward
	HasBar bool    // true when the market traded in [Time-30s, Time)
	Bar    Bar     // that interval's bar when HasBar
}

// MergeTicks merges several markets' bars into one time-ordered stream with a
// tick at every 30-second boundary T in (from, to], from rounded up to the
// bar grid. The tick at T carries the bar starting at T-30s for each market
// that traded then, and the carried-forward last close for each market that
// traded earlier in the range. Ticks are produced even when no market has a
// price yet, so time advances as it does live. A read error ends the stream.
func MergeTicks(r Reader, markets []string, from, to time.Time) iter.Seq2[Tick, error] {
	return func(yield func(Tick, error) bool) {
		names := slices.Compact(slices.Sorted(slices.Values(markets)))
		type source struct {
			market  string
			next    func() (Bar, error, bool)
			pending Bar
			has     bool
			price   float64
			priced  bool
		}
		srcs := make([]*source, 0, len(names))
		advance := func(s *source) error {
			b, err, ok := s.next()
			if err != nil {
				return fmt.Errorf("%s: %w", s.market, err)
			}
			s.pending, s.has = b, ok
			return nil
		}
		for _, m := range names {
			next, stop := iter.Pull2(r.Bars(m, from, to))
			defer stop()
			s := &source{market: m, next: next}
			if err := advance(s); err != nil {
				yield(Tick{}, err)
				return
			}
			srcs = append(srcs, s)
		}
		start := from.Truncate(BarInterval)
		if start.Before(from) {
			start = start.Add(BarInterval)
		}
		for s := start; !s.Add(BarInterval).After(to); s = s.Add(BarInterval) {
			tick := Tick{Time: s.Add(BarInterval)}
			for _, src := range srcs {
				mp := MarketPrice{Market: src.market}
				if src.has && src.pending.Start.Before(s) {
					yield(Tick{}, fmt.Errorf("%s: bar at %v is out of order or misaligned", src.market, src.pending.Start))
					return
				}
				if src.has && src.pending.Start.Equal(s) {
					mp.Bar, mp.HasBar = src.pending, true
					src.price, src.priced = src.pending.Close, true
					if err := advance(src); err != nil {
						yield(Tick{}, err)
						return
					}
				}
				if src.priced {
					mp.Price = src.price
					tick.Prices = append(tick.Prices, mp)
				}
			}
			if !yield(tick, nil) {
				return
			}
		}
	}
}

// CandleAggregator maintains clock-aligned N-minute candles from bars fed in
// time order, the way Kraken's OHLC endpoint presents them live: closed
// candles followed by the still-forming one. Intervals without trades
// produce no candle, as in Kraken's data.
type CandleAggregator struct {
	interval time.Duration
	keep     int
	candles  []Candle
	last     time.Time
	started  bool
}

// NewCandleAggregator returns an aggregator of interval candles (a positive
// multiple of BarInterval, e.g. 5 or 15 minutes) that keeps the most recent
// keep candles, the forming one included (the live bot keeps 100).
func NewCandleAggregator(interval time.Duration, keep int) (*CandleAggregator, error) {
	if interval <= 0 || interval%BarInterval != 0 {
		return nil, fmt.Errorf("candle interval %v is not a positive multiple of %v", interval, BarInterval)
	}
	if keep < 1 {
		return nil, errors.New("keep must be at least 1")
	}
	return &CandleAggregator{interval: interval, keep: keep}, nil
}

// Add folds the next bar into its candle. Bars must arrive in strictly
// increasing Start order; feed a bar only once it is complete (at tick
// Start+30s) so the forming candle never includes the future.
func (a *CandleAggregator) Add(b Bar) error {
	if a.started && !b.Start.After(a.last) {
		return fmt.Errorf("bar at %v does not follow %v", b.Start, a.last)
	}
	a.last, a.started = b.Start, true
	start := b.Start.Truncate(a.interval)
	if n := len(a.candles); n > 0 && a.candles[n-1].Start.Equal(start) {
		c := &a.candles[n-1]
		c.High, c.Low = max(c.High, b.High), min(c.Low, b.Low)
		c.Close = b.Close
		c.Volume += b.Volume
		c.Trades += b.Trades
		return nil
	}
	a.candles = append(a.candles, Candle{Start: start, Open: b.Open, High: b.High, Low: b.Low, Close: b.Close, Volume: b.Volume, Trades: b.Trades})
	if len(a.candles) > a.keep {
		a.candles = slices.Delete(a.candles, 0, len(a.candles)-a.keep)
	}
	return nil
}

// Candles returns a copy of the kept candles, oldest first, as seen at time
// at. forming is true when the last candle's interval has not ended by at
// (it is the in-progress candle Kraken returns as its last row).
func (a *CandleAggregator) Candles(at time.Time) (candles []Candle, forming bool) {
	out := slices.Clone(a.candles)
	if n := len(out); n > 0 {
		forming = out[n-1].Start.Add(a.interval).After(at)
	}
	return out, forming
}

// RollingVolume is a market's traded base volume over a trailing window (24 h
// for the live ticker's rolling volume), fed bars in time order.
type RollingVolume struct {
	window  time.Duration
	bars    []Bar
	sum     float64
	removed int
}

// NewRollingVolume returns an empty rolling volume over window.
func NewRollingVolume(window time.Duration) *RollingVolume {
	return &RollingVolume{window: window}
}

// Add appends the next bar (bars must be in time order).
func (v *RollingVolume) Add(b Bar) {
	v.bars = append(v.bars, b)
	v.sum += b.Volume
}

// At returns the volume of bars that ended within (t-window, t], i.e. with
// t-window <= Start and Start+30s <= t. Bars older than the window are
// dropped, so successive calls must not go back in time.
func (v *RollingVolume) At(t time.Time) float64 {
	cutoff := t.Add(-v.window)
	drop := 0
	for drop < len(v.bars) && v.bars[drop].Start.Before(cutoff) {
		v.sum -= v.bars[drop].Volume
		drop++
	}
	if drop > 0 {
		v.bars = slices.Delete(v.bars, 0, drop)
		v.removed += drop
	}
	if len(v.bars) == 0 {
		v.sum = 0
	} else if v.removed >= 4096 {
		// Re-sum now and then so subtraction rounding cannot accumulate.
		v.sum, v.removed = 0, 0
		for _, b := range v.bars {
			v.sum += b.Volume
		}
	}
	total := v.sum
	for i := len(v.bars) - 1; i >= 0 && v.bars[i].Start.Add(BarInterval).After(t); i-- {
		total -= v.bars[i].Volume // still in progress at t
	}
	return max(total, 0)
}
