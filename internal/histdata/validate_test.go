package histdata

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// importOne imports a single CSV body for XBTEUR and returns the file summary.
func importOne(t *testing.T, dir, name, body string, opts ImportOptions) FileSummary {
	t.Helper()
	src := t.TempDir()
	p := writeFile(t, src, name, body)
	sum, err := openCache(t, dir).ImportArchive(context.Background(), p, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Files) != 1 {
		t.Fatalf("summary = %+v", sum)
	}
	return sum.Files[0]
}

// TestImportValidation covers the findings raised while importing trades.
func TestImportValidation(t *testing.T) {
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	line := func(offset time.Duration, price, volume, side string, id int64) string {
		return tradeLine(base.Add(offset), price, volume, side, id)
	}

	t.Run("GIVEN a clean input WHEN imported THEN no findings are raised", func(t *testing.T) {
		f := importOne(t, t.TempDir(), "XBTEUR.csv", strings.Join([]string{archiveHeader,
			line(1*time.Second, "100", "1", "b", 1),
			line(2*time.Second, "100.5", "1", "s", 2),
			line(40*time.Second, "101", "1", "b", 3),
		}, "\n")+"\n", ImportOptions{})
		if len(f.FindingCounts) != 0 || len(f.Findings) != 0 {
			t.Fatalf("findings = %v %+v", f.FindingCounts, f.Findings)
		}
	})

	cases := []struct {
		name     string
		kind     string
		rows     []string
		trades   int64
		fileName string
		opts     ImportOptions
	}{
		{name: "a duplicate trade id", kind: KindDuplicateID, trades: 3, rows: []string{
			line(1*time.Second, "100", "1", "b", 5), line(2*time.Second, "100", "1", "b", 5), line(3*time.Second, "100", "1", "b", 6)}},
		{name: "a zero price", kind: KindInvalidValue, trades: 1, rows: []string{
			line(1*time.Second, "0", "1", "b", 1), line(2*time.Second, "100", "1", "b", 2)}},
		{name: "a negative volume", kind: KindInvalidValue, trades: 1, rows: []string{
			line(1*time.Second, "100", "-1", "b", 1), line(2*time.Second, "100", "1", "b", 2)}},
		{name: "a non-finite price", kind: KindInvalidValue, trades: 1, rows: []string{
			line(1*time.Second, "NaN", "1", "b", 1), line(2*time.Second, "100", "1", "b", 2)}},
		{name: "an infinite volume", kind: KindInvalidValue, trades: 1, rows: []string{
			line(1*time.Second, "100", "+Inf", "b", 1), line(2*time.Second, "100", "1", "b", 2)}},
		{name: "a price jump beyond the factor", kind: KindPriceJump, trades: 3, rows: []string{
			line(1*time.Second, "100", "1", "b", 1), line(2*time.Second, "200", "1", "b", 2), line(3*time.Second, "100", "1", "b", 3)}},
		{name: "a timestamp before Kraken existed", kind: KindOutOfRange, trades: 1, rows: []string{
			"946684800,100,1,b,m,,1", line(2*time.Second, "100", "1", "b", 2)}},
		{name: "a timestamp in the future", kind: KindOutOfRange, trades: 1, rows: []string{
			line(1*time.Second, "100", "1", "b", 1), tradeLine(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), "100", "1", "b", 2)}},
		{name: "a trade outside the quarter named by the file", kind: KindOutOfRange, trades: 2, fileName: "Kraken_Trades_2026Q2/XBTEUR.csv", rows: []string{
			tradeLine(time.Date(2026, 6, 30, 23, 0, 0, 0, time.UTC), "100", "1", "b", 1), line(2*time.Second, "100", "1", "b", 2)}},
		{name: "a run of empty intervals longer than the threshold", kind: KindNoTrades, trades: 2, opts: ImportOptions{GapThreshold: time.Hour}, rows: []string{
			line(1*time.Second, "100", "1", "b", 1), line(2*time.Hour, "100", "1", "b", 2)}},
		{name: "an unparseable price", kind: KindInvalidValue, trades: 1, rows: []string{
			line(1*time.Second, "abc", "1", "b", 1), line(2*time.Second, "100", "1", "b", 2)}},
	}
	for _, tc := range cases {
		t.Run("GIVEN "+tc.name+" WHEN imported THEN a "+tc.kind+" finding is recorded", func(t *testing.T) {
			dir := t.TempDir()
			name := tc.fileName
			if name == "" {
				name = "XBTEUR.csv"
			}
			src := t.TempDir()
			p := writeFile(t, src, name, archiveHeader+"\n"+strings.Join(tc.rows, "\n")+"\n")
			root := p
			if tc.fileName != "" {
				root = filepath.Join(src, strings.Split(tc.fileName, "/")[0])
			}
			sum, err := openCache(t, dir).ImportArchive(context.Background(), root, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			f := sum.Files[0]
			if f.FindingCounts[tc.kind] == 0 {
				t.Fatalf("no %s finding: %v %+v", tc.kind, f.FindingCounts, f.Findings)
			}
			if f.Trades != tc.trades {
				t.Errorf("kept %d trades, want %d", f.Trades, tc.trades)
			}
			m, err := LoadManifest(dir)
			if err != nil {
				t.Fatal(err)
			}
			recorded := 0
			for _, month := range m.Markets["BTC/EUR"].Months {
				for _, s := range month.Sources {
					recorded += s.FindingCounts[tc.kind]
				}
			}
			if recorded == 0 {
				t.Errorf("finding not recorded in the manifest")
			}
		})
	}
}

// writeMonth writes bars into the cache's month file for market.
func writeMonth(t *testing.T, dir, market string, month time.Time, bars []Bar) {
	t.Helper()
	p := filepath.Join(dir, "bars", MarketDir(market), month.Format("2006-01")+".bars")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, encodeBarFile(month, bars), 0o644); err != nil {
		t.Fatal(err)
	}
}

// hasFinding reports whether report has a finding of kind for market.
func hasFinding(r VerifyReport, kind, market string) bool {
	for _, f := range r.Findings {
		if f.Kind == kind && (market == "" || f.Market == market) {
			return true
		}
	}
	return false
}

// TestVerify covers the on-demand checks over the cache.
func TestVerify(t *testing.T) {
	june := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	july := june.AddDate(0, 1, 0)
	opts := VerifyOptions{GapThreshold: time.Hour}

	t.Run("GIVEN clean continuous bars WHEN verified THEN there are no findings and coverage is full", func(t *testing.T) {
		dir := t.TempDir()
		writeMonth(t, dir, "BTC/EUR", july, []Bar{bar(july, 100, 1), bar(july.Add(BarInterval), 100.1, 1), bar(july.Add(2*BarInterval), 100.2, 1)})
		r, err := openCache(t, dir).Verify(opts)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Findings) != 0 {
			t.Fatalf("findings = %+v", r.Findings)
		}
		if len(r.Markets) != 1 || r.Markets[0].Bars != 3 || r.Markets[0].Coverage != 1 {
			t.Fatalf("coverage = %+v", r.Markets)
		}
	})

	t.Run("GIVEN a gap inside a month WHEN verified THEN it is reported as no trades", func(t *testing.T) {
		dir := t.TempDir()
		writeMonth(t, dir, "BTC/EUR", july, []Bar{bar(july, 100, 1), bar(july.Add(3*time.Hour), 100, 1)})
		var eth []Bar
		for k := range 7 {
			eth = append(eth, bar(july.Add(time.Duration(k)*30*time.Minute), 10, 1))
		}
		writeMonth(t, dir, "ETH/EUR", july, eth)
		r, _ := openCache(t, dir).Verify(opts)
		if !hasFinding(r, KindNoTrades, "BTC/EUR") || hasFinding(r, KindAllMarketsGap, "") || hasFinding(r, KindBoundaryGap, "") {
			t.Fatalf("findings = %+v", r.Findings)
		}
	})

	t.Run("GIVEN a gap crossing a month boundary WHEN verified THEN it is reported as a likely missing-data boundary gap", func(t *testing.T) {
		dir := t.TempDir()
		writeMonth(t, dir, "BTC/EUR", june, []Bar{bar(june.Add(time.Hour), 100, 1)})
		writeMonth(t, dir, "BTC/EUR", july.AddDate(0, 1, 0), []Bar{bar(july.AddDate(0, 1, 0), 100, 1)})
		r, _ := openCache(t, dir).Verify(opts)
		if !hasFinding(r, KindBoundaryGap, "BTC/EUR") {
			t.Fatalf("findings = %+v", r.Findings)
		}
	})

	t.Run("GIVEN every market silent over the same hours WHEN verified THEN an all-markets gap is reported", func(t *testing.T) {
		dir := t.TempDir()
		for _, m := range []string{"BTC/EUR", "ETH/EUR"} {
			writeMonth(t, dir, m, july, []Bar{bar(july, 100, 1), bar(july.Add(5*time.Hour), 100, 1)})
		}
		r, _ := openCache(t, dir).Verify(opts)
		if !hasFinding(r, KindAllMarketsGap, "") {
			t.Fatalf("findings = %+v", r.Findings)
		}
	})

	t.Run("GIVEN a corrupted month file WHEN verified THEN an error finding is raised", func(t *testing.T) {
		dir := t.TempDir()
		writeMonth(t, dir, "BTC/EUR", july, []Bar{bar(july, 100, 1)})
		p := filepath.Join(dir, "bars", "BTC-EUR", "2026-07.bars")
		data, _ := os.ReadFile(p)
		data[len(data)-1] ^= 0xff
		_ = os.WriteFile(p, data, 0o644)
		r, _ := openCache(t, dir).Verify(opts)
		if !hasFinding(r, KindCorruptFile, "BTC/EUR") || !r.HasErrors() {
			t.Fatalf("findings = %+v", r.Findings)
		}
	})

	t.Run("GIVEN an impossible bar WHEN verified THEN an invalid value finding is raised", func(t *testing.T) {
		dir := t.TempDir()
		b := bar(july, 100, 1)
		b.Low = 101
		writeMonth(t, dir, "BTC/EUR", july, []Bar{b})
		r, _ := openCache(t, dir).Verify(opts)
		if !hasFinding(r, KindInvalidValue, "BTC/EUR") {
			t.Fatalf("findings = %+v", r.Findings)
		}
	})

	t.Run("GIVEN a price jump between bars WHEN verified THEN it is flagged", func(t *testing.T) {
		dir := t.TempDir()
		writeMonth(t, dir, "BTC/EUR", july, []Bar{bar(july, 100, 1), bar(july.Add(BarInterval), 300, 1)})
		r, _ := openCache(t, dir).Verify(opts)
		if !hasFinding(r, KindPriceJump, "BTC/EUR") {
			t.Fatalf("findings = %+v", r.Findings)
		}
	})

	t.Run("GIVEN a month file the manifest does not list WHEN verified THEN it is flagged", func(t *testing.T) {
		dir := t.TempDir()
		src := t.TempDir()
		p := writeFile(t, src, "XBTEUR.csv", archiveHeader+"\n"+tradeLine(july.Add(time.Second), "100", "1", "b", 1)+"\n")
		c := openCache(t, dir)
		if _, err := c.ImportArchive(context.Background(), p, ImportOptions{}); err != nil {
			t.Fatal(err)
		}
		writeMonth(t, dir, "BTC/EUR", june, []Bar{bar(june, 100, 1)})
		r, _ := c.Verify(opts)
		if !hasFinding(r, KindNotInManifest, "BTC/EUR") {
			t.Fatalf("findings = %+v", r.Findings)
		}
	})

	ohlcvtFixture := func(t *testing.T, dir string, rows []string) string {
		return writeFile(t, dir, "XBTEUR_1.csv", strings.Join(rows, "\n")+"\n")
	}
	minuteBars := []Bar{
		{Start: july, Open: 100, High: 102, Low: 99, Close: 101, Volume: 2, Trades: 3},
		{Start: july.Add(BarInterval), Open: 101, High: 103, Low: 100, Close: 102, Volume: 1, Trades: 1},
		{Start: july.Add(time.Minute), Open: 102, High: 102, Low: 102, Close: 102, Volume: 0.5, Trades: 1},
	}
	row := func(start time.Time, o, h, l, c, v float64, n int) string {
		return fmt.Sprintf("%d,%g,%g,%g,%g,%g,%d", start.Unix(), o, h, l, c, v, n)
	}

	t.Run("GIVEN an OHLCVT file matching the locally built candles WHEN cross-checked THEN no mismatch is found", func(t *testing.T) {
		dir := t.TempDir()
		writeMonth(t, dir, "BTC/EUR", july, minuteBars)
		p := ohlcvtFixture(t, t.TempDir(), []string{row(july, 100, 103, 99, 102, 3, 4), row(july.Add(time.Minute), 102, 102, 102, 102, 0.5, 1)})
		r, err := openCache(t, dir).Verify(VerifyOptions{GapThreshold: time.Hour, OHLCVT: []OHLCVTCheck{{Market: "BTC/EUR", Minutes: 1, Path: p}}})
		if err != nil {
			t.Fatal(err)
		}
		if hasFinding(r, KindOHLCVTMismatch, "") || hasFinding(r, KindOHLCVTMissing, "") {
			t.Fatalf("findings = %+v", r.Findings)
		}
		if len(r.CrossChecks) != 1 || r.CrossChecks[0].Matched != 2 || r.CrossChecks[0].Convention != "open" {
			t.Fatalf("cross checks = %+v", r.CrossChecks)
		}
	})

	t.Run("GIVEN an OHLCVT file that disagrees WHEN cross-checked THEN the mismatch is reported", func(t *testing.T) {
		dir := t.TempDir()
		writeMonth(t, dir, "BTC/EUR", july, minuteBars)
		p := ohlcvtFixture(t, t.TempDir(), []string{row(july, 100, 103, 99, 102, 3.5, 4), row(july.Add(time.Minute), 102, 102, 102, 102, 0.5, 1)})
		r, _ := openCache(t, dir).Verify(VerifyOptions{GapThreshold: time.Hour, OHLCVT: []OHLCVTCheck{{Market: "BTC/EUR", Minutes: 1, Path: p}}})
		if !hasFinding(r, KindOHLCVTMismatch, "BTC/EUR") {
			t.Fatalf("findings = %+v", r.Findings)
		}
	})
}
