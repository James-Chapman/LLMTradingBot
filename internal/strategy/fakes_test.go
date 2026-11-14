package strategy

import (
	"context"
	"sync"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/analysis"
	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/llm"
)

// fakeAnalyser is a deterministic Analyser that records its calls and tracks
// how many calls are in flight at once.
type fakeAnalyser struct {
	signal      llm.SignalAnalysis
	rec         llm.LLMTradeRecommendation
	delay       time.Duration
	mu          sync.Mutex
	signalCalls []llm.SignalRequest
	recCalls    []llm.RecommendationRequest
	inFlight    int
	maxInFlight int
}

// AnalyseSignal records the request and returns the configured analysis.
func (f *fakeAnalyser) AnalyseSignal(_ context.Context, req llm.SignalRequest) llm.SignalAnalysis {
	f.mu.Lock()
	f.signalCalls = append(f.signalCalls, req)
	f.mu.Unlock()
	return f.signal
}

// RecommendTrade records the request, optionally sleeps, and returns the configured recommendation.
func (f *fakeAnalyser) RecommendTrade(_ context.Context, req llm.RecommendationRequest) llm.LLMTradeRecommendation {
	f.mu.Lock()
	f.recCalls = append(f.recCalls, req)
	f.inFlight++
	if f.inFlight > f.maxInFlight {
		f.maxInFlight = f.inFlight
	}
	f.mu.Unlock()

	time.Sleep(f.delay)

	f.mu.Lock()
	f.inFlight--
	f.mu.Unlock()
	return f.rec
}

// scaleAnalyser returns a fake whose signal analysis uses the given confidence scale.
func scaleAnalyser(scale float64) *fakeAnalyser {
	return &fakeAnalyser{signal: llm.SignalAnalysis{ConfidenceScale: scale, LLMUsed: true, Reasoning: "fake"}}
}

// testSettings returns default settings for strategy tests.
func testSettings() config.Settings {
	return config.Defaults()
}

// bullishIndicators returns a fully populated set of indicators that support a long.
func bullishIndicators() analysis.Indicators {
	return analysis.Indicators{
		"rsi_14":        35.0,
		"ema_cross":     "bullish",
		"bb":            map[string]any{"position": 25.0},
		"macd":          map[string]any{"bias": "bullish", "signal_bias": "bullish", "histogram": 0.5},
		"stoch":         map[string]any{"k": 15.0, "d": 20.0},
		"williams_r":    -85.0,
		"price_changes": map[string]any{"5m": 1.2, "15m": 2.4},
		"atr_pct":       0.5,
	}
}

// bearishIndicators returns indicators that support a short (basic_strategy unit-test helper).
func bearishIndicators() analysis.Indicators {
	return analysis.Indicators{
		"rsi_14":        70.0,
		"ema_cross":     "bearish",
		"bb":            map[string]any{"position": 80.0},
		"macd":          map[string]any{"bias": "bearish", "signal_bias": "bearish"},
		"stoch":         map[string]any{"k": 85.0},
		"williams_r":    -15.0,
		"price_changes": map[string]any{"5m": -0.003, "15m": -0.002},
	}
}

// unitBullishIndicators mirrors _bullish_data() from the Python basic strategy unit tests.
func unitBullishIndicators() analysis.Indicators {
	return analysis.Indicators{
		"rsi_14":        30.0,
		"ema_cross":     "bullish",
		"bb":            map[string]any{"position": 20.0},
		"macd":          map[string]any{"bias": "bullish", "signal_bias": "bullish"},
		"stoch":         map[string]any{"k": 15.0},
		"williams_r":    -85.0,
		"price_changes": map[string]any{"5m": 0.003, "15m": 0.002},
	}
}

// single wraps one market's data in a strategy Input.
func single(market string, data MarketData) Input {
	return Input{MarketData: map[string]MarketData{market: data}}
}
