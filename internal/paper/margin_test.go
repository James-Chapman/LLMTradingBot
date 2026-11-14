package paper

import (
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

// TestPaperShortMargin covers reserving a paper short's proceeds as margin.
func TestPaperShortMargin(t *testing.T) {
	t.Run("GIVEN cash 200 and a short worth 100 WHEN a long for 199 follows and the short is stopped out 20% higher THEN cash never goes negative", func(t *testing.T) {
		engine := newEngine(200)
		engine.Execute(intent("A/EUR", domain.Short, 1), 100, ExecuteOptions{})

		longOrder, _ := engine.Execute(intent("B/EUR", domain.Long, 199), 1, ExecuteOptions{})
		shortID := engine.OpenPositions()[0].PositionID
		_, closed := engine.ClosePosition(shortID, 120, CloseOptions{})

		if longOrder.Status != "rejected" {
			t.Fatalf("the long should be refused while the short's margin is reserved, got %s", longOrder.Status)
		}
		if !closed || engine.Cash() < 0 {
			t.Fatalf("cash went negative after the stop: closed=%v cash=%v", closed, engine.Cash())
		}
	})

	t.Run("GIVEN a paper short is opened WHEN equity is compared at the fill price THEN it differs only by the fee", func(t *testing.T) {
		engine := newEngine(200)
		before := engine.TotalEquity(nil)

		order, _ := engine.Execute(intent("A/EUR", domain.Short, 1), 100, ExecuteOptions{})
		fee := order.Size * order.Price * TakerFeeRate
		after := engine.TotalEquity(map[string]float64{"A/EUR": order.Price})

		if !near(before-after, fee) {
			t.Fatalf("equity changed by %v, want only the fee %v", before-after, fee)
		}
	})

	t.Run("GIVEN an open paper short WHEN available cash is read THEN its proceeds are excluded but cash is unchanged", func(t *testing.T) {
		engine := newEngine(200)
		order, _ := engine.Execute(intent("A/EUR", domain.Short, 1), 100, ExecuteOptions{})
		proceeds := order.Size * order.Price

		if !near(engine.Cash()-engine.AvailableCash(), proceeds) {
			t.Fatalf("available %v should be cash %v less the reserved proceeds %v", engine.AvailableCash(), engine.Cash(), proceeds)
		}
	})

	t.Run("GIVEN only longs WHEN available cash is read THEN it equals cash", func(t *testing.T) {
		engine := newEngine(200)
		engine.Execute(intent("A/EUR", domain.Long, 1), 100, ExecuteOptions{})

		if engine.AvailableCash() != engine.Cash() {
			t.Fatalf("available %v, cash %v", engine.AvailableCash(), engine.Cash())
		}
	})

	t.Run("GIVEN an open short WHEN the short is closed THEN its margin is released", func(t *testing.T) {
		engine := newEngine(200)
		engine.Execute(intent("A/EUR", domain.Short, 1), 100, ExecuteOptions{})
		engine.ClosePosition(engine.OpenPositions()[0].PositionID, 100, CloseOptions{})

		if engine.AvailableCash() != engine.Cash() {
			t.Fatalf("margin still reserved: available %v, cash %v", engine.AvailableCash(), engine.Cash())
		}
	})
}

// TestRestoredPositionAccounting covers positions reloaded after a restart.
func TestRestoredPositionAccounting(t *testing.T) {
	t.Run("GIVEN a restored long with no stored direction WHEN it closes higher THEN the P&L is a profit", func(t *testing.T) {
		repo := &capturingRepo{openRows: []OpenPositionRow{{PositionID: "p1", Market: "BTC/EUR", Size: 1, AvgPrice: 100}}}
		engine := NewEngine(500, repo, nil)
		mustRestore(t, engine)

		order, _ := engine.ClosePosition("p1", 110, CloseOptions{})
		pnl, ok := engine.RecordClosedTrade("p1", order.Price, "manual_close", "")

		if !ok || pnl <= 0 {
			t.Fatalf("a long closed 10%% higher must be a profit, got %v", pnl)
		}
		if len(repo.outcomes) != 1 || repo.outcomes[0].Direction != "long" {
			t.Fatalf("outcome direction should be derived as long: %+v", repo.outcomes)
		}
	})

	t.Run("GIVEN a restored short with no stored direction WHEN it closes lower THEN the P&L is a profit", func(t *testing.T) {
		repo := &capturingRepo{openRows: []OpenPositionRow{{PositionID: "s1", Market: "BTC/EUR", Size: -1, AvgPrice: 100}}}
		engine := NewEngine(500, repo, nil)
		mustRestore(t, engine)

		engine.ClosePosition("s1", 90, CloseOptions{})
		pnl, ok := engine.RealisedPnL("s1")

		if !ok || pnl <= 0 {
			t.Fatalf("a short closed 10%% lower must be a profit, got %v", pnl)
		}
	})

	t.Run("GIVEN a position restored with its entry fee WHEN it closes THEN the P&L includes the entry fee", func(t *testing.T) {
		repo := &capturingRepo{openRows: []OpenPositionRow{{PositionID: "p1", Market: "BTC/EUR", Size: 1, AvgPrice: 100, Direction: "long", EntryFee: 0.26}}}
		engine := NewEngine(500, repo, nil)
		mustRestore(t, engine)

		order, _ := engine.ClosePosition("p1", 100, CloseOptions{})
		pnl, _ := engine.RealisedPnL("p1")

		exitFee := order.Size * order.Price * TakerFeeRate
		want := (order.Price - 100) - 0.26 - exitFee
		if !near(pnl, want) {
			t.Fatalf("pnl %v, want %v including the entry fee", pnl, want)
		}
	})

	t.Run("GIVEN a position opened by the engine WHEN it is persisted THEN the entry fee and short proceeds are stored", func(t *testing.T) {
		repo := &capturingRepo{}
		engine := NewEngine(500, repo, nil)

		order, _ := engine.Execute(intent("BTC/EUR", domain.Short, 1), 100, ExecuteOptions{})

		if len(repo.upserts) != 1 {
			t.Fatalf("expected one upsert, got %+v", repo.upserts)
		}
		row := repo.upserts[0]
		if !near(row.EntryFee, order.Size*order.Price*TakerFeeRate) || row.ShortProceeds == nil || !near(*row.ShortProceeds, order.Size*order.Price) {
			t.Fatalf("fee or proceeds not persisted: %+v", row)
		}
	})

	t.Run("GIVEN a short restored with its stored proceeds WHEN it closes THEN cash settles against those proceeds", func(t *testing.T) {
		repo := &capturingRepo{openRows: []OpenPositionRow{{
			PositionID: "s1", Market: "BTC/EUR", Size: -1, AvgPrice: 100, Direction: "short", ShortProceeds: domain.Float(95),
		}}}
		engine := NewEngine(500, repo, nil)
		mustRestore(t, engine)

		order, _ := engine.ClosePosition("s1", 100, CloseOptions{})

		exitFee := order.Size * order.Price * TakerFeeRate
		if want := 500 + 95 - order.Size*order.Price - exitFee; !near(engine.Cash(), want) {
			t.Fatalf("cash %v, want %v from the stored proceeds", engine.Cash(), want)
		}
	})

	t.Run("GIVEN a restored short without stored proceeds WHEN available cash is read THEN size times entry price is reserved", func(t *testing.T) {
		repo := &capturingRepo{openRows: []OpenPositionRow{{PositionID: "s1", Market: "BTC/EUR", Size: -2, AvgPrice: 50}}}
		engine := NewEngine(500, repo, nil)
		mustRestore(t, engine)

		if !near(engine.AvailableCash(), 400) {
			t.Fatalf("available %v, want 400", engine.AvailableCash())
		}
	})
}

// mustRestore restores the engine from its repository or fails the test.
func mustRestore(t *testing.T, engine *Engine) {
	t.Helper()
	if err := engine.RestoreFromDB(); err != nil {
		t.Fatalf("restore: %v", err)
	}
}
