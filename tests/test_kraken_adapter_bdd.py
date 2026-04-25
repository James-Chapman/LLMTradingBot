"""BDD coverage for Kraken market-data adapter resilience."""

import json
import unittest

from bdd_helpers import BACKEND_DIR  # noqa: F401
from ingestion.kraken_adapter import KrakenMarketAdapter


class FakeFrame:
    """Small DataFrame-like stand-in for pykrakenapi results."""

    def __init__(self, rows):
        self.rows = rows
        self.empty = not bool(rows)

    def iterrows(self):
        return iter(self.rows.items())


class FakeMarketAPI:
    """Kraken market API stub that can return sequenced ticker responses."""

    def __init__(self, ticker_responses):
        self.ticker_responses = list(ticker_responses)
        self.ticker_calls = []

    def get_tradable_asset_pairs(self):
        return FakeFrame({"XXBTZEUR": {"altname": "XBTEUR"}})

    def get_ticker_information(self, pair_string: str):
        self.ticker_calls.append(pair_string)
        response = self.ticker_responses.pop(0)
        if isinstance(response, Exception):
            raise response
        return response


class KrakenAdapterBDDTests(unittest.IsolatedAsyncioTestCase):
    # FEAT-002: GIVEN Kraken temporarily rate-limits ticker calls WHEN batch prices are fetched
    # THEN the adapter backs off and retries before returning market snapshots.
    async def test_given_rate_limited_ticker_when_batch_fetches_then_request_is_retried(self) -> None:
        sleeps = []

        async def fake_sleep(delay: float) -> None:
            sleeps.append(delay)

        api = FakeMarketAPI([
            RuntimeError("Kraken API error: ['EAPI:Rate limit exceeded']"),
            FakeFrame({"XXBTZEUR": {"c": ["50000.25"], "v": ["1.0", "2.5"]}}),
        ])
        adapter = KrakenMarketAdapter(backoff_sleep=fake_sleep, backoff_jitter=lambda delay: 0.0)
        adapter.api = api

        snapshots = await adapter.get_tickers_batch(["BTC/EUR"])

        self.assertEqual(len(api.ticker_calls), 2)
        self.assertEqual(sleeps, [0.5])
        self.assertIn("BTC/EUR", snapshots)
        self.assertAlmostEqual(snapshots["BTC/EUR"].price, 50000.25)
        self.assertAlmostEqual(snapshots["BTC/EUR"].volume, 2.5)

    # FEAT-001: GIVEN Kraken sends a WebSocket ticker message WHEN it is parsed
    # THEN the adapter emits the same MarketSnapshot contract used by polling.
    async def test_given_websocket_ticker_message_when_parsed_then_market_snapshot_is_returned(self) -> None:
        adapter = KrakenMarketAdapter()
        adapter._ws_pair_to_symbol = {"XBT/EUR": "BTC/EUR"}

        snapshot = adapter._snapshot_from_websocket_message(json.dumps([
            7,
            {"c": ["50123.4", "0.02"], "v": ["3.1", "9.7"]},
            "ticker",
            "XBT/EUR",
        ]))

        self.assertIsNotNone(snapshot)
        self.assertEqual(snapshot.symbol, "BTC/EUR")
        self.assertAlmostEqual(snapshot.price, 50123.4)
        self.assertAlmostEqual(snapshot.volume, 9.7)

    # FEAT-001: GIVEN the strategy loop needs prices WHEN source is inspected
    # THEN WebSocket snapshots are subscribed and used before REST polling fallback.
    def test_given_strategy_loop_when_source_inspected_then_websocket_cache_feeds_prices(self) -> None:
        main_source = (BACKEND_DIR / "main.py").read_text(encoding="utf-8")

        self.assertIn("_cache_market_snapshot", main_source)
        self.assertIn("kraken_adapter.start_subscription(_active_markets, _cache_market_snapshot)", main_source)
        self.assertIn("_latest_market_snapshots[sym]", main_source)
        self.assertIn("missing = [sym for sym in active if sym not in snapshots]", main_source)


if __name__ == "__main__":
    unittest.main()
