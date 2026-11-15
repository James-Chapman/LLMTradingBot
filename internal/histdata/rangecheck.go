package histdata

import (
	"maps"
	"slices"
	"time"
)

// RangeCoverage is what the cache holds for one market, judged against a
// range a backtest is about to read.
type RangeCoverage struct {
	Market  string    `json:"market"`
	HasData bool      `json:"has_data"`
	First   time.Time `json:"first,omitzero"` // first bar stored (whole cache)
	Last    time.Time `json:"last,omitzero"`  // last bar stored (whole cache)
	// StartsLate and EndsEarly mark coverage that begins more than the gap
	// threshold (1 h) after the range starts, or ends more than that before
	// it ends: legitimate for a pair listed later or delisted earlier, but a
	// backtest must show it.
	StartsLate bool `json:"starts_late"`
	EndsEarly  bool `json:"ends_early"`
	// MissingMonths are months inside both the range and the market's
	// first-to-last coverage with no month file: missing data, not "no trades".
	MissingMonths []string `json:"missing_months,omitempty"`
	// ErrorMonths are months in the range whose current data carries an
	// error-level finding (from the import sources still in the manifest, or
	// from re-verifying the month file). Re-importing the month from a clean
	// source supersedes the source and resolves it.
	ErrorMonths []string `json:"error_months,omitempty"`
}

// RangeReport is the data check of a range: per-market coverage plus the
// data layer's findings for the months the range touches (Verify's findings
// and the import findings recorded in the manifest).
type RangeReport struct {
	From     time.Time       `json:"from"`
	To       time.Time       `json:"to"`
	Markets  []RangeCoverage `json:"markets"`
	Findings []Finding       `json:"findings"`
	Counts   map[string]int  `json:"counts"`
}

// HasGaps reports whether any market misses a month inside its coverage.
func (r RangeReport) HasGaps() bool {
	return slices.ContainsFunc(r.Markets, func(m RangeCoverage) bool { return len(m.MissingMonths) > 0 })
}

// HasErrors reports whether any market-month has an error-level finding.
func (r RangeReport) HasErrors() bool {
	return slices.ContainsFunc(r.Markets, func(m RangeCoverage) bool { return len(m.ErrorMonths) > 0 })
}

// CheckRange checks markets over [from, to) before a backtest reads them:
// coverage, missing months and the data findings of the months involved. It
// reads the month files in the range (to re-verify them) and never writes.
func (c *Cache) CheckRange(markets []string, from, to time.Time) (RangeReport, error) {
	markets = slices.Compact(slices.Sorted(slices.Values(markets)))
	rep := RangeReport{From: from, To: to, Markets: []RangeCoverage{}, Counts: map[string]int{}}
	vr, err := c.Verify(VerifyOptions{Markets: markets, From: from, To: to})
	if err != nil {
		return rep, err
	}
	log := findingLog{list: vr.Findings, counts: maps.Clone(vr.Counts)}
	r := NewCacheReader(c.Dir)
	for _, market := range markets {
		cov, err := c.rangeCoverage(r, market, from, to, &log)
		if err != nil {
			return rep, err
		}
		for _, f := range vr.Findings {
			if f.Market == market && f.Severity == SeverityError && inRange(monthStart(f.Time), from, to) {
				cov.ErrorMonths = append(cov.ErrorMonths, monthKey(monthStart(f.Time)))
			}
		}
		slices.Sort(cov.ErrorMonths)
		cov.ErrorMonths = slices.Compact(cov.ErrorMonths)
		rep.Markets = append(rep.Markets, cov)
	}
	rep.Findings, rep.Counts = log.list, log.counts
	if rep.Findings == nil {
		rep.Findings = []Finding{}
	}
	if rep.Counts == nil {
		rep.Counts = map[string]int{}
	}
	return rep, nil
}

// rangeCoverage returns one market's coverage of [from, to) and adds the
// import findings of the months it touches to log.
func (c *Cache) rangeCoverage(r *CacheReader, market string, from, to time.Time, log *findingLog) (RangeCoverage, error) {
	cov := RangeCoverage{Market: market}
	first, last, ok := r.Coverage(market)
	if !ok {
		cov.StartsLate, cov.EndsEarly = true, true
		return cov, nil
	}
	cov.HasData, cov.First, cov.Last = true, first, last
	cov.StartsLate = first.After(from.Add(defaultGapThreshold))
	cov.EndsEarly = last.Add(BarInterval).Before(to.Add(-defaultGapThreshold))

	files, err := r.monthFiles(market)
	if err != nil {
		return cov, err
	}
	present := map[string]bool{}
	for _, f := range files {
		present[monthKey(f.month)] = true
	}
	lo := maxTime(monthStart(first), monthStart(from))
	hi := minTime(monthStart(last), monthStart(to.Add(-time.Nanosecond)))
	for m := lo; !m.After(hi); m = m.AddDate(0, 1, 0) {
		if !present[monthKey(m)] {
			cov.MissingMonths = append(cov.MissingMonths, monthKey(m))
		}
	}

	var mr *MarketRecord
	if c.manifest != nil {
		mr = c.manifest.Markets[market]
	}
	if mr == nil {
		return cov, nil
	}
	for _, key := range sortedKeys(mr.Months) {
		start, err := time.Parse("2006-01", key)
		if err != nil || !inRange(start, from, to) {
			continue
		}
		for _, s := range mr.Months[key].Sources {
			log.merge(findingLog{list: s.Findings, counts: s.FindingCounts})
			if hasErrors(s.Findings) && !slices.Contains(cov.ErrorMonths, key) {
				cov.ErrorMonths = append(cov.ErrorMonths, key)
			}
		}
	}
	return cov, nil
}

// inRange reports whether the month starting at month overlaps [from, to);
// a zero bound is open.
func inRange(month, from, to time.Time) bool {
	if !from.IsZero() && !month.AddDate(0, 1, 0).After(from) {
		return false
	}
	return to.IsZero() || month.Before(to)
}
