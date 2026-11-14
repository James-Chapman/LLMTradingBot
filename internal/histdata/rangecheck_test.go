package histdata

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// TestCheckRange covers what the cache can say about a backtest's range
// before the run: missing months, partial coverage and the data findings.
func TestCheckRange(t *testing.T) {
	jan := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	feb, mar, apr := jan.AddDate(0, 1, 0), jan.AddDate(0, 2, 0), jan.AddDate(0, 3, 0)

	t.Run("GIVEN a market with January and March but no February WHEN a range over all three is checked THEN February is a missing month, and a January-only range has none", func(t *testing.T) {
		dir := t.TempDir()
		writeMonth(t, dir, "BTC/EUR", jan, []Bar{bar(jan, 100, 1), bar(jan.Add(time.Hour), 100, 1)})
		writeMonth(t, dir, "BTC/EUR", mar, []Bar{bar(mar, 100, 1), bar(mar.Add(time.Hour), 100, 1)})
		c := openCache(t, dir)
		rep, err := c.CheckRange([]string{"BTC/EUR"}, jan, apr)
		if err != nil {
			t.Fatal(err)
		}
		if len(rep.Markets) != 1 || !slices.Equal(rep.Markets[0].MissingMonths, []string{"2026-02"}) {
			t.Fatalf("coverage %+v, want February missing", rep.Markets)
		}
		if !rep.HasGaps() {
			t.Fatal("a missing month must count as a gap")
		}
		rep, err = c.CheckRange([]string{"BTC/EUR"}, jan, jan.AddDate(0, 0, 10))
		if err != nil || len(rep.Markets[0].MissingMonths) != 0 || rep.HasGaps() {
			t.Fatalf("January only: %+v %v, want no missing month", rep.Markets, err)
		}
	})

	t.Run("GIVEN a market listed in March WHEN a range from January to the end of April is checked THEN it starts late and ends early but nothing is missing", func(t *testing.T) {
		dir := t.TempDir()
		writeMonth(t, dir, "NEW/EUR", mar, []Bar{bar(mar.Add(48*time.Hour), 5, 1), bar(mar.Add(72*time.Hour), 5, 1)})
		rep, err := openCache(t, dir).CheckRange([]string{"NEW/EUR"}, jan, apr.AddDate(0, 1, 0))
		if err != nil {
			t.Fatal(err)
		}
		m := rep.Markets[0]
		if !m.StartsLate || !m.EndsEarly || len(m.MissingMonths) != 0 || rep.HasGaps() || !m.First.Equal(mar.Add(48*time.Hour)) {
			t.Fatalf("coverage %+v, want a late start and an early end without missing months", m)
		}
	})

	t.Run("GIVEN a market with no data WHEN checked THEN it has no coverage at all", func(t *testing.T) {
		rep, err := openCache(t, t.TempDir()).CheckRange([]string{"NONE/EUR"}, jan, feb)
		if err != nil || rep.Markets[0].HasData || !rep.Markets[0].StartsLate {
			t.Fatalf("coverage %+v %v, want no data", rep.Markets, err)
		}
	})

	t.Run("GIVEN an import with a duplicate trade id and an implausible timestamp in July WHEN July is checked THEN the duplicate is counted and July is an error month, but June is clean", func(t *testing.T) {
		dir := t.TempDir()
		july := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
		june := july.AddDate(0, -1, 0)
		rows := []string{archiveHeader,
			tradeLine(june.Add(time.Hour), "100", "1", "b", 1),
			tradeLine(july.Add(time.Second), "100", "1", "b", 5),
			tradeLine(july.Add(2*time.Second), "100", "1", "s", 5), // duplicate id
			"1.0,100,1,b,m,,6", // 1970: implausible, skipped as an error
			tradeLine(july.Add(time.Hour), "100", "1", "b", 7),
		}
		importOne(t, dir, "XBTEUR.csv", strings.Join(rows, "\n")+"\n", ImportOptions{})
		c := openCache(t, dir)
		rep, err := c.CheckRange([]string{"BTC/EUR"}, july, july.AddDate(0, 0, 5))
		if err != nil {
			t.Fatal(err)
		}
		if rep.Counts[KindDuplicateID] != 1 || !slices.Equal(rep.Markets[0].ErrorMonths, []string{"2026-07"}) || !rep.HasErrors() {
			t.Fatalf("report %+v, want one duplicate and July as an error month", rep)
		}
		rep, err = c.CheckRange([]string{"BTC/EUR"}, june, june.AddDate(0, 0, 5))
		if err != nil || rep.Counts[KindDuplicateID] != 0 || len(rep.Markets[0].ErrorMonths) != 0 || rep.HasErrors() {
			t.Fatalf("June report %+v %v, want no findings", rep, err)
		}
	})

	t.Run("GIVEN two markets silent at the same time across a month boundary WHEN the range is checked THEN the boundary and all-markets gaps are reported", func(t *testing.T) {
		dir := t.TempDir()
		for _, m := range []string{"BTC/EUR", "ETH/EUR"} {
			writeMonth(t, dir, m, jan, []Bar{bar(feb.Add(-4*time.Hour), 100, 1), bar(feb.Add(-3*time.Hour), 100, 1)})
			writeMonth(t, dir, m, feb, []Bar{bar(feb.Add(3*time.Hour), 100, 1), bar(feb.Add(4*time.Hour), 100, 1)})
		}
		rep, err := openCache(t, dir).CheckRange([]string{"BTC/EUR", "ETH/EUR"}, jan, mar)
		if err != nil {
			t.Fatal(err)
		}
		if rep.Counts[KindBoundaryGap] != 2 || rep.Counts[KindAllMarketsGap] != 1 {
			t.Fatalf("counts %v, want two boundary gaps and one all-markets gap", rep.Counts)
		}
	})
}
