"""
Domain models using Pydantic v2
"""
import uuid
from datetime import datetime
from enum import Enum
from typing import Any, Dict, List, Optional

from pydantic import BaseModel, Field


class TradingMode(str, Enum):
    MANUAL = "manual"
    SEMI_AUTOMATED = "semi_automated"
    FULLY_AUTOMATED = "fully_automated"

class TradingEnvironment(str, Enum):
    PAPER = "paper"
    LIVE = "live"

class Direction(str, Enum):
    LONG = "long"
    SHORT = "short"

class MarketSnapshot(BaseModel):
    """Snapshot of market data"""
    symbol: str
    timestamp: datetime
    price: float
    volume: Optional[float] = None
    bid: Optional[float] = None
    ask: Optional[float] = None

class NewsItem(BaseModel):
    """Raw news item"""
    id: str = Field(default_factory=lambda: str(uuid.uuid4()))
    source: str
    title: str
    content: str
    published_at: datetime
    url: Optional[str] = None

class NewsSignal(BaseModel):
    """Processed news signal"""
    news_item_id: str
    asset_mentions: List[str]
    headline_sentiment: float = Field(ge=-1.0, le=1.0)  # -1 to 1
    summary_sentiment: Optional[float] = Field(default=None, ge=-1.0, le=1.0)
    event_type: Optional[str] = None
    event_severity: Optional[float] = Field(default=None, ge=0.0, le=1.0)
    confidence: float = Field(ge=0.0, le=1.0)

class EventSignal(BaseModel):
    """Detected market event"""
    event_type: str
    assets_affected: List[str]
    severity: float = Field(ge=0.0, le=1.0)
    description: str
    timestamp: datetime

class TradeIdea(BaseModel):
    """Strategy-generated trade idea"""
    id: str = Field(default_factory=lambda: str(uuid.uuid4()))
    strategy_id: str
    market: str
    direction: Direction
    thesis: str
    supporting_signals: Dict[str, Any]
    confidence: float = Field(ge=0.0, le=1.0)
    entry_plan: str
    exit_plan: str
    stop_or_invalidation: str
    position_sizing_proposal: float  # As percentage of equity
    mode_eligibility: List[TradingMode]

class RiskDecision(BaseModel):
    """Risk engine decision"""
    trade_idea_id: str
    approved: bool
    reason: str
    adjusted_sizing: Optional[float] = None
    proposed_size_eur: Optional[float] = None  # exact EUR amount the risk engine approved
    timestamp: datetime

class ApprovalRequest(BaseModel):
    """Pending approval request"""
    id: str
    trade_idea: TradeIdea
    risk_decision: RiskDecision
    expires_at: datetime
    status: str = "pending"  # pending, approved, rejected, expired

class ExecutionIntent(BaseModel):
    """Intent to execute a trade"""
    approval_request_id: str
    market: str
    direction: Direction
    size: float  # In base currency
    price: Optional[float] = None  # Limit price if applicable

class OrderRecord(BaseModel):
    """Exchange order record"""
    id: str
    market: str
    direction: Direction
    size: float
    price: float
    status: str  # pending, filled, cancelled, rejected
    timestamp: datetime
    exchange_order_id: Optional[str] = None
    position_id: Optional[str] = None  # position this order opened or closed

class FillRecord(BaseModel):
    """Order fill record"""
    order_id: str
    fill_price: float
    fill_size: float
    fee: float
    timestamp: datetime

class PositionRecord(BaseModel):
    """Current position"""
    position_id: str = Field(default_factory=lambda: str(uuid.uuid4()))
    market: str
    size: float  # Positive for long, negative for short
    avg_price: float
    unrealized_pnl: float
    timestamp: datetime

class EquitySnapshot(BaseModel):
    """Equity/account snapshot"""
    total_equity: float
    cash: float
    positions_value: float
    timestamp: datetime

class UniverseSnapshot(BaseModel):
    """Tradable universe snapshot"""
    fixed_markets: List[str]
    dynamic_markets: List[str]
    resolver_source: str
    resolved_at: datetime
    mapping: Dict[str, str]  # Coin to market mapping
