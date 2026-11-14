package strategy

import "testing"

// TestStopLossPercent covers converting the stop-loss fraction to a whole percentage.
func TestStopLossPercent(t *testing.T) {
	t.Run("GIVEN fractions that are inexact in floating point WHEN converted THEN the nearest whole percent is returned", func(t *testing.T) {
		cases := map[float64]int{0.29: 29, 0.57: 57, 0.03: 3, 0.05: 5, 0.1: 10}
		for pct, want := range cases {
			if got := stopLossPercent(pct); got != want {
				t.Errorf("stopLossPercent(%v) = %d, want %d", pct, got, want)
			}
		}
	})
}
