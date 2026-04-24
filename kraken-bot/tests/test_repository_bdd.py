"""BDD coverage for repository data-retention behavior."""
import unittest
from datetime import datetime, timedelta

from bdd_helpers import BACKEND_DIR  # noqa: F401
from storage import repository as repository_module
from storage.database import get_session, init_database
from storage.models import MarketSnapshotModel
from storage.repository import Repository


class RepositoryBDDTests(unittest.TestCase):

    # GIVEN one market has more ticks than the retention limit WHEN old ticks are trimmed
    # THEN only that market's newest ticks are kept and other markets are untouched.
    def test_given_many_price_ticks_when_trim_runs_then_latest_rows_for_symbol_are_kept(self) -> None:
        init_database("sqlite://")
        original_keep = repository_module._MAX_PRICE_ROWS_PER_SYMBOL
        repository_module._MAX_PRICE_ROWS_PER_SYMBOL = 3
        base_time = datetime(2026, 4, 24, 12, 0, 0)
        try:
            with get_session() as session:
                for idx in range(5):
                    session.add(MarketSnapshotModel(
                        symbol="BTC/EUR",
                        timestamp=base_time + timedelta(seconds=idx),
                        price=100.0 + idx,
                    ))
                session.add(MarketSnapshotModel(
                    symbol="ETH/EUR",
                    timestamp=base_time,
                    price=200.0,
                ))

            Repository().trim_old_price_ticks("BTC/EUR")

            with get_session() as session:
                btc_prices = [
                    row.price
                    for row in session.query(MarketSnapshotModel)
                    .filter(MarketSnapshotModel.symbol == "BTC/EUR")
                    .order_by(MarketSnapshotModel.timestamp.asc())
                    .all()
                ]
                eth_count = (
                    session.query(MarketSnapshotModel)
                    .filter(MarketSnapshotModel.symbol == "ETH/EUR")
                    .count()
                )

            self.assertEqual(btc_prices, [102.0, 103.0, 104.0])
            self.assertEqual(eth_count, 1)
        finally:
            repository_module._MAX_PRICE_ROWS_PER_SYMBOL = original_keep

    # GIVEN closed trades exist WHEN P&L summary is requested THEN daily and market totals are grouped.
    def test_given_closed_trades_when_pnl_summary_requested_then_totals_are_grouped(self) -> None:
        init_database("sqlite://")
        repository = Repository()
        entry_at = datetime(2026, 4, 24, 10, 0, 0)

        repository.save_signal_outcome(
            strategy_id="combined",
            market="BTC/EUR",
            direction="long",
            entry_price=100.0,
            exit_price=110.0,
            size=1.0,
            pnl=10.0,
            confidence=0.7,
            exit_reason="auto",
            entry_at=entry_at,
        )
        repository.save_signal_outcome(
            strategy_id="combined",
            market="ETH/EUR",
            direction="long",
            entry_price=100.0,
            exit_price=95.0,
            size=1.0,
            pnl=-5.0,
            confidence=0.7,
            exit_reason="auto",
            entry_at=entry_at,
        )

        summary = repository.get_pnl_summary()

        self.assertEqual(summary["total_pnl"], 5.0)
        self.assertEqual(summary["by_market"]["BTC/EUR"], 10.0)
        self.assertEqual(summary["by_market"]["ETH/EUR"], -5.0)
        self.assertEqual(summary["by_day"]["2026-04-24"], 5.0)

    # GIVEN operator control state WHEN it is saved and loaded THEN the selected strategy is persisted.
    def test_given_control_state_when_saved_then_selected_strategy_is_loaded(self) -> None:
        init_database("sqlite://")
        repository = Repository()

        repository.save_control_state(
            emergency_stop=False,
            disabled_markets=[],
            disabled_strategies=[],
            live_markets=[],
            selected_strategy="llm",
        )

        state = repository.load_control_state()

        self.assertIsNotNone(state)
        self.assertEqual(state["selected_strategy"], "llm")


if __name__ == "__main__":
    unittest.main()
