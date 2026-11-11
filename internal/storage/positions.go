package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"
)

// UpsertOpenPosition inserts or updates an open position. On update only size,
// avg_price and updated_at change; trade_idea_id is set only when non-empty so
// the opening signal link is never overwritten with nothing.
func (r *Repository) UpsertOpenPosition(ctx context.Context, p OpenPositionInput) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := formatDBTime(time.Now())
	var exists int
	err = tx.QueryRowContext(ctx, "SELECT 1 FROM open_positions WHERE position_id = ?", p.PositionID).Scan(&exists)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx, `
			INSERT INTO open_positions (
				position_id, market, size, avg_price, signal_confidence, strategy_id, direction,
				trade_idea_id, unrealized_pnl, opened_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0.0, ?, ?)`,
			p.PositionID, p.Market, p.Size, p.AvgPrice, nullableFloat(p.SignalConfidence), p.StrategyID, p.Direction,
			nullIfEmpty(p.TradeIdeaID), now, now)
	case err != nil:
		return fmt.Errorf("look up position %s: %w", p.PositionID, err)
	case p.TradeIdeaID != "":
		_, err = tx.ExecContext(ctx,
			"UPDATE open_positions SET size = ?, avg_price = ?, updated_at = ?, trade_idea_id = ? WHERE position_id = ?",
			p.Size, p.AvgPrice, now, p.TradeIdeaID, p.PositionID)
	default:
		_, err = tx.ExecContext(ctx,
			"UPDATE open_positions SET size = ?, avg_price = ?, updated_at = ? WHERE position_id = ?",
			p.Size, p.AvgPrice, now, p.PositionID)
	}
	if err != nil {
		return fmt.Errorf("upsert position %s: %w", p.PositionID, err)
	}
	return tx.Commit()
}

// DeleteOpenPosition removes one open position (no-op if absent).
func (r *Repository) DeleteOpenPosition(ctx context.Context, positionID string) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM open_positions WHERE position_id = ?", positionID)
	if err != nil {
		return fmt.Errorf("delete position %s: %w", positionID, err)
	}
	return nil
}

// GetOpenPositions returns every open position row.
func (r *Repository) GetOpenPositions(ctx context.Context) ([]OpenPosition, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT position_id, market, size, avg_price, signal_confidence, strategy_id, direction,
			trade_idea_id, unrealized_pnl, opened_at, updated_at
		FROM open_positions`)
	if err != nil {
		return nil, fmt.Errorf("get open positions: %w", err)
	}
	defer rows.Close()

	result := []OpenPosition{}
	for rows.Next() {
		var (
			p                                      OpenPosition
			market, strategy, direction, ideaID    sql.NullString
			size, avgPrice, confidence, unrealized sql.NullFloat64
			openedAt, updatedAt                    nullTime
		)
		if err := rows.Scan(&p.PositionID, &market, &size, &avgPrice, &confidence, &strategy, &direction,
			&ideaID, &unrealized, &openedAt, &updatedAt); err != nil {
			return nil, err
		}
		p.Market, p.Size, p.AvgPrice = market.String, size.Float64, avgPrice.Float64
		p.SignalConfidence = floatPtr(confidence)
		p.StrategyID, p.Direction, p.TradeIdeaID = stringPtr(strategy), stringPtr(direction), stringPtr(ideaID)
		p.UnrealizedPnL = unrealized.Float64
		p.OpenedAt, p.UpdatedAt = openedAt.orZero(), updatedAt.orZero()
		result = append(result, p)
	}
	return result, rows.Err()
}

// ClearAllOpenPositions deletes every open position and returns the count deleted.
func (r *Repository) ClearAllOpenPositions(ctx context.Context) (int, error) {
	res, err := r.db.ExecContext(ctx, "DELETE FROM open_positions")
	if err != nil {
		return 0, fmt.Errorf("clear open positions: %w", err)
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// SaveSignalOutcome records a closed position for learning. pnl_pct is
// pnl / (|size| * entry_price), or 0 when either is zero.
func (r *Repository) SaveSignalOutcome(ctx context.Context, o SignalOutcomeInput) error {
	pnlPct := 0.0
	if o.EntryPrice != 0 && o.Size != 0 {
		pnlPct = o.PnL / (math.Abs(o.Size) * o.EntryPrice)
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO signal_outcomes (
			strategy_id, market, direction, entry_price, exit_price, size, pnl, pnl_pct,
			confidence_at_entry, exit_reason, entry_at, exit_at, position_id, trade_idea_id, closing_trade_idea_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.StrategyID, o.Market, o.Direction, o.EntryPrice, o.ExitPrice, o.Size, o.PnL, pnlPct,
		nullableFloat(o.Confidence), o.ExitReason, dbTimeOrNil(o.EntryAt), formatDBTime(time.Now()),
		nullIfEmpty(o.PositionID), nullIfEmpty(o.TradeIdeaID), nullIfEmpty(o.ClosingTradeIdeaID))
	if err != nil {
		return fmt.Errorf("save signal outcome: %w", err)
	}
	return nil
}

// GetSignalOutcomes returns closed-trade rows newest first, optionally filtered
// by strategy ("" means all). limit <= 0 uses DefaultSignalOutcomeLimit.
func (r *Repository) GetSignalOutcomes(ctx context.Context, strategyID string, limit int) ([]SignalOutcome, error) {
	query := `SELECT id, strategy_id, market, direction, entry_price, exit_price, size, pnl, pnl_pct,
			confidence_at_entry, exit_reason, entry_at, exit_at, position_id, trade_idea_id, closing_trade_idea_id
		FROM signal_outcomes`
	args := []any{}
	if strategyID != "" {
		query += " WHERE strategy_id = ?"
		args = append(args, strategyID)
	}
	query += " ORDER BY exit_at DESC, id DESC LIMIT ?"
	args = append(args, orDefault(limit, DefaultSignalOutcomeLimit))

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("get signal outcomes: %w", err)
	}
	defer rows.Close()

	result := []SignalOutcome{}
	for rows.Next() {
		var (
			o                                          SignalOutcome
			strategy, market, direction, exitReason    sql.NullString
			positionID, ideaID, closingID              sql.NullString
			entry, exit, size, pnl, pnlPct, confidence sql.NullFloat64
			entryAt, exitAt                            nullTime
		)
		if err := rows.Scan(&o.ID, &strategy, &market, &direction, &entry, &exit, &size, &pnl, &pnlPct,
			&confidence, &exitReason, &entryAt, &exitAt, &positionID, &ideaID, &closingID); err != nil {
			return nil, err
		}
		o.StrategyID, o.Market, o.Direction, o.ExitReason = strategy.String, market.String, direction.String, exitReason.String
		o.EntryPrice, o.ExitPrice, o.Size = entry.Float64, exit.Float64, size.Float64
		o.PnL, o.PnLPct, o.ConfidenceAtEntry = pnl.Float64, pnlPct.Float64, floatPtr(confidence)
		o.EntryAt, o.ExitAt = entryAt.orZero(), exitAt.orZero()
		o.PositionID, o.TradeIdeaID, o.ClosingTradeIdeaID = stringPtr(positionID), stringPtr(ideaID), stringPtr(closingID)
		result = append(result, o)
	}
	return result, rows.Err()
}

// GetClosedTrades returns closed trades newest first, joined with the entry
// indicator snapshot of the linked trade idea (nil when there is none).
// limit <= 0 uses DefaultClosedTradesLimit.
func (r *Repository) GetClosedTrades(ctx context.Context, limit int) ([]ClosedTrade, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT so.strategy_id, so.market, so.direction, so.entry_price, so.exit_price, so.size, so.pnl,
			so.pnl_pct, so.confidence_at_entry, so.exit_reason, so.trade_idea_id, so.closing_trade_idea_id,
			so.entry_at, so.exit_at, so.position_id, ti.indicators
		FROM signal_outcomes so
		LEFT OUTER JOIN trade_ideas ti ON so.trade_idea_id = ti.id
		ORDER BY so.exit_at DESC, so.id DESC
		LIMIT ?`, orDefault(limit, DefaultClosedTradesLimit))
	if err != nil {
		return nil, fmt.Errorf("get closed trades: %w", err)
	}
	defer rows.Close()

	result := []ClosedTrade{}
	for rows.Next() {
		var (
			t                                          ClosedTrade
			strategy, market, direction, exitReason    sql.NullString
			ideaID, closingID, positionID, indicators  sql.NullString
			entry, exit, size, pnl, pnlPct, confidence sql.NullFloat64
			entryAt, exitAt                            nullTime
		)
		if err := rows.Scan(&strategy, &market, &direction, &entry, &exit, &size, &pnl, &pnlPct, &confidence,
			&exitReason, &ideaID, &closingID, &entryAt, &exitAt, &positionID, &indicators); err != nil {
			return nil, err
		}
		t.Strategy, t.Market, t.Direction, t.ExitReason = strategy.String, market.String, direction.String, exitReason.String
		t.EntryPrice, t.ExitPrice, t.Size = entry.Float64, exit.Float64, size.Float64
		t.PnL, t.PnLPct, t.Confidence = pnl.Float64, pnlPct.Float64, floatPtr(confidence)
		t.TradeIdeaID, t.ClosingTradeIdeaID = ideaID.String, closingID.String
		t.EntryAt, t.ExitAt = entryAt.iso(), exitAt.iso()
		t.PositionID, t.PositionIDFull = short8(positionID.String), positionID.String
		if err := decodeJSON(indicators, &t.Indicators); err != nil {
			return nil, err
		}
		if len(t.Indicators) == 0 {
			t.Indicators = nil // Python: `ind or None`
		}
		result = append(result, t)
	}
	return result, rows.Err()
}

// GetPnLSummary returns realised P&L totals grouped by exit day and market,
// rounded to 2 dp. limit <= 0 uses DefaultPnLSummaryLimit.
func (r *Repository) GetPnLSummary(ctx context.Context, limit int) (PnLSummary, error) {
	trades, err := r.GetClosedTrades(ctx, orDefault(limit, DefaultPnLSummaryLimit))
	if err != nil {
		return PnLSummary{}, err
	}
	byDay := map[string]float64{}
	byMarket := map[string]float64{}
	total := 0.0
	for _, t := range trades {
		total += t.PnL
		market := t.Market
		if market == "" {
			market = "unknown"
		}
		day := t.ExitAt
		if len(day) > 10 {
			day = day[:10]
		}
		if day == "" {
			day = "unknown"
		}
		byMarket[market] += t.PnL
		byDay[day] += t.PnL
	}
	return PnLSummary{TotalPnL: round2(total), ByDay: roundAll(byDay), ByMarket: roundAll(byMarket)}, nil
}

// roundAll rounds every value to 2 dp. Python sorted the keys; encoding/json
// already emits map keys in sorted order, so the JSON output is identical.
func roundAll(m map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(m))
	for k, v := range m {
		out[k] = round2(v)
	}
	return out
}
