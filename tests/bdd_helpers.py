"""Shared BDD-style test helpers for the trading bot."""
import sys
from datetime import datetime, timezone
from pathlib import Path

BACKEND_DIR = Path(__file__).resolve().parents[1] / "backend"
if str(BACKEND_DIR) not in sys.path:
    sys.path.insert(0, str(BACKEND_DIR))

from domain.models import (  # noqa: E402
    Direction,
    ExecutionIntent,
    PositionRecord,
    RiskDecision,
    TradeIdea,
    TradingMode,
)


# Build a valid strategy idea and allow each test to override the important details.
def make_trade_idea(
    *,
    market: str = "BTC/EUR",
    direction: Direction = Direction.LONG,
    confidence: float = 0.7,
    sizing: float = 0.20,
) -> TradeIdea:
    return TradeIdea(
        strategy_id="basic_and_llm_strategy",
        market=market,
        direction=direction,
        thesis="BDD test signal",
        supporting_signals={"source": "test"},
        confidence=confidence,
        entry_plan="Enter at market",
        exit_plan="Exit on invalidation",
        stop_or_invalidation="Stop at configured loss",
        position_sizing_proposal=sizing,
        mode_eligibility=[
            TradingMode.MANUAL,
            TradingMode.SEMI_AUTOMATED,
            TradingMode.FULLY_AUTOMATED,
        ],
    )


# Build a risk decision that matches the supplied trade idea.
def make_risk_decision(
    trade_idea: TradeIdea,
    *,
    approved: bool = True,
    reason: str = "All risk checks passed",
) -> RiskDecision:
    return RiskDecision(
        trade_idea_id=trade_idea.id,
        approved=approved,
        reason=reason,
        timestamp=datetime.now(timezone.utc),
    )


# Build an open position with the minimum fields used by risk and execution tests.
def make_position(
    *,
    market: str = "BTC/EUR",
    size: float = 1.0,
    avg_price: float = 100.0,
) -> PositionRecord:
    return PositionRecord(
        market=market,
        size=size,
        avg_price=avg_price,
        unrealized_pnl=0.0,
        timestamp=datetime.now(timezone.utc),
    )


# Build an execution intent for paper execution tests.
def make_intent(
    *,
    market: str = "BTC/EUR",
    direction: Direction = Direction.LONG,
    size: float = 1.0,
    approval_request_id: str = "bdd-approval",
) -> ExecutionIntent:
    return ExecutionIntent(
        approval_request_id=approval_request_id,
        market=market,
        direction=direction,
        size=size,
    )
