package backtest

import (
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

// withExitAt plans a sell of market at tick time when with the given
// confidence, marked as a rule exit or not.
func withExitAt(s *scripted, at int, market string, confidence float64, exit bool) *scripted {
	when := tick(at)
	s.plan[when] = append(s.plan[when], domain.TradeIdea{
		StrategyID: "scripted", Market: market, Direction: domain.Short, Confidence: confidence, Exit: exit,
	})
	return s
}

// TestExitGate covers the MIN_SIGNAL_CONFIDENCE gate and exits: a rule exit
// of a held long is never dropped for low confidence; other sells still are.
func TestExitGate(t *testing.T) {
	const ticks = 200
	run := func(t *testing.T, exit bool) *Result {
		s := (&scripted{}).at(tick(40), "BTC/EUR", domain.Long)
		withExitAt(s, 80, "BTC/EUR", 0.3, exit) // far below the 0.65 default gate
		return mustRun(t, oneMarket(flatSeries(ticks+10, 100)), withScript(testConfig([]string{"BTC/EUR"}, ticks), s, nil))
	}
	signalSells := func(res *Result) []OrderLogEntry {
		return ordersWhere(res, func(o OrderLogEntry) bool {
			return o.Status == StatusFilled && o.Reason == ReasonSignal && o.Side == "sell"
		})
	}

	t.Run("GIVEN a held long WHEN a rule exit arrives with a confidence below the gate THEN the long is sold at that tick", func(t *testing.T) {
		res := run(t, true)
		sells := signalSells(res)
		if len(sells) != 1 || !sells[0].DecisionTime.Equal(tick(80)) {
			t.Fatalf("signal sells %+v, want one decided at tick 80", sells)
		}
		if res.DecisionCounts[OutcomeLowConfidence] != 0 {
			t.Fatalf("the exit was counted as below the confidence gate: %v", res.DecisionCounts)
		}
	})

	t.Run("GIVEN a held long WHEN an ordinary sell signal arrives below the gate THEN it is dropped as before", func(t *testing.T) {
		res := run(t, false)
		if sells := signalSells(res); len(sells) != 0 {
			t.Fatalf("a low-confidence ordinary sell was executed: %+v", sells)
		}
		if res.DecisionCounts[OutcomeLowConfidence] != 1 {
			t.Fatalf("decision counts %v, want one below_min_confidence", res.DecisionCounts)
		}
	})

	t.Run("GIVEN nothing held WHEN a rule exit arrives THEN it is ignored, never a short", func(t *testing.T) {
		s := withExitAt(&scripted{plan: map[time.Time][]domain.TradeIdea{}}, 80, "BTC/EUR", 0.3, true)
		res := mustRun(t, oneMarket(flatSeries(ticks+10, 100)), withScript(testConfig([]string{"BTC/EUR"}, ticks), s, nil))
		if len(res.Orders) != 0 || res.DecisionCounts[OutcomeNoHolding] != 1 {
			t.Fatalf("orders %+v, counts %v: want the exit ignored", res.Orders, res.DecisionCounts)
		}
	})
}
