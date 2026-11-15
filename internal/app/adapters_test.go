package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/control"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/kraken"
	"github.com/James-Chapman/LLMTradingBot/internal/llm"
	"github.com/James-Chapman/LLMTradingBot/internal/paper"
	"github.com/James-Chapman/LLMTradingBot/internal/storage"
)

// newTestRepo opens a fresh repository backed by a temp-file SQLite DB.
func newTestRepo(t *testing.T) *storage.Repository {
	t.Helper()
	repo, err := storage.Open(filepath.Join(t.TempDir(), "trading_bot.db"))
	if err != nil {
		t.Fatalf("storage.Open failed: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

// sampleIdea returns a valid long trade idea for BTC/EUR.
func sampleIdea() domain.TradeIdea {
	return domain.TradeIdea{
		ID:                     domain.NewID(),
		StrategyID:             "basic_strategy",
		Market:                 "BTC/EUR",
		Direction:              domain.Long,
		Thesis:                 "BDD adapter signal",
		SupportingSignals:      map[string]any{"source": "test"},
		Confidence:             0.7,
		EntryPlan:              "Enter at market",
		ExitPlan:               "Exit on invalidation",
		StopOrInvalidation:     "Stop at configured loss",
		PositionSizingProposal: 0.2,
		ModeEligibility:        []domain.TradingMode{domain.ModeSemiAutomated},
	}
}

// TestRiskStateStoreAdapter covers the risk.StateStore adapter.
func TestRiskStateStoreAdapter(t *testing.T) {
	t.Run("GIVEN a repository WHEN risk state is saved through the adapter THEN it can be loaded back", func(t *testing.T) {
		repo := newTestRepo(t)
		store := riskStore{repo: repo}
		today := time.Now().UTC().Truncate(24 * time.Hour)

		if err := store.SaveRiskState(12.5, 480, today); err != nil {
			t.Fatalf("SaveRiskState: %v", err)
		}
		state, err := repo.LoadRiskState(context.Background())
		if err != nil || state == nil {
			t.Fatalf("expected persisted risk state, got %v / %v", state, err)
		}
		if state.DailyLoss != 12.5 || state.DailyStartEquity != 480 {
			t.Errorf("risk state not round-tripped: %+v", state)
		}
	})
}

// TestApprovalRepositoryAdapter covers the approval.Repository adapter.
func TestApprovalRepositoryAdapter(t *testing.T) {
	t.Run("GIVEN a saved approval WHEN pending approvals are loaded THEN it is returned and can be cleared", func(t *testing.T) {
		repo := newTestRepo(t)
		store := approvalStore{repo: repo}
		now := time.Now().UTC()
		req := domain.ApprovalRequest{
			ID:           domain.NewID(),
			TradeIdea:    sampleIdea(),
			RiskDecision: domain.RiskDecision{Approved: true, Reason: "ok", Timestamp: now},
			ExpiresAt:    now.Add(10 * time.Minute),
			Status:       "pending",
		}
		req.RiskDecision.TradeIdeaID = req.TradeIdea.ID

		if err := store.SaveApprovalRequest(req); err != nil {
			t.Fatalf("SaveApprovalRequest: %v", err)
		}
		loaded, err := store.LoadPendingApprovalRequests(now)
		if err != nil || len(loaded) != 1 || loaded[0].ID != req.ID {
			t.Fatalf("expected the saved approval back, got %v / %v", loaded, err)
		}
		cleared, err := store.ClearPendingApprovals()
		if err != nil || cleared != 1 {
			t.Fatalf("expected one cleared approval, got %d / %v", cleared, err)
		}
		loaded, _ = store.LoadPendingApprovalRequests(now)
		if len(loaded) != 0 {
			t.Errorf("cleared approvals must not reload, got %d", len(loaded))
		}
	})

	t.Run("GIVEN a saved approval WHEN its status is updated THEN it is no longer pending", func(t *testing.T) {
		repo := newTestRepo(t)
		store := approvalStore{repo: repo}
		now := time.Now().UTC()
		req := domain.ApprovalRequest{
			ID: domain.NewID(), TradeIdea: sampleIdea(),
			RiskDecision: domain.RiskDecision{Approved: true, Timestamp: now},
			ExpiresAt:    now.Add(time.Minute), Status: "pending",
		}
		_ = store.SaveApprovalRequest(req)
		if err := store.UpdateApprovalStatus(req.ID, "approved"); err != nil {
			t.Fatalf("UpdateApprovalStatus: %v", err)
		}
		loaded, _ := store.LoadPendingApprovalRequests(now)
		if len(loaded) != 0 {
			t.Errorf("approved request must not be pending, got %d", len(loaded))
		}
	})
}

// TestControlRepositoryAdapter covers the control.Repository adapter.
func TestControlRepositoryAdapter(t *testing.T) {
	t.Run("GIVEN an empty database WHEN control state is loaded THEN found is false", func(t *testing.T) {
		store := controlStore{repo: newTestRepo(t)}
		_, found, err := store.LoadControlState()
		if err != nil || found {
			t.Errorf("expected not found without error, got found=%v err=%v", found, err)
		}
	})

	t.Run("GIVEN saved control state WHEN loaded THEN toggles survive", func(t *testing.T) {
		store := controlStore{repo: newTestRepo(t)}
		in := control.PersistedState{
			EmergencyStop:    true,
			DisabledMarkets:  []string{"ETH/EUR"},
			LiveMarkets:      []string{"BTC/EUR"},
			SelectedStrategy: "basic_strategy",
		}
		if err := store.SaveControlState(in); err != nil {
			t.Fatalf("SaveControlState: %v", err)
		}
		out, found, err := store.LoadControlState()
		if err != nil || !found {
			t.Fatalf("expected found state, got %v / %v", found, err)
		}
		if !out.EmergencyStop || len(out.DisabledMarkets) != 1 || len(out.LiveMarkets) != 1 || out.SelectedStrategy != "basic_strategy" {
			t.Errorf("control state not round-tripped: %+v", out)
		}
	})

	t.Run("GIVEN an active emergency stop with its start time WHEN saved and loaded THEN the start time survives", func(t *testing.T) {
		store := controlStore{repo: newTestRepo(t)}
		since := time.Date(2026, 11, 14, 9, 30, 15, 123456000, time.UTC)
		if err := store.SaveControlState(control.PersistedState{EmergencyStop: true, StopSince: &since}); err != nil {
			t.Fatalf("SaveControlState: %v", err)
		}
		out, _, err := store.LoadControlState()
		if err != nil || out.StopSince == nil || !out.StopSince.Equal(since) {
			t.Errorf("stop time not round-tripped: %+v / %v", out.StopSince, err)
		}
	})
}

// TestPaperRepositoryAdapter covers the paper.Repository and OrderPnLStore adapters.
func TestPaperRepositoryAdapter(t *testing.T) {
	t.Run("GIVEN an open position WHEN upserted and reloaded THEN its context is preserved and deletion removes it", func(t *testing.T) {
		repo := newTestRepo(t)
		store := paperStore{repo: repo}
		conf := 0.8
		row := paper.OpenPositionRow{
			PositionID: domain.NewID(), Market: "BTC/EUR", Size: 0.01, AvgPrice: 50000,
			Direction: "long", StrategyID: "basic_strategy", SignalConfidence: &conf, TradeIdeaID: "idea-1",
		}
		if err := store.UpsertOpenPosition(row); err != nil {
			t.Fatalf("UpsertOpenPosition: %v", err)
		}
		rows, err := store.GetOpenPositions()
		if err != nil || len(rows) != 1 {
			t.Fatalf("expected one open position, got %v / %v", rows, err)
		}
		got := rows[0]
		if got.PositionID != row.PositionID || got.StrategyID != "basic_strategy" || got.Direction != "long" || got.TradeIdeaID != "idea-1" {
			t.Errorf("open position context lost: %+v", got)
		}
		if got.SignalConfidence == nil || *got.SignalConfidence != conf {
			t.Errorf("signal confidence lost: %+v", got.SignalConfidence)
		}
		if err := store.DeleteOpenPosition(row.PositionID); err != nil {
			t.Fatalf("DeleteOpenPosition: %v", err)
		}
		rows, _ = store.GetOpenPositions()
		if len(rows) != 0 {
			t.Errorf("deleted position still present: %v", rows)
		}
	})

	t.Run("GIVEN a paper short with its entry fee and proceeds WHEN upserted and reloaded THEN both are kept", func(t *testing.T) {
		store := paperStore{repo: newTestRepo(t)}
		row := paper.OpenPositionRow{
			PositionID: domain.NewID(), Market: "ETH/EUR", Size: -0.5, AvgPrice: 3000, Direction: "short",
			EntryFee: 3.9, ShortProceeds: domain.Float(1500),
		}
		if err := store.UpsertOpenPosition(row); err != nil {
			t.Fatalf("UpsertOpenPosition: %v", err)
		}
		rows, err := store.GetOpenPositions()
		if err != nil || len(rows) != 1 {
			t.Fatalf("expected one open position, got %v / %v", rows, err)
		}
		if got := rows[0]; got.EntryFee != 3.9 || got.ShortProceeds == nil || *got.ShortProceeds != 1500 {
			t.Errorf("entry fee or short proceeds lost: %+v", got)
		}
	})

	t.Run("GIVEN a filled paper order WHEN saved and P&L stamped THEN the ledger shows it", func(t *testing.T) {
		repo := newTestRepo(t)
		store := paperStore{repo: repo}
		order := domain.OrderRecord{
			ID: domain.NewID(), Market: "BTC/EUR", Direction: domain.Long, Size: 0.01, Price: 50000,
			Status: "filled", Timestamp: time.Now().UTC(), PositionID: domain.NewID(),
		}
		if err := store.SaveOrder(order, "auto", 1.3, "paper", "idea-1"); err != nil {
			t.Fatalf("SaveOrder: %v", err)
		}
		if err := store.SaveFill(domain.FillRecord{OrderID: order.ID, FillPrice: 50000, FillSize: 0.01, Fee: 1.3, Timestamp: time.Now().UTC()}); err != nil {
			t.Fatalf("SaveFill: %v", err)
		}
		if err := (paperStore{repo: repo}).UpdateOrderPnL(order.ID, 4.2); err != nil {
			t.Fatalf("UpdateOrderPnL: %v", err)
		}
		ledger, err := repo.GetTradeLedger(context.Background(), 10)
		if err != nil || len(ledger) != 1 || ledger[0].PositionIDFull != order.PositionID {
			t.Fatalf("expected the order in the ledger, got %v / %v", ledger, err)
		}
		if ledger[0].PnL == nil {
			t.Errorf("expected stamped P&L on the ledger row")
		}
	})

	t.Run("GIVEN a rejected trade and a signal outcome WHEN saved THEN both registers contain them", func(t *testing.T) {
		repo := newTestRepo(t)
		store := paperStore{repo: repo}
		if err := store.SaveRejectedTrade(paper.RejectedTrade{Market: "ETH/EUR", Direction: "long", Size: 1, Price: 3000, Reason: paper.ReasonInsufficientFunds}); err != nil {
			t.Fatalf("SaveRejectedTrade: %v", err)
		}
		rejected, _ := repo.GetRejectedTrades(context.Background(), 10)
		if len(rejected) != 1 || rejected[0].Market != "ETH/EUR" {
			t.Errorf("rejected trade not stored: %v", rejected)
		}
		err := store.SaveSignalOutcome(paper.SignalOutcome{
			StrategyID: "basic_strategy", Market: "ETH/EUR", Direction: "long", EntryPrice: 3000, ExitPrice: 3100,
			Size: 1, PnL: 100, ExitReason: "auto", EntryAt: time.Now().UTC().Add(-time.Hour), PositionID: "pos-1",
		})
		if err != nil {
			t.Fatalf("SaveSignalOutcome: %v", err)
		}
		outcomes, _ := repo.GetSignalOutcomes(context.Background(), "", 10)
		if len(outcomes) != 1 || outcomes[0].PnL <= 0 {
			t.Errorf("signal outcome not stored: %v", outcomes)
		}
	})
}

// TestKrakenOrderRepositoryAdapter covers the kraken.OrderRepository adapter.
func TestKrakenOrderRepositoryAdapter(t *testing.T) {
	t.Run("GIVEN a pending live order WHEN reconciled as filled THEN it leaves the pending list", func(t *testing.T) {
		repo := newTestRepo(t)
		store := krakenStore{repo: repo}
		order := domain.OrderRecord{
			ID: domain.NewID(), Market: "BTC/EUR", Direction: domain.Long, Size: 0.01, Price: 50000,
			Status: "pending", Timestamp: time.Now().UTC(), ExchangeOrderID: "TXID-1",
		}
		if err := store.SaveOrder(order, kraken.OrderMeta{ApprovalID: "auto", Environment: "live", ClientOrderID: order.ID}); err != nil {
			t.Fatalf("SaveOrder: %v", err)
		}
		pending, err := store.GetPendingLiveOrders()
		if err != nil || len(pending) != 1 || pending[0].ExchangeOrderID != "TXID-1" {
			t.Fatalf("expected one pending live order, got %v / %v", pending, err)
		}
		if err := store.UpdateOrderExecution(kraken.OrderUpdate{OrderID: order.ID, Status: "filled", ExecutedSize: 0.01, AvgPrice: 50100, Fee: 1.2}); err != nil {
			t.Fatalf("UpdateOrderExecution: %v", err)
		}
		pending, _ = store.GetPendingLiveOrders()
		if len(pending) != 0 {
			t.Errorf("filled order still pending: %v", pending)
		}
	})

	t.Run("GIVEN a Kraken-rejected order WHEN saved THEN the rejected register records it", func(t *testing.T) {
		repo := newTestRepo(t)
		store := krakenStore{repo: repo}
		err := store.SaveRejectedTrade(kraken.RejectedTrade{
			Market: "BTC/EUR", Direction: domain.Short, Size: 0.01, Price: 50000, Reason: kraken.ReasonMissingCredentials,
		})
		if err != nil {
			t.Fatalf("SaveRejectedTrade: %v", err)
		}
		rejected, _ := repo.GetRejectedTrades(context.Background(), 10)
		if len(rejected) != 1 || rejected[0].Reason != kraken.ReasonMissingCredentials {
			t.Errorf("rejected live trade not stored: %v", rejected)
		}
	})
}

// TestBriefingStoreAdapter covers the llm.BriefingStore adapter.
func TestBriefingStoreAdapter(t *testing.T) {
	t.Run("GIVEN an empty database WHEN the latest briefing and reflection are loaded THEN both are nil", func(t *testing.T) {
		store := briefingStore{repo: newTestRepo(t)}
		b, err := store.LoadLatestBriefing(context.Background())
		if err != nil || b != nil {
			t.Errorf("expected nil briefing, got %v / %v", b, err)
		}
		r, err := store.LoadLatestReflection(context.Background())
		if err != nil || r != nil {
			t.Errorf("expected nil reflection, got %v / %v", r, err)
		}
	})

	t.Run("GIVEN a saved briefing and reflection WHEN loaded THEN their content survives", func(t *testing.T) {
		store := briefingStore{repo: newTestRepo(t)}
		ctx := context.Background()
		generated := time.Date(2026, 5, 1, 12, 30, 0, 0, time.UTC)
		briefing := llm.MarketBriefing{
			MarketOutlooks:   map[string]llm.MarketOutlook{"BTC/EUR": {Bias: "bullish", Score: 0.6, Note: "ETF flows"}},
			OverallSentiment: 0.4,
			KeyInsight:       "Risk-on",
			ArticleCount:     3,
			GeneratedAt:      generated,
		}
		if err := store.SaveBriefing(ctx, briefing, []string{"a", "b"}); err != nil {
			t.Fatalf("SaveBriefing: %v", err)
		}
		got, err := store.LoadLatestBriefing(ctx)
		if err != nil || got == nil {
			t.Fatalf("expected a briefing, got %v / %v", got, err)
		}
		if got.KeyInsight != "Risk-on" || got.ArticleCount != 3 || got.MarketOutlooks["BTC/EUR"].Bias != "bullish" {
			t.Errorf("briefing not round-tripped: %+v", got)
		}
		if !got.GeneratedAt.Equal(generated) {
			t.Errorf("generated_at changed: %v vs %v", got.GeneratedAt, generated)
		}

		if err := store.SaveReflection(ctx, llm.Reflection{Pattern: "Late entries lose", Suggestion: "Wait", InsightConfidence: 0.6, GeneratedAt: generated}); err != nil {
			t.Fatalf("SaveReflection: %v", err)
		}
		ref, err := store.LoadLatestReflection(ctx)
		if err != nil || ref == nil || ref.Pattern != "Late entries lose" || !ref.GeneratedAt.Equal(generated) {
			t.Errorf("reflection not round-tripped: %+v / %v", ref, err)
		}
	})
}

// TestLearnerOutcomeConversion covers storage.SignalOutcome → learner.Outcome.
func TestLearnerOutcomeConversion(t *testing.T) {
	t.Run("GIVEN stored signal outcomes WHEN converted THEN strategy, market, direction and P&L are kept in order", func(t *testing.T) {
		rows := []storage.SignalOutcome{
			{StrategyID: "basic_strategy", Market: "BTC/EUR", Direction: "long", PnL: 5},
			{StrategyID: "llm_only_strategy", Market: "ETH/EUR", Direction: "short", PnL: -2},
		}
		out := learnerOutcomes(rows)
		if len(out) != 2 || out[0].StrategyID != "basic_strategy" || out[1].Pnl != -2 || out[1].Direction != "short" {
			t.Errorf("unexpected conversion: %+v", out)
		}
	})

	t.Run("GIVEN stored outcomes of a long and a short WHEN converted THEN each carries its entry notional", func(t *testing.T) {
		rows := []storage.SignalOutcome{
			{StrategyID: "s", Market: "BTC/EUR", Direction: "long", EntryPrice: 50000, Size: 0.002, PnL: 5},
			{StrategyID: "s", Market: "ETH/EUR", Direction: "short", EntryPrice: 3000, Size: -0.5, PnL: -2},
		}
		out := learnerOutcomes(rows)
		if len(out) != 2 || out[0].Notional != 100 || out[1].Notional != 1500 {
			t.Errorf("expected notionals 100 and 1500, got %+v", out)
		}
	})
}

// TestPaperEquityAdapter covers the portfolio.EquityPricedEngine adapter.
func TestPaperEquityAdapter(t *testing.T) {
	t.Run("GIVEN a paper engine with cash WHEN valued through the adapter THEN cash and equity match the engine", func(t *testing.T) {
		engine := paper.NewEngine(500, nil, nil)
		eq := paperEquity{engine: engine}
		if eq.GetCash() != engine.Cash() {
			t.Errorf("cash mismatch: %v vs %v", eq.GetCash(), engine.Cash())
		}
		if eq.GetTotalEquity(map[string]float64{}) != engine.TotalEquity(map[string]float64{}) {
			t.Errorf("equity mismatch")
		}
	})
}
