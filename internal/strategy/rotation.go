package strategy

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/llm"
	"github.com/James-Chapman/LLMTradingBot/internal/observability"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy/calc"
)

// Rotation parameters. Every watched market is ranked by its return over the
// last RotationLookback finished 4-hour candles; the strategy holds the
// RotationTop strongest while their return is positive.
const (
	// RotationLookback is the return window: 84 candles (14 days).
	RotationLookback = 84
	// RotationTop is how many of the strongest markets are held.
	RotationTop = 3
	// RotationMinClosedCandles is the warm-up before a market is ranked.
	RotationMinClosedCandles = RotationLookback + 1
)

// rotationInstructions is the rotation strategy's own prompt to the LLM.
const rotationInstructions = "This is a relative-strength rotation entry: every watched market is ranked by its return " +
	"over the last 14 days of finished 4-hour candles, and this market is among the 3 strongest with a positive " +
	"return. The position is held while the market stays near the top and its return stays positive, typically " +
	"for one to four weeks. Leaders often keep leading for a while, but a market that has already risen a lot can " +
	"reverse hard. Use a scale above 1.0 when the news or briefing explains the strength with something lasting " +
	"(adoption, upgrades, fund flows); below 1.0 when the rise rests on hype, a one-off event or a pump likely to " +
	"fade; below 0.7 only when you expect the leadership to end soon."

// RotationStrategy holds the strongest watched markets by 14-day return.
type RotationStrategy struct {
	settings config.Settings
	log      *slog.Logger
}

// NewRotation creates the relative-strength rotation strategy.
func NewRotation(s config.Settings) *RotationStrategy {
	return &RotationStrategy{settings: s, log: observability.Logger(RotationID)}
}

// NewRotationAndLLM creates the rotation strategy's LLM version.
func NewRotationAndLLM(s config.Settings) *LLMVersion {
	return NewLLMVersion(NewRotation(s), RotationAndLLMID, s)
}

// ID returns "rotation_strategy".
func (r *RotationStrategy) ID() string { return RotationID }

// UsesLLM is false: the ranking needs no news or analyser.
func (r *RotationStrategy) UsesLLM() bool { return false }

// RequiredPriceTicks is 0: markets wait for their own 4-hour candle warm-up.
func (r *RotationStrategy) RequiredPriceTicks() int { return 0 }

// ranked is one market's place in the ranking.
type ranked struct {
	market string
	ret    float64 // return over the lookback, as a fraction
	rank   int     // 1 = strongest
}

// Evaluate ranks the markets with enough candles and returns a long for the
// top RotationTop with a positive return, nothing for the next one (a
// buffer, so a market at the edge is not bought and sold on every swap), and
// an exit for the rest and for any with a negative return. As for every
// 4-hour strategy, only actionable ideas are returned (see actionable).
func (r *RotationStrategy) Evaluate(_ context.Context, in Input) []domain.TradeIdea {
	ranks := rankMarkets(in.MarketData)
	ideas := []domain.TradeIdea{}
	for _, m := range ranks {
		var dir domain.Direction
		switch {
		case m.ret <= 0 || m.rank > RotationTop+1:
			dir = domain.Short
		case m.rank <= RotationTop:
			dir = domain.Long
		default:
			continue // the buffer rank: keep whatever is held
		}
		if idea := r.idea(m, dir, len(ranks), in.MarketData[m.market].Price); actionable(&idea, in.Portfolio) {
			ideas = append(ideas, idea)
		}
	}
	sort.Slice(ideas, func(i, j int) bool { return ideas[i].Market < ideas[j].Market })
	return ideas
}

// rankMarkets returns every market with enough finished candles, strongest
// first (ties by market name, so the order is deterministic).
func rankMarkets(data map[string]MarketData) []ranked {
	var out []ranked
	for _, market := range sortedMarkets(data) {
		closes := closedCloses(data[market].TrendCandles)
		if len(closes) < RotationMinClosedCandles {
			continue
		}
		then := closes[len(closes)-1-RotationLookback]
		if then <= 0 {
			continue
		}
		out = append(out, ranked{market: market, ret: closes[len(closes)-1]/then - 1})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ret > out[j].ret })
	for i := range out {
		out[i].rank = i + 1
	}
	return out
}

// idea builds the trade idea for one ranked market.
func (r *RotationStrategy) idea(m ranked, dir domain.Direction, total int, price float64) domain.TradeIdea {
	thesis := fmt.Sprintf("14-day return %+.1f%%, rank %d of %d", m.ret*100, m.rank, total)
	if dir == domain.Short {
		thesis = "Out of the leaders: " + thesis
	}
	return domain.TradeIdea{
		ID:         domain.NewID(),
		StrategyID: RotationID,
		Market:     m.market,
		Direction:  dir,
		Thesis:     thesis,
		SupportingSignals: map[string]any{
			"rank":           m.rank,
			"ranked_markets": total,
			"return_14d":     pyRound(m.ret, 6),
			"current_price":  price,
		},
		Confidence:             CandleConfidence,
		EntryPlan:              fmt.Sprintf("Enter at market while in the top %d by 14-day return, with a positive return", RotationTop),
		ExitPlan:               fmt.Sprintf("Exit when the market drops below rank %d or its 14-day return turns negative, or on the stop-loss", RotationTop+1),
		StopOrInvalidation:     fmt.Sprintf("Abandon if unrealised loss reaches %d%%", stopLossPercent(r.settings.StopLossPct)),
		PositionSizingProposal: calc.PositionSizingProposal,
		ModeEligibility:        allModes(),
	}
}

// Prompt returns the rotation strategy's own prompt for an entry.
func (r *RotationStrategy) Prompt(idea domain.TradeIdea, _ MarketData) llm.StrategyPrompt {
	return llm.StrategyPrompt{
		Name:         fmt.Sprintf("Relative-strength rotation (top %d by 14-day return)", RotationTop),
		Instructions: rotationInstructions,
		Data:         promptData(idea.SupportingSignals),
	}
}
