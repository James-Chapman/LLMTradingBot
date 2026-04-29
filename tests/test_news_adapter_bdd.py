"""BDD coverage for news adapter factory and timestamp normalisation."""

import unittest
from datetime import datetime, timezone
from unittest.mock import patch

from bdd_helpers import BACKEND_DIR  # noqa: F401
from domain.models import NewsItem
from ingestion.news_adapter import (
    RSSAdapter,
    build_rss_adapters,
    normalise_news_item,
    rss_adapter_from_spec,
)


class RssAdapterFromSpecTests(unittest.TestCase):
    # GIVEN a valid 'Name::URL' spec WHEN parsed THEN an RSSAdapter with correct fields is returned.
    def test_given_valid_spec_when_parsed_then_adapter_has_correct_name_and_url(self) -> None:
        adapter = rss_adapter_from_spec("CoinDesk::https://www.coindesk.com/arc/outboundfeeds/rss/")

        self.assertIsNotNone(adapter)
        self.assertIsInstance(adapter, RSSAdapter)
        self.assertEqual(adapter.source_name, "CoinDesk")
        self.assertEqual(adapter.rss_url, "https://www.coindesk.com/arc/outboundfeeds/rss/")

    # GIVEN a spec with extra whitespace WHEN parsed THEN name and URL are stripped.
    def test_given_spec_with_whitespace_when_parsed_then_values_are_stripped(self) -> None:
        adapter = rss_adapter_from_spec("  My Source  ::  https://example.com/feed  ")

        self.assertIsNotNone(adapter)
        self.assertEqual(adapter.source_name, "My Source")
        self.assertEqual(adapter.rss_url, "https://example.com/feed")

    # GIVEN a spec with no '::' separator WHEN parsed THEN None is returned.
    def test_given_spec_without_separator_when_parsed_then_none_returned(self) -> None:
        with self.assertLogs("trading_bot.news_adapter", level="WARNING"):
            result = rss_adapter_from_spec("CoinDesk")

        self.assertIsNone(result)

    # GIVEN a spec with an empty name WHEN parsed THEN None is returned.
    def test_given_spec_with_empty_name_when_parsed_then_none_returned(self) -> None:
        with self.assertLogs("trading_bot.news_adapter", level="WARNING"):
            result = rss_adapter_from_spec("::https://example.com/feed")

        self.assertIsNone(result)

    # GIVEN a spec with an empty URL WHEN parsed THEN None is returned.
    def test_given_spec_with_empty_url_when_parsed_then_none_returned(self) -> None:
        with self.assertLogs("trading_bot.news_adapter", level="WARNING"):
            result = rss_adapter_from_spec("CoinDesk::")

        self.assertIsNone(result)

    # GIVEN a URL containing '::' WHEN parsed THEN only the first '::' is used as separator.
    def test_given_url_with_double_colon_when_parsed_then_url_is_preserved(self) -> None:
        adapter = rss_adapter_from_spec("Source::https://example.com/feed?a::b")

        self.assertIsNotNone(adapter)
        self.assertEqual(adapter.source_name, "Source")
        self.assertEqual(adapter.rss_url, "https://example.com/feed?a::b")


class BuildRssAdaptersTests(unittest.TestCase):
    # GIVEN a list of valid specs WHEN built THEN one adapter per spec is returned.
    def test_given_valid_specs_when_built_then_all_adapters_returned(self) -> None:
        specs = [
            "CoinDesk::https://www.coindesk.com/rss",
            "CoinTelegraph::https://cointelegraph.com/rss",
        ]
        adapters = build_rss_adapters(specs)

        self.assertEqual(len(adapters), 2)
        self.assertEqual(adapters[0].source_name, "CoinDesk")
        self.assertEqual(adapters[1].source_name, "CoinTelegraph")

    # GIVEN a mix of valid and invalid specs WHEN built THEN only valid adapters are included.
    def test_given_mixed_specs_when_built_then_invalid_specs_are_skipped(self) -> None:
        specs = [
            "CoinDesk::https://www.coindesk.com/rss",
            "bad-spec-no-separator",
            "CoinTelegraph::https://cointelegraph.com/rss",
        ]
        with self.assertLogs("trading_bot.news_adapter", level="WARNING"):
            adapters = build_rss_adapters(specs)

        self.assertEqual(len(adapters), 2)

    # GIVEN an empty list WHEN built THEN an empty list is returned.
    def test_given_empty_specs_when_built_then_empty_list_returned(self) -> None:
        adapters = build_rss_adapters([])
        self.assertEqual(adapters, [])


class RssAdapterFetchTests(unittest.IsolatedAsyncioTestCase):
    # GIVEN a configured RSSAdapter WHEN fetch_news is called THEN _fetch_rss is called with
    # the correct url and source_name.
    async def test_given_adapter_when_fetch_news_called_then_correct_args_used(self) -> None:
        adapter = rss_adapter_from_spec("TestFeed::https://example.com/rss")

        with patch("ingestion.news_adapter._fetch_rss", return_value=[]) as mock_fetch:
            await adapter.fetch_news()

        mock_fetch.assert_called_once_with("https://example.com/rss", "TestFeed")


class NewsTimestampNormalisationTests(unittest.TestCase):
    # GIVEN RSS and JSON news with mixed naive/aware timestamps
    # WHEN items are normalised and sorted THEN no datetime comparison error is raised.
    def test_given_mixed_timestamps_when_normalised_then_sort_is_safe(self) -> None:
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

    # GIVEN the news loop source WHEN inspected
    # THEN publish times are normalised before the descending sort.
    def test_given_news_loop_when_inspected_then_items_are_normalised_before_sort(self) -> None:
        main_source = (BACKEND_DIR / "main.py").read_text(encoding="utf-8")

        normalise_index = main_source.index("normalise_news_item(item)")
        sort_index = main_source.index("all_items.sort(key=lambda x: x.published_at")

        self.assertLess(normalise_index, sort_index)


if __name__ == "__main__":
    unittest.main()
