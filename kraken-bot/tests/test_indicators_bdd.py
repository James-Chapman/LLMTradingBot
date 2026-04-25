"""BDD coverage for pure technical indicator calculations."""
import unittest

from bdd_helpers import BACKEND_DIR  # noqa: F401
from analysis.indicators import _ema_series, atr_ohlc, compute_all, macd, price_changes, rsi, stochastic_ohlc


class IndicatorBDDTests(unittest.TestCase):
    # GIVEN too little price history WHEN indicators are computed THEN unavailable values are omitted.
    def test_given_short_price_history_when_compute_all_runs_then_only_available_indicators_return(self) -> None:
        result = compute_all([100.0, 101.0, 102.0])

        self.assertEqual(result, {})

    # GIVEN continuous gains WHEN RSI is computed THEN the market is reported as fully overbought.
    def test_given_all_gains_when_rsi_computed_then_rsi_is_one_hundred(self) -> None:
        prices = [float(price) for price in range(100, 116)]

        result = rsi(prices)

        self.assertEqual(result, 100.0)

    # GIVEN enough 30 second ticks WHEN price changes are computed THEN the named windows are returned.
    def test_given_price_history_when_price_changes_run_then_supported_windows_are_reported(self) -> None:
        prices = [100.0] * 31
        prices[-11] = 100.0
        prices[-1] = 105.0

        result = price_changes(prices, tick_seconds=30)

        self.assertEqual(result["5m"], 5.0)
        self.assertIn("15m", result)


    # GIVEN a mixed gain/loss series long enough to benefit from SMMA
    # WHEN rsi is computed THEN the result uses exponential smoothing not a simple window.
    def test_given_mixed_series_when_rsi_computed_then_smma_smoothing_is_applied(self) -> None:
        # Build a 30-price series: 14 gains of 1, then 15 losses of 1, then 1 gain.
        # SMA-based RSI would calculate entirely from the last 15 prices (all losses + 1 gain).
        # Wilder's SMMA carries forward the initial avg_gain, so RSI should be > 0.
        prices = [100.0 + i for i in range(15)]          # 14 gains of 1
        prices += [prices[-1] - i for i in range(1, 16)] # 15 losses of 1
        prices.append(prices[-1] + 1.0)                  # 1 final gain

        result = rsi(prices)

        # With pure-window SMA (last 15 prices = 14 losses + 1 gain): RSI ≈ 6.7
        # With Wilder SMMA using full history: RSI should be noticeably higher
        self.assertIsNotNone(result)
        self.assertGreater(result, 10.0,
                           "SMMA-smoothed RSI should be higher than the simple-window result")


    # GIVEN OHLC candles with meaningful high-low ranges WHEN atr_ohlc is computed
    # THEN the result reflects the true range, not close-to-close deltas.
    def test_given_ohlc_candles_when_atr_ohlc_computed_then_result_uses_true_range(self) -> None:
        # Each candle has a high-low range of 10, but close-to-close delta is 0
        candles = [{"h": 110.0, "l": 100.0, "c": 105.0} for _ in range(15)]

        result = atr_ohlc(candles)

        self.assertIsNotNone(result)
        # True range is 10 (h - l); close-to-close would give 0
        self.assertGreater(result, 5.0, "ATR from OHLC must reflect actual high-low range")

    # GIVEN OHLC candles where close is at the high WHEN stochastic_ohlc is computed
    # THEN %K is close to 100 (overbought).
    def test_given_close_at_high_when_stochastic_ohlc_computed_then_k_near_100(self) -> None:
        # Close equals the high — fully overbought
        candles = [{"h": 110.0, "l": 90.0, "c": 110.0} for _ in range(17)]

        result = stochastic_ohlc(candles)

        self.assertIsNotNone(result)
        self.assertGreater(result["k"], 90.0, "%K should be near 100 when close = high")

    # GIVEN flat tick closes and ranged OHLC candles WHEN all indicators are computed
    # THEN ATR and Stochastic use OHLC ranges instead of flat close-price proxies.
    def test_given_ohlc_candles_when_compute_all_runs_then_range_indicators_use_candles(self) -> None:
        prices = [105.0 for _ in range(40)]
        candles = [{"h": 110.0, "l": 90.0, "c": 110.0} for _ in range(17)]

        result = compute_all(prices, ohlc_candles=candles)

        self.assertGreater(result["atr"], 5.0)
        self.assertGreater(result["stoch"]["k"], 90.0)


    # GIVEN a valid price series WHEN MACD is computed THEN the fast and slow EMA series
    # align exactly so the zip produces no silent truncation (BUG-014).
    def test_given_valid_prices_when_macd_computed_then_fast_slow_series_lengths_match(self) -> None:
        # 50 prices gives fast_series len=39 (50-12+1), slow_series len=25 (50-26+1)
        # offset = slow-fast = 14; fast_series[14:] len = 39-14 = 25 == slow_series len
        prices = [100.0 + i * 0.5 for i in range(50)]
        fast = 12
        slow = 26

        fast_series = _ema_series(prices, fast)
        slow_series = _ema_series(prices, slow)
        offset = slow - fast
        aligned_fast = fast_series[offset:]

        self.assertEqual(
            len(aligned_fast), len(slow_series),
            "fast_series[offset:] and slow_series must have equal length so zip is lossless",
        )

    # GIVEN a price series long enough for MACD WHEN macd() is called
    # THEN a valid result is returned with no silent value truncation.
    def test_given_sufficient_prices_when_macd_called_then_result_is_complete(self) -> None:
        prices = [100.0 + i * 0.3 for i in range(40)]

        result = macd(prices)

        self.assertIsNotNone(result)
        self.assertIn("line", result)
        self.assertIn("signal", result)
        self.assertIn("histogram", result)


if __name__ == "__main__":
    unittest.main()
