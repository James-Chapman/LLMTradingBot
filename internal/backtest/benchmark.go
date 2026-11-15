package backtest

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/histdata"
)

// Benchmark names.
const (
	BenchmarkCash          = "cash"
	BenchmarkBuyHoldPrefix = "buy_and_hold:"
	BenchmarkEqualWeight   = "equal_weight_buy_and_hold"
)

// BenchmarkEntry is one market leg of a buy-and-hold benchmark.
type BenchmarkEntry struct {
	Market    string     `json:"market"`
	Time      *time.Time `json:"time"` // fill tick; nil when the leg never bought
	Price     float64    `json:"price"`
	Qty       float64    `json:"qty"`
	Fee       float64    `json:"fee"`
	ExitPrice float64    `json:"exit_price"`
	ExitFee   float64    `json:"exit_fee"`
	Note      string     `json:"note,omitempty"`
}

// Benchmark is a passive alternative over the same period with the same
// starting capital and the same fee, spread and valuation rules.
type Benchmark struct {
	Name            string           `json:"name"`
	Markets         []string         `json:"markets"`
	InitialCapital  float64          `json:"initial_capital"`
	FinalValue      float64          `json:"final_value"`
	NetPnL          float64          `json:"net_pnl"`
	NetReturnPct    float64          `json:"net_return_pct"`
	Fees            float64          `json:"fees"`
	MaxDrawdown     *DrawdownEpisode `json:"max_drawdown"`      // per tick, by percent
	ExcessReturnPct float64          `json:"excess_return_pct"` // strategy net % - this net %, percentage points
	Entries         []BenchmarkEntry `json:"entries"`
	Note            string           `json:"note"`

	Daily []DailyPoint `json:"-"` // end-of-day values, for the regime analysis
}

// MarketPath is a market's own behaviour over a run: its price at the start,
// at each UTC midnight (and the end), and each UTC day's realised volatility
// (the square root of the sum of squared 5-minute log returns of the tick price).
type MarketPath struct {
	Market     string       `json:"market"`
	StartPrice float64      `json:"start_price"`
	Closes     []DailyPoint `json:"closes"`
	DailyVol   []DailyPoint `json:"daily_volatility"`
}

// BenchmarkSet is the benchmarks of a run; Primary names the one the
// verdict and the regimes compare with.
type BenchmarkSet struct {
	Benchmarks []Benchmark  `json:"benchmarks"`
	Primary    string       `json:"primary"`
	Paths      []MarketPath `json:"-"`
	Notes      []string     `json:"notes"`
}

// bhLeg is one market of a buy-and-hold portfolio during the scan.
type bhLeg struct {
	market     string
	decideTick int // -1 before the first tradable tick
	done       bool
	entry      BenchmarkEntry
}

// bhPortfolio is one buy-and-hold benchmark during the scan.
type bhPortfolio struct {
	name  string
	alloc float64
	cash  float64
	fees  float64
	legs  []*bhLeg
	track *tracker
}

// pathState accumulates one market's path during the scan.
type pathState struct {
	path   MarketPath
	prev5  float64
	sumSq  float64
	sample int
}

// benchmarkMarkets returns the markets a run's benchmarks hold: per-market
// buy-and-hold for each traded market (the initial market when a
// single-market run never traded), and the initial list for equal weight.
func benchmarkMarkets(res *Result) (perMarket, initial []string) {
	initial = append([]string{}, res.InitialMarkets...)
	seen := map[string]bool{}
	for _, o := range res.Orders {
		if o.Status == StatusFilled && !seen[o.Market] {
			seen[o.Market] = true
			perMarket = append(perMarket, o.Market)
		}
	}
	sort.Strings(perMarket)
	if len(perMarket) == 0 && len(initial) == 1 {
		perMarket = initial
	}
	return perMarket, initial
}

// ComputeBenchmarks replays a run's markets without trading: buy-and-hold
// legs buy at the first tick with a price plus the run's latency, at the
// fill model's quote with the run's fee and order rules (no size impact, no
// liquidity cap), are marked at each tick's last price, and are sold at the
// end exactly as the run values its open positions. It also records each
// market's path for the regime analysis.
func ComputeBenchmarks(reader histdata.Reader, res *Result) (BenchmarkSet, error) {
	ex := res.Exchange
	rate := ex.feeRate()
	strategyPct := 0.0
	if res.StartingCapital > 0 {
		strategyPct = 100 * (res.EndingEquity - res.StartingCapital) / res.StartingCapital
	}
	set := BenchmarkSet{Notes: []string{
		"Buy-and-hold buys each market at the fill model's quote one latency after the first tick with a price, with the run's fee and order rules but no size impact or liquidity cap (slightly favourable to the benchmark), and is valued at the end like the strategy's open positions (the fill model's sell quote on the market's last bar with trades, less the fee).",
	}}
	set.Benchmarks = append(set.Benchmarks, Benchmark{
		Name: BenchmarkCash, Markets: []string{}, InitialCapital: res.StartingCapital, FinalValue: res.StartingCapital,
		ExcessReturnPct: strategyPct, Entries: []BenchmarkEntry{}, Note: "Holding the starting capital in cash: 0 % return, no interest.",
		Daily: flatDaily(res, res.StartingCapital),
	})

	perMarket, initial := benchmarkMarkets(res)
	var portfolios []*bhPortfolio
	newPortfolio := func(name string, markets []string) *bhPortfolio {
		p := &bhPortfolio{name: name, alloc: res.StartingCapital / float64(len(markets)), cash: res.StartingCapital,
			track: newTracker(res.Start, res.StartingCapital)}
		for _, m := range markets {
			p.legs = append(p.legs, &bhLeg{market: m, decideTick: -1, entry: BenchmarkEntry{Market: m}})
		}
		return p
	}
	for _, m := range perMarket {
		portfolios = append(portfolios, newPortfolio(BenchmarkBuyHoldPrefix+m, []string{m}))
	}
	if len(initial) > 1 {
		portfolios = append(portfolios, newPortfolio(BenchmarkEqualWeight, initial))
		set.Primary = BenchmarkEqualWeight
		if len(res.UniverseSelections) > 0 {
			set.Notes = append(set.Notes, "The equal-weight benchmark holds the first universe selection only; markets the point-in-time universe added later are not in it.")
		}
	} else if len(initial) == 1 {
		set.Primary = BenchmarkBuyHoldPrefix + initial[0]
	}

	markets := slices.Compact(slices.Sorted(slices.Values(append(append([]string{}, perMarket...), initial...))))
	feeds := map[string]*marketFeed{}
	paths := map[string]*pathState{}
	for _, m := range markets {
		feeds[m] = &marketFeed{market: m, spread: spreadEstimator{ring: make([]float64, ex.SpreadWindowBars)}}
		paths[m] = &pathState{path: MarketPath{Market: m, Closes: []DailyPoint{}, DailyVol: []DailyPoint{}}}
	}

	k := 0
	for tick, err := range histdata.MergeTicks(reader, markets, res.Start.Add(-PreRoll), res.End) {
		if err != nil {
			return BenchmarkSet{}, err
		}
		j := 0
		for _, m := range markets { // tick.Prices and markets are both sorted
			var mp *histdata.MarketPrice
			if j < len(tick.Prices) && tick.Prices[j].Market == m {
				mp = &tick.Prices[j]
				j++
			}
			feeds[m].setPrice(mp)
		}
		k++
		if tick.Time.Equal(res.Start) {
			for _, m := range markets {
				paths[m].path.StartPrice = feeds[m].last
			}
		}
		if !tick.Time.After(res.Start) {
			continue // pre-roll: the spread estimate and the last price only
		}
		for _, m := range markets {
			paths[m].observe(tick.Time, feeds[m].last)
		}
		for _, p := range portfolios {
			p.step(k, tick.Time, feeds, ex)
		}
	}
	for _, m := range markets {
		paths[m].finish(res.End, feeds[m].last)
		set.Paths = append(set.Paths, paths[m].path)
	}
	for _, p := range portfolios {
		b := p.finish(res, feeds, ex, rate)
		b.ExcessReturnPct = strategyPct - b.NetReturnPct
		set.Benchmarks = append(set.Benchmarks, b)
	}
	return set, nil
}

// flatDaily returns a constant daily series over a run (the cash benchmark).
func flatDaily(res *Result, value float64) []DailyPoint {
	tr := newTracker(res.Start, value)
	for _, p := range res.TickStats.Daily {
		tr.mark(p.Time, value)
	}
	tr.final(res.End, value)
	return tr.stats().Daily
}

// step advances a portfolio by one trading tick: legs decide at their first
// tick with a price, fill LatencyTicks later on a tick with trades, and the
// portfolio is marked at the last prices.
func (p *bhPortfolio) step(k int, at time.Time, feeds map[string]*marketFeed, ex ExchangeConfig) {
	rate := ex.feeRate()
	for _, l := range p.legs {
		f := feeds[l.market]
		if l.done {
			continue
		}
		if l.decideTick < 0 {
			if f.last > 0 {
				l.decideTick = k
			}
			continue
		}
		if k < l.decideTick+ex.LatencyTicks || !f.hasBar || f.bar.Volume <= 0 {
			continue
		}
		l.done = true
		rule, _ := ex.PairRules.For(l.market)
		price := roundPriceAgainst(ex.quote(true, f).price, rule.PairDecimals, true)
		qty := roundQtyDown(p.alloc/(price*(1+rate)), rule.LotDecimals)
		switch {
		case qty <= 0 || qty < rule.OrderMin:
			l.entry.Note = RejectMinOrder + ": the allocation stays in cash"
			continue
		case qty*price < rule.CostMin:
			l.entry.Note = RejectMinCost + ": the allocation stays in cash"
			continue
		}
		fill := at
		l.entry.Time, l.entry.Price, l.entry.Qty = &fill, price, qty
		l.entry.Fee = qty * price * rate
		p.cash -= qty*price + l.entry.Fee
		p.fees += l.entry.Fee
	}
	equity, open := p.cash, 0
	for _, l := range p.legs {
		if l.entry.Qty > 0 {
			equity += l.entry.Qty * feeds[l.market].last
			open++
		}
	}
	p.track.observe(at, equity, p.cash, open)
}

// finish sells every leg at the end like the run's end-of-run valuation and
// returns the benchmark.
func (p *bhPortfolio) finish(res *Result, feeds map[string]*marketFeed, ex ExchangeConfig, rate float64) Benchmark {
	b := Benchmark{Name: p.name, InitialCapital: res.StartingCapital, Entries: []BenchmarkEntry{}, Markets: []string{}}
	for _, l := range p.legs {
		b.Markets = append(b.Markets, l.market)
		if l.entry.Qty > 0 {
			rule, _ := ex.PairRules.For(l.market)
			l.entry.ExitPrice = roundPriceAgainst(ex.exitQuote(feeds[l.market]).price, rule.PairDecimals, false)
			l.entry.ExitFee = l.entry.Qty * l.entry.ExitPrice * rate
			p.cash += l.entry.Qty*l.entry.ExitPrice - l.entry.ExitFee
			p.fees += l.entry.ExitFee
		} else if l.entry.Note == "" {
			l.entry.Note = "no tick with trades to buy at: the allocation stays in cash"
		}
		b.Entries = append(b.Entries, l.entry)
	}
	p.track.final(res.End, p.cash)
	st := p.track.stats()
	b.FinalValue, b.Fees, b.MaxDrawdown, b.Daily = p.cash, p.fees, st.MaxDrawdownByPct, st.Daily
	b.NetPnL = b.FinalValue - b.InitialCapital
	if b.InitialCapital > 0 {
		b.NetReturnPct = 100 * b.NetPnL / b.InitialCapital
	}
	b.Note = fmt.Sprintf("Buy at the start and hold %d market(s) with %.6g each; one entry and one exit cost per market.", len(p.legs), p.alloc)
	return b
}

// observe records a trading tick's price for the market path.
func (s *pathState) observe(at time.Time, last float64) {
	if at.Unix()%300 == 0 && last > 0 {
		if s.prev5 > 0 {
			r := math.Log(last / s.prev5)
			s.sumSq += r * r
			s.sample++
		}
		s.prev5 = last
	}
	if at.Unix()%86400 == 0 {
		s.closeDay(at, last)
	}
}

// closeDay records the day ending at at.
func (s *pathState) closeDay(at time.Time, last float64) {
	if last > 0 {
		s.path.Closes = append(s.path.Closes, DailyPoint{Time: at, Value: last})
	}
	if s.sample > 0 {
		s.path.DailyVol = append(s.path.DailyVol, DailyPoint{Time: at, Value: math.Sqrt(s.sumSq)})
	}
	s.sumSq, s.sample = 0, 0
}

// finish closes a partial last day when the run does not end at midnight.
func (s *pathState) finish(end time.Time, last float64) {
	if end.Unix()%86400 != 0 {
		s.closeDay(end, last)
	}
}
