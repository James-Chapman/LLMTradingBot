package storage

import (
	"context"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/shadow"
)

// TestShadowReviews covers storing shadow LLM verdicts: one per strategy,
// market and closed candle, where an answer is never replaced.
func TestShadowReviews(t *testing.T) {
	ctx := context.Background()
	candle := time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)
	answered := shadow.Review{
		StrategyID: "donchian_strategy", Market: "BTC/EUR", Candle: candle, ReviewedAt: candle.Add(4*time.Hour + time.Minute),
		TradeIdeaID: "idea-1", BaseConfidence: 0.95, LLMUsed: true, Scale: 0.6, Sentiment: -0.4,
		Reasoning: "hype", WouldVeto: true, VetoThreshold: 0.7,
	}

	t.Run("GIVEN a saved verdict WHEN listed THEN every field is read back", func(t *testing.T) {
		repo := newTestRepo(t)
		if err := repo.SaveShadowReview(ctx, answered); err != nil {
			t.Fatal(err)
		}
		got, err := repo.GetShadowReviews(ctx, 0)
		if err != nil || len(got) != 1 {
			t.Fatalf("got %+v, %v", got, err)
		}
		if got[0] != answered {
			t.Fatalf("read back %+v\nwant %+v", got[0], answered)
		}
	})

	t.Run("GIVEN an unanswered verdict WHEN the LLM later answers for the same candle THEN the answer replaces it", func(t *testing.T) {
		repo := newTestRepo(t)
		down := answered
		down.LLMUsed, down.WouldVeto, down.Scale = false, false, 1
		_ = repo.SaveShadowReview(ctx, down)
		_ = repo.SaveShadowReview(ctx, answered)
		got, _ := repo.GetShadowReviews(ctx, 0)
		if len(got) != 1 || !got[0].LLMUsed || !got[0].WouldVeto {
			t.Fatalf("got %+v, want the answer only", got)
		}
	})

	t.Run("GIVEN an answered verdict WHEN another verdict for the same candle is saved THEN the first answer is kept", func(t *testing.T) {
		repo := newTestRepo(t)
		_ = repo.SaveShadowReview(ctx, answered)
		second := answered
		second.Scale, second.WouldVeto = 1.3, false
		down := answered
		down.LLMUsed = false
		_ = repo.SaveShadowReview(ctx, second)
		_ = repo.SaveShadowReview(ctx, down)
		got, _ := repo.GetShadowReviews(ctx, 0)
		if len(got) != 1 || got[0].Scale != 0.6 || !got[0].LLMUsed {
			t.Fatalf("got %+v, want the first answer", got)
		}
	})

	t.Run("GIVEN verdicts for several candles WHEN listed with a limit THEN the newest candles come first", func(t *testing.T) {
		repo := newTestRepo(t)
		for i := range 3 {
			r := answered
			r.Candle = candle.Add(time.Duration(i) * 4 * time.Hour)
			_ = repo.SaveShadowReview(ctx, r)
		}
		got, _ := repo.GetShadowReviews(ctx, 2)
		if len(got) != 2 || !got[0].Candle.Equal(candle.Add(8*time.Hour)) || !got[1].Candle.Equal(candle.Add(4*time.Hour)) {
			t.Fatalf("got %+v", got)
		}
	})
}
