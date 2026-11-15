package histdata

import (
	"cmp"
	"errors"
	"slices"
	"strings"
	"time"
)

// peggedRange is the smallest high-low move over 24 h, as a fraction of the
// low, for a pair to count as tradable. It restates the live bot's rule in
// internal/kraken/top_pairs.go (minDailyRange = 0.005, applied in
// TopPairsByVolume); that package is not imported here by design.
const peggedRange = 0.005

// UniverseOptions controls Universe.
type UniverseOptions struct {
	Quote    string // quote currency, e.g. "EUR"
	Top      int    // how many markets to select
	RankBy7d bool   // rank by traded value over 7 days instead of 24 hours
}

// UniverseEntry is one candidate market.
type UniverseEntry struct {
	Market   string  `json:"market"`
	Value24h float64 `json:"value_24h"` // traded value in the quote currency, preceding 24 h
	Value7d  float64 `json:"value_7d,omitempty"`
	Range24h float64 `json:"range_24h"` // (high-low)/low over the peg-test window
}

// UniverseResult is the point-in-time market list.
type UniverseResult struct {
	At       time.Time       `json:"at"`
	Quote    string          `json:"quote"`
	RankedBy string          `json:"ranked_by"` // "24h" or "7d"
	Selected []UniverseEntry `json:"selected"`
	Pegged   []UniverseEntry `json:"pegged,omitempty"`  // excluded by the peg rule
	NoData   []string        `json:"no_data,omitempty"` // quoted in Quote but no trades in the window
}

// Universe returns the Top markets quoted in opts.Quote with the highest
// traded value in the window before at, using only cached bars that ended at
// or before at (no look-ahead). Pegged pairs — 24 h range under 0.5 % — are
// excluded as the live bot does; with RankBy7d a market that did not trade
// in the last 24 h is peg-tested over the 7 days instead. Unlike live, the
// cache has no listing status, so any market that traded counts.
func Universe(r Reader, at time.Time, opts UniverseOptions) (UniverseResult, error) {
	quote := strings.ToUpper(opts.Quote)
	res := UniverseResult{At: at, Quote: quote, RankedBy: "24h"}
	if opts.RankBy7d {
		res.RankedBy = "7d"
	}
	if quote == "" || opts.Top < 1 {
		return res, errors.New("universe needs a quote currency and a positive top-N")
	}
	var candidates []UniverseEntry
	for _, market := range r.Markets() {
		if !strings.HasSuffix(market, "/"+quote) {
			continue
		}
		day, err := windowStats(r, market, at, 24*time.Hour)
		if err != nil {
			return res, err
		}
		e := UniverseEntry{Market: market, Value24h: day.value, Range24h: day.rangeFrac()}
		pegWindow := day
		if opts.RankBy7d {
			week, err := windowStats(r, market, at, 7*24*time.Hour)
			if err != nil {
				return res, err
			}
			e.Value7d = week.value
			if day.bars == 0 {
				pegWindow = week
				e.Range24h = week.rangeFrac()
			}
		}
		switch {
		case pegWindow.bars == 0:
			res.NoData = append(res.NoData, market)
		case pegWindow.low <= 0 || pegWindow.rangeFrac() < peggedRange:
			res.Pegged = append(res.Pegged, e)
		default:
			candidates = append(candidates, e)
		}
	}
	value := func(e UniverseEntry) float64 {
		if opts.RankBy7d {
			return e.Value7d
		}
		return e.Value24h
	}
	slices.SortFunc(candidates, func(a, b UniverseEntry) int {
		if c := cmp.Compare(value(b), value(a)); c != 0 {
			return c
		}
		return strings.Compare(a.Market, b.Market)
	})
	res.Selected = candidates[:min(opts.Top, len(candidates))]
	return res, nil
}

// window summarises a market's bars over a window.
type window struct {
	bars      int
	value     float64
	high, low float64
}

// rangeFrac is (high-low)/low.
func (w window) rangeFrac() float64 {
	if w.low <= 0 {
		return 0
	}
	return (w.high - w.low) / w.low
}

// windowStats sums the bars of market that ended in [at-span, at].
func windowStats(r Reader, market string, at time.Time, span time.Duration) (window, error) {
	var w window
	for b, err := range r.Bars(market, at.Add(-span), at) {
		if err != nil {
			return w, err
		}
		if b.Start.Add(BarInterval).After(at) {
			continue
		}
		if w.bars == 0 {
			w.high, w.low = b.High, b.Low
		}
		w.high, w.low = max(w.high, b.High), min(w.low, b.Low)
		w.value += barNotional(b)
		w.bars++
	}
	return w, nil
}

// barNotional is the quote value traded in a bar: the exact side notionals,
// plus volume without a known side valued at the mean of open, high, low and
// close (an approximation used only for older archives without sides).
func barNotional(b Bar) float64 {
	unknown := max(b.Volume-b.BuyVolume-b.SellVolume, 0)
	return b.BuyNotional + b.SellNotional + unknown*(b.Open+b.High+b.Low+b.Close)/4
}
