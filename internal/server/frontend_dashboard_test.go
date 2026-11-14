package server

import (
	"strings"
	"testing"
)

// TestFrontendDashboard covers price precision, the offline state, polling and
// the candle-chart lifecycle of the dashboard page.
func TestFrontendDashboard(t *testing.T) {
	index := readFrontend(t, "index.html")
	approvals := readFrontend(t, "approvals.html")
	sw := readFrontend(t, "sw.js")

	t.Run("GIVEN sub-cent coins WHEN prices are rendered THEN one adaptive helper formats every price", func(t *testing.T) {
		mustContain(t, "index", index, "function priceDecimals(", "function fmtPrice(", "3 - Math.floor(Math.log10(")
		mustContain(t, "markets", between(t, index, "renderMarkets(markets, disabledMarkets) {", "renderSignals(signals) {"), "fmtPrice(m.price_value)")
		mustContain(t, "positions", between(t, index, "_renderPositionRows(positions) {", "renderApprovals(approvals) {"),
			"fmtPrice(p.avg_price_value ?? p.avg_price)")
		mustContain(t, "ledger", between(t, index, "_renderLedgerRows(trades) {", "async loadClosedTrades() {"), "fmtPrice(t.price)")
		mustContain(t, "closed", between(t, index, "_renderClosedRows(trades) {", "async showSignal("), "fmtPrice(t.entry_price)")
		charts := between(t, index, "// ── Candle charts", "async loadLedger() {")
		mustContain(t, "charts", charts, "fmtPrice(last.close)", "priceFormat: { type: 'price', precision, minMove }")
		mustNotContain(t, "index", index, "priceFmt(", "fmt2(p.avg_price)", "fmt2(t.price)", "fmt2(t.entry_price)")
	})

	t.Run("GIVEN the bot stops answering WHEN the dashboard polls THEN a stale banner replaces the Live indicator", func(t *testing.T) {
		mustContain(t, "index", index, `x-show="connectionLost"`, "Connection to the bot lost", "showing data from",
			"const STALE_AFTER_MS", "setInterval(() => this._checkStale(), 5000)")
		mustNotContain(t, "index", index, `x-show="_swActive"`)
		mustContain(t, "live indicator", between(t, index, "<!-- Live indicator", "</span>"), "!connectionLost")
		// Trigger 1: the service worker's offline signal.
		mustContain(t, "sw handler", between(t, index, "_handleSwMessage(msg) {", "_applyDashboard(d) {"),
			"msg.type === 'API_OFFLINE'", "this._markConnectionLost()")
		// Trigger 2: time since the last successful dashboard response (works without a service worker).
		mustContain(t, "stale check", between(t, index, "_checkStale() {", "\n                },"),
			"Date.now() - this.lastDashboardAt", "STALE_AFTER_MS", "this._markConnectionLost()")
		load := between(t, index, "async loadDashboard() {", "async loadNews() {")
		mustContain(t, "loadDashboard", load, "this._markConnected()", "catch", "this._markConnectionLost()")
	})

	t.Run("GIVEN stale data WHEN an action button is used THEN the operator is warned before anything is sent", func(t *testing.T) {
		mustContain(t, "index", index, "_actionAllowedWhileStale() {")
		for _, fn := range []string{"async toggleMarket(market) {", "async toggleMarketLive(market) {",
			"async closePosition(", "async forgetPosition(", "async decideApproval(", "async resetPositions() {", "async resumeBot() {"} {
			body := between(t, index, fn, "\n                },")
			mustContain(t, fn, body, "this._actionAllowedWhileStale()")
			mustAppearBefore(t, fn, body, "this._actionAllowedWhileStale()", "fetch(")
		}
	})

	t.Run("GIVEN the bot is unreachable WHEN the service worker handles an API request THEN it never serves cached data as live", func(t *testing.T) {
		api := between(t, sw, "async function networkOnlyApi(", "async function networkFirst(")
		mustNotContain(t, "sw api", api, "cache.match", "cache.put")
		mustContain(t, "sw api", api, "status: 503", "API_OFFLINE")
	})

	t.Run("GIVEN a dashboard poll WHEN its response arrives THEN it is applied once and only by the page that asked", func(t *testing.T) {
		mustNotContain(t, "sw.js", sw, "API_UPDATE", "includeUncontrolled")
		mustNotContain(t, "index", index, "msg.type !== 'API_UPDATE'", "case '/api/dashboard':")
		if n := strings.Count(index, "this._applyDashboard("); n != 1 {
			t.Errorf("_applyDashboard must be called from exactly one place, found %d", n)
		}
		load := between(t, index, "async loadDashboard() {", "async loadNews() {")
		mustContain(t, "loadDashboard", load, "++this._dashboardRequestSeq", "seq < this._dashboardAppliedSeq")
		mustAppearBefore(t, "loadDashboard", load, "seq < this._dashboardAppliedSeq", "this._applyDashboard(")
	})

	t.Run("GIVEN the market list changes WHEN charts sync THEN dropped markets lose their chart and card and each market has one request in flight", func(t *testing.T) {
		charts := between(t, index, "// ── Candle charts", "async loadLedger() {")
		mustContain(t, "charts", charts, "_syncChartCards(symbols) {", "_removeChartCard(market) {",
			"entry.chart5.remove()", "entry.chart15.remove()", "entry.card.remove()",
			"chartRequests.get(market)", "chartRequests.set(market", "chartRequests.delete(market)",
			"new IntersectionObserver(", "setTimeout(")
		mustNotContain(t, "charts", charts, "_chartMarkets.push(")
	})

	t.Run("GIVEN an OHLC response WHEN a chart renders THEN the status is checked and candles are sorted and de-duplicated", func(t *testing.T) {
		render := between(t, index, "async _fetchAndRender(market) {", "\n                },")
		mustAppearBefore(t, "_fetchAndRender", render, "!r5.ok || !r15.ok", "r5.json()")
		mustContain(t, "index", index, "function toCandleSeries(", ".sort((a, b) => a.time - b.time)", "new Map()")
	})

	t.Run("GIVEN the dashboard starts WHEN the first loads run THEN the dashboard (and its currency) is loaded first", func(t *testing.T) {
		start := between(t, index, "async _initialLoad() {", "\n                },")
		mustAppearBefore(t, "_initialLoad", start, "await this.loadDashboard();", "this.loadLedger();")
		mustAppearBefore(t, "_initialLoad", start, "await this.loadDashboard();", "this.loadClosedTrades();")
		mustAppearBefore(t, "_initialLoad", start, "await this.loadDashboard();", "this.loadPnlSummary();")
	})

	t.Run("GIVEN a closed trade without an exit reason WHEN the table renders THEN it does not throw", func(t *testing.T) {
		mustNotContain(t, "index", index, "t.exit_reason.replace(")
		mustContain(t, "index", index, "(t.exit_reason || '').replace(")
	})

	t.Run("GIVEN the trade ledger WHEN its header and rows are inspected THEN there is one Strategy column", func(t *testing.T) {
		body := strings.Index(index, `id="ledger-body"`)
		start := strings.LastIndex(index[:body], "<table")
		if n := strings.Count(index[start:body], ">Strategy</th>"); n != 1 {
			t.Errorf("ledger header has %d Strategy columns, want 1", n)
		}
		rows := between(t, index, "_renderLedgerRows(trades) {", "async loadClosedTrades() {")
		if n := strings.Count(rows, "${strategyLabel}"); n != 1 {
			t.Errorf("ledger rows have %d strategy cells, want 1", n)
		}
	})

	t.Run("GIVEN the pages WHEN their scripts are inspected THEN the unused htmx library is not loaded", func(t *testing.T) {
		mustNotContain(t, "index", index, "htmx")
		mustNotContain(t, "approvals", approvals, "htmx")
	})

	t.Run("GIVEN a live environment WHEN the markets table renders THEN each market has a confirmed Live toggle", func(t *testing.T) {
		markets := between(t, index, "renderMarkets(markets, disabledMarkets) {", "renderSignals(signals) {")
		mustContain(t, "markets", markets, `data-toggle-live="${esc(m.symbol)}"`, "this.environment === 'live'", "m.live")
		mustNotContain(t, "markets", markets, "onclick=")
		mustContain(t, "index", index, "e.target.closest('[data-toggle-live]')", "this.toggleMarketLive(")
		toggle := between(t, index, "async toggleMarketLive(market) {", "\n                },")
		mustContain(t, "toggleMarketLive", toggle, "confirm(", "${market}", "real orders",
			"fetch(`/api/control/markets/${encodeURIComponent(market)}/live-toggle`", "response.ok", "catch", "alert(")
		mustAppearBefore(t, "toggleMarketLive", toggle, "confirm(", "fetch(")
	})

	t.Run("GIVEN live positions in the dashboard WHEN the positions table renders THEN they carry a LIVE badge and their pending status, and Close is disabled while an order is pending", func(t *testing.T) {
		rows := between(t, index, "_renderPositionRows(positions) {", "renderApprovals(approvals) {")
		mustContain(t, "positions", rows, "p.environment === 'live'", `class="pill pill-blue"`, ">LIVE</span>",
			"esc(statusLabel)", "const closable = !isLive || p.status === 'open'", "${closable ? '' : 'disabled'}")
		mustNotContain(t, "positions", rows, "onclick=")
	})

	t.Run("GIVEN a live position WHEN the operator forgets it THEN a confirmed request removes it without sending an order", func(t *testing.T) {
		rows := between(t, index, "_renderPositionRows(positions) {", "renderApprovals(approvals) {")
		mustContain(t, "positions", rows, `data-forget-position="${esc(p.position_id_full)}"`, "isLive ?")
		mustContain(t, "index", index, "e.target.closest('[data-forget-position]')", "this.forgetPosition(")
		forget := between(t, index, "async forgetPosition(", "\n                },")
		mustContain(t, "forgetPosition", forget, "this._actionAllowedWhileStale()", "confirm(", "No order is sent",
			"fetch(`/api/positions/${encodeURIComponent(positionIdFull)}/forget`", "r.ok", "alert(", "catch", "this.loadDashboard()")
		mustAppearBefore(t, "forgetPosition", forget, "this._actionAllowedWhileStale()", "confirm(")
		mustAppearBefore(t, "forgetPosition", forget, "confirm(", "fetch(")
	})

	t.Run("GIVEN a live position WHEN it is closed from the dashboard THEN the operator is told the sell was sent and the P&L follows once Kraken confirms", func(t *testing.T) {
		closeFn := between(t, index, "async closePosition(", "\n                },")
		mustContain(t, "closePosition", closeFn, "d.status === 'closing'", "sell order was sent", "once Kraken confirms")
		mustAppearBefore(t, "closePosition", closeFn, "d.status === 'closing'", "Position closed at")
	})

	t.Run("GIVEN the approve API WHEN an approval cannot execute THEN both pages explain the 503 and 409 answers instead of a removed status", func(t *testing.T) {
		pages := map[string]string{
			"decideApproval": between(t, index, "async decideApproval(", "\n                },"),
			"decide":         between(t, approvals, "async function decide(", "// Approve / Reject buttons"),
		}
		for name, body := range pages {
			mustNotContain(t, name, body, "approved_no_price", "d.status !== 'approved'")
			mustContain(t, name, body, "r.status === 503", "still pending", "r.status === 409", "d.environment")
			mustAppearBefore(t, name, body, "r.status === 503", "!r.ok")
		}
	})

	t.Run("GIVEN the approvals page WHEN its buttons render THEN no server value is placed in an inline handler", func(t *testing.T) {
		mustNotContain(t, "approvals", approvals, "onclick=", "${a.id}")
		mustContain(t, "approvals", approvals, `data-approval-id="${esc(a.id)}"`, `data-approval-action="approve"`,
			`data-approval-action="reject"`, "e.target.closest('[data-approval-action]')")
	})
}
