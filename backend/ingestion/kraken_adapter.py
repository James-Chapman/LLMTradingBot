"""
Kraken market data adapter
"""
import asyncio
from datetime import datetime, timezone
from functools import partial
from typing import Callable, Dict, List, Optional

import krakenex
from pykrakenapi import KrakenAPI

from domain.models import MarketSnapshot
from observability.logging import get_logger

logger = get_logger("kraken_adapter")


async def _in_thread(func, *args, **kwargs):
    """Run a blocking function in the default thread pool.

    pykrakenapi uses the synchronous `requests` library and calls time.sleep()
    for its rate limiter.  Running it in a thread keeps the asyncio event loop
    free to serve HTTP requests while Kraken API calls are in flight.
    """
    loop = asyncio.get_running_loop()
    bound = partial(func, *args, **kwargs) if kwargs else partial(func, *args)
    return await loop.run_in_executor(None, bound)


# Kraken uses XBT internally; we expose BTC to users
_BASE_ALIASES: Dict[str, str] = {"BTC": "XBT", "DOGE": "XDG"}
_BASE_ALIASES_REVERSE: Dict[str, str] = {v: k for k, v in _BASE_ALIASES.items()}


class KrakenMarketAdapter:
    """Adapter for Kraken market data."""

    def __init__(self, api_key: Optional[str] = None, api_secret: Optional[str] = None):
        self.api = KrakenAPI(krakenex.API(api_key, api_secret))
        self.websocket_url = "wss://ws.kraken.com"
        self.running = False
        # Populated lazily by _load_pair_map(); guards against repeat API calls
        self._altname_to_official: Dict[str, str] = {}  # "XBTEUR" → "XXBTZEUR"
        self._official_to_altname: Dict[str, str] = {}  # "XXBTZEUR" → "XBTEUR"

    # ------------------------------------------------------------------
    # Public API
    # ------------------------------------------------------------------

    async def validate_symbols(self, symbols: List[str]) -> List[str]:
        """Return only the symbols that exist as Kraken pairs."""
        await self._load_pair_map()
        valid: List[str] = []
        for sym in symbols:
            altname = self._to_altname(sym)
            if altname in self._altname_to_official:
                valid.append(sym)
            else:
                logger.warning("Symbol not available on Kraken, skipping",
                               extra={"symbol": sym, "altname_tried": altname})
        return valid

    async def get_tickers_batch(self, symbols: List[str]) -> Dict[str, MarketSnapshot]:
        """Fetch multiple tickers in a single Kraken API call.

        One call instead of N prevents rate-limit throttling.
        """
        if not symbols:
            return {}
        await self._load_pair_map()

        # Build altname→symbol lookup and official→symbol for response matching
        altname_to_sym: Dict[str, str] = {}
        for sym in symbols:
            altname = self._to_altname(sym)
            if altname in self._altname_to_official:
                altname_to_sym[altname] = sym

        if not altname_to_sym:
            logger.warning("No valid altnames found for batch ticker", extra={"symbols": symbols})
            return {}

        official_to_sym: Dict[str, str] = {
            self._altname_to_official[alt]: sym
            for alt, sym in altname_to_sym.items()
        }

        pair_string = ",".join(altname_to_sym.keys())
        try:
            ticker_data = await _in_thread(self.api.get_ticker_information, pair_string)
            if ticker_data.empty:
                return {}

            results: Dict[str, MarketSnapshot] = {}
            for official_name, row in ticker_data.iterrows():
                sym = official_to_sym.get(official_name)
                if sym:
                    results[sym] = MarketSnapshot(
                        symbol=sym,
                        timestamp=datetime.now(timezone.utc),
                        price=float(row["c"][0]),
                        volume=float(row["v"][1]),
                    )
            return results
        except Exception as e:
            logger.error("Batch ticker failed", extra={"error": str(e), "pairs": pair_string})
            return {}

    async def get_ticker(self, symbol: str) -> Optional[MarketSnapshot]:
        """Single-symbol ticker (convenience wrapper around get_tickers_batch)."""
        results = await self.get_tickers_batch([symbol])
        return results.get(symbol)

    async def get_ohlc(self, symbol: str, interval: int = 5, candle_limit: int = 100) -> List[Dict]:
        """Fetch OHLC candles directly from Kraken's public REST API.

        Bypasses pykrakenapi to avoid its pandas frequency-alias incompatibility
        with pandas 2.x (the 'T' minutes alias was removed).

        Returns newest-last list of {t, o, h, l, c, v} where t is Unix seconds.
        """
        await self._load_pair_map()
        altname = self._to_altname(symbol)
        try:
            raw = await _in_thread(self._fetch_ohlc_raw, altname, interval)
            candles = [
                {
                    "t": int(row[0]),
                    "o": float(row[1]),
                    "h": float(row[2]),
                    "l": float(row[3]),
                    "c": float(row[4]),
                    "v": float(row[6]),
                }
                for row in raw
            ]
            return candles[-candle_limit:]
        except Exception as e:
            logger.error(f"OHLC fetch failed for {symbol}: {e}")
            return []

    def _fetch_ohlc_raw(self, altname: str, interval: int) -> list:
        """Blocking HTTP call to Kraken public OHLC endpoint (run in thread pool)."""
        import json
        import urllib.request
        url = f"https://api.kraken.com/0/public/OHLC?pair={altname}&interval={interval}"
        with urllib.request.urlopen(url, timeout=10) as resp:
            data = json.loads(resp.read())
        if data.get("error"):
            raise RuntimeError(f"Kraken API error: {data['error']}")
        result = data.get("result", {})
        # Result has one key per pair plus "last" — find the candle list
        candles = next((v for k, v in result.items() if k != "last"), [])
        return candles

    async def subscribe_ticker(self, symbols: List[str], callback: Callable[[MarketSnapshot], None]):
        """Poll-based ticker subscription (WebSocket is a future enhancement)."""
        logger.info("Starting ticker subscription (polling)", extra={"symbols": symbols})
        while self.running:
            snapshots = await self.get_tickers_batch(symbols)
            for snapshot in snapshots.values():
                callback(snapshot)
            await asyncio.sleep(5)

    def start_subscription(self, symbols: List[str], callback: Callable[[MarketSnapshot], None]):
        self.running = True
        asyncio.create_task(self.subscribe_ticker(symbols, callback))

    def stop_subscription(self):
        self.running = False
        logger.info("Stopped ticker subscriptions")

    # ------------------------------------------------------------------
    # Internal helpers
    # ------------------------------------------------------------------

    def _to_altname(self, symbol: str) -> str:
        """Convert internal symbol 'BTC/EUR' → Kraken altname 'XBTEUR'."""
        if "/" not in symbol:
            return symbol
        base, quote = symbol.split("/", 1)
        kraken_base = _BASE_ALIASES.get(base, base)
        return f"{kraken_base}{quote}"

    async def _load_pair_map(self) -> None:
        """Load altname→official mapping from Kraken. Cached after first call."""
        if self._altname_to_official:
            return
        try:
            pairs_df = await _in_thread(self.api.get_tradable_asset_pairs)
            for official_name, row in pairs_df.iterrows():
                altname = row.get("altname", "")
                if altname:
                    self._altname_to_official[altname] = official_name
                    self._official_to_altname[official_name] = altname
            logger.info("Kraken pair map loaded",
                        extra={"total_pairs": len(self._altname_to_official)})
        except Exception as e:
            logger.error("Failed to load Kraken pair map", extra={"error": str(e)})
