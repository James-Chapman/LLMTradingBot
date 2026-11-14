// Package config defines the bot's settings: the built-in defaults, the
// registry of settings edited in the dashboard (fields.go) and the start-up
// flags needed before the database opens. Settings are stored in the
// database; there is no .env file.
package config

import "fmt"

// Settings holds every tunable value for the bot.
type Settings struct {
	// App / server
	Debug       bool
	Host        string
	Port        int
	CORSOrigins []string

	// Database — SQLite file path (start-up flag -db)
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

// EffectiveLogLevel is the log level to use: DEBUG when the Debug switch is
// on, otherwise LogLevel.
func (s Settings) EffectiveLogLevel() string {
	if s.Debug {
		return "DEBUG"
	}
	return s.LogLevel
}

// Bootstrap holds the settings needed before the database opens. They come
// from tradingbot's command-line flags, never from the database.
type Bootstrap struct {
	DatabasePath string
	Host         string
	Port         int
	LogFile      string
}

// Apply copies the non-zero bootstrap values into s.
func (b Bootstrap) Apply(s *Settings) {
	if b.DatabasePath != "" {
		s.DatabasePath = b.DatabasePath
	}
	if b.Host != "" {
		s.Host = b.Host
	}
	if b.Port != 0 {
		s.Port = b.Port
	}
	if b.LogFile != "" {
		s.LogFile = b.LogFile
	}
}
