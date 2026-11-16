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
	bearishTurn bool             // momentum fell past the gate, or the net vote is BASIC_EXIT_VOTES or more against a long
	bullishTurn bool             // momentum rose past the gate, or the net vote is BASIC_EXIT_VOTES or more for a long
	momentum    float64          // candle momentum (decimal); 0 when unknown
	netVotes    int              // net indicator votes for a long
	trend       int              // +1 above the higher-timeframe trend EMA, -1 below, 0 unknown or off
	bearRun     int              // closed candles in a row, up to this one, with a bearish turn
	bullRun     int              // closed candles in a row, up to this one, with a bullish turn
	freshSignal bool             // the entry signal is strong enough and was not on the previous candle
}

// turnRun follows a market from one closed candle to the next: the closed
// candles in a row on which the consensus has turned, and the direction of
// a strong-enough entry signal (+1, -1, 0); prev* are the values before the
// candle at candle.
type turnRun struct {
	candle             int64
	bear, bull         int
	prevBear, prevBull int
	sig, prevSig       int
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
	note    string // why a position is closed when it is not the consensus turn (the trailing stop)
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

	mu       sync.Mutex
	memo     map[string]basicMemo // market → reading on the newest candles
	entered  map[string]basicKey  // market → candles an entry was last emitted on
	runs     map[string]turnRun   // market → consecutive turned candles
	sides    map[string]int       // market → side held on the last evaluation (+1, -1, 0)
	closedAt map[string]int64     // market → newest closed candle when its position was last seen closed
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
	c.read = func(log *slog.Logger, symbol string, view MarketData) basicReading {
		return readBasic(log, symbol, view, c.settings.BasicExitVotes)
	}
	c.memo = map[string]basicMemo{}
	c.entered = map[string]basicKey{}
	c.runs = map[string]turnRun{}
	c.sides = map[string]int{}
	c.closedAt = map[string]int64{}
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
		if action, note := c.action(symbol, r, key, in.Portfolio, data.Price); action != actNone {
			out = append(out, basicDecision{symbol: symbol, data: data, reading: r, action: action, key: key, note: note})
		}
	}
	return out
}

// reading returns the rules' reading of a market's candles, reusing it
// until a new candle closes, with the count of closed candles in a row on
// which the consensus has turned. A candle is counted once, however often
// it is read.
func (c *basicCore) reading(symbol string, view MarketData, key basicKey) basicReading {
	c.mu.Lock()
	defer c.mu.Unlock()
	if m, ok := c.memo[symbol]; ok && m.key == key {
		return m.reading
	}
	r := c.read(c.log, symbol, view)
	run := c.runs[symbol]
	if run.candle != key.candles.last {
		// A new closed candle: the values so far become the base it adds to.
		run.candle, run.prevBear, run.prevBull, run.prevSig = key.candles.last, run.bear, run.bull, run.sig
	}
	run.bear, run.bull = extendRun(run.prevBear, r.bearishTurn), extendRun(run.prevBull, r.bullishTurn)
	run.sig = c.strongSignal(r)
	c.runs[symbol] = run
	r.bearRun, r.bullRun = run.bear, run.bull
	r.freshSignal = run.sig != 0 && run.sig != run.prevSig
	c.memo[symbol] = basicMemo{key: key, reading: r}
	return r
}

// strongSignal returns the direction of the reading's entry signal (+1 long,
// -1 short) when it has at least BASIC_ENTRY_VOTES net votes, else 0.
func (c *basicCore) strongSignal(r basicReading) int {
	if r.sig == nil || r.sig.Votes.Votes < max(1, c.settings.BasicEntryVotes) {
		return 0
	}
	if r.sig.Direction == domain.Long {
		return 1
	}
	return -1
}

// extendRun returns the run length after one more candle.
func extendRun(before int, turned bool) int {
	if turned {
		return before + 1
	}
	return 0
}

// action decides what to do about one market given what is held there:
// close a held position when its trailing stop is hit, when the consensus
// has turned against it on BASIC_EXIT_CONFIRM_CANDLES closed candles in a
// row, or on an opposite entry signal (all repeated every tick until done);
// otherwise follow an entry signal once per candle, unless it goes against
// the higher-timeframe trend or the market is cooling down after a close.
// An entry rejected by the risk engine, or vetoed by the LLM, waits for the
// next candle instead of being re-proposed on every tick. The long-only
// versions never open a short. The note says why a trailing stop closed it.
func (c *basicCore) action(symbol string, r basicReading, key basicKey, p Portfolio, price float64) (basicAction, string) {
	sigIs := func(d domain.Direction) bool { return r.sig != nil && r.sig.Direction == d }
	pos, side := heldPosition(p, symbol)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sides[symbol] != 0 && side == 0 {
		c.closedAt[symbol] = key.candles.last
	}
	c.sides[symbol] = side

	confirm := max(1, c.settings.BasicExitConfirmCandles)
	entry := actNone
	switch {
	case side > 0:
		if note := c.priceExit(r, pos, price); note != "" {
			return actExit, note
		}
		if r.bearRun >= confirm || sigIs(domain.Short) {
			return actExit, ""
		}
		return actNone, ""
	case side < 0:
		if note := c.priceExit(r, pos, price); note != "" {
			return actCover, note
		}
		if r.bullRun >= confirm || sigIs(domain.Long) {
			return actCover, ""
		}
		return actNone, ""
	case sigIs(domain.Long):
		entry = actEntry
	case sigIs(domain.Short) && !c.longOnly:
		entry = actShort
	default:
		return actNone, ""
	}
	if !c.entryQualifies(r) || c.againstTrend(entry, r) || c.coolingDown(symbol, key) {
		return actNone, ""
	}
	if last, ok := c.entered[symbol]; ok && last == key {
		return actNone, ""
	}
	c.entered[symbol] = key
	return entry, ""
}

// entryQualifies reports whether the reading's entry signal passes the
// stricter entry rules: at least BASIC_ENTRY_VOTES net votes, new on this
// candle when BASIC_FRESH_SIGNAL is on, and a candle ATR of at least
// BASIC_MIN_ATR_COST times FEE_AND_SLIPPAGE.
func (c *basicCore) entryQualifies(r basicReading) bool {
	s := c.settings
	if c.strongSignal(r) == 0 || (s.BasicFreshSignal && !r.freshSignal) {
		return false
	}
	// atr_pct is in percent; FEE_AND_SLIPPAGE is a fraction.
	return s.BasicMinATRCost <= 0 || r.sig.ATRPct/100 >= s.BasicMinATRCost*s.FeeAndSlippage
}

// againstTrend reports whether the trend filter is on and entry goes
// against the higher-timeframe trend. An unknown trend blocks nothing.
func (c *basicCore) againstTrend(entry basicAction, r basicReading) bool {
	if c.settings.BasicTrendEMA <= 0 {
		return false
	}
	return (entry == actEntry && r.trend < 0) || (entry == actShort && r.trend > 0)
}

// coolingDown reports whether fewer than BASIC_COOLDOWN_CANDLES candles have
// closed since a position in the market was last seen closed. The caller
// holds c.mu.
func (c *basicCore) coolingDown(symbol string, key basicKey) bool {
	n := c.settings.BasicCooldownCandles
	closed, ok := c.closedAt[symbol]
	if n <= 0 || !ok {
		return false
	}
	return key.candles.last < closed+int64(n*c.minutes*60)
}

// priceExit returns why the held position pos should close on the live price
// alone (its trailing stop or its profit target), or "" when neither is hit.
func (c *basicCore) priceExit(r basicReading, pos domain.PositionRecord, price float64) string {
	atr, ok := candleATR(r.view)
	if !ok || price <= 0 {
		return ""
	}
	if note := c.trailingStop(r, pos, price, atr); note != "" {
		return note
	}
	return c.profitTarget(pos, price, atr)
}

// candleATR returns the candle ATR of a view in price units.
func candleATR(view MarketData) (float64, bool) {
	atrPct, ok := view.Indicators.Num("atr_pct")
	if !ok || atrPct <= 0 || view.Price <= 0 {
		return 0, false
	}
	return atrPct / 100 * view.Price, true
}

// profitTarget returns why the held position pos should close when the live
// price is BASIC_TAKE_PROFIT_ATR candle ATRs in profit from the entry price,
// or "" while it is not or the target is off. The ATR is the current one.
func (c *basicCore) profitTarget(pos domain.PositionRecord, price, atr float64) string {
	mult := c.settings.BasicTakeProfitATR
	switch {
	case mult <= 0:
	case pos.Size > 0 && price >= pos.AvgPrice+mult*atr:
		return fmt.Sprintf("Profit target: price %.6g is %.1f ATR (%.6g) or more above the entry, %.6g", price, mult, atr, pos.AvgPrice)
	case pos.Size < 0 && price <= pos.AvgPrice-mult*atr:
		return fmt.Sprintf("Profit target: price %.6g is %.1f ATR (%.6g) or more below the entry, %.6g", price, mult, atr, pos.AvgPrice)
	}
	return ""
}

// trailingStop returns why the held position pos should close when the live
// price has given back BASIC_TRAIL_ATR candle ATRs (atr) from its best price
// since entry (the entry price, the live price and the extremes of the
// candles that began after it opened), or "" while it has not or it is off.
func (c *basicCore) trailingStop(r basicReading, pos domain.PositionRecord, price, atr float64) string {
	mult := c.settings.BasicTrailATR
	if mult <= 0 {
		return ""
	}
	opened := pos.Timestamp.Unix()
	best := max(pos.AvgPrice, price)
	if pos.Size < 0 {
		best = min(pos.AvgPrice, price)
	}
	for _, candle := range c.viewCandles(r.view) {
		switch {
		case candle.T < opened:
		case pos.Size > 0:
			best = max(best, candle.H)
		default:
			best = min(best, candle.L)
		}
	}
	if pos.Size > 0 && price <= best-mult*atr {
		return fmt.Sprintf("Trailing stop: price %.6g is more than %.1f ATR (%.6g) below the high since entry, %.6g", price, mult, atr, best)
	}
	if pos.Size < 0 && price >= best+mult*atr {
		return fmt.Sprintf("Trailing stop: price %.6g is more than %.1f ATR (%.6g) above the low since entry, %.6g", price, mult, atr, best)
	}
	return ""
}

// viewCandles returns the finished candles a candle view was built from.
func (c *basicCore) viewCandles(view MarketData) []domain.Candle {
	if c.minutes == 240 {
		return view.TrendCandles
	}
	return view.HourCandles
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
		ExitPlan:               c.exitPlan(slPct),
		StopOrInvalidation:     fmt.Sprintf("Abandon if unrealised loss reaches %d%%", slPct),
		PositionSizingProposal: calc.CalculatePositionSizing(sig.ATRPct),
		ModeEligibility:        allModes(),
	}
}

// exitPlan describes how an entry will be closed under the current settings.
func (c *basicCore) exitPlan(slPct int) string {
	s := c.settings
	plan := fmt.Sprintf("Exit when momentum or the indicator vote (%d against) turns against the position on %d closed %s candle(s) in a row",
		max(1, s.BasicExitVotes), max(1, s.BasicExitConfirmCandles), c.interval())
	if s.BasicTrailATR > 0 {
		plan += fmt.Sprintf(", when the price gives back %.1f ATR from its best since entry", s.BasicTrailATR)
	}
	return plan + fmt.Sprintf(", or at the %d%% stop-loss", slPct)
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
	thesis := d.note
	if thesis == "" {
		thesis = fmt.Sprintf("Consensus turned %s on the closed %s candles: momentum %.2f%%, net indicator votes %+d",
			turn, c.interval(), r.momentum*100, r.netVotes)
	}
	return domain.TradeIdea{
		ID:         domain.NewID(),
		StrategyID: c.id,
		Market:     d.symbol,
		Direction:  dir,
		Exit:       exit,
		Thesis:     thesis,
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
		if period := c.settings.BasicTrendEMA; period > 0 {
			// The slow trend line entries must agree with; absent until the
			// higher timeframe has period candles.
			if ema, ok := analysis.EMA(candleCloses(higher), period); ok {
				htf["trend_ema"] = ema
			}
		}
	}
	indicators := analysis.ComputeAll(closes, c.minutes*60, closed)
	// The 5m/15m price changes are 0 on hour bars; vote on the changes over
	// 10 and 30 candles instead, the same spans relative to the momentum.
	indicators["price_changes"] = analysis.CandleChanges(closes)
	view := MarketData{
		Price:           last.C,
		PreviousPrice:   closes[len(closes)-2],
		PriceHistory:    closes,
		Volume:          data.Volume,
		LLMSentiment:    data.LLMSentiment,
		HigherTimeframe: htf,
		Indicators:      indicators,
	}
	// The finished candles go with the view for the trailing stop's extremes.
	if c.minutes == 240 {
		view.TrendCandles = closed
	} else {
		view.HourCandles = closed
	}
	return view, key, true
}

// readBasic applies the basic rules to a candle view: the shared entry
// gates, whether the consensus has turned in either direction (momentum
// past the gate, or a net indicator vote of exitVotes or more), and the
// price against the higher-timeframe trend EMA.
func readBasic(log *slog.Logger, symbol string, view MarketData, exitVotes int) basicReading {
	r := basicReading{view: view, sig: evaluateIndicatorGates(log, symbol, view)}
	momentumTurn := 0
	previous := view.PreviousPrice
	if momentum, ok := calc.CalculateMomentum(view.Price, &previous, view.PriceHistory); ok {
		atrPct, _ := view.Indicators.Num("atr_pct")
		r.momentum = momentum
		if calc.EvaluateMomentumGate(momentum, atrPct).Passes {
			momentumTurn = sign(momentum)
		}
	}
	r.netVotes = calc.VoteIndicators(true, view.Indicators).Votes
	r.bearishTurn, r.bullishTurn = consensusTurns(momentumTurn, r.netVotes, exitVotes)
	if ema, ok := view.HigherTimeframe.Num("trend_ema"); ok && ema > 0 && view.Price > 0 {
		r.trend = sign(view.Price - ema)
	}
	return r
}

// consensusTurns reports whether the consensus reads bearish or bullish:
// momentum past its gate in that direction (momentumTurn -1 or +1), or a
// net indicator vote of at least exitVotes (at least 1) that way.
func consensusTurns(momentumTurn, netVotes, exitVotes int) (bearish, bullish bool) {
	need := max(1, exitVotes)
	return momentumTurn < 0 || netVotes <= -need, momentumTurn > 0 || netVotes >= need
}

// sign returns -1, 0 or +1 by the sign of v.
func sign(v float64) int {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	}
	return 0
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

// heldPosition returns the first open position in market with its side:
// +1 for a long, -1 for a short, 0 (and an empty record) for none.
func heldPosition(p Portfolio, market string) (domain.PositionRecord, int) {
	for _, pos := range p.OpenPositions {
		if pos.Market != market {
			continue
		}
		switch {
		case pos.Size > 0:
			return pos, 1
		case pos.Size < 0:
			return pos, -1
		}
	}
	return domain.PositionRecord{}, 0
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
