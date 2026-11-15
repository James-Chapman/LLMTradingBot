package strategy

import (
	"fmt"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

// MACD momentum parameters, in finished 4-hour candles: the classic 12/26/9
// stretched three times, so it follows multi-week momentum rather than the
// daily noise the 80 bp fee makes too expensive to trade.
const (
	// MACDFast is the fast EMA period (6 days).
	MACDFast = 36
	// MACDSlow is the slow EMA period (13 days).
	MACDSlow = 78
	// MACDSignal is the signal-line EMA period of the MACD line.
	MACDSignal = 27
	// MACDMinClosedCandles is the warm-up before any decision.
	MACDMinClosedCandles = MACDSlow + MACDSignal + 10
)

// macdInstructions is the MACD strategy's own prompt to the LLM.
const macdInstructions = "This is a slow 4-hour MACD momentum entry (EMA 36/78, signal 27, about a 13-day horizon): " +
	"the MACD line is above zero and above its signal line, so medium-term momentum is up and still strengthening. " +
	"The position is held while the histogram stays positive, typically for one to three weeks, and is sold when it " +
	"turns negative. Judge whether the momentum is likely to persist. Use a scale above 1.0 when the news or briefing " +
	"supports continued buying (fund flows, adoption, a broad market rally); below 1.0 when the rise looks late, " +
	"stretched or driven by a headline likely to fade, or when a known risk event is close; below 0.7 only when you " +
	"expect momentum to reverse soon."

// NewMACD creates the slow 4-hour MACD momentum strategy.
func NewMACD(s config.Settings) *CandleStrategy {
	return newCandleStrategy(MACDID, "4-hour MACD momentum (36/78/27)", macdInstructions, macdRule, s)
}

// NewMACDAndLLM creates the MACD strategy's LLM version.
func NewMACDAndLLM(s config.Settings) *LLMVersion {
	return NewLLMVersion(NewMACD(s), MACDAndLLMID, s)
}

// macdRule returns a long while the MACD line is above zero and above its
// signal line, an exit while it is below the signal line, and no idea when
// it is above the signal line but still below zero.
func macdRule(closed []domain.Candle) *candleSignal {
	n := len(closed)
	if n < MACDMinClosedCandles {
		return nil
	}
	closes := candleCloses(closed)
	fast, slow := emaAligned(closes, MACDFast), emaAligned(closes, MACDSlow)
	line := make([]float64, n)
	for i := range line {
		line[i] = fast[i] - slow[i] // NaN until the slow EMA exists
	}
	signal := emaAligned(line, MACDSignal)
	m, h := line[n-1], line[n-1]-signal[n-1]
	sig := &candleSignal{
		signals: map[string]any{
			"macd_4h":        pyRound(m, 8),
			"signal_4h":      pyRound(signal[n-1], 8),
			"histogram_4h":   pyRound(h, 8),
			"macd_pct":       pyRound(m/closes[n-1], 6), // the MACD line as a share of the price
			"close_4h":       closes[n-1],
			"closed_candles": n,
		},
		entryPlan: "Enter at market while the 4h MACD line is above zero and above its signal line",
		exitPlan:  "Exit when the 4h MACD histogram turns negative, or on the stop-loss / trailing stop",
	}
	switch {
	case h > 0 && m > 0:
		sig.direction = domain.Long
		sig.thesis = fmt.Sprintf("4h MACD %s above zero and %s above its signal", fmtPrice(m), fmtPrice(h))
	case h < 0:
		sig.direction = domain.Short
		sig.thesis = fmt.Sprintf("4h MACD histogram negative (%s)", fmtPrice(h))
	default:
		return nil
	}
	return sig
}
