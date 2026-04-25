"""BDD coverage for repository data-retention behavior."""
import unittest
from datetime import datetime, timedelta

from bdd_helpers import BACKEND_DIR  # noqa: F401
from domain.models import Direction, OrderRecord
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
        self.assertEqual(list(summary["by_day"].values()), [5.0])

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

    # GIVEN Kraken accepts a live order WHEN the trade ledger is requested
    # THEN the submitted live order is visible even before fill reconciliation.
    def test_given_live_pending_order_when_ledger_requested_then_order_is_visible(self) -> None:
        init_database("sqlite://")
        repository = Repository()
        order = OrderRecord(
            id="live-order-1",
            market="BTC/EUR",
            direction=Direction.LONG,
            size=0.1,
            price=50_000.0,
            status="pending",
            timestamp=datetime(2026, 4, 24, 12, 0, 0),
            exchange_order_id="TX123",
        )

        repository.save_order(order, approval_id="manual", environment="live")

        ledger = repository.get_trade_ledger()

        self.assertEqual(len(ledger), 1)
        self.assertEqual(ledger[0]["status"], "pending")
        self.assertEqual(ledger[0]["environment"], "live")
        self.assertEqual(ledger[0]["exchange_order_id"], "TX123")

    # GIVEN a close order is inside the page but its opener is outside the limit
    # WHEN the ledger is built THEN the returned close row is still classified as close.
    def test_given_open_order_outside_limit_when_ledger_requested_then_close_is_classified(self) -> None:
        init_database("sqlite://")
        repository = Repository()
        position_id = "position-1"
        repository.save_order(
            OrderRecord(
                id="open-order-1",
                market="BTC/EUR",
                direction=Direction.LONG,
                size=0.1,
                price=50_000.0,
                status="filled",
                timestamp=datetime(2026, 4, 24, 12, 0, 0),
                position_id=position_id,
            ),
            approval_id="auto",
        )
        repository.save_order(
            OrderRecord(
                id="close-order-1",
                market="BTC/EUR",
                direction=Direction.SHORT,
                size=0.1,
                price=51_000.0,
                status="filled",
                timestamp=datetime(2026, 4, 24, 13, 0, 0),
                position_id=position_id,
            ),
            approval_id="auto",
        )

        ledger = repository.get_trade_ledger(limit=1)

        self.assertEqual(len(ledger), 1)
        self.assertEqual(ledger[0]["id"], "close-or")
        self.assertEqual(ledger[0]["trade_type"], "close")


    # GIVEN the equity ticker fires multiple times WHEN each tick calls save_equity_snapshot
    # THEN all snapshots are retrievable from the database (BUG-013).
    def test_given_equity_ticker_fires_multiple_times_when_snapshots_saved_then_all_retrievable(self) -> None:
        init_database("sqlite://")
        repository = Repository()

        # Simulate three equity ticker fires (each at 10-second resolution)
        repository.save_equity_snapshot(1000.0, 800.0, 200.0)
        repository.save_equity_snapshot(1005.0, 800.0, 205.0)
        repository.save_equity_snapshot(995.0,  800.0, 195.0)

        history = repository.get_equity_history(limit=10)

        self.assertEqual(len(history), 3, "All three equity snapshots must be saved to DB")
        equities = [h["equity"] for h in history]
        self.assertIn(1000.0, equities)
        self.assertIn(1005.0, equities)
        self.assertIn(995.0,  equities)


if __name__ == "__main__":
    unittest.main()
