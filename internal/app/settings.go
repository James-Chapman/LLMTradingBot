package app

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/secret"
	"github.com/James-Chapman/LLMTradingBot/internal/storage"
)

// SettingView is one setting as the dashboard sees it. Secret values are
// never included: Value is empty and IsSet says whether one is stored.
type SettingView struct {
	Key            string `json:"key"`
	Label          string `json:"label"`
	Group          string `json:"group"`
	Help           string `json:"help"`
	Kind           string `json:"kind"`
	Live           bool   `json:"live"`
	Secret         bool   `json:"secret"`
	Value          string `json:"value"`
	IsSet          bool   `json:"is_set"`
	RestartPending bool   `json:"restart_pending"` // saved, but the running bot still uses the old value
}

// SettingsView is the GET /api/settings response.
type SettingsView struct {
	Fields          []SettingView `json:"fields"`
	RestartRequired []string      `json:"restart_required"` // keys saved but not yet in use
}

// SettingsUpdate is the POST /api/settings response.
type SettingsUpdate struct {
	AppliedLive     []string `json:"applied_live"`     // changed keys already in use
	RestartRequired []string `json:"restart_required"` // every key waiting for a restart
}

// LoadSettings builds the settings the bot starts with: the built-in
// defaults, overlaid with the settings saved from the dashboard, then the
// start-up flags. No .env file or environment variable is read. A stored
// value that no longer parses (or a secret that cannot be decrypted) is
// skipped with a warning so the bot still starts.
func LoadSettings(ctx context.Context, repo *storage.Repository, box secret.Box, boot config.Bootstrap) (config.Settings, []string, error) {
	s := config.Defaults()
	stored, err := repo.LoadSettings(ctx)
	if err != nil {
		return s, nil, err
	}
	var warnings []string
	for _, f := range config.Fields() {
		raw, ok := stored[f.Key]
		if !ok {
			continue
		}
		if f.Secret {
			plain, err := secret.Decode(box, raw)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s: %v (treated as not set)", f.Key, err))
				continue
			}
			raw = plain
		}
		if err := f.Apply(&s, raw); err != nil {
			warnings = append(warnings, fmt.Sprintf("%v (using the default)", err))
		}
	}
	boot.Apply(&s)
	// The default browser origins follow the port unless they were customised.
	if _, customised := stored["CORS_ORIGINS"]; !customised {
		port := strconv.Itoa(s.Port)
		s.CORSOrigins = []string{"http://localhost:" + port, "http://127.0.0.1:" + port}
	}
	return s, warnings, nil
}

// cfg returns a copy of the settings the running components use.
func (a *App) cfg() config.Settings {
	a.settingsMu.RLock()
	defer a.settingsMu.RUnlock()
	return a.settings
}

// savedSettings returns a copy of the settings as saved in the database.
func (a *App) savedSettings() config.Settings {
	a.settingsMu.RLock()
	defer a.settingsMu.RUnlock()
	return a.saved
}

// SettingsView lists every dashboard setting with its saved value and
// whether it is waiting for a restart.
func (a *App) SettingsView() SettingsView {
	a.settingsMu.RLock()
	saved, running := a.saved, a.settings
	a.settingsMu.RUnlock()

	view := SettingsView{Fields: []SettingView{}, RestartRequired: []string{}}
	for _, f := range config.Fields() {
		v := SettingView{Key: f.Key, Label: f.Label, Group: f.Group, Help: f.Help, Kind: string(f.Kind), Live: f.Live, Secret: f.Secret}
		value := f.Value(saved)
		v.IsSet = value != ""
		if !f.Secret {
			v.Value = value
		}
		v.RestartPending = value != f.Value(running)
		if v.RestartPending {
			view.RestartRequired = append(view.RestartRequired, f.Key)
		}
		view.Fields = append(view.Fields, v)
	}
	return view
}

// UpdateSettings saves settings changed in the dashboard. Every value is
// validated before anything is written, and all are saved in one
// transaction. Live settings take effect at once (risk engine, strategies,
// strategy loop); the rest are used after the next restart. An empty value
// clears a secret.
func (a *App) UpdateSettings(ctx context.Context, changes map[string]string) (SettingsUpdate, error) {
	if len(changes) == 0 {
		return SettingsUpdate{}, statusError(http.StatusBadRequest, "No settings to save")
	}
	a.saveMu.Lock()
	defer a.saveMu.Unlock()

	keys := make([]string, 0, len(changes))
	for key := range changes {
		keys = append(keys, key)
	}
	slices.Sort(keys)

	// Validate and encode everything first, so a bad value saves nothing.
	next := a.savedSettings()
	stored := make(map[string]string, len(keys))
	for _, key := range keys {
		f, ok := config.FieldByKey(key)
		if !ok {
			return SettingsUpdate{}, statusError(http.StatusBadRequest, fmt.Sprintf("Unknown setting %s", key))
		}
		if err := f.Apply(&next, changes[key]); err != nil {
			return SettingsUpdate{}, statusError(http.StatusBadRequest, err.Error())
		}
		value := f.Value(next)
		if f.Secret {
			sealed, err := secret.Encode(a.box, value)
			if err != nil {
				return SettingsUpdate{}, statusError(http.StatusBadRequest, fmt.Sprintf("%s: %v", key, err))
			}
			value = sealed
		}
		stored[key] = value
	}
	if err := a.repo.SaveSettings(ctx, stored); err != nil {
		return SettingsUpdate{}, err
	}

	// Copy the changed live settings into the running settings.
	running := a.cfg()
	applied := []string{}
	for _, key := range keys {
		f, _ := config.FieldByKey(key)
		if f.Live && f.Value(running) != f.Value(next) {
			_ = f.Apply(&running, f.Value(next)) // already validated above
			applied = append(applied, key)
		}
	}
	a.settingsMu.Lock()
	a.saved, a.settings = next, running
	a.settingsMu.Unlock()

	if len(applied) > 0 {
		a.risk.SetSettings(running)
		fresh := a.strategyFactory(running)
		a.mu.Lock()
		a.strategies = fresh
		a.mu.Unlock()
		a.activity.Info("Settings applied: "+strings.Join(applied, ", "), "Changed from the dashboard; in use now.")
	}
	pending := a.SettingsView().RestartRequired
	if waiting := without(keys, applied); len(waiting) > 0 && len(pending) > 0 {
		a.activity.Warn("Settings saved: "+strings.Join(waiting, ", "), "Restart the bot to apply them.")
	}
	a.logger.Info("Settings saved", "keys", keys, "applied_live", applied, "restart_required", pending)
	a.bumpVersion()
	return SettingsUpdate{AppliedLive: applied, RestartRequired: pending}, nil
}

// without returns the keys not in remove.
func without(keys, remove []string) []string {
	var out []string
	for _, k := range keys {
		if !slices.Contains(remove, k) {
			out = append(out, k)
		}
	}
	return out
}

// MarketRanker lists the biggest markets quoted in a currency (the Kraken
// market adapter in production).
type MarketRanker interface {
	TopPairsByVolume(ctx context.Context, quote string, n int) ([]string, error)
}

// UseLiveDefaultMarkets replaces the built-in default market list in s with
// the biggest pairs on Kraken right now, unless markets were saved from the
// dashboard. It reports whether the live list is used; on failure (or an
// empty answer) the built-in list stays and the error is returned.
func UseLiveDefaultMarkets(ctx context.Context, repo *storage.Repository, s *config.Settings, ranker MarketRanker) (bool, error) {
	stored, err := repo.LoadSettings(ctx)
	if err != nil {
		return false, err
	}
	if _, saved := stored["FIXED_MARKETS"]; saved {
		return false, nil
	}
	markets, err := ranker.TopPairsByVolume(ctx, s.BaseCurrency, config.DefaultMarketCount)
	if err != nil {
		return false, err
	}
	if len(markets) == 0 {
		return false, fmt.Errorf("Kraken listed no %s pairs", s.BaseCurrency)
	}
	s.FixedMarkets = markets
	return true, nil
}
