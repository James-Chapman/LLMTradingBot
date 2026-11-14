package backtest

import (
	"context"
	"iter"
	"math"
	"math/rand/v2"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/histdata"
	"github.com/James-Chapman/LLMTradingBot/internal/observability"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy"
)

// TestMain silences the engines' per-tick INFO logs so replays stay fast.
func TestMain(m *testing.M) {
	observability.SetLevel("ERROR")
	os.Exit(m.Run())
}

// memReader is an in-memory histdata.Reader over fixed bars.
type memReader struct {
	bars map[string][]histdata.Bar // per market, in time order
}

// Markets lists the markets held, sorted.
func (r memReader) Markets() []string {
	out := make([]string, 0, len(r.bars))
	for m := range r.bars {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// Coverage returns the first and last bar start of market.
func (r memReader) Coverage(market string) (time.Time, time.Time, bool) {
	bars := r.bars[market]
	if len(bars) == 0 {
		return time.Time{}, time.Time{}, false
	}
	return bars[0].Start, bars[len(bars)-1].Start, true
}

// Bars streams market's bars with from <= Start < to.
func (r memReader) Bars(market string, from, to time.Time) iter.Seq2[histdata.Bar, error] {
	return func(yield func(histdata.Bar, error) bool) {
		for _, b := range r.bars[market] {
			if b.Start.Before(from) || !b.Start.Before(to) {
				continue
			}
			if !yield(b, nil) {
				return
			}
		}
	}
}

// t0 is the start of every test series.
var t0 = time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)

// tick returns the time of the i-th tick after t0 (tick 1 is t0+30s).
func tick(i int) time.Time { return t0.Add(time.Duration(i) * histdata.BarInterval) }

// flatBar returns a bar of one price with both taker sides present: buyers
// pay price×(1+hs) and sellers receive price×(1-hs), volume base units each side.
func flatBar(start time.Time, price, hs, volume float64) histdata.Bar {
	half := volume / 2
	return histdata.Bar{
		Start: start, Open: price, High: price, Low: price, Close: price,
		Volume: volume, Trades: 10,
		BuyVolume: half, BuyNotional: half * price * (1 + hs),
		SellVolume: half, SellNotional: half * price * (1 - hs),
	}
}

// flatSeries returns n flat bars at price starting at t0.
func flatSeries(n int, price float64) []histdata.Bar {
	bars := make([]histdata.Bar, n)
	for i := range bars {
		bars[i] = flatBar(t0.Add(time.Duration(i)*histdata.BarInterval), price, 0.001, 1000)
	}
	return bars
}

// randomWalkBars returns n bars of a seeded geometric random walk with
// per-bar volatility sigma, trades on both sides, and occasional empty bars.
func randomWalkBars(seed uint64, n int, start time.Time, price, sigma float64) []histdata.Bar {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	bars := make([]histdata.Bar, 0, n)
	for i := 0; i < n; i++ {
		open := price
		price *= math.Exp(sigma * rng.NormFloat64())
		if rng.IntN(20) == 0 {
			continue // no trades this interval
		}
		b := flatBar(start.Add(time.Duration(i)*histdata.BarInterval), price, 0.0004, 5+rng.Float64()*20)
		b.Open = open
		b.High = math.Max(open, price) * (1 + 0.3*sigma*rng.Float64())
		b.Low = math.Min(open, price) * (1 - 0.3*sigma*rng.Float64())
		bars = append(bars, b)
	}
	return bars
}

// scripted is a test strategy that emits planned ideas at given tick times.
// The replay's observe hook keeps now at the current tick.
type scripted struct {
	now  time.Time
	plan map[time.Time][]domain.TradeIdea
}

// ID returns the scripted strategy id.
func (s *scripted) ID() string { return "scripted" }

// UsesLLM is false.
func (s *scripted) UsesLLM() bool { return false }

// RequiredPriceTicks is 0.
func (s *scripted) RequiredPriceTicks() int { return 0 }

// Evaluate returns the ideas planned for the current tick.
func (s *scripted) Evaluate(_ context.Context, _ strategy.Input) []domain.TradeIdea {
	return append([]domain.TradeIdea(nil), s.plan[s.now]...)
}

// at plans an idea for market and direction at tick time when.
func (s *scripted) at(when time.Time, market string, d domain.Direction) *scripted {
	if s.plan == nil {
		s.plan = map[time.Time][]domain.TradeIdea{}
	}
	s.plan[when] = append(s.plan[when], domain.TradeIdea{
		StrategyID: "scripted", Market: market, Direction: d, Confidence: 0.9, PositionSizingProposal: 0.1,
	})
	return s
}

// testSettings returns the bot defaults used by replay tests.
func testSettings() config.Settings {
	s := config.Defaults()
	s.StartingCapital = 1000
	return s
}

// testConfig returns a replay config over the given markets and tick range.
func testConfig(markets []string, ticks int) Config {
	return Config{
		Markets:    markets,
		Start:      t0,
		End:        tick(ticks),
		StrategyID: strategy.BasicID,
		Settings:   testSettings(),
		Exchange:   DefaultExchange(),
	}
}

// withScript makes cfg run s instead of a registry strategy, keeping s's
// clock at the tick being evaluated, and records every observation.
func withScript(cfg Config, s *scripted, obs *[]TickObservation) Config {
	cfg.strategy = s
	cfg.observe = func(o TickObservation) {
		s.now = o.Time
		if obs != nil {
			*obs = append(*obs, o)
		}
	}
	return cfg
}

// mustRun runs a replay and fails the test on error.
func mustRun(t *testing.T, r histdata.Reader, cfg Config) *Result {
	t.Helper()
	res, err := Run(context.Background(), r, cfg)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return res
}

// ordersWhere returns the logged orders matching keep.
func ordersWhere(res *Result, keep func(OrderLogEntry) bool) []OrderLogEntry {
	var out []OrderLogEntry
	for _, o := range res.Orders {
		if keep(o) {
			out = append(out, o)
		}
	}
	return out
}

// near reports whether a and b agree to within tol.
func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }
