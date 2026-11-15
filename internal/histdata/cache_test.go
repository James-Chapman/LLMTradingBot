package histdata

import (
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// TestBarFile covers the on-disk bar format and atomic writes.
func TestBarFile(t *testing.T) {
	month := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	awkward := []Bar{
		{Start: month, Open: 0.1 + 0.2, High: math.Nextafter(1, 2), Low: math.SmallestNonzeroFloat64, Close: 1e308,
			Volume: 1.0 / 3.0, Trades: 7, BuyVolume: math.Nextafter(0.5, 0), BuyNotional: 123456789.123456789,
			SellVolume: 2.5e-12, SellNotional: math.MaxFloat64},
		{Start: month.Add(29 * 24 * time.Hour), Open: 3, High: 4, Low: 2, Close: 3.3, Volume: 1, Trades: 1},
	}

	t.Run("GIVEN awkward float64 values WHEN written and read back THEN every bit round-trips", func(t *testing.T) {
		data := encodeBarFile(month, awkward)
		gotMonth, got, err := decodeBarFile(data)
		if err != nil {
			t.Fatal(err)
		}
		if !gotMonth.Equal(month) || len(got) != len(awkward) {
			t.Fatalf("month %v, %d bars", gotMonth, len(got))
		}
		for i := range got {
			a, b := got[i], awkward[i]
			same := a.Start.Equal(b.Start) && a.Trades == b.Trades
			for _, pair := range [][2]float64{{a.Open, b.Open}, {a.High, b.High}, {a.Low, b.Low}, {a.Close, b.Close},
				{a.Volume, b.Volume}, {a.BuyVolume, b.BuyVolume}, {a.BuyNotional, b.BuyNotional},
				{a.SellVolume, b.SellVolume}, {a.SellNotional, b.SellNotional}} {
				same = same && math.Float64bits(pair[0]) == math.Float64bits(pair[1])
			}
			if !same {
				t.Errorf("bar %d: got %+v want %+v", i, a, b)
			}
		}
	})

	t.Run("GIVEN a file with a flipped byte WHEN read THEN the corruption is detected", func(t *testing.T) {
		data := encodeBarFile(month, awkward)
		data[len(data)-3] ^= 0x40
		if _, _, err := decodeBarFile(data); err == nil {
			t.Fatal("expected a checksum error")
		}
	})

	t.Run("GIVEN a truncated file WHEN read THEN the damage is detected", func(t *testing.T) {
		data := encodeBarFile(month, awkward)
		if _, _, err := decodeBarFile(data[:len(data)-10]); err == nil {
			t.Fatal("expected a size error")
		}
		if _, _, err := decodeBarFile(data[:20]); err == nil {
			t.Fatal("expected a header error")
		}
	})

	t.Run("GIVEN a corrupted month file in the cache WHEN the reader streams it THEN the stream ends with an error", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "bars", "BTC-EUR", "2026-07.bars")
		data := encodeBarFile(month, awkward)
		data[barHeaderSize+5] ^= 0x01
		writeFile(t, filepath.Dir(p), filepath.Base(p), string(data))
		var gotErr error
		for _, err := range NewCacheReader(dir).Bars("BTC/EUR", month, month.AddDate(0, 1, 0)) {
			if err != nil {
				gotErr = err
			}
		}
		if gotErr == nil {
			t.Fatal("expected the reader to report the corruption")
		}
	})

	t.Run("GIVEN a write that fails part-way WHEN written atomically THEN no partial file is left and the old file survives", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "x.bars")
		if err := os.WriteFile(p, []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
		boom := errors.New("disk full")
		err := writeFileAtomic(p, func(w io.Writer) error {
			_, _ = w.Write([]byte("partial new content"))
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		got, _ := os.ReadFile(p)
		if string(got) != "old" {
			t.Errorf("old file changed to %q", got)
		}
		entries, _ := os.ReadDir(dir)
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		if !slices.Equal(names, []string{"x.bars"}) {
			t.Errorf("leftover files: %v", names)
		}
	})

	t.Run("GIVEN a write that fails WHEN no file existed THEN no file is created", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "new.bars")
		_ = writeFileAtomic(p, func(w io.Writer) error { return errors.New("fail") })
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("files left: %v", entries)
		}
	})
}

// TestCacheReader covers range queries over month files.
func TestCacheReader(t *testing.T) {
	dir := t.TempDir()
	june := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	july := june.AddDate(0, 1, 0)
	lastJune := july.Add(-BarInterval)
	juneBars := []Bar{bar(june, 1, 1), bar(lastJune.Add(-BarInterval), 2, 1), bar(lastJune, 3, 1)}
	julyBars := []Bar{bar(july, 4, 1), bar(july.Add(BarInterval), 5, 1), bar(july.Add(10*BarInterval), 6, 1)}
	for _, m := range []struct {
		month time.Time
		bars  []Bar
	}{{june, juneBars}, {july, julyBars}} {
		p := filepath.Join(dir, "bars", "BTC-EUR", m.month.Format("2006-01")+".bars")
		writeFile(t, filepath.Dir(p), filepath.Base(p), string(encodeBarFile(m.month, m.bars)))
	}
	r := NewCacheReader(dir)

	collect := func(from, to time.Time) []float64 {
		var out []float64
		for b, err := range r.Bars("BTC/EUR", from, to) {
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, b.Close)
		}
		return out
	}

	t.Run("GIVEN bars in two month files WHEN a range spanning both is read THEN exactly the bars in [from, to) come back in order", func(t *testing.T) {
		got := collect(lastJune.Add(-BarInterval), july.Add(10*BarInterval))
		if !slices.Equal(got, []float64{2, 3, 4, 5}) {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("GIVEN a range starting exactly on a bar WHEN read THEN that bar is included and the end bar excluded", func(t *testing.T) {
		if got := collect(july, july.Add(BarInterval)); !slices.Equal(got, []float64{4}) {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("GIVEN an empty range WHEN read THEN no bars come back", func(t *testing.T) {
		if got := collect(july.Add(2*BarInterval), july.Add(5*BarInterval)); len(got) != 0 {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("GIVEN the cache WHEN markets and coverage are asked THEN they reflect the files", func(t *testing.T) {
		if got := r.Markets(); !slices.Equal(got, []string{"BTC/EUR"}) {
			t.Fatalf("markets = %v", got)
		}
		first, last, ok := r.Coverage("BTC/EUR")
		if !ok || !first.Equal(june) || !last.Equal(july.Add(10*BarInterval)) {
			t.Fatalf("coverage = %v %v %v", first, last, ok)
		}
		if _, _, ok := r.Coverage("ETH/EUR"); ok {
			t.Fatal("expected no coverage for an absent market")
		}
	})

	t.Run("GIVEN a stream WHEN the consumer stops early THEN reading stops without error", func(t *testing.T) {
		n := 0
		for _, err := range r.Bars("BTC/EUR", june, july.AddDate(0, 1, 0)) {
			if err != nil {
				t.Fatal(err)
			}
			n++
			if n == 2 {
				break
			}
		}
		if n != 2 {
			t.Fatalf("n = %d", n)
		}
	})
}
