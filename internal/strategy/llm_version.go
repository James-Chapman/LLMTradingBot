package strategy

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"sync"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/llm"
	"github.com/James-Chapman/LLMTradingBot/internal/observability"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy/calc"
)

// Prompter is a non-LLM strategy that can describe one of its signals to the
// LLM in its own words: its LLM version sends this prompt with every entry.
type Prompter interface {
	Strategy
	// Prompt returns the strategy's own prompt for idea on data.
	Prompt(idea domain.TradeIdea, data MarketData) llm.StrategyPrompt
}

// llmVerdict is the LLM's answer for one market, kept while the signal's
// last closed 4-hour candle is unchanged.
type llmVerdict struct {
	candle   int64
	analysis llm.SignalAnalysis
}

// LLMVersion runs a base strategy and asks the LLM about each new entry with
// the base strategy's own prompt plus the news, briefing and portfolio. The
// LLM's confidence scale multiplies the entry's confidence; a scale below
// LLM_VETO_THRESHOLD drops it. Exits are passed on without asking the LLM,
// so an LLM outage or veto can never keep a position open.
type LLMVersion struct {
	base     Prompter
	id       string
	settings config.Settings
	log      *slog.Logger

	mu       sync.Mutex
	verdicts map[string]llmVerdict // market → verdict for its last closed candle
}

// NewLLMVersion creates the LLM version of base under the strategy id id.
func NewLLMVersion(base Prompter, id string, s config.Settings) *LLMVersion {
	return &LLMVersion{base: base, id: id, settings: s, log: observability.Logger(id), verdicts: map[string]llmVerdict{}}
}

// ID returns the LLM version's strategy id.
func (v *LLMVersion) ID() string { return v.id }

// Base returns the strategy this version wraps.
func (v *LLMVersion) Base() Strategy { return v.base }

// UsesLLM is true: entries are reviewed by the LLM signal analyser.
func (v *LLMVersion) UsesLLM() bool { return true }

// RequiredPriceTicks is the base strategy's need; the 4-hour strategies
// read candles, not the tick price history, so this is 0 for them.
func (v *LLMVersion) RequiredPriceTicks() int { return v.base.RequiredPriceTicks() }

// Evaluate returns the base strategy's ideas under this version's id, with
// each new entry scaled or vetoed by the LLM.
func (v *LLMVersion) Evaluate(ctx context.Context, in Input) []domain.TradeIdea {
	analyser := in.Analyser
	if analyser == nil {
		analyser = NeutralAnalyser{}
	}
	ideas := []domain.TradeIdea{}
	for _, idea := range v.base.Evaluate(ctx, in) {
		idea.StrategyID = v.id
		withLLMFields(&idea, nil)
		if idea.Direction != domain.Long || holdsLong(in.Portfolio, idea.Market) {
			// Exits, and entries the loop will skip because the market is
			// already held, go on without an LLM call.
			ideas = append(ideas, idea)
			continue
		}
		if reviewed := v.review(ctx, analyser, idea, in); reviewed != nil {
			ideas = append(ideas, *reviewed)
		}
	}
	return ideas
}

// review asks the LLM about one entry (or reuses its verdict for the same
// closed candle) and returns the scaled idea, or nil when vetoed.
func (v *LLMVersion) review(ctx context.Context, analyser Analyser, idea domain.TradeIdea, in Input) *domain.TradeIdea {
	data := in.MarketData[idea.Market]
	analysis := v.verdict(ctx, analyser, idea, data, in)
	if !analysis.LLMUsed {
		return &idea // LLM unavailable: the base entry stands, as basic_and_llm does
	}
	if analysis.ConfidenceScale < v.settings.LLMVetoThreshold {
		v.log.Info(fmt.Sprintf("%s long vetoed by LLM — scale %.2f < threshold %.2f: %s",
			idea.Market, analysis.ConfidenceScale, v.settings.LLMVetoThreshold, analysis.Reasoning))
		return nil
	}
	base := idea.Confidence
	idea.Confidence = math.Min(calc.MaxConfidence, base*analysis.ConfidenceScale)
	idea.Thesis += " | LLM: " + analysis.Reasoning
	withLLMFields(&idea, &analysis)
	v.log.Info("Generated trade idea", "strategy", v.id, "market", idea.Market,
		"base_confidence", pyRound(base, 3), "llm_scale", pyRound(analysis.ConfidenceScale, 3),
		"final_confidence", pyRound(idea.Confidence, 3))
	return &idea
}

// verdict returns the LLM's analysis of idea, asking only when the market's
// last closed 4-hour candle has changed since the last answer. Answers made
// without the LLM are not kept, so an outage is retried on the next tick.
func (v *LLMVersion) verdict(ctx context.Context, analyser Analyser, idea domain.TradeIdea, data MarketData, in Input) llm.SignalAnalysis {
	candle, hasCandle := lastClosedCandle(data.TrendCandles)
	v.mu.Lock()
	cached, ok := v.verdicts[idea.Market]
	v.mu.Unlock()
	if hasCandle && ok && cached.candle == candle {
		return cached.analysis
	}
	analysis := askLLM(ctx, analyser, v.base, idea, data, in, v.settings)
	if hasCandle && analysis.LLMUsed {
		v.mu.Lock()
		v.verdicts[idea.Market] = llmVerdict{candle: candle, analysis: analysis}
		v.mu.Unlock()
	}
	return analysis
}

// askLLM sends one entry of base to the LLM with base's own prompt and the
// news, indicators and portfolio in in. The LLM versions and the shadow
// reviews both ask through here, so a shadow verdict is exactly what the LLM
// version would have been told.
func askLLM(ctx context.Context, analyser Analyser, base Prompter, idea domain.TradeIdea, data MarketData, in Input, s config.Settings) llm.SignalAnalysis {
	prompt := base.Prompt(idea, data)
	return analyser.AnalyseSignal(ctx, llm.SignalRequest{
		Market:         idea.Market,
		Direction:      string(idea.Direction),
		MomentumPct:    change24h(data.TrendCandles) * 100,
		BaseConfidence: idea.Confidence,
		News:           in.News,
		CurrentPrice:   data.Price,
		Indicators:     data.Indicators,
		Equity:         in.Portfolio.Equity,
		Cash:           in.Portfolio.Cash,
		OpenPositions:  in.Portfolio.OpenPositions,
		TickSeconds:    s.StrategyIntervalSeconds,
		Strategy:       &prompt,
	})
}

// withLLMFields sets the llm_* supporting signals: nil when the LLM was not
// used (as basic_and_llm records them), else its sentiment, scale and reason.
func withLLMFields(idea *domain.TradeIdea, a *llm.SignalAnalysis) {
	signals := make(map[string]any, len(idea.SupportingSignals)+3)
	for k, val := range idea.SupportingSignals {
		signals[k] = val
	}
	signals["llm_sentiment"], signals["llm_confidence_scale"], signals["llm_reasoning"] = nil, nil, nil
	if a != nil {
		signals["llm_sentiment"] = pyRound(a.Sentiment, 3)
		signals["llm_confidence_scale"] = pyRound(a.ConfidenceScale, 3)
		signals["llm_reasoning"] = a.Reasoning
	}
	idea.SupportingSignals = signals
}

// holdsLong reports whether the portfolio holds a long position in market.
func holdsLong(p Portfolio, market string) bool {
	for _, pos := range p.OpenPositions {
		if pos.Market == market && pos.Size > 0 {
			return true
		}
	}
	return false
}

// lastClosedCandle returns the start time of the newest finished 4-hour
// candle (the last row is Kraken's forming candle) and whether there is one.
func lastClosedCandle(candles []domain.Candle) (int64, bool) {
	if len(candles) < 2 {
		return 0, false
	}
	return candles[len(candles)-2].T, true
}

// change24h is the change over the last six closed 4-hour candles, as a
// fraction; 0 when there are too few.
func change24h(candles []domain.Candle) float64 {
	closes := closedCloses(candles)
	if len(closes) < 7 || closes[len(closes)-7] == 0 {
		return 0
	}
	return closes[len(closes)-1]/closes[len(closes)-7] - 1
}

// promptData formats a strategy's supporting signals for its prompt,
// leaving out the llm_* fields its LLM version adds.
func promptData(signals map[string]any) map[string]string {
	out := make(map[string]string, len(signals))
	for k, val := range signals {
		if strings.HasPrefix(k, "llm_") || val == nil {
			continue
		}
		switch x := val.(type) {
		case float64:
			out[k] = strconv.FormatFloat(x, 'g', 6, 64)
		default:
			out[k] = fmt.Sprint(x)
		}
	}
	return out
}

// pct formats a fraction as a signed percentage with two decimals.
func pct(f float64) string { return strconv.FormatFloat(f*100, 'f', 2, 64) + "%" }
