"""Live Kraken execution engine."""
import asyncio
import uuid
from datetime import datetime, timezone
from functools import partial
from typing import Awaitable, Callable, Optional, Tuple

import krakenex

from domain.models import Direction, ExecutionIntent, OrderRecord
from kraken_retry import call_with_kraken_backoff
from observability.logging import get_logger

logger = get_logger("kraken_execution")

_BASE_ALIASES = {"BTC": "XBT", "DOGE": "XDG"}


# Run a blocking Kraken client call without blocking the event loop.
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
    """Submit live spot orders to Kraken.

    The engine intentionally mirrors PaperExecutionEngine.execute() so the main
    strategy flow can route per-market without changing approval/risk logic.
    Live orders return an empty position_id because exchange balances are the
    source of truth and fills may settle asynchronously after AddOrder accepts.
    """

    def __init__(
        self,
        api_key: Optional[str],
        api_secret: Optional[str],
        repository=None,
        api_client=None,
        backoff_sleep: Callable[[float], Awaitable[None]] = asyncio.sleep,
        backoff_jitter: Optional[Callable[[float], float]] = None,
    ):
        self._repo = repository
        self._api_key = api_key
        self._api_secret = api_secret
        self._api = api_client or (
            krakenex.API(api_key, api_secret) if api_key and api_secret else None
        )
        self._backoff_sleep = backoff_sleep
        self._backoff_jitter = backoff_jitter

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
        del strategy_id, signal_confidence
        order = OrderRecord(
            id=str(uuid.uuid4()),
            market=intent.market,
            direction=intent.direction,
            size=intent.size,
            price=intent.price or market_price,
            status="pending",
            timestamp=datetime.now(timezone.utc),
        )

        if self._api is None:
            order.status = "rejected"
            order.exchange_order_id = "missing_api_credentials"
            self._save_order(order, intent.approval_request_id, environment, trade_idea_id)
            return order, ""

        payload = self._build_add_order_payload(intent)
        try:
            response = await call_with_kraken_backoff(
                lambda: _in_thread(self._api.query_private, "AddOrder", payload),
                sleep=self._backoff_sleep,
                jitter=self._backoff_jitter,
                logger=logger,
                operation_name="Kraken AddOrder",
            )
        except Exception as exc:
            order.status = "rejected"
            order.exchange_order_id = str(exc)
            self._save_order(order, intent.approval_request_id, environment, trade_idea_id)
            logger.error("Kraken live order failed", extra={"market": intent.market, "error": str(exc)})
            return order, ""

        errors = response.get("error") or []
        if errors:
            order.status = "rejected"
            order.exchange_order_id = "; ".join(errors)
            self._save_order(order, intent.approval_request_id, environment, trade_idea_id)
            logger.warning("Kraken live order rejected", extra={"market": intent.market, "error": errors})
            return order, ""

        txids = response.get("result", {}).get("txid") or []
        order.exchange_order_id = txids[0] if txids else ""
        self._save_order(order, intent.approval_request_id, environment, trade_idea_id)
        logger.info("Kraken live order submitted", extra={
            "market": intent.market,
            "direction": intent.direction.value,
            "txid": order.exchange_order_id,
        })
        return order, ""

    async def reconcile_pending_orders(self) -> int:
        """Query Kraken for all pending live orders and update status in the repository.

        Returns the count of orders whose status changed.
        Calls QueryOrders with the txids of every pending live order and stamps
        status="filled" (with fill price/fee) or "canceled"/"expired" as appropriate.
        """
        if self._api is None or self._repo is None:
            return 0

        pending = self._repo.get_pending_live_orders()
        if not pending:
            return 0

        txids = [o["exchange_order_id"] for o in pending]
        try:
            response = await call_with_kraken_backoff(
                lambda: _in_thread(
                    self._api.query_private,
                    "QueryOrders",
                    {"txid": ",".join(txids), "trades": True},
                ),
                sleep=self._backoff_sleep,
                jitter=self._backoff_jitter,
                logger=logger,
                operation_name="Kraken QueryOrders",
            )
        except Exception as exc:
            logger.warning("Kraken QueryOrders failed", extra={"error": str(exc)})
            return 0

        errors = response.get("error") or []
        if errors:
            logger.warning("Kraken QueryOrders error", extra={"errors": errors})
            return 0

        result = response.get("result", {})
        updated = 0
        for order in pending:
            txid = order["exchange_order_id"]
            if txid not in result:
                continue
            info = result[txid]
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

    def _build_add_order_payload(self, intent: ExecutionIntent) -> dict:
        """Build Kraken AddOrder payload from an execution intent."""
        payload = {
            "pair": _to_kraken_pair(intent.market),
            "type": "buy" if intent.direction == Direction.LONG else "sell",
            "ordertype": "limit" if intent.price else "market",
            "volume": f"{intent.size:.8f}",
        }
        if intent.price:
            payload["price"] = f"{intent.price:.8f}"
        return payload

    def _save_order(
        self,
        order: OrderRecord,
        approval_id: str,
        environment: str,
        trade_idea_id: str,
    ) -> None:
        """Persist a live order when a repository is configured."""
        if self._repo:
            self._repo.save_order(
                order,
                approval_id,
                fee=0.0,
                environment=environment,
                trade_idea_id=trade_idea_id,
            )
