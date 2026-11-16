package control

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// fakeRepo is an in-memory control-state repository.
type fakeRepo struct {
	stored  *PersistedState
	saves   int
	loadErr error
}

// LoadControlState returns the stored state, if any.
func (f *fakeRepo) LoadControlState() (PersistedState, bool, error) {
	if f.loadErr != nil {
		return PersistedState{}, false, f.loadErr
	}
	if f.stored == nil {
		return PersistedState{}, false, nil
	}
	return *f.stored, true, nil
}

// SaveControlState stores a copy of the state.
func (f *fakeRepo) SaveControlState(state PersistedState) error {
	f.saves++
	f.stored = &state
	return nil
}

// fixedNow is a deterministic clock.
func fixedNow() time.Time { return time.Date(2026, 11, 11, 9, 30, 0, 0, time.UTC) }

// TestControlStateStrategySelection covers selecting strategies and legacy id mapping.
func TestControlStateStrategySelection(t *testing.T) {
	t.Run("GIVEN a new control state WHEN the snapshot is requested THEN the combined strategy is selected", func(t *testing.T) {
		state := NewState(nil)

		if got := state.Snapshot().SelectedStrategy; got != "basic_and_llm_strategy_1h" {
			t.Fatalf("selected %q, want basic_and_llm_strategy_1h", got)
		}
	})

	t.Run("GIVEN a strategy is selected WHEN queried THEN only that strategy is active", func(t *testing.T) {
		state := NewState(nil)

		state.SelectStrategy("llm")

		if state.SelectedStrategyID() != "llm" || !state.IsStrategySelected("llm") || state.IsStrategySelected("combined") {
			t.Fatalf("unexpected selection %q", state.SelectedStrategyID())
		}
		if state.Snapshot().SelectedStrategy != "llm" {
			t.Fatal("snapshot does not reflect selection")
		}
	})

	t.Run("GIVEN a legacy strategy id WHEN selected THEN the canonical id is stored and still matches", func(t *testing.T) {
		state := NewState(nil)

		state.SelectStrategy("llm_only")

		if state.SelectedStrategyID() != "llm_only_strategy" {
			t.Fatalf("selected %q, want llm_only_strategy", state.SelectedStrategyID())
		}
		if !state.IsStrategySelected("llm_only") || !state.IsStrategySelected("llm_only_strategy") {
			t.Fatal("legacy and canonical ids must both match the selection")
		}
	})

	t.Run("GIVEN a strategy is disabled WHEN checked THEN it reports disabled until re-enabled", func(t *testing.T) {
		state := NewState(nil)

		state.DisableStrategy("basic_strategy")
		disabled := !state.IsStrategyEnabled("basic_strategy")
		state.EnableStrategy("basic_strategy")

		if !disabled || !state.IsStrategyEnabled("basic_strategy") {
			t.Fatal("strategy toggle did not round-trip")
		}
	})
}

// TestControlStateToggles covers emergency stop and market/strategy toggles.
func TestControlStateToggles(t *testing.T) {
	t.Run("GIVEN an emergency stop WHEN activated and resumed THEN the flag and timestamp follow", func(t *testing.T) {
		state := NewState(fixedNow)

		state.ActivateStop()
		active := state.Snapshot()
		state.Resume()
		resumed := state.Snapshot()

		if !active.EmergencyStop || active.StopSince == nil || !active.StopSince.Equal(fixedNow()) {
			t.Fatalf("expected active stop with timestamp, got %+v", active)
		}
		if resumed.EmergencyStop || resumed.StopSince != nil || state.EmergencyStop() {
			t.Fatalf("expected cleared stop, got %+v", resumed)
		}
	})

	t.Run("GIVEN a market is disabled WHEN checked THEN it is not enabled until re-enabled", func(t *testing.T) {
		state := NewState(nil)

		state.DisableMarket("ETH/EUR")
		disabled := !state.IsMarketEnabled("ETH/EUR")
		state.EnableMarket("ETH/EUR")

		if !disabled || !state.IsMarketEnabled("ETH/EUR") {
			t.Fatal("market toggle did not round-trip")
		}
	})

	t.Run("GIVEN a market is set live WHEN checked THEN it routes live until unset", func(t *testing.T) {
		state := NewState(nil)

		state.SetMarketLive("BTC/EUR", true)
		live := state.IsMarketLive("BTC/EUR")
		state.SetMarketLive("BTC/EUR", false)

		if !live || state.IsMarketLive("BTC/EUR") {
			t.Fatal("live toggle did not round-trip")
		}
	})

	t.Run("GIVEN a fresh state WHEN the snapshot is JSON encoded THEN keys match the API and lists are empty arrays", func(t *testing.T) {
		raw, err := json.Marshal(NewState(nil).Snapshot())
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		_ = json.Unmarshal(raw, &decoded)

		for _, key := range []string{"emergency_stop", "stop_since", "disabled_markets", "disabled_strategies", "selected_strategy", "live_markets"} {
			if _, ok := decoded[key]; !ok {
				t.Fatalf("missing key %q in %s", key, raw)
			}
		}
		if decoded["stop_since"] != nil {
			t.Fatalf("stop_since must be null when not stopped, got %v", decoded["stop_since"])
		}
		if list, ok := decoded["disabled_markets"].([]any); !ok || len(list) != 0 {
			t.Fatalf("disabled_markets must be an empty array, got %v", decoded["disabled_markets"])
		}
	})

	t.Run("GIVEN several disabled markets WHEN snapshotted THEN they are sorted", func(t *testing.T) {
		state := NewState(nil)
		state.DisableMarket("SOL/EUR")
		state.DisableMarket("ADA/EUR")

		got := state.Snapshot().DisabledMarkets

		if len(got) != 2 || got[0] != "ADA/EUR" || got[1] != "SOL/EUR" {
			t.Fatalf("unexpected order %v", got)
		}
	})
}

// TestControlStatePersistence covers saving and restoring operator toggles.
func TestControlStatePersistence(t *testing.T) {
	t.Run("GIVEN a repository WHEN a toggle changes THEN the full state is saved", func(t *testing.T) {
		repo := &fakeRepo{}
		state := NewState(nil)
		state.SetRepo(repo)

		state.DisableMarket("ETH/EUR")
		state.ActivateStop()

		if repo.saves != 2 || !repo.stored.EmergencyStop || len(repo.stored.DisabledMarkets) != 1 {
			t.Fatalf("unexpected persisted state %+v after %d saves", repo.stored, repo.saves)
		}
	})

	t.Run("GIVEN a saved state WHEN a new instance loads from the DB THEN toggles are restored", func(t *testing.T) {
		repo := &fakeRepo{stored: &PersistedState{
			EmergencyStop:      true,
			DisabledMarkets:    []string{"ETH/EUR"},
			DisabledStrategies: []string{"basic_strategy"},
			LiveMarkets:        []string{"BTC/EUR"},
			SelectedStrategy:   "basic_trend",
		}}
		state := NewState(nil)
		state.SetRepo(repo)

		err := state.LoadFromDB()

		if err != nil {
			t.Fatal(err)
		}
		if !state.EmergencyStop() || state.IsMarketEnabled("ETH/EUR") || state.IsStrategyEnabled("basic_strategy") || !state.IsMarketLive("BTC/EUR") {
			t.Fatalf("toggles not restored: %+v", state.Snapshot())
		}
		if state.SelectedStrategyID() != "basic_strategy_1h" {
			t.Fatalf("legacy selection must map to canonical id, got %q", state.SelectedStrategyID())
		}
	})

	t.Run("GIVEN a saved selection of the tick-based basic strategies WHEN loaded THEN it maps to their 1-hour candle versions", func(t *testing.T) {
		for old, want := range map[string]string{"basic_strategy": "basic_strategy_1h", "basic_and_llm_strategy": "basic_and_llm_strategy_1h"} {
			state := NewState(nil)
			state.SetRepo(&fakeRepo{stored: &PersistedState{SelectedStrategy: old}})
			if err := state.LoadFromDB(); err != nil {
				t.Fatal(err)
			}
			if got := state.SelectedStrategyID(); got != want {
				t.Fatalf("saved %q loaded as %q, want %q", old, got, want)
			}
		}
	})

	t.Run("GIVEN a saved state with no selected strategy WHEN loaded THEN the default is used", func(t *testing.T) {
		state := NewState(nil)
		state.SetRepo(&fakeRepo{stored: &PersistedState{}})

		_ = state.LoadFromDB()

		if state.SelectedStrategyID() != DefaultSelectedStrategy {
			t.Fatalf("selected %q, want default", state.SelectedStrategyID())
		}
	})

	t.Run("GIVEN a failing repository WHEN loading THEN defaults are kept and the error returned", func(t *testing.T) {
		state := NewState(nil)
		state.SetRepo(&fakeRepo{loadErr: errors.New("db down")})

		err := state.LoadFromDB()

		if err == nil || state.EmergencyStop() || state.SelectedStrategyID() != DefaultSelectedStrategy {
			t.Fatalf("expected error and untouched defaults, got %v / %+v", err, state.Snapshot())
		}
	})

	t.Run("GIVEN no repository WHEN loading THEN it is a no-op", func(t *testing.T) {
		if err := NewState(nil).LoadFromDB(); err != nil {
			t.Fatal(err)
		}
	})
}
