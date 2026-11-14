package backtest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
)

// settingsAllowed reports whether a dashboard setting can shape a backtest:
// the risk, sizing and signal settings, plus the base currency. Secrets,
// LLM, news, server and universe-source settings are refused.
func settingsAllowed(f config.Field) bool {
	if f.Secret {
		return false
	}
	return f.Group == config.GroupRisk || f.Group == config.GroupSignals || f.Key == "BASE_CURRENCY"
}

// LoadSettings returns the built-in defaults overlaid with the JSON object in
// path (keys as in the dashboard's settings registry, e.g.
// {"MIN_SIGNAL_CONFIDENCE": 0.7}). Every value is validated by the registry.
// An empty path returns the defaults. The bot's database is never read.
func LoadSettings(path string) (config.Settings, error) {
	s, _, err := LoadSettingsKeys(path)
	return s, err
}

// LoadSettingsKeys is LoadSettings that also returns the keys the file set,
// sorted (Config.ExplicitSettings), so a run can tell an explicit value from
// a default.
func LoadSettingsKeys(path string) (config.Settings, []string, error) {
	s := config.Defaults()
	if path == "" {
		return s, []string{}, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return config.Settings{}, nil, fmt.Errorf("settings file: %w", err)
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return config.Settings{}, nil, fmt.Errorf("settings file %s: %w", path, err)
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		field, ok := config.FieldByKey(key)
		switch {
		case !ok:
			return config.Settings{}, nil, fmt.Errorf("settings file %s: unknown setting %s", path, key)
		case field.Secret:
			return config.Settings{}, nil, fmt.Errorf("settings file %s: %s is a secret; a backtest never reads secrets", path, key)
		case !settingsAllowed(field):
			return config.Settings{}, nil, fmt.Errorf("settings file %s: %s does not affect a backtest", path, key)
		}
		if err := field.Apply(&s, rawText(values[key])); err != nil {
			return config.Settings{}, nil, fmt.Errorf("settings file %s: %w", path, err)
		}
	}
	return s, keys, nil
}

// rawText returns a JSON value as the text the settings registry parses: a
// string's contents, or the literal JSON for numbers, booleans and arrays.
func rawText(v json.RawMessage) string {
	v = bytes.TrimSpace(v)
	var s string
	if len(v) > 0 && v[0] == '"' && json.Unmarshal(v, &s) == nil {
		return s
	}
	return string(v)
}

// SettingsSnapshot returns every backtest-relevant setting in s as text, by
// key, for the result file. It never includes a secret.
func SettingsSnapshot(s config.Settings) map[string]string {
	out := map[string]string{}
	for _, f := range config.Fields() {
		if settingsAllowed(f) {
			out[f.Key] = f.Value(s)
		}
	}
	return out
}
