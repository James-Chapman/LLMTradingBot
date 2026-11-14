package histdata

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

// fakeClock is a manual clock whose Sleep advances time instantly.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

// Now returns the fake time.
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Sleep records d and advances the fake time by it.
func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
	return ctx.Err()
}

// fakeTrades is a Kraken-like /0/public/Trades server over a fixed trade list.
type fakeTrades struct {
	mu       sync.Mutex
	clock    *fakeClock
	trades   []Trade
	pageSize int
	failAt   map[int]string // request number (1-based) → Kraken error text, or "500"
	requests []fakeRequest
}

// fakeRequest records one request the server received.
type fakeRequest struct {
	pair  string
	since int64
	at    time.Time
}

// ServeHTTP answers like Kraken: trades with time >= since, at most pageSize,
// result keyed by the official pair name and "last" set to the last trade's
// time in nanoseconds.
func (s *fakeTrades) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	s.requests = append(s.requests, fakeRequest{pair: r.URL.Query().Get("pair"), since: since, at: s.clock.Now()})
	if msg, ok := s.failAt[len(s.requests)]; ok {
		if msg == "500" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"error": []string{msg}})
		return
	}
	var rows [][]any
	last := since
	for _, t := range s.trades {
		if t.Time.UnixNano() < since || len(rows) == s.pageSize {
			continue
		}
		side := "s"
		if t.Buy {
			side = "b"
		}
		rows = append(rows, []any{strconv.FormatFloat(t.Price, 'f', -1, 64), strconv.FormatFloat(t.Volume, 'f', -1, 64),
			json.Number(unixText(t.Time)), side, "m", "", t.ID})
		last = t.Time.UnixNano()
	}
	if rows == nil {
		rows = [][]any{}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"error": []string{},
		"result": map[string]any{"XXBTZEUR": rows, "last": strconv.FormatInt(last, 10)}})
}

// newFakeTrades builds a server with n trades 10 s apart from start.
func newFakeTrades(clock *fakeClock, start time.Time, n, pageSize int) *fakeTrades {
	s := &fakeTrades{clock: clock, pageSize: pageSize, failAt: map[int]string{}}
	for i := range n {
		s.trades = append(s.trades, Trade{Time: start.Add(time.Duration(i) * 10 * time.Second), Price: 100 + float64(i),
			Volume: 1, Buy: i%2 == 0, ID: int64(i + 1)})
	}
	return s
}

// newTestFetcher returns a fetcher pointed at srv with the fake clock.
func newTestFetcher(srv *httptest.Server, clock *fakeClock) *RESTFetcher {
	return &RESTFetcher{BaseURL: srv.URL, Client: srv.Client(), Now: clock.Now, Sleep: clock.Sleep, MaxRetries: 3}
}

// TestRESTFetch covers paging Kraken's public Trades endpoint.
func TestRESTFetch(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Minute) // trades at 0..50 s are inside, 60 s is not

	t.Run("GIVEN several pages WHEN fetched THEN the last cursor is followed, the end time stops paging and calls are paced", func(t *testing.T) {
		clock := &fakeClock{now: time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)}
		fake := newFakeTrades(clock, start, 9, 2)
		srv := httptest.NewServer(fake)
		defer srv.Close()
		dir := t.TempDir()
		res, sum, err := openCache(t, dir).FetchREST(ctx, newTestFetcher(srv, clock), "BTC/EUR", start, end, ImportOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if !res.Complete || res.TotalTrades != 6 || sum.Trades != 6 {
			t.Fatalf("result = %+v, summary trades %d", res, sum.Trades)
		}
		if fake.requests[0].pair != "XBTEUR" || fake.requests[0].since != start.UnixNano() {
			t.Errorf("first request = %+v", fake.requests[0])
		}
		for i := 1; i < len(fake.requests); i++ {
			if fake.requests[i].since <= fake.requests[i-1].since {
				t.Errorf("request %d did not advance the cursor: %+v", i, fake.requests)
			}
			if gap := fake.requests[i].at.Sub(fake.requests[i-1].at); gap < 1100*time.Millisecond {
				t.Errorf("requests %d and %d only %v apart", i-1, i, gap)
			}
		}
		for _, r := range fake.requests {
			if r.since >= end.UnixNano() {
				t.Errorf("paged past the end time: %+v", r)
			}
		}
		bars := allBars(t, dir, "BTC/EUR")
		if len(bars) != 2 || bars[0].Trades != 3 || bars[1].Trades != 3 || bars[1].Close != 105 {
			t.Fatalf("bars = %+v", bars)
		}
		m, _ := LoadManifest(dir)
		if s := m.Markets["BTC/EUR"].Months["2026-10"].Sources[0]; s.Source != "rest" {
			t.Errorf("source = %+v", s)
		}
	})

	t.Run("GIVEN an interrupted fetch WHEN fetched again THEN it resumes from the stored cursor without duplicating trades", func(t *testing.T) {
		clock := &fakeClock{now: time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)}
		fake := newFakeTrades(clock, start, 9, 2)
		fake.failAt[3] = "500"
		srv := httptest.NewServer(fake)
		defer srv.Close()
		dir := t.TempDir()
		c := openCache(t, dir)
		f := newTestFetcher(srv, clock)
		f.MaxRetries = -1
		if _, _, err := c.FetchREST(ctx, f, "BTC/EUR", start, end, ImportOptions{}); err == nil {
			t.Fatal("expected the interrupted fetch to fail")
		}
		interruptedAt := len(fake.requests)
		res, sum, err := c.FetchREST(ctx, newTestFetcher(srv, clock), "BTC/EUR", start, end, ImportOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if !res.Resumed || fake.requests[interruptedAt].since == start.UnixNano() {
			t.Errorf("did not resume: %+v, requests %+v", res, fake.requests)
		}
		if res.TotalTrades != 6 || sum.Trades != 6 {
			t.Fatalf("trades = %d / %d, want 6", res.TotalTrades, sum.Trades)
		}
		if sum.FindingCounts[KindDuplicateID] != 0 {
			t.Errorf("duplicates were spooled: %v", sum.FindingCounts)
		}
	})

	t.Run("GIVEN a rate-limit error WHEN fetched THEN it backs off and retries", func(t *testing.T) {
		clock := &fakeClock{now: time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)}
		fake := newFakeTrades(clock, start, 3, 1000)
		fake.failAt[1] = "EAPI:Rate limit exceeded"
		srv := httptest.NewServer(fake)
		defer srv.Close()
		res, _, err := openCache(t, t.TempDir()).FetchREST(ctx, newTestFetcher(srv, clock), "BTC/EUR", start, end, ImportOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if res.TotalTrades != 3 {
			t.Fatalf("result = %+v", res)
		}
		if gap := fake.requests[1].at.Sub(fake.requests[0].at); gap < defaultRESTBackoff {
			t.Errorf("retried after only %v", gap)
		}
	})

	t.Run("GIVEN a throttle error with a retry time WHEN fetched THEN it waits until that time", func(t *testing.T) {
		clock := &fakeClock{now: time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)}
		retryAt := clock.now.Add(42 * time.Second)
		fake := newFakeTrades(clock, start, 3, 1000)
		fake.failAt[1] = fmt.Sprintf("EService: Throttled: %d", retryAt.Unix())
		srv := httptest.NewServer(fake)
		defer srv.Close()
		if _, _, err := openCache(t, t.TempDir()).FetchREST(ctx, newTestFetcher(srv, clock), "BTC/EUR", start, end, ImportOptions{}); err != nil {
			t.Fatal(err)
		}
		if fake.requests[1].at.Before(retryAt) {
			t.Errorf("retried at %v, before %v", fake.requests[1].at, retryAt)
		}
	})

	t.Run("GIVEN a non-retryable Kraken error WHEN fetched THEN it fails with that error", func(t *testing.T) {
		clock := &fakeClock{now: time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)}
		fake := newFakeTrades(clock, start, 3, 1000)
		fake.failAt[1] = "EQuery:Unknown asset pair"
		srv := httptest.NewServer(fake)
		defer srv.Close()
		_, _, err := openCache(t, t.TempDir()).FetchREST(ctx, newTestFetcher(srv, clock), "BTC/EUR", start, end, ImportOptions{})
		if err == nil || len(fake.requests) != 1 {
			t.Fatalf("err = %v after %d requests", err, len(fake.requests))
		}
	})
}
