"""
Kraken market data adapter
"""
import asyncio
import json
from datetime import datetime, timezone
from functools import partial
from typing import Any, Awaitable, Callable, Dict, List, Optional

import krakenex
import websockets
from pykrakenapi import KrakenAPI

from domain.models import MarketSnapshot
from kraken_retry import call_with_kraken_backoff
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

    def __init__(
        self,
        api_key: Optional[str] = None,
        api_secret: Optional[str] = None,
        backoff_sleep: Callable[[float], Awaitable[None]] = asyncio.sleep,
        backoff_jitter: Optional[Callable[[float], float]] = None,
        websocket_connect: Optional[Callable[[str], Any]] = None,
    ):
        self.api = KrakenAPI(krakenex.API(api_key, api_secret))
        self.websocket_url = "wss://ws.kraken.com"
        self.running = False
        self._backoff_sleep = backoff_sleep
        self._backoff_jitter = backoff_jitter
        self._websocket_connect = websocket_connect or websockets.connect
        # Populated lazily by _load_pair_map(); guards against repeat API calls
        self._altname_to_official: Dict[str, str] = {}  # "XBTEUR" → "XXBTZEUR"
        self._official_to_altname: Dict[str, str] = {}  # "XXBTZEUR" → "XBTEUR"
        self._ws_pair_to_symbol: Dict[str, str] = {}     # "XBT/EUR" → "BTC/EUR"

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
            ticker_data = await call_with_kraken_backoff(
                lambda: _in_thread(self.api.get_ticker_information, pair_string),
                sleep=self._backoff_sleep,
                jitter=self._backoff_jitter,
                logger=logger,
                operation_name="Kraken batch ticker",
            )
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
            raw = await call_with_kraken_backoff(
                lambda: _in_thread(self._fetch_ohlc_raw, altname, interval),
                sleep=self._backoff_sleep,
                jitter=self._backoff_jitter,
                logger=logger,
                operation_name="Kraken OHLC",
            )
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
        """Subscribe to ticker updates by WebSocket, falling back to polling."""
        logger.info("Starting ticker subscription", extra={"symbols": symbols, "transport": "websocket"})
        try:
            await self._subscribe_ticker_websocket(symbols, callback)
        except Exception as exc:
            logger.warning(
                "Kraken WebSocket ticker failed, falling back to polling",
                extra={"error": str(exc), "symbols": symbols},
            )
            await self._subscribe_ticker_polling(symbols, callback)

    # Poll ticker prices when WebSocket streaming is unavailable.
    async def _subscribe_ticker_polling(self, symbols: List[str], callback: Callable[[MarketSnapshot], None]) -> None:
        """Poll-based ticker subscription fallback."""
        logger.info("Starting ticker subscription fallback (polling)", extra={"symbols": symbols})
        while self.running:
            snapshots = await self.get_tickers_batch(symbols)
            for snapshot in snapshots.values():
                callback(snapshot)
            await asyncio.sleep(5)

    # Subscribe to Kraken's public WebSocket ticker channel.
    async def _subscribe_ticker_websocket(
        self,
        symbols: List[str],
        callback: Callable[[MarketSnapshot], None],
    ) -> None:
        """Stream ticker updates from Kraken WebSocket v1."""
        await self._load_pair_map()
        self._ws_pair_to_symbol = {
            self._to_websocket_pair(symbol): symbol
            for symbol in symbols
            if self._to_altname(symbol) in self._altname_to_official
        }
        ws_pairs = list(self._ws_pair_to_symbol.keys())
        if not ws_pairs:
            logger.warning("No valid WebSocket ticker pairs", extra={"symbols": symbols})
            return

        subscribe = {
            "event": "subscribe",
            "pair": ws_pairs,
            "subscription": {"name": "ticker"},
        }
        async with self._websocket_connect(self.websocket_url) as websocket:
            await websocket.send(json.dumps(subscribe))
            while self.running:
                try:
                    message = await asyncio.wait_for(websocket.recv(), timeout=5.0)
                except asyncio.TimeoutError:
                    continue
                snapshot = self._snapshot_from_websocket_message(message)
                if snapshot:
                    callback(snapshot)

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

    # Convert internal symbols to Kraken WebSocket pairs.
    def _to_websocket_pair(self, symbol: str) -> str:
        """Convert internal symbol 'BTC/EUR' to Kraken WebSocket pair 'XBT/EUR'."""
        if "/" not in symbol:
            return symbol
        base, quote = symbol.split("/", 1)
        return f"{_BASE_ALIASES.get(base, base)}/{quote}"

    # Convert Kraken WebSocket pairs back to internal symbols.
    def _from_websocket_pair(self, pair: str) -> str:
        """Convert Kraken WebSocket pair 'XBT/EUR' to internal symbol 'BTC/EUR'."""
        if "/" not in pair:
            return pair
        base, quote = pair.split("/", 1)
        return f"{_BASE_ALIASES_REVERSE.get(base, base)}/{quote}"

    # Convert a Kraken WebSocket ticker frame to the shared market snapshot model.
    def _snapshot_from_websocket_message(self, message: str) -> Optional[MarketSnapshot]:
        """Parse a Kraken WebSocket ticker message into a MarketSnapshot."""
        try:
            payload = json.loads(message)
            if not isinstance(payload, list) or len(payload) < 4 or payload[-2] != "ticker":
                return None
            data = payload[1]
            pair = str(payload[-1])
            symbol = self._ws_pair_to_symbol.get(pair, self._from_websocket_pair(pair))
            return MarketSnapshot(
                symbol=symbol,
                timestamp=datetime.now(timezone.utc),
                price=float(data["c"][0]),
                volume=float(data.get("v", [0.0, 0.0])[1]),
            )
        except (json.JSONDecodeError, KeyError, IndexError, TypeError, ValueError):
            logger.debug("Ignoring malformed Kraken WebSocket ticker message")
            return None

    async def _load_pair_map(self) -> None:
        """Load altname→official mapping from Kraken. Cached after first call."""
        if self._altname_to_official:
            return
        try:
            pairs_df = await call_with_kraken_backoff(
                lambda: _in_thread(self.api.get_tradable_asset_pairs),
                sleep=self._backoff_sleep,
                jitter=self._backoff_jitter,
                logger=logger,
                operation_name="Kraken asset pairs",
            )
            for official_name, row in pairs_df.iterrows():
                altname = row.get("altname", "")
                if altname:
                    self._altname_to_official[altname] = official_name
                    self._official_to_altname[official_name] = altname
            logger.info("Kraken pair map loaded",
                        extra={"total_pairs": len(self._altname_to_official)})
        except Exception as e:
            logger.error("Failed to load Kraken pair map", extra={"error": str(e)})
