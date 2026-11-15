package app

import (
	"context"
	"fmt"
	"maps"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/shadow"
	"github.com/James-Chapman/LLMTradingBot/internal/storage"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy"
)

// ShadowReport is GET /api/shadow-reviews: whether shadow reviews are on and
// how the LLM's would-be vetoes compare with each strategy's closed trades.
type ShadowReport struct {
	Enabled         bool                  `json:"enabled"`
	VetoThreshold   float64               `json:"veto_threshold"`
	MinScoredVetoes int                   `json:"min_scored_vetoes"`
	Strategies      []shadow.Score        `json:"strategies"`
	Recent          []shadow.RecentReview `json:"recent"`
}

// queueShadowReviews asks the LLM, in the background, about each new entry
// of a non-LLM 4-hour strategy as its LLM version would, and records the
// verdict. It runs only with LLM_SHADOW_REVIEW on, an LLM that can be tried
// and news to show it (the LLM version waits for news too). The ideas are
// never changed and the tick does not wait for the LLM.
func (a *App) queueShadowReviews(ctx context.Context, active strategy.Strategy, ideas []domain.TradeIdea, in strategy.Input) {
	cfg := a.cfg()
	if !cfg.LLMShadowReview || active.UsesLLM() || len(ideas) == 0 || !a.llmClient.CanAttempt() {
		return
	}
	in.News = newsArticles(a.News())
	if len(in.News) == 0 {
		return
	}
	in.Analyser = a.analyser
	in.Briefing = a.analyser.LatestBriefing()
	// The tick keeps using its ideas (handleIdea adds to their signals), so
	// the reviews get their own copies.
	own := make([]domain.TradeIdea, len(ideas))
	for i, idea := range ideas {
		idea.SupportingSignals = maps.Clone(idea.SupportingSignals)
		own[i] = idea
	}
	entries := a.shadow.Claim(active, own, in)
	if len(entries) == 0 {
		return
	}
	a.tasks.Add(1)
	go func() {
		defer a.tasks.Done()
		a.shadowMu.Lock()
		defer a.shadowMu.Unlock()
		for _, e := range entries {
			a.recordShadowVerdict(ctx, a.shadow.Review(ctx, a.analyser, e, cfg))
		}
	}()
}

// recordShadowVerdict saves one verdict and notes it in the activity feed.
func (a *App) recordShadowVerdict(ctx context.Context, v strategy.ShadowVerdict) {
	review := shadow.Review{
		StrategyID: v.StrategyID, Market: v.Market, Candle: time.Unix(v.Candle, 0).UTC(), ReviewedAt: a.now().UTC(),
		TradeIdeaID: v.TradeIdeaID, BaseConfidence: v.BaseConfidence, LLMUsed: v.Analysis.LLMUsed,
		Scale: v.Analysis.ConfidenceScale, Sentiment: v.Analysis.Sentiment, Reasoning: v.Analysis.Reasoning,
		WouldVeto: v.WouldVeto, VetoThreshold: v.VetoThreshold,
	}
	// Saved even while shutting down: the answer has already been paid for.
	if err := a.repo.SaveShadowReview(context.WithoutCancel(ctx), review); err != nil {
		a.logger.Warn("Could not save shadow review", "market", v.Market, "strategy", v.StrategyID, "error", err.Error())
		return
	}
	switch {
	case !v.Analysis.LLMUsed:
		a.activity.Info(fmt.Sprintf("Shadow review %s - LLM unavailable, will retry", v.Market), v.StrategyID)
	case v.WouldVeto:
		a.activity.Info(fmt.Sprintf("Shadow review %s - LLM would veto (scale %.2f < %.2f), trade unaffected", v.Market, v.Analysis.ConfidenceScale, v.VetoThreshold), v.Analysis.Reasoning)
	default:
		a.activity.Info(fmt.Sprintf("Shadow review %s - LLM would keep (scale %.2f)", v.Market, v.Analysis.ConfidenceScale), v.Analysis.Reasoning)
	}
}

// ShadowReviews scores the stored shadow verdicts against the closed trades
// of the strategies they reviewed.
func (a *App) ShadowReviews(ctx context.Context) (ShadowReport, error) {
	cfg := a.cfg()
	report := ShadowReport{
		Enabled: cfg.LLMShadowReview, VetoThreshold: cfg.LLMVetoThreshold, MinScoredVetoes: shadow.DefaultMinScoredVetoes,
		Strategies: []shadow.Score{}, Recent: []shadow.RecentReview{},
	}
	reviews, err := a.repo.GetShadowReviews(ctx, 0)
	if err != nil {
		return report, err
	}
	var outcomes []shadow.Outcome
	seen := map[string]bool{}
	for _, r := range reviews {
		if seen[r.StrategyID] {
			continue
		}
		seen[r.StrategyID] = true
		rows, err := a.repo.GetSignalOutcomes(ctx, r.StrategyID, storage.DefaultShadowReviewLimit)
		if err != nil {
			return report, err
		}
		for _, o := range rows {
			outcomes = append(outcomes, shadow.Outcome{StrategyID: o.StrategyID, Market: o.Market, Direction: o.Direction, EntryAt: o.EntryAt, PnL: o.PnL})
		}
	}
	summary := shadow.Summarise(reviews, outcomes, shadow.DefaultMinScoredVetoes)
	report.Strategies, report.Recent = summary.Strategies, summary.Recent
	return report, nil
}
