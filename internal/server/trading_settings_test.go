package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
)

// doJSON sends a request with a JSON body through the handler.
func (h *harness) doJSON(method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

// TestTradingSettingsEndpoints covers changing trading mode and environment over HTTP.
func TestTradingSettingsEndpoints(t *testing.T) {
	t.Run("GIVEN the bot WHEN a valid trading mode is posted THEN the control state and dashboard show it", func(t *testing.T) {
		h := newHarness(t, harnessConfig{})

		rec := h.doJSON(http.MethodPost, "/api/control/trading-mode", `{"mode":"semi_automated"}`)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d %s", rec.Code, rec.Body)
		}
		ctrl := decodeObject(t, h.do(http.MethodGet, "/api/control", nil))
		dash := decodeObject(t, h.do(http.MethodGet, "/api/dashboard", nil))
		if ctrl["trading_mode"] != "semi_automated" || dash["mode"] != "semi_automated" {
			t.Fatalf("mode not applied: control=%v dashboard mode=%v", ctrl["trading_mode"], dash["mode"])
		}
	})

	t.Run("GIVEN the bot WHEN an unknown mode or malformed body is posted THEN 400 and nothing changes", func(t *testing.T) {
		h := newHarness(t, harnessConfig{})

		for _, body := range []string{`{"mode":"yolo"}`, `not json`, `{}`} {
			if rec := h.doJSON(http.MethodPost, "/api/control/trading-mode", body); rec.Code != http.StatusBadRequest {
				t.Errorf("body %q: expected 400, got %d", body, rec.Code)
			}
		}
		if ctrl := decodeObject(t, h.do(http.MethodGet, "/api/control", nil)); ctrl["trading_mode"] != "manual" {
			t.Fatalf("mode must stay manual, got %v", ctrl["trading_mode"])
		}
	})

	t.Run("GIVEN paper WHEN live is requested without the typed confirmation THEN 400 and it stays paper", func(t *testing.T) {
		h := newHarness(t, harnessConfig{mutate: func(s *config.Settings) { s.KrakenAPIKey, s.KrakenAPISecret = "k", "s" }})

		rec := h.doJSON(http.MethodPost, "/api/control/trading-environment", `{"environment":"live"}`)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
		if body := decodeObject(t, rec); body["detail"] == "" {
			t.Errorf("expected an explanatory detail, got %v", body)
		}
		if dash := decodeObject(t, h.do(http.MethodGet, "/api/dashboard", nil)); dash["environment"] != "paper" {
			t.Fatalf("environment must stay paper, got %v", dash["environment"])
		}
	})

	t.Run("GIVEN Kraken keys WHEN live is requested with the typed confirmation THEN the environment becomes live", func(t *testing.T) {
		h := newHarness(t, harnessConfig{mutate: func(s *config.Settings) { s.KrakenAPIKey, s.KrakenAPISecret = "k", "s" }})

		rec := h.doJSON(http.MethodPost, "/api/control/trading-environment", `{"environment":"live","confirmation":"LIVE"}`)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d %s", rec.Code, rec.Body)
		}
		if ctrl := decodeObject(t, h.do(http.MethodGet, "/api/control", nil)); ctrl["trading_environment"] != "live" {
			t.Fatalf("environment not applied: %v", ctrl["trading_environment"])
		}
	})
}
