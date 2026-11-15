package backtest

import (
	"strings"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/histdata"
)

// withInterval returns cfg with STRATEGY_INTERVAL_SECONDS set explicitly, as
// a settings file or a sensitivity variation sets it.
func withInterval(cfg Config, seconds int) Config {
	cfg.Settings.StrategyIntervalSeconds = seconds
	cfg.ExplicitSettings = append(cfg.ExplicitSettings, "STRATEGY_INTERVAL_SECONDS")
	return cfg
}

// observedTimes returns the time of every strategy tick a run observed.
func observedTimes(obs []TickObservation) []time.Time {
	out := make([]time.Time, len(obs))
	for i, o := range obs {
		out[i] = o.Time
	}
	return out
}

// TestStrategyInterval covers replaying the strategy every
// STRATEGY_INTERVAL_SECONDS instead of every 30-second bar.
func TestStrategyInterval(t *testing.T) {
	const ticks = 2000 // 30-second bars after the start

	t.Run("GIVEN STRATEGY_INTERVAL_SECONDS 120 from the settings WHEN replayed THEN the strategy runs every 120 s from start+120 s and the price history grows by one price per strategy tick", func(t *testing.T) {
		var obs []TickObservation
		cfg := withScript(withInterval(testConfig([]string{"BTC/EUR"}, ticks), 120), &scripted{}, &obs)
		res := mustRun(t, oneMarket(flatSeries(ticks+10, 100)), cfg)
		times := observedTimes(obs)
		if len(times) != ticks/4 {
			t.Fatalf("%d strategy ticks, want %d", len(times), ticks/4)
		}
		if !times[0].Equal(t0.Add(120 * time.Second)) {
			t.Fatalf("first strategy tick %v, want start+120s", times[0])
		}
		for i := 1; i < len(times); i++ {
			if d := times[i].Sub(times[i-1]); d != 120*time.Second {
				t.Fatalf("strategy ticks %v and %v are %v apart", times[i-1], times[i], d)
			}
		}
		if res.Ticks != ticks/4 {
			t.Fatalf("result counts %d ticks, want %d", res.Ticks, ticks/4)
		}
	})

	t.Run("GIVEN no explicit interval WHEN replayed THEN every 30-second bar is a strategy tick, as before", func(t *testing.T) {
		var obs []TickObservation
		cfg := withScript(testConfig([]string{"BTC/EUR"}, ticks), &scripted{}, &obs)
		if cfg.Settings.StrategyIntervalSeconds == 30 {
			t.Fatal("test needs the built-in default to differ from 30 s")
		}
		mustRun(t, oneMarket(flatSeries(ticks+10, 100)), cfg)
		if len(obs) != ticks || obs[1].Time.Sub(obs[0].Time) != histdata.BarInterval {
			t.Fatalf("%d strategy ticks, want %d at 30 s", len(obs), ticks)
		}
	})

	t.Run("GIVEN a 120 s interval WHEN a buy is decided THEN it fills on the next 30-second bar, not at the next strategy tick", func(t *testing.T) {
		decide := t0.Add(120 * time.Second * 40) // past the 35-tick warm-up
		s := (&scripted{}).at(decide, "BTC/EUR", domain.Long)
		cfg := withScript(withInterval(testConfig([]string{"BTC/EUR"}, ticks), 120), s, nil)
		res := mustRun(t, oneMarket(flatSeries(ticks+10, 100)), cfg)
		filled := ordersWhere(res, func(o OrderLogEntry) bool { return o.Status == StatusFilled && o.Reason == ReasonSignal })
		if len(filled) != 1 {
			t.Fatalf("filled orders %+v, want one", res.Orders)
		}
		if !filled[0].DecisionTime.Equal(decide) || filled[0].FillTime.Sub(decide) != histdata.BarInterval {
			t.Fatalf("decided %v filled %v, want a fill 30 s after %v", filled[0].DecisionTime, filled[0].FillTime, decide)
		}
	})

	t.Run("GIVEN an interval that is not a whole number of 30-second bars WHEN the run starts THEN it is refused", func(t *testing.T) {
		cfg := withInterval(testConfig([]string{"BTC/EUR"}, ticks), 45)
		_, err := Run(t.Context(), oneMarket(flatSeries(ticks+10, 100)), cfg)
		if err == nil || !strings.Contains(err.Error(), "STRATEGY_INTERVAL_SECONDS") {
			t.Fatalf("err = %v, want a refusal naming STRATEGY_INTERVAL_SECONDS", err)
		}
	})

	t.Run("GIVEN a run WHEN its assumptions are listed THEN the strategy interval used is stated", func(t *testing.T) {
		res := mustRun(t, oneMarket(flatSeries(ticks+10, 100)), withInterval(testConfig([]string{"BTC/EUR"}, ticks), 120))
		for _, a := range res.Assumptions {
			if a.Name == "strategy_interval" {
				if !strings.Contains(a.Value, "120 s") {
					t.Fatalf("strategy_interval value %q, want 120 s", a.Value)
				}
				return
			}
		}
		t.Fatal("no strategy_interval assumption")
	})
}
