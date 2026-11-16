package config

import (
	"encoding/json"
	"fmt"
	"math"
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

// Bounds that keep a saved value usable by the components that read it.
const (
	// MaxStopPct caps STOP_LOSS_PCT and TRAILING_STOP_PCT: a stop further
	// than 50% away is no stop, and 0 would make the risk engine estimate
	// no loss and skip the per-trade loss check.
	MaxStopPct = 0.5
	// maxLLMTimeout (seconds) keeps the LLM timeouts far from overflowing
	// time.Duration.
	maxLLMTimeout = 600
	// MinStrategyIntervalSeconds and MaxStrategyIntervalSeconds bound
	// STRATEGY_INTERVAL_SECONDS: faster than 30 s adds load without fresher
	// prices; slower than 10 minutes leaves stops checked too rarely.
	MinStrategyIntervalSeconds = 30
	MaxStrategyIntervalSeconds = 600
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
	floatField("MAX_LOSS_PER_TRADE_PERCENT", "Max loss per trade (%)", GroupRisk, true, "Largest loss one trade may risk, as a percent of equity (above 0, up to 100). To stop trading use the emergency stop.", above(0, 100), func(s *Settings) *float64 { return &s.MaxLossPerTradePercent }),
	floatField("MAX_DAILY_LOSS_PERCENT", "Max daily loss (%)", GroupRisk, true, "New trades stop for the UTC day once this percent of equity is lost (above 0, up to 100). To stop trading use the emergency stop.", above(0, 100), func(s *Settings) *float64 { return &s.MaxDailyLossPercent }),
	floatField("STOP_LOSS_PCT", "Stop-loss", GroupRisk, true, "Stop-loss distance as a fraction (0.05 = 5%); above 0, at most 0.5.", above(0, MaxStopPct), func(s *Settings) *float64 { return &s.StopLossPct }),
	floatField("TRAILING_STOP_PCT", "Trailing stop", GroupRisk, true, "Trailing stop distance as a fraction (0.03 = 3%); above 0, at most 0.5.", above(0, MaxStopPct), func(s *Settings) *float64 { return &s.TrailingStopPct }),
	floatField("FEE_AND_SLIPPAGE", "Fee and slippage", GroupRisk, true, "Cost assumed per trade side as a fraction: the fee plus slippage (0.009 = Kraken Pro's 0.80% Tier 1 taker fee + 0.10% slippage). The risk engine sizes buys with it and the basic strategies' volatility filter compares candle ATR against it.", between(0, 1), func(s *Settings) *float64 { return &s.FeeAndSlippage }),
	floatField("MIN_24H_VOLUME", "Minimum 24h volume", GroupRisk, true, "Skip markets trading less than this in 24 hours (0 = off).", atLeast(0), func(s *Settings) *float64 { return &s.Min24hVolume }),

	// Signals (live)
	intField("STRATEGY_INTERVAL_SECONDS", "Strategy interval (s)", GroupSignals, true, "Seconds between strategy runs (30-600). Indicators count price ticks, so a longer interval also makes their windows and the warm-up last longer. Backtests run every 30 s unless a settings file sets this value.", MinStrategyIntervalSeconds, MaxStrategyIntervalSeconds, func(s *Settings) *int { return &s.StrategyIntervalSeconds }),
	floatField("MIN_SIGNAL_CONFIDENCE", "Minimum signal confidence", GroupSignals, true, "Fully automated mode only trades signals at or above this (0–1).", between(0, 1), func(s *Settings) *float64 { return &s.MinSignalConfidence }),
	floatField("LLM_VETO_THRESHOLD", "LLM veto threshold", GroupSignals, true, "The combined strategy drops signals the LLM rates below this (0–1).", between(0, 1), func(s *Settings) *float64 { return &s.LLMVetoThreshold }),
	boolField("LLM_SHADOW_REVIEW", "LLM shadow reviews", GroupSignals, true, "While a 4-hour strategy without LLM is selected, ask the LLM about each new entry as its LLM version would, and only record the answer. Intelligence → LLM Shadow Reviews shows whether its vetoes would have helped. Trades are never changed.", func(s *Settings) *bool { return &s.LLMShadowReview }),
	intField("BASIC_EXIT_VOTES", "Basic exit votes", GroupSignals, true, "Basic strategies: net indicator votes against a held position that count as a turn (1-10; a momentum move past its gate also counts).", 1, 10, func(s *Settings) *int { return &s.BasicExitVotes }),
	intField("BASIC_EXIT_CONFIRM_CANDLES", "Basic exit confirmation (candles)", GroupSignals, true, "Basic strategies: closed candles in a row the turn must hold before the rule exit sells (1-10).", 1, 10, func(s *Settings) *int { return &s.BasicExitConfirmCandles }),
	intField("BASIC_TREND_EMA", "Basic trend EMA", GroupSignals, true, "Basic strategies: longs only above, shorts only below this EMA of the higher timeframe (4-hour candles for 1h, days for 4h); 0 = off, at most 200.", 0, 200, func(s *Settings) *int { return &s.BasicTrendEMA }),
	floatField("BASIC_TRAIL_ATR", "Basic trailing stop (ATR)", GroupSignals, true, "Basic strategies: close a position once the price gives back this many candle ATRs from its best price since entry (0 = off, at most 20).", between(0, 20), func(s *Settings) *float64 { return &s.BasicTrailATR }),
	intField("BASIC_COOLDOWN_CANDLES", "Basic re-entry cooldown (candles)", GroupSignals, true, "Basic strategies: closed candles to wait after a position in a market closes before entering it again (0 = off, at most 100).", 0, 100, func(s *Settings) *int { return &s.BasicCooldownCandles }),
	intField("BASIC_ENTRY_VOTES", "Basic entry votes", GroupSignals, true, "Basic strategies: net indicator votes an entry signal needs (1-11; 1 = any consensus).", 1, 11, func(s *Settings) *int { return &s.BasicEntryVotes }),
	boolField("BASIC_FRESH_SIGNAL", "Basic fresh signals only", GroupSignals, true, "Basic strategies: enter only on the first closed candle of a signal, not on later candles while it persists.", func(s *Settings) *bool { return &s.BasicFreshSignal }),
	floatField("BASIC_MIN_ATR_COST", "Basic minimum ATR (costs)", GroupSignals, true, "Basic strategies: skip entries whose candle ATR% is below this many times FEE_AND_SLIPPAGE, too quiet for a typical move to pay the costs (0 = off, at most 20).", between(0, 20), func(s *Settings) *float64 { return &s.BasicMinATRCost }),
	floatField("BASIC_TAKE_PROFIT_ATR", "Basic profit target (ATR)", GroupSignals, true, "Basic strategies: close a position once the price is this many candle ATRs in profit from the entry (0 = off, at most 50).", between(0, 50), func(s *Settings) *float64 { return &s.BasicTakeProfitATR }),
	intField("MOMENTUM_LOOKBACK_TICKS", "Momentum lookback (ticks)", GroupSignals, true, "Price ticks used for the momentum calculation (1-199; the bot keeps 200 ticks per market).", 1, PriceHistoryTicks-1, func(s *Settings) *int { return &s.MomentumLookbackTicks }),
	intField("LLM_ONLY_MAX_CONCURRENCY", "LLM-only concurrency", GroupSignals, true, "Markets the LLM-only strategy analyses at once (1-16).", 1, 16, func(s *Settings) *int { return &s.LLMOnlyMaxConcurrency }),

	// Markets (restart: the market list is validated at start-up)
	stringField("BASE_CURRENCY", "Base currency", GroupMarkets, false, "Quote currency for every market, e.g. EUR.", false, nonEmpty, func(s *Settings) *string { return &s.BaseCurrency }),
	listField("FIXED_MARKETS", "Fixed markets", GroupMarkets, false, "Markets always traded, e.g. BTC/EUR. Until saved here, the 50 biggest pairs on Kraken by 24h volume (stablecoins excluded), picked at each start.", nil, func(s *Settings) *[]string { return &s.FixedMarkets }),
	stringField("DYNAMIC_UNIVERSE_SOURCE", "Dynamic market source", GroupMarkets, false, "Where extra markets come from: coingecko (Ethereum-ecosystem coins by market cap) or none. The old value coinmarketcap also means coingecko.", false, dynamicSource, func(s *Settings) *string { return &s.DynamicUniverseSource }),
	intField("MAX_ETH_ECOSYSTEM_COINS", "Max dynamic markets", GroupMarkets, false, "Cap on markets added by the dynamic source (0 = none, at most 50).", 0, 50, func(s *Settings) *int { return &s.MaxEthEcosystemCoins }),

	// Kraken (restart: the exchange client is built at start-up)
	stringField("KRAKEN_API_KEY", "Kraken API key", GroupKraken, false, "Needed for live trading. Stored encrypted.", true, nil, func(s *Settings) *string { return &s.KrakenAPIKey }),
	stringField("KRAKEN_API_SECRET", "Kraken API secret", GroupKraken, false, "Needed for live trading. Stored encrypted.", true, nil, func(s *Settings) *string { return &s.KrakenAPISecret }),
	stringField("KRAKEN_REST_URL", "Kraken REST URL", GroupKraken, false, "Kraken API base URL.", false, httpURL(false), func(s *Settings) *string { return &s.KrakenRESTURL }),

	// LLM (restart: clients are built at start-up)
	stringField("LMSTUDIO_BASE_URL", "LM Studio URL", GroupLLM, false, "Primary LLM, e.g. http://localhost:1234/v1. Empty = off.", false, httpURL(true), func(s *Settings) *string { return &s.LMStudioBaseURL }),
	stringField("LMSTUDIO_MODEL", "LM Studio model", GroupLLM, false, "Empty = use the model loaded in LM Studio.", false, nil, func(s *Settings) *string { return &s.LMStudioModel }),
	stringField("LMSTUDIO_API_KEY", "LM Studio API key", GroupLLM, false, "Only if your LM Studio server requires one. Stored encrypted.", true, nil, func(s *Settings) *string { return &s.LMStudioAPIKey }),
	intField("LMSTUDIO_TIMEOUT", "LM Studio timeout (s)", GroupLLM, false, "Seconds to wait for an LM Studio reply (1-600).", 1, maxLLMTimeout, func(s *Settings) *int { return &s.LMStudioTimeout }),
	stringField("OPENAI_BASE_URL", "Fallback LLM URL", GroupLLM, false, "OpenAI-compatible endpoint used while LM Studio is down. Empty = off.", false, httpURL(true), func(s *Settings) *string { return &s.OpenAIBaseURL }),
	stringField("OPENAI_MODEL", "Fallback LLM model", GroupLLM, false, "Model name for the fallback endpoint.", false, nil, func(s *Settings) *string { return &s.OpenAIModel }),
	stringField("OPENAI_API_KEY", "Fallback LLM API key", GroupLLM, false, "API key for the fallback endpoint. Stored encrypted.", true, nil, func(s *Settings) *string { return &s.OpenAIAPIKey }),
	intField("OPENAI_TIMEOUT", "Fallback LLM timeout (s)", GroupLLM, false, "Seconds to wait for a fallback reply (1-600).", 1, maxLLMTimeout, func(s *Settings) *int { return &s.OpenAITimeout }),

	// News & alerts
	// The dashboard shows this list as one on/off switch per news feed.
	listField("DISABLED_NEWS_SOURCES", "News feeds", GroupNews, true, "Switch a feed off to stop the bot reading it; its articles leave the news list at the next fetch. Every feed is on by default.", newsNames, func(s *Settings) *[]string { return &s.DisabledNewsSources }),
	listField("NEWS_SOURCES", "Extra news feeds", GroupNews, false, "Your own RSS or Atom feeds as Name::URL, read as well as the built-in feeds.", extraNewsFeeds, func(s *Settings) *[]string { return &s.NewsSources }),
	stringField("ALERT_WEBHOOK_URL", "Alert webhook URL", GroupNews, true, "Receives bot alerts (start, emergency stop, daily loss). Empty = off.", false, httpURL(true), func(s *Settings) *string { return &s.AlertWebhookURL }),

	// Advanced (restart)
	listField("CORS_ORIGINS", "Allowed browser origins", GroupAdvanced, false, "Other web origins allowed to call the API from a browser, e.g. http://192.168.1.10:8000. \"*\" is not allowed.", corsOrigins, func(s *Settings) *[]string { return &s.CORSOrigins }),
	listField("ALLOWED_HOSTS", "Allowed host names", GroupAdvanced, false, "Extra names the dashboard may be opened by when -host is not a loopback address, e.g. trading-pc.lan. localhost, IP addresses and this computer's name are always accepted.", hostNames, func(s *Settings) *[]string { return &s.AllowedHosts }),
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
			// ParseFloat accepts "NaN" and "Inf". NaN compares false against
			// every bound, so it would pass the range checks below.
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return fmt.Errorf("%q is not a finite number", raw)
			}
			if err := check(v); err != nil {
				return err
			}
			*ptr(s) = v
			return nil
		}}
}

// intField builds a whole-number setting between minimum and maximum.
func intField(key, label, group string, live bool, help string, minimum, maximum int, ptr func(*Settings) *int) Field {
	return Field{Key: key, Label: label, Group: group, Help: help, Kind: KindInt, Live: live,
		get: func(s Settings) string { return strconv.Itoa(*ptr(&s)) },
		apply: func(s *Settings, raw string) error {
			v, err := strconv.Atoi(raw)
			if err != nil {
				return fmt.Errorf("%q is not a whole number", raw)
			}
			if v < minimum || v > maximum {
				return fmt.Errorf("must be between %d and %d", minimum, maximum)
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

// listField builds a list setting written as a JSON array of strings; check
// (optional) validates the list and may return it normalised.
func listField(key, label, group string, live bool, help string, check func([]string) ([]string, error), ptr func(*Settings) *[]string) Field {
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
			if check != nil {
				checked, err := check(v)
				if err != nil {
					return err
				}
				v = checked
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

// above requires lo < v <= hi.
func above(lo, hi float64) func(float64) error {
	return func(v float64) error {
		if v <= lo || v > hi {
			return fmt.Errorf("must be above %g and at most %g", lo, hi)
		}
		return nil
	}
}

// dynamicSource accepts the dynamic market sources: coingecko, none, or the
// old name coinmarketcap (kept so a value saved by earlier versions loads).
func dynamicSource(v string) error {
	switch strings.ToLower(v) {
	case "coingecko", "coinmarketcap", "none":
		return nil
	}
	return fmt.Errorf("must be coingecko or none")
}

// corsOrigins refuses the wildcard origins: "*" would let every website read
// the API with the user's credentials, and "null" matches sandboxed pages.
func corsOrigins(origins []string) ([]string, error) {
	for _, o := range origins {
		if o == "*" || strings.EqualFold(o, "null") {
			return nil, fmt.Errorf("%q is not allowed; list each origin, e.g. http://192.168.1.10:8000", o)
		}
	}
	return origins, nil
}

// hostNames accepts bare host names (no scheme, port or path) and returns
// them lower-cased, as Host headers are compared.
func hostNames(hosts []string) ([]string, error) {
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" || strings.ContainsAny(h, ":/@ ") {
			return nil, fmt.Errorf("%q is not a host name; use a bare name like trading-pc.lan", h)
		}
		out = append(out, h)
	}
	return out, nil
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
