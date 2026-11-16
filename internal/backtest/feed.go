package backtest

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/histdata"
	"github.com/James-Chapman/LLMTradingBot/internal/marketdata"
)

// marketFeed turns one market's ticks from histdata.MergeTicks into what the
// live bot sees at each 30-second tick: the last traded price (carried
// forward when nothing traded), the price history, clock-aligned 5-minute,
// 15-minute and 4-hour candles ending with the forming one, and the rolling 24-hour
// volume. It also keeps the bar of the current tick, which prices fills
// decided on the previous tick, and the market's measured half-spread.
type marketFeed struct {
	market string

	last    float64      // last traded price; 0 before the first trade
	bar     histdata.Bar // trades of the interval that ended at this tick
	hasBar  bool
	lastBar histdata.Bar // the most recent bar with trades (bar.Start is the market's last trade interval)
	volume  float64      // base volume of the 24 hours before this tick

	c5, c15, c60, c240 *histdata.CandleAggregator
	vol                *histdata.RollingVolume
	spread             spreadEstimator
	hist               priceWindow
}

// trendInterval is the length of the 4-hour trend candles.
const trendInterval = marketdata.TrendCandleMinutes * time.Minute

// hourInterval is the length of the 1-hour candles.
const hourInterval = marketdata.HourCandleMinutes * time.Minute

// newMarketFeed returns an empty feed for market.
func newMarketFeed(market string, spreadWindow int) *marketFeed {
	c5, err5 := histdata.NewCandleAggregator(5*time.Minute, marketdata.CandleLimit)
	c15, err15 := histdata.NewCandleAggregator(15*time.Minute, marketdata.CandleLimit)
	c60, err60 := histdata.NewCandleAggregator(hourInterval, marketdata.HourCandleLimit)
	c240, err240 := histdata.NewCandleAggregator(trendInterval, marketdata.TrendCandleLimit)
	if err := errors.Join(err5, err15, err60, err240); err != nil {
		panic(err) // constant arguments: cannot fail
	}
	return &marketFeed{
		market: market, c5: c5, c15: c15, c60: c60, c240: c240,
		vol:    histdata.NewRollingVolume(24 * time.Hour),
		spread: spreadEstimator{ring: make([]float64, spreadWindow)},
		hist:   priceWindow{limit: marketdata.PriceHistoryTicks},
	}
}

// observe moves the feed to tick, taking in the market's price there (nil
// before its first trade). A bar is fed to the candles and the volume only
// at its own tick, so nothing later than the tick is ever seen.
func (f *marketFeed) observe(tick time.Time, mp *histdata.MarketPrice) error {
	f.setPrice(mp)
	if f.hasBar {
		if err := errors.Join(f.c5.Add(f.bar), f.c15.Add(f.bar), f.c60.Add(f.bar), f.c240.Add(f.bar)); err != nil {
			return fmt.Errorf("%s: %w", f.market, err)
		}
		f.vol.Add(f.bar)
	}
	// Queried every tick so the volume window never buffers more than 24 h.
	f.volume = f.vol.At(tick)
	return nil
}

// setPrice takes in the market's price at a tick (nil before its first
// trade): the last price, this tick's bar, the last bar with trades and the
// spread estimate. The benchmarks use it without the candles and volume.
func (f *marketFeed) setPrice(mp *histdata.MarketPrice) {
	f.hasBar = false
	if mp == nil {
		return
	}
	f.last = mp.Price
	if mp.HasBar {
		f.bar, f.hasBar, f.lastBar = mp.Bar, true, mp.Bar
		f.spread.add(mp.Bar)
	}
}

// staleAt reports whether the market's last trade is more than after
// before tick; a market that never traded is not stale (nothing can be held).
func (f *marketFeed) staleAt(tick time.Time, after time.Duration) bool {
	return f.last > 0 && tick.Sub(f.lastBar.Start.Add(histdata.BarInterval)) > after
}

// exitQuote prices selling the whole of a position at the end of a run: the
// fill model's sell quote on the market's last bar with trades.
func (x ExchangeConfig) exitQuote(f *marketFeed) fillQuote {
	b := f.lastBar
	if b.Close <= 0 {
		b = histdata.Bar{Close: f.last}
	}
	return x.quoteBar(false, f, b)
}

// volume24h returns the rolling 24-hour base volume as the strategy sees it.
func (f *marketFeed) volume24h() *float64 {
	v := f.volume
	return &v
}

// candles returns the candles live would build from Kraken's OHLC rows at
// tick: the aggregator's candles (intervals without trades have none) and,
// because Kraken always returns the forming row, a flat candle at the last
// close with zero volume when the interval holding tick has had no trade.
// With fillEmpty, every interval without trades between the first candle and
// tick gets such a flat candle too (the --fill-empty-candles sensitivity:
// whether Kraken's REST OHLC returns rows for empty intervals is not verified).
// At most limit candles, the newest, are returned (the live loop's window).
func candles(agg *histdata.CandleAggregator, tick time.Time, interval time.Duration, limit int, fillEmpty bool) []domain.Candle {
	rows, forming := agg.Candles(tick)
	out := make([]domain.Candle, 0, len(rows)+1)
	step := int64(interval / time.Second)
	current := tick.Truncate(interval).Unix()
	// Only the newest limit intervals can be returned, so flat candles are
	// only generated from there on (an illiquid market's 100 traded candles
	// can span weeks).
	earliest := current - int64(limit-1)*step
	var prev domain.Candle // the newest candle so far, kept or not
	hasPrev := false
	fill := func(upTo int64) {
		for t := prev.T + step; t < upTo; t += step {
			if t < earliest {
				t = earliest - step // jump to the first interval that can be returned
				continue
			}
			prev = domain.Candle{T: t, O: prev.C, H: prev.C, L: prev.C, C: prev.C}
			out = append(out, prev)
		}
	}
	for _, c := range rows {
		t := c.Start.Unix()
		if fillEmpty && hasPrev {
			fill(t)
		}
		prev, hasPrev = domain.Candle{T: t, O: c.Open, H: c.High, L: c.Low, C: c.Close, V: c.Volume}, true
		out = append(out, prev)
	}
	if hasPrev && !forming {
		if fillEmpty {
			fill(current)
		}
		last := prev.C
		out = append(out, domain.Candle{T: current, O: last, H: last, L: last, C: last})
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// priceWindow is the rolling price history, trimmed like the live loop's.
type priceWindow struct {
	limit int
	buf   []float64
}

// push appends a tick price.
func (w *priceWindow) push(p float64) {
	if len(w.buf) >= 2*w.limit {
		fresh := make([]float64, w.limit, 2*w.limit+1)
		copy(fresh, w.buf[len(w.buf)-w.limit:])
		w.buf = fresh
	}
	w.buf = append(w.buf, p)
}

// window returns the newest limit prices, oldest first.
func (w *priceWindow) window() []float64 {
	if len(w.buf) > w.limit {
		return w.buf[len(w.buf)-w.limit:]
	}
	return w.buf
}

// len returns how many prices the window holds.
func (w *priceWindow) len() int { return min(len(w.buf), w.limit) }

// spreadEstimator keeps the half-spread measured on recent bars that had
// trades on both sides: (buy average - sell average) / 2, relative to their
// midpoint.
type spreadEstimator struct {
	ring  []float64
	n     int // samples held
	next  int
	works []float64
}

// add records a bar's measured half-spread when both sides traded.
func (e *spreadEstimator) add(b histdata.Bar) {
	if b.BuyVolume <= 0 || b.SellVolume <= 0 {
		return
	}
	buy, sell := b.BuyNotional/b.BuyVolume, b.SellNotional/b.SellVolume
	mid := (buy + sell) / 2
	if !(mid > 0) {
		return
	}
	e.ring[e.next] = (buy - sell) / 2 / mid
	e.next = (e.next + 1) % len(e.ring)
	e.n = min(e.n+1, len(e.ring))
}

// median returns the median measured half-spread; ok is false before any sample.
func (e *spreadEstimator) median() (float64, bool) {
	if e.n == 0 {
		return 0, false
	}
	e.works = append(e.works[:0], e.ring[:e.n]...)
	sort.Float64s(e.works)
	mid := e.n / 2
	if e.n%2 == 1 {
		return e.works[mid], true
	}
	return (e.works[mid-1] + e.works[mid]) / 2, true
}
