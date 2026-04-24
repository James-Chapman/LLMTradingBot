"""
Universe resolver for tradable markets
"""
from datetime import datetime
from typing import List, Optional, Set

import httpx

from domain.models import UniverseSnapshot
from observability.logging import get_logger

logger = get_logger("universe_resolver")

_COINGECKO_ETH_ECOSYSTEM_URL = "https://api.coingecko.com/api/v3/coins/markets"
_DEFAULT_KRAKEN_EUR_MARKETS = {
    "ADA/EUR", "AAVE/EUR", "AVAX/EUR", "BAT/EUR", "COMP/EUR", "DOT/EUR",
    "LINK/EUR", "MKR/EUR", "SNX/EUR", "SOL/EUR", "UNI/EUR", "YFI/EUR",
}
_FALLBACK_ETH_ECOSYSTEM_COINS = [
    "ADA", "LINK", "UNI", "AAVE", "MKR", "SNX", "COMP", "YFI", "BAT",
]

class UniverseResolver:
    """Resolves the tradable universe of markets"""

    def __init__(
        self,
        fixed_markets: List[str],
        dynamic_source: str = "coingecko",
        http_client: Optional[httpx.AsyncClient] = None,
        available_markets: Optional[Set[str]] = None,
    ):
        self.fixed_markets = fixed_markets
        self.dynamic_source = dynamic_source
        self.max_eth_ecosystem_coins = 10
        self._http_client = http_client
        self._available_markets = available_markets

    async def resolve_universe(self) -> UniverseSnapshot:
        """Resolve the complete tradable universe"""
        logger.info("Resolving tradable universe")

        # Fixed markets
        dynamic_markets = await self._resolve_dynamic_markets()

        # Create mapping (for now, assume direct mapping)
        mapping = {}
        for market in self.fixed_markets + dynamic_markets:
            mapping[market] = market  # Identity mapping for now

        # De-duplicate: fixed markets take priority; preserve insertion order
        seen: set = set()
        deduped_dynamic: list = []
        for m in self.fixed_markets:
            seen.add(m)
        for m in dynamic_markets:
            if m not in seen:
                deduped_dynamic.append(m)
                seen.add(m)

        snapshot = UniverseSnapshot(
            fixed_markets=self.fixed_markets,
            dynamic_markets=deduped_dynamic,
            resolver_source=self.dynamic_source,
            resolved_at=datetime.utcnow(),
            mapping=mapping
        )

        logger.info("Universe resolved", extra={
            "fixed_count": len(self.fixed_markets),
            "dynamic_count": len(dynamic_markets),
            "total_markets": len(snapshot.fixed_markets) + len(snapshot.dynamic_markets)
        })

        return snapshot

    async def _resolve_dynamic_markets(self) -> List[str]:
        """Resolve dynamic ETH ecosystem markets from CoinGecko, with fallback."""
        available = self._available_markets or (_DEFAULT_KRAKEN_EUR_MARKETS | set(self.fixed_markets))
        try:
            close_client = False
            client = self._http_client
            if client is None:
                client = httpx.AsyncClient()
                close_client = True
            try:
                response = await client.get(
                    _COINGECKO_ETH_ECOSYSTEM_URL,
                    params={
                        "vs_currency": "eur",
                        "category": "ethereum-ecosystem",
                        "order": "market_cap_desc",
                        "per_page": self.max_eth_ecosystem_coins * 3,
                        "page": 1,
                        "sparkline": "false",
                    },
                    timeout=10,
                )
                response.raise_for_status()
                coins = [str(item.get("symbol", "")).upper() for item in response.json()]
            finally:
                if close_client:
                    await client.aclose()
        except Exception as exc:
            logger.warning("Dynamic universe API failed; using fallback", extra={"error": str(exc)})
            coins = _FALLBACK_ETH_ECOSYSTEM_COINS

        markets: List[str] = []
        for coin in coins:
            market = f"{coin}/EUR"
            if market in available and market not in markets:
                markets.append(market)
            if len(markets) >= self.max_eth_ecosystem_coins:
                break

        return markets
