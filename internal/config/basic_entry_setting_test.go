package config

import "testing"

// TestBasicEntrySettings covers the settings for stricter basic entries, the
// volatility filter and the profit target.
func TestBasicEntrySettings(t *testing.T) {
	byKey := map[string]Field{}
	for _, f := range Fields() {
		byKey[f.Key] = f
	}
	keys := []string{"BASIC_ENTRY_VOTES", "BASIC_FRESH_SIGNAL", "BASIC_MIN_ATR_COST", "BASIC_TAKE_PROFIT_ATR"}

	t.Run("GIVEN the settings fields WHEN listed THEN each is a live Signals setting", func(t *testing.T) {
		for _, k := range keys {
			f, ok := byKey[k]
			if !ok || f.Group != GroupSignals || !f.Live {
				t.Fatalf("%s: %+v (found %v)", k, f, ok)
			}
		}
		if byKey["BASIC_FRESH_SIGNAL"].Kind != KindBool {
			t.Fatal("BASIC_FRESH_SIGNAL should be true/false")
		}
	})

	t.Run("GIVEN default settings WHEN applied to the fields THEN every default is accepted", func(t *testing.T) {
		d := Defaults()
		for _, k := range keys {
			s := Defaults()
			if err := byKey[k].Apply(&s, byKey[k].Value(d)); err != nil {
				t.Fatalf("default of %s rejected: %v", k, err)
			}
		}
	})

	t.Run("GIVEN a value outside its range WHEN applied THEN it is rejected", func(t *testing.T) {
		for k, v := range map[string]string{"BASIC_ENTRY_VOTES": "0", "BASIC_MIN_ATR_COST": "-1", "BASIC_TAKE_PROFIT_ATR": "-1"} {
			s := Defaults()
			if err := byKey[k].Apply(&s, v); err == nil {
				t.Fatalf("%s accepted %s", k, v)
			}
		}
	})

	t.Run("GIVEN 0 for the volatility filter and profit target WHEN applied THEN it is accepted: 0 switches them off", func(t *testing.T) {
		for _, k := range []string{"BASIC_MIN_ATR_COST", "BASIC_TAKE_PROFIT_ATR"} {
			s := Defaults()
			if err := byKey[k].Apply(&s, "0"); err != nil {
				t.Fatalf("%s rejected 0: %v", k, err)
			}
		}
	})
}
