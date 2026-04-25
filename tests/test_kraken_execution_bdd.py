"""BDD coverage for live Kraken execution."""
import unittest

from bdd_helpers import make_intent
from domain.models import Direction
from execution.kraken import KrakenExecutionEngine


class FakeKrakenAPI:
    def __init__(self, response=None):
        self.response = response or {"error": [], "result": {"txid": ["TX123"]}}
        self.calls = []

    # Capture private Kraken calls without touching the network.
    def query_private(self, method: str, data: dict):
        self.calls.append((method, data))
        return self.response


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


if __name__ == "__main__":
    unittest.main()
