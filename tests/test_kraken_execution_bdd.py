"""BDD coverage for live Kraken execution."""
import unittest

from bdd_helpers import make_intent
from domain.models import Direction
from execution.kraken import KrakenExecutionEngine


class FakeKrakenAPI:
    def __init__(self, response=None, responses=None):
        self.response = response or {"error": [], "result": {"txid": ["TX123"]}}
        self.responses = list(responses or [])
        self.calls = []

    # Capture private Kraken calls without touching the network.
    def query_private(self, method: str, data: dict):
        self.calls.append((method, data))
        response = self.responses.pop(0) if self.responses else self.response
        if isinstance(response, Exception):
            raise response
        return response


class FakeRepo:
    """Minimal repository stub for reconciliation tests."""

    def __init__(self, pending_orders=None):
        self.pending_orders = pending_orders or []
        self.updates = []

    def get_pending_live_orders(self):
        return list(self.pending_orders)

    def update_order_status_and_fill(self, order_id, status, fill_price, fee):
        self.updates.append({"order_id": order_id, "status": status, "fill_price": fill_price, "fee": fee})

    def save_order(self, *args, **kwargs):
        pass


class KrakenExecutionEngineBDDTests(unittest.IsolatedAsyncioTestCase):

    # GIVEN a live long intent WHEN Kraken accepts the order THEN AddOrder is called as a buy.
    async def test_given_live_long_intent_when_executed_then_market_buy_is_submitted(self) -> None:
        api = FakeKrakenAPI()
        engine = KrakenExecutionEngine(api_key="key", api_secret="secret", api_client=api)

        order, position_id = await engine.execute(
            make_intent(market="BTC/EUR", direction=Direction.LONG, size=0.01),
            market_price=50_000.0,
        )

        self.assertEqual(order.status, "pending")
        self.assertEqual(order.exchange_order_id, "TX123")
        self.assertEqual(position_id, "")
        self.assertEqual(api.calls[0][0], "AddOrder")
        self.assertEqual(api.calls[0][1]["pair"], "XBTEUR")
        self.assertEqual(api.calls[0][1]["type"], "buy")
        self.assertEqual(api.calls[0][1]["ordertype"], "market")

    # GIVEN Kraken returns an error WHEN a live order is submitted THEN the order is rejected.
    async def test_given_kraken_error_when_executed_then_order_is_rejected(self) -> None:
        api = FakeKrakenAPI({"error": ["EOrder:Insufficient funds"], "result": {}})
        engine = KrakenExecutionEngine(api_key="key", api_secret="secret", api_client=api)

        order, position_id = await engine.execute(
            make_intent(market="ETH/EUR", direction=Direction.SHORT, size=0.5),
            market_price=2_000.0,
        )

        self.assertEqual(order.status, "rejected")
        self.assertEqual(order.exchange_order_id, "EOrder:Insufficient funds")
        self.assertEqual(position_id, "")
        self.assertEqual(api.calls[0][1]["type"], "sell")
        self.assertEqual(len(api.calls), 1)

    # FEAT-002: GIVEN Kraken rate-limits a live order WHEN execution retries
    # THEN the order is submitted after backoff instead of failing immediately.
    async def test_given_rate_limit_when_live_order_submitted_then_request_is_retried(self) -> None:
        sleeps = []

        async def fake_sleep(delay: float) -> None:
            sleeps.append(delay)

        api = FakeKrakenAPI(responses=[
            {"error": ["EAPI:Rate limit exceeded"], "result": {}},
            {"error": [], "result": {"txid": ["TX456"]}},
        ])
        engine = KrakenExecutionEngine(
            api_key="key",
            api_secret="secret",
            api_client=api,
            backoff_sleep=fake_sleep,
            backoff_jitter=lambda delay: 0.0,
        )

        order, position_id = await engine.execute(
            make_intent(market="BTC/EUR", direction=Direction.LONG, size=0.01),
            market_price=50_000.0,
        )

        self.assertEqual(order.status, "pending")
        self.assertEqual(order.exchange_order_id, "TX456")
        self.assertEqual(position_id, "")
        self.assertEqual(len(api.calls), 2)
        self.assertEqual(sleeps, [0.5])

    # GIVEN no API credentials WHEN live execution is requested THEN no Kraken call is made.
    async def test_given_missing_credentials_when_executed_then_order_is_rejected_locally(self) -> None:
        engine = KrakenExecutionEngine(api_key=None, api_secret=None)

        order, position_id = await engine.execute(
            make_intent(market="BTC/EUR", direction=Direction.LONG, size=0.01),
            market_price=50_000.0,
        )

        self.assertEqual(order.status, "rejected")
        self.assertEqual(order.exchange_order_id, "missing_api_credentials")
        self.assertEqual(position_id, "")

    # BUG-017: GIVEN a live order with a known txid WHEN reconciliation runs and Kraken reports filled
    # THEN the order record is updated to status="filled" with the actual fill price and fee.
    async def test_given_pending_order_when_kraken_reports_filled_then_order_is_updated(self) -> None:
        api = FakeKrakenAPI({
            "error": [],
            "result": {
                "TX999": {"status": "closed", "price": "51234.50", "fee": "1.34"},
            },
        })
        repo = FakeRepo(pending_orders=[
            {"id": "order-abc", "exchange_order_id": "TX999", "price": 51000.0},
        ])
        engine = KrakenExecutionEngine(api_key="k", api_secret="s", api_client=api, repository=repo)

        updated = await engine.reconcile_pending_orders()

        self.assertEqual(updated, 1)
        self.assertEqual(api.calls[0][0], "QueryOrders")
        self.assertIn("TX999", api.calls[0][1]["txid"])
        self.assertEqual(repo.updates[0]["order_id"], "order-abc")
        self.assertEqual(repo.updates[0]["status"], "filled")
        self.assertAlmostEqual(repo.updates[0]["fill_price"], 51234.50)
        self.assertAlmostEqual(repo.updates[0]["fee"], 1.34)

    # BUG-017: GIVEN a pending order WHEN Kraken reports it canceled THEN order is marked canceled.
    async def test_given_pending_order_when_kraken_reports_canceled_then_order_is_canceled(self) -> None:
        api = FakeKrakenAPI({
            "error": [],
            "result": {"TX888": {"status": "canceled", "price": "0", "fee": "0"}},
        })
        repo = FakeRepo(pending_orders=[
            {"id": "order-xyz", "exchange_order_id": "TX888", "price": 50000.0},
        ])
        engine = KrakenExecutionEngine(api_key="k", api_secret="s", api_client=api, repository=repo)

        updated = await engine.reconcile_pending_orders()

        self.assertEqual(updated, 1)
        self.assertEqual(repo.updates[0]["status"], "canceled")

    # BUG-017: GIVEN no pending orders WHEN reconciliation runs THEN no Kraken call is made.
    async def test_given_no_pending_orders_when_reconciliation_runs_then_no_api_call_is_made(self) -> None:
        api = FakeKrakenAPI()
        repo = FakeRepo(pending_orders=[])
        engine = KrakenExecutionEngine(api_key="k", api_secret="s", api_client=api, repository=repo)

        updated = await engine.reconcile_pending_orders()

        self.assertEqual(updated, 0)
        self.assertEqual(api.calls, [])

    # BUG-017: GIVEN no API client WHEN reconciliation runs THEN zero is returned safely.
    async def test_given_no_api_client_when_reconciliation_runs_then_zero_returned(self) -> None:
        repo = FakeRepo(pending_orders=[{"id": "x", "exchange_order_id": "TX1", "price": 100.0}])
        engine = KrakenExecutionEngine(api_key=None, api_secret=None, repository=repo)

        updated = await engine.reconcile_pending_orders()

        self.assertEqual(updated, 0)


if __name__ == "__main__":
    unittest.main()
