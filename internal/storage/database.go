// Package storage persists the bot's state in SQLite.
//
// The schema is byte-for-byte compatible with the one the Python/SQLAlchemy
// app created, so an existing trading_bot.db opens and keeps working.
package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/James-Chapman/LLMTradingBot/internal/observability"

	_ "modernc.org/sqlite" // registers the pure-Go "sqlite" driver
)

// schemaVersion is the applied schema version recorded in schema_version.
const schemaVersion = 1

// connPragmas run on every new connection. foreign_keys stays OFF because
// SQLAlchemy never enabled it, and existing rows may rely on that.
const connPragmas = "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(0)"

var logger = observability.Logger("repository")

// createTableStatements reproduce SQLAlchemy's create_all() DDL for every model.
var createTableStatements = []string{
	`CREATE TABLE IF NOT EXISTS universe_snapshots (
	id INTEGER NOT NULL,
	fixed_markets JSON,
	dynamic_markets JSON,
	resolver_source VARCHAR,
	resolved_at DATETIME,
	mapping JSON,
	PRIMARY KEY (id)
)`,
	`CREATE TABLE IF NOT EXISTS news_items (
	id VARCHAR NOT NULL,
	source VARCHAR,
	title VARCHAR,
	content TEXT,
	published_at DATETIME,
	url VARCHAR,
	PRIMARY KEY (id)
)`,
	`CREATE TABLE IF NOT EXISTS news_signals (
	id INTEGER NOT NULL,
	news_item_id VARCHAR,
	asset_mentions JSON,
	headline_sentiment FLOAT,
	summary_sentiment FLOAT,
	event_type VARCHAR,
	event_severity FLOAT,
	confidence FLOAT,
	created_at DATETIME,
	PRIMARY KEY (id),
	FOREIGN KEY(news_item_id) REFERENCES news_items (id)
)`,
	`CREATE TABLE IF NOT EXISTS market_snapshots (
	id INTEGER NOT NULL,
	symbol VARCHAR,
	timestamp DATETIME,
	price FLOAT,
	volume FLOAT,
	PRIMARY KEY (id)
)`,
	`CREATE TABLE IF NOT EXISTS trade_ideas (
	id VARCHAR NOT NULL,
	strategy_id VARCHAR,
	market VARCHAR,
	direction VARCHAR,
	thesis TEXT,
	supporting_signals JSON,
	confidence FLOAT,
	entry_plan TEXT,
	exit_plan TEXT,
	stop_or_invalidation TEXT,
	position_sizing_proposal FLOAT,
	mode_eligibility JSON,
	created_at DATETIME,
	momentum_pct FLOAT,
	indicators JSON,
	llm_used BOOLEAN,
	llm_sentiment FLOAT,
	llm_confidence_scale FLOAT,
	llm_reasoning TEXT,
	news_context JSON,
	risk_approved BOOLEAN,
	risk_reason TEXT,
	PRIMARY KEY (id)
)`,
	`CREATE TABLE IF NOT EXISTS risk_decisions (
	id INTEGER NOT NULL,
	trade_idea_id VARCHAR,
	approved BOOLEAN,
	reason TEXT,
	adjusted_sizing FLOAT,
	timestamp DATETIME,
	PRIMARY KEY (id),
	FOREIGN KEY(trade_idea_id) REFERENCES trade_ideas (id)
)`,
	`CREATE TABLE IF NOT EXISTS approval_requests (
	id VARCHAR NOT NULL,
	trade_idea_id VARCHAR,
	risk_decision_id INTEGER,
	expires_at DATETIME,
	status VARCHAR,
	created_at DATETIME,
	PRIMARY KEY (id),
	FOREIGN KEY(trade_idea_id) REFERENCES trade_ideas (id),
	FOREIGN KEY(risk_decision_id) REFERENCES risk_decisions (id)
)`,
	`CREATE TABLE IF NOT EXISTS order_records (
	id VARCHAR NOT NULL,
	approval_request_id VARCHAR,
	market VARCHAR,
	direction VARCHAR,
	size FLOAT,
	price FLOAT,
	fee FLOAT,
	status VARCHAR,
	environment VARCHAR,
	timestamp DATETIME,
	exchange_order_id VARCHAR,
	position_id VARCHAR,
	pnl FLOAT,
	trade_idea_id VARCHAR,
	PRIMARY KEY (id)
)`,
	`CREATE TABLE IF NOT EXISTS fill_records (
	id INTEGER NOT NULL,
	order_id VARCHAR,
	fill_price FLOAT,
	fill_size FLOAT,
	fee FLOAT,
	timestamp DATETIME,
	PRIMARY KEY (id),
	FOREIGN KEY(order_id) REFERENCES order_records (id)
)`,
	`CREATE TABLE IF NOT EXISTS open_positions (
	position_id VARCHAR NOT NULL,
	market VARCHAR,
	size FLOAT,
	avg_price FLOAT,
	signal_confidence FLOAT,
	strategy_id VARCHAR,
	direction VARCHAR,
	trade_idea_id VARCHAR,
	unrealized_pnl FLOAT,
	opened_at DATETIME,
	updated_at DATETIME,
	PRIMARY KEY (position_id)
)`,
	`CREATE TABLE IF NOT EXISTS signal_outcomes (
	id INTEGER NOT NULL,
	strategy_id VARCHAR,
	market VARCHAR,
	direction VARCHAR,
	entry_price FLOAT,
	exit_price FLOAT,
	size FLOAT,
	pnl FLOAT,
	pnl_pct FLOAT,
	confidence_at_entry FLOAT,
	exit_reason VARCHAR,
	entry_at DATETIME,
	exit_at DATETIME,
	position_id VARCHAR,
	trade_idea_id VARCHAR,
	closing_trade_idea_id VARCHAR,
	PRIMARY KEY (id)
)`,
	`CREATE TABLE IF NOT EXISTS equity_snapshots (
	id INTEGER NOT NULL,
	total_equity FLOAT,
	cash FLOAT,
	positions_value FLOAT,
	timestamp DATETIME,
	PRIMARY KEY (id)
)`,
	`CREATE TABLE IF NOT EXISTS audit_logs (
	id INTEGER NOT NULL,
	event_type VARCHAR,
	details JSON,
	timestamp DATETIME,
	PRIMARY KEY (id)
)`,
	`CREATE TABLE IF NOT EXISTS activity_log (
	id INTEGER NOT NULL,
	level VARCHAR,
	message TEXT,
	detail TEXT,
	timestamp DATETIME,
	PRIMARY KEY (id)
)`,
	`CREATE TABLE IF NOT EXISTS risk_rejections (
	id INTEGER NOT NULL,
	market VARCHAR,
	direction VARCHAR,
	confidence FLOAT,
	thesis TEXT,
	reason TEXT,
	trade_idea_id VARCHAR,
	timestamp DATETIME,
	PRIMARY KEY (id)
)`,
	`CREATE TABLE IF NOT EXISTS control_state (
	id INTEGER NOT NULL,
	emergency_stop BOOLEAN,
	disabled_markets JSON,
	disabled_strategies JSON,
	selected_strategy VARCHAR,
	live_markets JSON,
	trading_mode VARCHAR,
	trading_environment VARCHAR,
	updated_at DATETIME,
	PRIMARY KEY (id)
)`,
	`CREATE TABLE IF NOT EXISTS settings (
	key VARCHAR NOT NULL,
	value TEXT NOT NULL,
	updated_at DATETIME,
	PRIMARY KEY (key)
)`,
	`CREATE TABLE IF NOT EXISTS llm_briefings (
	id INTEGER NOT NULL,
	key_insight TEXT,
	overall_sentiment FLOAT,
	market_outlooks JSON,
	article_count INTEGER,
	briefed_news_ids JSON,
	generated_at DATETIME,
	PRIMARY KEY (id)
)`,
	`CREATE TABLE IF NOT EXISTS llm_reflections (
	id INTEGER NOT NULL,
	pattern TEXT,
	suggestion TEXT,
	insight_confidence FLOAT,
	generated_at DATETIME,
	PRIMARY KEY (id)
)`,
	`CREATE TABLE IF NOT EXISTS risk_state (
	id INTEGER NOT NULL,
	daily_loss FLOAT,
	daily_start_equity FLOAT,
	last_reset_date VARCHAR,
	updated_at DATETIME,
	PRIMARY KEY (id)
)`,
	`CREATE TABLE IF NOT EXISTS rejected_trades (
	id INTEGER NOT NULL,
	market VARCHAR,
	direction VARCHAR,
	size FLOAT,
	price FLOAT,
	confidence FLOAT,
	reason TEXT,
	trade_idea_id VARCHAR,
	timestamp DATETIME,
	PRIMARY KEY (id)
)`,
}

// indexDef describes one SQLAlchemy-generated index.
type indexDef struct {
	name    string
	table   string
	columns []string
}

// indexes lists every index SQLAlchemy created (index=True and __table_args__).
var indexes = []indexDef{
	{"ix_market_snapshots_symbol_timestamp", "market_snapshots", []string{"symbol", "timestamp"}},
	{"ix_order_records_market", "order_records", []string{"market"}},
	{"ix_order_records_position_id", "order_records", []string{"position_id"}},
	{"ix_order_records_trade_idea_id", "order_records", []string{"trade_idea_id"}},
	{"ix_open_positions_market", "open_positions", []string{"market"}},
	{"ix_open_positions_trade_idea_id", "open_positions", []string{"trade_idea_id"}},
	{"ix_signal_outcomes_strategy_id", "signal_outcomes", []string{"strategy_id"}},
	{"ix_signal_outcomes_market", "signal_outcomes", []string{"market"}},
	{"ix_signal_outcomes_position_id", "signal_outcomes", []string{"position_id"}},
	{"ix_signal_outcomes_trade_idea_id", "signal_outcomes", []string{"trade_idea_id"}},
	{"ix_signal_outcomes_closing_trade_idea_id", "signal_outcomes", []string{"closing_trade_idea_id"}},
	{"ix_activity_log_timestamp", "activity_log", []string{"timestamp"}},
	{"ix_risk_rejections_trade_idea_id", "risk_rejections", []string{"trade_idea_id"}},
	{"ix_risk_rejections_timestamp", "risk_rejections", []string{"timestamp"}},
	{"ix_llm_briefings_generated_at", "llm_briefings", []string{"generated_at"}},
	{"ix_llm_reflections_generated_at", "llm_reflections", []string{"generated_at"}},
	{"ix_rejected_trades_trade_idea_id", "rejected_trades", []string{"trade_idea_id"}},
	{"ix_rejected_trades_timestamp", "rejected_trades", []string{"timestamp"}},
}

// columnDef is a column name plus the DDL used to ALTER it onto a legacy table.
type columnDef struct {
	name string
	ddl  string
}

// columnMigrations mirror the _add_missing_columns calls in database.py.
var columnMigrations = []struct {
	table   string
	columns []columnDef
}{
	{"order_records", []columnDef{{"position_id", "TEXT"}, {"pnl", "REAL"}, {"trade_idea_id", "TEXT"}}},
	{"trade_ideas", []columnDef{
		{"momentum_pct", "REAL"},
		{"indicators", "JSON"},
		{"llm_used", "INTEGER"},
		{"llm_sentiment", "REAL"},
		{"llm_confidence_scale", "REAL"},
		{"llm_reasoning", "TEXT"},
		{"news_context", "JSON"},
		{"risk_approved", "INTEGER"},
		{"risk_reason", "TEXT"},
	}},
	{"signal_outcomes", []columnDef{{"trade_idea_id", "TEXT"}, {"position_id", "TEXT"}, {"closing_trade_idea_id", "TEXT"}}},
	{"open_positions", []columnDef{{"trade_idea_id", "TEXT"}}},
	{"control_state", []columnDef{
		{"live_markets", "JSON"},
		{"selected_strategy", "TEXT DEFAULT 'unknown'"},
		{"trading_mode", "TEXT"},
		{"trading_environment", "TEXT"},
	}},
}

// Repository is the single gateway for all database reads and writes.
type Repository struct {
	db *sql.DB
	// maxPriceRowsPerSymbol caps market_snapshots per symbol (overridable in tests).
	maxPriceRowsPerSymbol int
}

// Open opens (creating if needed) the SQLite DB at path and migrates its schema.
func Open(path string) (*Repository, error) {
	db, err := sql.Open("sqlite", path+"?"+connPragmas)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// Mirror SQLAlchemy's StaticPool: one shared connection serialises access.
	db.SetMaxOpenConns(1)

	if err := migrate(context.Background(), db); err != nil {
		db.Close()
		return nil, err
	}
	return &Repository{db: db, maxPriceRowsPerSymbol: MaxPriceRowsPerSymbol}, nil
}

// Close releases the underlying database connection.
func (r *Repository) Close() error {
	return r.db.Close()
}

// migrate brings any existing schema up to date, mirroring init_database().
func migrate(ctx context.Context, db *sql.DB) error {
	if err := dropLegacyOpenPositions(ctx, db); err != nil {
		return err
	}
	for _, stmt := range createTableStatements {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create table: %w", err)
		}
	}
	for _, m := range columnMigrations {
		if err := addMissingColumns(ctx, db, m.table, m.columns); err != nil {
			return err
		}
	}
	if err := createIndexes(ctx, db); err != nil {
		return err
	}
	return recordSchemaVersion(ctx, db)
}

// dropLegacyOpenPositions drops open_positions if it still uses market as its PK.
func dropLegacyOpenPositions(ctx context.Context, db *sql.DB) error {
	cols, err := tableColumns(ctx, db, "open_positions")
	if err != nil {
		return err
	}
	if len(cols) == 0 || cols["position_id"] {
		return nil
	}
	logger.Warn("dropping legacy open_positions table (market-as-PK schema)")
	if _, err := db.ExecContext(ctx, "DROP TABLE open_positions"); err != nil {
		return fmt.Errorf("drop legacy open_positions: %w", err)
	}
	return nil
}

// addMissingColumns adds only the columns still missing from an existing table.
func addMissingColumns(ctx context.Context, db *sql.DB, table string, columns []columnDef) error {
	existing, err := tableColumns(ctx, db, table)
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		return nil // table does not exist
	}
	for _, c := range columns {
		if existing[c.name] {
			continue
		}
		stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, c.name, c.ddl)
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate %s.%s: %w", table, c.name, err)
		}
		logger.Info("added missing column", "table", table, "column", c.name)
	}
	return nil
}

// createIndexes creates every model index whose columns exist.
//
// SQLAlchemy only created indexes alongside new tables; creating them on
// legacy tables too is harmless and keeps queries fast.
func createIndexes(ctx context.Context, db *sql.DB) error {
	for _, idx := range indexes {
		cols, err := tableColumns(ctx, db, idx.table)
		if err != nil {
			return err
		}
		if !hasAll(cols, idx.columns) {
			continue // legacy table lacks a column; skip rather than fail
		}
		stmt := fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s (%s)", idx.name, idx.table, strings.Join(idx.columns, ", "))
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create index %s: %w", idx.name, err)
		}
	}
	return nil
}

// recordSchemaVersion stores the applied schema version for later migrations.
func recordSchemaVersion(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (
                id INTEGER PRIMARY KEY CHECK (id = 1),
                version INTEGER NOT NULL,
                updated_at TEXT NOT NULL
            )`)
	if err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}
	_, err = db.ExecContext(ctx,
		"INSERT OR REPLACE INTO schema_version (id, version, updated_at) VALUES (1, ?, CURRENT_TIMESTAMP)",
		schemaVersion)
	if err != nil {
		return fmt.Errorf("record schema version: %w", err)
	}
	return nil
}

// tableColumns returns the column names of table (empty when it does not exist).
func tableColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", table, err)
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

// hasAll reports whether every name in want is present in cols.
func hasAll(cols map[string]bool, want []string) bool {
	for _, c := range want {
		if !cols[c] {
			return false
		}
	}
	return true
}
