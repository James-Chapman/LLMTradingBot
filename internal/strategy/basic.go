package strategy

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/observability"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy/calc"
)

// BasicStrategy is the indicator-only strategy: momentum gate, hard filters,
// indicator vote, support and consensus gates, then confidence.
type BasicStrategy struct {
	settings config.Settings
	log      *slog.Logger
}

// NewBasic creates the indicator-only strategy.
func NewBasic(s config.Settings) *BasicStrategy {
	return &BasicStrategy{settings: s, log: observability.Logger("basic_strategy")}
}

// ID returns "basic_strategy".
func (b *BasicStrategy) ID() string { return BasicID }

// UsesLLM is false: the basic strategy needs no news or analyser.
func (b *BasicStrategy) UsesLLM() bool { return false }

// RequiredPriceTicks is 0: the basic strategy waits for indicator warm-up instead.
func (b *BasicStrategy) RequiredPriceTicks() int { return 0 }

// Evaluate returns one idea per market whose indicators pass every gate.
func (b *BasicStrategy) Evaluate(_ context.Context, in Input) []domain.TradeIdea {
	b.log.Info(fmt.Sprintf("Starting evaluation across %d markets.", len(in.MarketData)))
	ideas := []domain.TradeIdea{}
	for _, symbol := range sortedMarkets(in.MarketData) {
		if idea := b.evaluateMarket(symbol, in.MarketData[symbol]); idea != nil {
			ideas = append(ideas, *idea)
		}
	}
	if len(ideas) == 0 {
		b.log.Debug("No signals generated this tick")
	} else {
		b.log.Info(fmt.Sprintf("%d trade ideas generated successfully.", len(ideas)))
	}
	return ideas
}

// evaluateMarket returns a trade idea for one market, or nil when gated out.
func (b *BasicStrategy) evaluateMarket(symbol string, data MarketData) *domain.TradeIdea {
	sig := evaluateIndicatorGates(b.log, symbol, data)
	if sig == nil {
		return nil
	}

	slPct := stopLossPercent(b.settings.StopLossPct)
	idea := &domain.TradeIdea{
		ID:                     domain.NewID(),
		StrategyID:             BasicID,
		Market:                 symbol,
		Direction:              sig.Direction,
		Thesis:                 strings.Join(sig.thesisParts(), " | "),
		SupportingSignals:      sig.supportingSignals(data),
		Confidence:             sig.Confidence.Confidence,
		EntryPlan:              "Enter at market on indicator consensus confirmation",
		ExitPlan:               fmt.Sprintf("Exit on momentum reversal or stop-loss at %d%% loss", slPct),
		StopOrInvalidation:     fmt.Sprintf("Abandon if unrealised loss reaches %d%%", slPct),
		PositionSizingProposal: calc.CalculatePositionSizing(sig.ATRPct),
		ModeEligibility:        allModes(),
	}
	b.log.Info("Generated trade idea", "strategy", BasicID, "market", symbol,
		"direction", string(sig.Direction), "confidence", pyRound(idea.Confidence, 3),
		"votes", fmt.Sprintf("%+d/%d", sig.Votes.Votes, sig.Votes.Available))
	return idea
}
