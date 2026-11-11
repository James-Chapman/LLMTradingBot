package llm

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/analysis"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

// fakeLLMClient records chat messages and returns a scripted JSON reply.
type fakeLLMClient struct {
	mu         sync.Mutex
	available  bool
	canAttempt bool
	response   map[string]any
	messages   []Message
	calls      int
}

// newFakeLLMClient returns a ready client that answers with response.
func newFakeLLMClient(response map[string]any) *fakeLLMClient {
	if response == nil {
		response = map[string]any{
			"action": "long", "confidence": 0.82, "sentiment": 0.7, "reasoning": "Indicator context included",
		}
	}
	return &fakeLLMClient{available: true, canAttempt: true, response: response}
}

// Available reports the scripted availability.
func (f *fakeLLMClient) Available() bool { return f.available }

// CanAttempt reports the scripted circuit state.
func (f *fakeLLMClient) CanAttempt() bool { return f.canAttempt }

// Probe always succeeds.
func (f *fakeLLMClient) Probe(context.Context) bool { return true }

// ModelName returns a fixed display name.
func (f *fakeLLMClient) ModelName() string { return "fake" }

// Chat captures the messages and returns the scripted response.
func (f *fakeLLMClient) Chat(_ context.Context, messages []Message, _ bool) (Reply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.messages = messages
	if f.response == nil {
		return Reply{}, ErrUnavailable
	}
	return Reply{JSON: f.response}, nil
}

// userPrompt returns the user message content of the last chat call.
func (f *fakeLLMClient) userPrompt(t *testing.T) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.messages) < 2 {
		t.Fatalf("expected system and user messages, got %d", len(f.messages))
	}
	return f.messages[1].Content
}

// fakeStore is an in-memory BriefingStore.
type fakeStore struct {
	mu             sync.Mutex
	briefing       *MarketBriefing
	briefedIDs     []string
	reflection     *Reflection
	saveErr        error
	loadErr        error
	savedBriefings int
}

// SaveBriefing records the briefing and briefed news IDs.
func (s *fakeStore) SaveBriefing(_ context.Context, b MarketBriefing, ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	s.savedBriefings++
	s.briefing, s.briefedIDs = &b, ids
	return nil
}

// LoadLatestBriefing returns the stored briefing.
func (s *fakeStore) LoadLatestBriefing(context.Context) (*MarketBriefing, error) {
	return s.briefing, s.loadErr
}

// SaveReflection records the reflection.
func (s *fakeStore) SaveReflection(_ context.Context, r Reflection) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.reflection = &r
	return nil
}

// LoadLatestReflection returns the stored reflection.
func (s *fakeStore) LoadLatestReflection(context.Context) (*Reflection, error) {
	return s.reflection, s.loadErr
}

// twoMarkets returns briefing input for BTC/EUR and ETH/EUR.
func twoMarkets() []MarketData {
	return []MarketData{
		{Market: "BTC/EUR", Price: 100.0, Indicators: analysis.Indicators{}},
		{Market: "ETH/EUR", Price: 200.0, Indicators: analysis.Indicators{}},
	}
}

// signalRequest returns a minimal BTC/EUR long signal.
func signalRequest() SignalRequest {
	return SignalRequest{
		Market: "BTC/EUR", Direction: "long", MomentumPct: 1.2, BaseConfidence: 0.65,
		CurrentPrice: 100.0, Indicators: analysis.Indicators{}, Equity: 500.0, Cash: 400.0,
	}
}

// TestRecommendTrade covers the LLM-only recommender.
func TestRecommendTrade(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN indicator context WHEN a recommendation runs THEN indicators are in the prompt", func(t *testing.T) {
		client := newFakeLLMClient(nil)
		analyser := NewAnalyser(client, "€")

		rec := analyser.RecommendTrade(ctx, RecommendationRequest{
			Market: "BTC/EUR", CurrentPrice: 102.0, PreviousPrice: 100.0,
			Indicators: analysis.Indicators{
				"rsi_14": 72.0, "rsi_signal": "overbought", "ema9": 101.0, "ema21": 99.0, "ema_cross": "bullish",
			},
			Equity: 500.0, Cash: 500.0,
		})

		prompt := client.userPrompt(t)
		if rec.Action != "long" || !rec.LLMUsed {
			t.Errorf("recommendation = %+v, want an LLM long", rec)
		}
		for _, want := range []string{"Technical indicators", "RSI(14): 72.0", "EMA9/21", "context only", "Momentum: +2.00%"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("prompt missing %q:\n%s", want, prompt)
			}
		}
	})

	t.Run("GIVEN alias and invalid actions WHEN parsed THEN they map to long/short/hold", func(t *testing.T) {
		cases := map[string]string{"BUY": "long", "bearish": "short", "no trade": "hold", "moon": "hold"}
		for raw, want := range cases {
			client := newFakeLLMClient(map[string]any{"action": raw, "confidence": 2.0, "sentiment": -3.0})
			rec := NewAnalyser(client, "€").RecommendTrade(ctx, RecommendationRequest{Market: "BTC/EUR", CurrentPrice: 1, PreviousPrice: 1})
			if rec.Action != want {
				t.Errorf("action %q -> %q, want %q", raw, rec.Action, want)
			}
			if rec.Confidence > 0.95 || rec.Sentiment < -1.0 {
				t.Errorf("confidence/sentiment not clamped: %+v", rec)
			}
		}
	})

	t.Run("GIVEN the client cannot attempt WHEN a recommendation runs THEN it holds without calling the LLM", func(t *testing.T) {
		client := newFakeLLMClient(nil)
		client.canAttempt = false

		rec := NewAnalyser(client, "€").RecommendTrade(ctx, RecommendationRequest{Market: "BTC/EUR"})

		if rec.Action != "hold" || rec.LLMUsed || client.calls != 0 {
			t.Errorf("rec = %+v calls = %d, want hold without a call", rec, client.calls)
		}
	})

	t.Run("GIVEN an unparseable confidence WHEN parsed THEN the recommendation holds", func(t *testing.T) {
		client := newFakeLLMClient(map[string]any{"action": "long", "confidence": "very"})

		rec := NewAnalyser(client, "€").RecommendTrade(ctx, RecommendationRequest{Market: "BTC/EUR"})

		if rec.Action != "hold" || rec.LLMUsed {
			t.Errorf("rec = %+v, want a parse-failure hold", rec)
		}
	})
}

// TestBriefMarket covers news-triggered market briefings.
func TestBriefMarket(t *testing.T) {
	ctx := context.Background()
	articles := []NewsArticle{{ID: "n1", Source: "test", Title: "Crypto", Summary: "Markets rise"}}

	t.Run("GIVEN variant market keys WHEN a briefing is parsed THEN exact watched symbols are restored", func(t *testing.T) {
		client := newFakeLLMClient(map[string]any{
			"market_outlooks": map[string]any{
				"btc eur": map[string]any{"bias": "bullish", "score": 0.4, "note": "breakout"},
				"ETH-EUR": map[string]any{"bias": "neutral", "score": 0.0, "note": "mixed"},
			},
			"overall_sentiment": 0.2,
			"key_insight":       "Broad risk appetite improving",
		})
		analyser := NewAnalyser(client, "€")

		briefing := analyser.BriefMarket(ctx, articles, twoMarkets())

		if briefing == nil {
			t.Fatal("expected a briefing")
		}
		if _, ok := briefing.MarketOutlooks["BTC/EUR"]; !ok {
			t.Errorf("outlooks = %v, want BTC/EUR key", briefing.MarketOutlooks)
		}
		if _, ok := briefing.MarketOutlooks["ETH/EUR"]; !ok || len(briefing.MarketOutlooks) != 2 {
			t.Errorf("outlooks = %v, want exactly BTC/EUR and ETH/EUR", briefing.MarketOutlooks)
		}
		if !strings.Contains(client.userPrompt(t), "Use exactly these market symbols") {
			t.Error("prompt must instruct the model to use exact market symbols")
		}
		if analyser.LatestBriefing() == nil {
			t.Error("the briefing must be cached as the latest briefing")
		}
	})

	t.Run("GIVEN a string outlook WHEN a briefing is parsed THEN the outlook is normalised", func(t *testing.T) {
		client := newFakeLLMClient(map[string]any{
			"market_outlooks":   map[string]any{"BTC/EUR": "bullish"},
			"overall_sentiment": 0.2,
			"key_insight":       "Risk appetite improving",
		})

		briefing := NewAnalyser(client, "€").BriefMarket(ctx, articles, twoMarkets()[:1])

		got := briefing.MarketOutlooks["BTC/EUR"]
		if got.Bias != "bullish" || got.Score != 0.0 || got.Note != "" {
			t.Errorf("outlook = %+v, want bullish/0/empty", got)
		}
	})

	t.Run("GIVEN a store WHEN a briefing completes THEN it is persisted with the briefed news IDs", func(t *testing.T) {
		client := newFakeLLMClient(map[string]any{"market_outlooks": map[string]any{}, "overall_sentiment": 5, "key_insight": "x"})
		store := &fakeStore{}
		analyser := NewAnalyser(client, "€")
		analyser.SetStore(store)

		briefing := analyser.BriefMarket(ctx, []NewsArticle{{ID: "a"}, {Title: "no id"}, {ID: "b"}}, twoMarkets())

		if briefing.OverallSentiment != 1.0 {
			t.Errorf("overall sentiment = %v, want clamped to 1", briefing.OverallSentiment)
		}
		if store.savedBriefings != 1 || strings.Join(store.briefedIDs, ",") != "a,b" {
			t.Errorf("saved = %d ids = %v, want one save with [a b]", store.savedBriefings, store.briefedIDs)
		}
	})

	t.Run("GIVEN the store fails WHEN a briefing completes THEN the briefing is still returned", func(t *testing.T) {
		analyser := NewAnalyser(newFakeLLMClient(map[string]any{"key_insight": "x"}), "€")
		analyser.SetStore(&fakeStore{saveErr: errors.New("disk full")})

		if analyser.BriefMarket(ctx, articles, twoMarkets()) == nil {
			t.Error("a persistence failure must not discard the briefing")
		}
	})

	t.Run("GIVEN no market data WHEN briefing THEN nothing is sent to the LLM", func(t *testing.T) {
		client := newFakeLLMClient(nil)

		if NewAnalyser(client, "€").BriefMarket(ctx, articles, nil) != nil || client.calls != 0 {
			t.Error("expected no briefing and no LLM call without market data")
		}
	})
}

// TestAnalyseSignal covers full-context signal analysis.
func TestAnalyseSignal(t *testing.T) {
	ctx := context.Background()
	reply := map[string]any{"sentiment": 0.2, "confidence_scale": 1.1, "reasoning": "Context accepted"}

	t.Run("GIVEN briefing and reflection context WHEN a signal is analysed THEN both are in the prompt", func(t *testing.T) {
		client := newFakeLLMClient(reply)
		analyser := NewAnalyser(client, "€")
		future := time.Now().Add(time.Hour) // clock skew must not produce negative ages
		analyser.latestBriefing = &MarketBriefing{
			MarketOutlooks:   map[string]MarketOutlook{"BTC/EUR": {Bias: "bullish", Score: 0.3, Note: "momentum"}},
			OverallSentiment: 0.25, KeyInsight: "Market context available", ArticleCount: 2, GeneratedAt: future,
		}
		analyser.latestReflection = &Reflection{
			Pattern: "Recent longs worked", Suggestion: "Keep position sizing steady", InsightConfidence: 0.7, GeneratedAt: future,
		}

		result := analyser.AnalyseSignal(ctx, signalRequest())

		prompt := client.userPrompt(t)
		if !result.LLMUsed || result.ConfidenceScale != 1.1 {
			t.Errorf("result = %+v, want an LLM-used analysis", result)
		}
		for _, want := range []string{"Latest market briefing (0m ago", "Your most recent self-reflection (0m ago", "BTC/EUR outlook: bullish", "Exposure: 20.0%"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("prompt missing %q:\n%s", want, prompt)
			}
		}
	})

	t.Run("GIVEN a legacy string outlook WHEN normalised into context THEN the prompt shows its bias", func(t *testing.T) {
		client := newFakeLLMClient(reply)
		analyser := NewAnalyser(client, "€")
		analyser.latestBriefing = &MarketBriefing{
			MarketOutlooks: NormaliseMarketOutlooks(map[string]any{"BTC/EUR": "bullish"}, nil),
			KeyInsight:     "Market context available", ArticleCount: 2, GeneratedAt: time.Now(),
		}

		result := analyser.AnalyseSignal(ctx, signalRequest())

		if !result.LLMUsed || !strings.Contains(client.userPrompt(t), "BTC/EUR outlook: bullish") {
			t.Errorf("result = %+v, want the legacy outlook in the prompt", result)
		}
	})

	t.Run("GIVEN a half-open circuit WHEN a signal is analysed THEN the LLM call is attempted", func(t *testing.T) {
		client := newFakeLLMClient(reply)
		client.available = false

		result := NewAnalyser(client, "€").AnalyseSignal(ctx, signalRequest())

		if !result.LLMUsed || client.messages[0].Role != "system" {
			t.Errorf("result = %+v, want an attempted LLM call", result)
		}
	})

	t.Run("GIVEN the client cannot attempt WHEN a signal is analysed THEN a neutral analysis is returned", func(t *testing.T) {
		client := newFakeLLMClient(reply)
		client.canAttempt = false

		result := NewAnalyser(client, "€").AnalyseSignal(ctx, signalRequest())

		if result.LLMUsed || result.ConfidenceScale != 1.0 || client.calls != 0 {
			t.Errorf("result = %+v, want neutral without a call", result)
		}
	})

	t.Run("GIVEN an out-of-range scale WHEN parsed THEN it is clamped", func(t *testing.T) {
		client := newFakeLLMClient(map[string]any{"sentiment": 0.1, "confidence_scale": 9.0})

		result := NewAnalyser(client, "€").AnalyseSignal(ctx, signalRequest())

		if result.ConfidenceScale != 2.0 {
			t.Errorf("scale = %v, want 2.0", result.ConfidenceScale)
		}
	})

	t.Run("GIVEN an unparseable sentiment WHEN parsed THEN a neutral analysis is returned", func(t *testing.T) {
		client := newFakeLLMClient(map[string]any{"sentiment": "bullish"})

		result := NewAnalyser(client, "€").AnalyseSignal(ctx, signalRequest())

		if result.LLMUsed || result.ConfidenceScale != 1.0 {
			t.Errorf("result = %+v, want neutral", result)
		}
	})
}

// TestReflectOnOutcomes covers hourly trade-outcome reflection.
func TestReflectOnOutcomes(t *testing.T) {
	ctx := context.Background()
	outcomes := func(n int) []TradeOutcome {
		out := make([]TradeOutcome, n)
		for i := range out {
			out[i] = TradeOutcome{
				Market: "BTC/EUR", Direction: "long", EntryPrice: 100, ExitPrice: 101, PnL: float64(i - 2),
				PnLPct: 0.01, Confidence: 0.6, ExitReason: "take_profit",
				Indicators: analysis.Indicators{"rsi_14": 75.0, "macd": map[string]any{"bias": "bullish"}, "atr_pct": 0.5},
			}
		}
		return out
	}

	t.Run("GIVEN fewer than five outcomes WHEN reflecting THEN nothing is sent", func(t *testing.T) {
		client := newFakeLLMClient(nil)

		if NewAnalyser(client, "€").ReflectOnOutcomes(ctx, outcomes(4)) != nil || client.calls != 0 {
			t.Error("expected no reflection for fewer than five outcomes")
		}
	})

	t.Run("GIVEN enough outcomes WHEN reflecting THEN a reflection is cached, persisted and the prompt has indicators", func(t *testing.T) {
		client := newFakeLLMClient(map[string]any{"pattern": "High RSI longs lose", "suggestion": "Avoid RSI > 70", "insight_confidence": 1.4})
		store := &fakeStore{}
		analyser := NewAnalyser(client, "€")
		analyser.SetStore(store)

		reflection := analyser.ReflectOnOutcomes(ctx, outcomes(6))

		if reflection == nil || reflection.InsightConfidence != 1.0 {
			t.Fatalf("reflection = %+v, want clamped confidence", reflection)
		}
		if analyser.LatestReflection() == nil || store.reflection == nil {
			t.Error("the reflection must be cached and persisted")
		}
		prompt := client.userPrompt(t)
		for _, want := range []string{"Performance summary (6 trades)", "at entry: RSI 75.0 | MACD bullish | ATR 0.50%", "conf 60%"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("prompt missing %q:\n%s", want, prompt)
			}
		}
	})
}

// TestLoadFromDB covers restoring cached context at startup.
func TestLoadFromDB(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN a stored briefing and reflection WHEN loaded THEN both are restored and normalised", func(t *testing.T) {
		store := &fakeStore{
			briefing: &MarketBriefing{
				MarketOutlooks: map[string]MarketOutlook{"BTC/EUR": {Bias: "", Score: 4}},
				KeyInsight:     "restored",
			},
			reflection: &Reflection{Pattern: "p", Suggestion: "s", InsightConfidence: 0.5},
		}
		analyser := NewAnalyser(newFakeLLMClient(nil), "€")
		analyser.SetStore(store)

		analyser.LoadFromDB(ctx)

		b := analyser.LatestBriefing()
		if b == nil || b.KeyInsight != "restored" || b.GeneratedAt.IsZero() {
			t.Fatalf("briefing = %+v, want restored with a timestamp", b)
		}
		if o := b.MarketOutlooks["BTC/EUR"]; o.Bias != "neutral" || o.Score != 1.0 {
			t.Errorf("outlook = %+v, want neutral bias and clamped score", o)
		}
		if r := analyser.LatestReflection(); r == nil || r.Pattern != "p" {
			t.Errorf("reflection = %+v, want restored", r)
		}
	})

	t.Run("GIVEN the store fails WHEN loaded THEN the analyser keeps empty context", func(t *testing.T) {
		analyser := NewAnalyser(newFakeLLMClient(nil), "€")
		analyser.SetStore(&fakeStore{loadErr: errors.New("locked")})

		analyser.LoadFromDB(ctx)

		if analyser.LatestBriefing() != nil || analyser.LatestReflection() != nil {
			t.Error("expected no context after a failed load")
		}
	})

	t.Run("GIVEN no store WHEN loaded THEN nothing happens", func(t *testing.T) {
		analyser := NewAnalyser(newFakeLLMClient(nil), "€")

		analyser.LoadFromDB(ctx)

		if analyser.LatestBriefing() != nil {
			t.Error("expected no briefing without a store")
		}
	})
}

// TestPromptHelpers covers news selection and formatting helpers.
func TestPromptHelpers(t *testing.T) {
	t.Run("GIVEN mixed news WHEN selecting for BTC THEN alias matches come first and general news pads to three", func(t *testing.T) {
		news := []NewsArticle{
			{ID: "1", Title: "Ethereum upgrade"},
			{ID: "2", Title: "Bitcoin ETF inflows"},
			{ID: "3", Title: "Macro outlook"},
		}

		got := relevantNews("BTC/EUR", news, 5)

		if len(got) != 3 || got[0].ID != "2" {
			t.Errorf("relevant = %+v, want bitcoin first then two general items", got)
		}
	})

	t.Run("GIVEN a long summary WHEN formatting news THEN it is truncated with an ellipsis", func(t *testing.T) {
		block := formatNewsBlock([]NewsArticle{{Source: "rss", Title: "T", Summary: strings.Repeat("é", 130)}})

		if !strings.HasSuffix(block, "…") || strings.Count(block, "é") != 120 {
			t.Errorf("block = %q, want 120 runes plus ellipsis", block)
		}
	})

	t.Run("GIVEN no news WHEN formatting THEN a placeholder is shown", func(t *testing.T) {
		if formatNewsBlock(nil) != "  No recent news" {
			t.Error("expected the empty-news placeholder")
		}
	})

	t.Run("GIVEN long and short positions WHEN formatting THEN direction and currency are shown", func(t *testing.T) {
		block := formatPositions([]domain.PositionRecord{
			{Market: "BTC/EUR", Size: 0.5, AvgPrice: 85420.5, UnrealizedPnL: 12.3},
			{Market: "ETH/EUR", Size: -1, AvgPrice: 2000, UnrealizedPnL: -4},
		}, "€")

		for _, want := range []string{"BTC/EUR LONG × 0.500000 @ €85,420.50 unrealised €+12.30", "ETH/EUR SHORT", "€-4.00"} {
			if !strings.Contains(block, want) {
				t.Errorf("block missing %q:\n%s", want, block)
			}
		}
	})

	t.Run("GIVEN grouped indicators WHEN formatting THEN each group renders a line", func(t *testing.T) {
		block := formatIndicators(analysis.Indicators{
			"price_changes": map[string]any{"5m": 0.5, "1m": -0.25},
			"bb":            map[string]any{"position": 80.0, "upper": 105.0, "lower": 95.0, "width_pct": 10.0},
			"macd":          map[string]any{"line": 0.12, "bias": "bullish", "histogram": 0.01},
			"stoch":         map[string]any{"k": 80.0, "d": 70.0, "bias": "overbought"},
			"williams_r":    -20.0,
			"atr":           1.5,
			"atr_pct":       0.25,
		})

		for _, want := range []string{"Change — 1m: -0.25% | 5m: +0.50%", "BB: pos 80.0% of band", "MACD: +0.1200 (bullish) | histogram +0.0100", "Stoch: %K 80.0 / %D 70.0", "Williams %R: -20.0", "ATR: 1.5 (0.250% of price)"} {
			if !strings.Contains(block, want) {
				t.Errorf("block missing %q:\n%s", want, block)
			}
		}
	})

	t.Run("GIVEN no indicators WHEN formatting THEN a placeholder is shown", func(t *testing.T) {
		if formatIndicators(nil) != "  Insufficient price history for indicators" {
			t.Error("expected the insufficient-history placeholder")
		}
	})
}

// TestAnalyserConcurrency checks the analyser is safe for concurrent use.
func TestAnalyserConcurrency(t *testing.T) {
	t.Run("GIVEN concurrent briefings and analyses WHEN run together THEN no data race or panic occurs", func(t *testing.T) {
		ctx := context.Background()
		client := newFakeLLMClient(map[string]any{
			"market_outlooks": map[string]any{"BTC/EUR": "bullish"}, "sentiment": 0.1, "confidence_scale": 1.0,
		})
		analyser := NewAnalyser(client, "€")
		analyser.SetStore(&fakeStore{})

		var wg sync.WaitGroup
		for range 20 {
			wg.Add(2)
			go func() { defer wg.Done(); analyser.BriefMarket(ctx, nil, twoMarkets()) }()
			go func() { defer wg.Done(); analyser.AnalyseSignal(ctx, signalRequest()) }()
		}
		wg.Wait()

		if analyser.LatestBriefing() == nil {
			t.Error("expected a cached briefing after concurrent runs")
		}
	})
}
