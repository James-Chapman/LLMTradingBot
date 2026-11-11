package risk

import (
	"strings"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

// fixedClock returns a clock function whose time can be advanced by tests.
func fixedClock(start time.Time) (func() time.Time, func(d time.Duration)) {
	current := start
	return func() time.Time { return current }, func(d time.Duration) { current = current.Add(d) }
}

// makeIdea builds a valid trade idea for risk tests.
func makeIdea(market string, direction domain.Direction, sizing float64) domain.TradeIdea {
	return domain.TradeIdea{
		ID:                     domain.NewID(),
		StrategyID:             "basic_and_llm_strategy",
		Market:                 market,
		Direction:              direction,
		Thesis:                 "BDD test signal",
		Confidence:             0.7,
		PositionSizingProposal: sizing,
		ModeEligibility:        []domain.TradingMode{domain.ModeManual, domain.ModeFullyAutomated},
	}
}

// makePosition builds an open position for a market.
func makePosition(market string, size float64) domain.PositionRecord {
	return domain.PositionRecord{PositionID: domain.NewID(), Market: market, Size: size, AvgPrice: 100.0}
}

// standardOptions supplies the cash and price used by most scenarios.
func standardOptions(cash, price float64, positions ...domain.PositionRecord) EvaluateOptions {
	return EvaluateOptions{
		OpenPositions: positions,
		AvailableCash: domain.Float(cash),
		MarketPrice:   domain.Float(price),
	}
}

// newTestEngine creates an engine with default settings and a fixed clock.
func newTestEngine(mutate func(*config.Settings)) *Engine {
	s := config.Defaults()
	if mutate != nil {
		mutate(&s)
	}
	now, _ := fixedClock(time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC))
	return NewEngine(s, now)
}

// TestRiskEngineEvaluateTrade covers approval and rejection rules for trade ideas.
func TestRiskEngineEvaluateTrade(t *testing.T) {
	t.Run("GIVEN sufficient cash and no matching position WHEN risk evaluates THEN the trade is approved", func(t *testing.T) {
		engine := newTestEngine(nil)
		idea := makeIdea("BTC/EUR", domain.Long, 0.20)

		decision := engine.EvaluateTrade(idea, standardOptions(500, 100))

		if !decision.Approved {
			t.Fatalf("expected approval, got %q", decision.Reason)
		}
		if decision.TradeIdeaID != idea.ID {
			t.Fatalf("decision not linked to idea")
		}
		if !strings.Contains(strings.ToLower(decision.Reason), "passed") {
			t.Fatalf("unexpected reason %q", decision.Reason)
		}
		if decision.ProposedSize == nil || *decision.ProposedSize <= 0 {
			t.Fatalf("expected a proposed size, got %v", decision.ProposedSize)
		}
	})

	t.Run("GIVEN a same-direction position WHEN risk evaluates THEN one-position-per-pair is enforced", func(t *testing.T) {
		engine := newTestEngine(nil)
		idea := makeIdea("ETH/EUR", domain.Long, 0.20)

		decision := engine.EvaluateTrade(idea, standardOptions(500, 100, makePosition("ETH/EUR", 1.0)))

		if decision.Approved || !strings.Contains(strings.ToLower(decision.Reason), "already open") {
			t.Fatalf("expected rejection for open position, got %+v", decision)
		}
	})

	t.Run("GIVEN an opposite-direction position WHEN risk evaluates THEN a closing signal can be approved", func(t *testing.T) {
		engine := newTestEngine(nil)
		idea := makeIdea("ETH/EUR", domain.Short, 0.20)

		decision := engine.EvaluateTrade(idea, standardOptions(500, 100, makePosition("ETH/EUR", 1.0)))

		if !decision.Approved {
			t.Fatalf("expected approval, got %q", decision.Reason)
		}
	})

	t.Run("GIVEN an open long WHEN a short closes it with no cash THEN the cash gate does not block it", func(t *testing.T) {
		engine := newTestEngine(nil)
		engine.UpdateEquity(10_000)
		idea := makeIdea("BTC/EUR", domain.Short, 0.05)

		decision := engine.EvaluateTrade(idea, standardOptions(0, 50_000, makePosition("BTC/EUR", 0.01)))

		reason := strings.ToLower(decision.Reason)
		if strings.Contains(reason, "already open") || strings.Contains(reason, "cash") {
			t.Fatalf("closing short must not be blocked by position or cash gates: %q", decision.Reason)
		}
	})

	t.Run("GIVEN too little cash WHEN a long is evaluated THEN cash sufficiency blocks it", func(t *testing.T) {
		engine := newTestEngine(nil)

		decision := engine.EvaluateTrade(makeIdea("BTC/EUR", domain.Long, 0.20), standardOptions(10, 100))

		if decision.Approved || !strings.Contains(strings.ToLower(decision.Reason), "insufficient cash") {
			t.Fatalf("expected insufficient cash rejection, got %+v", decision)
		}
	})

	t.Run("GIVEN no closable long and too little cash WHEN a short is evaluated THEN it is rejected", func(t *testing.T) {
		engine := newTestEngine(nil)

		decision := engine.EvaluateTrade(makeIdea("BTC/EUR", domain.Short, 0.20), standardOptions(10, 100))

		if decision.Approved || !strings.Contains(strings.ToLower(decision.Reason), "insufficient cash") {
			t.Fatalf("expected insufficient cash rejection, got %+v", decision)
		}
	})

	t.Run("GIVEN daily losses at the limit WHEN risk evaluates THEN new trades are blocked", func(t *testing.T) {
		engine := newTestEngine(nil)
		engine.RecordTradeResult(-25.0)

		decision := engine.EvaluateTrade(makeIdea("BTC/EUR", domain.Long, 0.20), standardOptions(500, 100))

		if decision.Approved || !strings.Contains(strings.ToLower(decision.Reason), "daily loss") {
			t.Fatalf("expected daily loss rejection, got %+v", decision)
		}
	})

	t.Run("GIVEN daily losses just below the limit WHEN a new trade would exceed it THEN it is rejected pre-emptively", func(t *testing.T) {
		engine := newTestEngine(nil)
		today := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		engine.RestoreState(State{DailyLoss: 24.0, DailyStartEquity: 500, LastResetDate: today})

		decision := engine.EvaluateTrade(makeIdea("BTC/EUR", domain.Long, 0.20), standardOptions(500, 100))

		if decision.Approved || !strings.Contains(strings.ToLower(decision.Reason), "daily loss") {
			t.Fatalf("expected daily loss rejection, got %+v", decision)
		}
	})

	t.Run("GIVEN a sizing below the minimum trade size WHEN risk evaluates THEN it is approved at the minimum with adjusted sizing", func(t *testing.T) {
		engine := newTestEngine(func(s *config.Settings) { s.TargetTradeAmount = 0 })

		decision := engine.EvaluateTrade(makeIdea("BTC/EUR", domain.Long, 0.05), standardOptions(500, 100))

		if !decision.Approved {
			t.Fatalf("expected approval at minimum size, got %q", decision.Reason)
		}
		if decision.AdjustedSizing == nil {
			t.Fatal("adjusted sizing must be set when clamped to minimum")
		}
		want := engine.settings.MinTradeSize / engine.CurrentEquity()
		if !almostEqual(*decision.AdjustedSizing, want) {
			t.Fatalf("adjusted sizing %v, want %v", *decision.AdjustedSizing, want)
		}
	})

	t.Run("GIVEN cash below target but above minimum WHEN a long is evaluated THEN sizing is reduced to spendable cash", func(t *testing.T) {
		engine := newTestEngine(nil)

		decision := engine.EvaluateTrade(makeIdea("BTC/EUR", domain.Long, 0.20), standardOptions(96.87, 100))

		if !decision.Approved || decision.AdjustedSizing == nil {
			t.Fatalf("expected reduced-size approval, got %+v", decision)
		}
		spendable := 96.87 / (1 + engine.settings.FeeAndSlippage)
		if !almostEqual(*decision.AdjustedSizing, spendable/engine.CurrentEquity()) {
			t.Fatalf("adjusted sizing %v not based on spendable cash", *decision.AdjustedSizing)
		}
		if *decision.ProposedSize > 96.87 {
			t.Fatalf("proposed size %v exceeds available cash", *decision.ProposedSize)
		}
	})

	t.Run("GIVEN a minimum liquidity rule WHEN 24h volume is below it THEN the trade is rejected", func(t *testing.T) {
		engine := newTestEngine(func(s *config.Settings) { s.Min24hVolume = 1_000 })
		opts := standardOptions(500, 100)
		opts.MarketVolume24h = domain.Float(50)

		decision := engine.EvaluateTrade(makeIdea("BTC/EUR", domain.Long, 0.20), opts)

		if decision.Approved || !strings.Contains(strings.ToLower(decision.Reason), "liquidity") {
			t.Fatalf("expected liquidity rejection, got %+v", decision)
		}
	})

	t.Run("GIVEN tiny equity WHEN a trade is evaluated THEN the per-trade loss limit rejects it", func(t *testing.T) {
		engine := newTestEngine(nil)
		engine.UpdateEquity(10)

		decision := engine.EvaluateTrade(makeIdea("BTC/EUR", domain.Long, 0.20), standardOptions(1_000, 50_000))

		if decision.Approved || !strings.Contains(strings.ToLower(decision.Reason), "loss") {
			t.Fatalf("expected per-trade loss rejection, got %+v", decision)
		}
	})

	t.Run("GIVEN a GBP base currency WHEN a rejection is explained THEN the reason uses the pound symbol", func(t *testing.T) {
		engine := newTestEngine(func(s *config.Settings) { s.BaseCurrency = "GBP" })

		decision := engine.EvaluateTrade(makeIdea("BTC/EUR", domain.Long, 0.20), standardOptions(10, 100))

		if !strings.Contains(decision.Reason, "£") {
			t.Fatalf("expected pound symbol in %q", decision.Reason)
		}
	})
}

// TestRiskEngineDailyTracking covers daily loss tracking and reset.
func TestRiskEngineDailyTracking(t *testing.T) {
	t.Run("GIVEN a losing trade WHEN recorded THEN daily loss grows and equity falls", func(t *testing.T) {
		engine := newTestEngine(nil)
		start := engine.CurrentEquity()

		engine.RecordTradeResult(-12.5)
		engine.RecordTradeResult(5.0)

		state := engine.Snapshot()
		if !almostEqual(state.DailyLoss, 12.5) {
			t.Fatalf("daily loss %v, want 12.5 (profits do not reduce it)", state.DailyLoss)
		}
		if !almostEqual(engine.CurrentEquity(), start-7.5) {
			t.Fatalf("equity %v, want %v", engine.CurrentEquity(), start-7.5)
		}
	})

	t.Run("GIVEN losses recorded yesterday WHEN the UTC date changes THEN daily counters reset", func(t *testing.T) {
		now, advance := fixedClock(time.Date(2026, 11, 11, 23, 0, 0, 0, time.UTC))
		engine := NewEngine(config.Defaults(), now)
		engine.RecordTradeResult(-20)

		advance(2 * time.Hour)
		engine.RecordTradeResult(-1)

		state := engine.Snapshot()
		if !almostEqual(state.DailyLoss, 1) {
			t.Fatalf("daily loss %v, want reset to 1", state.DailyLoss)
		}
		if state.LastResetDate.Day() != 12 {
			t.Fatalf("last reset date %v not advanced", state.LastResetDate)
		}
		if !almostEqual(state.DailyStartEquity, 480) {
			t.Fatalf("daily start equity %v, want equity at reset (480)", state.DailyStartEquity)
		}
	})

	t.Run("GIVEN persisted state from today WHEN restored THEN daily loss is applied", func(t *testing.T) {
		engine := newTestEngine(nil)

		restored := engine.RestoreState(State{DailyLoss: 30, DailyStartEquity: 500, LastResetDate: time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)})

		if !restored || !almostEqual(engine.Snapshot().DailyLoss, 30) {
			t.Fatalf("expected today's state to be restored, got %+v", engine.Snapshot())
		}
	})

	t.Run("GIVEN persisted state from a previous day WHEN restored THEN it is ignored", func(t *testing.T) {
		engine := newTestEngine(nil)

		restored := engine.RestoreState(State{DailyLoss: 99, DailyStartEquity: 500, LastResetDate: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)})

		if restored || engine.Snapshot().DailyLoss != 0 {
			t.Fatalf("stale state must not be restored, got %+v", engine.Snapshot())
		}
	})

	t.Run("GIVEN the engine WHEN the stop-loss assumption is read THEN it mirrors the configured stop-loss", func(t *testing.T) {
		engine := newTestEngine(func(s *config.Settings) { s.StopLossPct = 0.07 })

		if engine.StopLossAssumption() != 0.07 {
			t.Fatalf("stop-loss assumption %v, want 0.07", engine.StopLossAssumption())
		}
	})
}

// almostEqual compares floats to six decimal places.
func almostEqual(a, b float64) bool {
	d := a - b
	return d < 1e-6 && d > -1e-6
}
