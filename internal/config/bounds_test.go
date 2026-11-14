package config

import (
	"strings"
	"testing"
)

// applyRaw applies raw to the named field on fresh defaults and returns the
// error plus whether the setting changed.
func applyRaw(t *testing.T, key, raw string) (bool, error) {
	t.Helper()
	f, ok := FieldByKey(key)
	if !ok {
		t.Fatalf("no field %s", key)
	}
	s := Defaults()
	before := f.Value(s)
	err := f.Apply(&s, raw)
	return f.Value(s) != before, err
}

// TestSettingBounds covers the ranges that keep the risk engine and the LLM
// clients working.
func TestSettingBounds(t *testing.T) {
	t.Run("GIVEN a stop distance of 0 or 100% WHEN applied THEN it is refused and the setting unchanged", func(t *testing.T) {
		for _, key := range []string{"STOP_LOSS_PCT", "TRAILING_STOP_PCT"} {
			for _, raw := range []string{"0", "1", "0.51", "-0.1"} {
				changed, err := applyRaw(t, key, raw)
				if err == nil || !strings.Contains(err.Error(), key) || changed {
					t.Errorf("%s=%s: expected a refusal naming the key, got %v (changed %v)", key, raw, err, changed)
				}
			}
			if _, err := applyRaw(t, key, "0.5"); err != nil {
				t.Errorf("%s=0.5 should be allowed: %v", key, err)
			}
		}
	})

	t.Run("GIVEN a loss limit of 0 WHEN applied THEN it is refused", func(t *testing.T) {
		for _, key := range []string{"MAX_LOSS_PER_TRADE_PERCENT", "MAX_DAILY_LOSS_PERCENT"} {
			if changed, err := applyRaw(t, key, "0"); err == nil || changed {
				t.Errorf("%s=0: expected a refusal, got %v", key, err)
			}
			if _, err := applyRaw(t, key, "0.5"); err != nil {
				t.Errorf("%s=0.5 should be allowed: %v", key, err)
			}
		}
	})

	t.Run("GIVEN an LLM timeout too large for a duration WHEN applied THEN it is refused", func(t *testing.T) {
		for _, key := range []string{"LMSTUDIO_TIMEOUT", "OPENAI_TIMEOUT"} {
			for _, raw := range []string{"99999999999", "601"} {
				if changed, err := applyRaw(t, key, raw); err == nil || changed {
					t.Errorf("%s=%s: expected a refusal, got %v", key, raw, err)
				}
			}
			if _, err := applyRaw(t, key, "600"); err != nil {
				t.Errorf("%s=600 should be allowed: %v", key, err)
			}
		}
	})

	t.Run("GIVEN a momentum lookback longer than the kept price history WHEN applied THEN it is refused", func(t *testing.T) {
		if changed, err := applyRaw(t, "MOMENTUM_LOOKBACK_TICKS", "100000"); err == nil || changed {
			t.Errorf("expected a refusal, got %v", err)
		}
		if _, err := applyRaw(t, "MOMENTUM_LOOKBACK_TICKS", "200"); err == nil {
			t.Errorf("a lookback of 200 needs 201 ticks, more than the %d kept", PriceHistoryTicks)
		}
		if _, err := applyRaw(t, "MOMENTUM_LOOKBACK_TICKS", "199"); err != nil {
			t.Errorf("the longest lookback the history can fill should be allowed: %v", err)
		}
	})

	t.Run("GIVEN very large concurrency or dynamic market caps WHEN applied THEN they are refused", func(t *testing.T) {
		for key, raw := range map[string]string{"LLM_ONLY_MAX_CONCURRENCY": "1000", "MAX_ETH_ECOSYSTEM_COINS": "1000"} {
			if changed, err := applyRaw(t, key, raw); err == nil || changed {
				t.Errorf("%s=%s: expected a refusal, got %v", key, raw, err)
			}
		}
		if _, err := applyRaw(t, "MAX_ETH_ECOSYSTEM_COINS", "0"); err != nil {
			t.Errorf("0 dynamic markets (none) should be allowed: %v", err)
		}
	})

	t.Run("GIVEN a wildcard browser origin WHEN applied THEN it is refused", func(t *testing.T) {
		for _, raw := range []string{`["*"]`, `["http://localhost:8000","*"]`, `["null"]`} {
			if changed, err := applyRaw(t, "CORS_ORIGINS", raw); err == nil || !strings.Contains(err.Error(), "CORS_ORIGINS") || changed {
				t.Errorf("CORS_ORIGINS=%s: expected a refusal, got %v", raw, err)
			}
		}
		if _, err := applyRaw(t, "CORS_ORIGINS", `["http://192.168.1.10:8000"]`); err != nil {
			t.Errorf("an explicit origin should be allowed: %v", err)
		}
	})

	t.Run("GIVEN host names WHEN ALLOWED_HOSTS is applied THEN plain names are kept and URLs or ports refused", func(t *testing.T) {
		f, ok := FieldByKey("ALLOWED_HOSTS")
		if !ok || f.Live || f.Group != GroupAdvanced {
			t.Fatalf("expected a restart-required Advanced setting, got %+v (found %v)", f, ok)
		}
		s := Defaults()
		if err := f.Apply(&s, `["Trading-PC.lan","bot.example.com"]`); err != nil {
			t.Fatal(err)
		}
		if len(s.AllowedHosts) != 2 || s.AllowedHosts[0] != "trading-pc.lan" {
			t.Errorf("expected lower-cased host names, got %v", s.AllowedHosts)
		}
		for _, raw := range []string{`["http://bot.lan"]`, `["bot.lan:8000"]`, `[""]`, `["bot.lan/x"]`} {
			if changed, err := applyRaw(t, "ALLOWED_HOSTS", raw); err == nil || changed {
				t.Errorf("ALLOWED_HOSTS=%s: expected a refusal, got %v", raw, err)
			}
		}
	})

	t.Run("GIVEN the dynamic market source WHEN coingecko, the old coinmarketcap value or none is applied THEN it is accepted", func(t *testing.T) {
		for _, raw := range []string{"coingecko", "coinmarketcap", "none"} {
			if _, err := applyRaw(t, "DYNAMIC_UNIVERSE_SOURCE", raw); err != nil {
				t.Errorf("%s should be accepted: %v", raw, err)
			}
		}
		if _, err := applyRaw(t, "DYNAMIC_UNIVERSE_SOURCE", "binance"); err == nil {
			t.Error("an unknown source should be refused")
		}
		if Defaults().DynamicUniverseSource != "coingecko" {
			t.Errorf("the default should name the source actually used, got %q", Defaults().DynamicUniverseSource)
		}
	})

	t.Run("GIVEN the built-in defaults WHEN every field re-applies its own value THEN all are within bounds", func(t *testing.T) {
		d := Defaults()
		for _, f := range Fields() {
			s := Defaults()
			if err := f.Apply(&s, f.Value(d)); err != nil {
				t.Errorf("default %s=%q is out of bounds: %v", f.Key, f.Value(d), err)
			}
		}
	})
}

// TestIsLoopbackHost covers deciding whether the dashboard is reachable from other computers.
func TestIsLoopbackHost(t *testing.T) {
	t.Run("GIVEN loopback addresses WHEN checked THEN they are loopback", func(t *testing.T) {
		for _, host := range []string{"127.0.0.1", "localhost", "LOCALHOST", "::1", "[::1]", "127.0.0.2"} {
			if !IsLoopbackHost(host) {
				t.Errorf("%q should be loopback", host)
			}
		}
	})

	t.Run("GIVEN every-interface or LAN addresses WHEN checked THEN they are not loopback", func(t *testing.T) {
		for _, host := range []string{"0.0.0.0", "", "::", "192.168.1.10", "trading-pc.lan"} {
			if IsLoopbackHost(host) {
				t.Errorf("%q must not be loopback", host)
			}
		}
	})
}
