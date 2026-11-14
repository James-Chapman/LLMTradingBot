package server

import (
	"strings"
	"testing"
)

// TestFrontendSettings covers the dashboard's settings dialog, which replaces .env.
func TestFrontendSettings(t *testing.T) {
	index := readFrontend(t, "index.html")

	t.Run("GIVEN the header WHEN inspected THEN a Settings button opens the settings dialog", func(t *testing.T) {
		header := between(t, index, `<header class="app-header`, "</header>")
		mustContain(t, "header", header, `@click="openSettings()"`)
		mustContain(t, "index", index, `id="settings-dialog"`, `x-show="settingsOpen"`, "fetch('/api/settings')")
	})

	t.Run("GIVEN edited settings WHEN saved THEN the changes are listed in a confirmation before posting", func(t *testing.T) {
		fn := between(t, index, "async saveSettings(", "\n                },")
		confirmAt := strings.Index(fn, "confirm(")
		postAt := strings.Index(fn, "fetch('/api/settings', {")
		if confirmAt < 0 || postAt < 0 || confirmAt > postAt {
			t.Fatalf("saveSettings must confirm before posting:\n%s", fn)
		}
	})

	t.Run("GIVEN secret settings WHEN rendered THEN they use password inputs and are never pre-filled", func(t *testing.T) {
		dialog := between(t, index, `id="settings-dialog"`, "<!-- /settings-dialog -->")
		mustContain(t, "settings dialog", dialog, `type="password"`, "field.is_set", `autocomplete="new-password"`)
		mustNotContain(t, "settings dialog", dialog, `x-model="field.value" type="password"`)
	})

	t.Run("GIVEN settings that need a restart WHEN pending THEN the dashboard says so", func(t *testing.T) {
		mustContain(t, "index", index, "settingsRestartRequired.length", "Restart the bot to apply")
	})

	t.Run("GIVEN .env is gone WHEN the frontend is inspected THEN it no longer tells users to edit .env", func(t *testing.T) {
		mustNotContain(t, "index", index, ">.env<", "TRADING_MODE=")
	})
}
