package strategy

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"sync"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/llm"
	"github.com/James-Chapman/LLMTradingBot/internal/observability"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy/calc"
)

// CandleConfidence is the confidence of every 4-hour candle strategy idea.
// It is high for the reason given at TrendConfidence: an exit the learner
// scales below MIN_SIGNAL_CONFIDENCE would leave a position open.
const CandleConfidence = 0.95

// candleSignal is one market's decision by a 4-hour candle rule.
type candleSignal struct {
	direction domain.Direction
	thesis    string
	signals   map[string]any
	entryPlan string
	exitPlan  string
}

// candleRule decides one market from its finished 4-hour candles, oldest
// first; nil means no decision (warm-up, or the rule has no view).
type candleRule func(closed []domain.Candle) *candleSignal

// CandleStrategy is a strategy whose rule reads only finished 4-hour
// candles. Each rule rebuilds any "in a trade" state from the candle history
// on every tick, so the strategy itself holds no state.
type CandleStrategy struct {
	id           string
	name         string // used in the LLM prompt
	instructions string // the strategy's own prompt to the LLM
	rule         candleRule
	settings     config.Settings
	log          *slog.Logger

	mu   sync.Mutex
	memo map[string]memoEntry // market → last decision, reused until a candle closes
}

// memoEntry is a rule's decision for one set of finished candles.
type memoEntry struct {
	key candleKey
	sig *candleSignal
}

// candleKey identifies a set of finished candles: the rule's answer can only
// change when a candle closes (or the window shifts), so ticks in between
// reuse it instead of recomputing hundreds of candles.
type candleKey struct {
	n             int
	first, last   int64
	lastC, lastLo float64
}

// newCandleStrategy builds a 4-hour candle strategy from its rule and prompt.
func newCandleStrategy(id, name, instructions string, rule candleRule, s config.Settings) *CandleStrategy {
	return &CandleStrategy{id: id, name: name, instructions: instructions, rule: rule, settings: s,
		log: observability.Logger(id), memo: map[string]memoEntry{}}
}

// ID returns the strategy id.
func (c *CandleStrategy) ID() string { return c.id }

// UsesLLM is false: the rule needs no news or analyser.
func (c *CandleStrategy) UsesLLM() bool { return false }

// RequiredPriceTicks is 0: the rule waits for its own 4-hour candle warm-up.
func (c *CandleStrategy) RequiredPriceTicks() int { return 0 }

// Evaluate returns at most one idea per market, in market order.
func (c *CandleStrategy) Evaluate(_ context.Context, in Input) []domain.TradeIdea {
	ideas := []domain.TradeIdea{}
	for _, symbol := range sortedMarkets(in.MarketData) {
		data := in.MarketData[symbol]
		sig := c.decide(symbol, closedCandles(data.TrendCandles))
		if sig == nil {
			continue
		}
		signals := make(map[string]any, len(sig.signals)+1)
		for k, v := range sig.signals {
			signals[k] = v
		}
		signals["current_price"] = data.Price
		idea := domain.TradeIdea{
			ID:                     domain.NewID(),
			StrategyID:             c.id,
			Market:                 symbol,
			Direction:              sig.direction,
			Thesis:                 sig.thesis,
			SupportingSignals:      signals,
			Confidence:             CandleConfidence,
			EntryPlan:              sig.entryPlan,
			ExitPlan:               sig.exitPlan,
			StopOrInvalidation:     fmt.Sprintf("Abandon if unrealised loss reaches %d%%", stopLossPercent(c.settings.StopLossPct)),
			PositionSizingProposal: calc.PositionSizingProposal,
			ModeEligibility:        allModes(),
		}
		if !actionable(&idea, in.Portfolio) {
			continue
		}
		ideas = append(ideas, idea)
		c.log.Debug("Generated trade idea", "strategy", c.id, "market", symbol, "direction", string(sig.direction))
	}
	return ideas
}

// decide returns the rule's decision for market's finished candles, reusing
// the previous answer while no new candle has closed.
func (c *CandleStrategy) decide(market string, closed []domain.Candle) *candleSignal {
	if len(closed) == 0 {
		return nil
	}
	last := closed[len(closed)-1]
	key := candleKey{n: len(closed), first: closed[0].T, last: last.T, lastC: last.C, lastLo: last.L}
	c.mu.Lock()
	defer c.mu.Unlock()
	if m, ok := c.memo[market]; ok && m.key == key {
		return m.sig
	}
	sig := c.rule(closed)
	c.memo[market] = memoEntry{key: key, sig: sig}
	return sig
}

// Prompt returns the strategy's own prompt for an entry.
func (c *CandleStrategy) Prompt(idea domain.TradeIdea, _ MarketData) llm.StrategyPrompt {
	return llm.StrategyPrompt{Name: c.name, Instructions: c.instructions, Data: promptData(idea.SupportingSignals)}
}

// actionable reports whether a 4-hour strategy's idea can be acted on, and
// marks every sell as a rule exit: a long only for a market with no long
// held, a sell only for a held long. Repeating "stay in" or "stay out" on
// every tick would only fill the idea log, and a sell with nothing held
// would open a paper short.
func actionable(idea *domain.TradeIdea, p Portfolio) bool {
	held := holdsLong(p, idea.Market)
	if idea.Direction == domain.Short {
		idea.Exit = true
		return held
	}
	return !held
}

// ── 4-hour candle series helpers ────────────────────────────────────────────

// closedCandles drops the newest candle, which is still forming (Kraken's
// OHLC response always ends with it, and the backtest replay reproduces that).
func closedCandles(candles []domain.Candle) []domain.Candle {
	if len(candles) < 2 {
		return nil
	}
	return candles[:len(candles)-1]
}

// candleCloses returns the close of each candle.
func candleCloses(candles []domain.Candle) []float64 {
	out := make([]float64, len(candles))
	for i, c := range candles {
		out[i] = c.C
	}
	return out
}

// nanSeries returns n NaNs: the "not yet available" value of every series.
func nanSeries(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = math.NaN()
	}
	return out
}

// emaAligned returns the EMA of values with one entry per value: NaN until
// the first full period, then seeded with that period's simple average.
// Leading NaNs in values (a series that is itself warming up) are skipped.
func emaAligned(values []float64, period int) []float64 {
	out := nanSeries(len(values))
	start := 0
	for start < len(values) && math.IsNaN(values[start]) {
		start++
	}
	if period <= 0 || len(values)-start < period {
		return out
	}
	k := 2.0 / float64(period+1)
	sum := 0.0
	for _, v := range values[start : start+period] {
		sum += v
	}
	val := sum / float64(period)
	out[start+period-1] = val
	for i := start + period; i < len(values); i++ {
		val = values[i]*k + val*(1-k)
		out[i] = val
	}
	return out
}

// rsiAligned returns Wilder's RSI of closes with one entry per close (NaN
// for the first period closes), as analysis.RSI computes its last value.
func rsiAligned(closes []float64, period int) []float64 {
	out := nanSeries(len(closes))
	if period <= 0 || len(closes) <= period {
		return out
	}
	var gain, loss float64
	for i := 1; i <= period; i++ {
		d := closes[i] - closes[i-1]
		gain += max(d, 0)
		loss += max(-d, 0)
	}
	p := float64(period)
	gain, loss = gain/p, loss/p
	out[period] = rsiValue(gain, loss)
	for i := period + 1; i < len(closes); i++ {
		d := closes[i] - closes[i-1]
		gain = (gain*(p-1) + max(d, 0)) / p
		loss = (loss*(p-1) + max(-d, 0)) / p
		out[i] = rsiValue(gain, loss)
	}
	return out
}

// rsiValue turns average gain and loss into RSI (50 when nothing moved).
func rsiValue(gain, loss float64) float64 {
	if loss == 0 {
		if gain == 0 {
			return 50
		}
		return 100
	}
	return 100 - 100/(1+gain/loss)
}

// bollinger returns the period-candle simple average and population
// standard deviation of closes, one entry per close (NaN before the first
// full period).
func bollinger(closes []float64, period int) (mid, sd []float64) {
	mid, sd = nanSeries(len(closes)), nanSeries(len(closes))
	for i := period - 1; i < len(closes) && period > 0; i++ {
		window := closes[i-period+1 : i+1]
		m := 0.0
		for _, v := range window {
			m += v
		}
		m /= float64(period)
		ss := 0.0
		for _, v := range window {
			ss += (v - m) * (v - m)
		}
		mid[i], sd[i] = m, math.Sqrt(ss/float64(period))
	}
	return mid, sd
}

// quantile returns the q-quantile of values by linear interpolation
// between the order statistics (numpy's default); NaN for no values.
func quantile(values []float64, q float64) float64 {
	if len(values) == 0 {
		return math.NaN()
	}
	s := slices.Clone(values)
	slices.Sort(s)
	pos := q * float64(len(s)-1)
	lo := int(math.Floor(pos))
	hi := min(lo+1, len(s)-1)
	return s[lo] + (s[hi]-s[lo])*(pos-float64(lo))
}

// latch turns per-candle entry and exit conditions into "in a trade" state:
// on from an entry until the next exit. maxHold > 0 also ends a trade after
// that many candles. It returns the state and, for the last candle, how
// many candles ago the current trade began (-1 when not in one).
func latch(entry, exit []bool, maxHold int) (state []bool, age int) {
	state = make([]bool, len(entry))
	in, held := false, 0
	for i := range entry {
		switch {
		case !in && entry[i]:
			in, held = true, 0
		case in:
			held++
			if exit[i] || (maxHold > 0 && held >= maxHold) {
				in = false
			}
		}
		state[i] = in
	}
	if !in {
		return state, -1
	}
	return state, held
}
