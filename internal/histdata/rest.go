package histdata

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultRESTURL is Kraken's public REST API.
const DefaultRESTURL = "https://api.kraken.com"

// REST paging defaults. Kraken asks public callers to stay at about one call
// per second per pair; 1.1 s leaves a margin.
const (
	defaultRESTInterval   = 1100 * time.Millisecond
	defaultRESTBackoff    = 5 * time.Second
	defaultRESTMaxBackoff = 2 * time.Minute
	defaultRESTRetries    = 8
	restPageSize          = 1000
	spoolHeader           = "timestamp,price,volume,type,order_type,misc,trade_id"
)

// RESTFetcher pages GET /0/public/Trades. It uses net/http directly (public
// endpoint, no keys) so this package never links the live Kraken client.
type RESTFetcher struct {
	BaseURL     string        // default DefaultRESTURL
	Client      *http.Client  // default: a client with Timeout
	Timeout     time.Duration // per request, when Client is nil; default 60 s
	MinInterval time.Duration // between calls; default 1.1 s
	MaxRetries  int           // consecutive retryable failures before giving up; default 8, negative = none
	BaseBackoff time.Duration // first retry wait, doubled each time; default 5 s
	MaxBackoff  time.Duration // default 2 min
	Now         func() time.Time
	Sleep       func(ctx context.Context, d time.Duration) error
	Progress    func(msg string)

	lastCall time.Time
	called   bool
}

// FetchResult describes one FetchToSpool call.
type FetchResult struct {
	Market      string    `json:"market"`
	Pair        string    `json:"pair"`
	Pages       int       `json:"pages"`
	NewTrades   int64     `json:"new_trades"`
	TotalTrades int64     `json:"total_trades"` // in the spool
	Cursor      string    `json:"cursor"`       // next "since"
	Reached     time.Time `json:"reached,omitzero"`
	Complete    bool      `json:"complete"`  // stopped at the requested end time
	CaughtUp    bool      `json:"caught_up"` // reached the present before the end time
	Resumed     bool      `json:"resumed"`   // continued an earlier spool
	SpoolPath   string    `json:"spool_path"`
}

// restState is the spool's resumable cursor, saved after every page.
type restState struct {
	Market        string    `json:"market"`
	Pair          string    `json:"pair"`
	From          time.Time `json:"from"`
	To            time.Time `json:"to"`
	Cursor        string    `json:"cursor"`
	LastTradeID   int64     `json:"last_trade_id"`
	LastTradeTime time.Time `json:"last_trade_time,omitzero"`
	Trades        int64     `json:"trades"`
	SpoolBytes    int64     `json:"spool_bytes"` // committed length of trades.csv
	Complete      bool      `json:"complete"`
}

// defaults fills unset fields.
func (f *RESTFetcher) defaults() {
	if f.BaseURL == "" {
		f.BaseURL = DefaultRESTURL
	}
	if f.Client == nil {
		if f.Timeout <= 0 {
			f.Timeout = 60 * time.Second
		}
		f.Client = &http.Client{Timeout: f.Timeout}
	}
	if f.MinInterval <= 0 {
		f.MinInterval = defaultRESTInterval
	}
	if f.MaxRetries == 0 {
		f.MaxRetries = defaultRESTRetries
	}
	if f.BaseBackoff <= 0 {
		f.BaseBackoff = defaultRESTBackoff
	}
	if f.MaxBackoff <= 0 {
		f.MaxBackoff = defaultRESTMaxBackoff
	}
	if f.Now == nil {
		f.Now = time.Now
	}
	if f.Sleep == nil {
		f.Sleep = sleepCtx
	}
}

// sleepCtx waits for d or until ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// FetchREST pages Kraken's public trades for market over [from, to) into the
// cache's REST spool (resuming an interrupted fetch of the same start), then
// rebuilds the affected months from the whole spool with source "rest".
func (c *Cache) FetchREST(ctx context.Context, f *RESTFetcher, market string, from, to time.Time, opts ImportOptions) (FetchResult, FileSummary, error) {
	res, err := f.FetchToSpool(ctx, c.restDir(market), market, from, to)
	if err != nil {
		return res, FileSummary{}, err
	}
	sum, err := c.ImportSpool(ctx, market, opts)
	return res, sum, err
}

// ImportSpool rebuilds the cache months covered by market's REST spool.
func (c *Cache) ImportSpool(ctx context.Context, market string, opts ImportOptions) (FileSummary, error) {
	dir := c.restDir(market)
	st, ok, err := loadRESTState(dir)
	if err != nil {
		return FileSummary{}, err
	}
	if !ok {
		return FileSummary{}, fmt.Errorf("no REST spool for %s", market)
	}
	file, err := os.Open(filepath.Join(dir, "trades.csv"))
	if err != nil {
		return FileSummary{}, err
	}
	defer file.Close()
	// Only the committed prefix: bytes after SpoolBytes belong to a page
	// whose cursor was never saved.
	return c.importStream(ctx, "rest", market, io.LimitReader(file, st.SpoolBytes), opts, st.From)
}

// loadRESTState reads a spool's state; ok is false when there is none.
func loadRESTState(dir string) (restState, bool, error) {
	var st restState
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return st, false, nil
	}
	if err != nil {
		return st, false, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, false, fmt.Errorf("%s: %w", filepath.Join(dir, "state.json"), err)
	}
	return st, true, nil
}

// saveRESTState writes a spool's state atomically.
func saveRESTState(dir string, st restState) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeBytesAtomic(filepath.Join(dir, "state.json"), append(data, '\n'))
}

// FetchToSpool pages Kraken's public trades for market from `from` until the
// first trade at or after `to`, appending them to spoolDir/trades.csv in the
// archive's CSV layout. After every page the file is synced and the cursor
// saved, so an interrupted fetch with the same start resumes where it
// stopped; trades at or below the last stored trade id are skipped, so
// overlapping pages never duplicate a trade. from and to are floored to the
// 30-second grid.
func (f *RESTFetcher) FetchToSpool(ctx context.Context, spoolDir, market string, from, to time.Time) (FetchResult, error) {
	f.defaults()
	from, to = from.UTC().Truncate(BarInterval), to.UTC().Truncate(BarInterval)
	pair, err := RESTPairName(market)
	if err != nil {
		return FetchResult{}, err
	}
	if !to.After(from) {
		return FetchResult{}, fmt.Errorf("end %v is not after start %v", to, from)
	}
	res := FetchResult{Market: market, Pair: pair, SpoolPath: filepath.Join(spoolDir, "trades.csv")}
	if err := os.MkdirAll(spoolDir, 0o755); err != nil {
		return res, err
	}
	st, ok, err := loadRESTState(spoolDir)
	if err != nil {
		return res, err
	}
	if ok && st.Market == market && st.From.Equal(from) {
		res.Resumed = true
		if st.Complete && !to.After(st.To) {
			res.Complete, res.Cursor, res.TotalTrades = true, st.Cursor, st.Trades
			return res, nil
		}
	} else {
		st = restState{Market: market, Pair: pair, From: from, Cursor: strconv.FormatInt(from.UnixNano(), 10)}
		if err := os.WriteFile(res.SpoolPath, []byte(spoolHeader+"\n"), 0o644); err != nil {
			return res, err
		}
		st.SpoolBytes = int64(len(spoolHeader) + 1)
		if err := saveRESTState(spoolDir, st); err != nil {
			return res, err
		}
	}
	st.To, st.Complete = to, false

	// Drop any bytes of a page whose cursor was never saved.
	if err := os.Truncate(res.SpoolPath, st.SpoolBytes); err != nil {
		return res, err
	}
	file, err := os.OpenFile(res.SpoolPath, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return res, err
	}
	defer file.Close()

	for {
		page, err := f.page(ctx, pair, st.Cursor)
		if err != nil {
			return res, err
		}
		res.Pages++
		buf := &strings.Builder{}
		w := csv.NewWriter(buf)
		stop := false
		var newest time.Time
		for _, t := range page.trades {
			if !t.Time.Before(to) {
				stop = true
				break
			}
			if t.ID != 0 && t.ID <= st.LastTradeID {
				continue // already spooled (pages overlap at the cursor)
			}
			_ = w.Write([]string{t.timeText, t.priceText, t.volumeText, t.side, t.orderType, t.misc, strconv.FormatInt(t.ID, 10)})
			st.LastTradeID, st.LastTradeTime = t.ID, t.Time
			st.Trades++
			res.NewTrades++
			newest = t.Time
		}
		w.Flush()
		if _, err := file.WriteString(buf.String()); err != nil {
			return res, err
		}
		if err := file.Sync(); err != nil {
			return res, err
		}
		st.SpoolBytes += int64(buf.Len())
		switch {
		case stop:
			// Resume later from the last stored trade; its id filters repeats.
			if !st.LastTradeTime.IsZero() {
				st.Cursor = strconv.FormatInt(st.LastTradeTime.UnixNano(), 10)
			}
			st.Complete = true
		case len(page.trades) == 0 || page.last == st.Cursor || page.last == "":
			res.CaughtUp = true
		default:
			st.Cursor = page.last
		}
		if err := saveRESTState(spoolDir, st); err != nil {
			return res, err
		}
		if !newest.IsZero() {
			res.Reached = newest
		}
		if f.Progress != nil && res.Pages%60 == 0 {
			f.Progress(fmt.Sprintf("%s: %d pages, %d trades, at %s", market, res.Pages, st.Trades, res.Reached.Format(time.RFC3339)))
		}
		if st.Complete || res.CaughtUp {
			break
		}
	}
	res.Complete, res.Cursor, res.TotalTrades = st.Complete, st.Cursor, st.Trades
	return res, nil
}

// restTrade is one parsed REST trade, keeping the exact text for the spool.
type restTrade struct {
	Trade
	timeText, priceText, volumeText string
	side, orderType, misc           string
}

// restPage is one parsed Trades response.
type restPage struct {
	trades []restTrade
	last   string
}

// errRetryable marks a failure worth retrying after a wait.
type errRetryable struct {
	err  error
	wait time.Duration // 0 means use the backoff schedule
}

// Error implements error.
func (e errRetryable) Error() string { return e.err.Error() }

// page fetches one page, pacing calls and retrying rate limits and server
// errors with exponential backoff.
func (f *RESTFetcher) page(ctx context.Context, pair, since string) (restPage, error) {
	backoff := f.BaseBackoff
	for attempt := 0; ; attempt++ {
		if err := f.pace(ctx); err != nil {
			return restPage{}, err
		}
		p, err := f.call(ctx, pair, since)
		if err == nil {
			return p, nil
		}
		var re errRetryable
		if !errors.As(err, &re) || f.MaxRetries < 0 || attempt >= f.MaxRetries {
			return restPage{}, err
		}
		wait := re.wait
		if wait <= 0 {
			wait = backoff
			backoff = min(2*backoff, f.MaxBackoff)
		}
		if f.Progress != nil {
			f.Progress(fmt.Sprintf("%s: %v; retrying in %v", pair, err, wait))
		}
		if err := f.Sleep(ctx, wait); err != nil {
			return restPage{}, err
		}
	}
}

// pace waits until MinInterval has passed since the previous call.
func (f *RESTFetcher) pace(ctx context.Context) error {
	if f.called {
		if wait := f.MinInterval - f.Now().Sub(f.lastCall); wait > 0 {
			if err := f.Sleep(ctx, wait); err != nil {
				return err
			}
		}
	}
	f.lastCall, f.called = f.Now(), true
	return nil
}

// throttledPattern extracts the retry time from "EService: Throttled: <unix>".
var throttledPattern = regexp.MustCompile(`Throttled:\s*(\d+)`)

// call performs one HTTP request and parses the response.
func (f *RESTFetcher) call(ctx context.Context, pair, since string) (restPage, error) {
	q := url.Values{"pair": {pair}, "since": {since}, "count": {strconv.Itoa(restPageSize)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(f.BaseURL, "/")+"/0/public/Trades?"+q.Encode(), nil)
	if err != nil {
		return restPage{}, err
	}
	resp, err := f.Client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return restPage{}, ctx.Err()
		}
		return restPage{}, errRetryable{err: err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return restPage{}, errRetryable{err: err}
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return restPage{}, errRetryable{err: fmt.Errorf("HTTP %d", resp.StatusCode)}
	}
	if resp.StatusCode != http.StatusOK {
		return restPage{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var env struct {
		Error  []string                   `json:"error"`
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return restPage{}, fmt.Errorf("decode Trades response: %w", err)
	}
	if len(env.Error) > 0 {
		msg := strings.Join(env.Error, "; ")
		switch {
		case strings.Contains(msg, "Rate limit"):
			return restPage{}, errRetryable{err: errors.New(msg)}
		case strings.Contains(msg, "Throttled"):
			re := errRetryable{err: errors.New(msg)}
			if m := throttledPattern.FindStringSubmatch(msg); m != nil {
				if ts, err := strconv.ParseInt(m[1], 10, 64); err == nil {
					re.wait = max(time.Unix(ts, 0).Sub(f.Now()), time.Second)
				}
			}
			return restPage{}, re
		case strings.Contains(msg, "EService:Unavailable"), strings.Contains(msg, "EService:Busy"), strings.Contains(msg, "EGeneral:Internal"):
			return restPage{}, errRetryable{err: errors.New(msg)}
		}
		return restPage{}, fmt.Errorf("Kraken: %s", msg)
	}
	return parseTradesResult(env.Result)
}

// parseTradesResult reads the result object: one pair key (whose name may
// differ from the requested one, e.g. XXBTZEUR for XBTEUR) and "last".
func parseTradesResult(result map[string]json.RawMessage) (restPage, error) {
	var page restPage
	var rowsRaw json.RawMessage
	keys := 0
	for k, v := range result {
		if k == "last" {
			var s json.Number
			if err := json.Unmarshal(v, &s); err != nil {
				var str string
				if err := json.Unmarshal(v, &str); err != nil {
					return page, fmt.Errorf("unreadable last cursor %s", v)
				}
				s = json.Number(str)
			}
			page.last = s.String()
			continue
		}
		keys++
		rowsRaw = v
	}
	if keys != 1 {
		return page, fmt.Errorf("Trades result has %d pair keys, expected 1", keys)
	}
	var rows [][]json.RawMessage
	if err := json.Unmarshal(rowsRaw, &rows); err != nil {
		return page, fmt.Errorf("decode trades: %w", err)
	}
	for i, row := range rows {
		if len(row) < 7 {
			return page, fmt.Errorf("trade %d has %d fields, expected 7", i, len(row))
		}
		text := func(j int) string {
			var s string
			if err := json.Unmarshal(row[j], &s); err == nil {
				return s
			}
			return strings.TrimSpace(string(row[j]))
		}
		t := restTrade{timeText: text(2), priceText: text(0), volumeText: text(1), side: text(3), orderType: text(4), misc: text(5)}
		var err error
		if t.Time, err = parseUnixTime(t.timeText); err != nil {
			return page, fmt.Errorf("trade %d: %w", i, err)
		}
		if t.ID, err = strconv.ParseInt(text(6), 10, 64); err != nil {
			return page, fmt.Errorf("trade %d: id %q: %w", i, text(6), err)
		}
		page.trades = append(page.trades, t)
	}
	return page, nil
}
