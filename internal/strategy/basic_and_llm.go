package strategy

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/llm"
	"github.com/James-Chapman/LLMTradingBot/internal/observability"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy/calc"
)

// BasicAndLLMStrategy runs the basic indicator gates, then asks the LLM to
// validate the signal: confidence is scaled by the LLM and a scale below the
// veto threshold drops the trade.
type BasicAndLLMStrategy struct {
	settings config.Settings
	log      *slog.Logger
}

// NewBasicAndLLM creates the indicator-plus-LLM strategy.
func NewBasicAndLLM(s config.Settings) *BasicAndLLMStrategy {
	return &BasicAndLLMStrategy{settings: s, log: observability.Logger("basic_and_llm_strategy")}
}

// ID returns "basic_and_llm_strategy".
func (b *BasicAndLLMStrategy) ID() string { return BasicAndLLMID }

// UsesLLM is true: evaluation calls the LLM signal analyser.
func (b *BasicAndLLMStrategy) UsesLLM() bool { return true }

// RequiredPriceTicks is the full indicator lookback plus the current tick.
func (b *BasicAndLLMStrategy) RequiredPriceTicks() int { return LookbackTicks + 1 }

// Evaluate returns one LLM-validated idea per market that passes every gate.
// A nil analyser behaves like an unavailable LLM (neutral scale, no veto).
func (b *BasicAndLLMStrategy) Evaluate(ctx context.Context, in Input) []domain.TradeIdea {
	analyser := in.Analyser
	if analyser == nil {
		analyser = NeutralAnalyser{}
	}
	b.log.Info(fmt.Sprintf("Starting evaluation across %d markets.", len(in.MarketData)))
	ideas := []domain.TradeIdea{}
	for _, symbol := range sortedMarkets(in.MarketData) {
		if idea := b.evaluateMarket(ctx, symbol, in.MarketData[symbol], in, analyser); idea != nil {
			ideas = append(ideas, *idea)
		}
	}
	b.log.Info(fmt.Sprintf("Evaluation complete. Generated %d trade ideas.", len(ideas)))
	return ideas
}

// evaluateMarket returns an LLM-validated trade idea for one market, or nil.
func (b *BasicAndLLMStrategy) evaluateMarket(ctx context.Context, symbol string, data MarketData, in Input, analyser Analyser) *domain.TradeIdea {
	sig := evaluateIndicatorGates(b.log, symbol, data)
	if sig == nil {
		return nil
	}
	baseConfidence := sig.Confidence.Confidence

	analysis := analyser.AnalyseSignal(ctx, llm.SignalRequest{
		Market:         symbol,
		Direction:      string(sig.Direction),
		MomentumPct:    sig.Momentum * 100,
		BaseConfidence: baseConfidence,
		News:           in.News,
		CurrentPrice:   data.Price,
		Indicators:     data.Indicators,
		Equity:         in.Portfolio.Equity,
		Cash:           in.Portfolio.Cash,
		OpenPositions:  in.Portfolio.OpenPositions,
	})

	if analysis.ConfidenceScale < b.settings.LLMVetoThreshold {
		b.log.Info(fmt.Sprintf("%s %s vetoed by LLM — scale %.2f < threshold %.2f: %s",
			symbol, sig.Direction, analysis.ConfidenceScale, b.settings.LLMVetoThreshold, analysis.Reasoning))
		return nil
	}

	finalConfidence := math.Min(calc.MaxConfidence, baseConfidence*analysis.ConfidenceScale)

	thesis := sig.thesisParts()
	if analysis.LLMUsed {
		thesis = append(thesis, "LLM: "+analysis.Reasoning)
	}

	signals := sig.supportingSignals(data)
	// LLM fields are always present; nil when the LLM was not used (Python None).
	signals["llm_sentiment"] = nil
	signals["llm_confidence_scale"] = nil
	signals["llm_reasoning"] = nil
	if analysis.LLMUsed {
		signals["llm_sentiment"] = pyRound(analysis.Sentiment, 3)
		signals["llm_confidence_scale"] = pyRound(analysis.ConfidenceScale, 3)
		signals["llm_reasoning"] = analysis.Reasoning
	}

	slPct := stopLossPercent(b.settings.StopLossPct)
	idea := &domain.TradeIdea{
		ID:                     domain.NewID(),
		StrategyID:             BasicAndLLMID,
		Market:                 symbol,
		Direction:              sig.Direction,
		Thesis:                 strings.Join(thesis, " | "),
		SupportingSignals:      signals,
		Confidence:             finalConfidence,
		EntryPlan:              "Enter at market on indicator consensus and LLM validation",
		ExitPlan:               fmt.Sprintf("Exit on momentum reversal or stop-loss at %d%% loss", slPct),
		StopOrInvalidation:     fmt.Sprintf("Abandon if unrealised loss reaches %d%%", slPct),
		PositionSizingProposal: calc.CalculatePositionSizing(sig.ATRPct),
		ModeEligibility:        allModes(),
	}
	b.log.Info("Generated trade idea", "strategy", BasicAndLLMID, "market", symbol,
		"direction", string(sig.Direction), "base_confidence", pyRound(baseConfidence, 3),
		"llm_scale", pyRound(analysis.ConfidenceScale, 3), "final_confidence", pyRound(finalConfidence, 3),
		"votes", fmt.Sprintf("%+d/%d", sig.Votes.Votes, sig.Votes.Available), "llm_used", analysis.LLMUsed)
	return idea
}
