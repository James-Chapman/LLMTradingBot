package histdata

import (
	"testing"
	"time"
)

// TestMergeTicks covers the 30-second tick stream across markets.
func TestMergeTicks(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	r := fakeReader{bars: map[string][]Bar{
		"BTC/EUR": {bar(t0, 100, 1), bar(t0.Add(2*BarInterval), 102, 1)},
		"ETH/EUR": {bar(t0.Add(BarInterval), 10, 1)},
	}}

	var ticks []Tick
	for tk, err := range MergeTicks(r, []string{"ETH/EUR", "BTC/EUR"}, t0, t0.Add(3*BarInterval)) {
		if err != nil {
			t.Fatal(err)
		}
		ticks = append(ticks, tk)
	}

	t.Run("GIVEN bars WHEN merged THEN there is one tick per 30-second boundary, stamped at the end of its interval", func(t *testing.T) {
		if len(ticks) != 3 {
			t.Fatalf("got %d ticks: %+v", len(ticks), ticks)
		}
		for i, tk := range ticks {
			if want := t0.Add(time.Duration(i+1) * BarInterval); !tk.Time.Equal(want) {
				t.Errorf("tick %d at %v, want %v", i, tk.Time, want)
			}
		}
	})

	t.Run("GIVEN a market that has not traded yet WHEN merged THEN it is absent from the tick", func(t *testing.T) {
		if len(ticks[0].Prices) != 1 || ticks[0].Prices[0].Market != "BTC/EUR" {
			t.Fatalf("first tick = %+v", ticks[0])
		}
	})

	t.Run("GIVEN an empty interval WHEN merged THEN the last price is carried forward without a bar", func(t *testing.T) {
		second := ticks[1]
		if len(second.Prices) != 2 || second.Prices[0].Market != "BTC/EUR" || second.Prices[1].Market != "ETH/EUR" {
			t.Fatalf("second tick = %+v", second)
		}
		btc := second.Prices[0]
		if btc.Price != 100 || btc.HasBar {
			t.Errorf("BTC carried = %+v", btc)
		}
		eth := second.Prices[1]
		if eth.Price != 10 || !eth.HasBar || eth.Bar.Close != 10 {
			t.Errorf("ETH = %+v", eth)
		}
		third := ticks[2]
		if third.Prices[0].Price != 102 || !third.Prices[0].HasBar || third.Prices[1].Price != 10 || third.Prices[1].HasBar {
			t.Errorf("third tick = %+v", third)
		}
	})
}

// TestCandleAggregator covers clock-aligned candles with a forming candle.
func TestCandleAggregator(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 0, 3, 0, 0, time.UTC) // three minutes into a 5 m candle

	t.Run("GIVEN bars WHEN aggregated THEN candles are aligned to the clock and the forming candle updates", func(t *testing.T) {
		a, err := NewCandleAggregator(5*time.Minute, 100)
		if err != nil {
			t.Fatal(err)
		}
		mustAdd := func(b Bar) {
			if err := a.Add(b); err != nil {
				t.Fatal(err)
			}
		}
		mustAdd(Bar{Start: t0, Open: 10, High: 11, Low: 9, Close: 10.5, Volume: 1, Trades: 2})
		c, forming := a.Candles(t0.Add(BarInterval))
		if len(c) != 1 || !forming || !c[0].Start.Equal(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)) || c[0].Close != 10.5 {
			t.Fatalf("candles = %+v forming %v", c, forming)
		}
		mustAdd(Bar{Start: t0.Add(time.Minute), Open: 10.5, High: 12, Low: 10, Close: 11.5, Volume: 2, Trades: 1})
		c, forming = a.Candles(t0.Add(time.Minute + BarInterval))
		if len(c) != 1 || !forming || c[0].High != 12 || c[0].Low != 9 || c[0].Open != 10 || c[0].Close != 11.5 || c[0].Volume != 3 || c[0].Trades != 3 {
			t.Fatalf("updated forming candle = %+v", c)
		}
		c, forming = a.Candles(time.Date(2026, 7, 1, 0, 5, 0, 0, time.UTC))
		if len(c) != 1 || forming {
			t.Fatalf("at the boundary the candle is closed: %+v %v", c, forming)
		}
		mustAdd(Bar{Start: time.Date(2026, 7, 1, 0, 20, 0, 0, time.UTC), Open: 13, High: 13, Low: 13, Close: 13, Volume: 1, Trades: 1})
		c, forming = a.Candles(time.Date(2026, 7, 1, 0, 20, 30, 0, time.UTC))
		if len(c) != 2 || !forming || !c[1].Start.Equal(time.Date(2026, 7, 1, 0, 20, 0, 0, time.UTC)) {
			t.Fatalf("empty intervals must produce no candle: %+v", c)
		}
	})

	t.Run("GIVEN more candles than kept WHEN aggregated THEN only the most recent are returned", func(t *testing.T) {
		a, _ := NewCandleAggregator(15*time.Minute, 2)
		for i := range 4 {
			_ = a.Add(bar(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i)*15*time.Minute), float64(i), 1))
		}
		c, _ := a.Candles(time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC))
		if len(c) != 2 || c[0].Close != 2 || c[1].Close != 3 {
			t.Fatalf("candles = %+v", c)
		}
	})

	t.Run("GIVEN bars out of order WHEN added THEN the aggregator refuses them", func(t *testing.T) {
		a, _ := NewCandleAggregator(5*time.Minute, 10)
		_ = a.Add(bar(t0, 1, 1))
		if err := a.Add(bar(t0.Add(-BarInterval), 1, 1)); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("GIVEN an interval that is not a multiple of the bar WHEN created THEN it is rejected", func(t *testing.T) {
		if _, err := NewCandleAggregator(45*time.Second, 10); err == nil {
			t.Fatal("expected an error")
		}
	})
}

// TestRollingVolume covers the rolling 24-hour volume.
func TestRollingVolume(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	t.Run("GIVEN bars over more than a day WHEN the volume is read THEN bars older than 24 hours roll off", func(t *testing.T) {
		v := NewRollingVolume(24 * time.Hour)
		v.Add(bar(t0, 1, 5))
		v.Add(bar(t0.Add(12*time.Hour), 1, 2))
		if got := v.At(t0.Add(12*time.Hour + BarInterval)); got != 7 {
			t.Fatalf("volume = %v, want 7", got)
		}
		if got := v.At(t0.Add(24*time.Hour + BarInterval)); got != 2 {
			t.Fatalf("volume after roll-off = %v, want 2", got)
		}
		if got := v.At(t0.Add(48 * time.Hour)); got != 0 {
			t.Fatalf("volume after everything rolled off = %v, want 0", got)
		}
	})

	t.Run("GIVEN a bar still in progress at the query time WHEN the volume is read THEN it is not counted", func(t *testing.T) {
		v := NewRollingVolume(24 * time.Hour)
		v.Add(bar(t0, 1, 5))
		if got := v.At(t0.Add(BarInterval / 2)); got != 0 {
			t.Fatalf("volume = %v, want 0", got)
		}
	})
}
