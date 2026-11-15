package storage

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/James-Chapman/LLMTradingBot/internal/shadow"
)

// DefaultShadowReviewLimit is how many shadow reviews GetShadowReviews reads
// when no limit is given: about a year of reviews on 20 markets.
const DefaultShadowReviewLimit = 20000

// SaveShadowReview stores a shadow LLM verdict, one per strategy, market and
// closed candle. A verdict the LLM could not give is replaced by a later
// answer for the same candle; an answer is never replaced, so the stored
// verdict is the one the LLM version would have acted on first.
func (r *Repository) SaveShadowReview(ctx context.Context, v shadow.Review) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO llm_shadow_reviews (
			strategy_id, market, candle, reviewed_at, trade_idea_id, base_confidence,
			llm_used, confidence_scale, sentiment, reasoning, would_veto, veto_threshold
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (strategy_id, market, candle) DO UPDATE SET
			reviewed_at = excluded.reviewed_at, trade_idea_id = excluded.trade_idea_id,
			base_confidence = excluded.base_confidence, llm_used = excluded.llm_used,
			confidence_scale = excluded.confidence_scale, sentiment = excluded.sentiment,
			reasoning = excluded.reasoning, would_veto = excluded.would_veto,
			veto_threshold = excluded.veto_threshold
		WHERE llm_shadow_reviews.llm_used = 0 AND excluded.llm_used = 1`,
		v.StrategyID, v.Market, formatDBTime(v.Candle), dbTimeOrNil(v.ReviewedAt), nullIfEmpty(v.TradeIdeaID),
		v.BaseConfidence, v.LLMUsed, v.Scale, v.Sentiment, v.Reasoning, v.WouldVeto, v.VetoThreshold)
	if err != nil {
		return fmt.Errorf("save shadow review: %w", err)
	}
	return nil
}

// GetShadowReviews returns stored shadow verdicts, newest candle first.
// limit <= 0 uses DefaultShadowReviewLimit.
func (r *Repository) GetShadowReviews(ctx context.Context, limit int) ([]shadow.Review, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT strategy_id, market, candle, reviewed_at, trade_idea_id, base_confidence,
			llm_used, confidence_scale, sentiment, reasoning, would_veto, veto_threshold
		FROM llm_shadow_reviews ORDER BY candle DESC, reviewed_at DESC LIMIT ?`,
		orDefault(limit, DefaultShadowReviewLimit))
	if err != nil {
		return nil, fmt.Errorf("get shadow reviews: %w", err)
	}
	defer rows.Close()

	out := []shadow.Review{}
	for rows.Next() {
		var v shadow.Review
		var candle, reviewed nullTime
		var ideaID, reasoning sql.NullString
		var base, scale, sentiment, threshold sql.NullFloat64
		if err := rows.Scan(&v.StrategyID, &v.Market, &candle, &reviewed, &ideaID, &base,
			&v.LLMUsed, &scale, &sentiment, &reasoning, &v.WouldVeto, &threshold); err != nil {
			return nil, fmt.Errorf("scan shadow review: %w", err)
		}
		v.Candle, v.ReviewedAt = candle.Time, reviewed.Time
		v.TradeIdeaID, v.Reasoning = ideaID.String, reasoning.String
		v.BaseConfidence, v.Scale, v.Sentiment, v.VetoThreshold = base.Float64, scale.Float64, sentiment.Float64, threshold.Float64
		out = append(out, v)
	}
	return out, rows.Err()
}
