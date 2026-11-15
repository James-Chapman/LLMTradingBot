package server

import (
	"regexp"
	"strings"
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/llm"
)

// withoutFunction returns text with the named JavaScript helper's body removed,
// so checks for raw patterns do not trip over the helper that wraps them.
func withoutFunction(t *testing.T, text, signature string) string {
	t.Helper()
	i := strings.Index(text, signature)
	if i < 0 {
		t.Fatalf("helper %q not found", signature)
	}
	// The helper ends at the first closing brace indented like its signature.
	indent := text[strings.LastIndex(text[:i], "\n")+1 : i]
	j := strings.Index(text[i:], "\n"+indent+"}")
	if j < 0 {
		t.Fatalf("end of helper %q not found", signature)
	}
	return text[:i] + text[i+j:]
}

// mustAppearBefore fails unless first occurs in text before second.
func mustAppearBefore(t *testing.T, what, text, first, second string) {
	t.Helper()
	i, j := strings.Index(text, first), strings.Index(text, second)
	if i < 0 || j < 0 || i > j {
		t.Errorf("%s: expected %q before %q", what, first, second)
	}
}

// TestFrontendSafety covers escaping, timestamps and failure feedback in the dashboard pages.
func TestFrontendSafety(t *testing.T) {
	index := readFrontend(t, "index.html")
	approvals := readFrontend(t, "approvals.html")

	t.Run("GIVEN server strings rendered via innerHTML WHEN the dashboard source is inspected THEN each passes through esc()", func(t *testing.T) {
		for name, page := range map[string]string{"index": index, "approvals": approvals} {
			mustContain(t, name, page, "function esc(", ".replace(/&/g, '&amp;')", ".replace(/</g, '&lt;')",
				".replace(/>/g, '&gt;')", `.replace(/"/g, '&quot;')`, ".replace(/'/g, '&#39;')")
		}
		mustContain(t, "index", index,
			// news
			"${esc(n.title)}", "${esc(n.source)}",
			// signals
			"${esc(s.market)}", "${esc(s.thesis)}", "esc(s.risk_reason)",
			// dashboard approval cards
			"${esc(a.market)}", "${esc(a.thesis)}", "${esc(a.entry_plan)}", "${esc(a.exit_plan)}",
			// markets, positions, ledger, closed trades, P&L, charts
			"${esc(m.symbol)}", "${esc(p.market)}", "${esc(t.market)}", "${esc(t.source)}", "${esc(t.status)}",
			"${esc(label)}", "${esc(market)}", "${esc(statusLabel)}", "${esc(sourceLabel)}", "${esc(srcLabel)}",
		)
		// Desktop notifications are plain text, so only the innerHTML signal cards must escape.
		signalCards := between(t, index, "el.innerHTML = signals.map", "renderPositions(positions) {")
		mustNotContain(t, "signal cards", signalCards, "${s.market}", "${s.thesis}", "+ s.risk_reason")
		mustNotContain(t, "index", index,
			"${n.title}", "${n.source}", "${a.market}", "${a.thesis}",
			"${a.entry_plan}", "${a.exit_plan}", "${m.symbol}", "${p.market}", "${t.market}", "${t.source}",
			"${t.status}", ">${label}<", ">${market}<", ">${s}</button>", "${m}</button>",
			// a name embedded in an inline JS string can break out of the attribute
			"{detail:'${esc}'}", "window._toggleMarket('${m.symbol}')")
		mustContain(t, "approvals", approvals, "${esc(a.market)}", "${esc(a.thesis)}", "${esc(a.entry_plan)}",
			"${esc(a.exit_plan)}", "${esc(a.risk_reason)}", "${esc(a.strategy_id)}")
		mustNotContain(t, "approvals", approvals, "${a.market}", "${a.thesis}", "${a.entry_plan}", "${a.exit_plan}",
			"${a.risk_reason}", "${a.strategy_id}")
	})

	t.Run("GIVEN news filter chips WHEN a source is clicked THEN the source travels in a data attribute, not inline script", func(t *testing.T) {
		strip := between(t, index, "_renderNewsFilterStrip() {", "_applyNewsFilter() {")
		mustContain(t, "news strip", strip, `data-filter-event="news-filter-toggle"`, `data-filter-detail="${esc(s)}"`)
		mustNotContain(t, "news strip", strip, "onclick=")
		mustContain(t, "index", index, "e.target.closest('[data-filter-event]')", "chip.dataset.filterDetail")
	})

	t.Run("GIVEN a news article WHEN its link is rendered THEN only http(s) URLs become the href", func(t *testing.T) {
		mustContain(t, "index", index, "function safeUrl(", `/^https?:\/\//i`, `href="${esc(safeUrl(n.url))}"`)
		mustNotContain(t, "index", index, `href="${n.url || '#'}"`)
	})

	t.Run("GIVEN the emergency stop WHEN it is activated THEN the stopped state is shown only after the server confirms", func(t *testing.T) {
		stop := between(t, index, "async activateStop() {", "// ── Trading settings")
		mustContain(t, "activateStop", stop, "await fetch('/api/control/emergency-stop'", "response.ok", "catch", "FAILED")
		mustAppearBefore(t, "activateStop", stop, "response.ok", "this.emergencyStop = true")
	})

	t.Run("GIVEN a stopped bot WHEN resume is clicked THEN the operator confirms and a failure is reported", func(t *testing.T) {
		resume := between(t, index, "async resumeBot() {", "async selectStrategy(id) {")
		mustContain(t, "resumeBot", resume, "confirm(", "await fetch('/api/control/resume'", "response.ok", "catch", "FAILED")
		mustAppearBefore(t, "resumeBot", resume, "confirm(", "fetch(")
		mustAppearBefore(t, "resumeBot", resume, "response.ok", "this.emergencyStop = false")
	})

	t.Run("GIVEN the server's LLM backend names WHEN the LLM panel renders THEN each has a readable label", func(t *testing.T) {
		mustContain(t, "index", index,
			"llmBackend === '"+llm.BackendLMStudio+"' ? 'LM Studio'",
			"llmBackend === '"+llm.BackendRemote+"' ? 'Remote'",
			"'None'")
		mustNotContain(t, "index", index, "llmBackend === 'openai'", "llmBackend === 'transformers'")
	})

	t.Run("GIVEN naive UTC storage timestamps WHEN the pages parse them THEN parseUtc is always used", func(t *testing.T) {
		rawDate := regexp.MustCompile(`new Date\([^)]`)
		for name, page := range map[string]string{"index": index, "approvals": approvals} {
			mustContain(t, name, page, "function parseUtc(", "/(Z|[+-]\\d{2}:?\\d{2})$/i", "+ 'Z'")
			rest := withoutFunction(t, page, "function parseUtc(")
			for _, m := range rawDate.FindAllStringIndex(rest, -1) {
				line := rest[strings.LastIndex(rest[:m[0]], "\n")+1 : m[1]+40]
				t.Errorf("%s: server timestamp parsed without parseUtc: %s", name, strings.TrimSpace(line))
			}
		}
		mustNotContain(t, "index", index, "created_at+'Z'")
		mustContain(t, "index", index, "parseUtc(llmBriefing.generated_at)", "parseUtc(r.timestamp)",
			"parseUtc(n.published_at)", "parseUtc(t.exit_at)", "parseUtc(t.entry_at)", "parseUtc(timestamp)",
			"parseUtc(signalModal.created_at)", "parseUtc(openedRaw)", "parseUtc(t.timestamp)")
		mustContain(t, "approvals", approvals, "parseUtc(a.expires_at)")
	})

	t.Run("GIVEN a refused reset WHEN positions are reset THEN the server's detail is shown instead of a count", func(t *testing.T) {
		reset := between(t, index, "async resetPositions() {", "timeAgo(date) {")
		mustContain(t, "resetPositions", reset, "r.ok", "detail")
		mustAppearBefore(t, "resetPositions", reset, "r.ok", "Cleared ${d.cleared}")
	})

	t.Run("GIVEN an approval decision WHEN the server does not execute a real order THEN the operator is told", func(t *testing.T) {
		dash := between(t, index, "renderApprovals(approvals) {", "renderChart(history) {")
		page := between(t, approvals, "async function decide(", "load();\nsetInterval")
		for name, src := range map[string]string{"dashboard": dash, "approvals page": page} {
			mustContain(t, name, src, ".ok", "r.status === 409", "order_status === 'rejected'", "alert(", "catch")
		}
		mustNotContain(t, "dashboard", dash, "window._approveTrade('${a.id}')", "window._rejectTrade('${a.id}')")
	})

	t.Run("GIVEN settings and control requests WHEN they fail or the network drops THEN the error is surfaced", func(t *testing.T) {
		mode := between(t, index, "async setTradingMode(mode) {", "async setTradingEnvironment(")
		env := between(t, index, "async setTradingEnvironment(", "// ── Settings dialog")
		strategy := between(t, index, "async selectStrategy(id) {", "_strategyLabelParts(strategy) {")
		for name, src := range map[string]string{"setTradingMode": mode, "setTradingEnvironment": env, "selectStrategy": strategy} {
			mustContain(t, name, src, "catch", "this.tradingSettingsError =")
		}
		load := between(t, index, "async loadSettings() {", "closeSettings() {")
		mustContain(t, "loadSettings", load, "throw new Error(", "catch", "this.settingsError =")
		save := between(t, index, "async saveSettings() {", "async resumeBot() {")
		mustContain(t, "saveSettings", save, "catch", "this.settingsError =")
		toggle := between(t, index, "async toggleMarket(market) {", "renderMarkets(markets, disabledMarkets) {")
		mustContain(t, "toggleMarket", toggle, "response.ok", "catch", "alert(")
	})

	t.Run("GIVEN the approvals page WHEN the API refuses THEN an error replaces Loading and the hint names the dashboard", func(t *testing.T) {
		load := between(t, approvals, "async function load() {", "async function decide(")
		mustNotContain(t, "approvals load", load, "if (!r.ok) return;")
		mustContain(t, "approvals load", load, "showLoadError(")
		mustContain(t, "approvals", approvals, "function showLoadError(", "Trading settings")
		mustNotContain(t, "approvals", approvals, "TRADING_MODE=semi_automated")
	})

	t.Run("GIVEN CDN scripts WHEN the dashboard loads THEN every version is pinned", func(t *testing.T) {
		mustNotContain(t, "index", index, "alpinejs@3.x.x", `src="https://cdn.jsdelivr.net/npm/chart.js"`)
		pinned := regexp.MustCompile(`alpinejs@3\.\d+\.\d+/`)
		if !pinned.MatchString(index) {
			t.Error("Alpine.js must be pinned to an exact 3.x version")
		}
		if !regexp.MustCompile(`chart\.js@4\.\d+\.\d+`).MatchString(index) {
			t.Error("Chart.js must be pinned to an exact 4.x version")
		}
	})
}
