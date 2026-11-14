package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"time"
)

// dbTimeLayout is how SQLAlchemy's SQLite DateTime type stores values (UTC, no zone).
const dbTimeLayout = "2006-01-02 15:04:05.000000"

// parseLayouts are tried in order when reading a stored datetime.
// The Z07:00 layouts accept "Z", "+00:00" and other offsets (RFC 3339 included).
var parseLayouts = []string{
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02T15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04",
	"2006-01-02T15:04",
	"2006-01-02",
}

// formatDBTime renders t in SQLAlchemy's fixed-width UTC layout so text
// ordering matches chronological ordering for old and new rows alike.
func formatDBTime(t time.Time) string {
	return t.UTC().Format(dbTimeLayout)
}

// dbTimeOrNil formats t for storage, or returns NULL for the zero time.
func dbTimeOrNil(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return formatDBTime(t)
}

// timeOrNow returns t, or the current UTC time when t is zero.
func timeOrNow(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t
}

// parseDBTime parses any datetime text the Python app or this package wrote.
// Values without a zone are UTC.
func parseDBTime(s string) (time.Time, error) {
	for _, layout := range parseLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised datetime %q", s)
}

// pyISO mirrors Python's naive datetime.isoformat(): microseconds are shown
// only when non-zero and no zone suffix is added (the frontend relies on this).
func pyISO(t time.Time) string {
	t = t.UTC()
	if t.Nanosecond()/1000 == 0 {
		return t.Format("2006-01-02T15:04:05")
	}
	return t.Format("2006-01-02T15:04:05.000000")
}

// nullTime scans a DATETIME column that may be NULL, text, or (when the
// driver recognised the format) an already-parsed time.Time.
type nullTime struct {
	Time  time.Time
	Valid bool
}

// Scan implements sql.Scanner.
func (n *nullTime) Scan(value any) error {
	*n = nullTime{}
	switch v := value.(type) {
	case nil:
		return nil
	case time.Time:
		n.Time, n.Valid = v.UTC(), true
	case string:
		return n.scanText(v)
	case []byte:
		return n.scanText(string(v))
	case int64:
		n.Time, n.Valid = time.Unix(v, 0).UTC(), true
	default:
		return fmt.Errorf("cannot scan %T into a datetime", value)
	}
	return nil
}

// scanText parses a textual datetime; an empty string is treated as NULL.
func (n *nullTime) scanText(s string) error {
	if s == "" {
		return nil
	}
	t, err := parseDBTime(s)
	if err != nil {
		return err
	}
	n.Time, n.Valid = t, true
	return nil
}

// iso returns the Python-style isoformat string, or "" when NULL.
func (n nullTime) iso() string {
	if !n.Valid {
		return ""
	}
	return pyISO(n.Time)
}

// orZero returns the time, or the zero time when NULL.
func (n nullTime) orZero() time.Time {
	if !n.Valid {
		return time.Time{}
	}
	return n.Time
}

// encodeJSON marshals v for a JSON column.
func encodeJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encode JSON column: %w", err)
	}
	return string(b), nil
}

// decodeJSON unmarshals a JSON column into dst; NULL leaves dst untouched.
func decodeJSON(col sql.NullString, dst any) error {
	if !col.Valid || col.String == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(col.String), dst); err != nil {
		return fmt.Errorf("decode JSON column: %w", err)
	}
	return nil
}

// nullIfEmpty stores "" as NULL (Python's `value or None`).
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullableFloat converts an optional float to a driver value.
func nullableFloat(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

// nullableBool converts an optional bool to SQLite's 0/1 or NULL.
func nullableBool(p *bool) any {
	if p == nil {
		return nil
	}
	return boolInt(*p)
}

// boolInt converts a bool to SQLite's 0/1 integer.
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// floatPtr returns a pointer to the value, or nil when NULL.
func floatPtr(n sql.NullFloat64) *float64 {
	if !n.Valid {
		return nil
	}
	v := n.Float64
	return &v
}

// stringPtr returns a pointer to the value, or nil when NULL.
func stringPtr(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	v := n.String
	return &v
}

// boolPtr returns a pointer to the value, or nil when NULL.
func boolPtr(n sql.NullBool) *bool {
	if !n.Valid {
		return nil
	}
	v := n.Bool
	return &v
}

// short8 returns the first 8 characters of s (the dashboard's short ID form).
func short8(s string) string {
	r := []rune(s)
	if len(r) <= 8 {
		return s
	}
	return string(r[:8])
}

// round2 rounds to 2 decimal places (Python's round(x, 2) for display totals).
func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

// orDefault returns n, or def when n is not positive (Python default args).
func orDefault(n, def int) int {
	if n <= 0 {
		return def
	}
	return n
}
