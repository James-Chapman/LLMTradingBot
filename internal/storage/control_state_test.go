package storage

import (
	"context"
	"path/filepath"
	"testing"
)

// TestControlStateTradingSettings covers persisting the trading mode and
// environment chosen in the web interface.
func TestControlStateTradingSettings(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN a trading mode and environment WHEN the control state is saved and reloaded THEN both are restored", func(t *testing.T) {
		repo := newTestRepo(t)
		mustNoErr(t, repo.SaveControlState(ctx, ControlState{TradingMode: "fully_automated", TradingEnvironment: "live"}))

		state, err := repo.LoadControlState(ctx)
		mustNoErr(t, err)

		if state == nil || state.TradingMode != "fully_automated" || state.TradingEnvironment != "live" {
			t.Fatalf("trading settings not restored: %+v", state)
		}
	})

	t.Run("GIVEN a control state saved without trading settings WHEN reloaded THEN they are empty so the defaults apply", func(t *testing.T) {
		repo := newTestRepo(t)
		mustNoErr(t, repo.SaveControlState(ctx, ControlState{EmergencyStop: true}))

		state, err := repo.LoadControlState(ctx)
		mustNoErr(t, err)

		if state == nil || state.TradingMode != "" || state.TradingEnvironment != "" {
			t.Fatalf("expected empty trading settings, got %+v", state)
		}
	})

	t.Run("GIVEN a database created before trading settings existed WHEN opened THEN the columns are added and old toggles kept", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "legacy.db")
		createLegacyDB(t, path,
			`CREATE TABLE control_state (
				id INTEGER PRIMARY KEY, emergency_stop INTEGER, disabled_markets JSON,
				disabled_strategies JSON, selected_strategy VARCHAR, live_markets JSON, updated_at DATETIME)`,
			`INSERT INTO control_state (id, emergency_stop, disabled_markets, disabled_strategies, live_markets)
			 VALUES (1, 0, '["ETH/EUR"]', '[]', '[]')`,
		)

		repo := openTestRepo(t, path)

		cols := columnsOf(t, repo.db, "control_state")
		if !cols["trading_mode"] || !cols["trading_environment"] {
			t.Fatalf("trading settings columns were not added: %v", cols)
		}
		state, err := repo.LoadControlState(ctx)
		mustNoErr(t, err)
		if state == nil || len(state.DisabledMarkets) != 1 || state.TradingMode != "" {
			t.Fatalf("legacy control state not readable after migration: %+v", state)
		}
	})
}
