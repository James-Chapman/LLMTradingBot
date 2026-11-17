package config

import (
	"strings"
	"testing"
)

// TestSettings covers the built-in defaults and validation.
func TestSettings(t *testing.T) {
	t.Run("GIVEN no saved settings WHEN defaults are used THEN safe paper/manual defaults apply", func(t *testing.T) {
		s := Defaults()
		if s.TradingEnvironment != "paper" || s.TradingMode != "manual" {
			t.Fatalf("expected paper/manual defaults, got %s/%s", s.TradingEnvironment, s.TradingMode)
		}
		if s.LMStudioBaseURL == "" || s.Port == 0 || s.DatabasePath == "" {
			t.Fatalf("expected usable defaults, got %+v", s)
		}
	})

	t.Run("GIVEN an invalid trading mode WHEN validated THEN an error is returned", func(t *testing.T) {
		s := Defaults()
		s.TradingMode = "yolo"
		if err := s.Validate(); err == nil {
			t.Fatal("expected a validation error")
		}
	})
}

// TestSettingsFields covers the registry of dashboard-managed settings.
func TestSettingsFields(t *testing.T) {
	t.Run("GIVEN the registry WHEN listed THEN every key is unique and has a label, group and kind", func(t *testing.T) {
		seen := map[string]bool{}
		for _, f := range Fields() {
			if seen[f.Key] {
				t.Errorf("duplicate key %s", f.Key)
			}
			seen[f.Key] = true
			if f.Label == "" || f.Group == "" || f.Kind == "" {
				t.Errorf("%s: missing label, group or kind: %+v", f.Key, f)
			}
		}
		if len(seen) < 30 {
			t.Fatalf("expected the full settings list, got %d fields", len(seen))
		}
	})

	t.Run("GIVEN start-up flags and trading-dialog settings WHEN the registry is listed THEN they are not dashboard fields", func(t *testing.T) {
		for _, key := range []string{"HOST", "PORT", "DATABASE_PATH", "LOG_FILE", "TRADING_MODE", "TRADING_ENVIRONMENT"} {
			if _, ok := FieldByKey(key); ok {
				t.Errorf("%s must not be a dashboard setting", key)
			}
		}
	})

	t.Run("GIVEN API keys WHEN the registry is listed THEN they are marked secret and need a restart", func(t *testing.T) {
		for _, key := range []string{"KRAKEN_API_KEY", "KRAKEN_API_SECRET", "LMSTUDIO_API_KEY", "OPENAI_API_KEY"} {
			f, ok := FieldByKey(key)
			if !ok || !f.Secret || f.Live {
				t.Errorf("%s: expected a secret, restart-required field, got %+v (found %v)", key, f, ok)
			}
		}
	})

	t.Run("GIVEN risk and signal thresholds WHEN the registry is listed THEN they apply live; connections need a restart", func(t *testing.T) {
		for _, key := range []string{"MIN_SIGNAL_CONFIDENCE", "MAX_DAILY_LOSS_PERCENT", "STOP_LOSS_PCT", "LLM_VETO_THRESHOLD", "ALERT_WEBHOOK_URL"} {
			if f, ok := FieldByKey(key); !ok || !f.Live {
				t.Errorf("%s should apply live", key)
			}
		}
		for _, key := range []string{"FIXED_MARKETS", "LMSTUDIO_BASE_URL", "KRAKEN_REST_URL", "NEWS_SOURCES", "BASE_CURRENCY"} {
			if f, ok := FieldByKey(key); !ok || f.Live {
				t.Errorf("%s should need a restart", key)
			}
		}
	})

	t.Run("GIVEN the LLM weight setting WHEN it is edited THEN it applies live and only accepts 0 to 1", func(t *testing.T) {
		f, ok := FieldByKey("LLM_WEIGHT")
		if !ok || !f.Live || f.Group != GroupSignals {
			t.Fatalf("LLM_WEIGHT: expected a live signals setting, got %+v (found %v)", f, ok)
		}
		s := Defaults()
		if err := f.Apply(&s, "0.25"); err != nil || s.LLMWeight != 0.25 {
			t.Fatalf("0.25: err %v, weight %v", err, s.LLMWeight)
		}
		for _, bad := range []string{"-0.1", "1.5"} {
			if err := f.Apply(&s, bad); err == nil {
				t.Errorf("%s was accepted, want it refused", bad)
			}
		}
	})

	t.Run("GIVEN valid text for each kind WHEN applied THEN the setting changes and reads back the same", func(t *testing.T) {
		cases := map[string]string{
			"MIN_SIGNAL_CONFIDENCE":   "0.8",
			"MOMENTUM_LOOKBACK_TICKS": "12",
			"DEBUG":                   "true",
			"FIXED_MARKETS":           `["BTC/EUR","SOL/EUR"]`,
			"LMSTUDIO_MODEL":          "qwen3-8b",
		}
		for key, raw := range cases {
			f, _ := FieldByKey(key)
			s := Defaults()
			if err := f.Apply(&s, raw); err != nil {
				t.Fatalf("%s: %v", key, err)
			}
			if got := f.Value(s); got != raw {
				t.Errorf("%s: read back %q, want %q", key, got, raw)
			}
		}
	})

	t.Run("GIVEN invalid text WHEN applied THEN it is refused with the key named and the setting unchanged", func(t *testing.T) {
		cases := map[string]string{
			"MIN_SIGNAL_CONFIDENCE":   "high",
			"MOMENTUM_LOOKBACK_TICKS": "1.5",
			"DEBUG":                   "maybe",
			"FIXED_MARKETS":           "BTC/EUR",
			"MAX_DAILY_LOSS_PERCENT":  "-1",
			"LMSTUDIO_BASE_URL":       "localhost:1234",
			"LOG_LEVEL":               "LOUD",
		}
		for key, raw := range cases {
			f, _ := FieldByKey(key)
			s := Defaults()
			before := f.Value(s)
			err := f.Apply(&s, raw)
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Errorf("%s=%q: expected an error naming the key, got %v", key, raw, err)
			}
			if f.Value(s) != before {
				t.Errorf("%s: a refused value must not change the setting", key)
			}
		}
	})

	t.Run("GIVEN any decimal setting WHEN a non-finite number is applied THEN it is refused and the setting unchanged", func(t *testing.T) {
		// NaN compares false against every bound, so it would slip past the
		// range checks and switch off the risk gates that read the value.
		for _, f := range Fields() {
			if f.Kind != KindFloat {
				continue
			}
			for _, raw := range []string{"NaN", "Inf", "-Inf", "+Infinity"} {
				s := Defaults()
				before := f.Value(s)
				if err := f.Apply(&s, raw); err == nil {
					t.Errorf("%s=%q: expected a non-finite number to be refused", f.Key, raw)
				}
				if f.Value(s) != before {
					t.Errorf("%s=%q: a refused value must not change the setting", f.Key, raw)
				}
			}
		}
	})
}

// TestDefaultMarkets covers the built-in market list used when Kraken cannot be asked.
func TestDefaultMarkets(t *testing.T) {
	t.Run("GIVEN the defaults WHEN the market list is read THEN it holds the 50 biggest EUR pairs without stablecoins", func(t *testing.T) {
		markets := Defaults().FixedMarkets
		if len(markets) != DefaultMarketCount || DefaultMarketCount != 50 {
			t.Fatalf("expected %d markets, got %d", DefaultMarketCount, len(markets))
		}
		seen := map[string]bool{}
		for _, m := range markets {
			if !strings.HasSuffix(m, "/EUR") || seen[m] {
				t.Errorf("unexpected or duplicate market %q", m)
			}
			seen[m] = true
		}
		for _, want := range []string{"BTC/EUR", "ETH/EUR"} {
			if !seen[want] {
				t.Errorf("expected %s in the defaults", want)
			}
		}
		for _, pegged := range []string{"USDC/EUR", "USDT/EUR", "EURC/EUR"} {
			if seen[pegged] {
				t.Errorf("stablecoin %s must not be a default market", pegged)
			}
		}
	})
}

// TestEffectiveLogLevel covers combining LOG_LEVEL with the DEBUG switch.
func TestEffectiveLogLevel(t *testing.T) {
	t.Run("GIVEN DEBUG off WHEN the log level is resolved THEN LOG_LEVEL is used", func(t *testing.T) {
		s := Defaults()
		s.LogLevel = "WARNING"
		if got := s.EffectiveLogLevel(); got != "WARNING" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("GIVEN DEBUG on WHEN the log level is resolved THEN debug logging wins", func(t *testing.T) {
		s := Defaults()
		s.LogLevel, s.Debug = "ERROR", true
		if got := s.EffectiveLogLevel(); got != "DEBUG" {
			t.Fatalf("got %q", got)
		}
	})
}

// TestCurrencySymbol covers mapping ISO codes to display symbols.
func TestCurrencySymbol(t *testing.T) {
	t.Run("GIVEN a known code WHEN formatting THEN its symbol is returned", func(t *testing.T) {
		if CurrencySymbol("eur") != "€" {
			t.Fatal("expected euro sign")
		}
	})
	t.Run("GIVEN an unknown code WHEN formatting THEN the upper-cased code is returned", func(t *testing.T) {
		if CurrencySymbol("chf") != "CHF" {
			t.Fatal("expected CHF")
		}
	})
}
