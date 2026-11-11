package strategy

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/llm"
	"github.com/James-Chapman/LLMTradingBot/internal/observability"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy/calc"
)

// LLMOnlyStrategy applies no indicator gates: it passes price, indicator,
// portfolio and news context to the LLM and acts only on an explicit
// long/short recommendation. Markets are evaluated concurrently.
type LLMOnlyStrategy struct {
	settings config.Settings
	log      *slog.Logger
	// MaxConcurrency bounds the number of LLM calls in flight (at least 1).
	MaxConcurrency int
}

// NewLLMOnly creates the LLM-only strategy, bounded by s.LLMOnlyMaxConcurrency.
func NewLLMOnly(s config.Settings) *LLMOnlyStrategy {
	return &LLMOnlyStrategy{
		settings:       s,
		log:            observability.Logger("llm_only_strategy"),
		MaxConcurrency: max(1, s.LLMOnlyMaxConcurrency),
	}
}

// ID returns "llm_only_strategy".
func (l *LLMOnlyStrategy) ID() string { return LLMOnlyID }

// UsesLLM is true: evaluation asks the LLM for a recommendation.
func (l *LLMOnlyStrategy) UsesLLM() bool { return true }

// RequiredPriceTicks is the depth needed for averaged momentum (two windows).
func (l *LLMOnlyStrategy) RequiredPriceTicks() int { return calc.MomentumWindowPoints * 2 }

// Evaluate asks the LLM about every market concurrently (bounded by
// MaxConcurrency) and returns the ideas sorted by market.
func (l *LLMOnlyStrategy) Evaluate(ctx context.Context, in Input) []domain.TradeIdea {
	ideas := []domain.TradeIdea{}
	if in.Analyser == nil {
		l.log.Warn("LLMAnalyser not provided. Cannot evaluate market.")
		return ideas
	}

	markets := sortedMarkets(in.MarketData)
	l.log.Info(fmt.Sprintf("Starting evaluation across %d markets.", len(markets)))

	// Each goroutine writes only its own slot, so results keep the sorted order.
	results := make([]*domain.TradeIdea, len(markets))
	semaphore := make(chan struct{}, l.MaxConcurrency)
	var wg sync.WaitGroup
	for i, symbol := range markets {
		wg.Go(func() {
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-semaphore }()
			results[i] = l.evaluateMarketSafely(ctx, symbol, in.MarketData[symbol], in)
		})
	}
	wg.Wait()

	for _, idea := range results {
		if idea != nil {
			ideas = append(ideas, *idea)
		}
	}
	l.log.Info(fmt.Sprintf("Finished market evaluations. Successfully generated %d trade ideas.", len(ideas)))
	return ideas
}

// evaluateMarketSafely evaluates one market, logging and skipping it if the
// evaluation panics (Python gather(return_exceptions=True) behaviour).
func (l *LLMOnlyStrategy) evaluateMarketSafely(ctx context.Context, symbol string, data MarketData, in Input) (idea *domain.TradeIdea) {
	defer func() {
		if r := recover(); r != nil {
			l.log.Error(fmt.Sprintf("LLM market evaluation failed for %s: %v", symbol, r))
			idea = nil
		}
	}()
	return l.evaluateMarket(ctx, symbol, data, in)
}

// evaluateMarket returns a trade idea when the LLM recommends long or short.
func (l *LLMOnlyStrategy) evaluateMarket(ctx context.Context, symbol string, data MarketData, in Input) *domain.TradeIdea {
	if data.Price <= 0 {
		l.log.Warn(fmt.Sprintf("Skipping %s: Price data missing.", symbol))
		return nil
	}
	previous := data.PreviousPrice
	if previous == 0 {
		previous = data.Price // Python: data.get("previous_price", current_price)
	}

	rec := in.Analyser.RecommendTrade(ctx, llm.RecommendationRequest{
		Market:        symbol,
		CurrentPrice:  data.Price,
		PreviousPrice: previous,
		PriceHistory:  data.PriceHistory,
		Indicators:    data.Indicators,
		News:          in.News,
		Equity:        in.Portfolio.Equity,
		Cash:          in.Portfolio.Cash,
		OpenPositions: in.Portfolio.OpenPositions,
	})

	if !rec.LLMUsed {
		l.log.Info(fmt.Sprintf("Skipping %s: LLM analysis was not used.", symbol))
		return nil
	}
	var direction domain.Direction
	switch rec.Action {
	case "long":
		direction = domain.Long
	case "short":
		direction = domain.Short
	case "hold":
		l.log.Info(fmt.Sprintf("Skipping %s: LLM recommended 'hold'.", symbol))
		return nil
	default:
		l.log.Error(fmt.Sprintf("Skipping %s: Invalid action from LLM: %s.", symbol, rec.Action))
		return nil
	}

	momentum, ok := calc.CalculateMomentum(data.Price, &previous, data.PriceHistory)
	if !ok {
		momentum = 0
	}
	return l.buildTradeIdea(symbol, direction, data.Price, previous, momentum, rec)
}

// buildTradeIdea converts an LLM recommendation into the domain trade idea.
func (l *LLMOnlyStrategy) buildTradeIdea(symbol string, direction domain.Direction, currentPrice, previousPrice, momentum float64, rec llm.LLMTradeRecommendation) *domain.TradeIdea {
	l.log.Info(fmt.Sprintf("Building trade idea for %s (Direction: %s). Final Confidence: %.2f",
		symbol, direction, rec.Confidence))
	slPct := stopLossPercent(l.settings.StopLossPct)
	return &domain.TradeIdea{
		ID:         domain.NewID(),
		StrategyID: LLMOnlyID,
		Market:     symbol,
		Direction:  direction,
		Thesis:     "LLM recommendation: " + rec.Reasoning,
		SupportingSignals: map[string]any{
			"llm_only":           true,
			"llm_strategy":       true,
			"llm_action":         rec.Action,
			"llm_sentiment":      pyRound(rec.Sentiment, 3),
			"llm_reasoning":      rec.Reasoning,
			"pre_llm_confidence": rec.Confidence,
			"momentum":           pyRound(momentum, 6),
			"current_price":      currentPrice,
			"previous_price":     previousPrice,
		},
		Confidence:             rec.Confidence,
		EntryPlan:              "Enter at market on LLM recommendation",
		ExitPlan:               fmt.Sprintf("Exit on LLM reversal or stop-loss at %d%% loss", slPct),
		StopOrInvalidation:     fmt.Sprintf("Abandon if unrealised loss reaches %d%%", slPct),
		PositionSizingProposal: calc.CalculatePositionSizing(0.0),
		ModeEligibility:        allModes(),
	}
}
