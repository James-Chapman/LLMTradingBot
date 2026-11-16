package strategy

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/llm"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy/calc"
)

// BasicAndLLMStrategy runs the basic candle rules, then asks the LLM to
// validate each new entry (emitted once per candle, so asked once per
// candle): confidence is scaled by the LLM and a scale below the veto
// threshold drops the trade. Exits and covers of held positions skip the
// LLM. Four versions: 1h or 4h candles, with or without paper shorts.
type BasicAndLLMStrategy struct {
	basicCore
}

// newBasicAndLLM returns an indicator-plus-LLM strategy on candles of minutes length.
func newBasicAndLLM(id string, minutes int, longOnly bool, s config.Settings) *BasicAndLLMStrategy {
	b := &BasicAndLLMStrategy{}
	b.init(id, minutes, longOnly, s)
	return b
}

// NewBasicAndLLM1h creates basic_and_llm_strategy_1h (1-hour candles, may short).
func NewBasicAndLLM1h(s config.Settings) *BasicAndLLMStrategy {
	return newBasicAndLLM(BasicAndLLM1hID, 60, false, s)
}

// NewBasicAndLLM4h creates basic_and_llm_strategy_4h (4-hour candles, may short).
func NewBasicAndLLM4h(s config.Settings) *BasicAndLLMStrategy {
	return newBasicAndLLM(BasicAndLLM4hID, 240, false, s)
}

// NewBasicAndLLMLong1h creates basic_and_llm_strategy_long_1h (1-hour candles, long only).
func NewBasicAndLLMLong1h(s config.Settings) *BasicAndLLMStrategy {
	return newBasicAndLLM(BasicAndLLMLong1hID, 60, true, s)
}

// NewBasicAndLLMLong4h creates basic_and_llm_strategy_long_4h (4-hour candles, long only).
func NewBasicAndLLMLong4h(s config.Settings) *BasicAndLLMStrategy {
	return newBasicAndLLM(BasicAndLLMLong4hID, 240, true, s)
}

// UsesLLM is true: entries are validated by the LLM signal analyser.
func (b *BasicAndLLMStrategy) UsesLLM() bool { return true }

// Evaluate returns at most one idea per market, in market order. A nil
// analyser behaves like an unavailable LLM (neutral scale, no veto).
func (b *BasicAndLLMStrategy) Evaluate(ctx context.Context, in Input) []domain.TradeIdea {
	analyser := in.Analyser
	if analyser == nil {
		analyser = NeutralAnalyser{}
	}
	ideas := []domain.TradeIdea{}
	for _, d := range b.decisions(in) {
		if d.action == actExit || d.action == actCover {
			ideas = append(ideas, b.ruleIdea(d))
			continue
		}
		if idea := b.validated(ctx, d, in, analyser); idea != nil {
			ideas = append(ideas, *idea)
		}
	}
	return ideas
}

// validated returns the entry after the LLM's verdict, or nil when vetoed.
func (b *BasicAndLLMStrategy) validated(ctx context.Context, d basicDecision, in Input, analyser Analyser) *domain.TradeIdea {
	sig := d.reading.sig
	baseConfidence := sig.Confidence.Confidence
	answer := b.ask(ctx, d, in, analyser)
	if answer.ConfidenceScale < b.settings.LLMVetoThreshold {
		b.log.Debug(fmt.Sprintf("%s %s vetoed by LLM — scale %.2f < threshold %.2f: %s",
			d.symbol, sig.Direction, answer.ConfidenceScale, b.settings.LLMVetoThreshold, answer.Reasoning))
		return nil
	}

	idea := b.entryIdea(d, fmt.Sprintf("Enter at market on indicator consensus on the closed %s candles and LLM validation", b.interval()))
	idea.Confidence = math.Min(calc.MaxConfidence, baseConfidence*answer.ConfidenceScale)
	// LLM fields are always present; nil when the LLM was not used.
	idea.SupportingSignals["llm_sentiment"] = nil
	idea.SupportingSignals["llm_confidence_scale"] = nil
	idea.SupportingSignals["llm_reasoning"] = nil
	if answer.LLMUsed {
		idea.SupportingSignals["llm_sentiment"] = pyRound(answer.Sentiment, 3)
		idea.SupportingSignals["llm_confidence_scale"] = pyRound(answer.ConfidenceScale, 3)
		idea.SupportingSignals["llm_reasoning"] = answer.Reasoning
		idea.Thesis = strings.Join([]string{idea.Thesis, "LLM: " + answer.Reasoning}, " | ")
	}
	b.log.Debug("Generated trade idea", "strategy", b.id, "market", d.symbol,
		"direction", string(sig.Direction), "base_confidence", pyRound(baseConfidence, 3),
		"llm_scale", pyRound(answer.ConfidenceScale, 3), "final_confidence", pyRound(idea.Confidence, 3), "llm_used", answer.LLMUsed)
	return &idea
}

// ask returns the LLM's verdict on an entry. The prompt names the candle
// length, and the price is the live price, not the last candle's close.
func (b *BasicAndLLMStrategy) ask(ctx context.Context, d basicDecision, in Input, analyser Analyser) llm.SignalAnalysis {
	sig := d.reading.sig
	return analyser.AnalyseSignal(ctx, llm.SignalRequest{
		Market:         d.symbol,
		Direction:      string(sig.Direction),
		MomentumPct:    sig.Momentum * 100,
		BaseConfidence: sig.Confidence.Confidence,
		News:           in.News,
		CurrentPrice:   d.data.Price,
		Indicators:     d.reading.view.Indicators,
		Equity:         in.Portfolio.Equity,
		Cash:           in.Portfolio.Cash,
		OpenPositions:  in.Portfolio.OpenPositions,
		TickSeconds:    b.minutes * 60,
	})
}
