package backtest

import (
	"testing"
	"time"
)

// day returns midnight UTC n days after t0.
func day(n int) time.Time { return t0.Add(time.Duration(n) * 24 * time.Hour) }

// TestTickTracker covers the per-tick drawdown, exposure and daily series.
func TestTickTracker(t *testing.T) {
	t.Run("GIVEN an equity path with a recovered dip and a final unrecovered fall WHEN tracked THEN both drawdowns, their times and the longest duration are exact", func(t *testing.T) {
		tr := newTracker(t0, 100)
		tr.mark(t0.Add(1*time.Hour), 110) // peak
		tr.mark(t0.Add(2*time.Hour), 99)  // -11, -10 %
		tr.mark(t0.Add(3*time.Hour), 105)
		tr.mark(t0.Add(5*time.Hour), 120) // recovers the 110 peak at 5h: 4 h episode
		tr.mark(t0.Add(6*time.Hour), 90)  // -30, -25 %, never recovers
		tr.final(t0.Add(7*time.Hour), 96)
		s := tr.stats()

		dd := s.MaxDrawdownByPct
		if dd == nil || !near(dd.Pct, 25, 1e-12) || dd.Amount != 30 || dd.Peak != 120 || dd.Trough != 90 ||
			!dd.PeakTime.Equal(t0.Add(5*time.Hour)) || !dd.TroughTime.Equal(t0.Add(6*time.Hour)) || dd.RecoveryTime != nil {
			t.Fatalf("max drawdown by pct %+v", dd)
		}
		if a := s.MaxDrawdownByAmount; a == nil || a.Amount != 30 || a.RecoveryTime != nil {
			t.Fatalf("max drawdown by amount %+v", a)
		}
		// The first episode (110 at 1h, recovered at 5h) lasts 4 h; the open one
		// (120 at 5h to the end at 7h) lasts 2 h.
		if s.LongestDrawdownSeconds != 4*3600 || !s.LongestDrawdownRecovered ||
			!s.LongestDrawdownStart.Equal(t0.Add(1*time.Hour)) || !s.LongestDrawdownEnd.Equal(t0.Add(5*time.Hour)) {
			t.Fatalf("longest drawdown %+v", s)
		}
	})

	t.Run("GIVEN a drawdown still open at the end that outlasts every other WHEN tracked THEN it is the longest and marked unrecovered", func(t *testing.T) {
		tr := newTracker(t0, 100)
		tr.mark(t0.Add(time.Hour), 90)
		tr.final(t0.Add(10*time.Hour), 95)
		s := tr.stats()
		if s.LongestDrawdownSeconds != 10*3600 || s.LongestDrawdownRecovered || !s.LongestDrawdownStart.Equal(t0) {
			t.Fatalf("longest drawdown %+v", s)
		}
		if s.MaxDrawdownByPct.RecoveryTime != nil || !near(s.MaxDrawdownByPct.Pct, 10, 1e-12) {
			t.Fatalf("drawdown %+v", s.MaxDrawdownByPct)
		}
	})

	t.Run("GIVEN equity that never falls WHEN tracked THEN there is no drawdown", func(t *testing.T) {
		tr := newTracker(t0, 100)
		tr.mark(t0.Add(time.Hour), 100)
		tr.final(t0.Add(2*time.Hour), 101)
		if s := tr.stats(); s.MaxDrawdownByPct != nil || s.MaxDrawdownByAmount != nil || s.LongestDrawdownSeconds != 0 {
			t.Fatalf("stats %+v", s)
		}
	})

	t.Run("GIVEN ticks with and without positions WHEN observed THEN exposure, utilisation and average equity are per tick", func(t *testing.T) {
		tr := newTracker(t0, 100)
		tr.observe(t0.Add(30*time.Second), 100, 100, 0) // flat
		tr.observe(t0.Add(60*time.Second), 100, 50, 1)  // 50 % used
		tr.observe(t0.Add(90*time.Second), 120, 30, 2)  // 75 % used
		tr.observe(t0.Add(120*time.Second), 80, 80, 0)
		s := tr.stats()
		if s.Ticks != 4 || s.TicksInMarket != 2 || s.AverageEquity != 100 ||
			!near(s.AverageUtilisation, (0+0.5+0.75+0)/4, 1e-15) || s.MaxUtilisation != 0.75 {
			t.Fatalf("stats %+v", s)
		}
	})

	t.Run("GIVEN ticks across UTC midnights WHEN observed THEN the daily series holds each midnight's equity and ends with the final value", func(t *testing.T) {
		tr := newTracker(day(0), 100)
		tr.mark(day(0).Add(12*time.Hour), 105)
		tr.mark(day(1), 110)
		tr.mark(day(1).Add(time.Hour), 90)
		tr.mark(day(2), 120)
		tr.final(day(2), 118) // end-of-run valuation replaces the last tick's value
		got := tr.stats().Daily
		if len(got) != 2 || !got[0].Time.Equal(day(1)) || got[0].Value != 110 || !got[1].Time.Equal(day(2)) || got[1].Value != 118 {
			t.Fatalf("daily %+v", got)
		}
	})
}
