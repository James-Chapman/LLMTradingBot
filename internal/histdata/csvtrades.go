package histdata

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
)

// tradeRow is one parsed archive row.
type tradeRow struct {
	Trade
	SideKnown bool  // false when the source has no taker side for this trade
	Line      int64 // 1-based line number in the source
}

// columns holds the index of each known column; -1 when absent.
type columns struct {
	ts, price, volume, side, id int
}

// width returns the minimum number of fields a row needs.
func (c columns) width() int {
	return max(c.ts, c.price, c.volume, c.side, c.id) + 1
}

// tradeCSV reads Kraken trade CSVs: the time-and-sales archive (header row,
// seven columns), the same seven columns without a header, or the older
// three-column timestamp,price,volume files.
type tradeCSV struct {
	r       *csv.Reader
	cols    columns
	started bool
	line    int64
}

// newTradeCSV wraps r.
func newTradeCSV(r io.Reader) *tradeCSV {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.ReuseRecord = true
	cr.TrimLeadingSpace = true
	return &tradeCSV{r: cr}
}

// next returns the next row. A non-empty issue means the row's values are
// unusable (the caller records it as a finding and skips the row); err is a
// read or layout error, or io.EOF at the end.
func (c *tradeCSV) next() (row tradeRow, issue string, err error) {
	for {
		rec, err := c.r.Read()
		if errors.Is(err, io.EOF) {
			return tradeRow{}, "", io.EOF
		}
		if err != nil {
			return tradeRow{}, "", err // csv.ParseError carries the line number
		}
		line, _ := c.r.FieldPos(0)
		c.line = int64(line)
		if !c.started {
			c.started = true
			if isHeader(rec) {
				if c.cols, err = headerColumns(rec); err != nil {
					return tradeRow{}, "", err
				}
				continue
			}
			if c.cols, err = positionalColumns(len(rec)); err != nil {
				return tradeRow{}, "", err
			}
		}
		return c.parse(rec)
	}
}

// isHeader reports whether the first record is a header (its first field is
// not a number).
func isHeader(rec []string) bool {
	_, err := strconv.ParseFloat(strings.TrimSpace(rec[0]), 64)
	return err != nil
}

// headerColumns maps header names to column positions.
func headerColumns(rec []string) (columns, error) {
	c := columns{ts: -1, price: -1, volume: -1, side: -1, id: -1}
	for i, name := range rec {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "timestamp", "time":
			c.ts = i
		case "price":
			c.price = i
		case "volume", "qty", "amount":
			c.volume = i
		case "type", "side":
			c.side = i
		case "trade_id", "tradeid", "id":
			c.id = i
		}
	}
	if c.ts < 0 || c.price < 0 || c.volume < 0 {
		return c, fmt.Errorf("header %q lacks a timestamp, price or volume column", strings.Join(rec, ","))
	}
	return c, nil
}

// positionalColumns returns the layout of a header-less file by its width.
func positionalColumns(n int) (columns, error) {
	switch n {
	case 3:
		return columns{ts: 0, price: 1, volume: 2, side: -1, id: -1}, nil
	case 7:
		return columns{ts: 0, price: 1, volume: 2, side: 3, id: 6}, nil
	}
	return columns{}, fmt.Errorf("%d columns without a header: expected 3 (timestamp,price,volume) or 7 (timestamp,price,volume,type,order_type,misc,trade_id)", n)
}

// parse converts one record.
func (c *tradeCSV) parse(rec []string) (tradeRow, string, error) {
	if len(rec) < c.cols.width() {
		return tradeRow{}, "", fmt.Errorf("line %d: %d fields, expected at least %d", c.line, len(rec), c.cols.width())
	}
	row := tradeRow{Line: c.line}
	var err error
	if c.cols.side >= 0 {
		switch strings.ToLower(strings.TrimSpace(rec[c.cols.side])) {
		case "b", "buy":
			row.Buy, row.SideKnown = true, true
		case "s", "sell":
			row.SideKnown = true
		case "":
		default:
			return tradeRow{}, "", fmt.Errorf("line %d: side %q is not b or s; is this a trade file?", c.line, rec[c.cols.side])
		}
	}
	if c.cols.id >= 0 {
		if s := strings.TrimSpace(rec[c.cols.id]); s != "" {
			if row.ID, err = strconv.ParseInt(s, 10, 64); err != nil {
				return tradeRow{}, "", fmt.Errorf("line %d: trade id %q is not an integer", c.line, s)
			}
		}
	}
	if row.Time, err = parseUnixTime(rec[c.cols.ts]); err != nil {
		return row, fmt.Sprintf("line %d: %v", c.line, err), nil
	}
	if row.Price, err = strconv.ParseFloat(strings.TrimSpace(rec[c.cols.price]), 64); err != nil {
		return row, fmt.Sprintf("line %d: price %q is not a number", c.line, rec[c.cols.price]), nil
	}
	if row.Volume, err = strconv.ParseFloat(strings.TrimSpace(rec[c.cols.volume]), 64); err != nil {
		return row, fmt.Sprintf("line %d: volume %q is not a number", c.line, rec[c.cols.volume]), nil
	}
	if !(row.Price > 0) || math.IsInf(row.Price, 0) {
		return row, fmt.Sprintf("line %d: price %v is not a positive finite number", c.line, row.Price), nil
	}
	if !(row.Volume > 0) || math.IsInf(row.Volume, 0) {
		return row, fmt.Sprintf("line %d: volume %v is not a positive finite number", c.line, row.Volume), nil
	}
	return row, "", nil
}

// parseUnixTime parses UNIX seconds given as an integer ("1688669597"), a
// decimal ("1688669597.8277369"; digits past nanoseconds are truncated) or,
// as a fallback, in exponent form. The result is UTC.
func parseUnixTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if strings.ContainsAny(s, "eE") {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return time.Time{}, fmt.Errorf("timestamp %q is not UNIX seconds", s)
		}
		sec := math.Floor(f)
		return time.Unix(int64(sec), int64(math.Round((f-sec)*1e9))).UTC(), nil
	}
	intPart, frac, _ := strings.Cut(s, ".")
	sec, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("timestamp %q is not UNIX seconds", s)
	}
	var ns int64
	if frac != "" {
		if len(frac) > 9 {
			frac = frac[:9]
		}
		for _, r := range frac {
			if r < '0' || r > '9' {
				return time.Time{}, fmt.Errorf("timestamp %q is not UNIX seconds", s)
			}
		}
		ns, _ = strconv.ParseInt(frac+strings.Repeat("0", 9-len(frac)), 10, 64)
		if strings.HasPrefix(intPart, "-") {
			ns = -ns
		}
	}
	return time.Unix(sec, ns).UTC(), nil
}
