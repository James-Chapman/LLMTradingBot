"""
Indicator and LLM combined trading strategy.

Signal generation
-----------------
1. Momentum gate - price must move at least 0.2 % vs the lookback price.
2. Hard filters - extreme RSI or BB readings block the trade entirely.
3. Indicator vote - indicators each cast +1 (agrees) / -1 (opposes) / 0.
4. Support gate - at least 5 indicators must agree with the trade direction.
5. Consensus gate - net vote must be at least +1.
6. Confidence - base + indicator bonus - volatility penalty.
7. LLM analysis - LLM assesses if evidence supports or contradicts the signal.
8. Final confidence - base confidence × LLM confidence_scale.
9. Veto - skip trade if LLM confidence_scale < LLM_VETO_THRESHOLD.
"""

from typing import Any, Dict, List, Optional

from config.settings import settings
from domain.models import Direction, TradeIdea, TradingMode
from llm.analyser import LLMAnalyser
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

logger = get_logger("basic_and_llm_strategy")


class BasicAndLLMStrategy:
    """Indicator consensus strategy with LLM signal validation."""

    def __init__(self, strategy_id: str = "basic_and_llm_strategy") -> None:
        self.strategy_id = strategy_id
        self.uses_llm_analysis = True

    async def evaluate(
        self,
        market_data: Dict[str, Any],
        news_signals: List[Dict],
        analyser: LLMAnalyser,
        portfolio_data: Optional[Dict[str, Any]] = None,
        market_briefing: Optional[Any] = None,
    ) -> List[TradeIdea]:
        """Evaluate all markets and return trade ideas."""
        ideas = []

        logger.info("Starting evaluation across %d markets.", len(market_data))
        for symbol, data in market_data.items():
            idea = await self._evaluate_market(symbol, data, news_signals, analyser, portfolio_data)
            if idea is not None:
                ideas.append(idea)

        logger.info("Evaluation complete. Generated %d trade ideas.", len(ideas))
        return ideas

    async def _evaluate_market(
        self,
        symbol: str,
        data: Dict[str, Any],
        news_signals: List[Dict],
        analyser: LLMAnalyser,
        portfolio_data: Optional[Dict[str, Any]],
    ):
        """Evaluate a single market. Returns a TradeIdea or None."""

        logger.debug("[%s] --- starting evaluation ---", symbol)

        if "price" not in data or "previous_price" not in data:
            logger.debug("[%s] skip: missing price or previous_price in data", symbol)
            return None

        current_price = data["price"]
        previous_price = data["previous_price"]
        ind: Dict[str, Any] = data.get("indicators", {})

        logger.debug("[%s] price=%.4f prev=%.4f", symbol, current_price, previous_price)

        if not previous_price:
            logger.debug("[%s] skip: previous_price is zero/null", symbol)
            return None

        momentum = (current_price - previous_price) / previous_price

        # Dynamic threshold: require momentum to exceed ATR (typical noise), with a
        # fixed floor so near-zero ATR markets still filter out micro-moves.
        # atr_pct is in percentage units (e.g. 0.5 = 0.5%), momentum is a decimal fraction.
        atr_pct = ind.get("atr_pct") or 0.0
        dynamic_threshold = (atr_pct / 100) * MOMENTUM_ATR_MULTIPLIER
        effective_threshold = dynamic_threshold if dynamic_threshold > MOMENTUM_THRESHOLD else MOMENTUM_THRESHOLD

        logger.debug(
            "[%s] momentum=%.4f%% threshold=%.4f%% (ATR-based=%.4f%% floor=%.4f%%)",
            symbol, momentum * 100, effective_threshold * 100,
            dynamic_threshold * 100, MOMENTUM_THRESHOLD * 100,
        )

        if abs(momentum) < effective_threshold:
            logger.debug(
                "[%s] skip: momentum %.4f%% below threshold %.4f%%",
                symbol, momentum * 100, effective_threshold * 100,
            )
            return None

        is_long = momentum > 0
        direction = Direction.LONG if is_long else Direction.SHORT
        logger.debug("[%s] direction=%s momentum=%.4f%%", symbol, direction.value, momentum * 100)

        htf = data.get("higher_timeframe") or {}
        htf_ema_cross = htf.get("ema_cross")
        logger.debug("[%s] HTF EMA cross=%s", symbol, htf_ema_cross or "none")
        if htf_ema_cross and htf_ema_cross != "neutral":
            htf_supports_long = htf_ema_cross == "bullish"
            htf_supports_short = htf_ema_cross == "bearish"
            if (is_long and not htf_supports_long) or (not is_long and not htf_supports_short):
                logger.debug(
                    "[%s] skip: HTF EMA %s opposes %s direction",
                    symbol, htf_ema_cross, direction.value,
                )
                return None

        rsi_val = ind.get("rsi_14")
        bb_dict = ind.get("bb") or {}
        bb_pos = bb_dict.get("position")
        logger.debug("[%s] hard filters: RSI=%s BB_pos=%s", symbol, rsi_val, bb_pos)

        if rsi_val is not None:
            if is_long and rsi_val >= RSI_LONG_BLOCK:
                logger.debug("[%s] skip: RSI %.1f >= %d (overbought, blocks LONG)", symbol, rsi_val, RSI_LONG_BLOCK)
                return None
            if not is_long and rsi_val <= RSI_SHORT_BLOCK:
                logger.debug("[%s] skip: RSI %.1f <= %d (oversold, blocks SHORT)", symbol, rsi_val, RSI_SHORT_BLOCK)
                return None

        if bb_pos is not None:
            if is_long and bb_pos >= BB_LONG_BLOCK:
                logger.debug("[%s] skip: BB %.1f%% >= %d (upper extreme, blocks LONG)", symbol, bb_pos, BB_LONG_BLOCK)
                return None
            if not is_long and bb_pos <= BB_SHORT_BLOCK:
                logger.debug("[%s] skip: BB %.1f%% <= %d (lower extreme, blocks SHORT)", symbol, bb_pos, BB_SHORT_BLOCK)
                return None

        votes = 0
        available = 0
        supporting = 0

        def _vote(indicator_name: str, long_condition: bool, short_condition: bool) -> None:
            """Cast a vote when an indicator has a non-neutral opinion."""
            nonlocal votes, available, supporting
            if long_condition or short_condition:
                available += 1
                if (is_long and long_condition) or (not is_long and short_condition):
                    votes += 1
                    supporting += 1
                    logger.debug(
                        "[%s]   %s: +1 (supporting). total supporting=%d available=%d votes=%+d",
                        symbol, indicator_name, supporting, available, votes,
                    )
                elif (is_long and short_condition) or (not is_long and long_condition):
                    votes -= 1
                    logger.debug(
                        "[%s]   %s: -1 (opposing). total supporting=%d available=%d votes=%+d",
                        symbol, indicator_name, supporting, available, votes,
                    )
            else:
                logger.debug("[%s]   %s: 0 (neutral / no opinion)", symbol, indicator_name)

        logger.debug("[%s] --- indicator voting ---", symbol)

        if rsi_val is not None:
            logger.debug("[%s]   RSI=%.1f bull<%.0f bear>%.0f", symbol, rsi_val, RSI_BULLISH_THRESHOLD, RSI_BEARISH_THRESHOLD)
            _vote("RSI", rsi_val < RSI_BULLISH_THRESHOLD, rsi_val > RSI_BEARISH_THRESHOLD)

        ema_cross = ind.get("ema_cross")
        logger.debug("[%s]   EMA cross=%s", symbol, ema_cross or "none")
        if ema_cross:
            _vote("EMA_cross", ema_cross == "bullish", ema_cross == "bearish")

        if bb_pos is not None:
            logger.debug("[%s]   BB pos=%.1f%% bull<%.0f bear>%.0f", symbol, bb_pos, BB_BULLISH_THRESHOLD, BB_BEARISH_THRESHOLD)
            _vote("BB_pos", bb_pos < BB_BULLISH_THRESHOLD, bb_pos > BB_BEARISH_THRESHOLD)

        macd_dict = ind.get("macd") or {}
        macd_bias = macd_dict.get("bias")
        logger.debug("[%s]   MACD bias=%s", symbol, macd_bias or "none")
        if macd_bias:
            _vote("MACD_bias", macd_bias == "bullish", macd_bias == "bearish")

        sig_bias = macd_dict.get("signal_bias")
        logger.debug("[%s]   MACD signal_bias=%s", symbol, sig_bias or "none")
        if sig_bias:
            _vote("MACD_signal", sig_bias == "bullish", sig_bias == "bearish")

        stoch_dict = ind.get("stoch") or {}
        k_val = stoch_dict.get("k")
        logger.debug("[%s]   Stoch K=%s bull<%.0f bear>%.0f", symbol, k_val, STOCH_BULLISH_THRESHOLD, STOCH_BEARISH_THRESHOLD)
        if k_val is not None:
            _vote("Stoch_K", k_val < STOCH_BULLISH_THRESHOLD, k_val > STOCH_BEARISH_THRESHOLD)

        wr_val = ind.get("williams_r")
        logger.debug("[%s]   Williams R=%s bull<=%d bear>=%d", symbol, wr_val, WILLIAMS_BULLISH_THRESHOLD, WILLIAMS_BEARISH_THRESHOLD)
        if wr_val is not None:
            _vote("Williams_R", wr_val <= WILLIAMS_BULLISH_THRESHOLD, wr_val >= WILLIAMS_BEARISH_THRESHOLD)

        changes = ind.get("price_changes") or {}
        chg_5m = changes.get("5m")
        logger.debug("[%s]   5m change=%s", symbol, f"{chg_5m:.4f}%" if chg_5m is not None else "none")
        if chg_5m is not None:
            _vote("price_change_5m", chg_5m > 0, chg_5m < 0)

        chg_15m = changes.get("15m")
        logger.debug("[%s]   15m change=%s", symbol, f"{chg_15m:.4f}%" if chg_15m is not None else "none")
        if chg_15m is not None:
            _vote("price_change_15m", chg_15m > 0, chg_15m < 0)

        logger.debug(
            "[%s] vote summary: supporting=%d/%d votes=%+d | need supporting>=%d votes>=%d",
            symbol, supporting, available, votes,
            MIN_INDICATORS_FOR_SIGNAL, MIN_CONSENSUS_VOTES,
        )

        if supporting < MIN_INDICATORS_FOR_SIGNAL:
            logger.debug(
                "[%s] skip: only %d/%d indicators support (need %d)",
                symbol, supporting, available, MIN_INDICATORS_FOR_SIGNAL,
            )
            return None

        if votes < MIN_CONSENSUS_VOTES:
            logger.debug(
                "[%s] skip: net votes %+d < %d (available=%d)",
                symbol, votes, MIN_CONSENSUS_VOTES, available,
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

        base_confidence = max(
            MIN_CONFIDENCE,
            min(MAX_CONFIDENCE, base + ind_bonus + atr_penalty),
        )
        logger.debug(
            "[%s] confidence: base=%.3f ind_bonus=%.3f atr_penalty=%.3f -> base_confidence=%.3f",
            symbol, base, ind_bonus, atr_penalty, base_confidence,
        )

        if base_confidence < MIN_ACTIONABLE_CONFIDENCE:
            logger.debug(
                "[%s] skip: base_confidence %.3f < actionable threshold %.3f",
                symbol, base_confidence, MIN_ACTIONABLE_CONFIDENCE,
            )
            return None

        equity = portfolio_data.get("equity", 0.0) if portfolio_data else 0.0
        cash = portfolio_data.get("cash", 0.0) if portfolio_data else 0.0
        open_positions = portfolio_data.get("open_positions", []) if portfolio_data else []

        logger.debug(
            "[%s] calling LLM analyser (equity=%.2f cash=%.2f open_positions=%d)...",
            symbol, equity, cash, len(open_positions),
        )

        llm_analysis = await analyser.analyse_signal(
            market=symbol,
            direction=direction.value,
            momentum_pct=momentum * 100,
            base_confidence=base_confidence,
            news=news_signals,
            current_price=current_price,
            indicators=ind,
            equity=equity,
            cash=cash,
            open_positions=open_positions,
        )

        logger.debug(
            "[%s] LLM result: llm_used=%s scale=%.3f threshold=%.3f reasoning=%s",
            symbol, llm_analysis.llm_used, llm_analysis.confidence_scale,
            settings.llm_veto_threshold, llm_analysis.reasoning,
        )

        if llm_analysis.confidence_scale < settings.llm_veto_threshold:
            logger.info(
                "%s %s vetoed by LLM — scale %.2f < threshold %.2f: %s",
                symbol,
                direction.value,
                llm_analysis.confidence_scale,
                settings.llm_veto_threshold,
                llm_analysis.reasoning,
            )
            return None

        final_confidence = min(MAX_CONFIDENCE, base_confidence * llm_analysis.confidence_scale)
        logger.debug(
            "[%s] final_confidence=%.3f (base %.3f × LLM scale %.3f)",
            symbol, final_confidence, base_confidence, llm_analysis.confidence_scale,
        )

        sl_pct = int(settings.stop_loss_pct * 100)
        thesis_parts = [f"Momentum {momentum:.2%}"]
        if rsi_val is not None:
            thesis_parts.append(f"RSI {rsi_val}")
        if ema_cross and ema_cross != "neutral":
            thesis_parts.append(f"EMA {ema_cross}")
        if macd_bias and macd_bias != "neutral":
            thesis_parts.append(f"MACD {macd_bias}")
        if sig_bias and sig_bias != "neutral":
            thesis_parts.append(f"MACD-X {sig_bias}")
        if k_val is not None:
            thesis_parts.append(f"Stoch {k_val}")
        if wr_val is not None:
            thesis_parts.append(f"WR {wr_val}")
        thesis_parts.append(f"support {supporting}/{available} (net {votes:+d})")
        if llm_analysis.llm_used:
            thesis_parts.append(f"LLM: {llm_analysis.reasoning}")

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
                "llm_sentiment": round(llm_analysis.sentiment, 3) if llm_analysis.llm_used else None,
                "llm_confidence_scale": round(llm_analysis.confidence_scale, 3) if llm_analysis.llm_used else None,
                "llm_reasoning": llm_analysis.reasoning if llm_analysis.llm_used else None,
            },
            confidence=final_confidence,
            entry_plan="Enter at market on indicator consensus and LLM validation",
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
                "base_confidence": round(base_confidence, 3),
                "llm_scale": round(llm_analysis.confidence_scale, 3),
                "final_confidence": round(final_confidence, 3),
                "votes": f"{votes:+d}/{available}",
                "rsi": rsi_val,
                "ema_cross": ema_cross,
                "llm_used": llm_analysis.llm_used,
            },
        )
        logger.debug("[%s] --- evaluation complete ---", symbol)
        return idea

    def _position_sizing(self, symbol: str, atr_pct: float) -> float:
        """Return volatility-adjusted position sizing."""
        size = POSITION_SIZING_PROPOSAL
        if atr_pct > 1.0:
            size *= max(0.25, 1.0 / atr_pct)
        return max(MIN_DYNAMIC_POSITION_SIZE, min(MAX_DYNAMIC_POSITION_SIZE, size))
