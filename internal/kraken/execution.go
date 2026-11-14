package kraken

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/krakenfx/api-go/v2/pkg/decimal"
	"github.com/krakenfx/api-go/v2/pkg/spot"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/observability"
)

// Rejection reasons recorded in OrderRecord.ExchangeOrderID for locally refused orders.
const (
	ReasonMissingCredentials = "missing_api_credentials"
	ReasonPaperModeReadOnly  = "paper_mode_read_only"
)

// ErrMissingCredentials is returned when a private Kraken call is attempted without API keys.
var ErrMissingCredentials = errors.New("kraken: missing API credentials")

// queryOrdersBatchSize is Kraken's maximum number of txids per QueryOrders call.
const queryOrdersBatchSize = 50

// ReasonShuttingDown is the rejection reason for an order requested after the caller's context ended.
const ReasonShuttingDown = "cancelled_before_submission"

// addOrderTimeout bounds one AddOrder (including retries) once it is detached from the caller.
const addOrderTimeout = 30 * time.Second

// clientOrderGrace is how long an order without a txid may stay unseen by
// Kraken before reconciliation concludes it was never placed.
const clientOrderGrace = 5 * time.Minute

// unknownOutcomeMarkers are lower-case Kraken errors after which an order may
// or may not have reached the matching engine.
var unknownOutcomeMarkers = []string{"eservice:unavailable", "eservice:busy", "egeneral:internal error"}

// PendingLiveOrder is a live order awaiting reconciliation with Kraken.
type PendingLiveOrder struct {
	ID              string
	ExchangeOrderID string // Kraken txid; empty when the AddOrder reply was lost
	ClientOrderID   string // cl_ord_id sent with the order; empty for orders placed before 0.9.3
	PositionID      string // live position the order belongs to, if any
	Direction       domain.Direction
	Size            float64 // the volume sent to Kraken
	Price           float64
	Timestamp       time.Time // when the order was requested
}

// OrderMeta are the order_records fields saved alongside an order.
type OrderMeta struct {
	ApprovalID    string
	Fee           float64
	Environment   string
	TradeIdeaID   string
	ClientOrderID string
}

// OrderUpdate is what reconciliation learned about one live order.
// Status is "filled" whenever any volume executed (even if Kraken then
// cancelled or expired the rest), otherwise "canceled", "expired",
// "not_found" (never seen by Kraken) or "pending" (txid learned, still open).
type OrderUpdate struct {
	OrderID         string
	ExchangeOrderID string
	PositionID      string           // live position the order belongs to, when known
	Direction       domain.Direction // buy (long) or sell (short), when known
	Status          string
	KrakenStatus    string  // Kraken's own status, e.g. "canceled" for a partial fill
	ExecutedSize    float64 // base volume executed; 0 when nothing filled
	AvgPrice        float64 // average execution price (the order's price when nothing filled)
	Fee             float64
}

// RejectedTrade is an order refused at execution time (by Kraken or locally).
type RejectedTrade struct {
	Market      string
	Direction   domain.Direction
	Size        float64
	Price       float64
	Confidence  *float64
	Reason      string
	TradeIdeaID string
	Timestamp   time.Time
}

// OrderRepository is the persistence the execution engine needs.
type OrderRepository interface {
	// SaveOrder stores an accepted order in the trade ledger.
	SaveOrder(order domain.OrderRecord, meta OrderMeta) error
	// SaveRejectedTrade stores an execution-rejected order in the rejected-trades register.
	SaveRejectedTrade(trade RejectedTrade) error
	// GetPendingLiveOrders returns pending live orders that have a Kraken txid or a client order id.
	GetPendingLiveOrders() ([]PendingLiveOrder, error)
	// UpdateOrderExecution records a reconciled status, executed size, average price, fee and txid.
	UpdateOrderExecution(update OrderUpdate) error
}

// AccountSnapshot is the live Kraken account valuation shown on the dashboard.
type AccountSnapshot struct {
	Cash           float64 `json:"cash"`
	Equity         float64 `json:"equity"`
	PositionsValue float64 `json:"positions_value"`
}

// ExecutionOptions configures an ExecutionEngine; zero values select defaults.
type ExecutionOptions struct {
	APIKey     string
	APISecret  string
	RESTURL    string          // default DefaultRESTURL
	HTTPClient *http.Client    // default client with a 15 s timeout
	Repository OrderRepository // optional
	Backoff    BackoffOptions  // retry settings (Sleep/Jitter injectable)
	// Normalizer is an optional pre-loaded SDK normalizer; when nil one is loaded
	// lazily from Kraken's public Assets/AssetPairs endpoints on first order.
	Normalizer *spot.Normalizer
	// Now is the clock used for the client-order-id grace period (default UTC now).
	Now func() time.Time
}

// ExecuteOptions carries per-order context for Execute.
type ExecuteOptions struct {
	MarketPrice      float64
	StrategyID       string
	SignalConfidence *float64
	Environment      string // "live" (default); any other value is refused
	TradeIdeaID      string
	// PositionID links the order to a position in the caller's live book; it
	// is stamped on the order record and returned when the order is accepted.
	PositionID string
	// OrderID is the order's ID and Kraken client order id (cl_ord_id). The
	// caller allocates it so it can record the order before it is sent; empty
	// lets the engine allocate one.
	OrderID string
}

// ExecutionEngine submits live spot orders to Kraken via the official SDK.
// Every order carries its own ID as Kraken's client order id (cl_ord_id), so
// an order whose reply was lost can still be found and reconciled.
type ExecutionEngine struct {
	rest           *spot.REST
	httpClient     *http.Client
	repo           OrderRepository
	backoff        BackoffOptions
	hasCredentials bool
	log            *slog.Logger
	now            func() time.Time

	normMu     sync.Mutex
	normalizer *spot.Normalizer
}

// NewExecutionEngine builds a live execution engine. Without both API key and
// secret every private operation is refused locally.
func NewExecutionEngine(opts ExecutionOptions) *ExecutionEngine {
	restURL := opts.RESTURL
	if restURL == "" {
		restURL = DefaultRESTURL
	}
	now := opts.Now
	if now == nil {
		now = domain.NowUTC
	}
	return &ExecutionEngine{
		rest:           newREST(restURL, opts.APIKey, opts.APISecret),
		httpClient:     defaultHTTPClient(opts.HTTPClient),
		repo:           opts.Repository,
		backoff:        opts.Backoff,
		hasCredentials: opts.APIKey != "" && opts.APISecret != "",
		log:            observability.Logger("kraken_execution"),
		now:            now,
		normalizer:     opts.Normalizer,
	}
}

// HasCredentials reports whether API key and secret were both supplied.
func (e *ExecutionEngine) HasCredentials() bool {
	return e.hasCredentials
}

// restFor returns an SDK client bound to ctx.
func (e *ExecutionEngine) restFor(ctx context.Context) *spot.REST {
	return restWithContext(ctx, e.rest, e.httpClient)
}

// sendingREST returns an SDK client bound to ctx that sets *sent once it
// starts an HTTP request, so a failure before that point (building or
// signing the request) is known to have sent nothing.
func (e *ExecutionEngine) sendingREST(ctx context.Context, sent *bool) *spot.REST {
	bound := restWithContext(ctx, e.rest, e.httpClient)
	execute := bound.Executor
	bound.Executor = func(req *http.Request) (*http.Response, error) {
		*sent = true
		return execute(req)
	}
	return bound
}

// backoffFor returns the engine's backoff options labelled for one operation.
func (e *ExecutionEngine) backoffFor(operation string) BackoffOptions {
	opts := e.backoff
	opts.Logger = e.log
	opts.Operation = operation
	return opts
}

// Execute submits a live market order (or limit order when intent.Price is set)
// and returns the order record and, when Kraken accepted it, opts.PositionID.
// Refused or failed orders come back with Status "rejected" and the reason in
// ExchangeOrderID. An order whose outcome is unknown (timeout, unreadable
// reply, Kraken "service unavailable") comes back "pending" without a txid
// and is resolved later by ReconcilePendingOrders via its client order id.
func (e *ExecutionEngine) Execute(ctx context.Context, intent domain.ExecutionIntent, opts ExecuteOptions) (domain.OrderRecord, string) {
	price := opts.MarketPrice
	if intent.Price != nil {
		price = *intent.Price
	}
	orderID := opts.OrderID
	if orderID == "" {
		orderID = domain.NewID()
	}
	order := domain.OrderRecord{
		ID:         orderID,
		Market:     intent.Market,
		Direction:  intent.Direction,
		Size:       intent.Size,
		Price:      price,
		Status:     "pending",
		Timestamp:  domain.NowUTC(),
		PositionID: opts.PositionID,
	}

	environment := opts.Environment
	if environment == "" {
		environment = string(domain.EnvLive)
	}
	// Safety: the live engine never sends orders for paper trading.
	if environment != string(domain.EnvLive) {
		return e.reject(order, opts, ReasonPaperModeReadOnly), ""
	}
	if !e.hasCredentials {
		return e.reject(order, opts, ReasonMissingCredentials), ""
	}
	if ctx.Err() != nil {
		return e.reject(order, opts, ReasonShuttingDown), ""
	}

	// Once sending starts the request is detached from the caller: a Ctrl+C
	// must not cancel an order Kraken may already have accepted, which would
	// otherwise be recorded as rejected while it fills.
	placeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), addOrderTimeout)
	defer cancel()
	request := e.buildAddOrder(placeCtx, intent)
	request.ClOrdId = order.ID // a retry re-sends the same id, never a second order
	// The order records the volume actually sent (rounded to the pair's lot
	// size), which reconciliation falls back on when Kraken omits vol_exec.
	if sent, err := strconv.ParseFloat(request.Volume, 64); err == nil && sent > 0 {
		order.Size = sent
	}
	var sent bool // whether the last attempt's HTTP request was started
	resp, err := CallWithBackoff(placeCtx, e.addOrderBackoff(),
		func(ctx context.Context) (*spot.Response[spot.AddOrderResult], error) {
			sent = false
			return e.sendingREST(ctx, &sent).AddOrder(request)
		})
	switch {
	case err == nil:
		if len(resp.Result.ID) > 0 {
			order.ExchangeOrderID = resp.Result.ID[0]
		}
	case !sent:
		// The SDK failed while building or signing the request: nothing was sent.
		e.log.Error("Kraken live order not sent", "market", intent.Market, "cl_ord_id", order.ID, "error", err.Error())
		return e.reject(order, opts, "order not sent: "+err.Error()), ""
	case orderOutcomeUnknown(resp, err):
		e.log.Error("Kraken order outcome unknown - kept pending and will be reconciled by client order id",
			"market", intent.Market, "cl_ord_id", order.ID, "error", err.Error())
	default:
		e.log.Error("Kraken live order failed", "market", intent.Market, "error", err.Error())
		return e.reject(order, opts, err.Error()), ""
	}

	if e.repo != nil {
		meta := OrderMeta{
			ApprovalID: intent.ApprovalRequestID, Environment: environment,
			TradeIdeaID: opts.TradeIdeaID, ClientOrderID: order.ID,
		}
		if err := e.repo.SaveOrder(order, meta); err != nil {
			e.log.Error("Failed to save live order", "txid", order.ExchangeOrderID, "error", err.Error())
		}
	}
	e.log.Info("Kraken live order submitted",
		"market", intent.Market,
		"direction", string(intent.Direction),
		"txid", order.ExchangeOrderID,
		"cl_ord_id", order.ID,
	)
	return order, order.PositionID
}

// addOrderBackoff returns the AddOrder retry policy: only errors that mean the
// order was certainly not placed are retried.
func (e *ExecutionEngine) addOrderBackoff() BackoffOptions {
	opts := e.backoffFor("Kraken AddOrder")
	opts.Retryable = IsOrderNotPlacedError
	return opts
}

// orderOutcomeUnknown reports whether a failed AddOrder may still have placed
// the order: no readable Kraken reply at all, or a Kraken error that does not
// say the order was refused.
func orderOutcomeUnknown(resp *spot.Response[spot.AddOrderResult], err error) bool {
	if resp == nil || len(resp.Error) == 0 {
		return true // transport failure, timeout or an unreadable reply
	}
	return containsAnyFold(err.Error(), unknownOutcomeMarkers)
}

// reject marks order rejected with reason and records it in the rejected-trades register.
func (e *ExecutionEngine) reject(order domain.OrderRecord, opts ExecuteOptions, reason string) domain.OrderRecord {
	order.Status = "rejected"
	order.ExchangeOrderID = reason
	if e.repo != nil {
		err := e.repo.SaveRejectedTrade(RejectedTrade{
			Market:      order.Market,
			Direction:   order.Direction,
			Size:        order.Size,
			Price:       order.Price,
			Confidence:  opts.SignalConfidence,
			Reason:      reason,
			TradeIdeaID: opts.TradeIdeaID,
			Timestamp:   order.Timestamp,
		})
		if err != nil {
			e.log.Error("Failed to save rejected trade", "market", order.Market, "error", err.Error())
		}
	}
	return order
}

// buildAddOrder converts an intent into an SDK AddOrder request with formatted volume/price.
// A sell volume is rounded down, never to nearest, so an exit can never ask
// for more than the position holds.
func (e *ExecutionEngine) buildAddOrder(ctx context.Context, intent domain.ExecutionIntent) *spot.AddOrderRequest {
	sell := intent.Direction != domain.Long
	request := &spot.AddOrderRequest{
		OrderType: "market",
		Type:      "sell",
		Pair:      ToKrakenPair(intent.Market),
		Volume:    fmt.Sprintf("%.8f", intent.Size),
	}
	if sell {
		request.Volume = floorDecimal(intent.Size, 8)
	} else {
		request.Type = "buy"
	}
	if intent.Price != nil {
		request.OrderType = "limit"
		request.Price = fmt.Sprintf("%.8f", *intent.Price)
	}

	// Prefer the pair's own lot/tick precision when Kraken metadata is available.
	normalizer := e.ensureNormalizer(ctx)
	if normalizer == nil {
		return request
	}
	pairName, info, ok := lookupPair(normalizer, intent.Market)
	if !ok {
		return request
	}
	if sell {
		request.Volume = floorDecimal(intent.Size, info.LotDecimals)
	} else if volume, err := normalizer.FormatSize(pairName, exactDecimal(intent.Size)); err == nil {
		request.Volume = volume.String()
	}
	if intent.Price != nil {
		limit := exactDecimal(*intent.Price)
		if info.TickSize != nil {
			if formatted, err := normalizer.FormatPrice(pairName, limit); err == nil {
				request.Price = formatted.String()
			}
		} else {
			request.Price = limit.SetScale(int64(info.PairDecimals)).String()
		}
	}
	return request
}

// exactDecimal converts a float64 to an SDK decimal via its shortest exact
// decimal string. The SDK's NewFromFloat64 multiplies in binary and truncates,
// turning e.g. 0.29 into 0.28, so it is only used as a fallback.
func exactDecimal(value float64) *decimal.Decimal {
	d, err := decimal.NewFromString(strconv.FormatFloat(value, 'f', -1, 64))
	if err != nil {
		return decimal.NewFromFloat64(value)
	}
	return d
}

// floorDecimal formats a non-negative value with exactly decimals digits
// after the point, dropping (never rounding up) any further digits. It works
// on the shortest exact decimal form, so 0.29 stays 0.29.
func floorDecimal(value float64, decimals int) string {
	whole, frac, _ := strings.Cut(strconv.FormatFloat(value, 'f', -1, 64), ".")
	if decimals <= 0 {
		return whole
	}
	if len(frac) > decimals {
		frac = frac[:decimals]
	}
	return whole + "." + frac + strings.Repeat("0", decimals-len(frac))
}

// lookupPair finds the normalizer pair entry for an internal symbol, trying the
// internal name ("BTC/EUR") and Kraken's legacy name ("XBT/EUR").
func lookupPair(n *spot.Normalizer, symbol string) (string, *spot.AssetPair, bool) {
	candidates := []string{symbol}
	if base, quote, found := strings.Cut(symbol, "/"); found {
		if alias, ok := baseAliases[base]; ok {
			candidates = append(candidates, alias+"/"+quote)
		}
	}
	for _, name := range candidates {
		if info, err := n.PairInfo(name); err == nil {
			return name, info, true
		}
	}
	return "", nil, false
}

// ensureNormalizer returns the SDK normalizer, loading it on first use.
// A failed load returns nil (callers fall back to fixed precision) and is retried next time.
func (e *ExecutionEngine) ensureNormalizer(ctx context.Context) *spot.Normalizer {
	e.normMu.Lock()
	defer e.normMu.Unlock()
	if e.normalizer != nil {
		return e.normalizer
	}
	n := spot.NewNormalizer()
	if err := n.Use(e.restFor(ctx)); err != nil {
		e.log.Warn("Kraken asset metadata unavailable, using default precision", "error", err.Error())
		return nil
	}
	e.normalizer = n
	return n
}

// MinOrderSize returns Kraken's minimum order volume (ordermin) for market
// from the pair metadata; ok is false when it is unknown.
func (e *ExecutionEngine) MinOrderSize(ctx context.Context, market string) (float64, bool) {
	normalizer := e.ensureNormalizer(ctx)
	if normalizer == nil {
		return 0, false
	}
	_, info, ok := lookupPair(normalizer, market)
	if !ok {
		return 0, false
	}
	minimum, ok := decimalFloat(info.OrderMinimum)
	return minimum, ok && minimum > 0
}

// GetAccountSnapshot returns live cash (quote-currency balance) and equity
// (TradeBalance "eb") for quoteCurrency such as "EUR".
func (e *ExecutionEngine) GetAccountSnapshot(ctx context.Context, quoteCurrency string) (*AccountSnapshot, error) {
	if !e.hasCredentials {
		e.log.Warn("Kraken account snapshot unavailable: missing API credentials")
		return nil, ErrMissingCredentials
	}
	bareAsset := strings.ToUpper(quoteCurrency) // e.g. EUR
	cashAsset := "Z" + bareAsset                // Kraken legacy fiat key, e.g. ZEUR

	balances, err := e.restFor(ctx).Balances()
	if err != nil {
		e.log.Warn("Kraken Balance failed", "error", err.Error())
		return nil, fmt.Errorf("kraken balance: %w", err)
	}
	// Most accounts use Z-prefixed fiat keys (ZEUR); some return bare keys (EUR).
	cash, ok := decimalFloat(balances.Result[cashAsset])
	if !ok {
		cash, _ = decimalFloat(balances.Result[bareAsset])
	}

	equity := cash
	tradeBalance, err := spot.Call[map[string]*decimal.Decimal](e.restFor(ctx), spot.RequestOptions{
		Auth:   true,
		Method: "POST",
		Path:   "/0/private/TradeBalance",
		Body:   map[string]any{"asset": cashAsset},
	})
	if err != nil {
		e.log.Warn("Kraken TradeBalance failed", "error", err.Error())
	} else if eb, ok := decimalFloat(tradeBalance.Result["eb"]); ok {
		equity = eb
	}

	return &AccountSnapshot{
		Cash:           cash,
		Equity:         equity,
		PositionsValue: max(0, equity-cash),
	}, nil
}

// ReconcilePendingOrders queries Kraken for pending live orders, records
// what executed (size, average price, fee) in the repository and returns the
// updates for orders that finished. Orders with a txid are queried in
// batches; orders whose AddOrder reply was lost are looked up by client order
// id. tracked are the caller's own pending orders (its order ID is the client
// order id): any the ledger does not list as pending - its row could not be
// saved, or the process stopped before it was - is looked up by client order
// id too, so it is never left pending forever.
func (e *ExecutionEngine) ReconcilePendingOrders(ctx context.Context, tracked []PendingLiveOrder) []OrderUpdate {
	if !e.hasCredentials || e.repo == nil {
		return nil
	}
	pending, err := e.repo.GetPendingLiveOrders()
	if err != nil {
		e.log.Warn("Failed to load pending live orders - checking tracked orders only", "error", err.Error())
		pending = nil
	}

	listed := make(map[string]bool, len(pending))
	var withTxid, withoutTxid []PendingLiveOrder
	for _, order := range pending {
		listed[order.ID] = true
		switch {
		case order.ExchangeOrderID != "":
			withTxid = append(withTxid, order)
		case order.ClientOrderID != "":
			withoutTxid = append(withoutTxid, order)
		}
	}
	for _, order := range tracked {
		if listed[order.ID] || order.ID == "" {
			continue
		}
		if order.ClientOrderID == "" {
			order.ClientOrderID = order.ID
		}
		withoutTxid = append(withoutTxid, order)
	}

	var updated []OrderUpdate
	for start := 0; start < len(withTxid); start += queryOrdersBatchSize {
		batch := withTxid[start:min(start+queryOrdersBatchSize, len(withTxid))]
		updated = append(updated, e.reconcileBatch(ctx, batch)...)
	}
	for _, order := range withoutTxid {
		if update, ok := e.reconcileByClientID(ctx, order); ok {
			updated = append(updated, update)
		}
	}
	return updated
}

// reconcileBatch reconciles up to queryOrdersBatchSize orders with one QueryOrders call.
func (e *ExecutionEngine) reconcileBatch(ctx context.Context, pending []PendingLiveOrder) []OrderUpdate {
	txids := make([]string, len(pending))
	for i, order := range pending {
		txids[i] = order.ExchangeOrderID
	}
	resp, err := CallWithBackoff(ctx, e.backoffFor("Kraken QueryOrders"),
		func(ctx context.Context) (*spot.Response[map[string]spot.ClosedOrder], error) {
			return e.restFor(ctx).QueryOrders(&spot.QueryOrdersRequest{TxID: strings.Join(txids, ","), Trades: true})
		})
	if err != nil {
		e.log.Warn("Kraken QueryOrders failed", "error", err.Error())
		return nil
	}

	var updated []OrderUpdate
	for _, order := range pending {
		info, ok := resp.Result[order.ExchangeOrderID]
		if !ok || info.Order == nil {
			continue
		}
		update, finished := finishedOrderUpdate(order, order.ExchangeOrderID, info.Order)
		if finished && e.updateOrder(update) {
			updated = append(updated, update)
		}
	}
	return updated
}

// reconcileByClientID resolves an order that has no txid by its client order
// id: still open → remember its txid; closed → record the execution; unseen
// for longer than clientOrderGrace → mark it not_found. Kraken errors leave
// the order pending for the next run.
func (e *ExecutionEngine) reconcileByClientID(ctx context.Context, order PendingLiveOrder) (OrderUpdate, bool) {
	open, err := CallWithBackoff(ctx, e.backoffFor("Kraken OpenOrders"),
		func(ctx context.Context) (*spot.Response[spot.OpenOrdersResult], error) {
			return e.restFor(ctx).OpenOrders(&spot.OpenOrdersRequest{ClOrdID: order.ClientOrderID})
		})
	if err != nil {
		e.log.Warn("Kraken OpenOrders failed", "cl_ord_id", order.ClientOrderID, "error", err.Error())
		return OrderUpdate{}, false
	}
	// Kraken's filter is not trusted: an entry for another order is skipped.
	for txid, info := range open.Result.Open {
		if info.ClOrdID != order.ClientOrderID {
			continue
		}
		// Still working on the book: store the txid so later runs use QueryOrders.
		e.updateOrder(OrderUpdate{OrderID: order.ID, ExchangeOrderID: txid, Status: "pending", AvgPrice: order.Price})
		return OrderUpdate{}, false
	}

	closed, err := CallWithBackoff(ctx, e.backoffFor("Kraken ClosedOrders"),
		func(ctx context.Context) (*spot.Response[spot.ClosedOrdersResult], error) {
			return e.restFor(ctx).ClosedOrders(&spot.ClosedOrdersRequest{ClOrdID: order.ClientOrderID})
		})
	if err != nil {
		e.log.Warn("Kraken ClosedOrders failed", "cl_ord_id", order.ClientOrderID, "error", err.Error())
		return OrderUpdate{}, false
	}
	for txid, info := range closed.Result.Closed {
		if info.Order == nil || info.ClOrdID != order.ClientOrderID {
			continue
		}
		update, finished := finishedOrderUpdate(order, txid, info.Order)
		if finished && e.updateOrder(update) {
			return update, true
		}
		return OrderUpdate{}, false
	}

	if e.now().Sub(order.Timestamp) < clientOrderGrace {
		return OrderUpdate{}, false // Kraken may not list a brand-new order yet
	}
	update := OrderUpdate{OrderID: order.ID, PositionID: order.PositionID, Direction: order.Direction, Status: "not_found", AvgPrice: order.Price}
	e.log.Warn("Live order never reached Kraken - marked not_found", "order_id", order.ID, "cl_ord_id", order.ClientOrderID)
	if !e.updateOrder(update) {
		return OrderUpdate{}, false
	}
	return update, true
}

// finishedOrderUpdate converts Kraken's order info into an update once the
// order is closed, cancelled or expired. Any executed volume makes it a fill
// of that volume, at Kraken's average price and fee; a reply without vol_exec
// for a closed order is taken as fully executed: order.Size, the volume sent.
func finishedOrderUpdate(order PendingLiveOrder, txid string, info *spot.Order) (OrderUpdate, bool) {
	switch info.Status {
	case "closed", "canceled", "expired":
	default:
		return OrderUpdate{}, false
	}
	update := OrderUpdate{
		OrderID: order.ID, ExchangeOrderID: txid, PositionID: order.PositionID, Direction: order.Direction,
		KrakenStatus: info.Status, AvgPrice: order.Price,
	}
	executed, known := decimalFloat(info.VolumeExecuted)
	assumedFull := !known && info.Status == "closed"
	if assumedFull {
		executed = order.Size
	}
	if executed <= 0 && !assumedFull {
		update.Status = info.Status
		if info.Status == "closed" {
			update.Status = "canceled" // closed with nothing executed
		}
		return update, true
	}

	update.Status = "filled"
	update.ExecutedSize = executed
	if p, ok := decimalFloat(info.Price); ok && p > 0 {
		update.AvgPrice = p
	} else if cost, ok := decimalFloat(info.Cost); ok && cost > 0 {
		update.AvgPrice = cost / executed
	}
	update.Fee, _ = decimalFloat(info.Fee)
	return update, true
}

// updateOrder writes a reconciled status, returning false (and logging) on failure.
func (e *ExecutionEngine) updateOrder(update OrderUpdate) bool {
	if err := e.repo.UpdateOrderExecution(update); err != nil {
		e.log.Error("Failed to update reconciled order", "order_id", update.OrderID, "error", err.Error())
		return false
	}
	return true
}

// CancelOrder asks Kraken to cancel an open order by txid. The status change is
// picked up by the next ReconcilePendingOrders run.
func (e *ExecutionEngine) CancelOrder(ctx context.Context, txid string) error {
	if !e.hasCredentials {
		return ErrMissingCredentials
	}
	_, err := CallWithBackoff(ctx, e.backoffFor("Kraken CancelOrder"),
		func(ctx context.Context) (*spot.Response[spot.CancelResult], error) {
			return e.restFor(ctx).CancelOrder(&spot.CancelOrderRequest{TxID: txid})
		})
	if err != nil {
		e.log.Warn("Kraken CancelOrder failed", "txid", txid, "error", err.Error())
		return fmt.Errorf("kraken cancel order %s: %w", txid, err)
	}
	return nil
}
