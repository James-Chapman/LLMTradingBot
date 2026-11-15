package config

import "testing"

// TestStrategyIntervalSetting covers the dashboard setting for the strategy tick length.
func TestStrategyIntervalSetting(t *testing.T) {
	t.Run("GIVEN no saved settings WHEN defaults are used THEN the strategy runs every 60 seconds", func(t *testing.T) {
		if got := Defaults().StrategyIntervalSeconds; got != 60 {
			t.Fatalf("default interval = %d s, want 60", got)
		}
	})

	t.Run("GIVEN the registry WHEN listed THEN the interval is a live whole-number setting in the signals group", func(t *testing.T) {
		f, ok := FieldByKey("STRATEGY_INTERVAL_SECONDS")

		if !ok || !f.Live || f.Kind != KindInt || f.Group != GroupSignals {
			t.Fatalf("expected a live int signals field, got %+v (found %v)", f, ok)
		}
	})

	t.Run("GIVEN values inside 30-600 seconds WHEN applied THEN they are stored", func(t *testing.T) {
		for _, raw := range []string{"30", "120", "600"} {
			s := Defaults()
			f, _ := FieldByKey("STRATEGY_INTERVAL_SECONDS")

			if err := f.Apply(&s, raw); err != nil || f.Value(s) != raw {
				t.Errorf("%s: got %d (err %v)", raw, s.StrategyIntervalSeconds, err)
			}
		}
	})

	t.Run("GIVEN values outside 30-600 seconds WHEN applied THEN they are refused and the interval is unchanged", func(t *testing.T) {
		for _, raw := range []string{"29", "601", "0", "-60", "1.5"} {
			s := Defaults()
			f, _ := FieldByKey("STRATEGY_INTERVAL_SECONDS")

			if err := f.Apply(&s, raw); err == nil || s.StrategyIntervalSeconds != 60 {
				t.Errorf("%s: expected a refusal, got %d (err %v)", raw, s.StrategyIntervalSeconds, err)
			}
		}
	})
}
