"""BDD coverage for the basic strategy signal gates."""
import unittest

from bdd_helpers import BACKEND_DIR  # noqa: F401
from domain.models import Direction
from strategy.basic_strategy import BasicStrategy
from strategy.indicator_only_strategy import IndicatorOnlyStrategy


class BasicStrategyBDDTests(unittest.IsolatedAsyncioTestCase):
    # GIVEN the built-in strategies WHEN they are created THEN their public IDs match the UI labels.
    async def test_given_builtin_strategies_when_created_then_expected_strategy_ids_are_used(self) -> None:
        combined = BasicStrategy()
        indicator_only = IndicatorOnlyStrategy()

        self.assertEqual(combined.strategy_id, "combined")
        self.assertEqual(indicator_only.strategy_id, "indicator_only")

    # GIVEN momentum below the noise threshold WHEN strategy evaluates THEN no idea is emitted.
    async def test_given_small_momentum_when_strategy_evaluates_then_no_signal_is_generated(self) -> None:
        strategy = BasicStrategy()
        market_data = {"BTC/EUR": {"price": 100.10, "previous_price": 100.0, "indicators": {}}}

        ideas = await strategy.evaluate(market_data, news_signals=[])

        self.assertEqual(ideas, [])

    # GIVEN overbought RSI WHEN a long signal forms THEN the hard filter blocks the trade.
    async def test_given_overbought_rsi_when_long_signal_forms_then_no_signal_is_generated(self) -> None:
        strategy = BasicStrategy()
        market_data = {
            "BTC/EUR": {
                "price": 101.0,
                "previous_price": 100.0,
                "indicators": {"rsi_14": 85.0},
            }
        }

        ideas = await strategy.evaluate(market_data, news_signals=[])

        self.assertEqual(ideas, [])

    # GIVEN fewer than six usable indicators WHEN strategy evaluates THEN no trade signal is generated.
    async def test_given_fewer_than_six_indicators_when_strategy_evaluates_then_no_signal_is_generated(self) -> None:
        strategy = BasicStrategy()
        market_data = {
            "BTC/EUR": {
                "price": 102.0,
                "previous_price": 100.0,
                "indicators": {
                    "rsi_14": 35.0,
                    "ema_cross": "bullish",
                    "bb": {"position": 25.0},
                    "macd": {"bias": "bullish"},
                    "stoch": {"k": 15.0, "d": 20.0},
                },
            }
        }

        ideas = await strategy.evaluate(market_data, news_signals=[])

        self.assertEqual(ideas, [])

    # GIVEN positive momentum and supportive indicators WHEN strategy evaluates THEN it emits a long idea.
    async def test_given_supportive_bullish_context_when_strategy_evaluates_then_long_signal_is_generated(self) -> None:
        strategy = BasicStrategy()
        market_data = {
            "BTC/EUR": {
                "price": 102.0,
                "previous_price": 100.0,
                "indicators": {
                    "rsi_14": 35.0,
                    "ema_cross": "bullish",
                    "bb": {"position": 25.0},
                    "macd": {"bias": "bullish", "signal_bias": "bullish", "histogram": 0.5},
                    "stoch": {"k": 15.0, "d": 20.0},
                    "williams_r": -85.0,
                    "price_changes": {"5m": 1.2, "15m": 2.4},
                    "atr_pct": 0.5,
                },
            }
        }
        news = [{"asset_mentions": ["BTC"], "headline_sentiment": 0.4}]

        ideas = await strategy.evaluate(market_data, news_signals=news)

        self.assertEqual(len(ideas), 1)
        idea = ideas[0]
        self.assertEqual(idea.market, "BTC/EUR")
        self.assertEqual(idea.direction, Direction.LONG)
        self.assertGreaterEqual(idea.supporting_signals["indicator_votes"], 1)
        self.assertGreaterEqual(idea.confidence, 0.20)
        self.assertLessEqual(idea.confidence, 0.95)

    # GIVEN a bullish lower-timeframe signal but bearish higher-timeframe EMA
    # WHEN strategy evaluates THEN the confirmation gate blocks the trade.
    async def test_given_opposing_higher_timeframe_when_strategy_evaluates_then_signal_is_blocked(self) -> None:
        strategy = BasicStrategy()
        market_data = {
            "BTC/EUR": {
                "price": 102.0,
                "previous_price": 100.0,
                "higher_timeframe": {"ema_cross": "bearish"},
                "indicators": {
                    "rsi_14": 35.0,
                    "ema_cross": "bullish",
                    "bb": {"position": 25.0},
                    "macd": {"bias": "bullish", "signal_bias": "bullish", "histogram": 0.5},
                    "stoch": {"k": 15.0, "d": 20.0},
                    "williams_r": -85.0,
                    "price_changes": {"5m": 1.2, "15m": 2.4},
                    "atr_pct": 0.5,
                },
            }
        }

        ideas = await strategy.evaluate(market_data, news_signals=[])

        self.assertEqual(ideas, [])

    # GIVEN supportive LLM sentiment WHEN a signal is generated THEN confidence is increased.
    async def test_given_supportive_llm_sentiment_when_strategy_evaluates_then_confidence_is_boosted(self) -> None:
        strategy = BasicStrategy()
        base_context = {
            "price": 102.0,
            "previous_price": 100.0,
            "indicators": {
                "rsi_14": 35.0,
                "ema_cross": "bullish",
                "bb": {"position": 25.0},
                "macd": {"bias": "bullish", "signal_bias": "bullish", "histogram": 0.5},
                "stoch": {"k": 15.0, "d": 20.0},
                "williams_r": -85.0,
                "price_changes": {"5m": 1.2, "15m": 2.4},
                "atr_pct": 0.5,
            },
        }

        neutral = await strategy.evaluate({"BTC/EUR": base_context}, news_signals=[])
        supportive = await strategy.evaluate({
            "BTC/EUR": {**base_context, "llm_sentiment": 0.7},
        }, news_signals=[])

        self.assertGreater(supportive[0].confidence, neutral[0].confidence)

    # GIVEN indicator-only mode and hostile non-indicator context WHEN a signal is generated
    # THEN the strategy uses technical indicators only for confidence.
    async def test_given_indicator_only_strategy_when_context_is_hostile_then_non_indicator_sentiment_is_ignored(
        self,
    ) -> None:
        strategy = IndicatorOnlyStrategy()
        base_context = {
            "price": 102.0,
            "previous_price": 100.0,
            "llm_sentiment": -0.8,
            "indicators": {
                "rsi_14": 35.0,
                "ema_cross": "bullish",
                "bb": {"position": 25.0},
                "macd": {"bias": "bullish", "signal_bias": "bullish", "histogram": 0.5},
                "stoch": {"k": 15.0, "d": 20.0},
                "williams_r": -85.0,
                "price_changes": {"5m": 1.2, "15m": 2.4},
                "atr_pct": 0.5,
            },
        }
        hostile_news = [{"asset_mentions": ["BTC"], "headline_sentiment": -0.9}]

        ideas = await strategy.evaluate({"BTC/EUR": base_context}, news_signals=hostile_news)

        self.assertEqual(len(ideas), 1)
        idea = ideas[0]
        self.assertEqual(idea.strategy_id, "indicator_only")
        self.assertEqual(idea.direction, Direction.LONG)
        self.assertEqual(idea.supporting_signals["news_sentiment"], 0.0)
        self.assertEqual(idea.supporting_signals["llm_sentiment"], 0.0)
        self.assertGreaterEqual(idea.supporting_signals["indicators_available"], 6)

    # GIVEN opposing indicators WHEN consensus is available THEN no idea is emitted.
    async def test_given_opposing_consensus_when_strategy_evaluates_then_signal_is_blocked(self) -> None:
        strategy = BasicStrategy()
        market_data = {
            "ETH/EUR": {
                "price": 102.0,
                "previous_price": 100.0,
                "indicators": {
                    "rsi_14": 65.0,
                    "ema_cross": "bearish",
                    "bb": {"position": 80.0},
                    "macd": {"bias": "bearish", "signal_bias": "bearish"},
                },
            }
        }

        ideas = await strategy.evaluate(market_data, news_signals=[])

        self.assertEqual(ideas, [])


if __name__ == "__main__":
    unittest.main()
