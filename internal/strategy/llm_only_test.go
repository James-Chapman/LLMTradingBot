package strategy

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/llm"
)

// recommendation builds an LLM trade recommendation for tests.
func recommendation(action string, confidence float64, used bool) llm.LLMTradeRecommendation {
	return llm.LLMTradeRecommendation{Action: action, Confidence: confidence, Sentiment: 0.65, Reasoning: "fake reasoning", LLMUsed: used}
}

// llmCandles is how many 1-hour candles the LLM-only fixtures carry: the
// newest is still forming, so llmCandles-1 are finished.
const llmCandles = BasicMinClosedCandles + 10

// llmMarket returns market data at the given live price whose 1-hour candles
// are warmed up (a gentle rise, the last one forming).
func llmMarket(price float64) MarketData {
	return MarketData{Price: price, PreviousPrice: price, HourCandles: hourCandles(llmCandles, func(i int) float64 { return 100 + float64(i)*0.1 })}
}

// manyMarkets returns n markets with warmed-up 1-hour candles.
func manyMarkets(n int) map[string]MarketData {
	markets := map[string]MarketData{}
	for i := 0; i < n; i++ {
		markets[fmt.Sprintf("COIN%d/EUR", i)] = llmMarket(100.0)
	}
	return markets
}

// TestLLMOnlyStrategy covers the pure LLM recommendation strategy.
func TestLLMOnlyStrategy(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN the strategy is created WHEN its dispatch flags are read THEN it uses the LLM and waits for its own candles, not price ticks", func(t *testing.T) {
		s := NewLLMOnly(testSettings())
		if s.ID() != "llm_only_strategy" || !s.UsesLLM() {
			t.Fatalf("unexpected id/flags")
		}
		if s.RequiredPriceTicks() != 0 {
			t.Fatalf("required ticks = %d", s.RequiredPriceTicks())
		}
	})

	t.Run("GIVEN an LLM long recommendation WHEN evaluated THEN a long idea is emitted with the LLM context", func(t *testing.T) {
		a := &fakeAnalyser{rec: recommendation("long", 0.81, true)}
		in := single("BTC/EUR", llmMarket(100.0))
		in.Analyser = a
		in.Portfolio = Portfolio{Equity: 500, Cash: 500}

		ideas := NewLLMOnly(testSettings()).Evaluate(ctx, in)
		if len(ideas) != 1 {
			t.Fatalf("expected one idea, got %d", len(ideas))
		}
		idea := ideas[0]
		if idea.StrategyID != LLMOnlyID || idea.Direction != domain.Long || idea.Confidence != 0.81 {
			t.Fatalf("unexpected idea: %+v", idea)
		}
		if idea.SupportingSignals["llm_only"] != true || idea.SupportingSignals["llm_reasoning"] != "fake reasoning" {
			t.Fatalf("missing LLM context: %+v", idea.SupportingSignals)
		}
		if _, ok := idea.SupportingSignals["llm_sentiment"]; !ok {
			t.Fatalf("missing llm_sentiment")
		}
		if len(a.recCalls[0].Indicators) == 0 {
			t.Fatalf("analyser should receive indicators computed on the candles")
		}
		if a.recCalls[0].Equity != 500 || a.recCalls[0].Cash != 500 {
			t.Fatalf("analyser should receive portfolio context")
		}
	})

	t.Run("GIVEN an LLM short recommendation WHEN evaluated THEN a short idea is emitted", func(t *testing.T) {
		in := single("ETH/EUR", llmMarket(100.0))
		in.Analyser = &fakeAnalyser{rec: recommendation("short", 0.7, true)}
		ideas := NewLLMOnly(testSettings()).Evaluate(ctx, in)
		if len(ideas) != 1 || ideas[0].Direction != domain.Short {
			t.Fatalf("expected one short idea")
		}
	})

	t.Run("GIVEN an LLM hold recommendation WHEN evaluated THEN no idea is emitted", func(t *testing.T) {
		in := single("ETH/EUR", llmMarket(100.0))
		in.Analyser = &fakeAnalyser{rec: recommendation("hold", 0.9, true)}
		if ideas := NewLLMOnly(testSettings()).Evaluate(ctx, in); len(ideas) != 0 {
			t.Fatalf("expected no ideas")
		}
	})

	t.Run("GIVEN an invalid LLM action WHEN evaluated THEN no idea is emitted", func(t *testing.T) {
		in := single("ETH/EUR", llmMarket(100.0))
		in.Analyser = &fakeAnalyser{rec: recommendation("buy", 0.9, true)}
		if ideas := NewLLMOnly(testSettings()).Evaluate(ctx, in); len(ideas) != 0 {
			t.Fatalf("expected no ideas")
		}
	})

	t.Run("GIVEN the LLM is unavailable WHEN evaluated THEN no idea is emitted", func(t *testing.T) {
		in := single("SOL/EUR", llmMarket(100.0))
		in.Analyser = &fakeAnalyser{rec: recommendation("long", 0.9, false)}
		if ideas := NewLLMOnly(testSettings()).Evaluate(ctx, in); len(ideas) != 0 {
			t.Fatalf("expected no ideas")
		}
	})

	t.Run("GIVEN no analyser WHEN evaluated THEN no idea is emitted", func(t *testing.T) {
		in := single("SOL/EUR", llmMarket(100.0))
		if ideas := NewLLMOnly(testSettings()).Evaluate(ctx, in); len(ideas) != 0 {
			t.Fatalf("expected no ideas")
		}
	})

	t.Run("GIVEN multiple markets WHEN evaluated THEN they are processed concurrently and returned sorted", func(t *testing.T) {
		a := &fakeAnalyser{rec: recommendation("long", 0.7, true), delay: 50 * time.Millisecond}
		in := Input{MarketData: manyMarkets(5), Analyser: a}

		start := time.Now()
		ideas := NewLLMOnly(testSettings()).Evaluate(ctx, in)
		elapsed := time.Since(start)

		if len(ideas) != 5 {
			t.Fatalf("expected 5 ideas, got %d", len(ideas))
		}
		// Sequential would take 5 × 50 ms = 250 ms.
		if elapsed >= 200*time.Millisecond {
			t.Fatalf("evaluation took %v — likely sequential", elapsed)
		}
		for i := 1; i < len(ideas); i++ {
			if ideas[i-1].Market > ideas[i].Market {
				t.Fatalf("ideas not sorted by market")
			}
		}
	})

	t.Run("GIVEN many markets and a concurrency limit WHEN evaluated THEN LLM calls in flight never exceed it", func(t *testing.T) {
		s := testSettings()
		s.LLMOnlyMaxConcurrency = 3
		a := &fakeAnalyser{rec: recommendation("hold", 0.7, true), delay: 20 * time.Millisecond}
		NewLLMOnly(s).Evaluate(ctx, Input{MarketData: manyMarkets(8), Analyser: a})
		if a.maxInFlight > 3 || a.maxInFlight < 1 {
			t.Fatalf("max in flight = %d", a.maxInFlight)
		}
		if len(a.recCalls) != 8 {
			t.Fatalf("expected 8 calls, got %d", len(a.recCalls))
		}
	})

	t.Run("GIVEN a non-positive concurrency setting WHEN the strategy is created THEN at least one call may run", func(t *testing.T) {
		s := testSettings()
		s.LLMOnlyMaxConcurrency = 0
		if NewLLMOnly(s).MaxConcurrency < 1 {
			t.Fatalf("concurrency must be at least 1")
		}
	})
}

// TestLLMOnlyCandleCadence covers the finished 1-hour candles the strategy
// reads and the once-per-candle LLM cadence.
func TestLLMOnlyCandleCadence(t *testing.T) {
	ctx := context.Background()
	long := recommendation("long", 0.8, true)

	// withNewCandle returns the market with one more candle, so the previously
	// forming candle has finished and a new one is forming.
	withNewCandle := func(d MarketData) MarketData {
		c := d.HourCandles[len(d.HourCandles)-1]
		d.HourCandles = append(slices.Clone(d.HourCandles), domain.Candle{T: c.T + 3600, O: c.C, H: c.C, L: c.C, C: c.C + 1, V: 1})
		return d
	}

	t.Run("GIVEN warmed-up 1h candles WHEN evaluated THEN the LLM sees the finished closes, their indicators and the live price", func(t *testing.T) {
		a := &fakeAnalyser{rec: long}
		data := llmMarket(150.0)
		in := single("BTC/EUR", data)
		in.Analyser = a
		NewLLMOnly(testSettings()).Evaluate(ctx, in)
		if len(a.recCalls) != 1 {
			t.Fatalf("calls = %d, want 1", len(a.recCalls))
		}
		req := a.recCalls[0]
		closed := closedOf(data.HourCandles)
		if !slices.Equal(req.PriceHistory, closesOf(closed)) {
			t.Fatalf("price history is not the finished closes")
		}
		if req.PreviousPrice != closed[len(closed)-2].C {
			t.Fatalf("previous = %v, want the previous finished close", req.PreviousPrice)
		}
		if req.CurrentPrice != 150.0 {
			t.Fatalf("current = %v, want the live price", req.CurrentPrice)
		}
		if req.CandleSeconds != 3600 {
			t.Fatalf("candle seconds = %d, want 3600", req.CandleSeconds)
		}
		if _, ok := req.Indicators.Num("atr_pct"); !ok {
			t.Fatalf("indicators were not computed on the candles: %v", req.Indicators)
		}
	})

	t.Run("GIVEN more candles than the view keeps WHEN evaluated THEN the LLM gets only the newest bounded set", func(t *testing.T) {
		a := &fakeAnalyser{rec: long}
		in := single("BTC/EUR", MarketData{Price: 100, HourCandles: hourCandles(basicViewCandles+80, flat)})
		in.Analyser = a
		NewLLMOnly(testSettings()).Evaluate(ctx, in)
		if got := len(a.recCalls[0].PriceHistory); got != basicViewCandles {
			t.Fatalf("history = %d closes, want %d", got, basicViewCandles)
		}
	})

	t.Run("GIVEN too few finished candles WHEN evaluated THEN the market is skipped without asking the LLM", func(t *testing.T) {
		a := &fakeAnalyser{rec: long}
		in := single("BTC/EUR", MarketData{Price: 100, HourCandles: hourCandles(BasicMinClosedCandles, flat)}) // the last is forming
		in.Analyser = a
		if ideas := NewLLMOnly(testSettings()).Evaluate(ctx, in); len(ideas) != 0 || len(a.recCalls) != 0 {
			t.Fatalf("ideas %d, calls %d; want neither", len(ideas), len(a.recCalls))
		}
	})

	t.Run("GIVEN a candle already evaluated WHEN evaluated again on the same candle THEN the LLM is not asked again", func(t *testing.T) {
		a := &fakeAnalyser{rec: recommendation("hold", 0.5, true)}
		s := NewLLMOnly(testSettings())
		in := single("BTC/EUR", llmMarket(100.0))
		in.Analyser = a
		s.Evaluate(ctx, in)
		s.Evaluate(ctx, in)
		if len(a.recCalls) != 1 {
			t.Fatalf("calls = %d, want 1 per candle", len(a.recCalls))
		}
	})

	t.Run("GIVEN a candle already evaluated WHEN the next candle closes THEN the LLM is asked once more", func(t *testing.T) {
		a := &fakeAnalyser{rec: long}
		s := NewLLMOnly(testSettings())
		data := llmMarket(100.0)
		in := single("BTC/EUR", data)
		in.Analyser = a
		s.Evaluate(ctx, in)
		in = single("BTC/EUR", withNewCandle(data))
		in.Analyser = a
		s.Evaluate(ctx, in)
		s.Evaluate(ctx, in)
		if len(a.recCalls) != 2 {
			t.Fatalf("calls = %d, want 2", len(a.recCalls))
		}
	})

	t.Run("GIVEN the LLM was unavailable WHEN the same candle is evaluated again THEN the LLM is retried", func(t *testing.T) {
		a := &fakeAnalyser{rec: recommendation("long", 0.9, false)}
		s := NewLLMOnly(testSettings())
		in := single("BTC/EUR", llmMarket(100.0))
		in.Analyser = a
		s.Evaluate(ctx, in)
		a.mu.Lock()
		a.rec = long
		a.mu.Unlock()
		ideas := s.Evaluate(ctx, in)
		if len(a.recCalls) != 2 || len(ideas) != 1 {
			t.Fatalf("calls %d, ideas %d; want a retry that yields an idea", len(a.recCalls), len(ideas))
		}
	})

	t.Run("GIVEN several markets WHEN only one gets a new candle THEN only that market is asked again", func(t *testing.T) {
		a := &fakeAnalyser{rec: recommendation("hold", 0.5, true)}
		s := NewLLMOnly(testSettings())
		in := Input{MarketData: manyMarkets(3), Analyser: a}
		s.Evaluate(ctx, in)
		in.MarketData["COIN1/EUR"] = withNewCandle(in.MarketData["COIN1/EUR"])
		s.Evaluate(ctx, in)
		if len(a.recCalls) != 4 || a.recCalls[3].Market != "COIN1/EUR" {
			t.Fatalf("calls = %d, want 3 then one for COIN1/EUR", len(a.recCalls))
		}
	})

	t.Run("GIVEN a long idea WHEN built THEN it is labelled as 1h-candle based", func(t *testing.T) {
		in := single("BTC/EUR", llmMarket(100.0))
		in.Analyser = &fakeAnalyser{rec: long}
		idea := NewLLMOnly(testSettings()).Evaluate(ctx, in)[0]
		if idea.SupportingSignals["candle_interval"] != "1h" {
			t.Fatalf("candle_interval = %v", idea.SupportingSignals["candle_interval"])
		}
		if !strings.Contains(idea.EntryPlan, "1h") || !strings.Contains(idea.Thesis, "1h") {
			t.Fatalf("plan %q / thesis %q should mention the 1h candles", idea.EntryPlan, idea.Thesis)
		}
	})
}
