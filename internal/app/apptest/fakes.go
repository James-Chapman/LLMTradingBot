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

// Live is a fake Kraken execution engine that records submitted orders.
type Live struct {
	mu         sync.Mutex
	Executed   []domain.ExecutionIntent
	Status     string // order status returned by Execute (default "pending")
	Account    *kraken.AccountSnapshot
	Reconciles int
}

// Execute records the intent and returns an order with the configured status.
func (l *Live) Execute(_ context.Context, intent domain.ExecutionIntent, opts kraken.ExecuteOptions) (domain.OrderRecord, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Executed = append(l.Executed, intent)
	status := l.Status
	if status == "" {
		status = "pending"
	}
	return domain.OrderRecord{
		ID: domain.NewID(), Market: intent.Market, Direction: intent.Direction, Size: intent.Size,
		Price: opts.MarketPrice, Status: status, Timestamp: time.Now().UTC(), ExchangeOrderID: "FAKE-TXID",
	}, ""
}

// GetAccountSnapshot returns the configured account, or an error-free nil.
func (l *Live) GetAccountSnapshot(_ context.Context, _ string) (*kraken.AccountSnapshot, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.Account, nil
}

// ReconcilePendingOrders counts reconciliation calls.
func (l *Live) ReconcilePendingOrders(_ context.Context) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Reconciles++
	return 0
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
