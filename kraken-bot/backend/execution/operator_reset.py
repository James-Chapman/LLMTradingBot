"""Operator reset helpers for paper positions."""

from typing import Callable, Dict

from execution.paper import PaperExecutionEngine


# Close every open paper position through the normal close-order accounting path.
async def close_positions_for_operator_reset(
    *,
    paper_engine: PaperExecutionEngine,
    prices: Dict[str, float],
    record_trade_result: Callable[[float], None],
    repository=None,
    learner=None,
) -> int:
    """Close open positions for operator reset and persist realised accounting."""
    closed_count = 0
    positions_snapshot = list(paper_engine.open_positions())
    for pos in positions_snapshot:
        exit_price = prices.get(pos.market, pos.avg_price)
        order = await paper_engine.close_position(
            pos.position_id,
            exit_price,
            approval_request_id="operator_reset",
        )
        if order is None:
            continue
        pnl = paper_engine.record_closed_trade(
            pos.position_id,
            order.price,
            "operator_reset",
        )
        if pnl is None:
            continue
        record_trade_result(pnl)
        if repository is not None:
            repository.update_order_pnl(order.id, pnl)
        if learner is not None:
            meta = paper_engine._position_meta.get(pos.position_id, {})
            learner.record_outcome(
                meta.get("strategy_id", "unknown"),
                pos.market,
                meta.get("direction", "long"),
                pnl,
            )
        closed_count += 1
    return closed_count
