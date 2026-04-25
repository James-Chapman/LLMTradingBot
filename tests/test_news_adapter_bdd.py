"""BDD coverage for news adapters, including stub detection and warning behaviour."""
import unittest
from datetime import datetime, timezone

from bdd_helpers import BACKEND_DIR  # noqa: F401
from domain.models import NewsItem
from ingestion.news_adapter import CoinNewsAdapter, CoinWeekAdapter, normalise_news_item


class StubNewsAdapterBDDTests(unittest.IsolatedAsyncioTestCase):
    # BUG-019: GIVEN CoinNewsAdapter WHEN fetch_news is called THEN an empty list is returned
    # and a warning is logged so the operator knows the adapter is inactive.
    async def test_given_coinnews_adapter_when_fetch_news_called_then_empty_list_and_warning_logged(
        self,
    ) -> None:
        adapter = CoinNewsAdapter()
        with self.assertLogs("kraken_bot.news_adapter", level="WARNING") as cm:
            result = await adapter.fetch_news()

        self.assertEqual(result, [])
        self.assertTrue(
            any("stub" in msg.lower() or "coinnews" in msg.lower() for msg in cm.output),
            f"Expected a stub warning in logs, got: {cm.output}",
        )

    # BUG-019: GIVEN CoinWeekAdapter WHEN fetch_news is called THEN an empty list is returned
    # and a warning is logged so the operator knows the adapter is inactive.
    async def test_given_coinweek_adapter_when_fetch_news_called_then_empty_list_and_warning_logged(
        self,
    ) -> None:
        adapter = CoinWeekAdapter()
        with self.assertLogs("kraken_bot.news_adapter", level="WARNING") as cm:
            result = await adapter.fetch_news()

        self.assertEqual(result, [])
        self.assertTrue(
            any("stub" in msg.lower() or "coinweek" in msg.lower() for msg in cm.output),
            f"Expected a stub warning in logs, got: {cm.output}",
        )

    # BUG-019: GIVEN a stub adapter called multiple times WHEN warnings are checked
    # THEN the warning is only emitted once per instance to avoid log spam.
    async def test_given_stub_adapter_called_twice_when_warnings_checked_then_only_one_warning_emitted(
        self,
    ) -> None:
        adapter = CoinNewsAdapter()
        with self.assertLogs("kraken_bot.news_adapter", level="WARNING") as cm:
            await adapter.fetch_news()
            await adapter.fetch_news()

        warning_count = sum(1 for msg in cm.output if "WARNING" in msg)
        self.assertEqual(
            warning_count, 1, "Stub warning should only fire once per adapter instance"
        )

    # BUG-022: GIVEN RSS and JSON news with mixed naive/aware timestamps
    # WHEN items are normalised and sorted THEN no datetime comparison error is raised.
    def test_given_mixed_news_timestamps_when_normalised_then_sort_is_safe(self) -> None:
        naive_item = NewsItem(
            id="rss",
            source="RSS",
            title="Naive RSS time",
            content="",
            published_at=datetime(2026, 4, 25, 10, 0, 0),
            url="https://example.test/rss",
        )
        aware_item = NewsItem(
            id="json",
            source="JSON",
            title="Aware JSON time",
            content="",
            published_at=datetime(2026, 4, 25, 11, 0, 0, tzinfo=timezone.utc),
            url="https://example.test/json",
        )

        items = [normalise_news_item(naive_item), normalise_news_item(aware_item)]
        items.sort(key=lambda item: item.published_at, reverse=True)

        self.assertEqual(items[0].id, "json")
        self.assertTrue(all(item.published_at.tzinfo is not None for item in items))
        self.assertEqual(items[0].published_at.tzinfo, timezone.utc)

    # BUG-022: GIVEN the news loop source WHEN inspected
    # THEN publish times are normalised before the descending sort.
    def test_given_news_loop_when_inspected_then_items_are_normalised_before_sort(self) -> None:
        main_source = (BACKEND_DIR / "main.py").read_text(encoding="utf-8")

        normalise_index = main_source.index("normalise_news_item(item)")
        sort_index = main_source.index("all_items.sort(key=lambda x: x.published_at")

        self.assertLess(normalise_index, sort_index)


if __name__ == "__main__":
    unittest.main()
