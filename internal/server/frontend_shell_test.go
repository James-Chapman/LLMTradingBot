package server

import (
	"regexp"
	"strings"
	"testing"
)

// dashboardViews are the views of the dashboard in navigation order.
var dashboardViews = []string{"overview", "markets", "trading", "intelligence", "backtests", "activity"}

// TestFrontendShell covers the app shell: the view modules, navigation and
// hash routing, which data is loaded when, and the selected-market charts.
func TestFrontendShell(t *testing.T) {
	index := readFrontend(t, "index.html")
	scripts := appScripts(t)
	core := scripts["core.js"]

	t.Run("GIVEN the dashboard WHEN its scripts are inspected THEN core loads first, every view after it, backtests last and Alpine at the end", func(t *testing.T) {
		order := []string{"chart.js@", "lightweight-charts@", "/static/app/core.js", "/static/app/overview.js",
			"/static/app/markets.js", "/static/app/trading.js", "/static/app/intelligence.js", "/static/app/activity.js",
			"/static/app/settings.js", "/static/app/backtests.js", "alpinejs@"}
		tags := regexp.MustCompile(`<script\b[^>]*>`).FindAllString(index, -1)
		if len(tags) != len(order) {
			t.Fatalf("expected %d script tags, got %d: %v", len(order), len(tags), tags)
		}
		for i, want := range order {
			if !strings.Contains(tags[i], want) {
				t.Errorf("script %d: want %q, got %s", i, want, tags[i])
			}
			if !strings.Contains(tags[i], " defer") {
				t.Errorf("script %s must be deferred so the order above is the run order", tags[i])
			}
		}
	})

	t.Run("GIVEN the page WHEN inspected THEN no inline script is left beyond a few lines", func(t *testing.T) {
		inline := regexp.MustCompile(`(?s)<script\b[^>]*>(.*?)</script>`)
		for _, m := range inline.FindAllStringSubmatch(index, -1) {
			if lines := strings.Count(strings.TrimSpace(m[1]), "\n"); strings.TrimSpace(m[1]) != "" && lines > 4 {
				t.Errorf("inline script of %d lines; behaviour belongs in static/app", lines+1)
			}
		}
		mustNotContain(t, "index", index, "function dashboard()", "x-html")
	})

	t.Run("GIVEN each view WHEN the modules and markup are inspected THEN it is registered, routed and has its own section", func(t *testing.T) {
		quoted := make([]string, len(dashboardViews))
		for i, v := range dashboardViews {
			quoted[i] = "'" + v + "'"
		}
		mustContain(t, "core.js", core, "const VIEWS = ["+strings.Join(quoted, ", ")+"];")
		for _, view := range dashboardViews {
			mustContain(t, view+".js", scripts[view+".js"], "Dashboard.register('"+view+"', {")
			mustContain(t, "index", index, `<section data-view="`+view+`" x-show="view === '`+view+`'"`)
		}
		mustContain(t, "settings.js", scripts["settings.js"], "Dashboard.register('settings', {")
	})

	t.Run("GIVEN the Backtests view WHEN its section is inspected THEN it holds its own list and detail markup, not the redesign's placeholder", func(t *testing.T) {
		section := between(t, index, `<section data-view="backtests"`, "</section>")
		mustNotContain(t, "backtests section", section, `id="backtests-root"`)
		mustContain(t, "backtests section", section, `<h1 id="h-backtests" tabindex="-1"`, `id="bt-list"`, `id="bt-detail"`)
	})

	t.Run("GIVEN view modules WHEN they are merged THEN getters survive and a name defined twice is reported", func(t *testing.T) {
		mustContain(t, "core.js", core, "window.Dashboard = {", "register(name, mixin) {", "is registered twice")
		merge := funcBody(t, core, "function dashboard() {")
		mustContain(t, "dashboard()", merge, "Object.getOwnPropertyDescriptors(mixin)", "Object.defineProperties(root, descriptors)",
			"if (key in root) console.error(")
		mustNotContain(t, "dashboard()", merge, "Object.assign(")
		// The module contract is documented where view authors will look.
		mustContain(t, "core.js", core, "Module contract", "init<Name>()", "on<Name>Shown(arg)", "on<Name>Hidden()", "on<Name>Data(d)",
			"pollWhileShown(")
	})

	t.Run("GIVEN the navigation WHEN inspected THEN it links each view by hash in order and marks the current one", func(t *testing.T) {
		nav := between(t, index, `<nav class="app-nav"`, "</nav>")
		cursor := 0
		for _, view := range dashboardViews {
			link := `<a href="#/` + view + `" class="nav-item" :aria-current="view === '` + view + `' ? 'page' : null">`
			i := strings.Index(nav[cursor:], link)
			if i < 0 {
				t.Errorf("navigation: link for %s missing or out of order", view)
				break
			}
			cursor += i + len(link)
		}
	})

	t.Run("GIVEN a hash such as #/markets/BTC-EUR WHEN the page routes THEN the view and argument come from it and an unknown hash falls back to Overview", func(t *testing.T) {
		mustContain(t, "init", funcBody(t, core, "init() {"), "this._applyRoute(false)", "window.addEventListener('hashchange', () => this._applyRoute(true))")
		route := funcBody(t, core, "_applyRoute(byUser) {")
		mustContain(t, "_applyRoute", route, "location.hash", "VIEWS.includes(name)", "history.replaceState(null, '', '#/overview')",
			"rest.map(decodeURIComponent)")
		show := funcBody(t, core, "_showView(name, arg, byUser) {")
		mustContain(t, "_showView", show, "this._callViewHook(previous, 'on', 'Hidden')", "this.view = name",
			"this._callViewHook(name, 'on', 'Shown', arg)", ".focus(")
		mustAppearBefore(t, "_showView", show, "'Hidden'", "this.view = name")
		mustContain(t, "navigate", funcBody(t, core, "navigate(name, arg = '') {"), "location.hash = '#/' + name", "encodeURIComponent(arg)")
		// Markets keeps the selected market in the hash: 'BTC/EUR' ↔ 'BTC-EUR'.
		markets := scripts["markets.js"]
		mustContain(t, "marketSlug", funcBody(t, markets, "function marketSlug(symbol) {"), `.replace(/\//g, '-')`)
		mustContain(t, "selectMarket", funcBody(t, markets, "selectMarket(symbol) {"), "this.navigate('markets', marketSlug(symbol))")
		mustContain(t, "_resolveSelectedMarket", funcBody(t, markets, "_resolveSelectedMarket() {"),
			"marketSlug(m.symbol) === this._requestedMarketSlug", "history.replaceState(null, '', '#/markets/' + marketSlug(next.symbol))")
		// Trading keeps its tab in the hash.
		trading := scripts["trading.js"]
		mustContain(t, "showTradingTab", funcBody(t, trading, "showTradingTab(tab) {"), "this.navigate('trading', tab)")
		mustContain(t, "onTradingShown", funcBody(t, trading, "onTradingShown(arg) {"), "TRADING_TABS.includes(arg) ? arg : 'positions'")
		mustContain(t, "trading.js", trading, "const TRADING_TABS = ['positions', 'closed', 'ledger', 'rejected', 'risk', 'pnl'];")
		// The header approvals link opens the Overview at the approvals panel.
		mustContain(t, "index", between(t, index, `<header class="app-header`, "</header>"), `href="#/overview/approvals"`)
		mustContain(t, "onOverviewShown", funcBody(t, scripts["overview.js"], "onOverviewShown(arg) {"), "arg !== 'approvals'", "approvals-panel")
	})

	t.Run("GIVEN a view's own data WHEN the page runs THEN it is loaded only while that view is shown and the tab is visible", func(t *testing.T) {
		loaders := map[string][]string{
			"overview.js":     {"this.pollWhileShown('overview', 30000, () => this.loadPnlSummary())"},
			"markets.js":      {"this.pollWhileShown('markets', CHART_REFRESH_MS, () => this._loadSelectedChart())"},
			"trading.js":      {"this.pollWhileShown('trading', 15000, () => this.loadLedger())", "this.pollWhileShown('trading', 30000, () => this.loadClosedTrades())", "this.pollWhileShown('trading', 30000, () => this.loadRejectedTrades())", "this.pollWhileShown('trading', 30000, () => this.loadPnlSummary())"},
			"intelligence.js": {"this.pollWhileShown('intelligence', NEWS_REFRESH_MS, () => this.loadNews())"},
		}
		for file, calls := range loaders {
			name := strings.TrimSuffix(file, ".js")
			hook := funcBody(t, scripts[file], "init"+strings.ToUpper(name[:1])+name[1:]+"() {")
			mustContain(t, file, hook, calls...)
		}
		// Only core.js starts timers: the dashboard poll, the stale check and the view-poll ticker.
		for file, src := range scripts {
			if file != "core.js" && strings.Contains(src, "setInterval(") {
				t.Errorf("%s starts its own interval; use pollWhileShown so it stops when the view is hidden", file)
			}
		}
		due := funcBody(t, core, "_runDuePolls() {")
		mustContain(t, "_runDuePolls", due, "!this._ready", "document.hidden", "poll.viewName !== this.view", "poll.intervalMs")
		show := funcBody(t, core, "_showView(name, arg, byUser) {")
		mustContain(t, "_showView", show, "if (!changed) return;", "if (this._ready) this._runViewPolls(name)")
		// The first load does not fetch every view's data: only the dashboard, the settings and the shown view.
		start := funcBody(t, core, "async _initialLoad() {")
		mustContain(t, "_initialLoad", start, "this._runViewPolls(this.view)")
		mustNotContain(t, "_initialLoad", start, "loadLedger", "loadClosedTrades", "loadRejectedTrades", "loadNews", "loadPnlSummary")
	})

	t.Run("GIVEN the Markets view WHEN charts render THEN only the selected market has a chart pair", func(t *testing.T) {
		if n := strings.Count(index, `class="chart-pane chart-pane-5"`); n != 1 {
			t.Errorf("expected one 5-minute chart pane, got %d", n)
		}
		if n := strings.Count(index, `class="chart-pane chart-pane-15"`); n != 1 {
			t.Errorf("expected one 15-minute chart pane, got %d", n)
		}
		detail := between(t, index, `id="candle-detail"`, `<div class="two-col">`)
		mustNotContain(t, "chart detail", detail, "x-for=")
		markets := scripts["markets.js"]
		create := funcBody(t, markets, "_createCharts(market) {")
		mustContain(t, "_createCharts", create, "document.getElementById('candle-detail')", ".chart-pane-5", ".chart-pane-15",
			"candleCharts.set(market, entry)", "new ResizeObserver(")
		swap := funcBody(t, markets, "_setSelectedMarket(symbol) {")
		mustContain(t, "_setSelectedMarket", swap, "this._removeCharts(this.selectedMarket)")
		mustAppearBefore(t, "_setSelectedMarket", swap, "this._removeCharts(this.selectedMarket)", "this.selectedMarket = symbol")
		mustContain(t, "onMarketsHidden", funcBody(t, markets, "onMarketsHidden() {"), "this._removeCharts(market)")
		remove := funcBody(t, markets, "_removeCharts(market) {")
		mustContain(t, "_removeCharts", remove, "entry.chart5.remove()", "entry.chart15.remove()", "observer.disconnect()",
			"candleCharts.delete(market)")
		// A response for a market that is no longer shown is dropped instead of drawn.
		render := funcBody(t, markets, "async _fetchAndRender(market) {")
		mustContain(t, "_fetchAndRender", render, "market !== this.selectedMarket || this.view !== 'markets'")
		mustAppearBefore(t, "_fetchAndRender", render, "market !== this.selectedMarket", "this._createCharts(market)")
		mustNotContain(t, "markets.js", markets, "IntersectionObserver", "_syncChartCards")
	})
}
