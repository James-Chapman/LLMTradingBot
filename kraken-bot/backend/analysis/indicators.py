"""
Technical indicator calculations from a raw price list (oldest → newest).

All functions are pure Python — no pandas, numpy, or ta-lib required.
Designed for the 30-second tick stream stored in _price_history.

Available indicators
--------------------
rsi(prices, period=14)                       — Wilder RSI
ema_pair(prices, fast=9, slow=21)            — two EMAs + crossover
bollinger_bands(prices, period=20)           — upper/middle/lower/position/width
macd(prices, fast=12, slow=26, signal=9)     — line, signal, histogram, bias
atr(prices, period=14)                       — Average True Range (close-to-close)
stochastic(prices, k_period=14, d_period=3)  — %K, %D, bias
williams_r(prices, period=14)               — Williams %R (-100 to 0)
price_changes(prices, tick_seconds=30)       — 5m / 15m / 30m % change
compute_all(prices, tick_seconds=30)         — all of the above in one dict
"""
import math
from typing import Any, Dict, List, Optional

# ── Primitives ────────────────────────────────────────────────────────────────

def _ema(prices: List[float], period: int) -> Optional[float]:
    """Exponential moving average seeded with the SMA of the first window."""
    if len(prices) < period:
        return None
    k = 2.0 / (period + 1)
    val = sum(prices[:period]) / period       # SMA seed
    for p in prices[period:]:
        val = p * k + val * (1.0 - k)
    return val


def _ema_series(prices: List[float], period: int) -> List[float]:
    """Full EMA series, seeded with the SMA of the first window.

    Returns a list of length max(0, len(prices) - period + 1).
    Element 0 corresponds to prices[period-1] (first computable value).
    """
    if len(prices) < period:
        return []
    k = 2.0 / (period + 1)
    val = sum(prices[:period]) / period       # SMA seed
    result = [val]
    for p in prices[period:]:
        val = p * k + val * (1.0 - k)
        result.append(val)
    return result


def _sma(prices: List[float], period: int) -> Optional[float]:
    if len(prices) < period:
        return None
    return sum(prices[-period:]) / period


# ── Exported indicators ───────────────────────────────────────────────────────

def rsi(prices: List[float], period: int = 14) -> Optional[float]:
    """Wilder RSI using Smoothed Moving Average (SMMA) over the full price history.

    The SMA of the first `period` price changes seeds the smoothing.  Each
    subsequent bar applies the SMMA formula:
        avg = (prev_avg * (period - 1) + new_value) / period
    Using all available history (not just a fixed window) produces values that
    match trading-platform RSI and are materially different from the simple-
    average approximation when the lookback is longer than one period.

    Returns None if there are fewer than period+1 prices.
    """
    if len(prices) < period + 1:
        return None

    # Separate all price changes into gains and losses
    changes = [prices[i] - prices[i - 1] for i in range(1, len(prices))]
    gains  = [max(0.0, c) for c in changes]
    losses = [max(0.0, -c) for c in changes]

    # Seed with the SMA of the first `period` bars
    avg_gain = sum(gains[:period]) / period
    avg_loss = sum(losses[:period]) / period

    # Apply Wilder's exponential smoothing (alpha = 1/period) for all subsequent bars
    for i in range(period, len(changes)):
        avg_gain = (avg_gain * (period - 1) + gains[i]) / period
        avg_loss = (avg_loss * (period - 1) + losses[i]) / period

    if avg_loss == 0:
        return 100.0
    rs = avg_gain / avg_loss
    return round(100.0 - 100.0 / (1.0 + rs), 1)


def ema_pair(prices: List[float], fast: int = 9, slow: int = 21):
    """Returns (fast_ema, slow_ema) or (None, None)."""
    return _ema(prices, fast), _ema(prices, slow)


def bollinger_bands(prices: List[float], period: int = 20) -> Optional[Dict[str, float]]:
    """20-period Bollinger Bands (2 std).  Returns None if not enough data."""
    if len(prices) < period:
        return None
    window = prices[-period:]
    mean = sum(window) / period
    variance = sum((p - mean) ** 2 for p in window) / period
    std = math.sqrt(variance)
    upper = mean + 2 * std
    lower = mean - 2 * std
    current = prices[-1]
    position = (current - lower) / (upper - lower) if upper != lower else 0.5
    width_pct = (upper - lower) / mean * 100 if mean > 0 else 0.0
    return {
        "upper":    round(upper, 2),
        "middle":   round(mean, 2),
        "lower":    round(lower, 2),
        "position": round(max(0.0, min(1.0, position)) * 100, 1),  # 0–100 %
        "width_pct": round(width_pct, 2),
    }


def macd(
    prices: List[float],
    fast: int = 12,
    slow: int = 26,
    signal: int = 9,
) -> Optional[Dict[str, Any]]:
    """MACD line, signal line, and histogram.

    Requires at least slow + signal − 1 = 34 ticks (≈17 min at 30 s/tick).
    Returns None when there is insufficient history.
    """
    if len(prices) < slow + signal - 1:
        return None

    fast_series = _ema_series(prices, fast)   # len = N - fast + 1
    slow_series = _ema_series(prices, slow)   # len = N - slow + 1

    # Align: fast_series[slow-fast] corresponds to the same bar as slow_series[0]
    offset = slow - fast
    if len(fast_series) <= offset:
        return None

    macd_line_series = [
        f - s for f, s in zip(fast_series[offset:], slow_series)
    ]

    if len(macd_line_series) < signal:
        return None

    signal_series = _ema_series(macd_line_series, signal)
    if not signal_series:
        return None

    line_val    = macd_line_series[-1]
    signal_val  = signal_series[-1]
    histogram   = line_val - signal_val

    return {
        "line":         round(line_val, 4),
        "signal":       round(signal_val, 4),
        "histogram":    round(histogram, 4),
        "bias":         "bullish" if line_val > 0 else "bearish",
        "signal_bias":  "bullish" if line_val > signal_val else "bearish",
    }


def atr(prices: List[float], period: int = 14) -> Optional[float]:
    """Average True Range approximated from close prices (no H/L available).

    True Range ≈ |close[i] − close[i−1]|.
    Returns None when there are fewer than period+1 ticks.
    """
    if len(prices) < period + 1:
        return None
    window = prices[-(period + 1):]
    trs = [abs(window[i] - window[i - 1]) for i in range(1, len(window))]
    return round(sum(trs) / period, 6)


def stochastic(
    prices: List[float],
    k_period: int = 14,
    d_period: int = 3,
) -> Optional[Dict[str, Any]]:
    """Stochastic oscillator (%K and %D) approximated from close prices.

    Uses the close price as H/L proxy — the highest/lowest close in the window
    stands in for the true high/low.  Returns None when insufficient data.
    """
    required = k_period + d_period - 1
    if len(prices) < required:
        return None

    k_values: List[float] = []
    for i in range(d_period):
        idx = len(prices) - d_period + i
        window = prices[idx - k_period + 1 : idx + 1]
        if len(window) < k_period:
            return None
        high  = max(window)
        low   = min(window)
        close = window[-1]
        k = ((close - low) / (high - low) * 100.0) if high != low else 50.0
        k_values.append(round(k, 1))

    k_latest = k_values[-1]
    d_latest = round(sum(k_values) / d_period, 1)

    return {
        "k":    k_latest,
        "d":    d_latest,
        "bias": "oversold" if k_latest < 20 else ("overbought" if k_latest > 80 else "neutral"),
    }


def atr_ohlc(
    candles: List[Dict[str, float]],
    period: int = 14,
) -> Optional[float]:
    """Average True Range computed from real OHLC candles.

    Each candle must have keys ``h`` (high), ``l`` (low), and ``c`` (close).
    True Range = max(h-l, |h-prev_c|, |l-prev_c|).
    Returns None if there are fewer than period+1 candles.
    """
    if len(candles) < period + 1:
        return None
    window = candles[-(period + 1):]
    trs: List[float] = []
    for i in range(1, len(window)):
        prev_c = window[i - 1]["c"]
        h = window[i]["h"]
        lo = window[i]["l"]
        tr = max(h - lo, abs(h - prev_c), abs(lo - prev_c))
        trs.append(tr)
    return round(sum(trs) / period, 6)


def stochastic_ohlc(
    candles: List[Dict[str, float]],
    k_period: int = 14,
    d_period: int = 3,
) -> Optional[Dict[str, Any]]:
    """Stochastic oscillator (%K and %D) computed from real OHLC candles.

    Each candle must have keys ``h`` (high), ``l`` (low), and ``c`` (close).
    Returns None when there are fewer than k_period + d_period - 1 candles.
    """
    required = k_period + d_period - 1
    if len(candles) < required:
        return None

    k_values: List[float] = []
    for i in range(d_period):
        idx = len(candles) - d_period + i
        window = candles[idx - k_period + 1 : idx + 1]
        if len(window) < k_period:
            return None
        high  = max(c["h"] for c in window)
        low   = min(c["l"] for c in window)
        close = window[-1]["c"]
        k = ((close - low) / (high - low) * 100.0) if high != low else 50.0
        k_values.append(round(k, 1))

    k_latest = k_values[-1]
    d_latest = round(sum(k_values) / d_period, 1)
    return {
        "k":    k_latest,
        "d":    d_latest,
        "bias": "oversold" if k_latest < 20 else ("overbought" if k_latest > 80 else "neutral"),
    }


def williams_r(prices: List[float], period: int = 14) -> Optional[float]:
    """Williams %R.

    Range: −100 (most oversold) to 0 (most overbought).
    Returns None when there are fewer than period ticks.
    """
    if len(prices) < period:
        return None
    window = prices[-period:]
    high  = max(window)
    low   = min(window)
    close = window[-1]
    if high == low:
        return -50.0
    wr = (high - close) / (high - low) * -100.0
    return round(wr, 1)


def price_changes(prices: List[float], tick_seconds: int = 30) -> Dict[str, float]:
    """Percentage price change vs N minutes ago.  Skips windows with no data."""
    if len(prices) < 2:
        return {}
    current = prices[-1]
    result: Dict[str, float] = {}
    for label, minutes in (("5m", 5), ("15m", 15), ("30m", 30)):
        n = int(minutes * 60 / tick_seconds)
        if len(prices) > n:
            past = prices[-(n + 1)]
            if past > 0:
                result[label] = round((current - past) / past * 100, 2)
    return result


# ── Convenience wrapper ───────────────────────────────────────────────────────

def compute_all(
    prices: List[float],
    tick_seconds: int = 30,
    ohlc_candles: Optional[List[Dict[str, float]]] = None,
) -> Dict[str, Any]:
    """Return every indicator in one flat dict.  Missing indicators are omitted.

    When ``ohlc_candles`` is supplied (list of ``{t, o, h, l, c, v}`` dicts
    from the OHLC cache), ATR and Stochastic are computed from true high/low
    ranges instead of close-price proxies.
    """
    if not prices:
        return {}

    out: Dict[str, Any] = {}

    # Price changes
    changes = price_changes(prices, tick_seconds)
    if changes:
        out["price_changes"] = changes

    # RSI
    rsi_val = rsi(prices)
    if rsi_val is not None:
        out["rsi_14"] = rsi_val
        out["rsi_signal"] = (
            "overbought" if rsi_val >= 70
            else "oversold" if rsi_val <= 30
            else "neutral"
        )

    # EMA crossover
    e9, e21 = ema_pair(prices)
    if e9 is not None and e21 is not None:
        out["ema9"]      = round(e9, 2)
        out["ema21"]     = round(e21, 2)
        out["ema_cross"] = "bullish" if e9 > e21 else "bearish"

    # Bollinger Bands
    bb = bollinger_bands(prices)
    if bb:
        out["bb"] = bb

    # MACD (line + signal + histogram)
    m = macd(prices)
    if m:
        out["macd"] = m

    # ATR — use real OHLC when available, fall back to close-to-close approximation
    if ohlc_candles:
        atr_val = atr_ohlc(ohlc_candles)
    else:
        atr_val = atr(prices)
    if atr_val is not None:
        out["atr"] = atr_val
        out["atr_pct"] = round(atr_val / prices[-1] * 100, 3) if prices[-1] > 0 else None

    # Stochastic — use real OHLC when available, fall back to close-price proxy
    if ohlc_candles:
        stoch = stochastic_ohlc(ohlc_candles)
    else:
        stoch = stochastic(prices)
    if stoch:
        out["stoch"] = stoch

    # Williams %R
    wr = williams_r(prices)
    if wr is not None:
        out["williams_r"] = wr
        out["williams_r_signal"] = (
            "oversold"   if wr <= -80
            else "overbought" if wr >= -20
            else "neutral"
        )

    return out
