// Package portfolio builds mark-to-market equity snapshots for the dashboard.
package portfolio

import (
	"math"
	"time"
)

// EquityPricedEngine is the execution engine contract needed for mark-to-market valuation.
type EquityPricedEngine interface {
	// GetCash returns the current cash balance.
	GetCash() float64
	// GetTotalEquity returns cash plus positions marked at prices.
	GetTotalEquity(prices map[string]float64) float64
}

// EquitySnapshot is one account valuation as stored by the dashboard equity history.
type EquitySnapshot struct {
	Timestamp      string  `json:"timestamp"`
	Equity         float64 `json:"equity"`
	Cash           float64 `json:"cash"`
	PositionsValue float64 `json:"positions_value"`
}

// EquityPoint is one point on the dashboard equity graph.
type EquityPoint struct {
	Timestamp string  `json:"timestamp"`
	Equity    float64 `json:"equity"`
}

// equityTolerance is how close the last graph point must be to count as current.
const equityTolerance = 0.000000001

// FormatISO formats t like Python's datetime.isoformat() for aware datetimes:
// microseconds only when non-zero and a "+00:00" style offset.
func FormatISO(t time.Time) string {
	t = t.Truncate(time.Microsecond)
	if t.Nanosecond() == 0 {
		return t.Format("2006-01-02T15:04:05-07:00")
	}
	return t.Format("2006-01-02T15:04:05.000000-07:00")
}

// timestampOrNow returns ts, or the current UTC time when ts is zero.
func timestampOrNow(ts time.Time) time.Time {
	if ts.IsZero() {
		return time.Now().UTC()
	}
	return ts
}

// BuildEquitySnapshot returns a mark-to-market account snapshot for the current
// price tick. A zero timestamp means "now".
func BuildEquitySnapshot(engine EquityPricedEngine, prices map[string]float64, timestamp time.Time) EquitySnapshot {
	equity := engine.GetTotalEquity(prices)
	cash := engine.GetCash()
	return EquitySnapshot{
		Timestamp:      FormatISO(timestampOrNow(timestamp)),
		Equity:         equity,
		Cash:           cash,
		PositionsValue: equity - cash,
	}
}

// AlignEquityHistoryWithCurrent returns a copy of history whose final point
// matches currentEquity, appending a new point when it does not. A zero
// timestamp means "now". The input slice is not modified.
func AlignEquityHistoryWithCurrent(history []EquityPoint, currentEquity float64, timestamp time.Time) []EquityPoint {
	aligned := make([]EquityPoint, len(history), len(history)+1)
	copy(aligned, history)

	if n := len(aligned); n > 0 && math.Abs(aligned[n-1].Equity-currentEquity) <= equityTolerance {
		aligned[n-1].Equity = currentEquity
		return aligned
	}

	return append(aligned, EquityPoint{
		Timestamp: FormatISO(timestampOrNow(timestamp)),
		Equity:    currentEquity,
	})
}
