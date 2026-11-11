package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

// SavePriceTick records one price observation for a symbol (volume optional).
func (r *Repository) SavePriceTick(ctx context.Context, symbol string, price float64, volume *float64) error {
	_, err := r.db.ExecContext(ctx,
		"INSERT INTO market_snapshots (symbol, timestamp, price, volume) VALUES (?, ?, ?, ?)",
		symbol, formatDBTime(time.Now()), price, nullableFloat(volume))
	if err != nil {
		return fmt.Errorf("save price tick %s: %w", symbol, err)
	}
	return nil
}

// GetRecentPrices returns up to limit most-recent prices for symbol, oldest first.
// limit <= 0 uses DefaultRecentPricesLimit.
func (r *Repository) GetRecentPrices(ctx context.Context, symbol string, limit int) ([]float64, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT price FROM market_snapshots WHERE symbol = ? ORDER BY timestamp DESC, id DESC LIMIT ?",
		symbol, orDefault(limit, DefaultRecentPricesLimit))
	if err != nil {
		return nil, fmt.Errorf("get recent prices %s: %w", symbol, err)
	}
	defer rows.Close()

	newestFirst := []float64{}
	for rows.Next() {
		var price sql.NullFloat64
		if err := rows.Scan(&price); err != nil {
			return nil, err
		}
		newestFirst = append(newestFirst, price.Float64)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return reversed(newestFirst), nil
}

// TrimOldPriceTicks keeps only the newest MaxPriceRowsPerSymbol rows for symbol.
func (r *Repository) TrimOldPriceTicks(ctx context.Context, symbol string) error {
	_, err := r.db.ExecContext(ctx, `
		DELETE FROM market_snapshots
		WHERE symbol = ?
		AND id NOT IN (
			SELECT id FROM market_snapshots
			WHERE symbol = ?
			ORDER BY timestamp DESC, id DESC
			LIMIT ?
		)`, symbol, symbol, r.maxPriceRowsPerSymbol)
	if err != nil {
		return fmt.Errorf("trim price ticks %s: %w", symbol, err)
	}
	return nil
}

// SaveEquitySnapshot records the current account value.
func (r *Repository) SaveEquitySnapshot(ctx context.Context, totalEquity, cash, positionsValue float64) error {
	_, err := r.db.ExecContext(ctx,
		"INSERT INTO equity_snapshots (total_equity, cash, positions_value, timestamp) VALUES (?, ?, ?, ?)",
		totalEquity, cash, positionsValue, formatDBTime(time.Now()))
	if err != nil {
		return fmt.Errorf("save equity snapshot: %w", err)
	}
	return nil
}

// TrimOldEquitySnapshots deletes all but the newest keep snapshots.
// keep <= 0 uses MaxEquityRows.
func (r *Repository) TrimOldEquitySnapshots(ctx context.Context, keep int) error {
	_, err := r.db.ExecContext(ctx, `
		DELETE FROM equity_snapshots
		WHERE id NOT IN (
			SELECT id FROM equity_snapshots
			ORDER BY timestamp DESC, id DESC
			LIMIT ?
		)`, orDefault(keep, MaxEquityRows))
	if err != nil {
		return fmt.Errorf("trim equity snapshots: %w", err)
	}
	return nil
}

// GetEquityHistory returns up to limit newest equity snapshots, oldest first,
// for the equity chart. limit <= 0 uses DefaultEquityHistoryLimit.
func (r *Repository) GetEquityHistory(ctx context.Context, limit int) ([]EquityPoint, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT timestamp, total_equity FROM equity_snapshots ORDER BY timestamp DESC, id DESC LIMIT ?",
		orDefault(limit, DefaultEquityHistoryLimit))
	if err != nil {
		return nil, fmt.Errorf("get equity history: %w", err)
	}
	defer rows.Close()

	newestFirst := []EquityPoint{}
	for rows.Next() {
		var ts nullTime
		var equity sql.NullFloat64
		if err := rows.Scan(&ts, &equity); err != nil {
			return nil, err
		}
		newestFirst = append(newestFirst, EquityPoint{Timestamp: ts.iso(), Equity: equity.Float64})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return reversed(newestFirst), nil
}

// GetLatestEquity returns the newest total equity, or nil if none is stored.
func (r *Repository) GetLatestEquity(ctx context.Context) (*float64, error) {
	return r.latestEquityColumn(ctx, "total_equity")
}

// GetLatestCash returns the newest persisted cash balance, or nil if none is stored.
func (r *Repository) GetLatestCash(ctx context.Context) (*float64, error) {
	return r.latestEquityColumn(ctx, "cash")
}

// latestEquityColumn reads one column of the newest equity snapshot.
// column is always a package constant, never user input.
func (r *Repository) latestEquityColumn(ctx context.Context, column string) (*float64, error) {
	var v sql.NullFloat64
	err := r.db.QueryRowContext(ctx,
		"SELECT "+column+" FROM equity_snapshots ORDER BY timestamp DESC, id DESC LIMIT 1").Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get latest %s: %w", column, err)
	}
	return floatPtr(v), nil
}

// UpsertNewsItem stores a news item unless one with the same ID already exists.
// An empty URL or zero PublishedAt is stored as NULL.
func (r *Repository) UpsertNewsItem(ctx context.Context, item domain.NewsItem) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO news_items (id, source, title, content, published_at, url)
		VALUES (?, ?, ?, ?, ?, ?)`,
		item.ID, item.Source, item.Title, item.Content, dbTimeOrNil(item.PublishedAt), nullIfEmpty(item.URL))
	if err != nil {
		return fmt.Errorf("upsert news item %s: %w", item.ID, err)
	}
	return nil
}

// GetRecentNews returns the newest news items with a 200-character summary.
// limit <= 0 uses DefaultRecentNewsLimit.
func (r *Repository) GetRecentNews(ctx context.Context, limit int) ([]NewsEntry, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT id, source, title, url, content, published_at FROM news_items ORDER BY published_at DESC LIMIT ?",
		orDefault(limit, DefaultRecentNewsLimit))
	if err != nil {
		return nil, fmt.Errorf("get recent news: %w", err)
	}
	defer rows.Close()

	result := []NewsEntry{}
	for rows.Next() {
		var n NewsEntry
		var source, title, url, content sql.NullString
		var published nullTime
		if err := rows.Scan(&n.ID, &source, &title, &url, &content, &published); err != nil {
			return nil, err
		}
		n.Source, n.Title, n.URL = source.String, title.String, stringPtr(url)
		n.Summary = summarise(content.String, 200)
		n.PublishedAt = published.iso()
		result = append(result, n)
	}
	return result, rows.Err()
}

// summarise returns the first n characters of s with surrounding whitespace removed.
func summarise(s string, n int) string {
	runes := []rune(s)
	if len(runes) > n {
		runes = runes[:n]
	}
	return strings.TrimSpace(string(runes))
}

// reversed returns a reversed copy of s.
func reversed[T any](s []T) []T {
	out := make([]T, len(s))
	for i, v := range s {
		out[len(s)-1-i] = v
	}
	return out
}
