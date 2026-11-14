package server

import (
	"net/http"
	"strings"
	"testing"
)

// settingValue returns a field's value from a GET /api/settings body.
func settingValue(t *testing.T, body map[string]any, key string) map[string]any {
	t.Helper()
	for _, raw := range body["fields"].([]any) {
		f := raw.(map[string]any)
		if f["key"] == key {
			return f
		}
	}
	t.Fatalf("setting %s not listed", key)
	return nil
}

// TestSettingsEndpoints covers reading and saving settings over HTTP.
func TestSettingsEndpoints(t *testing.T) {
	t.Run("GIVEN the bot WHEN settings are requested THEN every field lists its metadata and restart state", func(t *testing.T) {
		h := newHarness(t, harnessConfig{})

		body := decodeObject(t, h.do(http.MethodGet, "/api/settings", nil))

		requireKeys(t, body, "fields", "restart_required")
		f := settingValue(t, body, "MIN_SIGNAL_CONFIDENCE")
		requireKeys(t, f, "key", "label", "group", "help", "kind", "live", "secret", "value", "is_set", "restart_pending")
	})

	t.Run("GIVEN a live setting WHEN saved THEN 200 reports it applied and the new value is listed", func(t *testing.T) {
		h := newHarness(t, harnessConfig{})

		rec := h.doJSON(http.MethodPost, "/api/settings", `{"changes":{"MIN_SIGNAL_CONFIDENCE":"0.8"}}`)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d %s", rec.Code, rec.Body)
		}
		if applied := decodeObject(t, rec)["applied_live"].([]any); len(applied) != 1 {
			t.Errorf("expected one live-applied setting, got %v", applied)
		}
		body := decodeObject(t, h.do(http.MethodGet, "/api/settings", nil))
		if settingValue(t, body, "MIN_SIGNAL_CONFIDENCE")["value"] != "0.8" {
			t.Fatal("new value not listed")
		}
	})

	t.Run("GIVEN an invalid value or malformed body WHEN saved THEN 400 explains why", func(t *testing.T) {
		h := newHarness(t, harnessConfig{})

		rec := h.doJSON(http.MethodPost, "/api/settings", `{"changes":{"MAX_DAILY_LOSS_PERCENT":"-1"}}`)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "MAX_DAILY_LOSS_PERCENT") {
			t.Fatalf("expected 400 naming the key, got %d %s", rec.Code, rec.Body)
		}
		for _, body := range []string{`not json`, `{}`, `{"changes":{}}`} {
			if rec := h.doJSON(http.MethodPost, "/api/settings", body); rec.Code != http.StatusBadRequest {
				t.Errorf("body %q: expected 400, got %d", body, rec.Code)
			}
		}
	})

	t.Run("GIVEN a saved API key WHEN settings are listed THEN the response never contains it", func(t *testing.T) {
		h := newHarness(t, harnessConfig{})
		h.doJSON(http.MethodPost, "/api/settings", `{"changes":{"KRAKEN_API_KEY":"very-secret-key"}}`)

		rec := h.do(http.MethodGet, "/api/settings", nil)

		if strings.Contains(rec.Body.String(), "very-secret-key") {
			t.Fatal("secret value leaked in GET /api/settings")
		}
		if settingValue(t, decodeObject(t, rec), "KRAKEN_API_KEY")["is_set"] != true {
			t.Fatal("expected the key to report is_set")
		}
	})
}
