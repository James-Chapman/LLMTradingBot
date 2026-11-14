package config

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Kind is how a setting's value is written as text.
type Kind string

// Setting kinds; lists are JSON arrays of strings.
const (
	KindString Kind = "string"
	KindInt    Kind = "int"
	KindFloat  Kind = "float"
	KindBool   Kind = "bool"
	KindList   Kind = "list"
)

// Setting groups, in the order the dashboard shows them.
const (
	GroupRisk     = "Risk & sizing"
	GroupSignals  = "Signals"
	GroupMarkets  = "Markets"
	GroupKraken   = "Kraken"
	GroupLLM      = "LLM"
	GroupNews     = "News & alerts"
	GroupAdvanced = "Advanced"
)

// Field describes one dashboard-managed setting. Live fields take effect as
// soon as they are saved; the rest are saved and used after a restart.
type Field struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Group  string `json:"group"`
	Help   string `json:"help"`
	Kind   Kind   `json:"kind"`
	Live   bool   `json:"live"`
	Secret bool   `json:"secret"`

	get   func(Settings) string
	apply func(*Settings, string) error
}

// Value returns the field's current value in s as text.
func (f Field) Value(s Settings) string { return f.get(s) }

// Apply parses raw and stores it in s. On error s is unchanged and the
// error names the key.
func (f Field) Apply(s *Settings, raw string) error {
	if err := f.apply(s, strings.TrimSpace(raw)); err != nil {
		return fmt.Errorf("%s: %w", f.Key, err)
	}
	return nil
}

// fields is the registry, in display order. Host, port, database path and log
// file are start-up flags; trading mode and environment have their own dialog.
var fields = []Field{
	// Risk & sizing (live: read by the risk engine on every evaluation)
	floatField("STARTING_CAPITAL", "Starting capital", GroupRisk, false, "Paper account size for a new database.", atLeast(0), func(s *Settings) *float64 { return &s.StartingCapital }),
	floatField("TARGET_TRADE_AMOUNT", "Target trade amount", GroupRisk, true, "Order value the risk engine aims for, in the base currency.", atLeast(0), func(s *Settings) *float64 { return &s.TargetTradeAmount }),
	floatField("MIN_TRADE_SIZE", "Minimum trade size", GroupRisk, true, "Smallest order value, in the base currency.", atLeast(0), func(s *Settings) *float64 { return &s.MinTradeSize }),
	floatField("MAX_LOSS_PER_TRADE_PERCENT", "Max loss per trade (%)", GroupRisk, true, "Largest loss one trade may risk, as a percent of equity.", between(0, 100), func(s *Settings) *float64 { return &s.MaxLossPerTradePercent }),
	floatField("MAX_DAILY_LOSS_PERCENT", "Max daily loss (%)", GroupRisk, true, "New trades stop for the UTC day once this percent of equity is lost.", between(0, 100), func(s *Settings) *float64 { return &s.MaxDailyLossPercent }),
	floatField("STOP_LOSS_PCT", "Stop-loss", GroupRisk, true, "Stop-loss distance as a fraction (0.05 = 5%).", between(0, 1), func(s *Settings) *float64 { return &s.StopLossPct }),
	floatField("TRAILING_STOP_PCT", "Trailing stop", GroupRisk, true, "Trailing stop distance as a fraction (0.03 = 3%).", between(0, 1), func(s *Settings) *float64 { return &s.TrailingStopPct }),
	floatField("FEE_AND_SLIPPAGE", "Fee and slippage", GroupRisk, true, "Round-trip cost assumed per trade as a fraction.", between(0, 1), func(s *Settings) *float64 { return &s.FeeAndSlippage }),
	floatField("MIN_24H_VOLUME", "Minimum 24h volume", GroupRisk, true, "Skip markets trading less than this in 24 hours (0 = off).", atLeast(0), func(s *Settings) *float64 { return &s.Min24hVolume }),

	// Signals (live)
	floatField("MIN_SIGNAL_CONFIDENCE", "Minimum signal confidence", GroupSignals, true, "Fully automated mode only trades signals at or above this (0–1).", between(0, 1), func(s *Settings) *float64 { return &s.MinSignalConfidence }),
	floatField("LLM_VETO_THRESHOLD", "LLM veto threshold", GroupSignals, true, "The combined strategy drops signals the LLM rates below this (0–1).", between(0, 1), func(s *Settings) *float64 { return &s.LLMVetoThreshold }),
	intField("MOMENTUM_LOOKBACK_TICKS", "Momentum lookback (ticks)", GroupSignals, true, "Price ticks used for the momentum calculation.", 1, func(s *Settings) *int { return &s.MomentumLookbackTicks }),
	intField("LLM_ONLY_MAX_CONCURRENCY", "LLM-only concurrency", GroupSignals, true, "Markets the LLM-only strategy analyses at once.", 1, func(s *Settings) *int { return &s.LLMOnlyMaxConcurrency }),

	// Markets (restart: the market list is validated at start-up)
	stringField("BASE_CURRENCY", "Base currency", GroupMarkets, false, "Quote currency for every market, e.g. EUR.", false, nonEmpty, func(s *Settings) *string { return &s.BaseCurrency }),
	listField("FIXED_MARKETS", "Fixed markets", GroupMarkets, false, "Markets always traded, e.g. BTC/EUR. Until saved here, the 50 biggest pairs on Kraken by 24h volume (stablecoins excluded), picked at each start.", func(s *Settings) *[]string { return &s.FixedMarkets }),
	stringField("DYNAMIC_UNIVERSE_SOURCE", "Dynamic market source", GroupMarkets, false, "Where extra markets come from: coinmarketcap or none.", false, nonEmpty, func(s *Settings) *string { return &s.DynamicUniverseSource }),
	intField("MAX_ETH_ECOSYSTEM_COINS", "Max dynamic markets", GroupMarkets, false, "Cap on markets added by the dynamic source.", 0, func(s *Settings) *int { return &s.MaxEthEcosystemCoins }),

	// Kraken (restart: the exchange client is built at start-up)
	stringField("KRAKEN_API_KEY", "Kraken API key", GroupKraken, false, "Needed for live trading. Stored encrypted.", true, nil, func(s *Settings) *string { return &s.KrakenAPIKey }),
	stringField("KRAKEN_API_SECRET", "Kraken API secret", GroupKraken, false, "Needed for live trading. Stored encrypted.", true, nil, func(s *Settings) *string { return &s.KrakenAPISecret }),
	stringField("KRAKEN_REST_URL", "Kraken REST URL", GroupKraken, false, "Kraken API base URL.", false, httpURL(false), func(s *Settings) *string { return &s.KrakenRESTURL }),

	// LLM (restart: clients are built at start-up)
	stringField("LMSTUDIO_BASE_URL", "LM Studio URL", GroupLLM, false, "Primary LLM, e.g. http://localhost:1234/v1. Empty = off.", false, httpURL(true), func(s *Settings) *string { return &s.LMStudioBaseURL }),
	stringField("LMSTUDIO_MODEL", "LM Studio model", GroupLLM, false, "Empty = use the model loaded in LM Studio.", false, nil, func(s *Settings) *string { return &s.LMStudioModel }),
	stringField("LMSTUDIO_API_KEY", "LM Studio API key", GroupLLM, false, "Only if your LM Studio server requires one. Stored encrypted.", true, nil, func(s *Settings) *string { return &s.LMStudioAPIKey }),
	intField("LMSTUDIO_TIMEOUT", "LM Studio timeout (s)", GroupLLM, false, "Seconds to wait for an LM Studio reply.", 1, func(s *Settings) *int { return &s.LMStudioTimeout }),
	stringField("OPENAI_BASE_URL", "Fallback LLM URL", GroupLLM, false, "OpenAI-compatible endpoint used while LM Studio is down. Empty = off.", false, httpURL(true), func(s *Settings) *string { return &s.OpenAIBaseURL }),
	stringField("OPENAI_MODEL", "Fallback LLM model", GroupLLM, false, "Model name for the fallback endpoint.", false, nil, func(s *Settings) *string { return &s.OpenAIModel }),
	stringField("OPENAI_API_KEY", "Fallback LLM API key", GroupLLM, false, "API key for the fallback endpoint. Stored encrypted.", true, nil, func(s *Settings) *string { return &s.OpenAIAPIKey }),
	intField("OPENAI_TIMEOUT", "Fallback LLM timeout (s)", GroupLLM, false, "Seconds to wait for a fallback reply.", 1, func(s *Settings) *int { return &s.OpenAITimeout }),

	// News & alerts
	listField("NEWS_SOURCES", "News feeds", GroupNews, false, "RSS feeds as Name::URL.", func(s *Settings) *[]string { return &s.NewsSources }),
	stringField("ALERT_WEBHOOK_URL", "Alert webhook URL", GroupNews, true, "Receives bot alerts (start, emergency stop, daily loss). Empty = off.", false, httpURL(true), func(s *Settings) *string { return &s.AlertWebhookURL }),

	// Advanced (restart)
	listField("CORS_ORIGINS", "Allowed browser origins", GroupAdvanced, false, "Origins allowed to call the API from a browser.", func(s *Settings) *[]string { return &s.CORSOrigins }),
	stringField("LOG_LEVEL", "Log level", GroupAdvanced, false, "DEBUG, INFO, WARNING or ERROR.", false, oneOf("DEBUG", "INFO", "WARNING", "ERROR"), func(s *Settings) *string { return &s.LogLevel }),
	boolField("DEBUG", "Debug logging", GroupAdvanced, false, "Log at DEBUG level whatever the log level says.", func(s *Settings) *bool { return &s.Debug }),
}

// Fields returns every dashboard-managed setting in display order.
func Fields() []Field { return append([]Field(nil), fields...) }

// FieldByKey looks up a dashboard-managed setting by its key.
func FieldByKey(key string) (Field, bool) {
	for _, f := range fields {
		if f.Key == key {
			return f, true
		}
	}
	return Field{}, false
}

// ---------------------------------------------------------------------------
// Field constructors
// ---------------------------------------------------------------------------

// stringField builds a text setting; check (optional) validates the value.
func stringField(key, label, group string, live bool, help string, secret bool, check func(string) error, ptr func(*Settings) *string) Field {
	return Field{Key: key, Label: label, Group: group, Help: help, Kind: KindString, Live: live, Secret: secret,
		get: func(s Settings) string { return *ptr(&s) },
		apply: func(s *Settings, raw string) error {
			if check != nil {
				if err := check(raw); err != nil {
					return err
				}
			}
			*ptr(s) = raw
			return nil
		}}
}

// floatField builds a decimal setting validated by check.
func floatField(key, label, group string, live bool, help string, check func(float64) error, ptr func(*Settings) *float64) Field {
	return Field{Key: key, Label: label, Group: group, Help: help, Kind: KindFloat, Live: live,
		get: func(s Settings) string { return strconv.FormatFloat(*ptr(&s), 'g', -1, 64) },
		apply: func(s *Settings, raw string) error {
			v, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				return fmt.Errorf("%q is not a number", raw)
			}
			if err := check(v); err != nil {
				return err
			}
			*ptr(s) = v
			return nil
		}}
}

// intField builds a whole-number setting with a minimum.
func intField(key, label, group string, live bool, help string, minimum int, ptr func(*Settings) *int) Field {
	return Field{Key: key, Label: label, Group: group, Help: help, Kind: KindInt, Live: live,
		get: func(s Settings) string { return strconv.Itoa(*ptr(&s)) },
		apply: func(s *Settings, raw string) error {
			v, err := strconv.Atoi(raw)
			if err != nil {
				return fmt.Errorf("%q is not a whole number", raw)
			}
			if v < minimum {
				return fmt.Errorf("must be at least %d", minimum)
			}
			*ptr(s) = v
			return nil
		}}
}

// boolField builds a true/false setting.
func boolField(key, label, group string, live bool, help string, ptr func(*Settings) *bool) Field {
	return Field{Key: key, Label: label, Group: group, Help: help, Kind: KindBool, Live: live,
		get: func(s Settings) string { return strconv.FormatBool(*ptr(&s)) },
		apply: func(s *Settings, raw string) error {
			v, err := strconv.ParseBool(raw)
			if err != nil {
				return fmt.Errorf("%q is not true or false", raw)
			}
			*ptr(s) = v
			return nil
		}}
}

// listField builds a list setting written as a JSON array of strings.
func listField(key, label, group string, live bool, help string, ptr func(*Settings) *[]string) Field {
	return Field{Key: key, Label: label, Group: group, Help: help, Kind: KindList, Live: live,
		get: func(s Settings) string {
			data, _ := json.Marshal(nonNil(*ptr(&s)))
			return string(data)
		},
		apply: func(s *Settings, raw string) error {
			var v []string
			if err := json.Unmarshal([]byte(raw), &v); err != nil {
				return fmt.Errorf("must be a JSON list of strings, e.g. [\"BTC/EUR\"]")
			}
			*ptr(s) = nonNil(v)
			return nil
		}}
}

// nonNil turns a nil list into an empty one so it is written as [].
func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// ---------------------------------------------------------------------------
// Validators
// ---------------------------------------------------------------------------

// atLeast requires v >= minimum.
func atLeast(minimum float64) func(float64) error {
	return func(v float64) error {
		if v < minimum {
			return fmt.Errorf("must be at least %g", minimum)
		}
		return nil
	}
}

// between requires lo <= v <= hi.
func between(lo, hi float64) func(float64) error {
	return func(v float64) error {
		if v < lo || v > hi {
			return fmt.Errorf("must be between %g and %g", lo, hi)
		}
		return nil
	}
}

// nonEmpty requires a non-blank value.
func nonEmpty(v string) error {
	if v == "" {
		return fmt.Errorf("must not be empty")
	}
	return nil
}

// httpURL requires an http(s) URL; allowEmpty permits "" to switch it off.
func httpURL(allowEmpty bool) func(string) error {
	return func(v string) error {
		if v == "" && allowEmpty {
			return nil
		}
		if !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
			return fmt.Errorf("must start with http:// or https://")
		}
		return nil
	}
}

// oneOf requires one of the allowed values.
func oneOf(allowed ...string) func(string) error {
	return func(v string) error {
		for _, a := range allowed {
			if v == a {
				return nil
			}
		}
		return fmt.Errorf("must be one of %s", strings.Join(allowed, ", "))
	}
}
