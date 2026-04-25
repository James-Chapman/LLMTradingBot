"""Indicator-only strategy with no news or LLM sentiment inputs."""

from strategy.basic_strategy import BasicStrategy


class IndicatorOnlyStrategy(BasicStrategy):
    """Technical-indicator strategy that ignores non-indicator sentiment."""

    def __init__(self) -> None:
        super().__init__(
            "indicator_only",
            use_news_sentiment=False,
            use_llm_sentiment=False,
        )
