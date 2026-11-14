// Package approval holds the operator approval queue shared by the HTTP API
// and any CLI, so both approval paths see identical state.
package approval

import (
	"sync"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/observability"
)

var logger = observability.Logger("approval_service")

// DefaultTTL is how long an approval request stays valid.
const DefaultTTL = 30 * time.Minute

// Repository persists approval requests (implemented by the storage repository).
type Repository interface {
	SaveApprovalRequest(req domain.ApprovalRequest) error
	UpdateApprovalStatus(approvalID, status string) error
	ClearPendingApprovals() (int, error)
	LoadPendingApprovalRequests(now time.Time) ([]domain.ApprovalRequest, error)
}

// Service is an in-memory approval queue with expiry, mirrored to a repository
// when one is configured. It is safe for concurrent use.
type Service struct {
	mu      sync.Mutex
	ttl     time.Duration
	repo    Repository
	now     func() time.Time
	pending []domain.ApprovalRequest // submission order
}

// NewService creates an approval queue. repo may be nil (in-memory only) and a
// nil clock defaults to the current UTC time.
func NewService(ttl time.Duration, repo Repository, now func() time.Time) *Service {
	if now == nil {
		now = domain.NowUTC
	}
	return &Service{ttl: ttl, repo: repo, now: now}
}

// HasPendingForMarket reports whether a live approval exists for the market.
func (s *Service) HasPendingForMarket(market string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked()
	return s.hasMarketLocked(market)
}

// Submit queues a new approval request. It returns false (no-op) when an
// approval for the same market is already pending, preventing duplicates.
func (s *Service) Submit(idea domain.TradeIdea, risk domain.RiskDecision) (domain.ApprovalRequest, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked()
	if s.hasMarketLocked(idea.Market) {
		return domain.ApprovalRequest{}, false
	}
	req := domain.ApprovalRequest{
		ID:           domain.NewID(),
		TradeIdea:    idea,
		RiskDecision: risk,
		ExpiresAt:    s.now().UTC().Add(s.ttl),
		Status:       "pending",
	}
	s.pending = append(s.pending, req)
	if s.repo != nil {
		if err := s.repo.SaveApprovalRequest(req); err != nil {
			logger.Error("Could not persist approval request", "approval_id", req.ID, "error", err.Error())
		}
	}
	logger.Info("Approval submitted",
		"approval_id", req.ID, "market", idea.Market, "direction", string(idea.Direction))
	return req, true
}

// Approve marks a valid approval as approved and removes it from the queue.
func (s *Service) Approve(approvalID string) (domain.ApprovalRequest, bool) {
	return s.resolve(approvalID, "approved")
}

// Reject marks a valid approval as rejected and removes it from the queue.
func (s *Service) Reject(approvalID string) (domain.ApprovalRequest, bool) {
	return s.resolve(approvalID, "rejected")
}

// ClearPending discards every pending approval (e.g. on emergency stop) and
// returns how many were cleared.
func (s *Service) ClearPending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := len(s.pending)
	if s.repo != nil {
		if _, err := s.repo.ClearPendingApprovals(); err != nil {
			logger.Error("Could not clear persisted approvals", "error", err.Error())
		}
	}
	s.pending = nil
	logger.Warn("Approval queue cleared", "cleared", count)
	return count
}

// GetPending returns the non-expired pending approvals in submission order.
func (s *Service) GetPending() []domain.ApprovalRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked()
	return append([]domain.ApprovalRequest{}, s.pending...)
}

// Get returns the approval only if it exists and has not expired.
func (s *Service) Get(approvalID string) (domain.ApprovalRequest, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.validIndexLocked(approvalID)
	if i < 0 {
		return domain.ApprovalRequest{}, false
	}
	return s.pending[i], true
}

// LoadPendingFromRepository replaces the queue with persisted pending,
// non-expired approvals and returns how many were loaded.
func (s *Service) LoadPendingFromRepository() (int, error) {
	if s.repo == nil {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	loaded, err := s.repo.LoadPendingApprovalRequests(s.now().UTC())
	if err != nil {
		return 0, err
	}
	s.pending = append([]domain.ApprovalRequest{}, loaded...)
	return len(s.pending), nil
}

// resolve applies a final status to a valid approval and removes it.
func (s *Service) resolve(approvalID, status string) (domain.ApprovalRequest, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.validIndexLocked(approvalID)
	if i < 0 {
		return domain.ApprovalRequest{}, false
	}
	req := s.pending[i]
	req.Status = status
	s.removeLocked(i)
	s.updateStatusLocked(approvalID, status)
	logger.Info("Approval "+status, "approval_id", approvalID)
	return req, true
}

// validIndexLocked returns the queue index of a non-expired approval, expiring
// it (and returning -1) if its TTL has elapsed. The caller holds s.mu.
func (s *Service) validIndexLocked(approvalID string) int {
	for i, req := range s.pending {
		if req.ID != approvalID {
			continue
		}
		if req.ExpiresAt.Before(s.now()) {
			s.removeLocked(i)
			s.updateStatusLocked(approvalID, "expired")
			logger.Warn("Approval expired", "approval_id", approvalID)
			return -1
		}
		return i
	}
	return -1
}

// purgeExpiredLocked drops expired approvals; the caller holds s.mu.
func (s *Service) purgeExpiredLocked() {
	now := s.now()
	kept := s.pending[:0]
	for _, req := range s.pending {
		if req.ExpiresAt.Before(now) {
			s.updateStatusLocked(req.ID, "expired")
			logger.Info("Purged expired approval", "approval_id", req.ID)
			continue
		}
		kept = append(kept, req)
	}
	s.pending = kept
}

// hasMarketLocked reports whether any queued approval targets market.
func (s *Service) hasMarketLocked(market string) bool {
	for _, req := range s.pending {
		if req.TradeIdea.Market == market {
			return true
		}
	}
	return false
}

// removeLocked deletes the queue entry at index i, preserving order.
func (s *Service) removeLocked(i int) {
	s.pending = append(s.pending[:i], s.pending[i+1:]...)
}

// updateStatusLocked persists a status change best-effort.
func (s *Service) updateStatusLocked(approvalID, status string) {
	if s.repo == nil {
		return
	}
	if err := s.repo.UpdateApprovalStatus(approvalID, status); err != nil {
		logger.Error("Could not persist approval status",
			"approval_id", approvalID, "status", status, "error", err.Error())
	}
}
