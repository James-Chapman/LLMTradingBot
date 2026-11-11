"""
Indicator-only trading strategy.

Signal generation
-----------------
1. Momentum gate - price must move at least 0.2 % vs the lookback price.
2. Hard filters - extreme RSI or BB readings block the trade entirely.
3. Indicator vote - indicators each cast +1 (agrees) / -1 (opposes) / 0.
4. Support gate - at least 5 indicators must agree with the trade direction.
5. Consensus gate - net vote must be at least +1.
6. Confidence - base + indicator bonus - volatility penalty.
"""

from typing import Any, Dict, List

from config.settings import settings
from domain.models import Direction, TradeIdea, TradingMode
from observability.logging import get_logger
from strategy.calculations import (
    calculate_confidence,
    calculate_momentum,
    calculate_position_sizing,
    check_hard_filters,
    check_higher_timeframe_alignment,
    evaluate_momentum_gate,
    vote_indicators,
)
from strategy.constants import (
    MIN_ACTIONABLE_CONFIDENCE,
    MIN_CONSENSUS_VOTES,
    MIN_INDICATORS_FOR_SIGNAL,
)

logger = get_logger("basic_strategy")


class BasicStrategy:
    """Indicator-only strategy."""

    def __init__(self, strategy_id: str = "basic_strategy") -> None:
        logger.debug(f"BasicStrategy initialized for ID: {strategy_id}")
        self.strategy_id = strategy_id

    async def evaluate(
        self,
        market_data: Dict[str, Any],
    ) -> List[TradeIdea]:
        """Evaluate all markets and return trade ideas."""
        logger.info("Starting evaluation across %d markets.", len(market_data))
        ideas = []

        for symbol, data in market_data.items():
            idea = self._evaluate_market(symbol, data)
            if idea is not None:
                ideas.append(idea)

        if not ideas:
            logger.debug("No signals generated this tick")
        else:
            logger.info("%d trade ideas generated successfully.", len(ideas))
        return ideas

    def _evaluate_market(
        self,
        symbol: str,
        data: Dict[str, Any],
    ):
        """Evaluate a single market. Returns a TradeIdea or None."""

        logger.debug("--- Starting evaluation for %s ---", symbol)

        if "price" not in data or "previous_price" not in data:
            logger.debug("%s skipped - Missing price data.", symbol)
            return None

        current_price = data["price"]
        previous_price = data["previous_price"]
        ind: Dict[str, Any] = data.get("indicators", {})
        logger.debug("Market %s data received: Price=%.2f, PrevPrice=%.2f.", symbol, current_price, previous_price)

        if not previous_price:
            logger.warning("Skipping %s: Previous price is missing.", symbol)
            return None

        momentum = calculate_momentum(current_price, previous_price, data.get("price_history"))
        if momentum is None:
            logger.warning("Skipping %s: Previous price is missing.", symbol)
            return None

        # Dynamic threshold: require momentum to exceed ATR (typical noise), with a
        # fixed floor so near-zero ATR markets still filter out micro-moves.
        # atr_pct is in percentage units (e.g. 0.5 = 0.5%), momentum is a decimal fraction.
        atr_pct = ind.get("atr_pct") or 0.0
        momentum_gate = evaluate_momentum_gate(momentum, atr_pct)

        if not momentum_gate.passes:
            logger.debug(
                "%s momentum %.4f%% below threshold %.4f%% (ATR-based=%.4f%%, fixed=%.4f%%)",
                symbol,
                momentum * 100,
                momentum_gate.effective_threshold * 100,
                momentum_gate.dynamic_threshold * 100,
                momentum_gate.effective_threshold * 100,
            )
            return None

        is_long = momentum > 0
        direction = Direction.LONG if is_long else Direction.SHORT

        htf = data.get("higher_timeframe") or {}
        alignment = check_higher_timeframe_alignment(is_long, htf)
        if not alignment.allowed:
            logger.debug("%s %s blocked by %s", symbol, direction.value, alignment.reason)
            return None

        hard_filter = check_hard_filters(is_long, ind)
        if hard_filter.blocked:
            logger.debug("%s %s blocked - %s", symbol, direction.value, hard_filter.reason)
            return None

        vote_summary = vote_indicators(is_long, ind)
        votes = vote_summary.votes
        available = vote_summary.available
        supporting = vote_summary.supporting
        values = vote_summary.values
        rsi_val = values["rsi_14"]
        ema_cross = values["ema_cross"]
        bb_pos = values["bb_position"]
        macd_bias = values["macd_bias"]
        sig_bias = values["macd_signal_bias"]
        k_val = values["stoch_k"]
        wr_val = values["williams_r"]
        chg_5m = values["price_change_5m"]
        chg_15m = values["price_change_15m"]

        if supporting < MIN_INDICATORS_FOR_SIGNAL:
            logger.debug(
                "%s %s skipped - only %d/%d indicators support the signal",
                symbol,
                direction.value,
                supporting,
                MIN_INDICATORS_FOR_SIGNAL,
            )
            return None

        if votes < MIN_CONSENSUS_VOTES:
            logger.debug(
                "%s %s skipped - indicator consensus %+d/%d",
                symbol,
                direction.value,
                votes,
                available,
            )
            return None

        confidence_breakdown = calculate_confidence(
            momentum=momentum,
            votes=votes,
            available=available,
            atr_pct=atr_pct,
        )
        confidence = confidence_breakdown.confidence

        logger.debug(
            "  [Confidence] Base Confidence: %.2f%%, Indicator Bonus: %.2f%%, ATR Penalty: %.2f%%",
            confidence_breakdown.base,
            confidence_breakdown.indicator_bonus * 100,
            confidence_breakdown.atr_penalty * 100,
        )
        logger.debug("  [Confidence] Final calculated confidence: %.2f%%", confidence * 100)

        if confidence < MIN_ACTIONABLE_CONFIDENCE:
            logger.debug(
                "%s %s skipped - Confidence %.2f%% below actionable threshold %.2f%%",
                symbol,
                direction.value,
                confidence * 100,
                MIN_ACTIONABLE_CONFIDENCE * 100,
            )
            return None

        sl_pct = int(settings.stop_loss_pct * 100)
        thesis_parts = [f"Momentum {momentum:.2%}"]
        if rsi_val is not None:
            logger.debug("  [Thesis] Appending RSI value %.1f.", rsi_val)
            thesis_parts.append(f"RSI {rsi_val}")
        if ema_cross and ema_cross != "neutral":
            logger.debug("  [Thesis] Appending EMA cross: %s", ema_cross)
            thesis_parts.append(f"EMA {ema_cross}")
        if macd_bias and macd_bias != "neutral":
            logger.debug("  [Thesis] Appending MACD bias: %s", macd_bias)
            thesis_parts.append(f"MACD {macd_bias}")
        if sig_bias and sig_bias != "neutral":
            logger.debug("  [Thesis] Appending MACD-X bias: %s", sig_bias)
            thesis_parts.append(f"MACD-X {sig_bias}")
        if k_val is not None:
            logger.debug("  [Thesis] Appending Stoch K value %.2f.", k_val)
            thesis_parts.append(f"Stoch {k_val}")
        if wr_val is not None:
            logger.debug("  [Thesis] Appending WR value %.2f.", wr_val)
            thesis_parts.append(f"WR {wr_val}")
        thesis_parts.append(f"support {supporting}/{available} (net {votes:+d})")

        position_size = calculate_position_sizing(atr_pct)

        idea = TradeIdea(
            strategy_id=self.strategy_id,
            market=symbol,
            direction=direction,
            thesis=" | ".join(thesis_parts),
            supporting_signals={
                "momentum": round(momentum, 6),
                "current_price": current_price,
                "previous_price": previous_price,
                "indicator_votes": votes,
                "indicators_supporting": supporting,
                "indicators_opposing": vote_summary.opposing,
                "indicators_available": available,
                "rsi_14": rsi_val,
                "ema_cross": ema_cross,
                "bb_position": bb_pos,
                "macd_bias": macd_bias,
                "macd_signal_bias": sig_bias,
                "macd_histogram": values["macd_histogram"],
                "stoch_k": k_val,
                "stoch_d": values["stoch_d"],
                "williams_r": wr_val,
                "atr_pct": round(atr_pct, 3) if atr_pct else None,
                "price_change_5m": chg_5m,
                "price_change_15m": chg_15m,
            },
            confidence=confidence,
            entry_plan="Enter at market on indicator consensus confirmation",
            exit_plan=f"Exit on momentum reversal or stop-loss at {sl_pct}% loss",
            stop_or_invalidation=f"Abandon if unrealised loss reaches {sl_pct}%",
            position_sizing_proposal=position_size,
            mode_eligibility=[
                TradingMode.MANUAL,
                TradingMode.SEMI_AUTOMATED,
                TradingMode.FULLY_AUTOMATED,
            ],
        )

        logger.info(
            "Generated trade idea",
            extra={
                "strategy": self.strategy_id,
                "market": symbol,
                "direction": direction.value,
                "confidence": round(confidence, 3),
                "votes": f"{votes:+d}/{available}",
                "rsi": rsi_val,
                "ema_cross": ema_cross,
            },
        )
        logger.debug("--- Finished evaluation for %s ---", symbol)
        return idea
