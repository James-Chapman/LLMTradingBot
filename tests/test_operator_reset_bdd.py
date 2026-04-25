"""BDD coverage for operator reset accounting."""
import unittest

from bdd_helpers import make_intent
from domain.models import Direction
from execution.operator_reset import close_positions_for_operator_reset
from execution.paper import PaperExecutionEngine


class CapturingRepository:
    """Capture persistence calls made during operator reset."""

    def __init__(self) -> None:
        self.saved_orders = []
        self.saved_fills = []
        self.saved_signal_outcomes = []
        self.deleted_positions = []
        self.order_pnls = []

    # GIVEN an order is persisted WHEN save_order is called THEN capture it.
    def save_order(self, order, approval_id="", fee=0.0, environment="paper", trade_idea_id="") -> None:
        self.saved_orders.append({
            "order": order,
            "approval_id": approval_id,
            "fee": fee,
            "environment": environment,
            "trade_idea_id": trade_idea_id,
        })

    # GIVEN a fill is persisted WHEN save_fill is called THEN capture it.
    def save_fill(self, fill) -> None:
        self.saved_fills.append(fill)

    # GIVEN a position closes WHEN delete_open_position is called THEN capture it.
    def delete_open_position(self, position_id: str) -> None:
        self.deleted_positions.append(position_id)

    # GIVEN an open position is persisted WHEN upsert_open_position is called THEN ignore it.
    def upsert_open_position(self, **_kwargs) -> None:
        return None

    # GIVEN a closed outcome is persisted WHEN save_signal_outcome is called THEN capture it.
    def save_signal_outcome(self, **kwargs) -> None:
        self.saved_signal_outcomes.append(kwargs)

    # GIVEN close order P&L is stamped WHEN update_order_pnl is called THEN capture it.
    def update_order_pnl(self, order_id: str, pnl: float) -> None:
        self.order_pnls.append((order_id, pnl))


class CapturingLearner:
    """Capture learner outcomes."""

    def __init__(self) -> None:
        self.outcomes = []

    # GIVEN a trade result is learned WHEN record_outcome is called THEN capture it.
    def record_outcome(self, strategy_id: str, market: str, direction: str, pnl: float) -> None:
        self.outcomes.append((strategy_id, market, direction, pnl))


class OperatorResetBDDTests(unittest.IsolatedAsyncioTestCase):
    # GIVEN open positions WHEN operator reset runs THEN close orders, P&L, and outcomes are persisted.
    async def test_given_open_position_when_operator_reset_runs_then_accounting_is_persisted(self) -> None:
        repository = CapturingRepository()
        learner = CapturingLearner()
        risk_pnls = []
        engine = PaperExecutionEngine(starting_capital=500.0, repository=repository)
        _, position_id = await engine.execute(
            make_intent(direction=Direction.LONG, size=1.0),
            100.0,
            strategy_id="combined",
        )

        closed = await close_positions_for_operator_reset(
            paper_engine=engine,
            prices={"BTC/EUR": 110.0},
            record_trade_result=risk_pnls.append,
            repository=repository,
            learner=learner,
        )

        close_order = repository.saved_orders[-1]["order"]
        self.assertEqual(closed, 1)
        self.assertEqual(repository.saved_orders[-1]["approval_id"], "operator_reset")
        self.assertEqual(repository.deleted_positions[-1], position_id)
        self.assertEqual(len(repository.saved_signal_outcomes), 1)
        self.assertEqual(repository.order_pnls[-1][0], close_order.id)
        self.assertAlmostEqual(repository.order_pnls[-1][1], repository.saved_signal_outcomes[-1]["pnl"])
        self.assertEqual(risk_pnls, [repository.saved_signal_outcomes[-1]["pnl"]])
        self.assertEqual(learner.outcomes[-1][0], "combined")
        self.assertEqual(engine.positions, {})


if __name__ == "__main__":
    unittest.main()
