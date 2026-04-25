"""Helpers for persisting realised risk state."""

from risk.engine import RiskEngine


# Record a realised trade result and persist daily risk counters.
def record_trade_result_and_persist(risk_engine: RiskEngine, repository, pnl: float) -> None:
    """Update risk state for a realised trade and save it to storage."""
    risk_engine.record_trade_result(pnl)
    repository.save_risk_state(
        daily_loss=risk_engine.daily_loss,
        daily_start_equity=risk_engine.daily_start_equity,
        last_reset_date=risk_engine._last_reset_date,
    )
