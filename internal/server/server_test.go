package server

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/app"
	"github.com/James-Chapman/LLMTradingBot/internal/app/apptest"
	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/news"
	"github.com/James-Chapman/LLMTradingBot/internal/observability"
	"github.com/James-Chapman/LLMTradingBot/internal/secret"
	"github.com/James-Chapman/LLMTradingBot/internal/storage"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy"
)

// frontendDir is the real frontend directory relative to this package.
const frontendDir = "../../frontend"

// harness is a running app plus its HTTP handler and fakes.
type harness struct {
	app      *app.App
	handler  http.Handler
	market   *apptest.Market
	live     *apptest.Live
	strategy *apptest.Strategy
	repo     *storage.Repository
}

// harnessConfig customises a test harness.
type harnessConfig struct {
	mutate func(*config.Settings)
	ideas  func(strategy.Input) []domain.TradeIdea
	run    bool                                  // start the background loops and wait for the first tick
	access func(*storage.Repository) TokenSource // builds the access token for a non-loopback harness
	seed   func(*storage.Repository)             // runs on the database before the app starts
}

// newHarness builds an app over a temp DB with network-free fakes.
func newHarness(t *testing.T, cfg harnessConfig) *harness {
	t.Helper()
	settings := config.Defaults()
	settings.FixedMarkets = []string{"BTC/EUR", "ETH/EUR"}
	settings.NewsSources = nil
	settings.LMStudioBaseURL = ""
	settings.OpenAIBaseURL = ""
	settings.TradingMode = "manual"
	settings.TradingEnvironment = "paper"
	if cfg.mutate != nil {
		cfg.mutate(&settings)
	}

	repo, err := storage.Open(filepath.Join(t.TempDir(), "trading_bot.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	// A full price history lets the indicator strategy evaluate on the first tick.
	for i := 0; i < strategy.LookbackTicks+5; i++ {
		_ = repo.SavePriceTick(context.Background(), "BTC/EUR", 50000, nil)
		_ = repo.SavePriceTick(context.Background(), "ETH/EUR", 3000, nil)
	}
	if cfg.seed != nil {
		cfg.seed(repo)
	}

	h := &harness{
		market:   apptest.NewMarket(map[string]float64{"BTC/EUR": 50000, "ETH/EUR": 3000}),
		live:     &apptest.Live{},
		strategy: &apptest.Strategy{StrategyID: strategy.Basic1hID, Ideas: cfg.ideas},
		repo:     repo,
	}
	a, err := app.NewWithOptions(context.Background(), settings, repo, app.Options{
		Market:          h.market,
		Live:            h.live,
		LLM:             &apptest.LLM{},
		Universe:        apptest.Universe{Markets: settings.FixedMarkets},
		NewsAdapters:    []news.Adapter{},
		StrategyFactory: func(config.Settings) []strategy.Strategy { return []strategy.Strategy{h.strategy} },
		Activity:        observability.NewActivityLog(200),
		Logger:          slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Intervals:       app.Intervals{Strategy: time.Hour, ReflectionDelay: time.Hour, OHLCRefresh: time.Hour},
		SecretBox:       secret.Reversible(),
	})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	h.app = a
	var access TokenSource
	if cfg.access != nil {
		access = cfg.access(repo)
	}
	// httptest requests carry Host example.com; accept it as the bot's own name.
	h.handler = New(a, Options{
		FrontendDir:      frontendDir,
		AllowedHosts:     []string{"example.com"},
		Access:           access,
		FailedLoginDelay: time.Millisecond,
	})
	if _, err := a.SelectStrategy(strategy.Basic1hID); err != nil {
		t.Fatalf("SelectStrategy: %v", err)
	}

	if cfg.run {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { a.Run(ctx); close(done) }()
		t.Cleanup(func() { cancel(); <-done })
		h.waitFor(t, func() bool {
			if h.strategy.Calls() == 0 {
				return false
			}
			return cfg.ideas == nil || len(a.Dashboard().Signals) > 0
		})
	}
	return h
}

// waitFor polls cond until it holds or the test times out.
func (h *harness) waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// do sends a request through the handler and returns the recorder.
func (h *harness) do(method, target string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

// decodeObject decodes a JSON object response.
func decodeObject(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("expected a JSON object, got %q: %v", rec.Body.String(), err)
	}
	return out
}

// decodeArray decodes a JSON array response (null fails).
func decodeArray(t *testing.T, rec *httptest.ResponseRecorder) []any {
	t.Helper()
	var out []any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out == nil {
		t.Fatalf("expected a JSON array, got %q: %v", rec.Body.String(), err)
	}
	return out
}

// requireKeys fails when any key is missing from obj.
func requireKeys(t *testing.T, obj map[string]any, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, ok := obj[k]; !ok {
			t.Errorf("missing key %q in %v", k, obj)
		}
	}
}

// longBTC is a strategy that always proposes a confident BTC long.
func longBTC(strategy.Input) []domain.TradeIdea {
	return []domain.TradeIdea{apptest.Idea(strategy.Basic1hID, "BTC/EUR", domain.Long, 0.9)}
}

// TestHealthEndpoints covers /health and /api/health.
func TestHealthEndpoints(t *testing.T) {
	h := newHarness(t, harnessConfig{})

	t.Run("GIVEN the bot WHEN /health is requested THEN it reports healthy with the app version", func(t *testing.T) {
		rec := h.do(http.MethodGet, "/health", nil)
		body := decodeObject(t, rec)
		if rec.Code != http.StatusOK || body["status"] != "healthy" || body["version"] != app.Version {
			t.Errorf("unexpected health response %d %v", rec.Code, body)
		}
	})

	t.Run("GIVEN the bot WHEN /api/health is requested THEN the LLM status keys are present", func(t *testing.T) {
		requireKeys(t, decodeObject(t, h.do(http.MethodGet, "/api/health", nil)), "llm_available", "llm_backend", "llm_model")
	})
}

// TestDashboard covers /api/dashboard and its ETag handling.
func TestDashboard(t *testing.T) {
	h := newHarness(t, harnessConfig{run: true, ideas: longBTC})

	t.Run("GIVEN a running bot WHEN the dashboard is requested THEN every key the frontend reads is present", func(t *testing.T) {
		rec := h.do(http.MethodGet, "/api/dashboard", nil)
		if rec.Code != http.StatusOK || rec.Header().Get("ETag") == "" {
			t.Fatalf("expected 200 with an ETag, got %d %v", rec.Code, rec.Header())
		}
		body := decodeObject(t, rec)
		requireKeys(t, body, "mode", "environment", "base_currency", "currency_symbol", "equity", "cash",
			"open_position_ids", "markets", "signals", "positions", "approvals", "equity_history",
			"risk_rejections", "activity", "control", "strategies", "warmup_ticks_remaining", "learning", "version", "llm")
		requireKeys(t, body["llm"].(map[string]any), "available", "status", "model", "backend", "reflection", "briefing")
		requireKeys(t, body["control"].(map[string]any), "emergency_stop", "disabled_markets", "selected_strategy", "live_markets")
		markets := body["markets"].([]any)
		if len(markets) != 2 {
			t.Fatalf("expected both markets, got %v", markets)
		}
		requireKeys(t, markets[0].(map[string]any), "symbol", "price", "enabled", "live")
		signals := body["signals"].([]any)
		if len(signals) == 0 {
			t.Fatalf("expected the buffered signal")
		}
		requireKeys(t, signals[0].(map[string]any), "strategy", "market", "direction", "confidence", "thesis", "risk_approved", "risk_reason", "trade_idea_id")
		strategies := body["strategies"].([]any)
		requireKeys(t, strategies[0].(map[string]any), "id", "label", "enabled", "selected")
	})

	t.Run("GIVEN an unchanged dashboard WHEN the ETag is sent back THEN 304 Not Modified is returned", func(t *testing.T) {
		etag := h.do(http.MethodGet, "/api/dashboard", nil).Header().Get("ETag")
		rec := h.do(http.MethodGet, "/api/dashboard", map[string]string{"If-None-Match": etag})
		if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
			t.Errorf("expected an empty 304, got %d %q", rec.Code, rec.Body.String())
		}
	})

	t.Run("GIVEN a state change WHEN the old ETag is sent THEN the full dashboard is returned", func(t *testing.T) {
		etag := h.do(http.MethodGet, "/api/dashboard", nil).Header().Get("ETag")
		h.do(http.MethodPost, "/api/control/resume", nil)
		rec := h.do(http.MethodGet, "/api/dashboard", map[string]string{"If-None-Match": etag})
		if rec.Code != http.StatusOK {
			t.Errorf("expected 200 after a change, got %d", rec.Code)
		}
	})

	t.Run("GIVEN a buffered signal WHEN its detail is requested THEN the signal context is returned", func(t *testing.T) {
		signals := h.app.Dashboard().Signals
		rec := h.do(http.MethodGet, "/api/signals/"+signals[0].TradeIdeaID, nil)
		body := decodeObject(t, rec)
		requireKeys(t, body, "id", "market", "indicators", "news_context", "risk_approved", "supporting_signals")
		ss := body["supporting_signals"].(map[string]any)
		if _, ok := ss["pre_llm_confidence"]; !ok {
			t.Errorf("pre_llm_confidence must be stamped on the stored idea")
		}
	})

	t.Run("GIVEN a signal from a 1-hour strategy WHEN its detail is requested THEN it names the strategy's candle length for the chart", func(t *testing.T) {
		signals := h.app.Dashboard().Signals
		body := decodeObject(t, h.do(http.MethodGet, "/api/signals/"+signals[0].TradeIdeaID, nil))
		want := float64(strategy.CandleMinutes(body["strategy_id"].(string)))
		if body["candle_minutes"] != want {
			t.Errorf("candle_minutes = %v, want %v for %v", body["candle_minutes"], want, body["strategy_id"])
		}
	})

	t.Run("GIVEN an unknown signal WHEN its detail is requested THEN 404 Signal not found", func(t *testing.T) {
		rec := h.do(http.MethodGet, "/api/signals/does-not-exist", nil)
		if rec.Code != http.StatusNotFound || decodeObject(t, rec)["detail"] != "Signal not found" {
			t.Errorf("unexpected response %d %s", rec.Code, rec.Body.String())
		}
	})
}

// TestPages covers the HTML shells, the service worker and static files.
func TestPages(t *testing.T) {
	h := newHarness(t, harnessConfig{})

	t.Run("GIVEN the dashboard shell WHEN / is requested THEN index.html is served uncached", func(t *testing.T) {
		rec := h.do(http.MethodGet, "/", nil)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `x-data="dashboard()"`) {
			t.Fatalf("index not served: %d", rec.Code)
		}
		if rec.Header().Get("Cache-Control") != "no-store, max-age=0" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
			t.Errorf("unexpected headers %v", rec.Header())
		}
	})

	t.Run("GIVEN the approvals shell WHEN /approvals is requested THEN approvals.html is served uncached", func(t *testing.T) {
		rec := h.do(http.MethodGet, "/approvals", nil)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `id="container"`) || rec.Header().Get("Cache-Control") != "no-store, max-age=0" {
			t.Errorf("approvals not served correctly: %d %v", rec.Code, rec.Header())
		}
	})

	t.Run("GIVEN the service worker WHEN /sw.js is requested THEN it is JavaScript and never cached", func(t *testing.T) {
		rec := h.do(http.MethodGet, "/sw.js", nil)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/javascript" || rec.Header().Get("Cache-Control") != "no-store, max-age=0" {
			t.Errorf("unexpected sw.js response %d %v", rec.Code, rec.Header())
		}
	})

	t.Run("GIVEN shared CSS WHEN /static/styles.css is requested THEN the stylesheet is served", func(t *testing.T) {
		rec := h.do(http.MethodGet, "/static/styles.css", nil)
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/css") {
			t.Errorf("stylesheet not served: %d %v", rec.Code, rec.Header())
		}
	})

	t.Run("GIVEN removed trial layouts WHEN /index2 or a static directory is requested THEN 404", func(t *testing.T) {
		for _, path := range []string{"/index2", "/index9", "/static/"} {
			if rec := h.do(http.MethodGet, path, nil); rec.Code != http.StatusNotFound {
				t.Errorf("%s: expected 404, got %d", path, rec.Code)
			}
		}
	})
}

// TestControlEndpoints covers the operator control routes.
func TestControlEndpoints(t *testing.T) {
	h := newHarness(t, harnessConfig{})

	t.Run("GIVEN a market with a slash WHEN toggled via an encoded path THEN it flips enabled", func(t *testing.T) {
		body := decodeObject(t, h.do(http.MethodPost, "/api/control/markets/BTC%2FEUR/toggle", nil))
		if body["market"] != "BTC/EUR" || body["enabled"] != false {
			t.Fatalf("expected BTC/EUR disabled, got %v", body)
		}
		body = decodeObject(t, h.do(http.MethodPost, "/api/control/markets/BTC/EUR/toggle", nil))
		if body["market"] != "BTC/EUR" || body["enabled"] != true {
			t.Errorf("expected BTC/EUR re-enabled via a raw slash path, got %v", body)
		}
	})

	t.Run("GIVEN a market WHEN live-toggled THEN it routes live and the control state shows it", func(t *testing.T) {
		body := decodeObject(t, h.do(http.MethodPost, "/api/control/markets/ETH%2FEUR/live-toggle", nil))
		if body["market"] != "ETH/EUR" || body["live"] != true {
			t.Fatalf("unexpected live toggle %v", body)
		}
		ctrl := decodeObject(t, h.do(http.MethodGet, "/api/control", nil))
		if live := ctrl["live_markets"].([]any); len(live) != 1 || live[0] != "ETH/EUR" {
			t.Errorf("live market not in control state: %v", ctrl)
		}
	})

	t.Run("GIVEN an unknown market action WHEN posted THEN 404", func(t *testing.T) {
		if rec := h.do(http.MethodPost, "/api/control/markets/BTC%2FEUR/explode", nil); rec.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d", rec.Code)
		}
	})

	t.Run("GIVEN strategies WHEN selected by legacy, toggle and unknown IDs THEN canonical IDs or 404 are returned", func(t *testing.T) {
		body := decodeObject(t, h.do(http.MethodPost, "/api/control/strategies/basic_trend/select", nil))
		if body["strategy_id"] != strategy.Basic1hID || body["selected"] != true {
			t.Errorf("legacy ID should map to basic_strategy, got %v", body)
		}
		body = decodeObject(t, h.do(http.MethodPost, "/api/control/strategies/basic_strategy/toggle", nil))
		if body["enabled"] != true || body["selected"] != true {
			t.Errorf("toggle should select, got %v", body)
		}
		rec := h.do(http.MethodPost, "/api/control/strategies/nope/select", nil)
		if rec.Code != http.StatusNotFound || decodeObject(t, rec)["detail"] != "Strategy not found" {
			t.Errorf("expected 404 Strategy not found, got %d %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("GIVEN registered strategies WHEN reloaded THEN their IDs are listed", func(t *testing.T) {
		body := decodeObject(t, h.do(http.MethodPost, "/api/control/strategies/reload", nil))
		if body["status"] != "reloaded" || len(body["strategies"].([]any)) != 1 {
			t.Errorf("unexpected reload response %v", body)
		}
	})

	t.Run("GIVEN the bot WHEN the emergency stop is activated and resumed THEN the control state follows", func(t *testing.T) {
		body := decodeObject(t, h.do(http.MethodPost, "/api/control/emergency-stop", nil))
		if body["status"] != "stopped" || body["timestamp"] == "" {
			t.Fatalf("unexpected stop response %v", body)
		}
		if decodeObject(t, h.do(http.MethodGet, "/api/control", nil))["emergency_stop"] != true {
			t.Errorf("stop not reflected in control state")
		}
		// A manual close reduces exposure and is allowed during the stop: an
		// unknown position is "not found", not "stopped".
		rec := h.do(http.MethodPost, "/api/positions/whatever/close", nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("closing during a stop must not be refused for the stop, got %d", rec.Code)
		}
		if decodeObject(t, h.do(http.MethodPost, "/api/control/resume", nil))["status"] != "resumed" {
			t.Errorf("resume failed")
		}
	})
}

// TestApprovalWorkflow ports the integration workflow: signal â†’ approval â†’ execution.
func TestApprovalWorkflow(t *testing.T) {
	h := newHarness(t, harnessConfig{run: true, ideas: longBTC, mutate: func(s *config.Settings) { s.TradingMode = "semi_automated" }})

	t.Run("GIVEN a semi-automated signal WHEN approvals are listed THEN the queued request has every field", func(t *testing.T) {
		list := decodeArray(t, h.do(http.MethodGet, "/api/approvals", nil))
		if len(list) != 1 {
			t.Fatalf("expected one approval, got %v", list)
		}
		requireKeys(t, list[0].(map[string]any), "id", "market", "direction", "strategy_id", "confidence", "size",
			"expires_at", "thesis", "entry_plan", "exit_plan", "risk_approved", "risk_reason", "adjusted_sizing", "status")
	})

	t.Run("GIVEN an emergency stop WHEN an approval is approved THEN 503 is returned", func(t *testing.T) {
		h.do(http.MethodPost, "/api/control/emergency-stop", nil)
		defer h.do(http.MethodPost, "/api/control/resume", nil)
		rec := h.do(http.MethodPost, "/api/approvals/any/approve", nil)
		if rec.Code != http.StatusServiceUnavailable || decodeObject(t, rec)["detail"] != "Emergency stop is active - cannot execute trades" {
			t.Errorf("unexpected response %d %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("GIVEN the stop cleared the queue WHEN the next tick runs THEN unknown approvals 404", func(t *testing.T) {
		rec := h.do(http.MethodPost, "/api/approvals/unknown/approve", nil)
		if rec.Code != http.StatusNotFound || decodeObject(t, rec)["detail"] != "Approval not found or expired" {
			t.Errorf("unexpected response %d %s", rec.Code, rec.Body.String())
		}
		rec = h.do(http.MethodPost, "/api/approvals/unknown/reject", nil)
		if rec.Code != http.StatusNotFound || decodeObject(t, rec)["detail"] != "Approval not found" {
			t.Errorf("unexpected response %d %s", rec.Code, rec.Body.String())
		}
	})
}

// TestApproveExecutes covers a successful approval executing a paper trade.
func TestApproveExecutes(t *testing.T) {
	h := newHarness(t, harnessConfig{run: true, ideas: longBTC, mutate: func(s *config.Settings) { s.TradingMode = "semi_automated" }})

	t.Run("GIVEN a pending approval WHEN approved THEN a paper order fills and a position opens", func(t *testing.T) {
		pending := h.app.PendingApprovals()
		if len(pending) != 1 {
			t.Fatalf("expected one pending approval")
		}
		body := decodeObject(t, h.do(http.MethodPost, "/api/approvals/"+pending[0].ID+"/approve", nil))
		if body["status"] != "approved" || body["order_status"] != "filled" || body["order_id"] == "" {
			t.Fatalf("unexpected approve response %v", body)
		}
		if len(h.app.Dashboard().Positions) != 1 || len(h.app.PendingApprovals()) != 0 {
			t.Errorf("approval should open a position and leave the queue")
		}
		ledger := decodeArray(t, h.do(http.MethodGet, "/api/trades", nil))
		if len(ledger) != 1 {
			t.Errorf("trade ledger should show the fill, got %v", ledger)
		}
	})
}

// TestRejectApproval covers rejecting a pending approval.
func TestRejectApproval(t *testing.T) {
	h := newHarness(t, harnessConfig{run: true, ideas: longBTC, mutate: func(s *config.Settings) { s.TradingMode = "semi_automated" }})

	t.Run("GIVEN a pending approval WHEN rejected THEN it leaves the queue", func(t *testing.T) {
		id := h.app.PendingApprovals()[0].ID
		body := decodeObject(t, h.do(http.MethodPost, "/api/approvals/"+id+"/reject", nil))
		if body["status"] != "rejected" || body["id"] != id || len(h.app.PendingApprovals()) != 0 {
			t.Errorf("unexpected reject result %v", body)
		}
	})
}

// TestPositionsAndExports covers manual close, CSV export and reset.
func TestPositionsAndExports(t *testing.T) {
	h := newHarness(t, harnessConfig{run: true, ideas: longBTC, mutate: func(s *config.Settings) { s.TradingMode = "fully_automated" }})

	t.Run("GIVEN an open position WHEN closed by its short ID THEN the close is recorded", func(t *testing.T) {
		positions := h.app.Dashboard().Positions
		if len(positions) != 1 {
			t.Fatalf("expected one open position, got %v", positions)
		}
		body := decodeObject(t, h.do(http.MethodPost, "/api/positions/"+positions[0].PositionID+"/close", nil))
		requireKeys(t, body, "status", "position_id", "order_id", "fill_price", "pnl", "cash")
		if body["status"] != "closed" || body["position_id"] != positions[0].PositionIDFull {
			t.Errorf("unexpected close response %v", body)
		}
		if len(decodeArray(t, h.do(http.MethodGet, "/api/closed-trades", nil))) != 1 {
			t.Errorf("closed trade should be listed")
		}
	})

	t.Run("GIVEN a closed trade WHEN exported THEN a CSV with the header and one row is downloaded", func(t *testing.T) {
		rec := h.do(http.MethodGet, "/api/export/trades.csv", nil)
		if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") || rec.Header().Get("Content-Disposition") != "attachment; filename=trades.csv" {
			t.Fatalf("unexpected CSV headers %v", rec.Header())
		}
		rows, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
		if err != nil || len(rows) != 2 || rows[0][0] != "strategy" || rows[0][len(rows[0])-1] != "closing_trade_idea_id" {
			t.Errorf("unexpected CSV %q (%v)", rec.Body.String(), err)
		}
	})

	t.Run("GIVEN no such position WHEN closed THEN 404 Position not found", func(t *testing.T) {
		rec := h.do(http.MethodPost, "/api/positions/zzzzzzzz/close", nil)
		if rec.Code != http.StatusNotFound || decodeObject(t, rec)["detail"] != "Position not found" {
			t.Errorf("unexpected response %d %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("GIVEN paper mode WHEN positions are reset THEN the cleared count is returned", func(t *testing.T) {
		body := decodeObject(t, h.do(http.MethodPost, "/api/positions/reset", nil))
		if _, ok := body["cleared"]; !ok || len(h.app.Dashboard().Positions) != 0 {
			t.Errorf("unexpected reset response %v", body)
		}
	})

	t.Run("GIVEN read endpoints WHEN requested THEN they return JSON collections", func(t *testing.T) {
		decodeArray(t, h.do(http.MethodGet, "/api/trades", nil))
		decodeArray(t, h.do(http.MethodGet, "/api/rejected-trades", nil))
		decodeArray(t, h.do(http.MethodGet, "/api/news", nil))
		decodeArray(t, h.do(http.MethodGet, "/api/learning", nil))
		requireKeys(t, decodeObject(t, h.do(http.MethodGet, "/api/pnl-summary", nil)), "total_pnl", "by_day", "by_market")
	})
}

// TestResetRefusedInLive covers the live-mode guard on position reset.
func TestResetRefusedInLive(t *testing.T) {
	h := newHarness(t, harnessConfig{mutate: func(s *config.Settings) { s.TradingEnvironment = "live" }})

	t.Run("GIVEN live mode WHEN positions are reset THEN 403 is returned", func(t *testing.T) {
		rec := h.do(http.MethodPost, "/api/positions/reset", nil)
		if rec.Code != http.StatusForbidden || decodeObject(t, rec)["detail"] != "Position reset is not allowed in live mode" {
			t.Errorf("unexpected response %d %s", rec.Code, rec.Body.String())
		}
	})
}

// TestLivePositionEndpoints covers the guards and the forget action for live positions.
func TestLivePositionEndpoints(t *testing.T) {
	const positionID = "3fa85f64-5717-4562-b3fc-2c963f66afa6"
	h := newHarness(t, harnessConfig{
		mutate: func(s *config.Settings) { s.TradingEnvironment = "live" },
		seed: func(repo *storage.Repository) {
			_ = repo.SaveLivePosition(context.Background(), storage.LivePosition{
				PositionID: positionID, Market: "BTC/EUR", Status: "open", Size: 0.002, AvgPrice: 50_000, OpenedAt: time.Now().UTC(),
			})
		},
	})

	t.Run("GIVEN a live position WHEN its market's live flag is turned off THEN 409 names the position", func(t *testing.T) {
		if rec := h.do(http.MethodPost, "/api/control/markets/BTC%2FEUR/live-toggle", nil); rec.Code != http.StatusOK {
			t.Fatalf("turning live on must be allowed, got %d %s", rec.Code, rec.Body.String())
		}
		rec := h.do(http.MethodPost, "/api/control/markets/BTC%2FEUR/live-toggle", nil)
		if rec.Code != http.StatusConflict || !strings.Contains(decodeObject(t, rec)["detail"].(string), "3fa85f64") {
			t.Errorf("unexpected response %d %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("GIVEN a live position WHEN switching to paper THEN 409 names the position", func(t *testing.T) {
		rec := h.doJSON(http.MethodPost, "/api/control/trading-environment", `{"environment":"paper"}`)
		if rec.Code != http.StatusConflict || !strings.Contains(decodeObject(t, rec)["detail"].(string), "BTC/EUR") {
			t.Errorf("unexpected response %d %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("GIVEN a live position WHEN it is forgotten THEN it is removed without an order and the switch to paper is allowed", func(t *testing.T) {
		body := decodeObject(t, h.do(http.MethodPost, "/api/positions/"+positionID[:8]+"/forget", nil))
		if body["status"] != "forgotten" || body["position_id"] != positionID {
			t.Fatalf("unexpected forget response %v", body)
		}
		if n := h.live.ExecutedCount(); n != 0 {
			t.Errorf("forget must not send an order, got %d", n)
		}
		if rec := h.do(http.MethodPost, "/api/positions/"+positionID+"/forget", nil); rec.Code != http.StatusNotFound {
			t.Errorf("a second forget must be 404, got %d", rec.Code)
		}
		if rec := h.doJSON(http.MethodPost, "/api/control/trading-environment", `{"environment":"paper"}`); rec.Code != http.StatusOK {
			t.Errorf("with no live position the switch to paper is allowed, got %d %s", rec.Code, rec.Body.String())
		}
	})
}

// TestOHLCEndpoint covers cached candles with slash-containing markets.
func TestOHLCEndpoint(t *testing.T) {
	h := newHarness(t, harnessConfig{})

	t.Run("GIVEN no cached candles WHEN OHLC is requested THEN an empty payload echoes the interval", func(t *testing.T) {
		body := decodeObject(t, h.do(http.MethodGet, "/api/ohlc/BTC%2FEUR?interval=15", nil))
		if body["symbol"] != "BTC/EUR" || body["interval"] != float64(15) || len(body["candles"].([]any)) != 0 {
			t.Errorf("unexpected OHLC payload %v", body)
		}
		body = decodeObject(t, h.do(http.MethodGet, "/api/ohlc/ETH/EUR", nil))
		if body["symbol"] != "ETH/EUR" || body["interval"] != float64(5) {
			t.Errorf("default interval should be 5, got %v", body)
		}
	})

	t.Run("GIVEN a non-numeric interval WHEN OHLC is requested THEN 422 is returned", func(t *testing.T) {
		if rec := h.do(http.MethodGet, "/api/ohlc/BTC%2FEUR?interval=abc", nil); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("expected 422, got %d", rec.Code)
		}
	})
}

// TestCORS covers the CORS middleware.
func TestCORS(t *testing.T) {
	h := newHarness(t, harnessConfig{})

	t.Run("GIVEN an allowed origin WHEN a request is made THEN the origin and credentials are allowed", func(t *testing.T) {
		rec := h.do(http.MethodGet, "/health", map[string]string{"Origin": "http://localhost:8000"})
		if rec.Header().Get("Access-Control-Allow-Origin") != "http://localhost:8000" || rec.Header().Get("Access-Control-Allow-Credentials") != "true" {
			t.Errorf("CORS headers missing: %v", rec.Header())
		}
	})

	t.Run("GIVEN an allowed origin WHEN a preflight is sent THEN methods and requested headers are allowed", func(t *testing.T) {
		rec := h.do(http.MethodOptions, "/api/control/resume", map[string]string{
			"Origin": "http://localhost:8000", "Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "content-type",
		})
		if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Access-Control-Allow-Methods"), "POST") ||
			rec.Header().Get("Access-Control-Allow-Headers") != "content-type" {
			t.Errorf("unexpected preflight %d %v", rec.Code, rec.Header())
		}
	})

	t.Run("GIVEN a foreign origin WHEN requests are made THEN no CORS grant is given", func(t *testing.T) {
		rec := h.do(http.MethodGet, "/health", map[string]string{"Origin": "http://evil.example"})
		if rec.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("foreign origin must not be allowed")
		}
		rec = h.do(http.MethodOptions, "/health", map[string]string{"Origin": "http://evil.example", "Access-Control-Request-Method": "GET"})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("foreign preflight should be refused, got %d", rec.Code)
		}
	})

	t.Run("GIVEN a page on a foreign origin WHEN it posts a state change THEN it is refused and nothing changes", func(t *testing.T) {
		// A plain cross-site POST needs no preflight, so the server must refuse it itself.
		rec := h.do(http.MethodPost, "/api/control/emergency-stop", map[string]string{"Origin": "http://evil.example"})
		if rec.Code != http.StatusForbidden {
			t.Errorf("foreign-origin POST should be 403, got %d", rec.Code)
		}
		if h.app.ControlSnapshot().EmergencyStop {
			t.Errorf("a refused request must not change the control state")
		}
	})

	t.Run("GIVEN the dashboard's own origin, an allowed origin or no origin WHEN a state change is posted THEN it is accepted", func(t *testing.T) {
		origins := map[string]string{
			"same origin as the Host header": "http://example.com", // httptest's default Host
			"allowed origin":                 "http://localhost:8000",
			"no origin (CLI or script)":      "",
		}
		for name, origin := range origins {
			headers := map[string]string{}
			if origin != "" {
				headers["Origin"] = origin
			}
			if rec := h.do(http.MethodPost, "/api/control/resume", headers); rec.Code != http.StatusOK {
				t.Errorf("%s: expected 200, got %d", name, rec.Code)
			}
		}
	})

	t.Run("GIVEN a foreign origin WHEN it reads data THEN the read still works without a CORS grant", func(t *testing.T) {
		rec := h.do(http.MethodGet, "/health", map[string]string{"Origin": "http://evil.example"})
		if rec.Code != http.StatusOK {
			t.Errorf("reads are not blocked server-side, got %d", rec.Code)
		}
	})
}
