package portfolio

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

// fakeEngine is a minimal EquityPricedEngine holding cash and fixed-size positions.
type fakeEngine struct {
	cash      float64
	positions map[string]float64 // market -> size
}

// GetCash returns the engine's cash balance.
func (f *fakeEngine) GetCash() float64 {
	return f.cash
}

// GetTotalEquity returns cash plus positions marked at prices.
func (f *fakeEngine) GetTotalEquity(prices map[string]float64) float64 {
	total := f.cash
	for market, size := range f.positions {
		total += size * prices[market]
	}
	return total
}

// TestBuildEquitySnapshot covers mark-to-market snapshots.
func TestBuildEquitySnapshot(t *testing.T) {
	t.Run("GIVEN cash and holdings WHEN a snapshot is built THEN equity is cash plus mark value", func(t *testing.T) {
		engine := &fakeEngine{cash: 650, positions: map[string]float64{"BTC/EUR": 2, "ETH/EUR": 3}}
		ts := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)

		snap := BuildEquitySnapshot(engine, map[string]float64{"BTC/EUR": 120, "ETH/EUR": 40}, ts)

		if snap.Timestamp != "2026-04-25T12:00:00+00:00" {
			t.Fatalf("unexpected timestamp %q", snap.Timestamp)
		}
		if math.Abs(snap.PositionsValue-360) > 1e-9 || snap.Cash != 650 || math.Abs(snap.Equity-1010) > 1e-9 {
			t.Fatalf("unexpected snapshot: %+v", snap)
		}
	})

	t.Run("GIVEN no timestamp WHEN a snapshot is built THEN the current UTC time is used", func(t *testing.T) {
		before := time.Now().UTC().Add(-time.Second)
		snap := BuildEquitySnapshot(&fakeEngine{cash: 10}, nil, time.Time{})
		parsed, err := time.Parse(time.RFC3339Nano, snap.Timestamp)
		if err != nil || parsed.Before(before) {
			t.Fatalf("unexpected timestamp %q (%v)", snap.Timestamp, err)
		}
	})

	t.Run("GIVEN a snapshot WHEN serialised THEN it uses the Python JSON keys", func(t *testing.T) {
		raw, _ := json.Marshal(BuildEquitySnapshot(&fakeEngine{cash: 10}, nil, time.Time{}))
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		for _, key := range []string{"timestamp", "equity", "cash", "positions_value"} {
			if _, ok := m[key]; !ok {
				t.Fatalf("missing key %q in %s", key, raw)
			}
		}
	})
}

// TestAlignEquityHistory covers aligning the graph tail with the header value.
func TestAlignEquityHistory(t *testing.T) {
	t.Run("GIVEN a different current equity WHEN history is aligned THEN a matching final point is appended", func(t *testing.T) {
		history := []EquityPoint{{Timestamp: "2026-04-25T12:00:00+00:00", Equity: 950}}
		ts := time.Date(2026, 4, 25, 12, 5, 0, 0, time.UTC)

		got := AlignEquityHistoryWithCurrent(history, 975.25, ts)

		last := got[len(got)-1]
		if len(got) != 2 || last.Timestamp != "2026-04-25T12:05:00+00:00" || last.Equity != 975.25 {
			t.Fatalf("unexpected history: %+v", got)
		}
		if len(history) != 1 {
			t.Fatal("input history must not be modified")
		}
	})

	t.Run("GIVEN a final point already matching WHEN aligned THEN no point is added", func(t *testing.T) {
		history := []EquityPoint{{Timestamp: "a", Equity: 100}, {Timestamp: "b", Equity: 975.25}}
		got := AlignEquityHistoryWithCurrent(history, 975.25, time.Time{})
		if len(got) != 2 || got[1].Equity != 975.25 || got[1].Timestamp != "b" {
			t.Fatalf("unexpected history: %+v", got)
		}
	})

	t.Run("GIVEN empty history WHEN aligned THEN a single current point is returned", func(t *testing.T) {
		got := AlignEquityHistoryWithCurrent(nil, 1000, time.Time{})
		if len(got) != 1 || got[0].Equity != 1000 || got[0].Timestamp == "" {
			t.Fatalf("unexpected history: %+v", got)
		}
	})
}

// TestFormatISO covers Python isoformat() compatibility.
func TestFormatISO(t *testing.T) {
	t.Run("GIVEN whole and fractional seconds WHEN formatted THEN output matches Python isoformat", func(t *testing.T) {
		whole := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
		frac := time.Date(2026, 4, 25, 12, 0, 0, 1500, time.UTC)
		if got := FormatISO(whole); got != "2026-04-25T12:00:00+00:00" {
			t.Fatalf("unexpected %q", got)
		}
		if got := FormatISO(frac); got != "2026-04-25T12:00:00.000001+00:00" {
			t.Fatalf("unexpected %q", got)
		}
	})
}
