package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// LivePosition is one row of live_positions: a position opened by a real
// Kraken order. Status is "entry_pending", "open" or "exit_pending".
type LivePosition struct {
	PositionID       string
	Market           string
	Status           string
	RequestedSize    float64 // base volume sent with the entry order
	Size             float64 // base volume actually executed and held
	AvgPrice         float64
	EntryFee         float64
	EntryOrderID     string
	ExitOrderID      string
	ExitReason       string
	ExitTradeIdeaID  string
	StrategyID       string
	TradeIdeaID      string
	SignalConfidence *float64
	OpenedAt         time.Time
	TrailingHigh     float64   // best price seen since entry (0: not recorded)
	ExitRequestedAt  time.Time // when the pending exit order was requested (zero: none)
}

// PositionOrder is one live order_records row of a position.
type PositionOrder struct {
	OrderID         string
	ExchangeOrderID string
	ClientOrderID   string
	Direction       string // "long" (buy) or "short" (sell)
	Status          string
	Size            float64 // the size sent, or the executed size once reconciled
	Price           float64
	Fee             float64
	PnLRecorded     bool // a realised P&L is stamped (the close was recorded)
	Timestamp       time.Time
}

// GetLiveOrdersForPosition returns the live orders stamped with positionID, oldest first.
func (r *Repository) GetLiveOrdersForPosition(ctx context.Context, positionID string) ([]PositionOrder, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, exchange_order_id, client_order_id, direction, status, size, price, fee, pnl, timestamp
		FROM order_records
		WHERE position_id = ? AND environment = 'live'
		ORDER BY timestamp ASC, rowid ASC`, positionID)
	if err != nil {
		return nil, fmt.Errorf("get live orders for position %s: %w", positionID, err)
	}
	defer rows.Close()

	result := []PositionOrder{}
	for rows.Next() {
		var (
			o                                 PositionOrder
			txid, clientID, direction, status sql.NullString
			size, price, fee, pnl             sql.NullFloat64
			timestamp                         nullTime
		)
		if err := rows.Scan(&o.OrderID, &txid, &clientID, &direction, &status, &size, &price, &fee, &pnl, &timestamp); err != nil {
			return nil, err
		}
		o.ExchangeOrderID, o.ClientOrderID, o.Direction, o.Status = txid.String, clientID.String, direction.String, status.String
		o.Size, o.Price, o.Fee, o.PnLRecorded = size.Float64, price.Float64, fee.Float64, pnl.Valid
		o.Timestamp = timestamp.orZero()
		result = append(result, o)
	}
	return result, rows.Err()
}

// SaveLivePosition inserts or fully replaces a live position.
func (r *Repository) SaveLivePosition(ctx context.Context, p LivePosition) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO live_positions (
			position_id, market, status, requested_size, size, avg_price, entry_fee,
			entry_order_id, exit_order_id, exit_reason, exit_trade_idea_id,
			strategy_id, trade_idea_id, signal_confidence, opened_at, updated_at,
			trailing_high, exit_requested_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(position_id) DO UPDATE SET
			market = excluded.market,
			status = excluded.status,
			requested_size = excluded.requested_size,
			size = excluded.size,
			avg_price = excluded.avg_price,
			entry_fee = excluded.entry_fee,
			entry_order_id = excluded.entry_order_id,
			exit_order_id = excluded.exit_order_id,
			exit_reason = excluded.exit_reason,
			exit_trade_idea_id = excluded.exit_trade_idea_id,
			strategy_id = excluded.strategy_id,
			trade_idea_id = excluded.trade_idea_id,
			signal_confidence = excluded.signal_confidence,
			opened_at = excluded.opened_at,
			updated_at = excluded.updated_at,
			trailing_high = excluded.trailing_high,
			exit_requested_at = excluded.exit_requested_at`,
		p.PositionID, p.Market, p.Status, p.RequestedSize, p.Size, p.AvgPrice, p.EntryFee,
		nullIfEmpty(p.EntryOrderID), nullIfEmpty(p.ExitOrderID), nullIfEmpty(p.ExitReason), nullIfEmpty(p.ExitTradeIdeaID),
		nullIfEmpty(p.StrategyID), nullIfEmpty(p.TradeIdeaID), nullableFloat(p.SignalConfidence),
		formatDBTime(timeOrNow(p.OpenedAt)), formatDBTime(time.Now()),
		p.TrailingHigh, dbTimeOrNil(p.ExitRequestedAt),
	)
	if err != nil {
		return fmt.Errorf("save live position %s: %w", p.PositionID, err)
	}
	return nil
}

// GetOrderExecution returns an order's status, size, price, fee and txid,
// and whether its realised P&L has been stamped. found is false when the
// order does not exist.
func (r *Repository) GetOrderExecution(ctx context.Context, orderID string) (exec OrderExecution, pnlRecorded, found bool, err error) {
	var status, txid sql.NullString
	var size, price, fee, pnl sql.NullFloat64
	err = r.db.QueryRowContext(ctx,
		"SELECT status, size, price, fee, exchange_order_id, pnl FROM order_records WHERE id = ?", orderID).
		Scan(&status, &size, &price, &fee, &txid, &pnl)
	if errors.Is(err, sql.ErrNoRows) {
		return OrderExecution{}, false, false, nil
	}
	if err != nil {
		return OrderExecution{}, false, false, fmt.Errorf("get order execution %s: %w", orderID, err)
	}
	exec = OrderExecution{
		OrderID: orderID, ExchangeOrderID: txid.String, Status: status.String,
		ExecutedSize: size.Float64, AvgPrice: price.Float64, Fee: fee.Float64,
	}
	return exec, pnl.Valid, true, nil
}

// DeleteLivePosition removes one live position (no-op if absent).
func (r *Repository) DeleteLivePosition(ctx context.Context, positionID string) error {
	if _, err := r.db.ExecContext(ctx, "DELETE FROM live_positions WHERE position_id = ?", positionID); err != nil {
		return fmt.Errorf("delete live position %s: %w", positionID, err)
	}
	return nil
}

// GetLivePositions returns every live position, oldest first.
func (r *Repository) GetLivePositions(ctx context.Context) ([]LivePosition, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT position_id, market, status, requested_size, size, avg_price, entry_fee,
			entry_order_id, exit_order_id, exit_reason, exit_trade_idea_id,
			strategy_id, trade_idea_id, signal_confidence, opened_at, trailing_high, exit_requested_at
		FROM live_positions
		ORDER BY opened_at ASC, rowid ASC`)
	if err != nil {
		return nil, fmt.Errorf("get live positions: %w", err)
	}
	defer rows.Close()

	result := []LivePosition{}
	for rows.Next() {
		var (
			p                                          LivePosition
			market, status, entryOrder, exitOrder      sql.NullString
			exitReason, exitIdea, strategy, ideaID     sql.NullString
			requested, size, avgPrice, fee, confidence sql.NullFloat64
			trailingHigh                               sql.NullFloat64
			openedAt, exitRequestedAt                  nullTime
		)
		if err := rows.Scan(&p.PositionID, &market, &status, &requested, &size, &avgPrice, &fee,
			&entryOrder, &exitOrder, &exitReason, &exitIdea, &strategy, &ideaID, &confidence, &openedAt,
			&trailingHigh, &exitRequestedAt); err != nil {
			return nil, err
		}
		p.TrailingHigh, p.ExitRequestedAt = trailingHigh.Float64, exitRequestedAt.orZero()
		p.Market, p.Status = market.String, status.String
		p.RequestedSize, p.Size, p.AvgPrice, p.EntryFee = requested.Float64, size.Float64, avgPrice.Float64, fee.Float64
		p.EntryOrderID, p.ExitOrderID, p.ExitReason, p.ExitTradeIdeaID = entryOrder.String, exitOrder.String, exitReason.String, exitIdea.String
		p.StrategyID, p.TradeIdeaID = strategy.String, ideaID.String
		p.SignalConfidence = floatPtr(confidence)
		p.OpenedAt = openedAt.orZero()
		result = append(result, p)
	}
	return result, rows.Err()
}
