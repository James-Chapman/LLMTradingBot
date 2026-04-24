"""
News ingestion adapters.

RSS-based adapters share a common _fetch_rss() helper.
FearGreedAdapter polls the Alternative.me JSON API (updates once per day).

All network calls run in a thread pool to avoid blocking the asyncio event loop.
Article IDs are derived from sha256(title + url) so they are stable across restarts
and the _briefed_news_ids deduplication set works correctly.
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
        link  = (item.findtext("link") or "").strip()
        description = (item.findtext("description") or "").strip()
        pub_date    = (item.findtext("pubDate") or "").strip()
        try:
            published_at = parsedate_to_datetime(pub_date).astimezone(timezone.utc).replace(tzinfo=None)
        except Exception:
            published_at = datetime.utcnow()

        items.append(NewsItem(
            id=_stable_id(title, link),
            source=source_name,
            title=title,
            content=description,
            published_at=published_at,
            url=link,
        ))

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
    """Generic RSS adapter — subclass and set RSS_URL."""
    RSS_URL: str = ""

    async def fetch_news(self) -> List[NewsItem]:
        loop = asyncio.get_running_loop()
        return await loop.run_in_executor(None, _fetch_rss, self.RSS_URL, self.source_name)


class CoinDeskAdapter(RSSAdapter):
    RSS_URL = "https://www.coindesk.com/arc/outboundfeeds/rss/"

    def __init__(self):
        super().__init__("CoinDesk")


class CoinTelegraphAdapter(RSSAdapter):
    RSS_URL = "https://cointelegraph.com/rss"

    def __init__(self):
        super().__init__("CoinTelegraph")


class TheBlockAdapter(RSSAdapter):
    """The Block — institutional-grade crypto news, strong on exchange and regulatory stories."""
    RSS_URL = "https://www.theblock.co/rss.xml"

    def __init__(self):
        super().__init__("The Block")


class DecryptAdapter(RSSAdapter):
    """Decrypt — accessible language, fast cadence, good DeFi coverage."""
    RSS_URL = "https://decrypt.co/feed"

    def __init__(self):
        super().__init__("Decrypt")


class BitcoinMagazineAdapter(RSSAdapter):
    """Bitcoin Magazine — BTC-specific depth: halving cycles, ETF flows, miner economics."""
    RSS_URL = "https://bitcoinmagazine.com/.rss/full/"

    def __init__(self):
        super().__init__("Bitcoin Magazine")


class CryptoSlateAdapter(RSSAdapter):
    """CryptoSlate — broad crypto coverage: altcoins, DeFi, exchange news."""
    RSS_URL = "https://cryptoslate.com/feed/"

    def __init__(self):
        super().__init__("CryptoSlate")


class CoinTelegraphMagazineAdapter(RSSAdapter):
    """Cointelegraph Magazine — long-form features and in-depth analysis."""
    RSS_URL = "https://magazine.cointelegraph.com/feed/"

    def __init__(self):
        super().__init__("CT Magazine")


class TheDefiantAdapter(RSSAdapter):
    """The Defiant — DeFi-focused reporting: protocols, yields, on-chain activity."""
    RSS_URL = "https://thedefiant.io/feed/"

    def __init__(self):
        super().__init__("The Defiant")


class CryptoPotaroAdapter(RSSAdapter):
    """CryptoPotato — high-frequency crypto news and price analysis."""
    RSS_URL = "https://cryptopotato.com/feed/"

    def __init__(self):
        super().__init__("CryptoPotato")


class CryptoNewsAdapter(RSSAdapter):
    """CryptoNews — broad market news, ICO and regulatory updates."""
    RSS_URL = "https://cryptonews.com/news/feed/"

    def __init__(self):
        super().__init__("CryptoNews")


class NewsBTCAdapter(RSSAdapter):
    """NewsBTC — technical price analysis and breaking crypto news."""
    RSS_URL = "https://www.newsbtc.com/feed/"

    def __init__(self):
        super().__init__("NewsBTC")


class ReutersBusinessAdapter(RSSAdapter):
    """Reuters Business — macro news: Fed/ECB decisions, dollar strength, geopolitical risk.
    Most impactful non-crypto source for EUR-quoted pairs."""
    RSS_URL = "https://feeds.reuters.com/reuters/businessNews"

    def __init__(self):
        super().__init__("Reuters")


class FearGreedAdapter(NewsAdapter):
    """Alternative.me Crypto Fear & Greed Index.

    Polls once per news cycle but only produces a new item when the reading
    changes from the previously seen value (the index updates once per day).
    Synthesises a NewsItem so the existing pipeline handles it without changes.
    """
    API_URL  = "https://api.alternative.me/fng/?limit=1"
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
        label = data["value_classification"]   # e.g. "Extreme Fear", "Greed"

        # Only emit a new item when the value changes — it updates once per day
        if value == self._last_value:
            return None
        self._last_value = value

        zone = (
            "Extreme Fear — market participants are very worried; historically a buying opportunity."
            if value <= 25 else
            "Fear — sentiment is cautious; prices may be suppressed below fair value."
            if value <= 45 else
            "Neutral — balanced market sentiment with no strong directional bias."
            if value <= 55 else
            "Greed — market is optimistic; risk of overshoot or near-term correction is elevated."
            if value <= 75 else
            "Extreme Greed — euphoric sentiment; historically precedes significant corrections."
        )

        title   = f"Crypto Fear & Greed Index: {label} ({value}/100)"
        content = (
            f"The Crypto Fear & Greed Index is currently {value}/100 — {label}. "
            f"{zone} "
            f"This reading is updated daily and reflects a composite of volatility, "
            f"market momentum, social media activity, and BTC dominance."
        )
        # ID is stable for today's value — refreshes if the score changes
        item_id = _stable_id(title, str(date.today()))

        logger.info(f"Fear & Greed updated: {label} ({value}/100)")
        return NewsItem(
            id=item_id,
            source="Fear & Greed",
            title=title,
            content=content,
            published_at=datetime.utcnow(),
            url=self.PAGE_URL,
        )


# ── Legacy stubs (kept to avoid import breakage) ──────────────────────────────

class CoinNewsAdapter(NewsAdapter):
    """CoinNews stub — ingestion method TBD (Phase 0)."""

    def __init__(self):
        super().__init__("CoinNews")

    async def fetch_news(self) -> List[NewsItem]:
        return []


class CoinWeekAdapter(NewsAdapter):
    """CoinWeek stub — ingestion method TBD (Phase 0)."""

    def __init__(self):
        super().__init__("CoinWeek")

    async def fetch_news(self) -> List[NewsItem]:
        return []
