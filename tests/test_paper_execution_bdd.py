"""BDD coverage for the paper execution engine."""
import unittest
from datetime import datetime, timezone

from bdd_helpers import make_intent
from domain.models import Direction, PositionRecord
from execution.paper import SLIPPAGE_RATE, TAKER_FEE_RATE, PaperExecutionEngine


class CapturingRepository:
    """Capture persistence calls made by the paper engine."""

    def __init__(self) -> None:
        self.saved_orders = []
        self.saved_fills = []
        self.saved_signal_outcomes = []
        self.deleted_positions = []

    # GIVEN an order is persisted WHEN save_order is called THEN capture the source ID.
    def save_order(self, order, approval_id="", fee=0.0, environment="paper", trade_idea_id="") -> None:
        self.saved_orders.append({
            "order": order,
            "approval_id": approval_id,
            "fee": fee,
            "environment": environment,
            "trade_idea_id": trade_idea_id,
        })

    # GIVEN a fill is persisted WHEN save_fill is called THEN capture the fill.
    def save_fill(self, fill) -> None:
        self.saved_fills.append(fill)

    # GIVEN a position closes WHEN delete_open_position is called THEN capture its ID.
    def delete_open_position(self, position_id: str) -> None:
        self.deleted_positions.append(position_id)

    # GIVEN an open position is persisted WHEN upsert_open_position is called THEN ignore it.
    def upsert_open_position(self, **_kwargs) -> None:
        return None

    # GIVEN a closed trade is persisted WHEN save_signal_outcome is called THEN capture it.
    def save_signal_outcome(self, **kwargs) -> None:
        self.saved_signal_outcomes.append(kwargs)


class PaperExecutionEngineBDDTests(unittest.IsolatedAsyncioTestCase):
    # GIVEN available cash WHEN a long intent executes THEN a filled order opens one position.
    async def test_given_cash_when_long_executes_then_position_opens_and_cash_includes_costs(self) -> None:
        engine = PaperExecutionEngine(starting_capital=500.0)
        market_price = 100.0
        intent = make_intent(direction=Direction.LONG, size=1.0)

        order, position_id = await engine.execute(intent, market_price)

        fill_price = market_price * (1 + SLIPPAGE_RATE)
        expected_fee = fill_price * TAKER_FEE_RATE
        self.assertEqual(order.status, "filled")
        self.assertEqual(order.position_id, position_id)
        self.assertIn(position_id, engine.positions)
        self.assertAlmostEqual(engine.positions[position_id].avg_price, fill_price)
        self.assertAlmostEqual(engine.cash, 500.0 - fill_price - expected_fee)

    # GIVEN a long is already open WHEN another same-market long executes THEN the order is rejected.
    async def test_given_existing_long_when_second_long_executes_then_order_is_rejected(self) -> None:
        engine = PaperExecutionEngine(starting_capital=500.0)
        first_intent = make_intent(direction=Direction.LONG, size=1.0)
        second_intent = make_intent(direction=Direction.LONG, size=1.0)

        await engine.execute(first_intent, 100.0)
        order, position_id = await engine.execute(second_intent, 100.0)

        self.assertEqual(order.status, "rejected")
        self.assertEqual(position_id, "")
        self.assertEqual(len(engine.positions), 1)

    # GIVEN an open long WHEN a matching short executes THEN the oldest long is closed.
    async def test_given_open_long_when_short_executes_then_position_is_closed_fifo(self) -> None:
        engine = PaperExecutionEngine(starting_capital=500.0)
        open_order, opened_position_id = await engine.execute(
            make_intent(direction=Direction.LONG, size=1.0),
            100.0,
        )

        close_order, closed_position_id = await engine.execute(
            make_intent(direction=Direction.SHORT, size=1.0),
            110.0,
        )

        self.assertEqual(open_order.status, "filled")
        self.assertEqual(close_order.status, "filled")
        self.assertEqual(closed_position_id, opened_position_id)
        self.assertEqual(engine.positions, {})

    # GIVEN a marked open position WHEN total equity is requested THEN live prices are used.
    async def test_given_open_position_when_equity_requested_then_cash_plus_market_value_is_returned(self) -> None:
        engine = PaperExecutionEngine(starting_capital=500.0)
        await engine.execute(make_intent(direction=Direction.LONG, size=1.0), 100.0)

        equity = engine.get_total_equity({"BTC/EUR": 120.0})

        self.assertAlmostEqual(equity, engine.cash + 120.0)

    # GIVEN a long position has made a new high WHEN price retraces past the trail
    # THEN the trailing stop reports that the position should close.
    async def test_given_long_trailing_high_when_price_retraces_then_trailing_stop_triggers(self) -> None:
        engine = PaperExecutionEngine(starting_capital=500.0)
        _, position_id = await engine.execute(make_intent(direction=Direction.LONG, size=1.0), 100.0)

        engine.update_trailing_prices({"BTC/EUR": 120.0})

        self.assertFalse(engine.trailing_stop_triggered(position_id, 116.0, 0.05))
        self.assertTrue(engine.trailing_stop_triggered(position_id, 113.0, 0.05))

    # GIVEN a long position is still profitable WHEN price retraces from a high
    # THEN the hard stop-loss condition is not triggered.
    async def test_given_profitable_long_when_price_retraces_then_stop_loss_does_not_trigger(self) -> None:
        engine = PaperExecutionEngine(starting_capital=500.0)
        _, position_id = await engine.execute(make_intent(direction=Direction.LONG, size=1.0), 100.0)

        engine.update_trailing_prices({"BTC/EUR": 120.0})

        self.assertTrue(engine.trailing_stop_triggered(position_id, 113.0, 0.05))
        self.assertFalse(engine.stop_loss_triggered(position_id, 113.0, 0.05))

    # GIVEN a short position is still profitable WHEN price retraces from a low
    # THEN the hard stop-loss condition is not triggered.
    async def test_given_profitable_short_when_price_retraces_then_stop_loss_does_not_trigger(self) -> None:
        engine = PaperExecutionEngine(starting_capital=500.0)
        _, position_id = await engine.execute(make_intent(direction=Direction.SHORT, size=1.0), 100.0)

        engine.update_trailing_prices({"BTC/EUR": 80.0})

        self.assertTrue(engine.trailing_stop_triggered(position_id, 87.0, 0.05))
        self.assertFalse(engine.stop_loss_triggered(position_id, 87.0, 0.05))

    # GIVEN a position has moved against entry WHEN loss exceeds the configured threshold
    # THEN the hard stop-loss condition is triggered.
    async def test_given_losing_positions_when_loss_exceeds_threshold_then_stop_loss_triggers(self) -> None:
        engine = PaperExecutionEngine(starting_capital=500.0)
        _, long_id = await engine.execute(make_intent(direction=Direction.LONG, size=1.0), 100.0)
        _, short_id = await engine.execute(
            make_intent(market="ETH/EUR", direction=Direction.SHORT, size=1.0),
            100.0,
        )

        self.assertTrue(engine.stop_loss_triggered(long_id, 94.0, 0.05))
        self.assertTrue(engine.stop_loss_triggered(short_id, 106.0, 0.05))

    # GIVEN a manual close uses the targeted close path WHEN the close order is saved
    # THEN the ledger source is not persisted as stop_loss.
    async def test_given_manual_close_when_order_is_saved_then_source_is_manual_close(self) -> None:
        repo = CapturingRepository()
        engine = PaperExecutionEngine(starting_capital=500.0, repository=repo)
        _, position_id = await engine.execute(make_intent(direction=Direction.LONG, size=1.0), 100.0)

        await engine.close_position(position_id, 110.0, approval_request_id="manual_close")

        close_order = repo.saved_orders[-1]
        self.assertEqual(close_order["approval_id"], "manual_close")

    # GIVEN a manual close WHEN the position closes THEN the log is not labelled stop-loss.
    async def test_given_manual_close_when_logged_then_message_uses_close_source(self) -> None:
        engine = PaperExecutionEngine(starting_capital=500.0)
        _, position_id = await engine.execute(make_intent(direction=Direction.LONG, size=1.0), 100.0)

        with self.assertLogs("kraken_bot.paper_engine", level="INFO") as captured:
            await engine.close_position(position_id, 110.0, approval_request_id="manual_close")

        self.assertIn("Position closed (manual_close)", captured.output[-1])
        self.assertNotIn("stop-loss", captured.output[-1])

    # GIVEN a long is closed with slippage and fees WHEN the outcome is recorded
    # THEN realised P&L matches the actual fills and account cash delta.
    async def test_given_long_closed_when_outcome_recorded_then_pnl_uses_fill_prices_and_fees(self) -> None:
        repo = CapturingRepository()
        engine = PaperExecutionEngine(starting_capital=500.0, repository=repo)
        _, position_id = await engine.execute(make_intent(direction=Direction.LONG, size=1.0), 100.0)

        close_order = await engine.close_position(position_id, 110.0, approval_request_id="manual_close")
        engine.record_closed_trade(position_id, close_order.price, "manual_close")

        entry_fill = 100.0 * (1 + SLIPPAGE_RATE)
        exit_fill = 110.0 * (1 - SLIPPAGE_RATE)
        entry_fee = entry_fill * TAKER_FEE_RATE
        exit_fee = exit_fill * TAKER_FEE_RATE
        expected_pnl = (exit_fill - entry_fill) - entry_fee - exit_fee
        outcome = repo.saved_signal_outcomes[-1]
        self.assertAlmostEqual(outcome["exit_price"], exit_fill)
        self.assertAlmostEqual(outcome["pnl"], expected_pnl)
        self.assertAlmostEqual(engine.cash - 500.0, expected_pnl)

    # GIVEN an opposite signal closes a long WHEN the outcome is recorded
    # THEN realised P&L uses the short fill price and both fees.
    async def test_given_short_closes_long_when_outcome_recorded_then_pnl_uses_actual_fill(self) -> None:
        repo = CapturingRepository()
        engine = PaperExecutionEngine(starting_capital=500.0, repository=repo)
        _, position_id = await engine.execute(make_intent(direction=Direction.LONG, size=1.0), 100.0)

        close_order, closed_position_id = await engine.execute(
            make_intent(direction=Direction.SHORT, size=1.0),
            110.0,
        )
        engine.record_closed_trade(closed_position_id, close_order.price, "auto")

        entry_fill = 100.0 * (1 + SLIPPAGE_RATE)
        exit_fill = 110.0 * (1 - SLIPPAGE_RATE)
        entry_fee = entry_fill * TAKER_FEE_RATE
        exit_fee = exit_fill * TAKER_FEE_RATE
        expected_pnl = (exit_fill - entry_fill) - entry_fee - exit_fee
        outcome = repo.saved_signal_outcomes[-1]
        self.assertEqual(closed_position_id, position_id)
        self.assertAlmostEqual(outcome["exit_price"], exit_fill)
        self.assertAlmostEqual(outcome["pnl"], expected_pnl)
        self.assertAlmostEqual(engine.cash - 500.0, expected_pnl)


    # GIVEN starting cash of €500 WHEN a paper short opens with no existing long
    # THEN cash does NOT increase — proceeds are locked as margin (BUG-012).
    async def test_given_no_existing_long_when_paper_short_opens_then_cash_does_not_increase(self) -> None:
        starting_cash = 500.0
        engine = PaperExecutionEngine(starting_capital=starting_cash)
        intent = make_intent(direction=Direction.SHORT, size=1.0)

        order, position_id = await engine.execute(intent, market_price=100.0)

        self.assertEqual(order.status, "filled", "short should be filled")
        self.assertLess(
            engine.cash, starting_cash,
            "Cash must decrease (not increase) when a paper short opens — fee is deducted",
        )

    # GIVEN a paper short is opened and then closed at a lower price WHEN the P&L is computed
    # THEN cash reflects the correct profit from the price fall (BUG-012).
    async def test_given_paper_short_opened_and_closed_when_pnl_computed_then_cash_reflects_profit(self) -> None:
        starting_cash = 1000.0
        engine = PaperExecutionEngine(starting_capital=starting_cash)
        open_intent = make_intent(direction=Direction.SHORT, size=1.0)

        open_order, position_id = await engine.execute(open_intent, market_price=100.0)
        self.assertEqual(open_order.status, "filled")

        # Close the short at a lower price — should be profitable
        close_order = await engine.close_position(position_id, market_price=90.0)
        self.assertIsNotNone(close_order)

        # Net cash change = profit (100-90) minus both fees; final cash should exceed starting
        self.assertGreater(
            engine.cash, starting_cash,
            "Cash after closing a profitable short must exceed starting cash",
        )


    # BUG-025: GIVEN a long restored with avg_price=50,000 (watermark reset to None)
    # WHEN update_trailing_prices is called with 60,000 (price has rallied since restart)
    # THEN trailing_high is 60,000 and trailing_stop_triggered fires at the correct level.
    def test_given_restored_long_when_price_updates_then_watermark_uses_live_price_not_entry(self) -> None:
        engine = PaperExecutionEngine(starting_capital=100_000.0)
        position_id = "test-restore-001"
        engine.positions[position_id] = PositionRecord(
            position_id=position_id,
            market="BTC/EUR",
            size=1.0,
            avg_price=50_000.0,
            unrealized_pnl=0.0,
            timestamp=datetime.now(timezone.utc),
        )
        # Simulate restore: watermarks are None
        engine._position_meta[position_id] = {
            "trailing_high": None,
            "trailing_low": None,
        }

        # First live price update after restart sets watermark to current price
        engine.update_trailing_prices({"BTC/EUR": 60_000.0})

        meta = engine._position_meta[position_id]
        self.assertEqual(meta["trailing_high"], 60_000.0)
        # 5% trail from 60,000 → stop at 57,000; price 57,000 should trigger
        self.assertTrue(engine.trailing_stop_triggered(position_id, 57_000.0, 0.05))
        # Price 58,000 is above the 57,000 stop — should not trigger
        self.assertFalse(engine.trailing_stop_triggered(position_id, 58_000.0, 0.05))

    # BUG-025: GIVEN a restored long with None watermark BEFORE the first price update
    # WHEN trailing_stop_triggered is called THEN it falls back to avg_price conservatively.
    def test_given_restored_long_before_price_update_when_stop_checked_then_fallback_to_avg_price(self) -> None:
        engine = PaperExecutionEngine(starting_capital=100_000.0)
        position_id = "test-restore-002"
        engine.positions[position_id] = PositionRecord(
            position_id=position_id,
            market="BTC/EUR",
            size=1.0,
            avg_price=50_000.0,
            unrealized_pnl=0.0,
            timestamp=datetime.now(timezone.utc),
        )
        engine._position_meta[position_id] = {"trailing_high": None, "trailing_low": None}

        # With no price update yet, stop falls back to avg_price (conservative)
        # 5% trail from 50,000 → stop at 47,500; price 47,000 should trigger
        self.assertTrue(engine.trailing_stop_triggered(position_id, 47_000.0, 0.05))
        # Price 48,000 is above 47,500 — should not trigger
        self.assertFalse(engine.trailing_stop_triggered(position_id, 48_000.0, 0.05))


if __name__ == "__main__":
    unittest.main()
