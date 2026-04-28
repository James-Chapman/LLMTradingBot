"""
BDD tests for strategy/basic_strategy.py (T2.Q7.4).

Covers: momentum gate, HTF EMA filter, RSI/BB hard blocks,
insufficient-indicator gate, and happy paths for LONG and SHORT.
"""

import pytest
from strategy.basic_strategy import BasicStrategy
from strategy.constants import MOMENTUM_THRESHOLD


# ── Helpers ───────────────────────────────────────────────────────────────────

def _bullish_data(price: float = 100.5, prev: float = 100.0) -> dict:
    """Market data with strong bullish indicators — should produce a LONG idea."""
    return {
        "price": price,
        "previous_price": prev,
        "indicators": {
            "rsi_14": 30.0,
            "ema_cross": "bullish",
            "bb": {"position": 20.0},
            "macd": {"bias": "bullish", "signal_bias": "bullish"},
            "stoch": {"k": 15.0},
            "williams_r": -85.0,
            "price_changes": {"5m": 0.003, "15m": 0.002},
        },
    }


def _bearish_data(price: float = 99.5, prev: float = 100.0) -> dict:
    """Market data with strong bearish indicators — should produce a SHORT idea."""
    return {
        "price": price,
        "previous_price": prev,
        "indicators": {
            "rsi_14": 70.0,
            "ema_cross": "bearish",
            "bb": {"position": 80.0},
            "macd": {"bias": "bearish", "signal_bias": "bearish"},
            "stoch": {"k": 85.0},
            "williams_r": -15.0,
            "price_changes": {"5m": -0.003, "15m": -0.002},
        },
    }


# ── Momentum gate ─────────────────────────────────────────────────────────────

class TestMomentumGateBDD:
    async def test_given_momentum_below_threshold_when_evaluated_then_no_idea(self):
        """
        GIVEN price moves only 0.1% (below 0.2% threshold),
        WHEN BasicStrategy evaluates the market,
        THEN no trade idea is produced.
        """
        strategy = BasicStrategy()
        data = _bullish_data(price=100.1, prev=100.0)  # 0.1% move
        ideas = await strategy.evaluate({"BTC/EUR": data})
        assert ideas == []

    async def test_given_momentum_above_threshold_when_evaluated_then_idea_produced(self):
        """
        GIVEN price moves 0.5% (above 0.2% threshold) with bullish indicators,
        WHEN BasicStrategy evaluates the market,
        THEN a LONG trade idea is produced.
        """
        strategy = BasicStrategy()
        ideas = await strategy.evaluate({"BTC/EUR": _bullish_data()})
        assert len(ideas) == 1
        assert ideas[0].direction.value == "long"


# ── RSI hard block ────────────────────────────────────────────────────────────

class TestRSIHardBlockBDD:
    async def test_given_overbought_rsi_when_long_momentum_then_no_idea(self):
        """
        GIVEN RSI >= 80 (overbought) with positive momentum,
        WHEN BasicStrategy evaluates the market,
        THEN no trade idea is produced (RSI hard block).
        """
        strategy = BasicStrategy()
        data = _bullish_data()
        data["indicators"]["rsi_14"] = 82.0  # >= RSI_LONG_BLOCK (80)
        ideas = await strategy.evaluate({"BTC/EUR": data})
        assert ideas == []

    async def test_given_oversold_rsi_when_short_momentum_then_no_idea(self):
        """
        GIVEN RSI <= 20 (oversold) with negative momentum,
        WHEN BasicStrategy evaluates the market,
        THEN no trade idea is produced (RSI hard block on short).
        """
        strategy = BasicStrategy()
        data = _bearish_data()
        data["indicators"]["rsi_14"] = 18.0  # <= RSI_SHORT_BLOCK (20)
        ideas = await strategy.evaluate({"BTC/EUR": data})
        assert ideas == []

    async def test_given_rsi_below_block_when_long_momentum_then_idea_produced(self):
        """
        GIVEN RSI = 30 (below the 80 block) with positive momentum and indicators,
        WHEN BasicStrategy evaluates the market,
        THEN a LONG trade idea is produced.
        """
        strategy = BasicStrategy()
        ideas = await strategy.evaluate({"BTC/EUR": _bullish_data()})
        assert len(ideas) == 1


# ── Bollinger Bands hard block ────────────────────────────────────────────────

class TestBBHardBlockBDD:
    async def test_given_bb_at_upper_extreme_when_long_momentum_then_no_idea(self):
        """
        GIVEN BB position >= 95 with positive momentum,
        WHEN BasicStrategy evaluates the market,
        THEN no trade idea is produced (BB upper extreme block).
        """
        strategy = BasicStrategy()
        data = _bullish_data()
        data["indicators"]["bb"]["position"] = 97.0  # >= BB_LONG_BLOCK (95)
        ideas = await strategy.evaluate({"BTC/EUR": data})
        assert ideas == []

    async def test_given_bb_at_lower_extreme_when_short_momentum_then_no_idea(self):
        """
        GIVEN BB position <= 5 with negative momentum,
        WHEN BasicStrategy evaluates the market,
        THEN no trade idea is produced (BB lower extreme block).
        """
        strategy = BasicStrategy()
        data = _bearish_data()
        data["indicators"]["bb"]["position"] = 3.0  # <= BB_SHORT_BLOCK (5)
        ideas = await strategy.evaluate({"BTC/EUR": data})
        assert ideas == []


# ── Insufficient indicators gate ──────────────────────────────────────────────

class TestInsufficientIndicatorsBDD:
    async def test_given_only_4_supporting_indicators_when_evaluated_then_no_idea(self):
        """
        GIVEN only 4 supporting indicators (below the 5-indicator minimum),
        WHEN BasicStrategy evaluates the market,
        THEN no trade idea is produced.
        """
        strategy = BasicStrategy()
        data = {
            "price": 100.5,
            "previous_price": 100.0,
            "indicators": {
                "rsi_14": 30.0,           # bullish → 1
                "ema_cross": "bullish",   # bullish → 2
                "bb": {"position": 20.0}, # bullish → 3
                "macd": {"bias": "bullish"},  # bullish → 4
                # no signal_bias, stoch, williams_r, or price_changes
            },
        }
        ideas = await strategy.evaluate({"BTC/EUR": data})
        assert ideas == []

    async def test_given_5_supporting_indicators_when_evaluated_then_idea_produced(self):
        """
        GIVEN exactly 5 supporting indicators (at the minimum threshold),
        WHEN BasicStrategy evaluates the market,
        THEN a trade idea is produced.
        """
        strategy = BasicStrategy()
        data = {
            "price": 100.5,
            "previous_price": 100.0,
            "indicators": {
                "rsi_14": 30.0,
                "ema_cross": "bullish",
                "bb": {"position": 20.0},
                "macd": {"bias": "bullish", "signal_bias": "bullish"},
                "stoch": {"k": 15.0},     # 5th supporting
            },
        }
        ideas = await strategy.evaluate({"BTC/EUR": data})
        assert len(ideas) == 1


# ── HTF EMA filter ────────────────────────────────────────────────────────────

class TestHTFEMAFilterBDD:
    async def test_given_bearish_htf_ema_when_long_signal_then_blocked(self):
        """
        GIVEN a bearish higher-timeframe EMA cross with a LONG momentum signal,
        WHEN BasicStrategy evaluates the market,
        THEN no trade idea is produced (HTF EMA filter).
        """
        strategy = BasicStrategy()
        data = _bullish_data()
        data["higher_timeframe"] = {"ema_cross": "bearish"}
        ideas = await strategy.evaluate({"BTC/EUR": data})
        assert ideas == []

    async def test_given_bullish_htf_ema_when_long_signal_then_not_blocked(self):
        """
        GIVEN a bullish higher-timeframe EMA cross matching the LONG signal,
        WHEN BasicStrategy evaluates the market,
        THEN the signal is not blocked by HTF filter.
        """
        strategy = BasicStrategy()
        data = _bullish_data()
        data["higher_timeframe"] = {"ema_cross": "bullish"}
        ideas = await strategy.evaluate({"BTC/EUR": data})
        assert len(ideas) == 1

    async def test_given_neutral_htf_ema_when_long_signal_then_not_blocked(self):
        """
        GIVEN a neutral higher-timeframe EMA cross,
        WHEN BasicStrategy evaluates the market,
        THEN the signal is not blocked by HTF filter.
        """
        strategy = BasicStrategy()
        data = _bullish_data()
        data["higher_timeframe"] = {"ema_cross": "neutral"}
        ideas = await strategy.evaluate({"BTC/EUR": data})
        assert len(ideas) == 1


# ── SHORT happy path ──────────────────────────────────────────────────────────

class TestShortHappyPathBDD:
    async def test_given_bearish_indicators_with_negative_momentum_when_evaluated_then_short_idea(self):
        """
        GIVEN negative price momentum with 9 bearish indicator votes,
        WHEN BasicStrategy evaluates the market,
        THEN a SHORT trade idea is produced.
        """
        strategy = BasicStrategy()
        ideas = await strategy.evaluate({"BTC/EUR": _bearish_data()})
        assert len(ideas) == 1
        assert ideas[0].direction.value == "short"


# ── Missing price data ────────────────────────────────────────────────────────

class TestMissingPriceDataBDD:
    async def test_given_missing_previous_price_when_evaluated_then_no_idea(self):
        """
        GIVEN market data with no previous_price key,
        WHEN BasicStrategy evaluates the market,
        THEN no trade idea is produced.
        """
        strategy = BasicStrategy()
        data = {"price": 100.5, "indicators": {}}
        ideas = await strategy.evaluate({"BTC/EUR": data})
        assert ideas == []
