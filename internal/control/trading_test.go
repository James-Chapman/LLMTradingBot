package control

import (
	"encoding/json"
	"testing"
)

// TestControlStateTradingSettings covers the trading mode and environment
// held in control state, seeded from the defaults and overridden by the web interface.
func TestControlStateTradingSettings(t *testing.T) {
	t.Run("GIVEN nothing stored WHEN seeded with default values THEN those values are used", func(t *testing.T) {
		state := NewState(nil)
		state.SetRepo(&fakeRepo{})
		_ = state.LoadFromDB()

		state.SeedTradingSettings("semi_automated", "paper")

		if state.TradingMode() != "semi_automated" || state.TradingEnvironment() != "paper" {
			t.Fatalf("seed values not used: %q / %q", state.TradingMode(), state.TradingEnvironment())
		}
	})

	t.Run("GIVEN values chosen in the web interface WHEN a new instance loads and is seeded with defaults THEN the stored values win", func(t *testing.T) {
		repo := &fakeRepo{stored: &PersistedState{TradingMode: "fully_automated", TradingEnvironment: "live"}}
		state := NewState(nil)
		state.SetRepo(repo)
		_ = state.LoadFromDB()

		state.SeedTradingSettings("manual", "paper")

		if state.TradingMode() != "fully_automated" || state.TradingEnvironment() != "live" {
			t.Fatalf("stored values must win over the defaults: %q / %q", state.TradingMode(), state.TradingEnvironment())
		}
	})

	t.Run("GIVEN a valid trading mode WHEN set THEN it is active and persisted", func(t *testing.T) {
		repo := &fakeRepo{}
		state := NewState(nil)
		state.SetRepo(repo)
		state.SeedTradingSettings("manual", "paper")

		err := state.SetTradingMode("fully_automated")

		if err != nil || state.TradingMode() != "fully_automated" {
			t.Fatalf("mode not set: err=%v mode=%q", err, state.TradingMode())
		}
		if repo.stored == nil || repo.stored.TradingMode != "fully_automated" || repo.stored.TradingEnvironment != "paper" {
			t.Fatalf("mode not persisted with the environment: %+v", repo.stored)
		}
	})

	t.Run("GIVEN an unknown trading mode or environment WHEN set THEN it is refused and nothing changes", func(t *testing.T) {
		repo := &fakeRepo{}
		state := NewState(nil)
		state.SetRepo(repo)
		state.SeedTradingSettings("manual", "paper")

		if err := state.SetTradingMode("yolo"); err == nil {
			t.Errorf("expected an error for an unknown mode")
		}
		if err := state.SetTradingEnvironment("testnet"); err == nil {
			t.Errorf("expected an error for an unknown environment")
		}
		if state.TradingMode() != "manual" || state.TradingEnvironment() != "paper" || repo.saves != 0 {
			t.Fatalf("refused changes must not alter or persist state: %q / %q, %d saves", state.TradingMode(), state.TradingEnvironment(), repo.saves)
		}
	})

	t.Run("GIVEN trading settings WHEN the snapshot is JSON encoded THEN the API shows mode and environment", func(t *testing.T) {
		state := NewState(nil)
		state.SeedTradingSettings("semi_automated", "live")

		raw, _ := json.Marshal(state.Snapshot())
		var got map[string]any
		_ = json.Unmarshal(raw, &got)

		if got["trading_mode"] != "semi_automated" || got["trading_environment"] != "live" {
			t.Fatalf("snapshot missing trading settings: %s", raw)
		}
	})
}
