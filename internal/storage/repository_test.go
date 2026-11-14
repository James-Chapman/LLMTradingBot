package storage

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/observability"
)

// TestRepositoryRetention ports the data-retention scenarios of tests/test_repository_bdd.py.
func TestRepositoryRetention(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN one market has more ticks than the limit WHEN old ticks are trimmed THEN only its newest ticks are kept", func(t *testing.T) {
		repo := newTestRepo(t)
		repo.maxPriceRowsPerSymbol = 3
		base := at(12, 0, 0)
		for i := 0; i < 5; i++ {
			_, err := repo.db.Exec(
				"INSERT INTO market_snapshots (symbol, timestamp, price) VALUES (?, ?, ?)",
				"BTC/EUR", formatDBTime(base.Add(time.Duration(i)*time.Second)), 100.0+float64(i),
			)
			mustNoErr(t, err)
		}
		_, err := repo.db.Exec(
			"INSERT INTO market_snapshots (symbol, timestamp, price) VALUES (?, ?, ?)",
			"ETH/EUR", formatDBTime(base), 200.0,
		)
		mustNoErr(t, err)

		mustNoErr(t, repo.TrimOldPriceTicks(ctx, "BTC/EUR"))

		btc, err := repo.GetRecentPrices(ctx, "BTC/EUR", 100)
		mustNoErr(t, err)
		eth, err := repo.GetRecentPrices(ctx, "ETH/EUR", 100)
		mustNoErr(t, err)
		if len(btc) != 3 || btc[0] != 102.0 || btc[2] != 104.0 {
			t.Errorf("expected newest three BTC prices oldest-first, got %v", btc)
		}
		if len(eth) != 1 {
			t.Errorf("other market must be untouched, got %v", eth)
		}
	})

	t.Run("GIVEN more equity snapshots than keep WHEN trimmed THEN only the newest remain", func(t *testing.T) {
		repo := newTestRepo(t)
		for i := 0; i < 5; i++ {
			mustNoErr(t, repo.SaveEquitySnapshot(ctx, 1000+float64(i), 800, 200))
		}

		mustNoErr(t, repo.TrimOldEquitySnapshots(ctx, 2))

		history, err := repo.GetEquityHistory(ctx, 10)
		mustNoErr(t, err)
		if len(history) != 2 || history[1].Equity != 1004 {
			t.Errorf("expected the two newest snapshots, got %+v", history)
		}
	})

	t.Run("GIVEN the equity ticker fires multiple times WHEN snapshots are saved THEN all are retrievable", func(t *testing.T) {
		repo := newTestRepo(t)
		mustNoErr(t, repo.SaveEquitySnapshot(ctx, 1000, 800, 200))
		mustNoErr(t, repo.SaveEquitySnapshot(ctx, 1005, 800, 205))
		mustNoErr(t, repo.SaveEquitySnapshot(ctx, 995, 800, 195))

		history, err := repo.GetEquityHistory(ctx, 10)
		mustNoErr(t, err)
		if len(history) != 3 {
			t.Fatalf("all three equity snapshots must be saved, got %d", len(history))
		}
		seen := map[float64]bool{}
		for _, h := range history {
			seen[h.Equity] = true
		}
		for _, want := range []float64{1000, 1005, 995} {
			if !seen[want] {
				t.Errorf("equity %v missing from history", want)
			}
		}
		latest, err := repo.GetLatestEquity(ctx)
		mustNoErr(t, err)
		cash, err := repo.GetLatestCash(ctx)
		mustNoErr(t, err)
		if latest == nil || cash == nil || *cash != 800 {
			t.Errorf("latest equity/cash should be available, got %v %v", latest, cash)
		}
	})

	t.Run("GIVEN no equity snapshots WHEN latest equity is requested THEN nil is returned", func(t *testing.T) {
		repo := newTestRepo(t)
		latest, err := repo.GetLatestEquity(ctx)
		mustNoErr(t, err)
		if latest != nil {
			t.Errorf("expected nil, got %v", *latest)
		}
	})
}

// TestRepositoryPnLAndControl covers P&L summaries and the control-state row.
func TestRepositoryPnLAndControl(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN closed trades exist WHEN the P&L summary is requested THEN daily and market totals are grouped", func(t *testing.T) {
		repo := newTestRepo(t)
		entryAt := at(10, 0, 0)
		for _, o := range []struct {
			market string
			exit   float64
			pnl    float64
		}{{"BTC/EUR", 110, 10}, {"ETH/EUR", 95, -5}} {
			mustNoErr(t, repo.SaveSignalOutcome(ctx, SignalOutcomeInput{
				StrategyID: "basic_and_llm_strategy", Market: o.market, Direction: "long",
				EntryPrice: 100, ExitPrice: o.exit, Size: 1, PnL: o.pnl,
				Confidence: domain.Float(0.7), ExitReason: "auto", EntryAt: entryAt,
			}))
		}

		summary, err := repo.GetPnLSummary(ctx, 0)
		mustNoErr(t, err)

		if summary.TotalPnL != 5 {
			t.Errorf("total_pnl = %v, want 5", summary.TotalPnL)
		}
		if summary.ByMarket["BTC/EUR"] != 10 || summary.ByMarket["ETH/EUR"] != -5 {
			t.Errorf("by_market wrong: %v", summary.ByMarket)
		}
		if len(summary.ByDay) != 1 {
			t.Errorf("both trades closed today so by_day should have one bucket: %v", summary.ByDay)
		}
		for _, v := range summary.ByDay {
			if v != 5 {
				t.Errorf("by_day total = %v, want 5", v)
			}
		}
	})

	t.Run("GIVEN a closed trade linked to a trade idea WHEN closed trades are listed THEN entry indicators are included", func(t *testing.T) {
		repo := newTestRepo(t)
		idea := makeTradeIdea()
		mustNoErr(t, repo.SaveTradeIdea(ctx, idea, SignalContext{Indicators: map[string]any{"rsi_14": 71.0}}))
		mustNoErr(t, repo.SaveSignalOutcome(ctx, SignalOutcomeInput{
			StrategyID: idea.StrategyID, Market: idea.Market, Direction: "long",
			EntryPrice: 100, ExitPrice: 90, Size: 2, PnL: -20, ExitReason: "stop_loss",
			EntryAt: at(9, 0, 0), PositionID: "abcdef1234567890", TradeIdeaID: idea.ID,
		}))
		mustNoErr(t, repo.SaveSignalOutcome(ctx, SignalOutcomeInput{
			StrategyID: "other", Market: "ETH/EUR", Direction: "short",
			EntryPrice: 0, ExitPrice: 90, Size: 2, PnL: 1, ExitReason: "auto", EntryAt: at(9, 0, 0),
		}))

		trades, err := repo.GetClosedTrades(ctx, 10)
		mustNoErr(t, err)
		if len(trades) != 2 {
			t.Fatalf("expected 2 closed trades, got %d", len(trades))
		}
		var linked, unlinked ClosedTrade
		for _, tr := range trades {
			if tr.TradeIdeaID == idea.ID {
				linked = tr
			} else {
				unlinked = tr
			}
		}
		if linked.Indicators["rsi_14"] != 71.0 {
			t.Errorf("linked trade should carry entry indicators, got %v", linked.Indicators)
		}
		if linked.PositionID != "abcdef12" || linked.PositionIDFull != "abcdef1234567890" {
			t.Errorf("position id short/full form wrong: %q %q", linked.PositionID, linked.PositionIDFull)
		}
		if linked.PnLPct != -0.1 {
			t.Errorf("pnl_pct = %v, want -0.1", linked.PnLPct)
		}
		if unlinked.Indicators != nil || unlinked.PnLPct != 0 {
			t.Errorf("unlinked trade should have null indicators and zero pnl_pct: %+v", unlinked)
		}

		outcomes, err := repo.GetSignalOutcomes(ctx, idea.StrategyID, 0)
		mustNoErr(t, err)
		if len(outcomes) != 1 || outcomes[0].PositionID == nil || outcomes[0].ConfidenceAtEntry != nil {
			t.Errorf("strategy filter or nullable fields wrong: %+v", outcomes)
		}
	})

	t.Run("GIVEN operator control state WHEN saved and loaded THEN the selected strategy is persisted", func(t *testing.T) {
		repo := newTestRepo(t)

		none, err := repo.LoadControlState(ctx)
		mustNoErr(t, err)
		if none != nil {
			t.Fatalf("control state should be nil before first save, got %+v", none)
		}

		mustNoErr(t, repo.SaveControlState(ctx, ControlState{SelectedStrategy: "llm"}))
		mustNoErr(t, repo.SaveControlState(ctx, ControlState{
			EmergencyStop: true, DisabledMarkets: []string{"BTC/EUR"}, SelectedStrategy: "llm",
		}))
		state, err := repo.LoadControlState(ctx)
		mustNoErr(t, err)

		if state == nil || state.SelectedStrategy != "llm" || !state.EmergencyStop {
			t.Fatalf("control state not persisted: %+v", state)
		}
		if state.LiveMarkets == nil || state.DisabledStrategies == nil {
			t.Error("list fields should be empty lists, not nil")
		}
	})
}

// TestRepositoryTradeLedger ports the ledger and rejected-trade scenarios.
func TestRepositoryTradeLedger(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN Kraken accepts a live order WHEN the ledger is requested THEN the pending live order is visible", func(t *testing.T) {
		repo := newTestRepo(t)
		order := domain.OrderRecord{
			ID: "live-order-1", Market: "BTC/EUR", Direction: domain.Long, Size: 0.1, Price: 50_000,
			Status: "pending", Timestamp: at(12, 0, 0), ExchangeOrderID: "TX123",
		}
		mustNoErr(t, repo.SaveOrder(ctx, order, SaveOrderOptions{ApprovalID: "manual", Environment: "live"}))

		ledger, err := repo.GetTradeLedger(ctx, 0)
		mustNoErr(t, err)

		if len(ledger) != 1 {
			t.Fatalf("expected one ledger row, got %d", len(ledger))
		}
		row := ledger[0]
		if row.Status != "pending" || row.Environment != "live" || row.ExchangeOrderID != "TX123" || row.Source != "manual" {
			t.Errorf("unexpected ledger row: %+v", row)
		}
		pending, err := repo.GetPendingLiveOrders(ctx)
		mustNoErr(t, err)
		if len(pending) != 1 || pending[0].ExchangeOrderID != "TX123" {
			t.Errorf("pending live order should await reconciliation: %+v", pending)
		}

		mustNoErr(t, repo.UpdateOrderStatusAndFill(ctx, "live-order-1", "filled", 50_100, 1.5))
		pending, err = repo.GetPendingLiveOrders(ctx)
		mustNoErr(t, err)
		ledger, err = repo.GetTradeLedger(ctx, 0)
		mustNoErr(t, err)
		if len(pending) != 0 || ledger[0].Price != 50_100 || ledger[0].Fee != 1.5 {
			t.Errorf("reconciliation not applied: pending=%v ledger=%+v", pending, ledger[0])
		}
	})

	t.Run("GIVEN an order linked to a trade idea WHEN the ledger is requested THEN the strategy is returned", func(t *testing.T) {
		repo := newTestRepo(t)
		idea := makeTradeIdea()
		mustNoErr(t, repo.SaveTradeIdea(ctx, idea, SignalContext{}))
		order := domain.OrderRecord{
			ID: "strategy-order-1", Market: "BTC/EUR", Direction: domain.Long, Size: 0.1, Price: 50_000,
			Status: "filled", Timestamp: at(12, 0, 0), PositionID: "position-1",
		}
		mustNoErr(t, repo.SaveOrder(ctx, order, SaveOrderOptions{ApprovalID: "auto", TradeIdeaID: idea.ID}))

		ledger, err := repo.GetTradeLedger(ctx, 0)
		mustNoErr(t, err)
		if ledger[0].Strategy != "basic_and_llm_strategy" || ledger[0].TradeType != "open" || ledger[0].Source != "auto" {
			t.Errorf("unexpected ledger row: %+v", ledger[0])
		}
	})

	t.Run("GIVEN a close order without a signal WHEN the ledger is requested THEN strategy falls back to the opener", func(t *testing.T) {
		repo := newTestRepo(t)
		idea := makeTradeIdea()
		mustNoErr(t, repo.SaveTradeIdea(ctx, idea, SignalContext{}))
		pos := "position-strategy-close"
		mustNoErr(t, repo.SaveOrder(ctx, domain.OrderRecord{
			ID: "open-with-strategy", Market: "BTC/EUR", Direction: domain.Long, Size: 0.1, Price: 50_000,
			Status: "filled", Timestamp: at(12, 0, 0), PositionID: pos,
		}, SaveOrderOptions{ApprovalID: "auto", TradeIdeaID: idea.ID}))
		mustNoErr(t, repo.SaveOrder(ctx, domain.OrderRecord{
			ID: "close-without-strategy", Market: "BTC/EUR", Direction: domain.Short, Size: 0.1, Price: 51_000,
			Status: "filled", Timestamp: at(13, 0, 0), PositionID: pos,
		}, SaveOrderOptions{ApprovalID: "stop_loss"}))

		ledger, err := repo.GetTradeLedger(ctx, 1)
		mustNoErr(t, err)
		if ledger[0].ID != "close-wi" || ledger[0].Strategy != "basic_and_llm_strategy" || ledger[0].Source != "stop-loss" {
			t.Errorf("unexpected ledger row: %+v", ledger[0])
		}
	})

	t.Run("GIVEN the opener is outside the limit WHEN the ledger is built THEN the close row is still classified as close", func(t *testing.T) {
		repo := newTestRepo(t)
		pos := "position-1"
		mustNoErr(t, repo.SaveOrder(ctx, domain.OrderRecord{
			ID: "open-order-1", Market: "BTC/EUR", Direction: domain.Long, Size: 0.1, Price: 50_000,
			Status: "filled", Timestamp: at(12, 0, 0), PositionID: pos,
		}, SaveOrderOptions{ApprovalID: "auto"}))
		mustNoErr(t, repo.SaveOrder(ctx, domain.OrderRecord{
			ID: "close-order-1", Market: "BTC/EUR", Direction: domain.Short, Size: 0.1, Price: 51_000,
			Status: "filled", Timestamp: at(13, 0, 0), PositionID: pos,
		}, SaveOrderOptions{ApprovalID: "auto"}))
		mustNoErr(t, repo.UpdateOrderPnL(ctx, "close-order-1", 100))

		ledger, err := repo.GetTradeLedger(ctx, 1)
		mustNoErr(t, err)
		if len(ledger) != 1 || ledger[0].ID != "close-or" || ledger[0].TradeType != "close" {
			t.Fatalf("unexpected ledger: %+v", ledger)
		}
		if ledger[0].PnL == nil || *ledger[0].PnL != 100 {
			t.Errorf("close order pnl should be stamped, got %v", ledger[0].PnL)
		}
	})

	t.Run("GIVEN a legacy rejected order row WHEN the ledger is requested THEN it is excluded", func(t *testing.T) {
		repo := newTestRepo(t)
		_, err := repo.db.Exec(`INSERT INTO order_records
			(id, approval_request_id, market, direction, size, price, fee, status, environment, timestamp)
			VALUES ('rejected-order-1', 'auto', 'BTC/EUR', 'long', 0.1, 50000.0, 0.0, 'rejected', 'paper', '2026-04-24 12:00:00.000000')`)
		mustNoErr(t, err)

		ledger, err := repo.GetTradeLedger(ctx, 0)
		mustNoErr(t, err)
		if len(ledger) != 0 {
			t.Errorf("rejected orders must not appear in the ledger: %+v", ledger)
		}
	})

	t.Run("GIVEN a rejected intent WHEN the rejected-trades register is queried THEN it appears with its reason and strategy", func(t *testing.T) {
		repo := newTestRepo(t)
		idea := makeTradeIdea()
		mustNoErr(t, repo.SaveTradeIdea(ctx, idea, SignalContext{}))
		mustNoErr(t, repo.SaveRejectedTrade(ctx, RejectedTradeInput{
			Market: "ETH/EUR", Direction: "short", Size: 0.5, Price: 2_000, Confidence: domain.Float(0.64),
			Reason: "insufficient_funds", TradeIdeaID: idea.ID, Timestamp: at(13, 0, 0),
		}))

		rejected, err := repo.GetRejectedTrades(ctx, 0)
		mustNoErr(t, err)
		if len(rejected) != 1 {
			t.Fatalf("expected one rejected trade, got %d", len(rejected))
		}
		r := rejected[0]
		if r.Market != "ETH/EUR" || r.Direction != "short" || r.Size != 0.5 || r.Reason != "insufficient_funds" ||
			r.TradeIdeaID != idea.ID || r.Strategy != "basic_and_llm_strategy" || r.Confidence == nil || *r.Confidence != 0.64 {
			t.Errorf("unexpected rejected trade: %+v", r)
		}
	})

	t.Run("GIVEN SaveOrder receives a rejected order WHEN persisting THEN it goes to the rejected-trades register", func(t *testing.T) {
		repo := newTestRepo(t)
		order := domain.OrderRecord{
			ID: "rejected-order-2", Market: "SOL/EUR", Direction: domain.Long, Size: 2, Price: 100,
			Status: "rejected", Timestamp: at(14, 0, 0), ExchangeOrderID: "EOrder:Insufficient funds",
		}
		mustNoErr(t, repo.SaveOrder(ctx, order, SaveOrderOptions{ApprovalID: "auto", Environment: "live", TradeIdeaID: "idea-2"}))

		ledger, err := repo.GetTradeLedger(ctx, 0)
		mustNoErr(t, err)
		rejected, err := repo.GetRejectedTrades(ctx, 0)
		mustNoErr(t, err)
		if len(ledger) != 0 {
			t.Errorf("rejected order must not reach the ledger: %+v", ledger)
		}
		if len(rejected) != 1 || rejected[0].Reason != "EOrder:Insufficient funds" || rejected[0].TradeIdeaID != "idea-2" {
			t.Errorf("rejected order not redirected: %+v", rejected)
		}
	})

	t.Run("GIVEN a fill is saved WHEN the fills table is read THEN the fill is stored", func(t *testing.T) {
		repo := newTestRepo(t)
		mustNoErr(t, repo.SaveFill(ctx, domain.FillRecord{OrderID: "o-1", FillPrice: 10, FillSize: 2, Fee: 0.1, Timestamp: at(12, 0, 0)}))
		var count int
		mustNoErr(t, repo.db.QueryRow("SELECT COUNT(*) FROM fill_records WHERE order_id = 'o-1'").Scan(&count))
		if count != 1 {
			t.Errorf("expected one fill, got %d", count)
		}
	})
}

// TestRepositorySignalsAndApprovals covers trade ideas, signal detail and approvals.
func TestRepositorySignalsAndApprovals(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN a trade idea with full signal context WHEN signal detail is requested THEN the context round-trips", func(t *testing.T) {
		repo := newTestRepo(t)
		idea := makeTradeIdea()
		mustNoErr(t, repo.SaveTradeIdea(ctx, idea, SignalContext{
			MomentumPct: 1.5,
			Indicators:  map[string]any{"rsi_14": 55.0},
			LLM:         &LLMAnalysis{Used: true, Sentiment: 0.4, ConfidenceScale: 1.2, Reasoning: "bullish"},
			NewsContext: []NewsContextItem{{Source: "CoinDesk", Title: "BTC up"}},
			RiskDecision: &domain.RiskDecision{
				TradeIdeaID: idea.ID, Approved: false, Reason: "max exposure",
			},
		}))

		detail, err := repo.GetSignalDetail(ctx, idea.ID)
		mustNoErr(t, err)
		if detail == nil {
			t.Fatal("signal detail should exist")
		}
		if !detail.LLMUsed || detail.LLMSentiment == nil || *detail.LLMSentiment != 0.4 || detail.LLMReasoning == nil {
			t.Errorf("LLM context lost: %+v", detail)
		}
		if detail.RiskApproved == nil || *detail.RiskApproved || detail.RiskReason == nil || *detail.RiskReason != "max exposure" {
			t.Errorf("risk context lost: %+v", detail)
		}
		if detail.Indicators["rsi_14"] != 55.0 || len(detail.NewsContext) != 1 || detail.SupportingSignals["source"] != "test" {
			t.Errorf("JSON context lost: %+v", detail)
		}
		if detail.CreatedAt == "" {
			t.Error("created_at should be set")
		}

		missing, err := repo.GetSignalDetail(ctx, "nope")
		mustNoErr(t, err)
		if missing != nil {
			t.Error("unknown trade idea should return nil")
		}
	})

	t.Run("GIVEN a trade idea without LLM use WHEN signal detail is serialised THEN LLM fields are null", func(t *testing.T) {
		repo := newTestRepo(t)
		idea := makeTradeIdea()
		mustNoErr(t, repo.SaveTradeIdea(ctx, idea, SignalContext{LLM: &LLMAnalysis{Used: false, Sentiment: 0.9}}))

		detail, err := repo.GetSignalDetail(ctx, idea.ID)
		mustNoErr(t, err)
		raw, err := json.Marshal(detail)
		mustNoErr(t, err)
		var decoded map[string]any
		mustNoErr(t, json.Unmarshal(raw, &decoded))
		if decoded["llm_sentiment"] != nil || decoded["risk_approved"] != nil || decoded["llm_used"] != false {
			t.Errorf("unexpected JSON for non-LLM signal: %s", raw)
		}
		if _, ok := decoded["news_context"].([]any); !ok {
			t.Errorf("news_context should be an empty list: %s", raw)
		}
	})

	t.Run("GIVEN a full signal was saved WHEN an approval request is saved THEN the signal context is not overwritten", func(t *testing.T) {
		repo := newTestRepo(t)
		idea := makeTradeIdea()
		mustNoErr(t, repo.SaveTradeIdea(ctx, idea, SignalContext{Indicators: map[string]any{"rsi_14": 30.0}}))
		req := domain.ApprovalRequest{
			ID: "approval-1", TradeIdea: idea,
			RiskDecision: domain.RiskDecision{TradeIdeaID: idea.ID, Approved: true, Reason: "ok", Timestamp: domain.NowUTC()},
			ExpiresAt:    domain.NowUTC().Add(time.Hour), Status: "pending",
		}
		mustNoErr(t, repo.SaveApprovalRequest(ctx, req))

		detail, err := repo.GetSignalDetail(ctx, idea.ID)
		mustNoErr(t, err)
		if detail.Indicators["rsi_14"] != 30.0 {
			t.Errorf("approval save overwrote signal context: %+v", detail.Indicators)
		}
	})

	t.Run("GIVEN pending and expired approvals WHEN pending requests are loaded THEN only live pending ones are reconstructed", func(t *testing.T) {
		repo := newTestRepo(t)
		now := domain.NowUTC()
		idea := makeTradeIdea()
		live := domain.ApprovalRequest{
			ID: "live", TradeIdea: idea,
			RiskDecision: domain.RiskDecision{TradeIdeaID: idea.ID, Approved: true, Reason: "ok", AdjustedSizing: domain.Float(0.1), Timestamp: now},
			ExpiresAt:    now.Add(time.Hour), Status: "pending",
		}
		expired := live
		expired.ID = "expired"
		expired.ExpiresAt = now.Add(-time.Hour)
		mustNoErr(t, repo.SaveApprovalRequest(ctx, live))
		mustNoErr(t, repo.SaveApprovalRequest(ctx, expired))
		// Saving again updates the same risk decision row rather than adding one.
		live.RiskDecision.Reason = "updated"
		mustNoErr(t, repo.SaveApprovalRequest(ctx, live))

		loaded, err := repo.LoadPendingApprovalRequests(ctx, now)
		mustNoErr(t, err)
		if len(loaded) != 1 || loaded[0].ID != "live" {
			t.Fatalf("expected only the live approval, got %+v", loaded)
		}
		got := loaded[0]
		if got.TradeIdea.Market != idea.Market || len(got.TradeIdea.ModeEligibility) != 3 || got.RiskDecision.Reason != "updated" {
			t.Errorf("approval not reconstructed: %+v", got)
		}
		if got.RiskDecision.AdjustedSizing == nil || got.ExpiresAt.Location() != time.UTC {
			t.Errorf("risk sizing or expiry timezone lost: %+v", got)
		}
		var riskRows int
		mustNoErr(t, repo.db.QueryRow("SELECT COUNT(*) FROM risk_decisions").Scan(&riskRows))
		if riskRows != 2 {
			t.Errorf("expected 2 risk decision rows, got %d", riskRows)
		}

		mustNoErr(t, repo.UpdateApprovalStatus(ctx, "live", "approved"))
		loaded, err = repo.LoadPendingApprovalRequests(ctx, now)
		mustNoErr(t, err)
		if len(loaded) != 0 {
			t.Errorf("approved request should no longer be pending: %+v", loaded)
		}
	})

	t.Run("GIVEN pending approvals WHEN they are cleared THEN the count is returned and none remain pending", func(t *testing.T) {
		repo := newTestRepo(t)
		now := domain.NowUTC()
		for _, id := range []string{"a", "b"} {
			idea := makeTradeIdea()
			mustNoErr(t, repo.SaveApprovalRequest(ctx, domain.ApprovalRequest{
				ID: id, TradeIdea: idea, RiskDecision: domain.RiskDecision{TradeIdeaID: idea.ID, Timestamp: now},
				ExpiresAt: now.Add(time.Hour), Status: "pending",
			}))
		}

		cleared, err := repo.ClearPendingApprovals(ctx)
		mustNoErr(t, err)
		loaded, err := repo.LoadPendingApprovalRequests(ctx, now)
		mustNoErr(t, err)
		if cleared != 2 || len(loaded) != 0 {
			t.Errorf("cleared=%d remaining=%d", cleared, len(loaded))
		}
	})
}

// TestRepositoryPositions covers the open_positions table.
func TestRepositoryPositions(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN an open position WHEN it is upserted without a trade idea THEN the opening signal link is preserved", func(t *testing.T) {
		repo := newTestRepo(t)
		mustNoErr(t, repo.UpsertOpenPosition(ctx, OpenPositionInput{
			PositionID: "p1", Market: "BTC/EUR", Size: 1, AvgPrice: 100, Direction: "long",
			StrategyID: "s", SignalConfidence: domain.Float(0.7), TradeIdeaID: "idea-1",
		}))
		mustNoErr(t, repo.UpsertOpenPosition(ctx, OpenPositionInput{PositionID: "p1", Market: "BTC/EUR", Size: 2, AvgPrice: 105}))

		positions, err := repo.GetOpenPositions(ctx)
		mustNoErr(t, err)
		if len(positions) != 1 {
			t.Fatalf("expected one position, got %d", len(positions))
		}
		p := positions[0]
		if p.Size != 2 || p.AvgPrice != 105 || p.TradeIdeaID == nil || *p.TradeIdeaID != "idea-1" {
			t.Errorf("upsert did not update size/price or lost trade idea: %+v", p)
		}
		if p.OpenedAt.IsZero() || p.UpdatedAt.Before(p.OpenedAt) {
			t.Errorf("timestamps not maintained: %+v", p)
		}
	})

	t.Run("GIVEN open positions WHEN one is deleted and then all are cleared THEN the table empties", func(t *testing.T) {
		repo := newTestRepo(t)
		for _, id := range []string{"p1", "p2", "p3"} {
			mustNoErr(t, repo.UpsertOpenPosition(ctx, OpenPositionInput{PositionID: id, Market: "BTC/EUR", Size: 1, AvgPrice: 1}))
		}
		mustNoErr(t, repo.DeleteOpenPosition(ctx, "p1"))
		cleared, err := repo.ClearAllOpenPositions(ctx)
		mustNoErr(t, err)
		positions, err := repo.GetOpenPositions(ctx)
		mustNoErr(t, err)
		if cleared != 2 || len(positions) != 0 {
			t.Errorf("cleared=%d remaining=%d", cleared, len(positions))
		}
	})
}

// TestRepositoryActivityAndHistory covers activity log, rejections, news and LLM state.
func TestRepositoryActivityAndHistory(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN activity entries WHEN recent activity is requested THEN they come back newest first", func(t *testing.T) {
		repo := newTestRepo(t)
		var store observability.ActivityStore = repo
		mustNoErr(t, store.SaveActivityLog("info", "first", ""))
		mustNoErr(t, store.SaveActivityLog("warn", "second", "detail"))

		entries, err := store.GetRecentActivity(10)
		mustNoErr(t, err)
		if len(entries) != 2 || entries[0].Message != "second" || entries[0].Detail != "detail" {
			t.Fatalf("expected newest-first entries, got %+v", entries)
		}
		if _, err := time.Parse(time.RFC3339Nano, entries[0].Timestamp); err != nil {
			t.Errorf("activity timestamp should be RFC 3339: %v", err)
		}
	})

	t.Run("GIVEN more activity than keep WHEN trimmed THEN only the newest rows remain", func(t *testing.T) {
		repo := newTestRepo(t)
		for i := 0; i < 5; i++ {
			mustNoErr(t, repo.SaveActivityLog("info", string(rune('a'+i)), ""))
		}
		mustNoErr(t, repo.TrimOldActivity(ctx, 2))

		entries, err := repo.GetRecentActivity(10)
		mustNoErr(t, err)
		if len(entries) != 2 || entries[0].Message != "e" {
			t.Errorf("expected two newest entries, got %+v", entries)
		}
	})

	t.Run("GIVEN risk rejections WHEN recent rejections are requested THEN newest come first", func(t *testing.T) {
		repo := newTestRepo(t)
		mustNoErr(t, repo.SaveRiskRejection(ctx, RiskRejectionInput{Market: "BTC/EUR", Direction: "long", Confidence: 0.6, Reason: "old", Timestamp: at(10, 0, 0)}))
		mustNoErr(t, repo.SaveRiskRejection(ctx, RiskRejectionInput{Market: "ETH/EUR", Direction: "short", Confidence: 0.7, Reason: "new", TradeIdeaID: "i"}))

		rows, err := repo.GetRecentRiskRejections(ctx, 0)
		mustNoErr(t, err)
		if len(rows) != 2 || rows[0].Reason != "new" || rows[0].TradeIdeaID != "i" || rows[1].TradeIdeaID != "" {
			t.Errorf("unexpected rejections: %+v", rows)
		}
	})

	t.Run("GIVEN news items WHEN recent news is requested THEN items are newest first with a trimmed summary", func(t *testing.T) {
		repo := newTestRepo(t)
		long := ""
		for i := 0; i < 300; i++ {
			long += "x"
		}
		mustNoErr(t, repo.UpsertNewsItem(ctx, domain.NewsItem{ID: "old", Source: "A", Title: "Old", Content: long, PublishedAt: at(9, 0, 0)}))
		mustNoErr(t, repo.UpsertNewsItem(ctx, domain.NewsItem{ID: "new", Source: "B", Title: "New", Content: "  hi  ", PublishedAt: at(10, 0, 0), URL: "https://x"}))
		// A second upsert with the same id is ignored.
		mustNoErr(t, repo.UpsertNewsItem(ctx, domain.NewsItem{ID: "new", Source: "C", Title: "Changed"}))

		news, err := repo.GetRecentNews(ctx, 0)
		mustNoErr(t, err)
		if len(news) != 2 || news[0].ID != "new" || news[0].Title != "New" || news[0].Summary != "hi" {
			t.Fatalf("unexpected news: %+v", news)
		}
		if len([]rune(news[1].Summary)) != 200 || news[1].URL != nil || news[0].URL == nil {
			t.Errorf("summary length or url handling wrong: %+v", news)
		}
	})

	t.Run("GIVEN LLM briefings and reflections WHEN the latest is loaded THEN the newest is returned", func(t *testing.T) {
		repo := newTestRepo(t)
		none, err := repo.LoadLatestLLMBriefing(ctx)
		mustNoErr(t, err)
		if none != nil {
			t.Fatal("no briefing should exist yet")
		}
		mustNoErr(t, repo.SaveLLMBriefing(ctx, LLMBriefingInput{KeyInsight: "old", GeneratedAt: at(9, 0, 0)}))
		mustNoErr(t, repo.SaveLLMBriefing(ctx, LLMBriefingInput{
			KeyInsight: "new", OverallSentiment: 0.3, ArticleCount: 2,
			MarketOutlooks: map[string]any{"BTC/EUR": map[string]any{"bias": "bullish"}}, BriefedNewsIDs: []string{"n1"},
		}))
		mustNoErr(t, repo.SaveLLMReflection(ctx, LLMReflectionInput{Pattern: "p", Suggestion: "s", InsightConfidence: 0.5}))

		briefing, err := repo.LoadLatestLLMBriefing(ctx)
		mustNoErr(t, err)
		reflection, err := repo.LoadLatestLLMReflection(ctx)
		mustNoErr(t, err)
		if briefing == nil || briefing.KeyInsight != "new" || len(briefing.BriefedNewsIDs) != 1 || briefing.MarketOutlooks["BTC/EUR"] == nil {
			t.Errorf("unexpected briefing: %+v", briefing)
		}
		if reflection == nil || reflection.Pattern != "p" || reflection.GeneratedAt == "" {
			t.Errorf("unexpected reflection: %+v", reflection)
		}
	})
}
