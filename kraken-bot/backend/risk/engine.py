"""
Risk management engine.

evaluate_trade() is called before every signal reaches approval or execution.
It enforces portfolio-level rules that apply identically to paper and live:
  - One open position per trading pair
  - Sufficient cash before any buy
  - Per-trade loss limit
  - Daily loss limit
  - Minimum trade size
"""
from datetime import date, datetime
from typing import List, Optional

from config.settings import settings
from domain.models import Direction, PositionRecord, RiskDecision, TradeIdea
from observability.logging import get_logger

logger = get_logger("risk_engine")

# These are read from settings so they can be tuned via .env without code changes.
# The module-level aliases keep existing imports (e.g. main.py) working unchanged.
MIN_TRADE_SIZE_EUR   = settings.min_trade_size
STOP_LOSS_ASSUMPTION = settings.stop_loss_pct
_FEE_AND_SLIPPAGE    = settings.fee_and_slippage
MIN_24H_VOLUME       = settings.min_24h_volume


class RiskEngine:
    """Centralised risk management — environment-agnostic."""

    def __init__(self):
        self.current_equity: float = settings.starting_capital
        self.daily_loss:     float = 0.0
        self.daily_start_equity: float = settings.starting_capital
        self._last_reset_date: date = datetime.utcnow().date()

    # ── Public API ────────────────────────────────────────────────────────

    async def evaluate_trade(
        self,
        trade_idea: TradeIdea,
        *,
        open_positions: Optional[List[PositionRecord]] = None,
        available_cash: Optional[float] = None,
        market_price: Optional[float] = None,
        market_volume_24h: Optional[float] = None,
    ) -> RiskDecision:
        """Evaluate a trade idea against all risk constraints.

        Keyword-only extra context (pass from strategy loop for full checks):
          open_positions  – current portfolio snapshot from the execution engine
          available_cash  – liquid cash available right now
          market_price    – live price for the signal's market
        """
        self._check_daily_reset()

        proposed_size_eur = trade_idea.position_sizing_proposal * self.current_equity
        if (
            MIN_24H_VOLUME > 0
            and market_volume_24h is not None
            and market_volume_24h < MIN_24H_VOLUME
        ):
            return self._reject(
                trade_idea,
                f"Insufficient liquidity: 24h volume {market_volume_24h:.2f} "
                f"below minimum {MIN_24H_VOLUME:.2f}",
            )

        # ── 1. One position per trading pair ─────────────────────────────
        if open_positions is not None:
            mkt = [p for p in open_positions if p.market == trade_idea.market]
            same_direction = any(
                (p.size > 0 and trade_idea.direction == Direction.LONG) or
                (p.size < 0 and trade_idea.direction == Direction.SHORT)
                for p in mkt
            )
            if same_direction:
                return self._reject(
                    trade_idea,
                    f"Position already open for {trade_idea.market} — one position per pair",
                )

        # ── 2. Cash sufficiency (buys only) ──────────────────────────────
        if (
            trade_idea.direction == Direction.LONG
            and available_cash is not None
            and market_price is not None
            and market_price > 0
        ):
            cost = proposed_size_eur * (1 + _FEE_AND_SLIPPAGE)
            if available_cash < cost:
                return self._reject(
                    trade_idea,
                    f"Insufficient cash: need €{cost:.2f}, have €{available_cash:.2f}",
                )

        # ── 3. Minimum trade size ─────────────────────────────────────────
        if proposed_size_eur < MIN_TRADE_SIZE_EUR:
            return self._reject(
                trade_idea,
                f"Trade size €{proposed_size_eur:.2f} below minimum €{MIN_TRADE_SIZE_EUR:.2f}",
            )

        # ── 4. Per-trade loss limit ───────────────────────────────────────
        max_loss = self.current_equity * settings.max_loss_per_trade_percent / 100
        estimated_loss = proposed_size_eur * STOP_LOSS_ASSUMPTION
        if estimated_loss > max_loss:
            return self._reject(
                trade_idea,
                f"Estimated loss €{estimated_loss:.2f} exceeds per-trade limit €{max_loss:.2f}",
            )

        # ── 5. Daily loss limit ───────────────────────────────────────────
        # Include the estimated loss of the *current* trade so a trade that
        # would push the day's total over the limit is rejected pre-emptively.
        daily_limit = self.current_equity * settings.max_daily_loss_percent / 100
        if self.daily_loss + estimated_loss >= daily_limit:
            return self._reject(
                trade_idea,
                f"Daily loss limit reached: €{self.daily_loss:.2f} accumulated + "
                f"€{estimated_loss:.2f} estimated = €{self.daily_loss + estimated_loss:.2f} "
                f"of €{daily_limit:.2f}",
            )

        return RiskDecision(
            trade_idea_id=trade_idea.id,
            approved=True,
            reason="All risk checks passed",
            adjusted_sizing=None,
            timestamp=datetime.utcnow(),
        )

    def update_equity(self, new_equity: float) -> None:
        self.current_equity = new_equity

    def record_trade_result(self, pnl: float) -> None:
        """Record a completed trade's P&L and update daily tracking."""
        self._check_daily_reset()
        self.daily_loss += max(0.0, -pnl)
        self.current_equity += pnl
        logger.info("Trade result recorded", extra={
            "pnl": pnl, "daily_loss": self.daily_loss,
            "current_equity": self.current_equity,
        })

    # ── Internal ──────────────────────────────────────────────────────────

    def _check_daily_reset(self) -> None:
        today = datetime.utcnow().date()
        if today != self._last_reset_date:
            logger.info("Daily risk reset", extra={
                "previous_date": self._last_reset_date.isoformat(),
                "daily_loss": self.daily_loss,
            })
            self.daily_loss = 0.0
            self.daily_start_equity = self.current_equity
            self._last_reset_date = today

    def _reject(self, trade_idea: TradeIdea, reason: str) -> RiskDecision:
        return RiskDecision(
            trade_idea_id=trade_idea.id,
            approved=False,
            reason=reason,
            timestamp=datetime.utcnow(),
        )
