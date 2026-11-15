// Package server exposes the bot's HTTP API and dashboard pages — the Go
// port of the FastAPI routes in backend/main.py. Paths, methods, status codes
// and JSON keys match main.py so the existing frontend works unchanged.
package server

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/app"
	"github.com/James-Chapman/LLMTradingBot/internal/config"
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

// TokenSource is the dashboard access token (app.AccessToken in production).
type TokenSource interface {
	Token() string
	Rotate(ctx context.Context) (string, error)
}

// SessionCookieName is the cookie set by POST /api/login.
const SessionCookieName = "tradingbot_session"

// defaultFailedLoginDelay slows down guessing: every wrong token waits this long.
const defaultFailedLoginDelay = time.Second

// Options configures the HTTP handler.
type Options struct {
	// FrontendDir holds index.html, approvals.html, login.html, sw.js and
	// static/ (default "frontend", relative to the working directory).
	FrontendDir string
	Logger      *slog.Logger
	// AllowedHosts are extra Host header names always accepted, on top of
	// those derived from the listen address and ALLOWED_HOSTS.
	AllowedHosts []string
	// Access is the access token required when the settings' Host is not a
	// loopback address. Without one such a server refuses every request
	// except /health and the login page.
	Access TokenSource
	// FailedLoginDelay is the wait after a wrong token (default 1 s).
	FailedLoginDelay time.Duration
}

// handler serves the API for one App.
type handler struct {
	app         *app.App
	frontendDir string
	logger      *slog.Logger
	// requireLogin is set when the bot listens on a non-loopback address.
	requireLogin bool
	access       TokenSource
	failDelay    time.Duration
}

// New returns the bot's HTTP handler: the Host check, then CORS, then (when
// the bot listens on a non-loopback address) the access-token check, then
// the routes.
func New(a *app.App, opts Options) http.Handler {
	settings := a.Settings()
	h := &handler{
		app:          a,
		frontendDir:  opts.FrontendDir,
		logger:       opts.Logger,
		requireLogin: !config.IsLoopbackHost(settings.Host),
		access:       opts.Access,
		failDelay:    opts.FailedLoginDelay,
	}
	if h.frontendDir == "" {
		h.frontendDir = "frontend"
	}
	if h.logger == nil {
		h.logger = observability.Logger("server")
	}
	if h.failDelay <= 0 {
		h.failDelay = defaultFailedLoginDelay
	}
	var next http.Handler = h.routes()
	if h.requireLogin {
		next = h.requireSession(next)
	}
	next = corsMiddleware(settings.CORSOrigins, next)
	return hostCheck(newHostPolicy(settings.Host, append(slices.Clone(opts.AllowedHosts), settings.AllowedHosts...)), next)
}

// routes registers every endpoint main.py exposed.
func (h *handler) routes() *http.ServeMux {
	mux := http.NewServeMux()

	// Pages
	mux.HandleFunc("GET /{$}", h.page("index.html"))
	mux.HandleFunc("GET /approvals", h.page("approvals.html"))
	mux.HandleFunc("GET /login", h.loginPage)

	// Access (only used when the bot listens on a non-loopback address)
	mux.HandleFunc("POST /api/login", h.login)
	mux.HandleFunc("POST /api/access-token/rotate", h.rotateToken)
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
	mux.HandleFunc("POST /api/positions/{position_id}/forget", h.forgetPosition)
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
	defer func() {
		if rec := recover(); rec != nil {
			h.logger.Error("Failed to get dashboard data", "error", fmt.Sprint(rec))
			writeJSON(w, http.StatusOK, h.app.FallbackDashboard())
		}
	}()
	view := h.app.Dashboard()
	// Only a real payload gets the ETag: a fallback sent with it would be
	// kept by the browser (304) until the next version change.
	w.Header().Set("ETag", etag)
	writeJSON(w, http.StatusOK, view)
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
	maxLoginBody    = 1 << 10  // login token
	maxSettingsBody = 64 << 10 // dashboard settings
)

// errTrailingData reports bytes after the JSON value in a request body.
var errTrailingData = errors.New("trailing data after the JSON value")

// decodeBody decodes a JSON request body of at most limit bytes into dst.
// It writes a 413 for an oversized body and a 400 for a missing or
// malformed one (including data after the JSON value), and returns false.
func decodeBody(w http.ResponseWriter, r *http.Request, limit int64, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	err := dec.Decode(dst)
	if err == nil && dec.Decode(&struct{}{}) != io.EOF {
		err = errTrailingData
	}
	var tooLarge *http.MaxBytesError
	switch {
	case err == nil:
		return true
	case errors.As(err, &tooLarge):
		writeDetail(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("Request body is larger than %d bytes", limit))
	default:
		writeDetail(w, http.StatusBadRequest, "Request body must be one JSON value")
	}
	return false
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
	// Only markets of the bot's universe may be saved into the control state.
	if (action == "toggle" || action == "live-toggle") && !h.app.KnownMarket(market) {
		writeDetail(w, http.StatusNotFound, "Market not found")
		return
	}
	switch action {
	case "toggle":
		writeJSON(w, http.StatusOK, h.app.ToggleMarket(market))
	case "live-toggle":
		result, err := h.app.ToggleMarketLive(market)
		h.writeResult(w, r, result, err)
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

// closePosition manually closes one open position: a paper position at the
// market price, or a live position by sending a real market sell ("closing").
func (h *handler) closePosition(w http.ResponseWriter, r *http.Request) {
	result, err := h.app.ClosePosition(r.Context(), r.PathValue("position_id"))
	h.writeResult(w, r, result, err)
}

// forgetPosition removes a live position from the bot's book without sending
// any order (the holding on Kraken is untouched).
func (h *handler) forgetPosition(w http.ResponseWriter, r *http.Request) {
	result, err := h.app.ForgetLivePosition(r.PathValue("position_id"))
	h.writeResult(w, r, result, err)
}

// resetPositions closes and logs every open paper position, one by one.
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
// all methods and all headers allowed for the configured origins. A "*"
// entry is never a wildcard (the settings refuse it): it would let every
// site read the API with the user's credentials.
func corsMiddleware(origins []string, next http.Handler) http.Handler {
	allowed := func(origin string) bool { return origin != "*" && slices.Contains(origins, origin) }

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
		} else if changesState(r.Method) && !sameOrigin(origin, r.Host) {
			// CORS headers only stop a foreign page reading the reply; a plain
			// cross-site POST is still delivered, so refuse it here.
			writeDetail(w, http.StatusForbidden, "Cross-origin request refused")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// changesState reports whether an HTTP method can modify the bot.
func changesState(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

// sameOrigin reports whether a browser Origin header names the host the
// request was sent to, i.e. the dashboard calling its own API.
func sameOrigin(origin, host string) bool {
	u, err := url.Parse(origin)
	return err == nil && u.Host != "" && strings.EqualFold(u.Host, host)
}

// ---------------------------------------------------------------------------
// Host check and access token
// ---------------------------------------------------------------------------

// hostPolicy lists the Host header names the server answers to.
type hostPolicy struct {
	names map[string]bool
	// anyIP accepts any IP-address Host. DNS rebinding always arrives with
	// the attacker's host name, never an IP literal, so this is safe; it
	// lets a LAN user open http://<this computer's IP>:<port>.
	anyIP bool
}

// newHostPolicy derives the accepted Host names from the listen address:
// loopback names always; for a non-loopback address also that address, this
// computer's name, extra, and any IP address.
func newHostPolicy(listenHost string, extra []string) hostPolicy {
	p := hostPolicy{names: map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}}
	for _, name := range extra {
		p.names[normalHost(name)] = true
	}
	if config.IsLoopbackHost(listenHost) {
		return p
	}
	p.anyIP = true
	if host := normalHost(listenHost); host != "" {
		p.names[host] = true
	}
	if name, err := os.Hostname(); err == nil && name != "" {
		p.names[normalHost(name)] = true
	}
	return p
}

// allows reports whether a request's Host header (host or host:port) is accepted.
func (p hostPolicy) allows(hostHeader string) bool {
	host := hostHeader
	if h, _, err := net.SplitHostPort(hostHeader); err == nil {
		host = h
	}
	host = normalHost(host)
	if p.names[host] {
		return true
	}
	return p.anyIP && net.ParseIP(host) != nil
}

// normalHost lower-cases a host name and drops IPv6 brackets and a trailing dot.
func normalHost(host string) string {
	return strings.TrimSuffix(strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]"), ".")
}

// hostCheck refuses (403) requests addressed to a host name the server is
// not reached by. This closes DNS rebinding: a foreign page whose name was
// re-pointed at this computer still sends its own name as the Host.
func hostCheck(policy hostPolicy, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !policy.allows(r.Host) {
			writeDetail(w, http.StatusForbidden, "Host not allowed. Add it under Settings → Advanced → Allowed host names")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// publicPaths are served without a session: liveness, the login page, its
// stylesheet and the login call itself.
var publicPaths = map[string]bool{
	"GET /health":             true,
	"GET /login":              true,
	"GET /static/styles.css":  true,
	"POST /api/login":         true,
	"HEAD /health":            true,
	"HEAD /static/styles.css": true,
}

// requireSession lets a request through when it carries the access token as
// "Authorization: Bearer <token>" or in the session cookie. Otherwise pages
// redirect to /login and everything else gets 401. A wrong bearer token
// waits failDelay before the answer.
func (h *handler) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if publicPaths[r.Method+" "+r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		if bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			if h.validToken(bearer) {
				next.ServeHTTP(w, r)
				return
			}
			h.waitAfterFailure(r.Context())
		} else if c, err := r.Cookie(SessionCookieName); err == nil && h.validToken(c.Value) {
			next.ServeHTTP(w, r)
			return
		}
		isPage := (r.Method == http.MethodGet || r.Method == http.MethodHead) && !strings.HasPrefix(r.URL.Path, "/api/")
		if isPage {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="tradingbot"`)
		writeDetail(w, http.StatusUnauthorized, "Access token required: log in at /login or send Authorization: Bearer <token>")
	})
}

// validToken compares candidate with the access token in constant time. It
// is false when no token is configured, so such a server admits nobody.
func (h *handler) validToken(candidate string) bool {
	if h.access == nil {
		return false
	}
	want := h.access.Token()
	return want != "" && subtle.ConstantTimeCompare([]byte(candidate), []byte(want)) == 1
}

// waitAfterFailure pauses after a wrong token so guessing is slow.
func (h *handler) waitAfterFailure(ctx context.Context) {
	timer := time.NewTimer(h.failDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// setSessionCookie stores token in the HttpOnly, SameSite=Strict session
// cookie (Secure over TLS). It lasts until the browser closes.
func setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
	})
}

// loginPage serves the token form; a loopback-bound bot needs no login and
// sends the browser to the dashboard.
func (h *handler) loginPage(w http.ResponseWriter, r *http.Request) {
	if !h.requireLogin {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	h.page("login.html")(w, r)
}

// login checks {"token": "..."} and sets the session cookie: 200 when the
// token is right, 401 (after the failure delay) when it is not.
func (h *handler) login(w http.ResponseWriter, r *http.Request) {
	if !h.requireLogin {
		writeDetail(w, http.StatusNotFound, "Login is not used: the dashboard listens on a loopback address")
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if !decodeBody(w, r, maxLoginBody, &body) {
		return
	}
	token := strings.TrimSpace(body.Token)
	if !h.validToken(token) {
		h.logger.Warn("Failed dashboard login", "remote", r.RemoteAddr)
		h.waitAfterFailure(r.Context())
		writeDetail(w, http.StatusUnauthorized, "Wrong access token")
		return
	}
	setSessionCookie(w, r, token)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// rotateToken replaces the access token (the caller already proved it holds
// the current one) and returns the new one, with a fresh session cookie.
// Every other session and script must use the new token from now on.
func (h *handler) rotateToken(w http.ResponseWriter, r *http.Request) {
	if !h.requireLogin || h.access == nil {
		writeDetail(w, http.StatusNotFound, "No access token is used: the dashboard listens on a loopback address")
		return
	}
	fresh, err := h.access.Rotate(r.Context())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.logger.Warn("Dashboard access token rotated", "remote", r.RemoteAddr)
	setSessionCookie(w, r, fresh)
	writeJSON(w, http.StatusOK, map[string]string{"token": fresh})
}
