package strategy

import (
	"fmt"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

// RSI dip parameters, on finished 4-hour candles. The strategy buys a
// capitulation: RSI deeply oversold and the close far below its 20-candle
// EMA. It sells the bounce, when RSI recovers, or after a time limit.
const (
	// RSIDipPeriod is the RSI period in candles.
	RSIDipPeriod = 14
	// RSIDipEntry is the RSI below which a dip may be bought.
	RSIDipEntry = 20.0
	// RSIDipExit is the RSI above which the bounce is sold.
	RSIDipExit = 50.0
	// RSIDipEMA is the EMA the dip is measured from, in candles.
	RSIDipEMA = 20
	// RSIDipMinGap is how far below that EMA the close must be (10 %).
	RSIDipMinGap = 0.10
	// RSIDipMaxHoldCandles ends a trade that has not bounced (3 days).
	RSIDipMaxHoldCandles = 18
	// RSIDipMinClosedCandles is the warm-up before any decision.
	RSIDipMinClosedCandles = 40
)

// rsiDipInstructions is the RSI dip strategy's own prompt to the LLM.
const rsiDipInstructions = "This is a 4-hour capitulation dip buy: the 14-candle RSI of finished 4-hour candles is below 20 " +
	"and the close is at least 10% below its 20-candle EMA. The trade bets on a relief bounce within about three " +
	"days; it is sold when RSI recovers above 50 or after 18 candles. It is a counter-trend trade, so the main risk " +
	"is buying the start of a collapse rather than its end. Use a scale above 1.0 when the sell-off looks like " +
	"forced selling or market-wide panic with no lasting damage to this asset; below 1.0 when the news shows a " +
	"lasting problem specific to it (hack, delisting, insolvency, regulatory action, token unlock); below 0.7 when " +
	"you expect the fall to continue."

// NewRSIDip creates the 4-hour RSI capitulation-dip strategy.
func NewRSIDip(s config.Settings) *CandleStrategy {
	return newCandleStrategy(RSIDipID, "4-hour RSI capitulation dip (RSI14 < 20, 10% under EMA20)",
		rsiDipInstructions, rsiDipRule, s)
}

// NewRSIDipAndLLM creates the RSI dip strategy's LLM version.
func NewRSIDipAndLLM(s config.Settings) *LLMVersion {
	return NewLLMVersion(NewRSIDip(s), RSIDipAndLLMID, s)
}

// rsiDipRule rebuilds the dip-trade state from the candle history and
// returns a long while in a dip trade, else an exit.
func rsiDipRule(closed []domain.Candle) *candleSignal {
	n := len(closed)
	if n < RSIDipMinClosedCandles {
		return nil
	}
	closes := candleCloses(closed)
	rsi, ema := rsiAligned(closes, RSIDipPeriod), emaAligned(closes, RSIDipEMA)
	entry, exit := make([]bool, n), make([]bool, n)
	for i := range closes {
		// NaN comparisons are false, so the warm-up candles never trigger.
		entry[i] = rsi[i] < RSIDipEntry && closes[i] < ema[i]*(1-RSIDipMinGap)
		exit[i] = rsi[i] > RSIDipExit
	}
	state, age := latch(entry, exit, RSIDipMaxHoldCandles)
	gap := closes[n-1]/ema[n-1] - 1
	sig := &candleSignal{
		signals: map[string]any{
			"rsi_4h":         pyRound(rsi[n-1], 2),
			"ema20_4h":       pyRound(ema[n-1], 8),
			"ema20_gap":      pyRound(gap, 6),
			"close_4h":       closes[n-1],
			"dip_age":        age, // candles since the dip was bought, -1 when none
			"closed_candles": n,
		},
		entryPlan: "Enter at market on a 4h close with RSI14 < 20 and 10% under the 20-candle EMA",
		exitPlan:  fmt.Sprintf("Exit when the 4h RSI recovers above %.0f or after %d candles, or on the stop-loss", RSIDipExit, RSIDipMaxHoldCandles),
	}
	if state[n-1] {
		sig.direction = domain.Long
		sig.thesis = fmt.Sprintf("4h capitulation dip %d candles ago: RSI %.1f, close %+.1f%% from EMA20", age, rsi[n-1], gap*100)
	} else {
		sig.direction = domain.Short
		sig.thesis = fmt.Sprintf("No 4h dip trade in force: RSI %.1f, close %+.1f%% from EMA20", rsi[n-1], gap*100)
	}
	return sig
}
