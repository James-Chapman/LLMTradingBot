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


if __name__ == "__main__":
    unittest.main()
