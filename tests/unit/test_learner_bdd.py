"""
BDD tests for strategy/learner.py (T2.Q7.5).

Covers: adjust_confidence before/after MIN_SAMPLES threshold,
record_outcome incrementally, load_from_outcomes seeding,
and quality_score direction (all-wins boost, all-losses suppress).
"""

import pytest
from types import SimpleNamespace
from strategy.learner import PerformanceLearner, MIN_SAMPLES


# ── Helpers ───────────────────────────────────────────────────────────────────

def _outcome(strategy_id: str, market: str, direction: str, pnl: float):
    """Fake SignalOutcomeModel-compatible object."""
    return SimpleNamespace(strategy_id=strategy_id, market=market, direction=direction, pnl=pnl)


def _seed_wins(learner: PerformanceLearner, n: int, strategy: str = "s", market: str = "BTC/EUR", direction: str = "long"):
    """Record n winning outcomes via record_outcome."""
    for _ in range(n):
        learner.record_outcome(strategy, market, direction, pnl=10.0)


def _seed_losses(learner: PerformanceLearner, n: int, strategy: str = "s", market: str = "BTC/EUR", direction: str = "long"):
    """Record n losing outcomes via record_outcome."""
    for _ in range(n):
        learner.record_outcome(strategy, market, direction, pnl=-10.0)


# ── adjust_confidence below threshold ────────────────────────────────────────

class TestAdjustConfidenceBelowThresholdBDD:
    def test_given_no_outcomes_when_adjust_confidence_called_then_returns_unchanged(self):
        """
        GIVEN a fresh learner with zero outcomes for the key,
        WHEN adjust_confidence is called,
        THEN the original confidence value is returned unchanged.
        """
        learner = PerformanceLearner()
        result = learner.adjust_confidence("s", "BTC/EUR", "long", 0.70)
        assert result == pytest.approx(0.70)

    def test_given_fewer_than_min_samples_when_adjust_confidence_called_then_returns_unchanged(self):
        """
        GIVEN MIN_SAMPLES - 1 outcomes recorded,
        WHEN adjust_confidence is called,
        THEN confidence is returned unchanged (threshold not reached).
        """
        learner = PerformanceLearner()
        _seed_wins(learner, MIN_SAMPLES - 1)
        result = learner.adjust_confidence("s", "BTC/EUR", "long", 0.70)
        assert result == pytest.approx(0.70)


# ── adjust_confidence at/above threshold ─────────────────────────────────────

class TestAdjustConfidenceAboveThresholdBDD:
    def test_given_all_winning_outcomes_when_adjust_confidence_called_then_confidence_boosted(self):
        """
        GIVEN MIN_SAMPLES winning outcomes (positive P&L),
        WHEN adjust_confidence is called with base confidence 0.60,
        THEN the returned confidence is higher than the base.
        """
        learner = PerformanceLearner()
        _seed_wins(learner, MIN_SAMPLES)
        result = learner.adjust_confidence("s", "BTC/EUR", "long", 0.60)
        assert result > 0.60

    def test_given_all_losing_outcomes_when_adjust_confidence_called_then_confidence_suppressed(self):
        """
        GIVEN MIN_SAMPLES losing outcomes (negative P&L),
        WHEN adjust_confidence is called with base confidence 0.70,
        THEN the returned confidence is lower than the base.
        """
        learner = PerformanceLearner()
        _seed_losses(learner, MIN_SAMPLES)
        result = learner.adjust_confidence("s", "BTC/EUR", "long", 0.70)
        assert result < 0.70

    def test_given_outcomes_above_threshold_when_adjust_confidence_called_then_capped_at_095(self):
        """
        GIVEN many winning outcomes pushing confidence high,
        WHEN adjust_confidence is called with base confidence 0.95,
        THEN the returned value does not exceed 0.95.
        """
        learner = PerformanceLearner()
        _seed_wins(learner, 20)
        result = learner.adjust_confidence("s", "BTC/EUR", "long", 0.95)
        assert result <= 0.95


# ── record_outcome ────────────────────────────────────────────────────────────

class TestRecordOutcomeBDD:
    def test_given_fresh_learner_when_outcome_recorded_then_raw_count_increases(self):
        """
        GIVEN a fresh learner,
        WHEN record_outcome is called once,
        THEN the internal raw_count for the key is 1.
        """
        learner = PerformanceLearner()
        learner.record_outcome("s", "BTC/EUR", "long", pnl=5.0)
        stats = learner._stats[("s", "BTC/EUR", "long")]
        assert stats.raw_count == 1

    def test_given_multiple_outcomes_when_recorded_then_count_matches(self):
        """
        GIVEN 7 outcomes recorded incrementally,
        WHEN raw_count is read,
        THEN it equals 7.
        """
        learner = PerformanceLearner()
        for i in range(7):
            learner.record_outcome("s", "BTC/EUR", "long", pnl=float(i - 3))
        stats = learner._stats[("s", "BTC/EUR", "long")]
        assert stats.raw_count == 7


# ── load_from_outcomes ────────────────────────────────────────────────────────

class TestLoadFromOutcomesBDD:
    def test_given_outcome_rows_when_loaded_then_raw_count_matches(self):
        """
        GIVEN a list of 6 outcome rows (newest-first),
        WHEN load_from_outcomes is called,
        THEN raw_count for the key equals 6.
        """
        learner = PerformanceLearner()
        rows = [_outcome("s", "BTC/EUR", "long", pnl=5.0) for _ in range(6)]
        learner.load_from_outcomes(rows)
        stats = learner._stats[("s", "BTC/EUR", "long")]
        assert stats.raw_count == 6

    def test_given_all_winning_rows_when_loaded_then_adjust_confidence_boosts(self):
        """
        GIVEN MIN_SAMPLES winning outcome rows seeded via load_from_outcomes,
        WHEN adjust_confidence is called,
        THEN returned confidence is higher than the base.
        """
        learner = PerformanceLearner()
        rows = [_outcome("s", "BTC/EUR", "long", pnl=10.0) for _ in range(MIN_SAMPLES)]
        learner.load_from_outcomes(rows)
        result = learner.adjust_confidence("s", "BTC/EUR", "long", 0.60)
        assert result > 0.60

    def test_given_mixed_keys_when_loaded_then_each_key_tracked_separately(self):
        """
        GIVEN outcomes for two different markets loaded together,
        WHEN raw_counts are read for each,
        THEN each key has the correct independent count.
        """
        learner = PerformanceLearner()
        rows = (
            [_outcome("s", "BTC/EUR", "long", pnl=10.0)] * 3
            + [_outcome("s", "ETH/EUR", "long", pnl=10.0)] * 4
        )
        learner.load_from_outcomes(rows)
        btc_count = learner._stats[("s", "BTC/EUR", "long")].raw_count
        eth_count = learner._stats[("s", "ETH/EUR", "long")].raw_count
        assert btc_count == 3
        assert eth_count == 4
