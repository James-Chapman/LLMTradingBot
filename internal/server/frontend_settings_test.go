package server

import (
	"strings"
	"testing"
)

// TestFrontendSettings covers the dashboard's settings dialog, which replaces .env.
func TestFrontendSettings(t *testing.T) {
	index := readFrontend(t, "index.html")
	settings := appScripts(t)["settings.js"]

	t.Run("GIVEN the header WHEN inspected THEN a Settings button opens the settings dialog", func(t *testing.T) {
		header := between(t, index, `<header class="app-header`, "</header>")
		mustContain(t, "header", header, `@click="openSettings()"`)
		mustContain(t, "index", index, `id="settings-dialog"`, `x-show="settingsOpen"`)
		mustContain(t, "settings.js", settings, "fetch('/api/settings')")
	})

	t.Run("GIVEN edited settings WHEN saved THEN the changes are listed in a confirmation before posting", func(t *testing.T) {
		fn := funcBody(t, settings, "async saveSettings(")
		confirmAt := strings.Index(fn, "confirm(")
		postAt := strings.Index(fn, "fetch('/api/settings', {")
		if confirmAt < 0 || postAt < 0 || confirmAt > postAt {
			t.Fatalf("saveSettings must confirm before posting:\n%s", fn)
		}
		mustContain(t, "saveSettings", fn, "this._describeSettingChange(")
	})

	t.Run("GIVEN unsaved changes WHEN the dialog is closed THEN they are only dropped after confirmation", func(t *testing.T) {
		mustContain(t, "closeSettings", funcBody(t, settings, "closeSettings() {"), "this.settingsChangeCount() > 0 && !confirm(")
	})

	t.Run("GIVEN secret settings WHEN rendered THEN they use password inputs and are never pre-filled", func(t *testing.T) {
		dialog := between(t, index, `id="settings-dialog"`, "<!-- /settings-dialog -->")
		mustContain(t, "settings dialog", dialog, `type="password"`, "field.is_set", `autocomplete="new-password"`)
		mustNotContain(t, "settings dialog", dialog, `x-model="field.value" type="password"`)
		mustContain(t, "resetSettingsDraft", funcBody(t, settings, "resetSettingsDraft(fields = this.settingsFields) {"),
			"if (field.secret) {\n                draft[field.key] = '';")
	})

	t.Run("GIVEN many settings WHEN the dialog is shown THEN one group at a time is picked from a side navigation", func(t *testing.T) {
		dialog := between(t, index, `id="settings-dialog"`, "<!-- /settings-dialog -->")
		mustContain(t, "settings dialog", dialog, `class="settings-nav"`, `@click="settingsGroup = group.name"`,
			`x-show="group.name === settingsGroup"`)
	})

	t.Run("GIVEN a setting WHEN rendered THEN its label and help sit beside a control sized for its kind", func(t *testing.T) {
		dialog := between(t, index, `id="settings-dialog"`, "<!-- /settings-dialog -->")
		mustContain(t, "settings dialog", dialog, `class="setting-text"`, `class="setting-control"`,
			`'is-number'`, `role="switch"`)
	})

	t.Run("GIVEN a list setting such as markets WHEN rendered THEN entries are removable chips with an add box, not a free-text area", func(t *testing.T) {
		dialog := between(t, index, `id="settings-dialog"`, "<!-- /settings-dialog -->")
		mustContain(t, "settings dialog", dialog, `class="chip-list"`, "removeListItem(", "addListItem(")
		mustNotContain(t, "settings dialog", dialog, "<textarea")
	})

	t.Run("GIVEN the settings are loaded WHEN the fields render THEN every list already has a draft to show", func(t *testing.T) {
		fn := funcBody(t, settings, "async loadSettings(")
		draftAt := strings.Index(fn, "this.resetSettingsDraft(data.fields)")
		fieldsAt := strings.Index(fn, "this.settingsFields = data.fields")
		if draftAt < 0 || fieldsAt < 0 || draftAt > fieldsAt {
			t.Fatalf("loadSettings must fill the draft before setting the fields:\n%s", fn)
		}
	})

	t.Run("GIVEN the add box of a list setting WHEN rendered THEN its button uses the themed secondary style", func(t *testing.T) {
		dialog := between(t, index, `id="settings-dialog"`, "<!-- /settings-dialog -->")
		mustContain(t, "settings dialog", dialog, `<button type="submit" class="btn btn-small btn-secondary">Add</button>`)
		styles := readFrontend(t, "static/styles.css")
		mustContain(t, "styles.css", styles, ".btn-secondary {")
	})

	t.Run("GIVEN settings that need a restart WHEN pending THEN the dashboard says so", func(t *testing.T) {
		mustContain(t, "index", index, "settingsRestartRequired.length", "Restart the bot to apply")
	})

	t.Run("GIVEN .env is gone WHEN the frontend is inspected THEN it no longer tells users to edit .env", func(t *testing.T) {
		all := dashboardSource(t)
		mustNotContain(t, "dashboard", all, ">.env<", "TRADING_MODE=")
	})
}
