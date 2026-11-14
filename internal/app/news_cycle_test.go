package app

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/news"
	"github.com/James-Chapman/LLMTradingBot/internal/storage"
)

// scriptedNews is a news adapter whose successive fetches follow a script; a
// nil entry is a failed fetch and the last entry repeats once the script ends.
type scriptedNews struct {
	mu     sync.Mutex
	name   string
	script [][]domain.NewsItem
	calls  int
}

// SourceName returns the adapter's name.
func (s *scriptedNews) SourceName() string { return s.name }

// FetchNews returns the next scripted result.
func (s *scriptedNews) FetchNews(context.Context) ([]domain.NewsItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := min(s.calls, len(s.script)-1)
	s.calls++
	if s.script[i] == nil {
		return []domain.NewsItem{}, errTest(s.name + " down")
	}
	return append([]domain.NewsItem(nil), s.script[i]...), nil
}

// newsItem builds an article from source published age ago.
func newsItem(id, source string, age time.Duration) domain.NewsItem {
	return domain.NewsItem{ID: id, Source: source, Title: "Article " + id, PublishedAt: time.Now().UTC().Add(-age), URL: "https://x.test/" + id}
}

// briefingTriggers returns the new-article count of every "Market briefing
// triggered" log record written since the buffer was last reset.
func briefingTriggers(t *testing.T, logs *bytes.Buffer) []int {
	t.Helper()
	var counts []int
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) != nil || record["msg"] != "Market briefing triggered" {
			continue
		}
		n, _ := record["new_articles"].(float64)
		counts = append(counts, int(n))
	}
	return counts
}

// cachedIDs returns the IDs in the app's news cache.
func cachedIDs(a *App) map[string]bool {
	ids := map[string]bool{}
	for _, n := range a.News() {
		ids[n.ID] = true
	}
	return ids
}

// runNewsCycle runs one news cycle, ignoring the all-sources-failed error.
func runNewsCycle(env *testEnv) {
	_ = env.app.newsCycle(context.Background())
	env.app.Wait()
}

// TestNewsCycleCache covers how the news cache survives failed and repeated fetches.
func TestNewsCycleCache(t *testing.T) {
	t.Run("GIVEN two feeds WHEN one fails for a cycle and recovers THEN its articles stay cached and none counts as new twice", func(t *testing.T) {
		env := newEnv(t, nil, nil)
		a := []domain.NewsItem{newsItem("a1", "A", time.Hour), newsItem("a2", "A", 2*time.Hour)}
		b := []domain.NewsItem{newsItem("b1", "B", 30*time.Minute), newsItem("b2", "B", 90*time.Minute)}
		env.app.newsAdapters = []news.Adapter{
			&scriptedNews{name: "A", script: [][]domain.NewsItem{a}},
			&scriptedNews{name: "B", script: [][]domain.NewsItem{b, nil, b, append(b, newsItem("b3", "B", time.Minute))}},
		}
		env.logs.Reset()

		runNewsCycle(env) // both feeds up
		runNewsCycle(env) // B fails
		duringFailure := cachedIDs(env.app)
		runNewsCycle(env) // B recovers with the same articles
		runNewsCycle(env) // B publishes one more

		if !duringFailure["b1"] || !duringFailure["b2"] || len(duringFailure) != 4 {
			t.Errorf("cache during the failure = %v, want all four articles", duringFailure)
		}
		if got := briefingTriggers(t, env.logs); len(got) != 2 || got[0] != 4 || got[1] != 1 {
			t.Errorf("briefing triggers = %v, want [4 1] (only genuinely unseen articles)", got)
		}
		if ids := cachedIDs(env.app); len(ids) != 5 || !ids["b3"] {
			t.Errorf("cache = %v, want the five distinct articles", ids)
		}
	})

	t.Run("GIVEN a filled cache WHEN every feed fails THEN the previous cache is kept and nothing is briefed", func(t *testing.T) {
		env := newEnv(t, nil, nil)
		items := []domain.NewsItem{newsItem("a1", "A", time.Hour), newsItem("b1", "B", time.Minute)}
		env.app.newsAdapters = []news.Adapter{
			&scriptedNews{name: "A", script: [][]domain.NewsItem{items[:1], nil}},
			&scriptedNews{name: "B", script: [][]domain.NewsItem{items[1:], nil}},
		}
		runNewsCycle(env)
		before := env.app.News()
		env.logs.Reset()

		err := env.app.newsCycle(context.Background())
		env.app.Wait()

		after := env.app.News()
		if err == nil {
			t.Error("expected an error when every news source fails")
		}
		if len(after) != len(before) || len(after) != 2 || after[0].ID != before[0].ID {
			t.Errorf("cache = %+v, want the previous %+v", after, before)
		}
		if got := briefingTriggers(t, env.logs); len(got) != 0 {
			t.Errorf("briefing triggers = %v, want none", got)
		}
	})

	t.Run("GIVEN a Fear & Greed reading WHEN later cycles return no item or the identical item THEN it stays cached once and is not new again", func(t *testing.T) {
		env := newEnv(t, nil, nil)
		reading := newsItem("fg-20", news.FearGreedSource, 0)
		env.app.newsAdapters = []news.Adapter{&scriptedNews{name: news.FearGreedSource, script: [][]domain.NewsItem{{reading}, {}, {reading}}}}
		env.logs.Reset()

		runNewsCycle(env)
		runNewsCycle(env) // unchanged reading: the adapter returns nothing
		afterQuiet := cachedIDs(env.app)
		runNewsCycle(env) // the same reading again

		if !afterQuiet["fg-20"] {
			t.Errorf("Fear & Greed reading dropped from the cache: %v", afterQuiet)
		}
		if n := len(env.app.News()); n != 1 {
			t.Errorf("cache holds %d items, want the reading once", n)
		}
		if got := briefingTriggers(t, env.logs); len(got) != 1 || got[0] != 1 {
			t.Errorf("briefing triggers = %v, want [1]", got)
		}
	})

	t.Run("GIVEN an article re-fetched with a later timestamp WHEN merged THEN it keeps the time it was first seen", func(t *testing.T) {
		env := newEnv(t, nil, nil)
		first := newsItem("u1", "A", time.Hour)
		later := first
		later.PublishedAt = time.Now().UTC()
		older := newsItem("o1", "A", 30*time.Minute)
		env.app.newsAdapters = []news.Adapter{&scriptedNews{name: "A", script: [][]domain.NewsItem{{first, older}, {later, older}}}}

		runNewsCycle(env)
		runNewsCycle(env)

		cached := env.app.News()
		if len(cached) != 2 || cached[0].ID != "o1" || cached[1].ID != "u1" {
			t.Errorf("cache order = %+v, want o1 before u1 (u1 keeps its first-seen time)", cached)
		}
	})

	t.Run("GIVEN more than the cache size WHEN merged THEN only the newest articles are kept, newest first", func(t *testing.T) {
		env := newEnv(t, nil, nil)
		var items []domain.NewsItem
		for i := range latestNewsMax + 10 {
			items = append(items, newsItem("n"+strconv.Itoa(i), "A", time.Duration(i)*time.Minute))
		}
		env.app.newsAdapters = []news.Adapter{&scriptedNews{name: "A", script: [][]domain.NewsItem{items}}}

		runNewsCycle(env)

		cached := env.app.News()
		if len(cached) != latestNewsMax || cached[0].ID != "n0" || cached[len(cached)-1].ID != "n"+strconv.Itoa(latestNewsMax-1) {
			t.Errorf("cache has %d items from %s to %s, want the %d newest", len(cached), cached[0].ID, cached[len(cached)-1].ID, latestNewsMax)
		}
	})
}

// TestMarketBriefingGate covers running at most one market briefing at a time.
func TestMarketBriefingGate(t *testing.T) {
	t.Run("GIVEN a briefing is running WHEN more triggers arrive THEN no second briefing runs concurrently and they are coalesced into one follow-up", func(t *testing.T) {
		env := newEnv(t, nil, nil)
		release := make(chan struct{})
		started := make(chan struct{}, 10)
		var mu sync.Mutex
		running, maxRunning := 0, 0
		var batches [][]string
		run := func(_ context.Context, articles []storage.NewsEntry) {
			mu.Lock()
			running++
			maxRunning = max(maxRunning, running)
			ids := make([]string, 0, len(articles))
			for _, n := range articles {
				ids = append(ids, n.ID)
			}
			batches = append(batches, ids)
			mu.Unlock()
			started <- struct{}{}
			<-release
			mu.Lock()
			running--
			mu.Unlock()
		}
		ctx := context.Background()

		env.app.startBriefing(ctx, []storage.NewsEntry{{ID: "first"}}, run)
		<-started // the first briefing is now in progress
		env.app.startBriefing(ctx, []storage.NewsEntry{{ID: "second"}}, run)
		env.app.startBriefing(ctx, []storage.NewsEntry{{ID: "third"}}, run)
		select {
		case <-started:
			t.Error("a second briefing started while the first was still running")
		case <-time.After(100 * time.Millisecond):
		}
		close(release)
		env.app.Wait()

		mu.Lock()
		defer mu.Unlock()
		if maxRunning != 1 {
			t.Errorf("max concurrent briefings = %d, want 1", maxRunning)
		}
		if len(batches) != 2 || strings.Join(batches[0], ",") != "first" || strings.Join(batches[1], ",") != "third,second" {
			t.Errorf("briefing batches = %v, want [[first] [third second]] (newest first)", batches)
		}
	})

	t.Run("GIVEN the follow-up briefing finished WHEN a new trigger arrives THEN a fresh briefing starts", func(t *testing.T) {
		env := newEnv(t, nil, nil)
		var mu sync.Mutex
		runs := 0
		run := func(context.Context, []storage.NewsEntry) {
			mu.Lock()
			runs++
			mu.Unlock()
		}

		env.app.startBriefing(context.Background(), []storage.NewsEntry{{ID: "a"}}, run)
		env.app.Wait()
		env.app.startBriefing(context.Background(), []storage.NewsEntry{{ID: "b"}}, run)
		env.app.Wait()

		mu.Lock()
		defer mu.Unlock()
		if runs != 2 {
			t.Errorf("briefings run = %d, want 2", runs)
		}
	})
}
