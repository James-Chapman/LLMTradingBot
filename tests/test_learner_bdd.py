"""BDD coverage for the strategy performance learner."""
import unittest

from bdd_helpers import BACKEND_DIR  # noqa: F401
from strategy.learner import MIN_SAMPLES, PerformanceLearner


class PerformanceLearnerBDDTests(unittest.TestCase):
    # GIVEN fewer than the minimum outcomes WHEN confidence is adjusted THEN confidence is unchanged.
    def test_given_insufficient_history_when_adjusting_confidence_then_base_confidence_is_returned(self) -> None:
        learner = PerformanceLearner()
        for _ in range(MIN_SAMPLES - 1):
            learner.record_outcome("combined", "BTC/EUR", "long", pnl=10.0)

        adjusted = learner.adjust_confidence("combined", "BTC/EUR", "long", 0.60)

        self.assertEqual(adjusted, 0.60)

    # GIVEN repeated winning outcomes WHEN confidence is adjusted THEN the signal is boosted but capped.
    def test_given_winning_history_when_adjusting_confidence_then_confidence_is_boosted(self) -> None:
        learner = PerformanceLearner()
        for _ in range(MIN_SAMPLES):
            learner.record_outcome("combined", "BTC/EUR", "long", pnl=10.0)

        adjusted = learner.adjust_confidence("combined", "BTC/EUR", "long", 0.60)

        self.assertGreater(adjusted, 0.60)
        self.assertLessEqual(adjusted, 0.95)

    # GIVEN repeated losing outcomes WHEN confidence is adjusted THEN the signal is suppressed.
    def test_given_losing_history_when_adjusting_confidence_then_confidence_is_reduced(self) -> None:
        learner = PerformanceLearner()
        for _ in range(MIN_SAMPLES):
            learner.record_outcome("combined", "BTC/EUR", "long", pnl=-10.0)

        adjusted = learner.adjust_confidence("combined", "BTC/EUR", "long", 0.60)

        self.assertLess(adjusted, 0.60)

    # GIVEN two histories with the same win rate WHEN confidence is adjusted
    # THEN larger average wins versus losses produce the higher confidence.
    def test_given_same_win_rate_with_different_pnl_magnitude_then_quality_changes_adjustment(self) -> None:
        favorable = PerformanceLearner()
        unfavorable = PerformanceLearner()
        for pnl in (30.0, -10.0, 30.0, -10.0, 30.0, -10.0):
            favorable.record_outcome("combined", "BTC/EUR", "long", pnl=pnl)
        for pnl in (10.0, -30.0, 10.0, -30.0, 10.0, -30.0):
            unfavorable.record_outcome("combined", "BTC/EUR", "long", pnl=pnl)

        favorable_adjusted = favorable.adjust_confidence("combined", "BTC/EUR", "long", 0.60)
        unfavorable_adjusted = unfavorable.adjust_confidence("combined", "BTC/EUR", "long", 0.60)
        summary = favorable.summary()[0]

        self.assertGreater(favorable_adjusted, unfavorable_adjusted)
        self.assertEqual(summary["mean_win_pnl"], 30.0)
        self.assertEqual(summary["mean_loss_pnl"], -10.0)
        self.assertGreater(summary["quality_score"], 0.0)

    # NUMPY-003: GIVEN 20 outcome records WHEN rolling win rate (last 10) is computed
    # THEN the result equals the manually counted value.
    def test_given_20_outcomes_when_rolling_win_rate_last_10_computed_then_matches_manual_count(self) -> None:
        learner = PerformanceLearner()
        # Record 20 outcomes: 10 wins then 10 losses (newest = last recorded)
        for _ in range(10):
            learner.record_outcome("combined", "BTC/EUR", "long", pnl=-5.0)  # oldest 10 = losses
        for _ in range(10):
            learner.record_outcome("combined", "BTC/EUR", "long", pnl=10.0)  # newest 10 = wins

        result = learner.rolling_win_rate("combined", "BTC/EUR", "long", n=10)

        # Last 10 recorded are all wins
        self.assertAlmostEqual(result, 1.0, places=6)

    # NUMPY-003: GIVEN outcome P&L values WHEN percentiles are requested
    # THEN 25th, 50th, and 75th percentile outputs match reference values.
    def test_given_outcome_pnl_values_when_percentiles_requested_then_match_reference(self) -> None:
        import statistics
        learner = PerformanceLearner()
        pnl_values = [10.0, -5.0, 8.0, -3.0, 12.0, 6.0, -1.0, 15.0, -8.0, 4.0]
        for p in pnl_values:
            learner.record_outcome("combined", "BTC/EUR", "long", pnl=p)

        result = learner.pnl_percentiles("combined", "BTC/EUR", "long")
        quartiles = statistics.quantiles(pnl_values, n=4)

        self.assertIn("p25", result)
        self.assertIn("p50", result)
        self.assertIn("p75", result)
        self.assertAlmostEqual(result["mean"], statistics.mean(pnl_values), places=6)
        self.assertAlmostEqual(result["p25"], quartiles[0], places=6)
        self.assertAlmostEqual(result["p50"], statistics.median(pnl_values), places=6)
        self.assertAlmostEqual(result["p75"], quartiles[2], places=6)

    # NUMPY-003: GIVEN no outcomes for a key WHEN rolling win rate is requested
    # THEN zero is returned without raising.
    def test_given_no_outcomes_when_rolling_win_rate_requested_then_zero_returned(self) -> None:
        learner = PerformanceLearner()

        result = learner.rolling_win_rate("combined", "BTC/EUR", "long", n=10)

        self.assertEqual(result, 0.0)

    # NUMPY-003: GIVEN no outcomes for a key WHEN pnl_percentiles is requested
    # THEN empty dict is returned without raising.
    def test_given_no_outcomes_when_pnl_percentiles_requested_then_empty_dict_returned(self) -> None:
        learner = PerformanceLearner()

        result = learner.pnl_percentiles("combined", "BTC/EUR", "long")

        self.assertEqual(result, {})


if __name__ == "__main__":
    unittest.main()
