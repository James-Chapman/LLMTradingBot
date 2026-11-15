package histdata

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"time"
)

// ManifestName is the provenance file at the root of the cache.
const ManifestName = "manifest.json"

// ToolVersion identifies this importer in the manifest. Bump it when the
// import or bar-building rules change, so reports can tell caches apart.
const ToolVersion = "histdata/1.0.0"

// Manifest records where every cached month came from.
type Manifest struct {
	Format         int                      `json:"format"`
	Tool           string                   `json:"tool"`                     // ToolVersion of the last writer
	BuildRevision  string                   `json:"build_revision,omitempty"` // VCS revision of the last writer, when known
	BarFileVersion int                      `json:"bar_file_version"`
	Markets        map[string]*MarketRecord `json:"markets"`
}

// MarketRecord holds one market's months, keyed "YYYY-MM".
type MarketRecord struct {
	Months map[string]*MonthRecord `json:"months"`
}

// MonthRecord describes one month file and the sources merged into it.
type MonthRecord struct {
	File       string         `json:"file"` // path relative to the cache root, forward slashes
	FileSHA256 string         `json:"file_sha256"`
	Bars       int            `json:"bars"`
	Sources    []SourceRecord `json:"sources"`
}

// SourceRecord is what one source contributed to one month.
type SourceRecord struct {
	Source            string         `json:"source"`                  // archive entry ("x.zip!XBTEUR.csv", "dir/XBTEUR.csv") or "rest"
	SourceSHA256      string         `json:"source_sha256,omitempty"` // SHA-256 of the source file's bytes
	Tool              string         `json:"tool"`
	ImportedAt        time.Time      `json:"imported_at"`
	CoverFrom         time.Time      `json:"cover_from"` // bars in [CoverFrom, CoverTo) came from this source
	CoverTo           time.Time      `json:"cover_to"`
	FirstTrade        time.Time      `json:"first_trade"`
	LastTrade         time.Time      `json:"last_trade"`
	Trades            int64          `json:"trades"`
	Bars              int            `json:"bars"`
	UnknownSideTrades int64          `json:"unknown_side_trades,omitempty"`
	FindingCounts     map[string]int `json:"finding_counts,omitempty"`
	Findings          []Finding      `json:"findings,omitempty"`
}

// Provenance is the manifest entry for one month a backtest read.
type Provenance struct {
	Market     string         `json:"market"`
	Month      string         `json:"month"`
	File       string         `json:"file"`
	FileSHA256 string         `json:"file_sha256"`
	Bars       int            `json:"bars"`
	Sources    []SourceRecord `json:"sources"`
}

// newManifest returns an empty manifest.
func newManifest() *Manifest {
	return &Manifest{Format: 1, Tool: ToolVersion, BarFileVersion: barFileVersion, Markets: map[string]*MarketRecord{}}
}

// LoadManifest reads dir's manifest; a missing file gives an empty manifest.
func LoadManifest(dir string) (*Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if errors.Is(err, fs.ErrNotExist) {
		return newManifest(), nil
	}
	if err != nil {
		return nil, err
	}
	m := newManifest()
	if err := json.Unmarshal(data, m); err != nil {
		return nil, fmt.Errorf("%s: %w", ManifestName, err)
	}
	if m.Markets == nil {
		m.Markets = map[string]*MarketRecord{}
	}
	return m, nil
}

// manifestExists reports whether dir has a manifest file.
func manifestExists(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ManifestName))
	return err == nil
}

// save writes the manifest atomically, stamping this tool's version.
func (m *Manifest) save(dir string) error {
	m.Tool = ToolVersion
	m.BarFileVersion = barFileVersion
	m.BuildRevision = buildRevision()
	data, err := json.MarshalIndent(m, "", "  ") // encoding/json sorts map keys
	if err != nil {
		return err
	}
	return writeBytesAtomic(filepath.Join(dir, ManifestName), append(data, '\n'))
}

// month returns (creating) the record for market and month key.
func (m *Manifest) month(market, key string) *MonthRecord {
	mr := m.Markets[market]
	if mr == nil {
		mr = &MarketRecord{Months: map[string]*MonthRecord{}}
		m.Markets[market] = mr
	}
	rec := mr.Months[key]
	if rec == nil {
		rec = &MonthRecord{}
		mr.Months[key] = rec
	}
	return rec
}

// Provenance returns the entries of market's months overlapping [from, to),
// in month order.
func (m *Manifest) Provenance(market string, from, to time.Time) []Provenance {
	mr := m.Markets[market]
	if mr == nil {
		return nil
	}
	var out []Provenance
	for _, key := range slices.Sorted(maps.Keys(mr.Months)) {
		start, err := time.Parse("2006-01", key)
		if err != nil || !start.AddDate(0, 1, 0).After(from) || !start.Before(to) {
			continue
		}
		rec := mr.Months[key]
		out = append(out, Provenance{Market: market, Month: key, File: rec.File, FileSHA256: rec.FileSHA256,
			Bars: rec.Bars, Sources: slices.Clone(rec.Sources)})
	}
	return out
}

// ProvenanceFor loads the manifest in dir and returns market's provenance
// for [from, to). Every backtest report should cite these entries.
func ProvenanceFor(dir, market string, from, to time.Time) ([]Provenance, error) {
	m, err := LoadManifest(dir)
	if err != nil {
		return nil, err
	}
	return m.Provenance(market, from, to), nil
}

// buildRevision returns the VCS revision the binary was built from, if any.
func buildRevision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	rev, dirty := "", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev != "" && dirty {
		rev += "+dirty"
	}
	return rev
}
