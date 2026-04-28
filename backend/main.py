"""
Kraken News-Aware Trading Bot - FastAPI Backend
"""

import asyncio
import csv
import io
from collections import defaultdict, deque
from contextlib import asynccontextmanager
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Dict, List

from bootstrap import ensure_project_venv

ensure_project_venv(__file__)

try:
    import httpx
    import uvicorn
    from fastapi import FastAPI, HTTPException, Query, Request
    from fastapi.middleware.cors import CORSMiddleware
    from fastapi.responses import FileResponse, Response
    from fastapi.staticfiles import StaticFiles

    from analysis.indicators import compute_all as compute_indicators
    from approval.service import ApprovalService
    from config.currency import currency_symbol
    from config.settings import settings
    from control.state import LEGACY_STRATEGY_IDS, ControlState
    from domain.models import ExecutionIntent, MarketSnapshot, TradeIdea
    from execution.kraken import KrakenExecutionEngine
    from execution.operator_reset import close_positions_for_operator_reset
    from execution.paper import PaperExecutionEngine
    from ingestion.kraken_adapter import KrakenMarketAdapter
    from ingestion.news_adapter import (
        BitcoinMagazineAdapter,
        CoinDeskAdapter,
        CoinTelegraphAdapter,
        CryptoPotaroAdapter,
        CryptoSlateAdapter,
        DecryptAdapter,
        FearGreedAdapter,
        NewsBTCAdapter,
        TheBlockAdapter,
        TheDefiantAdapter,
        normalise_news_item,
    )
    from llm.analyser import LLMAnalyser, SignalAnalysis
    from llm.transformers_client import TransformersClient
    from observability.activity import activity
    from observability.logging import get_logger, setup_logging
    from portfolio.equity import align_equity_history_with_current, build_equity_snapshot
    from risk.engine import STOP_LOSS_ASSUMPTION, RiskEngine
    from risk.persistence import record_trade_result_and_persist
    from storage.database import init_database
    from storage.repository import Repository
    from strategy.basic_and_llm_strategy import BasicAndLLMStrategy
    from strategy.basic_strategy import BasicStrategy
    from strategy.learner import PerformanceLearner
    from strategy.llm_only_strategy import LLMOnlyStrategy
    from universe.resolver import UniverseResolver
except ModuleNotFoundError as exc:
    from bootstrap import dependency_error_message, restart_with_project_venv

    restart_with_project_venv(__file__)
    raise SystemExit(dependency_error_message(exc, __file__)) from exc

BACKEND_DIR = Path(__file__).parent
FRONTEND_DIR = BACKEND_DIR.parent / "frontend"

setup_logging(settings.log_level, settings.log_file)
logger = get_logger("main")
CURRENCY_SYMBOL = currency_symbol(settings.base_currency)

init_database(settings.database_url)

repo = Repository()
learner = PerformanceLearner()
_llm_client = TransformersClient(settings.transformers_llm_model, settings.transformers_timeout)
_analyser = LLMAnalyser(_llm_client)

kraken_adapter = KrakenMarketAdapter(settings.kraken_api_key, settings.kraken_api_secret)
universe_resolver = UniverseResolver(settings.fixed_markets, settings.dynamic_universe_source)
basic_strategy = BasicStrategy()
basic_and_llm_strategy = BasicAndLLMStrategy()
llm_only_strategy = LLMOnlyStrategy()
strategies = [basic_strategy, basic_and_llm_strategy, llm_only_strategy]
STRATEGY_LABELS = {
    "basic_strategy": "Basic Strategy (Just market indicators, no LLM)",
    "basic_and_llm_strategy": "Basic + LLM Combined (LLM analysis augments indicator-based signals)",
    "llm_only_strategy": "All LLM (LLM generates signals from price, indicators, and news — no indicator consensus gate)",
}
risk_engine = RiskEngine()
approval_service = ApprovalService(repository=repo)
paper_engine = PaperExecutionEngine(settings.starting_capital, repository=repo)
kraken_engine = KrakenExecutionEngine(
    settings.kraken_api_key,
    settings.kraken_api_secret,
    repository=repo,
)
control = ControlState()
news_adapters = [
    CoinDeskAdapter(),
    CoinTelegraphAdapter(),
    TheBlockAdapter(),
    DecryptAdapter(),
    BitcoinMagazineAdapter(),
    CryptoSlateAdapter(),
    TheDefiantAdapter(),
    CryptoPotaroAdapter(),
    NewsBTCAdapter(),
    FearGreedAdapter(),
]


def _reload_strategy_instances() -> List[str]:
    """Rebuild strategy instances from current settings-backed defaults."""
    global basic_strategy, basic_and_llm_strategy, llm_only_strategy, strategies
    basic_strategy = BasicStrategy()
    basic_and_llm_strategy = BasicAndLLMStrategy()
    llm_only_strategy = LLMOnlyStrategy()
    strategies = [basic_strategy, basic_and_llm_strategy, llm_only_strategy]
    return [strategy.strategy_id for strategy in strategies]


def _strategy_by_id(strategy_id: str):
    """Return a registered strategy instance by ID."""
    canonical_id = LEGACY_STRATEGY_IDS.get(strategy_id, strategy_id)
    return next((strategy for strategy in strategies if strategy.strategy_id == canonical_id), None)


# Wire repo into singletons that need DB persistence.
activity.set_repo(repo)
control.set_repo(repo)
_analyser.set_repo(repo)
approval_service.load_pending_from_repository()

# In-memory state updated by the background loop
_latest_signals: List[Dict[str, Any]] = []  # rolling buffer, newest first
_current_prices: Dict[str, float] = {}
# Monotonic counter incremented whenever dashboard-visible state changes.
# The dashboard endpoint returns this as an ETag so the browser can skip
# parsing when nothing has changed.
_dashboard_version: int = 0
_latest_market_snapshots: Dict[str, MarketSnapshot] = {}
_active_markets: List[str] = []

_SIGNAL_BUFFER_MAX = 12  # maximum signals kept in the panel

# Rolling risk rejections - last 50, newest first; pre-loaded from DB.
# get_recent_risk_rejections returns newest-first; extend from oldest so the
# deque ends up with index-0 = newest (same order appendleft produces at runtime).
_risk_rejections: deque = deque(maxlen=50)
_db_rejections = repo.get_recent_risk_rejections(limit=50)
for _r in reversed(_db_rejections):  # reversed = oldest first into appendleft order
    _risk_rejections.appendleft(_r)

# News IDs that have already been sent to the LLM for a market briefing.
# Pre-populate with ALL news IDs currently in the DB so the first news-loop
# cycle doesn't re-brief on articles that were already processed before restart.
_briefed_news_ids: set = set()

# OHLC caches per timeframe: symbol to payload dict (refreshed by background task)
_ohlc_cache_5: Dict[str, dict] = {}  # 5-min candles
_ohlc_cache_15: Dict[str, dict] = {}  # 15-min candles
_OHLC_REFRESH_INTERVAL = 120  # seconds between full refresh cycles
_OHLC_INTER_MARKET_GAP = 2.5  # seconds between markets to respect Kraken rate limits
_OHLC_INTER_INTERVAL_GAP = 1.0  # seconds between the two interval fetches for the same market

# Rolling price history: deque per market (30 s ticks, max 200 = ~100 min window).
# _LOOKBACK_TICKS must be >= slow_ema + signal_period - 1 = 34 so that MACD is
# computable before the first strategy evaluation.  200-bar buffer lets Wilder
# SMMA (RSI) warm up well past its 14-bar seed window.
_LOOKBACK_TICKS = 34
# Number of live ticks that must pass before any new trade is opened after startup.
_TRADE_WARMUP_TICKS = 20
_session_tick: int = 0  # updated each tick; readable by dashboard endpoint

_price_history: Dict[str, deque] = defaultdict(lambda: deque(maxlen=200))

# Equity snapshots for chart (max 1 440 = 4 h at 10-s resolution) - pre-loaded from DB
_equity_history: deque = deque(maxlen=1440)
_current_equity: float = settings.starting_capital  # updated every 10 s by equity ticker
_db_equity = repo.get_equity_history(limit=1440)
if _db_equity:
    _equity_history.extend(_db_equity)
    _current_equity = _db_equity[-1]["equity"]  # seed from most recent DB snapshot
    # Restore the cash balance; positions are restored separately.
    _last_cash = repo.get_latest_cash()
    if _last_cash is not None:
        paper_engine.cash = _last_cash
else:
    _equity_history.append({"timestamp": datetime.now(timezone.utc).isoformat(), "equity": settings.starting_capital})

# Restore open positions from DB
paper_engine.restore_from_db()

# Restore risk engine daily tracking to keep the daily loss limit after restart.
_risk_state = repo.load_risk_state()
if _risk_state and _risk_state["last_reset_date"] == datetime.now(timezone.utc).date():
    risk_engine.daily_loss = _risk_state["daily_loss"]
    risk_engine.daily_start_equity = _risk_state["daily_start_equity"]

# Seed news cache from DB - 60 most recent articles regardless of age.
_latest_news: List[Dict[str, Any]] = repo.get_recent_news(limit=60)
# Mark all existing news as already-briefed so the first news-loop cycle doesn't
# re-brief on articles processed before this restart.
_briefed_news_ids.update(n["id"] for n in _latest_news)

# Seed learner from historical outcomes
learner.load_from_outcomes(repo.get_signal_outcomes(limit=1_000))

# Restore control toggles and LLM state from DB
control.load_from_db()
_analyser.load_from_db()

# Seed activity log from DB so recent history is visible immediately
activity.seed_from_db()


async def _async_db(fn, *args, **kwargs):
    """Run a synchronous repository call in a thread so the event loop stays free."""
    return await asyncio.to_thread(fn, *args, **kwargs)


def _record_trade_result(pnl: float) -> None:
    """Record a completed trade's P&L and persist the risk state to DB.

    Persistence ensures the daily loss limit survives a restart; without it
    the bot could exceed the configured limit by restarting mid-day.
    """
    record_trade_result_and_persist(risk_engine, repo, pnl)


# Return a three-state LLM status string for the dashboard.
def _llm_status() -> str:
    """Return 'not_configured', 'available', or 'unavailable'."""
    if not settings.transformers_llm_model:
        return "not_configured"
    return "available" if _llm_client.available else "unavailable"


# Safely extract the numeric LLM briefing score for a market.
def _briefing_sentiment_for_market(briefing: Any, market: str) -> float:
    """Return the market briefing score, falling back to neutral for malformed data."""
    if briefing is None:
        return 0.0
    outlooks = getattr(briefing, "market_outlooks", {}) or {}
    if not isinstance(outlooks, dict):
        return 0.0
    outlook = outlooks.get(market, {})
    if not isinstance(outlook, dict):
        return 0.0
    try:
        return max(-1.0, min(1.0, float(outlook.get("score", 0.0) or 0.0)))
    except (TypeError, ValueError):
        return 0.0


# Record a mark-to-market portfolio value point for dashboard graphing.
def _record_equity_snapshot(prices: Dict[str, float]) -> Dict[str, Any]:
    """Compute, cache, and persist cash plus current position value."""
    global _current_equity
    snapshot = build_equity_snapshot(paper_engine, prices)
    _current_equity = float(snapshot["equity"])
    risk_engine.update_equity(_current_equity)
    _equity_history.append(
        {
            "timestamp": str(snapshot["timestamp"]),
            "equity": _current_equity,
        }
    )
    repo.save_equity_snapshot(
        _current_equity,
        float(snapshot["cash"]),
        float(snapshot["positions_value"]),
    )
    return snapshot


# Cache the latest streamed market snapshot for strategy ticks.
# asyncio is single-threaded and cooperative — direct dict mutation from a
# callback in the same event loop is safe without a lock.
def _cache_market_snapshot(snapshot: MarketSnapshot) -> None:
    """Store the most recent ticker snapshot from Kraken streaming."""
    _latest_market_snapshots[snapshot.symbol] = snapshot
    _current_prices[snapshot.symbol] = snapshot.price


async def _send_alert(event_type: str, payload: Dict[str, Any]) -> None:
    """Post a JSON alert to the configured webhook URL, if one is set."""
    if not settings.alert_webhook_url:
        return
    try:
        async with httpx.AsyncClient(timeout=5) as client:
            await client.post(
                settings.alert_webhook_url,
                json={
                    "event_type": event_type,
                    "timestamp": datetime.now(timezone.utc).isoformat(),
                    **payload,
                },
            )
    except Exception as exc:
        logger.warning("Alert webhook failed", extra={"event_type": event_type, "error": str(exc)})


async def _strategy_loop() -> None:
    """Background task: poll market data, run strategy, and act based on trading mode."""
    global _latest_signals, _current_prices, _active_markets, _session_tick, _dashboard_version

    universe = await universe_resolver.resolve_universe()
    all_markets = universe.fixed_markets + universe.dynamic_markets
    _active_markets = await kraken_adapter.validate_symbols(all_markets)

    # Restore rolling price history from DB so momentum is available immediately
    for sym in _active_markets:
        saved = repo.get_recent_prices(sym, limit=60)
        for p in saved:
            _price_history[sym].append(p)
    if any(len(_price_history[m]) for m in _active_markets):
        activity.info(
            "Price history restored from DB",
            " | ".join(f"{m}: {len(_price_history[m])} ticks" for m in _active_markets),
        )

    activity.info(
        f"Bot started - {len(_active_markets)} markets active",
        f"Markets: {', '.join(_active_markets)} | Mode: {settings.trading_mode}",
    )
    await _send_alert("bot_started", {"markets": _active_markets, "mode": settings.trading_mode})
    logger.info("Active markets confirmed", extra={"markets": _active_markets})
    kraken_adapter.start_subscription(_active_markets, _cache_market_snapshot)

    tick = 0
    while True:
        _tick_start = asyncio.get_event_loop().time()
        try:
            if control.emergency_stop:
                activity.warn("Emergency stop active - tick skipped")
                await asyncio.sleep(30)
                continue

            # Only evaluate enabled markets.
            active = [m for m in _active_markets if control.is_market_enabled(m)]
            if not active:
                activity.warn("All markets disabled - tick skipped")
                await asyncio.sleep(30)
                continue

            snapshots = {sym: _latest_market_snapshots[sym] for sym in active if sym in _latest_market_snapshots}
            missing = [sym for sym in active if sym not in snapshots]
            if missing:
                snapshots.update(await kraken_adapter.get_tickers_batch(missing))
            if not snapshots:
                activity.warn("No market prices available - tick skipped")
                await asyncio.sleep(30)
                continue
            prices = {sym: snap.price for sym, snap in snapshots.items()}

            for sym, price in prices.items():
                _price_history[sym].append(price)

            tick += 1
            _session_tick = tick
            ticks_collected = min(len(next(iter(_price_history.values()), [])), _LOOKBACK_TICKS + 1)
            warming_up = ticks_collected <= _LOOKBACK_TICKS

            price_summary = "  ".join(f"{s} {CURRENCY_SYMBOL}{p:,.2f}" for s, p in prices.items())
            if warming_up:
                activity.info(
                    f"Tick #{tick} - prices fetched (warming up {ticks_collected}/{_LOOKBACK_TICKS + 1} ticks)",
                    price_summary,
                )
            else:
                activity.info(f"Tick #{tick} - prices fetched", price_summary)

            market_data: Dict[str, Any] = {}
            for sym, snap in snapshots.items():
                hist = _price_history[sym]
                # previous_price is settings.momentum_lookback_ticks back for momentum.
                _lb = settings.momentum_lookback_ticks + 1
                prev = hist[-_lb] if len(hist) >= _lb else snap.price
                # Pass 5-min OHLC candles when available so ATR/Stochastic use true H/L
                ohlc = _ohlc_cache_5.get(sym, {}).get("candles")
                ohlc_15 = _ohlc_cache_15.get(sym, {}).get("candles") or []
                htf_indicators = (
                    compute_indicators(
                        [c["c"] for c in ohlc_15 if "c" in c],
                        tick_seconds=900,
                        ohlc_candles=ohlc_15,
                    )
                    if ohlc_15
                    else {}
                )
                market_data[sym] = {
                    "price": snap.price,
                    "previous_price": prev,
                    "volume": snap.volume,
                    "llm_sentiment": _briefing_sentiment_for_market(_analyser.latest_briefing, sym),
                    "higher_timeframe": htf_indicators,
                    "indicators": compute_indicators(list(hist), ohlc_candles=ohlc),
                }

            _current_prices = prices
            _dashboard_version += 1
            paper_engine.update_mark_prices(prices)
            paper_engine.update_trailing_prices(prices)

            # Persist price ticks and portfolio value for this market-data tick.
            for sym, price in prices.items():
                repo.save_price_tick(sym, price)
            _record_equity_snapshot(prices)

            # Trim old price ticks, equity snapshots, and activity log periodically
            # (every ~5 min = 10 ticks).  Offloaded to a thread: DELETE+subquery scans
            # can take 5–50 ms on a busy DB.
            if tick % 10 == 0:
                for sym in active:
                    await _async_db(repo.trim_old_price_ticks, sym)
                await _async_db(repo.trim_old_equity_snapshots)
                await _async_db(repo.trim_old_activity, keep=2_000)

            # Stop-loss check: auto-close any position down at least 5%.
            for pos in paper_engine.open_positions():
                market_price = prices.get(pos.market)
                if market_price is None:
                    continue
                loss_pct = (
                    (pos.avg_price - market_price) / pos.avg_price
                    if pos.size > 0
                    else (market_price - pos.avg_price) / pos.avg_price
                )
                # Calculate hit conditions
                stop_hit = paper_engine.stop_loss_triggered(pos.position_id, market_price, STOP_LOSS_ASSUMPTION)
                trail_hit = not stop_hit and paper_engine.trailing_stop_triggered(
                    pos.position_id, market_price, settings.trailing_stop_pct
                )

                # Early exit if no hit
                if not (stop_hit or trail_hit):
                    continue

                # Ensure position still exists for closure
                if pos.position_id not in paper_engine.positions:
                    # Already closed by another mechanism
                    continue

                close_reason = "stop_loss" if stop_hit else "trailing_stop"
                order = await paper_engine.close_position(pos.position_id, market_price)
                if order:
                    pnl = paper_engine.record_closed_trade(
                        pos.position_id,
                        order.price,
                        close_reason,
                    )
                    if pnl is None:
                        continue
                    _record_trade_result(pnl)
                    repo.update_order_pnl(order.id, pnl)
                    meta = paper_engine._position_meta.get(pos.position_id, {})
                    learner.record_outcome(
                        meta.get("strategy_id", "unknown"),
                        pos.market,
                        meta.get("direction", "long"),
                        pnl,
                    )
                    label = "STOP-LOSS" if stop_hit else "TRAILING-STOP"
                    activity.warn(
                        f"{label}: {pos.market} [{pos.position_id[:8]}] closed at "
                        f"{CURRENCY_SYMBOL}{order.price:,.2f} ({loss_pct:.1%} loss)",
                        f"Entry {CURRENCY_SYMBOL}{pos.avg_price:,.2f}  PnL {CURRENCY_SYMBOL}{pnl:+,.2f}",
                    )
                    await _send_alert(
                        close_reason,
                        {"market": pos.market, "position_id": pos.position_id, "pnl": pnl},
                    )

            # Detect the active strategy type early so warmup logic can exclude LLM-only.
            _selected = _strategy_by_id(control.selected_strategy_id)
            _uses_llm = bool(getattr(_selected, "uses_llm_recommendation", False))  # type: ignore[arg-type]

            # Session trade warmup: block indicator-based strategies for the first
            # _TRADE_WARMUP_TICKS ticks so price history is stable before signals fire.
            # LLM-only strategies are exempt — they need no indicator history.
            if not _uses_llm and tick <= _TRADE_WARMUP_TICKS:
                activity.info(
                    f"Tick #{tick} - trade warmup ({tick}/{_TRADE_WARMUP_TICKS} ticks complete, "
                    f"{_TRADE_WARMUP_TICKS - tick} remaining)",
                )
                await asyncio.sleep(30)
                continue

            # Block indicator-based strategies further until the full lookback window fills.
            if warming_up and not _uses_llm:
                await asyncio.sleep(30)
                continue

            # Reconcile pending live orders every 2 ticks (~60 s) so fills are visible promptly.
            if tick % 2 == 0 and settings.trading_environment == "live":
                try:
                    await kraken_engine.reconcile_pending_orders()
                except Exception as e:
                    logger.warning("Order reconciliation error", extra={"error": str(e)})

            ideas: List[TradeIdea] = []
            active_strategy = _strategy_by_id(control.selected_strategy_id)
            if active_strategy is None:
                control.select_strategy("basic_and_llm_strategy")
                active_strategy = _strategy_by_id(control.selected_strategy_id)
            if active_strategy is not None:
                if getattr(active_strategy, "uses_llm_recommendation", False):
                    # LLMOnlyStrategy: pure LLM recommendation, no indicator gates
                    strategy_ideas = await active_strategy.evaluate(
                        market_data,
                        _latest_news,
                        analyser=_analyser,
                        equity=_current_equity,
                        cash=paper_engine.cash,
                        open_positions=paper_engine.open_positions(),
                    )
                elif getattr(active_strategy, "uses_llm_analysis", False):
                    # BasicAndLLMStrategy: indicator consensus + internal LLM analysis
                    strategy_ideas = await active_strategy.evaluate(
                        market_data,
                        _latest_news,
                        _analyser,
                        portfolio_data={
                            "equity": _current_equity,
                            "cash": paper_engine.cash,
                            "open_positions": paper_engine.open_positions(),
                        },
                    )
                else:
                    # BasicStrategy: indicators only
                    strategy_ideas = await active_strategy.evaluate(market_data)
                ideas.extend(strategy_ideas)

            if not ideas:
                activity.info("Strategies evaluated - no signals this tick")

            # Apply historical performance learning before risk evaluation so that the
            # adjusted confidence governs the min_signal_confidence gate.
            for idea in ideas:
                idea.confidence = learner.adjust_confidence(
                    idea.strategy_id, idea.market, idea.direction.value, idea.confidence
                )

            signals = []
            for idea in ideas:
                # LLM: full-context signal analysis (reuse indicators already computed)
                md = market_data.get(idea.market, {})
                _price = md.get("price", 0)
                _prev = md.get("previous_price", _price)
                _mom = ((_price - _prev) / _prev * 100) if _prev else 0.0
                _ind = md.get("indicators", {})
                # Capture confidence before LLM so the signal detail modal can show
                # the exact pre-LLM subtotal in its score breakdown.
                pre_llm_confidence = idea.confidence
                idea.supporting_signals["pre_llm_confidence"] = pre_llm_confidence

                if idea.supporting_signals.get("llm_only"):
                    llm_analysis = SignalAnalysis(
                        sentiment=float(idea.supporting_signals.get("llm_sentiment", 0.0)),
                        confidence_scale=1.0,
                        reasoning=str(idea.supporting_signals.get("llm_reasoning", "")),
                        llm_used=True,
                    )
                elif idea.strategy_id == "basic_and_llm_strategy":
                    # Veto, confidence scaling, and thesis are already applied inside
                    # BasicAndLLMStrategy._evaluate_market — just reconstruct from
                    # supporting_signals so save_trade_idea stores the real LLM values.
                    _llm_scale = idea.supporting_signals.get("llm_confidence_scale")
                    llm_analysis = SignalAnalysis(
                        sentiment=float(idea.supporting_signals.get("llm_sentiment") or 0.0),
                        confidence_scale=float(_llm_scale) if _llm_scale is not None else 1.0,
                        reasoning=str(idea.supporting_signals.get("llm_reasoning") or ""),
                        llm_used=_llm_scale is not None,
                    )
                else:
                    llm_analysis = SignalAnalysis(
                        sentiment=0.0,
                        confidence_scale=1.0,
                        reasoning="",
                        llm_used=False,
                    )

                risk_decision = await risk_engine.evaluate_trade(
                    idea,
                    open_positions=paper_engine.open_positions(),
                    available_cash=paper_engine.cash,
                    market_price=prices.get(idea.market),
                    market_volume_24h=(market_data.get(idea.market) or {}).get("volume"),
                )

                # Persist trade idea with full signal context for post-trade traceability.
                # Build relevant-news list: articles mentioning this asset, padded to 5.
                _base = idea.market.split("/")[0].lower()
                _aliases = {
                    "btc": ["bitcoin", "btc"],
                    "xbt": ["bitcoin", "btc"],
                    "eth": ["ethereum", "eth"],
                    "sol": ["solana", "sol"],
                    "doge": ["dogecoin", "doge"],
                    "ada": ["cardano", "ada"],
                }
                _terms = _aliases.get(_base, [_base])
                _relevant = [
                    n
                    for n in _latest_news
                    if any(t in (n.get("title", "") + n.get("summary", "")).lower() for t in _terms)
                ]
                _news_for_signal = (_relevant + [n for n in _latest_news if n not in _relevant])[:5]
                repo.save_trade_idea(
                    idea,
                    momentum_pct=_mom,
                    indicators=_ind,
                    llm_analysis=llm_analysis,
                    news_context=[
                        {
                            "source": n["source"],
                            "title": n["title"],
                            "url": n.get("url", ""),
                            "summary": n.get("summary", ""),
                        }
                        for n in _news_for_signal
                    ],
                    risk_decision=risk_decision,
                )

                direction_label = "LONG" if idea.direction.value == "long" else "SHORT"

                if not risk_decision.approved:
                    activity.warn(
                        f"Signal {idea.market} {direction_label} {idea.confidence:.0%} - risk blocked",
                        risk_decision.reason,
                    )
                    _now = datetime.now(timezone.utc)
                    _rejection = {
                        "market": idea.market,
                        "direction": idea.direction.value,
                        "confidence": idea.confidence,
                        "thesis": idea.thesis,
                        "reason": risk_decision.reason,
                        "timestamp": _now.isoformat(),
                        "trade_idea_id": idea.id,
                    }
                    _risk_rejections.appendleft(_rejection)
                    repo.save_risk_rejection(
                        market=idea.market,
                        direction=idea.direction.value,
                        confidence=idea.confidence,
                        thesis=idea.thesis,
                        reason=risk_decision.reason,
                        trade_idea_id=idea.id,
                        timestamp=_now,
                    )
                    if "daily loss" in risk_decision.reason.lower():
                        await _send_alert(
                            "daily_loss_limit_hit",
                            {"market": idea.market, "reason": risk_decision.reason},
                        )

                elif settings.trading_mode == "manual":
                    activity.info(
                        f"Signal {idea.market} {direction_label} {idea.confidence:.0%} - visible in dashboard (manual mode, no action taken)",
                        idea.thesis,
                    )

                elif settings.trading_mode == "semi_automated":
                    # One position per pair: skip if there is already an open position
                    # in the same direction (opposite direction = closing trade, always allowed).
                    _existing = paper_engine.positions_for_market(idea.market)
                    _blocked = any(
                        (p.size > 0 and idea.direction.value == "long")
                        or (p.size < 0 and idea.direction.value == "short")
                        for p in _existing
                    )
                    if _blocked:
                        activity.info(
                            f"Signal {idea.market} {direction_label} - skipped (position already open)",
                            idea.thesis,
                        )
                    elif approval_service.submit(idea, risk_decision) is not None:
                        activity.success(
                            f"Signal {idea.market} {direction_label} {idea.confidence:.0%} - submitted for approval",
                            idea.thesis,
                        )
                    else:
                        activity.info(
                            f"Signal {idea.market} {direction_label} - skipped (approval already pending)",
                            idea.thesis,
                        )

                elif settings.trading_mode == "fully_automated":
                    # Executes immediately, with no approval step.
                    # Gate 1: confidence threshold (configurable via MIN_SIGNAL_CONFIDENCE env var)
                    if idea.confidence < settings.min_signal_confidence:
                        activity.info(
                            f"Signal {idea.market} {direction_label} skipped - confidence "
                            f"{idea.confidence:.0%} below threshold {settings.min_signal_confidence:.0%}",
                        )
                        continue
                    # One position per pair: skip same-direction signals.
                    _existing = paper_engine.positions_for_market(idea.market)
                    _blocked = any(
                        (p.size > 0 and idea.direction.value == "long")
                        or (p.size < 0 and idea.direction.value == "short")
                        for p in _existing
                    )
                    market_price = prices.get(idea.market)
                    if _blocked:
                        pass  # already in this trade, skip silently
                    elif not market_price:
                        activity.warn(
                            f"Signal {idea.market} {direction_label} - auto-execution skipped (no live price)",
                        )
                    else:
                        # Capture the FIFO long before execute() removes it (needed for PnL)
                        _closing_long = None
                        if idea.direction.value == "short":
                            _longs = sorted(
                                [p for p in _existing if p.size > 0],
                                key=lambda p: p.timestamp,
                            )
                            _closing_long = _longs[0] if _longs else None

                        if risk_decision.proposed_size_eur is not None:
                            size_base = risk_decision.proposed_size_eur / market_price
                        else:
                            sizing = risk_decision.adjusted_sizing or idea.position_sizing_proposal
                            size_base = (sizing * _current_equity) / market_price
                        intent = ExecutionIntent(
                            approval_request_id="auto",
                            market=idea.market,
                            direction=idea.direction,
                            size=size_base,
                        )
                        is_live_market = control.is_market_live(idea.market)
                        execution_engine = kraken_engine if is_live_market else paper_engine
                        order, pos_id = await execution_engine.execute(
                            intent,
                            market_price,
                            strategy_id=idea.strategy_id,
                            signal_confidence=idea.confidence,
                            environment="live" if is_live_market else "paper",
                            trade_idea_id=idea.id,
                        )

                        if (
                            not is_live_market
                            and order.status == "filled"
                            and idea.direction.value == "short"
                            and pos_id
                        ):
                            pnl = paper_engine.record_closed_trade(
                                pos_id,
                                order.price,
                                "auto",
                                closing_trade_idea_id=idea.id,
                            )
                            if pnl is not None:
                                _record_trade_result(pnl)
                                repo.update_order_pnl(order.id, pnl)
                                learner.record_outcome(idea.strategy_id, idea.market, idea.direction.value, pnl)

                        # Persist cash immediately so a crash between ticks doesn't lose this trade
                        if not is_live_market and order.status == "filled":
                            _eq = paper_engine.get_total_equity(prices)
                            repo.save_equity_snapshot(_eq, paper_engine.cash, _eq - paper_engine.cash)

                        activity.success(
                            f"Signal {idea.market} {direction_label} {idea.confidence:.0%} - auto-executed ({order.status})",
                            f"Size: {size_base:.6f} @ {CURRENCY_SYMBOL}{market_price:,.2f}  Order: {order.id[:8]}",
                        )

                signals.append(
                    {
                        "strategy": idea.strategy_id,
                        "market": idea.market,
                        "direction": idea.direction.value,
                        "confidence": idea.confidence,
                        "thesis": idea.thesis,
                        "risk_approved": risk_decision.approved,
                        "risk_reason": risk_decision.reason,
                        "trade_idea_id": idea.id,
                    }
                )

            if signals:
                # Prepend new signals; drop stale entries for the same markets;
                # keep the buffer at most _SIGNAL_BUFFER_MAX entries.
                _new_markets = {s["market"] for s in signals}
                _retained = [s for s in _latest_signals if s["market"] not in _new_markets]
                _latest_signals = (signals + _retained)[:_SIGNAL_BUFFER_MAX]
                _dashboard_version += 1

        except Exception as e:
            logger.error("Strategy loop error", extra={"error": str(e)}, exc_info=True)
            activity.error("Strategy loop error", str(e))

        _elapsed = asyncio.get_event_loop().time() - _tick_start
        await asyncio.sleep(max(0.0, 30.0 - _elapsed))


async def _market_briefing_task(new_articles: List[Dict[str, Any]]) -> None:
    """Background task: brief the LLM whenever new news arrives.

    Gathers current prices + indicators for every active market and asks the
    LLM to produce a market-wide outlook that is then cached on _analyser and
    injected into every subsequent signal analysis.
    """
    if not _current_prices:
        return
    market_data: Dict[str, Any] = {}
    for market in _active_markets:
        price = _current_prices.get(market)
        if price is None:
            continue
        hist = list(_price_history.get(market, []))
        ohlc = _ohlc_cache_5.get(market, {}).get("candles")
        market_data[market] = {
            "price": price,
            "indicators": compute_indicators(hist, ohlc_candles=ohlc),
        }
    if not market_data:
        return

    try:
        briefing = await _analyser.brief_market(new_articles, market_data)
        if briefing:
            outlooks = "  ".join(
                f"{m}: {v.get('bias', '?') if isinstance(v, dict) else '?'}"
                for m, v in briefing.market_outlooks.items()
            )
            activity.info(
                f"LLM briefing ({len(new_articles)} new article(s)): {briefing.key_insight}",
                outlooks,
            )
    except Exception as e:
        logger.error("Market briefing task error", extra={"error": str(e)}, exc_info=True)


async def _news_loop() -> None:
    """Fetch RSS news every 5 minutes and cache for the dashboard.

    When genuinely new articles are detected (IDs not seen before), a
    background market-briefing task is spawned to update the LLM's view.
    """
    global _latest_news, _briefed_news_ids
    while True:
        try:
            all_items = []
            for adapter in news_adapters:
                items = await adapter.fetch_news()
                all_items.extend(items)
            all_items = [normalise_news_item(item) for item in all_items]
            all_items.sort(key=lambda x: x.published_at, reverse=True)
            for item in all_items:
                try:
                    await _async_db(
                        repo.upsert_news_item,
                        id=item.id,
                        source=item.source,
                        title=item.title,
                        content=getattr(item, "content", ""),
                        published_at=item.published_at,
                        url=item.url,
                    )
                except Exception:
                    pass  # duplicate or constraint; ignore

            # Keep the 60 most recent articles across all sources (no time cutoff)
            _latest_news = [
                {
                    "id": item.id,
                    "source": item.source,
                    "title": item.title,
                    "url": item.url,
                    "summary": (getattr(item, "content", "") or "")[:200].strip(),
                    "published_at": item.published_at.isoformat(),
                }
                for item in all_items
            ][:60]

            # Detect genuinely new articles and trigger a market briefing.
            # Always replace (not union) _briefed_news_ids so that IDs aged out of
            # _latest_news are removed immediately; keeps the set bounded to <= 60 items.
            current_ids = {n["id"] for n in _latest_news}
            new_ids = current_ids - _briefed_news_ids
            _briefed_news_ids = current_ids  # replace, not grow; O(1) bound
            if new_ids:
                new_articles = [n for n in _latest_news if n["id"] in new_ids]
                asyncio.create_task(_market_briefing_task(new_articles))
                logger.info("Market briefing triggered", extra={"new_articles": len(new_articles)})

            activity.info(
                f"News fetched - {len(_latest_news)} articles", ", ".join(sorted({i["source"] for i in _latest_news}))
            )
            logger.info("News feed updated", extra={"count": len(_latest_news)})
        except Exception as e:
            logger.error("News loop error", extra={"error": str(e)}, exc_info=True)
            activity.error("News fetch failed", str(e))
        await asyncio.sleep(300)


async def _reflection_loop() -> None:
    """Hourly: ask the LLM to surface patterns from recent trade outcomes."""
    await asyncio.sleep(120)  # wait for first trades before first reflection
    while True:
        try:
            # get_closed_trades returns plain dicts required by reflect_on_outcomes.
            outcomes = repo.get_closed_trades(limit=50)
            reflection = await _analyser.reflect_on_outcomes(outcomes)
            if reflection:
                activity.info(
                    f"LLM reflection: {reflection.pattern}",
                    f"Suggestion: {reflection.suggestion} ({reflection.insight_confidence:.0%} confidence)",
                )
        except Exception as e:
            logger.error("Reflection loop error", extra={"error": str(e)}, exc_info=True)
        await asyncio.sleep(3600)


async def _ohlc_loop() -> None:
    """Fetch 5-min and 15-min OHLC for each active market sequentially.

    Runs on a slow cycle so we never exceed Kraken's public rate limit regardless
    of how many browser tabs are open; the endpoint only reads from these caches.
    """
    # Wait until the strategy loop has confirmed the active market list
    while not _active_markets:
        await asyncio.sleep(2)

    while True:
        markets = [m for m in _active_markets if control.is_market_enabled(m)]
        for market in markets:
            # Fetch 5-min candles
            try:
                candles = await kraken_adapter.get_ohlc(market, interval=5, candle_limit=100)
                if candles:
                    _ohlc_cache_5[market] = {"symbol": market, "interval": 5, "candles": candles}
                    logger.debug("OHLC 5m refreshed", extra={"market": market, "candles": len(candles)})
            except Exception as e:
                logger.error("OHLC 5m fetch failed", extra={"market": market, "error": str(e)}, exc_info=True)

            await asyncio.sleep(_OHLC_INTER_INTERVAL_GAP)

            # Fetch 15-min candles
            try:
                candles = await kraken_adapter.get_ohlc(market, interval=15, candle_limit=100)
                if candles:
                    _ohlc_cache_15[market] = {"symbol": market, "interval": 15, "candles": candles}
                    logger.debug("OHLC 15m refreshed", extra={"market": market, "candles": len(candles)})
            except Exception as e:
                logger.error("OHLC 15m fetch failed", extra={"market": market, "error": str(e)}, exc_info=True)

            # Gap between markets prevents hitting Kraken's rate limit.
            await asyncio.sleep(_OHLC_INTER_MARKET_GAP)

        await asyncio.sleep(_OHLC_REFRESH_INTERVAL)


async def _equity_ticker_loop() -> None:
    """Every 10 s: recompute portfolio equity and push a snapshot to the history deque.

    Running independently from the 30-s strategy loop gives the frontend chart a
    smooth, near-realtime view of portfolio value as market prices move.
    """
    while True:
        await asyncio.sleep(10)
        if not _current_prices:
            continue
        try:
            _record_equity_snapshot(_current_prices)
        except Exception as e:
            logger.warning("Equity ticker error", extra={"error": str(e)}, exc_info=True)


@asynccontextmanager
async def lifespan(app: FastAPI):
    if not settings.transformers_llm_model:
        logger.warning("LLM model not configured — set TRANSFORMERS_LLM_MODEL in .env")

    ok = await _llm_client.probe()
    if not ok:
        logger.error("LLM probe failed — Transformers model unavailable")
    asyncio.create_task(_strategy_loop())
    asyncio.create_task(_news_loop())
    asyncio.create_task(_ohlc_loop())
    asyncio.create_task(_reflection_loop())
    asyncio.create_task(_equity_ticker_loop())
    yield


app = FastAPI(
    title=settings.app_name,
    description="News-aware trading bot for Kraken exchange",
    version=settings.version,
    lifespan=lifespan,
)

app.add_middleware(
    CORSMiddleware,
    allow_origins=settings.cors_origins,
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)

app.mount("/static", StaticFiles(directory=str(FRONTEND_DIR / "static")), name="static")


# Page routes


# Serve the dashboard shell without browser caching so inline scripts stay current.
@app.get("/")
async def root():
    return FileResponse(
        str(FRONTEND_DIR / "index.html"),
        headers={"Cache-Control": "no-store, max-age=0"},
    )


# Serve the approvals shell without browser caching so frontend fixes apply immediately.
@app.get("/approvals")
async def approvals_page():
    return FileResponse(
        str(FRONTEND_DIR / "approvals.html"),
        headers={"Cache-Control": "no-store, max-age=0"},
    )


@app.get("/sw.js")
async def service_worker():
    """Serve the Service Worker from the root scope with correct MIME type.

    Must be served from / (not /static/) so the SW can intercept all same-origin
    requests. The no-cache header ensures the browser always checks for an updated
    worker rather than serving a stale version.
    """
    from fastapi.responses import Response as _Resp

    sw_path = FRONTEND_DIR / "sw.js"
    content = sw_path.read_bytes()
    return _Resp(
        content=content,
        media_type="application/javascript",
        headers={"Cache-Control": "no-store, max-age=0"},
    )


# Dashboard


@app.get("/api/dashboard")
async def get_dashboard(request: Request, response: Response):
    etag = str(_dashboard_version)
    if request.headers.get("if-none-match") == etag:
        return Response(status_code=304)
    response.headers["ETag"] = etag
    try:
        ctrl = control.snapshot()
        disabled_markets = set(ctrl["disabled_markets"])
        live_markets = set(ctrl.get("live_markets", []))

        markets = [
            {
                "symbol": market,
                "price": f"{price:.2f}",
                "enabled": market not in disabled_markets,
                "live": market in live_markets,
            }
            for market, price in _current_prices.items()
        ]
        # Also include known active markets that haven't priced yet
        priced = {m["symbol"] for m in markets}
        for m in _active_markets:
            if m not in priced:
                markets.append(
                    {
                        "symbol": m,
                        "price": "-",
                        "enabled": m not in disabled_markets,
                        "live": m in live_markets,
                    }
                )

        positions = [
            {
                "position_id": pos.position_id[:8],  # short form for display
                "position_id_full": pos.position_id,  # full UUID for API calls
                "market": pos.market,
                "direction": "long" if pos.size > 0 else "short",
                "size": f"{abs(pos.size):.6f}",
                "avg_price": f"{pos.avg_price:.2f}",
                "unrealized_pnl": f"{pos.unrealized_pnl:.2f}",
                "trade_idea_id": paper_engine._position_meta.get(pos.position_id, {}).get("trade_idea_id", ""),
            }
            for pos in paper_engine.open_positions()
        ]

        pending = approval_service.get_pending()
        approvals = [_approval_to_dict(req) for req in pending]

        # get_total_equity uses avg_price as fallback when a market has no live price,
        # so this is safe to call even when _current_prices is empty on first startup.
        equity = paper_engine.get_total_equity(_current_prices)
        equity_history = align_equity_history_with_current(_equity_history, equity)

        strategy_states = [
            {
                "id": active_strategy.strategy_id,
                "label": STRATEGY_LABELS.get(active_strategy.strategy_id, active_strategy.strategy_id),
                "enabled": control.is_strategy_selected(active_strategy.strategy_id),
                "selected": control.is_strategy_selected(active_strategy.strategy_id),
            }
            for active_strategy in strategies
        ]

        ref = _analyser.latest_reflection
        brf = _analyser.latest_briefing
        open_pos_ids = [pos.position_id for pos in paper_engine.open_positions()]
        return {
            "mode": settings.trading_mode,
            "environment": settings.trading_environment,
            "base_currency": settings.base_currency,
            "currency_symbol": CURRENCY_SYMBOL,
            "equity": f"{equity:.2f}",
            "cash": f"{paper_engine.cash:.2f}",
            "open_position_ids": open_pos_ids,
            "markets": markets,
            "signals": _latest_signals,
            "positions": positions,
            "approvals": approvals,
            "equity_history": equity_history,
            "risk_rejections": list(_risk_rejections)[:20],
            "activity": activity.recent(60),
            "control": ctrl,
            "strategies": strategy_states,
            "warmup_ticks_remaining": max(0, _TRADE_WARMUP_TICKS - _session_tick),
            "learning": learner.summary(),
            "llm": {
                "available": _llm_client.available,
                "status": _llm_status(),
                "model": _llm_client.llm_model,
                "reflection": {
                    "pattern": ref.pattern,
                    "suggestion": ref.suggestion,
                    "confidence": ref.insight_confidence,
                    "generated_at": ref.generated_at.isoformat(),
                }
                if ref
                else None,
                "briefing": {
                    "key_insight": brf.key_insight,
                    "overall_sentiment": brf.overall_sentiment,
                    "market_outlooks": brf.market_outlooks,
                    "article_count": brf.article_count,
                    "generated_at": brf.generated_at.isoformat(),
                }
                if brf
                else None,
            },
        }
    except Exception as e:
        logger.error("Failed to get dashboard data", extra={"error": str(e)})
        return {
            "mode": settings.trading_mode,
            "environment": settings.trading_environment,
            "base_currency": settings.base_currency,
            "currency_symbol": CURRENCY_SYMBOL,
            "equity": f"{settings.starting_capital:.2f}",
            "markets": [],
            "signals": [],
            "positions": [],
            "approvals": [],
            "equity_history": [
                {"timestamp": datetime.now(timezone.utc).isoformat(), "equity": settings.starting_capital}
            ],
            "risk_rejections": [],
            "activity": activity.recent(60),
            "control": control.snapshot(),
            "strategies": [],
        }


# Approvals


@app.get("/api/approvals")
async def get_approvals():
    pending = approval_service.get_pending()
    return [_approval_to_dict(req) for req in pending]


@app.post("/api/approvals/{approval_id}/approve")
async def approve_trade(approval_id: str):
    if control.emergency_stop:
        raise HTTPException(status_code=503, detail="Emergency stop is active - cannot execute trades")

    pending = approval_service.get(approval_id)
    if pending is None:
        raise HTTPException(status_code=404, detail="Approval not found or expired")

    req = approval_service.approve(approval_id)
    if req is None:
        raise HTTPException(status_code=404, detail="Approval expired between peek and approve")

    idea = req.trade_idea
    market_price = _current_prices.get(idea.market)
    if market_price is None or market_price <= 0:
        logger.warning("Cannot execute: no live price for market", extra={"market": idea.market})
        return {
            "status": "approved_no_price",
            "id": approval_id,
            "detail": f"No live price for {idea.market}; trade logged but not executed",
        }

    equity = paper_engine.get_total_equity(_current_prices)
    if req.risk_decision.proposed_size_eur is not None:
        size_base = req.risk_decision.proposed_size_eur / market_price
    else:
        sizing = req.risk_decision.adjusted_sizing or idea.position_sizing_proposal
        size_base = (sizing * equity) / market_price

    # Capture the FIFO long before execute() removes it (needed for PnL)
    _closing_long = None
    if idea.direction.value == "short":
        _longs = sorted(
            [p for p in paper_engine.positions_for_market(idea.market) if p.size > 0],
            key=lambda p: p.timestamp,
        )
        _closing_long = _longs[0] if _longs else None

    intent = ExecutionIntent(
        approval_request_id=approval_id,
        market=idea.market,
        direction=idea.direction,
        size=size_base,
    )

    is_live_market = control.is_market_live(idea.market)
    execution_engine = kraken_engine if is_live_market else paper_engine
    order, pos_id = await execution_engine.execute(
        intent,
        market_price,
        strategy_id=idea.strategy_id,
        signal_confidence=idea.confidence,
        environment="live" if is_live_market else "paper",
        trade_idea_id=idea.id,
    )
    logger.info(
        "Trade executed after approval",
        extra={
            "approval_id": approval_id,
            "order_id": order.id,
            "market": idea.market,
            "status": order.status,
            "environment": "live" if is_live_market else "paper",
            "position_id": pos_id,
        },
    )
    direction_label = "LONG" if idea.direction.value == "long" else "SHORT"
    if order.status == "rejected":
        activity.error(
            f"Manual approval: {idea.market} {direction_label} - order REJECTED (insufficient funds)",
            f"Equity {CURRENCY_SYMBOL}{equity:.2f}  Cash {CURRENCY_SYMBOL}{paper_engine.cash:.2f}  "
            f"Needed {CURRENCY_SYMBOL}{size_base * market_price:.2f}",
        )
    else:
        # Persist cash immediately so a crash between ticks doesn't lose this trade
        if not is_live_market:
            _eq = paper_engine.get_total_equity(_current_prices)
            repo.save_equity_snapshot(_eq, paper_engine.cash, _eq - paper_engine.cash)

        # Record outcome and PnL if this approval closed an existing long
        if not is_live_market and idea.direction.value == "short" and pos_id:
            pnl = paper_engine.record_closed_trade(
                pos_id,
                order.price,
                "manual_approve",
                closing_trade_idea_id=idea.id,
            )
            if pnl is not None:
                _record_trade_result(pnl)
                repo.update_order_pnl(order.id, pnl)
                learner.record_outcome(idea.strategy_id, idea.market, idea.direction.value, pnl)
        activity.success(
            f"Manual approval: {idea.market} {direction_label} - "
            f"{'live' if is_live_market else 'paper'} order {order.status}",
            f"Size: {size_base:.6f} @ {CURRENCY_SYMBOL}{market_price:,.2f}  "
            f"Order: {order.id[:8]}  [{pos_id[:8] if pos_id else '-'}]",
        )
    return {"status": "approved", "id": approval_id, "order_id": order.id, "order_status": order.status}


@app.post("/api/approvals/{approval_id}/reject")
async def reject_trade(approval_id: str):
    req = approval_service.reject(approval_id)
    if req is None:
        raise HTTPException(status_code=404, detail="Approval not found")
    logger.info("Trade rejected via API", extra={"approval_id": approval_id})
    idea = req.trade_idea
    direction_label = "LONG" if idea.direction.value == "long" else "SHORT"
    activity.warn(f"Manual rejection: {idea.market} {direction_label} - operator declined")
    return {"status": "rejected", "id": approval_id}


# Control endpoints


@app.get("/api/control")
async def get_control():
    return control.snapshot()


@app.post("/api/control/emergency-stop")
async def activate_emergency_stop():
    global _dashboard_version
    control.activate_stop()
    cleared = approval_service.clear_pending()
    _dashboard_version += 1
    logger.warning("Emergency stop activated - approval queue cleared", extra={"cleared": cleared})
    await _send_alert("emergency_stop_activated", {"cleared_approvals": cleared})
    return {"status": "stopped", "timestamp": datetime.now(timezone.utc).isoformat()}


@app.post("/api/control/resume")
async def resume_bot():
    global _dashboard_version
    control.resume()
    _dashboard_version += 1
    return {"status": "resumed", "timestamp": datetime.now(timezone.utc).isoformat()}


@app.post("/api/control/markets/{market:path}/toggle")
async def toggle_market(market: str):
    global _dashboard_version
    if control.is_market_enabled(market):
        control.disable_market(market)
        enabled = False
    else:
        control.enable_market(market)
        enabled = True
    _dashboard_version += 1
    return {"market": market, "enabled": enabled}


@app.post("/api/control/markets/{market:path}/live-toggle")
async def toggle_market_live(market: str):
    global _dashboard_version
    live = not control.is_market_live(market)
    control.set_market_live(market, live)
    _dashboard_version += 1
    return {"market": market, "live": live}


@app.post("/api/control/strategies/{strategy_id}/toggle")
async def toggle_strategy(strategy_id: str):
    global _dashboard_version
    strategy = _strategy_by_id(strategy_id)
    if strategy is None:
        raise HTTPException(status_code=404, detail="Strategy not found")
    control.select_strategy(strategy.strategy_id)
    _dashboard_version += 1
    return {"strategy_id": strategy.strategy_id, "enabled": True, "selected": True}


@app.post("/api/control/strategies/{strategy_id}/select")
async def select_strategy(strategy_id: str):
    global _dashboard_version
    strategy = _strategy_by_id(strategy_id)
    if strategy is None:
        raise HTTPException(status_code=404, detail="Strategy not found")
    control.select_strategy(strategy.strategy_id)
    _dashboard_version += 1
    return {"strategy_id": strategy.strategy_id, "selected": True}


@app.post("/api/control/strategies/reload")
async def reload_strategies():
    ids = _reload_strategy_instances()
    activity.info("Strategies reloaded", ", ".join(ids))
    return {"status": "reloaded", "strategies": ids}


# Misc


@app.get("/api/learning")
async def get_learning():
    return learner.summary()


@app.get("/api/ohlc/{market:path}")
async def get_ohlc(market: str, interval: int = Query(5, description="Candle interval in minutes (5 or 15)")):
    """Returns cached OHLC data for the requested interval.

    The background _ohlc_loop refreshes both timeframes every 2 minutes.
    """
    cache = _ohlc_cache_15 if interval == 15 else _ohlc_cache_5
    cached = cache.get(market)
    if cached:
        return cached
    # Not yet populated; return empty so the chart waits gracefully.
    return {"symbol": market, "interval": interval, "candles": []}


@app.get("/api/trades")
async def get_trades():
    """Full trade ledger: filled paper and accepted live orders, newest first."""
    return repo.get_trade_ledger(limit=200)


@app.get("/api/rejected-trades")
async def get_rejected_trades():
    """Execution-rejected orders (e.g. insufficient funds), newest first."""
    return repo.get_rejected_trades(limit=100)


@app.get("/api/closed-trades")
async def get_closed_trades():
    """Signal outcomes: every fully-closed position, newest first."""
    return repo.get_closed_trades(limit=200)


@app.get("/api/pnl-summary")
async def get_pnl_summary():
    """P&L grouped by day and market for dashboard summaries."""
    return repo.get_pnl_summary()


@app.get("/api/export/trades.csv")
async def export_trades_csv():
    """Export closed trade history as CSV."""
    rows = repo.get_closed_trades(limit=10_000)
    output = io.StringIO()
    fieldnames = [
        "strategy",
        "market",
        "direction",
        "entry_price",
        "exit_price",
        "size",
        "pnl",
        "pnl_pct",
        "confidence",
        "exit_reason",
        "entry_at",
        "exit_at",
        "position_id",
        "trade_idea_id",
        "closing_trade_idea_id",
    ]
    writer = csv.DictWriter(output, fieldnames=fieldnames, extrasaction="ignore")
    writer.writeheader()
    writer.writerows(rows)
    return Response(
        content=output.getvalue(),
        media_type="text/csv",
        headers={"Content-Disposition": "attachment; filename=trades.csv"},
    )


@app.get("/api/signals/{trade_idea_id}")
async def get_signal_detail(trade_idea_id: str):
    """Return the full signal context for a specific trade idea (for the signal detail modal)."""
    detail = repo.get_signal_detail(trade_idea_id)
    if detail is None:
        raise HTTPException(status_code=404, detail="Signal not found")
    return detail


@app.post("/api/positions/{position_id}/close")
async def close_position(position_id: str):
    """Manually close a specific open position at the current market price."""
    if control.emergency_stop:
        raise HTTPException(status_code=503, detail="Emergency stop is active")

    # The dashboard truncates position IDs to 8 chars; accept both full and short forms.
    pos = paper_engine.positions.get(position_id)
    if pos is None:
        # Try matching by prefix (UI passes 8-char truncated IDs)
        for pid, p in paper_engine.positions.items():
            if pid.startswith(position_id):
                pos = p
                position_id = pid
                break
    if pos is None:
        raise HTTPException(status_code=404, detail="Position not found")

    market_price = _current_prices.get(pos.market)
    if not market_price:
        raise HTTPException(status_code=503, detail=f"No live price for {pos.market}")

    order = await paper_engine.close_position(
        position_id,
        market_price,
        approval_request_id="manual_close",
    )
    if order is None:
        raise HTTPException(status_code=404, detail="Position not found or already closed")

    # Write a signal_outcome row for history and LLM learning
    pnl = paper_engine.record_closed_trade(position_id, order.price, "manual_close")
    if pnl is None:
        raise HTTPException(status_code=500, detail="Closed position outcome could not be recorded")
    _record_trade_result(pnl)
    repo.update_order_pnl(order.id, pnl)
    meta = paper_engine._position_meta.get(position_id, {})
    learner.record_outcome(
        meta.get("strategy_id", "unknown"),
        pos.market,
        meta.get("direction", "long"),
        pnl,
    )

    _eq = paper_engine.get_total_equity(_current_prices)
    repo.save_equity_snapshot(_eq, paper_engine.cash, _eq - paper_engine.cash)

    direction_label = "LONG" if order.direction.value == "long" else "SHORT"
    activity.success(
        f"Position manually closed: {pos.market} {direction_label}",
        f"@ {CURRENCY_SYMBOL}{order.price:,.2f}  PnL {CURRENCY_SYMBOL}{pnl:+,.2f}  "
        f"Cash now {CURRENCY_SYMBOL}{paper_engine.cash:,.2f}",
    )
    logger.info(
        "Position manually closed",
        extra={
            "position_id": position_id,
            "market": pos.market,
            "fill_price": order.price,
            "order_id": order.id,
            "pnl": pnl,
        },
    )
    return {
        "status": "closed",
        "position_id": position_id,
        "order_id": order.id,
        "fill_price": order.price,
        "pnl": pnl,
        "cash": paper_engine.cash,
    }


@app.post("/api/positions/reset")
async def reset_positions():
    """Clear all open positions from memory and the database.

    Before clearing, writes a signal_outcome record for every open position at
    the last known market price so the history and LLM learning data are preserved.
    Only available in paper mode.
    """
    if settings.trading_environment == "live":
        raise HTTPException(status_code=403, detail="Position reset is not allowed in live mode")

    count = await close_positions_for_operator_reset(
        paper_engine=paper_engine,
        prices=_current_prices,
        record_trade_result=_record_trade_result,
        repository=repo,
        learner=learner,
    )
    repo.clear_all_open_positions()
    paper_engine.positions.clear()
    paper_engine._position_meta.clear()
    _eq = paper_engine.get_total_equity(_current_prices)
    repo.save_equity_snapshot(_eq, paper_engine.cash, _eq - paper_engine.cash)

    logger.warning("Open positions reset by operator", extra={"cleared": count})
    activity.warn(f"Positions reset by operator - {count} position(s) cleared and logged")
    return {"cleared": count}


@app.get("/api/news")
async def get_news():
    return _latest_news


@app.get("/api/health")
async def health_check():
    return {
        "llm_available": _llm_client.available,
        "llm_state": _llm_client.circuit_state,
        "llm_model": _llm_client.llm_model,
    }


@app.get("/health")
async def health():
    return {"status": "healthy", "version": settings.version}


def _approval_to_dict(req) -> Dict[str, Any]:
    idea = req.trade_idea
    risk = req.risk_decision
    return {
        "id": req.id,
        "market": idea.market,
        "direction": idea.direction.value,
        "strategy_id": idea.strategy_id,
        "confidence": idea.confidence,
        "size": idea.position_sizing_proposal * 100,
        "expires_at": req.expires_at.isoformat(),
        "thesis": idea.thesis,
        "entry_plan": idea.entry_plan,
        "exit_plan": idea.exit_plan,
        "risk_approved": risk.approved,
        "risk_reason": risk.reason,
        "adjusted_sizing": risk.adjusted_sizing,
        "status": req.status,
    }


if __name__ == "__main__":
    logger.info(
        "Starting Kraken Trading Bot",
        extra={
            "host": settings.host,
            "port": settings.port,
            "mode": settings.trading_mode,
            "environment": settings.trading_environment,
        },
    )
    uvicorn.run(
        "main:app",
        host=settings.host,
        port=settings.port,
        reload=settings.debug,
        log_level=settings.log_level.lower(),
    )
