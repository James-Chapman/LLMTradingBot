package strategy

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

// slope returns 4-hour candles rising (or falling) by rate per candle.
func slope(rate float64) []domain.Candle {
	return trendCandles(RotationMinClosedCandles+10, func(i int) float64 { return 100 * (1 + rate*float64(i)) })
}

// holding returns in with a long held in each market.
func holding(in Input, markets ...string) Input {
	for _, m := range markets {
		in.Portfolio.OpenPositions = append(in.Portfolio.OpenPositions, domain.PositionRecord{Market: m, Size: 1})
	}
	return in
}

// rotationInput returns one market per rate, named M0/EUR, M1/EUR, ….
func rotationInput(rates ...float64) Input {
	md := map[string]MarketData{}
	for i, r := range rates {
		c := slope(r)
		md[fmt.Sprintf("M%d/EUR", i)] = MarketData{Price: c[len(c)-1].C, TrendCandles: c}
	}
	return Input{MarketData: md}
}

// byMarket indexes ideas by market.
func byMarket(ideas []domain.TradeIdea) map[string]domain.TradeIdea {
	out := map[string]domain.TradeIdea{}
	for _, i := range ideas {
		out[i.Market] = i
	}
	return out
}

// TestRotationStrategy covers holding the strongest markets by 4-hour return.
func TestRotationStrategy(t *testing.T) {
	ctx := context.Background()
	s := NewRotation(testSettings())

	t.Run("GIVEN six rising markets with the weakest two held WHEN ranked THEN the strongest are bought, the buffer rank gets nothing and the held laggards are sold", func(t *testing.T) {
		got := byMarket(s.Evaluate(ctx, holding(rotationInput(0.006, 0.005, 0.004, 0.003, 0.002, 0.001), "M3/EUR", "M5/EUR")))
		for i := range RotationTop {
			if d := got[fmt.Sprintf("M%d/EUR", i)].Direction; d != domain.Long {
				t.Fatalf("rank %d: %q, want long", i+1, d)
			}
		}
		if _, ok := got[fmt.Sprintf("M%d/EUR", RotationTop)]; ok {
			t.Fatalf("rank %d is in the hold buffer and should get no idea", RotationTop+1)
		}
		if d := got["M5/EUR"].Direction; d != domain.Short || !got["M5/EUR"].Exit {
			t.Fatalf("the weakest held market: %q, want a short rule exit", d)
		}
		if _, ok := got["M4/EUR"]; ok {
			t.Fatal("a laggard not held must get no idea")
		}
		for _, idea := range got {
			if idea.Confidence < testSettings().MinSignalConfidence {
				t.Fatalf("confidence %.2f below the gate", idea.Confidence)
			}
		}
	})

	t.Run("GIVEN a top-ranked market whose return is negative WHEN ranked THEN it is not bought", func(t *testing.T) {
		got := byMarket(s.Evaluate(ctx, holding(rotationInput(-0.001, -0.002, -0.003, -0.004), "M0/EUR")))
		for m, idea := range got {
			if idea.Direction == domain.Long {
				t.Fatalf("%s bought on a negative return", m)
			}
		}
		if got["M0/EUR"].Direction != domain.Short {
			t.Fatalf("a falling market should be sold: %+v", got["M0/EUR"])
		}
	})

	t.Run("GIVEN a market without enough candles WHEN ranked THEN it gets no idea and does not take a rank", func(t *testing.T) {
		in := rotationInput(0.003, 0.002, 0.001)
		short := trendCandles(RotationMinClosedCandles-1, func(i int) float64 { return 100 * (1 + 0.05*float64(i)) })
		in.MarketData["NEW/EUR"] = MarketData{Price: short[len(short)-1].C, TrendCandles: short}
		got := byMarket(s.Evaluate(ctx, in))
		if _, ok := got["NEW/EUR"]; ok {
			t.Fatal("a market in warm-up got an idea")
		}
		if got["M2/EUR"].Direction != domain.Long {
			t.Fatalf("the third market should still rank in the top %d: %+v", RotationTop, got["M2/EUR"])
		}
	})

	t.Run("GIVEN an entry WHEN its prompt is built THEN it lists the market's rank and return", func(t *testing.T) {
		in := rotationInput(0.006, 0.005, 0.004, 0.003)
		idea := byMarket(s.Evaluate(ctx, in))["M0/EUR"]
		p := s.Prompt(idea, in.MarketData["M0/EUR"])
		if !strings.Contains(p.Name, "rotation") || p.Data["rank"] != "1" || p.Data["return_14d"] == "" || p.Data["ranked_markets"] != "4" {
			t.Fatalf("prompt %+v", p)
		}
	})

	t.Run("GIVEN the rotation LLM version WHEN created THEN it wraps the rotation strategy", func(t *testing.T) {
		v := NewRotationAndLLM(testSettings())
		if v.ID() != RotationAndLLMID || v.Base().ID() != RotationID {
			t.Fatalf("id %q base %q", v.ID(), v.Base().ID())
		}
	})
}
