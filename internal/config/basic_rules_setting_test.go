package config

import (
	"strconv"
	"testing"
)

// TestBasicRuleSettings covers the five settings that tune the exits and
// entries of the eight basic strategies.
func TestBasicRuleSettings(t *testing.T) {
	keys := []string{"BASIC_EXIT_VOTES", "BASIC_EXIT_CONFIRM_CANDLES", "BASIC_TREND_EMA", "BASIC_TRAIL_ATR", "BASIC_COOLDOWN_CANDLES"}
	byKey := map[string]Field{}
	for _, f := range Fields() {
		byKey[f.Key] = f
	}

	t.Run("GIVEN the settings fields WHEN listed THEN each basic rule setting is a live number in the Signals group", func(t *testing.T) {
		for _, k := range keys {
			f, ok := byKey[k]
			if !ok {
				t.Fatalf("%s is not a settings field", k)
			}
			if f.Group != GroupSignals || !f.Live || (f.Kind != KindInt && f.Kind != KindFloat) {
				t.Fatalf("field %+v", f)
			}
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
		if d.BasicExitVotes < 1 || d.BasicExitConfirmCandles < 1 {
			t.Fatalf("an exit needs at least one vote on at least one candle: %+v", d)
		}
	})

	t.Run("GIVEN a value outside its range WHEN applied THEN it is rejected and the setting is unchanged", func(t *testing.T) {
		bad := map[string]float64{"BASIC_EXIT_VOTES": 0, "BASIC_EXIT_CONFIRM_CANDLES": 0, "BASIC_TREND_EMA": -1, "BASIC_TRAIL_ATR": -0.5, "BASIC_COOLDOWN_CANDLES": -1}
		for k, v := range bad {
			s := Defaults()
			before := byKey[k].Value(s)
			if err := byKey[k].Apply(&s, strconv.FormatFloat(v, 'f', -1, 64)); err == nil {
				t.Fatalf("%s accepted %v", k, v)
			}
			if byKey[k].Value(s) != before {
				t.Fatalf("%s changed on a rejected value", k)
			}
		}
	})

	t.Run("GIVEN 0 for the trend EMA, trailing stop and cooldown WHEN applied THEN it is accepted: 0 switches that rule off", func(t *testing.T) {
		for _, k := range []string{"BASIC_TREND_EMA", "BASIC_TRAIL_ATR", "BASIC_COOLDOWN_CANDLES"} {
			s := Defaults()
			if err := byKey[k].Apply(&s, "0"); err != nil {
				t.Fatalf("%s rejected 0: %v", k, err)
			}
		}
	})
}
