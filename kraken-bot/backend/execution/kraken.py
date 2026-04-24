"""Live Kraken execution engine."""
import asyncio
import uuid
from datetime import datetime
from functools import partial
from typing import Optional, Tuple

import krakenex

from domain.models import Direction, ExecutionIntent, OrderRecord
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
    ):
        self._repo = repository
        self._api_key = api_key
        self._api_secret = api_secret
        self._api = api_client or (
            krakenex.API(api_key, api_secret) if api_key and api_secret else None
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
        del strategy_id, signal_confidence
        order = OrderRecord(
            id=str(uuid.uuid4()),
            market=intent.market,
            direction=intent.direction,
            size=intent.size,
            price=intent.price or market_price,
            status="pending",
            timestamp=datetime.utcnow(),
        )

        if self._api is None:
            order.status = "rejected"
            order.exchange_order_id = "missing_api_credentials"
            self._save_order(order, intent.approval_request_id, environment, trade_idea_id)
            return order, ""

        payload = self._build_add_order_payload(intent)
        try:
            response = await _in_thread(self._api.query_private, "AddOrder", payload)
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
