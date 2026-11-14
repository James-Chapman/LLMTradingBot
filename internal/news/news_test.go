package news

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

const sampleFeed = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:atom="http://www.w3.org/2005/Atom">
<channel>
  <title>Test Feed</title>
  <atom:link href="https://example.test/feed" rel="self"/>
  <item>
    <title>  Bitcoin hits new high  </title>
    <link>https://example.test/btc</link>
    <description><![CDATA[<p>BTC rallied &amp; more</p>]]></description>
    <pubDate>Sat, 25 Apr 2026 12:00:00 +0200</pubDate>
  </item>
  <item>
    <title></title>
    <link>https://example.test/empty</link>
  </item>
  <item>
    <title>Ether update</title>
    <link>https://example.test/eth</link>
    <description>ETH &nbsp; news</description>
    <pubDate>not a date</pubDate>
  </item>
</channel>
</rss>`

// TestRSSAdapterFromSpec covers 'Name::URL' spec parsing.
func TestRSSAdapterFromSpec(t *testing.T) {
	t.Run("GIVEN a valid 'Name::URL' spec WHEN parsed THEN an adapter with that name and URL is returned", func(t *testing.T) {
		adapter := RSSAdapterFromSpec("CoinDesk::https://www.coindesk.com/arc/outboundfeeds/rss/")

		if adapter == nil || adapter.SourceName() != "CoinDesk" || adapter.URL != "https://www.coindesk.com/arc/outboundfeeds/rss/" {
			t.Fatalf("unexpected adapter %+v", adapter)
		}
	})

	t.Run("GIVEN a spec with extra whitespace WHEN parsed THEN name and URL are trimmed", func(t *testing.T) {
		adapter := RSSAdapterFromSpec("  My Source  ::  https://example.com/feed  ")

		if adapter == nil || adapter.SourceName() != "My Source" || adapter.URL != "https://example.com/feed" {
			t.Fatalf("unexpected adapter %+v", adapter)
		}
	})

	t.Run("GIVEN invalid specs WHEN parsed THEN no adapter is returned", func(t *testing.T) {
		for _, spec := range []string{"CoinDesk", "::https://example.com/feed", "CoinDesk::", "   ::   "} {
			if adapter := RSSAdapterFromSpec(spec); adapter != nil {
				t.Errorf("expected nil for %q, got %+v", spec, adapter)
			}
		}
	})

	t.Run("GIVEN a URL containing '::' WHEN parsed THEN only the first separator splits name from URL", func(t *testing.T) {
		adapter := RSSAdapterFromSpec("Source::https://example.com/feed?a::b")

		if adapter == nil || adapter.SourceName() != "Source" || adapter.URL != "https://example.com/feed?a::b" {
			t.Fatalf("unexpected adapter %+v", adapter)
		}
	})
}

// TestBuildRSSAdapters covers building adapters from configured specs.
func TestBuildRSSAdapters(t *testing.T) {
	t.Run("GIVEN valid specs WHEN built THEN one adapter per spec is returned in order", func(t *testing.T) {
		adapters := BuildRSSAdapters([]string{"CoinDesk::https://www.coindesk.com/rss", "CoinTelegraph::https://cointelegraph.com/rss"})

		if len(adapters) != 2 || adapters[0].SourceName() != "CoinDesk" || adapters[1].SourceName() != "CoinTelegraph" {
			t.Fatalf("unexpected adapters %+v", adapters)
		}
	})

	t.Run("GIVEN valid and invalid specs WHEN built THEN invalid specs are skipped", func(t *testing.T) {
		adapters := BuildRSSAdapters([]string{"CoinDesk::https://www.coindesk.com/rss", "bad-spec-no-separator", "CoinTelegraph::https://cointelegraph.com/rss"})

		if len(adapters) != 2 {
			t.Fatalf("expected 2 adapters, got %d", len(adapters))
		}
	})

	t.Run("GIVEN no specs WHEN built THEN an empty list is returned", func(t *testing.T) {
		if adapters := BuildRSSAdapters(nil); len(adapters) != 0 {
			t.Fatalf("expected no adapters, got %d", len(adapters))
		}
	})
}

// TestRSSAdapterFetch covers fetching and parsing RSS over HTTP.
func TestRSSAdapterFetch(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN a configured adapter WHEN news is fetched THEN its URL is requested and items carry its source name", func(t *testing.T) {
		var gotPath, gotUA string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath, gotUA = r.URL.Path, r.Header.Get("User-Agent")
			_, _ = w.Write([]byte(sampleFeed))
		}))
		defer server.Close()
		adapter := RSSAdapterFromSpec("TestFeed::" + server.URL + "/rss")
		adapter.Client = server.Client()

		items, err := adapter.FetchNews(ctx)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if gotPath != "/rss" || !strings.Contains(gotUA, "KrakenBot") {
			t.Fatalf("unexpected request path=%q ua=%q", gotPath, gotUA)
		}
		if len(items) != 2 {
			t.Fatalf("expected 2 items (empty title skipped), got %d", len(items))
		}
		for _, item := range items {
			if item.Source != "TestFeed" {
				t.Fatalf("expected source TestFeed, got %q", item.Source)
			}
		}
	})

	t.Run("GIVEN an RSS document WHEN parsed THEN items have trimmed fields, stable IDs and UTC publish times", func(t *testing.T) {
		now := time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC)

		items, err := ParseRSS([]byte(sampleFeed), "Feed", 20, now)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		first := items[0]
		if first.Title != "Bitcoin hits new high" || first.URL != "https://example.test/btc" || !strings.Contains(first.Content, "BTC rallied") {
			t.Fatalf("unexpected item %+v", first)
		}
		if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(first.ID) || first.ID != StableID(first.Title, first.URL) {
			t.Fatalf("expected 16-char stable hex id, got %q", first.ID)
		}
		if first.PublishedAt.Location() != time.UTC || !first.PublishedAt.Equal(time.Date(2026, 4, 25, 10, 0, 0, 0, time.UTC)) {
			t.Fatalf("expected 10:00 UTC, got %v", first.PublishedAt)
		}
		if !items[1].PublishedAt.Equal(now) {
			t.Fatalf("expected unparseable date to fall back to now, got %v", items[1].PublishedAt)
		}

		again, _ := ParseRSS([]byte(sampleFeed), "Feed", 20, now)
		if again[0].ID != first.ID {
			t.Fatal("expected IDs to be stable across parses")
		}
	})

	t.Run("GIVEN a max item count WHEN parsed THEN no more than that many feed items are considered", func(t *testing.T) {
		items, err := ParseRSS([]byte(sampleFeed), "Feed", 1, time.Now())

		if err != nil || len(items) != 1 {
			t.Fatalf("expected 1 item, got %d err=%v", len(items), err)
		}
	})

	t.Run("GIVEN an ISO-8859-1 encoded feed WHEN parsed THEN text is decoded", func(t *testing.T) {
		feed := []byte("<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?><rss><channel><item><title>Caf\xe9 crypto</title><link>https://x.test</link></item></channel></rss>")

		items, err := ParseRSS(feed, "Latin", 20, time.Now())

		if err != nil || len(items) != 1 || items[0].Title != "Café crypto" {
			t.Fatalf("unexpected items %+v err=%v", items, err)
		}
	})

	t.Run("GIVEN the feed returns an HTTP error WHEN fetched THEN an error and no items are returned", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		defer server.Close()
		adapter := NewRSSAdapter("Broken", server.URL)
		adapter.Client = server.Client()

		items, err := adapter.FetchNews(ctx)

		if err == nil || len(items) != 0 {
			t.Fatalf("expected error and no items, got %d err=%v", len(items), err)
		}
	})

	t.Run("GIVEN malformed XML WHEN parsed THEN an error is returned", func(t *testing.T) {
		if _, err := ParseRSS([]byte("<rss><channel><item><title>x</title>"), "Bad", 20, time.Now()); err == nil {
			t.Fatal("expected parse error")
		}
	})
}

// TestNewsTimestampNormalisation covers UTC normalisation of publish times.
func TestNewsTimestampNormalisation(t *testing.T) {
	t.Run("GIVEN news with mixed time zones WHEN normalised and sorted THEN all are UTC and ordered by instant", func(t *testing.T) {
		plus2 := time.FixedZone("+02", 2*60*60)
		zoned := domain.NewsItem{ID: "rss", PublishedAt: time.Date(2026, 4, 25, 12, 30, 0, 0, plus2)} // 10:30 UTC
		utc := domain.NewsItem{ID: "json", PublishedAt: time.Date(2026, 4, 25, 11, 0, 0, 0, time.UTC)}

		items := []domain.NewsItem{NormaliseNewsItem(zoned), NormaliseNewsItem(utc)}
		sort.Slice(items, func(i, j int) bool { return items[i].PublishedAt.After(items[j].PublishedAt) })

		if items[0].ID != "json" {
			t.Fatalf("expected the later UTC item first, got %q", items[0].ID)
		}
		for _, item := range items {
			if item.PublishedAt.Location() != time.UTC {
				t.Fatalf("expected UTC, got %v", item.PublishedAt.Location())
			}
		}
	})

	t.Run("GIVEN a UTC time WHEN normalised THEN the instant is unchanged", func(t *testing.T) {
		in := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		if out := NormalisePublishedAt(in); !out.Equal(in) {
			t.Fatalf("expected %v, got %v", in, out)
		}
	})
}

// TestDefaultSignal covers the neutral default news signal.
func TestDefaultSignal(t *testing.T) {
	t.Run("GIVEN a news item WHEN the default signal is built THEN it is neutral with medium confidence", func(t *testing.T) {
		signal := DefaultSignal(domain.NewsItem{ID: "abc"})

		if signal.NewsItemID != "abc" || signal.HeadlineSentiment != 0 || signal.Confidence != 0.5 || len(signal.AssetMentions) != 0 {
			t.Fatalf("unexpected signal %+v", signal)
		}
	})
}

// newFearGreedServer serves a sequence of Fear & Greed readings and counts requests.
func newFearGreedServer(t *testing.T, bodies ...string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(calls.Add(1)) - 1
		if i >= len(bodies) {
			i = len(bodies) - 1
		}
		if bodies[i] == "" {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(bodies[i]))
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

// fearGreedBody returns an alternative.me style response for value and label.
func fearGreedBody(value, label string) string {
	return `{"name":"Fear and Greed Index","data":[{"value":"` + value + `","value_classification":"` + label + `","timestamp":"1745539200"}],"metadata":{"error":null}}`
}

// TestFearGreedAdapter covers the Fear & Greed index adapter.
func TestFearGreedAdapter(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN a new index reading WHEN fetched THEN one UTC news item describing the reading is produced", func(t *testing.T) {
		server, _ := newFearGreedServer(t, fearGreedBody("20", "Extreme Fear"))
		adapter := NewFearGreedAdapter()
		adapter.APIURL, adapter.Client = server.URL, server.Client()

		items, err := adapter.FetchNews(ctx)

		if err != nil || len(items) != 1 {
			t.Fatalf("expected one item, got %d err=%v", len(items), err)
		}
		item := items[0]
		if item.Source != "Fear & Greed" || !strings.Contains(item.Title, "Extreme Fear") || !strings.Contains(item.Title, "20/100") {
			t.Fatalf("unexpected item %+v", item)
		}
		if !strings.Contains(item.Content, "buying opportunity") || item.URL == "" || item.PublishedAt.Location() != time.UTC {
			t.Fatalf("unexpected item details %+v", item)
		}
		if adapter.SourceName() != "Fear & Greed" {
			t.Fatalf("unexpected source name %q", adapter.SourceName())
		}
	})

	t.Run("GIVEN the reading has not changed WHEN fetched again THEN no item is produced until it changes", func(t *testing.T) {
		server, _ := newFearGreedServer(t, fearGreedBody("60", "Greed"), fearGreedBody("60", "Greed"), fearGreedBody("80", "Extreme Greed"))
		adapter := NewFearGreedAdapter()
		adapter.APIURL, adapter.Client = server.URL, server.Client()

		first, _ := adapter.FetchNews(ctx)
		second, _ := adapter.FetchNews(ctx)
		third, _ := adapter.FetchNews(ctx)

		if len(first) != 1 || len(second) != 0 || len(third) != 1 {
			t.Fatalf("expected 1/0/1 items, got %d/%d/%d", len(first), len(second), len(third))
		}
		if !strings.Contains(third[0].Content, "Extreme Greed") {
			t.Fatalf("unexpected changed reading %+v", third[0])
		}
	})

	t.Run("GIVEN the index API is down WHEN fetched THEN an error and no items are returned", func(t *testing.T) {
		server, _ := newFearGreedServer(t, "")
		adapter := NewFearGreedAdapter()
		adapter.APIURL, adapter.Client = server.URL, server.Client()

		items, err := adapter.FetchNews(ctx)

		if err == nil || len(items) != 0 {
			t.Fatalf("expected error and no items, got %d err=%v", len(items), err)
		}
	})

	t.Run("GIVEN index values across zones WHEN described THEN each zone has its own explanation", func(t *testing.T) {
		seen := map[string]bool{}
		for _, v := range []int{10, 40, 50, 70, 90} {
			seen[fearGreedZone(v)] = true
		}
		if len(seen) != 5 {
			t.Fatalf("expected 5 distinct zone descriptions, got %d", len(seen))
		}
	})
}

// TestAdapterInterface checks both adapters satisfy the shared Adapter interface.
func TestAdapterInterface(t *testing.T) {
	t.Run("GIVEN RSS and Fear & Greed adapters WHEN combined THEN both are usable as Adapter", func(t *testing.T) {
		var adapters []Adapter
		for _, a := range BuildRSSAdapters([]string{"A::https://a.test"}) {
			adapters = append(adapters, a)
		}
		adapters = append(adapters, NewFearGreedAdapter())

		if len(adapters) != 2 {
			t.Fatalf("expected 2 adapters, got %d", len(adapters))
		}
	})
}
