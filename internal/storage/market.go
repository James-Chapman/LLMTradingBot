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

// EnvironmentPaper is the environment label of paper-trading equity snapshots.
const EnvironmentPaper = "paper"

// equityEnvironment returns the environment an equity snapshot belongs to;
// "" means paper, the environment every snapshot had before it was recorded.
func equityEnvironment(environment string) string {
	if environment == "" {
		return EnvironmentPaper
	}
	return environment
}

// SaveEquitySnapshot records a paper account value (see SaveEquitySnapshotFor).
func (r *Repository) SaveEquitySnapshot(ctx context.Context, totalEquity, cash, positionsValue float64) error {
	return r.SaveEquitySnapshotFor(ctx, EnvironmentPaper, totalEquity, cash, positionsValue)
}

// SaveEquitySnapshotFor records the current account value of one trading
// environment ("paper" or "live"), so the two series never mix.
func (r *Repository) SaveEquitySnapshotFor(ctx context.Context, environment string, totalEquity, cash, positionsValue float64) error {
	_, err := r.db.ExecContext(ctx,
		"INSERT INTO equity_snapshots (total_equity, cash, positions_value, timestamp, environment) VALUES (?, ?, ?, ?, ?)",
		totalEquity, cash, positionsValue, formatDBTime(time.Now()), equityEnvironment(environment))
	if err != nil {
		return fmt.Errorf("save equity snapshot: %w", err)
	}
	return nil
}

// TrimOldEquitySnapshots deletes all but the newest keep snapshots of each
// environment, so a long live session cannot push out the paper history.
// keep <= 0 uses MaxEquityRows.
func (r *Repository) TrimOldEquitySnapshots(ctx context.Context, keep int) error {
	_, err := r.db.ExecContext(ctx, `
		DELETE FROM equity_snapshots
		WHERE id IN (
			SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (
					PARTITION BY COALESCE(environment, 'paper')
					ORDER BY timestamp DESC, id DESC
				) AS newest_rank
				FROM equity_snapshots
			)
			WHERE newest_rank > ?
		)`, orDefault(keep, MaxEquityRows))
	if err != nil {
		return fmt.Errorf("trim equity snapshots: %w", err)
	}
	return nil
}

// GetEquityHistory returns the paper equity history (see GetEquityHistoryFor).
func (r *Repository) GetEquityHistory(ctx context.Context, limit int) ([]EquityPoint, error) {
	return r.GetEquityHistoryFor(ctx, EnvironmentPaper, limit)
}

// GetEquityHistoryFor returns up to limit newest equity snapshots of one
// environment, oldest first, for the equity chart. limit <= 0 uses
// DefaultEquityHistoryLimit.
func (r *Repository) GetEquityHistoryFor(ctx context.Context, environment string, limit int) ([]EquityPoint, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT timestamp, total_equity FROM equity_snapshots
		WHERE COALESCE(environment, 'paper') = ?
		ORDER BY timestamp DESC, id DESC LIMIT ?`,
		equityEnvironment(environment), orDefault(limit, DefaultEquityHistoryLimit))
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

// GetLatestEquity returns the newest paper total equity, or nil if none is stored.
func (r *Repository) GetLatestEquity(ctx context.Context) (*float64, error) {
	return r.latestEquityColumn(ctx, EnvironmentPaper, "total_equity")
}

// GetLatestCash returns the newest persisted paper cash balance, or nil if none is stored.
func (r *Repository) GetLatestCash(ctx context.Context) (*float64, error) {
	return r.GetLatestCashFor(ctx, EnvironmentPaper)
}

// GetLatestCashFor returns the newest cash balance of one environment, or nil
// if that environment has no snapshot.
func (r *Repository) GetLatestCashFor(ctx context.Context, environment string) (*float64, error) {
	return r.latestEquityColumn(ctx, environment, "cash")
}

// latestEquityColumn reads one column of an environment's newest equity
// snapshot. column is always a package constant, never user input.
func (r *Repository) latestEquityColumn(ctx context.Context, environment, column string) (*float64, error) {
	var v sql.NullFloat64
	err := r.db.QueryRowContext(ctx,
		"SELECT "+column+" FROM equity_snapshots WHERE COALESCE(environment, 'paper') = ? ORDER BY timestamp DESC, id DESC LIMIT 1",
		equityEnvironment(environment)).Scan(&v)
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
