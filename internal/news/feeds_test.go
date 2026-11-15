package news

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
)

const sampleAtomFeed = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Atom Test</title>
  <link href="https://example.test/"/>
  <entry>
    <title type="html"><![CDATA[Stablecoins gain ground]]></title>
    <id>https://example.test/news/stable</id>
    <link rel="enclosure" href="https://img.example.test/a.png" type="image/png"/>
    <link href="https://example.test/news/stable"/>
    <updated>2026-11-14T09:00:00.000Z</updated>
    <published>2026-11-14T08:00:00.000Z</published>
    <summary type="html"><![CDATA[<p>Digital dollars &amp; more</p>]]></summary>
  </entry>
  <entry>
    <title>Only updated</title>
    <link rel="alternate" href="https://example.test/news/updated"/>
    <updated>2026-11-13T10:30:00+02:00</updated>
    <content type="html">Body text</content>
  </entry>
  <entry>
    <title></title>
    <link href="https://example.test/news/empty"/>
  </entry>
</feed>`

// TestParseAtom covers reading Atom feeds such as Blockworks with the RSS adapter.
func TestParseAtom(t *testing.T) {
	now := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)

	t.Run("GIVEN an Atom document WHEN parsed THEN entries become items with their page link, summary and publish time", func(t *testing.T) {
		items, err := ParseRSS([]byte(sampleAtomFeed), "Atom", 20, now)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(items) != 2 {
			t.Fatalf("expected 2 items (untitled entry skipped), got %d: %+v", len(items), items)
		}
		first := items[0]
		if first.Title != "Stablecoins gain ground" || first.URL != "https://example.test/news/stable" {
			t.Errorf("first item = %+v, want the title and the alternate (not enclosure) link", first)
		}
		if first.Content != "Digital dollars & more" {
			t.Errorf("content = %q, want the cleaned summary", first.Content)
		}
		if !first.PublishedAt.Equal(time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)) {
			t.Errorf("published = %v, want the <published> time", first.PublishedAt)
		}
	})

	t.Run("GIVEN an Atom entry with only <updated> and <content> WHEN parsed THEN those are used", func(t *testing.T) {
		items, _ := ParseRSS([]byte(sampleAtomFeed), "Atom", 20, now)

		second := items[1]
		if second.URL != "https://example.test/news/updated" || second.Content != "Body text" {
			t.Errorf("second item = %+v", second)
		}
		if !second.PublishedAt.Equal(time.Date(2026, 11, 13, 8, 30, 0, 0, time.UTC)) || second.PublishedAt.Location() != time.UTC {
			t.Errorf("published = %v, want the <updated> time in UTC", second.PublishedAt)
		}
	})

	t.Run("GIVEN an Atom feed served over HTTP WHEN fetched THEN items carry the adapter's source name", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(sampleAtomFeed))
		}))
		defer server.Close()
		adapter := NewRSSAdapter("Blockworks", server.URL)
		adapter.Client = server.Client()

		items, err := adapter.FetchNews(context.Background())

		if err != nil || len(items) != 2 || items[0].Source != "Blockworks" {
			t.Fatalf("items %+v, err %v", items, err)
		}
	})
}

// TestBuildAdapters covers turning the feed catalogue and extra feeds into adapters.
func TestBuildAdapters(t *testing.T) {
	builtIn := []config.NewsFeed{
		{Name: "Alpha", URL: "https://alpha.test/rss", Category: "Crypto news", Kind: config.NewsKindFeed},
		{Name: FearGreedSource, URL: FearGreedPageURL, Category: "Market sentiment", Kind: config.NewsKindFearGreed},
	}

	t.Run("GIVEN built-in feeds and extra feeds WHEN built THEN there is one adapter per feed, built-in first", func(t *testing.T) {
		adapters := BuildAdapters(builtIn, []string{"Mine::https://mine.test/rss"})

		names := []string{}
		for _, a := range adapters {
			names = append(names, a.SourceName())
		}
		if len(names) != 3 || names[0] != "Alpha" || names[1] != FearGreedSource || names[2] != "Mine" {
			t.Fatalf("adapters = %v, want [Alpha Fear & Greed Mine]", names)
		}
		if _, ok := adapters[1].(*FearGreedAdapter); !ok {
			t.Errorf("Fear & Greed should use the index adapter, got %T", adapters[1])
		}
		if rss, ok := adapters[0].(*RSSAdapter); !ok || rss.URL != "https://alpha.test/rss" {
			t.Errorf("Alpha should be an RSS adapter for its URL, got %#v", adapters[0])
		}
	})

	t.Run("GIVEN an extra feed named like a built-in feed WHEN built THEN it is not added twice", func(t *testing.T) {
		adapters := BuildAdapters(builtIn, []string{"alpha::https://elsewhere.test/rss", "bad spec"})

		if len(adapters) != 2 {
			t.Fatalf("expected only the 2 built-in adapters, got %d", len(adapters))
		}
	})

	t.Run("GIVEN the shipped catalogue WHEN compared with this package THEN Fear & Greed has the same name", func(t *testing.T) {
		if config.FearGreedFeedName != FearGreedSource {
			t.Fatalf("config names Fear & Greed %q, the adapter %q", config.FearGreedFeedName, FearGreedSource)
		}
	})
}
