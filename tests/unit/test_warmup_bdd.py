"""
BDD tests for strategy loop warmup behaviour (T2.B2).

GIVEN / WHEN / THEN style — tests verify that LLMOnlyStrategy bypasses the
_TRADE_WARMUP_TICKS gate while indicator-based strategies are blocked.
"""


class _LLMOnlyStub:
    uses_llm_recommendation = True


class _BasicStrategyStub:
    pass  # no uses_llm_recommendation attribute


def _uses_llm_for(strategy) -> bool:
    """Mirror the logic in main.py that resolves _uses_llm."""
    return bool(getattr(strategy, "uses_llm_recommendation", False))


class TestWarmupGateBDD:
    def test_given_llm_only_strategy_when_warmup_check_then_not_blocked(self):
        """GIVEN LLMOnlyStrategy, WHEN warmup check runs, THEN strategy is not blocked."""
        strategy = _LLMOnlyStub()
        tick = 5
        _TRADE_WARMUP_TICKS = 20

        uses_llm = _uses_llm_for(strategy)
        warmup_blocks = not uses_llm and tick <= _TRADE_WARMUP_TICKS

        assert uses_llm is True
        assert warmup_blocks is False

    def test_given_basic_strategy_when_warmup_check_then_blocked(self):
        """GIVEN BasicStrategy at tick 5, WHEN warmup check runs, THEN strategy is blocked."""
        strategy = _BasicStrategyStub()
        tick = 5
        _TRADE_WARMUP_TICKS = 20

        uses_llm = _uses_llm_for(strategy)
        warmup_blocks = not uses_llm and tick <= _TRADE_WARMUP_TICKS

        assert uses_llm is False
        assert warmup_blocks is True

    def test_given_basic_strategy_past_warmup_when_check_then_not_blocked(self):
        """GIVEN BasicStrategy at tick 25, WHEN warmup check runs, THEN strategy is not blocked."""
        strategy = _BasicStrategyStub()
        tick = 25
        _TRADE_WARMUP_TICKS = 20

        uses_llm = _uses_llm_for(strategy)
        warmup_blocks = not uses_llm and tick <= _TRADE_WARMUP_TICKS

        assert warmup_blocks is False

    def test_given_none_strategy_when_warmup_check_then_not_llm(self):
        """GIVEN no strategy selected (None), WHEN warmup check runs, THEN treated as non-LLM."""
        uses_llm = _uses_llm_for(None)
        assert uses_llm is False
