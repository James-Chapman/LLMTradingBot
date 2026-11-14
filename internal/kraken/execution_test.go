package kraken

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

// testSecret is a syntactically valid (base64) Kraken API secret for signing.
var testSecret = base64.StdEncoding.EncodeToString([]byte("test-secret"))

// savedOrder records one SaveOrder call on fakeRepo.
type savedOrder struct {
	Order       domain.OrderRecord
	ApprovalID  string
	Fee         float64
	Environment string
	TradeIdeaID string
}

// orderUpdate records one UpdateOrderStatusAndFill call on fakeRepo.
type orderUpdate struct {
	OrderID   string
	Status    string
	FillPrice float64
	Fee       float64
}

// fakeRepo is an in-memory OrderRepository for engine tests.
type fakeRepo struct {
	pending  []PendingLiveOrder
	orders   []savedOrder
	rejected []RejectedTrade
	updates  []orderUpdate
}

// SaveOrder records a saved order.
func (r *fakeRepo) SaveOrder(order domain.OrderRecord, approvalID string, fee float64, environment, tradeIdeaID string) error {
	r.orders = append(r.orders, savedOrder{order, approvalID, fee, environment, tradeIdeaID})
	return nil
}

// SaveRejectedTrade records a rejected trade.
func (r *fakeRepo) SaveRejectedTrade(trade RejectedTrade) error {
	r.rejected = append(r.rejected, trade)
	return nil
}

// GetPendingLiveOrders returns the configured pending orders.
func (r *fakeRepo) GetPendingLiveOrders() ([]PendingLiveOrder, error) {
	return append([]PendingLiveOrder(nil), r.pending...), nil
}

// UpdateOrderStatusAndFill records a status update.
func (r *fakeRepo) UpdateOrderStatusAndFill(orderID, status string, fillPrice, fee float64) error {
	r.updates = append(r.updates, orderUpdate{orderID, status, fillPrice, fee})
	return nil
}

// newTestEngine builds an authenticated ExecutionEngine pointed at a fake Kraken server.
func newTestEngine(f *fakeKraken, repo OrderRepository, sleeps *[]time.Duration) *ExecutionEngine {
	var discard []time.Duration
	if sleeps == nil {
		sleeps = &discard
	}
	return NewExecutionEngine(ExecutionOptions{
		APIKey:     "test-key",
		APISecret:  testSecret,
		RESTURL:    f.server.URL,
		HTTPClient: f.server.Client(),
		Repository: repo,
		Backoff:    BackoffOptions{Sleep: recordingSleep(sleeps), Jitter: zeroJitter},
	})
}

// intent builds an ExecutionIntent for tests.
func intent(market string, direction domain.Direction, size float64) domain.ExecutionIntent {
	return domain.ExecutionIntent{ApprovalRequestID: "approval-1", Market: market, Direction: direction, Size: size}
}

// TestExecutionEngineExecute covers live order placement.
func TestExecutionEngineExecute(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN a live long intent WHEN Kraken accepts the order THEN a signed market buy is submitted and the order saved as pending", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/private/AddOrder", `{"error":[],"result":{"descr":{"order":"buy 0.01 XBTEUR @ market"},"txid":["TX123"]}}`)
		repo := &fakeRepo{}
		engine := newTestEngine(f, repo, nil)

		order, positionID := engine.Execute(ctx, intent("BTC/EUR", domain.Long, 0.01), ExecuteOptions{MarketPrice: 50_000, TradeIdeaID: "idea-1"})

		if order.Status != "pending" || order.ExchangeOrderID != "TX123" || positionID != "" {
			t.Fatalf("unexpected result order=%+v position=%q", order, positionID)
		}
		if order.Price != 50_000 || order.ID == "" || order.Timestamp.Location() != time.UTC {
			t.Fatalf("expected market price, id and UTC timestamp, got %+v", order)
		}
		calls := f.calls("/0/private/AddOrder")
		if len(calls) != 1 {
			t.Fatalf("expected one AddOrder call, got %d", len(calls))
		}
		body := calls[0].Body
		if body["pair"] != "XBTEUR" || body["type"] != "buy" || body["ordertype"] != "market" || body["price"] != nil {
			t.Fatalf("unexpected AddOrder body %v", body)
		}
		if calls[0].Headers.Get("API-Key") != "test-key" || calls[0].Headers.Get("API-Sign") == "" {
			t.Fatal("expected a signed private request")
		}
		if len(repo.orders) != 1 || repo.orders[0].Environment != "live" || repo.orders[0].ApprovalID != "approval-1" || repo.orders[0].TradeIdeaID != "idea-1" {
			t.Fatalf("expected saved live order, got %+v", repo.orders)
		}
	})

	t.Run("GIVEN Kraken returns an error WHEN a live short is submitted THEN the sell order is rejected with the Kraken reason", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/private/AddOrder", `{"error":["EOrder:Insufficient funds"]}`)
		engine := newTestEngine(f, nil, nil)

		order, positionID := engine.Execute(ctx, intent("ETH/EUR", domain.Short, 0.5), ExecuteOptions{MarketPrice: 2_000})

		if order.Status != "rejected" || !strings.Contains(order.ExchangeOrderID, "EOrder:Insufficient funds") || positionID != "" {
			t.Fatalf("unexpected result %+v", order)
		}
		calls := f.calls("/0/private/AddOrder")
		if len(calls) != 1 || calls[0].Body["type"] != "sell" {
			t.Fatalf("expected one sell attempt, got %+v", calls)
		}
	})

	t.Run("GIVEN Kraken rate-limits a live order WHEN execution retries THEN the order is submitted after backoff", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/private/AddOrder",
			`{"error":["EAPI:Rate limit exceeded"]}`,
			`{"error":[],"result":{"txid":["TX456"]}}`)
		var sleeps []time.Duration
		engine := newTestEngine(f, nil, &sleeps)

		order, _ := engine.Execute(ctx, intent("BTC/EUR", domain.Long, 0.01), ExecuteOptions{MarketPrice: 50_000})

		if order.Status != "pending" || order.ExchangeOrderID != "TX456" {
			t.Fatalf("unexpected result %+v", order)
		}
		if n := len(f.calls("/0/private/AddOrder")); n != 2 {
			t.Fatalf("expected 2 AddOrder calls, got %d", n)
		}
		if len(sleeps) != 1 || sleeps[0] != 500*time.Millisecond {
			t.Fatalf("expected one 500ms backoff, got %v", sleeps)
		}
	})

	t.Run("GIVEN no API credentials WHEN live execution is requested THEN the order is rejected locally without calling Kraken", func(t *testing.T) {
		f := newFakeKraken(t)
		repo := &fakeRepo{}
		engine := NewExecutionEngine(ExecutionOptions{RESTURL: f.server.URL, HTTPClient: f.server.Client(), Repository: repo})

		order, positionID := engine.Execute(ctx, intent("BTC/EUR", domain.Long, 0.01), ExecuteOptions{MarketPrice: 50_000})

		if order.Status != "rejected" || order.ExchangeOrderID != ReasonMissingCredentials || positionID != "" {
			t.Fatalf("unexpected result %+v", order)
		}
		if f.totalRequests() != 0 {
			t.Fatalf("expected no Kraken calls, got %d", f.totalRequests())
		}
		if len(repo.rejected) != 1 || len(repo.orders) != 0 {
			t.Fatalf("expected one rejected trade and no orders, got %+v / %+v", repo.rejected, repo.orders)
		}
		if engine.HasCredentials() {
			t.Fatal("engine must report missing credentials")
		}
	})

	t.Run("GIVEN a paper environment WHEN the live engine is asked to execute THEN nothing is sent to Kraken", func(t *testing.T) {
		f := newFakeKraken(t)
		engine := newTestEngine(f, nil, nil)

		order, _ := engine.Execute(ctx, intent("BTC/EUR", domain.Long, 0.01), ExecuteOptions{MarketPrice: 50_000, Environment: "paper"})

		if order.Status != "rejected" || order.ExchangeOrderID != ReasonPaperModeReadOnly {
			t.Fatalf("unexpected result %+v", order)
		}
		if f.totalRequests() != 0 {
			t.Fatalf("expected no Kraken calls, got %d", f.totalRequests())
		}
	})

	t.Run("GIVEN live execution is rejected WHEN a repository is configured THEN the rejected-trades register is used instead of the ledger", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/private/AddOrder", `{"error":["EOrder:Insufficient funds"]}`)
		repo := &fakeRepo{}
		engine := newTestEngine(f, repo, nil)

		order, _ := engine.Execute(ctx, intent("ETH/EUR", domain.Short, 0.5), ExecuteOptions{
			MarketPrice:      2_000,
			SignalConfidence: domain.Float(0.71),
			TradeIdeaID:      "idea-live-rejected-1",
		})

		if order.Status != "rejected" || len(repo.orders) != 0 || len(repo.rejected) != 1 {
			t.Fatalf("expected one rejected trade only, got orders=%+v rejected=%+v", repo.orders, repo.rejected)
		}
		rejected := repo.rejected[0]
		if rejected.Market != "ETH/EUR" || rejected.Direction != domain.Short || rejected.Size != 0.5 || rejected.Price != 2_000 {
			t.Fatalf("unexpected rejected trade %+v", rejected)
		}
		if rejected.Confidence == nil || *rejected.Confidence != 0.71 || rejected.TradeIdeaID != "idea-live-rejected-1" {
			t.Fatalf("unexpected rejected trade metadata %+v", rejected)
		}
		if !strings.Contains(rejected.Reason, "EOrder:Insufficient funds") {
			t.Fatalf("unexpected reason %q", rejected.Reason)
		}
	})

	t.Run("GIVEN Kraken asset metadata WHEN a limit order is placed THEN price and volume are formatted to the pair's tick and lot sizes", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/public/Assets", `{"error":[],"result":{
			"XXBT":{"aclass":"currency","altname":"XBT","decimals":10,"display_decimals":5},
			"ZEUR":{"aclass":"currency","altname":"EUR","decimals":4,"display_decimals":2}}}`)
		f.on("/0/public/AssetPairs", `{"error":[],"result":{"XXBTZEUR":{"altname":"XBTEUR","wsname":"XBT/EUR","base":"XXBT","quote":"ZEUR",
			"pair_decimals":1,"lot_decimals":8,"lot_multiplier":1,"tick_size":"0.1"}}}`)
		f.on("/0/private/AddOrder", `{"error":[],"result":{"txid":["TX789"]}}`)
		engine := newTestEngine(f, nil, nil)
		in := intent("BTC/EUR", domain.Long, 0.0123456789)
		in.Price = domain.Float(50_000.123)

		order, _ := engine.Execute(ctx, in, ExecuteOptions{MarketPrice: 49_000})

		if order.Status != "pending" || order.Price != 50_000.123 {
			t.Fatalf("expected pending limit order at the intent price, got %+v", order)
		}
		body := f.calls("/0/private/AddOrder")[0].Body
		if body["ordertype"] != "limit" || body["price"] != "50000.1" || body["volume"] != "0.01234568" {
			t.Fatalf("unexpected formatted AddOrder body %v", body)
		}
	})

	t.Run("GIVEN asset metadata is unavailable WHEN an order is placed THEN volume falls back to eight decimals", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/private/AddOrder", `{"error":[],"result":{"txid":["TX1"]}}`)
		engine := newTestEngine(f, nil, nil)

		order, _ := engine.Execute(ctx, intent("BTC/EUR", domain.Long, 0.5), ExecuteOptions{MarketPrice: 1})

		if order.Status != "pending" {
			t.Fatalf("expected order to proceed without metadata, got %+v", order)
		}
		if body := f.calls("/0/private/AddOrder")[0].Body; body["volume"] != "0.50000000" {
			t.Fatalf("unexpected fallback volume %v", body["volume"])
		}
	})
}

// TestExecutionEngineAccountSnapshot covers live balance reads.
func TestExecutionEngineAccountSnapshot(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN live Kraken balances WHEN a snapshot is requested THEN cash comes from Balance and equity from TradeBalance", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/private/Balance", `{"error":[],"result":{"ZEUR":"250.00","XXBT":"0.10000000"}}`)
		f.on("/0/private/TradeBalance", `{"error":[],"result":{"eb":"9250.00","tb":"9000.00"}}`)
		engine := newTestEngine(f, nil, nil)

		snap, err := engine.GetAccountSnapshot(ctx, "EUR")

		if err != nil || snap == nil {
			t.Fatalf("unexpected error %v", err)
		}
		if snap.Cash != 250 || snap.Equity != 9250 || snap.PositionsValue != 9000 {
			t.Fatalf("unexpected snapshot %+v", snap)
		}
		if tb := f.calls("/0/private/TradeBalance"); len(tb) != 1 || tb[0].Body["asset"] != "ZEUR" {
			t.Fatalf("expected TradeBalance for ZEUR, got %+v", tb)
		}
	})

	t.Run("GIVEN a USD account WHEN a snapshot is requested THEN TradeBalance is queried for ZUSD", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/private/Balance", `{"error":[],"result":{"ZUSD":"2000.00"}}`)
		f.on("/0/private/TradeBalance", `{"error":[],"result":{"eb":"2500.00"}}`)
		engine := newTestEngine(f, nil, nil)

		snap, err := engine.GetAccountSnapshot(ctx, "usd")

		if err != nil || snap.Cash != 2000 || snap.Equity != 2500 {
			t.Fatalf("unexpected snapshot %+v err=%v", snap, err)
		}
		if f.calls("/0/private/TradeBalance")[0].Body["asset"] != "ZUSD" {
			t.Fatal("expected ZUSD asset")
		}
	})

	t.Run("GIVEN balances keyed without the Z prefix WHEN a snapshot is requested THEN cash is still read", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/private/Balance", `{"error":[],"result":{"EUR":"850.00","XXBT":"0.01000000"}}`)
		f.on("/0/private/TradeBalance", `{"error":[],"result":{"eb":"1650.00"}}`)
		engine := newTestEngine(f, nil, nil)

		snap, err := engine.GetAccountSnapshot(ctx, "EUR")

		if err != nil || snap.Cash != 850 || snap.Equity != 1650 || snap.PositionsValue != 800 {
			t.Fatalf("unexpected snapshot %+v err=%v", snap, err)
		}
	})

	t.Run("GIVEN TradeBalance fails WHEN a snapshot is requested THEN equity falls back to cash", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/private/Balance", `{"error":[],"result":{"ZEUR":"300.00"}}`)
		f.on("/0/private/TradeBalance", `{"error":["EGeneral:Internal error"]}`)
		engine := newTestEngine(f, nil, nil)

		snap, err := engine.GetAccountSnapshot(ctx, "EUR")

		if err != nil || snap.Cash != 300 || snap.Equity != 300 || snap.PositionsValue != 0 {
			t.Fatalf("unexpected snapshot %+v err=%v", snap, err)
		}
	})

	t.Run("GIVEN the Balance endpoint errors WHEN a snapshot is requested THEN no snapshot is returned", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/private/Balance", `{"error":["EAPI:Invalid key"]}`)
		engine := newTestEngine(f, nil, nil)

		snap, err := engine.GetAccountSnapshot(ctx, "EUR")

		if snap != nil || err == nil {
			t.Fatalf("expected nil snapshot and error, got %+v err=%v", snap, err)
		}
	})

	t.Run("GIVEN no API credentials WHEN a snapshot is requested THEN nothing is called and ErrMissingCredentials is returned", func(t *testing.T) {
		f := newFakeKraken(t)
		engine := NewExecutionEngine(ExecutionOptions{RESTURL: f.server.URL, HTTPClient: f.server.Client()})

		snap, err := engine.GetAccountSnapshot(ctx, "EUR")

		if snap != nil || !errors.Is(err, ErrMissingCredentials) || f.totalRequests() != 0 {
			t.Fatalf("expected missing-credentials error, got %+v err=%v calls=%d", snap, err, f.totalRequests())
		}
	})
}

// TestExecutionEngineReconcile covers pending-order reconciliation.
func TestExecutionEngineReconcile(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN a pending live order WHEN Kraken reports it closed THEN it is marked filled with the fill price and fee", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/private/QueryOrders", `{"error":[],"result":{"TX999":{"status":"closed","price":"51234.50","fee":"1.34"}}}`)
		repo := &fakeRepo{pending: []PendingLiveOrder{{ID: "order-abc", ExchangeOrderID: "TX999", Price: 51_000}}}
		engine := newTestEngine(f, repo, nil)

		updated := engine.ReconcilePendingOrders(ctx)

		if updated != 1 || len(repo.updates) != 1 {
			t.Fatalf("expected one update, got %d %+v", updated, repo.updates)
		}
		u := repo.updates[0]
		if u.OrderID != "order-abc" || u.Status != "filled" || u.FillPrice != 51234.50 || u.Fee != 1.34 {
			t.Fatalf("unexpected update %+v", u)
		}
		if calls := f.calls("/0/private/QueryOrders"); len(calls) != 1 || !strings.Contains(calls[0].Body["txid"].(string), "TX999") {
			t.Fatalf("expected QueryOrders for TX999, got %+v", calls)
		}
	})

	t.Run("GIVEN pending orders WHEN Kraken reports canceled, expired and open THEN only terminal states are updated", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/private/QueryOrders", `{"error":[],"result":{
			"TX888":{"status":"canceled","price":"0","fee":"0"},
			"TX777":{"status":"expired"},
			"TX666":{"status":"open"}}}`)
		repo := &fakeRepo{pending: []PendingLiveOrder{
			{ID: "order-xyz", ExchangeOrderID: "TX888", Price: 50_000},
			{ID: "order-exp", ExchangeOrderID: "TX777", Price: 10},
			{ID: "order-open", ExchangeOrderID: "TX666", Price: 20},
			{ID: "order-missing", ExchangeOrderID: "TX555", Price: 30},
		}}
		engine := newTestEngine(f, repo, nil)

		updated := engine.ReconcilePendingOrders(ctx)

		if updated != 2 {
			t.Fatalf("expected 2 updates, got %d (%+v)", updated, repo.updates)
		}
		statuses := map[string]orderUpdate{}
		for _, u := range repo.updates {
			statuses[u.OrderID] = u
		}
		if statuses["order-xyz"].Status != "canceled" || statuses["order-xyz"].FillPrice != 50_000 {
			t.Fatalf("expected canceled at original price, got %+v", statuses["order-xyz"])
		}
		if statuses["order-exp"].Status != "expired" {
			t.Fatalf("expected expired, got %+v", statuses["order-exp"])
		}
	})

	t.Run("GIVEN no pending orders WHEN reconciliation runs THEN no Kraken call is made", func(t *testing.T) {
		f := newFakeKraken(t)
		engine := newTestEngine(f, &fakeRepo{}, nil)

		if updated := engine.ReconcilePendingOrders(ctx); updated != 0 || f.totalRequests() != 0 {
			t.Fatalf("expected no work, got updated=%d calls=%d", updated, f.totalRequests())
		}
	})

	t.Run("GIVEN no API credentials WHEN reconciliation runs THEN zero is returned safely", func(t *testing.T) {
		f := newFakeKraken(t)
		repo := &fakeRepo{pending: []PendingLiveOrder{{ID: "x", ExchangeOrderID: "TX1", Price: 100}}}
		engine := NewExecutionEngine(ExecutionOptions{RESTURL: f.server.URL, HTTPClient: f.server.Client(), Repository: repo})

		if updated := engine.ReconcilePendingOrders(ctx); updated != 0 || f.totalRequests() != 0 {
			t.Fatalf("expected no work, got updated=%d calls=%d", updated, f.totalRequests())
		}
	})

	t.Run("GIVEN no repository WHEN reconciliation runs THEN zero is returned safely", func(t *testing.T) {
		f := newFakeKraken(t)
		engine := newTestEngine(f, nil, nil)

		if updated := engine.ReconcilePendingOrders(ctx); updated != 0 || f.totalRequests() != 0 {
			t.Fatalf("expected no work, got updated=%d", updated)
		}
	})
}

// TestExecutionEngineCancelOrder covers order cancellation.
func TestExecutionEngineCancelOrder(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN an open order WHEN it is cancelled THEN CancelOrder is sent for its txid", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/private/CancelOrder", `{"error":[],"result":{"count":1}}`)
		engine := newTestEngine(f, nil, nil)

		err := engine.CancelOrder(ctx, "TX321")

		calls := f.calls("/0/private/CancelOrder")
		if err != nil || len(calls) != 1 || calls[0].Body["txid"] != "TX321" {
			t.Fatalf("expected CancelOrder for TX321, got err=%v calls=%+v", err, calls)
		}
	})

	t.Run("GIVEN no API credentials WHEN cancelling THEN ErrMissingCredentials is returned without a call", func(t *testing.T) {
		f := newFakeKraken(t)
		engine := NewExecutionEngine(ExecutionOptions{RESTURL: f.server.URL, HTTPClient: f.server.Client()})

		if err := engine.CancelOrder(ctx, "TX321"); !errors.Is(err, ErrMissingCredentials) || f.totalRequests() != 0 {
			t.Fatalf("expected missing-credentials error, got %v", err)
		}
	})
}
