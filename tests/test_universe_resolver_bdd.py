"""BDD coverage for universe resolver duplicate-market deduplication."""
import sys
import unittest
from pathlib import Path

BACKEND_DIR = Path(__file__).resolve().parents[1] / "backend"
if str(BACKEND_DIR) not in sys.path:
    sys.path.insert(0, str(BACKEND_DIR))

from universe.resolver import UniverseResolver  # noqa: E402


class FakeResponse:
    def __init__(self, payload):
        self._payload = payload

    def raise_for_status(self) -> None:
        return None

    def json(self):
        return self._payload


class FakeHTTPClient:
    def __init__(self, payload):
        self.payload = payload
        self.url = ""
        self.params = {}

    async def get(self, url, params=None, timeout=None):
        self.url = url
        self.params = params or {}
        return FakeResponse(self.payload)


class UniverseResolverBDDTests(unittest.IsolatedAsyncioTestCase):

    # GIVEN fixed and dynamic markets with no overlap WHEN resolved
    # THEN all markets appear exactly once.
    async def test_given_no_overlap_when_resolved_then_all_markets_present(self) -> None:
        resolver = UniverseResolver(fixed_markets=["BTC/EUR", "ETH/EUR"])
        # Patch dynamic to return distinct markets
        async def _mock(): return ["SOL/EUR", "ADA/EUR"]
        resolver._resolve_dynamic_markets = _mock  # type: ignore[method-assign]

        snapshot = await resolver.resolve_universe()

        all_markets = snapshot.fixed_markets + snapshot.dynamic_markets
        self.assertEqual(len(all_markets), len(set(all_markets)))

    # GIVEN a market that is in both fixed and dynamic lists WHEN resolved
    # THEN that market appears exactly once in the combined universe.
    async def test_given_overlapping_markets_when_resolved_then_no_duplicates(self) -> None:
        resolver = UniverseResolver(fixed_markets=["BTC/EUR", "ETH/EUR"])
        # ETH/EUR is in both fixed and dynamic — must be deduplicated
        async def _mock(): return ["ETH/EUR", "SOL/EUR"]
        resolver._resolve_dynamic_markets = _mock  # type: ignore[method-assign]

        snapshot = await resolver.resolve_universe()

        all_markets = snapshot.fixed_markets + snapshot.dynamic_markets
        self.assertEqual(len(all_markets), len(set(all_markets)),
                         f"Duplicate markets found: {all_markets}")
        self.assertIn("ETH/EUR", snapshot.fixed_markets,
                      "Fixed market should be retained in fixed_markets")
        self.assertNotIn("ETH/EUR", snapshot.dynamic_markets,
                         "Duplicate should be removed from dynamic_markets")

    # GIVEN all dynamic markets are duplicates of fixed WHEN resolved
    # THEN dynamic_markets is empty and fixed_markets is unchanged.
    async def test_given_all_dynamic_are_duplicates_when_resolved_then_dynamic_is_empty(self) -> None:
        resolver = UniverseResolver(fixed_markets=["BTC/EUR", "ETH/EUR"])
        async def _mock(): return ["BTC/EUR", "ETH/EUR"]
        resolver._resolve_dynamic_markets = _mock  # type: ignore[method-assign]

        snapshot = await resolver.resolve_universe()

        self.assertEqual(snapshot.dynamic_markets, [])
        self.assertEqual(snapshot.fixed_markets, ["BTC/EUR", "ETH/EUR"])

    # GIVEN CoinGecko returns ETH ecosystem coins WHEN dynamic markets are resolved
    # THEN only available EUR Kraken pairs are returned in market-cap order.
    async def test_given_coingecko_payload_when_dynamic_resolved_then_available_eur_pairs_returned(self) -> None:
        payload = [
            {"symbol": "link"},
            {"symbol": "uni"},
            {"symbol": "notkraken"},
            {"symbol": "aave"},
        ]
        client = FakeHTTPClient(payload)
        resolver = UniverseResolver(
            fixed_markets=["BTC/EUR"],
            http_client=client,
            available_markets={"LINK/EUR", "UNI/EUR", "AAVE/EUR"},
        )

        markets = await resolver._resolve_dynamic_markets()

        self.assertEqual(markets, ["LINK/EUR", "UNI/EUR", "AAVE/EUR"])
        self.assertIn("coins/markets", client.url)
        self.assertEqual(client.params["category"], "ethereum-ecosystem")


if __name__ == "__main__":
    unittest.main()
