"""BDD coverage for repository data-retention behavior."""
import unittest
from datetime import datetime, timedelta

from bdd_helpers import BACKEND_DIR, make_trade_idea  # noqa: F401
from domain.models import Direction, OrderRecord
from storage import repository as repository_module
from storage.database import get_session, init_database
from storage.models import MarketSnapshotModel, OrderRecordModel
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

    # GIVEN an order is linked to a trade idea WHEN the ledger is requested
    # THEN the strategy that generated the trade is returned.
    def test_given_order_linked_to_trade_idea_when_ledger_requested_then_strategy_is_returned(self) -> None:
        init_database("sqlite://")
        repository = Repository()
        idea = make_trade_idea()
        repository.save_trade_idea(idea)
        order = OrderRecord(
            id="strategy-order-1",
            market="BTC/EUR",
            direction=Direction.LONG,
            size=0.1,
            price=50_000.0,
            status="filled",
            timestamp=datetime(2026, 4, 24, 12, 0, 0),
            position_id="position-1",
        )

        repository.save_order(order, approval_id="auto", trade_idea_id=idea.id)

        ledger = repository.get_trade_ledger()

        self.assertEqual(ledger[0]["strategy"], "combined")

    # GIVEN a close order has no direct signal WHEN the ledger is requested
    # THEN strategy falls back to the opening order's signal.
    def test_given_close_order_without_signal_when_ledger_requested_then_open_strategy_is_returned(self) -> None:
        init_database("sqlite://")
        repository = Repository()
        idea = make_trade_idea()
        repository.save_trade_idea(idea)
        position_id = "position-strategy-close"
        repository.save_order(
            OrderRecord(
                id="open-with-strategy",
                market="BTC/EUR",
                direction=Direction.LONG,
                size=0.1,
                price=50_000.0,
                status="filled",
                timestamp=datetime(2026, 4, 24, 12, 0, 0),
                position_id=position_id,
            ),
            approval_id="auto",
            trade_idea_id=idea.id,
        )
        repository.save_order(
            OrderRecord(
                id="close-without-strategy",
                market="BTC/EUR",
                direction=Direction.SHORT,
                size=0.1,
                price=51_000.0,
                status="filled",
                timestamp=datetime(2026, 4, 24, 13, 0, 0),
                position_id=position_id,
            ),
            approval_id="stop_loss",
        )

        ledger = repository.get_trade_ledger(limit=1)

        self.assertEqual(ledger[0]["id"], "close-wi")
        self.assertEqual(ledger[0]["strategy"], "combined")

    # E13: GIVEN legacy rejected order rows exist WHEN the trade ledger is requested
    # THEN they are excluded from the operational ledger.
    def test_given_rejected_order_row_when_ledger_requested_then_it_is_excluded(self) -> None:
        init_database("sqlite://")
        repository = Repository()
        with get_session() as session:
            session.add(OrderRecordModel(
                id="rejected-order-1",
                approval_request_id="auto",
                market="BTC/EUR",
                direction="long",
                size=0.1,
                price=50_000.0,
                fee=0.0,
                status="rejected",
                environment="paper",
                timestamp=datetime(2026, 4, 24, 12, 0, 0),
            ))

        self.assertEqual(repository.get_trade_ledger(), [])

    # E13: GIVEN a rejected intent WHEN the rejected-trades register is queried
    # THEN the intent appears with its rejection reason.
    def test_given_rejected_trade_saved_when_register_requested_then_reason_is_returned(self) -> None:
        init_database("sqlite://")
        repository = Repository()
        idea = make_trade_idea()
        repository.save_trade_idea(idea)

        repository.save_rejected_trade(
            market="ETH/EUR",
            direction="short",
            size=0.5,
            price=2_000.0,
            confidence=0.64,
            reason="insufficient_funds",
            trade_idea_id=idea.id,
            timestamp=datetime(2026, 4, 24, 13, 0, 0),
        )

        rejected_trades = repository.get_rejected_trades()

        self.assertEqual(len(rejected_trades), 1)
        self.assertEqual(rejected_trades[0]["market"], "ETH/EUR")
        self.assertEqual(rejected_trades[0]["direction"], "short")
        self.assertEqual(rejected_trades[0]["size"], 0.5)
        self.assertEqual(rejected_trades[0]["confidence"], 0.64)
        self.assertEqual(rejected_trades[0]["reason"], "insufficient_funds")
        self.assertEqual(rejected_trades[0]["trade_idea_id"], idea.id)
        self.assertEqual(rejected_trades[0]["strategy"], "combined")

    # E13: GIVEN save_order receives a rejected order WHEN it persists
    # THEN the repository redirects it to the rejected-trades register.
    def test_given_rejected_order_saved_when_persisting_then_register_receives_it(self) -> None:
        init_database("sqlite://")
        repository = Repository()
        rejected_order = OrderRecord(
            id="rejected-order-2",
            market="SOL/EUR",
            direction=Direction.LONG,
            size=2.0,
            price=100.0,
            status="rejected",
            timestamp=datetime(2026, 4, 24, 14, 0, 0),
            exchange_order_id="EOrder:Insufficient funds",
        )

        repository.save_order(rejected_order, approval_id="auto", environment="live", trade_idea_id="idea-2")

        self.assertEqual(repository.get_trade_ledger(), [])
        rejected_trades = repository.get_rejected_trades()
        self.assertEqual(len(rejected_trades), 1)
        self.assertEqual(rejected_trades[0]["market"], "SOL/EUR")
        self.assertEqual(rejected_trades[0]["reason"], "EOrder:Insufficient funds")
        self.assertEqual(rejected_trades[0]["trade_idea_id"], "idea-2")

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


class AsyncDbHelperBDDTests(unittest.IsolatedAsyncioTestCase):
    # QUALITY-003: GIVEN a synchronous repository call WHEN wrapped with _async_db
    # THEN it completes without blocking the event loop.
    async def test_given_sync_repo_call_when_wrapped_with_async_db_then_event_loop_is_not_blocked(self) -> None:
        import backend.main as bot_main  # noqa: PLC0415

        calls = []

        def slow_sync():
            calls.append("called")
            return "result"

        result = await bot_main._async_db(slow_sync)

        self.assertEqual(result, "result")
        self.assertEqual(calls, ["called"])

    # QUALITY-003: GIVEN two concurrent tasks WHEN one calls _async_db with a slow function
    # THEN the other task is not blocked and runs concurrently.
    async def test_given_slow_db_call_when_async_db_used_then_other_tasks_run_concurrently(self) -> None:
        import asyncio as _asyncio
        import time
        import backend.main as bot_main  # noqa: PLC0415

        order = []

        async def fast_task():
            order.append("fast_start")
            await _asyncio.sleep(0)
            order.append("fast_end")

        def slow_sync():
            time.sleep(0.05)
            order.append("slow_done")

        await _asyncio.gather(
            bot_main._async_db(slow_sync),
            fast_task(),
        )

        # fast_task should have run (and completed) while slow_sync was sleeping in its thread
        self.assertIn("fast_start", order)
        self.assertIn("fast_end", order)
        self.assertIn("slow_done", order)
        # fast_task should complete before the slow sync returns
        self.assertLess(order.index("fast_end"), order.index("slow_done"))


if __name__ == "__main__":
    unittest.main()
