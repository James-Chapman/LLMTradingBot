package backtest

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Thresholds behind the metrics and the honesty warnings (docs/14).
const (
	// MinDaysForAnnualised is the shortest period with an annualised return.
	MinDaysForAnnualised = 30
	// MinDailyReturnsForRatios and MinTradesForRatios gate Sharpe and Sortino.
	MinDailyReturnsForRatios = 60
	MinTradesForRatios       = 30
	// MeaningfulTrades and MeaningfulDays are the least a result needs before
	// it is called anything but anecdotal.
	MeaningfulTrades = 30
	MeaningfulDays   = 90
	// BreakevenTolerance: a round trip whose net P&L is within half a cent
	// of zero is breakeven, neither a win nor a loss.
	BreakevenTolerance = 0.005
	// daysPerYear annualises a 24/7 market.
	daysPerYear = 365
)

// Metrics are the headline numbers of one run. Amounts are in the quote
// currency; fields ending in Pct are percentages (5 = 5 %). A nil pointer
// means "not defined for this run"; the matching note says why.
type Metrics struct {
	Days                float64  `json:"days"`
	InitialCapital      float64  `json:"initial_capital"`
	FinalValue          float64  `json:"final_value"` // ending equity after the end-of-run valuation
	NetPnL              float64  `json:"net_pnl"`     // final value - initial capital
	NetReturnPct        float64  `json:"net_return_pct"`
	GrossPnL            float64  `json:"gross_pnl"`            // at reference prices, before fees and execution costs (see Bridge)
	TotalFees           float64  `json:"total_fees"`           // logged fees of filled orders
	TotalExecutionCost  float64  `json:"total_execution_cost"` // latency drift + spread + impact + rounding
	AnnualisedReturnPct *float64 `json:"annualised_return_pct"`
	AnnualisedNote      string   `json:"annualised_note,omitempty"`

	RoundTrips     int      `json:"round_trips"`       // closed positions, end-of-run valuations included
	EndOfRunCloses int      `json:"end_of_run_closes"` // of which valued at the end of the run
	Wins           int      `json:"wins"`
	Losses         int      `json:"losses"`
	Breakeven      int      `json:"breakeven"`
	WinRatePct     *float64 `json:"win_rate_pct"`
	AvgNetPnL      *float64 `json:"avg_net_pnl"`
	MedianNetPnL   *float64 `json:"median_net_pnl"`
	AvgWin         *float64 `json:"avg_win"`
	AvgLoss        *float64 `json:"avg_loss"` // negative
	PayoffRatio    *float64 `json:"payoff_ratio"`
	ProfitFactor   *float64 `json:"profit_factor"`
	// ProfitFactorNote explains a null profit factor or payoff ratio.
	ProfitFactorNote string `json:"profit_factor_note,omitempty"`

	MaxDrawdown              *DrawdownEpisode `json:"max_drawdown"`        // deepest in percent
	MaxDrawdownAmount        *DrawdownEpisode `json:"max_drawdown_amount"` // deepest in currency
	LongestDrawdownSeconds   int64            `json:"longest_drawdown_seconds"`
	LongestDrawdownStart     *time.Time       `json:"longest_drawdown_start"`
	LongestDrawdownRecovered bool             `json:"longest_drawdown_recovered"`
	DrawdownResolution       string           `json:"drawdown_resolution"`

	DailyReturns int      `json:"daily_returns"`
	Sharpe       *float64 `json:"sharpe"`
	Sortino      *float64 `json:"sortino"`
	RatioNote    string   `json:"ratio_note,omitempty"`

	ExposurePct       float64  `json:"exposure_pct"`        // share of ticks with any open position
	AvgUtilisationPct float64  `json:"avg_utilisation_pct"` // mean positions value / equity
	MaxUtilisationPct float64  `json:"max_utilisation_pct"`
	TradedValue       float64  `json:"traded_value"` // sum of filled order notionals
	Turnover          *float64 `json:"turnover"`     // traded value / average tick equity

	// Stale positions (see Result.StalePositions): NetPnLWorstCase values the
	// positions still stale at the end at zero instead of their last trade.
	StalePositions        int     `json:"stale_positions"`
	StaleAtEnd            int     `json:"stale_at_end"`
	NetPnLWorstCase       float64 `json:"net_pnl_worst_case"`
	NetReturnPctWorstCase float64 `json:"net_return_pct_worst_case"`
	// DataWarnings counts the data-coverage warnings of the run (Result.DataWarnings).
	DataWarnings int `json:"data_warnings"`
}

// ptr returns a pointer to v.
func ptr(v float64) *float64 { return &v }

// runDays returns the length of a run in days.
func runDays(res *Result) float64 { return res.End.Sub(res.Start).Hours() / 24 }

// ComputeMetrics derives the metrics of a run from its result alone.
func ComputeMetrics(res *Result) Metrics {
	b := ComputeBridge(res)
	m := Metrics{
		Days: runDays(res), InitialCapital: res.StartingCapital, FinalValue: res.EndingEquity,
		NetPnL: res.EndingEquity - res.StartingCapital, GrossPnL: b.GrossPnL, TotalFees: b.Fees,
		TotalExecutionCost: b.LatencyDrift + b.SpreadCost + b.ImpactCost + b.RoundingCost,
		DrawdownResolution: "every 30 s tick, plus the end-of-run valuation",
	}
	m.DataWarnings = len(res.DataWarnings)
	m.StalePositions, m.NetPnLWorstCase = len(res.StalePositions), res.EndingEquityWorstCase-res.StartingCapital
	for _, s := range res.StalePositions {
		if s.AtEnd {
			m.StaleAtEnd++
		}
	}
	if res.StartingCapital > 0 {
		m.NetReturnPct = 100 * m.NetPnL / res.StartingCapital
		m.NetReturnPctWorstCase = 100 * m.NetPnLWorstCase / res.StartingCapital
	}
	switch {
	case m.Days < MinDaysForAnnualised:
		m.AnnualisedNote = fmt.Sprintf("not annualised: the period is %.1f days, under %d days", m.Days, MinDaysForAnnualised)
	case res.StartingCapital <= 0 || res.EndingEquity <= 0:
		m.AnnualisedNote = "not annualised: the account was wiped out"
	default:
		m.AnnualisedReturnPct = ptr(100 * (math.Pow(res.EndingEquity/res.StartingCapital, daysPerYear/m.Days) - 1))
	}
	tradeStats(&m, res.Trades)
	drawdownStats(&m, res.TickStats)
	ratioStats(&m, res)

	ts := res.TickStats
	if ts.Ticks > 0 {
		m.ExposurePct = 100 * float64(ts.TicksInMarket) / float64(ts.Ticks)
	}
	m.AvgUtilisationPct, m.MaxUtilisationPct = 100*ts.AverageUtilisation, 100*ts.MaxUtilisation
	for _, o := range res.Orders {
		if o.Status == StatusFilled {
			m.TradedValue += o.Notional
		}
	}
	if ts.AverageEquity > 0 {
		m.Turnover = ptr(m.TradedValue / ts.AverageEquity)
	}
	return m
}

// tradeStats fills the per-round-trip statistics.
func tradeStats(m *Metrics, trades []ClosedTrade) {
	m.RoundTrips = len(trades)
	if len(trades) == 0 {
		m.ProfitFactorNote = "no round trips"
		return
	}
	pnls := make([]float64, 0, len(trades))
	sum, winSum, lossSum := 0.0, 0.0, 0.0
	for _, t := range trades {
		if t.ExitReason == ReasonEndOfRun || t.ExitReason == ReasonStaleLiquidation {
			m.EndOfRunCloses++
		}
		pnls = append(pnls, t.PnL)
		sum += t.PnL
		switch {
		case t.PnL > BreakevenTolerance:
			m.Wins++
			winSum += t.PnL
		case t.PnL < -BreakevenTolerance:
			m.Losses++
			lossSum += t.PnL
		default:
			m.Breakeven++
		}
	}
	n := float64(len(trades))
	m.WinRatePct = ptr(100 * float64(m.Wins) / n)
	m.AvgNetPnL = ptr(sum / n)
	m.MedianNetPnL = ptr(median(pnls))
	if m.Wins > 0 {
		m.AvgWin = ptr(winSum / float64(m.Wins))
	}
	if m.Losses > 0 {
		m.AvgLoss = ptr(lossSum / float64(m.Losses))
		m.ProfitFactor = ptr(winSum / -lossSum)
	} else {
		m.ProfitFactorNote = "no losing round trip: the profit factor and payoff ratio are undefined"
	}
	if m.AvgWin != nil && m.AvgLoss != nil {
		m.PayoffRatio = ptr(*m.AvgWin / -*m.AvgLoss)
	} else if m.ProfitFactorNote == "" {
		m.ProfitFactorNote = "no winning round trip: the payoff ratio is undefined"
	}
}

// median returns the median of values (which it sorts).
func median(values []float64) float64 {
	sort.Float64s(values)
	n := len(values)
	if n%2 == 1 {
		return values[n/2]
	}
	return (values[n/2-1] + values[n/2]) / 2
}

// drawdownStats copies the per-tick drawdown statistics.
func drawdownStats(m *Metrics, ts TickStats) {
	m.MaxDrawdown, m.MaxDrawdownAmount = ts.MaxDrawdownByPct, ts.MaxDrawdownByAmount
	m.LongestDrawdownSeconds, m.LongestDrawdownRecovered = ts.LongestDrawdownSeconds, ts.LongestDrawdownRecovered
	if ts.LongestDrawdownSeconds > 0 {
		start := ts.LongestDrawdownStart
		m.LongestDrawdownStart = &start
	}
}

// dailyReturns returns the end-of-UTC-day returns, the first one against the
// starting capital.
func dailyReturns(start float64, daily []DailyPoint) []float64 {
	out := make([]float64, 0, len(daily))
	prev := start
	for _, p := range daily {
		if prev > 0 {
			out = append(out, p.Value/prev-1)
		}
		prev = p.Value
	}
	return out
}

// ratioStats computes Sharpe and Sortino from daily returns (risk-free 0,
// annualised with the square root of 365), only with enough data.
func ratioStats(m *Metrics, res *Result) {
	r := dailyReturns(res.StartingCapital, res.TickStats.Daily)
	m.DailyReturns = len(r)
	if len(r) < MinDailyReturnsForRatios || m.RoundTrips < MinTradesForRatios {
		m.RatioNote = fmt.Sprintf("Sharpe and Sortino are reported only with at least %d daily returns and %d round trips (this run: %d and %d)",
			MinDailyReturnsForRatios, MinTradesForRatios, len(r), m.RoundTrips)
		return
	}
	mean, sq, down := 0.0, 0.0, 0.0
	for _, x := range r {
		mean += x
	}
	mean /= float64(len(r))
	for _, x := range r {
		sq += (x - mean) * (x - mean)
		if x < 0 {
			down += x * x
		}
	}
	sd := math.Sqrt(sq / float64(len(r)-1))
	dd := math.Sqrt(down / float64(len(r)))
	var notes []string
	if sd > 0 {
		m.Sharpe = ptr(mean / sd * math.Sqrt(daysPerYear))
	} else {
		notes = append(notes, "Sharpe undefined: daily returns have no variance")
	}
	if dd > 0 {
		m.Sortino = ptr(mean / dd * math.Sqrt(daysPerYear))
	} else {
		notes = append(notes, "Sortino undefined: no losing day")
	}
	m.RatioNote = strings.Join(notes, "; ")
}

// SignalStats counts the strategy's ideas and why those not traded were not.
type SignalStats struct {
	Ideas     int `json:"ideas"`     // ideas the strategy returned
	Submitted int `json:"submitted"` // ideas that became an order
	Filled    int `json:"filled"`    // of which the order filled
	// NotTraded counts the other ideas by decision outcome (confidence gate,
	// risk rejection, spot sell with no holding, ...).
	NotTraded map[string]int `json:"not_traded"`
	// RiskReasons counts risk rejections by the reason's text before any ':'.
	RiskReasons map[string]int `json:"risk_reasons"`
	// OrderRejections counts every order (signals and stops) not filled, by reason.
	OrderRejections map[string]int `json:"order_rejections"`
}

// ComputeSignals counts a run's ideas, orders and rejections.
func ComputeSignals(res *Result) SignalStats {
	s := SignalStats{NotTraded: map[string]int{}, RiskReasons: map[string]int{}, OrderRejections: map[string]int{}}
	status := map[string]string{}
	for _, o := range res.Orders {
		status[o.ID] = o.Status
		if o.Status != StatusFilled {
			s.OrderRejections[o.RejectReason]++
		}
	}
	for _, d := range res.Decisions {
		s.Ideas++
		if d.Outcome != OutcomeSubmitted {
			s.NotTraded[d.Outcome]++
			if d.Outcome == OutcomeRiskRejected {
				reason, _, _ := strings.Cut(d.RiskReason, ":")
				s.RiskReasons[strings.TrimSpace(reason)]++
			}
			continue
		}
		s.Submitted++
		if status[d.OrderID] == StatusFilled {
			s.Filled++
		}
	}
	return s
}
