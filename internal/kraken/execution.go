package kraken

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
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

// PendingLiveOrder is a live order awaiting reconciliation with Kraken.
type PendingLiveOrder struct {
	ID              string
	ExchangeOrderID string
	Price           float64
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
	SaveOrder(order domain.OrderRecord, approvalID string, fee float64, environment, tradeIdeaID string) error
	// SaveRejectedTrade stores an execution-rejected order in the rejected-trades register.
	SaveRejectedTrade(trade RejectedTrade) error
	// GetPendingLiveOrders returns pending live orders that have a Kraken txid.
	GetPendingLiveOrders() ([]PendingLiveOrder, error)
	// UpdateOrderStatusAndFill sets an order's status, fill price and fee.
	UpdateOrderStatusAndFill(orderID, status string, fillPrice, fee float64) error
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
}

// ExecuteOptions carries per-order context for Execute.
type ExecuteOptions struct {
	MarketPrice      float64
	StrategyID       string
	SignalConfidence *float64
	Environment      string // "live" (default); any other value is refused
	TradeIdeaID      string
}

// ExecutionEngine submits live spot orders to Kraken via the official SDK.
// Live orders return an empty position ID: exchange balances are the source of
// truth and fills may settle after the order is accepted.
type ExecutionEngine struct {
	rest           *spot.REST
	httpClient     *http.Client
	repo           OrderRepository
	backoff        BackoffOptions
	hasCredentials bool
	log            *slog.Logger

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
	return &ExecutionEngine{
		rest:           newREST(restURL, opts.APIKey, opts.APISecret),
		httpClient:     defaultHTTPClient(opts.HTTPClient),
		repo:           opts.Repository,
		backoff:        opts.Backoff,
		hasCredentials: opts.APIKey != "" && opts.APISecret != "",
		log:            observability.Logger("kraken_execution"),
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

// backoffFor returns the engine's backoff options labelled for one operation.
func (e *ExecutionEngine) backoffFor(operation string) BackoffOptions {
	opts := e.backoff
	opts.Logger = e.log
	opts.Operation = operation
	return opts
}

// Execute submits a live market order (or limit order when intent.Price is set)
// and returns the order record and an always-empty position ID. Refused or
// failed orders come back with Status "rejected" and the reason in ExchangeOrderID.
func (e *ExecutionEngine) Execute(ctx context.Context, intent domain.ExecutionIntent, opts ExecuteOptions) (domain.OrderRecord, string) {
	price := opts.MarketPrice
	if intent.Price != nil {
		price = *intent.Price
	}
	order := domain.OrderRecord{
		ID:        domain.NewID(),
		Market:    intent.Market,
		Direction: intent.Direction,
		Size:      intent.Size,
		Price:     price,
		Status:    "pending",
		Timestamp: domain.NowUTC(),
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

	request := e.buildAddOrder(ctx, intent)
	resp, err := CallWithBackoff(ctx, e.backoffFor("Kraken AddOrder"),
		func(ctx context.Context) (*spot.Response[spot.AddOrderResult], error) {
			return e.restFor(ctx).AddOrder(request)
		})
	if err != nil {
		e.log.Error("Kraken live order failed", "market", intent.Market, "error", err.Error())
		return e.reject(order, opts, err.Error()), ""
	}

	if len(resp.Result.ID) > 0 {
		order.ExchangeOrderID = resp.Result.ID[0]
	}
	if e.repo != nil {
		if err := e.repo.SaveOrder(order, intent.ApprovalRequestID, 0, environment, opts.TradeIdeaID); err != nil {
			e.log.Error("Failed to save live order", "txid", order.ExchangeOrderID, "error", err.Error())
		}
	}
	e.log.Info("Kraken live order submitted",
		"market", intent.Market,
		"direction", string(intent.Direction),
		"txid", order.ExchangeOrderID,
	)
	return order, ""
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
func (e *ExecutionEngine) buildAddOrder(ctx context.Context, intent domain.ExecutionIntent) *spot.AddOrderRequest {
	request := &spot.AddOrderRequest{
		OrderType: "market",
		Type:      "sell",
		Pair:      ToKrakenPair(intent.Market),
		Volume:    fmt.Sprintf("%.8f", intent.Size),
	}
	if intent.Direction == domain.Long {
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
	if volume, err := normalizer.FormatSize(pairName, decimal.NewFromFloat64(intent.Size)); err == nil {
		request.Volume = volume.String()
	}
	if intent.Price != nil {
		limit := decimal.NewFromFloat64(*intent.Price)
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

// ReconcilePendingOrders queries Kraken for pending live orders and updates
// their status in the repository. It returns how many orders changed.
func (e *ExecutionEngine) ReconcilePendingOrders(ctx context.Context) int {
	if !e.hasCredentials || e.repo == nil {
		return 0
	}
	pending, err := e.repo.GetPendingLiveOrders()
	if err != nil {
		e.log.Warn("Failed to load pending live orders", "error", err.Error())
		return 0
	}

	updated := 0
	for start := 0; start < len(pending); start += queryOrdersBatchSize {
		batch := pending[start:min(start+queryOrdersBatchSize, len(pending))]
		updated += e.reconcileBatch(ctx, batch)
	}
	return updated
}

// reconcileBatch reconciles up to queryOrdersBatchSize orders with one QueryOrders call.
func (e *ExecutionEngine) reconcileBatch(ctx context.Context, pending []PendingLiveOrder) int {
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
		return 0
	}

	updated := 0
	for _, order := range pending {
		info, ok := resp.Result[order.ExchangeOrderID]
		if !ok || info.Order == nil {
			continue
		}
		txid := order.ExchangeOrderID
		switch info.Status {
		case "closed":
			fillPrice := order.Price
			if p, ok := decimalFloat(info.Price); ok && p > 0 {
				fillPrice = p
			}
			fee, _ := decimalFloat(info.Fee)
			if e.updateOrder(order.ID, "filled", fillPrice, fee) {
				e.log.Info("Reconciled live order filled", "txid", txid, "fill_price", fillPrice)
				updated++
			}
		case "canceled", "expired":
			if e.updateOrder(order.ID, info.Status, order.Price, 0) {
				e.log.Info("Reconciled live order", "txid", txid, "status", info.Status)
				updated++
			}
		}
	}
	return updated
}

// updateOrder writes a reconciled status, returning false (and logging) on failure.
func (e *ExecutionEngine) updateOrder(orderID, status string, fillPrice, fee float64) bool {
	if err := e.repo.UpdateOrderStatusAndFill(orderID, status, fillPrice, fee); err != nil {
		e.log.Error("Failed to update reconciled order", "order_id", orderID, "error", err.Error())
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
