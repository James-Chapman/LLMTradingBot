"""Deterministic historical replay harness."""

import argparse
import asyncio
import json
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Dict, List, Optional

from analysis.indicators import compute_all
from domain.models import ExecutionIntent, OrderRecord
from execution.paper import PaperExecutionEngine
from ingestion.kraken_adapter import KrakenMarketAdapter
from risk.engine import RiskEngine
from strategy.basic_strategy import BasicStrategy
from strategy.indicator_only_strategy import IndicatorOnlyStrategy
from strategy.llm_only_strategy import LLMOnlyStrategy


@dataclass
class ReplayResult:
    """Structured result from a deterministic replay run."""

    strategy_id: str
    starting_equity: float
    ending_equity: float
    realised_pnl: float
    fees: float
    max_drawdown: float
    trade_count: int
    win_rate: float
    orders: List[OrderRecord] = field(default_factory=list)
    signal_decisions: List[Dict[str, Any]] = field(default_factory=list)
    equity_curve: List[float] = field(default_factory=list)


# Convert a replay result to JSON-serialisable report data.
def replay_result_to_dict(result: ReplayResult) -> Dict[str, Any]:
    """Return a JSON-serialisable replay report."""
    return {
        "summary": {
            "strategy_id": result.strategy_id,
            "starting_equity": result.starting_equity,
            "ending_equity": result.ending_equity,
            "realised_pnl": result.realised_pnl,
            "fees": result.fees,
            "max_drawdown": result.max_drawdown,
            "trade_count": result.trade_count,
            "win_rate": result.win_rate,
        },
        "orders": [
            {
                "id": order.id,
                "market": order.market,
                "direction": order.direction.value,
                "size": order.size,
                "price": order.price,
                "status": order.status,
                "position_id": order.position_id or "",
                "timestamp": order.timestamp.isoformat(),
            }
            for order in result.orders
        ],
        "signals": result.signal_decisions,
        "equity_curve": result.equity_curve,
    }


# Write a replay report to disk as JSON.
def write_replay_report(result: ReplayResult, path: Path) -> None:
    """Write replay report JSON to the requested path."""
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(
        json.dumps(replay_result_to_dict(result), indent=2),
        encoding="utf-8",
    )


# Raise when a caller explicitly requires a profitable replay and it is not profitable.
def enforce_profit_requirement(result: ReplayResult, require_profit: bool) -> None:
    """Validate explicit replay profit acceptance."""
    if require_profit and result.realised_pnl <= 0:
        raise ValueError(
            f"Replay was not profitable: realised_pnl={result.realised_pnl:.6f}"
        )


# Fetch recent Kraken OHLC candles and replay them through the selected strategy.
async def run_live_kraken_replay(
    *,
    market: str,
    hours: int,
    interval: int,
    strategy_id: str,
    starting_capital: float = 500.0,
    require_profit: bool = False,
    analyser=None,
) -> ReplayResult:
    """Run an opt-in live-data replay from Kraken public OHLC data."""
    adapter = KrakenMarketAdapter(None, None)
    candle_limit = int(hours * 60 / interval)
    candles = await adapter.get_ohlc(market, interval=interval, candle_limit=candle_limit)
    result = await run_replay(
        candles_by_market={market: candles},
        strategy_id=strategy_id,
        starting_capital=starting_capital,
        analyser=analyser,
    )
    enforce_profit_requirement(result, require_profit)
    return result


# Return the strategy instance for a replay strategy ID.
def _strategy_for(strategy_id: str):
    """Create the selected strategy for replay."""
    if strategy_id == "indicator_only":
        return IndicatorOnlyStrategy()
    if strategy_id == "combined":
        return BasicStrategy()
    if strategy_id == "llm":
        return LLMOnlyStrategy()
    raise ValueError(f"Unknown strategy_id: {strategy_id}")


# Resample 5-minute candles into 15-minute candles using fixed 3-candle buckets.
def resample_to_15m(candles: List[Dict[str, float]]) -> List[Dict[str, float]]:
    """Return 15-minute OHLC candles derived from 5-minute input candles."""
    result: List[Dict[str, float]] = []
    for idx in range(0, len(candles) - 2, 3):
        group = candles[idx:idx + 3]
        result.append({
            "t": group[-1].get("t", ""),
            "o": group[0]["o"],
            "h": max(c["h"] for c in group),
            "l": min(c["l"] for c in group),
            "c": group[-1]["c"],
            "v": sum(c.get("v", 0.0) for c in group),
        })
    return result


# Compute maximum drawdown from an equity curve.
def _max_drawdown(equity_curve: List[float]) -> float:
    """Return the largest peak-to-trough drawdown as a positive cash value."""
    peak = equity_curve[0] if equity_curve else 0.0
    max_drawdown = 0.0
    for equity in equity_curve:
        peak = max(peak, equity)
        max_drawdown = max(max_drawdown, peak - equity)
    return max_drawdown


# Run a deterministic replay over historical OHLC candles.
async def run_replay(
    *,
    candles_by_market: Dict[str, List[Dict[str, float]]],
    strategy_id: str,
    starting_capital: float = 500.0,
    news_signals: Optional[List[Dict[str, Any]]] = None,
    analyser=None,
) -> ReplayResult:
    """Replay candles through strategy, risk, and paper execution."""
    if not candles_by_market:
        raise ValueError("candles_by_market must not be empty")

    strategy = _strategy_for(strategy_id)
    risk_engine = RiskEngine()
    risk_engine.update_equity(starting_capital)
    paper_engine = PaperExecutionEngine(starting_capital=starting_capital)
    signals: List[Dict[str, Any]] = []
    equity_curve = [starting_capital]
    news = news_signals or []
    length = min(len(candles) for candles in candles_by_market.values())

    for idx in range(length):
        market_data: Dict[str, Any] = {}
        prices: Dict[str, float] = {}
        for market, candles in candles_by_market.items():
            visible = candles[:idx + 1]
            current = visible[-1]
            prices[market] = current["c"]
            closes = [c["c"] for c in visible]
            htf_candles = resample_to_15m(visible)
            market_data[market] = {
                "price": current["c"],
                "previous_price": closes[-11] if len(closes) > 10 else closes[0],
                "volume": current.get("v", 0.0),
                "indicators": compute_all(closes, tick_seconds=300, ohlc_candles=visible),
                "higher_timeframe": (
                    compute_all(
                        [c["c"] for c in htf_candles],
                        tick_seconds=900,
                        ohlc_candles=htf_candles,
                    )
                    if htf_candles else {}
                ),
                "visible_candle_count": len(visible),
            }

        paper_engine.update_mark_prices(prices)
        equity = paper_engine.get_total_equity(prices)
        risk_engine.update_equity(equity)
        equity_curve.append(equity)

        if getattr(strategy, "uses_llm_recommendation", False):
            ideas = await strategy.evaluate(
                market_data,
                news,
                analyser=analyser,
                equity=equity,
                cash=paper_engine.cash,
                open_positions=paper_engine.open_positions(),
            )
        else:
            ideas = await strategy.evaluate(market_data, news)

        for idea in ideas:
            price = prices.get(idea.market)
            if price is None or price <= 0:
                continue
            decision = await risk_engine.evaluate_trade(
                idea,
                open_positions=paper_engine.open_positions(),
                available_cash=paper_engine.cash,
                market_price=price,
                market_volume_24h=market_data[idea.market].get("volume"),
            )
            signals.append({
                "market": idea.market,
                "strategy_id": idea.strategy_id,
                "direction": idea.direction.value,
                "approved": decision.approved,
                "reason": decision.reason,
                "visible_candle_count": market_data[idea.market]["visible_candle_count"],
            })
            if not decision.approved:
                continue
            size = (idea.position_sizing_proposal * equity) / price
            intent = ExecutionIntent(
                approval_request_id="backtest",
                market=idea.market,
                direction=idea.direction,
                size=size,
            )
            await paper_engine.execute(
                intent,
                price,
                strategy_id=idea.strategy_id,
                signal_confidence=idea.confidence,
                environment="paper",
                trade_idea_id=idea.id,
            )

    final_prices = {
        market: candles[-1]["c"]
        for market, candles in candles_by_market.items()
        if candles
    }
    for pos in list(paper_engine.open_positions()):
        await paper_engine.close_position(
            pos.position_id,
            final_prices.get(pos.market, pos.avg_price),
            approval_request_id="backtest_final_close",
        )

    ending_equity = paper_engine.get_total_equity(final_prices)
    equity_curve.append(ending_equity)
    close_orders = [
        order for order in paper_engine.orders
        if order.position_id and order.id != paper_engine.orders[0].id
    ]
    fees = sum(fill.fee for fill in paper_engine.fills)
    realised_pnl = ending_equity - starting_capital
    wins = [order for order in close_orders if realised_pnl > 0]
    win_rate = len(wins) / len(close_orders) if close_orders else 0.0
    return ReplayResult(
        strategy_id=strategy_id,
        starting_equity=starting_capital,
        ending_equity=ending_equity,
        realised_pnl=realised_pnl,
        fees=fees,
        max_drawdown=_max_drawdown(equity_curve),
        trade_count=len(close_orders),
        win_rate=win_rate,
        orders=paper_engine.orders,
        signal_decisions=signals,
        equity_curve=equity_curve,
    )


# Parse CLI arguments for manual replay runs.
def _parse_args() -> argparse.Namespace:
    """Parse command-line arguments for live Kraken replay."""
    parser = argparse.ArgumentParser(description="Run a historical replay backtest.")
    parser.add_argument("--market", default="BTC/EUR")
    parser.add_argument("--hours", type=int, default=48)
    parser.add_argument("--interval", type=int, default=5)
    parser.add_argument("--strategy", default="combined")
    parser.add_argument("--starting-capital", type=float, default=500.0)
    parser.add_argument("--source", choices=["kraken"], default="kraken")
    parser.add_argument("--require-profit", action="store_true")
    parser.add_argument("--report", default="")
    return parser.parse_args()


# Execute the CLI entry point.
async def _main() -> None:
    """Run the requested replay and write a JSON report."""
    args = _parse_args()
    del args.source
    result = await run_live_kraken_replay(
        market=args.market,
        hours=args.hours,
        interval=args.interval,
        strategy_id=args.strategy,
        starting_capital=args.starting_capital,
        require_profit=args.require_profit,
    )
    report_path = Path(args.report) if args.report else Path(
        "docs/backtests"
    ) / (
        f"{datetime.now(timezone.utc).date()}_"
        f"{args.market.replace('/', '-')}_{args.strategy}_{args.hours}h.json"
    )
    write_replay_report(result, report_path)
    print(json.dumps(replay_result_to_dict(result)["summary"], indent=2))


if __name__ == "__main__":
    asyncio.run(_main())
