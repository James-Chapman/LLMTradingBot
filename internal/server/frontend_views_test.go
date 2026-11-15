package server

import (
	"strings"
	"testing"
)

// TestFrontendViewDetails covers how the views fit their content: candle charts
// that fill their width, the briefing's outlook list, clamped activity details,
// the two-row phone header and the phone list → detail flow of Markets.
func TestFrontendViewDetails(t *testing.T) {
	index := readFrontend(t, "index.html")
	css := readFrontend(t, "static/styles.css")
	scripts := appScripts(t)
	markets := scripts["markets.js"]
	phone := between(t, css, "@media (max-width: 767px) {", "\n}")

	t.Run("GIVEN candles WHEN a chart is drawn or resized THEN its time scale is fitted to the data", func(t *testing.T) {
		render := funcBody(t, markets, "async _fetchAndRender(market) {")
		mustContain(t, "_fetchAndRender", render, "entry.series5.setData(c5)", "entry.series15.setData(c15)", ".timeScale().fitContent()")
		mustAppearBefore(t, "_fetchAndRender", render, "entry.series5.setData(c5)", "entry.chart5.timeScale().fitContent()")
		mustAppearBefore(t, "_fetchAndRender", render, "entry.series15.setData(c15)", "entry.chart15.timeScale().fitContent()")
		// A fixed window pinned to "now" bunched the candles into one side of the chart.
		mustNotContain(t, "_fetchAndRender", render, "setVisibleRange")
		create := funcBody(t, markets, "_createCharts(market) {")
		resize := between(t, create, "new ResizeObserver(", "resizer.observe(el)")
		mustContain(t, "chart resize", resize, "chart.applyOptions({ width: el.clientWidth, height: el.clientHeight })", "chart.timeScale().fitContent()")
	})

	t.Run("GIVEN a briefing with many outlooks WHEN shown THEN non-neutral ones come first, a few are shown and a button shows the rest", func(t *testing.T) {
		intel := scripts["intelligence.js"]
		order := funcBody(t, intel, "briefingOutlooks() {")
		mustContain(t, "briefingOutlooks", order, "outlookRank(", "localeCompare(")
		mustContain(t, "outlookRank", funcBody(t, intel, "function outlookRank(bias) {"), "biasDirection(bias)", "bearish", "bullish")
		// Shades the LLM writes ('mildly_bullish') count with their direction, not as neutral.
		mustContain(t, "biasDirection", funcBody(t, intel, "function biasDirection(bias) {"), "text.includes('bear')", "text.includes('bull')")
		mustContain(t, "intelligence.js", intel, "const OUTLOOK_PREVIEW = 9;")
		mustContain(t, "shownOutlooks", funcBody(t, intel, "shownOutlooks() {"), "this.outlooksExpanded", ".slice(0, OUTLOOK_PREVIEW)")
		mustContain(t, "outlookSummary", funcBody(t, intel, "outlookSummary() {"), "' · '")
		// The expanded state lasts while the view is open and resets when it is left.
		mustContain(t, "onIntelligenceHidden", funcBody(t, intel, "onIntelligenceHidden() {"), "this.outlooksExpanded = false")
		briefing := between(t, index, "<!-- Market briefing", "<!-- Signals -->")
		mustContain(t, "briefing", briefing, `x-for="[mkt, out] in shownOutlooks()"`, `x-text="outlookSummary()"`,
			`<button type="button"`, `@click="outlooksExpanded = !outlooksExpanded"`, `:aria-expanded="outlooksExpanded ? 'true' : 'false'"`,
			"'Show all ' + briefingOutlooks().length + ' outlooks'", "'Show fewer'")
		// Briefing and signals sit side by side, so signals are on the first screen.
		mustContain(t, "intelligence layout", between(t, index, `<section data-view="intelligence"`, "<!-- News -->"),
			`<div class="intel-grid">`)
		mustAppearBefore(t, "intelligence", index, `<div class="intel-grid">`, "<!-- Market briefing")
	})

	t.Run("GIVEN a long activity detail WHEN listed THEN it is clamped to two lines with a more / less button that keeps the full text", func(t *testing.T) {
		activity := scripts["activity.js"]
		mustContain(t, "activity.js", activity, "toggleActivityDetail(entry) {", "isActivityOpen(entry) {", "activityKey(entry) {")
		mustContain(t, "core.js", scripts["core.js"], "function isClamped(el) {")
		mustContain(t, "css", css, ".is-clamped {", "-webkit-line-clamp: 2;", ".more-toggle {")
		for name, list := range map[string]string{
			"activity log":    between(t, index, "<!-- Bot activity log -->", "<!-- System health -->"),
			"recent activity": between(t, index, "<!-- Recent activity strip -->", "</section>"),
		} {
			mustContain(t, name, list, `x-data="{ overflows: false }"`, `:class="{ 'is-clamped': !isActivityOpen(e) }"`,
				"isClamped($el)", `<button type="button" class="more-toggle"`, `@click="toggleActivityDetail(e)"`,
				`:aria-expanded="isActivityOpen(e) ? 'true' : 'false'"`, `x-text="e.detail"`)
		}
	})

	t.Run("GIVEN a phone WHEN the header is laid out THEN it is two rows with the emergency stop on the first", func(t *testing.T) {
		header := strings.Join(strings.Fields(between(t, phone, ".app-header {", "}")), " ")
		mustContain(t, "phone header", header, "display: grid;", `grid-template-areas: "brand stop stop" "status status settings";`)
		mustContain(t, "phone css", phone, ".hdr-actions {\n        display: contents;", "grid-area: stop;", ".hdr-settings {\n        grid-area: settings;",
			".brand-version,\n    .hdr-llm-model {\n        display: none;")
		// The stop button keeps its full label: nothing in the phone rules hides or shrinks it.
		mustNotContain(t, "phone css", phone, ".btn-stop {\n        display: none")
		stop := between(t, index, `class="btn btn-stop"`, "</button>")
		mustContain(t, "stop button", stop, "Emergency stop")
		mustNotContain(t, "stop button", stop, "hide-sm")
		mustContain(t, "header", between(t, index, `<header class="app-header`, "</header>"), `class="btn btn-ghost btn-icon hdr-settings"`)
	})

	t.Run("GIVEN a phone WHEN Markets opens THEN the list shows alone, a market opens its own detail page with a back control and nothing is auto-selected", func(t *testing.T) {
		mustContain(t, "markets.js", markets, "const NARROW_SCREEN = window.matchMedia('(max-width: 767px)');")
		resolve := funcBody(t, markets, "_resolveSelectedMarket() {")
		mustContain(t, "_resolveSelectedMarket", resolve, "if (this.marketsNarrow && !requested) {", "this._setSelectedMarket('')", "this.markets[0]")
		mustAppearBefore(t, "_resolveSelectedMarket", resolve, "if (this.marketsNarrow && !requested) {", "this.markets[0]")
		mustContain(t, "selectMarket", funcBody(t, markets, "selectMarket(symbol) {"), "this._marketListScrollY = window.scrollY")
		mustContain(t, "onMarketsShown", funcBody(t, markets, "onMarketsShown(arg) {"), "window.scrollTo(0, this._marketListScrollY)")
		mustContain(t, "marketsBack", funcBody(t, markets, "marketsBack() {"), "history.back()", "this.navigate('markets')")
		mustContain(t, "initMarkets", funcBody(t, markets, "initMarkets() {"), "NARROW_SCREEN.addEventListener('change'")
		layout := between(t, index, `<div class="markets-layout"`, ">")
		mustContain(t, "markets layout", layout, "'show-list': marketsNarrow && !selectedMarket", "'show-detail': marketsNarrow && selectedMarket")
		mustContain(t, "market detail", between(t, index, `id="candle-detail"`, `<div class="panel-body`), `@click="marketsBack()"`, "← Markets")
		mustContain(t, "phone css", phone, ".markets-layout.show-list > .market-detail,\n    .markets-layout.show-detail > .markets-list {\n        display: none;")
	})

	t.Run("GIVEN a phone WHEN the market list is shown THEN the trading switch fits and signal and position fold under the symbol", func(t *testing.T) {
		head := between(t, index, `<table class="table" id="markets-table">`, "</thead>")
		mustContain(t, "markets head", head, `<th class="hide-sm">Signal</th>`, `<th class="hide-sm">Position</th>`, "<th>Trading</th>")
		mustContain(t, "markets rows", between(t, index, `x-for="m in filteredMarkets()"`, "</template>"), `class="market-sub show-sm"`)
		mustContain(t, "phone css", phone, ".show-sm {\n        display: flex;", ".tabs {\n        flex-wrap: wrap;", ".table .empty-row td {\n        text-align: left;")
	})

	t.Run("GIVEN a touch layout WHEN controls are sized THEN they are at least 36 px tall", func(t *testing.T) {
		touch := between(t, css, "@media (max-width: 767px), (pointer: coarse) {", "\n}")
		mustContain(t, "touch css", touch, ".btn,", ".input,", ".btn-small,", ".filter-chip,", ".more-toggle {\n        min-height: 36px;",
			".hdr-trading,", ".hdr-approvals,", "a.panel-meta,", ".switch::before {")
	})

	t.Run("GIVEN keyboard focus on the current view's nav item WHEN it is drawn THEN the focus ring beats the current-item indicator", func(t *testing.T) {
		mustContain(t, "css", css, ".app-nav .nav-item:focus-visible {\n    box-shadow: var(--focus-ring);")
		mustAppearBefore(t, "css", css, `.nav-item[aria-current="page"] {`, ".app-nav .nav-item:focus-visible {")
	})

	t.Run("GIVEN neutral figures WHEN shown THEN they are not coloured", func(t *testing.T) {
		// The Pending approvals tile is a link; its count must not take the link colour.
		mustContain(t, "css", css, "a.kpi {\n    color: var(--text);")
		// The approvals page's badge is amber only while trades wait.
		approvals := readFrontend(t, "approvals.html")
		mustContain(t, "approvals", approvals, `<span id="count-badge" class="pill pill-outline">`, "setBadge(`${approvals.length} pending`, 'pill-warning')",
			"setBadge('None pending', 'pill-outline')")
	})
}
