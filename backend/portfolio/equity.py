"""Portfolio equity snapshot helpers."""

from datetime import datetime, timezone
from typing import Any, Dict, Iterable, Protocol, TypedDict


class EquityPricedEngine(Protocol):
    """Execution engine contract required for mark-to-market valuation."""

    cash: float

    # Return current cash plus marked position value.
    def get_total_equity(self, prices: Dict[str, float]) -> float:
        """Return current cash plus marked position value."""
        ...


class EquitySnapshot(TypedDict):
    """Shape stored by the dashboard equity history."""

    timestamp: str
    equity: float
    cash: float
    positions_value: float


# Build the dashboard equity snapshot from account cash and current holding value.
def build_equity_snapshot(
    engine: EquityPricedEngine,
    prices: Dict[str, float],
    timestamp: datetime | None = None,
) -> EquitySnapshot:
    """Return a mark-to-market account snapshot for the current price tick."""
    snapshot_time = timestamp or datetime.now(timezone.utc)
    equity = float(engine.get_total_equity(prices))
    cash = float(engine.cash)
    return {
        "timestamp": snapshot_time.isoformat(),
        "equity": equity,
        "cash": cash,
        "positions_value": equity - cash,
    }


# Return graph history with a final point that matches the dashboard total equity.
def align_equity_history_with_current(
    history: Iterable[Dict[str, Any]],
    current_equity: float,
    timestamp: datetime | None = None,
) -> list[Dict[str, Any]]:
    """Return a copy of history whose final equity point matches current_equity."""
    aligned = [dict(point) for point in history]
    equity = float(current_equity)
    if aligned:
        try:
            last_equity = float(aligned[-1].get("equity", 0.0))
        except (TypeError, ValueError):
            last_equity = None
        if last_equity is not None and abs(last_equity - equity) <= 0.000000001:
            aligned[-1]["equity"] = equity
            return aligned

    snapshot_time = timestamp or datetime.now(timezone.utc)
    aligned.append({"timestamp": snapshot_time.isoformat(), "equity": equity})
    return aligned
