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
)
```

Both credentials are `None` in paper mode. The adapter checks for their presence before any private API call.

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

Called every 30 seconds from `_strategy_loop`. A single batch call avoids the rate limit overhead of per-symbol requests.

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

Polling-based subscription shim. Calls `get_tickers_batch()` on an interval (currently unused — the strategy loop fetches directly). Provided for future use if the polling architecture is replaced with WebSocket streaming.

---

### Rate Limiting

Kraken's public REST API has a per-IP rate limit. The adapter does not implement retry backoff itself — rate limit avoidance is achieved through loop design:

| Loop | Frequency | Call type |
|------|-----------|-----------|
| `_strategy_loop` | Every 30s | `get_tickers_batch()` — single batch request |
| `_ohlc_loop` | Every 120s (2.5s/market) | `get_ohlc()` — one request per market |

At two active markets, the OHLC loop sends two requests per 120-second cycle. Combined with the ticker batch (one request every 30 seconds), this stays well within Kraken's documented public tier limits.

---

## News Feed Adapters (`backend/ingestion/news_adapter.py`)

### Threading Model

RSS feed parsing is synchronous (blocking HTTP + XML). All adapter `fetch_news()` methods use:

```python
loop = asyncio.get_running_loop()
items = await loop.run_in_executor(None, self._fetch_rss, url, source_name)
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

Fetches and parses an RSS/Atom feed using Python's standard library `urllib.request` and `xml.etree.ElementTree`. No third-party HTTP or feed-parsing libraries are used.

**Parse logic:**
1. Download feed XML with a 10-second timeout.
2. Find all `<item>` (RSS) or `<entry>` (Atom) elements.
3. For each item, extract: `title`, `link`, `description` (used as `content`), `pubDate` / `published`.
4. Generate a stable article ID: `sha256(title + url)[:16]` — consistent across restarts, no duplicates.
5. Parse `pubDate` with multiple format fallbacks (RFC 2822, ISO 8601, etc.).
6. Return up to `max_items` most recent items.

**`NewsItem` structure:**

```python
@dataclass
class NewsItem:
    id:           str       # sha256(title+url)[:16]
    source:       str       # "CoinDesk" or "CoinTelegraph"
    title:        str
    content:      str       # RSS description field (full HTML body)
    published_at: datetime
    url:          str
```

---

### `CoinDeskAdapter`

```python
class CoinDeskAdapter:
    RSS_URL = "https://www.coindesk.com/arc/outboundfeeds/rss/"
    
    async def fetch_news(self) -> List[NewsItem]
```

Fetches from CoinDesk's official RSS feed. Returns up to 20 most recent articles.

---

### `CoinTelegraphAdapter`

```python
class CoinTelegraphAdapter:
    RSS_URL = "https://cointelegraph.com/rss"
    
    async def fetch_news(self) -> List[NewsItem]
```

Fetches from CoinTelegraph's official RSS feed. Returns up to 20 most recent articles.

---

### Stub Adapters (Phase 0)

Two additional adapters are defined but currently return empty lists. They serve as extension points for future ingestion sources:

- `CoinNewsAdapter` — ingestion method TBD
- `CoinWeekAdapter` — ingestion method TBD

To add a new source, implement `async def fetch_news(self) -> List[NewsItem]` and add the instance to the `_news_loop` fetch sequence in `main.py`.

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
    ├── fetch CoinDesk
    ├── fetch CoinTelegraph
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
