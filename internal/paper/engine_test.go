package paper

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

// savedOrder is one captured SaveOrder call.
type savedOrder struct {
	order       domain.OrderRecord
	approvalID  string
	fee         float64
	environment string
	tradeIdeaID string
}

// capturingRepo records every persistence call made by the engine.
type capturingRepo struct {
	openRows       []OpenPositionRow
	orders         []savedOrder
	fills          []domain.FillRecord
	rejected       []RejectedTrade
	upserts        []OpenPositionRow
	deleted        []string
	outcomes       []SignalOutcome
	orderPnLs      map[string]float64
	failOutcomeErr error
}

// GetOpenPositions returns the seeded open rows.
func (c *capturingRepo) GetOpenPositions() ([]OpenPositionRow, error) { return c.openRows, nil }

// SaveRejectedTrade captures a rejected trade.
func (c *capturingRepo) SaveRejectedTrade(r RejectedTrade) error {
	c.rejected = append(c.rejected, r)
	return nil
}

// SaveOrder captures an order.
func (c *capturingRepo) SaveOrder(order domain.OrderRecord, approvalID string, fee float64, environment, tradeIdeaID string) error {
	c.orders = append(c.orders, savedOrder{order, approvalID, fee, environment, tradeIdeaID})
	return nil
}

// SaveFill captures a fill.
func (c *capturingRepo) SaveFill(fill domain.FillRecord) error {
	c.fills = append(c.fills, fill)
	return nil
}

// UpsertOpenPosition captures an open-position write.
func (c *capturingRepo) UpsertOpenPosition(row OpenPositionRow) error {
	c.upserts = append(c.upserts, row)
	return nil
}

// DeleteOpenPosition captures a deleted position id.
func (c *capturingRepo) DeleteOpenPosition(positionID string) error {
	c.deleted = append(c.deleted, positionID)
	return nil
}

// SaveSignalOutcome captures a closed-trade outcome.
func (c *capturingRepo) SaveSignalOutcome(o SignalOutcome) error {
	if c.failOutcomeErr != nil {
		return c.failOutcomeErr
	}
	c.outcomes = append(c.outcomes, o)
	return nil
}

// UpdateOrderPnL captures realised P&L stamped on an order.
func (c *capturingRepo) UpdateOrderPnL(orderID string, pnl float64) error {
	if c.orderPnLs == nil {
		c.orderPnLs = map[string]float64{}
	}
	c.orderPnLs[orderID] = pnl
	return nil
}

// intent builds an execution intent.
func intent(market string, direction domain.Direction, size float64) domain.ExecutionIntent {
	return domain.ExecutionIntent{ApprovalRequestID: "bdd-approval", Market: market, Direction: direction, Size: size}
}

// near compares floats with a small tolerance.
func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// newEngine creates an engine without persistence.
func newEngine(capital float64) *Engine { return NewEngine(capital, nil, nil) }

// TestPaperExecute covers fills, fees, slippage and fund checks for new orders.
func TestPaperExecute(t *testing.T) {
	t.Run("GIVEN cash WHEN a long executes THEN one position opens and cash includes costs", func(t *testing.T) {
		engine := newEngine(500)

		order, positionID := engine.Execute(intent("BTC/EUR", domain.Long, 1), 100, ExecuteOptions{})

		fillPrice := 100 * (1 + SlippageRate)
		fee := fillPrice * TakerFeeRate
		if order.Status != "filled" || order.PositionID != positionID {
			t.Fatalf("unexpected order %+v", order)
		}
		pos, ok := engine.Position(positionID)
		if !ok || !near(pos.AvgPrice, fillPrice) {
			t.Fatalf("position missing or wrong price: %+v", pos)
		}
		if !near(engine.Cash(), 500-fillPrice-fee) {
			t.Fatalf("cash %v, want %v", engine.Cash(), 500-fillPrice-fee)
		}
	})

	t.Run("GIVEN an open long WHEN another same-market long executes THEN it is rejected", func(t *testing.T) {
		engine := newEngine(500)
		engine.Execute(intent("BTC/EUR", domain.Long, 1), 100, ExecuteOptions{})

		order, positionID := engine.Execute(intent("BTC/EUR", domain.Long, 1), 100, ExecuteOptions{})

		if order.Status != "rejected" || positionID != "" || len(engine.OpenPositions()) != 1 {
			t.Fatalf("expected rejection, got %+v / %q", order, positionID)
		}
	})

	t.Run("GIVEN a rejection WHEN a repository is configured THEN the rejected trade is saved but not the order", func(t *testing.T) {
		repo := &capturingRepo{}
		engine := NewEngine(10, repo, nil)

		order, positionID := engine.Execute(intent("BTC/EUR", domain.Long, 1), 100, ExecuteOptions{
			SignalConfidence: domain.Float(0.82),
			TradeIdeaID:      "idea-rejected-1",
		})

		if order.Status != "rejected" || positionID != "" {
			t.Fatalf("expected rejection, got %+v", order)
		}
		if len(repo.orders) != 0 || len(repo.rejected) != 1 {
			t.Fatalf("orders %d, rejected %d", len(repo.orders), len(repo.rejected))
		}
		r := repo.rejected[0]
		if r.Market != "BTC/EUR" || r.Direction != "long" || r.Reason != "insufficient_funds" || r.TradeIdeaID != "idea-rejected-1" || r.Confidence == nil || *r.Confidence != 0.82 {
			t.Fatalf("unexpected rejected trade %+v", r)
		}
	})

	t.Run("GIVEN an open long WHEN a matching short executes THEN the oldest long is closed", func(t *testing.T) {
		engine := newEngine(500)
		openOrder, openedID := engine.Execute(intent("BTC/EUR", domain.Long, 1), 100, ExecuteOptions{})

		closeOrder, closedID := engine.Execute(intent("BTC/EUR", domain.Short, 1), 110, ExecuteOptions{})

		if openOrder.Status != "filled" || closeOrder.Status != "filled" || closedID != openedID {
			t.Fatalf("expected FIFO close of %q, got %q", openedID, closedID)
		}
		if len(engine.OpenPositions()) != 0 {
			t.Fatal("expected no open positions")
		}
	})

	t.Run("GIVEN an open short WHEN another short executes THEN it is rejected", func(t *testing.T) {
		engine := newEngine(1000)
		engine.Execute(intent("BTC/EUR", domain.Short, 1), 100, ExecuteOptions{})

		order, _ := engine.Execute(intent("BTC/EUR", domain.Short, 1), 100, ExecuteOptions{})

		if order.Status != "rejected" {
			t.Fatalf("expected rejection, got %+v", order)
		}
	})

	t.Run("GIVEN a repository WHEN a long fills THEN order, fill and open position are persisted", func(t *testing.T) {
		repo := &capturingRepo{}
		engine := NewEngine(500, repo, nil)

		_, positionID := engine.Execute(intent("BTC/EUR", domain.Long, 1), 100, ExecuteOptions{
			StrategyID: "basic_strategy", TradeIdeaID: "idea-1", Environment: "paper",
		})

		if len(repo.orders) != 1 || len(repo.fills) != 1 || len(repo.upserts) != 1 {
			t.Fatalf("orders %d fills %d upserts %d", len(repo.orders), len(repo.fills), len(repo.upserts))
		}
		up := repo.upserts[0]
		if up.PositionID != positionID || up.Direction != "long" || up.StrategyID != "basic_strategy" || up.TradeIdeaID != "idea-1" {
			t.Fatalf("unexpected upsert %+v", up)
		}
		if repo.orders[0].environment != "paper" || repo.orders[0].tradeIdeaID != "idea-1" {
			t.Fatalf("unexpected saved order %+v", repo.orders[0])
		}
	})
}

// TestPaperShorts covers opening and settling short positions.
func TestPaperShorts(t *testing.T) {
	t.Run("GIVEN no existing long WHEN a paper short opens THEN cash does not increase", func(t *testing.T) {
		engine := newEngine(500)

		order, _ := engine.Execute(intent("BTC/EUR", domain.Short, 1), 100, ExecuteOptions{})

		if order.Status != "filled" || engine.Cash() >= 500 {
			t.Fatalf("expected filled short with fee deducted, cash %v", engine.Cash())
		}
	})

	t.Run("GIVEN a paper short WHEN closed at a lower price THEN cash reflects the profit", func(t *testing.T) {
		engine := newEngine(1000)
		_, positionID := engine.Execute(intent("BTC/EUR", domain.Short, 1), 100, ExecuteOptions{})

		_, ok := engine.ClosePosition(positionID, 90, CloseOptions{})

		if !ok || engine.Cash() <= 1000 {
			t.Fatalf("expected profit, cash %v", engine.Cash())
		}
	})

	t.Run("GIVEN an open short WHEN price falls THEN equity exceeds cash", func(t *testing.T) {
		engine := newEngine(1000)
		engine.Execute(intent("BTC/EUR", domain.Short, 1), 100, ExecuteOptions{})

		equity := engine.TotalEquity(map[string]float64{"BTC/EUR": 80})

		if equity <= engine.Cash() || equity <= 999 {
			t.Fatalf("equity %v must reflect short profit (cash %v)", equity, engine.Cash())
		}
	})

	t.Run("GIVEN an open short WHEN price rises THEN equity is below cash", func(t *testing.T) {
		engine := newEngine(1000)
		engine.Execute(intent("BTC/EUR", domain.Short, 1), 100, ExecuteOptions{})

		if equity := engine.TotalEquity(map[string]float64{"BTC/EUR": 120}); equity >= engine.Cash() {
			t.Fatalf("equity %v must reflect short loss (cash %v)", equity, engine.Cash())
		}
	})
}

// TestPaperEquityAndMarks covers mark-to-market prices and total equity.
func TestPaperEquityAndMarks(t *testing.T) {
	t.Run("GIVEN an open long WHEN equity is requested THEN cash plus market value is returned", func(t *testing.T) {
		engine := newEngine(500)
		engine.Execute(intent("BTC/EUR", domain.Long, 1), 100, ExecuteOptions{})

		if equity := engine.TotalEquity(map[string]float64{"BTC/EUR": 120}); !near(equity, engine.Cash()+120) {
			t.Fatalf("equity %v, want %v", equity, engine.Cash()+120)
		}
	})

	t.Run("GIVEN an open long WHEN mark prices update THEN unrealised P&L follows the price", func(t *testing.T) {
		engine := newEngine(500)
		_, positionID := engine.Execute(intent("BTC/EUR", domain.Long, 1), 100, ExecuteOptions{})

		engine.UpdateMarkPrices(map[string]float64{"BTC/EUR": 120})

		pos, _ := engine.Position(positionID)
		if !near(pos.UnrealizedPnL, 120-pos.AvgPrice) {
			t.Fatalf("unrealised pnl %v", pos.UnrealizedPnL)
		}
	})

	t.Run("GIVEN positions on two markets WHEN filtered by market THEN only that market returns", func(t *testing.T) {
		engine := newEngine(1000)
		engine.Execute(intent("BTC/EUR", domain.Long, 1), 100, ExecuteOptions{})
		engine.Execute(intent("ETH/EUR", domain.Long, 1), 100, ExecuteOptions{})

		got := engine.PositionsForMarket("ETH/EUR")

		if len(got) != 1 || got[0].Market != "ETH/EUR" {
			t.Fatalf("unexpected positions %+v", got)
		}
	})
}

// TestPaperStops covers stop-loss and trailing-stop triggers.
func TestPaperStops(t *testing.T) {
	t.Run("GIVEN a long made a new high WHEN price retraces past the trail THEN the trailing stop triggers", func(t *testing.T) {
		engine := newEngine(500)
		_, id := engine.Execute(intent("BTC/EUR", domain.Long, 1), 100, ExecuteOptions{})

		engine.UpdateTrailingPrices(map[string]float64{"BTC/EUR": 120})

		if engine.TrailingStopTriggered(id, 116, 0.05) || !engine.TrailingStopTriggered(id, 113, 0.05) {
			t.Fatal("trailing stop should trigger only below 114")
		}
	})

	t.Run("GIVEN a profitable long WHEN price retraces from a high THEN the hard stop-loss does not trigger", func(t *testing.T) {
		engine := newEngine(500)
		_, id := engine.Execute(intent("BTC/EUR", domain.Long, 1), 100, ExecuteOptions{})
		engine.UpdateTrailingPrices(map[string]float64{"BTC/EUR": 120})

		if !engine.TrailingStopTriggered(id, 113, 0.05) || engine.StopLossTriggered(id, 113, 0.05) {
			t.Fatal("expected trailing stop only")
		}
	})

	t.Run("GIVEN a profitable short WHEN price retraces from a low THEN the hard stop-loss does not trigger", func(t *testing.T) {
		engine := newEngine(500)
		_, id := engine.Execute(intent("BTC/EUR", domain.Short, 1), 100, ExecuteOptions{})
		engine.UpdateTrailingPrices(map[string]float64{"BTC/EUR": 80})

		if !engine.TrailingStopTriggered(id, 87, 0.05) || engine.StopLossTriggered(id, 87, 0.05) {
			t.Fatal("expected trailing stop only")
		}
	})

	t.Run("GIVEN positions moving against entry WHEN loss exceeds the threshold THEN the stop-loss triggers", func(t *testing.T) {
		engine := newEngine(500)
		_, longID := engine.Execute(intent("BTC/EUR", domain.Long, 1), 100, ExecuteOptions{})
		_, shortID := engine.Execute(intent("ETH/EUR", domain.Short, 1), 100, ExecuteOptions{})

		if !engine.StopLossTriggered(longID, 94, 0.05) || !engine.StopLossTriggered(shortID, 106, 0.05) {
			t.Fatal("expected both stop-losses to trigger")
		}
	})

	t.Run("GIVEN an unknown position WHEN stops are checked THEN neither triggers", func(t *testing.T) {
		engine := newEngine(500)

		if engine.StopLossTriggered("missing", 1, 0.05) || engine.TrailingStopTriggered("missing", 1, 0.05) {
			t.Fatal("unknown position must not trigger")
		}
	})
}

// TestPaperClosing covers closing positions and recording closed trades.
func TestPaperClosing(t *testing.T) {
	t.Run("GIVEN a manual close WHEN the order is saved THEN the source is manual_close", func(t *testing.T) {
		repo := &capturingRepo{}
		engine := NewEngine(500, repo, nil)
		_, id := engine.Execute(intent("BTC/EUR", domain.Long, 1), 100, ExecuteOptions{})

		engine.ClosePosition(id, 110, CloseOptions{ApprovalRequestID: "manual_close"})

		last := repo.orders[len(repo.orders)-1]
		if last.approvalID != "manual_close" || repo.deleted[len(repo.deleted)-1] != id {
			t.Fatalf("unexpected close persistence %+v", last)
		}
	})

	t.Run("GIVEN no close source WHEN a position closes THEN it defaults to stop_loss", func(t *testing.T) {
		repo := &capturingRepo{}
		engine := NewEngine(500, repo, nil)
		_, id := engine.Execute(intent("BTC/EUR", domain.Long, 1), 100, ExecuteOptions{})

		engine.ClosePosition(id, 110, CloseOptions{})

		if repo.orders[len(repo.orders)-1].approvalID != "stop_loss" {
			t.Fatalf("default source %q", repo.orders[len(repo.orders)-1].approvalID)
		}
	})

	t.Run("GIVEN an unknown position WHEN closed THEN nothing happens", func(t *testing.T) {
		engine := newEngine(500)

		if _, ok := engine.ClosePosition("missing", 100, CloseOptions{}); ok {
			t.Fatal("unknown position must not close")
		}
	})

	t.Run("GIVEN a long closed with slippage and fees WHEN the outcome is recorded THEN P&L matches fills and cash delta", func(t *testing.T) {
		repo := &capturingRepo{}
		engine := NewEngine(500, repo, nil)
		_, id := engine.Execute(intent("BTC/EUR", domain.Long, 1), 100, ExecuteOptions{StrategyID: "basic_strategy"})

		closeOrder, _ := engine.ClosePosition(id, 110, CloseOptions{ApprovalRequestID: "manual_close"})
		pnl, ok := engine.RecordClosedTrade(id, closeOrder.Price, "manual_close", "")

		entry := 100 * (1 + SlippageRate)
		exit := 110 * (1 - SlippageRate)
		want := (exit - entry) - entry*TakerFeeRate - exit*TakerFeeRate
		outcome := repo.outcomes[len(repo.outcomes)-1]
		if !ok || !near(pnl, want) || !near(outcome.PnL, want) || !near(outcome.ExitPrice, exit) {
			t.Fatalf("pnl %v outcome %+v, want %v", pnl, outcome, want)
		}
		if !near(engine.Cash()-500, want) {
			t.Fatalf("cash delta %v, want %v", engine.Cash()-500, want)
		}
		if outcome.StrategyID != "basic_strategy" || outcome.Direction != "long" || outcome.PositionID != id || outcome.ExitReason != "manual_close" {
			t.Fatalf("outcome context wrong %+v", outcome)
		}
	})

	t.Run("GIVEN a short closes a long WHEN the outcome is recorded THEN P&L uses the actual fill and both fees", func(t *testing.T) {
		repo := &capturingRepo{}
		engine := NewEngine(500, repo, nil)
		_, openedID := engine.Execute(intent("BTC/EUR", domain.Long, 1), 100, ExecuteOptions{})

		closeOrder, closedID := engine.Execute(intent("BTC/EUR", domain.Short, 1), 110, ExecuteOptions{})
		pnl, ok := engine.RecordClosedTrade(closedID, closeOrder.Price, "auto", "closing-idea")

		entry := 100 * (1 + SlippageRate)
		exit := 110 * (1 - SlippageRate)
		want := (exit - entry) - entry*TakerFeeRate - exit*TakerFeeRate
		outcome := repo.outcomes[len(repo.outcomes)-1]
		if closedID != openedID || !ok || !near(pnl, want) || !near(outcome.ExitPrice, exit) {
			t.Fatalf("pnl %v outcome %+v, want %v", pnl, outcome, want)
		}
		if !near(engine.Cash()-500, want) || outcome.ClosingTradeIdeaID != "closing-idea" {
			t.Fatalf("cash delta %v, outcome %+v", engine.Cash()-500, outcome)
		}
		if repo.deleted[len(repo.deleted)-1] != openedID {
			t.Fatal("closed position must be deleted from storage")
		}
	})

	t.Run("GIVEN no repository WHEN a closed trade is recorded THEN nothing is returned but realised P&L is available", func(t *testing.T) {
		engine := newEngine(500)
		_, id := engine.Execute(intent("BTC/EUR", domain.Long, 1), 100, ExecuteOptions{})
		order, _ := engine.ClosePosition(id, 110, CloseOptions{})

		_, recorded := engine.RecordClosedTrade(id, order.Price, "manual_close", "")
		pnl, ok := engine.RealisedPnL(id)

		if recorded || !ok || pnl <= 0 {
			t.Fatalf("recorded %v, realised %v %v", recorded, pnl, ok)
		}
	})

	t.Run("GIVEN an open position WHEN a closed trade is recorded THEN nothing is returned", func(t *testing.T) {
		repo := &capturingRepo{}
		engine := NewEngine(500, repo, nil)
		_, id := engine.Execute(intent("BTC/EUR", domain.Long, 1), 100, ExecuteOptions{})

		if _, ok := engine.RecordClosedTrade(id, 110, "manual_close", ""); ok || len(repo.outcomes) != 0 {
			t.Fatal("an open position has no closed trade to record")
		}
	})

	t.Run("GIVEN the outcome store fails WHEN a closed trade is recorded THEN the P&L is still returned", func(t *testing.T) {
		repo := &capturingRepo{failOutcomeErr: errors.New("db down")}
		engine := NewEngine(500, repo, nil)
		_, id := engine.Execute(intent("BTC/EUR", domain.Long, 1), 100, ExecuteOptions{})
		order, _ := engine.ClosePosition(id, 110, CloseOptions{})

		if _, ok := engine.RecordClosedTrade(id, order.Price, "manual_close", ""); !ok {
			t.Fatal("P&L must be returned so risk tracking still updates")
		}
	})
}

// TestPaperRestore covers restoring open positions from the database.
func TestPaperRestore(t *testing.T) {
	opened := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	restoredRepo := func() *capturingRepo {
		return &capturingRepo{openRows: []OpenPositionRow{{
			PositionID: "test-restore-001", Market: "BTC/EUR", Size: 1, AvgPrice: 50_000,
			Direction: "long", StrategyID: "basic_strategy", TradeIdeaID: "idea-9", OpenedAt: opened,
		}}}
	}

	t.Run("GIVEN open rows in the DB WHEN restored THEN positions and their context are loaded", func(t *testing.T) {
		engine := NewEngine(100_000, restoredRepo(), nil)

		if err := engine.RestoreFromDB(); err != nil {
			t.Fatal(err)
		}

		pos, ok := engine.Position("test-restore-001")
		meta, _ := engine.Meta("test-restore-001")
		if !ok || pos.AvgPrice != 50_000 || !pos.Timestamp.Equal(opened) {
			t.Fatalf("position not restored: %+v", pos)
		}
		if meta.StrategyID != "basic_strategy" || meta.TradeIdeaID != "idea-9" || meta.TrailingHigh != nil {
			t.Fatalf("meta not restored correctly: %+v", meta)
		}
	})

	t.Run("GIVEN a restored long WHEN the first live price arrives THEN the watermark uses it, not the entry", func(t *testing.T) {
		engine := NewEngine(100_000, restoredRepo(), nil)
		_ = engine.RestoreFromDB()

		engine.UpdateTrailingPrices(map[string]float64{"BTC/EUR": 60_000})

		meta, _ := engine.Meta("test-restore-001")
		if meta.TrailingHigh == nil || *meta.TrailingHigh != 60_000 {
			t.Fatalf("trailing high %v, want 60000", meta.TrailingHigh)
		}
		if !engine.TrailingStopTriggered("test-restore-001", 57_000, 0.05) || engine.TrailingStopTriggered("test-restore-001", 58_000, 0.05) {
			t.Fatal("trailing stop must use the live high")
		}
	})

	t.Run("GIVEN a restored long before any price update WHEN the stop is checked THEN it falls back to the entry price", func(t *testing.T) {
		engine := NewEngine(100_000, restoredRepo(), nil)
		_ = engine.RestoreFromDB()

		if !engine.TrailingStopTriggered("test-restore-001", 47_000, 0.05) || engine.TrailingStopTriggered("test-restore-001", 48_000, 0.05) {
			t.Fatal("trailing stop must fall back to avg price")
		}
	})

	t.Run("GIVEN open positions WHEN cleared THEN no positions or metadata remain", func(t *testing.T) {
		engine := NewEngine(100_000, restoredRepo(), nil)
		_ = engine.RestoreFromDB()

		engine.ClearPositions()

		if _, ok := engine.Meta("test-restore-001"); ok || len(engine.OpenPositions()) != 0 {
			t.Fatal("expected everything cleared")
		}
	})

	t.Run("GIVEN restored cash WHEN set THEN the engine uses it", func(t *testing.T) {
		engine := newEngine(500)

		engine.SetCash(321)

		if engine.Cash() != 321 {
			t.Fatalf("cash %v", engine.Cash())
		}
	})
}
