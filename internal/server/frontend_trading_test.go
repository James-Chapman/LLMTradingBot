package server

import (
	"strings"
	"testing"
)

// TestFrontendTradingSettings covers the dashboard controls for changing the
// trading mode and paper/live environment, which must never change on one click.
func TestFrontendTradingSettings(t *testing.T) {
	index := readFrontend(t, "index.html")
	settings := appScripts(t)["settings.js"]

	t.Run("GIVEN the header WHEN the mode and environment badges are inspected THEN they open the trading settings dialog", func(t *testing.T) {
		header := between(t, index, `<header class="app-header`, "</header>")
		mustContain(t, "header", header, `@click="openTradingSettings()"`, "environment === 'live'", "modeOption(mode).short")
		mustContain(t, "index", index, `x-show="tradingSettingsOpen"`, `id="trading-settings"`)
	})

	t.Run("GIVEN a trading mode change WHEN the handler runs THEN it asks for confirmation before posting", func(t *testing.T) {
		fn := funcBody(t, settings, "async setTradingMode(")
		confirmAt := strings.Index(fn, "confirm(")
		postAt := strings.Index(fn, "fetch('/api/control/trading-mode'")
		if confirmAt < 0 || postAt < 0 || confirmAt > postAt {
			t.Fatalf("setTradingMode must confirm before posting:\n%s", fn)
		}
	})

	t.Run("GIVEN a switch to live WHEN the dialog is inspected THEN the button stays disabled until LIVE is typed", func(t *testing.T) {
		dialog := between(t, index, `id="trading-settings"`, "<!-- /trading-settings -->")
		mustContain(t, "trading settings", dialog, `x-model="liveConfirmText"`, `:disabled="liveConfirmText !== 'LIVE'"`)
		fn := funcBody(t, settings, "async setTradingEnvironment(")
		mustContain(t, "setTradingEnvironment", fn, "fetch('/api/control/trading-environment'", "confirmation: this.liveConfirmText",
			"environment === 'live' && this.liveConfirmText !== 'LIVE'")
		mustAppearBefore(t, "setTradingEnvironment", fn, "this.liveConfirmText !== 'LIVE'", "fetch(")
	})

	t.Run("GIVEN a switch back to paper WHEN the handler runs THEN it still asks for confirmation", func(t *testing.T) {
		fn := funcBody(t, settings, "async setTradingEnvironment(")
		mustContain(t, "setTradingEnvironment", fn, "environment === 'paper' && !confirm(")
	})

	t.Run("GIVEN the trading settings dialog WHEN inspected THEN the strategy choice sits below Trading mode and above Money", func(t *testing.T) {
		dialog := between(t, index, `id="trading-settings"`, "<!-- /trading-settings -->")
		modeAt := strings.Index(dialog, ">Trading mode</h3>")
		strategyAt := strings.Index(dialog, `@click="selectStrategy(`)
		moneyAt := strings.Index(dialog, ">Money</h3>")
		if modeAt < 0 || strategyAt < 0 || moneyAt < 0 || !(modeAt < strategyAt && strategyAt < moneyAt) {
			t.Fatalf("expected Trading mode, then strategy, then Money (got %d, %d, %d)", modeAt, strategyAt, moneyAt)
		}
	})

	t.Run("GIVEN the Markets view WHEN inspected THEN it does not hold the strategy selector", func(t *testing.T) {
		markets := between(t, index, `<section data-view="markets"`, "</section>")
		mustNotContain(t, "markets view", markets, "selectStrategy(")
	})

	t.Run("GIVEN a strategy change WHEN the handler runs THEN it asks for confirmation before posting", func(t *testing.T) {
		fn := funcBody(t, settings, "async selectStrategy(")
		confirmAt := strings.Index(fn, "confirm(")
		postAt := strings.Index(fn, "fetch(`/api/control/strategies/")
		if confirmAt < 0 || postAt < 0 || confirmAt > postAt {
			t.Fatalf("selectStrategy must confirm before posting:\n%s", fn)
		}
	})

	t.Run("GIVEN a refused change WHEN the API returns an error THEN its detail is shown to the user", func(t *testing.T) {
		for _, name := range []string{"async setTradingMode(", "async setTradingEnvironment(", "async selectStrategy("} {
			mustContain(t, name, funcBody(t, settings, name), "this.tradingSettingsError = await this._refusalDetail(response)")
		}
		mustContain(t, "trading settings", between(t, index, `id="trading-settings"`, "<!-- /trading-settings -->"),
			`x-text="tradingSettingsError"`)
	})
}
