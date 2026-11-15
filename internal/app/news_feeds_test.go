package app

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/news"
)

// fetchCount returns how many times a scripted adapter has been asked for news.
func fetchCount(s *scriptedNews) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// TestNewsFeedSwitches covers switching individual news feeds off from the dashboard.
func TestNewsFeedSwitches(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN two feeds with cached articles WHEN one is switched off THEN it applies at once, is not fetched and its articles leave the cache", func(t *testing.T) {
		env := newEnv(t, nil, nil)
		a := &scriptedNews{name: "A", script: [][]domain.NewsItem{{newsItem("a1", "A", time.Hour)}}}
		b := &scriptedNews{name: "B", script: [][]domain.NewsItem{{newsItem("b1", "B", time.Minute)}}}
		env.app.newsAdapters = []news.Adapter{a, b}
		runNewsCycle(env)

		result, err := env.app.UpdateSettings(ctx, map[string]string{"DISABLED_NEWS_SOURCES": `["B"]`})
		if err != nil {
			t.Fatalf("UpdateSettings: %v", err)
		}
		runNewsCycle(env)

		if !slices.Contains(result.AppliedLive, "DISABLED_NEWS_SOURCES") {
			t.Errorf("applied live = %v, want the feed switches in use at once", result.AppliedLive)
		}
		if fetchCount(b) != 1 || fetchCount(a) != 2 {
			t.Errorf("fetches A=%d B=%d, want A every cycle and B only before it was switched off", fetchCount(a), fetchCount(b))
		}
		if ids := cachedIDs(env.app); ids["b1"] || !ids["a1"] {
			t.Errorf("cache = %v, want A's article only", ids)
		}
	})

	t.Run("GIVEN a switched-off feed WHEN it is switched back on THEN the next cycle fetches it again", func(t *testing.T) {
		env := newEnv(t, func(s *config.Settings) { s.DisabledNewsSources = []string{"B"} }, nil)
		b := &scriptedNews{name: "B", script: [][]domain.NewsItem{{newsItem("b1", "B", time.Minute)}}}
		env.app.newsAdapters = []news.Adapter{b}
		runNewsCycle(env)

		if _, err := env.app.UpdateSettings(ctx, map[string]string{"DISABLED_NEWS_SOURCES": `[]`}); err != nil {
			t.Fatalf("UpdateSettings: %v", err)
		}
		runNewsCycle(env)

		if fetchCount(b) != 1 || !cachedIDs(env.app)["b1"] {
			t.Errorf("fetches=%d cache=%v, want B fetched once after being switched on", fetchCount(b), cachedIDs(env.app))
		}
	})

	t.Run("GIVEN every feed switched off WHEN the news cycle runs THEN nothing is fetched and no failure is reported", func(t *testing.T) {
		env := newEnv(t, func(s *config.Settings) { s.DisabledNewsSources = []string{"A"} }, nil)
		a := &scriptedNews{name: "A", script: [][]domain.NewsItem{nil}}
		env.app.newsAdapters = []news.Adapter{a}

		err := env.app.newsCycle(ctx)
		env.app.Wait()

		if err != nil || fetchCount(a) != 0 {
			t.Fatalf("err=%v fetches=%d, want no error and no fetch", err, fetchCount(a))
		}
		if !env.activityContains("All news feeds are switched off") {
			t.Error("expected an activity entry saying every feed is off")
		}
	})

	t.Run("GIVEN the settings view WHEN read THEN it lists the built-in feeds for the switches", func(t *testing.T) {
		env := newEnv(t, nil, nil)

		view := env.app.SettingsView()

		if len(view.NewsFeeds) != len(config.BuiltInNewsFeeds()) || view.NewsFeeds[0].Name == "" {
			t.Fatalf("news feeds = %+v, want the built-in catalogue", view.NewsFeeds)
		}
		fieldView(t, view, "DISABLED_NEWS_SOURCES")
	})
}
