package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/observability"
)

// Repository is the activity log's persistence backend.
var _ observability.ActivityStore = (*Repository)(nil)

// SaveActivityLog persists one activity-feed entry (observability.ActivityStore).
func (r *Repository) SaveActivityLog(level, message, detail string) error {
	_, err := r.db.ExecContext(context.Background(),
		"INSERT INTO activity_log (level, message, detail, timestamp) VALUES (?, ?, ?, ?)",
		level, message, detail, formatDBTime(time.Now()))
	if err != nil {
		return fmt.Errorf("save activity log: %w", err)
	}
	return nil
}

// GetRecentActivity returns the newest activity entries, newest first, with
// RFC 3339 UTC timestamps to match the in-memory activity log.
// limit <= 0 uses DefaultActivityLimit.
func (r *Repository) GetRecentActivity(limit int) ([]observability.ActivityEntry, error) {
	rows, err := r.db.QueryContext(context.Background(),
		"SELECT timestamp, level, message, detail FROM activity_log ORDER BY timestamp DESC, id DESC LIMIT ?",
		orDefault(limit, DefaultActivityLimit))
	if err != nil {
		return nil, fmt.Errorf("get recent activity: %w", err)
	}
	defer rows.Close()

	result := []observability.ActivityEntry{}
	for rows.Next() {
		var ts nullTime
		var level, message, detail sql.NullString
		if err := rows.Scan(&ts, &level, &message, &detail); err != nil {
			return nil, err
		}
		entry := observability.ActivityEntry{Level: level.String, Message: message.String, Detail: detail.String}
		if ts.Valid {
			entry.Timestamp = ts.Time.Format(time.RFC3339Nano)
		}
		result = append(result, entry)
	}
	return result, rows.Err()
}

// TrimOldActivity keeps only the newest keep activity rows by deleting every
// row whose id is <= the id of the first row beyond keep (as Python did).
// keep <= 0 uses DefaultActivityKeep.
func (r *Repository) TrimOldActivity(ctx context.Context, keep int) error {
	keep = orDefault(keep, DefaultActivityKeep)
	var cutoffID int64
	err := r.db.QueryRowContext(ctx,
		"SELECT id FROM activity_log ORDER BY timestamp DESC, id DESC LIMIT 1 OFFSET ?", keep).Scan(&cutoffID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // fewer than keep rows
	}
	if err != nil {
		return fmt.Errorf("find activity cutoff: %w", err)
	}
	if _, err := r.db.ExecContext(ctx, "DELETE FROM activity_log WHERE id <= ?", cutoffID); err != nil {
		return fmt.Errorf("trim activity: %w", err)
	}
	return nil
}

// SaveRiskRejection records a risk-engine rejection of a signal.
func (r *Repository) SaveRiskRejection(ctx context.Context, in RiskRejectionInput) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO risk_rejections (market, direction, confidence, thesis, reason, trade_idea_id, timestamp)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		in.Market, in.Direction, in.Confidence, in.Thesis, in.Reason, nullIfEmpty(in.TradeIdeaID),
		formatDBTime(timeOrNow(in.Timestamp)))
	if err != nil {
		return fmt.Errorf("save risk rejection: %w", err)
	}
	return nil
}

// GetRecentRiskRejections returns the newest risk rejections first.
// limit <= 0 uses DefaultRiskRejectionsLimit.
func (r *Repository) GetRecentRiskRejections(ctx context.Context, limit int) ([]RiskRejection, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT market, direction, confidence, thesis, reason, trade_idea_id, timestamp
		FROM risk_rejections ORDER BY timestamp DESC, id DESC LIMIT ?`,
		orDefault(limit, DefaultRiskRejectionsLimit))
	if err != nil {
		return nil, fmt.Errorf("get risk rejections: %w", err)
	}
	defer rows.Close()

	result := []RiskRejection{}
	for rows.Next() {
		var market, direction, thesis, reason, ideaID sql.NullString
		var confidence sql.NullFloat64
		var ts nullTime
		if err := rows.Scan(&market, &direction, &confidence, &thesis, &reason, &ideaID, &ts); err != nil {
			return nil, err
		}
		result = append(result, RiskRejection{
			Market: market.String, Direction: direction.String, Confidence: confidence.Float64,
			Thesis: thesis.String, Reason: reason.String, TradeIdeaID: ideaID.String, Timestamp: ts.iso(),
		})
	}
	return result, rows.Err()
}

// SaveRejectedTrade records an execution-rejected order (e.g. insufficient funds).
func (r *Repository) SaveRejectedTrade(ctx context.Context, in RejectedTradeInput) error {
	reason := in.Reason
	if reason == "" {
		reason = DefaultRejectedTradeReason
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO rejected_trades (market, direction, size, price, confidence, reason, trade_idea_id, timestamp)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		in.Market, in.Direction, in.Size, in.Price, nullableFloat(in.Confidence), reason,
		nullIfEmpty(in.TradeIdeaID), formatDBTime(timeOrNow(in.Timestamp)))
	if err != nil {
		return fmt.Errorf("save rejected trade: %w", err)
	}
	return nil
}

// GetRejectedTrades returns execution-rejected orders newest first, with the
// strategy of the linked trade idea. limit <= 0 uses DefaultRejectedTradesLimit.
func (r *Repository) GetRejectedTrades(ctx context.Context, limit int) ([]RejectedTrade, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, market, direction, size, price, confidence, reason, trade_idea_id, timestamp
		FROM rejected_trades ORDER BY timestamp DESC, id DESC LIMIT ?`,
		orDefault(limit, DefaultRejectedTradesLimit))
	if err != nil {
		return nil, fmt.Errorf("get rejected trades: %w", err)
	}
	result := []RejectedTrade{}
	ideaIDs := []string{}
	for rows.Next() {
		var t RejectedTrade
		var market, direction, reason, ideaID sql.NullString
		var size, price, confidence sql.NullFloat64
		var ts nullTime
		if err := rows.Scan(&t.ID, &market, &direction, &size, &price, &confidence, &reason, &ideaID, &ts); err != nil {
			rows.Close()
			return nil, err
		}
		t.Market, t.Direction, t.Reason, t.TradeIdeaID = market.String, direction.String, reason.String, ideaID.String
		t.Size, t.Price, t.Confidence = size.Float64, price.Float64, floatPtr(confidence)
		t.Timestamp = ts.iso()
		result = append(result, t)
		ideaIDs = append(ideaIDs, t.TradeIdeaID)
	}
	// Close before the follow-up query: the pool holds a single connection.
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	strategyByIdea, err := r.strategiesForIdeas(ctx, ideaIDs)
	if err != nil {
		return nil, err
	}
	for i := range result {
		result[i].Strategy = strategyByIdea[result[i].TradeIdeaID]
	}
	return result, nil
}

// SaveControlState upserts the single control-state row (id = 1). Nil lists
// are stored as [] and an empty SelectedStrategy uses DefaultSelectedStrategy.
func (r *Repository) SaveControlState(ctx context.Context, s ControlState) error {
	disabledMarkets, err := encodeJSON(nonNilStrings(s.DisabledMarkets))
	if err != nil {
		return err
	}
	disabledStrategies, err := encodeJSON(nonNilStrings(s.DisabledStrategies))
	if err != nil {
		return err
	}
	liveMarkets, err := encodeJSON(nonNilStrings(s.LiveMarkets))
	if err != nil {
		return err
	}
	selected := s.SelectedStrategy
	if selected == "" {
		selected = DefaultSelectedStrategy
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO control_state (id, emergency_stop, disabled_markets, disabled_strategies, live_markets, selected_strategy, updated_at)
		VALUES (1, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			emergency_stop = excluded.emergency_stop,
			disabled_markets = excluded.disabled_markets,
			disabled_strategies = excluded.disabled_strategies,
			live_markets = excluded.live_markets,
			selected_strategy = excluded.selected_strategy,
			updated_at = excluded.updated_at`,
		boolInt(s.EmergencyStop), disabledMarkets, disabledStrategies, liveMarkets, selected, formatDBTime(time.Now()))
	if err != nil {
		return fmt.Errorf("save control state: %w", err)
	}
	return nil
}

// LoadControlState returns the persisted control state, or nil if never saved.
func (r *Repository) LoadControlState(ctx context.Context) (*ControlState, error) {
	var stop sql.NullBool
	var disabledMarkets, disabledStrategies, liveMarkets, selected sql.NullString
	err := r.db.QueryRowContext(ctx, `
		SELECT emergency_stop, disabled_markets, disabled_strategies, live_markets, selected_strategy
		FROM control_state WHERE id = 1`).Scan(&stop, &disabledMarkets, &disabledStrategies, &liveMarkets, &selected)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load control state: %w", err)
	}

	state := ControlState{EmergencyStop: stop.Bool, SelectedStrategy: selected.String}
	if state.SelectedStrategy == "" {
		state.SelectedStrategy = defaultUnknownStrategyLabel
	}
	for _, f := range []struct {
		col sql.NullString
		dst *[]string
	}{
		{disabledMarkets, &state.DisabledMarkets},
		{disabledStrategies, &state.DisabledStrategies},
		{liveMarkets, &state.LiveMarkets},
	} {
		if err := decodeJSON(f.col, f.dst); err != nil {
			return nil, err
		}
		*f.dst = nonNilStrings(*f.dst)
	}
	return &state, nil
}

// SaveLLMBriefing stores an LLM market briefing.
func (r *Repository) SaveLLMBriefing(ctx context.Context, b LLMBriefingInput) error {
	outlooks, err := encodeJSON(nonNilMap(b.MarketOutlooks))
	if err != nil {
		return err
	}
	newsIDs, err := encodeJSON(nonNilStrings(b.BriefedNewsIDs))
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO llm_briefings (key_insight, overall_sentiment, market_outlooks, article_count, briefed_news_ids, generated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		b.KeyInsight, b.OverallSentiment, outlooks, b.ArticleCount, newsIDs, formatDBTime(timeOrNow(b.GeneratedAt)))
	if err != nil {
		return fmt.Errorf("save LLM briefing: %w", err)
	}
	return nil
}

// LoadLatestLLMBriefing returns the newest LLM briefing, or nil if none exists.
func (r *Repository) LoadLatestLLMBriefing(ctx context.Context) (*LLMBriefing, error) {
	var keyInsight, outlooks, newsIDs sql.NullString
	var sentiment sql.NullFloat64
	var count sql.NullInt64
	var generated nullTime
	err := r.db.QueryRowContext(ctx, `
		SELECT key_insight, overall_sentiment, market_outlooks, article_count, briefed_news_ids, generated_at
		FROM llm_briefings ORDER BY generated_at DESC, id DESC LIMIT 1`,
	).Scan(&keyInsight, &sentiment, &outlooks, &count, &newsIDs, &generated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load LLM briefing: %w", err)
	}

	b := LLMBriefing{
		KeyInsight: keyInsight.String, OverallSentiment: sentiment.Float64,
		ArticleCount: int(count.Int64), GeneratedAt: generated.iso(),
	}
	if err := decodeJSON(outlooks, &b.MarketOutlooks); err != nil {
		return nil, err
	}
	if err := decodeJSON(newsIDs, &b.BriefedNewsIDs); err != nil {
		return nil, err
	}
	b.MarketOutlooks = nonNilMap(b.MarketOutlooks)
	b.BriefedNewsIDs = nonNilStrings(b.BriefedNewsIDs)
	return &b, nil
}

// SaveLLMReflection stores an LLM outcome reflection.
func (r *Repository) SaveLLMReflection(ctx context.Context, in LLMReflectionInput) error {
	_, err := r.db.ExecContext(ctx,
		"INSERT INTO llm_reflections (pattern, suggestion, insight_confidence, generated_at) VALUES (?, ?, ?, ?)",
		in.Pattern, in.Suggestion, in.InsightConfidence, formatDBTime(timeOrNow(in.GeneratedAt)))
	if err != nil {
		return fmt.Errorf("save LLM reflection: %w", err)
	}
	return nil
}

// LoadLatestLLMReflection returns the newest LLM reflection, or nil if none exists.
func (r *Repository) LoadLatestLLMReflection(ctx context.Context) (*LLMReflection, error) {
	var pattern, suggestion sql.NullString
	var confidence sql.NullFloat64
	var generated nullTime
	err := r.db.QueryRowContext(ctx, `
		SELECT pattern, suggestion, insight_confidence, generated_at
		FROM llm_reflections ORDER BY generated_at DESC, id DESC LIMIT 1`,
	).Scan(&pattern, &suggestion, &confidence, &generated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load LLM reflection: %w", err)
	}
	return &LLMReflection{
		Pattern: pattern.String, Suggestion: suggestion.String,
		InsightConfidence: confidence.Float64, GeneratedAt: generated.iso(),
	}, nil
}

// SaveRiskState upserts the single risk-state row so daily_loss survives a
// restart. lastResetDate is stored as its UTC calendar date "YYYY-MM-DD".
func (r *Repository) SaveRiskState(ctx context.Context, dailyLoss, dailyStartEquity float64, lastResetDate time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO risk_state (id, daily_loss, daily_start_equity, last_reset_date, updated_at)
		VALUES (1, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			daily_loss = excluded.daily_loss,
			daily_start_equity = excluded.daily_start_equity,
			last_reset_date = excluded.last_reset_date,
			updated_at = excluded.updated_at`,
		dailyLoss, dailyStartEquity, lastResetDate.UTC().Format(time.DateOnly), formatDBTime(time.Now()))
	if err != nil {
		return fmt.Errorf("save risk state: %w", err)
	}
	return nil
}

// LoadRiskState returns the persisted risk state, or nil if none exists.
// A missing or unparseable reset date is deliberately treated as "no state"
// (Python swallowed the ValueError the same way) so a fresh day starts clean.
func (r *Repository) LoadRiskState(ctx context.Context) (*RiskState, error) {
	var loss, startEquity sql.NullFloat64
	var resetDate sql.NullString
	err := r.db.QueryRowContext(ctx,
		"SELECT daily_loss, daily_start_equity, last_reset_date FROM risk_state WHERE id = 1",
	).Scan(&loss, &startEquity, &resetDate)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load risk state: %w", err)
	}
	day, err := time.Parse(time.DateOnly, resetDate.String)
	if err != nil {
		return nil, nil // deliberately swallowed, see above
	}
	return &RiskState{DailyLoss: loss.Float64, DailyStartEquity: startEquity.Float64, LastResetDate: day}, nil
}

// nonNilStrings returns s, or an empty slice when s is nil (Python's `x or []`).
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
