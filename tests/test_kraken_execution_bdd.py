"""BDD coverage for live Kraken execution."""
import unittest

from bdd_helpers import make_intent
from domain.models import Direction
from execution.kraken import KrakenExecutionEngine


class FakeKrakenUser:
    """Stub for kraken.spot.User — returns clean dicts, raises on error responses."""

    def __init__(self, responses=None, response=None):
        self._responses = list(responses or [])
        self._default = response or {"ZEUR": "0.00"}
        self.calls = []

    def _next(self, method, **kwargs):
        self.calls.append((method, kwargs))
        resp = self._responses.pop(0) if self._responses else self._default
        if isinstance(resp, Exception):
            raise resp
        if isinstance(resp, dict) and resp.get("error"):
            raise Exception(resp["error"][0] if resp["error"] else "API error")
        return resp.get("result", resp)

    def get_account_balance(self):
        return self._next("Balance")

    def get_trade_balance(self, asset="ZUSD"):
        return self._next("TradeBalance", asset=asset)

    def get_orders_info(self, txid, trades=False):
        return self._next("QueryOrders", txid=txid)


class FakeKrakenTrade:
    """Stub for kraken.spot.Trade — returns clean dicts, raises on error responses."""

    def __init__(self, responses=None, response=None):
        self._responses = list(responses or [])
        self._default = response or {"txid": ["TX123"]}
        self.calls = []

    def create_order(self, **kwargs):
        self.calls.append(("AddOrder", kwargs))
        resp = self._responses.pop(0) if self._responses else self._default
        if isinstance(resp, Exception):
            raise resp
        if isinstance(resp, dict) and resp.get("error"):
            raise Exception(resp["error"][0] if resp["error"] else "API error")
        return resp.get("result", resp)


class FakeRepo:
    """Minimal repository stub for reconciliation tests."""

    def __init__(self, pending_orders=None):
        self.pending_orders = pending_orders or []
        self.updates = []
        self.saved_orders = []
        self.saved_rejected_trades = []

    def get_pending_live_orders(self):
        return list(self.pending_orders)

    def update_order_status_and_fill(self, order_id, status, fill_price, fee):
        self.updates.append({"order_id": order_id, "status": status, "fill_price": fill_price, "fee": fee})

    def save_order(self, *args, **kwargs):
        self.saved_orders.append({"args": args, "kwargs": kwargs})

    def save_rejected_trade(self, **kwargs):
        self.saved_rejected_trades.append(kwargs)


class KrakenExecutionEngineBDDTests(unittest.IsolatedAsyncioTestCase):

    # GIVEN a live long intent WHEN Kraken accepts the order THEN AddOrder is called as a buy.
    async def test_given_live_long_intent_when_executed_then_market_buy_is_submitted(self) -> None:
        trade = FakeKrakenTrade()
        engine = KrakenExecutionEngine(
            api_key="key", api_secret="secret",
            trade_client=trade, user_client=FakeKrakenUser(),
        )

        order, position_id = await engine.execute(
            make_intent(market="BTC/EUR", direction=Direction.LONG, size=0.01),
            market_price=50_000.0,
        )

        self.assertEqual(order.status, "pending")
        self.assertEqual(order.exchange_order_id, "TX123")
        self.assertEqual(position_id, "")
        method, kwargs = trade.calls[0]
        self.assertEqual(method, "AddOrder")
        self.assertEqual(kwargs["pair"], "XBTEUR")
        self.assertEqual(kwargs["side"], "buy")
        self.assertEqual(kwargs["ordertype"], "market")

    # GIVEN Kraken returns an error WHEN a live order is submitted THEN the order is rejected.
    async def test_given_kraken_error_when_executed_then_order_is_rejected(self) -> None:
        trade = FakeKrakenTrade(response={"error": ["EOrder:Insufficient funds"], "result": {}})
        engine = KrakenExecutionEngine(
            api_key="key", api_secret="secret",
            trade_client=trade, user_client=FakeKrakenUser(),
        )

        order, position_id = await engine.execute(
            make_intent(market="ETH/EUR", direction=Direction.SHORT, size=0.5),
            market_price=2_000.0,
        )

        self.assertEqual(order.status, "rejected")
        self.assertIn("EOrder:Insufficient funds", order.exchange_order_id)
        self.assertEqual(position_id, "")
        self.assertEqual(len(trade.calls), 1)
        self.assertEqual(trade.calls[0][1]["side"], "sell")

    # FEAT-002: GIVEN Kraken rate-limits a live order WHEN execution retries
    # THEN the order is submitted after backoff instead of failing immediately.
    async def test_given_rate_limit_when_live_order_submitted_then_request_is_retried(self) -> None:
        sleeps = []

        async def fake_sleep(delay: float) -> None:
            sleeps.append(delay)

        trade = FakeKrakenTrade(responses=[
            {"error": ["EAPI:Rate limit exceeded"], "result": {}},
            {"txid": ["TX456"]},
        ])
        engine = KrakenExecutionEngine(
            api_key="key", api_secret="secret",
            trade_client=trade, user_client=FakeKrakenUser(),
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
        self.assertEqual(len(trade.calls), 2)
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

    # GIVEN live Kraken balances WHEN get_account_snapshot is called with a quote currency
    # THEN cash comes from Balance and equity from TradeBalance (eb field).
    async def test_given_kraken_balances_when_account_snapshot_requested_then_cash_and_equity_are_live(self) -> None:
        user = FakeKrakenUser(responses=[
            {"ZEUR": "250.00", "XXBT": "0.10000000"},   # Balance response
            {"eb": "9250.00", "tb": "9000.00"},           # TradeBalance response
        ])
        engine = KrakenExecutionEngine(
            api_key="key", api_secret="secret",
            user_client=user, trade_client=FakeKrakenTrade(),
        )

        snapshot = await engine.get_account_snapshot(quote_currency="EUR")

        self.assertEqual(user.calls[0][0], "Balance")
        self.assertEqual(user.calls[1][0], "TradeBalance")
        self.assertEqual(user.calls[1][1]["asset"], "ZEUR")
        self.assertAlmostEqual(snapshot["cash"], 250.0)
        self.assertAlmostEqual(snapshot["equity"], 9250.0)
        self.assertAlmostEqual(snapshot["positions_value"], 9000.0)

    # E13: GIVEN live execution rejects an order WHEN a repository is configured
    # THEN the order is stored in the rejected-trades register instead of the trade ledger.
    async def test_given_kraken_rejects_order_when_repo_present_then_rejected_trade_is_saved_not_order(self) -> None:
        trade = FakeKrakenTrade(response={"error": ["EOrder:Insufficient funds"], "result": {}})
        repo = FakeRepo()
        engine = KrakenExecutionEngine(
            api_key="key", api_secret="secret",
            trade_client=trade, user_client=FakeKrakenUser(),
            repository=repo,
        )

        order, position_id = await engine.execute(
            make_intent(market="ETH/EUR", direction=Direction.SHORT, size=0.5),
            market_price=2_000.0,
            signal_confidence=0.71,
            trade_idea_id="idea-live-rejected-1",
        )

        self.assertEqual(order.status, "rejected")
        self.assertEqual(position_id, "")
        self.assertEqual(repo.saved_orders, [])
        self.assertEqual(len(repo.saved_rejected_trades), 1)
        rejected = repo.saved_rejected_trades[0]
        self.assertEqual(rejected["market"], "ETH/EUR")
        self.assertEqual(rejected["direction"], "short")
        self.assertEqual(rejected["size"], 0.5)
        self.assertEqual(rejected["price"], 2_000.0)
        self.assertEqual(rejected["confidence"], 0.71)
        self.assertIn("EOrder:Insufficient funds", rejected["reason"])
        self.assertEqual(rejected["trade_idea_id"], "idea-live-rejected-1")

    # BUG-017: GIVEN a live order with a known txid WHEN reconciliation runs and Kraken reports filled
    # THEN the order record is updated to status="filled" with the actual fill price and fee.
    async def test_given_pending_order_when_kraken_reports_filled_then_order_is_updated(self) -> None:
        user = FakeKrakenUser(response={
            "TX999": {"status": "closed", "price": "51234.50", "fee": "1.34"},
        })
        repo = FakeRepo(pending_orders=[
            {"id": "order-abc", "exchange_order_id": "TX999", "price": 51000.0},
        ])
        engine = KrakenExecutionEngine(
            api_key="k", api_secret="s",
            user_client=user, trade_client=FakeKrakenTrade(),
            repository=repo,
        )

        updated = await engine.reconcile_pending_orders()

        self.assertEqual(updated, 1)
        self.assertEqual(user.calls[0][0], "QueryOrders")
        self.assertIn("TX999", user.calls[0][1]["txid"])
        self.assertEqual(repo.updates[0]["order_id"], "order-abc")
        self.assertEqual(repo.updates[0]["status"], "filled")
        self.assertAlmostEqual(repo.updates[0]["fill_price"], 51234.50)
        self.assertAlmostEqual(repo.updates[0]["fee"], 1.34)

    # BUG-017: GIVEN a pending order WHEN Kraken reports it canceled THEN order is marked canceled.
    async def test_given_pending_order_when_kraken_reports_canceled_then_order_is_canceled(self) -> None:
        user = FakeKrakenUser(response={
            "TX888": {"status": "canceled", "price": "0", "fee": "0"},
        })
        repo = FakeRepo(pending_orders=[
            {"id": "order-xyz", "exchange_order_id": "TX888", "price": 50000.0},
        ])
        engine = KrakenExecutionEngine(
            api_key="k", api_secret="s",
            user_client=user, trade_client=FakeKrakenTrade(),
            repository=repo,
        )

        updated = await engine.reconcile_pending_orders()

        self.assertEqual(updated, 1)
        self.assertEqual(repo.updates[0]["status"], "canceled")

    # BUG-017: GIVEN no pending orders WHEN reconciliation runs THEN no Kraken call is made.
    async def test_given_no_pending_orders_when_reconciliation_runs_then_no_api_call_is_made(self) -> None:
        user = FakeKrakenUser()
        repo = FakeRepo(pending_orders=[])
        engine = KrakenExecutionEngine(
            api_key="k", api_secret="s",
            user_client=user, trade_client=FakeKrakenTrade(),
            repository=repo,
        )

        updated = await engine.reconcile_pending_orders()

        self.assertEqual(updated, 0)
        self.assertEqual(user.calls, [])

    # BUG-017: GIVEN no API client WHEN reconciliation runs THEN zero is returned safely.
    async def test_given_no_api_client_when_reconciliation_runs_then_zero_returned(self) -> None:
        repo = FakeRepo(pending_orders=[{"id": "x", "exchange_order_id": "TX1", "price": 100.0}])
        engine = KrakenExecutionEngine(api_key=None, api_secret=None, repository=repo)

        updated = await engine.reconcile_pending_orders()

        self.assertEqual(updated, 0)

    # GIVEN no API credentials WHEN get_account_snapshot is called
    # THEN None is returned without making any API call.
    async def test_given_no_credentials_when_get_account_snapshot_then_returns_none(self) -> None:
        engine = KrakenExecutionEngine(api_key=None, api_secret=None)

        result = await engine.get_account_snapshot("EUR")

        self.assertIsNone(result)

    # GIVEN the Balance endpoint raises WHEN get_account_snapshot is called
    # THEN None is returned gracefully.
    async def test_given_balance_api_error_when_get_account_snapshot_then_returns_none(self) -> None:
        user = FakeKrakenUser(response={"error": ["EAPI:Invalid key"], "result": {}})
        engine = KrakenExecutionEngine(
            api_key="k", api_secret="s",
            user_client=user, trade_client=FakeKrakenTrade(),
        )

        result = await engine.get_account_snapshot("EUR")

        self.assertIsNone(result)

    # GIVEN a USD account WHEN get_account_snapshot is called
    # THEN it queries the ZUSD asset key for TradeBalance.
    async def test_given_usd_base_currency_when_account_snapshot_then_queries_zusd(self) -> None:
        user = FakeKrakenUser(responses=[
            {"ZUSD": "2000.00"},
            {"eb": "2500.00"},
        ])
        engine = KrakenExecutionEngine(
            api_key="k", api_secret="s",
            user_client=user, trade_client=FakeKrakenTrade(),
        )

        snapshot = await engine.get_account_snapshot("USD")

        self.assertAlmostEqual(snapshot["cash"], 2000.0)
        self.assertAlmostEqual(snapshot["equity"], 2500.0)
        self.assertEqual(user.calls[1][1]["asset"], "ZUSD")

    # GIVEN an account that returns "EUR" key (no Z-prefix) WHEN get_account_snapshot is called
    # THEN cash is still read correctly — not reported as 0.
    async def test_given_balance_without_z_prefix_when_account_snapshot_then_cash_is_nonzero(self) -> None:
        user = FakeKrakenUser(responses=[
            {"EUR": "850.00", "XXBT": "0.01000000"},  # no ZEUR key
            {"eb": "1650.00"},
        ])
        engine = KrakenExecutionEngine(
            api_key="k", api_secret="s",
            user_client=user, trade_client=FakeKrakenTrade(),
        )

        snapshot = await engine.get_account_snapshot("EUR")

        self.assertAlmostEqual(snapshot["cash"], 850.0)
        self.assertAlmostEqual(snapshot["equity"], 1650.0)
        self.assertAlmostEqual(snapshot["positions_value"], 800.0)


if __name__ == "__main__":
    unittest.main()
