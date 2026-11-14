package backtest

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/histdata"
)

// TestReplayProvenance covers that a result cites the data it was built from.
func TestReplayProvenance(t *testing.T) {
	t.Run("GIVEN a provenance source WHEN replayed THEN the result cites every market read over the whole range read, pre-roll included", func(t *testing.T) {
		const ticks = 200
		reader := memReader{bars: map[string][]histdata.Bar{
			"BTC/EUR": randomWalkBars(1, ticks+1, t0, 50000, 0.002),
			"ETH/EUR": randomWalkBars(2, ticks+1, t0, 3000, 0.003),
		}}
		cfg := testConfig([]string{"ETH/EUR", "BTC/EUR"}, ticks)
		type call struct {
			market   string
			from, to time.Time
		}
		var calls []call
		cfg.Provenance = func(market string, from, to time.Time) ([]histdata.Provenance, error) {
			calls = append(calls, call{market, from, to})
			return []histdata.Provenance{{Market: market, Month: "2026-01", FileSHA256: "sha-" + market,
				Sources: []histdata.SourceRecord{{Source: "x.zip!" + market}}}}, nil
		}
		res := mustRun(t, reader, cfg)
		if len(calls) != 2 {
			t.Fatalf("provenance asked %d times, want once per market", len(calls))
		}
		for _, c := range calls {
			if !c.from.Equal(cfg.Start.Add(-PreRoll)) || !c.to.Equal(cfg.End) {
				t.Fatalf("%s: provenance range [%v, %v) is not the range read", c.market, c.from, c.to)
			}
		}
		var cited []string
		for _, p := range res.DataProvenance {
			cited = append(cited, p.Market)
			if p.FileSHA256 == "" || len(p.Sources) == 0 {
				t.Fatalf("entry %+v lacks the file checksum or its sources", p)
			}
		}
		if !slices.Equal(cited, []string{"BTC/EUR", "ETH/EUR"}) {
			t.Fatalf("cited %v, want every market read, sorted", cited)
		}
	})

	t.Run("GIVEN a provenance source that fails WHEN replayed THEN the run fails rather than produce an uncited result", func(t *testing.T) {
		cfg := testConfig([]string{"BTC/EUR"}, 100)
		cfg.Provenance = func(string, time.Time, time.Time) ([]histdata.Provenance, error) {
			return nil, errors.New("manifest unreadable")
		}
		_, err := Run(t.Context(), oneMarket(randomWalkBars(1, 101, t0, 100, 0.002)), cfg)
		if err == nil || !strings.Contains(err.Error(), "manifest unreadable") {
			t.Fatalf("err %v", err)
		}
	})

	t.Run("GIVEN no provenance source WHEN replayed THEN the result says provenance is not recorded", func(t *testing.T) {
		res := mustRun(t, oneMarket(randomWalkBars(1, 101, t0, 100, 0.002)), testConfig([]string{"BTC/EUR"}, 100))
		if res.DataProvenance == nil || len(res.DataProvenance) != 0 {
			t.Fatalf("provenance %v, want an empty list", res.DataProvenance)
		}
	})
}
