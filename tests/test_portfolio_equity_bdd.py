"""BDD coverage for portfolio value snapshots."""

import unittest
from datetime import datetime, timezone

from bdd_helpers import BACKEND_DIR, make_intent  # noqa: F401
from domain.models import Direction
from execution.paper import PaperExecutionEngine
from portfolio.equity import align_equity_history_with_current, build_equity_snapshot


class PortfolioEquityBDDTests(unittest.IsolatedAsyncioTestCase):
    # GIVEN cash and held crypto WHEN a portfolio snapshot is built
    # THEN equity is cash plus the current mark-to-market value of holdings.
    async def test_given_cash_and_holdings_when_snapshot_built_then_equity_is_cash_plus_mark_value(self) -> None:
        engine = PaperExecutionEngine(starting_capital=1_000.0)
        await engine.execute(
            make_intent(market="BTC/EUR", direction=Direction.LONG, size=2.0),
            market_price=100.0,
        )
        await engine.execute(
            make_intent(market="ETH/EUR", direction=Direction.LONG, size=3.0),
            market_price=50.0,
        )
        timestamp = datetime(2026, 4, 25, 12, 0, tzinfo=timezone.utc)

        snapshot = build_equity_snapshot(
            engine,
            {"BTC/EUR": 120.0, "ETH/EUR": 40.0},
            timestamp=timestamp,
        )

        self.assertEqual(snapshot["timestamp"], "2026-04-25T12:00:00+00:00")
        self.assertAlmostEqual(snapshot["positions_value"], 360.0)
        self.assertAlmostEqual(snapshot["cash"], engine.cash)
        self.assertAlmostEqual(snapshot["equity"], engine.cash + 360.0)

    # GIVEN the strategy loop receives a fresh market-data tick WHEN source is inspected
    # THEN the tick path records a fresh portfolio-value snapshot for the graph.
    def test_given_strategy_tick_when_source_inspected_then_tick_records_fresh_equity_snapshot(self) -> None:
        main_source = (BACKEND_DIR / "main.py").read_text(encoding="utf-8")

        self.assertIn("_record_equity_snapshot(prices)", main_source)
        self.assertNotIn(
            "repo.save_equity_snapshot(_current_equity, paper_engine.cash",
            main_source,
            "Strategy ticks must not persist stale _current_equity values to the graph history.",
        )

    # GIVEN live mode is enabled WHEN the dashboard builds account totals
    # THEN cash and equity come from the cached globals updated by the equity ticker
    # (not from a fresh per-poll Kraken API call and not from the paper engine).
    def test_given_live_mode_when_dashboard_totals_built_then_kraken_snapshot_is_used(self) -> None:
        main_source = (BACKEND_DIR / "main.py").read_text(encoding="utf-8")

        # _get_account_snapshot still exists (used by equity ticker and approval endpoint)
        self.assertIn("async def _get_account_snapshot", main_source)
        # Dashboard serves from the globals kept fresh every 10 s — no per-poll Kraken call
        self.assertIn('"equity": f"{_current_equity:.2f}"', main_source)
        self.assertIn('"cash": f"{_current_cash:.2f}"', main_source)
        # Direct paper-engine reads must not appear in the dashboard response body
        self.assertNotIn('"cash": f"{paper_engine.cash:.2f}"', main_source)

    # GIVEN persisted equity snapshots WHEN the backend starts
    # THEN the dashboard restores the full in-memory graph window, not only a short slice.
    def test_given_persisted_equity_when_backend_starts_then_full_graph_window_is_loaded(self) -> None:
        main_source = (BACKEND_DIR / "main.py").read_text(encoding="utf-8")

        self.assertIn("_equity_history: deque = deque(maxlen=1440)", main_source)
        self.assertIn("repo.get_equity_history(limit=1440)", main_source)
        self.assertNotIn("repo.get_equity_history(limit=288)", main_source)

    # GIVEN live mode and the Kraken Balance call fails WHEN _get_account_snapshot is called
    # THEN it returns the last-known cached globals — never the paper engine's depleted cash.
    def test_given_live_kraken_failure_when_snapshot_falls_back_then_paper_values_not_used(self) -> None:
        main_source = (BACKEND_DIR / "main.py").read_text(encoding="utf-8")

        # The live fallback must serve _current_cash / _current_equity, not paper_engine
        self.assertIn('"cash": _current_cash', main_source)
        self.assertIn('"equity": _current_equity', main_source)

    # GIVEN live mode WHEN the backend starts THEN _current_cash and _current_equity
    # are NOT seeded from the paper DB so a depleted paper balance cannot appear on screen.
    def test_given_live_mode_when_backend_starts_then_display_globals_not_seeded_from_paper_db(self) -> None:
        main_source = (BACKEND_DIR / "main.py").read_text(encoding="utf-8")

        # In live mode the startup block must guard the display-global assignment
        self.assertIn('trading_environment == "paper"', main_source)

    # GIVEN the dashboard computes a current total equity WHEN the graph history is prepared
    # THEN the final graph point matches the same total equity shown in the header.
    def test_given_current_equity_when_history_aligned_then_last_point_matches_header_value(self) -> None:
        timestamp = datetime(2026, 4, 25, 12, 5, tzinfo=timezone.utc)

        history = align_equity_history_with_current(
            [{"timestamp": "2026-04-25T12:00:00+00:00", "equity": 950.0}],
            current_equity=975.25,
            timestamp=timestamp,
        )

        self.assertEqual(history[-1]["timestamp"], "2026-04-25T12:05:00+00:00")
        self.assertEqual(history[-1]["equity"], 975.25)


if __name__ == "__main__":
    unittest.main()
