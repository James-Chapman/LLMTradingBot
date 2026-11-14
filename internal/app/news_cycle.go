package app

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/portfolio"
	"github.com/James-Chapman/LLMTradingBot/internal/storage"
)

const (
	// briefedNewsIDsMax bounds the set of already-briefed article IDs. When it
	// is exceeded the set is rebuilt from the cache alone, which is safe: an
	// article that has dropped out of the cache is older than all 60 cached
	// ones, so it cannot re-enter the cache and be counted as new.
	briefedNewsIDsMax = 1000
	// newsSummaryRunes caps the excerpt kept per cached article.
	newsSummaryRunes = 200
)

// newsEntry converts a fetched item into a cache entry with a short summary.
func newsEntry(item domain.NewsItem) storage.NewsEntry {
	var url *string
	if item.URL != "" {
		u := item.URL
		url = &u
	}
	summary := item.Content
	if r := []rune(summary); len(r) > newsSummaryRunes {
		summary = string(r[:newsSummaryRunes])
	}
	return storage.NewsEntry{
		ID: item.ID, Source: item.Source, Title: item.Title, URL: url,
		Summary: strings.TrimSpace(summary), PublishedAt: portfolio.FormatISO(item.PublishedAt),
	}
}

// mergeNews adds fetched entries to the previous cache and returns the
// newest limit entries, newest first. An article already in the cache keeps
// its cached entry, so the time it was first seen is not replaced by a later
// fetch; articles missing from this fetch (for example because their feed
// failed) stay cached.
func mergeNews(previous, fetched []storage.NewsEntry, limit int) []storage.NewsEntry {
	seen := make(map[string]bool, len(previous)+len(fetched))
	merged := make([]storage.NewsEntry, 0, len(previous)+len(fetched))
	for _, list := range [][]storage.NewsEntry{previous, fetched} {
		for _, n := range list {
			if seen[n.ID] {
				continue
			}
			seen[n.ID] = true
			merged = append(merged, n)
		}
	}
	// Stable: on equal times cached entries stay ahead of newly fetched ones.
	sort.SliceStable(merged, func(i, j int) bool {
		return publishedTime(merged[i]).After(publishedTime(merged[j]))
	})
	return merged[:min(len(merged), limit)]
}

// publishedTime parses a cache entry's ISO publish time (zero if unparseable).
func publishedTime(n storage.NewsEntry) time.Time {
	t, err := time.Parse(time.RFC3339Nano, n.PublishedAt)
	if err != nil {
		return time.Time{}
	}
	return t
}

// briefingGate lets at most one market briefing run at a time. Articles that
// arrive while a briefing is running are queued and briefed together in one
// follow-up once it finishes, instead of starting briefings that pile up
// behind a slow LLM.
type briefingGate struct {
	mu      sync.Mutex
	running bool
	pending []storage.NewsEntry // newest first
}

// begin reports whether the caller should start a briefing for articles now.
// If one is already running, the articles are queued for its follow-up.
func (g *briefingGate) begin(articles []storage.NewsEntry) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.running {
		g.running = true
		return true
	}
	// Later triggers carry newer articles, so they go in front.
	queued := append(append([]storage.NewsEntry(nil), articles...), g.pending...)
	g.pending = queued[:min(len(queued), latestNewsMax)]
	return false
}

// next returns the articles queued during the briefing that just finished,
// or marks the gate idle and returns false when nothing is queued.
func (g *briefingGate) next() ([]storage.NewsEntry, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.pending) == 0 {
		g.running = false
		return nil, false
	}
	batch := g.pending
	g.pending = nil
	return batch, true
}

// startBriefing runs run for articles in the background unless a briefing is
// already running, in which case the articles join that briefing's single
// follow-up run.
func (a *App) startBriefing(ctx context.Context, articles []storage.NewsEntry, run func(context.Context, []storage.NewsEntry)) {
	if !a.briefing.begin(articles) {
		a.logger.Info("Market briefing already running - new articles queued for one follow-up", "new_articles", len(articles))
		return
	}
	a.tasks.Add(1)
	go func() {
		defer a.tasks.Done()
		for batch, ok := articles, true; ok; batch, ok = a.briefing.next() {
			run(ctx, batch)
		}
	}()
}
