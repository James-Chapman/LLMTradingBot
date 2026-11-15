package server

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestFrontendBacktests covers the read-only Backtests view: how it reaches
// the viewer API, the order in which a result is shown (caveats before
// numbers), how null metrics and server text are rendered, and the
// lifecycle of its requests and its chart.
func TestFrontendBacktests(t *testing.T) {
	index := readFrontend(t, "index.html")
	scripts := appScripts(t)
	bt := scripts["backtests.js"]
	section := between(t, index, `<section data-view="backtests"`, "</section>")

	t.Run("GIVEN the dashboard WHEN the Backtests module is inspected THEN it registers its view, has a nav entry and a labelled section", func(t *testing.T) {
		mustContain(t, "backtests.js", bt, "Dashboard.register('backtests', {", "initBacktests() {", "onBacktestsShown(arg) {", "onBacktestsHidden() {")
		mustContain(t, "index", index, `<a href="#/backtests" class="nav-item"`)
		mustContain(t, "backtests section", section, `aria-labelledby="h-backtests"`, `<h1 id="h-backtests" tabindex="-1"`)
	})

	t.Run("GIVEN a backtest id with slashes WHEN any API URL is built THEN each path segment is encoded on its own", func(t *testing.T) {
		path := funcBody(t, bt, "function backtestPath(id) {")
		mustContain(t, "backtestPath", path, ".split('/').map(encodeURIComponent).join('/')")
		url := funcBody(t, bt, "function backtestUrl(id, params) {")
		mustContain(t, "backtestUrl", url, "'/api/backtests/' + backtestPath(id)", "new URLSearchParams(")
		// Every request of the view goes through those two helpers or is the list itself.
		for _, m := range regexp.MustCompile(`_btFetch\([^;]*`).FindAllString(bt, -1) {
			if strings.HasPrefix(m, "_btFetch(res,") {
				continue // the definition
			}
			if !strings.Contains(m, "backtestUrl(") && !strings.Contains(m, "'/api/backtests'") {
				t.Errorf("request not built with backtestUrl: %s", m)
			}
		}
		for _, part := range []string{"part: 'equity'", "part: 'trades'", "part: 'orders'", "part: 'summary_md'"} {
			mustContain(t, "backtests.js", bt, part)
		}
		// Runs open through a hash route built from the encoded id; no server value becomes an href.
		mustContain(t, "backtestHref", funcBody(t, bt, "function backtestHref(id) {"), "'#/backtests/' + backtestPath(id)")
		mustContain(t, "openBacktest", funcBody(t, bt, "openBacktest(id) {"), "location.hash = backtestHref(id)")
		mustNotContain(t, "backtests section", section, ":href=", "x-bind:href=")
	})

	t.Run("GIVEN a run's detail WHEN the markup is read THEN the honesty block comes before the verdict and the key figures and is never collapsible", func(t *testing.T) {
		mustAppearBefore(t, "backtests section", section, `id="bt-honesty"`, `id="bt-verdict"`)
		mustAppearBefore(t, "backtests section", section, `id="bt-verdict"`, `id="bt-figures"`)
		mustAppearBefore(t, "backtests section", section, `id="bt-figures"`, `id="bt-equity"`)
		at := strings.Index(section, `id="bt-honesty"`)
		if at < 0 {
			t.Fatal(`honesty block id="bt-honesty" missing`)
		}
		honesty := section[:at]
		if open, closed := strings.Count(honesty, "<details"), strings.Count(honesty, "</details>"); open != closed {
			t.Error("the honesty block sits inside a <details> disclosure")
		}
		block := between(t, section, `id="bt-honesty"`, `id="bt-verdict"`)
		mustNotContain(t, "honesty block", block, "<details", "x-collapse", "@click")
		// Multi-run summaries carry the backtester's own fixed "Read this first" lines (render_multi.go header()).
		multi, err := os.ReadFile(filepath.Join("..", "backtest", "render_multi.go"))
		if err != nil {
			t.Fatalf("read render_multi.go: %v", err)
		}
		for _, line := range []string{
			"Every run here replays one historical path; robustness tests reduce, but do not remove, the chance that a result is luck or fitted to that path.",
			"Survivorship, fee tier and in-sample warnings of each run are in its own summary.md under runs/.",
		} {
			mustContain(t, "render_multi.go", string(multi), line)
			mustContain(t, "backtests.js", bt, line)
		}
	})

	t.Run("GIVEN server text WHEN the view renders it THEN it is never markup and the Markdown summary stays plain text", func(t *testing.T) {
		mustNotContain(t, "backtests.js", bt, "innerHTML", "insertAdjacentHTML", "x-html", "document.write(", "marked", "DOMParser")
		mustNotContain(t, "backtests section", section, "x-html", "innerHTML")
		mustContain(t, "backtests section", section, `<pre class="bt-pre" x-text="btSummaryMd.data">`)
		// No server value inside an inline event handler: handlers only call component methods.
		mustNotContain(t, "backtests section", section, `onclick=`, `:onclick`, `x-bind:onclick`)
	})

	t.Run("GIVEN a metric the backtester reported as null WHEN it is formatted THEN it shows n/a, never 0", func(t *testing.T) {
		value := funcBody(t, bt, "function btValue(value, format) {")
		mustContain(t, "btValue", value, "value === null || value === undefined", "return 'n/a'")
		mustAppearBefore(t, "btValue", value, "return 'n/a'", "switch (format)")
		figures := funcBody(t, bt, "get btFigures() {")
		mustContain(t, "btFigures", figures, "btValue(m.win_rate_pct", "btValue(m.profit_factor", "m.profit_factor_note",
			"m.annualised_note", "m.ratio_note", "m.stale_positions > 0", "m.net_pnl_worst_case")
		mustNotContain(t, "btFigures", figures, "|| 0", "?? 0")
	})

	t.Run("GIVEN a verdict WHEN it is shown THEN only the backtester's positive rules are green and the text is not rewritten", func(t *testing.T) {
		mustContain(t, "backtests.js", bt, "const BT_POSITIVE_RULES = ['positive_not_validated', 'above_buy_and_hold', 'positive_in_and_out_of_sample', 'positive_at_every_tested_fee'];")
		cls := funcBody(t, bt, "function btVerdictClass(rule) {")
		mustContain(t, "btVerdictClass", cls, "BT_POSITIVE_RULES.includes(rule)")
		verdict := between(t, section, `id="bt-verdict"`, `id="bt-figures"`)
		mustContain(t, "verdict markup", verdict, `x-text="line.text"`, `x-text="line.rule"`)
	})

	t.Run("GIVEN the equity chart WHEN a run is left or another is drawn THEN the one chart instance is destroyed first", func(t *testing.T) {
		destroy := funcBody(t, bt, "_destroyBacktestChart() {")
		mustContain(t, "_destroyBacktestChart", destroy, "backtestChart.destroy()", "backtestChart = null")
		render := funcBody(t, bt, "_renderBacktestChart() {")
		mustContain(t, "_renderBacktestChart", render, "this._destroyBacktestChart()", "new Chart(")
		mustAppearBefore(t, "_renderBacktestChart", render, "this._destroyBacktestChart()", "new Chart(")
		mustContain(t, "_resetBacktestDetail", funcBody(t, bt, "_resetBacktestDetail() {"), "this._destroyBacktestChart()")
		mustContain(t, "onBacktestsHidden", funcBody(t, bt, "onBacktestsHidden() {"), "this._resetBacktestDetail()")
		if n := strings.Count(bt, "new Chart("); n != 1 {
			t.Errorf("expected one chart construction, got %d", n)
		}
	})

	t.Run("GIVEN a slow response WHEN the user has moved to another run or left THEN the late response is dropped", func(t *testing.T) {
		fetch := funcBody(t, bt, "async _btFetch(res, url, as = 'json') {")
		mustContain(t, "_btFetch", fetch, "const seq = ++res.seq;", "if (seq !== res.seq) return false;")
		mustAppearBefore(t, "_btFetch", fetch, "if (seq !== res.seq) return false;", "res.data = body")
		reset := funcBody(t, bt, "_resetBacktestDetail() {")
		mustContain(t, "_resetBacktestDetail", reset, "res.seq++")
		after := funcBody(t, bt, "_afterBacktestDetail(id) {")
		mustContain(t, "_afterBacktestDetail", after, "id !== this.btDetailId", "status === 404", "history.replaceState(null, '', '#/backtests')")
	})

	t.Run("GIVEN the Backtests view WHEN it loads data THEN it only reads, on entering the view or on Refresh, never on a timer", func(t *testing.T) {
		mustNotContain(t, "backtests.js", bt, "method:", "'POST'", "'PUT'", "'DELETE'", "'PATCH'", "setInterval(", "pollWhileShown(")
		shown := funcBody(t, bt, "onBacktestsShown(arg) {")
		mustContain(t, "onBacktestsShown", shown, "this.loadBacktests()", "this.openBacktestDetail(arg)")
		mustContain(t, "backtests section", section, `@click="loadBacktests()"`)
	})
}
