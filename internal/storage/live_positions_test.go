package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

// TestLivePositions covers the persisted live position book.
func TestLivePositions(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN a live position is saved WHEN the database is reopened THEN it is loaded with every field", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "live.db")
		repo, err := Open(path)
		mustNoErr(t, err)
		opened := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
		pos := LivePosition{
			PositionID: "live-1", Market: "BTC/EUR", Status: "open", RequestedSize: 0.02, Size: 0.015,
			AvgPrice: 50_100, EntryFee: 0.6, EntryOrderID: "order-1", StrategyID: "basic_strategy",
			TradeIdeaID: "idea-1", SignalConfidence: domain.Float(0.8), OpenedAt: opened,
		}
		mustNoErr(t, repo.SaveLivePosition(ctx, pos))
		mustNoErr(t, repo.Close())

		reopened := openTestRepo(t, path)
		loaded, err := reopened.GetLivePositions(ctx)
		mustNoErr(t, err)
		if len(loaded) != 1 {
			t.Fatalf("expected one live position, got %+v", loaded)
		}
		got := loaded[0]
		if got.PositionID != "live-1" || got.Status != "open" || got.Size != 0.015 || got.RequestedSize != 0.02 ||
			got.AvgPrice != 50_100 || got.EntryFee != 0.6 || got.EntryOrderID != "order-1" || got.TradeIdeaID != "idea-1" ||
			got.SignalConfidence == nil || *got.SignalConfidence != 0.8 || !got.OpenedAt.Equal(opened) {
			t.Errorf("live position not restored intact: %+v", got)
		}
	})

	t.Run("GIVEN a saved live position WHEN it is saved again with an exit order THEN the row is updated, not duplicated", func(t *testing.T) {
		repo := newTestRepo(t)
		pos := LivePosition{PositionID: "live-2", Market: "ETH/EUR", Status: "open", Size: 1, AvgPrice: 3000, OpenedAt: time.Now().UTC()}
		mustNoErr(t, repo.SaveLivePosition(ctx, pos))
		pos.Status, pos.ExitOrderID, pos.ExitReason = "exit_pending", "order-exit", "stop_loss"
		mustNoErr(t, repo.SaveLivePosition(ctx, pos))

		loaded, err := repo.GetLivePositions(ctx)
		mustNoErr(t, err)
		if len(loaded) != 1 || loaded[0].Status != "exit_pending" || loaded[0].ExitOrderID != "order-exit" || loaded[0].ExitReason != "stop_loss" {
			t.Errorf("expected one updated row, got %+v", loaded)
		}
	})

	t.Run("GIVEN a live position WHEN it is deleted THEN it is gone", func(t *testing.T) {
		repo := newTestRepo(t)
		mustNoErr(t, repo.SaveLivePosition(ctx, LivePosition{PositionID: "live-3", Market: "BTC/EUR", Status: "open", OpenedAt: time.Now().UTC()}))
		mustNoErr(t, repo.DeleteLivePosition(ctx, "live-3"))
		loaded, err := repo.GetLivePositions(ctx)
		mustNoErr(t, err)
		if len(loaded) != 0 {
			t.Errorf("expected no live positions, got %+v", loaded)
		}
	})

	t.Run("GIVEN paper and live positions WHEN the paper positions are cleared THEN the live position is kept", func(t *testing.T) {
		repo := newTestRepo(t)
		mustNoErr(t, repo.UpsertOpenPosition(ctx, OpenPositionInput{PositionID: "paper-1", Market: "BTC/EUR", Size: 0.1, AvgPrice: 50_000, Direction: "long"}))
		mustNoErr(t, repo.SaveLivePosition(ctx, LivePosition{PositionID: "live-4", Market: "BTC/EUR", Status: "open", Size: 0.1, OpenedAt: time.Now().UTC()}))

		_, err := repo.ClearAllOpenPositions(ctx)
		mustNoErr(t, err)

		live, err := repo.GetLivePositions(ctx)
		mustNoErr(t, err)
		paper, err := repo.GetOpenPositions(ctx)
		mustNoErr(t, err)
		if len(live) != 1 || len(paper) != 0 {
			t.Errorf("a paper reset must not touch the live book: live=%+v paper=%+v", live, paper)
		}
	})
}

// TestLiveOrderReconciliationStorage covers the order columns reconciliation reads and writes.
func TestLiveOrderReconciliationStorage(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)

	t.Run("GIVEN a pending live order with a client order id but no txid WHEN pending orders are listed THEN it is included with its size, client id and time", func(t *testing.T) {
		repo := newTestRepo(t)
		order := domain.OrderRecord{ID: "order-c", Market: "BTC/EUR", Direction: domain.Long, Size: 0.01, Price: 50_000, Status: "pending", Timestamp: at}
		mustNoErr(t, repo.SaveOrder(ctx, order, SaveOrderOptions{Environment: "live", ClientOrderID: "order-c"}))
		legacy := domain.OrderRecord{ID: "order-legacy", Market: "BTC/EUR", Direction: domain.Long, Size: 0.01, Price: 50_000, Status: "pending", Timestamp: at}
		mustNoErr(t, repo.SaveOrder(ctx, legacy, SaveOrderOptions{Environment: "live"}))

		pending, err := repo.GetPendingLiveOrders(ctx)
		mustNoErr(t, err)
		if len(pending) != 1 {
			t.Fatalf("expected only the reconcilable order, got %+v", pending)
		}
		p := pending[0]
		if p.ID != "order-c" || p.ClientOrderID != "order-c" || p.ExchangeOrderID != "" || p.Size != 0.01 || !p.Timestamp.Equal(at) {
			t.Errorf("unexpected pending order %+v", p)
		}
	})

	t.Run("GIVEN a pending live order WHEN a partial execution is recorded THEN size, price, fee and txid reflect what executed", func(t *testing.T) {
		repo := newTestRepo(t)
		order := domain.OrderRecord{ID: "order-p", Market: "BTC/EUR", Direction: domain.Long, Size: 0.02, Price: 50_000, Status: "pending", Timestamp: at}
		mustNoErr(t, repo.SaveOrder(ctx, order, SaveOrderOptions{Environment: "live", ClientOrderID: "order-p"}))

		mustNoErr(t, repo.UpdateOrderExecution(ctx, OrderExecution{
			OrderID: "order-p", ExchangeOrderID: "TXP", Status: "filled", ExecutedSize: 0.005, AvgPrice: 50_200, Fee: 0.4,
		}))

		ledger, err := repo.GetTradeLedger(ctx, 0)
		mustNoErr(t, err)
		if len(ledger) != 1 || ledger[0].Size != 0.005 || ledger[0].Price != 50_200 || ledger[0].Fee != 0.4 || ledger[0].ExchangeOrderID != "TXP" || ledger[0].Status != "filled" {
			t.Errorf("execution not recorded: %+v", ledger)
		}
	})

	t.Run("GIVEN a pending live order WHEN it is cancelled with nothing executed THEN its requested size is kept and it leaves the pending list", func(t *testing.T) {
		repo := newTestRepo(t)
		order := domain.OrderRecord{ID: "order-z", Market: "BTC/EUR", Direction: domain.Long, Size: 0.02, Price: 50_000, Status: "pending", Timestamp: at, ExchangeOrderID: "TXZ"}
		mustNoErr(t, repo.SaveOrder(ctx, order, SaveOrderOptions{Environment: "live", ClientOrderID: "order-z"}))

		mustNoErr(t, repo.UpdateOrderExecution(ctx, OrderExecution{OrderID: "order-z", Status: "canceled", AvgPrice: 50_000}))

		pending, err := repo.GetPendingLiveOrders(ctx)
		mustNoErr(t, err)
		if len(pending) != 0 {
			t.Fatalf("cancelled order still pending: %+v", pending)
		}
		var size float64
		var txid string
		mustNoErr(t, repo.db.QueryRowContext(ctx, "SELECT size, exchange_order_id FROM order_records WHERE id = 'order-z'").Scan(&size, &txid))
		if size != 0.02 || txid != "TXZ" {
			t.Errorf("an empty update must keep the requested size and txid, got %v %q", size, txid)
		}
	})

	t.Run("GIVEN a pending live order of a position WHEN pending orders are listed THEN its position and direction are included", func(t *testing.T) {
		repo := newTestRepo(t)
		order := domain.OrderRecord{ID: "order-s", Market: "BTC/EUR", Direction: domain.Short, Size: 0.01, Price: 50_000, Status: "pending", Timestamp: at, PositionID: "live-9"}
		mustNoErr(t, repo.SaveOrder(ctx, order, SaveOrderOptions{Environment: "live", ClientOrderID: "order-s"}))

		pending, err := repo.GetPendingLiveOrders(ctx)
		mustNoErr(t, err)
		if len(pending) != 1 || pending[0].PositionID != "live-9" || pending[0].Direction != "short" {
			t.Errorf("expected the position and direction, got %+v", pending)
		}
	})

	t.Run("GIVEN a position's live orders WHEN they are listed by position THEN each comes with its execution and whether its P&L is stamped", func(t *testing.T) {
		repo := newTestRepo(t)
		buy := domain.OrderRecord{ID: "order-b", Market: "BTC/EUR", Direction: domain.Long, Size: 0.01, Price: 50_000, Status: "filled", Timestamp: at, PositionID: "live-7"}
		sell := domain.OrderRecord{ID: "order-x", Market: "BTC/EUR", Direction: domain.Short, Size: 0.01, Price: 51_000, Status: "filled", Timestamp: at.Add(time.Minute), PositionID: "live-7"}
		other := domain.OrderRecord{ID: "order-o", Market: "BTC/EUR", Direction: domain.Short, Size: 0.01, Price: 51_000, Status: "pending", Timestamp: at, PositionID: "live-8"}
		for _, o := range []domain.OrderRecord{sell, buy, other} {
			mustNoErr(t, repo.SaveOrder(ctx, o, SaveOrderOptions{Environment: "live", ClientOrderID: o.ID}))
		}
		mustNoErr(t, repo.UpdateOrderPnL(ctx, "order-x", 9.5))

		orders, err := repo.GetLiveOrdersForPosition(ctx, "live-7")
		mustNoErr(t, err)
		if len(orders) != 2 || orders[0].OrderID != "order-b" || orders[1].OrderID != "order-x" {
			t.Fatalf("expected the position's two orders oldest first, got %+v", orders)
		}
		if orders[0].PnLRecorded || !orders[1].PnLRecorded || orders[1].Direction != "short" || orders[1].Price != 51_000 || orders[1].ClientOrderID != "order-x" {
			t.Errorf("unexpected order details %+v", orders)
		}
	})

	t.Run("GIVEN a live position with a trailing high and a pending exit WHEN the database is reopened THEN both are restored", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "trail.db")
		repo, err := Open(path)
		mustNoErr(t, err)
		requested := at.Add(time.Hour)
		mustNoErr(t, repo.SaveLivePosition(ctx, LivePosition{
			PositionID: "live-t", Market: "BTC/EUR", Status: "exit_pending", Size: 0.01, AvgPrice: 100,
			OpenedAt: at, TrailingHigh: 120, ExitOrderID: "order-e", ExitRequestedAt: requested,
		}))
		mustNoErr(t, repo.Close())

		loaded, err := openTestRepo(t, path).GetLivePositions(ctx)
		mustNoErr(t, err)
		if len(loaded) != 1 || loaded[0].TrailingHigh != 120 || !loaded[0].ExitRequestedAt.Equal(requested) {
			t.Errorf("trailing high or exit time lost: %+v", loaded)
		}
	})

	t.Run("GIVEN a live_positions table from before trailing highs were stored WHEN the database opens THEN the columns are added and old rows still load", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "legacy_live.db")
		createLegacyDB(t, path,
			`CREATE TABLE live_positions (
				position_id VARCHAR NOT NULL, market VARCHAR, status VARCHAR, requested_size FLOAT, size FLOAT,
				avg_price FLOAT, entry_fee FLOAT, entry_order_id VARCHAR, exit_order_id VARCHAR, exit_reason VARCHAR,
				exit_trade_idea_id VARCHAR, strategy_id VARCHAR, trade_idea_id VARCHAR, signal_confidence FLOAT,
				opened_at DATETIME, updated_at DATETIME, PRIMARY KEY (position_id))`,
			`INSERT INTO live_positions (position_id, market, status, size, avg_price, opened_at)
			 VALUES ('old-live', 'BTC/EUR', 'open', 0.1, 50000.0, '2026-11-14 12:00:00.000000')`,
		)
		repo := openTestRepo(t, path)

		cols := columnsOf(t, repo.db, "live_positions")
		if !cols["trailing_high"] || !cols["exit_requested_at"] {
			t.Fatalf("live_positions columns not added: %v", cols)
		}
		loaded, err := repo.GetLivePositions(ctx)
		mustNoErr(t, err)
		if len(loaded) != 1 || loaded[0].TrailingHigh != 0 || !loaded[0].ExitRequestedAt.IsZero() {
			t.Errorf("an old row must load with no trailing high, got %+v", loaded)
		}
	})

	t.Run("GIVEN a legacy order_records table WHEN the database opens THEN the client order id column is added", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "legacy_orders.db")
		createLegacyDB(t, path,
			`CREATE TABLE order_records (
				id VARCHAR NOT NULL, approval_request_id VARCHAR, market VARCHAR, direction VARCHAR,
				size FLOAT, price FLOAT, fee FLOAT, status VARCHAR, environment VARCHAR,
				timestamp DATETIME, exchange_order_id VARCHAR, PRIMARY KEY (id))`,
			`INSERT INTO order_records (id, market, direction, size, price, fee, status, environment, timestamp, exchange_order_id)
			 VALUES ('old-live', 'BTC/EUR', 'long', 0.1, 50000.0, 0.0, 'pending', 'live', '2026-04-24 12:00:00.000000', 'TXOLD')`,
		)
		repo := openTestRepo(t, path)

		if !columnsOf(t, repo.db, "order_records")["client_order_id"] {
			t.Fatal("order_records.client_order_id was not added")
		}
		pending, err := repo.GetPendingLiveOrders(ctx)
		mustNoErr(t, err)
		if len(pending) != 1 || pending[0].ExchangeOrderID != "TXOLD" || pending[0].ClientOrderID != "" {
			t.Errorf("an existing pending order must still reconcile by txid, got %+v", pending)
		}
	})
}
