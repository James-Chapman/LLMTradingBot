package backtest

import (
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/histdata"
)

// Ticks used by the fill tests: the strategy first evaluates at tick 35
// (warm-up), so ideas are planned from tick 40. A decision at tick k fills on
// the bar with Start = tick(k), i.e. bars[k], reported at tick k+1.
const (
	entryTick = 40
	exitTick  = 50
)

// buyAt sets the taker-buy average price of bar b to p.
func buyAt(b *histdata.Bar, p float64) { b.BuyNotional = b.BuyVolume * p }

// sellAt sets the taker-sell average price of bar b to p.
func sellAt(b *histdata.Bar, p float64) { b.SellNotional = b.SellVolume * p }

// oneMarket returns a reader holding bars for BTC/EUR.
func oneMarket(bars []histdata.Bar) memReader {
	return memReader{bars: map[string][]histdata.Bar{"BTC/EUR": bars}}
}

// TestSimulatedExchangeFills covers how the simulated exchange prices,
// sizes and rejects orders.
func TestSimulatedExchangeFills(t *testing.T) {
	t.Run("GIVEN a long decided at a tick WHEN it fills THEN it fills on the next bar's taker-buy average, never at the decision tick", func(t *testing.T) {
		bars := flatSeries(80, 100)
		buyAt(&bars[entryTick], 101)
		s := (&scripted{}).at(tick(entryTick), "BTC/EUR", domain.Long)
		res := mustRun(t, oneMarket(bars), withScript(testConfig([]string{"BTC/EUR"}, 79), s, nil))

		buys := ordersWhere(res, func(o OrderLogEntry) bool { return o.Side == SideBuy })
		if len(buys) != 1 || buys[0].Status != StatusFilled {
			t.Fatalf("want one filled buy, got %+v", buys)
		}
		b := buys[0]
		if !b.DecisionTime.Equal(tick(entryTick)) || !b.FillTime.Equal(tick(entryTick+1)) {
			t.Fatalf("decided %v filled %v, want %v then %v", b.DecisionTime, b.FillTime, tick(entryTick), tick(entryTick+1))
		}
		if b.ReferencePrice != 100 || b.FillPrice != 101 || b.PriceSource != SourceTakerSide {
			t.Fatalf("reference %v fill %v source %q, want 100, 101 from the taker side", b.ReferencePrice, b.FillPrice, b.PriceSource)
		}
		if !near(b.SpreadCost+b.ImpactCost+b.LatencyDrift+b.RoundingCost, b.FillPrice-b.ReferencePrice, 1e-9) {
			t.Fatalf("cost components %+v do not add up to fill - reference", b)
		}
	})

	t.Run("GIVEN a long whose price gaps through the stop WHEN the stop triggers THEN it sells on the bar after the breach at that bar's price, worse than the stop", func(t *testing.T) {
		bars := flatSeries(90, 100)
		for i := 60; i < len(bars); i++ {
			bars[i] = flatBar(bars[i].Start, 94, 0.001, 1000)
		}
		bars[61] = flatBar(bars[61].Start, 90.09, 0.001, 1000)
		sellAt(&bars[61], 90)
		s := (&scripted{}).at(tick(entryTick), "BTC/EUR", domain.Long)
		res := mustRun(t, oneMarket(bars), withScript(testConfig([]string{"BTC/EUR"}, 89), s, nil))

		sells := ordersWhere(res, func(o OrderLogEntry) bool { return o.Side == SideSell && o.Status == StatusFilled })
		if len(sells) != 1 {
			t.Fatalf("want one filled sell, got %+v", sells)
		}
		x := sells[0]
		// bars[60] (close 94) is seen at tick 61; the exit prices on bars[61].
		if x.Reason != ReasonStopLoss || !x.DecisionTime.Equal(tick(61)) || !x.FillTime.Equal(tick(62)) {
			t.Fatalf("exit %+v, want a stop_loss decided at tick 61 and filled at tick 62", x)
		}
		stopLevel := 100.1 * (1 - testSettings().StopLossPct)
		if x.FillPrice != 90 || x.FillPrice >= stopLevel {
			t.Fatalf("stop filled at %v, want the gapped 90 (stop level %v)", x.FillPrice, stopLevel)
		}
	})

	t.Run("GIVEN a round trip WHEN the bars have both taker sides THEN the buy pays the buy side and the sell receives the sell side", func(t *testing.T) {
		s := (&scripted{}).at(tick(entryTick), "BTC/EUR", domain.Long).at(tick(exitTick), "BTC/EUR", domain.Short)
		res := mustRun(t, oneMarket(flatSeries(80, 100)), withScript(testConfig([]string{"BTC/EUR"}, 79), s, nil))
		filled := ordersWhere(res, func(o OrderLogEntry) bool { return o.Status == StatusFilled && o.Reason == ReasonSignal })
		if len(filled) != 2 || !near(filled[0].FillPrice, 100.1, 1e-9) || !near(filled[1].FillPrice, 99.9, 1e-9) {
			t.Fatalf("want a buy at 100.1 and a sell at 99.9, got %+v", filled)
		}
		if filled[1].Side != SideSell || filled[1].PositionID != filled[0].PositionID {
			t.Fatalf("the sell must close the bought position: %+v", filled)
		}
	})

	t.Run("GIVEN a fill bar with taker sells only WHEN a long fills THEN it pays the sell average plus the full measured spread, not the close plus half of it", func(t *testing.T) {
		bars := flatSeries(80, 100)
		bars[entryTick] = sellsOnly(flatBar(bars[entryTick].Start, 102, 0.001, 1000)) // every trade hit the bid at 101.898
		s := (&scripted{}).at(tick(entryTick), "BTC/EUR", domain.Long)
		res := mustRun(t, oneMarket(bars), withScript(testConfig([]string{"BTC/EUR"}, 79), s, nil))
		b := ordersWhere(res, func(o OrderLogEntry) bool { return o.Side == SideBuy })[0]
		// Every earlier bar measured a 10 bp half-spread, above the 5 bp floor:
		// the ask is the bid plus the full 20 bp spread.
		bid := 102 * 0.999
		if b.PriceSource != SourceOtherSideSpread || !near(b.FillPrice, bid*1.002, 1e-9) || !near(b.HalfSpread, 0.001, 1e-12) {
			t.Fatalf("fill %+v, want the 101.898 bid plus 2 × 10 bp", b)
		}
		if !near(b.FillBarMid, bid*1.001, 1e-9) || !near(b.SpreadCost, b.FillPrice-b.FillBarMid, 1e-12) {
			t.Fatalf("fill %+v: the spread cost must be measured from the estimated mid %v", b, bid*1.001)
		}
	})

	t.Run("GIVEN a fill bar with taker buys only WHEN a long is sold THEN it receives the buy average less the full measured spread", func(t *testing.T) {
		bars := flatSeries(80, 100)
		bars[exitTick] = buysOnly(flatBar(bars[exitTick].Start, 100, 0.001, 1000)) // every trade lifted the ask at 100.1
		s := (&scripted{}).at(tick(entryTick), "BTC/EUR", domain.Long).at(tick(exitTick), "BTC/EUR", domain.Short)
		res := mustRun(t, oneMarket(bars), withScript(testConfig([]string{"BTC/EUR"}, 79), s, nil))
		x := ordersWhere(res, func(o OrderLogEntry) bool { return o.Side == SideSell && o.Reason == ReasonSignal })[0]
		if x.PriceSource != SourceOtherSideSpread || !near(x.FillPrice, 100.1*0.998, 1e-9) {
			t.Fatalf("sell %+v, want the 100.1 ask less 2 × 10 bp", x)
		}
	})

	t.Run("GIVEN a fill bar whose taker sides are unknown WHEN a long fills THEN it pays the close plus the measured half-spread", func(t *testing.T) {
		bars := flatSeries(80, 100)
		bars[entryTick] = flatBar(bars[entryTick].Start, 102, 0.001, 1000)
		bars[entryTick].BuyVolume, bars[entryTick].BuyNotional, bars[entryTick].SellVolume, bars[entryTick].SellNotional = 0, 0, 0, 0
		s := (&scripted{}).at(tick(entryTick), "BTC/EUR", domain.Long)
		res := mustRun(t, oneMarket(bars), withScript(testConfig([]string{"BTC/EUR"}, 79), s, nil))
		b := ordersWhere(res, func(o OrderLogEntry) bool { return o.Side == SideBuy })[0]
		if b.PriceSource != SourceMeasuredSpread || !near(b.FillPrice, 102*1.001, 1e-9) || !near(b.HalfSpread, 0.001, 1e-12) {
			t.Fatalf("fill %+v, want close 102 plus a 10 bp measured half-spread", b)
		}
	})

	t.Run("GIVEN a two-sided bar in which buyers paid less on average than sellers received WHEN a long fills THEN it never pays less than the sell average", func(t *testing.T) {
		bars := flatSeries(80, 100)
		buyAt(&bars[entryTick], 100.2)
		sellAt(&bars[entryTick], 100.5) // the price rose inside the bar
		s := (&scripted{}).at(tick(entryTick), "BTC/EUR", domain.Long)
		b := ordersWhere(mustRun(t, oneMarket(bars), withScript(testConfig([]string{"BTC/EUR"}, 79), s, nil)), func(o OrderLogEntry) bool { return o.Side == SideBuy })[0]
		if !near(b.FillPrice, 100.5, 1e-9) {
			t.Fatalf("fill %+v, want the 100.5 sell average", b)
		}
	})

	t.Run("GIVEN the fixed fill model WHEN a long fills THEN it pays the next close plus the fixed half-spread", func(t *testing.T) {
		cfg := testConfig([]string{"BTC/EUR"}, 79)
		cfg.Exchange.FillModel = FillFixed
		cfg.Exchange.FixedHalfSpreadBps = 20
		bars := flatSeries(80, 100)
		bars[entryTick] = flatBar(bars[entryTick].Start, 103, 0.001, 1000)
		s := (&scripted{}).at(tick(entryTick), "BTC/EUR", domain.Long)
		b := ordersWhere(mustRun(t, oneMarket(bars), withScript(cfg, s, nil)), func(o OrderLogEntry) bool { return o.Side == SideBuy })[0]
		if b.PriceSource != SourceFixedSpread || !near(b.FillPrice, 103*1.002, 1e-9) {
			t.Fatalf("fill %+v, want 103 plus 20 bp", b)
		}
	})

	t.Run("GIVEN an order above the impact threshold of the bar's taker-buy value WHEN it fills THEN impact is added in proportion to the excess", func(t *testing.T) {
		bars := flatSeries(80, 100)
		bars[entryTick] = flatBar(bars[entryTick].Start, 100, 0.001, 10) // 500.5 EUR bought by takers; the order is 100.1 EUR
		s := (&scripted{}).at(tick(entryTick), "BTC/EUR", domain.Long)
		b := ordersWhere(mustRun(t, oneMarket(bars), withScript(testConfig([]string{"BTC/EUR"}, 79), s, nil)), func(o OrderLogEntry) bool { return o.Side == SideBuy })[0]
		participation := 100.1 / 500.5
		wantImpact := 100.1 * DefaultExchange().ImpactBpsPerUnit / 1e4 * (participation - DefaultExchange().ImpactThreshold)
		if b.Status != StatusFilled || !near(b.Participation, participation, 1e-12) || !near(b.ImpactCost, wantImpact, 1e-9) || !near(b.FillPrice, 100.1+wantImpact, 1e-9) {
			t.Fatalf("fill %+v, want participation %v and impact %v", b, participation, wantImpact)
		}
	})

	t.Run("GIVEN a buy worth 15 % of the bar's two sides but 30 % of its taker buys WHEN it would fill THEN participation is measured against the buy side and the default 0.25 cap rejects it", func(t *testing.T) {
		if DefaultExchange().LiquidityCap != 0.25 {
			t.Fatalf("default liquidity cap %v, want 0.25", DefaultExchange().LiquidityCap)
		}
		bars := flatSeries(80, 100)
		bars[entryTick] = flatBar(bars[entryTick].Start, 100, 0.001, 6.6667) // ~333.7 EUR of taker buys, ~667 EUR in total
		s := (&scripted{}).at(tick(entryTick), "BTC/EUR", domain.Long)
		res := mustRun(t, oneMarket(bars), withScript(testConfig([]string{"BTC/EUR"}, 79), s, nil))
		b := ordersWhere(res, func(o OrderLogEntry) bool { return o.Side == SideBuy })[0]
		if b.Status != StatusRejected || b.RejectReason != RejectLiquidity || !near(b.Participation, 100.1/(3.33335*100.1), 1e-9) {
			t.Fatalf("order %+v, want an insufficient_liquidity rejection at participation 0.3 of the buy side", b)
		}
	})

	t.Run("GIVEN a bar without taker sides WHEN participation is measured THEN it is against half the bar's traded value", func(t *testing.T) {
		bars := flatSeries(80, 100)
		bars[entryTick] = flatBar(bars[entryTick].Start, 100, 0.001, 10)
		bars[entryTick].BuyVolume, bars[entryTick].BuyNotional, bars[entryTick].SellVolume, bars[entryTick].SellNotional = 0, 0, 0, 0
		s := (&scripted{}).at(tick(entryTick), "BTC/EUR", domain.Long)
		b := ordersWhere(mustRun(t, oneMarket(bars), withScript(testConfig([]string{"BTC/EUR"}, 79), s, nil)), func(o OrderLogEntry) bool { return o.Side == SideBuy })[0]
		if want := b.FilledQty * (100 * 1.001) / (100 * 10 / 2); b.Status != StatusFilled || !near(b.Participation, want, 1e-12) {
			t.Fatalf("order %+v, want participation %v of half the 1000 EUR traded", b, want)
		}
	})

	t.Run("GIVEN an order larger than the bar's traded value WHEN it would fill THEN it is rejected for insufficient liquidity", func(t *testing.T) {
		bars := flatSeries(80, 100)
		bars[entryTick] = flatBar(bars[entryTick].Start, 100, 0.001, 0.5)
		s := (&scripted{}).at(tick(entryTick), "BTC/EUR", domain.Long)
		res := mustRun(t, oneMarket(bars), withScript(testConfig([]string{"BTC/EUR"}, 79), s, nil))
		b := ordersWhere(res, func(o OrderLogEntry) bool { return o.Side == SideBuy })[0]
		if b.Status != StatusRejected || b.RejectReason != RejectLiquidity || res.RejectionCounts[RejectLiquidity] != 1 {
			t.Fatalf("order %+v counts %v, want an insufficient_liquidity rejection", b, res.RejectionCounts)
		}
	})

	t.Run("GIVEN a fill bar with no trades WHEN an order would fill THEN it is rejected for insufficient liquidity", func(t *testing.T) {
		bars := flatSeries(80, 100)
		bars = append(bars[:entryTick], bars[entryTick+1:]...) // no trades in the fill interval
		s := (&scripted{}).at(tick(entryTick), "BTC/EUR", domain.Long)
		b := ordersWhere(mustRun(t, oneMarket(bars), withScript(testConfig([]string{"BTC/EUR"}, 78), s, nil)), func(o OrderLogEntry) bool { return o.Side == SideBuy })[0]
		if b.Status != StatusRejected || b.RejectReason != RejectLiquidity {
			t.Fatalf("order %+v, want an insufficient_liquidity rejection", b)
		}
	})

	t.Run("GIVEN pair rules with a minimum order WHEN the order is smaller THEN it is rejected and counted", func(t *testing.T) {
		cfg := testConfig([]string{"BTC/EUR"}, 79)
		cfg.Exchange.PairRules = mustPairRules(t, `{"error":[],"result":{"XXBTZEUR":{"altname":"XBTEUR","wsname":"XBT/EUR","lot_decimals":8,"pair_decimals":1,"ordermin":"5","costmin":"0.5","tick_size":"0.1","status":"online"}}}`)
		s := (&scripted{}).at(tick(entryTick), "BTC/EUR", domain.Long)
		res := mustRun(t, oneMarket(flatSeries(80, 100)), withScript(cfg, s, nil))
		b := ordersWhere(res, func(o OrderLogEntry) bool { return o.Side == SideBuy })[0]
		if b.Status != StatusRejected || b.RejectReason != RejectMinOrder || res.RejectionCounts[RejectMinOrder] != 1 {
			t.Fatalf("order %+v counts %v, want a below_min_order rejection", b, res.RejectionCounts)
		}
	})

	t.Run("GIVEN pair rules with lot and price decimals WHEN a long fills THEN quantity rounds down and the price rounds against the trader", func(t *testing.T) {
		cfg := testConfig([]string{"BTC/EUR"}, 79)
		cfg.Exchange.PairRules = mustPairRules(t, `{"error":[],"result":{"XXBTZEUR":{"wsname":"XBT/EUR","lot_decimals":2,"pair_decimals":1,"ordermin":"0.01","costmin":"0.5","tick_size":"0.1"}}}`)
		s := (&scripted{}).at(tick(entryTick), "BTC/EUR", domain.Long)
		b := ordersWhere(mustRun(t, oneMarket(flatSeries(80, 30)), withScript(cfg, s, nil)), func(o OrderLogEntry) bool { return o.Side == SideBuy })[0]
		// 100 EUR at 30 = 3.333... base, rounded down to 3.33; 30.03 rounds up to 30.1.
		if b.RequestedQty != 3.33 || b.FilledQty != 3.33 || !near(b.FillPrice, 30.1, 1e-9) || !near(b.RoundingCost, 0.07, 1e-9) {
			t.Fatalf("order %+v, want 3.33 @ 30.1 with 0.07 rounding", b)
		}
	})

	t.Run("GIVEN the price jumps before the fill WHEN the buy costs more than the cash THEN it is rejected for insufficient funds and cash never goes negative", func(t *testing.T) {
		bars := flatSeries(80, 100)
		buyAt(&bars[entryTick], 2000)
		s := (&scripted{}).at(tick(entryTick), "BTC/EUR", domain.Long)
		var obs []TickObservation
		res := mustRun(t, oneMarket(bars), withScript(testConfig([]string{"BTC/EUR"}, 79), s, &obs))
		b := ordersWhere(res, func(o OrderLogEntry) bool { return o.Side == SideBuy })[0]
		if b.Status != StatusRejected || b.RejectReason != RejectFunds || res.RejectionCounts[RejectFunds] != 1 {
			t.Fatalf("order %+v, want an insufficient_funds rejection", b)
		}
		for _, o := range obs {
			if o.Cash < 0 {
				t.Fatalf("cash went negative at %v: %v", o.Time, o.Cash)
			}
		}
		if res.EndingEquity != 1000 {
			t.Fatalf("ending equity %v, want the untouched 1000", res.EndingEquity)
		}
	})

	t.Run("GIVEN a sell signal with nothing held WHEN it is handled THEN nothing is ordered and the skip is counted", func(t *testing.T) {
		s := (&scripted{}).at(tick(entryTick), "BTC/EUR", domain.Short)
		res := mustRun(t, oneMarket(flatSeries(80, 100)), withScript(testConfig([]string{"BTC/EUR"}, 79), s, nil))
		if len(res.Orders) != 0 || res.DecisionCounts[OutcomeNoHolding] != 1 {
			t.Fatalf("orders %+v counts %v, want none and one spot_no_holding", res.Orders, res.DecisionCounts)
		}
	})

	t.Run("GIVEN random long and short signals WHEN replayed THEN no short position is ever opened and every sell closes a held long", func(t *testing.T) {
		rng := rand.New(rand.NewPCG(7, 11))
		s := &scripted{}
		for i := 36; i < 590; i++ {
			if rng.IntN(4) == 0 {
				d := domain.Long
				if rng.IntN(2) == 0 {
					d = domain.Short
				}
				s.at(tick(i), "BTC/EUR", d)
			}
		}
		var obs []TickObservation
		res := mustRun(t, oneMarket(randomWalkBars(3, 600, t0, 100, 0.002)), withScript(testConfig([]string{"BTC/EUR"}, 599), s, &obs))
		for _, o := range obs {
			for _, p := range o.Positions {
				if p.Size < 0 {
					t.Fatalf("a short position was opened at %v: %+v", o.Time, p)
				}
			}
		}
		held := map[string]bool{}
		sells := 0
		for _, o := range res.Orders {
			if o.Status != StatusFilled {
				continue
			}
			if o.Side == SideBuy {
				held[o.PositionID] = true
				continue
			}
			sells++
			if !held[o.PositionID] {
				t.Fatalf("sell %+v closes nothing held", o)
			}
			delete(held, o.PositionID)
		}
		if sells == 0 {
			t.Fatal("the scenario produced no sells")
		}
	})
}

// sellsOnly returns b as if every trade had been a taker sell at b's sell average.
func sellsOnly(b histdata.Bar) histdata.Bar {
	avg := b.SellNotional / b.SellVolume
	b.SellVolume, b.SellNotional = b.Volume, b.Volume*avg
	b.BuyVolume, b.BuyNotional = 0, 0
	b.Open, b.High, b.Low, b.Close = avg, avg, avg, avg
	return b
}

// buysOnly returns b as if every trade had been a taker buy at b's buy average.
func buysOnly(b histdata.Bar) histdata.Bar {
	avg := b.BuyNotional / b.BuyVolume
	b.BuyVolume, b.BuyNotional = b.Volume, b.Volume*avg
	b.SellVolume, b.SellNotional = 0, 0
	b.Open, b.High, b.Low, b.Close = avg, avg, avg, avg
	return b
}

// TestFillModelBias covers properties of the fill model that small,
// systematic biases would break.
func TestFillModelBias(t *testing.T) {
	t.Run("GIVEN any bar WHEN a market order is quoted THEN a buy never fills below the taker-sell average and a sell never above the taker-buy average", func(t *testing.T) {
		rng := rand.New(rand.NewPCG(17, 23))
		for _, model := range []string{FillSpread, FillFixed} {
			x := DefaultExchange()
			x.FillModel = model
			for i := 0; i < 20000; i++ {
				f := newMarketFeed("BTC/EUR", x.SpreadWindowBars)
				for j := rng.IntN(5); j > 0; j-- { // some markets have a measured spread, some not
					f.spread.add(flatBar(t0, 100, rng.Float64()*0.01, 10))
				}
				mid := 50 + rng.Float64()*100
				b := histdata.Bar{Start: t0, Volume: 1 + rng.Float64()*10, Trades: 3}
				if rng.IntN(3) != 0 {
					b.BuyVolume = b.Volume * rng.Float64()
					b.BuyNotional = b.BuyVolume * mid * (1 + 0.02*(rng.Float64()-0.5))
				}
				if rng.IntN(3) != 0 {
					b.SellVolume = b.Volume * rng.Float64()
					b.SellNotional = b.SellVolume * mid * (1 + 0.02*(rng.Float64()-0.5))
				}
				b.Close = mid * (1 + 0.02*(rng.Float64()-0.5))
				b.Open, b.High, b.Low = b.Close, b.Close, b.Close
				f.last, f.bar, f.hasBar = b.Close, b, true
				buy, sell := x.quote(true, f), x.quote(false, f)
				if model == FillSpread && b.SellVolume > 0 && buy.price < b.SellNotional/b.SellVolume-1e-12 {
					t.Fatalf("%s: buy filled at %v below the sell average %v in %+v", model, buy.price, b.SellNotional/b.SellVolume, b)
				}
				if model == FillSpread && b.BuyVolume > 0 && sell.price > b.BuyNotional/b.BuyVolume+1e-12 {
					t.Fatalf("%s: sell filled at %v above the buy average %v in %+v", model, sell.price, b.BuyNotional/b.BuyVolume, b)
				}
				if buy.price < buy.mid || sell.price > sell.mid {
					t.Fatalf("%s: a quote crossed its own mid estimate: buy %+v sell %+v", model, buy, sell)
				}
			}
		}
	})

	t.Run("GIVEN a market with a known constant spread and one-sided fill bars WHEN N round trips are replayed THEN the spread paid is 2·s·notional per round trip", func(t *testing.T) {
		const mid, s = 100.0, 0.001 // takers buy at 100.1 and sell at 99.9
		bars := flatSeries(400, mid)
		cfg := testConfig([]string{"BTC/EUR"}, 399)
		cfg.DisableLearner = true
		plan := &scripted{}
		trips := 0
		for k := 40; k+10 < 390; k += 30 {
			plan.at(tick(k), "BTC/EUR", domain.Long).at(tick(k+10), "BTC/EUR", domain.Short)
			if trips%3 != 2 { // two trips in three fill on one-sided bars
				bars[k] = sellsOnly(bars[k])      // the buy's fill bar only has sells at the bid
				bars[k+10] = buysOnly(bars[k+10]) // the sell's fill bar only has buys at the ask
			}
			trips++
		}
		res := mustRun(t, oneMarket(bars), withScript(cfg, plan, nil))
		if len(res.Trades) != trips {
			t.Fatalf("want %d round trips, got %d", trips, len(res.Trades))
		}
		paid, reported, notional := 0.0, 0.0, 0.0
		for _, o := range ordersWhere(res, func(o OrderLogEntry) bool { return o.Status == StatusFilled }) {
			sign := 1.0
			if o.Side == SideSell {
				sign = -1
			}
			paid += sign * (o.FillPrice - mid) * o.FilledQty
			reported += o.SpreadCost * o.FilledQty
			if o.Side == SideBuy {
				notional += o.FilledQty * mid
			}
		}
		want := 2 * s * notional
		if !near(paid, want, 0.01*want) {
			t.Fatalf("fills paid %v of spread against the true mid, want 2·s·notional = %v", paid, want)
		}
		if !near(reported, want, 0.01*want) {
			t.Fatalf("the reported spread cost is %v, want %v", reported, want)
		}
	})
}

// mustPairRules writes raw AssetPairs JSON to a temp file and loads it.
func mustPairRules(t *testing.T, raw string) PairRules {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pairs.json")
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	rules, err := LoadPairRules(path)
	if err != nil {
		t.Fatalf("load pair rules: %v", err)
	}
	return rules
}
