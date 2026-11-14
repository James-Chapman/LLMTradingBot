package approval

import (
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

// testClock is an adjustable clock for expiry scenarios.
type testClock struct{ now time.Time }

// Now returns the current fake time.
func (c *testClock) Now() time.Time { return c.now }

// Advance moves the fake time forward.
func (c *testClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

// newClock creates a clock at a fixed instant.
func newClock() *testClock {
	return &testClock{now: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}
}

// statusUpdate is one captured status change.
type statusUpdate struct{ id, status string }

// fakeRepo is an in-memory approval repository that behaves like storage.
type fakeRepo struct {
	rows          map[string]domain.ApprovalRequest
	statusUpdates []statusUpdate
}

// newFakeRepo creates an empty fake repository.
func newFakeRepo() *fakeRepo { return &fakeRepo{rows: map[string]domain.ApprovalRequest{}} }

// SaveApprovalRequest stores the request.
func (f *fakeRepo) SaveApprovalRequest(req domain.ApprovalRequest) error {
	f.rows[req.ID] = req
	return nil
}

// UpdateApprovalStatus records and applies a status change.
func (f *fakeRepo) UpdateApprovalStatus(id, status string) error {
	f.statusUpdates = append(f.statusUpdates, statusUpdate{id, status})
	if row, ok := f.rows[id]; ok {
		row.Status = status
		f.rows[id] = row
	}
	return nil
}

// ClearPendingApprovals marks every pending row cleared.
func (f *fakeRepo) ClearPendingApprovals() (int, error) {
	count := 0
	for id, row := range f.rows {
		if row.Status == "pending" {
			row.Status = "cleared"
			f.rows[id] = row
			count++
		}
	}
	return count, nil
}

// LoadPendingApprovalRequests returns pending, unexpired rows.
func (f *fakeRepo) LoadPendingApprovalRequests(now time.Time) ([]domain.ApprovalRequest, error) {
	var out []domain.ApprovalRequest
	for _, row := range f.rows {
		if row.Status == "pending" && row.ExpiresAt.After(now) {
			out = append(out, row)
		}
	}
	return out, nil
}

// makeIdea builds a trade idea for a market.
func makeIdea(market string) domain.TradeIdea {
	return domain.TradeIdea{ID: domain.NewID(), StrategyID: "basic_and_llm_strategy", Market: market, Direction: domain.Long, Confidence: 0.7}
}

// makeRisk builds an approved risk decision for an idea.
func makeRisk(idea domain.TradeIdea, reason string) domain.RiskDecision {
	return domain.RiskDecision{TradeIdeaID: idea.ID, Approved: true, Reason: reason}
}

// TestApprovalService covers submitting, expiring, approving and rejecting trade approvals.
func TestApprovalService(t *testing.T) {
	t.Run("GIVEN a trade idea WHEN submitted THEN the request is pending", func(t *testing.T) {
		svc := NewService(DefaultTTL, nil, newClock().Now)
		idea := makeIdea("BTC/EUR")

		req, ok := svc.Submit(idea, makeRisk(idea, "ok"))

		if !ok || req.Status != "pending" {
			t.Fatalf("expected pending request, got %+v", req)
		}
		pending := svc.GetPending()
		if len(pending) != 1 || pending[0].ID != req.ID {
			t.Fatalf("expected the request in the queue, got %+v", pending)
		}
	})

	t.Run("GIVEN a market already awaiting approval WHEN another signal arrives THEN it is not queued twice", func(t *testing.T) {
		svc := NewService(DefaultTTL, nil, newClock().Now)
		first := makeIdea("ETH/EUR")
		second := makeIdea("ETH/EUR")

		firstReq, _ := svc.Submit(first, makeRisk(first, "ok"))
		_, ok := svc.Submit(second, makeRisk(second, "ok"))

		if ok {
			t.Fatal("duplicate market must not be queued")
		}
		if pending := svc.GetPending(); len(pending) != 1 || pending[0].ID != firstReq.ID {
			t.Fatalf("queue changed: %+v", pending)
		}
		if !svc.HasPendingForMarket("ETH/EUR") || svc.HasPendingForMarket("BTC/EUR") {
			t.Fatal("pending-for-market check is wrong")
		}
	})

	t.Run("GIVEN a pending request WHEN approved THEN it is returned approved and removed", func(t *testing.T) {
		repo := newFakeRepo()
		svc := NewService(DefaultTTL, repo, newClock().Now)
		idea := makeIdea("BTC/EUR")
		req, _ := svc.Submit(idea, makeRisk(idea, "ok"))

		approved, ok := svc.Approve(req.ID)

		if !ok || approved.Status != "approved" {
			t.Fatalf("expected approved request, got %+v", approved)
		}
		if len(svc.GetPending()) != 0 {
			t.Fatal("approved request must leave the queue")
		}
		if repo.rows[req.ID].Status != "approved" {
			t.Fatalf("persisted status %q, want approved", repo.rows[req.ID].Status)
		}
	})

	t.Run("GIVEN a pending request WHEN rejected THEN it is returned rejected and removed", func(t *testing.T) {
		svc := NewService(DefaultTTL, nil, newClock().Now)
		idea := makeIdea("BTC/EUR")
		req, _ := svc.Submit(idea, makeRisk(idea, "ok"))

		rejected, ok := svc.Reject(req.ID)

		if !ok || rejected.Status != "rejected" || len(svc.GetPending()) != 0 {
			t.Fatalf("expected rejected and removed, got %+v", rejected)
		}
	})

	t.Run("GIVEN an unknown id WHEN approved THEN nothing is returned", func(t *testing.T) {
		svc := NewService(DefaultTTL, nil, newClock().Now)

		if _, ok := svc.Approve("missing"); ok {
			t.Fatal("unknown approval must not be approved")
		}
	})

	t.Run("GIVEN an expired request WHEN pending is read THEN it is purged and marked expired", func(t *testing.T) {
		clock := newClock()
		repo := newFakeRepo()
		svc := NewService(DefaultTTL, repo, clock.Now)
		idea := makeIdea("BTC/EUR")
		req, _ := svc.Submit(idea, makeRisk(idea, "ok"))

		clock.Advance(DefaultTTL + time.Second)
		pending := svc.GetPending()

		if len(pending) != 0 {
			t.Fatalf("expired request must be purged, got %+v", pending)
		}
		if _, ok := svc.Get(req.ID); ok {
			t.Fatal("expired request must not be retrievable")
		}
		if repo.rows[req.ID].Status != "expired" {
			t.Fatalf("persisted status %q, want expired", repo.rows[req.ID].Status)
		}
	})

	t.Run("GIVEN an expired request WHEN rejected THEN it expires instead of being rejected", func(t *testing.T) {
		clock := newClock()
		repo := newFakeRepo()
		svc := NewService(DefaultTTL, repo, clock.Now)
		idea := makeIdea("BTC/EUR")
		req, _ := svc.Submit(idea, makeRisk(idea, "ok"))

		clock.Advance(DefaultTTL + time.Second)
		_, ok := svc.Reject(req.ID)

		if ok {
			t.Fatal("expired request must not be rejected")
		}
		last := repo.statusUpdates[len(repo.statusUpdates)-1]
		if last != (statusUpdate{req.ID, "expired"}) {
			t.Fatalf("last status update %+v, want expired", last)
		}
	})

	t.Run("GIVEN an expired request WHEN get is called THEN nothing is returned and it is marked expired", func(t *testing.T) {
		clock := newClock()
		repo := newFakeRepo()
		svc := NewService(DefaultTTL, repo, clock.Now)
		idea := makeIdea("BTC/EUR")
		req, _ := svc.Submit(idea, makeRisk(idea, "ok"))

		clock.Advance(DefaultTTL + time.Second)
		_, ok := svc.Get(req.ID)

		if ok {
			t.Fatal("expired request must not be returned")
		}
		last := repo.statusUpdates[len(repo.statusUpdates)-1]
		if last != (statusUpdate{req.ID, "expired"}) {
			t.Fatalf("last status update %+v, want expired", last)
		}
	})

	t.Run("GIVEN a valid request WHEN get is called THEN it is returned", func(t *testing.T) {
		svc := NewService(DefaultTTL, nil, newClock().Now)
		idea := makeIdea("BTC/EUR")
		req, _ := svc.Submit(idea, makeRisk(idea, "ok"))

		got, ok := svc.Get(req.ID)

		if !ok || got.ID != req.ID {
			t.Fatalf("expected the request, got %+v", got)
		}
	})

	t.Run("GIVEN several pending approvals WHEN cleared THEN the queue is empty and the count returned", func(t *testing.T) {
		repo := newFakeRepo()
		svc := NewService(DefaultTTL, repo, newClock().Now)
		for _, m := range []string{"BTC/EUR", "ETH/EUR", "SOL/EUR"} {
			idea := makeIdea(m)
			svc.Submit(idea, makeRisk(idea, "ok"))
		}

		cleared := svc.ClearPending()

		if cleared != 3 || len(svc.GetPending()) != 0 {
			t.Fatalf("cleared %d, pending %d", cleared, len(svc.GetPending()))
		}
		if pending, _ := repo.LoadPendingApprovalRequests(newClock().Now()); len(pending) != 0 {
			t.Fatal("persisted approvals must be cleared too")
		}
	})

	t.Run("GIVEN a persisted pending approval WHEN a new service loads THEN it is restored with its context", func(t *testing.T) {
		repo := newFakeRepo()
		clock := newClock()
		svc := NewService(DefaultTTL, repo, clock.Now)
		idea := makeIdea("LINK/EUR")
		req, _ := svc.Submit(idea, makeRisk(idea, "size adjusted"))

		reloaded := NewService(DefaultTTL, repo, clock.Now)
		n, err := reloaded.LoadPendingFromRepository()

		if err != nil || n != 1 {
			t.Fatalf("loaded %d, err %v", n, err)
		}
		pending := reloaded.GetPending()
		if len(pending) != 1 || pending[0].ID != req.ID || pending[0].TradeIdea.Market != "LINK/EUR" || pending[0].RiskDecision.Reason != "size adjusted" {
			t.Fatalf("unexpected restored queue %+v", pending)
		}
	})

	t.Run("GIVEN a persisted approval that was approved WHEN a new service loads THEN nothing is pending", func(t *testing.T) {
		repo := newFakeRepo()
		clock := newClock()
		svc := NewService(DefaultTTL, repo, clock.Now)
		idea := makeIdea("SOL/EUR")
		req, _ := svc.Submit(idea, makeRisk(idea, "ok"))
		svc.Approve(req.ID)

		reloaded := NewService(DefaultTTL, repo, clock.Now)
		reloaded.LoadPendingFromRepository()

		if len(reloaded.GetPending()) != 0 {
			t.Fatal("approved request must not be restored as pending")
		}
	})

	t.Run("GIVEN no repository WHEN loading pending THEN nothing is loaded", func(t *testing.T) {
		svc := NewService(DefaultTTL, nil, nil)

		n, err := svc.LoadPendingFromRepository()

		if n != 0 || err != nil {
			t.Fatalf("expected no-op, got %d %v", n, err)
		}
	})

	t.Run("GIVEN pending requests WHEN read THEN they keep submission order", func(t *testing.T) {
		svc := NewService(DefaultTTL, nil, newClock().Now)
		markets := []string{"BTC/EUR", "ETH/EUR", "SOL/EUR", "ADA/EUR"}
		for _, m := range markets {
			idea := makeIdea(m)
			svc.Submit(idea, makeRisk(idea, "ok"))
		}

		pending := svc.GetPending()

		for i, m := range markets {
			if pending[i].TradeIdea.Market != m {
				t.Fatalf("position %d is %s, want %s", i, pending[i].TradeIdea.Market, m)
			}
		}
	})
}
