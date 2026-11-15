package strategy

import (
	"context"
	"strings"
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

// trendCandles returns n 4-hour candles whose closes follow step(i), oldest
// first; the last one stands for Kraken's forming candle.
func trendCandles(n int, step func(i int) float64) []domain.Candle {
	out := make([]domain.Candle, n)
	for i := range out {
		c := step(i)
		out[i] = domain.Candle{T: int64(i) * 4 * 3600, O: c, H: c * 1.001, L: c * 0.999, C: c, V: 1}
	}
	return out
}

// rising is a steady 0.5 % rise per 4-hour candle.
func rising(i int) float64 { return 1000 * (1 + 0.005*float64(i)) }

// falling is a steady 0.5 % fall per 4-hour candle.
func falling(i int) float64 { return 2000 * (1 - 0.003*float64(i)) }

// evalTrend runs a fresh TrendStrategy over one market's 4-hour candles.
func evalTrend(candles []domain.Candle) []domain.TradeIdea {
	price := 0.0
	if len(candles) > 0 {
		price = candles[len(candles)-1].C
	}
	data := MarketData{Price: price, PreviousPrice: price, TrendCandles: candles}
	return NewTrend(testSettings()).Evaluate(context.Background(), single("ETH/EUR", data))
}

// TestTrendStrategy covers the 4-hour trend strategy's entry, exit and warm-up behaviour.
func TestTrendStrategy(t *testing.T) {
	t.Run("GIVEN the trend strategy WHEN created THEN it has its own id and needs no LLM context", func(t *testing.T) {
		s := NewTrend(testSettings())
		if s.ID() != TrendID || TrendID != "trend_strategy" {
			t.Fatalf("id = %q", s.ID())
		}
		if s.UsesLLM() || s.RequiredPriceTicks() != 0 {
			t.Fatal("the trend strategy must not need LLM context")
		}
	})

	t.Run("GIVEN fewer closed 4-hour candles than the warm-up WHEN evaluated THEN no idea is emitted", func(t *testing.T) {
		if ideas := evalTrend(trendCandles(TrendMinClosedCandles, rising)); len(ideas) != 0 {
			t.Fatalf("expected no ideas during warm-up, got %d", len(ideas))
		}
	})

	t.Run("GIVEN no 4-hour candles WHEN evaluated THEN no idea is emitted", func(t *testing.T) {
		if ideas := evalTrend(nil); len(ideas) != 0 {
			t.Fatalf("expected no ideas, got %d", len(ideas))
		}
	})

	t.Run("GIVEN a steady 4-hour uptrend WHEN evaluated THEN one long idea above the automated confidence gate is emitted", func(t *testing.T) {
		ideas := evalTrend(trendCandles(TrendMinClosedCandles+40, rising))
		if len(ideas) != 1 {
			t.Fatalf("expected one idea, got %d", len(ideas))
		}
		idea := ideas[0]
		if idea.Direction != domain.Long || idea.StrategyID != TrendID || idea.Market != "ETH/EUR" {
			t.Fatalf("unexpected idea %+v", idea)
		}
		if idea.Confidence < testSettings().MinSignalConfidence {
			t.Fatalf("confidence %.2f is below the default automated gate", idea.Confidence)
		}
		if idea.PositionSizingProposal <= 0 || idea.Thesis == "" {
			t.Fatalf("idea lacks sizing or thesis: %+v", idea)
		}
	})

	t.Run("GIVEN a held long in a steady 4-hour downtrend WHEN evaluated THEN one exit (short) idea is emitted", func(t *testing.T) {
		candles := trendCandles(TrendMinClosedCandles+40, falling)
		in := single("ETH/EUR", MarketData{Price: candles[len(candles)-1].C, TrendCandles: candles})
		in.Portfolio.OpenPositions = []domain.PositionRecord{{Market: "ETH/EUR", Size: 1}}
		ideas := NewTrend(testSettings()).Evaluate(context.Background(), in)
		if len(ideas) != 1 || ideas[0].Direction != domain.Short || !ideas[0].Exit {
			t.Fatalf("expected one short rule exit, got %+v", ideas)
		}
		if ideas[0].Confidence < testSettings().MinSignalConfidence {
			t.Fatalf("exit confidence %.2f is below the default automated gate", ideas[0].Confidence)
		}
	})

	t.Run("GIVEN an uptrend whose forming candle crashes WHEN evaluated THEN the decision ignores the forming candle", func(t *testing.T) {
		candles := trendCandles(TrendMinClosedCandles+40, rising)
		last := &candles[len(candles)-1]
		last.O, last.H, last.L, last.C = last.C, last.C, last.C*0.2, last.C*0.2
		ideas := evalTrend(candles)
		if len(ideas) != 1 || ideas[0].Direction != domain.Long {
			t.Fatalf("expected the closed-candle uptrend to stay long, got %+v", ideas)
		}
	})

	t.Run("GIVEN nothing held in a 4-hour downtrend WHEN evaluated THEN no idea is emitted", func(t *testing.T) {
		if ideas := evalTrend(trendCandles(TrendMinClosedCandles+40, falling)); len(ideas) != 0 {
			t.Fatalf("expected no ideas, got %+v", ideas)
		}
	})

	t.Run("GIVEN a held long in an uptrend WHEN evaluated THEN no idea is emitted (already held)", func(t *testing.T) {
		candles := trendCandles(TrendMinClosedCandles+40, rising)
		in := single("ETH/EUR", MarketData{Price: candles[len(candles)-1].C, TrendCandles: candles})
		in.Portfolio.OpenPositions = []domain.PositionRecord{{Market: "ETH/EUR", Size: 1}}
		if ideas := NewTrend(testSettings()).Evaluate(context.Background(), in); len(ideas) != 0 {
			t.Fatalf("expected no ideas, got %+v", ideas)
		}
	})

	t.Run("GIVEN two markets with opposite trends and the falling one held WHEN evaluated together THEN each gets its own direction", func(t *testing.T) {
		in := Input{MarketData: map[string]MarketData{
			"BTC/EUR": {Price: 1, PreviousPrice: 1, TrendCandles: trendCandles(TrendMinClosedCandles+40, rising)},
			"ETH/EUR": {Price: 1, PreviousPrice: 1, TrendCandles: trendCandles(TrendMinClosedCandles+40, falling)},
		}, Portfolio: Portfolio{OpenPositions: []domain.PositionRecord{{Market: "ETH/EUR", Size: 1}}}}
		ideas := NewTrend(testSettings()).Evaluate(context.Background(), in)
		if len(ideas) != 2 || ideas[0].Market != "BTC/EUR" || ideas[0].Direction != domain.Long ||
			ideas[1].Market != "ETH/EUR" || ideas[1].Direction != domain.Short {
			t.Fatalf("unexpected ideas %+v", ideas)
		}
	})
}

// TestTrendPrompt covers the prompt the trend strategy's LLM version sends.
func TestTrendPrompt(t *testing.T) {
	t.Run("GIVEN a trend entry WHEN its prompt is built THEN it names the strategy, explains how to judge it and lists the EMAs", func(t *testing.T) {
		candles := trendCandles(TrendMinClosedCandles+40, rising)
		data := MarketData{Price: candles[len(candles)-1].C, TrendCandles: candles}
		ideas := NewTrend(testSettings()).Evaluate(context.Background(), single("ETH/EUR", data))
		p := NewTrend(testSettings()).Prompt(ideas[0], data)
		if !strings.Contains(p.Name, "trend") || !strings.Contains(p.Instructions, "EMA") {
			t.Fatalf("prompt %+v", p)
		}
		for _, k := range []string{"ema_fast_4h", "ema_slow_4h", "ema_gap"} {
			if p.Data[k] == "" {
				t.Fatalf("prompt data lacks %s: %v", k, p.Data)
			}
		}
	})

	t.Run("GIVEN the trend LLM version WHEN created THEN it wraps the trend strategy under its own id", func(t *testing.T) {
		v := NewTrendAndLLM(testSettings())
		if v.ID() != TrendAndLLMID || TrendAndLLMID != "trend_and_llm_strategy" || v.Base().ID() != TrendID || !v.UsesLLM() {
			t.Fatalf("id %q base %q", v.ID(), v.Base().ID())
		}
	})
}
