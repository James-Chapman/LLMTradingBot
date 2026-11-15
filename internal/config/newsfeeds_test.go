package config

import (
	"slices"
	"strings"
	"testing"
)

// TestBuiltInNewsFeeds covers the catalogue of feeds the bot ships with.
func TestBuiltInNewsFeeds(t *testing.T) {
	t.Run("GIVEN the catalogue WHEN listed THEN every feed has a unique name, a category and an http(s) URL", func(t *testing.T) {
		seen := map[string]bool{}
		for _, f := range BuiltInNewsFeeds() {
			key := strings.ToLower(f.Name)
			if seen[key] {
				t.Errorf("duplicate feed name %q", f.Name)
			}
			seen[key] = true
			if f.Category == "" || !strings.HasPrefix(f.URL, "https://") {
				t.Errorf("%s: needs a category and an https URL, got %+v", f.Name, f)
			}
		}
	})

	t.Run("GIVEN the catalogue WHEN listed THEN it holds the original, new crypto, macro and sentiment feeds", func(t *testing.T) {
		names := []string{}
		for _, f := range BuiltInNewsFeeds() {
			names = append(names, f.Name)
		}
		for _, want := range []string{"CoinDesk", "NewsBTC", "CryptoBriefing", "Bitcoin.com News", "Blockworks",
			"Federal Reserve", "SEC", "ECB", FearGreedFeedName} {
			if !slices.Contains(names, want) {
				t.Errorf("catalogue is missing %q: %v", want, names)
			}
		}
	})

	t.Run("GIVEN the catalogue WHEN listed THEN only Fear & Greed is an index rather than a feed", func(t *testing.T) {
		for _, f := range BuiltInNewsFeeds() {
			if (f.Kind == NewsKindFearGreed) != (f.Name == FearGreedFeedName) {
				t.Errorf("%s has kind %q", f.Name, f.Kind)
			}
		}
	})

	t.Run("GIVEN a caller edits the returned catalogue WHEN listed again THEN the built-in list is unchanged", func(t *testing.T) {
		feeds := BuiltInNewsFeeds()
		feeds[0].Name = "Changed"

		if BuiltInNewsFeeds()[0].Name == "Changed" {
			t.Fatal("the catalogue must be returned as a copy")
		}
	})
}

// TestNewsFeedSettings covers the extra-feed list and the per-feed switches.
func TestNewsFeedSettings(t *testing.T) {
	t.Run("GIVEN no saved settings WHEN defaults are used THEN there are no extra feeds and every feed is switched on", func(t *testing.T) {
		s := Defaults()

		if len(s.NewsSources) != 0 || len(s.DisabledNewsSources) != 0 {
			t.Fatalf("extra feeds %v, switched off %v; want both empty", s.NewsSources, s.DisabledNewsSources)
		}
	})

	t.Run("GIVEN the feed switches WHEN the registry is listed THEN they are a live list in the news group", func(t *testing.T) {
		f, ok := FieldByKey("DISABLED_NEWS_SOURCES")

		if !ok || !f.Live || f.Kind != KindList || f.Group != GroupNews {
			t.Fatalf("expected a live news list field, got %+v (found %v)", f, ok)
		}
	})

	t.Run("GIVEN a saved extra-feed list that repeats built-in feeds WHEN applied THEN only the genuinely extra feeds remain", func(t *testing.T) {
		s := Defaults()
		f, _ := FieldByKey("NEWS_SOURCES")

		err := f.Apply(&s, `["CoinDesk::https://www.coindesk.com/arc/outboundfeeds/rss/","coindesk::https://other.test/rss","My Feed::https://my.test/rss"]`)

		if err != nil || len(s.NewsSources) != 1 || s.NewsSources[0] != "My Feed::https://my.test/rss" {
			t.Fatalf("got %v (err %v), want only My Feed", s.NewsSources, err)
		}
	})

	t.Run("GIVEN an extra feed that is not Name::URL WHEN applied THEN it is refused and the list is unchanged", func(t *testing.T) {
		for _, raw := range []string{`["no separator"]`, `["Name::ftp://x.test/rss"]`, `["::https://x.test/rss"]`} {
			s := Defaults()
			f, _ := FieldByKey("NEWS_SOURCES")

			if err := f.Apply(&s, raw); err == nil || len(s.NewsSources) != 0 {
				t.Errorf("%s: expected a refusal, got %v (err %v)", raw, s.NewsSources, err)
			}
		}
	})

	t.Run("GIVEN two extra feeds with the same name WHEN applied THEN the list is refused", func(t *testing.T) {
		s := Defaults()
		f, _ := FieldByKey("NEWS_SOURCES")

		if err := f.Apply(&s, `["Mine::https://a.test/rss","mine::https://b.test/rss"]`); err == nil {
			t.Fatalf("expected a duplicate-name refusal, got %v", s.NewsSources)
		}
	})

	t.Run("GIVEN switched-off names with spaces and repeats WHEN applied THEN each name is kept once, trimmed", func(t *testing.T) {
		s := Defaults()
		f, _ := FieldByKey("DISABLED_NEWS_SOURCES")

		err := f.Apply(&s, `[" SEC ","SEC","","Fear & Greed"]`)

		if err != nil || !slices.Equal(s.DisabledNewsSources, []string{"SEC", "Fear & Greed"}) {
			t.Fatalf("got %v (err %v), want [SEC Fear & Greed]", s.DisabledNewsSources, err)
		}
	})
}
