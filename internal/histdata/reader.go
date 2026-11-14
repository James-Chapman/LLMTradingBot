package histdata

import (
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// CacheReader implements Reader over a cache directory. It reads month files
// only, verifying each file's header and checksum before yielding its bars.
type CacheReader struct {
	dir string
}

// NewCacheReader returns a reader over the cache in dir.
func NewCacheReader(dir string) *CacheReader { return &CacheReader{dir: dir} }

var _ Reader = (*CacheReader)(nil)

// monthFile is one month file of a market.
type monthFile struct {
	month time.Time
	path  string
	size  int64
}

// Markets lists the markets with at least one month file, sorted.
func (r *CacheReader) Markets() []string {
	entries, err := os.ReadDir(filepath.Join(r.dir, "bars"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		market, ok := marketFromDir(e.Name())
		if !e.IsDir() || !ok {
			continue
		}
		if files, err := r.monthFiles(market); err == nil && len(files) > 0 {
			out = append(out, market)
		}
	}
	slices.Sort(out)
	return out
}

// monthFiles lists market's month files in month order.
func (r *CacheReader) monthFiles(market string) ([]monthFile, error) {
	dir := filepath.Join(r.dir, "bars", MarketDir(market))
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []monthFile
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".bars") {
			continue
		}
		month, err := time.Parse("2006-01", strings.TrimSuffix(name, ".bars"))
		if err != nil {
			continue
		}
		var size int64
		if info, err := e.Info(); err == nil {
			size = info.Size()
		}
		out = append(out, monthFile{month: month, path: filepath.Join(dir, name), size: size})
	}
	slices.SortFunc(out, func(a, b monthFile) int { return a.month.Compare(b.month) })
	return out, nil
}

// readMonthFile reads and verifies one month file.
func readMonthFile(f monthFile) ([]Bar, error) {
	data, err := os.ReadFile(f.path)
	if err != nil {
		return nil, err
	}
	month, bars, err := decodeBarFile(data)
	if err == nil && !month.Equal(f.month) {
		err = fmt.Errorf("header month %s does not match the file name", monthKey(month))
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", f.path, err)
	}
	return bars, nil
}

// MarketListing is one market's entry in Cache.List.
type MarketListing struct {
	Market string    `json:"market"`
	First  time.Time `json:"first,omitzero"`
	Last   time.Time `json:"last,omitzero"`
	Months int       `json:"months"`
	Bars   int       `json:"bars"` // from the month file headers
	Bytes  int64     `json:"bytes"`
}

// List summarises every cached market cheaply: it reads month file headers
// (and the first and last files for coverage), not every bar.
func (c *Cache) List() ([]MarketListing, error) {
	r := NewCacheReader(c.Dir)
	var out []MarketListing
	for _, market := range r.Markets() {
		files, err := r.monthFiles(market)
		if err != nil {
			return nil, err
		}
		l := MarketListing{Market: market, Months: len(files)}
		for _, f := range files {
			l.Bytes += f.size
			if f.size >= barHeaderSize {
				l.Bars += int((f.size - barHeaderSize) / barRecordSize)
			}
		}
		l.First, l.Last, _ = r.Coverage(market)
		out = append(out, l)
	}
	return out, nil
}

// Coverage returns the first and last bar start stored for market.
func (r *CacheReader) Coverage(market string) (first, last time.Time, ok bool) {
	files, err := r.monthFiles(market)
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	for _, f := range files {
		if bars, err := readMonthFile(f); err == nil && len(bars) > 0 {
			first, ok = bars[0].Start, true
			break
		}
	}
	for i := len(files) - 1; ok && i >= 0; i-- {
		if bars, err := readMonthFile(files[i]); err == nil && len(bars) > 0 {
			return first, bars[len(bars)-1].Start, true
		}
	}
	return time.Time{}, time.Time{}, false
}

// Bars streams market's bars with from <= Start < to in time order, reading
// only the month files that overlap the range. Each file is verified whole
// before any of its bars is yielded, so a consumer never sees corrupt bars;
// a damaged file ends the stream with its error.
func (r *CacheReader) Bars(market string, from, to time.Time) iter.Seq2[Bar, error] {
	return func(yield func(Bar, error) bool) {
		files, err := r.monthFiles(market)
		if err != nil {
			yield(Bar{}, err)
			return
		}
		for _, f := range files {
			if !f.month.AddDate(0, 1, 0).After(from) {
				continue
			}
			if !f.month.Before(to) {
				return
			}
			bars, err := readMonthFile(f)
			if err != nil {
				yield(Bar{}, err)
				return
			}
			for _, b := range bars {
				if b.Start.Before(from) {
					continue
				}
				if !b.Start.Before(to) {
					return
				}
				if !yield(b, nil) {
					return
				}
			}
		}
	}
}
