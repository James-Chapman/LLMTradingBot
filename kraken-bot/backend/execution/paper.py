"""
Paper execution engine.

Simulates Kraken spot fills with realistic fee and slippage modelling.
Each approved trade creates its own PositionRecord (keyed by position_id),
so multiple trades on the same pair are all visible independently.
Positions are persisted to SQLite via Repository and survive restarts.
"""
import uuid
from datetime import datetime, timezone
from typing import Dict, List, Optional, Tuple

from domain.models import (
    Direction,
    ExecutionIntent,
    FillRecord,
    OrderRecord,
    PositionRecord,
)
from observability.logging import get_logger

logger = get_logger("paper_engine")

TAKER_FEE_RATE = 0.0026   # Kraken taker fee for low-volume accounts (0.26%)
SLIPPAGE_RATE  = 0.001    # Conservative one-way slippage estimate (0.1%)
SIMULATED_LATENCY_MS = 150


class PaperExecutionEngine:
    """Simulate spot order fills against live market prices.

    self.positions is keyed by position_id (UUID), NOT by market, so
    multiple independent trades on the same pair are all tracked separately.
    """

    def __init__(self, starting_capital: float, repository=None):
        self.cash = starting_capital
        # Dict[position_id → PositionRecord]
        self.positions: Dict[str, PositionRecord] = {}
        self.orders: List[OrderRecord] = []
        self.fills: List[FillRecord] = []
        self._repo = repository
        # Per-position metadata keyed by position_id
        self._position_meta: Dict[str, dict] = {}

    # ── Restore ───────────────────────────────────────────────────────────

    def restore_from_db(self) -> None:
        """Reload open positions from the database after a restart."""
        if self._repo is None:
            return
        rows = self._repo.get_open_positions()
        for row in rows:
            self.positions[row.position_id] = PositionRecord(
                position_id=row.position_id,
                market=row.market,
                size=row.size,
                avg_price=row.avg_price,
                unrealized_pnl=row.unrealized_pnl or 0.0,
                timestamp=row.opened_at or datetime.now(timezone.utc),
            )
            self._position_meta[row.position_id] = {
                "strategy_id": row.strategy_id or "",
                "direction": row.direction or "",
                "confidence": row.signal_confidence,
                "opened_at": row.opened_at or datetime.now(timezone.utc),
                "market": row.market,
                # Restored so record_closed_trade() can link the outcome to the opening signal
                "trade_idea_id": row.trade_idea_id or "",
                "trailing_high": row.avg_price if row.size > 0 else None,
                "trailing_low": row.avg_price if row.size < 0 else None,
            }
        if rows:
            logger.info("Restored open positions from DB", extra={"count": len(rows)})

    # ── Public API ────────────────────────────────────────────────────────

    async def execute(self, intent: ExecutionIntent, market_price: float,
                      strategy_id: str = "", signal_confidence: Optional[float] = None,
                      environment: str = "paper",
                      trade_idea_id: str = "") -> Tuple[OrderRecord, str]:
        """Simulate an immediate fill.

        Returns (OrderRecord, position_id).
        For a LONG: position_id is the newly opened position.
        For a SHORT closing a long: position_id is the position that was closed.
        For a SHORT opening a paper short: position_id is the new short position.
        Returns ("") as position_id when the order is rejected.
        """
        order_id = str(uuid.uuid4())
        fill_price = self._apply_slippage(intent.direction, market_price)
        fill_value = intent.size * fill_price
        fee = fill_value * TAKER_FEE_RATE

        order = OrderRecord(
            id=order_id,
            market=intent.market,
            direction=intent.direction,
            size=intent.size,
            price=fill_price,
            status="pending",
            timestamp=datetime.now(timezone.utc),
        )

        if not self._has_sufficient_funds(intent, fill_value, fee):
            order.status = "rejected"
            self.orders.append(order)
            if self._repo:
                self._repo.save_order(order, intent.approval_request_id, fee, environment,
                                      trade_idea_id=trade_idea_id)
            logger.warning("Paper order rejected: insufficient funds", extra={
                "market": intent.market, "required": fill_value + fee,
                "available_cash": self.cash,
            })
            return order, ""

        order.status = "filled"
        self.orders.append(order)

        fill = FillRecord(
            order_id=order_id,
            fill_price=fill_price,
            fill_size=intent.size,
            fee=fee,
            timestamp=datetime.now(timezone.utc),
        )
        self.fills.append(fill)

        position_id = self._apply_fill(intent, fill_price, fee, strategy_id, signal_confidence,
                                       trade_idea_id=trade_idea_id)
        order.position_id = position_id

        if self._repo:
            self._repo.save_order(order, intent.approval_request_id, fee, environment,
                                  trade_idea_id=trade_idea_id)
            self._repo.save_fill(fill)
            if position_id in self.positions:
                pos = self.positions[position_id]
                meta = self._position_meta.get(position_id, {})
                self._repo.upsert_open_position(
                    position_id=position_id,
                    market=intent.market,
                    size=pos.size,
                    avg_price=pos.avg_price,
                    direction=meta.get("direction", intent.direction.value),
                    strategy_id=meta.get("strategy_id", strategy_id),
                    signal_confidence=meta.get("confidence", signal_confidence),
                    # Persist the opening signal so it survives restarts
                    trade_idea_id=meta.get("trade_idea_id", trade_idea_id),
                )
            else:
                # Position was closed (SHORT matched a LONG)
                self._repo.delete_open_position(position_id)

        logger.info("Paper fill simulated", extra={
            "order_id": order_id, "market": intent.market,
            "direction": intent.direction.value, "fill_price": fill_price,
            "fill_size": intent.size, "fee": fee, "position_id": position_id,
        })
        return order, position_id

    async def close_position(
        self,
        position_id: str,
        market_price: float,
        environment: str = "paper",
        approval_request_id: str = "stop_loss",
    ) -> Optional[OrderRecord]:
        """Close a specific position by ID (used by the stop-loss loop).

        Unlike execute(), this targets a named position rather than doing
        FIFO matching, so the correct position is always closed.
        """
        pos = self.positions.get(position_id)
        if pos is None:
            return None

        exit_direction = Direction.SHORT if pos.size > 0 else Direction.LONG
        fill_price = self._apply_slippage(exit_direction, market_price)
        fill_size = abs(pos.size)
        fill_value = fill_size * fill_price
        fee = fill_value * TAKER_FEE_RATE

        if exit_direction == Direction.SHORT:
            # Closing a long: receive sale proceeds
            self.cash += fill_value - fee
        else:
            # Closing a short: release the locked proceeds and settle P&L.
            # short_proceeds = original sell value locked at open; fall back to entry price × size
            # for positions restored from DB (which pre-date this fix).
            meta_preview = self._position_meta.get(position_id, {})
            short_proceeds = meta_preview.get("short_proceeds", pos.avg_price * fill_size)
            self.cash += short_proceeds - fill_value - fee

        order_id = str(uuid.uuid4())
        order = OrderRecord(
            id=order_id, market=pos.market, direction=exit_direction,
            size=fill_size, price=fill_price, status="filled",
            timestamp=datetime.now(timezone.utc), position_id=position_id,
        )
        self.orders.append(order)

        fill = FillRecord(
            order_id=order_id, fill_price=fill_price,
            fill_size=fill_size, fee=fee, timestamp=datetime.now(timezone.utc),
        )
        self.fills.append(fill)

        # Preserve close metadata before removing
        meta = self._position_meta.get(position_id, {})
        meta["avg_price_at_close"] = pos.avg_price
        meta["size_at_close"] = fill_size
        meta["exit_price_at_close"] = fill_price
        meta["exit_fee_at_close"] = fee
        self._position_meta[position_id] = meta

        del self.positions[position_id]

        if self._repo:
            self._repo.save_order(order, approval_request_id, fee, environment)
            self._repo.save_fill(fill)
            self._repo.delete_open_position(position_id)

        logger.info("Position closed (%s)", approval_request_id, extra={
            "position_id": position_id, "market": pos.market,
            "fill_price": fill_price, "fee": fee,
        })
        return order

    def record_closed_trade(self, position_id: str, exit_price: float,
                            exit_reason: str = "manual",
                            closing_trade_idea_id: str = "") -> Optional[float]:
        """Persist a SignalOutcome for a closed position. Call after execute()/close_position().

        closing_trade_idea_id: the ID of the signal that triggered this close, where applicable
        (auto-close via SHORT signal or manual_approve). Empty for stop-loss and manual UI close.
        """
        if self._repo is None:
            return None
        meta = self._position_meta.get(position_id, {})
        avg_price = meta.get("avg_price_at_close")
        size = meta.get("size_at_close")
        direction = meta.get("direction", "")
        if avg_price is None or size is None:
            return None
        actual_exit_price = meta.get("exit_price_at_close", exit_price)
        entry_fee = float(meta.get("entry_fee") or 0.0)
        exit_fee = float(meta.get("exit_fee_at_close") or 0.0)
        signed_size = size if direction == "long" else -size
        pnl = signed_size * (actual_exit_price - avg_price) - entry_fee - exit_fee
        # Look up market from meta or fall back to scanning orders
        market = meta.get("market", "")
        if not market:
            for o in reversed(self.orders):
                if o.position_id == position_id:
                    market = o.market
                    break
        self._repo.save_signal_outcome(
            strategy_id=meta.get("strategy_id", "unknown"),
            market=market,
            direction=direction,
            entry_price=avg_price,
            exit_price=actual_exit_price,
            size=abs(size),
            pnl=pnl,
            confidence=meta.get("confidence"),
            exit_reason=exit_reason,
            entry_at=meta.get("opened_at", datetime.now(timezone.utc)),
            position_id=position_id,
            trade_idea_id=meta.get("trade_idea_id", ""),
            closing_trade_idea_id=closing_trade_idea_id,
        )
        return pnl

    def update_mark_prices(self, prices: Dict[str, float]) -> None:
        """Recompute unrealised P&L for all open positions."""
        for pid, pos in self.positions.items():
            price = prices.get(pos.market)
            if price is not None:
                pnl = pos.size * (price - pos.avg_price)
                self.positions[pid] = pos.model_copy(update={"unrealized_pnl": pnl})

    def update_trailing_prices(self, prices: Dict[str, float]) -> None:
        """Update per-position trailing high/low watermarks."""
        for pid, pos in self.positions.items():
            price = prices.get(pos.market)
            if price is None:
                continue
            meta = self._position_meta.setdefault(pid, {})
            if pos.size > 0:
                meta["trailing_high"] = max(meta.get("trailing_high", pos.avg_price), price)
            else:
                meta["trailing_low"] = min(meta.get("trailing_low", pos.avg_price), price)

    def trailing_stop_triggered(self, position_id: str, market_price: float, trail_pct: float) -> bool:
        """Return True when price retraces from the best seen price by trail_pct."""
        pos = self.positions.get(position_id)
        if pos is None:
            return False
        meta = self._position_meta.get(position_id, {})
        if pos.size > 0:
            high = meta.get("trailing_high", pos.avg_price)
            return market_price <= high * (1.0 - trail_pct)
        low = meta.get("trailing_low", pos.avg_price)
        return market_price >= low * (1.0 + trail_pct)

    # Return True only when the current price is losing versus the entry price.
    def stop_loss_triggered(self, position_id: str, market_price: float, stop_loss_pct: float) -> bool:
        """Return True when a position is down by at least stop_loss_pct."""
        pos = self.positions.get(position_id)
        if pos is None:
            return False
        if pos.avg_price <= 0:
            return False
        if pos.size > 0:
            loss_pct = (pos.avg_price - market_price) / pos.avg_price
        else:
            loss_pct = (market_price - pos.avg_price) / pos.avg_price
        return loss_pct >= stop_loss_pct

    def get_total_equity(self, prices: Dict[str, float]) -> float:
        positions_value = sum(
            pos.size * prices.get(pos.market, pos.avg_price)
            for pos in self.positions.values()
        )
        return self.cash + positions_value

    def open_positions(self) -> List[PositionRecord]:
        return list(self.positions.values())

    def positions_for_market(self, market: str) -> List[PositionRecord]:
        return [p for p in self.positions.values() if p.market == market]

    # ── Internal helpers ──────────────────────────────────────────────────

    def _apply_slippage(self, direction: Direction, price: float) -> float:
        return price * (1 + SLIPPAGE_RATE) if direction == Direction.LONG else price * (1 - SLIPPAGE_RATE)

    def _has_sufficient_funds(self, intent: ExecutionIntent, fill_value: float, fee: float) -> bool:
        market_positions = [p for p in self.positions.values() if p.market == intent.market]

        if intent.direction == Direction.LONG:
            # Hard limit: one position per pair — block if a long already exists
            if any(p.size > 0 for p in market_positions):
                logger.warning("One-position-per-pair limit: long already open", extra={"market": intent.market})
                return False
            return self.cash >= fill_value + fee

        # SHORT — closing an existing long takes priority
        total_long = sum(p.size for p in market_positions if p.size > 0)
        if total_long >= intent.size:
            return True
        # Hard limit: block paper shorts if a short already exists
        if any(p.size < 0 for p in market_positions):
            logger.warning("One-position-per-pair limit: short already open", extra={"market": intent.market})
            return False
        # Paper short (no existing long) — require cash as margin
        return self.cash >= fill_value + fee

    def _apply_fill(self, intent: ExecutionIntent, fill_price: float, fee: float,
                    strategy_id: str, signal_confidence: Optional[float],
                    trade_idea_id: str = "") -> str:
        """Apply a confirmed fill to cash and positions. Returns the affected position_id."""
        market = intent.market
        fill_value = intent.size * fill_price

        if intent.direction == Direction.LONG:
            self.cash -= fill_value + fee
            position_id = str(uuid.uuid4())
            self.positions[position_id] = PositionRecord(
                position_id=position_id,
                market=market,
                size=intent.size,
                avg_price=fill_price,
                unrealized_pnl=0.0,
                timestamp=datetime.now(timezone.utc),
            )
            self._position_meta[position_id] = {
                "strategy_id": strategy_id,
                "direction": "long",
                "confidence": signal_confidence,
                "opened_at": datetime.now(timezone.utc),
                "market": market,
                "trade_idea_id": trade_idea_id,
                "entry_fee": fee,
                "trailing_high": fill_price,
                "trailing_low": None,
            }
            return position_id

        # SHORT — close oldest matching long (FIFO), or open a paper short
        market_longs = sorted(
            [(pid, p) for pid, p in self.positions.items()
             if p.market == market and p.size > 0],
            key=lambda x: x[1].timestamp,
        )

        if market_longs:
            # Closing a long: receive the sale proceeds minus fee
            self.cash += fill_value - fee
            pid, pos = market_longs[0]
            meta = self._position_meta.get(pid, {})
            meta["avg_price_at_close"] = pos.avg_price
            meta["size_at_close"] = pos.size
            meta["exit_price_at_close"] = fill_price
            meta["exit_fee_at_close"] = fee
            meta["market"] = market
            self._position_meta[pid] = meta
            del self.positions[pid]
            return pid

        # No long to close — open a paper short position.
        # Deduct only the entry fee; lock proceeds as margin so free cash cannot increase.
        self.cash -= fee
        position_id = str(uuid.uuid4())
        self.positions[position_id] = PositionRecord(
            position_id=position_id,
            market=market,
            size=-intent.size,
            avg_price=fill_price,
            unrealized_pnl=0.0,
            timestamp=datetime.now(timezone.utc),
        )
        self._position_meta[position_id] = {
            "strategy_id": strategy_id,
            "direction": "short",
            "confidence": signal_confidence,
            "opened_at": datetime.now(timezone.utc),
            "market": market,
            "trade_idea_id": trade_idea_id,
            "entry_fee": fee,
            "short_proceeds": fill_value,   # locked margin — released on close
            "trailing_high": None,
            "trailing_low": fill_price,
        }
        return position_id
