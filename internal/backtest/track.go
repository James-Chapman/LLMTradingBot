package backtest

import "time"

// DrawdownEpisode is one fall from a running peak. Pct is in percent of the
// peak. RecoveryTime is the first time the value was back at or above the
// peak; nil when it never recovered before the end.
type DrawdownEpisode struct {
	Amount       float64    `json:"amount"`
	Pct          float64    `json:"pct"`
	Peak         float64    `json:"peak"`
	PeakTime     time.Time  `json:"peak_time"`
	Trough       float64    `json:"trough"`
	TroughTime   time.Time  `json:"trough_time"`
	RecoveryTime *time.Time `json:"recovery_time"`
}

// DailyPoint is a value at the end of a UTC day (Time is the following
// midnight). The last point of a series is the final value at the end.
type DailyPoint struct {
	Time  time.Time `json:"time"`
	Value float64   `json:"value"`
}

// TickStats are measured on every 30-second tick of a run, so sampling the
// equity curve cannot hide a peak or a trough. The drawdown series starts at
// the starting capital and ends with the end-of-run valuation.
type TickStats struct {
	Ticks              int     `json:"ticks"`               // trading ticks observed
	TicksInMarket      int     `json:"ticks_in_market"`     // ticks with at least one open position
	AverageEquity      float64 `json:"average_equity"`      // mean tick equity
	AverageUtilisation float64 `json:"average_utilisation"` // mean of positions value / equity, a fraction
	MaxUtilisation     float64 `json:"max_utilisation"`

	MaxDrawdownByPct    *DrawdownEpisode `json:"max_drawdown_by_pct"`    // nil: never below a peak
	MaxDrawdownByAmount *DrawdownEpisode `json:"max_drawdown_by_amount"` // may be a different episode
	// The longest time spent below a previous peak: from the peak to the
	// recovery, or to the end when it never recovered.
	LongestDrawdownSeconds   int64     `json:"longest_drawdown_seconds"`
	LongestDrawdownStart     time.Time `json:"longest_drawdown_start"`
	LongestDrawdownEnd       time.Time `json:"longest_drawdown_end"`
	LongestDrawdownRecovered bool      `json:"longest_drawdown_recovered"`

	Daily []DailyPoint `json:"daily_equity"` // end-of-UTC-day values, then the final value
}

// tracker accumulates TickStats incrementally. The replay and the
// buy-and-hold benchmark use the same tracker so their drawdowns compare.
type tracker struct {
	start time.Time
	last  time.Time

	peak     float64
	peakTime time.Time
	inDD     bool
	episode  int

	byPct, byAmt     *DrawdownEpisode
	byPctEp, byAmtEp int

	longest          time.Duration
	longestStart     time.Time
	longestEnd       time.Time
	longestRecovered bool

	ticks, inMarket int
	eqSum, utilSum  float64
	utilMax         float64
	daily           []DailyPoint
}

// newTracker starts a tracker at start with the initial value as its peak.
func newTracker(start time.Time, value float64) *tracker {
	return &tracker{start: start, last: start, peak: value, peakTime: start}
}

// observe records one trading tick: exposure, utilisation and the value.
func (t *tracker) observe(at time.Time, equity, cash float64, open int) {
	t.ticks++
	t.eqSum += equity
	if open > 0 {
		t.inMarket++
	}
	if equity > 0 {
		u := (equity - cash) / equity
		t.utilSum += u
		t.utilMax = max(t.utilMax, u)
	}
	t.mark(at, equity)
}

// mark records a value for the drawdown and, at a UTC midnight after the
// start, for the daily series.
func (t *tracker) mark(at time.Time, value float64) {
	if value >= t.peak {
		if t.inDD {
			t.recover(at)
		}
		t.peak, t.peakTime = value, at
	} else {
		if !t.inDD {
			t.inDD = true
			t.episode++
		}
		amount := t.peak - value
		pct := 0.0
		if t.peak > 0 {
			pct = 100 * amount / t.peak
		}
		ep := DrawdownEpisode{Amount: amount, Pct: pct, Peak: t.peak, PeakTime: t.peakTime, Trough: value, TroughTime: at}
		if t.byPct == nil || pct > t.byPct.Pct {
			t.byPct, t.byPctEp = &ep, t.episode
		}
		if t.byAmt == nil || amount > t.byAmt.Amount {
			e := ep
			t.byAmt, t.byAmtEp = &e, t.episode
		}
	}
	if at.After(t.start) && at.Unix()%86400 == 0 {
		t.setDaily(at, value)
	}
	t.last = at
}

// setDaily sets the daily value at at, replacing a point already there.
func (t *tracker) setDaily(at time.Time, value float64) {
	if n := len(t.daily); n > 0 && t.daily[n-1].Time.Equal(at) {
		t.daily[n-1].Value = value
		return
	}
	t.daily = append(t.daily, DailyPoint{Time: at, Value: value})
}

// recover closes the current drawdown episode at at.
func (t *tracker) recover(at time.Time) {
	t.inDD = false
	if d := at.Sub(t.peakTime); d > t.longest {
		t.longest, t.longestStart, t.longestEnd, t.longestRecovered = d, t.peakTime, at, true
	}
	rec := at
	if t.byPct != nil && t.byPctEp == t.episode {
		t.byPct.RecoveryTime = &rec
	}
	if t.byAmt != nil && t.byAmtEp == t.episode {
		t.byAmt.RecoveryTime = &rec
	}
}

// final records the value at the end (after any end-of-run valuation); it
// is always the last point of the daily series.
func (t *tracker) final(at time.Time, value float64) {
	t.mark(at, value)
	t.setDaily(at, value)
}

// stats returns the accumulated statistics; an open drawdown counts up to
// the last value.
func (t *tracker) stats() TickStats {
	s := TickStats{
		Ticks: t.ticks, TicksInMarket: t.inMarket, MaxUtilisation: t.utilMax,
		MaxDrawdownByPct: t.byPct, MaxDrawdownByAmount: t.byAmt,
		Daily: append([]DailyPoint{}, t.daily...),
	}
	if t.ticks > 0 {
		s.AverageEquity = t.eqSum / float64(t.ticks)
		s.AverageUtilisation = t.utilSum / float64(t.ticks)
	}
	longest, start, end, recovered := t.longest, t.longestStart, t.longestEnd, t.longestRecovered
	if t.inDD {
		if d := t.last.Sub(t.peakTime); d > longest {
			longest, start, end, recovered = d, t.peakTime, t.last, false
		}
	}
	if longest > 0 {
		s.LongestDrawdownSeconds = int64(longest / time.Second)
		s.LongestDrawdownStart, s.LongestDrawdownEnd, s.LongestDrawdownRecovered = start, end, recovered
	}
	return s
}
