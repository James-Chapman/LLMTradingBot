package strategy

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/James-Chapman/LLMTradingBot/internal/analysis"
	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/observability"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy/calc"
)

// Candle windows of the basic strategies.
const (
	// BasicMinClosedCandles is how many finished candles a basic strategy
	// needs before it reads a market: MACD needs 34, plus a margin so the
	// EMAs have settled a little.
	BasicMinClosedCandles = 40
	// basicViewCandles is how many of the newest finished candles the
	// indicators are computed on.
	basicViewCandles = 200
	// candlesPerDay is the number of 4-hour candles in a UTC day.
	candlesPerDay = 6
)

// basicReading is what the basic rules saw on one set of finished candles.
type basicReading struct {
	view        MarketData       // the candle view the rules read
	sig         *indicatorSignal // the entry signal, nil when a gate rejected it
	bearishTurn bool             // momentum fell past the gate, or the net indicator vote is bearish
	bullishTurn bool             // momentum rose past the gate, or the net indicator vote is bullish
	momentum    float64          // candle momentum (decimal); 0 when unknown
	netVotes    int              // net indicator votes for a long
}

// basicKey identifies the candles a reading was made on: the basic
// candles, plus the newest higher-timeframe candle for the 1h versions.
type basicKey struct {
	candles candleKey
	higher  int64
}

// basicAction is what a basic strategy does about one market this tick.
type basicAction int

// Basic strategy actions.
const (
	actNone  basicAction = iota
	actEntry             // open a long
	actShort             // open a paper short (never in the long-only versions)
	actExit              // sell a held long: a rule exit
	actCover             // buy back a held paper short
)

// basicDecision is one market's action with the reading behind it.
type basicDecision struct {
	symbol  string
	data    MarketData // the tick's market data (live price)
	reading basicReading
	action  basicAction
	key     basicKey
}

// basicCore is shared by the eight basic strategies: the original basic
// indicator gates, read on finished 1-hour or 4-hour candles instead of
// price ticks, with a rule exit when the consensus turns.
type basicCore struct {
	id       string
	minutes  int  // candle length: 60 or 240
	longOnly bool // never open a short
	settings config.Settings
	log      *slog.Logger

	// viewOf builds the candle view of a market; false while warming up.
	viewOf func(symbol string, data MarketData) (MarketData, basicKey, bool)
	// read applies the rules to a candle view (stubbed in tests).
	read func(log *slog.Logger, symbol string, view MarketData) basicReading

	mu      sync.Mutex
	memo    map[string]basicMemo // market → reading on the newest candles
	entered map[string]basicKey  // market → candles an entry was last emitted on
}

// basicMemo is a reading cached until a new candle closes.
type basicMemo struct {
	key     basicKey
	reading basicReading
}

// init sets up the core for one strategy id.
func (c *basicCore) init(id string, minutes int, longOnly bool, s config.Settings) {
	c.id, c.minutes, c.longOnly, c.settings = id, minutes, longOnly, s
	c.log = observability.Logger(id)
	c.viewOf = c.candleView
	c.read = readBasic
	c.memo = map[string]basicMemo{}
	c.entered = map[string]basicKey{}
}

// ID returns the strategy id.
func (c *basicCore) ID() string { return c.id }

// RequiredPriceTicks is 0: the strategy waits for its own candle warm-up.
func (c *basicCore) RequiredPriceTicks() int { return 0 }

// interval returns the candle length as shown to people: "1h" or "4h".
func (c *basicCore) interval() string { return fmt.Sprintf("%dh", c.minutes/60) }

// decisions returns the action for every market that has one, in market order.
func (c *basicCore) decisions(in Input) []basicDecision {
	out := []basicDecision{}
	for _, symbol := range sortedMarkets(in.MarketData) {
		data := in.MarketData[symbol]
		view, key, ok := c.viewOf(symbol, data)
		if !ok {
			continue
		}
		r := c.reading(symbol, view, key)
		if action := c.action(symbol, r, key, in.Portfolio); action != actNone {
			out = append(out, basicDecision{symbol: symbol, data: data, reading: r, action: action, key: key})
		}
	}
	return out
}

// reading returns the rules' reading of a market's candles, reusing it
// until a new candle closes.
func (c *basicCore) reading(symbol string, view MarketData, key basicKey) basicReading {
	c.mu.Lock()
	defer c.mu.Unlock()
	if m, ok := c.memo[symbol]; ok && m.key == key {
		return m.reading
	}
	r := c.read(c.log, symbol, view)
	c.memo[symbol] = basicMemo{key: key, reading: r}
	return r
}

// action decides what to do about one market given what is held there:
// sell a held long when the consensus turns bearish, buy back a held short
// when it turns bullish (both repeated every tick until done), otherwise
// follow an entry signal once per candle: an entry rejected by the risk
// engine, or vetoed by the LLM, waits for the next candle instead of being
// re-proposed on every tick. The long-only versions never open a short.
func (c *basicCore) action(symbol string, r basicReading, key basicKey, p Portfolio) basicAction {
	sigIs := func(d domain.Direction) bool { return r.sig != nil && r.sig.Direction == d }
	entry := actNone
	switch side := heldSide(p, symbol); {
	case side > 0:
		if r.bearishTurn || sigIs(domain.Short) {
			return actExit
		}
		return actNone
	case side < 0:
		if r.bullishTurn || sigIs(domain.Long) {
			return actCover
		}
		return actNone
	case sigIs(domain.Long):
		entry = actEntry
	case sigIs(domain.Short) && !c.longOnly:
		entry = actShort
	default:
		return actNone
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if last, ok := c.entered[symbol]; ok && last == key {
		return actNone
	}
	c.entered[symbol] = key
	return entry
}

// entryIdea builds the idea for a new long or paper short.
func (c *basicCore) entryIdea(d basicDecision, entryPlan string) domain.TradeIdea {
	sig := d.reading.sig
	signals := sig.supportingSignals(d.reading.view)
	signals["current_price"] = d.data.Price // the live price, not the last candle's close
	signals["candle_interval"] = c.interval()
	slPct := stopLossPercent(c.settings.StopLossPct)
	return domain.TradeIdea{
		ID:                     domain.NewID(),
		StrategyID:             c.id,
		Market:                 d.symbol,
		Direction:              sig.Direction,
		Thesis:                 strings.Join(append(sig.thesisParts(), c.interval()+" candles"), " | "),
		SupportingSignals:      signals,
		Confidence:             sig.Confidence.Confidence,
		EntryPlan:              entryPlan,
		ExitPlan:               fmt.Sprintf("Exit when momentum or the indicator vote turns against the position on a closed %s candle, or at the %d%% stop-loss", c.interval(), slPct),
		StopOrInvalidation:     fmt.Sprintf("Abandon if unrealised loss reaches %d%%", slPct),
		PositionSizingProposal: calc.CalculatePositionSizing(sig.ATRPct),
		ModeEligibility:        allModes(),
	}
}

// ruleIdea builds the idea that closes a held position: a sell of a held
// long (a rule exit, which skips MIN_SIGNAL_CONFIDENCE) or a buy covering a
// paper short. Its confidence is CandleConfidence for the reason given there.
func (c *basicCore) ruleIdea(d basicDecision) domain.TradeIdea {
	r := d.reading
	dir, turn, exit := domain.Short, "bearish", true
	if d.action == actCover {
		dir, turn, exit = domain.Long, "bullish", false
	}
	return domain.TradeIdea{
		ID:         domain.NewID(),
		StrategyID: c.id,
		Market:     d.symbol,
		Direction:  dir,
		Exit:       exit,
		Thesis: fmt.Sprintf("Consensus turned %s on the closed %s candles: momentum %.2f%%, net indicator votes %+d",
			turn, c.interval(), r.momentum*100, r.netVotes),
		SupportingSignals: map[string]any{
			"current_price":   d.data.Price,
			"candle_interval": c.interval(),
			"momentum":        pyRound(r.momentum, 6),
			"indicator_votes": r.netVotes,
		},
		Confidence:             CandleConfidence,
		EntryPlan:              "Close the position at market",
		ExitPlan:               "Position closed",
		StopOrInvalidation:     fmt.Sprintf("Abandon if unrealised loss reaches %d%%", stopLossPercent(c.settings.StopLossPct)),
		PositionSizingProposal: calc.PositionSizingProposal,
		ModeEligibility:        allModes(),
	}
}

// candleView builds the market data the basic rules read: the newest
// finished candles as the price history, with indicators computed on them.
// The higher timeframe is the 4-hour candles for the 1h versions and whole
// UTC days for the 4h versions. false while too few candles have finished.
func (c *basicCore) candleView(_ string, data MarketData) (MarketData, basicKey, bool) {
	source, higher, higherSeconds := data.HourCandles, closedCandles(data.TrendCandles), 4*3600
	if c.minutes == 240 {
		source, higherSeconds = data.TrendCandles, 86400
		higher = wholeDays(closedCandles(data.TrendCandles))
	}
	closed := lastN(closedCandles(source), basicViewCandles)
	if len(closed) < BasicMinClosedCandles {
		return MarketData{}, basicKey{}, false
	}
	higher = lastN(higher, basicViewCandles)

	closes := candleCloses(closed)
	last := closed[len(closed)-1]
	key := basicKey{candles: candleKey{n: len(closed), first: closed[0].T, last: last.T, lastC: last.C, lastLo: last.L}}
	htf := analysis.Indicators{}
	if len(higher) > 0 {
		key.higher = higher[len(higher)-1].T
		htf = analysis.ComputeAll(candleCloses(higher), higherSeconds, higher)
	}
	indicators := analysis.ComputeAll(closes, c.minutes*60, closed)
	// The 5m/15m price changes are 0 on hour bars; vote on the changes over
	// 10 and 30 candles instead, the same spans relative to the momentum.
	indicators["price_changes"] = analysis.CandleChanges(closes)
	return MarketData{
		Price:           last.C,
		PreviousPrice:   closes[len(closes)-2],
		PriceHistory:    closes,
		Volume:          data.Volume,
		LLMSentiment:    data.LLMSentiment,
		HigherTimeframe: htf,
		Indicators:      indicators,
	}, key, true
}

// readBasic applies the basic rules to a candle view: the shared entry
// gates, and whether the consensus has turned (momentum past the gate, or
// the net indicator vote) in either direction.
func readBasic(log *slog.Logger, symbol string, view MarketData) basicReading {
	r := basicReading{view: view, sig: evaluateIndicatorGates(log, symbol, view)}
	previous := view.PreviousPrice
	if momentum, ok := calc.CalculateMomentum(view.Price, &previous, view.PriceHistory); ok {
		atrPct, _ := view.Indicators.Num("atr_pct")
		passes := calc.EvaluateMomentumGate(momentum, atrPct).Passes
		r.momentum = momentum
		r.bearishTurn = passes && momentum < 0
		r.bullishTurn = passes && momentum > 0
	}
	r.netVotes = calc.VoteIndicators(true, view.Indicators).Votes
	r.bearishTurn = r.bearishTurn || r.netVotes < 0
	r.bullishTurn = r.bullishTurn || r.netVotes > 0
	return r
}

// wholeDays merges finished 4-hour candles into UTC-day candles, keeping
// only days with all six candles.
func wholeDays(fours []domain.Candle) []domain.Candle {
	days := []domain.Candle{}
	for i := 0; i < len(fours); {
		day := fours[i].T / 86400
		j := i
		for j < len(fours) && fours[j].T/86400 == day {
			j++
		}
		if j-i == candlesPerDay {
			days = append(days, mergeCandles(fours[i:j]))
		}
		i = j
	}
	return days
}

// mergeCandles returns one candle spanning candles (oldest first).
func mergeCandles(candles []domain.Candle) domain.Candle {
	first := candles[0]
	out := domain.Candle{T: first.T, O: first.O, H: first.H, L: first.L, C: candles[len(candles)-1].C}
	for _, c := range candles {
		out.H, out.L, out.V = max(out.H, c.H), min(out.L, c.L), out.V+c.V
	}
	return out
}

// lastN returns the newest n candles.
func lastN(candles []domain.Candle, n int) []domain.Candle {
	if len(candles) > n {
		return candles[len(candles)-n:]
	}
	return candles
}

// heldSide returns +1 when a long of market is held, -1 for a short, 0 for none.
func heldSide(p Portfolio, market string) int {
	for _, pos := range p.OpenPositions {
		if pos.Market != market {
			continue
		}
		switch {
		case pos.Size > 0:
			return 1
		case pos.Size < 0:
			return -1
		}
	}
	return 0
}

// BasicStrategy is the indicator-only strategy on finished candles:
// momentum gate, higher-timeframe check, hard filters, indicator vote and
// confidence, with a rule exit when the consensus turns. Four versions:
// 1h or 4h candles, with or without paper shorts.
type BasicStrategy struct {
	basicCore
}

// newBasic returns an indicator-only strategy on candles of minutes length.
func newBasic(id string, minutes int, longOnly bool, s config.Settings) *BasicStrategy {
	b := &BasicStrategy{}
	b.init(id, minutes, longOnly, s)
	return b
}

// NewBasic1h creates basic_strategy_1h (1-hour candles, may short).
func NewBasic1h(s config.Settings) *BasicStrategy { return newBasic(Basic1hID, 60, false, s) }

// NewBasic4h creates basic_strategy_4h (4-hour candles, may short).
func NewBasic4h(s config.Settings) *BasicStrategy { return newBasic(Basic4hID, 240, false, s) }

// NewBasicLong1h creates basic_strategy_long_1h (1-hour candles, long only).
func NewBasicLong1h(s config.Settings) *BasicStrategy { return newBasic(BasicLong1hID, 60, true, s) }

// NewBasicLong4h creates basic_strategy_long_4h (4-hour candles, long only).
func NewBasicLong4h(s config.Settings) *BasicStrategy { return newBasic(BasicLong4hID, 240, true, s) }

// UsesLLM is false: the basic strategy needs no news or analyser.
func (b *BasicStrategy) UsesLLM() bool { return false }

// Evaluate returns at most one idea per market, in market order.
func (b *BasicStrategy) Evaluate(_ context.Context, in Input) []domain.TradeIdea {
	ideas := []domain.TradeIdea{}
	for _, d := range b.decisions(in) {
		var idea domain.TradeIdea
		if d.action == actEntry || d.action == actShort {
			idea = b.entryIdea(d, fmt.Sprintf("Enter at market on indicator consensus on the closed %s candles", b.interval()))
		} else {
			idea = b.ruleIdea(d)
		}
		ideas = append(ideas, idea)
		b.log.Debug("Generated trade idea", "strategy", b.id, "market", d.symbol,
			"direction", string(idea.Direction), "exit", idea.Exit, "confidence", pyRound(idea.Confidence, 3))
	}
	return ideas
}
