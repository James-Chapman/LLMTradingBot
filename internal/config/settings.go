// Package config defines the bot's settings: the built-in defaults, the
// registry of settings edited in the dashboard (fields.go) and the start-up
// flags needed before the database opens. Settings are stored in the
// database; there is no .env file.
package config

import (
	"fmt"
	"net"
	"slices"
	"strings"
)

// Settings holds every tunable value for the bot.
type Settings struct {
	// App / server
	Debug        bool
	Host         string
	Port         int
	CORSOrigins  []string
	AllowedHosts []string // extra Host header names accepted when not bound to loopback

	// Database — SQLite file path (start-up flag -db)
	DatabasePath string

	// Trading
	BaseCurrency            string
	StartingCapital         float64
	TargetTradeAmount       float64
	MaxLossPerTradePercent  float64
	MaxDailyLossPercent     float64
	MinTradeSize            float64
	StopLossPct             float64
	TrailingStopPct         float64
	MomentumLookbackTicks   int
	StrategyIntervalSeconds int // seconds between strategy ticks
	FeeAndSlippage          float64
	MinSignalConfidence     float64
	LLMVetoThreshold        float64
	LLMShadowReview         bool // ask the LLM about non-LLM 4-hour entries and only record its verdict
	Min24hVolume            float64
	AlertWebhookURL         string

	// Modes
	TradingMode        string // manual | semi_automated | fully_automated
	TradingEnvironment string // paper | live

	// Kraken
	KrakenAPIKey    string
	KrakenAPISecret string
	KrakenRESTURL   string

	// News: extra feeds ("Name::URL") on top of BuiltInNewsFeeds, and the
	// names of sources (built-in or extra) switched off in the dashboard
	NewsSources         []string
	DisabledNewsSources []string

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

// DefaultMarketCount is how many markets the bot trades when none were saved
// from the dashboard: the biggest pairs on Kraken by 24h traded value.
const DefaultMarketCount = 50

// PriceHistoryTicks is how many price ticks per market the bot keeps in
// memory (app.priceHistoryMax). Momentum over N ticks needs N+1 of them, so
// MOMENTUM_LOOKBACK_TICKS is capped at PriceHistoryTicks-1.
const PriceHistoryTicks = 200

// defaultMarkets is the fallback when Kraken cannot be asked for its biggest
// pairs at start-up: the 50 biggest EUR pairs by 24h traded value on
// 2026-11-14, stablecoins excluded.
var defaultMarkets = []string{
	"BTC/EUR", "ETH/EUR", "SOL/EUR", "XRP/EUR", "ZEC/EUR", "HYPE/EUR", "NEAR/EUR", "AVAX/EUR",
	"LINK/EUR", "ADA/EUR", "SUI/EUR", "UNI/EUR", "LTC/EUR", "TAO/EUR", "PUMP/EUR", "INJ/EUR",
	"DOGE/EUR", "XLM/EUR", "NIGHT/EUR", "PEPE/EUR", "ZRO/EUR", "ONDO/EUR", "ENA/EUR", "QNT/EUR",
	"VVV/EUR", "XDC/EUR", "TRX/EUR", "JUP/EUR", "AAVE/EUR", "BCH/EUR", "BNB/EUR", "DOT/EUR",
	"HBAR/EUR", "PAXG/EUR", "FET/EUR", "USELESS/EUR", "TIA/EUR", "CRV/EUR", "ARB/EUR", "ALGO/EUR",
	"RENDER/EUR", "W/EUR", "TRUMP/EUR", "ICP/EUR", "VIRTUAL/EUR", "ATOM/EUR", "WLD/EUR", "MON/EUR",
	"ZAMA/EUR", "OP/EUR",
}

// Defaults returns the built-in default settings.
func Defaults() Settings {
	return Settings{
		Host:                    "127.0.0.1",
		Port:                    8000,
		CORSOrigins:             []string{"http://localhost:8000", "http://127.0.0.1:8000"},
		DatabasePath:            "./trading_bot.db",
		BaseCurrency:            "EUR",
		StartingCapital:         500.0,
		TargetTradeAmount:       100.0,
		MaxLossPerTradePercent:  5.0,
		MaxDailyLossPercent:     5.0,
		MinTradeSize:            50.0,
		StopLossPct:             0.05,
		TrailingStopPct:         0.03,
		MomentumLookbackTicks:   10,
		StrategyIntervalSeconds: 60,
		FeeAndSlippage:          0.0036,
		MinSignalConfidence:     0.65,
		LLMVetoThreshold:        0.70,
		LLMShadowReview:         true,
		Min24hVolume:            0.0,
		TradingMode:             "manual",
		TradingEnvironment:      "paper",
		KrakenRESTURL:           "https://api.kraken.com",
		NewsSources:             []string{},
		DisabledNewsSources:     []string{},
		FixedMarkets:            slices.Clone(defaultMarkets),
		DynamicUniverseSource:   "coingecko",
		MaxEthEcosystemCoins:    10,
		LMStudioBaseURL:         "http://localhost:1234/v1",
		LMStudioTimeout:         60,
		OpenAITimeout:           30,
		LLMOnlyMaxConcurrency:   3,
		LogLevel:                "INFO",
		LogFile:                 "trading_bot.log",
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

// IsLoopbackHost reports whether a listen address only accepts connections
// from this computer: "localhost" or a loopback IP such as 127.0.0.1 or ::1.
// An empty host or 0.0.0.0 listens on every interface and is not loopback.
func IsLoopbackHost(host string) bool {
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
