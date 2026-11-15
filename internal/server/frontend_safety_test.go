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
	body := funcBody(t, text, signature)
	i := strings.Index(text, body)
	return text[:i] + text[i+len(body):]
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
	scripts := appScripts(t)
	core := scripts["core.js"]
	settings := scripts["settings.js"]
	all := dashboardSource(t)

	t.Run("GIVEN server strings WHEN the dashboard renders them THEN Alpine text bindings are used and no HTML is built from them", func(t *testing.T) {
		// The dashboard renders through x-text, which never parses HTML. Nothing
		// assigns innerHTML or uses x-html, so no server string can become markup.
		mustNotContain(t, "dashboard", all, "x-html", ".innerHTML", "insertAdjacentHTML", "document.write(")
		// esc() stays available (and correct) for any view that must build HTML.
		for name, page := range map[string]string{"core.js": core, "approvals": approvals} {
			mustContain(t, name, page, "function esc(", ".replace(/&/g, '&amp;')", ".replace(/</g, '&lt;')",
				".replace(/>/g, '&gt;')", `.replace(/"/g, '&quot;')`, ".replace(/'/g, '&#39;')")
		}
		// The fields that used to be escaped by hand are bound as text.
		mustContain(t, "index", index,
			`x-text="n.title"`, `x-text="n.source"`, // news
			`x-text="s.market"`, `x-text="s.thesis"`, "s.risk_reason", // signals
			`x-text="a.market"`, `x-text="a.thesis"`, `x-text="a.entry_plan"`, `x-text="a.exit_plan"`, // approval cards
			`x-text="m.symbol"`, `x-text="p.market"`, `x-text="t.market"`, `x-text="t.source"`, `x-text="t.status"`, // tables
			`x-text="label"`, `x-text="closedSourceLabel(t)"`, `x-text="positionStatusLabel(p)"`, `x-text="source"`)
		// No server value is spliced into an inline handler.
		mustNotContain(t, "index", index, "onclick=", "{detail:'${esc}'}", "window._toggleMarket(")
		mustContain(t, "approvals", approvals, "${esc(a.market)}", "${esc(a.thesis)}", "${esc(a.entry_plan)}",
			"${esc(a.exit_plan)}", "${esc(a.risk_reason)}", "${esc(a.strategy_id)}")
		mustNotContain(t, "approvals", approvals, "${a.market}", "${a.thesis}", "${a.entry_plan}", "${a.exit_plan}",
			"${a.risk_reason}", "${a.strategy_id}")
	})

	t.Run("GIVEN news filter chips WHEN a source is clicked THEN the source goes to a component method, not inline script", func(t *testing.T) {
		strip := between(t, index, `id="news-source-filter"`, "</template>")
		mustContain(t, "news strip", strip, `@click="toggleNewsSource(source)"`, `x-text="source"`)
		mustNotContain(t, "news strip", strip, "onclick=")
		mustContain(t, "intelligence.js", scripts["intelligence.js"], "toggleNewsSource(source) {", "filteredNews() {")
	})

	t.Run("GIVEN a news article WHEN its link is rendered THEN only http(s) URLs become the href", func(t *testing.T) {
		mustContain(t, "core.js", core, "function safeUrl(", `/^https?:\/\//i`)
		mustContain(t, "index", index, `:href="safeUrl(n.url)"`)
		if n, safe := strings.Count(index, ":href="), strings.Count(index, `:href="safeUrl(`); n != safe {
			t.Errorf("every bound href must go through safeUrl: %d bound, %d through safeUrl", n, safe)
		}
		mustNotContain(t, "index", index, `href="${n.url || '#'}"`, `:href="n.url"`)
	})

	t.Run("GIVEN the emergency stop WHEN it is activated THEN the stopped state is shown only after the server confirms", func(t *testing.T) {
		stop := funcBody(t, core, "async activateStop() {")
		mustContain(t, "activateStop", stop, "await fetch('/api/control/emergency-stop'", "response.ok", "catch", "FAILED", "alert(")
		mustAppearBefore(t, "activateStop", stop, "response.ok", "this.emergencyStop = true")
	})

	t.Run("GIVEN a stopped bot WHEN resume is clicked THEN the operator confirms and a failure is reported", func(t *testing.T) {
		resume := funcBody(t, core, "async resumeBot() {")
		mustContain(t, "resumeBot", resume, "confirm(", "await fetch('/api/control/resume'", "response.ok", "catch", "FAILED", "alert(")
		mustAppearBefore(t, "resumeBot", resume, "confirm(", "fetch(")
		mustAppearBefore(t, "resumeBot", resume, "response.ok", "this.emergencyStop = false")
	})

	t.Run("GIVEN the server's LLM backend names WHEN the LLM panel renders THEN each has a readable label", func(t *testing.T) {
		mustContain(t, "core.js", core,
			"llmBackend === '"+llm.BackendLMStudio+"' ? 'LM Studio'",
			"llmBackend === '"+llm.BackendRemote+"' ? 'Remote'",
			"'None'")
		mustNotContain(t, "dashboard", all, "llmBackend === 'openai'", "llmBackend === 'transformers'")
	})

	t.Run("GIVEN naive UTC storage timestamps WHEN the pages parse them THEN parseUtc is always used", func(t *testing.T) {
		rawDate := regexp.MustCompile(`new Date\([^)]`)
		for name, page := range map[string]string{"dashboard": all, "approvals": approvals} {
			mustContain(t, name, page, "function parseUtc(", "/(Z|[+-]\\d{2}:?\\d{2})$/i", "+ 'Z'")
			rest := withoutFunction(t, page, "function parseUtc(")
			for _, m := range rawDate.FindAllStringIndex(rest, -1) {
				line := rest[strings.LastIndex(rest[:m[0]], "\n")+1 : min(m[1]+40, len(rest))]
				t.Errorf("%s: server timestamp parsed without parseUtc: %s", name, strings.TrimSpace(line))
			}
		}
		mustNotContain(t, "dashboard", all, "created_at+'Z'")
		mustContain(t, "dashboard", all, "parseUtc(llmBriefing.generated_at)", "parseUtc(r.timestamp)",
			"parseUtc(n.published_at)", "parseUtc(t.exit_at)", "parseUtc(t.entry_at)", "parseUtc(entry.timestamp)",
			"parseUtc(signalModal.created_at)", "parseUtc(openedRaw)", "parseUtc(t.timestamp)", "parseUtc(a.expires_at)",
			"parseUtc(trade.exit_at)", "parseUtc(stopSince)")
		mustContain(t, "approvals", approvals, "parseUtc(a.expires_at)")
	})

	t.Run("GIVEN a refused reset WHEN positions are reset THEN the server's detail is shown instead of a count", func(t *testing.T) {
		reset := funcBody(t, scripts["trading.js"], "async resetPositions() {")
		mustContain(t, "resetPositions", reset, "r.ok", "detail")
		mustAppearBefore(t, "resetPositions", reset, "r.ok", "Cleared ${d.cleared}")
	})

	t.Run("GIVEN an approval decision WHEN the server does not execute a real order THEN the operator is told", func(t *testing.T) {
		dash := funcBody(t, scripts["overview.js"], "async decideApproval(")
		page := between(t, approvals, "async function decide(", "load();\nsetInterval")
		mustContain(t, "dashboard", dash, ".ok", "r.status === 409", "order_status === 'rejected'", "this.notify('error'", "catch")
		mustContain(t, "approvals page", page, ".ok", "r.status === 409", "order_status === 'rejected'", "alert(", "catch")
		mustNotContain(t, "dashboard", all, "window._approveTrade(", "window._rejectTrade(")
	})

	t.Run("GIVEN dashboard feedback WHEN an action fails THEN a sticky error toast is shown", func(t *testing.T) {
		notify := funcBody(t, core, "notify(level, message, sticky = level === 'error' || level === 'warning') {")
		mustContain(t, "notify", notify, "this.toasts.push(", "if (!sticky) setTimeout(")
		mustContain(t, "index", index, `x-for="t in toasts"`, `:role="t.level === 'error' ? 'alert' : 'status'"`, "dismissToast(t.id)")
	})

	t.Run("GIVEN settings and control requests WHEN they fail or the network drops THEN the error is surfaced", func(t *testing.T) {
		for _, fn := range []string{"async setTradingMode(mode) {", "async setTradingEnvironment(", "async selectStrategy(id) {"} {
			mustContain(t, fn, funcBody(t, settings, fn), "catch", "this.tradingSettingsError =")
		}
		mustContain(t, "loadSettings", funcBody(t, settings, "async loadSettings() {"), "throw new Error(")
		mustContain(t, "openSettings", funcBody(t, settings, "async openSettings() {"), "catch", "this.settingsError =")
		mustContain(t, "saveSettings", funcBody(t, settings, "async saveSettings() {"), "catch", "this.settingsError =")
		toggle := funcBody(t, scripts["markets.js"], "async toggleMarket(market) {")
		mustContain(t, "toggleMarket", toggle, "response.ok", "catch", "this.notify('error'")
		show := funcBody(t, scripts["intelligence.js"], "async showSignal(tradeIdeaId) {")
		mustContain(t, "showSignal", show, "encodeURIComponent(tradeIdeaId)", "r.ok", "catch", "this.notify('error'")
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
		for name, pattern := range map[string]string{
			"Alpine.js":          `alpinejs@3\.\d+\.\d+/`,
			"Chart.js":           `chart\.js@4\.\d+\.\d+`,
			"Lightweight Charts": `lightweight-charts@4\.\d+\.\d+/`,
		} {
			if !regexp.MustCompile(pattern).MatchString(index) {
				t.Errorf("%s must be pinned to an exact version", name)
			}
		}
	})

	t.Run("GIVEN the dialogs WHEN they open THEN focus moves in, Tab stays inside and Escape closes them", func(t *testing.T) {
		for _, marker := range []string{`id="trading-settings"`, `id="settings-dialog"`, "<!-- ── Signal Detail Modal"} {
			dialog := between(t, index, marker, `role="dialog"`)
			mustContain(t, marker, dialog, "@keydown.escape.window=", "focusDialog($el)")
		}
		if n := strings.Count(index, `@keydown.tab="trapTab($event)"`); n != 3 {
			t.Errorf("expected the three dialogs to trap Tab, got %d", n)
		}
		mustContain(t, "trapTab", funcBody(t, core, "trapTab(event) {"), "event.preventDefault()", "first.focus()", "last.focus()")
	})
}
