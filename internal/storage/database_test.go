package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// columnsOf returns the column names of a table in repo's DB.
func columnsOf(t *testing.T, db *sql.DB, table string) map[string]bool {
	t.Helper()
	cols, err := tableColumns(context.Background(), db, table)
	mustNoErr(t, err)
	return cols
}

// createLegacyDB writes a DB file whose tables pre-date the migration columns.
func createLegacyDB(t *testing.T, path string, statements ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	mustNoErr(t, err)
	defer db.Close()
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("legacy setup %q failed: %v", stmt, err)
		}
	}
}

// TestDatabaseMigration ports tests/test_database_migration_bdd.py.
func TestDatabaseMigration(t *testing.T) {
	t.Run("GIVEN a fresh database WHEN Open runs THEN all required columns are present", func(t *testing.T) {
		repo := newTestRepo(t)

		outcomeCols := columnsOf(t, repo.db, "signal_outcomes")
		posCols := columnsOf(t, repo.db, "open_positions")
		controlCols := columnsOf(t, repo.db, "control_state")
		orderCols := columnsOf(t, repo.db, "order_records")

		for _, c := range []string{"position_id", "closing_trade_idea_id", "trade_idea_id"} {
			if !outcomeCols[c] {
				t.Errorf("signal_outcomes.%s missing", c)
			}
		}
		if !posCols["trade_idea_id"] {
			t.Error("open_positions.trade_idea_id missing")
		}
		if !controlCols["selected_strategy"] || !controlCols["live_markets"] {
			t.Error("control_state migration columns missing")
		}
		if !orderCols["position_id"] || !orderCols["pnl"] {
			t.Error("order_records migration columns missing")
		}
	})

	t.Run("GIVEN a legacy database missing migration columns WHEN Open runs THEN columns are added and data is kept", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "legacy.db")
		createLegacyDB(t, path,
			`CREATE TABLE order_records (
				id VARCHAR NOT NULL, approval_request_id VARCHAR, market VARCHAR, direction VARCHAR,
				size FLOAT, price FLOAT, fee FLOAT, status VARCHAR, environment VARCHAR,
				timestamp DATETIME, exchange_order_id VARCHAR, PRIMARY KEY (id))`,
			`INSERT INTO order_records (id, market, direction, size, price, fee, status, environment, timestamp)
			 VALUES ('legacy-order', 'BTC/EUR', 'long', 0.1, 50000.0, 0.0, 'filled', 'paper', '2026-04-24 12:00:00.000000')`,
			`CREATE TABLE signal_outcomes (
				id INTEGER PRIMARY KEY, strategy_id TEXT, market TEXT, direction TEXT,
				entry_price REAL, exit_price REAL, size REAL, pnl REAL, pnl_pct REAL,
				confidence_at_entry REAL, exit_reason TEXT, entry_at DATETIME, exit_at DATETIME)`,
			`CREATE TABLE open_positions (
				position_id TEXT PRIMARY KEY, market TEXT, size REAL, avg_price REAL,
				signal_confidence REAL, strategy_id TEXT, direction TEXT, unrealized_pnl REAL,
				opened_at DATETIME, updated_at DATETIME)`,
			`CREATE TABLE control_state (
				id INTEGER PRIMARY KEY, emergency_stop INTEGER, disabled_markets JSON,
				disabled_strategies JSON, updated_at DATETIME)`,
			`INSERT INTO control_state (id, emergency_stop, disabled_markets, disabled_strategies)
			 VALUES (1, 1, '["ETH/EUR"]', '[]')`,
		)

		repo := openTestRepo(t, path)

		orderCols := columnsOf(t, repo.db, "order_records")
		for _, c := range []string{"position_id", "pnl", "trade_idea_id"} {
			if !orderCols[c] {
				t.Errorf("order_records.%s was not added", c)
			}
		}
		outcomeCols := columnsOf(t, repo.db, "signal_outcomes")
		for _, c := range []string{"position_id", "closing_trade_idea_id", "trade_idea_id"} {
			if !outcomeCols[c] {
				t.Errorf("signal_outcomes.%s was not added", c)
			}
		}
		if !columnsOf(t, repo.db, "open_positions")["trade_idea_id"] {
			t.Error("open_positions.trade_idea_id was not added")
		}

		ledger, err := repo.GetTradeLedger(context.Background(), 0)
		mustNoErr(t, err)
		if len(ledger) != 1 || ledger[0].Market != "BTC/EUR" {
			t.Fatalf("legacy order row not readable after migration: %+v", ledger)
		}

		state, err := repo.LoadControlState(context.Background())
		mustNoErr(t, err)
		if state == nil || !state.EmergencyStop || len(state.DisabledMarkets) != 1 {
			t.Fatalf("legacy control state not preserved: %+v", state)
		}
		if state.SelectedStrategy != "unknown" {
			t.Errorf("legacy control_state should default selected_strategy to 'unknown', got %q", state.SelectedStrategy)
		}
	})

	t.Run("GIVEN open_positions uses the old market-as-PK schema WHEN Open runs THEN it is rebuilt with position_id", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "legacy_positions.db")
		createLegacyDB(t, path,
			`CREATE TABLE open_positions (market TEXT PRIMARY KEY, size REAL, avg_price REAL)`,
			`INSERT INTO open_positions (market, size, avg_price) VALUES ('BTC/EUR', 1.0, 100.0)`,
		)

		repo := openTestRepo(t, path)

		cols := columnsOf(t, repo.db, "open_positions")
		if !cols["position_id"] || !cols["trade_idea_id"] {
			t.Fatalf("open_positions not rebuilt: %v", cols)
		}
		positions, err := repo.GetOpenPositions(context.Background())
		mustNoErr(t, err)
		if len(positions) != 0 {
			t.Errorf("legacy open_positions rows should be dropped, got %d", len(positions))
		}
	})

	t.Run("GIVEN an initialised database WHEN Open is called again THEN no error is raised", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "twice.db")
		first, err := Open(path)
		mustNoErr(t, err)
		mustNoErr(t, first.Close())

		openTestRepo(t, path)
	})

	t.Run("GIVEN the database is initialised WHEN the schema is inspected THEN the schema version is recorded", func(t *testing.T) {
		repo := newTestRepo(t)

		var version int
		var updatedAt string
		err := repo.db.QueryRow("SELECT version, updated_at FROM schema_version WHERE id = 1").Scan(&version, &updatedAt)
		mustNoErr(t, err)
		if version < 1 {
			t.Errorf("schema version should be >= 1, got %d", version)
		}
		if updatedAt == "" {
			t.Error("schema_version.updated_at should be set")
		}
	})

	t.Run("GIVEN a fresh database WHEN Open runs THEN indexes match the SQLAlchemy schema", func(t *testing.T) {
		repo := newTestRepo(t)

		var count int
		err := repo.db.QueryRow(
			"SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name IN (?, ?, ?)",
			"ix_market_snapshots_symbol_timestamp", "ix_order_records_position_id", "ix_activity_log_timestamp",
		).Scan(&count)
		mustNoErr(t, err)
		if count != 3 {
			t.Errorf("expected 3 named indexes, found %d", count)
		}
	})

	t.Run("GIVEN an opened database WHEN pragmas are read THEN WAL is on and foreign keys stay off like SQLAlchemy", func(t *testing.T) {
		repo := newTestRepo(t)

		var mode string
		var fk int
		mustNoErr(t, repo.db.QueryRow("PRAGMA journal_mode").Scan(&mode))
		mustNoErr(t, repo.db.QueryRow("PRAGMA foreign_keys").Scan(&fk))
		if !strings.EqualFold(mode, "wal") {
			t.Errorf("journal_mode should be wal, got %q", mode)
		}
		if fk != 0 {
			t.Errorf("foreign_keys should be off, got %d", fk)
		}
	})
}
