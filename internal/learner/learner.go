// Package learner adjusts signal confidence based on historical trade outcomes.
//
// It tracks win rate per (strategy_id, market, direction) using exponential
// recency weighting so recent trades count more than old ones, and applies no
// adjustment until at least MinSamples outcomes exist for a key.
package learner

import (
	"math"
	"sort"
	"strconv"
	"sync"

	"github.com/James-Chapman/LLMTradingBot/internal/observability"
)

const (
	// MinSamples is the minimum number of outcomes before adjustments kick in.
	MinSamples = 5
	// Decay is the weight of each older trade relative to the next newer one
	// (most recent = 1.0, previous = 0.92, ...).
	Decay = 0.92
)

var logger = observability.Logger("learner")

// Outcome is one closed-trade result used to seed the learner.
type Outcome struct {
	StrategyID string
	Market     string
	Direction  string
	Pnl        float64
	Notional   float64 // entry price × size of the trade; 0 when unknown
}

// key identifies one learner bucket.
type key struct {
	strategyID string
	market     string
	direction  string
}

// stats holds the outcome history for one key. The quality score and win
// rate are derived from it with recency weights; the raw P&L totals feed the
// dashboard's average win/loss columns.
type stats struct {
	rawCount     int
	winPnlTotal  float64
	winCount     int
	lossPnlTotal float64
	lossCount    int
	pnlHistory   []float64 // currency P&L, oldest first
	returns      []float64 // P&L as a fraction of notional (or currency P&L when unknown), oldest first
}

// tradeReturn returns P&L as a fraction of the trade's notional, so outcomes
// from small and large accounts compare alike. Without a notional the
// currency P&L is used; the score's normalisation still removes its scale.
func tradeReturn(pnl, notional float64) float64 {
	if notional > 0 {
		return pnl / notional
	}
	return pnl
}

// recordPnl appends one outcome to the history and the raw win/loss totals.
func (s *stats) recordPnl(pnl, notional float64) {
	s.pnlHistory = append(s.pnlHistory, pnl)
	s.returns = append(s.returns, tradeReturn(pnl, notional))
	s.rawCount++
	if pnl > 0 {
		s.winPnlTotal += pnl
		s.winCount++
	} else if pnl < 0 {
		s.lossPnlTotal += pnl
		s.lossCount++
	}
}

// meanWinPnl returns the average winning P&L, or 0 when there are no wins.
func (s *stats) meanWinPnl() float64 {
	if s.winCount == 0 {
		return 0
	}
	return s.winPnlTotal / float64(s.winCount)
}

// meanLossPnl returns the average losing P&L, or 0 when there are no losses.
func (s *stats) meanLossPnl() float64 {
	if s.lossCount == 0 {
		return 0
	}
	return s.lossPnlTotal / float64(s.lossCount)
}

// weightedWinRate returns the recency-weighted share of winning trades
// (newest weight 1.0, each older one Decay times the next), or ok=false when
// there are no outcomes.
func (s *stats) weightedWinRate() (float64, bool) {
	wins, total, weight := 0.0, 0.0, 1.0
	for i := len(s.pnlHistory) - 1; i >= 0; i-- {
		total += weight
		if s.pnlHistory[i] > 0 {
			wins += weight
		}
		weight *= Decay
	}
	if total == 0 {
		return 0, false
	}
	return wins / total, true
}

// weightedMeanReturns returns the recency-weighted mean return of winning
// and of losing trades (0 when there are none), using the same weights as
// weightedWinRate.
func (s *stats) weightedMeanReturns() (meanWin, meanLoss float64) {
	winSum, winWeight, lossSum, lossWeight, weight := 0.0, 0.0, 0.0, 0.0, 1.0
	for i := len(s.returns) - 1; i >= 0; i-- {
		switch r := s.returns[i]; {
		case r > 0:
			winSum += weight * r
			winWeight += weight
		case r < 0:
			lossSum += weight * r
			lossWeight += weight
		}
		weight *= Decay
	}
	if winWeight > 0 {
		meanWin = winSum / winWeight
	}
	if lossWeight > 0 {
		meanLoss = lossSum / lossWeight
	}
	return meanWin, meanLoss
}

// qualityScore returns a magnitude-aware expectancy score in [-1, 1]: the
// recency-weighted expectancy of a trade's return divided by the larger of
// the mean win and mean loss, so it does not depend on account or trade size.
func (s *stats) qualityScore() float64 {
	winRate, ok := s.weightedWinRate()
	if !ok {
		winRate = 0.5
	}
	meanWin, meanLoss := s.weightedMeanReturns()
	normaliser := math.Max(math.Abs(meanWin), math.Abs(meanLoss))
	if normaliser == 0 {
		return 0 // only break-even trades
	}
	expectancy := winRate*meanWin + (1.0-winRate)*meanLoss
	return math.Max(-1.0, math.Min(1.0, expectancy/normaliser))
}

// PnlStats is the P&L distribution for one learner key.
type PnlStats struct {
	Mean   float64 `json:"mean"`
	Median float64 `json:"median"`
	P25    float64 `json:"p25"`
	P50    float64 `json:"p50"`
	P75    float64 `json:"p75"`
	P95    float64 `json:"p95"`
}

// SummaryRow is one dashboard row of per-key learner stats.
type SummaryRow struct {
	Strategy         string  `json:"strategy"`
	Market           string  `json:"market"`
	Direction        string  `json:"direction"`
	Trades           int     `json:"trades"`
	WinRate          float64 `json:"win_rate"`
	RollingWinRate10 float64 `json:"rolling_win_rate_10"`
	MeanWinPnl       float64 `json:"mean_win_pnl"`
	MeanLossPnl      float64 `json:"mean_loss_pnl"`
	AveragePnl       float64 `json:"average_pnl"`
	MedianPnl        float64 `json:"median_pnl"`
	PnlP25           float64 `json:"pnl_p25"`
	PnlP75           float64 `json:"pnl_p75"`
	PnlP95           float64 `json:"pnl_p95"`
	QualityScore     float64 `json:"quality_score"`
}

// PerformanceLearner tracks outcomes per key; it is safe for concurrent use.
type PerformanceLearner struct {
	mu    sync.Mutex
	stats map[key]*stats
	order []key // insertion order, so Summary ties sort like Python's dict
}

// New returns an empty PerformanceLearner.
func New() *PerformanceLearner {
	return &PerformanceLearner{stats: map[key]*stats{}}
}

// statsFor returns the stats for k, creating them when absent. Caller holds mu.
func (l *PerformanceLearner) statsFor(k key) *stats {
	s, ok := l.stats[k]
	if !ok {
		s = &stats{}
		l.stats[k] = s
		l.order = append(l.order, k)
	}
	return s
}

// LoadFromOutcomes seeds the learner from outcome rows ordered newest-first (as read from the DB).
func (l *PerformanceLearner) LoadFromOutcomes(outcomes []Outcome) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Group by key, keeping first-seen order and newest-first row order.
	grouped := map[key][]Outcome{}
	var groupOrder []key
	for _, o := range outcomes {
		k := key{o.StrategyID, o.Market, o.Direction}
		if _, ok := grouped[k]; !ok {
			groupOrder = append(groupOrder, k)
		}
		grouped[k] = append(grouped[k], o)
	}

	for _, k := range groupOrder {
		s := l.statsFor(k)
		// Rows arrived newest-first; the history is kept oldest-first.
		rows := grouped[k]
		for i := len(rows) - 1; i >= 0; i-- {
			s.recordPnl(rows[i].Pnl, rows[i].Notional)
		}
	}

	if len(groupOrder) > 0 {
		logger.Info("Learner seeded from DB", "keys", len(groupOrder), "total_outcomes", len(outcomes))
	}
}

// RecordOutcome records a new outcome whose trade notional is unknown; prefer
// RecordOutcomeWithNotional so the score uses percentage returns.
func (l *PerformanceLearner) RecordOutcome(strategyID, market, direction string, pnl float64) {
	l.RecordOutcomeWithNotional(strategyID, market, direction, pnl, 0)
}

// RecordOutcomeWithNotional records a new outcome incrementally (no DB read
// needed). notional is the trade's entry price × size; 0 means unknown.
func (l *PerformanceLearner) RecordOutcomeWithNotional(strategyID, market, direction string, pnl, notional float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.statsFor(key{strategyID, market, direction}).recordPnl(pnl, notional)
}

// history returns the P&L history for a key, or nil. Caller holds mu.
func (l *PerformanceLearner) history(k key) []float64 {
	if s, ok := l.stats[k]; ok {
		return s.pnlHistory
	}
	return nil
}

// rollingWinRate returns the share of winning trades among the latest n P&L values.
func rollingWinRate(pnl []float64, n int) float64 {
	if n <= 0 || len(pnl) == 0 {
		return 0
	}
	trailing := pnl[max(0, len(pnl)-n):]
	wins := 0
	for _, p := range trailing {
		if p > 0 {
			wins++
		}
	}
	return float64(wins) / float64(len(trailing))
}

// RollingWinRate returns the win rate over the latest n outcomes for a key (0 when none).
func (l *PerformanceLearner) RollingWinRate(strategyID, market, direction string, n int) float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return rollingWinRate(l.history(key{strategyID, market, direction}), n)
}

// percentile returns the q-th percentile (0..100) of sorted values using
// numpy's "weibull" method (same as Python statistics.quantiles "exclusive").
func percentile(sorted []float64, q float64) float64 {
	n := len(sorted)
	// 0-based virtual index (n+1)q - 1, clipped to the data range.
	idx := math.Max(0, math.Min(float64(n-1), float64(n+1)*q/100-1))
	lo := int(math.Floor(idx))
	if lo >= n-1 {
		return sorted[n-1]
	}
	frac := idx - float64(lo)
	return sorted[lo] + frac*(sorted[lo+1]-sorted[lo])
}

// pnlStats computes the P&L distribution; ok is false when pnl is empty.
func pnlStats(pnl []float64) (PnlStats, bool) {
	if len(pnl) == 0 {
		return PnlStats{}, false
	}
	sorted := append([]float64(nil), pnl...)
	sort.Float64s(sorted)

	total := 0.0
	for _, p := range pnl {
		total += p
	}
	median := sorted[len(sorted)/2]
	if len(sorted)%2 == 0 {
		median = (sorted[len(sorted)/2-1] + sorted[len(sorted)/2]) / 2
	}
	return PnlStats{
		Mean:   total / float64(len(pnl)),
		Median: median,
		P25:    percentile(sorted, 25),
		P50:    percentile(sorted, 50),
		P75:    percentile(sorted, 75),
		P95:    percentile(sorted, 95),
	}, true
}

// PnlPercentiles returns the mean, median and percentile P&L for a key; ok is false when there is no history.
func (l *PerformanceLearner) PnlPercentiles(strategyID, market, direction string) (PnlStats, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return pnlStats(l.history(key{strategyID, market, direction}))
}

// AdjustConfidence scales confidence by historical quality for this signal type.
// Scale is 0.5x at quality -1, 1.0x at 0 and 1.5x at +1, capped at 0.95.
// Below MinSamples outcomes the base confidence is returned unchanged.
func (l *PerformanceLearner) AdjustConfidence(strategyID, market, direction string, baseConfidence float64) float64 {
	l.mu.Lock()
	defer l.mu.Unlock()

	k := key{strategyID, market, direction}
	s, ok := l.stats[k]
	if !ok || s.rawCount < MinSamples {
		return baseConfidence
	}

	quality := s.qualityScore()
	adjusted := math.Min(0.95, baseConfidence*(1.0+quality*0.5))
	logger.Debug("Confidence adjusted",
		"key", []string{strategyID, market, direction},
		"quality_score", roundTo(quality, 3),
		"base", baseConfidence,
		"adjusted", adjusted,
	)
	return adjusted
}

// roundTo rounds x to the given decimal places exactly like Python's round()
// (correctly rounded on the exact binary value, ties to even).
func roundTo(x float64, places int) float64 {
	rounded, err := strconv.ParseFloat(strconv.FormatFloat(x, 'f', places, 64), 64)
	if err != nil {
		return x
	}
	return rounded
}

// Summary returns dashboard rows of per-key stats, sorted by market. Never nil.
func (l *PerformanceLearner) Summary() []SummaryRow {
	l.mu.Lock()
	defer l.mu.Unlock()

	rows := []SummaryRow{}
	for _, k := range l.order {
		s := l.stats[k]
		if s.rawCount == 0 {
			continue
		}
		winRate, _ := s.weightedWinRate()
		dist, _ := pnlStats(s.pnlHistory)
		rows = append(rows, SummaryRow{
			Strategy:         k.strategyID,
			Market:           k.market,
			Direction:        k.direction,
			Trades:           s.rawCount,
			WinRate:          roundTo(winRate, 3),
			RollingWinRate10: roundTo(rollingWinRate(s.pnlHistory, 10), 3),
			MeanWinPnl:       roundTo(s.meanWinPnl(), 2),
			MeanLossPnl:      roundTo(s.meanLossPnl(), 2),
			AveragePnl:       roundTo(dist.Mean, 2),
			MedianPnl:        roundTo(dist.Median, 2),
			PnlP25:           roundTo(dist.P25, 2),
			PnlP75:           roundTo(dist.P75, 2),
			PnlP95:           roundTo(dist.P95, 2),
			QualityScore:     roundTo(s.qualityScore(), 3),
		})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Market < rows[j].Market })
	return rows
}
