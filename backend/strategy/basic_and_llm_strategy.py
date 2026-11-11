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
    MAX_CONFIDENCE,
    MIN_ACTIONABLE_CONFIDENCE,
    MIN_CONSENSUS_VOTES,
    MIN_INDICATORS_FOR_SIGNAL,
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

        momentum = calculate_momentum(current_price, previous_price, data.get("price_history"))
        if momentum is None:
            logger.debug("[%s] skip: previous_price is zero/null", symbol)
            return None

        # Dynamic threshold: require momentum to exceed ATR (typical noise), with a
        # fixed floor so near-zero ATR markets still filter out micro-moves.
        # atr_pct is in percentage units (e.g. 0.5 = 0.5%), momentum is a decimal fraction.
        atr_pct = ind.get("atr_pct") or 0.0
        momentum_gate = evaluate_momentum_gate(momentum, atr_pct)

        logger.debug(
            "[%s] momentum=%.4f%% threshold=%.4f%% (ATR-based=%.4f%% floor=%.4f%%)",
            symbol, momentum * 100, momentum_gate.effective_threshold * 100,
            momentum_gate.dynamic_threshold * 100, momentum_gate.effective_threshold * 100,
        )

        if not momentum_gate.passes:
            logger.debug(
                "[%s] skip: momentum %.4f%% below threshold %.4f%%",
                symbol, momentum * 100, momentum_gate.effective_threshold * 100,
            )
            return None

        is_long = momentum > 0
        direction = Direction.LONG if is_long else Direction.SHORT
        logger.debug("[%s] direction=%s momentum=%.4f%%", symbol, direction.value, momentum * 100)

        htf = data.get("higher_timeframe") or {}
        logger.debug("[%s] HTF EMA cross=%s", symbol, htf.get("ema_cross") or "none")
        alignment = check_higher_timeframe_alignment(is_long, htf)
        if not alignment.allowed:
            logger.debug("[%s] skip: %s opposes %s direction", symbol, alignment.reason, direction.value)
            return None

        hard_filter = check_hard_filters(is_long, ind)
        if hard_filter.blocked:
            logger.debug("[%s] skip: %s", symbol, hard_filter.reason)
            return None

        logger.debug("[%s] --- indicator voting ---", symbol)
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

        confidence_breakdown = calculate_confidence(
            momentum=momentum,
            votes=votes,
            available=available,
            atr_pct=atr_pct,
        )
        base_confidence = confidence_breakdown.confidence
        logger.debug(
            "[%s] confidence: base=%.3f ind_bonus=%.3f atr_penalty=%.3f -> base_confidence=%.3f",
            symbol,
            confidence_breakdown.base,
            confidence_breakdown.indicator_bonus,
            confidence_breakdown.atr_penalty,
            base_confidence,
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
