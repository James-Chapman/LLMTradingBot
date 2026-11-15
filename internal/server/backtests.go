package server

// Read-only viewer API for the result directories written by cmd/backtest.
//
// The backtester (internal/backtest) is deliberately not imported: the files
// are read as files, only the fields the list needs are decoded, and
// summary.json, run.json and costs.json are passed through as raw JSON so new
// fields reach the dashboard without server changes.
//
// Path safety: every file is opened through an os.Root on the results
// folder, which refuses any name (including a symlink target) that resolves
// outside it. On top of that the id must be a plain relative slash-separated
// path (no "..", ".", empty segment, backslash, colon, NUL or control
// character, no Windows reserved name), and only the fixed file names below
// are ever opened.

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Size limits of the files the viewer reads. Variables so tests can lower them.
var (
	// maxBacktestCSVBytes is the largest CSV file the viewer parses (413 above).
	maxBacktestCSVBytes int64 = 256 << 20
	// maxBacktestTextBytes is the largest JSON or Markdown file the viewer reads.
	maxBacktestTextBytes int64 = 64 << 20
)

// Paging and downsampling limits of the CSV parts.
const (
	defaultEquityPoints = 2000
	minEquityPoints     = 2
	maxEquityPoints     = 20000
	defaultRowLimit     = 500
	maxRowLimit         = 5000
	// maxListDepth bounds how deep the list looks for run directories.
	maxListDepth = 8
	// maxBacktestIDLength bounds the id taken from the URL.
	maxBacktestIDLength = 512
)

// The fixed set of file names the viewer ever opens.
const (
	fileSummaryJSON = "summary.json"
	fileSummaryMD   = "summary.md"
	fileRunJSON     = "run.json"
	fileCostsJSON   = "costs.json"
	fileEquityCSV   = "equity.csv"
	fileTradesCSV   = "trades.csv"
	fileOrdersCSV   = "orders.csv"
	dirChildRuns    = "runs"
)

// reportFileNames mark a directory as a run directory (written by the
// backtester), as opposed to a folder that only groups runs.
var reportFileNames = []string{fileSummaryJSON, fileSummaryMD, fileRunJSON, "result.json", fileCostsJSON, fileEquityCSV, fileTradesCSV, fileOrdersCSV}

// Parts of GET /api/backtests/{id...}?part=...
const (
	partEquity    = "equity"
	partTrades    = "trades"
	partOrders    = "orders"
	partSummaryMD = "summary_md"
)

// backtestTextColumns are CSV columns that stay strings; every other column
// is a number, or null when empty.
var backtestTextColumns = map[string]bool{
	"time": true, "id": true, "position_id": true, "market": true, "side": true, "reason": true,
	"strategy_id": true, "idea_id": true, "entry_time": true, "exit_time": true, "decision_time": true,
	"fill_time": true, "price_source": true, "status": true, "reject_reason": true, "exit_reason": true,
}

// errBacktestNotFound reports an id that names no readable run directory.
var errBacktestNotFound = errors.New("backtest not found")

// backtestFileError reports a problem with one of a run's fixed files.
type backtestFileError struct {
	name   string // fixed file name, never a filesystem path
	status int
	detail string
}

// Error returns the detail sent to the client.
func (e *backtestFileError) Error() string { return e.detail }

// missingFile is the error for a run file that does not exist (or cannot be opened inside the root).
func missingFile(name string) error {
	return &backtestFileError{name: name, status: http.StatusNotFound, detail: name + " not found for this backtest"}
}

// fileTooLarge is the error for a run file above its size limit.
func fileTooLarge(name string, limit int64) error {
	return &backtestFileError{name: name, status: http.StatusRequestEntityTooLarge, detail: fmt.Sprintf("%s is larger than %d bytes", name, limit)}
}

// badFile is the error for a run file that cannot be parsed.
func badFile(name, what string) error {
	return &backtestFileError{name: name, status: http.StatusUnprocessableEntity, detail: fmt.Sprintf("%s is not a valid %s", name, what)}
}

// ---------------------------------------------------------------------------
// Path safety
// ---------------------------------------------------------------------------

// validBacktestID reports whether id is a plain relative path of names
// separated by "/": no "..", ".", empty or absolute segments, backslashes,
// drive letters, NUL or other control characters, or Windows reserved names.
func validBacktestID(id string) bool {
	if id == "" || len(id) > maxBacktestIDLength || !fs.ValidPath(id) || id == "." {
		return false
	}
	if strings.ContainsAny(id, `\:`) {
		return false
	}
	for _, r := range id {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return filepath.IsLocal(filepath.FromSlash(id))
}

// openResultsRoot opens the results folder as an os.Root; it returns nil,
// nil when the folder does not exist.
func (h *handler) openResultsRoot() (*os.Root, error) {
	root, err := os.OpenRoot(h.resultsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return root, err
}

// runDirectory checks that id names a directory inside root holding a
// summary.json, and returns its root-relative OS path.
func runDirectory(root *os.Root, id string) (string, error) {
	if !validBacktestID(id) {
		return "", errBacktestNotFound
	}
	dir := filepath.FromSlash(id)
	// os.Root resolves symlinks and refuses any that leave the root.
	info, err := root.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", errBacktestNotFound
	}
	summary, err := root.Stat(filepath.Join(dir, fileSummaryJSON))
	if err != nil || !summary.Mode().IsRegular() {
		return "", errBacktestNotFound
	}
	return dir, nil
}

// openRunFile opens one fixed file of a run directory after checking it is
// a regular file of at most limit bytes.
func openRunFile(root *os.Root, dir, name string, limit int64) (*os.File, error) {
	f, err := root.Open(filepath.Join(dir, name))
	if err != nil {
		// Missing, or a symlink out of the root: both are "not found".
		return nil, missingFile(name)
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, missingFile(name)
	}
	if info.Size() > limit {
		_ = f.Close()
		return nil, fileTooLarge(name, limit)
	}
	return f, nil
}

// readRunText reads one fixed text file of a run directory.
func readRunText(root *os.Root, dir, name string) ([]byte, error) {
	f, err := openRunFile(root, dir, name, maxBacktestTextBytes)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxBacktestTextBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBacktestTextBytes {
		return nil, fileTooLarge(name, maxBacktestTextBytes)
	}
	return data, nil
}

// readRunJSON reads one fixed JSON file of a run directory as raw JSON.
func readRunJSON(root *os.Root, dir, name string) (json.RawMessage, error) {
	data, err := readRunText(root, dir, name)
	if err != nil {
		return nil, err
	}
	if !json.Valid(data) {
		return nil, badFile(name, "JSON file")
	}
	return json.RawMessage(data), nil
}

// readOptionalJSON is readRunJSON that returns nil (JSON null) for a missing file.
func readOptionalJSON(root *os.Root, dir, name string) (json.RawMessage, error) {
	raw, err := readRunJSON(root, dir, name)
	var fe *backtestFileError
	if errors.As(err, &fe) && fe.status == http.StatusNotFound {
		return nil, nil
	}
	return raw, err
}

// childRunIDs returns the ids of a multi-run parent's runs/NN_label
// directories, sorted; a run directory has none.
func childRunIDs(root *os.Root, id string) []string {
	children := []string{}
	f, err := root.Open(filepath.Join(filepath.FromSlash(id), dirChildRuns))
	if err != nil {
		return children
	}
	defer f.Close()
	entries, err := f.ReadDir(-1)
	if err != nil {
		return children
	}
	for _, e := range entries {
		child := path.Join(id, dirChildRuns, e.Name())
		if e.IsDir() && validBacktestID(child) {
			children = append(children, child)
		}
	}
	slices.Sort(children)
	return children
}

// ---------------------------------------------------------------------------
// Summary headline (the only fields of summary.json the server decodes)
// ---------------------------------------------------------------------------

// headlineRow is the part of a metrics block or multi-run RunRow the list shows.
type headlineRow struct {
	NetPnL           *float64 `json:"net_pnl"`
	NetReturnPct     *float64 `json:"net_return_pct"`
	RoundTrips       *int     `json:"round_trips"`
	ReconcileWarning bool     `json:"reconcile_warning"`
	DataWarnings     int      `json:"data_warnings"`
}

// verdictLine is one line of a summary's verdict.
type verdictLine struct {
	Rule string `json:"rule"`
	Text string `json:"text"`
}

// summaryHead holds the summary.json fields the list needs, for every kind.
type summaryHead struct {
	Kind       string        `json:"kind"`
	StrategyID string        `json:"strategy_id"`
	Start      string        `json:"start"`
	End        string        `json:"end"`
	Markets    []string      `json:"markets"`
	Verdict    []verdictLine `json:"verdict"`
	Honesty    struct {
		Lines []string `json:"lines"`
	} `json:"honesty"`
	Reconciliation struct {
		Warning bool `json:"warning"`
	} `json:"reconciliation"`
	Metrics     headlineRow  `json:"metrics"`       // kind run
	OutOfSample *headlineRow `json:"out_of_sample"` // kind split
	Run         *headlineRow `json:"run"`           // kind regimes
	Baseline    *headlineRow `json:"baseline"`      // kind sensitivity
	Aggregate   *struct {    // kind walkforward
		CompoundedReturnPct *float64 `json:"compounded_return_pct"`
		RoundTrips          *int     `json:"round_trips"`
	} `json:"aggregate"`
}

// runHead holds the run.json fields the list needs.
type runHead struct {
	Markets        []string `json:"markets"`
	InitialMarkets []string `json:"initial_markets"`
}

// headline returns the row the list shows for the summary's kind: a single
// run's metrics, split's out-of-sample run, regimes' whole run,
// sensitivity's baseline, or walk-forward's test-window aggregate.
func (s summaryHead) headline() headlineRow {
	switch s.Kind {
	case "split":
		if s.OutOfSample != nil {
			return *s.OutOfSample
		}
	case "regimes":
		if s.Run != nil {
			return *s.Run
		}
	case "sensitivity":
		if s.Baseline != nil {
			return *s.Baseline
		}
	case "walkforward":
		if s.Aggregate != nil {
			return headlineRow{NetReturnPct: s.Aggregate.CompoundedReturnPct, RoundTrips: s.Aggregate.RoundTrips}
		}
	case "run":
		return s.Metrics
	}
	return headlineRow{}
}

// ownWarnings reports the warnings visible in the summary itself: an
// accounting or data warning in the honesty block or the reconciliation, or
// on the headline row of a multi-run summary.
func (s summaryHead) ownWarnings() bool {
	if s.Reconciliation.Warning {
		return true
	}
	for _, line := range s.Honesty.Lines {
		if strings.Contains(line, "WARNING") {
			return true
		}
	}
	row := s.headline()
	return row.ReconcileWarning || row.DataWarnings > 0
}

// readSummaryHead decodes the headline fields of a run directory's summary.json.
func readSummaryHead(root *os.Root, dir string) (summaryHead, fs.FileInfo, error) {
	var head summaryHead
	data, err := readRunText(root, dir, fileSummaryJSON)
	if err != nil {
		return head, nil, err
	}
	if err := json.Unmarshal(data, &head); err != nil || head.Kind == "" {
		return head, nil, badFile(fileSummaryJSON, "backtest summary")
	}
	info, err := root.Stat(filepath.Join(dir, fileSummaryJSON))
	if err != nil {
		return head, nil, missingFile(fileSummaryJSON)
	}
	return head, info, nil
}

// ---------------------------------------------------------------------------
// GET /api/backtests
// ---------------------------------------------------------------------------

// backtestEntry is one run in the list.
type backtestEntry struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind"`
	Strategy     string   `json:"strategy"`
	Markets      []string `json:"markets"`
	Start        string   `json:"start"`
	End          string   `json:"end"`
	NetReturnPct *float64 `json:"net_return_pct"`
	NetPnL       *float64 `json:"net_pnl"`
	RoundTrips   *int     `json:"round_trips"`
	Verdict      string   `json:"verdict"`
	VerdictRule  string   `json:"verdict_rule"`
	HasWarnings  bool     `json:"has_warnings"`
	Modified     string   `json:"modified"`
	Children     []string `json:"children"`

	modified time.Time
}

// backtestList is the list response.
type backtestList struct {
	Backtests []backtestEntry `json:"backtests"`
	Skipped   int             `json:"skipped"`
}

// listBacktests returns the runs under the results folder, newest first.
func (h *handler) listBacktests(w http.ResponseWriter, r *http.Request) {
	out := backtestList{Backtests: []backtestEntry{}}
	root, err := h.openResultsRoot()
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if root != nil {
		defer root.Close()
		scanResults(root, ".", 0, &out)
	}
	// Newest first; the id breaks ties so the order is deterministic.
	slices.SortFunc(out.Backtests, func(a, b backtestEntry) int {
		if c := b.modified.Compare(a.modified); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	writeJSON(w, http.StatusOK, out)
}

// scanResults walks dir (root-relative, slash-separated) for run
// directories. A directory holding report files is one entry and is not
// descended into (a multi-run parent's runs/ become its children); other
// directories are folders that group runs. Symlinks are never followed.
func scanResults(root *os.Root, dir string, depth int, out *backtestList) {
	f, err := root.Open(filepath.FromSlash(dir))
	if err != nil {
		return
	}
	entries, err := f.ReadDir(-1)
	_ = f.Close()
	if err != nil {
		return
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	if dir != "." && holdsReportFiles(entries) {
		if entry, err := buildEntry(root, dir); err == nil {
			out.Backtests = append(out.Backtests, entry)
		} else {
			out.Skipped++
		}
		return
	}
	if depth >= maxListDepth {
		return
	}
	for _, e := range entries {
		// DirEntry.IsDir is false for a symlink, so links are not walked.
		if e.IsDir() {
			scanResults(root, path.Join(dir, e.Name()), depth+1, out)
		}
	}
}

// holdsReportFiles reports whether a directory listing contains any file
// the backtester writes.
func holdsReportFiles(entries []fs.DirEntry) bool {
	for _, e := range entries {
		if !e.IsDir() && slices.Contains(reportFileNames, e.Name()) {
			return true
		}
	}
	return false
}

// buildEntry reads one run directory's list entry.
func buildEntry(root *os.Root, id string) (backtestEntry, error) {
	dir, err := runDirectory(root, id)
	if err != nil {
		return backtestEntry{}, err
	}
	head, info, err := readSummaryHead(root, dir)
	if err != nil {
		return backtestEntry{}, err
	}
	row := head.headline()
	entry := backtestEntry{
		ID: id, Kind: head.Kind, Strategy: head.StrategyID, Start: head.Start, End: head.End,
		NetReturnPct: row.NetReturnPct, NetPnL: row.NetPnL, RoundTrips: row.RoundTrips,
		HasWarnings: head.ownWarnings(), Children: childRunIDs(root, id),
		modified: info.ModTime().UTC(), Markets: head.Markets,
	}
	entry.Modified = entry.modified.Format(time.RFC3339)
	if len(head.Verdict) > 0 {
		entry.Verdict, entry.VerdictRule = head.Verdict[0].Text, head.Verdict[0].Rule
	}
	if len(entry.Markets) == 0 {
		entry.Markets = runMarkets(root, dir)
	}
	entry.Markets = nonNil(entry.Markets)
	// A multi-run parent warns when any of its runs does.
	for _, child := range entry.Children {
		if childDir, err := runDirectory(root, child); err == nil {
			if childHead, _, err := readSummaryHead(root, childDir); err == nil && childHead.ownWarnings() {
				entry.HasWarnings = true
			}
		}
	}
	return entry, nil
}

// runMarkets returns run.json's markets (multi-run) or initial_markets (single run).
func runMarkets(root *os.Root, dir string) []string {
	data, err := readRunText(root, dir, fileRunJSON)
	if err != nil {
		return nil
	}
	var head runHead
	if json.Unmarshal(data, &head) != nil {
		return nil
	}
	if len(head.Markets) > 0 {
		return head.Markets
	}
	return head.InitialMarkets
}

// ---------------------------------------------------------------------------
// GET /api/backtests/{id...}
// ---------------------------------------------------------------------------

// backtestDetail is the response without a part.
type backtestDetail struct {
	ID       string          `json:"id"`
	Kind     string          `json:"kind"`
	Summary  json.RawMessage `json:"summary"`
	Run      json.RawMessage `json:"run"`   // null when run.json is missing
	Costs    json.RawMessage `json:"costs"` // null when costs.json is missing (multi-run parents)
	Children []string        `json:"children"`
}

// equityResponse is the ?part=equity response.
type equityResponse struct {
	ID             string   `json:"id"`
	Columns        []string `json:"columns"`
	Points         [][]any  `json:"points"`
	Downsampled    bool     `json:"downsampled"`
	OriginalPoints int      `json:"original_points"`
	MaxPoints      int      `json:"max_points"`
}

// rowsResponse is the ?part=trades and ?part=orders response.
type rowsResponse struct {
	ID      string           `json:"id"`
	Part    string           `json:"part"`
	Columns []string         `json:"columns"`
	Rows    []map[string]any `json:"rows"`
	Total   int              `json:"total"`
	Limit   int              `json:"limit"`
	Offset  int              `json:"offset"`
}

// backtestQuery is the validated query of a detail request.
type backtestQuery struct {
	part      string
	maxPoints int
	limit     int
	offset    int
	status    string
}

// parseBacktestQuery validates part and its parameters (unknown parameters are ignored).
func parseBacktestQuery(q url.Values) (backtestQuery, error) {
	out := backtestQuery{status: q.Get("status")}
	if parts, ok := q["part"]; ok {
		out.part = parts[0]
		if !slices.Contains([]string{partEquity, partTrades, partOrders, partSummaryMD}, out.part) {
			return out, errors.New("part must be one of equity, trades, orders, summary_md")
		}
	}
	var err error
	if out.maxPoints, err = intParam(q, "max_points", defaultEquityPoints, minEquityPoints, maxEquityPoints); err != nil {
		return out, err
	}
	if out.limit, err = intParam(q, "limit", defaultRowLimit, 1, maxRowLimit); err != nil {
		return out, err
	}
	if out.offset, err = intParam(q, "offset", 0, 0, math.MaxInt); err != nil {
		return out, err
	}
	return out, nil
}

// intParam reads an integer query parameter: def when absent, an error when
// not an integer or below lowest, capped at highest.
func intParam(q url.Values, name string, def, lowest, highest int) (int, error) {
	raw := q.Get(name)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < lowest {
		return 0, fmt.Errorf("%s must be an integer of at least %d", name, lowest)
	}
	return min(v, highest), nil
}

// getBacktest serves one run: its stored JSON, or one part of it.
func (h *handler) getBacktest(w http.ResponseWriter, r *http.Request) {
	query, err := parseBacktestQuery(r.URL.Query())
	if err != nil {
		writeDetail(w, http.StatusBadRequest, err.Error())
		return
	}
	root, err := h.openResultsRoot()
	if err != nil || root == nil {
		writeDetail(w, http.StatusNotFound, "Backtest not found")
		return
	}
	defer root.Close()
	id := r.PathValue("id")
	dir, err := runDirectory(root, id)
	if err != nil {
		h.writeBacktestError(w, r, err)
		return
	}
	switch query.part {
	case "":
		h.serveBacktestJSON(w, r, root, id, dir)
	case partSummaryMD:
		data, err := readRunText(root, dir, fileSummaryMD)
		if err != nil {
			h.writeBacktestError(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(data)
	case partEquity:
		resp, err := readEquity(root, dir, query.maxPoints)
		h.writeBacktestResult(w, r, id, resp, err)
	case partTrades:
		resp, err := readRows(root, dir, fileTradesCSV, query.limit, query.offset, "")
		h.writeBacktestResult(w, r, id, resp, err)
	case partOrders:
		resp, err := readRows(root, dir, fileOrdersCSV, query.limit, query.offset, query.status)
		h.writeBacktestResult(w, r, id, resp, err)
	}
}

// serveBacktestJSON writes a run's summary, run and costs JSON as stored.
func (h *handler) serveBacktestJSON(w http.ResponseWriter, r *http.Request, root *os.Root, id, dir string) {
	summary, err := readRunJSON(root, dir, fileSummaryJSON)
	if err != nil {
		h.writeBacktestError(w, r, err)
		return
	}
	var head struct {
		Kind string `json:"kind"`
	}
	_ = json.Unmarshal(summary, &head)
	run, err := readOptionalJSON(root, dir, fileRunJSON)
	if err != nil {
		h.writeBacktestError(w, r, err)
		return
	}
	costs, err := readOptionalJSON(root, dir, fileCostsJSON)
	if err != nil {
		h.writeBacktestError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, backtestDetail{ID: id, Kind: head.Kind, Summary: summary, Run: run, Costs: costs, Children: childRunIDs(root, id)})
}

// writeBacktestResult stamps the id on a part response and writes it, or the error.
func (h *handler) writeBacktestResult(w http.ResponseWriter, r *http.Request, id string, resp any, err error) {
	if err != nil {
		h.writeBacktestError(w, r, err)
		return
	}
	switch v := resp.(type) {
	case *equityResponse:
		v.ID = id
	case *rowsResponse:
		v.ID = id
	}
	writeJSON(w, http.StatusOK, resp)
}

// writeBacktestError maps a viewer error to its status; the detail never
// contains a filesystem path.
func (h *handler) writeBacktestError(w http.ResponseWriter, r *http.Request, err error) {
	var fe *backtestFileError
	switch {
	case errors.Is(err, errBacktestNotFound):
		writeDetail(w, http.StatusNotFound, "Backtest not found")
	case errors.As(err, &fe):
		writeDetail(w, fe.status, fe.detail)
	default:
		h.writeError(w, r, err)
	}
}

// ---------------------------------------------------------------------------
// CSV parts
// ---------------------------------------------------------------------------

// csvTable streams one CSV file of a run directory.
type csvTable struct {
	file    *os.File
	name    string
	reader  *csv.Reader
	columns []string
}

// openCSV opens a run's CSV file (at most maxBacktestCSVBytes) and reads its header.
func openCSV(root *os.Root, dir, name string) (*csvTable, error) {
	f, err := openRunFile(root, dir, name, maxBacktestCSVBytes)
	if err != nil {
		return nil, err
	}
	t := &csvTable{file: f, name: name}
	if err := t.rewind(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return t, nil
}

// rewind restarts reading after the header.
func (t *csvTable) rewind() error {
	if _, err := t.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	// The limit guards against a file that grew after its size was checked.
	t.reader = csv.NewReader(io.LimitReader(t.file, maxBacktestCSVBytes))
	header, err := t.reader.Read()
	if errors.Is(err, io.EOF) {
		return badFile(t.name, "CSV file (no header)")
	}
	if err != nil {
		return badFile(t.name, "CSV file")
	}
	t.columns = slices.Clone(header)
	return nil
}

// next returns the next record, io.EOF at the end, or a bad-file error.
func (t *csvTable) next() ([]string, error) {
	record, err := t.reader.Read()
	if errors.Is(err, io.EOF) {
		return nil, io.EOF
	}
	if err != nil {
		return nil, badFile(t.name, "CSV file")
	}
	return record, nil
}

// close releases the file.
func (t *csvTable) close() { _ = t.file.Close() }

// column returns the index of a column, or -1.
func (t *csvTable) column(name string) int { return slices.Index(t.columns, name) }

// cellValue types one CSV cell: text columns stay strings, empty numeric
// cells are null, numbers are numbers, and anything else stays a string.
func cellValue(column, raw string) any {
	if backtestTextColumns[column] {
		return raw
	}
	if raw == "" {
		return nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return raw
	}
	return v
}

// typedRecord converts one CSV record to typed values in column order.
func (t *csvTable) typedRecord(record []string) []any {
	out := make([]any, len(record))
	for i, raw := range record {
		out[i] = cellValue(t.columns[i], raw)
	}
	return out
}

// equityBucket tracks the lowest and highest equity point of one bucket.
type equityBucket struct {
	used           bool
	minIdx, maxIdx int
	minVal, maxVal float64
	minRec, maxRec []any
}

// add puts point idx (equity value, typed record) into the bucket.
func (b *equityBucket) add(idx int, value float64, rec []any) {
	if !b.used || value < b.minVal {
		b.minIdx, b.minVal, b.minRec = idx, value, rec
	}
	if !b.used || value > b.maxVal {
		b.maxIdx, b.maxVal, b.maxRec = idx, value, rec
	}
	b.used = true
}

// flush appends the bucket's minimum and maximum in time order and resets it.
func (b *equityBucket) flush(points [][]any) [][]any {
	if !b.used {
		return points
	}
	switch {
	case b.minIdx == b.maxIdx:
		points = append(points, b.minRec)
	case b.minIdx < b.maxIdx:
		points = append(points, b.minRec, b.maxRec)
	default:
		points = append(points, b.maxRec, b.minRec)
	}
	*b = equityBucket{}
	return points
}

// readEquity parses equity.csv, downsampled to at most maxPoints: the first
// and last points are kept, and the interior is cut into (maxPoints-2)/2
// buckets of which the lowest and highest equity point each survive, so a
// drawdown is never smoothed away. Two streaming passes: count, then sample.
func readEquity(root *os.Root, dir string, maxPoints int) (*equityResponse, error) {
	t, err := openCSV(root, dir, fileEquityCSV)
	if err != nil {
		return nil, err
	}
	defer t.close()
	total := 0
	for {
		if _, err := t.next(); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, err
		}
		total++
	}
	resp := &equityResponse{Columns: t.columns, Points: [][]any{}, OriginalPoints: total, MaxPoints: maxPoints, Downsampled: total > maxPoints}
	if err := t.rewind(); err != nil {
		return nil, err
	}
	equityCol := t.column("equity")
	if resp.Downsampled && equityCol < 0 {
		return nil, badFile(fileEquityCSV, "equity curve (no equity column)")
	}
	interior := total - 2
	buckets := (maxPoints - 2) / 2
	var bucket equityBucket
	current := -1
	for i := 0; ; i++ {
		record, err := t.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		rec := t.typedRecord(record)
		if !resp.Downsampled || i == 0 {
			resp.Points = append(resp.Points, rec)
			continue
		}
		if i == total-1 {
			resp.Points = append(bucket.flush(resp.Points), rec)
			continue
		}
		if buckets == 0 {
			continue
		}
		value, err := strconv.ParseFloat(record[equityCol], 64)
		if err != nil || math.IsNaN(value) {
			return nil, badFile(fileEquityCSV, "equity curve (non-numeric equity)")
		}
		// Interior point i (1..total-2) falls in bucket (i-1)*buckets/interior.
		if b := (i - 1) * buckets / interior; b != current {
			resp.Points = bucket.flush(resp.Points)
			current = b
		}
		bucket.add(i, value, rec)
	}
	return resp, nil
}

// readRows parses one page of trades.csv or orders.csv as JSON objects,
// counting every row (matching status, when given) into Total.
func readRows(root *os.Root, dir, name string, limit, offset int, status string) (*rowsResponse, error) {
	t, err := openCSV(root, dir, name)
	if err != nil {
		return nil, err
	}
	defer t.close()
	statusCol := t.column("status")
	if status != "" && statusCol < 0 {
		return nil, badFile(name, "order log (no status column)")
	}
	resp := &rowsResponse{Part: strings.TrimSuffix(name, ".csv"), Columns: t.columns, Rows: []map[string]any{}, Limit: limit, Offset: offset}
	for {
		record, err := t.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if status != "" && record[statusCol] != status {
			continue
		}
		if resp.Total >= offset && len(resp.Rows) < limit {
			row := make(map[string]any, len(record))
			for i, raw := range record {
				row[t.columns[i]] = cellValue(t.columns[i], raw)
			}
			resp.Rows = append(resp.Rows, row)
		}
		resp.Total++
	}
	return resp, nil
}
