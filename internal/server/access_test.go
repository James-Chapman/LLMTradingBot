package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/app"
	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/secret"
	"github.com/James-Chapman/LLMTradingBot/internal/storage"
)

// send runs a request with optional body, headers and cookies through the handler.
func (h *harness) send(method, target, body string, headers map[string]string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		if strings.EqualFold(k, "Host") {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

// lanHarness is a harness bound to 0.0.0.0 with a stored access token.
func lanHarness(t *testing.T) (*harness, *app.AccessToken) {
	t.Helper()
	var token *app.AccessToken
	h := newHarness(t, harnessConfig{
		mutate: func(s *config.Settings) { s.Host = "0.0.0.0" },
		access: func(repo *storage.Repository) TokenSource {
			tok, _, err := app.LoadAccessToken(context.Background(), repo, secret.Reversible())
			if err != nil {
				t.Fatalf("LoadAccessToken: %v", err)
			}
			token = tok
			return tok
		},
	})
	return h, token
}

// sessionCookie returns the session cookie set by a response, or nil.
func sessionCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookieName {
			return c
		}
	}
	return nil
}

// TestHostHeaderCheck covers refusing requests addressed to a foreign host name (DNS rebinding).
func TestHostHeaderCheck(t *testing.T) {
	t.Run("GIVEN a loopback-bound bot WHEN a request names another host THEN it is refused", func(t *testing.T) {
		h := newHarness(t, harnessConfig{})

		for _, path := range []string{"/api/dashboard", "/", "/health"} {
			if rec := h.send(http.MethodGet, path, "", map[string]string{"Host": "evil.example"}); rec.Code != http.StatusForbidden {
				t.Errorf("%s: expected 403, got %d", path, rec.Code)
			}
		}
		if rec := h.send(http.MethodPost, "/api/control/emergency-stop", "", map[string]string{"Host": "evil.example:8000"}); rec.Code != http.StatusForbidden {
			t.Errorf("expected 403 for a state change, got %d", rec.Code)
		}
		if h.app.ControlSnapshot().EmergencyStop {
			t.Fatal("a refused request must change nothing")
		}
	})

	t.Run("GIVEN a loopback-bound bot WHEN a request names localhost, 127.0.0.1 or [::1] on any port THEN it is served without a token", func(t *testing.T) {
		h := newHarness(t, harnessConfig{})

		for _, host := range []string{"localhost:8000", "127.0.0.1:8000", "[::1]:9000", "LOCALHOST", "127.0.0.1"} {
			if rec := h.send(http.MethodGet, "/api/dashboard", "", map[string]string{"Host": host}); rec.Code != http.StatusOK {
				t.Errorf("%s: expected 200, got %d", host, rec.Code)
			}
		}
	})

	t.Run("GIVEN a bot bound to 0.0.0.0 WHEN a request names an IP address or this computer THEN it passes the host check, another name is refused", func(t *testing.T) {
		h, token := lanHarness(t)
		auth := "Bearer " + token.Token()
		hosts := []string{"192.168.1.20:8000", "[fe80::1]:8000", "localhost:8000"}
		if name, err := os.Hostname(); err == nil && name != "" {
			hosts = append(hosts, strings.ToLower(name)+":8000")
		}

		for _, host := range hosts {
			if rec := h.send(http.MethodGet, "/api/control", "", map[string]string{"Host": host, "Authorization": auth}); rec.Code != http.StatusOK {
				t.Errorf("%s: expected 200, got %d", host, rec.Code)
			}
		}
		if rec := h.send(http.MethodGet, "/api/control", "", map[string]string{"Host": "evil.example", "Authorization": auth}); rec.Code != http.StatusForbidden {
			t.Errorf("expected 403 for a foreign name, got %d", rec.Code)
		}
	})

	t.Run("GIVEN ALLOWED_HOSTS lists a name WHEN the bot is bound to 0.0.0.0 THEN that name passes", func(t *testing.T) {
		var tok *app.AccessToken
		h := newHarness(t, harnessConfig{
			mutate: func(s *config.Settings) { s.Host, s.AllowedHosts = "0.0.0.0", []string{"trading-pc.lan"} },
			access: func(repo *storage.Repository) TokenSource {
				tok, _, _ = app.LoadAccessToken(context.Background(), repo, secret.Reversible())
				return tok
			},
		})

		rec := h.send(http.MethodGet, "/api/control", "", map[string]string{"Host": "Trading-PC.lan:8000", "Authorization": "Bearer " + tok.Token()})

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
	})
}

// TestAccessTokenAuth covers the login required when the bot is reachable from other machines.
func TestAccessTokenAuth(t *testing.T) {
	t.Run("GIVEN a bot bound to 0.0.0.0 WHEN the API is called without a token THEN 401 with a detail", func(t *testing.T) {
		h, _ := lanHarness(t)

		rec := h.send(http.MethodGet, "/api/dashboard", "", nil)

		if rec.Code != http.StatusUnauthorized || decodeObject(t, rec)["detail"] == "" {
			t.Fatalf("expected 401 with a detail, got %d %s", rec.Code, rec.Body)
		}
		if rec := h.send(http.MethodPost, "/api/control/emergency-stop", "", nil); rec.Code != http.StatusUnauthorized || h.app.ControlSnapshot().EmergencyStop {
			t.Fatalf("an unauthenticated state change must be refused, got %d", rec.Code)
		}
	})

	t.Run("GIVEN a bot bound to 0.0.0.0 WHEN the API is called with the bearer token THEN it is served", func(t *testing.T) {
		h, token := lanHarness(t)

		rec := h.send(http.MethodGet, "/api/dashboard", "", map[string]string{"Authorization": "Bearer " + token.Token()})

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		if rec := h.send(http.MethodGet, "/api/dashboard", "", map[string]string{"Authorization": "Bearer wrong"}); rec.Code != http.StatusUnauthorized {
			t.Fatalf("a wrong bearer token must get 401, got %d", rec.Code)
		}
	})

	t.Run("GIVEN the right token WHEN logging in THEN a strict HttpOnly session cookie is set and authorises the API", func(t *testing.T) {
		h, token := lanHarness(t)

		rec := h.send(http.MethodPost, "/api/login", `{"token":"`+token.Token()+`"}`, nil)

		cookie := sessionCookie(rec)
		if rec.Code != http.StatusOK || cookie == nil {
			t.Fatalf("expected 200 with a session cookie, got %d %v", rec.Code, rec.Header())
		}
		if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" {
			t.Errorf("cookie must be HttpOnly, SameSite=Strict, Path=/: %+v", cookie)
		}
		if rec := h.send(http.MethodGet, "/api/dashboard", "", nil, cookie); rec.Code != http.StatusOK {
			t.Fatalf("the session cookie must authorise, got %d", rec.Code)
		}
		if rec := h.send(http.MethodGet, "/", "", nil, cookie); rec.Code != http.StatusOK {
			t.Fatalf("the session cookie must open the dashboard page, got %d", rec.Code)
		}
	})

	t.Run("GIVEN a wrong token WHEN logging in THEN 401 and no cookie", func(t *testing.T) {
		h, _ := lanHarness(t)

		rec := h.send(http.MethodPost, "/api/login", `{"token":"guess"}`, nil)

		if rec.Code != http.StatusUnauthorized || sessionCookie(rec) != nil {
			t.Fatalf("expected 401 without a cookie, got %d", rec.Code)
		}
	})

	t.Run("GIVEN no session WHEN a page is requested THEN it redirects to the login page, which is served", func(t *testing.T) {
		h, _ := lanHarness(t)

		for _, path := range []string{"/", "/approvals", "/sw.js"} {
			rec := h.send(http.MethodGet, path, "", nil)
			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
				t.Errorf("%s: expected a redirect to /login, got %d %q", path, rec.Code, rec.Header().Get("Location"))
			}
		}
		login := h.send(http.MethodGet, "/login", "", nil)
		if login.Code != http.StatusOK || !strings.Contains(login.Body.String(), "/api/login") {
			t.Fatalf("expected the login page, got %d", login.Code)
		}
		if rec := h.send(http.MethodGet, "/static/styles.css", "", nil); rec.Code != http.StatusOK {
			t.Errorf("the login page's stylesheet must load without a session, got %d", rec.Code)
		}
	})

	t.Run("GIVEN no session WHEN /health is requested THEN it stays open", func(t *testing.T) {
		h, _ := lanHarness(t)

		if rec := h.send(http.MethodGet, "/health", "", nil); rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
	})

	t.Run("GIVEN a stored token WHEN the settings are listed THEN the token is not in them", func(t *testing.T) {
		h, token := lanHarness(t)

		rec := h.send(http.MethodGet, "/api/settings", "", map[string]string{"Authorization": "Bearer " + token.Token()})

		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), token.Token()) {
			t.Fatalf("the settings must not contain the token (status %d)", rec.Code)
		}
	})

	t.Run("GIVEN a session WHEN the token is rotated THEN the old token stops working and the new one works", func(t *testing.T) {
		h, token := lanHarness(t)
		old := token.Token()

		rec := h.send(http.MethodPost, "/api/access-token/rotate", "", map[string]string{"Authorization": "Bearer " + old})

		fresh, _ := decodeObject(t, rec)["token"].(string)
		if rec.Code != http.StatusOK || fresh == "" || fresh == old || sessionCookie(rec) == nil {
			t.Fatalf("expected a new token and cookie, got %d %s", rec.Code, rec.Body)
		}
		if rec := h.send(http.MethodGet, "/api/control", "", map[string]string{"Authorization": "Bearer " + old}); rec.Code != http.StatusUnauthorized {
			t.Errorf("the old token must stop working, got %d", rec.Code)
		}
		if rec := h.send(http.MethodGet, "/api/control", "", map[string]string{"Authorization": "Bearer " + fresh}); rec.Code != http.StatusOK {
			t.Errorf("the new token must work, got %d", rec.Code)
		}
		if rec := h.send(http.MethodPost, "/api/access-token/rotate", "", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("rotating without the token must be refused, got %d", rec.Code)
		}
	})

	t.Run("GIVEN a loopback-bound bot WHEN the API and pages are requested without a token THEN they are served and login is not used", func(t *testing.T) {
		h := newHarness(t, harnessConfig{})

		if rec := h.send(http.MethodGet, "/api/dashboard", "", nil); rec.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rec.Code)
		}
		if rec := h.send(http.MethodGet, "/", "", nil); rec.Code != http.StatusOK {
			t.Errorf("expected the dashboard page, got %d", rec.Code)
		}
		if rec := h.send(http.MethodGet, "/login", "", nil); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
			t.Errorf("the login page should send a loopback user to the dashboard, got %d", rec.Code)
		}
	})

	t.Run("GIVEN a bot bound to 0.0.0.0 without an access token WHEN the API is called THEN every request is refused", func(t *testing.T) {
		h := newHarness(t, harnessConfig{mutate: func(s *config.Settings) { s.Host = "0.0.0.0" }})

		if rec := h.send(http.MethodGet, "/api/dashboard", "", map[string]string{"Authorization": "Bearer "}); rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 when no token is configured, got %d", rec.Code)
		}
	})
}

// TestRequestBodies covers body size and shape limits.
func TestRequestBodies(t *testing.T) {
	t.Run("GIVEN a body over the limit WHEN posted THEN 413 and nothing changes", func(t *testing.T) {
		h := newHarness(t, harnessConfig{})
		big := `{"mode":"semi_automated","pad":"` + strings.Repeat("x", 4<<10) + `"}`

		rec := h.send(http.MethodPost, "/api/control/trading-mode", big, nil)

		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("expected 413, got %d %s", rec.Code, rec.Body)
		}
		if h.app.ControlSnapshot().TradingMode != "manual" {
			t.Fatal("an oversized body must change nothing")
		}
	})

	t.Run("GIVEN trailing data after the JSON value WHEN posted THEN 400", func(t *testing.T) {
		h := newHarness(t, harnessConfig{})

		rec := h.send(http.MethodPost, "/api/control/trading-mode", `{"mode":"semi_automated"} {"mode":"x"}`, nil)

		if rec.Code != http.StatusBadRequest || h.app.ControlSnapshot().TradingMode != "manual" {
			t.Fatalf("expected 400 and no change, got %d", rec.Code)
		}
		if rec := h.send(http.MethodPost, "/api/control/trading-mode", "{\"mode\":\"semi_automated\"}\n", nil); rec.Code != http.StatusOK {
			t.Fatalf("a trailing newline is fine, got %d", rec.Code)
		}
	})
}

// TestUnknownMarketToggle covers toggling a market the bot does not know.
func TestUnknownMarketToggle(t *testing.T) {
	t.Run("GIVEN an unknown market WHEN toggled or live-toggled THEN 404 and nothing is saved", func(t *testing.T) {
		h := newHarness(t, harnessConfig{})

		for _, path := range []string{"/api/control/markets/NOPE%2FEUR/toggle", "/api/control/markets/%3Cscript%3E/live-toggle"} {
			if rec := h.send(http.MethodPost, path, "", nil); rec.Code != http.StatusNotFound {
				t.Errorf("%s: expected 404, got %d", path, rec.Code)
			}
		}
		snap := h.app.ControlSnapshot()
		if len(snap.DisabledMarkets) != 0 || len(snap.LiveMarkets) != 0 {
			t.Fatalf("nothing may be saved for an unknown market, got %+v", snap)
		}
	})
}
