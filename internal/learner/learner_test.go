package learner

import (
	"encoding/json"
	"math"
	"sync"
	"testing"
)

// seed records n outcomes with the given pnl for strategy "s", BTC/EUR long.
func seed(l *PerformanceLearner, n int, pnl float64) {
	for i := 0; i < n; i++ {
		l.RecordOutcome("s", "BTC/EUR", "long", pnl)
	}
}

// tradesFor returns the trade count shown in Summary for a market, or 0.
func tradesFor(l *PerformanceLearner, market string) int {
	for _, row := range l.Summary() {
		if row.Market == market {
			return row.Trades
		}
	}
	return 0
}

// TestAdjustConfidence covers the MIN_SAMPLES threshold and direction of adjustment.
func TestAdjustConfidence(t *testing.T) {
	t.Run("GIVEN no outcomes WHEN confidence is adjusted THEN it is unchanged", func(t *testing.T) {
		if got := New().AdjustConfidence("s", "BTC/EUR", "long", 0.70); got != 0.70 {
			t.Fatalf("expected 0.70, got %v", got)
		}
	})

	t.Run("GIVEN fewer than MinSamples outcomes WHEN confidence is adjusted THEN it is unchanged", func(t *testing.T) {
		l := New()
		seed(l, MinSamples-1, 10)
		if got := l.AdjustConfidence("s", "BTC/EUR", "long", 0.60); got != 0.60 {
			t.Fatalf("expected 0.60, got %v", got)
		}
	})

	t.Run("GIVEN winning history WHEN confidence is adjusted THEN it is boosted but capped", func(t *testing.T) {
		l := New()
		seed(l, MinSamples, 10)
		got := l.AdjustConfidence("s", "BTC/EUR", "long", 0.60)
		if got <= 0.60 || got > 0.95 {
			t.Fatalf("expected boost within cap, got %v", got)
		}
	})

	t.Run("GIVEN losing history WHEN confidence is adjusted THEN it is suppressed", func(t *testing.T) {
		l := New()
		seed(l, MinSamples, -10)
		if got := l.AdjustConfidence("s", "BTC/EUR", "long", 0.70); got >= 0.70 {
			t.Fatalf("expected suppression, got %v", got)
		}
	})

	t.Run("GIVEN many wins and a high base WHEN confidence is adjusted THEN it never exceeds 0.95", func(t *testing.T) {
		l := New()
		seed(l, 20, 10)
		if got := l.AdjustConfidence("s", "BTC/EUR", "long", 0.95); got > 0.95 {
			t.Fatalf("expected <= 0.95, got %v", got)
		}
	})

	t.Run("GIVEN equal win rates but different magnitudes WHEN adjusted THEN better expectancy scores higher", func(t *testing.T) {
		favourable, unfavourable := New(), New()
		for _, pnl := range []float64{30, -10, 30, -10, 30, -10} {
			favourable.RecordOutcome("combined", "BTC/EUR", "long", pnl)
		}
		for _, pnl := range []float64{10, -30, 10, -30, 10, -30} {
			unfavourable.RecordOutcome("combined", "BTC/EUR", "long", pnl)
		}

		fav := favourable.AdjustConfidence("combined", "BTC/EUR", "long", 0.60)
		unfav := unfavourable.AdjustConfidence("combined", "BTC/EUR", "long", 0.60)
		row := favourable.Summary()[0]

		if fav <= unfav {
			t.Fatalf("expected %v > %v", fav, unfav)
		}
		if row.MeanWinPnl != 30.0 || row.MeanLossPnl != -10.0 || row.QualityScore <= 0 {
			t.Fatalf("unexpected summary row: %+v", row)
		}
	})
}

// TestRecordAndLoad covers incremental recording and DB seeding.
func TestRecordAndLoad(t *testing.T) {
	t.Run("GIVEN a fresh learner WHEN one outcome is recorded THEN its trade count is 1", func(t *testing.T) {
		l := New()
		l.RecordOutcome("s", "BTC/EUR", "long", 5)
		if got := tradesFor(l, "BTC/EUR"); got != 1 {
			t.Fatalf("expected 1, got %d", got)
		}
	})

	t.Run("GIVEN 7 outcomes WHEN recorded THEN the trade count is 7", func(t *testing.T) {
		l := New()
		for i := 0; i < 7; i++ {
			l.RecordOutcome("s", "BTC/EUR", "long", float64(i-3))
		}
		if got := tradesFor(l, "BTC/EUR"); got != 7 {
			t.Fatalf("expected 7, got %d", got)
		}
	})

	t.Run("GIVEN 6 outcome rows WHEN loaded THEN the trade count is 6", func(t *testing.T) {
		l := New()
		rows := make([]Outcome, 6)
		for i := range rows {
			rows[i] = Outcome{StrategyID: "s", Market: "BTC/EUR", Direction: "long", Pnl: 5}
		}
		l.LoadFromOutcomes(rows)
		if got := tradesFor(l, "BTC/EUR"); got != 6 {
			t.Fatalf("expected 6, got %d", got)
		}
	})

	t.Run("GIVEN winning rows WHEN loaded THEN confidence is boosted", func(t *testing.T) {
		l := New()
		rows := make([]Outcome, MinSamples)
		for i := range rows {
			rows[i] = Outcome{StrategyID: "s", Market: "BTC/EUR", Direction: "long", Pnl: 10}
		}
		l.LoadFromOutcomes(rows)
		if got := l.AdjustConfidence("s", "BTC/EUR", "long", 0.60); got <= 0.60 {
			t.Fatalf("expected boost, got %v", got)
		}
	})

	t.Run("GIVEN mixed keys WHEN loaded THEN each key is tracked separately", func(t *testing.T) {
		l := New()
		var rows []Outcome
		for i := 0; i < 3; i++ {
			rows = append(rows, Outcome{StrategyID: "s", Market: "BTC/EUR", Direction: "long", Pnl: 10})
		}
		for i := 0; i < 4; i++ {
			rows = append(rows, Outcome{StrategyID: "s", Market: "ETH/EUR", Direction: "long", Pnl: 10})
		}
		l.LoadFromOutcomes(rows)
		if tradesFor(l, "BTC/EUR") != 3 || tradesFor(l, "ETH/EUR") != 4 {
			t.Fatalf("unexpected counts: %+v", l.Summary())
		}
	})

	t.Run("GIVEN newest-first rows WHEN loaded THEN the rolling window sees the newest outcomes", func(t *testing.T) {
		l := New()
		var rows []Outcome
		for i := 0; i < 10; i++ { // newest 10: wins
			rows = append(rows, Outcome{StrategyID: "s", Market: "BTC/EUR", Direction: "long", Pnl: 10})
		}
		for i := 0; i < 10; i++ { // oldest 10: losses
			rows = append(rows, Outcome{StrategyID: "s", Market: "BTC/EUR", Direction: "long", Pnl: -5})
		}
		l.LoadFromOutcomes(rows)
		if got := l.RollingWinRate("s", "BTC/EUR", "long", 10); got != 1.0 {
			t.Fatalf("expected 1.0, got %v", got)
		}
	})
}

// TestStatistics covers rolling win rate and percentiles.
func TestStatistics(t *testing.T) {
	t.Run("GIVEN 20 outcomes WHEN the last-10 win rate is computed THEN it matches a manual count", func(t *testing.T) {
		l := New()
		seed(l, 10, -5)
		seed(l, 10, 10)
		if got := l.RollingWinRate("s", "BTC/EUR", "long", 10); math.Abs(got-1.0) > 1e-6 {
			t.Fatalf("expected 1.0, got %v", got)
		}
	})

	t.Run("GIVEN no outcomes WHEN the win rate is requested THEN it is zero", func(t *testing.T) {
		if got := New().RollingWinRate("s", "BTC/EUR", "long", 10); got != 0 {
			t.Fatalf("expected 0, got %v", got)
		}
	})

	t.Run("GIVEN P&L values WHEN percentiles are requested THEN they match exclusive quartiles", func(t *testing.T) {
		l := New()
		for _, p := range []float64{10.0, -5.0, 8.0, -3.0, 12.0, 6.0, -1.0, 15.0, -8.0, 4.0} {
			l.RecordOutcome("s", "BTC/EUR", "long", p)
		}
		// Reference: Python statistics.quantiles(n=4) (exclusive method) and mean/median.
		got, ok := l.PnlPercentiles("s", "BTC/EUR", "long")
		if !ok {
			t.Fatal("expected percentiles")
		}
		checks := map[string][2]float64{
			"mean": {got.Mean, 3.8},
			"p25":  {got.P25, -3.5},
			"p50":  {got.P50, 5.0},
			"p75":  {got.P75, 10.5},
		}
		for name, pair := range checks {
			if math.Abs(pair[0]-pair[1]) > 1e-6 {
				t.Fatalf("%s: expected %v, got %v", name, pair[1], pair[0])
			}
		}
		if got.Median != got.P50 || got.P95 < got.P75 {
			t.Fatalf("unexpected distribution: %+v", got)
		}
	})

	t.Run("GIVEN a single outcome WHEN percentiles are requested THEN every value equals it", func(t *testing.T) {
		l := New()
		l.RecordOutcome("s", "BTC/EUR", "long", 7)
		got, _ := l.PnlPercentiles("s", "BTC/EUR", "long")
		if got.P25 != 7 || got.P95 != 7 || got.Mean != 7 || got.Median != 7 {
			t.Fatalf("unexpected: %+v", got)
		}
	})

	t.Run("GIVEN no outcomes WHEN percentiles are requested THEN none are available", func(t *testing.T) {
		if _, ok := New().PnlPercentiles("s", "BTC/EUR", "long"); ok {
			t.Fatal("expected no percentiles")
		}
	})
}

// TestSummary covers the dashboard summary shape.
func TestSummary(t *testing.T) {
	t.Run("GIVEN an empty learner WHEN summarised THEN the JSON is an empty array", func(t *testing.T) {
		raw, _ := json.Marshal(New().Summary())
		if string(raw) != "[]" {
			t.Fatalf("expected [], got %s", raw)
		}
	})

	t.Run("GIVEN several markets WHEN summarised THEN rows are sorted by market with Python keys", func(t *testing.T) {
		l := New()
		l.RecordOutcome("s", "SOL/EUR", "long", 1)
		l.RecordOutcome("s", "BTC/EUR", "short", -1)
		l.RecordOutcome("s", "ETH/EUR", "long", 2)
		rows := l.Summary()
		if rows[0].Market != "BTC/EUR" || rows[1].Market != "ETH/EUR" || rows[2].Market != "SOL/EUR" {
			t.Fatalf("unexpected order: %+v", rows)
		}

		raw, _ := json.Marshal(rows[0])
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		for _, key := range []string{"strategy", "market", "direction", "trades", "win_rate",
			"rolling_win_rate_10", "mean_win_pnl", "mean_loss_pnl", "average_pnl", "median_pnl",
			"pnl_p25", "pnl_p75", "pnl_p95", "quality_score"} {
			if _, ok := m[key]; !ok {
				t.Fatalf("missing JSON key %q in %s", key, raw)
			}
		}
		if len(m) != 14 {
			t.Fatalf("expected 14 keys, got %d", len(m))
		}
	})
}

// TestConcurrency covers safe shared use from multiple goroutines.
func TestConcurrency(t *testing.T) {
	t.Run("GIVEN concurrent writers and readers WHEN they run THEN every outcome is counted", func(t *testing.T) {
		l := New()
		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 50; j++ {
					l.RecordOutcome("s", "BTC/EUR", "long", float64(j%3-1))
					_ = l.AdjustConfidence("s", "BTC/EUR", "long", 0.5)
					_ = l.Summary()
				}
			}()
		}
		wg.Wait()
		if got := tradesFor(l, "BTC/EUR"); got != 1000 {
			t.Fatalf("expected 1000, got %d", got)
		}
	})
}
