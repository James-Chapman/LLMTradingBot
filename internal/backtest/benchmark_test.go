package backtest

import (
	"math"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/histdata"
)

// preRolled returns n flat bars at price starting a pre-roll before t0, so
// the market has a price (and a measured spread) at the start.
func preRolled(n int, price float64) []histdata.Bar {
	from := t0.Add(-PreRoll)
	pre := int(PreRoll / histdata.BarInterval)
	bars := make([]histdata.Bar, pre+n)
	for i := range bars {
		bars[i] = flatBar(from.Add(time.Duration(i)*histdata.BarInterval), price, 0.001, 1000)
	}
	return bars
}

// benchmarkNamed returns the benchmark called name.
func benchmarkNamed(t *testing.T, set BenchmarkSet, name string) Benchmark {
	t.Helper()
	for _, b := range set.Benchmarks {
		if b.Name == name {
			return b
		}
	}
	t.Fatalf("no benchmark %q in %+v", name, set.Benchmarks)
	return Benchmark{}
}

// TestBenchmarks covers the cash and buy-and-hold benchmarks.
func TestBenchmarks(t *testing.T) {
	t.Run("GIVEN a flat market WHEN buy-and-hold is computed THEN it pays one entry and one exit cost at the strategy's fill and valuation rules", func(t *testing.T) {
		bars := preRolled(200, 100)
		cfg := testConfig([]string{"BTC/EUR"}, 199)
		s := (&scripted{}).at(tick(40), "BTC/EUR", domain.Long) // held to the end
		res := mustRun(t, oneMarket(bars), withScript(cfg, s, nil))
		set, err := ComputeBenchmarks(oneMarket(bars), res)
		if err != nil {
			t.Fatal(err)
		}
		bh := benchmarkNamed(t, set, BenchmarkBuyHoldPrefix+"BTC/EUR")

		b := bars[len(bars)-1]
		buy := b.BuyNotional / b.BuyVolume // the taker-buy average the spread model fills at
		sell := b.SellNotional / b.SellVolume
		hs := (buy - sell) / 2 / ((buy + sell) / 2)
		rate := res.Exchange.FeeBps / 1e4
		qty := roundQtyDown(1000/(buy*(1+rate)), FallbackPairRule.LotDecimals)
		exit := 100 * (1 - hs)
		want := 1000 - qty*buy - qty*buy*rate + qty*exit - qty*exit*rate
		if len(bh.Entries) != 1 || !near(bh.Entries[0].Price, buy, 1e-12) || !near(bh.Entries[0].Qty, qty, 1e-15) {
			t.Fatalf("entries %+v, want one buy of %v at %v", bh.Entries, qty, buy)
		}
		if !near(bh.FinalValue, want, 1e-9) || !near(bh.Fees, qty*buy*rate+qty*exit*rate, 1e-9) {
			t.Fatalf("final %v fees %v, want %v and one entry plus one exit fee", bh.FinalValue, bh.Fees, want)
		}
		if !near(bh.Entries[0].ExitPrice, exit, 1e-12) {
			t.Fatalf("exit price %v, want %v", bh.Entries[0].ExitPrice, exit)
		}
		var endSell OrderLogEntry
		for _, o := range res.Orders {
			if o.Reason == ReasonEndOfRun {
				endSell = o
			}
		}
		if endSell.ID == "" || endSell.FillPrice != bh.Entries[0].ExitPrice {
			t.Fatalf("the strategy's end-of-run valuation %v and the benchmark's exit %v must use the same marking", endSell.FillPrice, bh.Entries[0].ExitPrice)
		}
		if bh.MaxDrawdown == nil || bh.MaxDrawdown.Amount <= 0 {
			t.Fatalf("the entry and exit costs are a drawdown on a flat market: %+v", bh.MaxDrawdown)
		}
		if !near(bh.ExcessReturnPct, 100*(res.EndingEquity-1000)/1000-bh.NetReturnPct, 1e-12) {
			t.Fatalf("excess return %v", bh.ExcessReturnPct)
		}
		if cash := benchmarkNamed(t, set, BenchmarkCash); cash.FinalValue != 1000 || cash.NetReturnPct != 0 {
			t.Fatalf("cash benchmark %+v", cash)
		}
		if set.Primary != BenchmarkBuyHoldPrefix+"BTC/EUR" {
			t.Fatalf("primary %q", set.Primary)
		}
	})

	t.Run("GIVEN two markets WHEN the equal-weight benchmark is computed THEN half the capital buys each market at the start", func(t *testing.T) {
		reader := memReader{bars: map[string][]histdata.Bar{"BTC/EUR": preRolled(200, 100), "ETH/EUR": preRolled(200, 50)}}
		res := mustRun(t, reader, withScript(testConfig([]string{"BTC/EUR", "ETH/EUR"}, 199), &scripted{}, nil))
		set, err := ComputeBenchmarks(reader, res)
		if err != nil {
			t.Fatal(err)
		}
		ew := benchmarkNamed(t, set, BenchmarkEqualWeight)
		if len(ew.Entries) != 2 || set.Primary != BenchmarkEqualWeight {
			t.Fatalf("equal weight %+v primary %q", ew, set.Primary)
		}
		for _, e := range ew.Entries {
			if spent := e.Qty*e.Price + e.Fee; spent > 500 || spent < 499.99 || !e.Time.Equal(tick(2)) {
				t.Fatalf("entry %+v spends %v of a 500 allocation at %v", e, spent, e.Time)
			}
		}
		if ew.FinalValue >= 1000 || ew.FinalValue < 980 {
			t.Fatalf("flat markets lose exactly the entry and exit costs, got %v", ew.FinalValue)
		}
	})

	t.Run("GIVEN a run WHEN the market paths are scanned THEN each day's close and realised volatility are recorded", func(t *testing.T) {
		n := 2*2880 + 10
		bars := preRolled(n, 100)
		for i := range bars {
			if bars[i].Start.After(t0.Add(24 * time.Hour)) {
				bars[i] = flatBar(bars[i].Start, 110, 0.001, 1000)
			}
		}
		cfg := testConfig([]string{"BTC/EUR"}, 2*2880)
		res := mustRun(t, oneMarket(bars), withScript(cfg, &scripted{}, nil))
		set, err := ComputeBenchmarks(oneMarket(bars), res)
		if err != nil {
			t.Fatal(err)
		}
		p := set.Paths[0]
		if p.StartPrice != 100 || len(p.Closes) != 2 || p.Closes[0].Value != 100 || p.Closes[1].Value != 110 {
			t.Fatalf("path %+v", p)
		}
		if len(p.DailyVol) != 2 || p.DailyVol[0].Value != 0 || !near(p.DailyVol[1].Value, math.Log(1.1), 1e-12) {
			t.Fatalf("daily volatility %+v", p.DailyVol)
		}
	})
}
