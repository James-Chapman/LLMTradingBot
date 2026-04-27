"""LLM-only trading strategy.

This strategy does not apply indicator gates or indicator voting. It passes
price, indicator, portfolio, and news context to the LLM and only acts on the
LLM's explicit long/short recommendation.
"""

import asyncio
from typing import Any, Dict, List, Optional

from config.settings import settings
from domain.models import Direction, PositionRecord, TradeIdea, TradingMode
from llm.analyser import LLMTradeRecommendation
from observability.logging import get_logger

logger = get_logger("llm_only_strategy")


class LLMOnlyStrategy:
    """LLM-led strategy with no local indicator gating."""

    def __init__(self, strategy_id: str = "llm_only_strategy", max_concurrency: Optional[int] = None) -> None:
        self.strategy_id = strategy_id
        self.uses_llm_recommendation = True
        configured = max_concurrency or settings.llm_only_max_concurrency
        self.max_concurrency = max(1, configured)

    # Evaluate markets by asking the LLM for an explicit long/short/hold action.
    async def evaluate(
        self,
        market_data: Dict[str, Any],
        news_signals: List[Dict],
        *,
        analyser=None,
        equity: float = 0.0,
        cash: float = 0.0,
        open_positions: Optional[List[PositionRecord]] = None,
    ) -> List[TradeIdea]:
        """Evaluate all markets concurrently using asyncio.gather.

        Sequential evaluation blocked the strategy loop for O(n) × LLM-latency
        seconds.  Gathering all coroutines runs them in parallel within the
        single event loop — wall-clock time is bounded by the slowest call
        rather than the sum of all calls.
        """
        if analyser is None:
            logger.warning("LLMAnalyser not provided. Cannot evaluate market.")
            return []

        positions = open_positions or []
        semaphore = asyncio.Semaphore(self.max_concurrency)
        logger.debug("Initialized LLMOnlyStrategy with max concurrency set to %d.", self.max_concurrency)

        async def _bounded_evaluate(symbol: str, data: Dict[str, Any]):
            """Evaluate one market while respecting the configured concurrency limit."""
            logger.debug("Starting evaluation for %s within semaphore slot.", symbol)
            async with semaphore:
                return await self._evaluate_market(
                    symbol,
                    data,
                    news_signals,
                    analyser=analyser,
                    equity=equity,
                    cash=cash,
                    open_positions=positions,
                )

        coros = [_bounded_evaluate(symbol, data) for symbol, data in market_data.items()]
        logger.debug("Dispatching %d market evaluation coroutines concurrently.", len(coros))
        results = await asyncio.gather(*coros, return_exceptions=True)

        ideas: List[TradeIdea] = []
        for i, result in enumerate(results):
            symbol = list(market_data.keys())[i]  # Use index to get symbol for logging context
            if isinstance(result, Exception):
                logger.error("LLM market evaluation failed for %s: %s", symbol, result)
                continue
            if result is not None:
                ideas.append(result)

        logger.info("Finished market evaluations. Successfully generated %d trade ideas.", len(ideas))
        return ideas

    # Evaluate one market and convert an LLM trade recommendation into a TradeIdea.
    async def _evaluate_market(
        self,
        symbol: str,
        data: Dict[str, Any],
        news_signals: List[Dict],
        *,
        analyser,
        equity: float,
        cash: float,
        open_positions: List[PositionRecord],
    ) -> Optional[TradeIdea]:
        """Return a TradeIdea when the LLM recommends long or short."""
        logger.debug("--- Starting _evaluate_market for %s ---", symbol)
        if "price" not in data:
            logger.warning("Skipping %s: Price data missing.", symbol)
            return None

        current_price = data["price"]
        previous_price = data.get("previous_price", current_price)
        indicators = data.get("indicators", {})
        logger.debug("Market %s context received: CurrentPrice=%.2f, Indicators available.", symbol, current_price)

        recommendation = await analyser.recommend_trade(
            market=symbol,
            current_price=current_price,
            previous_price=previous_price,
            indicators=indicators,
            news=news_signals,
            equity=equity,
            cash=cash,
            open_positions=open_positions,
        )
        logger.debug("LLM recommendation received for %s.", symbol)

        if not recommendation.llm_used:
            logger.info("Skipping %s: LLM analysis was not used.", symbol)
            return None
        if recommendation.action == "hold":
            logger.info("Skipping %s: LLM recommended 'hold'.", symbol)
            return None
        if recommendation.action not in {"long", "short"}:
            logger.error("Skipping %s: Invalid action from LLM: %s.", symbol, recommendation.action)
            return None

        direction = Direction.LONG if recommendation.action == "long" else Direction.SHORT
        momentum = (current_price - previous_price) / previous_price if previous_price else 0.0
        logger.debug(
            "LLM confirmed direction for %s: %s with momentum %.2f%%.", symbol, direction.value, momentum * 100
        )
        return self._build_trade_idea(
            symbol=symbol,
            direction=direction,
            current_price=current_price,
            previous_price=previous_price,
            momentum=momentum,
            recommendation=recommendation,
        )

    # Build the domain trade idea from the LLM recommendation.
    def _build_trade_idea(
        self,
        *,
        symbol: str,
        direction: Direction,
        current_price: float,
        previous_price: float,
        momentum: float,
        recommendation: LLMTradeRecommendation,
    ) -> TradeIdea:
        """Create the TradeIdea consumed by risk, approval, and execution."""
        logger.info(
            "Building trade idea for %s (Direction: %s). Final Confidence: %.2f",
            symbol,
            direction.value,
            recommendation.confidence,
        )
        sl_pct = int(settings.stop_loss_pct * 100)
        return TradeIdea(
            strategy_id=self.strategy_id,
            market=symbol,
            direction=direction,
            thesis=f"LLM recommendation: {recommendation.reasoning}",
            supporting_signals={
                "llm_only": True,
                "llm_strategy": True,
                "llm_action": recommendation.action,
                "llm_sentiment": round(recommendation.sentiment, 3),
                "llm_reasoning": recommendation.reasoning,
                "pre_llm_confidence": recommendation.confidence,
                "momentum": round(momentum, 6),
                "current_price": current_price,
                "previous_price": previous_price,
            },
            confidence=recommendation.confidence,
            entry_plan="Enter at market on LLM recommendation",
            exit_plan=f"Exit on LLM reversal or stop-loss at {sl_pct}% loss",
            stop_or_invalidation=f"Abandon if unrealised loss reaches {sl_pct}%",
            position_sizing_proposal=0.20,
            mode_eligibility=[
                TradingMode.MANUAL,
                TradingMode.SEMI_AUTOMATED,
                TradingMode.FULLY_AUTOMATED,
            ],
        )
