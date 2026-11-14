// Package paper simulates Kraken spot fills with fee and slippage modelling.
//
// Each approved trade creates its own position keyed by a UUID position id, so
// several trades on the same pair stay independently visible. Positions are
// mirrored to the database through Repository and survive restarts.
package paper

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/observability"
)

var logger = observability.Logger("paper_engine")

const (
	// TakerFeeRate is Kraken's taker fee for low-volume accounts (0.26%).
	TakerFeeRate = 0.0026
	// SlippageRate is a conservative one-way slippage estimate (0.1%).
	SlippageRate = 0.001
	// SimulatedLatencyMS is the nominal simulated fill latency.
	SimulatedLatencyMS = 150
)

// Execution rejection reasons, persisted with rejected trades.
const (
	ReasonInsufficientFunds = "insufficient_funds"
	ReasonLongAlreadyOpen   = "long_position_already_open"
	ReasonShortAlreadyOpen  = "short_position_already_open"
)

// OpenPositionRow is an open position as stored in the database.
type OpenPositionRow struct {
	PositionID       string
	Market           string
	Size             float64
	AvgPrice         float64
	UnrealizedPnL    float64
	Direction        string
	StrategyID       string
	SignalConfidence *float64
	TradeIdeaID      string
	OpenedAt         time.Time // zero when unknown; ignored on upsert
}

// RejectedTrade is an execution-rejected order for the rejected-trades register.
type RejectedTrade struct {
	Market      string
	Direction   string
	Size        float64
	Price       float64
	Confidence  *float64
	Reason      string
	TradeIdeaID string
}

// SignalOutcome is a closed-position record used for history and learning.
type SignalOutcome struct {
	StrategyID         string
	Market             string
	Direction          string
	EntryPrice         float64
	ExitPrice          float64
	Size               float64
	PnL                float64
	Confidence         *float64
	ExitReason         string
	EntryAt            time.Time
	PositionID         string
	TradeIdeaID        string
	ClosingTradeIdeaID string
}

// Repository is the persistence the paper engine needs (implemented by the
// storage repository).
type Repository interface {
	GetOpenPositions() ([]OpenPositionRow, error)
	SaveRejectedTrade(trade RejectedTrade) error
	SaveOrder(order domain.OrderRecord, approvalID string, fee float64, environment, tradeIdeaID string) error
	SaveFill(fill domain.FillRecord) error
	UpsertOpenPosition(row OpenPositionRow) error
	DeleteOpenPosition(positionID string) error
	SaveSignalOutcome(outcome SignalOutcome) error
}

// PositionMeta is per-position context kept alongside the position record. It
// outlives the position after a close so the outcome can be recorded.
type PositionMeta struct {
	StrategyID    string
	Direction     string // "long" | "short" ("" if unknown)
	Confidence    *float64
	OpenedAt      time.Time
	Market        string
	TradeIdeaID   string // signal that opened the position
	EntryFee      float64
	ShortProceeds *float64 // locked sale value of a paper short; nil for longs/restored rows
	TrailingHigh  *float64 // nil until the first live price after a restore
	TrailingLow   *float64

	// Close details, set once the position has been closed.
	Closed           bool
	AvgPriceAtClose  float64
	SizeAtClose      float64
	ExitPriceAtClose float64
	ExitFeeAtClose   float64
}

// ExecuteOptions carries the optional context for Execute.
type ExecuteOptions struct {
	StrategyID       string
	SignalConfidence *float64
	Environment      string // default "paper"
	TradeIdeaID      string
}

// CloseOptions carries the optional context for ClosePosition.
type CloseOptions struct {
	Environment       string // default "paper"
	ApprovalRequestID string // close source recorded on the order; default "stop_loss"
}

// Engine simulates spot fills against live prices. It is safe for concurrent use.
type Engine struct {
	mu        sync.Mutex
	repo      Repository
	now       func() time.Time
	cash      float64
	positions map[string]domain.PositionRecord
	openOrder []string // open position ids in insertion order
	meta      map[string]*PositionMeta
	orders    []domain.OrderRecord
	fills     []domain.FillRecord
}

// NewEngine creates a paper engine with the given cash. repo may be nil
// (no persistence) and a nil clock defaults to the current UTC time.
func NewEngine(startingCapital float64, repo Repository, now func() time.Time) *Engine {
	if now == nil {
		now = domain.NowUTC
	}
	return &Engine{
		repo:      repo,
		now:       now,
		cash:      startingCapital,
		positions: map[string]domain.PositionRecord{},
		meta:      map[string]*PositionMeta{},
	}
}

// RestoreFromDB reloads open positions from the database after a restart.
func (e *Engine) RestoreFromDB() error {
	if e.repo == nil {
		return nil
	}
	rows, err := e.repo.GetOpenPositions()
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, row := range rows {
		openedAt := row.OpenedAt
		if openedAt.IsZero() {
			openedAt = e.now().UTC()
		}
		if _, exists := e.positions[row.PositionID]; !exists {
			e.openOrder = append(e.openOrder, row.PositionID)
		}
		e.positions[row.PositionID] = domain.PositionRecord{
			PositionID:    row.PositionID,
			Market:        row.Market,
			Size:          row.Size,
			AvgPrice:      row.AvgPrice,
			UnrealizedPnL: row.UnrealizedPnL,
			Timestamp:     openedAt,
		}
		// Watermarks stay nil: UpdateTrailingPrices seeds them from the first
		// live price rather than the stale entry price.
		e.meta[row.PositionID] = &PositionMeta{
			StrategyID:  row.StrategyID,
			Direction:   row.Direction,
			Confidence:  copyFloat(row.SignalConfidence),
			OpenedAt:    openedAt,
			Market:      row.Market,
			TradeIdeaID: row.TradeIdeaID,
		}
	}
	if len(rows) > 0 {
		logger.Info("Restored open positions from DB", "count", len(rows))
	}
	return nil
}

// Execute simulates an immediate fill and returns the order and the affected
// position id: the new position for a long or paper short, the closed long
// for a short that closes one, or "" when the order is rejected.
func (e *Engine) Execute(intent domain.ExecutionIntent, marketPrice float64, opts ExecuteOptions) (domain.OrderRecord, string) {
	if opts.Environment == "" {
		opts.Environment = "paper"
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	fillPrice := applySlippage(intent.Direction, marketPrice)
	fillValue := intent.Size * fillPrice
	fee := fillValue * TakerFeeRate
	order := domain.OrderRecord{
		ID:        domain.NewID(),
		Market:    intent.Market,
		Direction: intent.Direction,
		Size:      intent.Size,
		Price:     fillPrice,
		Status:    "pending",
		Timestamp: e.now().UTC(),
	}

	if reason := e.rejectionReasonLocked(intent, fillValue, fee); reason != "" {
		order.Status = "rejected"
		e.orders = append(e.orders, order)
		if e.repo != nil {
			e.logRepoError("SaveRejectedTrade", e.repo.SaveRejectedTrade(RejectedTrade{
				Market:      intent.Market,
				Direction:   string(intent.Direction),
				Size:        intent.Size,
				Price:       fillPrice,
				Confidence:  copyFloat(opts.SignalConfidence),
				Reason:      reason,
				TradeIdeaID: opts.TradeIdeaID,
			}))
		}
		logger.Warn("Paper order rejected", "market", intent.Market,
			"required", fillValue+fee, "available_cash", e.cash, "reason", reason)
		return order, ""
	}

	fill := domain.FillRecord{
		OrderID:   order.ID,
		FillPrice: fillPrice,
		FillSize:  intent.Size,
		Fee:       fee,
		Timestamp: e.now().UTC(),
	}
	e.fills = append(e.fills, fill)
	positionID := e.applyFillLocked(intent, fillPrice, fee, opts)
	order.Status = "filled"
	order.PositionID = positionID
	e.orders = append(e.orders, order)

	if e.repo != nil {
		e.logRepoError("SaveOrder", e.repo.SaveOrder(order, intent.ApprovalRequestID, fee, opts.Environment, opts.TradeIdeaID))
		e.logRepoError("SaveFill", e.repo.SaveFill(fill))
		if pos, open := e.positions[positionID]; open {
			meta := e.meta[positionID]
			e.logRepoError("UpsertOpenPosition", e.repo.UpsertOpenPosition(OpenPositionRow{
				PositionID:       positionID,
				Market:           intent.Market,
				Size:             pos.Size,
				AvgPrice:         pos.AvgPrice,
				Direction:        meta.Direction,
				StrategyID:       meta.StrategyID,
				SignalConfidence: copyFloat(meta.Confidence),
				TradeIdeaID:      meta.TradeIdeaID, // opening signal survives restarts
				OpenedAt:         meta.OpenedAt,
			}))
		} else {
			// The short closed an existing long.
			e.logRepoError("DeleteOpenPosition", e.repo.DeleteOpenPosition(positionID))
		}
	}

	logger.Info("Paper fill simulated", "order_id", order.ID, "market", intent.Market,
		"direction", string(intent.Direction), "fill_price", fillPrice,
		"fill_size", intent.Size, "fee", fee, "position_id", positionID)
	return order, positionID
}

// ClosePosition closes a specific position by id at the market price (used by
// stop-loss, manual close and operator reset). It returns false if the
// position is not open.
func (e *Engine) ClosePosition(positionID string, marketPrice float64, opts CloseOptions) (domain.OrderRecord, bool) {
	if opts.Environment == "" {
		opts.Environment = "paper"
	}
	if opts.ApprovalRequestID == "" {
		opts.ApprovalRequestID = "stop_loss"
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	pos, ok := e.positions[positionID]
	if !ok {
		return domain.OrderRecord{}, false
	}
	exitDirection := domain.Long
	if pos.Size > 0 {
		exitDirection = domain.Short
	}
	fillPrice := applySlippage(exitDirection, marketPrice)
	fillSize := math.Abs(pos.Size)
	fillValue := fillSize * fillPrice
	fee := fillValue * TakerFeeRate

	meta := e.metaLocked(positionID)
	if exitDirection == domain.Short {
		// Closing a long: receive the sale proceeds.
		e.cash += fillValue - fee
	} else {
		// Closing a short: release the locked proceeds and settle P&L. Restored
		// shorts have no recorded proceeds, so fall back to entry price × size.
		proceeds := pos.AvgPrice * fillSize
		if meta.ShortProceeds != nil {
			proceeds = *meta.ShortProceeds
		}
		e.cash += proceeds - fillValue - fee
	}

	now := e.now().UTC()
	order := domain.OrderRecord{
		ID: domain.NewID(), Market: pos.Market, Direction: exitDirection,
		Size: fillSize, Price: fillPrice, Status: "filled",
		Timestamp: now, PositionID: positionID,
	}
	e.orders = append(e.orders, order)
	fill := domain.FillRecord{OrderID: order.ID, FillPrice: fillPrice, FillSize: fillSize, Fee: fee, Timestamp: now}
	e.fills = append(e.fills, fill)

	markClosed(meta, pos.AvgPrice, fillSize, fillPrice, fee)
	e.removePositionLocked(positionID)

	if e.repo != nil {
		e.logRepoError("SaveOrder", e.repo.SaveOrder(order, opts.ApprovalRequestID, fee, opts.Environment, ""))
		e.logRepoError("SaveFill", e.repo.SaveFill(fill))
		e.logRepoError("DeleteOpenPosition", e.repo.DeleteOpenPosition(positionID))
	}
	logger.Info(fmt.Sprintf("Position closed (%s)", opts.ApprovalRequestID),
		"position_id", positionID, "market", pos.Market, "fill_price", fillPrice, "fee", fee)
	return order, true
}

// RealisedPnL computes the realised P&L of a closed position from its actual
// fill prices and both fees. It returns false if the position is not closed.
func (e *Engine) RealisedPnL(positionID string) (float64, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	meta, ok := e.meta[positionID]
	if !ok || !meta.Closed {
		return 0, false
	}
	return realisedPnL(meta), true
}

// RecordClosedTrade persists a signal outcome for a closed position and returns
// its realised P&L. Call after Execute/ClosePosition. It returns false when no
// repository is configured or the position has not been closed.
// closingTradeIdeaID is the signal that triggered the close, if any.
func (e *Engine) RecordClosedTrade(positionID string, exitPrice float64, exitReason, closingTradeIdeaID string) (float64, bool) {
	if e.repo == nil {
		return 0, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	meta, ok := e.meta[positionID]
	if !ok || !meta.Closed {
		return 0, false
	}
	actualExit := meta.ExitPriceAtClose
	if actualExit == 0 {
		actualExit = exitPrice
	}
	pnl := realisedPnL(meta)

	market := meta.Market
	if market == "" {
		for i := len(e.orders) - 1; i >= 0; i-- {
			if e.orders[i].PositionID == positionID {
				market = e.orders[i].Market
				break
			}
		}
	}
	strategyID := meta.StrategyID
	if strategyID == "" {
		strategyID = "unknown"
	}
	openedAt := meta.OpenedAt
	if openedAt.IsZero() {
		openedAt = e.now().UTC()
	}
	e.logRepoError("SaveSignalOutcome", e.repo.SaveSignalOutcome(SignalOutcome{
		StrategyID:         strategyID,
		Market:             market,
		Direction:          meta.Direction,
		EntryPrice:         meta.AvgPriceAtClose,
		ExitPrice:          actualExit,
		Size:               math.Abs(meta.SizeAtClose),
		PnL:                pnl,
		Confidence:         copyFloat(meta.Confidence),
		ExitReason:         exitReason,
		EntryAt:            openedAt,
		PositionID:         positionID,
		TradeIdeaID:        meta.TradeIdeaID,
		ClosingTradeIdeaID: closingTradeIdeaID,
	}))
	return pnl, true
}

// UpdateMarkPrices recomputes unrealised P&L for every open position.
func (e *Engine) UpdateMarkPrices(prices map[string]float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for id, pos := range e.positions {
		if price, ok := prices[pos.Market]; ok {
			pos.UnrealizedPnL = pos.Size * (price - pos.AvgPrice)
			e.positions[id] = pos
		}
	}
}

// UpdateTrailingPrices updates per-position trailing high/low watermarks.
func (e *Engine) UpdateTrailingPrices(prices map[string]float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for id, pos := range e.positions {
		price, ok := prices[pos.Market]
		if !ok {
			continue
		}
		meta := e.metaLocked(id)
		if pos.Size > 0 {
			if meta.TrailingHigh == nil || price > *meta.TrailingHigh {
				meta.TrailingHigh = domain.Float(price)
			}
		} else if meta.TrailingLow == nil || price < *meta.TrailingLow {
			meta.TrailingLow = domain.Float(price)
		}
	}
}

// TrailingStopTriggered reports whether price has retraced trailPct from the
// best price seen (falling back to the entry price before any update).
func (e *Engine) TrailingStopTriggered(positionID string, marketPrice, trailPct float64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	pos, ok := e.positions[positionID]
	if !ok {
		return false
	}
	meta := e.meta[positionID]
	if pos.Size > 0 {
		high := pos.AvgPrice
		if meta != nil && meta.TrailingHigh != nil && *meta.TrailingHigh != 0 {
			high = *meta.TrailingHigh
		}
		return marketPrice <= high*(1-trailPct)
	}
	low := pos.AvgPrice
	if meta != nil && meta.TrailingLow != nil && *meta.TrailingLow != 0 {
		low = *meta.TrailingLow
	}
	return marketPrice >= low*(1+trailPct)
}

// StopLossTriggered reports whether a position is losing at least stopLossPct
// versus its entry price.
func (e *Engine) StopLossTriggered(positionID string, marketPrice, stopLossPct float64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	pos, ok := e.positions[positionID]
	if !ok || pos.AvgPrice <= 0 {
		return false
	}
	lossPct := (marketPrice - pos.AvgPrice) / pos.AvgPrice
	if pos.Size > 0 {
		lossPct = (pos.AvgPrice - marketPrice) / pos.AvgPrice
	}
	return lossPct >= stopLossPct
}

// TotalEquity returns cash plus position value at the given prices (entry
// price when a market has no price).
func (e *Engine) TotalEquity(prices map[string]float64) float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	positionsValue := 0.0
	for _, pos := range e.positions {
		price, ok := prices[pos.Market]
		if !ok {
			price = pos.AvgPrice
		}
		if pos.Size > 0 {
			positionsValue += pos.Size * price // long: mark-to-market value
		} else {
			// Short: only the fee left cash at open, so add unrealised P&L.
			positionsValue += (pos.AvgPrice - price) * math.Abs(pos.Size)
		}
	}
	return e.cash + positionsValue
}

// Cash returns the free paper cash.
func (e *Engine) Cash() float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cash
}

// SetCash overrides the paper cash (used to restore it from the DB at startup).
func (e *Engine) SetCash(cash float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cash = cash
}

// Position returns an open position by id.
func (e *Engine) Position(positionID string) (domain.PositionRecord, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	pos, ok := e.positions[positionID]
	return pos, ok
}

// OpenPositions returns all open positions in the order they were opened.
func (e *Engine) OpenPositions() []domain.PositionRecord {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]domain.PositionRecord, 0, len(e.openOrder))
	for _, id := range e.openOrder {
		out = append(out, e.positions[id])
	}
	return out
}

// PositionsForMarket returns the open positions for one market.
func (e *Engine) PositionsForMarket(market string) []domain.PositionRecord {
	var out []domain.PositionRecord
	for _, pos := range e.OpenPositions() {
		if pos.Market == market {
			out = append(out, pos)
		}
	}
	return out
}

// Meta returns a copy of the context for a position (open or closed).
func (e *Engine) Meta(positionID string) (PositionMeta, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	meta, ok := e.meta[positionID]
	if !ok {
		return PositionMeta{}, false
	}
	clone := *meta
	clone.Confidence = copyFloat(meta.Confidence)
	clone.ShortProceeds = copyFloat(meta.ShortProceeds)
	clone.TrailingHigh = copyFloat(meta.TrailingHigh)
	clone.TrailingLow = copyFloat(meta.TrailingLow)
	return clone, true
}

// ClearPositions drops every open position and all position metadata from
// memory (operator reset). It does not touch the database.
func (e *Engine) ClearPositions() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.positions = map[string]domain.PositionRecord{}
	e.openOrder = nil
	e.meta = map[string]*PositionMeta{}
}

// Orders returns a copy of every order the engine has produced.
func (e *Engine) Orders() []domain.OrderRecord {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]domain.OrderRecord{}, e.orders...)
}

// Fills returns a copy of every simulated fill.
func (e *Engine) Fills() []domain.FillRecord {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]domain.FillRecord{}, e.fills...)
}

// rejectionReasonLocked returns why an intent cannot fill, or "" if it can.
func (e *Engine) rejectionReasonLocked(intent domain.ExecutionIntent, fillValue, fee float64) string {
	totalLong, hasLong, hasShort := 0.0, false, false
	for _, pos := range e.positions {
		if pos.Market != intent.Market {
			continue
		}
		if pos.Size > 0 {
			totalLong += pos.Size
			hasLong = true
		} else if pos.Size < 0 {
			hasShort = true
		}
	}
	affordable := e.cash >= fillValue+fee

	if intent.Direction == domain.Long {
		// Hard limit: one position per pair.
		if hasLong {
			logger.Warn("One-position-per-pair limit: long already open", "market", intent.Market)
			return ReasonLongAlreadyOpen
		}
		if !affordable {
			return ReasonInsufficientFunds
		}
		return ""
	}

	// Short: closing an existing long takes priority.
	if totalLong >= intent.Size {
		return ""
	}
	if hasShort {
		logger.Warn("One-position-per-pair limit: short already open", "market", intent.Market)
		return ReasonShortAlreadyOpen
	}
	// A paper short with no long to close needs cash as margin.
	if !affordable {
		return ReasonInsufficientFunds
	}
	return ""
}

// applyFillLocked applies a confirmed fill to cash and positions and returns
// the affected position id.
func (e *Engine) applyFillLocked(intent domain.ExecutionIntent, fillPrice, fee float64, opts ExecuteOptions) string {
	fillValue := intent.Size * fillPrice
	now := e.now().UTC()

	if intent.Direction == domain.Long {
		e.cash -= fillValue + fee
		id := e.openPositionLocked(intent.Market, intent.Size, fillPrice, now)
		e.meta[id] = &PositionMeta{
			StrategyID: opts.StrategyID, Direction: "long", Confidence: copyFloat(opts.SignalConfidence),
			OpenedAt: now, Market: intent.Market, TradeIdeaID: opts.TradeIdeaID,
			EntryFee: fee, TrailingHigh: domain.Float(fillPrice),
		}
		return id
	}

	// Short: close the oldest long on this market (FIFO) if there is one.
	var longs []domain.PositionRecord
	for _, id := range e.openOrder {
		if pos := e.positions[id]; pos.Market == intent.Market && pos.Size > 0 {
			longs = append(longs, pos)
		}
	}
	sort.SliceStable(longs, func(i, j int) bool { return longs[i].Timestamp.Before(longs[j].Timestamp) })
	if len(longs) > 0 {
		oldest := longs[0]
		e.cash += fillValue - fee // receive sale proceeds minus fee
		meta := e.metaLocked(oldest.PositionID)
		markClosed(meta, oldest.AvgPrice, oldest.Size, fillPrice, fee)
		meta.Market = intent.Market
		e.removePositionLocked(oldest.PositionID)
		return oldest.PositionID
	}

	// No long to close: open a paper short. Only the fee leaves cash; the sale
	// proceeds are locked as margin so free cash cannot increase.
	e.cash -= fee
	id := e.openPositionLocked(intent.Market, -intent.Size, fillPrice, now)
	e.meta[id] = &PositionMeta{
		StrategyID: opts.StrategyID, Direction: "short", Confidence: copyFloat(opts.SignalConfidence),
		OpenedAt: now, Market: intent.Market, TradeIdeaID: opts.TradeIdeaID,
		EntryFee: fee, ShortProceeds: domain.Float(fillValue), TrailingLow: domain.Float(fillPrice),
	}
	return id
}

// openPositionLocked adds a new position and returns its id.
func (e *Engine) openPositionLocked(market string, size, price float64, now time.Time) string {
	id := domain.NewID()
	e.positions[id] = domain.PositionRecord{
		PositionID: id, Market: market, Size: size, AvgPrice: price, Timestamp: now,
	}
	e.openOrder = append(e.openOrder, id)
	return id
}

// removePositionLocked deletes an open position (its metadata is kept).
func (e *Engine) removePositionLocked(positionID string) {
	delete(e.positions, positionID)
	for i, id := range e.openOrder {
		if id == positionID {
			e.openOrder = append(e.openOrder[:i], e.openOrder[i+1:]...)
			break
		}
	}
}

// metaLocked returns the metadata for a position, creating it if missing.
func (e *Engine) metaLocked(positionID string) *PositionMeta {
	meta, ok := e.meta[positionID]
	if !ok {
		meta = &PositionMeta{}
		e.meta[positionID] = meta
	}
	return meta
}

// logRepoError logs a failed persistence call; the in-memory state stays authoritative.
func (e *Engine) logRepoError(op string, err error) {
	if err != nil {
		logger.Error("Paper engine persistence failed", "operation", op, "error", err.Error())
	}
}

// markClosed records close details on a position's metadata.
func markClosed(meta *PositionMeta, avgPrice, size, exitPrice, exitFee float64) {
	meta.Closed = true
	meta.AvgPriceAtClose = avgPrice
	meta.SizeAtClose = size
	meta.ExitPriceAtClose = exitPrice
	meta.ExitFeeAtClose = exitFee
}

// realisedPnL computes P&L from close metadata. As in the Python engine, any
// direction other than "long" is treated as a short.
func realisedPnL(meta *PositionMeta) float64 {
	signedSize := -meta.SizeAtClose
	if meta.Direction == "long" {
		signedSize = meta.SizeAtClose
	}
	return signedSize*(meta.ExitPriceAtClose-meta.AvgPriceAtClose) - meta.EntryFee - meta.ExitFeeAtClose
}

// applySlippage moves the price against the trader by SlippageRate.
func applySlippage(direction domain.Direction, price float64) float64 {
	if direction == domain.Long {
		return price * (1 + SlippageRate)
	}
	return price * (1 - SlippageRate)
}

// copyFloat returns an independent copy of an optional float.
func copyFloat(v *float64) *float64 {
	if v == nil {
		return nil
	}
	return domain.Float(*v)
}
