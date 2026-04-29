"""
News ingestion adapters.

RSS sources are configured via NEWS_SOURCES in settings as a JSON array of
'Name::URL' strings. rss_adapter_from_spec() parses each entry and
build_rss_adapters() constructs the full list at startup.

FearGreedAdapter polls the Alternative.me JSON API (updates once per day) and
is always included — it cannot be expressed as a plain RSS URL.

All network calls run in a thread pool to avoid blocking the asyncio event loop.
Article IDs are derived from sha256(title + url) so they are stable across
restarts and the _briefed_news_ids deduplication set works correctly.
"""

import asyncio
import hashlib
import json
import urllib.error
import urllib.request
import xml.etree.ElementTree as ET
from datetime import date, datetime, timezone
from email.utils import parsedate_to_datetime
from typing import List, Optional

from domain.models import NewsItem, NewsSignal
from observability.logging import get_logger

logger = get_logger("news_adapter")


def _stable_id(title: str, url: str) -> str:
    """Stable 16-char hex ID derived from content — consistent across restarts."""
    return hashlib.sha256((title + url).encode()).hexdigest()[:16]


# Normalize feed timestamps so mixed RSS/JSON sources can be sorted safely.
def normalise_published_at(value: datetime) -> datetime:
    """Return a timezone-aware UTC publication timestamp."""
    if value.tzinfo is None or value.tzinfo.utcoffset(value) is None:
        return value.replace(tzinfo=timezone.utc)
    return value.astimezone(timezone.utc)


# Return a copy of a news item with a timezone-aware UTC publication timestamp.
def normalise_news_item(item: NewsItem) -> NewsItem:
    """Return a news item whose published_at is timezone-aware UTC."""
    return item.model_copy(update={"published_at": normalise_published_at(item.published_at)})


def _fetch_rss(url: str, source_name: str, max_items: int = 20) -> List[NewsItem]:
    """Fetch and parse an RSS feed synchronously. Intended to run in a thread."""
    try:
        req = urllib.request.Request(
            url,
            headers={"User-Agent": "KrakenBot/0.1 (RSS reader; personal use)"},
        )
        with urllib.request.urlopen(req, timeout=15) as resp:
            content = resp.read()
    except urllib.error.URLError as e:
        logger.warning(f"{source_name} RSS fetch failed", extra={"url": url, "error": str(e)})
        return []
    except Exception as e:
        logger.warning(f"{source_name} unexpected fetch error", extra={"error": str(e)})
        return []

    try:
        root = ET.fromstring(content)
    except ET.ParseError as e:
        logger.warning(f"{source_name} RSS parse failed", extra={"error": str(e)})
        return []

    items: List[NewsItem] = []
    for item in root.findall(".//item")[:max_items]:
        title = (item.findtext("title") or "").strip()
        if not title:
            continue
        link = (item.findtext("link") or "").strip()
        description = (item.findtext("description") or "").strip()
        pub_date = (item.findtext("pubDate") or "").strip()
        try:
            published_at = normalise_published_at(parsedate_to_datetime(pub_date))
        except Exception:
            published_at = datetime.now(timezone.utc)

        items.append(
            NewsItem(
                id=_stable_id(title, link),
                source=source_name,
                title=title,
                content=description,
                published_at=published_at,
                url=link,
            )
        )

    logger.info(f"Fetched {len(items)} items from {source_name}")
    return items


class NewsAdapter:
    """Base class for news adapters."""

    def __init__(self, source_name: str):
        self.source_name = source_name

    async def fetch_news(self) -> List[NewsItem]:
        raise NotImplementedError

    async def process_news(self, news_item: NewsItem) -> NewsSignal:
        return NewsSignal(
            news_item_id=news_item.id,
            asset_mentions=[],
            headline_sentiment=0.0,
            summary_sentiment=None,
            event_type=None,
            event_severity=None,
            confidence=0.5,
        )


class RSSAdapter(NewsAdapter):
    """Generic RSS adapter. Constructed via rss_adapter_from_spec()."""

    def __init__(self, source_name: str, rss_url: str):
        super().__init__(source_name)
        self.rss_url = rss_url

    async def fetch_news(self) -> List[NewsItem]:
        loop = asyncio.get_running_loop()
        return await loop.run_in_executor(None, _fetch_rss, self.rss_url, self.source_name)


def rss_adapter_from_spec(spec: str) -> Optional[RSSAdapter]:
    """Parse a 'Name::URL' spec string into an RSSAdapter, or None if invalid."""
    if "::" not in spec:
        logger.warning(f"Invalid NEWS_SOURCES entry (expected 'Name::URL'): {spec!r}")
        return None
    name, url = spec.split("::", 1)
    name = name.strip()
    url = url.strip()
    if not name or not url:
        logger.warning(f"Invalid NEWS_SOURCES entry — name or URL is empty: {spec!r}")
        return None
    return RSSAdapter(name, url)


def build_rss_adapters(specs: List[str]) -> List[RSSAdapter]:
    """Build a list of RSSAdapters from a list of 'Name::URL' spec strings."""
    adapters: List[RSSAdapter] = []
    for spec in specs:
        adapter = rss_adapter_from_spec(spec)
        if adapter is not None:
            adapters.append(adapter)
    return adapters


class FearGreedAdapter(NewsAdapter):
    """Alternative.me Crypto Fear & Greed Index.

    Polls once per news cycle but only produces a new item when the reading
    changes from the previously seen value (the index updates once per day).
    Synthesises a NewsItem so the existing pipeline handles it without changes.
    """

    API_URL = "https://api.alternative.me/fng/?limit=1"
    PAGE_URL = "https://alternative.me/crypto/fear-and-greed-index/"

    def __init__(self):
        super().__init__("Fear & Greed")
        self._last_value: Optional[int] = None

    async def fetch_news(self) -> List[NewsItem]:
        loop = asyncio.get_running_loop()
        item = await loop.run_in_executor(None, self._fetch)
        return [item] if item else []

    def _fetch(self) -> Optional[NewsItem]:
        try:
            req = urllib.request.Request(
                self.API_URL,
                headers={"User-Agent": "KrakenBot/0.1"},
            )
            with urllib.request.urlopen(req, timeout=10) as resp:
                data = json.loads(resp.read())["data"][0]
        except Exception as e:
            logger.warning("Fear & Greed fetch failed", extra={"error": str(e)})
            return None

        value = int(data["value"])
        label = data["value_classification"]

        if value == self._last_value:
            return None
        self._last_value = value

        zone = (
            "Extreme Fear — market participants are very worried; historically a buying opportunity."
            if value <= 25
            else "Fear — sentiment is cautious; prices may be suppressed below fair value."
            if value <= 45
            else "Neutral — balanced market sentiment with no strong directional bias."
            if value <= 55
            else "Greed — market is optimistic; risk of overshoot or near-term correction is elevated."
            if value <= 75
            else "Extreme Greed — euphoric sentiment; historically precedes significant corrections."
        )

        title = f"Crypto Fear & Greed Index: {label} ({value}/100)"
        content = (
            f"The Crypto Fear & Greed Index is currently {value}/100 — {label}. "
            f"{zone} "
            f"This reading is updated daily and reflects a composite of volatility, "
            f"market momentum, social media activity, and BTC dominance."
        )
        item_id = _stable_id(title, str(date.today()))

        logger.info(f"Fear & Greed updated: {label} ({value}/100)")
        return NewsItem(
            id=item_id,
            source="Fear & Greed",
            title=title,
            content=content,
            published_at=datetime.now(timezone.utc),
            url=self.PAGE_URL,
        )
