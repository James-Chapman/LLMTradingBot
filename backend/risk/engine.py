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
from datetime import date, datetime, timezone
from typing import List, Optional

from config.currency import currency_symbol
from config.settings import settings
from domain.models import Direction, PositionRecord, RiskDecision, TradeIdea
from observability.logging import get_logger

logger = get_logger("risk_engine")

# These are read from settings so they can be tuned via .env without code changes.
# The module-level aliases keep existing imports, such as main.py, working unchanged.
MIN_TRADE_SIZE_EUR = settings.min_trade_size
TARGET_TRADE_AMOUNT_EUR = settings.target_trade_amount
STOP_LOSS_ASSUMPTION = settings.stop_loss_pct
_FEE_AND_SLIPPAGE = settings.fee_and_slippage
MIN_24H_VOLUME = settings.min_24h_volume
_DISPLAY_CURRENCY = currency_symbol(settings.base_currency)


class RiskEngine:
    """Centralised risk management across paper and live environments."""

    def __init__(self) -> None:
        self.current_equity: float = settings.starting_capital
        self.daily_loss: float = 0.0
        self.daily_start_equity: float = settings.starting_capital
        self._last_reset_date: date = datetime.now(timezone.utc).date()

    # Evaluate a trade idea against all risk constraints.
    async def evaluate_trade(
        self,
        trade_idea: TradeIdea,
        *,
        open_positions: Optional[List[PositionRecord]] = None,
        available_cash: Optional[float] = None,
        market_price: Optional[float] = None,
        market_volume_24h: Optional[float] = None,
    ) -> RiskDecision:
        self._check_daily_reset()

        strategy_size_eur = trade_idea.position_sizing_proposal * self.current_equity
        proposed_size_eur = (
            TARGET_TRADE_AMOUNT_EUR
            if TARGET_TRADE_AMOUNT_EUR > 0
            else strategy_size_eur
        )
        # Exchange minimum is a floor, not a risk gate — clamp up silently so the
        # remaining checks run against the actual size that will be traded.
        _adjusted = abs(proposed_size_eur - strategy_size_eur) > 0.000001
        if proposed_size_eur < MIN_TRADE_SIZE_EUR:
            proposed_size_eur = MIN_TRADE_SIZE_EUR
            _adjusted = True

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

        positions_for_market: List[PositionRecord] = []
        if open_positions is not None:
            positions_for_market = [p for p in open_positions if p.market == trade_idea.market]
            same_direction = any(
                (p.size > 0 and trade_idea.direction == Direction.LONG)
                or (p.size < 0 and trade_idea.direction == Direction.SHORT)
                for p in positions_for_market
            )
            if same_direction:
                return self._reject(
                    trade_idea,
                    f"Position already open for {trade_idea.market} - one position per pair",
                )

        total_closable_long = sum(p.size for p in positions_for_market if p.size > 0)
        proposed_base_size = (
            proposed_size_eur / market_price
            if market_price is not None and market_price > 0 else 0.0
        )
        short_fully_closes_long = (
            trade_idea.direction == Direction.SHORT
            and proposed_base_size > 0
            and total_closable_long >= proposed_base_size
        )
        requires_cash = (
            trade_idea.direction == Direction.LONG
            or (
                trade_idea.direction == Direction.SHORT
                and not short_fully_closes_long
            )
        )
        if available_cash is not None and market_price is not None and market_price > 0 and requires_cash:
            cost = proposed_size_eur * (1 + _FEE_AND_SLIPPAGE)
            if available_cash < cost:
                cash_sized_eur = available_cash / (1 + _FEE_AND_SLIPPAGE)
                if cash_sized_eur < MIN_TRADE_SIZE_EUR:
                    min_cost = MIN_TRADE_SIZE_EUR * (1 + _FEE_AND_SLIPPAGE)
                    return self._reject(
                        trade_idea,
                        f"Insufficient cash: need at least {_DISPLAY_CURRENCY}{min_cost:.2f}, "
                        f"have {_DISPLAY_CURRENCY}{available_cash:.2f}",
                    )
                proposed_size_eur = cash_sized_eur
                _adjusted = True

        max_loss = self.current_equity * settings.max_loss_per_trade_percent / 100
        estimated_loss = proposed_size_eur * STOP_LOSS_ASSUMPTION
        if estimated_loss > max_loss:
            return self._reject(
                trade_idea,
                f"Estimated loss {_DISPLAY_CURRENCY}{estimated_loss:.2f} exceeds "
                f"per-trade limit {_DISPLAY_CURRENCY}{max_loss:.2f}",
            )

        daily_limit = self.current_equity * settings.max_daily_loss_percent / 100
        if self.daily_loss + estimated_loss >= daily_limit:
            return self._reject(
                trade_idea,
                f"Daily loss limit reached: {_DISPLAY_CURRENCY}{self.daily_loss:.2f} accumulated + "
                f"{_DISPLAY_CURRENCY}{estimated_loss:.2f} estimated = "
                f"{_DISPLAY_CURRENCY}{self.daily_loss + estimated_loss:.2f} of "
                f"{_DISPLAY_CURRENCY}{daily_limit:.2f}",
            )

        return RiskDecision(
            trade_idea_id=trade_idea.id,
            approved=True,
            reason="All risk checks passed",
            adjusted_sizing=proposed_size_eur / self.current_equity if _adjusted else None,
            timestamp=datetime.now(timezone.utc),
        )

    # Update the equity used by risk limits.
    def update_equity(self, new_equity: float) -> None:
        self.current_equity = new_equity

    # Record a completed trade's P&L and update daily tracking.
    def record_trade_result(self, pnl: float) -> None:
        self._check_daily_reset()
        self.daily_loss += max(0.0, -pnl)
        self.current_equity += pnl
        logger.info("Trade result recorded", extra={
            "pnl": pnl,
            "daily_loss": self.daily_loss,
            "current_equity": self.current_equity,
        })

    # Reset daily risk counters when the UTC date changes.
    def _check_daily_reset(self) -> None:
        today = datetime.now(timezone.utc).date()
        if today != self._last_reset_date:
            logger.info("Daily risk reset", extra={
                "previous_date": self._last_reset_date.isoformat(),
                "daily_loss": self.daily_loss,
            })
            self.daily_loss = 0.0
            self.daily_start_equity = self.current_equity
            self._last_reset_date = today

    # Build a rejected risk decision with the supplied reason.
    def _reject(self, trade_idea: TradeIdea, reason: str) -> RiskDecision:
        return RiskDecision(
            trade_idea_id=trade_idea.id,
            approved=False,
            reason=reason,
            timestamp=datetime.now(timezone.utc),
        )
