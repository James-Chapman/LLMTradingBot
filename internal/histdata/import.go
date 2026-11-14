package histdata

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ImportOptions controls an import. The zero value imports everything with
// the default validation thresholds.
type ImportOptions struct {
	Mapper       MarketMapper
	Markets      []string      // only these markets; empty means all
	From, To     time.Time     // keep trades in [From, To); zero means unbounded; floored to BarInterval
	JumpFactor   float64       // flag consecutive prices whose ratio exceeds this; 0 means 1.25
	GapThreshold time.Duration // report runs of empty intervals at least this long; 0 means 1 h
	DryRun       bool          // validate and summarise without writing the cache
	Progress     func(msg string)
}

// ImportSummary describes one ImportArchive call.
type ImportSummary struct {
	Files    []FileSummary `json:"files"`
	Unmapped []string      `json:"unmapped,omitempty"` // CSV files whose names map to no market
	Skipped  []string      `json:"skipped,omitempty"`  // non-CSV files, and CSVs excluded by the market filter
}

// FileSummary describes one imported source file.
type FileSummary struct {
	Source            string         `json:"source"`
	Market            string         `json:"market"`
	SHA256            string         `json:"sha256"`
	Trades            int64          `json:"trades"`        // trades kept
	Rejected          int64          `json:"rejected"`      // rows skipped as invalid or implausible
	OutsideRange      int64          `json:"outside_range"` // valid rows outside the From/To filter
	BuyVolume         float64        `json:"buy_volume"`    // taker-buy base volume
	SellVolume        float64        `json:"sell_volume"`   // taker-sell base volume
	UnknownSideTrades int64          `json:"unknown_side_trades"`
	UnknownSideVolume float64        `json:"unknown_side_volume"` // in Volume, in neither side total
	Bars              int            `json:"bars"`
	Months            []string       `json:"months"`
	First             time.Time      `json:"first,omitzero"`
	Last              time.Time      `json:"last,omitzero"`
	FindingCounts     map[string]int `json:"finding_counts,omitempty"`
	Findings          []Finding      `json:"findings,omitempty"`
}

// ImportArchive imports a directory of CSV files, a zip of them, or a single
// CSV file. Files are processed in name order; each is streamed once.
func (c *Cache) ImportArchive(ctx context.Context, path string, opts ImportOptions) (ImportSummary, error) {
	var sum ImportSummary
	info, err := os.Stat(path)
	if err != nil {
		return sum, err
	}
	if !info.IsDir() {
		if isZip(path) {
			return sum, c.importZip(ctx, path, opts, &sum)
		}
		f, err := c.importFile(ctx, path, filepath.Base(path), opts, &sum)
		if f != nil {
			sum.Files = append(sum.Files, *f)
		}
		return sum, err
	}
	root := filepath.Base(filepath.Clean(path))
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(path, p)
		name := root + "/" + filepath.ToSlash(rel)
		if isZip(p) {
			return c.importZip(ctx, p, opts, &sum)
		}
		f, err := c.importFile(ctx, p, name, opts, &sum)
		if f != nil {
			sum.Files = append(sum.Files, *f)
		}
		return err
	})
	return sum, err
}

// isZip reports whether path names a zip archive.
func isZip(path string) bool { return strings.EqualFold(filepath.Ext(path), ".zip") }

// selectMarket maps a file name to a market and applies the filter; ok is
// false (and display is recorded in sum) when the file is not imported.
func selectMarket(name, display string, opts ImportOptions, sum *ImportSummary) (string, bool) {
	if _, isCSV := fileStem(name); !isCSV {
		sum.Skipped = append(sum.Skipped, display)
		return "", false
	}
	market, ok := opts.Mapper.MarketForFile(name)
	if !ok {
		sum.Unmapped = append(sum.Unmapped, display)
		return "", false
	}
	if len(opts.Markets) > 0 && !slices.Contains(opts.Markets, market) {
		sum.Skipped = append(sum.Skipped, display)
		return "", false
	}
	return market, true
}

// importFile imports one CSV file from disk.
func (c *Cache) importFile(ctx context.Context, path, name string, opts ImportOptions, sum *ImportSummary) (*FileSummary, error) {
	market, ok := selectMarket(name, name, opts, sum)
	if !ok {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fs, err := c.importStream(ctx, name, market, f, opts, time.Time{})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return &fs, nil
}

// importZip imports every CSV entry of a zip archive, streaming each entry.
func (c *Cache) importZip(ctx context.Context, path string, opts ImportOptions, sum *ImportSummary) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	defer zr.Close()
	entries := slices.Clone(zr.File)
	slices.SortFunc(entries, func(a, b *zip.File) int { return strings.Compare(a.Name, b.Name) })
	for _, e := range entries {
		if e.FileInfo().IsDir() {
			continue
		}
		name := filepath.Base(path) + "!" + e.Name
		market, ok := selectMarket(e.Name, name, opts, sum)
		if !ok {
			continue
		}
		rc, err := e.Open()
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		fs, err := c.importStream(ctx, name, market, rc, opts, time.Time{})
		rc.Close() // archive/zip verifies the entry's CRC-32 at EOF
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		sum.Files = append(sum.Files, fs)
	}
	return nil
}

// quarterPattern finds a "2026Q3"-style period in an archive name.
var quarterPattern = regexp.MustCompile(`(20\d\d)Q([1-4])`)

// namedQuarter returns the quarter a source name declares, if any.
func namedQuarter(source string) (from, to time.Time, ok bool) {
	m := quarterPattern.FindStringSubmatch(source)
	if m == nil {
		return time.Time{}, time.Time{}, false
	}
	year, _ := strconv.Atoi(m[1])
	q, _ := strconv.Atoi(m[2])
	from = time.Date(year, time.Month(3*(q-1)+1), 1, 0, 0, 0, 0, time.UTC)
	return from, from.AddDate(0, 3, 0), true
}

// monthBuffer collects one month's bars and statistics while streaming.
type monthBuffer struct {
	start       time.Time
	bars        []Bar
	log         findingLog
	first, last time.Time
	trades      int64
	unknown     int64
}

// importStream imports one source's trades for market: validate each trade,
// build 30 s bars, and rebuild each month as soon as the stream moves past
// it (bars never span months, so a month switch closes the open bar).
// coverFrom, when set, declares the source complete from that instant (REST
// spools); otherwise coverage starts at the first kept trade's bar.
func (c *Cache) importStream(ctx context.Context, source, market string, r io.Reader, opts ImportOptions, coverFrom time.Time) (FileSummary, error) {
	from, to := opts.From.Truncate(BarInterval), opts.To.Truncate(BarInterval)
	hash := sha256.New()
	rows := newTradeCSV(io.TeeReader(r, hash))
	now := c.now()
	val := newTradeValidator(market, source, opts, now)
	gapThreshold := opts.GapThreshold
	if gapThreshold <= 0 {
		gapThreshold = defaultGapThreshold
	}
	sum := FileSummary{Source: source, Market: market}
	var (
		fileLog   findingLog
		orphans   findingLog // findings raised before the first kept trade
		builder   barBuilder
		month     *monthBuffer
		srcFrom   time.Time // start of this source's coverage
		prevBar   *Bar
		cutBefore bool // valid trades before From were dropped
		cutAfter  bool // valid trades at or after To were dropped
		written   bool
	)
	log := func(f Finding) {
		fileLog.add(f)
		if month != nil {
			month.log.add(f)
		} else {
			orphans.add(f)
		}
	}
	addBar := func(b Bar) {
		if prevBar != nil {
			if gap := b.Start.Sub(prevBar.Start.Add(BarInterval)); gap >= gapThreshold {
				log(Finding{Kind: KindNoTrades, Severity: SeverityInfo, Market: market, Time: prevBar.Start.Add(BarInterval), End: b.Start,
					Detail: fmt.Sprintf("no trades for %v", gap)})
			}
		}
		pb := b
		prevBar = &pb
		sum.Bars++
		month.bars = append(month.bars, b)
	}
	flush := func(coverTo time.Time) error {
		if b, ok := builder.flush(); ok {
			addBar(b)
		}
		rec := SourceRecord{
			Source: source, Tool: ToolVersion, ImportedAt: now,
			CoverFrom: maxTime(srcFrom, month.start), CoverTo: coverTo,
			FirstTrade: month.first, LastTrade: month.last, Trades: month.trades, Bars: len(month.bars),
			UnknownSideTrades: month.unknown, FindingCounts: month.log.counts, Findings: month.log.list,
		}
		sum.Months = append(sum.Months, monthKey(month.start))
		if opts.DryRun {
			return nil
		}
		written = true
		return c.applyMonth(market, month.start, month.bars, rec)
	}

	for n := int64(1); ; n++ {
		if n%10000 == 0 {
			if err := ctx.Err(); err != nil {
				return sum, err
			}
		}
		if n%1000000 == 0 && opts.Progress != nil {
			opts.Progress(fmt.Sprintf("%s: %d rows read, at %s", source, n, sum.Last.Format(time.RFC3339)))
		}
		row, issue, err := rows.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return sum, err
		}
		if issue != "" {
			sum.Rejected++
			log(Finding{Kind: KindInvalidValue, Severity: SeverityWarning, Market: market, Time: row.Time, Detail: issue + " (row skipped)"})
			continue
		}
		if !from.IsZero() && row.Time.Before(from) {
			sum.OutsideRange++
			cutBefore = true
			continue
		}
		if !to.IsZero() && !row.Time.Before(to) {
			sum.OutsideRange++
			cutAfter = true
			continue
		}
		var rowFindings []Finding
		keep, err := val.check(row, func(f Finding) { rowFindings = append(rowFindings, f) })
		if err != nil {
			return sum, err
		}
		if !keep {
			sum.Rejected++
			for _, f := range rowFindings {
				log(f)
			}
			continue
		}

		ms := monthStart(row.Time)
		if month != nil && !ms.Equal(month.start) {
			if err := flush(month.start.AddDate(0, 1, 0)); err != nil {
				return sum, err
			}
			month = nil
		}
		if month == nil {
			month = &monthBuffer{start: ms, first: row.Time}
			if sum.Trades == 0 {
				month.log.merge(orphans)
				sum.First = row.Time
				srcFrom = row.Time.Truncate(BarInterval)
				if cutBefore {
					srcFrom = from
				}
				if !coverFrom.IsZero() && coverFrom.Before(srcFrom) {
					srcFrom = coverFrom.Truncate(BarInterval)
				}
			}
		}
		for _, f := range rowFindings {
			log(f)
		}
		if done, ok := builder.add(row.Trade, row.SideKnown); ok {
			addBar(done)
		}
		val.countTrade(row)
		sum.Trades++
		sum.Last = row.Time
		month.trades++
		month.last = row.Time
		switch {
		case !row.SideKnown:
			sum.UnknownSideTrades++
			sum.UnknownSideVolume += row.Volume
			month.unknown++
		case row.Buy:
			sum.BuyVolume += row.Volume
		default:
			sum.SellVolume += row.Volume
		}
	}
	if sum.UnknownSideTrades > 0 {
		log(Finding{Kind: KindUnknownSide, Severity: SeverityInfo, Market: market,
			Detail: fmt.Sprintf("%d trades (%g base volume) have no taker side; they count in Volume but in neither side total", sum.UnknownSideTrades, sum.UnknownSideVolume)})
	}
	if month != nil {
		coverTo := sum.Last.Truncate(BarInterval).Add(BarInterval)
		if cutAfter {
			coverTo = minTime(to, month.start.AddDate(0, 1, 0))
		}
		if err := flush(coverTo); err != nil {
			return sum, err
		}
	}
	sum.SHA256 = hex.EncodeToString(hash.Sum(nil))
	sum.FindingCounts, sum.Findings = fileLog.counts, fileLog.list
	if written {
		c.stampSource(market, source, sum.SHA256, sum.Months)
		if err := c.manifest.save(c.Dir); err != nil {
			return sum, err
		}
	}
	return sum, nil
}

// stampSource records the source hash, known only at the end of the stream,
// on every month record the source just wrote.
func (c *Cache) stampSource(market, source, sha string, months []string) {
	mr := c.manifest.Markets[market]
	if mr == nil {
		return
	}
	for _, key := range months {
		rec := mr.Months[key]
		if rec == nil {
			continue
		}
		for i := range rec.Sources {
			if rec.Sources[i].Source == source && rec.Sources[i].SourceSHA256 == "" {
				rec.Sources[i].SourceSHA256 = sha
			}
		}
	}
}

// maxTime returns the later of a and b.
func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// minTime returns the earlier of a and b.
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// tradeValidator applies the per-trade checks while importing.
type tradeValidator struct {
	market, source   string
	jump             float64
	earliest, latest time.Time
	qFrom, qTo       time.Time
	hasQuarter       bool
	last             tradeRow
	seen             bool
}

// newTradeValidator prepares the checks for one source.
func newTradeValidator(market, source string, opts ImportOptions, now time.Time) *tradeValidator {
	v := &tradeValidator{market: market, source: source, jump: opts.JumpFactor}
	if v.jump <= 1 {
		v.jump = defaultJumpFactor
	}
	v.earliest, _ = time.Parse(time.RFC3339, plausibleEarliestUTC)
	v.latest = now.Add(24 * time.Hour)
	v.qFrom, v.qTo, v.hasQuarter = namedQuarter(source)
	return v
}

// check validates one row. keep is false for a row that must not enter the
// bars (implausible time); err aborts the import (time order broken).
func (v *tradeValidator) check(row tradeRow, log func(Finding)) (bool, error) {
	f := Finding{Market: v.market, Time: row.Time}
	if row.Time.Before(v.earliest) || row.Time.After(v.latest) {
		f.Kind, f.Severity = KindOutOfRange, SeverityError
		f.Detail = fmt.Sprintf("line %d: time %s is outside the plausible range %s..%s (row skipped)",
			row.Line, row.Time.Format(time.RFC3339Nano), v.earliest.Format(time.DateOnly), v.latest.Format(time.DateOnly))
		log(f)
		return false, nil
	}
	if v.seen && row.Time.Before(v.last.Time) {
		return false, fmt.Errorf("line %d: time %s is before the previous trade's %s; trades must be in time order (file rejected, nothing from it after the last completed month was written)",
			row.Line, row.Time.Format(time.RFC3339Nano), v.last.Time.Format(time.RFC3339Nano))
	}
	if v.hasQuarter && (row.Time.Before(v.qFrom) || !row.Time.Before(v.qTo)) {
		f.Kind, f.Severity = KindOutOfRange, SeverityWarning
		f.Detail = fmt.Sprintf("line %d: time %s is outside the quarter %s..%s named by the file (kept)",
			row.Line, row.Time.Format(time.RFC3339Nano), v.qFrom.Format(time.DateOnly), v.qTo.Format(time.DateOnly))
		log(f)
	}
	if v.seen && row.ID != 0 && v.last.ID != 0 && row.ID <= v.last.ID {
		f.Kind, f.Severity = KindDuplicateID, SeverityWarning
		f.Detail = fmt.Sprintf("line %d: trade id %d does not follow %d (kept)", row.Line, row.ID, v.last.ID)
		log(f)
	}
	if v.seen {
		if ratio := row.Price / v.last.Price; ratio > v.jump || ratio < 1/v.jump {
			f.Kind, f.Severity = KindPriceJump, SeverityWarning
			f.Detail = fmt.Sprintf("line %d: price %g after %g (×%.3f, factor %.2f; kept)", row.Line, row.Price, v.last.Price, ratio, v.jump)
			log(f)
		}
	}
	return true, nil
}

// countTrade makes row the previous trade for the next checks.
func (v *tradeValidator) countTrade(row tradeRow) {
	v.last, v.seen = row, true
}

// sortedKeys returns m's keys in order (for deterministic output).
func sortedKeys[V any](m map[string]V) []string { return slices.Sorted(maps.Keys(m)) }

// finite reports whether v is a finite number.
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
