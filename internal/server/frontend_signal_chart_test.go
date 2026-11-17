package server

import (
	"testing"
)

// TestSignalChart covers the candle chart in the signal dialog: the market's
// candles beside the signal's details, with the signal's time marked by a
// vertical line, opening on the strategy's own candles with a switch.
func TestSignalChart(t *testing.T) {
	index := readFrontend(t, "index.html")
	intelligence := readFrontend(t, "static/app/intelligence.js")
	styles := readFrontend(t, "static/styles.css")
	dialog := between(t, index, `class="modal-panel signal-panel"`, "<!-- Score breakdown -->")

	t.Run("GIVEN a signal is opened WHEN the dialog shows THEN a candle chart sits beside the signal details", func(t *testing.T) {
		mustContain(t, "signal dialog", dialog, `class="signal-layout"`, `class="signal-chart"`, `class="chart-pane signal-chart-pane"`, `class="signal-details stack"`)
		mustAppearBefore(t, "signal dialog", dialog, `class="signal-chart"`, `class="signal-details stack"`)
		// Side by side on wide screens, stacked on narrow ones.
		mustContain(t, "styles.css", styles, ".signal-layout {", "grid-template-columns: minmax(0, 1.15fr) minmax(0, 1fr);")
		mustContain(t, "styles.css", between(t, styles, "@media (max-width: 899px) {", "}"), ".signal-layout {")
	})

	t.Run("GIVEN the chart WHEN it opens THEN it uses the strategy's candles and offers the other intervals", func(t *testing.T) {
		mustContain(t, "signal dialog", dialog, `x-for="m in signalChartIntervals"`, `:aria-pressed="signalChart.minutes === m"`, `@click="loadSignalChart(m)"`)
		mustContain(t, "intelligence.js", intelligence, "signalChartIntervals: [5, 15, 60, 240]", "s.candle_minutes", "/api/ohlc/${encodeURIComponent(market)}?interval=${minutes}")
	})

	t.Run("GIVEN the candles WHEN they are drawn THEN a vertical line marks the signal's time and follows scrolling and resizing", func(t *testing.T) {
		mustContain(t, "signal dialog", dialog, `class="signal-chart-line"`)
		mustContain(t, "intelligence.js", intelligence, "timeToCoordinate(", "subscribeVisibleLogicalRangeChange(", "new ResizeObserver(")
		// timeToCoordinate gives an off-screen x (not null) for a candle scrolled out of view.
		placeLine := between(t, intelligence, "function placeSignalLine(view) {", "\n}")
		mustContain(t, "placeSignalLine", placeLine, "x < 0 || x > timeScale.width()")
		mustContain(t, "styles.css", styles, ".signal-chart-line {")
	})

	t.Run("GIVEN a signal older than the cached candles WHEN the chart is drawn THEN it says so instead of drawing a misplaced line", func(t *testing.T) {
		mustContain(t, "signal dialog", dialog, "signalChart.state === 'outside'")
		mustContain(t, "intelligence.js", intelligence, "state: 'outside'")
	})

	t.Run("GIVEN the dialog closes WHEN the chart is no longer shown THEN it is removed with its observers", func(t *testing.T) {
		closeFn := between(t, intelligence, "closeSignal() {", "\n    },")
		mustContain(t, "closeSignal", closeFn, "this._removeSignalChart();")
		mustContain(t, "intelligence.js", intelligence, "view.resizer.disconnect();", "view.chart.remove();")
	})
}
