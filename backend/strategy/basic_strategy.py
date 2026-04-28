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
from strategy.constants import (
    ATR_PENALTY_PER_EXTRA_PCT,
    ATR_VOLATILITY_THRESHOLD_PCT,
    BASE_CONFIDENCE_CAP,
    BB_BEARISH_THRESHOLD,
    BB_BULLISH_THRESHOLD,
    BB_LONG_BLOCK,
    BB_SHORT_BLOCK,
    INDICATOR_VOTE_WEIGHT,
    MAX_ATR_PENALTY,
    MAX_CONFIDENCE,
    MAX_DYNAMIC_POSITION_SIZE,
    MAX_INDICATOR_BONUS,
    MIN_ACTIONABLE_CONFIDENCE,
    MIN_CONFIDENCE,
    MIN_CONSENSUS_VOTES,
    MIN_DYNAMIC_POSITION_SIZE,
    MIN_INDICATOR_BONUS,
    MIN_INDICATORS_FOR_SIGNAL,
    MOMENTUM_ATR_MULTIPLIER,
    MOMENTUM_THRESHOLD,
    POSITION_SIZING_PROPOSAL,
    RSI_BEARISH_THRESHOLD,
    RSI_BULLISH_THRESHOLD,
    RSI_LONG_BLOCK,
    RSI_SHORT_BLOCK,
    STOCH_BEARISH_THRESHOLD,
    STOCH_BULLISH_THRESHOLD,
    WILLIAMS_BEARISH_THRESHOLD,
    WILLIAMS_BULLISH_THRESHOLD,
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

        momentum = (current_price - previous_price) / previous_price

        # Dynamic threshold: require momentum to exceed ATR (typical noise), with a
        # fixed floor so near-zero ATR markets still filter out micro-moves.
        # atr_pct is in percentage units (e.g. 0.5 = 0.5%), momentum is a decimal fraction.
        atr_pct = ind.get("atr_pct") or 0.0
        dynamic_threshold = (atr_pct / 100) * MOMENTUM_ATR_MULTIPLIER
        effective_threshold = dynamic_threshold if dynamic_threshold > MOMENTUM_THRESHOLD else MOMENTUM_THRESHOLD

        if abs(momentum) < effective_threshold:
            logger.debug(
                "%s momentum %.4f%% below threshold %.4f%% (ATR-based=%.4f%%, fixed=%.4f%%)",
                symbol,
                momentum * 100,
                effective_threshold * 100,
                dynamic_threshold * 100,
                MOMENTUM_THRESHOLD * 100,
            )
            return None

        is_long = momentum > 0
        direction = Direction.LONG if is_long else Direction.SHORT

        htf = data.get("higher_timeframe") or {}
        htf_ema_cross = htf.get("ema_cross")
        if htf_ema_cross and htf_ema_cross != "neutral":
            htf_supports_long = htf_ema_cross == "bullish"
            htf_supports_short = htf_ema_cross == "bearish"
            if (is_long and not htf_supports_long) or (not is_long and not htf_supports_short):
                logger.debug(
                    "%s %s blocked by higher-timeframe EMA %s",
                    symbol,
                    direction.value,
                    htf_ema_cross,
                )
                return None

        rsi_val = ind.get("rsi_14")
        bb_dict = ind.get("bb") or {}
        bb_pos = bb_dict.get("position")

        if rsi_val is not None:
            if is_long and rsi_val >= RSI_LONG_BLOCK:
                logger.debug("%s LONG blocked - RSI %.1f (overbought)", symbol, rsi_val)
                return None
            if not is_long and rsi_val <= RSI_SHORT_BLOCK:
                logger.debug("%s SHORT blocked - RSI %.1f (oversold)", symbol, rsi_val)
                return None

        if bb_pos is not None:
            if is_long and bb_pos >= BB_LONG_BLOCK:
                logger.debug("%s LONG blocked - BB %.1f%% (upper extreme)", symbol, bb_pos)
                return None
            if not is_long and bb_pos <= BB_SHORT_BLOCK:
                logger.debug("%s SHORT blocked - BB %.1f%% (lower extreme)", symbol, bb_pos)
                return None

        votes = 0
        available = 0
        supporting = 0

        def _vote(long_condition: bool, short_condition: bool) -> None:
            """Cast a vote when an indicator has a non-neutral opinion."""
            nonlocal votes, available, supporting
            if long_condition or short_condition:
                available += 1
                logger.debug("  [Vote] Available Indicator Count Increased (Total: %d)", available)
                if (is_long and long_condition) or (not is_long and short_condition):
                    votes += 1
                    supporting += 1
                    logger.debug("  [Vote] Supporting (+1). Votes: %d/%d", votes, available)
                elif (is_long and short_condition) or (not is_long and long_condition):
                    votes -= 1
                    logger.debug("  [Vote] Opposing (-1). Votes: %d/%d", votes, available)

        if rsi_val is not None:
            logger.debug(
                "  [Indicator] RSI check (%.1f). Long condition met: %s.", rsi_val, rsi_val < RSI_BULLISH_THRESHOLD
            )
            _vote(rsi_val < RSI_BULLISH_THRESHOLD, rsi_val > RSI_BEARISH_THRESHOLD)

        ema_cross = ind.get("ema_cross")
        if ema_cross:
            logger.debug(
                "  [Indicator] EMA cross check (%s). Long condition met: %s.", ema_cross, ema_cross == "bullish"
            )
            _vote(ema_cross == "bullish", ema_cross == "bearish")

        if bb_pos is not None:
            logger.debug(
                "  [Indicator] BB position check (%.1f%%). Long condition met: %s.",
                bb_pos,
                bb_pos < BB_BULLISH_THRESHOLD,
            )
            _vote(bb_pos < BB_BULLISH_THRESHOLD, bb_pos > BB_BEARISH_THRESHOLD)

        macd_dict = ind.get("macd") or {}
        macd_bias = macd_dict.get("bias")
        if macd_bias:
            logger.debug(
                "  [Indicator] MACD bias check (%s). Long condition met: %s.", macd_bias, macd_bias == "bullish"
            )
            _vote(macd_bias == "bullish", macd_bias == "bearish")

        sig_bias = macd_dict.get("signal_bias")
        if sig_bias:
            logger.debug(
                "  [Indicator] MACD-X bias check (%s). Long condition met: %s.", sig_bias, sig_bias == "bullish"
            )
            _vote(sig_bias == "bullish", sig_bias == "bearish")

        stoch_dict = ind.get("stoch") or {}
        k_val = stoch_dict.get("k")
        if k_val is not None:
            logger.debug(
                "  [Indicator] Stoch K check (%.2f). Long condition met: %s.", k_val, k_val < STOCH_BULLISH_THRESHOLD
            )
            _vote(k_val < STOCH_BULLISH_THRESHOLD, k_val > STOCH_BEARISH_THRESHOLD)

        wr_val = ind.get("williams_r")
        if wr_val is not None:
            logger.debug(
                "  [Indicator] Williams R check (%.2f). Long condition met: %s.",
                wr_val,
                wr_val <= WILLIAMS_BULLISH_THRESHOLD,
            )
            _vote(wr_val <= WILLIAMS_BULLISH_THRESHOLD, wr_val >= WILLIAMS_BEARISH_THRESHOLD)

        changes = ind.get("price_changes") or {}
        chg_5m = changes.get("5m")
        if chg_5m is not None:
            logger.debug("  [Indicator] 5m change check (%.2f%%). Long condition met: %s.", chg_5m * 100, chg_5m > 0)
            _vote(chg_5m > 0, chg_5m < 0)

        chg_15m = changes.get("15m")
        if chg_15m is not None:
            logger.debug("  [Indicator] 15m change check (%.2f%%). Long condition met: %s.", chg_15m * 100, chg_15m > 0)
            _vote(chg_15m > 0, chg_15m < 0)

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

        base = min(abs(momentum) * 100, BASE_CONFIDENCE_CAP)

        ind_bonus = 0.0
        if available > 0:
            ind_bonus = max(
                MIN_INDICATOR_BONUS,
                min(MAX_INDICATOR_BONUS, votes * INDICATOR_VOTE_WEIGHT),
            )

        atr_pct = ind.get("atr_pct") or 0.0
        atr_penalty = (
            -min(
                MAX_ATR_PENALTY,
                max(
                    0.0,
                    (atr_pct - ATR_VOLATILITY_THRESHOLD_PCT) * ATR_PENALTY_PER_EXTRA_PCT,
                ),
            )
            if atr_pct > ATR_VOLATILITY_THRESHOLD_PCT
            else 0.0
        )

        confidence = max(
            MIN_CONFIDENCE,
            min(MAX_CONFIDENCE, base + ind_bonus + atr_penalty),
        )

        logger.debug(
            "  [Confidence] Base Confidence: %.2f%%, Indicator Bonus: %.2f%%, ATR Penalty: %.2f%%",
            base,
            ind_bonus * 100,
            atr_penalty * 100,
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

        position_size = self._position_sizing(symbol, atr_pct)

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
                "indicators_opposing": available - supporting,
                "indicators_available": available,
                "rsi_14": rsi_val,
                "ema_cross": ema_cross,
                "bb_position": bb_pos,
                "macd_bias": macd_bias,
                "macd_signal_bias": sig_bias,
                "macd_histogram": macd_dict.get("histogram"),
                "stoch_k": k_val,
                "stoch_d": stoch_dict.get("d"),
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

    def _position_sizing(self, symbol: str, atr_pct: float) -> float:
        """Return volatility-adjusted position sizing."""
        logger.debug("  [Position Sizing] Calculating size for %s (ATR %.3f%%).", symbol, atr_pct * 100)
        size = POSITION_SIZING_PROPOSAL
        if atr_pct > 1.0:
            new_size = max(0.25, 1.0 / atr_pct)
            logger.debug(
                "  [Position Sizing] ATR exceeds threshold (%.3f%%). Applying multiplier %.2f.", atr_pct * 100, new_size
            )
            size *= new_size
        else:
            logger.debug("  [Position Sizing] ATR is within acceptable range.")

        final_size = max(MIN_DYNAMIC_POSITION_SIZE, min(MAX_DYNAMIC_POSITION_SIZE, size))
        logger.debug(
            "  [Position Sizing] Final proposed size capped between %.2f%% and %.2f%%.",
            MIN_DYNAMIC_POSITION_SIZE * 100,
            MAX_DYNAMIC_POSITION_SIZE * 100,
        )
        return final_size
