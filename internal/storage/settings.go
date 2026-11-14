package storage

import (
	"context"
	"fmt"
	"time"
)

// LoadSettings returns every stored dashboard setting as key → text value.
// Secret values are returned still sealed.
func (r *Repository) LoadSettings(ctx context.Context) (map[string]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}
	defer rows.Close()
	values := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("load settings: %w", err)
		}
		values[key] = value
	}
	return values, rows.Err()
}

// SaveSettings upserts the given settings in one transaction, so a save is
// all-or-nothing. Keys not given are left unchanged.
func (r *Repository) SaveSettings(ctx context.Context, values map[string]string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit
	now := formatDBTime(time.Now())
	for key, value := range values {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			key, value, now); err != nil {
			return fmt.Errorf("save setting %s: %w", key, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	return nil
}
