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
	scripts := appScripts(t)
	core := scripts["core.js"]
	markets := scripts["markets.js"]
	trading := scripts["trading.js"]
	overview := scripts["overview.js"]
	all := dashboardSource(t)

	t.Run("GIVEN sub-cent coins WHEN prices are rendered THEN one adaptive helper formats every price", func(t *testing.T) {
		mustContain(t, "core.js", core, "function priceDecimals(", "function fmtPrice(", "3 - Math.floor(Math.log10(")
		mustContain(t, "markets", funcBody(t, markets, "marketPriceText(m) {"), "fmtPrice(m.price_value)")
		mustContain(t, "positions", between(t, index, `<tbody id="positions-body">`, "</tbody>"), "fmtPrice(p.avg_price_value ?? p.avg_price)")
		mustContain(t, "ledger", between(t, index, `<tbody id="ledger-body">`, "</tbody>"), "fmtPrice(t.price)")
		mustContain(t, "closed", between(t, index, `<tbody id="closed-body">`, "</tbody>"), "fmtPrice(t.entry_price)")
		render := funcBody(t, markets, "async _fetchAndRender(market) {")
		mustContain(t, "charts", render, "fmtPrice(last.close)", "priceFormat: { type: 'price', precision, minMove }")
		mustNotContain(t, "dashboard", all, "priceFmt(", "fmt2(p.avg_price)", "fmt2(t.price)", "fmt2(t.entry_price)")
	})

	t.Run("GIVEN money values WHEN they are formatted THEN they always have two decimals", func(t *testing.T) {
		mustContain(t, "fmtMoney", funcBody(t, core, "function fmtMoney(value) {"), "minimumFractionDigits: 2, maximumFractionDigits: 2")
		mustContain(t, "fmtSignedMoney", funcBody(t, core, "function fmtSignedMoney(value, symbol) {"), "fmtMoney(Math.abs(v))")
	})

	t.Run("GIVEN the bot stops answering WHEN the dashboard polls THEN a stale banner replaces the Live indicator", func(t *testing.T) {
		mustContain(t, "index", index, `x-show="connectionLost"`, "Connection to the bot lost", "showing data from")
		mustContain(t, "core.js", core, "const STALE_AFTER_MS", "setInterval(() => this._checkStale(), 5000)")
		mustNotContain(t, "index", index, `x-show="_swActive"`)
		mustContain(t, "live indicator", between(t, index, "<!-- Live indicator", "</span>"), "!connectionLost")
		// Trigger 1: the service worker's offline signal.
		mustContain(t, "sw handler", funcBody(t, core, "_handleSwMessage(msg) {"),
			"msg.type === 'API_OFFLINE'", "this._markConnectionLost()")
		// Trigger 2: time since the last successful dashboard response (works without a service worker).
		mustContain(t, "stale check", funcBody(t, core, "_checkStale() {"),
			"Date.now() - this.lastDashboardAt", "STALE_AFTER_MS", "this._markConnectionLost()")
		load := funcBody(t, core, "async loadDashboard() {")
		mustContain(t, "loadDashboard", load, "this._markConnected()", "catch", "this._markConnectionLost()")
	})

	t.Run("GIVEN stale data WHEN an action button is used THEN the operator is warned before anything is sent", func(t *testing.T) {
		mustContain(t, "core.js", core, "_actionAllowedWhileStale() {")
		actions := map[string]string{
			"async toggleMarket(market) {":     markets,
			"async toggleMarketLive(market) {": markets,
			"async closePosition(":             trading,
			"async forgetPosition(":            trading,
			"async resetPositions() {":         trading,
			"async decideApproval(":            overview,
			"async resumeBot() {":              core,
			"async setTradingMode(":            scripts["settings.js"],
			"async setTradingEnvironment(":     scripts["settings.js"],
			"async selectStrategy(":            scripts["settings.js"],
			"async saveSettings() {":           scripts["settings.js"],
		}
		for fn, src := range actions {
			body := funcBody(t, src, fn)
			mustContain(t, fn, body, "this._actionAllowedWhileStale()")
			mustAppearBefore(t, fn, body, "this._actionAllowedWhileStale()", "fetch(")
		}
		// The emergency stop is never held back by the stale-data question.
		mustNotContain(t, "activateStop", funcBody(t, core, "async activateStop() {"), "_actionAllowedWhileStale")
	})

	t.Run("GIVEN the bot is unreachable WHEN the service worker handles an API request THEN it never serves cached data as live", func(t *testing.T) {
		api := between(t, sw, "async function networkOnlyApi(", "function cacheable(")
		mustNotContain(t, "sw api", api, "cache.match", "cache.put")
		mustContain(t, "sw api", api, "status: 503", "API_OFFLINE")
	})

	t.Run("GIVEN a dashboard poll WHEN its response arrives THEN it is applied once and only by the page that asked", func(t *testing.T) {
		mustNotContain(t, "sw.js", sw, "API_UPDATE", "includeUncontrolled")
		mustNotContain(t, "dashboard", all, "msg.type !== 'API_UPDATE'", "case '/api/dashboard':")
		if n := strings.Count(all, "this._applyDashboard("); n != 1 {
			t.Errorf("_applyDashboard must be called from exactly one place, found %d", n)
		}
		load := funcBody(t, core, "async loadDashboard() {")
		mustContain(t, "loadDashboard", load, "++this._dashboardRequestSeq", "seq < this._dashboardAppliedSeq")
		mustAppearBefore(t, "loadDashboard", load, "seq < this._dashboardAppliedSeq", "this._applyDashboard(")
		// Every view reacts to the applied response through its on<Name>Data hook.
		mustContain(t, "_applyDashboard", funcBody(t, core, "_applyDashboard(d) {"), "this._callViewHooks('on', 'Data', d)")
	})

	t.Run("GIVEN overlapping loads of a view's table WHEN responses arrive out of order THEN only the newest is shown and a failed refresh keeps the old rows", func(t *testing.T) {
		fetchRes := funcBody(t, core, "async fetchResource(res, url) {")
		mustContain(t, "fetchResource", fetchRes, "const seq = ++res.seq", "if (seq !== res.seq) return;", "res.error = e.message",
			"if (!res.at) res.state = 'error'")
		mustAppearBefore(t, "fetchResource", fetchRes, "if (!r.ok)", "r.json()")
	})

	t.Run("GIVEN the selected market WHEN its charts load THEN it has one request in flight", func(t *testing.T) {
		load := funcBody(t, markets, "_loadChart(market) {")
		mustContain(t, "_loadChart", load, "chartRequests.has(market)", "chartRequests.get(market)", "chartRequests.set(market",
			"chartRequests.delete(market)")
		mustAppearBefore(t, "_loadChart", load, "chartRequests.has(market)", "this._fetchAndRender(market)")
	})

	t.Run("GIVEN an OHLC response WHEN a chart renders THEN the status is checked and candles are sorted and de-duplicated", func(t *testing.T) {
		render := funcBody(t, markets, "async _fetchAndRender(market) {")
		mustAppearBefore(t, "_fetchAndRender", render, "!r5.ok || !r15.ok", "r5.json()")
		mustContain(t, "markets.js", markets, "function toCandleSeries(", ".sort((a, b) => a.time - b.time)", "new Map()")
	})

	t.Run("GIVEN the dashboard starts WHEN the first loads run THEN the dashboard (and its currency) is loaded before any view data", func(t *testing.T) {
		start := funcBody(t, core, "async _initialLoad() {")
		mustAppearBefore(t, "_initialLoad", start, "await this.loadDashboard();", "this._ready = true;")
		mustAppearBefore(t, "_initialLoad", start, "await this.loadDashboard();", "this._runViewPolls(this.view);")
	})

	t.Run("GIVEN a closed trade without an exit reason WHEN the table renders THEN it does not throw", func(t *testing.T) {
		mustNotContain(t, "trading.js", trading, "t.exit_reason.replace(", "trade.exit_reason.replace(")
		mustContain(t, "closedSourceLabel", funcBody(t, trading, "closedSourceLabel(trade) {"), "(trade.exit_reason || '').replace(")
	})

	t.Run("GIVEN today's realised P&L WHEN it is shown THEN it comes from the P&L summary's UTC day and nothing is invented", func(t *testing.T) {
		today := funcBody(t, overview, "realisedToday() {")
		mustContain(t, "realisedToday", today, "this.pnlSummary.data?.by_day", "new Date().toISOString().slice(0, 10)", "return null")
		mustContain(t, "index", index, "Realised P&amp;L today (UTC)", "realisedToday() === null")
	})

	t.Run("GIVEN the pages WHEN their scripts are inspected THEN the unused htmx library is not loaded", func(t *testing.T) {
		mustNotContain(t, "dashboard", all, "htmx")
		mustNotContain(t, "approvals", approvals, "htmx")
	})

	t.Run("GIVEN a live environment WHEN the markets table renders THEN each market has a confirmed Live toggle", func(t *testing.T) {
		table := between(t, index, `id="markets-table"`, "</table>")
		mustContain(t, "markets table", table, `x-show="environment === 'live'"`, "m.live", `@click.stop="toggleMarketLive(m.symbol)"`)
		mustNotContain(t, "markets table", table, "onclick=")
		toggle := funcBody(t, markets, "async toggleMarketLive(market) {")
		mustContain(t, "toggleMarketLive", toggle, "confirm(", "${market}", "real orders",
			"fetch(`/api/control/markets/${encodeURIComponent(market)}/live-toggle`", "response.ok", "catch", "this.notify('error'")
		mustAppearBefore(t, "toggleMarketLive", toggle, "confirm(", "fetch(")
	})

	t.Run("GIVEN live positions in the dashboard WHEN the positions tables render THEN they carry a LIVE badge and their pending status, and Close is disabled while an order is pending", func(t *testing.T) {
		mustContain(t, "isLivePosition", funcBody(t, trading, "isLivePosition(position) {"), "position.environment === 'live'")
		mustContain(t, "positionClosable", funcBody(t, trading, "positionClosable(position) {"),
			"!this.isLivePosition(position) || position.status === 'open'")
		// Both the Trading table and the Overview summary show the badge, the status and the guarded Close.
		overviewRows := between(t, index, `<template x-for="p in positions"`, "</template>")
		tradingRows := between(t, index, `<tbody id="positions-body">`, "</tbody>")
		for name, rows := range map[string]string{"overview positions": overviewRows, "trading positions": tradingRows} {
			mustContain(t, name, rows, `x-show="isLivePosition(p)" class="pill pill-live"`, ">LIVE</span>",
				"positionStatusLabel(p)", `:disabled="!positionClosable(p)"`, `@click="closePosition(p.position_id_full)"`)
			mustNotContain(t, name, rows, "onclick=")
		}
	})

	t.Run("GIVEN a live position WHEN the operator forgets it THEN a confirmed request removes it without sending an order", func(t *testing.T) {
		rows := between(t, index, `<tbody id="positions-body">`, "</tbody>")
		mustContain(t, "positions", rows, `x-show="isLivePosition(p)"`, `@click="forgetPosition(p.position_id_full)"`)
		forget := funcBody(t, trading, "async forgetPosition(")
		mustContain(t, "forgetPosition", forget, "this._actionAllowedWhileStale()", "confirm(", "No order is sent",
			"fetch(`/api/positions/${encodeURIComponent(positionIdFull)}/forget`", "r.ok", "this.notify('error'", "catch", "this.loadDashboard()")
		mustAppearBefore(t, "forgetPosition", forget, "this._actionAllowedWhileStale()", "confirm(")
		mustAppearBefore(t, "forgetPosition", forget, "confirm(", "fetch(")
	})

	t.Run("GIVEN a live position WHEN it is closed from the dashboard THEN the operator is told the sell was sent and the P&L follows once Kraken confirms", func(t *testing.T) {
		closeFn := funcBody(t, trading, "async closePosition(")
		mustContain(t, "closePosition", closeFn, "d.status === 'closing'", "sell order was sent", "once Kraken confirms")
		mustAppearBefore(t, "closePosition", closeFn, "d.status === 'closing'", "Position closed at")
		mustAppearBefore(t, "closePosition", closeFn, "confirm(", "fetch(")
	})

	t.Run("GIVEN the approve API WHEN an approval cannot execute THEN both pages explain the 503 and 409 answers instead of a removed status", func(t *testing.T) {
		pages := map[string]string{
			"decideApproval": funcBody(t, overview, "async decideApproval("),
			"decide":         between(t, approvals, "async function decide(", "// Approve / Reject buttons"),
		}
		for name, body := range pages {
			mustNotContain(t, name, body, "approved_no_price", "d.status !== 'approved'")
			mustContain(t, name, body, "r.status === 503", "still pending", "r.status === 409", "d.environment")
			mustAppearBefore(t, name, body, "r.status === 503", "!r.ok")
		}
	})

	t.Run("GIVEN pending approvals WHEN a decision is being sent THEN its buttons are disabled until the answer arrives", func(t *testing.T) {
		queue := between(t, index, `id="approvals-queue"`, "</template>")
		mustContain(t, "approvals queue", queue, `:disabled="approvalBusy[a.id] || emergencyStop"`,
			`@click="decideApproval(a.id, 'approve')"`, `@click="decideApproval(a.id, 'reject')"`)
		decide := funcBody(t, overview, "async decideApproval(")
		mustContain(t, "decideApproval", decide, "[id]: true", "finally")
	})

	t.Run("GIVEN the approvals page WHEN its buttons render THEN no server value is placed in an inline handler", func(t *testing.T) {
		mustNotContain(t, "approvals", approvals, "onclick=", "${a.id}")
		mustContain(t, "approvals", approvals, `data-approval-id="${esc(a.id)}"`, `data-approval-action="approve"`,
			`data-approval-action="reject"`, "e.target.closest('[data-approval-action]')")
	})

	t.Run("GIVEN the approvals page WHEN a dashboard link is followed THEN it opens the Overview's approvals panel", func(t *testing.T) {
		mustContain(t, "approvals", approvals, `href="/#/overview/approvals"`)
	})
}
