"""
SQLAlchemy database models
"""
from datetime import datetime, timezone

from sqlalchemy import JSON, Boolean, Column, DateTime, Float, ForeignKey, Integer, String, Text
from sqlalchemy.orm import DeclarativeBase


def _utcnow() -> datetime:
    """Return the current UTC time as a timezone-aware datetime (replaces deprecated utcnow)."""
    return datetime.now(timezone.utc)


class Base(DeclarativeBase):
    pass

class UniverseSnapshotModel(Base):
    __tablename__ = "universe_snapshots"
    id = Column(Integer, primary_key=True)
    fixed_markets = Column(JSON)
    dynamic_markets = Column(JSON)
    resolver_source = Column(String)
    resolved_at = Column(DateTime, default=_utcnow)
    mapping = Column(JSON)

class NewsItemModel(Base):
    __tablename__ = "news_items"
    id = Column(String, primary_key=True)
    source = Column(String)
    title = Column(String)
    content = Column(Text)
    published_at = Column(DateTime)
    url = Column(String, nullable=True, unique=True)

class NewsSignalModel(Base):
    __tablename__ = "news_signals"
    id = Column(Integer, primary_key=True)
    news_item_id = Column(String, ForeignKey("news_items.id"))
    asset_mentions = Column(JSON)
    headline_sentiment = Column(Float)
    summary_sentiment = Column(Float, nullable=True)
    event_type = Column(String, nullable=True)
    event_severity = Column(Float, nullable=True)
    confidence = Column(Float)
    created_at = Column(DateTime, default=_utcnow)

class MarketSnapshotModel(Base):
    __tablename__ = "market_snapshots"
    id = Column(Integer, primary_key=True, autoincrement=True)
    symbol = Column(String, index=True)
    timestamp = Column(DateTime, index=True)
    price = Column(Float)
    volume = Column(Float, nullable=True)

class TradeIdeaModel(Base):
    __tablename__ = "trade_ideas"
    id = Column(String, primary_key=True)
    strategy_id = Column(String)
    market = Column(String)
    direction = Column(String)
    thesis = Column(Text)
    supporting_signals = Column(JSON)
    confidence = Column(Float)          # final confidence after all adjustments
    entry_plan = Column(Text)
    exit_plan = Column(Text)
    stop_or_invalidation = Column(Text)
    position_sizing_proposal = Column(Float)
    mode_eligibility = Column(JSON)
    created_at = Column(DateTime, default=_utcnow)
    # Signal context (added for trade→signal linkage)
    momentum_pct = Column(Float, nullable=True)       # % price move that triggered the signal
    indicators = Column(JSON, nullable=True)           # full indicator snapshot at signal time
    llm_used = Column(Boolean, nullable=True)
    llm_sentiment = Column(Float, nullable=True)
    llm_confidence_scale = Column(Float, nullable=True)
    llm_reasoning = Column(Text, nullable=True)
    news_context = Column(JSON, nullable=True)         # relevant article snippets shown to LLM
    risk_approved = Column(Boolean, nullable=True)
    risk_reason = Column(Text, nullable=True)

class RiskDecisionModel(Base):
    __tablename__ = "risk_decisions"
    id = Column(Integer, primary_key=True)
    trade_idea_id = Column(String, ForeignKey("trade_ideas.id"))
    approved = Column(Boolean)
    reason = Column(Text)
    adjusted_sizing = Column(Float, nullable=True)
    timestamp = Column(DateTime, default=_utcnow)

class ApprovalRequestModel(Base):
    __tablename__ = "approval_requests"
    id = Column(String, primary_key=True)
    trade_idea_id = Column(String, ForeignKey("trade_ideas.id"))
    risk_decision_id = Column(Integer, ForeignKey("risk_decisions.id"))
    expires_at = Column(DateTime)
    status = Column(String, default="pending")
    created_at = Column(DateTime, default=_utcnow)

class OrderRecordModel(Base):
    __tablename__ = "order_records"
    id = Column(String, primary_key=True)
    approval_request_id = Column(String, nullable=True)
    market = Column(String, index=True)
    direction = Column(String)
    size = Column(Float)
    price = Column(Float)
    fee = Column(Float, nullable=True)
    status = Column(String)
    environment = Column(String, default="paper")  # paper | live
    timestamp = Column(DateTime, default=_utcnow)
    exchange_order_id = Column(String, nullable=True)
    position_id = Column(String, nullable=True, index=True)   # links open→close pairs
    pnl = Column(Float, nullable=True)                        # realised P&L (close orders only)
    trade_idea_id = Column(String, nullable=True, index=True) # signal that triggered this order

class FillRecordModel(Base):
    __tablename__ = "fill_records"
    id = Column(Integer, primary_key=True, autoincrement=True)
    order_id = Column(String, ForeignKey("order_records.id"))
    fill_price = Column(Float)
    fill_size = Column(Float)
    fee = Column(Float)
    timestamp = Column(DateTime, default=_utcnow)

class OpenPositionModel(Base):
    """One row per open trade. PK is position_id (UUID) so multiple trades per pair are tracked."""
    __tablename__ = "open_positions"
    position_id = Column(String, primary_key=True)
    market = Column(String, index=True)           # non-unique — many positions per pair allowed
    size = Column(Float)
    avg_price = Column(Float)
    signal_confidence = Column(Float, nullable=True)
    strategy_id = Column(String, nullable=True)
    direction = Column(String, nullable=True)
    trade_idea_id = Column(String, nullable=True, index=True)  # signal that opened this position
    unrealized_pnl = Column(Float, default=0.0)
    opened_at = Column(DateTime, default=_utcnow)
    updated_at = Column(DateTime, default=_utcnow, onupdate=_utcnow)

class SignalOutcomeModel(Base):
    """Closed trade record used for learning. Written when a position is fully closed."""
    __tablename__ = "signal_outcomes"
    id = Column(Integer, primary_key=True, autoincrement=True)
    strategy_id = Column(String, index=True)
    market = Column(String, index=True)
    direction = Column(String)
    entry_price = Column(Float)
    exit_price = Column(Float)
    size = Column(Float)
    pnl = Column(Float)
    pnl_pct = Column(Float)
    confidence_at_entry = Column(Float, nullable=True)
    exit_reason = Column(String)  # stop_loss | manual_approve | manual_reject | auto
    entry_at = Column(DateTime)
    exit_at = Column(DateTime, default=_utcnow)
    position_id = Column(String, nullable=True, index=True)             # UUID of the position that was closed
    trade_idea_id = Column(String, nullable=True, index=True)           # opening signal that created the position
    closing_trade_idea_id = Column(String, nullable=True, index=True)  # signal that triggered the close (auto/approve only)

class EquitySnapshotModel(Base):
    __tablename__ = "equity_snapshots"
    id = Column(Integer, primary_key=True, autoincrement=True)
    total_equity = Column(Float)
    cash = Column(Float)
    positions_value = Column(Float)
    timestamp = Column(DateTime, default=_utcnow)

class AuditLogModel(Base):
    __tablename__ = "audit_logs"
    id = Column(Integer, primary_key=True, autoincrement=True)
    event_type = Column(String)
    details = Column(JSON)
    timestamp = Column(DateTime, default=_utcnow)

class ActivityLogModel(Base):
    """Persisted activity-log entries — mirrors the in-memory rolling deque."""
    __tablename__ = "activity_log"
    id = Column(Integer, primary_key=True, autoincrement=True)
    level = Column(String)          # info | warn | error | success
    message = Column(Text)
    detail = Column(Text, default="")
    timestamp = Column(DateTime, default=_utcnow, index=True)

class RiskRejectionModel(Base):
    """Every risk-engine rejection of a signal, for dashboard history."""
    __tablename__ = "risk_rejections"
    id = Column(Integer, primary_key=True, autoincrement=True)
    market = Column(String)
    direction = Column(String)
    confidence = Column(Float)
    thesis = Column(Text, default="")
    reason = Column(Text)
    trade_idea_id = Column(String, nullable=True, index=True)
    timestamp = Column(DateTime, default=_utcnow, index=True)

class ControlStateModel(Base):
    """Single-row table storing operator toggles so they survive restarts."""
    __tablename__ = "control_state"
    id = Column(Integer, primary_key=True, default=1)   # always row 1
    emergency_stop = Column(Boolean, default=False)
    disabled_markets = Column(JSON, default=list)        # list[str]
    disabled_strategies = Column(JSON, default=list)     # list[str]
    selected_strategy = Column(String, default="combined")
    live_markets = Column(JSON, default=list)            # markets routed to live execution
    updated_at = Column(DateTime, default=_utcnow)

class LLMBriefingModel(Base):
    """Most-recent (and historical) LLM market briefings triggered by news."""
    __tablename__ = "llm_briefings"
    id = Column(Integer, primary_key=True, autoincrement=True)
    key_insight = Column(Text)
    overall_sentiment = Column(Float)
    market_outlooks = Column(JSON)      # {market: {bias, score, note}}
    article_count = Column(Integer)
    briefed_news_ids = Column(JSON, default=list)  # list[str] of news IDs included
    generated_at = Column(DateTime, default=_utcnow, index=True)

class LLMReflectionModel(Base):
    """Hourly outcome reflections from the LLM trading coach."""
    __tablename__ = "llm_reflections"
    id = Column(Integer, primary_key=True, autoincrement=True)
    pattern = Column(Text)
    suggestion = Column(Text)
    insight_confidence = Column(Float)
    generated_at = Column(DateTime, default=_utcnow, index=True)


class RiskStateModel(Base):
    """Single-row table that persists RiskEngine daily tracking across restarts.

    Row id is always 1 — use REPLACE INTO semantics via session.merge().
    """
    __tablename__ = "risk_state"
    id = Column(Integer, primary_key=True, default=1)   # always row 1
    daily_loss = Column(Float, default=0.0)
    daily_start_equity = Column(Float, default=0.0)
    last_reset_date = Column(String)   # ISO date string "YYYY-MM-DD"
    updated_at = Column(DateTime, default=_utcnow)
