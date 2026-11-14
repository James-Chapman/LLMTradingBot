// Package server exposes the bot's HTTP API and dashboard pages — the Go
// port of the FastAPI routes in backend/main.py. Paths, methods, status codes
// and JSON keys match main.py so the existing frontend works unchanged.
package server

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/James-Chapman/LLMTradingBot/internal/app"
	"github.com/James-Chapman/LLMTradingBot/internal/observability"
	"github.com/James-Chapman/LLMTradingBot/internal/storage"
)

// noStore disables browser caching for the HTML shells and service worker.
const noStore = "no-store, max-age=0"

// Limits used by the read endpoints (as in main.py).
const (
	tradeLedgerLimit    = 200
	rejectedTradesLimit = 100
	closedTradesLimit   = 200
	csvExportLimit      = 10_000
)

// csvFields are the closed-trade columns written by the CSV export, in order.
var csvFields = []string{
	"strategy", "market", "direction", "entry_price", "exit_price", "size", "pnl", "pnl_pct",
	"confidence", "exit_reason", "entry_at", "exit_at", "position_id", "trade_idea_id", "closing_trade_idea_id",
}

// Options configures the HTTP handler.
type Options struct {
	// FrontendDir holds index.html, approvals.html, sw.js and static/
	// (default "frontend", relative to the working directory).
	FrontendDir string
	Logger      *slog.Logger
}

// handler serves the API for one App.
type handler struct {
	app         *app.App
	frontendDir string
	logger      *slog.Logger
}

// New returns the bot's HTTP handler wrapped in the CORS middleware.
func New(a *app.App, opts Options) http.Handler {
	h := &handler{app: a, frontendDir: opts.FrontendDir, logger: opts.Logger}
	if h.frontendDir == "" {
		h.frontendDir = "frontend"
	}
	if h.logger == nil {
		h.logger = observability.Logger("server")
	}
	return corsMiddleware(a.Settings().CORSOrigins, h.routes())
}

// routes registers every endpoint main.py exposed.
func (h *handler) routes() *http.ServeMux {
	mux := http.NewServeMux()

	// Pages
	mux.HandleFunc("GET /{$}", h.page("index.html"))
	mux.HandleFunc("GET /approvals", h.page("approvals.html"))
	mux.HandleFunc("GET /sw.js", h.serviceWorker)
	mux.Handle("GET /static/", h.staticFiles())

	// Dashboard and approvals
	mux.HandleFunc("GET /api/dashboard", h.dashboard)
	mux.HandleFunc("GET /api/approvals", h.listApprovals)
	mux.HandleFunc("POST /api/approvals/{approval_id}/approve", h.approve)
	mux.HandleFunc("POST /api/approvals/{approval_id}/reject", h.reject)

	// Control
	mux.HandleFunc("GET /api/control", h.getControl)
	mux.HandleFunc("POST /api/control/emergency-stop", h.emergencyStop)
	mux.HandleFunc("POST /api/control/resume", h.resume)
	mux.HandleFunc("POST /api/control/markets/{market...}", h.marketAction)
	mux.HandleFunc("POST /api/control/trading-mode", h.setTradingMode)
	mux.HandleFunc("POST /api/control/trading-environment", h.setTradingEnvironment)
	mux.HandleFunc("POST /api/control/strategies/{strategy_id}/toggle", h.toggleStrategy)
	mux.HandleFunc("POST /api/control/strategies/{strategy_id}/select", h.selectStrategy)
	mux.HandleFunc("POST /api/control/strategies/reload", h.reloadStrategies)

	// Settings
	mux.HandleFunc("GET /api/settings", h.getSettings)
	mux.HandleFunc("POST /api/settings", h.updateSettings)

	// Misc
	mux.HandleFunc("GET /api/learning", h.learning)
	mux.HandleFunc("GET /api/ohlc/{market...}", h.ohlc)
	mux.HandleFunc("GET /api/trades", h.trades)
	mux.HandleFunc("GET /api/rejected-trades", h.rejectedTrades)
	mux.HandleFunc("GET /api/closed-trades", h.closedTrades)
	mux.HandleFunc("GET /api/pnl-summary", h.pnlSummary)
	mux.HandleFunc("GET /api/export/trades.csv", h.exportTradesCSV)
	mux.HandleFunc("GET /api/signals/{trade_idea_id}", h.signalDetail)
	mux.HandleFunc("POST /api/positions/{position_id}/close", h.closePosition)
	mux.HandleFunc("POST /api/positions/reset", h.resetPositions)
	mux.HandleFunc("GET /api/news", h.news)
	mux.HandleFunc("GET /api/health", h.apiHealth)
	mux.HandleFunc("GET /health", h.health)
	return mux
}

// ---------------------------------------------------------------------------
// Response helpers
// ---------------------------------------------------------------------------

// writeJSON writes v as a JSON response with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		http.Error(w, `{"detail":"Internal Server Error"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// writeDetail writes a FastAPI-style {"detail": ...} error body.
func writeDetail(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, map[string]string{"detail": detail})
}

// writeError maps an app error to its HTTP status, or 500 for unexpected errors.
func (h *handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var se *app.StatusError
	if errors.As(err, &se) {
		writeDetail(w, se.Status, se.Detail)
		return
	}
	h.logger.Error("Request failed", "path", r.URL.Path, "error", err.Error())
	writeDetail(w, http.StatusInternalServerError, "Internal Server Error")
}

// writeResult writes v as JSON, or the error when err is set.
func (h *handler) writeResult(w http.ResponseWriter, r *http.Request, v any, err error) {
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// nonNil returns an empty slice instead of nil so JSON shows [] not null.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// ---------------------------------------------------------------------------
// Pages
// ---------------------------------------------------------------------------

// page serves an HTML shell without browser caching so frontend fixes apply immediately.
func (h *handler) page(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(h.frontendDir, name)
		if _, err := os.Stat(path); err != nil {
			writeDetail(w, http.StatusNotFound, "Not Found")
			return
		}
		w.Header().Set("Cache-Control", noStore)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.ServeFile(w, r, path)
	}
}

// serviceWorker serves sw.js from the root scope as uncached JavaScript.
func (h *handler) serviceWorker(w http.ResponseWriter, r *http.Request) {
	content, err := os.ReadFile(filepath.Join(h.frontendDir, "sw.js"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/javascript")
	w.Header().Set("Cache-Control", noStore)
	_, _ = w.Write(content)
}

// staticFiles serves frontend/static under /static/ without directory listings.
func (h *handler) staticFiles() http.Handler {
	files := http.StripPrefix("/static/", http.FileServer(http.Dir(filepath.Join(h.frontendDir, "static"))))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			writeDetail(w, http.StatusNotFound, "Not Found")
			return
		}
		files.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// Dashboard and approvals
// ---------------------------------------------------------------------------

// dashboard returns the dashboard payload, or 304 when the ETag still matches.
func (h *handler) dashboard(w http.ResponseWriter, r *http.Request) {
	etag := strconv.FormatInt(h.app.DashboardVersion(), 10)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", etag)
	defer func() {
		if rec := recover(); rec != nil {
			h.logger.Error("Failed to get dashboard data", "error", fmt.Sprint(rec))
			writeJSON(w, http.StatusOK, h.app.FallbackDashboard())
		}
	}()
	writeJSON(w, http.StatusOK, h.app.Dashboard())
}

// listApprovals returns the pending approvals.
func (h *handler) listApprovals(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.app.PendingApprovals())
}

// approve executes a pending approval.
func (h *handler) approve(w http.ResponseWriter, r *http.Request) {
	result, err := h.app.ApproveTrade(r.Context(), r.PathValue("approval_id"))
	h.writeResult(w, r, result, err)
}

// reject declines a pending approval.
func (h *handler) reject(w http.ResponseWriter, r *http.Request) {
	result, err := h.app.RejectTrade(r.PathValue("approval_id"))
	h.writeResult(w, r, result, err)
}

// ---------------------------------------------------------------------------
// Control
// ---------------------------------------------------------------------------

// getControl returns the operator control state.
func (h *handler) getControl(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.app.ControlSnapshot())
}

// emergencyStop halts trading and clears the approval queue.
func (h *handler) emergencyStop(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.app.ActivateEmergencyStop(r.Context()))
}

// resume clears the emergency stop.
func (h *handler) resume(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.app.Resume())
}

// Request body limits for the endpoints that accept JSON.
const (
	maxTradingBody  = 1 << 10  // trading mode / environment
	maxSettingsBody = 64 << 10 // dashboard settings
)

// decodeBody decodes a JSON request body of at most limit bytes into dst,
// writing a 400 and returning false when it is missing or malformed.
func decodeBody(w http.ResponseWriter, r *http.Request, limit int64, dst any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, limit)).Decode(dst); err != nil {
		writeDetail(w, http.StatusBadRequest, "Request body must be JSON")
		return false
	}
	return true
}

// setTradingMode changes the trading mode: {"mode": "manual|semi_automated|fully_automated"}.
func (h *handler) setTradingMode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode string `json:"mode"`
	}
	if !decodeBody(w, r, maxTradingBody, &body) {
		return
	}
	result, err := h.app.SetTradingMode(body.Mode)
	h.writeResult(w, r, result, err)
}

// setTradingEnvironment switches paper/live: {"environment": "paper|live",
// "confirmation": "LIVE"}. The confirmation is only required for live.
func (h *handler) setTradingEnvironment(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Environment  string `json:"environment"`
		Confirmation string `json:"confirmation"`
	}
	if !decodeBody(w, r, maxTradingBody, &body) {
		return
	}
	result, err := h.app.SetTradingEnvironment(body.Environment, body.Confirmation)
	h.writeResult(w, r, result, err)
}

// getSettings lists every dashboard setting. Secret values are never sent.
func (h *handler) getSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.app.SettingsView())
}

// updateSettings saves changed settings: {"changes": {"KEY": "value", ...}}.
func (h *handler) updateSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Changes map[string]string `json:"changes"`
	}
	if !decodeBody(w, r, maxSettingsBody, &body) {
		return
	}
	result, err := h.app.UpdateSettings(r.Context(), body.Changes)
	h.writeResult(w, r, result, err)
}

// marketAction handles /api/control/markets/{market}/toggle and /live-toggle.
// Market symbols contain a slash ("BTC/EUR", sent encoded as BTC%2FEUR), so
// the route captures the remainder of the path and the action is the last
// segment.
func (h *handler) marketAction(w http.ResponseWriter, r *http.Request) {
	rest := r.PathValue("market")
	market, action, ok := cutLast(rest, "/")
	if !ok || market == "" {
		writeDetail(w, http.StatusNotFound, "Not Found")
		return
	}
	switch action {
	case "toggle":
		writeJSON(w, http.StatusOK, h.app.ToggleMarket(market))
	case "live-toggle":
		writeJSON(w, http.StatusOK, h.app.ToggleMarketLive(market))
	default:
		writeDetail(w, http.StatusNotFound, "Not Found")
	}
}

// cutLast splits s around the last sep.
func cutLast(s, sep string) (before, after string, found bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+len(sep):], true
}

// toggleStrategy selects a strategy (only one strategy is active at a time).
func (h *handler) toggleStrategy(w http.ResponseWriter, r *http.Request) {
	id, err := h.app.SelectStrategy(r.PathValue("strategy_id"))
	h.writeResult(w, r, map[string]any{"strategy_id": id, "enabled": true, "selected": true}, err)
}

// selectStrategy makes a strategy the active one.
func (h *handler) selectStrategy(w http.ResponseWriter, r *http.Request) {
	id, err := h.app.SelectStrategy(r.PathValue("strategy_id"))
	h.writeResult(w, r, map[string]any{"strategy_id": id, "selected": true}, err)
}

// reloadStrategies rebuilds the strategy instances.
func (h *handler) reloadStrategies(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "reloaded", "strategies": h.app.ReloadStrategies()})
}

// ---------------------------------------------------------------------------
// Misc
// ---------------------------------------------------------------------------

// learning returns the learner summary.
func (h *handler) learning(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, nonNil(h.app.LearningSummary()))
}

// ohlc returns cached candles; interval is 5 (default) or 15 minutes.
func (h *handler) ohlc(w http.ResponseWriter, r *http.Request) {
	interval := 5
	if raw := r.URL.Query().Get("interval"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": []map[string]any{{
				"type": "int_parsing", "loc": []string{"query", "interval"},
				"msg": "Input should be a valid integer, unable to parse string as an integer", "input": raw,
			}}})
			return
		}
		interval = v
	}
	writeJSON(w, http.StatusOK, h.app.OHLC(r.PathValue("market"), interval))
}

// trades returns the trade ledger, newest first.
func (h *handler) trades(w http.ResponseWriter, r *http.Request) {
	rows, err := h.app.Repository().GetTradeLedger(r.Context(), tradeLedgerLimit)
	h.writeResult(w, r, nonNil(rows), err)
}

// rejectedTrades returns execution-rejected orders, newest first.
func (h *handler) rejectedTrades(w http.ResponseWriter, r *http.Request) {
	rows, err := h.app.Repository().GetRejectedTrades(r.Context(), rejectedTradesLimit)
	h.writeResult(w, r, nonNil(rows), err)
}

// closedTrades returns fully closed positions, newest first.
func (h *handler) closedTrades(w http.ResponseWriter, r *http.Request) {
	rows, err := h.app.Repository().GetClosedTrades(r.Context(), closedTradesLimit)
	h.writeResult(w, r, nonNil(rows), err)
}

// pnlSummary returns realised P&L grouped by day and market.
func (h *handler) pnlSummary(w http.ResponseWriter, r *http.Request) {
	summary, err := h.app.Repository().GetPnLSummary(r.Context(), 0)
	h.writeResult(w, r, summary, err)
}

// exportTradesCSV downloads the closed-trade history as CSV.
func (h *handler) exportTradesCSV(w http.ResponseWriter, r *http.Request) {
	rows, err := h.app.Repository().GetClosedTrades(r.Context(), csvExportLimit)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)
	writer.UseCRLF = true // matches Python's csv module
	_ = writer.Write(csvFields)
	for _, t := range rows {
		_ = writer.Write(closedTradeRecord(t))
	}
	writer.Flush()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=trades.csv")
	_, _ = w.Write(buf.Bytes())
}

// closedTradeRecord renders one closed trade in csvFields order.
func closedTradeRecord(t storage.ClosedTrade) []string {
	confidence := ""
	if t.Confidence != nil {
		confidence = pyFloat(*t.Confidence)
	}
	return []string{
		t.Strategy, t.Market, t.Direction, pyFloat(t.EntryPrice), pyFloat(t.ExitPrice), pyFloat(t.Size),
		pyFloat(t.PnL), pyFloat(t.PnLPct), confidence, t.ExitReason, t.EntryAt, t.ExitAt,
		t.PositionID, t.TradeIdeaID, t.ClosingTradeIdeaID,
	}
}

// pyFloat formats a float like Python's str(float): shortest repr, always
// with a decimal point, scientific notation outside [1e-4, 1e16).
func pyFloat(v float64) string {
	abs := v
	if abs < 0 {
		abs = -abs
	}
	if abs != 0 && (abs < 1e-4 || abs >= 1e16) {
		s := strconv.FormatFloat(v, 'e', -1, 64)
		mantissa, exp, _ := strings.Cut(s, "e")
		sign := exp[:1]
		digits := strings.TrimLeft(exp[1:], "0")
		if len(digits) < 2 {
			digits = strings.Repeat("0", 2-len(digits)) + digits
		}
		return mantissa + "e" + sign + digits
	}
	s := strconv.FormatFloat(v, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// signalDetail returns the full context of one trade idea.
func (h *handler) signalDetail(w http.ResponseWriter, r *http.Request) {
	detail, err := h.app.Repository().GetSignalDetail(r.Context(), r.PathValue("trade_idea_id"))
	if err == nil && detail == nil {
		writeDetail(w, http.StatusNotFound, "Signal not found")
		return
	}
	h.writeResult(w, r, detail, err)
}

// closePosition manually closes one open paper position.
func (h *handler) closePosition(w http.ResponseWriter, r *http.Request) {
	result, err := h.app.ClosePosition(r.Context(), r.PathValue("position_id"))
	h.writeResult(w, r, result, err)
}

// resetPositions closes and clears every open paper position.
func (h *handler) resetPositions(w http.ResponseWriter, r *http.Request) {
	count, err := h.app.ResetPositions(r.Context())
	h.writeResult(w, r, map[string]int{"cleared": count}, err)
}

// news returns the cached latest news.
func (h *handler) news(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.app.News())
}

// apiHealth returns LLM availability.
func (h *handler) apiHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.app.LLMHealth())
}

// health reports liveness and the app version.
func (h *handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "healthy", "version": app.Version})
}

// ---------------------------------------------------------------------------
// CORS
// ---------------------------------------------------------------------------

// corsAllowMethods mirrors Starlette's allow_methods=["*"] expansion.
const corsAllowMethods = "DELETE, GET, HEAD, OPTIONS, PATCH, POST, PUT"

// corsMiddleware ports Starlette's CORSMiddleware with allow_credentials,
// all methods and all headers allowed for the configured origins.
func corsMiddleware(origins []string, next http.Handler) http.Handler {
	allowAll := slices.Contains(origins, "*")
	allowed := func(origin string) bool { return allowAll || slices.Contains(origins, origin) }

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}
		headers := w.Header()
		headers.Add("Vary", "Origin")

		// Preflight request.
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			if !allowed(origin) {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte("Disallowed CORS origin"))
				return
			}
			headers.Set("Access-Control-Allow-Origin", origin)
			headers.Set("Access-Control-Allow-Credentials", "true")
			headers.Set("Access-Control-Allow-Methods", corsAllowMethods)
			headers.Set("Access-Control-Max-Age", "600")
			if reqHeaders := r.Header.Get("Access-Control-Request-Headers"); reqHeaders != "" {
				headers.Set("Access-Control-Allow-Headers", reqHeaders)
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("OK"))
			return
		}

		if allowed(origin) {
			headers.Set("Access-Control-Allow-Origin", origin)
			headers.Set("Access-Control-Allow-Credentials", "true")
		}
		next.ServeHTTP(w, r)
	})
}
