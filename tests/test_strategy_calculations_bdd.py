"""BDD coverage for shared strategy calculation formulas."""

import unittest
from pathlib import Path

from bdd_helpers import BACKEND_DIR  # noqa: F401
from strategy.calculations import (
    calculate_confidence,
    calculate_momentum,
    calculate_position_sizing,
    check_hard_filters,
    check_higher_timeframe_alignment,
    evaluate_momentum_gate,
    vote_indicators,
)


ROOT_DIR = Path(__file__).resolve().parents[1]


class StrategyCalculationsBDDTests(unittest.TestCase):
    # GIVEN current and previous prices WHEN momentum is calculated
    # THEN sparse data falls back to the shared decimal momentum formula.
    def test_given_prices_when_momentum_calculated_then_decimal_change_is_returned(self) -> None:
        self.assertEqual(calculate_momentum(102.0, 100.0), 0.02)
        self.assertAlmostEqual(calculate_momentum(100.0, 110.0), -0.090909, places=6)
        self.assertIsNone(calculate_momentum(100.0, 0.0))

    # GIVEN enough recent prices WHEN momentum is calculated
    # THEN averaged windows are used instead of two isolated sampled points.
    def test_given_price_history_when_momentum_calculated_then_window_average_change_is_returned(self) -> None:
        history = [100.0, 101.0, 99.0, 100.5, 99.5, 101.0, 102.0, 100.0, 101.5, 100.5]

        momentum = calculate_momentum(
            current_price=103.0,
            previous_price=100.0,
            price_history=history,
        )

        self.assertAlmostEqual(momentum or 0.0, 0.01, places=6)
        self.assertNotAlmostEqual(momentum or 0.0, 0.03, places=6)

    # GIVEN too few history points WHEN momentum is calculated
    # THEN the prior two-point behavior remains available.
    def test_given_short_price_history_when_momentum_calculated_then_two_point_change_is_returned(self) -> None:
        momentum = calculate_momentum(
            current_price=103.0,
            previous_price=100.0,
            price_history=[100.0, 101.0, 103.0],
        )

        self.assertEqual(momentum, 0.03)

    # GIVEN ATR volatility is present WHEN the momentum gate is evaluated
    # THEN the effective threshold is the larger of ATR noise and the fixed floor.
    def test_given_atr_when_momentum_gate_evaluated_then_larger_threshold_is_used(self) -> None:
        low_atr = evaluate_momentum_gate(momentum=0.003, atr_pct=0.05)
        high_atr = evaluate_momentum_gate(momentum=0.003, atr_pct=0.5)

        self.assertTrue(low_atr.passes)
        self.assertEqual(low_atr.effective_threshold, 0.002)
        self.assertFalse(high_atr.passes)
        self.assertEqual(high_atr.effective_threshold, 0.005)

    # GIVEN extreme RSI or Bollinger readings WHEN hard filters are checked
    # THEN trades into reversal-prone extremes are blocked consistently.
    def test_given_extreme_indicators_when_hard_filters_checked_then_block_reason_is_returned(self) -> None:
        long_block = check_hard_filters(is_long=True, indicators={"rsi_14": 82.0})
        short_block = check_hard_filters(is_long=False, indicators={"bb": {"position": 3.0}})
        clean = check_hard_filters(is_long=True, indicators={"rsi_14": 35.0, "bb": {"position": 25.0}})

        self.assertTrue(long_block.blocked)
        self.assertIn("RSI", long_block.reason)
        self.assertTrue(short_block.blocked)
        self.assertIn("BB", short_block.reason)
        self.assertFalse(clean.blocked)

    # GIVEN a higher timeframe EMA opinion WHEN alignment is checked
    # THEN opposing trend context blocks while neutral context passes.
    def test_given_higher_timeframe_ema_when_checked_then_opposition_blocks_only(self) -> None:
        self.assertFalse(check_higher_timeframe_alignment(True, {"ema_cross": "bearish"}).allowed)
        self.assertTrue(check_higher_timeframe_alignment(True, {"ema_cross": "neutral"}).allowed)
        self.assertTrue(check_higher_timeframe_alignment(False, {"ema_cross": "bearish"}).allowed)

    # GIVEN all bullish indicators WHEN indicator votes are counted
    # THEN the same vote summary is available to every strategy.
    def test_given_bullish_indicators_when_votes_counted_then_summary_matches_strategy_rules(self) -> None:
        indicators = {
            "rsi_14": 35.0,
            "ema_cross": "bullish",
            "bb": {"position": 25.0},
            "macd": {"bias": "bullish", "signal_bias": "bullish", "histogram": 0.5},
            "stoch": {"k": 15.0, "d": 20.0},
            "williams_r": -85.0,
            "price_changes": {"5m": 1.2, "15m": 2.4},
        }

        summary = vote_indicators(is_long=True, indicators=indicators)

        self.assertEqual(summary.votes, 9)
        self.assertEqual(summary.available, 9)
        self.assertEqual(summary.supporting, 9)
        self.assertEqual(summary.opposing, 0)
        self.assertEqual(summary.values["macd_signal_bias"], "bullish")

    # GIVEN momentum, votes, and ATR WHEN confidence is calculated
    # THEN the shared capped confidence components are returned.
    def test_given_signal_components_when_confidence_calculated_then_shared_caps_are_used(self) -> None:
        breakdown = calculate_confidence(momentum=0.02, votes=9, available=9, atr_pct=1.5)

        self.assertEqual(breakdown.base, 0.50)
        self.assertEqual(breakdown.indicator_bonus, 0.40)
        self.assertEqual(breakdown.atr_penalty, -0.025)
        self.assertEqual(breakdown.confidence, 0.875)

    # GIVEN volatility changes WHEN position sizing is calculated
    # THEN the same bounded sizing formula is used by all signal strategies.
    def test_given_atr_when_position_sizing_calculated_then_size_is_bounded(self) -> None:
        self.assertEqual(calculate_position_sizing(atr_pct=0.5), 0.20)
        self.assertEqual(calculate_position_sizing(atr_pct=2.0), 0.10)
        self.assertEqual(calculate_position_sizing(atr_pct=20.0), 0.05)

    # GIVEN strategy source files WHEN inspected
    # THEN overlapping formulas come from strategy.calculations.
    def test_given_strategy_sources_when_inspected_then_shared_calculations_are_used(self) -> None:
        strategy_files = [
            ROOT_DIR / "backend" / "strategy" / "basic_strategy.py",
            ROOT_DIR / "backend" / "strategy" / "basic_and_llm_strategy.py",
            ROOT_DIR / "backend" / "strategy" / "llm_only_strategy.py",
        ]

        for path in strategy_files:
            source = path.read_text(encoding="utf-8")
            self.assertIn("strategy.calculations", source)

        for path in strategy_files[:2]:
            source = path.read_text(encoding="utf-8")
            self.assertNotIn("votes = 0", source)
            self.assertNotIn("base = min(abs(momentum) * 100", source)
            self.assertNotIn("POSITION_SIZING_PROPOSAL", source)


if __name__ == "__main__":
    unittest.main()
