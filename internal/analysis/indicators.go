package analysis

import (
	"math"
	"strconv"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

// Default indicator periods used by ComputeAll (mirrors the Python defaults).
const (
	RSIPeriod        = 14
	EMAFastPeriod    = 9
	EMASlowPeriod    = 21
	BBPeriod         = 20
	MACDFastPeriod   = 12
	MACDSlowPeriod   = 26
	MACDSignalPeriod = 9
	ATRPeriod        = 14
	StochKPeriod     = 14
	StochDPeriod     = 3
	WilliamsRPeriod  = 14
	DefaultTickSecs  = 30
)

// ── Primitives ────────────────────────────────────────────────────────────────

// roundTo rounds x to the given decimal places exactly like Python's round():
// correctly rounded on the exact binary value, with ties going to even.
func roundTo(x float64, places int) float64 {
	rounded, err := strconv.ParseFloat(strconv.FormatFloat(x, 'f', places, 64), 64)
	if err != nil {
		return x
	}
	return rounded
}

// sum returns the sequential sum of values.
func sum(values []float64) float64 {
	total := 0.0
	for _, v := range values {
		total += v
	}
	return total
}

// mean returns the arithmetic mean of a non-empty slice.
func mean(values []float64) float64 {
	return sum(values) / float64(len(values))
}

// minMax returns the smallest and largest values of a non-empty slice.
func minMax(values []float64) (float64, float64) {
	lo, hi := values[0], values[0]
	for _, v := range values[1:] {
		lo = math.Min(lo, v)
		hi = math.Max(hi, v)
	}
	return lo, hi
}

// EMA returns the exponential moving average seeded with the SMA of the first window.
func EMA(prices []float64, period int) (float64, bool) {
	series := EMASeries(prices, period)
	if len(series) == 0 {
		return 0, false
	}
	return series[len(series)-1], true
}

// EMASeries returns the full EMA series, seeded with the SMA of the first window.
// Its length is max(0, len(prices)-period+1); element 0 corresponds to prices[period-1].
func EMASeries(prices []float64, period int) []float64 {
	if period <= 0 || len(prices) < period {
		return nil
	}
	k := 2.0 / float64(period+1)
	val := sum(prices[:period]) / float64(period) // SMA seed
	result := []float64{val}
	for _, p := range prices[period:] {
		val = p*k + val*(1.0-k)
		result = append(result, val)
	}
	return result
}

// ── Exported indicators ───────────────────────────────────────────────────────

// RSI returns Wilder's RSI using SMMA smoothing over the full price history.
// It is unavailable when there are fewer than period+1 prices.
func RSI(prices []float64, period int) (float64, bool) {
	if period <= 0 || len(prices) < period+1 {
		return 0, false
	}

	gains := make([]float64, len(prices)-1)
	losses := make([]float64, len(prices)-1)
	for i := 1; i < len(prices); i++ {
		change := prices[i] - prices[i-1]
		if change > 0 {
			gains[i-1] = change
		} else if change < 0 {
			losses[i-1] = -change
		}
	}

	// Seed with the SMA of the first `period` changes, then apply Wilder smoothing.
	avgGain := mean(gains[:period])
	avgLoss := mean(losses[:period])
	p := float64(period)
	for i := period; i < len(gains); i++ {
		avgGain = (avgGain*(p-1) + gains[i]) / p
		avgLoss = (avgLoss*(p-1) + losses[i]) / p
	}

	if avgLoss == 0 {
		// No losses: overbought if there were gains, neutral if nothing moved.
		if avgGain == 0 {
			return 50.0, true
		}
		return 100.0, true
	}
	rs := avgGain / avgLoss
	return roundTo(100.0-100.0/(1.0+rs), 1), true
}

// BollingerBands returns 2-std bands as a nested map, or nil with too little data.
func BollingerBands(prices []float64, period int) map[string]any {
	if period <= 0 || len(prices) < period {
		return nil
	}
	window := prices[len(prices)-period:]
	avg := mean(window)
	variance := 0.0
	for _, p := range window {
		variance += (p - avg) * (p - avg)
	}
	std := math.Sqrt(variance / float64(period)) // population std, like numpy

	upper := avg + 2*std
	lower := avg - 2*std
	current := prices[len(prices)-1]
	position := 0.5
	if upper != lower {
		position = (current - lower) / (upper - lower)
	}
	widthPct := 0.0
	if avg > 0 {
		widthPct = (upper - lower) / avg * 100
	}
	return map[string]any{
		"upper":     roundTo(upper, 2),
		"middle":    roundTo(avg, 2),
		"lower":     roundTo(lower, 2),
		"position":  roundTo(math.Max(0.0, math.Min(1.0, position))*100, 1), // 0–100 %
		"width_pct": roundTo(widthPct, 2),
	}
}

// directionLabel returns "bullish", "bearish" or "neutral" for a vs b.
func directionLabel(a, b float64) string {
	if a > b {
		return "bullish"
	}
	if a < b {
		return "bearish"
	}
	return "neutral"
}

// MACD returns the MACD line, signal, histogram and biases, or nil with too little data.
// It needs at least slow+signal-1 prices.
func MACD(prices []float64, fast, slow, signal int) map[string]any {
	if len(prices) < slow+signal-1 {
		return nil
	}

	fastSeries := EMASeries(prices, fast) // len = N - fast + 1
	slowSeries := EMASeries(prices, slow) // len = N - slow + 1

	// Align: fastSeries[slow-fast] is the same bar as slowSeries[0].
	offset := slow - fast
	if offset < 0 || len(fastSeries) <= offset {
		return nil
	}
	aligned := fastSeries[offset:]
	n := min(len(aligned), len(slowSeries))
	lineSeries := make([]float64, n)
	for i := 0; i < n; i++ {
		lineSeries[i] = aligned[i] - slowSeries[i]
	}

	if len(lineSeries) < signal {
		return nil
	}
	signalSeries := EMASeries(lineSeries, signal)
	if len(signalSeries) == 0 {
		return nil
	}

	lineVal := lineSeries[len(lineSeries)-1]
	signalVal := signalSeries[len(signalSeries)-1]
	return map[string]any{
		"line":        roundTo(lineVal, 4),
		"signal":      roundTo(signalVal, 4),
		"histogram":   roundTo(lineVal-signalVal, 4),
		"bias":        directionLabel(lineVal, 0),
		"signal_bias": directionLabel(lineVal, signalVal),
	}
}

// ATR returns the Average True Range approximated from close-to-close moves,
// rounded to 6dp. It is unavailable when there are fewer than period+1 prices.
func ATR(prices []float64, period int) (float64, bool) {
	atr, ok := atrClose(prices, period)
	return roundTo(atr, 6), ok
}

// atrClose returns the unrounded close-to-close ATR used by ATR.
func atrClose(prices []float64, period int) (float64, bool) {
	if period <= 0 || len(prices) < period+1 {
		return 0, false
	}
	window := prices[len(prices)-(period+1):]
	total := 0.0
	for i := 1; i < len(window); i++ {
		total += math.Abs(window[i] - window[i-1])
	}
	return total / float64(period), true
}

// ATROHLC returns the Average True Range from real OHLC candles, rounded to
// 6dp. True Range = max(h-l, |h-prevC|, |l-prevC|). Needs period+1 candles.
func ATROHLC(candles []domain.Candle, period int) (float64, bool) {
	atr, ok := atrOHLC(candles, period)
	return roundTo(atr, 6), ok
}

// atrOHLC returns the unrounded OHLC ATR used by ATROHLC.
func atrOHLC(candles []domain.Candle, period int) (float64, bool) {
	if period <= 0 || len(candles) < period+1 {
		return 0, false
	}
	window := candles[len(candles)-(period+1):]
	total := 0.0
	for i := 1; i < len(window); i++ {
		prevClose := window[i-1].C
		h, l := window[i].H, window[i].L
		total += math.Max(h-l, math.Max(math.Abs(h-prevClose), math.Abs(l-prevClose)))
	}
	return total / float64(period), true
}

// stochasticK returns %K rounded to 1dp, or 50 when the range is flat.
func stochasticK(high, low, closePrice float64) float64 {
	if high == low {
		return 50.0
	}
	return roundTo((closePrice-low)/(high-low)*100.0, 1)
}

// stochasticResult builds the nested %K/%D/bias map from the last d %K values.
func stochasticResult(kValues []float64) map[string]any {
	kLatest := kValues[len(kValues)-1]
	bias := "neutral"
	if kLatest < 20 {
		bias = "oversold"
	} else if kLatest > 80 {
		bias = "overbought"
	}
	return map[string]any{
		"k":    kLatest,
		"d":    roundTo(sum(kValues)/float64(len(kValues)), 1),
		"bias": bias,
	}
}

// Stochastic returns %K, %D and bias using closes as the high/low proxy.
// It is nil when there are fewer than kPeriod+dPeriod-1 prices.
func Stochastic(prices []float64, kPeriod, dPeriod int) map[string]any {
	if kPeriod <= 0 || dPeriod <= 0 || len(prices) < kPeriod+dPeriod-1 {
		return nil
	}
	kValues := make([]float64, 0, dPeriod)
	for i := 0; i < dPeriod; i++ {
		idx := len(prices) - dPeriod + i
		window := prices[idx-kPeriod+1 : idx+1]
		low, high := minMax(window)
		kValues = append(kValues, stochasticK(high, low, window[len(window)-1]))
	}
	return stochasticResult(kValues)
}

// StochasticOHLC returns %K, %D and bias from real OHLC candles.
// It is nil when there are fewer than kPeriod+dPeriod-1 candles.
func StochasticOHLC(candles []domain.Candle, kPeriod, dPeriod int) map[string]any {
	if kPeriod <= 0 || dPeriod <= 0 || len(candles) < kPeriod+dPeriod-1 {
		return nil
	}
	kValues := make([]float64, 0, dPeriod)
	for i := 0; i < dPeriod; i++ {
		idx := len(candles) - dPeriod + i
		window := candles[idx-kPeriod+1 : idx+1]
		high, low := window[0].H, window[0].L
		for _, c := range window[1:] {
			high = math.Max(high, c.H)
			low = math.Min(low, c.L)
		}
		kValues = append(kValues, stochasticK(high, low, window[len(window)-1].C))
	}
	return stochasticResult(kValues)
}

// WilliamsR returns Williams %R in the range -100 (oversold) to 0 (overbought).
// It is unavailable when there are fewer than period prices.
func WilliamsR(prices []float64, period int) (float64, bool) {
	if period <= 0 || len(prices) < period {
		return 0, false
	}
	window := prices[len(prices)-period:]
	low, high := minMax(window)
	if high == low {
		return -50.0, true
	}
	return roundTo((high-window[len(window)-1])/(high-low)*-100.0, 1), true
}

// PriceChanges returns the % change vs 5, 15 and 30 minutes ago, skipping
// windows without enough history. The result is never nil.
func PriceChanges(prices []float64, tickSeconds int) map[string]any {
	result := map[string]any{}
	if len(prices) < 2 {
		return result
	}
	if tickSeconds <= 0 {
		tickSeconds = DefaultTickSecs
	}
	current := prices[len(prices)-1]
	windows := []struct {
		label   string
		minutes int
	}{{"5m", 5}, {"15m", 15}, {"30m", 30}}
	for _, w := range windows {
		n := int(float64(w.minutes*60) / float64(tickSeconds))
		if len(prices) > n {
			past := prices[len(prices)-(n+1)]
			if past > 0 {
				result[w.label] = roundTo((current-past)/past*100, 2)
			}
		}
	}
	return result
}

// CandleChangeSpans are the candle counts CandleChanges measures over: on
// 30-second ticks the basic momentum compares two 5-tick windows (10 ticks,
// 5 minutes), and the price-change votes span 5 and 15 minutes, 1 and 3
// times that; on candles the momentum spans 10 candles, so 10 and 30.
var CandleChangeSpans = []int{10, 30}

// CandleChanges returns the percentage change of the last close over each
// of CandleChangeSpans candles, keyed "10_candles" and "30_candles"; a span
// longer than the history is left out.
func CandleChanges(closes []float64) map[string]any {
	result := map[string]any{}
	if len(closes) == 0 {
		return result
	}
	current := closes[len(closes)-1]
	for _, n := range CandleChangeSpans {
		if len(closes) <= n {
			continue
		}
		if past := closes[len(closes)-1-n]; past > 0 {
			result[strconv.Itoa(n)+"_candles"] = roundTo((current-past)/past*100, 2)
		}
	}
	return result
}

// ── Convenience wrapper ───────────────────────────────────────────────────────

// thresholdLabel classifies a value as oversold/overbought/neutral using inclusive bounds.
func thresholdLabel(value, oversoldAt, overboughtAt float64) string {
	if value >= overboughtAt {
		return "overbought"
	}
	if value <= oversoldAt {
		return "oversold"
	}
	return "neutral"
}

// ComputeAll returns every indicator in one flat snapshot; missing indicators are omitted.
// When ohlcCandles is non-empty, ATR and Stochastic use true high/low ranges instead
// of close-price proxies.
func ComputeAll(prices []float64, tickSeconds int, ohlcCandles []domain.Candle) Indicators {
	out := Indicators{}
	if len(prices) == 0 {
		return out
	}
	last := prices[len(prices)-1]

	if changes := PriceChanges(prices, tickSeconds); len(changes) > 0 {
		out["price_changes"] = changes
	}

	if rsi, ok := RSI(prices, RSIPeriod); ok {
		out["rsi_14"] = rsi
		out["rsi_signal"] = thresholdLabel(rsi, 30, 70)
	}

	e9, okFast := EMA(prices, EMAFastPeriod)
	e21, okSlow := EMA(prices, EMASlowPeriod)
	if okFast && okSlow {
		out["ema9"] = roundTo(e9, 2)
		out["ema21"] = roundTo(e21, 2)
		out["ema_cross"] = directionLabel(e9, e21)
	}

	if bb := BollingerBands(prices, BBPeriod); bb != nil {
		out["bb"] = bb
	}

	if m := MACD(prices, MACDFastPeriod, MACDSlowPeriod, MACDSignalPeriod); m != nil {
		out["macd"] = m
	}

	// ATR: real OHLC when available, else the close-to-close approximation.
	// atr_pct uses the unrounded ATR: on sub-cent coins the 6dp value is 0.
	var atr float64
	var okATR bool
	if len(ohlcCandles) > 0 {
		atr, okATR = atrOHLC(ohlcCandles, ATRPeriod)
	} else {
		atr, okATR = atrClose(prices, ATRPeriod)
	}
	if okATR {
		out["atr"] = roundTo(atr, 6)
		if last > 0 {
			out["atr_pct"] = roundTo(atr/last*100, 3)
		} else {
			out["atr_pct"] = nil // Python stores None here, serialised as null
		}
	}

	// Stochastic: real OHLC when available, else the close-price proxy.
	var stoch map[string]any
	if len(ohlcCandles) > 0 {
		stoch = StochasticOHLC(ohlcCandles, StochKPeriod, StochDPeriod)
	} else {
		stoch = Stochastic(prices, StochKPeriod, StochDPeriod)
	}
	if stoch != nil {
		out["stoch"] = stoch
	}

	if wr, ok := WilliamsR(prices, WilliamsRPeriod); ok {
		out["williams_r"] = wr
		out["williams_r_signal"] = thresholdLabel(wr, -80, -20)
	}

	return out
}
