// Package livebook tracks positions opened by real (live) Kraken orders,
// separately from the paper engine's positions. A position is recorded, with
// the ID of its order, before the entry order is sent (and marked
// exit-pending before a sell is sent), so a crash cannot leave a Kraken order
// the book does not know about and a second order can never be sent while the
// first is still unconfirmed.
package livebook

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

// Status is the lifecycle stage of a live position.
type Status string

const (
	// EntryPending: the buy was requested (recorded before it is sent) and its
	// execution is not yet known.
	EntryPending Status = "entry_pending"
	// Open: the buy executed (fully or partly); Size is what is held.
	Open Status = "open"
	// ExitPending: a sell for the held size was requested (recorded before it
	// is sent) and awaits its fill.
	ExitPending Status = "exit_pending"
)

// Position is one live long position (the spot account cannot short).
type Position struct {
	ID              string
	Market          string
	Status          Status
	RequestedSize   float64 // base volume sent with the entry order
	Size            float64 // base volume executed and held (0 until the entry fills)
	AvgPrice        float64 // average entry price (the reference price until filled)
	EntryFee        float64 // entry fee attributable to Size
	EntryOrderID    string
	ExitOrderID     string // the pending sell, while Status is ExitPending
	ExitReason      string // why the pending sell was sent (auto, stop_loss, ...)
	ExitTradeIdeaID string // signal that triggered the pending sell, if any
	StrategyID      string
	TradeIdeaID     string
	Confidence      *float64
	OpenedAt        time.Time
	TrailingHigh    float64   // best price seen since entry (persisted; 0 until first seen)
	ExitRequestedAt time.Time // when the pending sell was requested, while Status is ExitPending
}

// HeldSize is the size the position counts as for risk: the executed size, or
// the requested size while the entry is still unconfirmed.
func (p Position) HeldSize() float64 {
	if p.Size > 0 {
		return p.Size
	}
	return p.RequestedSize
}

// Store persists live positions.
type Store interface {
	SaveLivePosition(p Position) error
	DeleteLivePosition(id string) error
	LoadLivePositions() ([]Position, error)
}

// Book is the in-memory live position book, written through to a Store.
// It is safe for concurrent use.
type Book struct {
	mu        sync.Mutex
	store     Store
	positions map[string]Position
}

// New returns an empty book persisting to store (nil keeps it in memory only).
func New(store Store) *Book {
	return &Book{store: store, positions: map[string]Position{}}
}

// Restore replaces the book with the stored positions and returns how many were loaded.
func (b *Book) Restore() (int, error) {
	if b.store == nil {
		return 0, nil
	}
	rows, err := b.store.LoadLivePositions()
	if err != nil {
		return 0, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.positions = make(map[string]Position, len(rows))
	for _, p := range rows {
		b.positions[p.ID] = p
	}
	return len(rows), nil
}

// Save adds or replaces a position. It is kept in memory even when the store
// fails (the error is returned for logging): an accepted order must stay
// tracked for this session whatever happens to the database. Store writes
// happen under the book's lock, so the stored row always matches the latest
// in-memory state (a trailing-high write can never overwrite a newer status).
func (b *Book) Save(p Position) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	// The trailing high only rises over a position's life, so a copy taken
	// before the latest high cannot lower it.
	if old, ok := b.positions[p.ID]; ok {
		p.TrailingHigh = max(p.TrailingHigh, old.TrailingHigh)
	}
	b.positions[p.ID] = p
	if b.store == nil {
		return nil
	}
	return b.store.SaveLivePosition(p)
}

// Remove deletes a position from the book and the store.
func (b *Book) Remove(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.positions, id)
	if b.store == nil {
		return nil
	}
	return b.store.DeleteLivePosition(id)
}

// Get returns the position with the full ID id.
func (b *Book) Get(id string) (Position, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p, ok := b.positions[id]
	return p, ok
}

// Find returns the position whose ID is id or starts with id (the
// dashboard's 8-character form).
func (b *Book) Find(id string) (Position, bool) {
	if p, ok := b.Get(id); ok {
		return p, true
	}
	if id == "" {
		return Position{}, false
	}
	for _, p := range b.All() {
		if strings.HasPrefix(p.ID, id) {
			return p, true
		}
	}
	return Position{}, false
}

// ByOrderID returns the position whose entry or pending exit order is orderID.
func (b *Book) ByOrderID(orderID string) (Position, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, p := range b.positions {
		if orderID != "" && (p.EntryOrderID == orderID || p.ExitOrderID == orderID) {
			return p, true
		}
	}
	return Position{}, false
}

// All returns every position, oldest first.
func (b *Book) All() []Position {
	b.mu.Lock()
	out := make([]Position, 0, len(b.positions))
	for _, p := range b.positions {
		out = append(out, p)
	}
	b.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if !out[i].OpenedAt.Equal(out[j].OpenedAt) {
			return out[i].OpenedAt.Before(out[j].OpenedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// ForMarket returns the positions on market, oldest first.
func (b *Book) ForMarket(market string) []Position {
	var out []Position
	for _, p := range b.All() {
		if p.Market == market {
			out = append(out, p)
		}
	}
	return out
}

// Records returns every position as a domain.PositionRecord (a long of its
// HeldSize) for the risk engine and the one-position-per-pair checks.
func (b *Book) Records() []domain.PositionRecord {
	all := b.All()
	out := make([]domain.PositionRecord, 0, len(all))
	for _, p := range all {
		out = append(out, domain.PositionRecord{
			PositionID: p.ID, Market: p.Market, Size: p.HeldSize(), AvgPrice: p.AvgPrice, Timestamp: p.OpenedAt,
		})
	}
	return out
}

// UpdateTrailingHighs raises each open position's trailing high to the
// current price when the price is a new best, and persists every change. A
// position with no recorded high (one saved before highs were stored) starts
// from the higher of its entry price and the current price, so a restart
// below the entry cannot move the trailing stop's reference down.
func (b *Book) UpdateTrailingHighs(prices map[string]float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, p := range b.positions {
		price, ok := prices[p.Market]
		if !ok || p.Status != Open {
			continue
		}
		high := p.TrailingHigh
		if high <= 0 {
			high = max(p.AvgPrice, price)
		}
		high = max(high, price)
		if high == p.TrailingHigh {
			continue
		}
		p.TrailingHigh = high
		b.positions[id] = p
		if b.store != nil {
			// A failed write only loses the new high at a restart; the next
			// rising price writes it again.
			_ = b.store.SaveLivePosition(p)
		}
	}
}

// StopLossHit reports whether a long bought at entry has lost at least pct
// at price (the paper engine's stop-loss rule for longs).
func StopLossHit(entry, price, pct float64) bool {
	if entry <= 0 {
		return false
	}
	return (entry-price)/entry >= pct
}

// TrailingStopHit reports whether price has retraced pct from the best price
// seen, using the entry price before any high was recorded (the paper
// engine's trailing-stop rule for longs).
func TrailingStopHit(high, entry, price, pct float64) bool {
	if high <= 0 {
		high = entry
	}
	return high > 0 && price <= high*(1-pct)
}
