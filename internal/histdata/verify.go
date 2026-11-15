package histdata

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// VerifyOptions controls Cache.Verify.
type VerifyOptions struct {
	Markets      []string      // empty means every cached market
	GapThreshold time.Duration // 0 means 1 h
	JumpFactor   float64       // bar open vs previous close; 0 means 1.25
	OHLCVT       []OHLCVTCheck // optional cross-checks against Kraken's OHLCVT files
	// From and To limit the check to the month files overlapping [From, To)
	// (and the import findings of those months); zero means unbounded.
	From, To time.Time
}

// OHLCVTCheck names one Kraken OHLCVT CSV file to compare against.
type OHLCVTCheck struct {
	Market  string
	Minutes int // the file's candle interval
	Path    string
}

// MarketCoverage summarises one market's cached data.
type MarketCoverage struct {
	Market         string         `json:"market"`
	First          time.Time      `json:"first,omitzero"`
	Last           time.Time      `json:"last,omitzero"`
	Months         int            `json:"months"`
	Bars           int            `json:"bars"`
	Intervals      int64          `json:"intervals"` // 30 s intervals from First to Last inclusive
	Coverage       float64        `json:"coverage"`  // Bars / Intervals: share of intervals with a trade
	Trades         int64          `json:"trades"`
	Volume         float64        `json:"volume"`
	Gaps           int            `json:"gaps"` // runs of empty intervals at least the threshold long
	LongestGap     time.Duration  `json:"longest_gap"`
	Bytes          int64          `json:"bytes"`
	ImportFindings map[string]int `json:"import_findings,omitempty"` // counts recorded in the manifest at import
}

// CrossCheck summarises one OHLCVT comparison.
type CrossCheck struct {
	Market     string `json:"market"`
	Minutes    int    `json:"minutes"`
	Path       string `json:"path"`
	Convention string `json:"convention"` // "open" or "close": which timestamp convention matched the file
	Compared   int    `json:"compared"`
	Matched    int    `json:"matched"`
	Mismatched int    `json:"mismatched"`
	Missing    int    `json:"missing"`
}

// VerifyReport is the result of Cache.Verify.
type VerifyReport struct {
	Markets     []MarketCoverage `json:"markets"`
	CrossChecks []CrossCheck     `json:"cross_checks,omitempty"`
	Findings    []Finding        `json:"findings"`
	Counts      map[string]int   `json:"counts"`
}

// HasErrors reports whether any finding is an error.
func (r VerifyReport) HasErrors() bool { return hasErrors(r.Findings) }

// gap is one run of empty intervals.
type gap struct {
	from, to time.Time
}

// Verify re-checks the cache: file integrity and manifest agreement, bar
// sanity, price jumps between bars, gaps (classified as genuine no-trade
// periods, month/source boundary gaps, or all-market gaps), coverage, and
// optional OHLCVT cross-checks. It reports and never modifies anything.
func (c *Cache) Verify(opts VerifyOptions) (VerifyReport, error) {
	if opts.GapThreshold <= 0 {
		opts.GapThreshold = defaultGapThreshold
	}
	if opts.JumpFactor <= 1 {
		opts.JumpFactor = defaultJumpFactor
	}
	r := NewCacheReader(c.Dir)
	markets := opts.Markets
	if len(markets) == 0 {
		markets = r.Markets()
	}
	markets = slices.Sorted(slices.Values(markets))
	markets = slices.Compact(markets)
	checkManifest := manifestExists(c.Dir)

	var log findingLog
	report := VerifyReport{}
	gaps := map[string][]gap{}
	spans := map[string]gap{}
	for _, market := range markets {
		cov, mgaps, err := c.verifyMarket(r, market, opts, checkManifest, &log)
		if err != nil {
			return report, err
		}
		report.Markets = append(report.Markets, cov)
		gaps[market] = mgaps
		if cov.Bars > 0 {
			spans[market] = gap{from: cov.First, to: cov.Last}
		}
	}
	allMarketGaps(markets, gaps, spans, opts.GapThreshold, &log)
	for _, chk := range opts.OHLCVT {
		cc, err := crossCheckOHLCVT(r, chk, &log)
		if err != nil {
			return report, err
		}
		report.CrossChecks = append(report.CrossChecks, cc)
	}
	report.Findings, report.Counts = log.list, log.counts
	if report.Counts == nil {
		report.Counts = map[string]int{}
	}
	return report, nil
}

// verifyMarket checks one market's month files and returns its coverage and gaps.
func (c *Cache) verifyMarket(r *CacheReader, market string, opts VerifyOptions, checkManifest bool, log *findingLog) (MarketCoverage, []gap, error) {
	cov := MarketCoverage{Market: market}
	files, err := r.monthFiles(market)
	if err != nil {
		return cov, nil, err
	}
	var mr *MarketRecord
	if c.manifest != nil {
		mr = c.manifest.Markets[market]
	}
	if mr != nil {
		cov.ImportFindings = map[string]int{}
		for _, key := range sortedKeys(mr.Months) {
			if start, err := time.Parse("2006-01", key); err == nil && !inRange(start, opts.From, opts.To) {
				continue
			}
			for _, s := range mr.Months[key].Sources {
				for k, n := range s.FindingCounts {
					cov.ImportFindings[k] += n
				}
			}
		}
	}
	edges := sourceEdges(mr)
	var (
		prev *Bar
		gaps []gap
	)
	for _, f := range files {
		if !inRange(f.month, opts.From, opts.To) {
			continue
		}
		cov.Months++
		cov.Bytes += f.size
		data, err := os.ReadFile(f.path)
		if err != nil {
			return cov, nil, err
		}
		if checkManifest {
			key := monthKey(f.month)
			switch rec := monthRecord(mr, key); {
			case rec == nil:
				log.add(Finding{Kind: KindNotInManifest, Severity: SeverityWarning, Market: market, Time: f.month,
					Detail: fmt.Sprintf("%s is not listed in %s (import interrupted, or copied in by hand)", filepath.Base(f.path), ManifestName)})
			case rec.FileSHA256 != sha256Hex(data):
				log.add(Finding{Kind: KindNotInManifest, Severity: SeverityWarning, Market: market, Time: f.month,
					Detail: fmt.Sprintf("%s differs from the hash recorded in %s", filepath.Base(f.path), ManifestName)})
			}
		}
		_, bars, err := decodeBarFile(data)
		if err != nil {
			log.add(Finding{Kind: KindCorruptFile, Severity: SeverityError, Market: market, Time: f.month,
				Detail: fmt.Sprintf("%s: %v", filepath.Base(f.path), err)})
			continue
		}
		for i := range bars {
			b := bars[i]
			checkBar(market, b, log)
			if prev != nil {
				if ratio := b.Open / prev.Close; prev.Close > 0 && (ratio > opts.JumpFactor || ratio < 1/opts.JumpFactor) {
					log.add(Finding{Kind: KindPriceJump, Severity: SeverityWarning, Market: market, Time: b.Start,
						Detail: fmt.Sprintf("open %g after close %g (×%.3f, factor %.2f)", b.Open, prev.Close, ratio, opts.JumpFactor)})
				}
				g := gap{from: prev.Start.Add(BarInterval), to: b.Start}
				if d := g.to.Sub(g.from); d >= opts.GapThreshold {
					gaps = append(gaps, g)
					cov.Gaps++
					cov.LongestGap = max(cov.LongestGap, d)
					classifyGap(market, g, edges, log)
				}
			}
			if cov.Bars == 0 {
				cov.First = b.Start
			}
			cov.Last = b.Start
			cov.Bars++
			cov.Trades += int64(b.Trades)
			cov.Volume += b.Volume
			prev = &bars[i]
		}
	}
	if cov.Bars > 0 {
		cov.Intervals = int64(cov.Last.Sub(cov.First)/BarInterval) + 1
		cov.Coverage = float64(cov.Bars) / float64(cov.Intervals)
	}
	return cov, gaps, nil
}

// monthRecord returns the manifest record of a month, or nil.
func monthRecord(mr *MarketRecord, key string) *MonthRecord {
	if mr == nil {
		return nil
	}
	return mr.Months[key]
}

// sourceEdges lists the instants where one import source's coverage ends and
// another's begins, from the manifest.
func sourceEdges(mr *MarketRecord) []time.Time {
	if mr == nil {
		return nil
	}
	var out []time.Time
	for _, key := range sortedKeys(mr.Months) {
		for _, s := range mr.Months[key].Sources {
			out = append(out, s.CoverFrom, s.CoverTo)
		}
	}
	slices.SortFunc(out, func(a, b time.Time) int { return a.Compare(b) })
	return slices.CompactFunc(out, func(a, b time.Time) bool { return a.Equal(b) })
}

// classifyGap records g as a boundary gap (it reaches a month boundary or an
// import source's edge, so data may be missing) or as a genuine no-trade period.
func classifyGap(market string, g gap, edges []time.Time, log *findingLog) {
	f := Finding{Market: market, Time: g.from, End: g.to}
	boundary := monthStart(g.to).After(g.from) // a month starts inside (from, to]
	reason := "spans a month boundary"
	for _, e := range edges {
		if !boundary && !e.Before(g.from) && !e.After(g.to) {
			boundary, reason = true, "touches the edge of an import source"
		}
	}
	if boundary {
		f.Kind, f.Severity = KindBoundaryGap, SeverityWarning
		f.Detail = fmt.Sprintf("no trades for %v; it %s, so data may be missing", g.to.Sub(g.from), reason)
	} else {
		f.Kind, f.Severity = KindNoTrades, SeverityInfo
		f.Detail = fmt.Sprintf("no trades for %v", g.to.Sub(g.from))
	}
	log.add(f)
}

// allMarketGaps reports periods in which every market covering the period
// was silent at once — an exchange outage or missing data, not illiquidity.
// It needs at least two markets.
func allMarketGaps(markets []string, gaps map[string][]gap, spans map[string]gap, threshold time.Duration, log *findingLog) {
	if len(markets) < 2 {
		return
	}
	seen := map[[2]int64]bool{}
	for _, a := range markets {
		for _, g := range gaps[a] {
			common, others := g, 0
			for _, b := range markets {
				if b == a {
					continue
				}
				span, ok := spans[b]
				if !ok || span.from.After(common.from) || span.to.Before(common.to) {
					continue // b does not cover this period
				}
				others++
				var best *gap
				for _, h := range gaps[b] {
					lo, hi := maxTime(common.from, h.from), minTime(common.to, h.to)
					if hi.After(lo) {
						best = &gap{from: lo, to: hi}
						break
					}
				}
				if best == nil {
					others = -1
					break
				}
				common = *best
			}
			if others < 1 || common.to.Sub(common.from) < threshold {
				continue
			}
			key := [2]int64{common.from.Unix(), common.to.Unix()}
			if seen[key] {
				continue
			}
			seen[key] = true
			log.add(Finding{Kind: KindAllMarketsGap, Severity: SeverityWarning, Time: common.from, End: common.to,
				Detail: fmt.Sprintf("all %d covered markets had no trades for %v: likely an outage or missing data", others+1, common.to.Sub(common.from))})
		}
	}
}

// checkBar flags impossible bar values.
func checkBar(market string, b Bar, log *findingLog) {
	values := []float64{b.Open, b.High, b.Low, b.Close}
	ok := b.Trades > 0 && finite(b.Volume) && b.Volume > 0 && b.BuyVolume >= 0 && b.SellVolume >= 0 &&
		b.BuyVolume+b.SellVolume <= b.Volume*(1+1e-9)
	for _, v := range values {
		ok = ok && finite(v) && v > 0 && v >= b.Low && v <= b.High
	}
	if !ok {
		log.add(Finding{Kind: KindInvalidValue, Severity: SeverityError, Market: market, Time: b.Start,
			Detail: fmt.Sprintf("impossible bar O=%g H=%g L=%g C=%g V=%g trades=%d", b.Open, b.High, b.Low, b.Close, b.Volume, b.Trades)})
	}
}

// Candle is an N-minute OHLCV candle, as Kraken's OHLC endpoint returns it.
type Candle struct {
	Start  time.Time // interval start (Kraken's OHLC "time"), UTC, clock-aligned
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume float64
	Trades int
}

// Cross-check tolerances. Candle prices are trade prices, so they must match
// to rounding of the decimal text; volumes are sums, so they may differ in
// the last digits from summation order and the file's printed precision.
const (
	priceTolerance     = 1e-9 // relative
	volumeRelTolerance = 1e-6 // relative
	volumeAbsTolerance = 1e-8 // absolute, base units
)

// crossCheckOHLCVT compares locally built candles with a Kraken OHLCVT file
// (timestamp,open,high,low,close,volume,trades; no header). Kraken does not
// document whether the timestamp is the candle's open or close, so both are
// tried and the better-matching convention is reported.
func crossCheckOHLCVT(r Reader, chk OHLCVTCheck, log *findingLog) (CrossCheck, error) {
	cc := CrossCheck{Market: chk.Market, Minutes: chk.Minutes, Path: chk.Path}
	interval := time.Duration(chk.Minutes) * time.Minute
	if interval <= 0 || interval%BarInterval != 0 {
		return cc, fmt.Errorf("OHLCVT interval %d minutes is not usable", chk.Minutes)
	}
	theirs, err := readOHLCVT(chk.Path)
	if err != nil {
		return cc, err
	}
	if len(theirs) == 0 {
		return cc, nil
	}
	first, last, ok := r.Coverage(chk.Market)
	if !ok {
		return cc, fmt.Errorf("no cached data for %s", chk.Market)
	}
	ours := map[int64]Candle{}
	from := theirs[0].Start.Add(-interval)
	to := theirs[len(theirs)-1].Start.Add(2 * interval)
	for b, err := range r.Bars(chk.Market, from, to) {
		if err != nil {
			return cc, err
		}
		k := b.Start.Truncate(interval).Unix()
		c, seen := ours[k]
		if !seen {
			c = Candle{Start: b.Start.Truncate(interval), Open: b.Open, High: b.High, Low: b.Low}
		}
		c.High, c.Low = max(c.High, b.High), min(c.Low, b.Low)
		c.Close = b.Close
		c.Volume += b.Volume
		c.Trades += b.Trades
		ours[k] = c
	}
	score := func(shift time.Duration) int {
		n := 0
		for _, t := range theirs {
			if o, ok := ours[t.Start.Add(shift).Unix()]; ok && candlesMatch(o, t) {
				n++
			}
		}
		return n
	}
	shift := time.Duration(0)
	cc.Convention = "open"
	if score(-interval) > score(0) {
		shift, cc.Convention = -interval, "close"
	}
	matchedKeys := map[int64]bool{}
	for _, t := range theirs {
		start := t.Start.Add(shift)
		if start.Before(first.Truncate(interval)) || start.After(last) {
			continue // outside the cache's coverage
		}
		cc.Compared++
		o, ok := ours[start.Unix()]
		matchedKeys[start.Unix()] = true
		switch {
		case !ok:
			cc.Missing++
			log.add(Finding{Kind: KindOHLCVTMissing, Severity: SeverityWarning, Market: chk.Market, Time: start,
				Detail: fmt.Sprintf("%dm candle in %s has no trades in the cache", chk.Minutes, filepath.Base(chk.Path))})
		case candlesMatch(o, t):
			cc.Matched++
		default:
			cc.Mismatched++
			log.add(Finding{Kind: KindOHLCVTMismatch, Severity: SeverityWarning, Market: chk.Market, Time: start,
				Detail: fmt.Sprintf("%dm candle: built O=%g H=%g L=%g C=%g V=%g N=%d, file O=%g H=%g L=%g C=%g V=%g N=%d",
					chk.Minutes, o.Open, o.High, o.Low, o.Close, o.Volume, o.Trades, t.Open, t.High, t.Low, t.Close, t.Volume, t.Trades)})
		}
	}
	lo, hi := theirs[0].Start.Add(shift), theirs[len(theirs)-1].Start.Add(shift)
	for _, k := range slices.Sorted(maps.Keys(ours)) {
		start := time.Unix(k, 0).UTC()
		if matchedKeys[k] || start.Before(lo) || start.After(hi) {
			continue
		}
		cc.Missing++
		log.add(Finding{Kind: KindOHLCVTMissing, Severity: SeverityWarning, Market: chk.Market, Time: start,
			Detail: fmt.Sprintf("%dm candle built from trades is absent from %s", chk.Minutes, filepath.Base(chk.Path))})
	}
	return cc, nil
}

// candlesMatch compares two candles within the documented tolerances.
func candlesMatch(a, b Candle) bool {
	near := func(x, y float64) bool { return math.Abs(x-y) <= priceTolerance*math.Max(math.Abs(x), math.Abs(y)) }
	vol := math.Abs(a.Volume-b.Volume) <= math.Max(volumeAbsTolerance, volumeRelTolerance*math.Max(a.Volume, b.Volume))
	return near(a.Open, b.Open) && near(a.High, b.High) && near(a.Low, b.Low) && near(a.Close, b.Close) && vol && a.Trades == b.Trades
}

// readOHLCVT reads a Kraken OHLCVT CSV, sorted by time.
func readOHLCVT(path string) ([]Candle, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cr := newTradeCSV(f).r
	var out []Candle
	for line := 1; ; line++ {
		rec, err := cr.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if line == 1 && isHeader(rec) {
			continue
		}
		if len(rec) < 7 {
			return nil, fmt.Errorf("%s line %d: %d fields, expected 7 (timestamp,open,high,low,close,volume,trades)", path, line, len(rec))
		}
		ts, err := parseUnixTime(rec[0])
		if err != nil {
			return nil, fmt.Errorf("%s line %d: %w", path, line, err)
		}
		var v [5]float64
		for i := range v {
			if v[i], err = strconv.ParseFloat(strings.TrimSpace(rec[i+1]), 64); err != nil {
				return nil, fmt.Errorf("%s line %d: field %d %q is not a number", path, line, i+2, rec[i+1])
			}
		}
		n, err := strconv.Atoi(strings.TrimSpace(rec[6]))
		if err != nil {
			return nil, fmt.Errorf("%s line %d: trades %q is not an integer", path, line, rec[6])
		}
		out = append(out, Candle{Start: ts, Open: v[0], High: v[1], Low: v[2], Close: v[3], Volume: v[4], Trades: n})
	}
	slices.SortStableFunc(out, func(a, b Candle) int { return cmp.Compare(a.Start.UnixNano(), b.Start.UnixNano()) })
	return out, nil
}
