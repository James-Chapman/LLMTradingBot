package app

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/llm"
)

// updateGolden rewrites the golden files instead of comparing against them.
var updateGolden = flag.Bool("update-golden", false, "rewrite golden test files")

// goldenBriefingStore serves one fixed market briefing to the analyser.
type goldenBriefingStore struct{ briefing *llm.MarketBriefing }

// SaveBriefing is unused by the golden test.
func (s goldenBriefingStore) SaveBriefing(context.Context, llm.MarketBriefing, []string) error {
	return nil
}

// LoadLatestBriefing returns the fixed briefing.
func (s goldenBriefingStore) LoadLatestBriefing(context.Context) (*llm.MarketBriefing, error) {
	return s.briefing, nil
}

// SaveReflection is unused by the golden test.
func (s goldenBriefingStore) SaveReflection(context.Context, llm.Reflection) error { return nil }

// LoadLatestReflection returns no reflection.
func (s goldenBriefingStore) LoadLatestReflection(context.Context) (*llm.Reflection, error) {
	return nil, nil
}

// goldenSeries returns a deterministic wavy price series of n points.
func goldenSeries(n int, base, amplitude float64, phase float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = base + amplitude*math.Sin(float64(i)/7+phase) + float64(i)*0.013*base/1000
	}
	return out
}

// goldenCandles returns n deterministic candles starting at start, step seconds apart.
func goldenCandles(n int, start int64, step int64, base float64) []domain.Candle {
	out := make([]domain.Candle, n)
	for i := range out {
		c := base + 3*math.Sin(float64(i)/5)
		out[i] = domain.Candle{T: start + int64(i)*step, O: c - 0.4, H: c + 1.1, L: c - 1.3, C: c, V: 2 + float64(i%7)}
	}
	return out
}

// TestBuildMarketDataGolden pins the strategy input the live loop builds, so
// moving the builder into a shared package cannot change it.
func TestBuildMarketDataGolden(t *testing.T) {
	t.Run("GIVEN representative ticks, candles, volumes and a briefing WHEN the live loop builds market data THEN it matches the pinned golden output", func(t *testing.T) {
		settings := config.Defaults()
		settings.MomentumLookbackTicks = 10
		a := &App{settings: settings}
		a.analyser = llm.NewAnalyser(nil, "€")
		a.analyser.SetStore(goldenBriefingStore{briefing: &llm.MarketBriefing{
			MarketOutlooks: map[string]llm.MarketOutlook{
				"BTC/EUR": {Bias: "bullish", Score: 0.4},
				"ETH/EUR": {Bias: "very bearish", Score: -3}, // clamped to -1
			},
			GeneratedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		}})
		a.analyser.LoadFromDB(context.Background())

		vol := 1234.5
		start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC).Unix()
		ordered := []pricedSnapshot{
			{symbol: "BTC/EUR", snap: domain.MarketSnapshot{Symbol: "BTC/EUR", Price: 50123.4, Volume: &vol}},
			{symbol: "ETH/EUR", snap: domain.MarketSnapshot{Symbol: "ETH/EUR", Price: 3001.5}},
			{symbol: "SOL/EUR", snap: domain.MarketSnapshot{Symbol: "SOL/EUR", Price: 140.25}},
			{symbol: "ADA/EUR", snap: domain.MarketSnapshot{Symbol: "ADA/EUR", Price: 0.51}},
		}
		histories := map[string][]float64{
			"BTC/EUR": goldenSeries(200, 50000, 120, 0), // full window
			"ETH/EUR": goldenSeries(36, 3000, 9, 1),     // just past warm-up
			"SOL/EUR": goldenSeries(10, 140, 0.8, 2),    // shorter than the momentum lookback
			"ADA/EUR": goldenSeries(11, 0.5, 0.01, 3),   // exactly the lookback
		}
		ohlc5 := map[string][]domain.Candle{
			"BTC/EUR": goldenCandles(100, start, 300, 50000),
			"ETH/EUR": goldenCandles(20, start, 300, 3000),
		}
		ohlc15 := map[string][]domain.Candle{
			"BTC/EUR": goldenCandles(100, start, 900, 50000),
			"SOL/EUR": goldenCandles(5, start, 900, 140),
		}

		got, err := json.MarshalIndent(a.buildMarketData(ordered, histories, ohlc5, ohlc15), "", " ")
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join("testdata", "marketdata_golden.json")
		if *updateGolden {
			if err := os.MkdirAll("testdata", 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, got, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read golden file (run with -update-golden against the reference code): %v", err)
		}
		if !bytes.Equal(bytes.TrimSpace(got), bytes.TrimSpace(want)) {
			t.Fatalf("market data differs from the golden output")
		}
	})
}
