"""
Repository — all database read/write operations in one place.
Every method opens its own session, commits, and closes.
"""
from datetime import datetime, timezone
from typing import Dict, List, Optional

from sqlalchemy import text

from domain.models import (
    ApprovalRequest,
    Direction,
    FillRecord,
    OrderRecord,
    RiskDecision,
    TradeIdea,
    TradingMode,
)
from observability.logging import get_logger

from .database import get_session
from .models import (
    ActivityLogModel,
    ApprovalRequestModel,
    ControlStateModel,
    EquitySnapshotModel,
    FillRecordModel,
    LLMBriefingModel,
    LLMReflectionModel,
    MarketSnapshotModel,
    NewsItemModel,
    OpenPositionModel,
    OrderRecordModel,
    RejectedTradeModel,
    RiskDecisionModel,
    RiskRejectionModel,
    RiskStateModel,
    SignalOutcomeModel,
    TradeIdeaModel,
)

logger = get_logger("repository")

# Trim price ticks older than this many rows per symbol to keep the DB lean
_MAX_PRICE_ROWS_PER_SYMBOL = 5_760  # 48 h at 30 s intervals


class Repository:

    # ── Orders & fills ────────────────────────────────────────────────────

    def save_trade_idea(self, idea, *, momentum_pct: float = 0.0,
                        indicators: dict = None, llm_analysis=None,
                        news_context: list = None, risk_decision=None) -> None:
        """Persist a TradeIdea with full signal context for post-trade traceability."""
        with get_session() as s:
            s.merge(TradeIdeaModel(
                id=idea.id,
                strategy_id=idea.strategy_id,
                market=idea.market,
                direction=idea.direction.value,
                thesis=idea.thesis,
                supporting_signals=idea.supporting_signals,
                confidence=idea.confidence,
                entry_plan=idea.entry_plan,
                exit_plan=idea.exit_plan,
                stop_or_invalidation=idea.stop_or_invalidation,
                position_sizing_proposal=idea.position_sizing_proposal,
                mode_eligibility=[m.value for m in idea.mode_eligibility],
                momentum_pct=momentum_pct,
                indicators=indicators or {},
                llm_used=llm_analysis.llm_used if llm_analysis else False,
                llm_sentiment=llm_analysis.sentiment if llm_analysis and llm_analysis.llm_used else None,
                llm_confidence_scale=llm_analysis.confidence_scale if llm_analysis and llm_analysis.llm_used else None,
                llm_reasoning=llm_analysis.reasoning if llm_analysis and llm_analysis.llm_used else None,
                news_context=news_context or [],
                risk_approved=risk_decision.approved if risk_decision else None,
                risk_reason=risk_decision.reason if risk_decision else None,
            ))

    def get_signal_detail(self, trade_idea_id: str) -> Optional[Dict]:
        """Return the full signal context for one trade idea, or None if not found."""
        with get_session() as s:
            r = s.get(TradeIdeaModel, trade_idea_id)
            if r is None:
                return None
            return {
                "id":                    r.id,
                "strategy_id":           r.strategy_id,
                "market":                r.market,
                "direction":             r.direction,
                "thesis":                r.thesis,
                "supporting_signals":    r.supporting_signals or {},
                "confidence":            r.confidence,
                "momentum_pct":          r.momentum_pct,
                "indicators":            r.indicators or {},
                "llm_used":              r.llm_used or False,
                "llm_sentiment":         r.llm_sentiment,
                "llm_confidence_scale":  r.llm_confidence_scale,
                "llm_reasoning":         r.llm_reasoning,
                "news_context":          r.news_context or [],
                "risk_approved":         r.risk_approved,
                "risk_reason":           r.risk_reason,
                "entry_plan":            r.entry_plan,
                "exit_plan":             r.exit_plan,
                "stop_or_invalidation":  r.stop_or_invalidation,
                "created_at":            r.created_at.isoformat() if r.created_at else "",
            }

    def save_approval_request(self, request: ApprovalRequest) -> None:
        """Persist a pending approval request and its reconstructable context."""
        idea = request.trade_idea
        risk = request.risk_decision
        with get_session() as s:
            s.merge(TradeIdeaModel(
                id=idea.id,
                strategy_id=idea.strategy_id,
                market=idea.market,
                direction=idea.direction.value,
                thesis=idea.thesis,
                supporting_signals=idea.supporting_signals,
                confidence=idea.confidence,
                entry_plan=idea.entry_plan,
                exit_plan=idea.exit_plan,
                stop_or_invalidation=idea.stop_or_invalidation,
                position_sizing_proposal=idea.position_sizing_proposal,
                mode_eligibility=[mode.value for mode in idea.mode_eligibility],
            ))
            existing = s.get(ApprovalRequestModel, request.id)
            risk_row = None
            if existing and existing.risk_decision_id:
                risk_row = s.get(RiskDecisionModel, existing.risk_decision_id)
            if risk_row:
                risk_row.approved = risk.approved
                risk_row.reason = risk.reason
                risk_row.adjusted_sizing = risk.adjusted_sizing
                risk_row.timestamp = risk.timestamp
            else:
                risk_row = RiskDecisionModel(
                    trade_idea_id=risk.trade_idea_id,
                    approved=risk.approved,
                    reason=risk.reason,
                    adjusted_sizing=risk.adjusted_sizing,
                    timestamp=risk.timestamp,
                )
                s.add(risk_row)
                s.flush()
            s.merge(ApprovalRequestModel(
                id=request.id,
                trade_idea_id=idea.id,
                risk_decision_id=risk_row.id,
                expires_at=request.expires_at,
                status=request.status,
            ))

    def update_approval_status(self, approval_id: str, status: str) -> None:
        """Update a persisted approval status if it exists."""
        with get_session() as s:
            row = s.get(ApprovalRequestModel, approval_id)
            if row:
                row.status = status

    def clear_pending_approvals(self) -> int:
        """Mark all persisted pending approvals as cleared and return the count."""
        with get_session() as s:
            rows = (
                s.query(ApprovalRequestModel)
                .filter(ApprovalRequestModel.status == "pending")
                .all()
            )
            for row in rows:
                row.status = "cleared"
            return len(rows)

    def load_pending_approval_requests(self, now: datetime) -> List[ApprovalRequest]:
        """Return pending, non-expired approval requests reconstructed from storage."""
        with get_session() as s:
            rows = (
                s.query(ApprovalRequestModel)
                .filter(
                    ApprovalRequestModel.status == "pending",
                    ApprovalRequestModel.expires_at > now,
                )
                .all()
            )
            result: List[ApprovalRequest] = []
            for row in rows:
                idea_row = s.get(TradeIdeaModel, row.trade_idea_id)
                risk_row = s.get(RiskDecisionModel, row.risk_decision_id)
                if idea_row is None or risk_row is None:
                    continue
                idea = TradeIdea(
                    id=idea_row.id,
                    strategy_id=idea_row.strategy_id,
                    market=idea_row.market,
                    direction=Direction(idea_row.direction),
                    thesis=idea_row.thesis,
                    supporting_signals=idea_row.supporting_signals or {},
                    confidence=idea_row.confidence,
                    entry_plan=idea_row.entry_plan,
                    exit_plan=idea_row.exit_plan,
                    stop_or_invalidation=idea_row.stop_or_invalidation,
                    position_sizing_proposal=idea_row.position_sizing_proposal,
                    mode_eligibility=[
                        TradingMode(mode) for mode in (idea_row.mode_eligibility or [])
                    ],
                )
                risk = RiskDecision(
                    trade_idea_id=risk_row.trade_idea_id,
                    approved=risk_row.approved,
                    reason=risk_row.reason,
                    adjusted_sizing=risk_row.adjusted_sizing,
                    timestamp=risk_row.timestamp or datetime.now(timezone.utc),
                )
                # SQLite returns naive datetimes; attach UTC so comparisons with
                # timezone-aware datetime.now(timezone.utc) don't raise TypeError.
                expires_at = row.expires_at
                if expires_at is not None and expires_at.tzinfo is None:
                    expires_at = expires_at.replace(tzinfo=timezone.utc)
                result.append(ApprovalRequest(
                    id=row.id,
                    trade_idea=idea,
                    risk_decision=risk,
                    expires_at=expires_at,
                    status=row.status,
                ))
            return result

    def save_order(self, order: OrderRecord, approval_id: str = "",
                   fee: float = 0.0, environment: str = "paper",
                   trade_idea_id: str = "") -> None:
        """Persist a non-rejected order record for the operational trade ledger."""
        if order.status == "rejected":
            direction = order.direction.value if hasattr(order.direction, "value") else str(order.direction)
            self.save_rejected_trade(
                market=order.market,
                direction=direction,
                size=order.size,
                price=order.price,
                reason=order.exchange_order_id or "rejected",
                trade_idea_id=trade_idea_id,
                timestamp=order.timestamp,
            )
            return

        with get_session() as s:
            s.merge(OrderRecordModel(
                id=order.id,
                approval_request_id=approval_id or None,
                market=order.market,
                direction=order.direction.value,
                size=order.size,
                price=order.price,
                fee=fee,
                status=order.status,
                environment=environment,
                timestamp=order.timestamp,
                exchange_order_id=order.exchange_order_id,
                position_id=getattr(order, "position_id", None),
                trade_idea_id=trade_idea_id or None,
            ))

    def get_trade_ledger(self, limit: int = 200) -> list:
        """Return visible order records, newest first, for the trade ledger.

        trade_type is derived by identifying which order opened each position:
        rows are fetched newest-first, so iterating and overwriting the mapping
        means the final value per position_id is the oldest (= opening) order.
        Any other order for that position_id is a closing order.
        """
        with get_session() as s:
            rows = (
                s.query(OrderRecordModel)
                .filter(OrderRecordModel.status.in_(["filled", "pending", "submitted"]))
                .order_by(OrderRecordModel.timestamp.desc())
                .limit(limit)
                .all()
            )

            # First pass: find each returned position's opening order across full history.
            # oldest order's id as the value — that is the position opener.
            position_ids = {r.position_id for r in rows if r.position_id}
            position_opener: dict = {}
            position_opener_idea: dict = {}
            for position_id in position_ids:
                opener = (
                    s.query(OrderRecordModel)
                    .filter(OrderRecordModel.position_id == position_id)
                    .order_by(OrderRecordModel.timestamp.asc())
                    .first()
                )
                if opener:
                    position_opener[position_id] = opener.id
                    position_opener_idea[position_id] = opener.trade_idea_id

            trade_idea_ids = {r.trade_idea_id for r in rows if r.trade_idea_id}
            trade_idea_ids.update(idea_id for idea_id in position_opener_idea.values() if idea_id)
            strategy_by_idea = {}
            if trade_idea_ids:
                strategy_rows = (
                    s.query(TradeIdeaModel.id, TradeIdeaModel.strategy_id)
                    .filter(TradeIdeaModel.id.in_(trade_idea_ids))
                    .all()
                )
                strategy_by_idea = {idea_id: strategy_id for idea_id, strategy_id in strategy_rows}

            result = []
            for r in rows:
                apr = r.approval_request_id or ""
                if apr == "stop_loss":
                    source = "stop-loss"
                elif apr == "auto":
                    source = "auto"
                elif apr:
                    source = "manual"
                else:
                    source = "system"
                # An order opens a position if it is the earliest order for that
                # position_id; any later order for the same position_id closes it.
                if r.position_id and position_opener.get(r.position_id) == r.id:
                    trade_type = "open"
                elif r.position_id:
                    trade_type = "close"
                else:
                    trade_type = "open"   # no position_id → unmatched, treat as open
                strategy_idea_id = r.trade_idea_id or position_opener_idea.get(r.position_id)
                result.append({
                    "id":               r.id[:8],
                    "position_id":      (r.position_id or "")[:8],  # short form for display
                    "position_id_full": r.position_id or "",
                    "trade_idea_id":    r.trade_idea_id or "",
                    "strategy":         strategy_by_idea.get(strategy_idea_id, ""),
                    "market":           r.market,
                    "direction":        r.direction,
                    "trade_type":       trade_type,
                    "size":             r.size,
                    "price":            r.price,
                    "fee":              r.fee or 0.0,
                    "value":            (r.size or 0) * (r.price or 0),
                    "pnl":              r.pnl,      # None for opens; set on close orders
                    "status":           r.status,
                    "environment":      r.environment or "paper",
                    "exchange_order_id": r.exchange_order_id or "",
                    "source":           source,
                    "timestamp":        r.timestamp.isoformat() if r.timestamp else "",
                })
            return result

    def update_order_pnl(self, order_id: str, pnl: float) -> None:
        """Stamp the realised P&L onto a close order after execution."""
        with get_session() as s:
            row = s.get(OrderRecordModel, order_id)
            if row:
                row.pnl = pnl

    def get_pending_live_orders(self) -> list:
        """Return pending live orders that have a Kraken txid awaiting reconciliation."""
        with get_session() as s:
            rows = (
                s.query(OrderRecordModel)
                .filter(
                    OrderRecordModel.status == "pending",
                    OrderRecordModel.environment == "live",
                    OrderRecordModel.exchange_order_id.isnot(None),
                )
                .all()
            )
            return [
                {"id": r.id, "exchange_order_id": r.exchange_order_id, "price": r.price or 0.0}
                for r in rows
            ]

    def update_order_status_and_fill(
        self, order_id: str, status: str, fill_price: float, fee: float
    ) -> None:
        """Update a live order's status, fill price, and fee after Kraken reconciliation."""
        with get_session() as s:
            row = s.get(OrderRecordModel, order_id)
            if row:
                row.status = status
                row.price = fill_price
                row.fee = fee

    def save_fill(self, fill: FillRecord) -> None:
        with get_session() as s:
            s.add(FillRecordModel(
                order_id=fill.order_id,
                fill_price=fill.fill_price,
                fill_size=fill.fill_size,
                fee=fill.fee,
                timestamp=fill.timestamp,
            ))

    # ── Open positions ────────────────────────────────────────────────────

    def upsert_open_position(self, position_id: str, market: str, size: float,
                             avg_price: float, direction: str = "",
                             strategy_id: str = "",
                             signal_confidence: Optional[float] = None,
                             trade_idea_id: str = "") -> None:
        """Insert or update an open position row, preserving the opening signal link."""
        with get_session() as s:
            existing = s.get(OpenPositionModel, position_id)
            if existing:
                existing.size = size
                existing.avg_price = avg_price
                existing.updated_at = datetime.now(timezone.utc)
                # Update trade_idea_id only if we now have one (don't overwrite with empty)
                if trade_idea_id:
                    existing.trade_idea_id = trade_idea_id
            else:
                s.add(OpenPositionModel(
                    position_id=position_id,
                    market=market,
                    size=size,
                    avg_price=avg_price,
                    direction=direction,
                    strategy_id=strategy_id,
                    signal_confidence=signal_confidence,
                    trade_idea_id=trade_idea_id or None,
                    opened_at=datetime.now(timezone.utc),
                    updated_at=datetime.now(timezone.utc),
                ))

    def delete_open_position(self, position_id: str) -> None:
        with get_session() as s:
            row = s.get(OpenPositionModel, position_id)
            if row:
                s.delete(row)

    def get_open_positions(self) -> List[OpenPositionModel]:
        with get_session() as s:
            rows = s.query(OpenPositionModel).all()
            s.expunge_all()
            return rows

    # ── Signal outcomes (learning) ────────────────────────────────────────

    def save_signal_outcome(self, *, strategy_id: str, market: str, direction: str,
                            entry_price: float, exit_price: float, size: float,
                            pnl: float, confidence: Optional[float],
                            exit_reason: str, entry_at: datetime,
                            position_id: str = "",
                            trade_idea_id: str = "",
                            closing_trade_idea_id: str = "") -> None:
        """Persist a closed-position record.

        position_id links back to the open_positions / order_records UUID.
        closing_trade_idea_id links to the signal that triggered the close.
        """
        pnl_pct = pnl / (abs(size) * entry_price) if entry_price and size else 0.0
        with get_session() as s:
            s.add(SignalOutcomeModel(
                strategy_id=strategy_id,
                market=market,
                direction=direction,
                entry_price=entry_price,
                exit_price=exit_price,
                size=size,
                pnl=pnl,
                pnl_pct=pnl_pct,
                confidence_at_entry=confidence,
                exit_reason=exit_reason,
                entry_at=entry_at,
                exit_at=datetime.now(timezone.utc),
                position_id=position_id or None,
                trade_idea_id=trade_idea_id or None,
                closing_trade_idea_id=closing_trade_idea_id or None,
            ))

    def get_signal_outcomes(self, strategy_id: Optional[str] = None,
                            limit: int = 1_000) -> List[SignalOutcomeModel]:
        with get_session() as s:
            q = s.query(SignalOutcomeModel)
            if strategy_id:
                q = q.filter(SignalOutcomeModel.strategy_id == strategy_id)
            rows = q.order_by(SignalOutcomeModel.exit_at.desc()).limit(limit).all()
            s.expunge_all()
            return rows

    # ── Price ticks ───────────────────────────────────────────────────────

    def save_price_tick(self, symbol: str, price: float, volume: Optional[float] = None) -> None:
        with get_session() as s:
            s.add(MarketSnapshotModel(
                symbol=symbol,
                timestamp=datetime.now(timezone.utc),
                price=price,
                volume=volume,
            ))

    def get_recent_prices(self, symbol: str, limit: int = 60) -> List[float]:
        """Return up to `limit` most-recent prices for a symbol, oldest-first."""
        with get_session() as s:
            rows = (
                s.query(MarketSnapshotModel.price)
                .filter(MarketSnapshotModel.symbol == symbol)
                .order_by(MarketSnapshotModel.timestamp.desc())
                .limit(limit)
                .all()
            )
        return [r[0] for r in reversed(rows)]

    def trim_old_price_ticks(self, symbol: str) -> None:
        """Keep only the most recent _MAX_PRICE_ROWS_PER_SYMBOL rows per symbol."""
        with get_session() as s:
            s.execute(
                text(
                    """
                    DELETE FROM market_snapshots
                    WHERE symbol = :symbol
                    AND id NOT IN (
                        SELECT id
                        FROM market_snapshots
                        WHERE symbol = :symbol
                        ORDER BY timestamp DESC, id DESC
                        LIMIT :keep
                    )
                    """
                ),
                {"symbol": symbol, "keep": _MAX_PRICE_ROWS_PER_SYMBOL},
            )

    # ── News ──────────────────────────────────────────────────────────────

    def upsert_news_item(self, *, id: str, source: str, title: str,
                         content: str, published_at: datetime,
                         url: Optional[str]) -> None:
        with get_session() as s:
            existing = s.get(NewsItemModel, id)
            if not existing:
                s.add(NewsItemModel(
                    id=id, source=source, title=title,
                    content=content, published_at=published_at, url=url,
                ))

    def get_recent_news(self, limit: int = 60) -> List[Dict]:
        """Return the `limit` most recent news items, newest first."""
        with get_session() as s:
            rows = (
                s.query(NewsItemModel)
                .order_by(NewsItemModel.published_at.desc())
                .limit(limit)
                .all()
            )
            return [
                {
                    "id":           r.id,
                    "source":       r.source,
                    "title":        r.title,
                    "url":          r.url,
                    "summary":      (r.content or "")[:200].strip(),
                    "published_at": r.published_at.isoformat() if r.published_at else "",
                }
                for r in rows
            ]

    # ── Equity ────────────────────────────────────────────────────────────

    def save_equity_snapshot(self, total_equity: float, cash: float,
                             positions_value: float) -> None:
        with get_session() as s:
            s.add(EquitySnapshotModel(
                total_equity=total_equity,
                cash=cash,
                positions_value=positions_value,
                timestamp=datetime.now(timezone.utc),
            ))

    def get_equity_history(self, limit: int = 288) -> List[Dict]:
        """Return oldest-first equity snapshots for the equity chart."""
        with get_session() as s:
            rows = (
                s.query(EquitySnapshotModel)
                .order_by(EquitySnapshotModel.timestamp.desc())
                .limit(limit)
                .all()
            )
            return [
                {"timestamp": r.timestamp.isoformat(), "equity": r.total_equity}
                for r in reversed(rows)
            ]

    def get_latest_equity(self) -> Optional[float]:
        with get_session() as s:
            row = (
                s.query(EquitySnapshotModel.total_equity)
                .order_by(EquitySnapshotModel.timestamp.desc())
                .first()
            )
            return row[0] if row else None

    def get_latest_cash(self) -> Optional[float]:
        """Return the last persisted cash balance (not total equity)."""
        with get_session() as s:
            row = (
                s.query(EquitySnapshotModel.cash)
                .order_by(EquitySnapshotModel.timestamp.desc())
                .first()
            )
            return row[0] if row else None

    # ── Closed trades (signal outcomes) ──────────────────────────────────

    def get_closed_trades(self, limit: int = 200) -> List[Dict]:
        """Return closed trade records (signal outcomes) newest-first.

        Left-joins trade_ideas to include the indicator snapshot stored at entry time.
        The `indicators` field enables the LLM reflection loop to find patterns like
        "high RSI at entry correlated with losing longs".
        """
        with get_session() as s:
            rows = (
                s.query(SignalOutcomeModel, TradeIdeaModel.indicators)
                .outerjoin(
                    TradeIdeaModel,
                    SignalOutcomeModel.trade_idea_id == TradeIdeaModel.id,
                )
                .order_by(SignalOutcomeModel.exit_at.desc())
                .limit(limit)
                .all()
            )
            return [
                {
                    "strategy":      r.strategy_id,
                    "market":        r.market,
                    "direction":     r.direction,
                    "entry_price":   r.entry_price,
                    "exit_price":    r.exit_price,
                    "size":          r.size,
                    "pnl":           r.pnl,
                    "pnl_pct":       r.pnl_pct,
                    "confidence":    r.confidence_at_entry,
                    "exit_reason":   r.exit_reason,
                    "trade_idea_id":         r.trade_idea_id or "",
                    "closing_trade_idea_id": r.closing_trade_idea_id or "",
                    "entry_at":              r.entry_at.isoformat() if r.entry_at else "",
                    "exit_at":               r.exit_at.isoformat() if r.exit_at else "",
                    # Short Pos ID for display (same 8-char prefix used in trade ledger)
                    "position_id":           (r.position_id or "")[:8],
                    "position_id_full":      r.position_id or "",
                    # Indicator state at entry — None when no linked trade_idea exists
                    "indicators":    ind or None,
                }
                for r, ind in rows
            ]

    def get_pnl_summary(self, limit: int = 1_000) -> Dict:
        """Return P&L totals grouped by day and market."""
        trades = self.get_closed_trades(limit=limit)
        by_day: Dict[str, float] = {}
        by_market: Dict[str, float] = {}
        total = 0.0
        for trade in trades:
            pnl = float(trade.get("pnl") or 0.0)
            total += pnl
            market = trade.get("market") or "unknown"
            day = (trade.get("exit_at") or "")[:10] or "unknown"
            by_market[market] = by_market.get(market, 0.0) + pnl
            by_day[day] = by_day.get(day, 0.0) + pnl
        return {
            "total_pnl": round(total, 2),
            "by_day": {k: round(v, 2) for k, v in sorted(by_day.items())},
            "by_market": {k: round(v, 2) for k, v in sorted(by_market.items())},
        }

    def clear_all_open_positions(self) -> int:
        """Delete every row from open_positions. Returns the count deleted."""
        with get_session() as s:
            count = s.query(OpenPositionModel).count()
            s.query(OpenPositionModel).delete(synchronize_session=False)
            return count

    # ── Activity log ──────────────────────────────────────────────────────

    def save_activity_log(self, level: str, message: str, detail: str = "") -> None:
        with get_session() as s:
            s.add(ActivityLogModel(level=level, message=message, detail=detail,
                                   timestamp=datetime.now(timezone.utc)))

    def get_recent_activity(self, limit: int = 200) -> List[Dict]:
        """Return most-recent activity entries, newest-first (matches in-memory deque order)."""
        with get_session() as s:
            rows = (
                s.query(ActivityLogModel)
                .order_by(ActivityLogModel.timestamp.desc())
                .limit(limit)
                .all()
            )
            return [
                {"timestamp": r.timestamp.isoformat(), "level": r.level,
                 "message": r.message, "detail": r.detail or ""}
                for r in rows
            ]

    def trim_old_activity(self, keep: int = 2_000) -> None:
        """Keep only the most recent `keep` activity rows."""
        with get_session() as s:
            count = s.query(ActivityLogModel).count()
            if count > keep:
                cutoff_id = (
                    s.query(ActivityLogModel.id)
                    .order_by(ActivityLogModel.timestamp.desc())
                    .offset(keep).limit(1).scalar()
                )
                if cutoff_id:
                    s.query(ActivityLogModel).filter(
                        ActivityLogModel.id <= cutoff_id
                    ).delete(synchronize_session=False)

    # ── Risk rejections ───────────────────────────────────────────────────

    def save_risk_rejection(self, *, market: str, direction: str,
                            confidence: float, thesis: str, reason: str,
                            trade_idea_id: str = "",
                            timestamp: Optional[datetime] = None) -> None:
        with get_session() as s:
            s.add(RiskRejectionModel(
                market=market,
                direction=direction,
                confidence=confidence,
                thesis=thesis,
                reason=reason,
                trade_idea_id=trade_idea_id or None,
                timestamp=timestamp or datetime.now(timezone.utc),
            ))

    def get_recent_risk_rejections(self, limit: int = 50) -> List[Dict]:
        """Return most-recent risk rejections, newest-first."""
        with get_session() as s:
            rows = (
                s.query(RiskRejectionModel)
                .order_by(RiskRejectionModel.timestamp.desc())
                .limit(limit)
                .all()
            )
            return [
                {"market": r.market, "direction": r.direction,
                 "confidence": r.confidence, "thesis": r.thesis or "",
                 "reason": r.reason,
                 "trade_idea_id": r.trade_idea_id or "",
                 "timestamp": r.timestamp.isoformat() if r.timestamp else ""}
                for r in rows
            ]

    # ── Rejected trades (execution-level) ────────────────────────────────

    def save_rejected_trade(self, *, market: str, direction: str,
                            size: float, price: float,
                            confidence: Optional[float] = None,
                            reason: str = "insufficient_funds",
                            trade_idea_id: str = "",
                            timestamp: Optional[datetime] = None) -> None:
        """Persist an execution-rejected order (e.g. insufficient funds)."""
        with get_session() as s:
            s.add(RejectedTradeModel(
                market=market,
                direction=direction,
                size=size,
                price=price,
                confidence=confidence,
                reason=reason,
                trade_idea_id=trade_idea_id or None,
                timestamp=timestamp or datetime.now(timezone.utc),
            ))

    def get_rejected_trades(self, limit: int = 100) -> List[Dict]:
        """Return execution-rejected orders, newest first."""
        with get_session() as s:
            rows = (
                s.query(RejectedTradeModel)
                .order_by(RejectedTradeModel.timestamp.desc())
                .limit(limit)
                .all()
            )
            trade_idea_ids = {r.trade_idea_id for r in rows if r.trade_idea_id}
            strategy_by_idea = {}
            if trade_idea_ids:
                strategy_rows = (
                    s.query(TradeIdeaModel.id, TradeIdeaModel.strategy_id)
                    .filter(TradeIdeaModel.id.in_(trade_idea_ids))
                    .all()
                )
                strategy_by_idea = {idea_id: strategy_id for idea_id, strategy_id in strategy_rows}
            return [
                {"id": r.id,
                 "market": r.market, "direction": r.direction,
                 "size": r.size, "price": r.price,
                 "confidence": r.confidence,
                 "reason": r.reason,
                 "trade_idea_id": r.trade_idea_id or "",
                 "strategy": strategy_by_idea.get(r.trade_idea_id, ""),
                 "timestamp": r.timestamp.isoformat() if r.timestamp else ""}
                for r in rows
            ]

    # ── Control state ─────────────────────────────────────────────────────

    def save_control_state(self, emergency_stop: bool,
                           disabled_markets: list, disabled_strategies: list,
                           live_markets: list = None,
                           selected_strategy: str = "combined") -> None:
        """Upsert the single control-state row (id=1)."""
        with get_session() as s:
            row = s.get(ControlStateModel, 1)
            if row:
                row.emergency_stop = emergency_stop
                row.disabled_markets = disabled_markets
                row.disabled_strategies = disabled_strategies
                row.live_markets = live_markets or []
                row.selected_strategy = selected_strategy
                row.updated_at = datetime.now(timezone.utc)
            else:
                s.add(ControlStateModel(
                    id=1,
                    emergency_stop=emergency_stop,
                    disabled_markets=disabled_markets,
                    disabled_strategies=disabled_strategies,
                    live_markets=live_markets or [],
                    selected_strategy=selected_strategy,
                    updated_at=datetime.now(timezone.utc),
                ))

    def load_control_state(self) -> Optional[Dict]:
        """Return the persisted control state, or None if never saved."""
        with get_session() as s:
            row = s.get(ControlStateModel, 1)
            if row is None:
                return None
            return {
                "emergency_stop": row.emergency_stop,
                "disabled_markets": row.disabled_markets or [],
                "disabled_strategies": row.disabled_strategies or [],
                "live_markets": row.live_markets or [],
                "selected_strategy": row.selected_strategy or "combined",
            }

    # ── LLM briefings ─────────────────────────────────────────────────────

    def save_llm_briefing(self, *, key_insight: str, overall_sentiment: float,
                          market_outlooks: dict, article_count: int,
                          briefed_news_ids: list,
                          generated_at: Optional[datetime] = None) -> None:
        with get_session() as s:
            s.add(LLMBriefingModel(
                key_insight=key_insight,
                overall_sentiment=overall_sentiment,
                market_outlooks=market_outlooks,
                article_count=article_count,
                briefed_news_ids=briefed_news_ids,
                generated_at=generated_at or datetime.now(timezone.utc),
            ))

    def load_latest_llm_briefing(self) -> Optional[Dict]:
        with get_session() as s:
            row = (
                s.query(LLMBriefingModel)
                .order_by(LLMBriefingModel.generated_at.desc())
                .first()
            )
            if row is None:
                return None
            return {
                "key_insight": row.key_insight,
                "overall_sentiment": row.overall_sentiment,
                "market_outlooks": row.market_outlooks or {},
                "article_count": row.article_count,
                "briefed_news_ids": row.briefed_news_ids or [],
                "generated_at": row.generated_at.isoformat() if row.generated_at else "",
            }

    # ── LLM reflections ───────────────────────────────────────────────────

    def save_llm_reflection(self, *, pattern: str, suggestion: str,
                            insight_confidence: float,
                            generated_at: Optional[datetime] = None) -> None:
        with get_session() as s:
            s.add(LLMReflectionModel(
                pattern=pattern,
                suggestion=suggestion,
                insight_confidence=insight_confidence,
                generated_at=generated_at or datetime.now(timezone.utc),
            ))

    def load_latest_llm_reflection(self) -> Optional[Dict]:
        with get_session() as s:
            row = (
                s.query(LLMReflectionModel)
                .order_by(LLMReflectionModel.generated_at.desc())
                .first()
            )
            if row is None:
                return None
            return {
                "pattern": row.pattern,
                "suggestion": row.suggestion,
                "insight_confidence": row.insight_confidence,
                "generated_at": row.generated_at.isoformat() if row.generated_at else "",
            }

    # ── Risk state ────────────────────────────────────────────────────────

    def save_risk_state(self, *, daily_loss: float, daily_start_equity: float,
                        last_reset_date) -> None:
        """Upsert the single-row risk state so daily_loss survives a restart."""
        from datetime import date as date_type
        date_str = (
            last_reset_date.isoformat()
            if isinstance(last_reset_date, date_type)
            else str(last_reset_date)
        )
        with get_session() as s:
            s.merge(RiskStateModel(
                id=1,
                daily_loss=daily_loss,
                daily_start_equity=daily_start_equity,
                last_reset_date=date_str,
                updated_at=datetime.now(timezone.utc),
            ))

    def load_risk_state(self) -> Optional[Dict]:
        """Return persisted risk state, or None if no record exists."""
        from datetime import date as date_type
        with get_session() as s:
            row = s.get(RiskStateModel, 1)
            if row is None:
                return None
            try:
                reset_date = date_type.fromisoformat(row.last_reset_date)
            except (TypeError, ValueError):
                return None
            return {
                "daily_loss": row.daily_loss,
                "daily_start_equity": row.daily_start_equity,
                "last_reset_date": reset_date,
            }
