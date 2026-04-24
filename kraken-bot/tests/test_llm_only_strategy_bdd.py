"""BDD coverage for the LLM-only strategy."""
import asyncio
import time
import unittest

from bdd_helpers import BACKEND_DIR  # noqa: F401
from domain.models import Direction
from llm.analyser import LLMTradeRecommendation
from strategy.llm_only_strategy import LLMOnlyStrategy


class FakeAnalyser:
    def __init__(self, recommendation: LLMTradeRecommendation) -> None:
        self.recommendation = recommendation
        self.calls = []

    # Capture the LLM recommendation inputs for assertions.
    async def recommend_trade(self, **kwargs):
        self.calls.append(kwargs)
        return self.recommendation


class SlowFakeAnalyser:
    """Simulates a slow LLM (50 ms per call) to verify concurrent execution."""

    def __init__(self, delay: float = 0.05) -> None:
        self.delay = delay
        self.calls: list = []

    async def recommend_trade(self, **kwargs):
        await asyncio.sleep(self.delay)
        self.calls.append(kwargs["market"])
        return LLMTradeRecommendation(
            action="long", confidence=0.70, sentiment=0.5,
            reasoning="concurrent test", llm_used=True,
        )


class LLMOnlyStrategyBDDTests(unittest.IsolatedAsyncioTestCase):
    # GIVEN an LLM long recommendation WHEN the strategy evaluates THEN it emits a long trade idea.
    async def test_given_llm_long_recommendation_when_strategy_evaluates_then_long_signal_is_generated(self) -> None:
        strategy = LLMOnlyStrategy()
        indicators = {"rsi_14": 72.0, "ema_cross": "bearish"}
        analyser = FakeAnalyser(
            LLMTradeRecommendation(
                action="long",
                confidence=0.81,
                sentiment=0.65,
                reasoning="News and portfolio context justify a long",
                llm_used=True,
            )
        )
        market_data = {
            "BTC/EUR": {
                "price": 100.0,
                "previous_price": 99.0,
                "indicators": indicators,
            }
        }

        ideas = await strategy.evaluate(
            market_data,
            news_signals=[],
            analyser=analyser,
            equity=500.0,
            cash=500.0,
            open_positions=[],
        )

        self.assertEqual(len(ideas), 1)
        self.assertEqual(ideas[0].strategy_id, "llm")
        self.assertEqual(ideas[0].direction, Direction.LONG)
        self.assertEqual(ideas[0].confidence, 0.81)
        self.assertTrue(ideas[0].supporting_signals["llm_only"])
        self.assertEqual(analyser.calls[0]["indicators"], indicators)

    # GIVEN an LLM hold recommendation WHEN the strategy evaluates THEN no trade idea is emitted.
    async def test_given_llm_hold_recommendation_when_strategy_evaluates_then_no_signal_is_generated(self) -> None:
        strategy = LLMOnlyStrategy()
        analyser = FakeAnalyser(
            LLMTradeRecommendation(
                action="hold",
                confidence=0.90,
                sentiment=0.0,
                reasoning="No asymmetric opportunity",
                llm_used=True,
            )
        )
        market_data = {"ETH/EUR": {"price": 100.0, "previous_price": 100.0, "indicators": {}}}

        ideas = await strategy.evaluate(
            market_data,
            news_signals=[],
            analyser=analyser,
            equity=500.0,
            cash=500.0,
            open_positions=[],
        )

        self.assertEqual(ideas, [])

    # GIVEN the LLM is unavailable WHEN the strategy evaluates THEN no trade idea is emitted.
    async def test_given_llm_unavailable_when_strategy_evaluates_then_no_signal_is_generated(self) -> None:
        strategy = LLMOnlyStrategy()
        analyser = FakeAnalyser(
            LLMTradeRecommendation(
                action="long",
                confidence=0.90,
                sentiment=0.7,
                reasoning="Unavailable",
                llm_used=False,
            )
        )
        market_data = {"SOL/EUR": {"price": 100.0, "previous_price": 98.0, "indicators": {}}}

        ideas = await strategy.evaluate(
            market_data,
            news_signals=[],
            analyser=analyser,
            equity=500.0,
            cash=500.0,
            open_positions=[],
        )

        self.assertEqual(ideas, [])


    # GIVEN multiple markets with LLM long recommendations WHEN the strategy evaluates
    # THEN all markets are processed concurrently and one idea per actionable market is returned.
    async def test_given_multiple_markets_when_strategy_evaluates_then_all_processed_concurrently(self) -> None:
        markets = {f"COIN{i}/EUR": {"price": 100.0, "previous_price": 99.0, "indicators": {}}
                   for i in range(5)}
        analyser = SlowFakeAnalyser(delay=0.05)   # 50 ms per market
        strategy = LLMOnlyStrategy()

        start = time.monotonic()
        ideas = await strategy.evaluate(markets, news_signals=[], analyser=analyser,
                                        equity=500.0, cash=500.0, open_positions=[])
        elapsed = time.monotonic() - start

        # All 5 markets processed
        self.assertEqual(len(ideas), 5)
        # Concurrent execution: 5 × 50 ms sequential = 250 ms; concurrent < 150 ms
        self.assertLess(elapsed, 0.15,
                        f"Concurrent gather took {elapsed:.3f}s — likely still sequential")


if __name__ == "__main__":
    unittest.main()
