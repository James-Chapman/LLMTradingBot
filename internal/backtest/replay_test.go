package backtest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/kraken"
	"github.com/James-Chapman/LLMTradingBot/internal/llm"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy"
)

// fixtureAnalyser always recommends a confident long, like the Python FixtureAnalyser.
type fixtureAnalyser struct {
	mu    sync.Mutex
	calls int
}

// AnalyseSignal returns a supportive analysis.
func (f *fixtureAnalyser) AnalyseSignal(_ context.Context, _ llm.SignalRequest) llm.SignalAnalysis {
	return llm.SignalAnalysis{ConfidenceScale: 1.0, Sentiment: 0.6, Reasoning: "fixture uptrend", LLMUsed: true}
}

// RecommendTrade counts the call and returns a long recommendation.
func (f *fixtureAnalyser) RecommendTrade(_ context.Context, _ llm.RecommendationRequest) llm.LLMTradeRecommendation {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return llm.LLMTradeRecommendation{Action: "long", Confidence: 0.80, Sentiment: 0.60, Reasoning: "fixture uptrend", LLMUsed: true}
}

// fixtureFile is the pinned generator spec for a profitable 48-hour replay.
type fixtureFile struct {
	Generator struct {
		Count      int     `json:"count"`
		StartPrice float64 `json:"start_price"`
		CloseStep  float64 `json:"close_step"`
		HighOffset float64 `json:"high_offset"`
		LowOffset  float64 `json:"low_offset"`
		Volume     float64 `json:"volume"`
	} `json:"generator"`
}

// profitable48hCandles builds the 576 pinned synthetic 5-minute candles.
func profitable48hCandles(t *testing.T) []domain.Candle {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "btc_eur_5m_profitable_48h.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var f fixtureFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	g := f.Generator
	start := time.Date(2026, 4, 23, 0, 0, 0, 0, time.UTC).Unix()
	candles := make([]domain.Candle, 0, g.Count)
	price := g.StartPrice
	for i := 0; i < g.Count; i++ {
		closePrice := price + g.CloseStep
		candles = append(candles, domain.Candle{
			T: start + int64(i)*300,
			O: price,
			H: closePrice + g.HighOffset,
			L: price - g.LowOffset,
			C: closePrice,
			V: g.Volume,
		})
		price = closePrice
	}
	return candles
}

// llmOnlyReplay runs the fixture through the LLM-only strategy.
func llmOnlyReplay(t *testing.T, candles []domain.Candle) Result {
	t.Helper()
	result, err := RunReplay(context.Background(), Options{
		CandlesByMarket: map[string][]domain.Candle{"BTC/EUR": candles},
		StrategyID:      strategy.LLMOnlyID,
		StartingCapital: 500.0,
		Analyser:        &fixtureAnalyser{},
		Settings:        defaultSettings(),
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	return result
}

// fakeOHLC is an OHLCSource that serves fixed candles and records the request.
type fakeOHLC struct {
	candles  []domain.Candle
	symbol   string
	interval int
	limit    int
}

// GetOHLC returns the configured candles.
func (f *fakeOHLC) GetOHLC(_ context.Context, symbol string, intervalMinutes, limit int) ([]domain.Candle, error) {
	f.symbol, f.interval, f.limit = symbol, intervalMinutes, limit
	return f.candles, nil
}

// TestReplay covers the deterministic historical replay engine.
func TestReplay(t *testing.T) {
	t.Run("GIVEN a pinned profitable 48h fixture WHEN replayed THEN the bot ends in profit after fees", func(t *testing.T) {
		result := llmOnlyReplay(t, profitable48hCandles(t))
		if result.EndingEquity <= result.StartingEquity || result.RealisedPnL <= 0 {
			t.Fatalf("expected profit: %+v", result)
		}
		if result.TradeCount < 1 || result.Fees <= 0 {
			t.Fatalf("expected at least one fee-paying trade: count=%d fees=%v", result.TradeCount, result.Fees)
		}
	})

	t.Run("GIVEN the same fixture and settings WHEN replayed twice THEN results are identical", func(t *testing.T) {
		candles := profitable48hCandles(t)
		first := llmOnlyReplay(t, candles)
		second := llmOnlyReplay(t, candles)
		if first.EndingEquity != second.EndingEquity || first.RealisedPnL != second.RealisedPnL {
			t.Fatalf("equity differs between runs")
		}
		if len(first.Orders) != len(second.Orders) || !reflect.DeepEqual(first.SignalDecisions, second.SignalDecisions) {
			t.Fatalf("orders or signals differ between runs")
		}
	})

	t.Run("GIVEN future candles WHEN replay runs THEN the strategy only sees visible candles", func(t *testing.T) {
		result := llmOnlyReplay(t, profitable48hCandles(t)[:40])
		if len(result.SignalDecisions) == 0 {
			t.Fatalf("expected signal decisions")
		}
		for _, s := range result.SignalDecisions {
			if s.VisibleCandleCount > 40 {
				t.Fatalf("look-ahead: visible count %d", s.VisibleCandleCount)
			}
		}
	})

	t.Run("GIVEN a replay result WHEN a report is written THEN it contains summary and ledger data", func(t *testing.T) {
		result := llmOnlyReplay(t, profitable48hCandles(t))
		path := filepath.Join(t.TempDir(), "nested", "replay_report_test.json")
		if err := WriteReport(result, path); err != nil {
			t.Fatalf("write report: %v", err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("report not written: %v", err)
		}
		var report map[string]any
		if err := json.Unmarshal(raw, &report); err != nil {
			t.Fatalf("report is not JSON: %v", err)
		}
		summary := report["summary"].(map[string]any)
		if summary["strategy_id"] != "llm_only_strategy" {
			t.Fatalf("wrong strategy id: %v", summary["strategy_id"])
		}
		if summary["ending_equity"].(float64) <= summary["starting_equity"].(float64) {
			t.Fatalf("report should show profit")
		}
		if orders := report["orders"].([]any); len(orders) < 2 {
			t.Fatalf("expected open and close orders, got %d", len(orders))
		}
		for _, key := range []string{"signals", "equity_curve"} {
			if _, ok := report[key]; !ok {
				t.Fatalf("report missing %s", key)
			}
		}
	})

	t.Run("GIVEN an LLM strategy and no analyser WHEN replayed THEN it runs with a neutral LLM", func(t *testing.T) {
		for _, id := range []string{strategy.LLMOnlyID, strategy.BasicAndLLMID, strategy.BasicID} {
			result, err := RunReplay(context.Background(), Options{
				CandlesByMarket: map[string][]domain.Candle{"BTC/EUR": profitable48hCandles(t)[:60]},
				StrategyID:      id,
				Settings:        defaultSettings(),
			})
			if err != nil {
				t.Fatalf("%s: %v", id, err)
			}
			if result.StartingEquity != 500.0 || len(result.EquityCurve) != 62 {
				t.Fatalf("%s: unexpected result shape: start=%v curve=%d", id, result.StartingEquity, len(result.EquityCurve))
			}
			if id == strategy.LLMOnlyID && len(result.SignalDecisions) != 0 {
				t.Fatalf("neutral LLM should never recommend a trade")
			}
		}
	})

	t.Run("GIVEN invalid input WHEN replayed THEN an error is returned", func(t *testing.T) {
		if _, err := RunReplay(context.Background(), Options{StrategyID: strategy.BasicID}); err == nil {
			t.Fatalf("expected error for empty candles")
		}
		_, err := RunReplay(context.Background(), Options{
			CandlesByMarket: map[string][]domain.Candle{"BTC/EUR": profitable48hCandles(t)[:5]},
			StrategyID:      "nope",
		})
		if err == nil {
			t.Fatalf("expected error for unknown strategy")
		}
	})

	t.Run("GIVEN an OHLC source WHEN a live replay runs THEN the candle limit covers the requested hours", func(t *testing.T) {
		src := &fakeOHLC{candles: profitable48hCandles(t)[:50]}
		result, err := RunLiveReplay(context.Background(), src, LiveOptions{
			Market: "BTC/EUR", Hours: 48, Interval: 5, StrategyID: strategy.LLMOnlyID,
			StartingCapital: 500, Analyser: &fixtureAnalyser{}, Settings: defaultSettings(),
		})
		if err != nil {
			t.Fatalf("live replay: %v", err)
		}
		if src.symbol != "BTC/EUR" || src.interval != 5 || src.limit != 576 {
			t.Fatalf("unexpected OHLC request: %+v", src)
		}
		if result.StrategyID != strategy.LLMOnlyID {
			t.Fatalf("wrong strategy")
		}
	})

	t.Run("GIVEN a losing live replay with require-profit WHEN run THEN acceptance fails", func(t *testing.T) {
		falling := profitable48hCandles(t)[:50]
		for i := range falling {
			falling[i].C, falling[i].O = 200-float64(i), 200-float64(i)+1
		}
		_, err := RunLiveReplay(context.Background(), &fakeOHLC{candles: falling}, LiveOptions{
			Market: "BTC/EUR", Hours: 4, Interval: 5, StrategyID: strategy.LLMOnlyID,
			StartingCapital: 500, RequireProfit: true, Analyser: &fixtureAnalyser{}, Settings: defaultSettings(),
		})
		if !errors.Is(err, ErrNotProfitable) {
			t.Fatalf("expected ErrNotProfitable, got %v", err)
		}
	})
}

// TestProfitRequirement covers explicit profit acceptance.
func TestProfitRequirement(t *testing.T) {
	loss := Result{StrategyID: "combined", StartingEquity: 500, EndingEquity: 499, RealisedPnL: -1, MaxDrawdown: 1, TradeCount: 1}

	t.Run("GIVEN a losing smoke result WHEN profit is not required THEN no error is raised", func(t *testing.T) {
		if err := EnforceProfitRequirement(loss, false); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("GIVEN a losing acceptance result WHEN profit is required THEN acceptance fails", func(t *testing.T) {
		if err := EnforceProfitRequirement(loss, true); !errors.Is(err, ErrNotProfitable) {
			t.Fatalf("expected ErrNotProfitable, got %v", err)
		}
	})
}

// TestReplayMetrics covers drawdown, trade statistics and resampling.
func TestReplayMetrics(t *testing.T) {
	t.Run("GIVEN an equity series with a known drawdown WHEN computed THEN the peak-to-trough loss is returned", func(t *testing.T) {
		got := MaxDrawdown([]float64{1000, 1100, 900, 1050, 800})
		if got < 299.999999 || got > 300.000001 {
			t.Fatalf("drawdown = %v", got)
		}
	})

	t.Run("GIVEN an empty equity series WHEN max drawdown is computed THEN zero is returned", func(t *testing.T) {
		if MaxDrawdown(nil) != 0 {
			t.Fatalf("expected zero")
		}
	})

	t.Run("GIVEN mixed P&L values WHEN trade stats are computed THEN win rate and profit factor match", func(t *testing.T) {
		stats := ComputeTradeStats([]float64{10, -5, 8, -3, 12})
		if diff := stats.WinRate - 0.6; diff > 1e-9 || diff < -1e-9 {
			t.Fatalf("win rate = %v", stats.WinRate)
		}
		if diff := stats.ProfitFactor - 30.0/8.0; diff > 1e-9 || diff < -1e-9 {
			t.Fatalf("profit factor = %v", stats.ProfitFactor)
		}
	})

	t.Run("GIVEN all-loss P&L WHEN trade stats are computed THEN win rate and profit factor are zero", func(t *testing.T) {
		stats := ComputeTradeStats([]float64{-1, -2, -3})
		if stats.WinRate != 0 || stats.ProfitFactor != 0 {
			t.Fatalf("unexpected stats: %+v", stats)
		}
	})

	t.Run("GIVEN only winning P&L WHEN trade stats are computed THEN profit factor is the gross profit", func(t *testing.T) {
		stats := ComputeTradeStats([]float64{2, 3})
		if stats.WinRate != 1 || stats.ProfitFactor != 5 {
			t.Fatalf("unexpected stats: %+v", stats)
		}
	})

	t.Run("GIVEN nine 5m candles WHEN resampled to 15m THEN three aggregated candles are returned", func(t *testing.T) {
		candles := make([]domain.Candle, 9)
		for i := range candles {
			f := float64(i)
			candles[i] = domain.Candle{T: int64(i), O: 100 + f, H: 102 + f, L: 99 + f, C: 101 + f, V: 10}
		}
		got := ResampleTo15m(candles)
		if len(got) != 3 {
			t.Fatalf("got %d candles", len(got))
		}
		first := got[0]
		if first.O != 100 || first.H != 104 || first.L != 99 || first.C != 103 || first.V != 30 || first.T != 2 {
			t.Fatalf("unexpected first candle: %+v", first)
		}
	})

	t.Run("GIVEN fewer than three candles WHEN resampled THEN no 15m candles are returned", func(t *testing.T) {
		if got := ResampleTo15m(make([]domain.Candle, 2)); len(got) != 0 {
			t.Fatalf("expected none, got %d", len(got))
		}
	})
}

// defaultSettings returns a pointer to the default settings.
func defaultSettings() *config.Settings {
	s := config.Defaults()
	return &s
}

// The Kraken market adapter must be usable as a replay candle source.
var _ OHLCSource = (*kraken.MarketAdapter)(nil)
