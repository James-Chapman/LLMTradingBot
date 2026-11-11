// Package backtest replays historical OHLC candles through a strategy, the
// risk engine and the paper execution engine, deterministically.
package backtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/analysis"
	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/llm"
	"github.com/James-Chapman/LLMTradingBot/internal/paper"
	"github.com/James-Chapman/LLMTradingBot/internal/risk"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy"
)

// DefaultStartingCapital is the replay starting cash when none is given.
const DefaultStartingCapital = 500.0

// ErrNotProfitable is returned when a replay must be profitable and is not.
var ErrNotProfitable = errors.New("replay was not profitable")

// OHLCSource supplies historical candles; *kraken.MarketAdapter satisfies it.
type OHLCSource interface {
	GetOHLC(ctx context.Context, symbol string, intervalMinutes, limit int) ([]domain.Candle, error)
}

// SignalDecision records the risk verdict for one replayed trade idea.
type SignalDecision struct {
	Market             string `json:"market"`
	StrategyID         string `json:"strategy_id"`
	Direction          string `json:"direction"`
	Approved           bool   `json:"approved"`
	Reason             string `json:"reason"`
	VisibleCandleCount int    `json:"visible_candle_count"`
}

// Result is the structured outcome of a replay run.
type Result struct {
	StrategyID      string
	StartingEquity  float64
	EndingEquity    float64
	RealisedPnL     float64
	Fees            float64
	MaxDrawdown     float64
	TradeCount      int
	WinRate         float64
	ProfitFactor    float64
	Orders          []domain.OrderRecord
	SignalDecisions []SignalDecision
	EquityCurve     []float64
}

// Options configures RunReplay.
type Options struct {
	CandlesByMarket map[string][]domain.Candle // 5-minute candles, oldest first
	StrategyID      string
	StartingCapital float64           // 0 uses DefaultStartingCapital
	News            []llm.NewsArticle // optional news fed to LLM strategies
	Analyser        strategy.Analyser // nil uses strategy.NeutralAnalyser
	Settings        *config.Settings  // nil uses config.Defaults()
}

// LiveOptions configures RunLiveReplay.
type LiveOptions struct {
	Market          string
	Hours           int
	Interval        int // candle interval in minutes
	StrategyID      string
	StartingCapital float64
	RequireProfit   bool
	Analyser        strategy.Analyser
	Settings        *config.Settings
}

// TradeStats are the win rate and profit factor of closed replay trades.
type TradeStats struct {
	WinRate      float64
	ProfitFactor float64
}

// ReportSummary is the headline section of a replay report.
type ReportSummary struct {
	StrategyID     string  `json:"strategy_id"`
	StartingEquity float64 `json:"starting_equity"`
	EndingEquity   float64 `json:"ending_equity"`
	RealisedPnL    float64 `json:"realised_pnl"`
	Fees           float64 `json:"fees"`
	MaxDrawdown    float64 `json:"max_drawdown"`
	TradeCount     int     `json:"trade_count"`
	WinRate        float64 `json:"win_rate"`
	ProfitFactor   float64 `json:"profit_factor"`
}

// ReportOrder is one order in a replay report.
type ReportOrder struct {
	ID         string  `json:"id"`
	Market     string  `json:"market"`
	Direction  string  `json:"direction"`
	Size       float64 `json:"size"`
	Price      float64 `json:"price"`
	Status     string  `json:"status"`
	PositionID string  `json:"position_id"`
	Timestamp  string  `json:"timestamp"`
}

// Report is the JSON-serialisable replay report.
type Report struct {
	Summary     ReportSummary    `json:"summary"`
	Orders      []ReportOrder    `json:"orders"`
	Signals     []SignalDecision `json:"signals"`
	EquityCurve []float64        `json:"equity_curve"`
}

// ToReport converts a replay result to report data.
func (r Result) ToReport() Report {
	orders := make([]ReportOrder, 0, len(r.Orders))
	for _, o := range r.Orders {
		orders = append(orders, ReportOrder{
			ID:         o.ID,
			Market:     o.Market,
			Direction:  string(o.Direction),
			Size:       o.Size,
			Price:      o.Price,
			Status:     o.Status,
			PositionID: o.PositionID,
			Timestamp:  isoFormat(o.Timestamp),
		})
	}
	signals := r.SignalDecisions
	if signals == nil {
		signals = []SignalDecision{}
	}
	curve := r.EquityCurve
	if curve == nil {
		curve = []float64{}
	}
	return Report{
		Summary: ReportSummary{
			StrategyID:     r.StrategyID,
			StartingEquity: r.StartingEquity,
			EndingEquity:   r.EndingEquity,
			RealisedPnL:    r.RealisedPnL,
			Fees:           r.Fees,
			MaxDrawdown:    r.MaxDrawdown,
			TradeCount:     r.TradeCount,
			WinRate:        r.WinRate,
			ProfitFactor:   r.ProfitFactor,
		},
		Orders:      orders,
		Signals:     signals,
		EquityCurve: curve,
	}
}

// isoFormat formats a time like Python's datetime.isoformat() (microseconds only when non-zero).
func isoFormat(t time.Time) string {
	if t.Nanosecond()/1000 == 0 {
		return t.Format("2006-01-02T15:04:05-07:00")
	}
	return t.Format("2006-01-02T15:04:05.000000-07:00")
}

// WriteReport writes the replay report as indented JSON, creating parent directories.
func WriteReport(r Result, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(r.ToReport(), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// DefaultReportPath returns docs/backtests/<date>_<market>_<strategy>_<hours>h.json.
func DefaultReportPath(now time.Time, market, strategyID string, hours int) string {
	name := fmt.Sprintf("%s_%s_%s_%dh.json",
		now.UTC().Format("2006-01-02"), strings.ReplaceAll(market, "/", "-"), strategyID, hours)
	return filepath.Join("docs", "backtests", name)
}

// EnforceProfitRequirement returns ErrNotProfitable when profit is required and P&L is not positive.
func EnforceProfitRequirement(r Result, requireProfit bool) error {
	if requireProfit && r.RealisedPnL <= 0 {
		return fmt.Errorf("%w: realised_pnl=%.6f", ErrNotProfitable, r.RealisedPnL)
	}
	return nil
}

// RunLiveReplay fetches recent candles from src and replays them through the selected strategy.
func RunLiveReplay(ctx context.Context, src OHLCSource, opts LiveOptions) (Result, error) {
	if opts.Interval <= 0 {
		return Result{}, fmt.Errorf("interval must be positive, got %d", opts.Interval)
	}
	candleLimit := opts.Hours * 60 / opts.Interval
	candles, err := src.GetOHLC(ctx, opts.Market, opts.Interval, candleLimit)
	if err != nil {
		return Result{}, err
	}
	result, err := RunReplay(ctx, Options{
		CandlesByMarket: map[string][]domain.Candle{opts.Market: candles},
		StrategyID:      opts.StrategyID,
		StartingCapital: opts.StartingCapital,
		Analyser:        opts.Analyser,
		Settings:        opts.Settings,
	})
	if err != nil {
		return Result{}, err
	}
	return result, EnforceProfitRequirement(result, opts.RequireProfit)
}

// ResampleTo15m groups 5-minute candles into 15-minute candles in fixed
// 3-candle buckets; a trailing incomplete bucket is dropped.
func ResampleTo15m(candles []domain.Candle) []domain.Candle {
	complete := (len(candles) / 3) * 3
	result := make([]domain.Candle, 0, complete/3)
	for i := 0; i < complete; i += 3 {
		group := candles[i : i+3]
		c := domain.Candle{T: group[2].T, O: group[0].O, H: group[0].H, L: group[0].L, C: group[2].C}
		for _, g := range group {
			c.H = math.Max(c.H, g.H)
			c.L = math.Min(c.L, g.L)
			c.V += g.V
		}
		result = append(result, c)
	}
	return result
}

// MaxDrawdown returns the largest peak-to-trough fall of an equity curve as a positive value.
func MaxDrawdown(equity []float64) float64 {
	if len(equity) == 0 {
		return 0
	}
	peak := equity[0]
	worst := 0.0
	for _, e := range equity {
		peak = math.Max(peak, e)
		worst = math.Max(worst, peak-e)
	}
	return worst
}

// ComputeTradeStats returns the win rate and profit factor of realised trade P&L values.
// With no losses the profit factor is the gross profit (0 when there are no wins).
func ComputeTradeStats(pnl []float64) TradeStats {
	if len(pnl) == 0 {
		return TradeStats{}
	}
	wins := 0
	grossProfit, grossLoss := 0.0, 0.0
	for _, p := range pnl {
		switch {
		case p > 0:
			wins++
			grossProfit += p
		case p < 0:
			grossLoss += -p
		}
	}
	stats := TradeStats{WinRate: float64(wins) / float64(len(pnl))}
	if grossLoss == 0 {
		stats.ProfitFactor = grossProfit
	} else {
		stats.ProfitFactor = grossProfit / grossLoss
	}
	return stats
}

// closeOrders returns orders that close a previously opened position, by
// pairing repeated position ids in chronological order.
func closeOrders(orders []domain.OrderRecord) []domain.OrderRecord {
	seen := map[string]bool{}
	var closes []domain.OrderRecord
	for _, o := range orders {
		if o.PositionID == "" {
			continue
		}
		if seen[o.PositionID] {
			closes = append(closes, o)
		} else {
			seen[o.PositionID] = true
		}
	}
	return closes
}

// closedOrderPnLs returns the realised P&L of each closed position.
func closedOrderPnLs(engine *paper.Engine, closes []domain.OrderRecord) []float64 {
	var pnl []float64
	for _, o := range closes {
		if v, ok := engine.RealisedPnL(o.PositionID); ok {
			pnl = append(pnl, v)
		}
	}
	return pnl
}

// newStrategy returns the strategy for a replay strategy id.
func newStrategy(id string, s config.Settings) (strategy.Strategy, error) {
	st := strategy.ByID(strategy.All(s), id)
	if st == nil {
		return nil, fmt.Errorf("unknown strategy_id: %s", id)
	}
	return st, nil
}

// buildMarketData returns the strategy context for one market using only the
// candles visible at this replay step (no look-ahead).
func buildMarketData(visible []domain.Candle) strategy.MarketData {
	current := visible[len(visible)-1]
	closes := make([]float64, len(visible))
	for i, c := range visible {
		closes[i] = c.C
	}
	// Previous price is the close 10 candles back (Python closes[-11]).
	previous := closes[0]
	if len(closes) > 10 {
		previous = closes[len(closes)-11]
	}
	htf := analysis.Indicators{}
	if htfCandles := ResampleTo15m(visible); len(htfCandles) > 0 {
		htfCloses := make([]float64, len(htfCandles))
		for i, c := range htfCandles {
			htfCloses[i] = c.C
		}
		htf = analysis.ComputeAll(htfCloses, 900, htfCandles)
	}
	volume := current.V
	return strategy.MarketData{
		Price:           current.C,
		PreviousPrice:   previous,
		Volume:          &volume,
		Indicators:      analysis.ComputeAll(closes, 300, visible),
		HigherTimeframe: htf,
	}
}

// RunReplay replays candles through the strategy, risk engine and paper execution.
// All markets are replayed for the length of the shortest candle series; open
// positions are closed at the final close price.
func RunReplay(ctx context.Context, opts Options) (Result, error) {
	if len(opts.CandlesByMarket) == 0 {
		return Result{}, errors.New("candles_by_market must not be empty")
	}
	settings := config.Defaults()
	if opts.Settings != nil {
		settings = *opts.Settings
	}
	st, err := newStrategy(opts.StrategyID, settings)
	if err != nil {
		return Result{}, err
	}
	startingCapital := opts.StartingCapital
	if startingCapital == 0 {
		startingCapital = DefaultStartingCapital
	}
	analyser := opts.Analyser
	if analyser == nil {
		analyser = strategy.NeutralAnalyser{}
	}

	markets := make([]string, 0, len(opts.CandlesByMarket))
	length := -1
	for m, candles := range opts.CandlesByMarket {
		markets = append(markets, m)
		if length < 0 || len(candles) < length {
			length = len(candles)
		}
	}
	sort.Strings(markets)

	riskEngine := risk.NewEngine(settings, nil)
	riskEngine.UpdateEquity(startingCapital)
	engine := paper.NewEngine(startingCapital, nil, nil)
	signals := []SignalDecision{}
	equityCurve := make([]float64, 0, length+2)
	equityCurve = append(equityCurve, startingCapital)

	for idx := 0; idx < length; idx++ {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		marketData := make(map[string]strategy.MarketData, len(markets))
		prices := make(map[string]float64, len(markets))
		for _, m := range markets {
			data := buildMarketData(opts.CandlesByMarket[m][:idx+1])
			marketData[m] = data
			prices[m] = data.Price
		}
		visibleCount := idx + 1

		engine.UpdateMarkPrices(prices)
		equity := engine.TotalEquity(prices)
		riskEngine.UpdateEquity(equity)
		equityCurve = append(equityCurve, equity)

		ideas := st.Evaluate(ctx, strategy.Input{
			MarketData: marketData,
			News:       opts.News,
			Analyser:   analyser,
			Portfolio:  strategy.Portfolio{Equity: equity, Cash: engine.Cash(), OpenPositions: engine.OpenPositions()},
		})

		for _, idea := range ideas {
			price, ok := prices[idea.Market]
			if !ok || price <= 0 {
				continue
			}
			cash := engine.Cash()
			decision := riskEngine.EvaluateTrade(idea, risk.EvaluateOptions{
				OpenPositions:   engine.OpenPositions(),
				AvailableCash:   &cash,
				MarketPrice:     &price,
				MarketVolume24h: marketData[idea.Market].Volume,
			})
			signals = append(signals, SignalDecision{
				Market:             idea.Market,
				StrategyID:         idea.StrategyID,
				Direction:          string(idea.Direction),
				Approved:           decision.Approved,
				Reason:             decision.Reason,
				VisibleCandleCount: visibleCount,
			})
			if !decision.Approved {
				continue
			}
			confidence := idea.Confidence
			engine.Execute(domain.ExecutionIntent{
				ApprovalRequestID: "backtest",
				Market:            idea.Market,
				Direction:         idea.Direction,
				Size:              idea.PositionSizingProposal * equity / price,
			}, price, paper.ExecuteOptions{
				StrategyID:       idea.StrategyID,
				SignalConfidence: &confidence,
				Environment:      "paper",
				TradeIdeaID:      idea.ID,
			})
		}
	}

	finalPrices := map[string]float64{}
	for m, candles := range opts.CandlesByMarket {
		if len(candles) > 0 {
			finalPrices[m] = candles[len(candles)-1].C
		}
	}
	for _, pos := range engine.OpenPositions() {
		price, ok := finalPrices[pos.Market]
		if !ok {
			price = pos.AvgPrice
		}
		engine.ClosePosition(pos.PositionID, price, paper.CloseOptions{ApprovalRequestID: "backtest_final_close"})
	}

	endingEquity := engine.TotalEquity(finalPrices)
	equityCurve = append(equityCurve, endingEquity)
	orders := engine.Orders()
	closes := closeOrders(orders)
	fees := 0.0
	for _, f := range engine.Fills() {
		fees += f.Fee
	}
	stats := ComputeTradeStats(closedOrderPnLs(engine, closes))
	return Result{
		StrategyID:      opts.StrategyID,
		StartingEquity:  startingCapital,
		EndingEquity:    endingEquity,
		RealisedPnL:     endingEquity - startingCapital,
		Fees:            fees,
		MaxDrawdown:     MaxDrawdown(equityCurve),
		TradeCount:      len(closes),
		WinRate:         stats.WinRate,
		ProfitFactor:    stats.ProfitFactor,
		Orders:          orders,
		SignalDecisions: signals,
		EquityCurve:     equityCurve,
	}, nil
}
