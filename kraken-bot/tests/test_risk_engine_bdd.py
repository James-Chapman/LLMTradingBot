"""BDD coverage for portfolio risk checks."""
import unittest

from bdd_helpers import make_position, make_trade_idea
from domain.models import Direction
from risk import engine as risk_module
from risk.engine import RiskEngine


class RiskEngineBDDTests(unittest.IsolatedAsyncioTestCase):
    # GIVEN sufficient cash and no matching position WHEN risk evaluates THEN the trade is approved.
    async def test_given_valid_long_when_risk_evaluates_then_trade_is_approved(self) -> None:
        engine = RiskEngine()
        idea = make_trade_idea(direction=Direction.LONG)

        decision = await engine.evaluate_trade(
            idea,
            open_positions=[],
            available_cash=500.0,
            market_price=100.0,
        )

        self.assertTrue(decision.approved)
        self.assertEqual(decision.trade_idea_id, idea.id)
        self.assertIn("passed", decision.reason.lower())

    # GIVEN a same-direction position WHEN risk evaluates THEN one-position-per-pair is enforced.
    async def test_given_same_direction_position_when_risk_evaluates_then_trade_is_rejected(self) -> None:
        engine = RiskEngine()
        idea = make_trade_idea(market="ETH/EUR", direction=Direction.LONG)
        existing_position = make_position(market="ETH/EUR", size=1.0)

        decision = await engine.evaluate_trade(
            idea,
            open_positions=[existing_position],
            available_cash=500.0,
            market_price=100.0,
        )

        self.assertFalse(decision.approved)
        self.assertIn("already open", decision.reason.lower())

    # GIVEN an opposite-direction position WHEN risk evaluates THEN closing signals are allowed through position checks.
    async def test_given_opposite_direction_position_when_risk_evaluates_then_trade_can_be_approved(self) -> None:
        engine = RiskEngine()
        idea = make_trade_idea(market="ETH/EUR", direction=Direction.SHORT)
        existing_long = make_position(market="ETH/EUR", size=1.0)

        decision = await engine.evaluate_trade(
            idea,
            open_positions=[existing_long],
            available_cash=500.0,
            market_price=100.0,
        )

        self.assertTrue(decision.approved)

    # GIVEN too little cash WHEN a long is evaluated THEN cash sufficiency blocks it.
    async def test_given_insufficient_cash_when_long_evaluates_then_trade_is_rejected(self) -> None:
        engine = RiskEngine()
        idea = make_trade_idea(direction=Direction.LONG)

        decision = await engine.evaluate_trade(
            idea,
            open_positions=[],
            available_cash=10.0,
            market_price=100.0,
        )

        self.assertFalse(decision.approved)
        self.assertIn("insufficient cash", decision.reason.lower())

    # GIVEN daily losses at the configured limit WHEN risk evaluates THEN new trades are blocked.
    async def test_given_daily_loss_limit_reached_when_risk_evaluates_then_trade_is_rejected(self) -> None:
        engine = RiskEngine()
        engine.record_trade_result(-25.0)
        idea = make_trade_idea(direction=Direction.LONG)

        decision = await engine.evaluate_trade(
            idea,
            open_positions=[],
            available_cash=500.0,
            market_price=100.0,
        )

        self.assertFalse(decision.approved)
        self.assertIn("daily loss", decision.reason.lower())


    # GIVEN daily losses that are just below the limit WHEN a new trade would push
    # the projected total over the limit THEN the trade is rejected pre-emptively.
    async def test_given_near_limit_daily_loss_when_new_trade_would_exceed_then_trade_is_rejected(self) -> None:
        engine = RiskEngine()
        # Record a loss just below the daily limit (default 5% of 500 = £25)
        # The next trade has an estimated loss that would push the total over.
        daily_limit = engine.current_equity * 0.05   # 25.0
        engine.daily_loss = daily_limit - 1.0        # 24.0 — just under

        # Position sizing: 20% of 500 = £100 size; stop_loss 5% → estimated loss £5
        # 24 + 5 = 29 > 25 → should be rejected
        idea = make_trade_idea(direction=Direction.LONG, sizing=0.20)

        decision = await engine.evaluate_trade(
            idea,
            open_positions=[],
            available_cash=500.0,
            market_price=100.0,
        )

        self.assertFalse(decision.approved)
        self.assertIn("daily loss", decision.reason.lower())

    # GIVEN a minimum liquidity rule WHEN supplied 24h volume is below the threshold
    # THEN risk rejects the trade before approval.
    async def test_given_low_market_volume_when_risk_evaluates_then_trade_is_rejected(self) -> None:
        original_min_volume = risk_module.MIN_24H_VOLUME
        risk_module.MIN_24H_VOLUME = 1_000.0
        try:
            engine = RiskEngine()
            idea = make_trade_idea(direction=Direction.LONG)

            decision = await engine.evaluate_trade(
                idea,
                open_positions=[],
                available_cash=500.0,
                market_price=100.0,
                market_volume_24h=50.0,
            )
        finally:
            risk_module.MIN_24H_VOLUME = original_min_volume

        self.assertFalse(decision.approved)
        self.assertIn("liquidity", decision.reason.lower())


if __name__ == "__main__":
    unittest.main()
