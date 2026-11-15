package histdata

import (
	"slices"
	"testing"
	"time"
)

// TestUniverse covers the point-in-time market selection.
func TestUniverse(t *testing.T) {
	date := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	day := func(offset time.Duration, low, high, volume float64) Bar {
		return Bar{Start: date.Add(offset), Open: low, High: high, Low: low, Close: high, Volume: volume, Trades: 1,
			BuyVolume: volume, BuyNotional: volume * low}
	}
	r := fakeReader{bars: map[string][]Bar{
		// Large before the date, nothing after it.
		"BTC/EUR": {day(-2*time.Hour, 100, 110, 10)},
		// Small before the date, but huge after it: must not leak in.
		"ETH/EUR": {day(-3*time.Hour, 10, 11, 1), day(time.Hour, 10, 11, 1e9)},
		// Pegged: under 0.5 % range over 24 h.
		"USDC/EUR": {day(-time.Hour, 0.92, 0.921, 1e6)},
		// Traded only more than 24 h before the date.
		"SOL/EUR": {day(-30*time.Hour, 50, 60, 1e6)},
		// Other quote currency.
		"BTC/USD": {day(-time.Hour, 100, 110, 1e6)},
	}}

	t.Run("GIVEN a date WHEN the universe is built THEN only data before the date ranks markets and pegged pairs are skipped", func(t *testing.T) {
		u, err := Universe(r, date, UniverseOptions{Quote: "EUR", Top: 50})
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, e := range u.Selected {
			got = append(got, e.Market)
		}
		if !slices.Equal(got, []string{"BTC/EUR", "ETH/EUR"}) {
			t.Fatalf("selected = %v", got)
		}
		if u.Selected[1].Value24h != 10 {
			t.Errorf("ETH value = %v, want 10 (only the bar before the date)", u.Selected[1].Value24h)
		}
		if len(u.Pegged) != 1 || u.Pegged[0].Market != "USDC/EUR" {
			t.Errorf("pegged = %+v", u.Pegged)
		}
		if !slices.Equal(u.NoData, []string{"SOL/EUR"}) {
			t.Errorf("no data = %v", u.NoData)
		}
	})

	t.Run("GIVEN a top-N smaller than the candidates WHEN the universe is built THEN only N are selected", func(t *testing.T) {
		u, _ := Universe(r, date, UniverseOptions{Quote: "EUR", Top: 1})
		if len(u.Selected) != 1 || u.Selected[0].Market != "BTC/EUR" {
			t.Fatalf("selected = %+v", u.Selected)
		}
	})

	t.Run("GIVEN ranking by 7 days WHEN the universe is built THEN older trading within the week counts", func(t *testing.T) {
		u, _ := Universe(r, date, UniverseOptions{Quote: "EUR", Top: 50, RankBy7d: true})
		if len(u.Selected) == 0 || u.Selected[0].Market != "SOL/EUR" {
			t.Fatalf("selected = %+v", u.Selected)
		}
	})
}
