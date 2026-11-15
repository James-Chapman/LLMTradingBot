package paper

import (
	"fmt"
	"math"
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

// TestEngineOptions covers the injectable costs and id source a backtest uses.
func TestEngineOptions(t *testing.T) {
	long := domain.ExecutionIntent{Market: "BTC/EUR", Direction: domain.Long, Size: 0.01}

	t.Run("GIVEN no options WHEN a long fills THEN the live paper fee and slippage constants apply", func(t *testing.T) {
		e := NewEngineWithOptions(1000, nil, nil, Options{})
		order, _ := e.Execute(long, 10000, ExecuteOptions{})
		price, slip := 10000.0, SlippageRate // runtime float maths, as the engine does
		wantPrice := price * (1 + slip)
		if order.Price != wantPrice {
			t.Fatalf("fill price %v, want %v", order.Price, wantPrice)
		}
		if fee := e.Fills()[0].Fee; math.Abs(fee-0.01*wantPrice*TakerFeeRate) > 1e-12 {
			t.Fatalf("fee %v, want the %v taker rate", fee, TakerFeeRate)
		}
	})

	t.Run("GIVEN explicit costs WHEN a long opens and closes THEN the fill is at the given price and the fee uses the given rate", func(t *testing.T) {
		e := NewEngineWithOptions(1000, nil, nil, Options{Costs: &Costs{FeeRate: 0.008, SlippageRate: 0}})
		order, posID := e.Execute(long, 10000, ExecuteOptions{})
		if order.Price != 10000 {
			t.Fatalf("fill price %v, want the unslipped 10000", order.Price)
		}
		if fee := e.Fills()[0].Fee; math.Abs(fee-0.8) > 1e-12 {
			t.Fatalf("entry fee %v, want 0.8", fee)
		}
		if e.Cash() != 1000-100-0.8 {
			t.Fatalf("cash %v after the buy", e.Cash())
		}
		closeOrder, ok := e.ClosePosition(posID, 10000, CloseOptions{})
		if !ok || closeOrder.Price != 10000 {
			t.Fatalf("close %+v ok=%v", closeOrder, ok)
		}
		pnl, _ := e.RealisedPnL(posID)
		if math.Abs(pnl-(-1.6)) > 1e-12 {
			t.Fatalf("round-trip pnl %v, want -1.6 (two fees)", pnl)
		}
	})

	t.Run("GIVEN an id source WHEN orders and positions are created THEN their ids come from it in order", func(t *testing.T) {
		n := 0
		e := NewEngineWithOptions(1000, nil, nil, Options{NewID: func() string { n++; return fmt.Sprintf("id-%d", n) }})
		order, posID := e.Execute(long, 10000, ExecuteOptions{})
		if order.ID != "id-1" || posID != "id-2" {
			t.Fatalf("order %q position %q, want id-1 then id-2", order.ID, posID)
		}
	})

	t.Run("GIVEN several open positions WHEN equity is summed repeatedly THEN the result is identical every time", func(t *testing.T) {
		e := NewEngineWithOptions(100000, nil, nil, Options{})
		prices := map[string]float64{}
		for i := 0; i < 12; i++ {
			m := fmt.Sprintf("C%02d/EUR", i)
			e.Execute(domain.ExecutionIntent{Market: m, Direction: domain.Long, Size: 0.1 + float64(i)*0.0137}, 1.1+float64(i)*0.31, ExecuteOptions{})
			prices[m] = 1.3 + float64(i)*0.29
		}
		first := e.TotalEquity(prices)
		for i := 0; i < 200; i++ {
			if got := e.TotalEquity(prices); got != first {
				t.Fatalf("equity %v differs from %v: the sum order is not fixed", got, first)
			}
		}
	})
}
