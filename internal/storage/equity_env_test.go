package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestEquitySnapshotsPerEnvironment covers keeping paper and live equity
// snapshots apart so neither environment's values leak into the other.
func TestEquitySnapshotsPerEnvironment(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN paper and live snapshots WHEN the latest paper cash is read THEN the live balance is ignored", func(t *testing.T) {
		repo := newTestRepo(t)
		mustNoErr(t, repo.SaveEquitySnapshotFor(ctx, "paper", 480, 300, 180))
		mustNoErr(t, repo.SaveEquitySnapshotFor(ctx, "live", 9_000, 9_000, 0))

		paperCash, err := repo.GetLatestCashFor(ctx, "paper")
		mustNoErr(t, err)
		liveCash, err := repo.GetLatestCashFor(ctx, "live")
		mustNoErr(t, err)

		if paperCash == nil || *paperCash != 300 {
			t.Fatalf("paper cash should come from the paper row, got %v", paperCash)
		}
		if liveCash == nil || *liveCash != 9_000 {
			t.Fatalf("live cash should come from the live row, got %v", liveCash)
		}
	})

	t.Run("GIVEN interleaved paper and live snapshots WHEN the history of one environment is read THEN only its rows are returned", func(t *testing.T) {
		repo := newTestRepo(t)
		mustNoErr(t, repo.SaveEquitySnapshotFor(ctx, "paper", 500, 500, 0))
		mustNoErr(t, repo.SaveEquitySnapshotFor(ctx, "live", 9_000, 9_000, 0))
		mustNoErr(t, repo.SaveEquitySnapshotFor(ctx, "paper", 505, 400, 105))

		paperHistory, err := repo.GetEquityHistoryFor(ctx, "paper", 10)
		mustNoErr(t, err)
		liveHistory, err := repo.GetEquityHistoryFor(ctx, "live", 10)
		mustNoErr(t, err)

		if len(paperHistory) != 2 || paperHistory[0].Equity != 500 || paperHistory[1].Equity != 505 {
			t.Fatalf("paper history mixes environments: %+v", paperHistory)
		}
		if len(liveHistory) != 1 || liveHistory[0].Equity != 9_000 {
			t.Fatalf("live history mixes environments: %+v", liveHistory)
		}
	})

	t.Run("GIVEN no snapshots for an environment WHEN its latest cash is read THEN nil is returned", func(t *testing.T) {
		repo := newTestRepo(t)
		mustNoErr(t, repo.SaveEquitySnapshotFor(ctx, "live", 9_000, 9_000, 0))

		cash, err := repo.GetLatestCashFor(ctx, "paper")
		mustNoErr(t, err)
		if cash != nil {
			t.Fatalf("expected no paper cash, got %v", *cash)
		}
	})

	t.Run("GIVEN many live snapshots WHEN snapshots are trimmed THEN each environment keeps its own newest rows", func(t *testing.T) {
		repo := newTestRepo(t)
		mustNoErr(t, repo.SaveEquitySnapshotFor(ctx, "paper", 500, 500, 0))
		for i := 0; i < 5; i++ {
			mustNoErr(t, repo.SaveEquitySnapshotFor(ctx, "live", 9_000+float64(i), 9_000, 0))
		}

		mustNoErr(t, repo.TrimOldEquitySnapshots(ctx, 2))

		paperHistory, err := repo.GetEquityHistoryFor(ctx, "paper", 10)
		mustNoErr(t, err)
		liveHistory, err := repo.GetEquityHistoryFor(ctx, "live", 10)
		mustNoErr(t, err)
		if len(paperHistory) != 1 {
			t.Fatalf("the paper row must survive live trimming, got %+v", paperHistory)
		}
		if len(liveHistory) != 2 || liveHistory[1].Equity != 9_004 {
			t.Fatalf("expected the 2 newest live rows, got %+v", liveHistory)
		}
	})

	t.Run("GIVEN a database with snapshots from before environments were recorded WHEN opened THEN the old rows count as paper", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "legacy_equity.db")
		createLegacyDB(t, path,
			`CREATE TABLE equity_snapshots (
				id INTEGER NOT NULL, total_equity FLOAT, cash FLOAT, positions_value FLOAT,
				timestamp DATETIME, PRIMARY KEY (id))`,
			`INSERT INTO equity_snapshots (total_equity, cash, positions_value, timestamp)
			 VALUES (512.5, 312.5, 200, '2026-04-24 12:00:00.000000')`,
		)

		repo := openTestRepo(t, path)

		if !columnsOf(t, repo.db, "equity_snapshots")["environment"] {
			t.Fatal("equity_snapshots.environment was not added")
		}
		cash, err := repo.GetLatestCashFor(ctx, "paper")
		mustNoErr(t, err)
		if cash == nil || *cash != 312.5 {
			t.Fatalf("legacy snapshot should be read as paper, got %v", cash)
		}
		history, err := repo.GetEquityHistoryFor(ctx, "live", 10)
		mustNoErr(t, err)
		if len(history) != 0 {
			t.Fatalf("legacy snapshot must not appear as live, got %+v", history)
		}
	})
}

// TestPersistedPositionColumns covers the schema for a position's entry fee
// and a paper short's locked proceeds.
func TestPersistedPositionColumns(t *testing.T) {
	t.Run("GIVEN a fresh database WHEN opened THEN open_positions has entry_fee and short_proceeds", func(t *testing.T) {
		repo := newTestRepo(t)
		cols := columnsOf(t, repo.db, "open_positions")
		if !cols["entry_fee"] || !cols["short_proceeds"] {
			t.Fatalf("position fee columns missing: %v", cols)
		}
	})

	t.Run("GIVEN a database from before the fee columns WHEN opened THEN they are added and the old row is kept", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "legacy_fee.db")
		createLegacyDB(t, path,
			`CREATE TABLE open_positions (
				position_id TEXT PRIMARY KEY, market TEXT, size REAL, avg_price REAL,
				signal_confidence REAL, strategy_id TEXT, direction TEXT, trade_idea_id TEXT,
				unrealized_pnl REAL, opened_at DATETIME, updated_at DATETIME)`,
			`INSERT INTO open_positions (position_id, market, size, avg_price) VALUES ('p1', 'BTC/EUR', 1.0, 100.0)`,
		)

		repo := openTestRepo(t, path)

		cols := columnsOf(t, repo.db, "open_positions")
		if !cols["entry_fee"] || !cols["short_proceeds"] {
			t.Fatalf("position fee columns not added: %v", cols)
		}
		positions, err := repo.GetOpenPositions(context.Background())
		mustNoErr(t, err)
		if len(positions) != 1 || positions[0].PositionID != "p1" {
			t.Fatalf("legacy position not kept: %+v", positions)
		}
	})
}

// TestEmergencyStopTimestamp covers persisting when the emergency stop was pressed.
func TestEmergencyStopTimestamp(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN an active emergency stop with a timestamp WHEN saved and reloaded THEN the timestamp is restored", func(t *testing.T) {
		repo := newTestRepo(t)
		since := time.Date(2026, 11, 14, 9, 30, 0, 0, time.UTC)
		mustNoErr(t, repo.SaveControlState(ctx, ControlState{EmergencyStop: true, EmergencyStopSince: &since}))

		state, err := repo.LoadControlState(ctx)
		mustNoErr(t, err)
		if state == nil || state.EmergencyStopSince == nil || !state.EmergencyStopSince.Equal(since) {
			t.Fatalf("stop timestamp not restored: %+v", state)
		}
	})

	t.Run("GIVEN no emergency stop WHEN saved and reloaded THEN there is no timestamp", func(t *testing.T) {
		repo := newTestRepo(t)
		mustNoErr(t, repo.SaveControlState(ctx, ControlState{}))

		state, err := repo.LoadControlState(ctx)
		mustNoErr(t, err)
		if state == nil || state.EmergencyStopSince != nil {
			t.Fatalf("expected no timestamp, got %+v", state)
		}
	})
}

// TestTrimOldActivityOrder covers trimming the activity log when the clock
// has stepped backwards.
func TestTrimOldActivityOrder(t *testing.T) {
	t.Run("GIVEN newer rows stamped earlier after a clock step back WHEN the log is trimmed THEN the newest rows by insertion survive", func(t *testing.T) {
		repo := newTestRepo(t)
		ctx := context.Background()
		// Three rows written in order, but the clock went back an hour before the last two.
		for i, ts := range []string{"2026-04-24 12:00:00.000000", "2026-04-24 11:00:00.000000", "2026-04-24 11:00:01.000000"} {
			_, err := repo.db.Exec("INSERT INTO activity_log (level, message, detail, timestamp) VALUES ('info', ?, '', ?)",
				[]string{"oldest", "newer", "newest"}[i], ts)
			mustNoErr(t, err)
		}

		mustNoErr(t, repo.TrimOldActivity(ctx, 2))

		rows, err := repo.db.Query("SELECT message FROM activity_log ORDER BY id")
		mustNoErr(t, err)
		defer rows.Close()
		var kept []string
		for rows.Next() {
			var m string
			mustNoErr(t, rows.Scan(&m))
			kept = append(kept, m)
		}
		if len(kept) != 2 || kept[0] != "newer" || kept[1] != "newest" {
			t.Fatalf("expected the two newest rows, kept %v", kept)
		}
	})
}
