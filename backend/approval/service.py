"""
Approval service — shared by browser UI and CLI.
"""
import uuid
from datetime import datetime, timedelta, timezone
from typing import Any, Dict, List, Optional

from domain.models import ApprovalRequest, RiskDecision, TradeIdea
from observability.logging import get_logger

logger = get_logger("approval_service")

DEFAULT_TTL_MINUTES = 30


class ApprovalService:
    """In-memory approval queue with expiry.

    Both the FastAPI endpoints and the CLI read/write through this single
    instance so the two approval paths share identical state.
    """

    def __init__(self, ttl_minutes: int = DEFAULT_TTL_MINUTES, repository: Any = None):
        self._ttl = timedelta(minutes=ttl_minutes)
        self._pending: Dict[str, ApprovalRequest] = {}
        self._repository = repository

    # ------------------------------------------------------------------
    # Write side
    # ------------------------------------------------------------------

    def has_pending_for_market(self, market: str) -> bool:
        """True if there is already a live (non-expired) approval for this market."""
        self._purge_expired()
        return any(r.trade_idea.market == market for r in self._pending.values())

    def submit(self, trade_idea: TradeIdea, risk_decision: RiskDecision) -> Optional[ApprovalRequest]:
        """Create a new approval request and add it to the queue.

        Returns None (no-op) if an approval for the same market is already pending,
        preventing the queue from accumulating duplicates across ticks.
        """
        if self.has_pending_for_market(trade_idea.market):
            return None
        request = ApprovalRequest(
            id=str(uuid.uuid4()),
            trade_idea=trade_idea,
            risk_decision=risk_decision,
            expires_at=datetime.now(timezone.utc) + self._ttl,
            status="pending",
        )
        self._pending[request.id] = request
        if self._repository:
            self._repository.save_approval_request(request)
        logger.info("Approval submitted", extra={
            "approval_id": request.id,
            "market": trade_idea.market,
            "direction": trade_idea.direction.value,
        })
        return request

    def approve(self, approval_id: str) -> Optional[ApprovalRequest]:
        """Mark an approval as approved and remove it from the queue."""
        req = self._get_if_valid(approval_id)
        if req is None:
            return None
        req.status = "approved"
        del self._pending[approval_id]
        if self._repository:
            self._repository.update_approval_status(approval_id, "approved")
        logger.info("Approval approved", extra={"approval_id": approval_id})
        return req

    def clear_pending(self) -> int:
        """Discard all pending approvals (e.g. on emergency stop).

        Returns the number of requests that were cleared.
        """
        count = len(self._pending)
        if self._repository:
            self._repository.clear_pending_approvals()
        self._pending.clear()
        logger.warning("Approval queue cleared", extra={"cleared": count})
        return count

    def reject(self, approval_id: str) -> Optional[ApprovalRequest]:
        """Mark an approval as rejected and remove it from the queue."""
        req = self._get_if_valid(approval_id)
        if req is None:
            return None
        req.status = "rejected"
        del self._pending[approval_id]
        if self._repository:
            self._repository.update_approval_status(approval_id, "rejected")
        logger.info("Approval rejected", extra={"approval_id": approval_id})
        return req

    # ------------------------------------------------------------------
    # Read side
    # ------------------------------------------------------------------

    def get_pending(self) -> List[ApprovalRequest]:
        """Return non-expired pending approvals, purging expired ones."""
        self._purge_expired()
        return list(self._pending.values())

    def get(self, approval_id: str) -> Optional[ApprovalRequest]:
        """Return the approval only if it exists and has not expired."""
        return self._get_if_valid(approval_id)

    def load_pending_from_repository(self) -> int:
        """Load persisted pending approvals from the repository.

        Returns the number of pending approvals loaded.
        """
        if not self._repository:
            return 0
        loaded = self._repository.load_pending_approval_requests(datetime.now(timezone.utc))
        self._pending = {request.id: request for request in loaded}
        return len(self._pending)

    # ------------------------------------------------------------------
    # Internal helpers
    # ------------------------------------------------------------------

    def _get_if_valid(self, approval_id: str) -> Optional[ApprovalRequest]:
        req = self._pending.get(approval_id)
        if req is None:
            return None
        if req.expires_at < datetime.now(timezone.utc):
            req.status = "expired"
            del self._pending[approval_id]
            if self._repository:
                self._repository.update_approval_status(approval_id, "expired")
            logger.warning("Approval expired", extra={"approval_id": approval_id})
            return None
        return req

    def _purge_expired(self) -> None:
        now = datetime.now(timezone.utc)
        expired = [id for id, req in self._pending.items() if req.expires_at < now]
        for id in expired:
            self._pending[id].status = "expired"
            del self._pending[id]
            if self._repository:
                self._repository.update_approval_status(id, "expired")
            logger.info("Purged expired approval", extra={"approval_id": id})
