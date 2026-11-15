package strategy

import (
	"fmt"
	"math"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

// Donchian breakout parameters, in finished 4-hour candles. The strategy
// buys a close above the highest high of the entry channel and sells a close
// below the lowest low of the shorter exit channel (turtle-style).
const (
	// DonchianEntryCandles is the entry channel: 90 candles (15 days).
	DonchianEntryCandles = 90
	// DonchianExitCandles is the exit channel: 30 candles (5 days).
	DonchianExitCandles = 30
)

// donchianInstructions is the Donchian strategy's own prompt to the LLM.
const donchianInstructions = "This is a 4-hour Donchian channel breakout: the last finished 4-hour candle closed above " +
	"the highest high of the previous 90 candles (15 days). Breakouts are bought because a few of them start " +
	"large trends; many fail and fall back into the range, and the position is sold when a close breaks the " +
	"lowest low of the last 30 candles (5 days). Judge whether this breakout is likely to follow through over " +
	"days to weeks. Use a scale above 1.0 when the news or briefing gives a lasting reason for the move (adoption, " +
	"listings, fund flows, regulation); below 1.0 when it looks like a one-off spike on a headline, a short squeeze " +
	"or a thin market likely to fall back; below 0.7 only when you expect the breakout to fail."

// NewDonchian creates the 4-hour Donchian breakout strategy.
func NewDonchian(s config.Settings) *CandleStrategy {
	return newCandleStrategy(DonchianID, "4-hour Donchian channel breakout (90-candle entry, 30-candle exit)",
		donchianInstructions, donchianRule, s)
}

// NewDonchianAndLLM creates the Donchian strategy's LLM version.
func NewDonchianAndLLM(s config.Settings) *LLMVersion {
	return NewLLMVersion(NewDonchian(s), DonchianAndLLMID, s)
}

// donchianRule rebuilds the breakout state from the candle history and
// returns a long while in a breakout trade, else an exit.
func donchianRule(closed []domain.Candle) *candleSignal {
	n := len(closed)
	if n <= DonchianEntryCandles {
		return nil
	}
	entry, exit := make([]bool, n), make([]bool, n)
	var high, low float64
	for i := DonchianEntryCandles; i < n; i++ {
		high, low = channel(closed[i-DonchianEntryCandles:i]), lowest(closed[i-DonchianExitCandles:i])
		entry[i], exit[i] = closed[i].C > high, closed[i].C < low
	}
	state, age := latch(entry, exit, 0)
	last := closed[n-1].C
	sig := &candleSignal{
		signals: map[string]any{
			"channel_high":   pyRound(high, 8),
			"exit_level":     pyRound(low, 8),
			"close_4h":       last,
			"breakout_age":   age, // candles since the breakout, -1 when none
			"closed_candles": n,
		},
		entryPlan: "Enter at market after a 4h close above the 90-candle high",
		exitPlan:  "Exit on a 4h close below the 30-candle low, or on the stop-loss / trailing stop",
	}
	if state[n-1] {
		sig.direction = domain.Long
		sig.thesis = fmt.Sprintf("4h breakout %d candles ago; close %s, exit below %s", age, fmtPrice(last), fmtPrice(low))
	} else {
		sig.direction = domain.Short
		sig.thesis = fmt.Sprintf("No 4h breakout in force; close %s, 90-candle high %s", fmtPrice(last), fmtPrice(high))
	}
	return sig
}

// channel returns the highest high of candles.
func channel(candles []domain.Candle) float64 {
	h := math.Inf(-1)
	for _, c := range candles {
		h = max(h, c.H)
	}
	return h
}

// lowest returns the lowest low of candles.
func lowest(candles []domain.Candle) float64 {
	l := math.Inf(1)
	for _, c := range candles {
		l = min(l, c.L)
	}
	return l
}
