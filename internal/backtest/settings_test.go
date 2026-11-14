package backtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
)

// writeFile writes content to a new file in a temp dir and returns its path.
func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestBacktestSettingsFile covers where a backtest takes its settings from.
func TestBacktestSettingsFile(t *testing.T) {
	t.Run("GIVEN no settings file WHEN settings load THEN the built-in defaults are used", func(t *testing.T) {
		s, err := LoadSettings("")
		if err != nil {
			t.Fatal(err)
		}
		if s.MinSignalConfidence != config.Defaults().MinSignalConfidence || s.StopLossPct != config.Defaults().StopLossPct {
			t.Fatalf("want defaults, got %+v", s)
		}
	})

	t.Run("GIVEN a JSON file with dashboard keys WHEN settings load THEN they are validated by the settings registry and applied", func(t *testing.T) {
		path := writeFile(t, "s.json", `{"MIN_SIGNAL_CONFIDENCE": 0.8, "MOMENTUM_LOOKBACK_TICKS": 12, "TARGET_TRADE_AMOUNT": "250", "BASE_CURRENCY": "EUR"}`)
		s, err := LoadSettings(path)
		if err != nil {
			t.Fatal(err)
		}
		if s.MinSignalConfidence != 0.8 || s.MomentumLookbackTicks != 12 || s.TargetTradeAmount != 250 {
			t.Fatalf("settings not applied: %+v", s)
		}
		snap := SettingsSnapshot(s)
		if snap["MIN_SIGNAL_CONFIDENCE"] != "0.8" || snap["MOMENTUM_LOOKBACK_TICKS"] != "12" {
			t.Fatalf("snapshot %v does not record the settings used", snap)
		}
		for key := range snap {
			if f, _ := config.FieldByKey(key); f.Secret {
				t.Fatalf("the snapshot records a secret field %s", key)
			}
		}
	})

	for name, raw := range map[string]string{
		"a secret":         `{"KRAKEN_API_KEY": "abc"}`,
		"an unknown key":   `{"NOT_A_SETTING": 1}`,
		"a live-only key":  `{"LMSTUDIO_BASE_URL": "http://localhost:1234/v1"}`,
		"an invalid value": `{"STOP_LOSS_PCT": 0.9}`,
		"malformed JSON":   `{"STOP_LOSS_PCT": `,
	} {
		t.Run("GIVEN a settings file with "+name+" WHEN settings load THEN it is refused", func(t *testing.T) {
			if _, err := LoadSettings(writeFile(t, "s.json", raw)); err == nil {
				t.Fatal("want an error")
			} else if name == "a secret" && !strings.Contains(err.Error(), "secret") {
				t.Fatalf("the error must say secrets are not read: %v", err)
			}
		})
	}
}

// TestPairRules covers the exchange rules file and its fallback.
func TestPairRules(t *testing.T) {
	t.Run("GIVEN no pair rules file WHEN rules are asked for THEN the documented conservative fallback applies", func(t *testing.T) {
		rules, err := LoadPairRules("")
		if err != nil {
			t.Fatal(err)
		}
		r, fromFile := rules.For("BTC/EUR")
		if fromFile || r != FallbackPairRule {
			t.Fatalf("got %+v fromFile=%v, want the fallback", r, fromFile)
		}
	})

	t.Run("GIVEN a Kraken AssetPairs export WHEN loaded THEN pairs are keyed by internal symbols and XBT/XDG are renamed", func(t *testing.T) {
		rules := mustPairRules(t, `{"error":[],"result":{
			"XXBTZEUR":{"wsname":"XBT/EUR","lot_decimals":8,"pair_decimals":1,"ordermin":"0.00005","costmin":"0.5","tick_size":"0.1"},
			"XDGEUR":{"wsname":"XDG/EUR","lot_decimals":8,"pair_decimals":7,"ordermin":"10","costmin":"0.5","tick_size":"0.0000001"}}}`)
		btc, ok := rules.For("BTC/EUR")
		if !ok || btc.OrderMin != 0.00005 || btc.CostMin != 0.5 || btc.PairDecimals != 1 || btc.LotDecimals != 8 {
			t.Fatalf("BTC/EUR rule %+v ok=%v", btc, ok)
		}
		if doge, ok := rules.For("DOGE/EUR"); !ok || doge.OrderMin != 10 {
			t.Fatalf("DOGE/EUR rule %+v ok=%v", doge, ok)
		}
		if _, ok := rules.For("ETH/EUR"); ok {
			t.Fatal("a pair missing from the file must use the fallback")
		}
	})
}
