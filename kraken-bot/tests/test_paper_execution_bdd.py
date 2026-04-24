"""BDD coverage for the paper execution engine."""
import unittest

from bdd_helpers import make_intent
from domain.models import Direction
from execution.paper import SLIPPAGE_RATE, TAKER_FEE_RATE, PaperExecutionEngine


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


if __name__ == "__main__":
    unittest.main()
