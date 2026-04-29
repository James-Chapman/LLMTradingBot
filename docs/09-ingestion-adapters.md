# Ingestion Adapters

The ingestion layer consists of two independent subsystems: the Kraken market data adapter and the news feed adapters. Both are called from background loops in `main.py` and feed data into the strategy pipeline.

---

## Kraken Market Adapter (`backend/ingestion/kraken_adapter.py`)

**Class:** `KrakenMarketAdapter`

Provides async access to Kraken's public and private REST API. In paper trading mode, only public endpoints are used (ticker prices, OHLC candles). All blocking HTTP calls are offloaded to a thread pool via `asyncio.get_running_loop().run_in_executor(None, ...)` to keep the event loop free.

### Constructor

```python
KrakenMarketAdapter(
    api_key:    Optional[str],   # from settings.kraken_api_key
    api_secret: Optional[str],   # from settings.kraken_api_secret
    backoff_sleep: Callable = asyncio.sleep,
    backoff_jitter: Optional[Callable] = None,
    websocket_connect: Optional[Callable] = None,
)
```

Both credentials are `None` in paper mode. `backoff_sleep`, `backoff_jitter`, and `websocket_connect` are injectable for BDD tests; normal runtime uses `asyncio.sleep`, random jitter, and `websockets.connect`.

---

### Symbol Mapping

Kraken uses internal symbols that differ from the user-facing format:

| User-Facing | Kraken Internal |
|-------------|----------------|
| `BTC/EUR`   | `XBTEUR`        |
| `ETH/EUR`   | `ETHEUR`        |
| `DOGE/EUR`  | `XDGEUR`        |

The mapping is handled by `_to_altname()`:

```python
BASE_ALIASES = {"BTC": "XBT", "DOGE": "XDG"}

def _to_altname(self, symbol: str) -> str:
    base, quote = symbol.split("/")
    kraken_base = BASE_ALIASES.get(base, base)
    return f"{kraken_base}{quote}"
```

A `_pair_map` dict is lazily loaded from `/0/public/AssetPairs` on the first call that needs it. It maps Kraken pair names to their metadata, enabling validation and reverse lookup.

---

### `validate_symbols()`

```python
async def validate_symbols(symbols: List[str]) -> List[str]
```

Called at startup to filter `settings.fixed_markets` to only those Kraken recognises. Returns the validated subset. Any symbol that fails to map to a known pair is dropped with a warning.

---

### `get_tickers_batch()`

```python
async def get_tickers_batch(symbols: List[str]) -> Dict[str, MarketSnapshot]
```

Fetches live ticker prices for multiple markets in a single HTTP request to Kraken's `/0/public/Ticker` endpoint:

```
GET https://api.kraken.com/0/public/Ticker?pair=XBTEUR,ETHEUR
```

Returns a `Dict[str, MarketSnapshot]` where keys are the original user-facing symbols (e.g., `"BTC/EUR"`).

```python
@dataclass
class MarketSnapshot:
    symbol:    str
    timestamp: datetime
    price:     float    # last trade close price ("c" field from Kraken)
    volume:    float    # 24h volume
```

Used as the REST fallback when a WebSocket snapshot has not arrived for a symbol yet. A single batch call avoids the rate limit overhead of per-symbol requests.

---

### `get_ohlc()`

```python
async def get_ohlc(
    symbol:       str,
    interval:     int = 5,        # minutes per candle (1, 5, 15, 30, 60, 240, 1440, 10080, 21600)
    candle_limit: int = 100,      # max candles to return
) -> List[Dict]
```

Fetches OHLC candlestick data from Kraken's `/0/public/OHLC` endpoint. **Does not use `pykrakenapi`** — that library has a pandas 2.x incompatibility with the array indexing API used for OHLC, so raw `urllib` is used instead.

```
GET https://api.kraken.com/0/public/OHLC?pair=XBTEUR&interval=5
```

Returns a list of candle dicts, oldest-to-newest:

```python
{"t": 1745409000, "o": 85120.0, "h": 85680.0, "l": 85050.0, "c": 85420.0, "v": 1.234}
```

`t` is Unix timestamp in seconds. Result is truncated to `candle_limit` most recent candles.

Called by `_ohlc_loop` every 120 seconds with a 2.5-second inter-market gap to respect Kraken's public rate limit.

---

### `subscribe_ticker()` / `start_subscription()`

Starts a public Kraken WebSocket v1 `ticker` subscription. Messages are parsed into the existing `MarketSnapshot` contract and cached by `_strategy_loop` through `_cache_market_snapshot()`.

If WebSocket setup or streaming fails, the adapter logs a warning and falls back to polling with `get_tickers_batch()`. The strategy loop reads cached WebSocket snapshots first and only calls REST for markets that have not produced a stream update yet.

---

### Rate Limiting

Kraken's public REST API has a per-IP rate limit. REST calls are wrapped by `kraken_retry.call_with_kraken_backoff()`, which retries rate-limit and temporary-lockout responses with exponential backoff before surfacing the error. Rate limit avoidance is still supported by loop design:

| Loop | Frequency | Call type |
|------|-----------|-----------|
| `_strategy_loop` | Every 30s | WebSocket cache first; `get_tickers_batch()` only for missing stream prices |
| `_ohlc_loop` | Every 120s (2.5s/market) | `get_ohlc()` — one request per market |

At two active markets, the OHLC loop sends two requests per 120-second cycle. Combined with the ticker batch (one request every 30 seconds), this stays well within Kraken's documented public tier limits.

---

## News Feed Adapters (`backend/ingestion/news_adapter.py`)

### Configurable RSS Sources

RSS sources are configured via `NEWS_SOURCES` in `.env` as a JSON array of `"Name::URL"` strings:

```
NEWS_SOURCES=["CoinDesk::https://www.coindesk.com/arc/outboundfeeds/rss/","My Feed::https://example.com/rss"]
```

At startup, `build_rss_adapters(settings.news_sources)` parses each entry into an `RSSAdapter` instance. Invalid entries (missing `::`, empty name, or empty URL) are skipped with a warning log. The `FearGreedAdapter` is always appended automatically — it does not need a `NEWS_SOURCES` entry.

To add a new RSS source at runtime, update `NEWS_SOURCES` in `.env` and restart the bot.

---

### `rss_adapter_from_spec(spec)` / `build_rss_adapters(specs)`

```python
def rss_adapter_from_spec(spec: str) -> Optional[RSSAdapter]
def build_rss_adapters(specs: List[str]) -> List[RSSAdapter]
```

`rss_adapter_from_spec` splits on the first `::`, strips whitespace, and returns an `RSSAdapter` or `None` (with a warning) if the spec is malformed. `build_rss_adapters` calls the former for each entry and returns only the valid adapters.

---

### Threading Model

RSS feed parsing is synchronous (blocking HTTP + XML). All `RSSAdapter.fetch_news()` calls use:

```python
loop = asyncio.get_running_loop()
items = await loop.run_in_executor(None, _fetch_rss, self.rss_url, self.source_name)
```

This delegates the blocking call to the default `ThreadPoolExecutor`, preventing it from stalling the asyncio event loop during the 300-second news loop.

---

### `_fetch_rss()` (internal)

```python
def _fetch_rss(
    url:         str,
    source_name: str,
    max_items:   int = 20,
) -> List[NewsItem]
```

Fetches and parses an RSS feed using Python's standard library `urllib.request` and `xml.etree.ElementTree`. No third-party HTTP or feed-parsing libraries are used.

**Parse logic:**
1. Download feed XML with a 15-second timeout.
2. Find all `<item>` elements.
3. For each item, extract: `title`, `link`, `description` (used as `content`), `pubDate`.
4. Generate a stable article ID: `sha256(title + url)[:16]` — consistent across restarts, no duplicates.
5. Parse `pubDate` and normalise it to timezone-aware UTC before returning a `NewsItem`.
6. Return up to `max_items` most recent items.

**`NewsItem` structure:**

```python
@dataclass
class NewsItem:
    id:           str       # sha256(title+url)[:16]
    source:       str       # value of Name from the spec
    title:        str
    content:      str       # RSS description field
    published_at: datetime  # timezone-aware UTC
    url:          str
```

---

### News Deduplication

The `_news_loop` in `main.py` deduplicates at two levels:

1. **Database level:** `repo.upsert_news_item()` uses `INSERT OR IGNORE` (SQLite), so re-fetching the same article never creates duplicate rows.

2. **Briefing level:** `_briefed_news_ids` is a module-level `set` tracking which article IDs have already triggered a market briefing. Only `new_ids = current_ids - _briefed_news_ids` triggers a briefing call.

On restart, `_briefed_news_ids` starts empty. The first news fetch always triggers a full briefing with all cached articles — this ensures the LLM has an updated market view immediately after startup.

---

### News Cache Structure

After each fetch cycle, the in-memory `_latest_news` cache is rebuilt:

```python
_latest_news = [
    {
        "id":           item.id,
        "source":       item.source,
        "title":        item.title,
        "url":          item.url,
        "summary":      (item.content or "")[:200].strip(),   # first 200 chars
        "published_at": item.published_at.isoformat(),
    }
    for item in top_30_items
]
```

`summary` is truncated to 200 characters from the raw `content` field. This is what is included in LLM signal analysis prompts and returned by `GET /api/news`.

The full `content` is stored in the database but not retained in the in-memory cache (avoids memory bloat from large HTML bodies).

---

### News → LLM Briefing Trigger

The news loop fires `_market_briefing_task()` as an independent `asyncio.create_task()` when new articles are detected. This keeps the briefing non-blocking relative to the news polling cycle.

```
_news_loop tick:
    ├── fetch each adapter in news_adapters (RSS sources + FearGreedAdapter)
    ├── upsert to DB
    ├── rebuild _latest_news cache
    ├── compute new_ids = current_ids - _briefed_news_ids
    └── if new_ids:
            update _briefed_news_ids
            asyncio.create_task(_market_briefing_task(new_articles))
                └── collect prices + indicators for all active markets
                └── call _analyser.brief_market(articles, market_data)
                └── cache result in _analyser.latest_briefing
                └── log to activity feed
```

The briefing task only fires when `_current_prices` is non-empty. If news arrives before the strategy loop has completed its first tick, the briefing is skipped for that cycle (the next news cycle will include those articles in the new-article count).
