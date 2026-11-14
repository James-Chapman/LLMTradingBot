# Ingestion Adapters

The ingestion layer consists of two independent subsystems: the Kraken market data adapter (`internal/kraken`) and the news feed adapters (`internal/news`). Both are called from background goroutines in `internal/app/loops.go` and feed data into the strategy pipeline.

---

## Kraken Market Adapter (`internal/kraken/market.go`)

**Type:** `MarketAdapter`

Provides access to Kraken's public REST and WebSocket APIs through the official Kraken Go SDK, `github.com/krakenfx/api-go/v2` (`pkg/spot`). It never uses API keys, so it is safe in paper mode. Every call takes a `context.Context`. The SDK has no context support of its own, so `restWithContext()` attaches cancellation through the SDK's `Executor` hook. REST calls use a 15-second HTTP timeout.

### Constructor

```go
kraken.NewMarketAdapter(kraken.MarketAdapterOptions{
	RESTURL:      "",             // default https://api.kraken.com (KRAKEN_REST_URL)
	HTTPClient:   nil,            // default client with a 15 s timeout
	WebSocketURL: "",             // default wss://ws.kraken.com/v2
	Stream:       nil,            // default: SDK spot WebSocket (v2 ticker channel)
	PollInterval: 0,              // REST polling fallback interval (default 5 s)
	Backoff:      kraken.BackoffOptions{}, // retry settings; Sleep/Jitter injectable
})
```

Zero values select the defaults. `HTTPClient`, `Stream`, and the backoff `Sleep`/`Jitter` functions are injectable so tests can run against `httptest` fakes without real network calls. At runtime the app builds the adapter with only `RESTURL` set from `KRAKEN_REST_URL`.

---

### Symbol Mapping

Kraken REST uses internal symbols that differ from the user-facing format:

| User-Facing | Kraken REST altname | WebSocket v2 symbol |
|-------------|---------------------|---------------------|
| `BTC/EUR`   | `XBTEUR`            | `BTC/EUR`           |
| `ETH/EUR`   | `ETHEUR`            | `ETH/EUR`           |
| `DOGE/EUR`  | `XDGEUR`            | `DOGE/EUR`          |

The REST mapping is handled by `ToKrakenPair()` (`internal/kraken/client.go`):

```go
var baseAliases = map[string]string{"BTC": "XBT", "DOGE": "XDG"}

// ToKrakenPair converts an internal symbol like "BTC/EUR" to Kraken's altname "XBTEUR".
func ToKrakenPair(symbol string) string {
	base, quote, found := strings.Cut(symbol, "/")
	if !found {
		return symbol
	}
	if alias, ok := baseAliases[base]; ok {
		base = alias
	}
	return base + quote
}
```

`FromKrakenSymbol()` reverses the alias (`XBT/EUR` → `BTC/EUR`). WebSocket v2 already uses readable names such as `BTC/EUR`.

A pair map is lazily loaded from `/0/public/AssetPairs` on the first call that needs it. It maps altnames to Kraken's official pair names (`XBTEUR` ↔ `XXBTZEUR`), enabling validation and reverse lookup. A failed load is retried on the next call.

---

### `ValidateSymbols()`

```go
func (a *MarketAdapter) ValidateSymbols(ctx context.Context, symbols []string) []string
```

Called at startup to filter the configured markets to only those Kraken recognises. Returns the validated subset. Any symbol that fails to map to a known pair is dropped with a warning. If the pair map cannot be loaded, it returns an empty slice.

---

### `GetTickersBatch()`

```go
func (a *MarketAdapter) GetTickersBatch(ctx context.Context, symbols []string) (map[string]domain.MarketSnapshot, error)
```

Fetches live ticker prices for multiple markets in a single request to Kraken's `/0/public/Ticker` endpoint:

```
GET https://api.kraken.com/0/public/Ticker?pair=XBTEUR,ETHEUR
```

Returns a map keyed by the original user-facing symbols (e.g. `"BTC/EUR"`). Kraken may key its response by official name or altname; both are accepted. The returned map is never nil; it is empty when the call fails.

```go
type MarketSnapshot struct {
	Symbol    string    `json:"symbol"`
	Timestamp time.Time `json:"timestamp"`
	Price     float64   `json:"price"`            // last trade close price ("c" field)
	Volume    *float64  `json:"volume,omitempty"` // rolling 24h volume
	Bid       *float64  `json:"bid,omitempty"`
	Ask       *float64  `json:"ask,omitempty"`
}
```

Used as the REST fallback when a WebSocket snapshot has not arrived for a symbol yet. A single batch call avoids the rate-limit overhead of per-symbol requests. `GetTicker()` is a single-symbol convenience wrapper around it.

---

### `GetOHLC()`

```go
func (a *MarketAdapter) GetOHLC(ctx context.Context, symbol string, intervalMinutes, limit int) ([]domain.Candle, error)
```

Fetches OHLC candlestick data from Kraken's `/0/public/OHLC` endpoint through the SDK:

```
GET https://api.kraken.com/0/public/OHLC?pair=XBTEUR&interval=5
```

Returns candles oldest-to-newest:

```go
type Candle struct {
	T int64   `json:"t"` // Unix seconds
	O float64 `json:"o"`
	H float64 `json:"h"`
	L float64 `json:"l"`
	C float64 `json:"c"`
	V float64 `json:"v"`
}
```

The result is truncated to the `limit` most recent candles (a `limit` of zero or less returns everything Kraken sent).

Called by `ohlcLoop` every 120 seconds with 100 candles per request, a 1-second gap between the 5- and 15-minute fetches, and a 2.5-second inter-market gap to respect Kraken's public rate limit.

---

### `SubscribeTicker()` / `Start()` / `Stop()`

`Start(symbols, cb)` runs `SubscribeTicker()` in a background goroutine, replacing any running subscription. `Stop()` cancels it and waits up to 5 seconds for it to exit.

`SubscribeTicker()` opens a public Kraken **WebSocket v2** `ticker` subscription (`wss://ws.kraken.com/v2`) using the SDK's spot WebSocket, with the SDK's auto-reconnect disabled. Symbols are sent in v2 form (`BTC/EUR`). Messages are parsed by `ParseTickerMessage()` into `domain.MarketSnapshot` values and passed to the callback; the app caches them in `cacheMarketSnapshot()`. Rejected subscriptions are logged per symbol.

If WebSocket setup or streaming fails (including a dropped connection), the adapter logs a warning and falls back to polling `GetTickersBatch()` every 5 seconds. The strategy loop reads cached snapshots first and only calls REST for markets that have not produced an update yet.

---

### Rate Limiting (`internal/kraken/retry.go`)

Kraken's public REST API has a per-IP rate limit. REST calls are wrapped by the generic `CallWithBackoff()`, which retries transient errors (`rate limit`, `temporary lockout`, `service unavailable`, `too many requests`) with exponential backoff and up to 10% random jitter. The defaults are 3 attempts with a 500 ms initial delay. Rate limit avoidance is still supported by loop design:

| Loop | Frequency | Call type |
|------|-----------|-----------|
| `strategyLoop` | Every 30s | WebSocket cache first; `GetTickersBatch()` only for missing stream prices |
| `ohlcLoop` | Every 120s (2.5s/market) | `GetOHLC()`: one request per market and interval |

At two active markets, the OHLC loop sends four requests per 120-second cycle. Combined with the occasional ticker batch, this stays well within Kraken's documented public tier limits.

---

### Live Execution (`internal/kraken/execution.go`)

Live order placement (`ExecutionEngine`) uses the same SDK with API keys. `Execute()` refuses an order unless the environment passed with it is `live` and both `KRAKEN_API_KEY` and `KRAKEN_API_SECRET` are set. A refused order is returned with status `rejected` and stored in the rejected-trades register with the reason (`missing_api_credentials`, or the paper-mode read-only reason). The app routes an order to this engine, with environment `live`, only for markets toggled to live on the dashboard; every other market goes to the paper engine.

---

## News Feed Adapters (`internal/news`)

Every source implements the `news.Adapter` interface:

```go
type Adapter interface {
	SourceName() string
	// FetchNews returns the latest items; on failure it logs, returns an empty slice and the error.
	FetchNews(ctx context.Context) ([]domain.NewsItem, error)
}
```

### Configurable RSS Sources

RSS sources are the `NEWS_SOURCES` setting (dashboard → ⚙ Settings → News & alerts), one `Name::URL` entry per line:

```
CoinDesk::https://www.coindesk.com/arc/outboundfeeds/rss/
My Feed::https://example.com/rss
```

At startup, `news.BuildRSSAdapters(settings.NewsSources)` parses each entry into an `RSSAdapter`. Invalid entries (missing `::`, empty name, or empty URL) are skipped with a warning log. A `FearGreedAdapter` is always appended automatically; it does not need a `NEWS_SOURCES` entry.

To add a new RSS source, add a line to **News feeds** in the Settings dialog, save, and restart the bot.

---

### `RSSAdapterFromSpec(spec)` / `BuildRSSAdapters(specs)`

```go
func RSSAdapterFromSpec(spec string) *RSSAdapter
func BuildRSSAdapters(specs []string) []*RSSAdapter
```

`RSSAdapterFromSpec` splits on the first `::`, trims whitespace, and returns an `*RSSAdapter`, or `nil` (with a warning) if the spec is malformed. `BuildRSSAdapters` calls it for each entry and returns only the valid adapters.

---

### Concurrency Model

`RSSAdapter.FetchNews()` is a plain blocking call that takes a context. `newsLoop` runs in its own goroutine and fetches each adapter in turn every 300 seconds, so slow feeds never block the strategy loop. Each request has a 15-second timeout.

---

### `ParseRSS()` (`internal/news/rss.go`)

```go
func ParseRSS(data []byte, sourceName string, maxItems int, now time.Time) ([]domain.NewsItem, error)
```

`FetchNews()` downloads the feed with `net/http` (custom `User-Agent`, 10 MiB body limit) and parses it with the standard library's `encoding/xml`. No third-party HTTP or feed-parsing libraries are used.

**Parse logic:**
1. Download feed XML with a 15-second timeout; non-2xx responses are errors.
2. Stream `<item>` elements with a non-strict decoder that understands HTML entities (such as `&nbsp;`) and ISO-8859-1 feeds.
3. For each item, extract: `title`, `link`, `description` (used as `Content`), `pubDate`.
4. Clean `title` and `description` with `news.CleanText()` (`internal/news/sanitise.go`). Feed text is untrusted and ends up in LLM prompts, so it is reduced to plain single-line text: `<script>`/`<style>` elements and all other HTML tags are removed, entities are decoded (two passes, because feeds often escape their HTML), and every run of whitespace or control characters, including newlines and Unicode line separators, becomes one space. The title is capped at 200 runes (`MaxTitleRunes`) and the description at 300 runes (`MaxDescriptionRunes`), cut on rune boundaries. Items whose cleaned title is empty are skipped.
5. Keep the `link` only when it is an absolute `http`/`https` URL; `javascript:`, `ftp:` or relative links leave the item without a URL.
6. Generate a stable article ID: `StableID(title, url)` = `sha256(title + url)[:16]` hex of the cleaned title and kept link, consistent across restarts so duplicates are never created.
7. Parse `pubDate` (RFC 822/1123, or RFC 3339) and normalise it to UTC. A trailing US zone abbreviation (`EST`, `EDT`, `CST`, `CDT`, `MST`, `MDT`, `PST`, `PDT`) is replaced by its numeric offset first, because Go's parser would read it as UTC: `Mon, 02 Jan 2006 15:04:05 EST` is 20:04:05 UTC.
8. Items whose date cannot be parsed: `ParseRSS()` gives them `now`. `RSSAdapter.FetchNews()` instead gives them the time the adapter first saw them and keeps that time on later fetches (the adapter remembers it for as long as the item stays in the feed), so an undated item does not jump to the top of the newest-first order on every fetch. The adapter's clock is `RSSAdapter.Now` (nil: the current UTC time), injectable for tests.
9. Consider up to `maxItems` items (default 20).

**`NewsItem` structure (`internal/domain`):**

```go
type NewsItem struct {
	ID          string    `json:"id"`      // sha256(title+url)[:16]
	Source      string    `json:"source"`  // value of Name from the spec
	Title       string    `json:"title"`
	Content     string    `json:"content"` // RSS description field
	PublishedAt time.Time `json:"published_at"` // UTC
	URL         string    `json:"url,omitempty"`
}
```

---

### Fear & Greed Adapter (`internal/news/feargreed.go`)

`FearGreedAdapter` reads the latest Alternative.me Crypto Fear & Greed index (`https://api.alternative.me/fng/?limit=1`). It returns one news item only when the reading has changed since the last call. The item's title is `Crypto Fear & Greed Index: <label> (<value>/100)`, its content explains what the reading's zone means for sentiment, and it links to the Alternative.me index page. Its ID is derived from the title and the UTC date, so there is one ID per reading per day.

Because the news cache is merged across cycles (see below), a reading stays cached after the cycle that produced it, until 60 newer articles push it out. A later identical reading has the same ID, so it is neither duplicated in the cache nor counted as new. When the value changes, the new reading is a new article; the older reading also stays in the cache until it ages out.

---

### News Deduplication

`newsCycle()` in `internal/app/loops.go` deduplicates at two levels:

1. **Database level:** `repo.UpsertNewsItem()` stores an item only when no row with the same ID exists, so re-fetching the same article never creates duplicate rows.

2. **Briefing level:** `briefedNewsIDs` is a set on the `App` of article IDs that have already been briefed. Only articles in the merged cache that are not in the set count as new and trigger a briefing; they are added to the set at that moment. The set always contains every cached ID and survives failed cycles. It is bounded: above 1,000 IDs (`briefedNewsIDsMax`) it is rebuilt from the cache alone. That is safe because an article that has dropped out of the cache is older than all 60 cached articles, so it cannot re-enter the cache and count as new.

On restart, the cache is seeded from the 60 newest articles in the database and all of them are marked as briefed, so the first news cycle only briefs articles that are genuinely new since the restart.

---

### News Cache Structure

Each cycle's items are converted to cache entries by `newsEntry()` (`internal/app/news_cycle.go`) and **merged** into the previous in-memory `latestNews` cache by `mergeNews()`: entries are de-duplicated by ID, sorted newest first by publish time, and the 60 newest are kept. An article that is already cached keeps its cached entry, so a later fetch cannot move its publish time (this also keeps the first-seen time of undated items). Articles from a feed that failed this cycle stay cached.

```go
storage.NewsEntry{
	ID:          item.ID,
	Source:      item.Source,
	Title:       item.Title,
	URL:         url, // nil when the item has no link
	Summary:     strings.TrimSpace(summary), // first 200 runes of the cleaned Content
	PublishedAt: portfolio.FormatISO(item.PublishedAt),
}
```

`Summary` is truncated to 200 runes of the `Content` field, which for RSS items is already cleaned plain text (see `ParseRSS()`). This is what is included in LLM prompts and returned by `GET /api/news`.

If **every** adapter returns an error in a cycle, nothing is merged: the previous cache is kept unchanged (an empty cache would pause LLM strategies with "waiting for news"), nothing is briefed, LM Studio is still re-probed, and `newsCycle()` returns an error that the news loop shows as "News fetch failed" in the activity feed. A cycle where only some adapters fail proceeds normally with the others.

The full `Content` is stored in the database but not retained in the in-memory cache, which avoids memory bloat from large HTML bodies.

---

### News → LLM Briefing Trigger

The news cycle starts `marketBriefingTask()` in a background goroutine when new articles are detected, through `startBriefing()` (`internal/app/news_cycle.go`). This keeps the briefing non-blocking relative to the news polling cycle. The goroutine is tracked by a `sync.WaitGroup` so shutdown waits for it.

At most **one briefing runs at a time** (`briefingGate`). If new articles arrive while a briefing is still running (for example behind a slow LLM), no second briefing starts: the articles are queued, newest first and capped at 60, and briefed together in one follow-up run as soon as the current briefing finishes. However many triggers arrive meanwhile, there is only one follow-up.

```
newsCycle:
    ├── fetch each adapter (RSS sources + FearGreedAdapter)
    ├── if every adapter failed: keep the cache, re-probe the LLM, return an error
    ├── normalise timestamps to UTC, sort newest first
    ├── upsert to DB
    ├── merge into the latestNews cache (dedupe by ID, 60 newest)
    ├── fresh = cached articles not in briefedNewsIDs; add them to briefedNewsIDs
    ├── if fresh is non-empty:
    │       startBriefing(fresh)   (queued for the follow-up if one is running)
    │           └── collect prices + indicators for all active markets
    │           └── call analyser.BriefMarket(articles, marketData)
    │           └── cache and persist the result as the latest briefing
    │           └── log to activity feed
    ├── log "News fetched" to the activity feed
    └── llmClient.RecheckPrimary()   (reclaim LM Studio if it is back)
```

The briefing task only calls the LLM when at least one active market has a price. If news arrives before the strategy loop has priced any market, the briefing is skipped. Those articles are already recorded in `briefedNewsIDs`, so they do not trigger a briefing on the next cycle; only newer articles will.
