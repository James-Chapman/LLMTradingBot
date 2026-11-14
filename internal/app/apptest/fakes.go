// Package apptest provides network-free fakes for the app's external
// dependencies (Kraken market data and execution, LLM, universe, news and
// strategies) so app and server tests never touch the real network.
package apptest

import (
	"context"
	"sync"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/kraken"
	"github.com/James-Chapman/LLMTradingBot/internal/llm"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy"
)

// Market is a fake Kraken market-data source with settable prices and candles.
type Market struct {
	mu      sync.Mutex
	Prices  map[string]float64
	Candles map[int][]domain.Candle // interval → candles for every symbol
	Started []string
	Stopped bool
}

// NewMarket returns a fake market quoting the given prices.
func NewMarket(prices map[string]float64) *Market {
	return &Market{Prices: prices, Candles: map[int][]domain.Candle{}}
}

// SetPrice changes the quoted price for a symbol.
func (m *Market) SetPrice(symbol string, price float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Prices[symbol] = price
}

// RemovePrice stops quoting symbol, as if Kraken's ticker had no answer for it.
func (m *Market) RemovePrice(symbol string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.Prices, symbol)
}

// ValidateSymbols returns the symbols the fake quotes a price for.
func (m *Market) ValidateSymbols(_ context.Context, symbols []string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []string{}
	for _, s := range symbols {
		if _, ok := m.Prices[s]; ok {
			out = append(out, s)
		}
	}
	return out
}

// GetTickersBatch returns snapshots for the requested symbols that have prices.
func (m *Market) GetTickersBatch(_ context.Context, symbols []string) (map[string]domain.MarketSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]domain.MarketSnapshot{}
	for _, s := range symbols {
		if p, ok := m.Prices[s]; ok {
			out[s] = domain.MarketSnapshot{Symbol: s, Price: p, Timestamp: time.Now().UTC()}
		}
	}
	return out, nil
}

// GetOHLC returns the configured candles for the interval.
func (m *Market) GetOHLC(_ context.Context, _ string, interval, _ int) ([]domain.Candle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]domain.Candle(nil), m.Candles[interval]...), nil
}

// Start records the subscribed symbols; the fake never streams.
func (m *Market) Start(symbols []string, _ func(domain.MarketSnapshot)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Started = append([]string{}, symbols...)
}

// Stop records that the subscription was stopped.
func (m *Market) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Stopped = true
}

// Live is a fake Kraken execution engine that records submitted orders and
// replays queued reconciliation updates.
type Live struct {
	mu         sync.Mutex
	Executed   []domain.ExecutionIntent
	Orders     []domain.OrderRecord // every order Execute returned, oldest first
	Status     string               // order status returned by Execute (default "pending")
	Account    *kraken.AccountSnapshot
	Reconciles int
	// Tracked is the tracked-order list passed to each ReconcilePendingOrders call.
	Tracked [][]kraken.PendingLiveOrder
	// MinOrder is the minimum order volume per market (absent: unknown).
	MinOrder map[string]float64
	// OnAccepted, when set, runs with every accepted order before Execute
	// returns - e.g. to save it like the real engine, or to panic and so
	// simulate the process stopping right after Kraken accepted the order.
	OnAccepted func(order domain.OrderRecord)
	updates    []kraken.OrderUpdate
}

// Execute records the intent and returns an order with the configured status.
// Like the real engine it uses opts.OrderID as the order ID when given, and
// accepted orders carry and return opts.PositionID.
func (l *Live) Execute(_ context.Context, intent domain.ExecutionIntent, opts kraken.ExecuteOptions) (domain.OrderRecord, string) {
	l.mu.Lock()
	l.Executed = append(l.Executed, intent)
	status := l.Status
	if status == "" {
		status = "pending"
	}
	id := opts.OrderID
	if id == "" {
		id = domain.NewID()
	}
	order := domain.OrderRecord{
		ID: id, Market: intent.Market, Direction: intent.Direction, Size: intent.Size,
		Price: opts.MarketPrice, Status: status, Timestamp: time.Now().UTC(), ExchangeOrderID: "FAKE-TXID",
	}
	positionID := ""
	if status != "rejected" {
		order.PositionID = opts.PositionID
		positionID = opts.PositionID
	}
	l.Orders = append(l.Orders, order)
	hook := l.OnAccepted
	l.mu.Unlock()
	if hook != nil && status != "rejected" {
		hook(order)
	}
	return order, positionID
}

// MinOrderSize returns the configured minimum order volume for market.
func (l *Live) MinOrderSize(_ context.Context, market string) (float64, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	v, ok := l.MinOrder[market]
	return v, ok
}

// SetStatus changes the order status Execute returns ("rejected" refuses orders).
func (l *Live) SetStatus(status string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Status = status
}

// GetAccountSnapshot returns the configured account, or an error-free nil.
func (l *Live) GetAccountSnapshot(_ context.Context, _ string) (*kraken.AccountSnapshot, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.Account, nil
}

// ReconcilePendingOrders counts the call, records the tracked orders and
// returns (then forgets) the queued updates.
func (l *Live) ReconcilePendingOrders(_ context.Context, tracked []kraken.PendingLiveOrder) []kraken.OrderUpdate {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Reconciles++
	l.Tracked = append(l.Tracked, append([]kraken.PendingLiveOrder(nil), tracked...))
	out := l.updates
	l.updates = nil
	return out
}

// LastTracked returns the tracked orders passed to the latest reconciliation.
func (l *Live) LastTracked() []kraken.PendingLiveOrder {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.Tracked) == 0 {
		return nil
	}
	return l.Tracked[len(l.Tracked)-1]
}

// ReconcileCount returns how many times reconciliation ran.
func (l *Live) ReconcileCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.Reconciles
}

// QueueUpdate makes the next ReconcilePendingOrders report update.
func (l *Live) QueueUpdate(update kraken.OrderUpdate) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.updates = append(l.updates, update)
}

// FillLastOrder queues a full fill of the most recent order at price with fee
// and returns that order.
func (l *Live) FillLastOrder(price, fee float64) domain.OrderRecord {
	order := l.LastOrder()
	l.QueueUpdate(kraken.OrderUpdate{
		OrderID: order.ID, ExchangeOrderID: order.ExchangeOrderID, PositionID: order.PositionID, Direction: order.Direction,
		Status: "filled", KrakenStatus: "closed", ExecutedSize: order.Size, AvgPrice: price, Fee: fee,
	})
	return order
}

// SellCount returns how many live sells (shorts) were submitted.
func (l *Live) SellCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, intent := range l.Executed {
		if intent.Direction == domain.Short {
			n++
		}
	}
	return n
}

// LastOrder returns the most recent order Execute returned (zero if none).
func (l *Live) LastOrder() domain.OrderRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.Orders) == 0 {
		return domain.OrderRecord{}
	}
	return l.Orders[len(l.Orders)-1]
}

// ExecutedCount returns how many live orders were submitted.
func (l *Live) ExecutedCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.Executed)
}

// LLM is a fake LLM client that is never available.
type LLM struct{ Rechecks int }

// Available reports false.
func (f *LLM) Available() bool { return false }

// CanAttempt reports false.
func (f *LLM) CanAttempt() bool { return false }

// Probe reports false.
func (f *LLM) Probe(context.Context) bool { return false }

// Chat always fails with llm.ErrUnavailable.
func (f *LLM) Chat(context.Context, []llm.Message, bool) (llm.Reply, error) {
	return llm.Reply{}, llm.ErrUnavailable
}

// ModelName returns a fixed display name.
func (f *LLM) ModelName() string { return "fake-model (Fake)" }

// ActiveBackend reports no backend.
func (f *LLM) ActiveBackend() string { return llm.BackendNone }

// RecheckPrimary counts recheck calls.
func (f *LLM) RecheckPrimary(context.Context) { f.Rechecks++ }

// Universe is a fake universe resolver returning fixed markets only.
type Universe struct{ Markets []string }

// ResolveUniverse returns the configured markets as fixed markets.
func (u Universe) ResolveUniverse(context.Context) domain.UniverseSnapshot {
	return domain.UniverseSnapshot{FixedMarkets: u.Markets, DynamicMarkets: []string{}, ResolverSource: "fake"}
}

// News is a fake news adapter returning fixed items.
type News struct{ Items []domain.NewsItem }

// SourceName returns "Fake".
func (n News) SourceName() string { return "Fake" }

// FetchNews returns the configured items.
func (n News) FetchNews(context.Context) ([]domain.NewsItem, error) {
	return append([]domain.NewsItem(nil), n.Items...), nil
}

// Strategy is a configurable fake strategy.
type Strategy struct {
	StrategyID string
	LLM        bool
	Ticks      int
	// Ideas builds the ideas for one evaluation; nil yields none.
	Ideas func(in strategy.Input) []domain.TradeIdea

	mu    sync.Mutex
	calls int
}

// ID returns the configured strategy ID.
func (s *Strategy) ID() string { return s.StrategyID }

// UsesLLM returns the configured LLM flag.
func (s *Strategy) UsesLLM() bool { return s.LLM }

// RequiredPriceTicks returns the configured tick requirement.
func (s *Strategy) RequiredPriceTicks() int { return s.Ticks }

// Evaluate counts the call and returns the configured ideas.
func (s *Strategy) Evaluate(_ context.Context, in strategy.Input) []domain.TradeIdea {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	if s.Ideas == nil {
		return nil
	}
	return s.Ideas(in)
}

// Calls returns how many times Evaluate ran.
func (s *Strategy) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// Idea returns a fresh trade idea for market and direction.
func Idea(strategyID, market string, direction domain.Direction, confidence float64) domain.TradeIdea {
	return domain.TradeIdea{
		ID:                     domain.NewID(),
		StrategyID:             strategyID,
		Market:                 market,
		Direction:              direction,
		Thesis:                 "fake thesis",
		SupportingSignals:      map[string]any{},
		Confidence:             confidence,
		EntryPlan:              "enter",
		ExitPlan:               "exit",
		StopOrInvalidation:     "stop",
		PositionSizingProposal: 0.2,
		ModeEligibility:        []domain.TradingMode{domain.ModeManual, domain.ModeSemiAutomated, domain.ModeFullyAutomated},
	}
}
