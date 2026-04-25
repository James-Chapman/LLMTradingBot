"""BDD coverage for the approval queue."""
import unittest
from datetime import datetime, timedelta, timezone

from bdd_helpers import make_risk_decision, make_trade_idea
from approval.service import ApprovalService
from storage.database import init_database
from storage.repository import Repository


class CapturingApprovalRepository:
    """Capture approval persistence updates."""

    def __init__(self) -> None:
        self.status_updates = []

    # GIVEN an approval is submitted WHEN persisted THEN ignore the full payload.
    def save_approval_request(self, _request) -> None:
        return None

    # GIVEN approval status changes WHEN updated THEN capture the new status.
    def update_approval_status(self, approval_id: str, status: str) -> None:
        self.status_updates.append((approval_id, status))


class ApprovalServiceBDDTests(unittest.TestCase):
    # GIVEN a valid signal WHEN it is submitted THEN it appears in the pending queue.
    def test_given_trade_idea_when_submitted_then_request_is_pending(self) -> None:
        service = ApprovalService(ttl_minutes=30)
        idea = make_trade_idea()
        risk = make_risk_decision(idea)

        request = service.submit(idea, risk)

        self.assertIsNotNone(request)
        self.assertEqual(request.status, "pending")
        self.assertEqual(service.get_pending(), [request])

    # GIVEN a market already awaiting approval WHEN another signal arrives THEN it is not queued twice.
    def test_given_pending_market_when_duplicate_signal_submitted_then_queue_is_unchanged(self) -> None:
        service = ApprovalService(ttl_minutes=30)
        first_idea = make_trade_idea(market="ETH/EUR")
        second_idea = make_trade_idea(market="ETH/EUR")

        first_request = service.submit(first_idea, make_risk_decision(first_idea))
        second_request = service.submit(second_idea, make_risk_decision(second_idea))

        self.assertIsNone(second_request)
        self.assertEqual(service.get_pending(), [first_request])

    # GIVEN a pending approval WHEN it is approved THEN it is removed from the queue.
    def test_given_pending_request_when_approved_then_request_is_returned_and_removed(self) -> None:
        service = ApprovalService(ttl_minutes=30)
        idea = make_trade_idea()
        request = service.submit(idea, make_risk_decision(idea))

        approved = service.approve(request.id)

        self.assertEqual(approved.status, "approved")
        self.assertEqual(service.get_pending(), [])

    # GIVEN an expired request WHEN pending requests are read THEN the expired request is purged.
    def test_given_expired_request_when_pending_read_then_request_is_purged(self) -> None:
        service = ApprovalService(ttl_minutes=30)
        idea = make_trade_idea()
        request = service.submit(idea, make_risk_decision(idea))
        request.expires_at = datetime.now(timezone.utc) - timedelta(seconds=1)

        pending = service.get_pending()

        self.assertEqual(pending, [])
        self.assertIsNone(service.get(request.id))

    # GIVEN an expired approval WHEN reject is called THEN it expires instead of being rejected.
    def test_given_expired_request_when_rejected_then_request_is_marked_expired(self) -> None:
        repository = CapturingApprovalRepository()
        service = ApprovalService(ttl_minutes=30, repository=repository)
        idea = make_trade_idea()
        request = service.submit(idea, make_risk_decision(idea))
        request.expires_at = datetime.now(timezone.utc) - timedelta(seconds=1)

        rejected = service.reject(request.id)

        self.assertIsNone(rejected)
        self.assertIsNone(service.get(request.id))
        self.assertEqual(repository.status_updates[-1], (request.id, "expired"))

    # GIVEN multiple pending approvals WHEN clear_pending is called THEN all are removed
    # and the count of cleared items is returned.
    def test_given_pending_approvals_when_clear_pending_called_then_queue_is_empty(self) -> None:
        service = ApprovalService(ttl_minutes=30)
        for market in ("BTC/EUR", "ETH/EUR", "SOL/EUR"):
            idea = make_trade_idea(market=market)
            service.submit(idea, make_risk_decision(idea))

        cleared = service.clear_pending()

        self.assertEqual(cleared, 3)
        self.assertEqual(service.get_pending(), [])

    # GIVEN a pending approval was persisted WHEN a new service instance loads
    # THEN the pending request is restored with its trade idea and risk decision.
    def test_given_persisted_pending_approval_when_service_reloads_then_request_is_restored(self) -> None:
        init_database("sqlite://")
        repository = Repository()
        service = ApprovalService(ttl_minutes=30, repository=repository)
        idea = make_trade_idea(market="LINK/EUR")
        risk = make_risk_decision(idea, reason="size adjusted")

        request = service.submit(idea, risk)
        reloaded = ApprovalService(ttl_minutes=30, repository=repository)
        reloaded.load_pending_from_repository()

        pending = reloaded.get_pending()
        self.assertEqual(len(pending), 1)
        self.assertEqual(pending[0].id, request.id)
        self.assertEqual(pending[0].trade_idea.market, "LINK/EUR")
        self.assertEqual(pending[0].risk_decision.reason, "size adjusted")

    # GIVEN a persisted approval is approved WHEN a new service instance loads
    # THEN the approved request is not restored as pending.
    def test_given_persisted_approval_when_approved_then_reload_has_no_pending_request(self) -> None:
        init_database("sqlite://")
        repository = Repository()
        service = ApprovalService(ttl_minutes=30, repository=repository)
        idea = make_trade_idea(market="SOL/EUR")
        request = service.submit(idea, make_risk_decision(idea))

        service.approve(request.id)
        reloaded = ApprovalService(ttl_minutes=30, repository=repository)
        reloaded.load_pending_from_repository()

        self.assertEqual(reloaded.get_pending(), [])


if __name__ == "__main__":
    unittest.main()
