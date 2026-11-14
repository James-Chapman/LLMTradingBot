package histdata

import (
	"archive/zip"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// at parses an RFC 3339 time for test fixtures.
func at(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("bad fixture time %q: %v", s, err)
	}
	return v.UTC()
}

// unixText formats a time as decimal UNIX seconds, the archive's timestamp form.
func unixText(v time.Time) string {
	sec := v.Unix()
	ns := v.Nanosecond()
	if ns == 0 {
		return strconv.FormatInt(sec, 10)
	}
	frac := strings.TrimRight(fmt.Sprintf("%09d", ns), "0")
	return strconv.FormatInt(sec, 10) + "." + frac
}

// tradeLine builds one 7-column archive row.
func tradeLine(v time.Time, price, volume, side string, id int64) string {
	return fmt.Sprintf("%s,%s,%s,%s,m,,%d", unixText(v), price, volume, side, id)
}

// archiveHeader is the header row of Kraken's time-and-sales CSV files.
const archiveHeader = "timestamp,price,volume,type,order_type,misc,trade_id"

// writeFile writes content to dir/name, creating directories.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// writeZip writes a zip archive holding the given name → content entries.
func writeZip(t *testing.T, path string, entries map[string]string, order []string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, name := range order {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(entries[name])); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// allBars collects every cached bar of market.
func allBars(t *testing.T, dir, market string) []Bar {
	t.Helper()
	var out []Bar
	far := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	for b, err := range NewCacheReader(dir).Bars(market, time.Time{}, far) {
		if err != nil {
			t.Fatalf("read bars: %v", err)
		}
		out = append(out, b)
	}
	return out
}

// openCache opens a cache in dir with a fixed import clock.
func openCache(t *testing.T, dir string) *Cache {
	t.Helper()
	c, err := OpenCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	c.Now = func() time.Time { return time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC) }
	return c
}

// fakeReader is an in-memory Reader for the series and universe tests.
type fakeReader struct {
	bars map[string][]Bar
}

// Markets lists the fake's markets, sorted.
func (f fakeReader) Markets() []string {
	var out []string
	for m := range f.bars {
		out = append(out, m)
	}
	slices.Sort(out)
	return out
}

// Coverage returns the first and last bar start of market.
func (f fakeReader) Coverage(market string) (time.Time, time.Time, bool) {
	b := f.bars[market]
	if len(b) == 0 {
		return time.Time{}, time.Time{}, false
	}
	return b[0].Start, b[len(b)-1].Start, true
}

// Bars streams market's bars in [from, to).
func (f fakeReader) Bars(market string, from, to time.Time) iter.Seq2[Bar, error] {
	return func(yield func(Bar, error) bool) {
		for _, b := range f.bars[market] {
			if b.Start.Before(from) || !b.Start.Before(to) {
				continue
			}
			if !yield(b, nil) {
				return
			}
		}
	}
}

// bar makes a one-trade bar at start with the given price and volume.
func bar(start time.Time, price, volume float64) Bar {
	return Bar{Start: start, Open: price, High: price, Low: price, Close: price, Volume: volume, Trades: 1,
		BuyVolume: volume, BuyNotional: price * volume}
}
