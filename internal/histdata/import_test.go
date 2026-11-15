package histdata

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestMarketMapping covers how archive file names become markets.
func TestMarketMapping(t *testing.T) {
	m := MarketMapper{}
	cases := map[string]string{
		"XBTEUR.csv":            "BTC/EUR",
		"XXBTZEUR.csv":          "BTC/EUR",
		"XDGEUR.csv":            "DOGE/EUR",
		"ETH_EUR.csv":           "ETH/EUR",
		"eth-eur.CSV":           "ETH/EUR",
		"XAUTEUR.csv":           "XAUT/EUR",
		"ETHXBT.csv":            "ETH/BTC",
		"USDCUSD.csv":           "USDC/USD",
		"some/dir/SOLEUR.csv":   "SOL/EUR",
		"XETHZEUR.csv":          "ETH/EUR",
		"PEPEEUR.csv":           "PEPE/EUR",
		"Kraken/ADAUSDT.csv":    "ADA/USDT",
		"XXRPZUSD.csv":          "XRP/USD",
		"MATICEUR.csv":          "MATIC/EUR",
		"NIGHTEUR.csv":          "NIGHT/EUR",
		"XTZEUR.csv":            "XTZ/EUR",
		"WBTCXBT.csv":           "WBTC/BTC",
		"btc_eur.csv":           "BTC/EUR",
		"XBT-EUR.csv":           "BTC/EUR",
		"SHIB_EUR.csv":          "SHIB/EUR",
		"LUNA2EUR.csv":          "LUNA2/EUR",
		"1INCHEUR.csv":          "1INCH/EUR",
		"ZEUSEUR.csv":           "ZEUS/EUR",
		"XRPEUR.csv":            "XRP/EUR",
		"Q3/XDGUSD.csv":         "DOGE/USD",
		"EURTEUR.csv":           "EURT/EUR",
		"USDTEUR.csv":           "USDT/EUR",
		"ETHUSDC.csv":           "ETH/USDC",
		"DAIEUR.csv":            "DAI/EUR",
		"PYUSDEUR.csv":          "PYUSD/EUR",
		"AUDUSD.csv":            "AUD/USD",
		"SOLETH.csv":            "SOL/ETH",
		"ATOMXBT.csv":           "ATOM/BTC",
		"GBPUSD.csv":            "GBP/USD",
		"xbtgbp.csv":            "BTC/GBP",
		"ADACAD.csv":            "ADA/CAD",
		"BTCCHF.csv":            "BTC/CHF",
		"ETHJPY.csv":            "ETH/JPY",
		"ETHAUD.csv":            "ETH/AUD",
		"A_B_C_EUR.csv":         "ABC/EUR",
		"ABC.EUR.csv":           "ABC/EUR",
		"XBTEUR (copy).csv":     "",
		"README.csv":            "",
		"EUR.csv":               "",
		"FOO.csv":               "",
		"XBTEUR.txt":            "",
		"MANIFEST.json":         "",
		"Kraken_Trades.csv":     "",
		"ETH/":                  "",
		"":                      "",
		"..csv":                 "",
		"€EUR.csv":              "",
		"XBTEUR_1.csv":          "",
		"XBTEUR_60.csv":         "",
		"LONGNAMETOKEN_EUR.csv": "LONGNAMETOKEN/EUR",
	}
	for name, want := range cases {
		t.Run("GIVEN file "+name+" WHEN mapped THEN the market is "+want, func(t *testing.T) {
			got, ok := m.MarketForFile(name)
			if want == "" {
				if ok {
					t.Fatalf("expected %q to be unmapped, got %q", name, got)
				}
				return
			}
			if !ok || got != want {
				t.Fatalf("MarketForFile(%q) = %q, %v; want %q", name, got, ok, want)
			}
		})
	}

	t.Run("GIVEN an override WHEN the file is mapped THEN the override wins", func(t *testing.T) {
		m := MarketMapper{Overrides: map[string]string{"weirdname": "RENDER/EUR"}}
		got, ok := m.MarketForFile("dir/WEIRDNAME.csv")
		if !ok || got != "RENDER/EUR" {
			t.Fatalf("got %q %v", got, ok)
		}
	})

	t.Run("GIVEN a market WHEN the REST pair name is built THEN renamed assets use Kraken's altname", func(t *testing.T) {
		for market, want := range map[string]string{"BTC/EUR": "XBTEUR", "DOGE/EUR": "XDGEUR", "ETH/BTC": "ETHXBT", "SOL/EUR": "SOLEUR"} {
			got, err := RESTPairName(market)
			if err != nil || got != want {
				t.Errorf("RESTPairName(%q) = %q, %v; want %q", market, got, err, want)
			}
		}
		if _, err := RESTPairName("BTCEUR"); err == nil {
			t.Error("expected an error for a market without a slash")
		}
	})
}

// TestTimestampUnits covers the archive timestamp formats.
func TestTimestampUnits(t *testing.T) {
	cases := map[string]time.Time{
		"1688669597":           time.Unix(1688669597, 0).UTC(),
		"1688669597.8277369":   time.Unix(1688669597, 827736900).UTC(),
		"1559347203.7998":      time.Unix(1559347203, 799800000).UTC(),
		" 1559347203.5 ":       time.Unix(1559347203, 500000000).UTC(),
		"1688669597.123456789": time.Unix(1688669597, 123456789).UTC(),
		"1.6886695975e9":       time.Unix(1688669597, 500000000).UTC(),
	}
	for in, want := range cases {
		t.Run("GIVEN timestamp "+in+" WHEN parsed THEN it is the right instant", func(t *testing.T) {
			got, err := parseUnixTime(in)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(want) || got.Location() != time.UTC {
				t.Fatalf("parseUnixTime(%q) = %v, want %v", in, got, want)
			}
		})
	}
	for _, bad := range []string{"", "abc", "12.3.4", "12.x", "."} {
		t.Run("GIVEN malformed timestamp "+bad+" WHEN parsed THEN it is rejected", func(t *testing.T) {
			if _, err := parseUnixTime(bad); err == nil {
				t.Fatalf("expected an error for %q", bad)
			}
		})
	}
}

// boundaryFixture is a header CSV whose trades straddle a 30-second and a
// month boundary.
func boundaryFixture(t *testing.T) string {
	t.Helper()
	rows := []string{
		archiveHeader,
		tradeLine(at(t, "2026-06-30T23:59:01Z"), "100", "1", "b", 1),
		tradeLine(at(t, "2026-06-30T23:59:44.5Z"), "102", "2", "s", 2),
		tradeLine(at(t, "2026-06-30T23:59:59.999Z"), "101", "0.5", "b", 3),
		tradeLine(at(t, "2026-07-01T00:00:00Z"), "103", "1", "s", 4),
		tradeLine(at(t, "2026-07-01T00:00:29.9Z"), "104", "1", "b", 5),
		tradeLine(at(t, "2026-07-01T00:00:30Z"), "105", "3", "b", 6),
	}
	return strings.Join(rows, "\n") + "\n"
}

// TestImportArchive covers importing trade archives into the cache.
func TestImportArchive(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN trades straddling a 30-second and a month boundary WHEN imported THEN they land in the right bars and month files", func(t *testing.T) {
		src := t.TempDir()
		writeFile(t, src, "XBTEUR.csv", boundaryFixture(t))
		dir := t.TempDir()
		c := openCache(t, dir)
		sum, err := c.ImportArchive(ctx, src, ImportOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if len(sum.Files) != 1 || sum.Files[0].Market != "BTC/EUR" || sum.Files[0].Trades != 6 {
			t.Fatalf("summary = %+v", sum)
		}
		bars := allBars(t, dir, "BTC/EUR")
		if len(bars) != 4 {
			t.Fatalf("got %d bars: %+v", len(bars), bars)
		}
		if b := bars[0]; !b.Start.Equal(at(t, "2026-06-30T23:59:00Z")) || b.Open != 100 || b.Close != 100 || b.Trades != 1 || b.Volume != 1 {
			t.Errorf("first bar = %+v", b)
		}
		if b := bars[1]; !b.Start.Equal(at(t, "2026-06-30T23:59:30Z")) || b.Open != 102 || b.High != 102 || b.Low != 101 || b.Close != 101 || b.Trades != 2 || b.Volume != 2.5 {
			t.Errorf("second bar = %+v", b)
		}
		july := bars[2]
		if !july.Start.Equal(at(t, "2026-07-01T00:00:00Z")) || july.Open != 103 || july.Close != 104 || july.Trades != 2 {
			t.Errorf("third bar = %+v", july)
		}
		if !bars[3].Start.Equal(at(t, "2026-07-01T00:00:30Z")) || bars[3].Close != 105 {
			t.Errorf("fourth bar = %+v", bars[3])
		}
		for _, f := range []string{"2026-06.bars", "2026-07.bars"} {
			if _, err := os.Stat(filepath.Join(dir, "bars", "BTC-EUR", f)); err != nil {
				t.Errorf("month file %s missing: %v", f, err)
			}
		}
		if got := sum.Files[0].Months; !slices.Equal(got, []string{"2026-06", "2026-07"}) {
			t.Errorf("months = %v", got)
		}
	})

	t.Run("GIVEN buys and sells WHEN imported THEN the side totals are correct", func(t *testing.T) {
		src := t.TempDir()
		writeFile(t, src, "XBTEUR.csv", boundaryFixture(t))
		dir := t.TempDir()
		sum, err := openCache(t, dir).ImportArchive(ctx, src, ImportOptions{})
		if err != nil {
			t.Fatal(err)
		}
		f := sum.Files[0]
		if f.BuyVolume != 5.5 || f.SellVolume != 3 || f.UnknownSideVolume != 0 || f.UnknownSideTrades != 0 {
			t.Errorf("side totals = buy %v sell %v unknown %v", f.BuyVolume, f.SellVolume, f.UnknownSideVolume)
		}
		second := allBars(t, dir, "BTC/EUR")[1]
		if second.BuyVolume != 0.5 || second.BuyNotional != 50.5 || second.SellVolume != 2 || second.SellNotional != 204 {
			t.Errorf("second bar sides = %+v", second)
		}
	})

	t.Run("GIVEN an older archive without a header or side column WHEN imported THEN it imports and the unknown side is reported", func(t *testing.T) {
		src := t.TempDir()
		rows := []string{
			unixText(at(t, "2021-03-01T10:00:01Z")) + ",50000.5,0.25",
			unixText(at(t, "2021-03-01T10:00:02.5Z")) + ",50001,0.75",
		}
		writeFile(t, src, "XXBTZEUR.csv", strings.Join(rows, "\n")+"\n")
		dir := t.TempDir()
		sum, err := openCache(t, dir).ImportArchive(ctx, src, ImportOptions{})
		if err != nil {
			t.Fatal(err)
		}
		f := sum.Files[0]
		if f.Market != "BTC/EUR" || f.Trades != 2 || f.UnknownSideTrades != 2 || f.UnknownSideVolume != 1 {
			t.Fatalf("summary = %+v", f)
		}
		if f.FindingCounts[KindUnknownSide] != 1 {
			t.Errorf("expected a side_unknown finding, got %v", f.FindingCounts)
		}
		b := allBars(t, dir, "BTC/EUR")
		if len(b) != 1 || b[0].Volume != 1 || b[0].BuyVolume != 0 || b[0].SellVolume != 0 || b[0].Close != 50001 {
			t.Fatalf("bars = %+v", b)
		}
	})

	t.Run("GIVEN file names that cannot be mapped WHEN imported THEN they are reported and not imported", func(t *testing.T) {
		src := t.TempDir()
		writeFile(t, src, "FOO.csv", archiveHeader+"\n"+tradeLine(at(t, "2026-01-01T00:00:00Z"), "1", "1", "b", 1)+"\n")
		writeFile(t, src, "notes.txt", "hello")
		writeFile(t, src, "SOLEUR.csv", archiveHeader+"\n"+tradeLine(at(t, "2026-01-01T00:00:00Z"), "90", "1", "b", 1)+"\n")
		dir := t.TempDir()
		sum, err := openCache(t, dir).ImportArchive(ctx, src, ImportOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if len(sum.Unmapped) != 1 || !strings.HasSuffix(sum.Unmapped[0], "FOO.csv") {
			t.Errorf("unmapped = %v", sum.Unmapped)
		}
		if len(sum.Skipped) != 1 || !strings.HasSuffix(sum.Skipped[0], "notes.txt") {
			t.Errorf("skipped = %v", sum.Skipped)
		}
		if got := NewCacheReader(dir).Markets(); !slices.Equal(got, []string{"SOL/EUR"}) {
			t.Errorf("markets = %v", got)
		}
	})

	t.Run("GIVEN a market filter WHEN imported THEN only those markets are imported", func(t *testing.T) {
		src := t.TempDir()
		writeFile(t, src, "SOLEUR.csv", archiveHeader+"\n"+tradeLine(at(t, "2026-01-01T00:00:00Z"), "90", "1", "b", 1)+"\n")
		writeFile(t, src, "XBTEUR.csv", boundaryFixture(t))
		dir := t.TempDir()
		if _, err := openCache(t, dir).ImportArchive(ctx, src, ImportOptions{Markets: []string{"BTC/EUR"}}); err != nil {
			t.Fatal(err)
		}
		if got := NewCacheReader(dir).Markets(); !slices.Equal(got, []string{"BTC/EUR"}) {
			t.Errorf("markets = %v", got)
		}
	})

	t.Run("GIVEN a date range WHEN imported THEN only trades inside it are kept", func(t *testing.T) {
		src := t.TempDir()
		writeFile(t, src, "XBTEUR.csv", boundaryFixture(t))
		dir := t.TempDir()
		_, err := openCache(t, dir).ImportArchive(ctx, src, ImportOptions{From: at(t, "2026-07-01T00:00:00Z"), To: at(t, "2026-07-01T00:00:30Z")})
		if err != nil {
			t.Fatal(err)
		}
		b := allBars(t, dir, "BTC/EUR")
		if len(b) != 1 || !b[0].Start.Equal(at(t, "2026-07-01T00:00:00Z")) || b[0].Trades != 2 {
			t.Fatalf("bars = %+v", b)
		}
	})

	t.Run("GIVEN a zip archive WHEN imported THEN its CSV entries are read directly", func(t *testing.T) {
		src := t.TempDir()
		zipPath := filepath.Join(src, "Kraken_Trades_2026Q3.zip")
		writeZip(t, zipPath, map[string]string{
			"MANIFEST.json":     "{}",
			"trades/XBTEUR.csv": archiveHeader + "\n" + tradeLine(at(t, "2026-07-02T00:00:01Z"), "100", "1", "b", 1) + "\n",
			"trades/XDGEUR.csv": archiveHeader + "\n" + tradeLine(at(t, "2026-07-02T00:00:01Z"), "0.1", "100", "s", 1) + "\n",
		}, []string{"MANIFEST.json", "trades/XBTEUR.csv", "trades/XDGEUR.csv"})
		dir := t.TempDir()
		sum, err := openCache(t, dir).ImportArchive(ctx, zipPath, ImportOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if got := NewCacheReader(dir).Markets(); !slices.Equal(got, []string{"BTC/EUR", "DOGE/EUR"}) {
			t.Errorf("markets = %v", got)
		}
		if len(sum.Files) != 2 || sum.Files[0].Source != "Kraken_Trades_2026Q3.zip!trades/XBTEUR.csv" || len(sum.Files[0].SHA256) != 64 {
			t.Errorf("files = %+v", sum.Files)
		}
		if len(sum.Skipped) != 1 {
			t.Errorf("skipped = %v", sum.Skipped)
		}
	})

	t.Run("GIVEN the same trades WHEN imported twice THEN the cache is unchanged and holds no duplicates", func(t *testing.T) {
		src := t.TempDir()
		writeFile(t, src, "XBTEUR.csv", boundaryFixture(t))
		dir := t.TempDir()
		c := openCache(t, dir)
		if _, err := c.ImportArchive(ctx, src, ImportOptions{}); err != nil {
			t.Fatal(err)
		}
		before := allBars(t, dir, "BTC/EUR")
		fileBefore, _ := os.ReadFile(filepath.Join(dir, "bars", "BTC-EUR", "2026-07.bars"))
		if _, err := c.ImportArchive(ctx, src, ImportOptions{}); err != nil {
			t.Fatal(err)
		}
		after := allBars(t, dir, "BTC/EUR")
		fileAfter, _ := os.ReadFile(filepath.Join(dir, "bars", "BTC-EUR", "2026-07.bars"))
		if !slices.Equal(before, after) || !bytes.Equal(fileBefore, fileAfter) {
			t.Fatalf("re-import changed the cache: %+v vs %+v", before, after)
		}
		m, err := LoadManifest(dir)
		if err != nil {
			t.Fatal(err)
		}
		if n := len(m.Markets["BTC/EUR"].Months["2026-07"].Sources); n != 1 {
			t.Errorf("expected one source record after re-import, got %d", n)
		}
	})

	t.Run("GIVEN a later import overlapping part of a month WHEN imported THEN bars outside its coverage are kept", func(t *testing.T) {
		dir := t.TempDir()
		c := openCache(t, dir)
		a := t.TempDir()
		writeFile(t, a, "XBTEUR.csv", archiveHeader+"\n"+
			tradeLine(at(t, "2026-07-01T00:00:01Z"), "100", "1", "b", 1)+"\n"+
			tradeLine(at(t, "2026-07-01T00:10:01Z"), "101", "1", "b", 2)+"\n")
		if _, err := c.ImportArchive(ctx, a, ImportOptions{}); err != nil {
			t.Fatal(err)
		}
		b := t.TempDir()
		writeFile(t, b, "XBTEUR.csv", archiveHeader+"\n"+
			tradeLine(at(t, "2026-07-01T00:10:01Z"), "101", "1", "b", 2)+"\n"+
			tradeLine(at(t, "2026-07-01T00:20:01Z"), "102", "1", "b", 3)+"\n")
		if _, err := c.ImportArchive(ctx, filepath.Join(b, "XBTEUR.csv"), ImportOptions{}); err != nil {
			t.Fatal(err)
		}
		bars := allBars(t, dir, "BTC/EUR")
		if len(bars) != 3 || bars[0].Close != 100 || bars[1].Close != 101 || bars[2].Close != 102 || bars[1].Trades != 1 {
			t.Fatalf("bars = %+v", bars)
		}
	})

	t.Run("GIVEN trades out of time order WHEN imported THEN the file is rejected with the line number", func(t *testing.T) {
		src := t.TempDir()
		writeFile(t, src, "XBTEUR.csv", archiveHeader+"\n"+
			tradeLine(at(t, "2026-07-01T00:00:05Z"), "100", "1", "b", 1)+"\n"+
			tradeLine(at(t, "2026-07-01T00:00:01Z"), "100", "1", "b", 2)+"\n")
		dir := t.TempDir()
		_, err := openCache(t, dir).ImportArchive(ctx, src, ImportOptions{})
		if err == nil || !strings.Contains(err.Error(), "line 3") || !strings.Contains(err.Error(), "time order") {
			t.Fatalf("err = %v", err)
		}
		if got := NewCacheReader(dir).Markets(); len(got) != 0 {
			t.Errorf("a rejected file must not be cached, got %v", got)
		}
	})

	t.Run("GIVEN an import WHEN the manifest is read THEN it records provenance per month", func(t *testing.T) {
		src := t.TempDir()
		writeFile(t, src, "XBTEUR.csv", boundaryFixture(t))
		dir := t.TempDir()
		if _, err := openCache(t, dir).ImportArchive(ctx, src, ImportOptions{}); err != nil {
			t.Fatal(err)
		}
		prov, err := ProvenanceFor(dir, "BTC/EUR", at(t, "2026-07-01T00:00:00Z"), at(t, "2026-08-01T00:00:00Z"))
		if err != nil {
			t.Fatal(err)
		}
		if len(prov) != 1 || prov[0].Month != "2026-07" || prov[0].Bars != 2 || len(prov[0].FileSHA256) != 64 {
			t.Fatalf("provenance = %+v", prov)
		}
		s := prov[0].Sources[0]
		if s.Source != filepath.Base(src)+"/XBTEUR.csv" || s.Trades != 3 || len(s.SourceSHA256) != 64 ||
			!s.FirstTrade.Equal(at(t, "2026-07-01T00:00:00Z")) || !s.LastTrade.Equal(at(t, "2026-07-01T00:00:30Z")) ||
			!s.ImportedAt.Equal(time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)) {
			t.Errorf("source record = %+v", s)
		}
		m, _ := LoadManifest(dir)
		if m.Tool == "" || m.BarFileVersion != barFileVersion {
			t.Errorf("manifest header = %+v", m)
		}
	})

	t.Run("GIVEN a dry run WHEN imported THEN nothing is written", func(t *testing.T) {
		src := t.TempDir()
		writeFile(t, src, "XBTEUR.csv", boundaryFixture(t))
		dir := t.TempDir()
		sum, err := openCache(t, dir).ImportArchive(ctx, src, ImportOptions{DryRun: true})
		if err != nil || len(sum.Files) != 1 || sum.Files[0].Bars != 4 {
			t.Fatalf("summary = %+v, %v", sum, err)
		}
		if got := NewCacheReader(dir).Markets(); len(got) != 0 {
			t.Errorf("dry run wrote %v", got)
		}
	})
}

// TestCacheList covers the cache listing.
func TestCacheList(t *testing.T) {
	t.Run("GIVEN imported data WHEN the cache is listed THEN each market shows its coverage, months, bars and size", func(t *testing.T) {
		src := t.TempDir()
		writeFile(t, src, "XBTEUR.csv", boundaryFixture(t))
		dir := t.TempDir()
		c := openCache(t, dir)
		if _, err := c.ImportArchive(context.Background(), src, ImportOptions{}); err != nil {
			t.Fatal(err)
		}
		got, err := c.List()
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 {
			t.Fatalf("listing = %+v", got)
		}
		m := got[0]
		if m.Market != "BTC/EUR" || m.Months != 2 || m.Bars != 4 || m.Bytes <= 0 ||
			!m.First.Equal(at(t, "2026-06-30T23:59:00Z")) || !m.Last.Equal(at(t, "2026-07-01T00:00:30Z")) {
			t.Fatalf("listing = %+v", m)
		}
	})
}
