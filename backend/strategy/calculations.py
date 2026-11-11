"""Shared strategy calculation formulas.

Raw technical indicators live in ``analysis.indicators``. This module owns the
decision formulas that turn indicator values into strategy gates, votes,
confidence, and position sizing.
"""

import math
from dataclasses import dataclass
from typing import Any, Dict, Optional, Sequence

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
    MIN_CONFIDENCE,
    MIN_DYNAMIC_POSITION_SIZE,
    MIN_INDICATOR_BONUS,
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

MOMENTUM_WINDOW_POINTS = 5


@dataclass(frozen=True)
class MomentumGate:
    """Result of applying the fixed and ATR-adjusted momentum threshold."""

    passes: bool
    dynamic_threshold: float
    effective_threshold: float


@dataclass(frozen=True)
class HardFilterResult:
    """Result of the reversal-risk hard filter checks."""

    blocked: bool
    reason: str = ""


@dataclass(frozen=True)
class HigherTimeframeAlignment:
    """Result of checking higher-timeframe trend alignment."""

    allowed: bool
    reason: str = ""


@dataclass(frozen=True)
class VoteSummary:
    """Indicator vote totals and extracted values used by strategy outputs."""

    votes: int
    available: int
    supporting: int
    opposing: int
    values: Dict[str, Any]


@dataclass(frozen=True)
class ConfidenceBreakdown:
    """Confidence subtotal components before learner or LLM adjustment."""

    base: float
    indicator_bonus: float
    atr_penalty: float
    confidence: float


# Return usable positive float prices from a recent history sample.
def _clean_price_history(price_history: Optional[Sequence[float]]) -> list[float]:
    """Return finite positive prices that can be used for averaged momentum."""
    clean_prices: list[float] = []
    for price in price_history or []:
        try:
            value = float(price)
        except (TypeError, ValueError):
            continue
        if value > 0 and math.isfinite(value):
            clean_prices.append(value)
    return clean_prices


# Return a finite positive float price when one can be parsed.
def _positive_price(price: Optional[float]) -> Optional[float]:
    """Return a finite positive price value, or None when unusable."""
    try:
        value = float(price) if price is not None else 0.0
    except (TypeError, ValueError):
        return None
    if value > 0 and math.isfinite(value):
        return value
    return None


# Calculate the arithmetic mean for a non-empty price window.
def _window_average(prices: Sequence[float]) -> float:
    """Return the average price for a fixed momentum window."""
    return sum(prices) / len(prices)


# Calculate decimal price momentum from averaged windows when available.
def calculate_momentum(
    current_price: float,
    previous_price: Optional[float],
    price_history: Optional[Sequence[float]] = None,
) -> Optional[float]:
    """Return decimal price momentum, or None when the baseline is unusable."""
    clean_history = _clean_price_history(price_history)
    required_points = MOMENTUM_WINDOW_POINTS * 2
    if len(clean_history) >= required_points:
        baseline_window = clean_history[-required_points:-MOMENTUM_WINDOW_POINTS]
        recent_window = clean_history[-MOMENTUM_WINDOW_POINTS:]
        baseline_average = _window_average(baseline_window)
        if baseline_average <= 0:
            return None
        return (_window_average(recent_window) - baseline_average) / baseline_average

    current = _positive_price(current_price)
    previous = _positive_price(previous_price)
    if current is None or previous is None:
        return None
    return (current - previous) / previous


# Apply the fixed momentum floor and ATR-derived noise threshold.
def evaluate_momentum_gate(momentum: float, atr_pct: float = 0.0) -> MomentumGate:
    """Return whether momentum exceeds the effective movement threshold."""
    dynamic_threshold = (atr_pct / 100) * MOMENTUM_ATR_MULTIPLIER
    effective_threshold = max(dynamic_threshold, MOMENTUM_THRESHOLD)
    return MomentumGate(
        passes=abs(momentum) >= effective_threshold,
        dynamic_threshold=dynamic_threshold,
        effective_threshold=effective_threshold,
    )


# Check whether RSI or Bollinger readings block the proposed direction.
def check_hard_filters(is_long: bool, indicators: Dict[str, Any]) -> HardFilterResult:
    """Return a blocking reason for reversal-prone RSI or BB extremes."""
    rsi_val = indicators.get("rsi_14")
    if rsi_val is not None:
        if is_long and rsi_val >= RSI_LONG_BLOCK:
            return HardFilterResult(True, f"RSI {rsi_val:.1f} overbought")
        if not is_long and rsi_val <= RSI_SHORT_BLOCK:
            return HardFilterResult(True, f"RSI {rsi_val:.1f} oversold")

    bb_pos = (indicators.get("bb") or {}).get("position")
    if bb_pos is not None:
        if is_long and bb_pos >= BB_LONG_BLOCK:
            return HardFilterResult(True, f"BB {bb_pos:.1f}% upper extreme")
        if not is_long and bb_pos <= BB_SHORT_BLOCK:
            return HardFilterResult(True, f"BB {bb_pos:.1f}% lower extreme")

    return HardFilterResult(False)


# Check whether higher-timeframe EMA trend opposes the proposed direction.
def check_higher_timeframe_alignment(
    is_long: bool,
    higher_timeframe: Optional[Dict[str, Any]],
) -> HigherTimeframeAlignment:
    """Return whether higher-timeframe EMA allows the proposed direction."""
    htf_ema_cross = (higher_timeframe or {}).get("ema_cross")
    if not htf_ema_cross or htf_ema_cross == "neutral":
        return HigherTimeframeAlignment(True)

    htf_supports_long = htf_ema_cross == "bullish"
    htf_supports_short = htf_ema_cross == "bearish"
    if (is_long and htf_supports_long) or (not is_long and htf_supports_short):
        return HigherTimeframeAlignment(True)
    return HigherTimeframeAlignment(False, f"higher-timeframe EMA {htf_ema_cross}")


# Count one indicator vote when an indicator has a directional opinion.
def _cast_vote(summary: Dict[str, int], is_long: bool, long_condition: bool, short_condition: bool) -> None:
    """Mutate vote totals for one non-neutral indicator opinion."""
    if not (long_condition or short_condition):
        return

    summary["available"] += 1
    if (is_long and long_condition) or (not is_long and short_condition):
        summary["votes"] += 1
        summary["supporting"] += 1
    elif (is_long and short_condition) or (not is_long and long_condition):
        summary["votes"] -= 1
        summary["opposing"] += 1


# Vote all supported indicators using the shared strategy thresholds.
def vote_indicators(is_long: bool, indicators: Dict[str, Any]) -> VoteSummary:
    """Return indicator vote totals and extracted values for strategy output."""
    totals = {"votes": 0, "available": 0, "supporting": 0, "opposing": 0}

    rsi_val = indicators.get("rsi_14")
    if rsi_val is not None:
        _cast_vote(totals, is_long, rsi_val < RSI_BULLISH_THRESHOLD, rsi_val > RSI_BEARISH_THRESHOLD)

    ema_cross = indicators.get("ema_cross")
    if ema_cross:
        _cast_vote(totals, is_long, ema_cross == "bullish", ema_cross == "bearish")

    bb_pos = (indicators.get("bb") or {}).get("position")
    if bb_pos is not None:
        _cast_vote(totals, is_long, bb_pos < BB_BULLISH_THRESHOLD, bb_pos > BB_BEARISH_THRESHOLD)

    macd_dict = indicators.get("macd") or {}
    macd_bias = macd_dict.get("bias")
    if macd_bias:
        _cast_vote(totals, is_long, macd_bias == "bullish", macd_bias == "bearish")

    sig_bias = macd_dict.get("signal_bias")
    if sig_bias:
        _cast_vote(totals, is_long, sig_bias == "bullish", sig_bias == "bearish")

    stoch_dict = indicators.get("stoch") or {}
    k_val = stoch_dict.get("k")
    if k_val is not None:
        _cast_vote(totals, is_long, k_val < STOCH_BULLISH_THRESHOLD, k_val > STOCH_BEARISH_THRESHOLD)

    wr_val = indicators.get("williams_r")
    if wr_val is not None:
        _cast_vote(totals, is_long, wr_val <= WILLIAMS_BULLISH_THRESHOLD, wr_val >= WILLIAMS_BEARISH_THRESHOLD)

    changes = indicators.get("price_changes") or {}
    chg_5m = changes.get("5m")
    if chg_5m is not None:
        _cast_vote(totals, is_long, chg_5m > 0, chg_5m < 0)

    chg_15m = changes.get("15m")
    if chg_15m is not None:
        _cast_vote(totals, is_long, chg_15m > 0, chg_15m < 0)

    values = {
        "rsi_14": rsi_val,
        "ema_cross": ema_cross,
        "bb_position": bb_pos,
        "macd_bias": macd_bias,
        "macd_signal_bias": sig_bias,
        "macd_histogram": macd_dict.get("histogram"),
        "stoch_k": k_val,
        "stoch_d": stoch_dict.get("d"),
        "williams_r": wr_val,
        "price_change_5m": chg_5m,
        "price_change_15m": chg_15m,
    }
    return VoteSummary(
        votes=totals["votes"],
        available=totals["available"],
        supporting=totals["supporting"],
        opposing=totals["opposing"],
        values=values,
    )


# Calculate base strategy confidence before learner or LLM changes.
def calculate_confidence(
    *,
    momentum: float,
    votes: int,
    available: int,
    atr_pct: float = 0.0,
) -> ConfidenceBreakdown:
    """Return capped confidence and its component values."""
    base = min(abs(momentum) * 100, BASE_CONFIDENCE_CAP)
    indicator_bonus = 0.0
    if available > 0:
        indicator_bonus = max(
            MIN_INDICATOR_BONUS,
            min(MAX_INDICATOR_BONUS, votes * INDICATOR_VOTE_WEIGHT),
        )
    atr_penalty = (
        -min(
            MAX_ATR_PENALTY,
            max(0.0, (atr_pct - ATR_VOLATILITY_THRESHOLD_PCT) * ATR_PENALTY_PER_EXTRA_PCT),
        )
        if atr_pct > ATR_VOLATILITY_THRESHOLD_PCT
        else 0.0
    )
    confidence = max(MIN_CONFIDENCE, min(MAX_CONFIDENCE, base + indicator_bonus + atr_penalty))
    return ConfidenceBreakdown(
        base=base,
        indicator_bonus=indicator_bonus,
        atr_penalty=atr_penalty,
        confidence=confidence,
    )


# Calculate volatility-adjusted position sizing for strategy trade ideas.
def calculate_position_sizing(atr_pct: float) -> float:
    """Return bounded position sizing after ATR volatility adjustment."""
    size = POSITION_SIZING_PROPOSAL
    if atr_pct > 1.0:
        size *= max(0.25, 1.0 / atr_pct)
    return max(MIN_DYNAMIC_POSITION_SIZE, min(MAX_DYNAMIC_POSITION_SIZE, size))
