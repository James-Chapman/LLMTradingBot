"""
BDD tests for risk/engine.py (T2.Q7.3).

Covers: one-position-per-pair gate, insufficient-cash gate,
per-trade loss limit gate, daily loss limit gate, and the happy path.
"""

import pytest
from datetime import datetime, timezone
from domain.models import Direction, PositionRecord, TradeIdea, TradingMode
from risk.engine import RiskEngine


# ── Helpers ───────────────────────────────────────────────────────────────────

def _idea(
    market: str = "BTC/EUR",
    direction: Direction = Direction.LONG,
    confidence: float = 0.70,
    sizing: float = 0.10,
) -> TradeIdea:
    return TradeIdea(
        strategy_id="test_strategy",
        market=market,
        direction=direction,
        thesis="test",
        supporting_signals={},
        confidence=confidence,
        entry_plan="enter",
        exit_plan="exit",
        stop_or_invalidation="stop",
        position_sizing_proposal=sizing,
        mode_eligibility=[TradingMode.FULLY_AUTOMATED],
    )


def _open_long(market: str = "BTC/EUR", size: float = 0.01) -> PositionRecord:
    return PositionRecord(
        position_id="pos-001",
        market=market,
        size=size,
        avg_price=50_000.0,
        unrealized_pnl=0.0,
        timestamp=datetime.now(timezone.utc),
    )


# ── Tests ─────────────────────────────────────────────────────────────────────

class TestRiskEngineOnePositionPerPairBDD:
    async def test_given_existing_long_when_new_long_submitted_then_rejected(self):
        """
        GIVEN an open LONG position for BTC/EUR,
        WHEN a new LONG idea is evaluated,
        THEN the decision is rejected with 'already open' reason.
        """
        engine = RiskEngine()
        engine.current_equity = 1_000.0

        result = await engine.evaluate_trade(
            _idea(direction=Direction.LONG),
            open_positions=[_open_long()],
            available_cash=1_000.0,
            market_price=50_000.0,
        )
        assert result.approved is False
        assert "already open" in result.reason.lower() or "one position" in result.reason.lower()

    async def test_given_existing_long_when_short_submitted_then_not_blocked_by_position_gate(self):
        """
        GIVEN an open LONG position for BTC/EUR,
        WHEN a SHORT idea is evaluated,
        THEN the position gate does NOT block it (different direction).
        """
        engine = RiskEngine()
        engine.current_equity = 10_000.0

        result = await engine.evaluate_trade(
            _idea(direction=Direction.SHORT, sizing=0.05),
            open_positions=[_open_long()],
            available_cash=10_000.0,
            market_price=50_000.0,
        )
        assert "already open" not in result.reason.lower()
        assert "one position" not in result.reason.lower()


class TestRiskEngineInsufficientCashBDD:
    async def test_given_no_cash_when_long_submitted_then_rejected(self):
        """
        GIVEN zero available cash,
        WHEN a LONG idea is evaluated,
        THEN the decision is rejected with an insufficient-cash reason.
        """
        engine = RiskEngine()
        engine.current_equity = 1_000.0

        result = await engine.evaluate_trade(
            _idea(direction=Direction.LONG, sizing=0.20),
            open_positions=[],
            available_cash=0.0,
            market_price=50_000.0,
        )
        assert result.approved is False
        assert "cash" in result.reason.lower() or "insufficient" in result.reason.lower()


class TestRiskEnginePerTradeLossBDD:
    async def test_given_tiny_equity_when_trade_submitted_then_rejected_for_loss_limit(self):
        """
        GIVEN equity=10 EUR (5% per-trade limit = 0.50 EUR),
        WHEN a trade with any non-trivial size is evaluated,
        THEN the decision is rejected for exceeding the per-trade loss limit.
        """
        engine = RiskEngine()
        engine.current_equity = 10.0  # 5% limit = 0.50 EUR

        result = await engine.evaluate_trade(
            _idea(direction=Direction.LONG, sizing=0.20),
            open_positions=[],
            available_cash=1_000.0,
            market_price=50_000.0,
        )
        assert result.approved is False
        assert "loss" in result.reason.lower()


class TestRiskEngineDailyLossBDD:
    async def test_given_daily_loss_at_limit_when_trade_submitted_then_rejected(self):
        """
        GIVEN daily_loss already equal to 100% of equity (far beyond the daily limit),
        WHEN a new trade is evaluated,
        THEN the decision is rejected for exceeding the daily loss limit.
        """
        engine = RiskEngine()
        engine.current_equity = 1_000.0
        engine.daily_loss = 1_000.0  # exhausted all equity as daily loss

        result = await engine.evaluate_trade(
            _idea(direction=Direction.LONG, sizing=0.10),
            open_positions=[],
            available_cash=1_000.0,
            market_price=50_000.0,
        )
        assert result.approved is False
        assert "daily" in result.reason.lower()


class TestRiskEngineHappyPathBDD:
    async def test_given_sufficient_cash_no_positions_when_long_then_approved(self):
        """
        GIVEN ample cash, no open positions, and equity within limits,
        WHEN a standard LONG idea is evaluated,
        THEN the decision is approved.
        """
        engine = RiskEngine()
        engine.current_equity = 10_000.0

        result = await engine.evaluate_trade(
            _idea(direction=Direction.LONG, sizing=0.10),
            open_positions=[],
            available_cash=10_000.0,
            market_price=50_000.0,
        )
        assert result.approved is True
