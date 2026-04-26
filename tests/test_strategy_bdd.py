"""BDD coverage for the basic strategy signal gates."""
import unittest

from bdd_helpers import BACKEND_DIR  # noqa: F401
from analysis.indicators import compute_all
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

    # GIVEN fewer than five usable indicators WHEN strategy evaluates THEN no trade signal is generated.
    async def test_given_fewer_than_five_indicators_when_strategy_evaluates_then_no_signal_is_generated(
        self,
    ) -> None:
        strategy = BasicStrategy()
        market_data = {
            "BTC/EUR": {
                "price": 102.0,
                "previous_price": 100.0,
                "indicators": {
                    "rsi_14": 35.0,          # supports LONG (1)
                    "ema_cross": "bullish",  # supports LONG (2)
                    "bb": {"position": 50.0},  # neutral 30-70 — no vote
                    "macd": {"bias": "bullish"},  # supports LONG (3) — no signal_bias key
                    "stoch": {"k": 15.0, "d": 20.0},  # supports LONG (4)
                },
            }
        }

        ideas = await strategy.evaluate(market_data, news_signals=[])

        self.assertEqual(ideas, [])

    # BUG-020: GIVEN five trend indicators agree in a strongly trending market
    # WHEN the contrarian indicators (RSI, BB, Stoch, WR) all oppose the direction
    # THEN a signal IS generated — five agreeing trend indicators is the minimum threshold.
    # (Previously blocked because MIN_INDICATORS_FOR_SIGNAL was 6, requiring a contrarian
    # indicator to also agree, which is impossible in a pure trend.)
    async def test_given_five_trend_indicators_in_trending_market_when_strategy_evaluates_then_signal_is_generated(self) -> None:
        strategy = BasicStrategy()
        market_data = {
            "AXS/EUR": {
                "price": 102.0,
                "previous_price": 100.0,
                "indicators": {
                    "rsi_14": 76.6,        # > 60 — opposes LONG (contrarian signal)
                    "ema_cross": "bullish",  # supports LONG ✓
                    "bb": {"position": 80.0},  # > 70 — opposes LONG (contrarian)
                    "macd": {"bias": "bullish", "signal_bias": "bullish", "histogram": 0.5},  # ✓ ✓
                    "stoch": {"k": 93.2, "d": 90.0},  # > 80 — opposes LONG (contrarian)
                    "williams_r": -0.0,    # >= -20 — opposes LONG (contrarian)
                    "price_changes": {"5m": 1.2, "15m": 2.4},  # ✓ ✓
                    "atr_pct": 0.5,
                },
            }
        }

        ideas = await strategy.evaluate(market_data, news_signals=[])

        # EMA, MACD_bias, MACD_sig, 5m, 15m = 5 supporting → meets MIN_INDICATORS_FOR_SIGNAL
        self.assertEqual(len(ideas), 1, "Five trend indicators in agreement should produce a signal")
        self.assertEqual(ideas[0].supporting_signals["indicators_supporting"], 5)
        self.assertEqual(ideas[0].supporting_signals["indicators_opposing"], 4)

    # GIVEN exactly six indicators agree WHEN more indicators oppose THEN the signal still passes.
    async def test_given_exactly_six_supporting_indicators_when_strategy_evaluates_then_signal_can_pass(self) -> None:
        strategy = BasicStrategy()
        market_data = {
            "AXS/EUR": {
                "price": 102.0,
                "previous_price": 100.0,
                "indicators": {
                    "rsi_14": 38.0,
                    "ema_cross": "bullish",
                    "bb": {"position": 80.0},
                    "macd": {"bias": "bullish", "signal_bias": "bullish", "histogram": 0.5},
                    "stoch": {"k": 90.0, "d": 88.0},
                    "williams_r": -10.0,
                    "price_changes": {"5m": 1.2, "15m": 2.4},
                    "atr_pct": 0.5,
                },
            }
        }

        ideas = await strategy.evaluate(market_data, news_signals=[])

        self.assertEqual(len(ideas), 1)
        self.assertEqual(ideas[0].supporting_signals["indicators_supporting"], 6)
        self.assertEqual(ideas[0].supporting_signals["indicators_opposing"], 3)

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
        self.assertGreaterEqual(idea.supporting_signals["indicators_supporting"], 6)
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


    # BUG-016: GIVEN fewer than 34 price ticks in history WHEN strategy evaluates
    # THEN no trade idea is emitted because MACD and Bollinger are absent, leaving
    # fewer than MIN_INDICATORS_FOR_SIGNAL usable indicators.
    async def test_given_short_price_history_when_strategy_evaluates_then_no_signal_due_to_insufficient_indicators(
        self,
    ) -> None:
        strategy = BasicStrategy()
        # 20 ticks: RSI (needs 15) and EMA (needs 9/21) are present, but MACD (needs 34) is absent.
        # With only EMA, RSI, price_changes, and stochastic available (<= 5 indicators), signal
        # should be blocked even with strong momentum.
        prices = [100.0 + i * 0.5 for i in range(20)]
        indicators = compute_all(prices, tick_seconds=30)
        market_data = {
            "BTC/EUR": {
                "price": prices[-1],
                "previous_price": prices[0],
                "indicators": indicators,
            }
        }

        ideas = await strategy.evaluate(market_data, news_signals=[])

        self.assertNotIn("macd", indicators, "MACD should be absent with only 20 ticks")
        self.assertEqual(ideas, [], "No signal should fire when MACD is absent — too few indicators")

    # BUG-016: GIVEN the warm-up constant WHEN inspected THEN it is at least the MACD minimum bar count.
    def test_given_warmup_constant_when_inspected_then_it_meets_macd_minimum(self) -> None:
        import backend.main as bot_main  # noqa: PLC0415

        macd_minimum = 34  # slow_ema=26 + signal=9 - 1
        self.assertGreaterEqual(
            bot_main._LOOKBACK_TICKS,
            macd_minimum,
            "_LOOKBACK_TICKS must be >= 34 so all indicators are computable before strategy fires",
        )

    # GIVEN the trade warmup constant WHEN inspected THEN it is at least 1 tick and at most
    # _LOOKBACK_TICKS, ensuring the warmup completes before indicator-based strategies fire.
    def test_given_trade_warmup_constant_when_inspected_then_it_is_a_positive_tick_count(self) -> None:
        import backend.main as bot_main  # noqa: PLC0415

        self.assertGreaterEqual(
            bot_main._TRADE_WARMUP_TICKS,
            1,
            "_TRADE_WARMUP_TICKS must be at least 1 tick",
        )
        self.assertLessEqual(
            bot_main._TRADE_WARMUP_TICKS,
            bot_main._LOOKBACK_TICKS,
            "_TRADE_WARMUP_TICKS should not exceed _LOOKBACK_TICKS — indicator warmup already covers that window",
        )

    # BUG-028: GIVEN legacy LLM briefing outlook data is a string WHEN the strategy loop reads sentiment
    # THEN score extraction falls back to neutral instead of crashing.
    def test_given_string_llm_outlook_when_score_extracted_then_neutral_score_is_used(self) -> None:
        import backend.main as bot_main  # noqa: PLC0415
        from llm.analyser import MarketBriefing  # noqa: PLC0415

        briefing = MarketBriefing(
            market_outlooks={"BTC/EUR": "bullish"},
            overall_sentiment=0.2,
            key_insight="Legacy malformed context",
            article_count=1,
        )

        score = bot_main._briefing_sentiment_for_market(briefing, "BTC/EUR")

        self.assertEqual(score, 0.0)

    # BUG-024: GIVEN a market snapshot with previous_price = 0.0
    # WHEN the strategy evaluates THEN it returns None instead of raising ZeroDivisionError.
    async def test_given_zero_previous_price_when_strategy_evaluates_then_no_crash(self) -> None:
        strategy = BasicStrategy()
        market_data = {"BTC/EUR": {"price": 50000.0, "previous_price": 0.0, "indicators": {}}}

        ideas = await strategy.evaluate(market_data, news_signals=[])

        self.assertEqual(ideas, [])

    # QUALITY-004: GIVEN price history where the prior tick is 10% above current
    # WHEN the strategy receives previous_price = hist[-2] (prior tick, not 17-min ref)
    # THEN momentum reflects the 30-second drop and a SHORT signal can form.
    async def test_given_prior_tick_as_previous_price_when_large_drop_then_momentum_is_30s_change(
        self,
    ) -> None:
        strategy = BasicStrategy()
        # previous_price = 110.0, current = 100.0 → -9.1% momentum (well above 0.2% threshold)
        market_data = {
            "BTC/EUR": {
                "price": 100.0,
                "previous_price": 110.0,  # prior tick — genuine 30-second drop
                "indicators": {
                    "rsi_14": 75.0,           # > 60 bearish threshold — supports SHORT
                    "ema_cross": "bearish",   # supports SHORT
                    "bb": {"position": 85.0}, # > 70 bearish threshold — supports SHORT
                    "macd": {"bias": "bearish", "signal_bias": "bearish", "histogram": -0.5},
                    "stoch": {"k": 90.0, "d": 88.0},   # > 80 — supports SHORT
                    "williams_r": -5.0,       # > -20 bearish threshold — supports SHORT
                    "price_changes": {"5m": -3.0, "15m": -5.0},
                    "atr_pct": 0.5,
                },
            }
        }

        ideas = await strategy.evaluate(market_data, news_signals=[])

        self.assertEqual(len(ideas), 1)
        self.assertEqual(ideas[0].direction.value, "short")


if __name__ == "__main__":
    unittest.main()
