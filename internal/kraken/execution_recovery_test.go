package kraken

import (
	"context"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

// btcEURMetadata queues Kraken asset metadata for BTC/EUR with 8 lot
// decimals and an order minimum of 0.0001 BTC.
func btcEURMetadata(f *fakeKraken) {
	f.on("/0/public/Assets", `{"error":[],"result":{
		"XXBT":{"aclass":"currency","altname":"XBT","decimals":10,"display_decimals":5},
		"ZEUR":{"aclass":"currency","altname":"EUR","decimals":4,"display_decimals":2}}}`)
	f.on("/0/public/AssetPairs", `{"error":[],"result":{"XXBTZEUR":{"altname":"XBTEUR","wsname":"XBT/EUR","base":"XXBT","quote":"ZEUR",
		"pair_decimals":1,"lot_decimals":8,"lot_multiplier":1,"tick_size":"0.1","ordermin":"0.0001"}}}`)
}

// TestExecutionEngineOrderSafety covers once-only placement details: caller
// order IDs, sell volumes and orders that never left the process.
func TestExecutionEngineOrderSafety(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN an order ID allocated by the caller WHEN the order is submitted THEN it is the order's ID and its client order id", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/private/AddOrder", `{"error":[],"result":{"txid":["TX1"]}}`)
		repo := &fakeRepo{}
		engine := newTestEngine(f, repo, nil)

		order, _ := engine.Execute(ctx, intent("BTC/EUR", domain.Long, 0.01), ExecuteOptions{MarketPrice: 50_000, OrderID: "intent-1"})

		if order.ID != "intent-1" || f.calls("/0/private/AddOrder")[0].Body["cl_ord_id"] != "intent-1" {
			t.Fatalf("expected the caller's order ID everywhere, got %+v", order)
		}
		if len(repo.orders) != 1 || repo.orders[0].Order.ID != "intent-1" || repo.orders[0].ClientOrderID != "intent-1" {
			t.Fatalf("expected the order saved under the caller's ID, got %+v", repo.orders)
		}
	})

	t.Run("GIVEN asset metadata WHEN a sell is placed THEN its volume is rounded down to the lot size so it never exceeds the holding", func(t *testing.T) {
		f := newFakeKraken(t)
		btcEURMetadata(f)
		f.on("/0/private/AddOrder", `{"error":[],"result":{"txid":["TXS"]}}`)
		engine := newTestEngine(f, nil, nil)

		engine.Execute(ctx, intent("BTC/EUR", domain.Short, 0.0123456789), ExecuteOptions{MarketPrice: 50_000})

		if body := f.calls("/0/private/AddOrder")[0].Body; body["volume"] != "0.01234567" {
			t.Fatalf("expected the sell volume rounded down, got %v", body["volume"])
		}
	})

	t.Run("GIVEN no asset metadata WHEN a sell is placed THEN the eight-decimal fallback also rounds down", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/private/AddOrder", `{"error":[],"result":{"txid":["TXS"]}}`)
		engine := newTestEngine(f, nil, nil)

		engine.Execute(ctx, intent("BTC/EUR", domain.Short, 0.0123456789), ExecuteOptions{MarketPrice: 50_000})

		if body := f.calls("/0/private/AddOrder")[0].Body; body["volume"] != "0.01234567" {
			t.Fatalf("expected the fallback sell volume rounded down, got %v", body["volume"])
		}
	})

	t.Run("GIVEN a size with more decimals than Kraken accepts WHEN the order is submitted THEN the order records the volume actually sent", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/private/AddOrder", `{"error":[],"result":{"txid":["TXS"]}}`)
		repo := &fakeRepo{}
		engine := newTestEngine(f, repo, nil)

		order, _ := engine.Execute(ctx, intent("BTC/EUR", domain.Short, 0.0123456789), ExecuteOptions{MarketPrice: 50_000})

		if order.Size != 0.01234567 || len(repo.orders) != 1 || repo.orders[0].Order.Size != 0.01234567 {
			t.Fatalf("expected the sent volume 0.01234567 on the order, got %+v / %+v", order, repo.orders)
		}
	})

	t.Run("GIVEN the request cannot be built WHEN an order is submitted THEN nothing reaches Kraken and the order is rejected, not left pending", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/private/AddOrder", `{"error":[],"result":{"txid":["TX-NEVER"]}}`)
		repo := &fakeRepo{}
		engine := NewExecutionEngine(ExecutionOptions{
			APIKey: "test-key", APISecret: "%%% not base64 %%%", RESTURL: f.server.URL, HTTPClient: f.server.Client(),
			Repository: repo, Backoff: BackoffOptions{Sleep: recordingSleep(new([]time.Duration)), Jitter: zeroJitter},
		})

		order, positionID := engine.Execute(ctx, intent("BTC/EUR", domain.Long, 0.01), ExecuteOptions{MarketPrice: 50_000, PositionID: "pos-x"})

		if n := len(f.calls("/0/private/AddOrder")); n != 0 {
			t.Fatalf("expected no request to reach Kraken, got %d", n)
		}
		if order.Status != "rejected" || positionID != "" || len(repo.orders) != 0 || len(repo.rejected) != 1 {
			t.Fatalf("an order that was never sent must be rejected, got %+v / %q / %+v", order, positionID, repo.orders)
		}
	})

	t.Run("GIVEN asset metadata WHEN the minimum order size is asked for THEN Kraken's ordermin is returned", func(t *testing.T) {
		f := newFakeKraken(t)
		btcEURMetadata(f)
		engine := newTestEngine(f, nil, nil)

		minimum, ok := engine.MinOrderSize(ctx, "BTC/EUR")

		if !ok || minimum != 0.0001 {
			t.Fatalf("expected ordermin 0.0001, got %v %v", minimum, ok)
		}
		if _, ok := engine.MinOrderSize(ctx, "NOPE/EUR"); ok {
			t.Error("an unknown pair has no known minimum")
		}
	})
}

// TestExecutionEngineReconcileTracked covers reconciling orders by client
// order id: verifying Kraken's answer and orders the ledger does not list.
func TestExecutionEngineReconcileTracked(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN a lookup by client order id WHEN Kraken returns another order's fill THEN it is ignored", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/private/OpenOrders", `{"error":[],"result":{"open":{"TXOTHER-OPEN":{"status":"open","cl_ord_id":"someone-else","vol":"1","vol_exec":"0"}}}}`)
		f.on("/0/private/ClosedOrders", `{"error":[],"result":{"closed":{"TXOTHER":{"status":"closed","cl_ord_id":"someone-else","vol":"1","vol_exec":"1","price":"50000","fee":"1"}}}}`)
		repo := &fakeRepo{pending: []PendingLiveOrder{{ID: "order-m", ClientOrderID: "order-m", Size: 0.01, Price: 50_000, Timestamp: time.Now().UTC()}}}
		engine := newTestEngine(f, repo, nil)

		updated := engine.ReconcilePendingOrders(ctx, nil)

		if len(updated) != 0 || len(repo.updates) != 0 {
			t.Fatalf("another order's result must not be applied, got %+v / %+v", updated, repo.updates)
		}
	})

	t.Run("GIVEN a tracked order whose ledger row could not be saved WHEN reconciliation runs THEN it is found at Kraken by client order id", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/private/OpenOrders", `{"error":[],"result":{"open":{}}}`)
		f.on("/0/private/ClosedOrders", `{"error":[],"result":{"closed":{"TXT":{"status":"closed","cl_ord_id":"order-t","vol":"0.01","vol_exec":"0.01","price":"50100","fee":"0.5"}}}}`)
		repo := &fakeRepo{} // the ledger has no pending row for it
		engine := newTestEngine(f, repo, nil)
		tracked := []PendingLiveOrder{{ID: "order-t", PositionID: "pos-t", Direction: domain.Short, Size: 0.01, Price: 50_000, Timestamp: time.Now().UTC()}}

		updated := engine.ReconcilePendingOrders(ctx, tracked)

		if len(updated) != 1 || updated[0].OrderID != "order-t" || updated[0].Status != "filled" || updated[0].ExecutedSize != 0.01 {
			t.Fatalf("expected the tracked order's fill, got %+v", updated)
		}
		if updated[0].PositionID != "pos-t" || updated[0].Direction != domain.Short {
			t.Errorf("the update must name its position and side, got %+v", updated[0])
		}
		if calls := f.calls("/0/private/ClosedOrders"); len(calls) != 1 || calls[0].Body["cl_ord_id"] != "order-t" {
			t.Errorf("expected a lookup by the order ID as client order id, got %+v", calls)
		}
	})

	t.Run("GIVEN a tracked order the ledger already lists as pending WHEN reconciliation runs THEN it is looked up once", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/private/QueryOrders", `{"error":[],"result":{"TXP":{"status":"open","vol":"0.01","vol_exec":"0"}}}`)
		pending := PendingLiveOrder{ID: "order-p", ExchangeOrderID: "TXP", ClientOrderID: "order-p", Size: 0.01, Price: 50_000, Timestamp: time.Now().UTC()}
		repo := &fakeRepo{pending: []PendingLiveOrder{pending}}
		engine := newTestEngine(f, repo, nil)

		engine.ReconcilePendingOrders(ctx, []PendingLiveOrder{{ID: "order-p", Timestamp: time.Now().UTC()}})

		if n := len(f.calls("/0/private/QueryOrders")); n != 1 {
			t.Errorf("expected one QueryOrders call, got %d", n)
		}
		if n := len(f.calls("/0/private/ClosedOrders")); n != 0 {
			t.Errorf("an order already reconciled by txid must not also be looked up by client id, got %d", n)
		}
	})

	t.Run("GIVEN a tracked order Kraken never received WHEN the grace period has passed THEN it is reported not found with its position", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/private/OpenOrders", `{"error":[],"result":{"open":{}}}`)
		f.on("/0/private/ClosedOrders", `{"error":[],"result":{"closed":{}}}`)
		engine := newTestEngine(f, &fakeRepo{}, nil)
		tracked := []PendingLiveOrder{
			{ID: "order-old", PositionID: "pos-old", Direction: domain.Long, Size: 0.01, Price: 50_000, Timestamp: time.Now().UTC().Add(-time.Hour)},
			{ID: "order-new", PositionID: "pos-new", Direction: domain.Long, Size: 0.01, Price: 50_000, Timestamp: time.Now().UTC()},
		}

		updated := engine.ReconcilePendingOrders(ctx, tracked)

		if len(updated) != 1 || updated[0].OrderID != "order-old" || updated[0].Status != "not_found" || updated[0].PositionID != "pos-old" {
			t.Fatalf("only the old order should be given up on, got %+v", updated)
		}
	})

	t.Run("GIVEN a pending order of a position WHEN Kraken reports it filled THEN the update names its position and side", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/private/QueryOrders", `{"error":[],"result":{"TX1":{"status":"closed","vol":"0.01","vol_exec":"0.01","price":"50100","fee":"0.5"}}}`)
		repo := &fakeRepo{pending: []PendingLiveOrder{{ID: "order-1", ExchangeOrderID: "TX1", PositionID: "pos-1", Direction: domain.Long, Size: 0.01, Price: 50_000}}}
		engine := newTestEngine(f, repo, nil)

		updated := engine.ReconcilePendingOrders(ctx, nil)

		if len(updated) != 1 || updated[0].PositionID != "pos-1" || updated[0].Direction != domain.Long {
			t.Fatalf("expected the position and side on the update, got %+v", updated)
		}
	})
}
