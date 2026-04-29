"""Live Kraken execution engine using python-kraken-sdk."""
import asyncio
import uuid
from datetime import datetime, timezone
from functools import partial
from typing import Awaitable, Callable, Optional, Tuple, TypedDict

from kraken.spot import Trade, User

from domain.models import Direction, ExecutionIntent, OrderRecord
from kraken_retry import call_with_kraken_backoff
from observability.logging import get_logger

logger = get_logger("kraken_execution")

_BASE_ALIASES = {"BTC": "XBT", "DOGE": "XDG"}


class KrakenAccountSnapshot(TypedDict):
    """Live account valuation displayed by the dashboard."""

    cash: float
    equity: float
    positions_value: float


# Run a blocking SDK call in the thread pool without blocking the event loop.
async def _in_thread(func, *args, **kwargs):
    loop = asyncio.get_running_loop()
    bound = partial(func, *args, **kwargs) if kwargs else partial(func, *args)
    return await loop.run_in_executor(None, bound)


# Convert internal symbols like BTC/EUR to Kraken order-pair altnames.
def _to_kraken_pair(symbol: str) -> str:
    if "/" not in symbol:
        return symbol
    base, quote = symbol.split("/", 1)
    return f"{_BASE_ALIASES.get(base, base)}{quote}"


class KrakenExecutionEngine:
    """Submit live spot orders to Kraken via python-kraken-sdk.

    Mirrors PaperExecutionEngine.execute() so the main strategy flow can route
    per-market without changing approval/risk logic.  Live orders return an empty
    position_id because exchange balances are the source of truth and fills may
    settle asynchronously after the order is accepted.
    """

    def __init__(
        self,
        api_key: Optional[str],
        api_secret: Optional[str],
        repository=None,
        user_client=None,   # injected in tests
        trade_client=None,  # injected in tests
        backoff_sleep: Callable[[float], Awaitable[None]] = asyncio.sleep,
        backoff_jitter: Optional[Callable[[float], float]] = None,
    ):
        self._repo = repository
        self._backoff_sleep = backoff_sleep
        self._backoff_jitter = backoff_jitter

        has_credentials = bool(api_key and api_secret)
        self._user: Optional[User] = (
            user_client if user_client is not None
            else (User(key=api_key, secret=api_secret) if has_credentials else None)
        )
        self._trade: Optional[Trade] = (
            trade_client if trade_client is not None
            else (Trade(key=api_key, secret=api_secret) if has_credentials else None)
        )

    async def execute(
        self,
        intent: ExecutionIntent,
        market_price: float,
        strategy_id: str = "",
        signal_confidence: Optional[float] = None,
        environment: str = "live",
        trade_idea_id: str = "",
    ) -> Tuple[OrderRecord, str]:
        """Submit a live market or limit order to Kraken."""
        del strategy_id
        order = OrderRecord(
            id=str(uuid.uuid4()),
            market=intent.market,
            direction=intent.direction,
            size=intent.size,
            price=intent.price or market_price,
            status="pending",
            timestamp=datetime.now(timezone.utc),
        )

        if self._trade is None:
            order.status = "rejected"
            order.exchange_order_id = "missing_api_credentials"
            self._save_rejected_trade(order, signal_confidence, "missing_api_credentials", trade_idea_id)
            return order, ""

        payload = {
            "ordertype": "limit" if intent.price else "market",
            "side": "buy" if intent.direction == Direction.LONG else "sell",
            "pair": _to_kraken_pair(intent.market),
            "volume": f"{intent.size:.8f}",
        }
        if intent.price:
            payload["price"] = f"{intent.price:.8f}"

        try:
            result = await call_with_kraken_backoff(
                lambda: _in_thread(self._trade.create_order, **payload),
                sleep=self._backoff_sleep,
                jitter=self._backoff_jitter,
                logger=logger,
                operation_name="Kraken AddOrder",
            )
        except Exception as exc:
            order.status = "rejected"
            order.exchange_order_id = str(exc)
            self._save_rejected_trade(order, signal_confidence, str(exc), trade_idea_id)
            logger.error("Kraken live order failed", extra={"market": intent.market, "error": str(exc)})
            return order, ""

        txids = result.get("txid") or []
        order.exchange_order_id = txids[0] if txids else ""
        self._save_order(order, intent.approval_request_id, environment, trade_idea_id)
        logger.info("Kraken live order submitted", extra={
            "market": intent.market,
            "direction": intent.direction.value,
            "txid": order.exchange_order_id,
        })
        return order, ""

    # Fetch live Kraken balances for cash and total equity.
    async def get_account_snapshot(self, quote_currency: str) -> Optional[KrakenAccountSnapshot]:
        """Return live cash and total equity from the Kraken account.

        cash  — fiat balance in the quote currency (e.g. ZEUR → EUR amount).
        equity — equivalent balance reported by Kraken TradeBalance (includes all
                 open positions marked to market by Kraken itself).
        """
        if self._user is None:
            logger.warning("Kraken account snapshot unavailable: missing API credentials")
            return None

        cash_asset = f"Z{quote_currency.upper()}"  # EUR → ZEUR, USD → ZUSD, GBP → ZGBP
        bare_asset = quote_currency.upper()        # fallback: "EUR" for accounts without Z-prefix

        try:
            balances = await _in_thread(self._user.get_account_balance)
            # Kraken uses Z-prefixed keys (ZEUR) for most accounts; some return bare keys (EUR).
            cash = float(balances.get(cash_asset) or balances.get(bare_asset, 0.0))
        except Exception as exc:
            logger.warning("Kraken get_account_balance failed", extra={"error": str(exc)})
            return None

        try:
            tb = await _in_thread(self._user.get_trade_balance, asset=cash_asset)
            equity = float(tb.get("eb", cash))
        except Exception as exc:
            logger.warning("Kraken get_trade_balance failed", extra={"error": str(exc)})
            equity = cash  # fall back to cash-only equity

        return {
            "cash": cash,
            "equity": equity,
            "positions_value": max(0.0, equity - cash),
        }

    async def reconcile_pending_orders(self) -> int:
        """Query Kraken for all pending live orders and update status in the repository.

        Returns the count of orders whose status changed.
        """
        if self._user is None or self._repo is None:
            return 0

        pending = self._repo.get_pending_live_orders()
        if not pending:
            return 0

        txids = [o["exchange_order_id"] for o in pending]
        try:
            orders_info = await call_with_kraken_backoff(
                lambda: _in_thread(
                    self._user.get_orders_info,
                    txid=",".join(txids),
                    trades=True,
                ),
                sleep=self._backoff_sleep,
                jitter=self._backoff_jitter,
                logger=logger,
                operation_name="Kraken QueryOrders",
            )
        except Exception as exc:
            logger.warning("Kraken get_orders_info failed", extra={"error": str(exc)})
            return 0

        updated = 0
        for order in pending:
            txid = order["exchange_order_id"]
            if txid not in orders_info:
                continue
            info = orders_info[txid]
            kraken_status = info.get("status", "")
            if kraken_status == "closed":
                fill_price = float(info.get("price", order["price"]))
                fee = float(info.get("fee", 0.0))
                self._repo.update_order_status_and_fill(order["id"], "filled", fill_price, fee)
                logger.info("Reconciled live order filled", extra={"txid": txid, "fill_price": fill_price})
                updated += 1
            elif kraken_status in ("canceled", "expired"):
                self._repo.update_order_status_and_fill(order["id"], kraken_status, order["price"], 0.0)
                logger.info("Reconciled live order", extra={"txid": txid, "status": kraken_status})
                updated += 1

        return updated

    def _save_rejected_trade(
        self,
        order: OrderRecord,
        confidence: Optional[float],
        reason: str,
        trade_idea_id: str,
    ) -> None:
        if self._repo:
            self._repo.save_rejected_trade(
                market=order.market,
                direction=order.direction.value,
                size=order.size,
                price=order.price,
                confidence=confidence,
                reason=reason,
                trade_idea_id=trade_idea_id,
                timestamp=order.timestamp,
            )

    def _save_order(
        self,
        order: OrderRecord,
        approval_id: str,
        environment: str,
        trade_idea_id: str,
    ) -> None:
        if self._repo:
            self._repo.save_order(
                order,
                approval_id,
                fee=0.0,
                environment=environment,
                trade_idea_id=trade_idea_id,
            )
