package histdata

import (
	"maps"
	"slices"
	"time"
)

// Severity grades a validation finding.
type Severity string

// Finding severities. Errors mean the data cannot be trusted as stored;
// warnings are likely data problems; info records genuine market facts.
const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

// Finding kinds.
const (
	KindOutOfRange       = "timestamp_out_of_range" // implausible time (row skipped) or outside the file's named quarter (kept)
	KindDuplicateID      = "duplicate_trade_id"     // trade id not above the previous one (kept)
	KindInvalidValue     = "invalid_value"          // non-positive / non-finite / unparseable value (row skipped), or an impossible bar
	KindPriceJump        = "price_jump"             // consecutive prices differ by more than the jump factor (kept)
	KindNoTrades         = "no_trades"              // a run of empty 30 s intervals longer than the gap threshold
	KindBoundaryGap      = "boundary_gap"           // such a gap at a month or source boundary: likely missing data
	KindAllMarketsGap    = "all_markets_gap"        // every covered market silent at once: likely an outage or missing data
	KindUnknownSide      = "side_unknown"           // trades without a taker side (counted in Volume, in neither side total)
	KindCorruptFile      = "corrupt_file"           // a month file failed its header or checksum check
	KindNotInManifest    = "not_in_manifest"        // a month file the manifest does not list, or whose hash differs
	KindOHLCVTMismatch   = "ohlcvt_mismatch"        // a locally built candle differs from Kraken's OHLCVT file
	KindOHLCVTMissing    = "ohlcvt_missing"         // a candle present on one side only
	maxFindingsPerKind   = 20                       // detailed findings kept per kind; all are counted
	defaultJumpFactor    = 1.25
	defaultGapThreshold  = time.Hour
	plausibleEarliestUTC = "2013-01-01T00:00:00Z" // before Kraken's first trade
)

// Finding is one validation result.
type Finding struct {
	Kind     string    `json:"kind"`
	Severity Severity  `json:"severity"`
	Market   string    `json:"market,omitempty"`
	Time     time.Time `json:"time,omitzero"`
	End      time.Time `json:"end,omitzero"`
	Detail   string    `json:"detail"`
}

// findingLog keeps the first maxFindingsPerKind findings of each kind and
// counts all of them.
type findingLog struct {
	list   []Finding
	counts map[string]int
}

// add records f.
func (l *findingLog) add(f Finding) {
	if l.counts == nil {
		l.counts = map[string]int{}
	}
	l.counts[f.Kind]++
	if l.counts[f.Kind] <= maxFindingsPerKind {
		l.list = append(l.list, f)
	}
}

// merge adds every finding and count of o.
func (l *findingLog) merge(o findingLog) {
	if l.counts == nil {
		l.counts = map[string]int{}
	}
	for _, k := range slices.Sorted(maps.Keys(o.counts)) {
		l.counts[k] += o.counts[k]
	}
	for _, f := range o.list {
		n := 0
		for _, g := range l.list {
			if g.Kind == f.Kind {
				n++
			}
		}
		if n < maxFindingsPerKind {
			l.list = append(l.list, f)
		}
	}
}

// hasErrors reports whether any finding is an error.
func hasErrors(fs []Finding) bool {
	for _, f := range fs {
		if f.Severity == SeverityError {
			return true
		}
	}
	return false
}
