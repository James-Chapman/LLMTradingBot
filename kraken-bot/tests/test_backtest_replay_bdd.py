"""BDD coverage for historical replay backtesting."""
import json
import unittest
from pathlib import Path
from tempfile import TemporaryDirectory

from bdd_helpers import BACKEND_DIR  # noqa: F401
from backtest.replay import (
    ReplayResult,
    enforce_profit_requirement,
    replay_result_to_dict,
    run_replay,
    write_replay_report,
)
from llm.analyser import LLMTradeRecommendation


class FixtureAnalyser:
    """Deterministic LLM recommendation fixture for replay tests."""

    def __init__(self) -> None:
        self.calls = []

    async def recommend_trade(self, **kwargs):
        self.calls.append(kwargs)
        return LLMTradeRecommendation(
            action="long",
            confidence=0.80,
            sentiment=0.60,
            reasoning="fixture uptrend",
            llm_used=True,
        )


FIXTURE_PATH = (
    Path(__file__).resolve().parent
    / "fixtures"
    / "historical"
    / "btc_eur_5m_profitable_48h.json"
)


# Build deterministic 5-minute candles from the pinned fixture spec.
def _profitable_48h_candles() -> list:
    """Return 576 pinned synthetic 5-minute candles for a profitable 48-hour replay."""
    fixture = json.loads(FIXTURE_PATH.read_text(encoding="utf-8"))
    generator = fixture["generator"]
    candles = []
    price = float(generator["start_price"])
    for idx in range(int(generator["count"])):
        open_price = price
        close_price = price + float(generator["close_step"])
        candles.append({
            "t": f"2026-04-23T{idx:04d}",
            "o": open_price,
            "h": close_price + float(generator["high_offset"]),
            "l": open_price - float(generator["low_offset"]),
            "c": close_price,
            "v": float(generator["volume"]),
        })
        price = close_price
    return candles


class BacktestReplayBDDTests(unittest.IsolatedAsyncioTestCase):
    # GIVEN a pinned 48-hour profitable fixture WHEN replay runs THEN ending equity is profitable.
    async def test_given_profitable_48h_fixture_when_replayed_then_bot_creates_profit(self) -> None:
        analyser = FixtureAnalyser()

        result = await run_replay(
            candles_by_market={"BTC/EUR": _profitable_48h_candles()},
            strategy_id="llm",
            starting_capital=500.0,
            analyser=analyser,
        )

        self.assertGreater(result.ending_equity, result.starting_equity)
        self.assertGreater(result.realised_pnl, 0.0)
        self.assertGreaterEqual(result.trade_count, 1)
        self.assertGreater(result.fees, 0.0)

    # GIVEN the same fixture and settings WHEN replay runs twice THEN results are deterministic.
    async def test_given_same_fixture_when_replayed_twice_then_results_are_identical(self) -> None:
        candles = {"BTC/EUR": _profitable_48h_candles()}

        first = await run_replay(
            candles_by_market=candles,
            strategy_id="llm",
            starting_capital=500.0,
            analyser=FixtureAnalyser(),
        )
        second = await run_replay(
            candles_by_market=candles,
            strategy_id="llm",
            starting_capital=500.0,
            analyser=FixtureAnalyser(),
        )

        self.assertAlmostEqual(first.ending_equity, second.ending_equity)
        self.assertAlmostEqual(first.realised_pnl, second.realised_pnl)
        self.assertEqual(len(first.orders), len(second.orders))
        self.assertEqual(first.signal_decisions, second.signal_decisions)

    # GIVEN historical candles WHEN replay is at a point in time THEN only visible candles are supplied.
    async def test_given_future_candles_when_replay_runs_then_strategy_does_not_look_ahead(self) -> None:
        analyser = FixtureAnalyser()

        result = await run_replay(
            candles_by_market={"BTC/EUR": _profitable_48h_candles()[:40]},
            strategy_id="llm",
            starting_capital=500.0,
            analyser=analyser,
        )

        self.assertTrue(result.signal_decisions)
        self.assertLessEqual(max(signal["visible_candle_count"] for signal in result.signal_decisions), 40)

    # GIVEN a replay result WHEN a report is written THEN the artifact contains summary and ledger data.
    async def test_given_replay_result_when_report_written_then_report_contains_summary_and_orders(self) -> None:
        result = await run_replay(
            candles_by_market={"BTC/EUR": _profitable_48h_candles()},
            strategy_id="llm",
            starting_capital=500.0,
            analyser=FixtureAnalyser(),
        )

        with TemporaryDirectory() as tmp:
            path = Path(tmp) / "report.json"
            write_replay_report(result, path)

            report = replay_result_to_dict(result)
            self.assertTrue(path.exists())
            self.assertEqual(report["summary"]["strategy_id"], "llm")
            self.assertGreater(report["summary"]["ending_equity"], report["summary"]["starting_equity"])
            self.assertGreaterEqual(len(report["orders"]), 2)

    # GIVEN live replay is run without explicit profit mode WHEN P&L is negative THEN it remains a smoke result.
    def test_given_live_smoke_result_when_profit_not_required_then_loss_does_not_raise(self) -> None:
        result = ReplayResult(
            strategy_id="combined",
            starting_equity=500.0,
            ending_equity=499.0,
            realised_pnl=-1.0,
            fees=0.0,
            max_drawdown=1.0,
            trade_count=1,
            win_rate=0.0,
        )

        enforce_profit_requirement(result, require_profit=False)

    # GIVEN live replay is run with require-profit WHEN P&L is negative THEN acceptance fails.
    def test_given_live_acceptance_result_when_profit_required_then_loss_raises(self) -> None:
        result = ReplayResult(
            strategy_id="combined",
            starting_equity=500.0,
            ending_equity=499.0,
            realised_pnl=-1.0,
            fees=0.0,
            max_drawdown=1.0,
            trade_count=1,
            win_rate=0.0,
        )

        with self.assertRaises(ValueError):
            enforce_profit_requirement(result, require_profit=True)


if __name__ == "__main__":
    unittest.main()
