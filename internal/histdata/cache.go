package histdata

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// DefaultCacheDir is the cache location, relative to the working directory.
// It is never the bot's database.
const DefaultCacheDir = "data/history"

// Cache is the local bar store: dir/bars/<BASE-QUOTE>/<YYYY-MM>.bars month
// files, dir/manifest.json, and dir/rest/<BASE-QUOTE>/ REST trade spools.
type Cache struct {
	Dir      string
	Now      func() time.Time // import timestamps; defaults to time.Now
	manifest *Manifest
}

// OpenCache opens (creating) the cache directory and loads its manifest.
func OpenCache(dir string) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	m, err := LoadManifest(dir)
	if err != nil {
		return nil, err
	}
	return &Cache{Dir: dir, Now: time.Now, manifest: m}, nil
}

// Manifest returns the cache's in-memory manifest.
func (c *Cache) Manifest() *Manifest { return c.manifest }

// now returns the import clock's time in UTC.
func (c *Cache) now() time.Time {
	if c.Now == nil {
		return time.Now().UTC()
	}
	return c.Now().UTC()
}

// monthKey formats a month as its file and manifest key.
func monthKey(month time.Time) string { return month.Format("2006-01") }

// monthStart returns the first instant of t's UTC month.
func monthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// monthRel is the month file path relative to the cache root.
func monthRel(market string, month time.Time) string {
	return filepath.Join("bars", MarketDir(market), monthKey(month)+".bars")
}

// restDir is the REST spool directory of market.
func (c *Cache) restDir(market string) string {
	return filepath.Join(c.Dir, "rest", MarketDir(market))
}

// readMonth returns the bars of an existing month file, or nil when absent.
func (c *Cache) readMonth(market string, month time.Time) ([]Bar, error) {
	path := filepath.Join(c.Dir, monthRel(market, month))
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	got, bars, err := decodeBarFile(data)
	if err == nil && !got.Equal(month) {
		err = fmt.Errorf("header month %s does not match the file name", monthKey(got))
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w; delete the file and re-import its sources", path, err)
	}
	return bars, nil
}

// applyMonth rebuilds one month file: existing bars outside rec's coverage
// are kept, those inside it are replaced by bars (so re-importing the same
// trades is idempotent), and the manifest's month record is updated.
func (c *Cache) applyMonth(market string, month time.Time, bars []Bar, rec SourceRecord) error {
	existing, err := c.readMonth(market, month)
	if err != nil {
		return err
	}
	merged := make([]Bar, 0, len(existing)+len(bars))
	for _, b := range existing {
		if b.Start.Before(rec.CoverFrom) || !b.Start.Before(rec.CoverTo) {
			merged = append(merged, b)
		}
	}
	merged = append(merged, bars...)
	slices.SortStableFunc(merged, func(a, b Bar) int { return a.Start.Compare(b.Start) })

	data := encodeBarFile(month, merged)
	rel := monthRel(market, month)
	if err := writeBytesAtomic(filepath.Join(c.Dir, rel), data); err != nil {
		return err
	}

	mr := c.manifest.month(market, monthKey(month))
	mr.File = filepath.ToSlash(rel)
	mr.FileSHA256 = sha256Hex(data)
	mr.Bars = len(merged)
	kept := mr.Sources[:0]
	for _, s := range mr.Sources {
		// A record wholly inside the new coverage has been superseded.
		superseded := !s.CoverFrom.Before(rec.CoverFrom) && !s.CoverTo.After(rec.CoverTo)
		if !superseded {
			kept = append(kept, s)
		}
	}
	mr.Sources = append(kept, rec)
	slices.SortStableFunc(mr.Sources, func(a, b SourceRecord) int {
		if c := a.CoverFrom.Compare(b.CoverFrom); c != 0 {
			return c
		}
		return cmp.Compare(a.Source, b.Source)
	})
	return nil
}
