package analysis

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

// flat returns n copies of value.
func flat(value float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = value
	}
	return out
}

// trendingUp returns n prices starting at start and rising by step.
func trendingUp(start, step float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = start + float64(i)*step
	}
	return out
}

// reversed returns a reversed copy of prices.
func reversed(prices []float64) []float64 {
	out := make([]float64, len(prices))
	for i, p := range prices {
		out[len(prices)-1-i] = p
	}
	return out
}

// candles returns n identical OHLC candles.
func candles(h, l, c float64, n int) []domain.Candle {
	out := make([]domain.Candle, n)
	for i := range out {
		out[i] = domain.Candle{H: h, L: l, C: c}
	}
	return out
}

// near reports whether a and b differ by at most tol.
func near(a, b, tol float64) bool {
	return math.Abs(a-b) <= tol
}

// TestRSI covers Wilder RSI behaviour.
func TestRSI(t *testing.T) {
	t.Run("GIVEN fewer than 15 prices WHEN RSI is computed THEN it is unavailable", func(t *testing.T) {
		if _, ok := RSI(flat(100, 14), 14); ok {
			t.Fatal("expected RSI to be unavailable")
		}
	})

	t.Run("GIVEN continuous gains WHEN RSI is computed THEN it is 100", func(t *testing.T) {
		got, ok := RSI(trendingUp(100, 1, 16), 14)
		if !ok || got != 100.0 {
			t.Fatalf("expected 100, got %v (ok=%v)", got, ok)
		}
	})

	t.Run("GIVEN flat prices WHEN RSI is computed THEN it is neutral at 50", func(t *testing.T) {
		got, ok := RSI(flat(100, 50), 14)
		if !ok || got != 50.0 {
			t.Fatalf("expected 50 (no gains and no losses), got %v (ok=%v)", got, ok)
		}
	})

	t.Run("GIVEN an uptrend WHEN RSI is computed THEN it is above 80", func(t *testing.T) {
		got, ok := RSI(trendingUp(100, 1, 50), 14)
		if !ok || got <= 80 {
			t.Fatalf("expected > 80, got %v", got)
		}
	})

	t.Run("GIVEN a downtrend WHEN RSI is computed THEN it is below 20", func(t *testing.T) {
		got, ok := RSI(reversed(trendingUp(100, 1, 50)), 14)
		if !ok || got >= 20 {
			t.Fatalf("expected < 20, got %v", got)
		}
	})

	t.Run("GIVEN a mixed series WHEN RSI is computed THEN SMMA smoothing carries earlier gains", func(t *testing.T) {
		prices := trendingUp(100, 1, 15)
		last := prices[len(prices)-1]
		for i := 1; i <= 15; i++ {
			prices = append(prices, last-float64(i))
		}
		prices = append(prices, prices[len(prices)-1]+1)

		got, ok := RSI(prices, 14)
		if !ok || got <= 10 {
			t.Fatalf("expected SMMA RSI > 10, got %v", got)
		}
	})

	t.Run("GIVEN a price list WHEN RSI is computed THEN it matches the reference to 1dp", func(t *testing.T) {
		prices := []float64{100.0, 101.0, 100.5, 102.0, 101.2, 103.0, 102.4, 104.0, 103.1, 105.0,
			104.2, 106.0, 105.5, 107.0, 106.1, 108.0, 107.4, 109.0, 108.3, 110.0}
		period := 14
		var gains, losses []float64
		for i := 1; i < len(prices); i++ {
			c := prices[i] - prices[i-1]
			gains = append(gains, math.Max(0, c))
			losses = append(losses, math.Max(0, -c))
		}
		avgGain, avgLoss := 0.0, 0.0
		for i := 0; i < period; i++ {
			avgGain += gains[i]
			avgLoss += losses[i]
		}
		avgGain /= float64(period)
		avgLoss /= float64(period)
		for i := period; i < len(gains); i++ {
			avgGain = (avgGain*float64(period-1) + gains[i]) / float64(period)
			avgLoss = (avgLoss*float64(period-1) + losses[i]) / float64(period)
		}
		ref := 100.0 - 100.0/(1.0+avgGain/avgLoss)

		got, ok := RSI(prices, period)
		if !ok || !near(got, ref, 0.05) {
			t.Fatalf("expected ~%v, got %v", ref, got)
		}
	})
}

// TestEMA covers EMA values and series alignment.
func TestEMA(t *testing.T) {
	t.Run("GIVEN fewer than 21 prices WHEN the slow EMA is computed THEN it is unavailable", func(t *testing.T) {
		if _, ok := EMA(flat(100, 20), 21); ok {
			t.Fatal("expected slow EMA to be unavailable")
		}
	})

	t.Run("GIVEN an uptrend WHEN the EMA pair is computed THEN fast is above slow", func(t *testing.T) {
		prices := trendingUp(100, 1, 60)
		fast, okF := EMA(prices, 9)
		slow, okS := EMA(prices, 21)
		if !okF || !okS || fast <= slow {
			t.Fatalf("expected fast > slow, got %v vs %v", fast, slow)
		}
	})

	t.Run("GIVEN a downtrend WHEN the EMA pair is computed THEN fast is below slow", func(t *testing.T) {
		prices := reversed(trendingUp(100, 1, 60))
		fast, okF := EMA(prices, 9)
		slow, okS := EMA(prices, 21)
		if !okF || !okS || fast >= slow {
			t.Fatalf("expected fast < slow, got %v vs %v", fast, slow)
		}
	})

	t.Run("GIVEN valid prices WHEN MACD series are aligned THEN fast and slow lengths match", func(t *testing.T) {
		prices := trendingUp(100, 0.5, 50)
		fastSeries := EMASeries(prices, 12)
		slowSeries := EMASeries(prices, 26)
		if len(fastSeries[26-12:]) != len(slowSeries) {
			t.Fatalf("aligned lengths differ: %d vs %d", len(fastSeries[14:]), len(slowSeries))
		}
	})
}

// TestBollingerBands covers Bollinger band outputs.
func TestBollingerBands(t *testing.T) {
	t.Run("GIVEN fewer than 20 prices WHEN bands are computed THEN they are unavailable", func(t *testing.T) {
		if BollingerBands(flat(100, 19), 20) != nil {
			t.Fatal("expected nil bands")
		}
	})

	t.Run("GIVEN flat prices WHEN bands are computed THEN position is 50", func(t *testing.T) {
		bb := BollingerBands(flat(100, 30), 20)
		pos, ok := Num(bb, "position")
		if !ok || !near(pos, 50, 1) {
			t.Fatalf("expected ~50, got %v", pos)
		}
	})

	t.Run("GIVEN 30 prices WHEN bands are computed THEN middle equals the 20-period SMA", func(t *testing.T) {
		prices := trendingUp(90, 1, 30)
		expected := 0.0
		for _, p := range prices[10:] {
			expected += p
		}
		expected /= 20
		middle, ok := Num(BollingerBands(prices, 20), "middle")
		if !ok || !near(middle, expected, expected*1e-4) {
			t.Fatalf("expected %v, got %v", expected, middle)
		}
	})

	t.Run("GIVEN a price list WHEN bands are computed THEN they match the reference", func(t *testing.T) {
		prices := make([]float64, 25)
		for i := range prices {
			prices[i] = 100.0 + float64(i)*0.5 + float64(i%3)*0.2
		}
		window := prices[5:]
		mean := 0.0
		for _, p := range window {
			mean += p
		}
		mean /= 20
		variance := 0.0
		for _, p := range window {
			variance += (p - mean) * (p - mean)
		}
		std := math.Sqrt(variance / 20)

		bb := BollingerBands(prices, 20)
		upper, _ := Num(bb, "upper")
		middle, _ := Num(bb, "middle")
		lower, _ := Num(bb, "lower")
		if !near(upper, roundTo(mean+2*std, 2), 1e-8) ||
			!near(middle, roundTo(mean, 2), 1e-8) ||
			!near(lower, roundTo(mean-2*std, 2), 1e-8) {
			t.Fatalf("bands differ from reference: %v", bb)
		}
	})
}

// TestMACD covers MACD availability and direction.
func TestMACD(t *testing.T) {
	t.Run("GIVEN fewer than 34 prices WHEN MACD is computed THEN it is unavailable", func(t *testing.T) {
		if MACD(flat(100, 33), 12, 26, 9) != nil {
			t.Fatal("expected nil MACD")
		}
	})

	t.Run("GIVEN enough prices WHEN MACD is computed THEN line, signal and histogram are present", func(t *testing.T) {
		for _, prices := range [][]float64{flat(100, 40), trendingUp(100, 0.3, 40)} {
			m := MACD(prices, 12, 26, 9)
			for _, key := range []string{"line", "signal", "histogram", "bias", "signal_bias"} {
				if _, ok := m[key]; !ok {
					t.Fatalf("missing %q in %v", key, m)
				}
			}
		}
	})

	t.Run("GIVEN an uptrend WHEN MACD is computed THEN the line is positive and bias bullish", func(t *testing.T) {
		m := MACD(trendingUp(100, 1, 60), 12, 26, 9)
		line, ok := Num(m, "line")
		if !ok || line <= 0 || m["bias"] != "bullish" {
			t.Fatalf("expected bullish positive line, got %v", m)
		}
	})
}

// TestATR covers close-price and OHLC ATR.
func TestATR(t *testing.T) {
	t.Run("GIVEN fewer than 15 prices WHEN ATR is computed THEN it is unavailable", func(t *testing.T) {
		if _, ok := ATR(flat(100, 14), 14); ok {
			t.Fatal("expected ATR to be unavailable")
		}
	})

	t.Run("GIVEN volatile prices WHEN ATR is computed THEN it is positive", func(t *testing.T) {
		prices := make([]float64, 30)
		for i := range prices {
			prices[i] = 95
			if i%2 == 0 {
				prices[i] = 105
			}
		}
		got, ok := ATR(prices, 14)
		if !ok || got <= 0 {
			t.Fatalf("expected positive ATR, got %v", got)
		}
	})

	t.Run("GIVEN flat prices WHEN ATR is computed THEN it is zero", func(t *testing.T) {
		got, ok := ATR(flat(100, 20), 14)
		if !ok || !near(got, 0, 1e-9) {
			t.Fatalf("expected 0, got %v", got)
		}
	})

	t.Run("GIVEN ranged OHLC candles WHEN OHLC ATR is computed THEN it reflects the true range", func(t *testing.T) {
		got, ok := ATROHLC(candles(110, 100, 105, 15), 14)
		if !ok || got <= 5 {
			t.Fatalf("expected ATR > 5, got %v", got)
		}
	})

	t.Run("GIVEN too few candles WHEN OHLC ATR is computed THEN it is unavailable", func(t *testing.T) {
		if _, ok := ATROHLC(candles(110, 100, 105, 14), 14); ok {
			t.Fatal("expected ATR to be unavailable")
		}
	})
}

// TestStochasticAndWilliams covers oscillator outputs.
func TestStochasticAndWilliams(t *testing.T) {
	t.Run("GIVEN fewer than 16 prices WHEN stochastic is computed THEN it is unavailable", func(t *testing.T) {
		if Stochastic(flat(100, 15), 14, 3) != nil {
			t.Fatal("expected nil stochastic")
		}
	})

	t.Run("GIVEN a close at the period high WHEN stochastic is computed THEN %K is ~100", func(t *testing.T) {
		k, ok := Num(Stochastic(append(flat(100, 15), 200), 14, 3), "k")
		if !ok || !near(k, 100, 1) {
			t.Fatalf("expected ~100, got %v", k)
		}
	})

	t.Run("GIVEN a close at the period low WHEN stochastic is computed THEN %K is ~0", func(t *testing.T) {
		st := Stochastic(append(flat(200, 15), 100), 14, 3)
		k, ok := Num(st, "k")
		if !ok || !near(k, 0, 1) || st["bias"] != "oversold" {
			t.Fatalf("expected ~0 oversold, got %v", st)
		}
	})

	t.Run("GIVEN candles closing at the high WHEN OHLC stochastic is computed THEN %K is above 90", func(t *testing.T) {
		k, ok := Num(StochasticOHLC(candles(110, 90, 110, 17), 14, 3), "k")
		if !ok || k <= 90 {
			t.Fatalf("expected > 90, got %v", k)
		}
	})

	t.Run("GIVEN fewer than 14 prices WHEN Williams %R is computed THEN it is unavailable", func(t *testing.T) {
		if _, ok := WilliamsR(flat(100, 13), 14); ok {
			t.Fatal("expected Williams %R to be unavailable")
		}
	})

	t.Run("GIVEN the last price at the high WHEN Williams %R is computed THEN it is ~0", func(t *testing.T) {
		got, ok := WilliamsR(append(flat(100, 13), 110), 14)
		if !ok || !near(got, 0, 1) {
			t.Fatalf("expected ~0, got %v", got)
		}
	})

	t.Run("GIVEN flat prices WHEN Williams %R is computed THEN it is -50", func(t *testing.T) {
		got, ok := WilliamsR(flat(100, 20), 14)
		if !ok || !near(got, -50, 0.1) {
			t.Fatalf("expected -50, got %v", got)
		}
	})

	t.Run("GIVEN a price list WHEN ATR, Williams and stochastic run THEN they match the reference", func(t *testing.T) {
		prices := make([]float64, 20)
		for i := range prices {
			prices[i] = 100.0 + float64(i)*0.5 - float64(i%3)*0.4
		}
		window := prices[5:]
		trs := 0.0
		for i := 1; i < len(window); i++ {
			trs += math.Abs(window[i] - window[i-1])
		}
		atrRef := trs / 14
		wrWindow := prices[6:]
		high, low := wrWindow[0], wrWindow[0]
		for _, p := range wrWindow {
			high = math.Max(high, p)
			low = math.Min(low, p)
		}
		wrRef := (high - wrWindow[len(wrWindow)-1]) / (high - low) * -100

		atr, _ := ATR(prices, 14)
		wr, _ := WilliamsR(prices, 14)
		st := Stochastic(prices, 14, 3)
		if !near(atr, atrRef, 1e-6) || !near(wr, wrRef, 0.05) {
			t.Fatalf("atr=%v (ref %v) wr=%v (ref %v)", atr, atrRef, wr, wrRef)
		}
		if _, ok := st["k"]; !ok {
			t.Fatal("stochastic missing k")
		}
		if _, ok := st["d"]; !ok {
			t.Fatal("stochastic missing d")
		}
	})
}

// TestPriceChanges covers the 5m/15m/30m windows.
func TestPriceChanges(t *testing.T) {
	t.Run("GIVEN 31 ticks WHEN price changes run THEN 5m and 15m windows are reported", func(t *testing.T) {
		prices := flat(100, 31)
		prices[30] = 105
		got := PriceChanges(prices, 30)
		if v, _ := Num(got, "5m"); v != 5.0 {
			t.Fatalf("expected 5m = 5.0, got %v", got)
		}
		if _, ok := got["15m"]; !ok {
			t.Fatal("expected 15m window")
		}
	})

	t.Run("GIVEN a rising series WHEN price changes run THEN 5m change is positive", func(t *testing.T) {
		v, ok := Num(PriceChanges(trendingUp(100, 0.1, 31), 30), "5m")
		if !ok || v <= 0 {
			t.Fatalf("expected positive 5m change, got %v", v)
		}
	})

	t.Run("GIVEN too few ticks WHEN price changes run THEN the result is empty", func(t *testing.T) {
		if got := PriceChanges(flat(100, 5), 30); len(got) != 0 {
			t.Fatalf("expected empty, got %v", got)
		}
	})
}

// TestComputeAll covers the combined indicator snapshot.
func TestComputeAll(t *testing.T) {
	t.Run("GIVEN no prices WHEN ComputeAll runs THEN the snapshot is empty", func(t *testing.T) {
		if got := ComputeAll(nil, 30, nil); len(got) != 0 {
			t.Fatalf("expected empty, got %v", got)
		}
	})

	t.Run("GIVEN a single price or short history WHEN ComputeAll runs THEN unavailable values are omitted", func(t *testing.T) {
		if got := ComputeAll([]float64{100}, 30, nil); len(got) != 0 {
			t.Fatalf("expected empty, got %v", got)
		}
		if got := ComputeAll([]float64{100, 101, 102}, 30, nil); len(got) != 0 {
			t.Fatalf("expected empty, got %v", got)
		}
	})

	t.Run("GIVEN 50 prices WHEN ComputeAll runs THEN every Python key is present", func(t *testing.T) {
		got := ComputeAll(trendingUp(100, 0.5, 50), 30, nil)
		expected := []string{"price_changes", "rsi_14", "rsi_signal", "ema9", "ema21", "ema_cross",
			"bb", "macd", "atr", "atr_pct", "stoch", "williams_r", "williams_r_signal"}
		if len(got) != len(expected) {
			t.Fatalf("expected %d keys, got %v", len(expected), got)
		}
		for _, key := range expected {
			if _, ok := got[key]; !ok {
				t.Fatalf("missing key %q", key)
			}
		}
		for _, group := range []string{"bb", "macd", "stoch", "price_changes"} {
			if _, ok := got.Group(group); !ok {
				t.Fatalf("%q is not a nested map", group)
			}
		}
		if cross, _ := got.Str("ema_cross"); cross != "bullish" {
			t.Fatalf("expected bullish ema_cross, got %q", cross)
		}
	})

	t.Run("GIVEN flat ticks and ranged candles WHEN ComputeAll runs THEN ATR and stochastic use candles", func(t *testing.T) {
		got := ComputeAll(flat(105, 40), 30, candles(110, 90, 110, 17))
		atr, _ := got.Num("atr")
		stoch, _ := got.Group("stoch")
		k, _ := Num(stoch, "k")
		if atr <= 5 || k <= 90 {
			t.Fatalf("expected candle-based ATR/stoch, got atr=%v k=%v", atr, k)
		}
	})

	t.Run("GIVEN a zero latest price WHEN ComputeAll runs THEN atr_pct is present but null", func(t *testing.T) {
		prices := append(flat(100, 20), 0)
		got := ComputeAll(prices, 30, nil)
		v, present := got["atr_pct"]
		if !present || v != nil {
			t.Fatalf("expected atr_pct present and nil, got %v (present=%v)", v, present)
		}
	})

	t.Run("GIVEN a sub-cent coin with tiny moves WHEN ComputeAll runs THEN atr_pct still reflects its volatility", func(t *testing.T) {
		prices := make([]float64, 40)
		for i := range prices {
			// Alternate by 2e-8 around 0.0000085: ATR rounds to 0 at 6dp but is ~0.24% of price.
			prices[i] = 0.0000085 + float64(i%2)*0.00000002
		}
		got := ComputeAll(prices, 30, nil)
		pct, ok := got.Num("atr_pct")
		if !ok || pct <= 0.1 {
			t.Fatalf("expected atr_pct around 0.235, got %v (ok=%v)", pct, ok)
		}
	})

	t.Run("GIVEN sub-cent OHLC candles WHEN ComputeAll runs THEN atr_pct still reflects the true range", func(t *testing.T) {
		got := ComputeAll(flat(0.0000085, 40), 30, candles(0.00000851, 0.00000849, 0.0000085, 17))
		pct, ok := got.Num("atr_pct")
		if !ok || pct <= 0.1 {
			t.Fatalf("expected atr_pct around 0.235, got %v (ok=%v)", pct, ok)
		}
	})

	t.Run("GIVEN a full snapshot WHEN serialised to JSON THEN numbers are float64 and it round-trips", func(t *testing.T) {
		got := ComputeAll(trendingUp(100, 0.5, 50), 30, nil)
		assertNoInts(t, got)
		raw, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("marshal failed: %v", err)
		}
		var back map[string]any
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatalf("unmarshal failed: %v", err)
		}
		if len(back) != len(got) {
			t.Fatalf("round trip lost keys: %v", back)
		}
	})
}

// assertNoInts fails when any value in the nested map is not float64, string, nil or a map.
func assertNoInts(t *testing.T, m map[string]any) {
	t.Helper()
	for key, value := range m {
		switch v := value.(type) {
		case float64, string, nil:
		case map[string]any:
			assertNoInts(t, v)
		default:
			t.Fatalf("key %q has unexpected type %T", key, value)
		}
	}
}

// TestRoundTo covers Python-compatible rounding.
func TestRoundTo(t *testing.T) {
	t.Run("GIVEN exact and inexact halves WHEN rounded THEN results match Python round()", func(t *testing.T) {
		cases := []struct {
			in     float64
			places int
			want   float64
		}{
			{2.5, 0, 2.0},    // ties to even
			{0.125, 2, 0.12}, // exact binary tie → even
			{2.675, 2, 2.67}, // binary value is below the half
			{1.005, 2, 1.0},  // binary value is below the half
			{-0.0451, 3, -0.045},
		}
		for _, c := range cases {
			if got := roundTo(c.in, c.places); got != c.want {
				t.Fatalf("roundTo(%v, %d) = %v, want %v", c.in, c.places, got, c.want)
			}
		}
	})
}
