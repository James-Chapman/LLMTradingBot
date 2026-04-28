"""
BDD tests for analysis/indicators.py (T2.Q7.2).

Tests cover: rsi, ema_pair, bollinger_bands, macd, atr, stochastic,
williams_r, price_changes, compute_all.
"""

import pytest
from analysis.indicators import (
    atr,
    bollinger_bands,
    compute_all,
    ema_pair,
    macd,
    price_changes,
    rsi,
    stochastic,
    williams_r,
)


# ── Helpers ───────────────────────────────────────────────────────────────────

def _flat(value: float, n: int) -> list:
    return [value] * n


def _trending_up(start: float, step: float, n: int) -> list:
    return [start + i * step for i in range(n)]


# ── RSI ──────────────────────────────────────────────────────────────────────

class TestRSIBDD:
    def test_given_insufficient_prices_when_rsi_called_then_returns_none(self):
        """GIVEN fewer than 15 prices, WHEN rsi() is called, THEN result is None."""
        assert rsi([100.0] * 14) is None

    def test_given_flat_prices_when_rsi_called_then_returns_100(self):
        """
        GIVEN all identical prices (zero gains, zero losses), WHEN rsi() is called,
        THEN result is 100 (no losses branch returns 100).
        """
        result = rsi(_flat(100.0, 50))
        assert result == pytest.approx(100.0, abs=0.1)

    def test_given_all_gains_when_rsi_called_then_returns_near_100(self):
        """GIVEN only up-moves, WHEN rsi() is called, THEN result is near 100."""
        result = rsi(_trending_up(100.0, 1.0, 50))
        assert result is not None
        assert result > 80

    def test_given_all_losses_when_rsi_called_then_returns_near_0(self):
        """GIVEN only down-moves, WHEN rsi() is called, THEN result is near 0."""
        result = rsi(list(reversed(_trending_up(100.0, 1.0, 50))))
        assert result is not None
        assert result < 20


# ── EMA pair ─────────────────────────────────────────────────────────────────

class TestEMAPairBDD:
    def test_given_insufficient_prices_when_ema_pair_called_then_both_none(self):
        """GIVEN fewer than 21 prices, WHEN ema_pair() is called, THEN slow EMA is None."""
        fast, slow = ema_pair([100.0] * 20)
        assert slow is None

    def test_given_uptrend_when_ema_pair_called_then_fast_above_slow(self):
        """GIVEN a sustained uptrend, WHEN ema_pair() is called, THEN fast EMA > slow EMA."""
        fast, slow = ema_pair(_trending_up(100.0, 1.0, 60))
        assert fast is not None and slow is not None
        assert fast > slow

    def test_given_downtrend_when_ema_pair_called_then_fast_below_slow(self):
        """GIVEN a sustained downtrend, WHEN ema_pair() is called, THEN fast EMA < slow EMA."""
        fast, slow = ema_pair(list(reversed(_trending_up(100.0, 1.0, 60))))
        assert fast is not None and slow is not None
        assert fast < slow


# ── Bollinger Bands ───────────────────────────────────────────────────────────

class TestBollingerBandsBDD:
    def test_given_insufficient_prices_when_bb_called_then_returns_none(self):
        """GIVEN fewer than 20 prices, WHEN bollinger_bands() is called, THEN result is None."""
        assert bollinger_bands([100.0] * 19) is None

    def test_given_flat_prices_when_bb_called_then_position_is_50(self):
        """GIVEN flat prices (zero spread), WHEN bollinger_bands() is called, THEN position is 50."""
        result = bollinger_bands(_flat(100.0, 30))
        assert result is not None
        assert result["position"] == pytest.approx(50.0, abs=1.0)

    def test_given_enough_prices_when_bb_called_then_middle_equals_sma(self):
        """GIVEN 30 prices, WHEN bollinger_bands() is called, THEN middle == mean of last 20."""
        prices = _trending_up(90.0, 1.0, 30)
        result = bollinger_bands(prices)
        expected_middle = sum(prices[-20:]) / 20
        assert result is not None
        assert result["middle"] == pytest.approx(expected_middle, rel=1e-4)


# ── MACD ─────────────────────────────────────────────────────────────────────

class TestMACDBDD:
    def test_given_insufficient_prices_when_macd_called_then_returns_none(self):
        """GIVEN fewer than 34 prices (slow+signal-1), WHEN macd() is called, THEN result is None."""
        # Threshold: len < 26 + 9 - 1 = 34
        assert macd([100.0] * 33) is None

    def test_given_enough_flat_prices_when_macd_called_then_returns_dict(self):
        """GIVEN 34+ flat prices, WHEN macd() is called, THEN result is a dict with required keys."""
        result = macd([100.0] * 40)
        assert result is not None
        assert "line" in result and "signal" in result and "histogram" in result

    def test_given_uptrend_when_macd_called_then_line_positive(self):
        """GIVEN sustained uptrend with 60 prices, WHEN macd() is called, THEN line > 0."""
        result = macd(_trending_up(100.0, 1.0, 60))
        assert result is not None
        assert result["line"] > 0


# ── ATR ───────────────────────────────────────────────────────────────────────

class TestATRBDD:
    def test_given_insufficient_prices_when_atr_called_then_returns_none(self):
        """GIVEN fewer than 15 prices, WHEN atr() is called, THEN result is None."""
        assert atr([100.0] * 14) is None

    def test_given_volatile_prices_when_atr_called_then_positive_float(self):
        """GIVEN alternating high/low prices, WHEN atr() is called, THEN result > 0."""
        prices = [100.0 + (5.0 if i % 2 == 0 else -5.0) for i in range(30)]
        result = atr(prices)
        assert result is not None
        assert isinstance(result, float)
        assert result > 0

    def test_given_flat_prices_when_atr_called_then_returns_zero(self):
        """GIVEN perfectly flat prices, WHEN atr() is called, THEN result is 0."""
        result = atr(_flat(100.0, 20))
        assert result == pytest.approx(0.0, abs=1e-9)


# ── Stochastic ────────────────────────────────────────────────────────────────

class TestStochasticBDD:
    def test_given_insufficient_prices_when_stochastic_called_then_returns_none(self):
        """GIVEN fewer than 16 prices (k_period+d_period-1), WHEN stochastic() is called, THEN None."""
        # Required: 14 + 3 - 1 = 16
        assert stochastic([100.0] * 15) is None

    def test_given_prices_at_period_high_when_stochastic_called_then_k_near_100(self):
        """GIVEN prices ending well above the window low, WHEN stochastic() is called, THEN k=100."""
        prices = _flat(100.0, 15) + [200.0]  # 16 prices; last is much higher
        result = stochastic(prices)
        assert result is not None
        assert result["k"] == pytest.approx(100.0, abs=1.0)

    def test_given_prices_at_period_low_when_stochastic_called_then_k_near_0(self):
        """GIVEN prices ending well below the window high, WHEN stochastic() is called, THEN k=0."""
        prices = _flat(200.0, 15) + [100.0]
        result = stochastic(prices)
        assert result is not None
        assert result["k"] == pytest.approx(0.0, abs=1.0)


# ── Williams %R ───────────────────────────────────────────────────────────────

class TestWilliamsRBDD:
    def test_given_insufficient_prices_when_williams_r_called_then_returns_none(self):
        """GIVEN fewer than 14 prices, WHEN williams_r() is called, THEN result is None."""
        assert williams_r([100.0] * 13) is None

    def test_given_prices_at_period_high_when_williams_r_called_then_near_zero(self):
        """GIVEN last price equals period high, WHEN williams_r() is called, THEN result near 0."""
        prices = _flat(100.0, 13) + [110.0]
        result = williams_r(prices)
        assert result is not None
        assert result == pytest.approx(0.0, abs=1.0)

    def test_given_flat_prices_when_williams_r_called_then_returns_minus_50(self):
        """GIVEN all identical prices (high == low), WHEN williams_r() is called, THEN -50."""
        result = williams_r(_flat(100.0, 20))
        assert result == pytest.approx(-50.0, abs=0.1)


# ── price_changes ─────────────────────────────────────────────────────────────

class TestPriceChangesBDD:
    def test_given_30_prices_when_price_changes_called_then_5m_change_present(self):
        """GIVEN 31 prices (> 10 ticks), WHEN price_changes(tick_seconds=30) is called, THEN '5m' key present."""
        prices = _trending_up(100.0, 0.1, 31)
        result = price_changes(prices, tick_seconds=30)
        assert "5m" in result
        assert result["5m"] > 0

    def test_given_insufficient_prices_when_price_changes_called_then_empty_dict(self):
        """GIVEN fewer than 11 prices, WHEN price_changes(30s) is called, THEN empty dict."""
        result = price_changes([100.0] * 5, tick_seconds=30)
        assert result == {}


# ── compute_all ───────────────────────────────────────────────────────────────

class TestComputeAllBDD:
    def test_given_50_prices_when_compute_all_called_then_dict_has_standard_keys(self):
        """GIVEN 50 prices, WHEN compute_all() is called, THEN rsi_14, ema_cross, bb, macd keys present."""
        prices = _trending_up(100.0, 0.5, 50)
        result = compute_all(prices)
        for key in ("rsi_14", "ema_cross", "bb", "macd"):
            assert key in result, f"Missing key: {key}"

    def test_given_empty_prices_when_compute_all_called_then_returns_empty_dict(self):
        """GIVEN empty price list, WHEN compute_all() is called, THEN returns empty dict."""
        assert compute_all([]) == {}

    def test_given_single_price_when_compute_all_called_then_no_exception(self):
        """GIVEN a single price, WHEN compute_all() is called, THEN no exception raised."""
        result = compute_all([100.0])
        assert isinstance(result, dict)
