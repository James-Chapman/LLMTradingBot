// Package config loads bot settings from environment variables and a .env file.
//
// Precedence (highest first): real environment variables, then the .env file,
// then the defaults defined in Defaults(). List-valued settings are JSON arrays,
// e.g. FIXED_MARKETS=["BTC/EUR","ETH/EUR"].
package config

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Settings holds every tunable value for the bot.
type Settings struct {
	// App / server
	Debug       bool
	Host        string
	Port        int
	CORSOrigins []string

	// Database — a plain SQLite file path. A legacy SQLAlchemy URL
	// ("sqlite:///./trading_bot.db") is accepted and converted.
	DatabasePath string

	// Trading
	BaseCurrency           string
	StartingCapital        float64
	TargetTradeAmount      float64
	MaxLossPerTradePercent float64
	MaxDailyLossPercent    float64
	MinTradeSize           float64
	StopLossPct            float64
	TrailingStopPct        float64
	MomentumLookbackTicks  int
	FeeAndSlippage         float64
	MinSignalConfidence    float64
	LLMVetoThreshold       float64
	Min24hVolume           float64
	AlertWebhookURL        string

	// Modes
	TradingMode        string // manual | semi_automated | fully_automated
	TradingEnvironment string // paper | live

	// Kraken
	KrakenAPIKey    string
	KrakenAPISecret string
	KrakenRESTURL   string

	// News sources, each "Name::URL"
	NewsSources []string

	// Universe
	FixedMarkets          []string
	DynamicUniverseSource string
	MaxEthEcosystemCoins  int

	// Primary LLM — LM Studio (OpenAI-compatible server, normally local)
	LMStudioBaseURL string
	LMStudioAPIKey  string
	LMStudioModel   string
	LMStudioTimeout int // seconds

	// Fallback LLM — any remote OpenAI-compatible endpoint
	OpenAIBaseURL string
	OpenAIAPIKey  string
	OpenAIModel   string
	OpenAITimeout int // seconds

	LLMOnlyMaxConcurrency int

	// Logging
	LogLevel string
	LogFile  string
}

// Defaults returns the built-in default settings.
func Defaults() Settings {
	return Settings{
		Host:                   "127.0.0.1",
		Port:                   8000,
		CORSOrigins:            []string{"http://localhost:8000", "http://127.0.0.1:8000"},
		DatabasePath:           "./trading_bot.db",
		BaseCurrency:           "EUR",
		StartingCapital:        500.0,
		TargetTradeAmount:      100.0,
		MaxLossPerTradePercent: 5.0,
		MaxDailyLossPercent:    5.0,
		MinTradeSize:           50.0,
		StopLossPct:            0.05,
		TrailingStopPct:        0.03,
		MomentumLookbackTicks:  10,
		FeeAndSlippage:         0.0036,
		MinSignalConfidence:    0.65,
		LLMVetoThreshold:       0.70,
		Min24hVolume:           0.0,
		TradingMode:            "manual",
		TradingEnvironment:     "paper",
		KrakenRESTURL:          "https://api.kraken.com",
		NewsSources: []string{
			"CoinDesk::https://www.coindesk.com/arc/outboundfeeds/rss/",
			"CoinTelegraph::https://cointelegraph.com/rss",
			"The Block::https://www.theblock.co/rss.xml",
			"Decrypt::https://decrypt.co/feed",
			"Bitcoin Magazine::https://bitcoinmagazine.com/.rss/full/",
			"CryptoSlate::https://cryptoslate.com/feed/",
			"The Defiant::https://thedefiant.io/feed/",
			"CryptoPotato::https://cryptopotato.com/feed/",
			"NewsBTC::https://www.newsbtc.com/feed/",
		},
		FixedMarkets:          []string{"BTC/EUR", "ETH/EUR"},
		DynamicUniverseSource: "coinmarketcap",
		MaxEthEcosystemCoins:  10,
		LMStudioBaseURL:       "http://localhost:1234/v1",
		LMStudioTimeout:       60,
		OpenAITimeout:         30,
		LLMOnlyMaxConcurrency: 3,
		LogLevel:              "INFO",
		LogFile:               "trading_bot.log",
	}
}

// Load builds Settings from defaults, the given .env file (if it exists) and
// the process environment. An empty envFile skips file loading.
func Load(envFile string) (Settings, error) {
	fileVals := map[string]string{}
	if envFile != "" {
		vals, err := ReadEnvFile(envFile)
		if err != nil && !os.IsNotExist(err) {
			return Settings{}, err
		}
		fileVals = vals
	}
	lookup := func(key string) (string, bool) {
		if v, ok := os.LookupEnv(key); ok {
			return v, true
		}
		v, ok := fileVals[key]
		return v, ok
	}
	return FromLookup(lookup)
}

// FromLookup builds Settings from defaults overlaid with values from lookup.
// It is the pure core of Load and is what tests exercise.
func FromLookup(lookup func(string) (string, bool)) (Settings, error) {
	s := Defaults()
	p := parser{lookup: lookup}

	p.boolean("DEBUG", &s.Debug)
	p.str("HOST", &s.Host)
	p.integer("PORT", &s.Port)
	p.list("CORS_ORIGINS", &s.CORSOrigins)

	if v, ok := lookup("DATABASE_PATH"); ok && v != "" {
		s.DatabasePath = v
	} else if v, ok := lookup("DATABASE_URL"); ok && v != "" {
		s.DatabasePath = strings.TrimPrefix(v, "sqlite:///")
	}

	p.str("BASE_CURRENCY", &s.BaseCurrency)
	p.float("STARTING_CAPITAL", &s.StartingCapital)
	p.float("TARGET_TRADE_AMOUNT", &s.TargetTradeAmount)
	p.float("MAX_LOSS_PER_TRADE_PERCENT", &s.MaxLossPerTradePercent)
	p.float("MAX_DAILY_LOSS_PERCENT", &s.MaxDailyLossPercent)
	p.float("MIN_TRADE_SIZE", &s.MinTradeSize)
	p.float("STOP_LOSS_PCT", &s.StopLossPct)
	p.float("TRAILING_STOP_PCT", &s.TrailingStopPct)
	p.integer("MOMENTUM_LOOKBACK_TICKS", &s.MomentumLookbackTicks)
	p.float("FEE_AND_SLIPPAGE", &s.FeeAndSlippage)
	p.float("MIN_SIGNAL_CONFIDENCE", &s.MinSignalConfidence)
	p.float("LLM_VETO_THRESHOLD", &s.LLMVetoThreshold)
	p.float("MIN_24H_VOLUME", &s.Min24hVolume)
	p.str("ALERT_WEBHOOK_URL", &s.AlertWebhookURL)

	p.str("TRADING_MODE", &s.TradingMode)
	p.str("TRADING_ENVIRONMENT", &s.TradingEnvironment)

	p.str("KRAKEN_API_KEY", &s.KrakenAPIKey)
	p.str("KRAKEN_API_SECRET", &s.KrakenAPISecret)
	p.str("KRAKEN_REST_URL", &s.KrakenRESTURL)

	p.list("NEWS_SOURCES", &s.NewsSources)
	p.list("FIXED_MARKETS", &s.FixedMarkets)
	p.str("DYNAMIC_UNIVERSE_SOURCE", &s.DynamicUniverseSource)
	p.integer("MAX_ETH_ECOSYSTEM_COINS", &s.MaxEthEcosystemCoins)

	p.str("LMSTUDIO_BASE_URL", &s.LMStudioBaseURL)
	p.str("LMSTUDIO_API_KEY", &s.LMStudioAPIKey)
	p.str("LMSTUDIO_MODEL", &s.LMStudioModel)
	p.integer("LMSTUDIO_TIMEOUT", &s.LMStudioTimeout)

	p.str("OPENAI_BASE_URL", &s.OpenAIBaseURL)
	p.str("OPENAI_API_KEY", &s.OpenAIAPIKey)
	p.str("OPENAI_MODEL", &s.OpenAIModel)
	p.integer("OPENAI_TIMEOUT", &s.OpenAITimeout)

	p.integer("LLM_ONLY_MAX_CONCURRENCY", &s.LLMOnlyMaxConcurrency)

	p.str("LOG_LEVEL", &s.LogLevel)
	p.str("LOG_FILE", &s.LogFile)

	if p.err != nil {
		return Settings{}, p.err
	}
	return s, s.Validate()
}

// Validate checks enumerated settings hold an allowed value.
func (s Settings) Validate() error {
	switch s.TradingMode {
	case "manual", "semi_automated", "fully_automated":
	default:
		return fmt.Errorf("TRADING_MODE must be manual, semi_automated or fully_automated, got %q", s.TradingMode)
	}
	switch s.TradingEnvironment {
	case "paper", "live":
	default:
		return fmt.Errorf("TRADING_ENVIRONMENT must be paper or live, got %q", s.TradingEnvironment)
	}
	return nil
}

// ReadEnvFile parses a simple KEY=VALUE .env file. Blank lines and lines
// starting with '#' are ignored; matching surrounding quotes are stripped.
func ReadEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	vals := map[string]string{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // NEWS_SOURCES lines can be long
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(strings.TrimPrefix(key, "export "))
		vals[key] = unquote(strings.TrimSpace(value))
	}
	return vals, scanner.Err()
}

// unquote strips one pair of matching single or double quotes.
func unquote(v string) string {
	if len(v) >= 2 {
		first, last := v[0], v[len(v)-1]
		if (first == '"' || first == '\'') && first == last {
			return v[1 : len(v)-1]
		}
	}
	return v
}

// parser applies typed lookups and remembers the first conversion error.
type parser struct {
	lookup func(string) (string, bool)
	err    error
}

// get returns a non-empty value for key, if present.
func (p *parser) get(key string) (string, bool) {
	v, ok := p.lookup(key)
	if !ok || strings.TrimSpace(v) == "" {
		return "", false
	}
	return strings.TrimSpace(v), true
}

// fail records the first error encountered.
func (p *parser) fail(key string, err error) {
	if p.err == nil {
		p.err = fmt.Errorf("invalid value for %s: %w", key, err)
	}
}

// str overwrites dst when key is set (empty values keep the default).
func (p *parser) str(key string, dst *string) {
	if v, ok := p.get(key); ok {
		*dst = v
	}
}

// integer parses key as an int.
func (p *parser) integer(key string, dst *int) {
	if v, ok := p.get(key); ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			p.fail(key, err)
			return
		}
		*dst = n
	}
}

// float parses key as a float64.
func (p *parser) float(key string, dst *float64) {
	if v, ok := p.get(key); ok {
		n, err := strconv.ParseFloat(v, 64)
		if err != nil {
			p.fail(key, err)
			return
		}
		*dst = n
	}
}

// boolean parses key as a bool (true/false/1/0/yes/no).
func (p *parser) boolean(key string, dst *bool) {
	if v, ok := p.get(key); ok {
		switch strings.ToLower(v) {
		case "1", "true", "yes", "on":
			*dst = true
		case "0", "false", "no", "off":
			*dst = false
		default:
			p.fail(key, fmt.Errorf("not a boolean: %q", v))
		}
	}
}

// list parses key as a JSON array of strings, falling back to comma separation.
func (p *parser) list(key string, dst *[]string) {
	v, ok := p.get(key)
	if !ok {
		return
	}
	if strings.HasPrefix(v, "[") {
		var out []string
		if err := json.Unmarshal([]byte(v), &out); err != nil {
			p.fail(key, err)
			return
		}
		*dst = out
		return
	}
	var out []string
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	*dst = out
}
