package strategy

import (
	"fmt"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

// Bollinger squeeze parameters, in finished 4-hour candles. The strategy
// waits for the bands to narrow to one of their tightest widths of the last
// 20 days, then buys a close above the upper band in an uptrend, and sells a
// close back under the middle band.
const (
	// SqueezeBandCandles is the Bollinger period (about 7 days).
	SqueezeBandCandles = 40
	// SqueezeBandWidth is the band distance in standard deviations.
	SqueezeBandWidth = 2.0
	// SqueezeLookback is the history the band width is ranked against.
	SqueezeLookback = 120
	// SqueezeQuantile: a width at or below this quantile of the lookback is a squeeze.
	SqueezeQuantile = 0.2
	// SqueezeRecent is how many candles before the breakout the squeeze may have been.
	SqueezeRecent = 6
	// SqueezeTrendEMA is the uptrend filter: the breakout close must be above this EMA.
	SqueezeTrendEMA = 100
	// SqueezeMinClosedCandles is the warm-up before any decision.
	SqueezeMinClosedCandles = SqueezeBandCandles + SqueezeLookback
)

// squeezeInstructions is the Bollinger squeeze strategy's own prompt to the LLM.
const squeezeInstructions = "This is a 4-hour Bollinger squeeze breakout: the 40-candle bands narrowed to one of their " +
	"tightest widths of the last 20 days, and the last finished candle closed above the upper band while price is " +
	"above its 100-candle EMA. Quiet periods often end in a strong move; the trade bets that this one is upward. " +
	"It is sold on a close back below the middle band, so false breakouts are cut quickly. Use a scale above 1.0 " +
	"when the news or briefing gives a reason for an upside expansion (a catalyst, fund flows, a broad rally); " +
	"below 1.0 when the breakout looks like noise in a thin market or the news points the other way; below 0.7 only " +
	"when you expect the move to reverse."

// NewSqueeze creates the 4-hour Bollinger squeeze breakout strategy.
func NewSqueeze(s config.Settings) *CandleStrategy {
	return newCandleStrategy(SqueezeID, "4-hour Bollinger squeeze breakout (40-candle bands, 2 SD)",
		squeezeInstructions, squeezeRule, s)
}

// NewSqueezeAndLLM creates the squeeze strategy's LLM version.
func NewSqueezeAndLLM(s config.Settings) *LLMVersion {
	return NewLLMVersion(NewSqueeze(s), SqueezeAndLLMID, s)
}

// squeezeRule rebuilds the breakout state from the candle history and
// returns a long while in a squeeze breakout trade, else an exit.
func squeezeRule(closed []domain.Candle) *candleSignal {
	n := len(closed)
	if n < SqueezeMinClosedCandles {
		return nil
	}
	closes := candleCloses(closed)
	mid, sd := bollinger(closes, SqueezeBandCandles)
	trend := emaAligned(closes, SqueezeTrendEMA)
	width := nanSeries(n)
	for i := range closes {
		width[i] = 2 * SqueezeBandWidth * sd[i] / mid[i]
	}
	squeeze := make([]bool, n)
	for i := SqueezeBandCandles - 1 + SqueezeLookback - 1; i < n; i++ {
		squeeze[i] = width[i] <= quantile(width[i-SqueezeLookback+1:i+1], SqueezeQuantile)
	}
	entry, exit := make([]bool, n), make([]bool, n)
	for i := range closes {
		recent := false
		for j := max(0, i-SqueezeRecent); j < i; j++ {
			recent = recent || squeeze[j]
		}
		entry[i] = recent && closes[i] > mid[i]+SqueezeBandWidth*sd[i] && closes[i] > trend[i]
		exit[i] = closes[i] < mid[i]
	}
	state, age := latch(entry, exit, 0)
	upper := mid[n-1] + SqueezeBandWidth*sd[n-1]
	sig := &candleSignal{
		signals: map[string]any{
			"upper_band":     pyRound(upper, 8),
			"middle_band":    pyRound(mid[n-1], 8),
			"bandwidth_pct":  pyRound(width[n-1]*100, 3),
			"ema100_4h":      pyRound(trend[n-1], 8),
			"close_4h":       closes[n-1],
			"breakout_age":   age, // candles since the breakout, -1 when none
			"closed_candles": n,
		},
		entryPlan: "Enter at market on a 4h close above the upper band soon after a squeeze, above the 100-candle EMA",
		exitPlan:  "Exit on a 4h close below the middle band, or on the stop-loss / trailing stop",
	}
	if state[n-1] {
		sig.direction = domain.Long
		sig.thesis = fmt.Sprintf("4h squeeze breakout %d candles ago; close %s, exit below %s", age, fmtPrice(closes[n-1]), fmtPrice(mid[n-1]))
	} else {
		sig.direction = domain.Short
		sig.thesis = fmt.Sprintf("No 4h squeeze breakout in force; band width %.2f%%", width[n-1]*100)
	}
	return sig
}
