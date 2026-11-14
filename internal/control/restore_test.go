package control

import (
	"testing"
	"time"
)

// TestControlStateRestore covers reloading the control state after a restart.
func TestControlStateRestore(t *testing.T) {
	t.Run("GIVEN the emergency stop was pressed WHEN the bot restarts THEN the stop and its timestamp are restored", func(t *testing.T) {
		repo := &fakeRepo{}
		before := NewState(fixedNow)
		before.SetRepo(repo)
		before.ActivateStop()

		after := NewState(func() time.Time { return fixedNow().Add(time.Hour) })
		after.SetRepo(repo)
		if err := after.LoadFromDB(); err != nil {
			t.Fatalf("load: %v", err)
		}

		snap := after.Snapshot()
		if !snap.EmergencyStop || snap.StopSince == nil || !snap.StopSince.Equal(fixedNow()) {
			t.Fatalf("stop timestamp not restored: %+v", snap)
		}
	})

	t.Run("GIVEN a stored stop without a timestamp WHEN restored THEN the load time is used so it is never null", func(t *testing.T) {
		loadTime := fixedNow().Add(time.Hour)
		repo := &fakeRepo{stored: &PersistedState{EmergencyStop: true}}
		state := NewState(func() time.Time { return loadTime })
		state.SetRepo(repo)
		if err := state.LoadFromDB(); err != nil {
			t.Fatalf("load: %v", err)
		}

		if since := state.Snapshot().StopSince; since == nil || !since.Equal(loadTime) {
			t.Fatalf("expected the load time, got %v", since)
		}
	})

	t.Run("GIVEN the stop is resumed WHEN the state is saved THEN no timestamp is stored", func(t *testing.T) {
		repo := &fakeRepo{}
		state := NewState(fixedNow)
		state.SetRepo(repo)
		state.ActivateStop()
		state.Resume()

		if repo.stored.EmergencyStop || repo.stored.StopSince != nil {
			t.Fatalf("resumed state still carries a stop: %+v", repo.stored)
		}
	})

	t.Run("GIVEN an invalid stored trading mode WHEN restored THEN the mode falls back to manual", func(t *testing.T) {
		repo := &fakeRepo{stored: &PersistedState{TradingMode: "yolo", TradingEnvironment: "paper"}}
		state := NewState(fixedNow)
		state.SetRepo(repo)
		if err := state.LoadFromDB(); err != nil {
			t.Fatalf("load: %v", err)
		}

		if got := state.TradingMode(); got != "manual" {
			t.Fatalf("mode %q, want manual", got)
		}
	})

	t.Run("GIVEN an invalid stored environment WHEN restored THEN the environment falls back to paper", func(t *testing.T) {
		repo := &fakeRepo{stored: &PersistedState{TradingMode: "manual", TradingEnvironment: "LIVE!"}}
		state := NewState(fixedNow)
		state.SetRepo(repo)
		if err := state.LoadFromDB(); err != nil {
			t.Fatalf("load: %v", err)
		}

		if got := state.TradingEnvironment(); got != "paper" {
			t.Fatalf("environment %q, want paper", got)
		}
	})

	t.Run("GIVEN no stored trading settings WHEN restored THEN they stay empty so the configured defaults are seeded", func(t *testing.T) {
		repo := &fakeRepo{stored: &PersistedState{}}
		state := NewState(fixedNow)
		state.SetRepo(repo)
		if err := state.LoadFromDB(); err != nil {
			t.Fatalf("load: %v", err)
		}
		state.SeedTradingSettings("semi_automated", "paper")

		if state.TradingMode() != "semi_automated" {
			t.Fatalf("mode %q, want the seeded default", state.TradingMode())
		}
	})
}
