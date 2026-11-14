package app

import (
	"context"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/control"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/kraken"
	"github.com/James-Chapman/LLMTradingBot/internal/learner"
	"github.com/James-Chapman/LLMTradingBot/internal/livebook"
	"github.com/James-Chapman/LLMTradingBot/internal/llm"
	"github.com/James-Chapman/LLMTradingBot/internal/paper"
	"github.com/James-Chapman/LLMTradingBot/internal/storage"
)

// The adapters below bridge *storage.Repository (ctx-first methods with input
// structs) to the small persistence interfaces each component declares.
// Database calls are local and fast, so a background context is used where
// the consumer interface carries none.

// riskStore adapts the repository to risk.StateStore.
type riskStore struct{ repo *storage.Repository }

// SaveRiskState persists the daily risk counters.
func (s riskStore) SaveRiskState(dailyLoss, dailyStartEquity float64, lastResetDate time.Time) error {
	return s.repo.SaveRiskState(context.Background(), dailyLoss, dailyStartEquity, lastResetDate)
}

// approvalStore adapts the repository to approval.Repository.
type approvalStore struct{ repo *storage.Repository }

// SaveApprovalRequest persists a pending approval.
func (s approvalStore) SaveApprovalRequest(req domain.ApprovalRequest) error {
	return s.repo.SaveApprovalRequest(context.Background(), req)
}

// UpdateApprovalStatus updates a persisted approval's status.
func (s approvalStore) UpdateApprovalStatus(approvalID, status string) error {
	return s.repo.UpdateApprovalStatus(context.Background(), approvalID, status)
}

// ClearPendingApprovals marks every pending approval as cleared.
func (s approvalStore) ClearPendingApprovals() (int, error) {
	return s.repo.ClearPendingApprovals(context.Background())
}

// LoadPendingApprovalRequests returns pending, non-expired approvals.
func (s approvalStore) LoadPendingApprovalRequests(now time.Time) ([]domain.ApprovalRequest, error) {
	return s.repo.LoadPendingApprovalRequests(context.Background(), now)
}

// controlStore adapts the repository to control.Repository.
type controlStore struct{ repo *storage.Repository }

// LoadControlState returns the persisted control toggles; found is false when never saved.
func (s controlStore) LoadControlState() (control.PersistedState, bool, error) {
	st, err := s.repo.LoadControlState(context.Background())
	if err != nil || st == nil {
		return control.PersistedState{}, false, err
	}
	return control.PersistedState{
		EmergencyStop:      st.EmergencyStop,
		StopSince:          st.EmergencyStopSince,
		DisabledMarkets:    st.DisabledMarkets,
		DisabledStrategies: st.DisabledStrategies,
		LiveMarkets:        st.LiveMarkets,
		SelectedStrategy:   st.SelectedStrategy,
		TradingMode:        st.TradingMode,
		TradingEnvironment: st.TradingEnvironment,
	}, true, nil
}

// SaveControlState persists the control toggles.
func (s controlStore) SaveControlState(state control.PersistedState) error {
	return s.repo.SaveControlState(context.Background(), storage.ControlState{
		EmergencyStop:      state.EmergencyStop,
		EmergencyStopSince: state.StopSince,
		DisabledMarkets:    state.DisabledMarkets,
		DisabledStrategies: state.DisabledStrategies,
		LiveMarkets:        state.LiveMarkets,
		SelectedStrategy:   state.SelectedStrategy,
		TradingMode:        state.TradingMode,
		TradingEnvironment: state.TradingEnvironment,
	})
}

// paperStore adapts the repository to paper.Repository and paper.OrderPnLStore.
type paperStore struct{ repo *storage.Repository }

// GetOpenPositions returns every persisted open position.
func (s paperStore) GetOpenPositions() ([]paper.OpenPositionRow, error) {
	rows, err := s.repo.GetOpenPositions(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]paper.OpenPositionRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, paper.OpenPositionRow{
			PositionID:       r.PositionID,
			Market:           r.Market,
			Size:             r.Size,
			AvgPrice:         r.AvgPrice,
			UnrealizedPnL:    r.UnrealizedPnL,
			Direction:        derefString(r.Direction),
			StrategyID:       derefString(r.StrategyID),
			SignalConfidence: r.SignalConfidence,
			TradeIdeaID:      derefString(r.TradeIdeaID),
			OpenedAt:         r.OpenedAt,
			EntryFee:         r.EntryFee,
			ShortProceeds:    r.ShortProceeds,
		})
	}
	return out, nil
}

// SaveRejectedTrade records a paper order refused at execution time.
func (s paperStore) SaveRejectedTrade(trade paper.RejectedTrade) error {
	return s.repo.SaveRejectedTrade(context.Background(), storage.RejectedTradeInput{
		Market:      trade.Market,
		Direction:   trade.Direction,
		Size:        trade.Size,
		Price:       trade.Price,
		Confidence:  trade.Confidence,
		Reason:      trade.Reason,
		TradeIdeaID: trade.TradeIdeaID,
	})
}

// SaveOrder persists a paper order for the trade ledger.
func (s paperStore) SaveOrder(order domain.OrderRecord, approvalID string, fee float64, environment, tradeIdeaID string) error {
	return saveOrder(s.repo, order, approvalID, fee, environment, tradeIdeaID)
}

// SaveFill records a simulated fill.
func (s paperStore) SaveFill(fill domain.FillRecord) error {
	return s.repo.SaveFill(context.Background(), fill)
}

// UpsertOpenPosition inserts or updates an open position.
func (s paperStore) UpsertOpenPosition(row paper.OpenPositionRow) error {
	return s.repo.UpsertOpenPosition(context.Background(), storage.OpenPositionInput{
		PositionID:       row.PositionID,
		Market:           row.Market,
		Size:             row.Size,
		AvgPrice:         row.AvgPrice,
		Direction:        row.Direction,
		StrategyID:       row.StrategyID,
		SignalConfidence: row.SignalConfidence,
		TradeIdeaID:      row.TradeIdeaID,
		EntryFee:         row.EntryFee,
		ShortProceeds:    row.ShortProceeds,
	})
}

// DeleteOpenPosition removes one open position.
func (s paperStore) DeleteOpenPosition(positionID string) error {
	return s.repo.DeleteOpenPosition(context.Background(), positionID)
}

// SaveSignalOutcome records a closed position for history and learning.
func (s paperStore) SaveSignalOutcome(o paper.SignalOutcome) error {
	return s.repo.SaveSignalOutcome(context.Background(), storage.SignalOutcomeInput{
		StrategyID:         o.StrategyID,
		Market:             o.Market,
		Direction:          o.Direction,
		EntryPrice:         o.EntryPrice,
		ExitPrice:          o.ExitPrice,
		Size:               o.Size,
		PnL:                o.PnL,
		Confidence:         o.Confidence,
		ExitReason:         o.ExitReason,
		EntryAt:            o.EntryAt,
		PositionID:         o.PositionID,
		TradeIdeaID:        o.TradeIdeaID,
		ClosingTradeIdeaID: o.ClosingTradeIdeaID,
	})
}

// UpdateOrderPnL stamps realised P&L onto a close order (paper.OrderPnLStore).
func (s paperStore) UpdateOrderPnL(orderID string, pnl float64) error {
	return s.repo.UpdateOrderPnL(context.Background(), orderID, pnl)
}

// krakenStore adapts the repository to kraken.OrderRepository.
type krakenStore struct{ repo *storage.Repository }

// SaveOrder stores an accepted live order, with its client order id, in the trade ledger.
func (s krakenStore) SaveOrder(order domain.OrderRecord, meta kraken.OrderMeta) error {
	return s.repo.SaveOrder(context.Background(), order, storage.SaveOrderOptions{
		ApprovalID:    meta.ApprovalID,
		Fee:           meta.Fee,
		Environment:   meta.Environment,
		TradeIdeaID:   meta.TradeIdeaID,
		ClientOrderID: meta.ClientOrderID,
	})
}

// SaveRejectedTrade stores a live order refused by Kraken or locally.
func (s krakenStore) SaveRejectedTrade(trade kraken.RejectedTrade) error {
	return s.repo.SaveRejectedTrade(context.Background(), storage.RejectedTradeInput{
		Market:      trade.Market,
		Direction:   string(trade.Direction),
		Size:        trade.Size,
		Price:       trade.Price,
		Confidence:  trade.Confidence,
		Reason:      trade.Reason,
		TradeIdeaID: trade.TradeIdeaID,
		Timestamp:   trade.Timestamp,
	})
}

// GetPendingLiveOrders returns pending live orders awaiting reconciliation.
func (s krakenStore) GetPendingLiveOrders() ([]kraken.PendingLiveOrder, error) {
	rows, err := s.repo.GetPendingLiveOrders(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]kraken.PendingLiveOrder, 0, len(rows))
	for _, r := range rows {
		out = append(out, kraken.PendingLiveOrder{
			ID: r.ID, ExchangeOrderID: r.ExchangeOrderID, ClientOrderID: r.ClientOrderID,
			PositionID: r.PositionID, Direction: domain.Direction(r.Direction),
			Size: r.Size, Price: r.Price, Timestamp: r.Timestamp,
		})
	}
	return out, nil
}

// UpdateOrderExecution records a reconciled live order's status and execution.
func (s krakenStore) UpdateOrderExecution(u kraken.OrderUpdate) error {
	return s.repo.UpdateOrderExecution(context.Background(), storage.OrderExecution{
		OrderID: u.OrderID, ExchangeOrderID: u.ExchangeOrderID, Status: u.Status,
		ExecutedSize: u.ExecutedSize, AvgPrice: u.AvgPrice, Fee: u.Fee,
	})
}

// liveBookStore adapts the repository to livebook.Store.
type liveBookStore struct{ repo *storage.Repository }

// SaveLivePosition persists one live position.
func (s liveBookStore) SaveLivePosition(p livebook.Position) error {
	return s.repo.SaveLivePosition(context.Background(), storage.LivePosition{
		PositionID: p.ID, Market: p.Market, Status: string(p.Status),
		RequestedSize: p.RequestedSize, Size: p.Size, AvgPrice: p.AvgPrice, EntryFee: p.EntryFee,
		EntryOrderID: p.EntryOrderID, ExitOrderID: p.ExitOrderID, ExitReason: p.ExitReason,
		ExitTradeIdeaID: p.ExitTradeIdeaID, StrategyID: p.StrategyID, TradeIdeaID: p.TradeIdeaID,
		SignalConfidence: p.Confidence, OpenedAt: p.OpenedAt,
		TrailingHigh: p.TrailingHigh, ExitRequestedAt: p.ExitRequestedAt,
	})
}

// DeleteLivePosition removes one live position.
func (s liveBookStore) DeleteLivePosition(id string) error {
	return s.repo.DeleteLivePosition(context.Background(), id)
}

// LoadLivePositions returns every persisted live position.
func (s liveBookStore) LoadLivePositions() ([]livebook.Position, error) {
	rows, err := s.repo.GetLivePositions(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]livebook.Position, 0, len(rows))
	for _, r := range rows {
		out = append(out, livebook.Position{
			ID: r.PositionID, Market: r.Market, Status: livebook.Status(r.Status),
			RequestedSize: r.RequestedSize, Size: r.Size, AvgPrice: r.AvgPrice, EntryFee: r.EntryFee,
			EntryOrderID: r.EntryOrderID, ExitOrderID: r.ExitOrderID, ExitReason: r.ExitReason,
			ExitTradeIdeaID: r.ExitTradeIdeaID, StrategyID: r.StrategyID, TradeIdeaID: r.TradeIdeaID,
			Confidence: r.SignalConfidence, OpenedAt: r.OpenedAt,
			TrailingHigh: r.TrailingHigh, ExitRequestedAt: r.ExitRequestedAt,
		})
	}
	return out, nil
}

// saveOrder is the shared paper/live order persistence call.
func saveOrder(repo *storage.Repository, order domain.OrderRecord, approvalID string, fee float64, environment, tradeIdeaID string) error {
	return repo.SaveOrder(context.Background(), order, storage.SaveOrderOptions{
		ApprovalID:  approvalID,
		Fee:         fee,
		Environment: environment,
		TradeIdeaID: tradeIdeaID,
	})
}

// briefingStore adapts the repository to llm.BriefingStore.
type briefingStore struct{ repo *storage.Repository }

// SaveBriefing persists a market briefing with the news IDs it covered.
func (s briefingStore) SaveBriefing(ctx context.Context, b llm.MarketBriefing, briefedNewsIDs []string) error {
	outlooks := make(map[string]any, len(b.MarketOutlooks))
	for market, o := range b.MarketOutlooks {
		outlooks[market] = map[string]any{"bias": o.Bias, "score": o.Score, "note": o.Note}
	}
	return s.repo.SaveLLMBriefing(ctx, storage.LLMBriefingInput{
		KeyInsight:       b.KeyInsight,
		OverallSentiment: b.OverallSentiment,
		MarketOutlooks:   outlooks,
		ArticleCount:     b.ArticleCount,
		BriefedNewsIDs:   briefedNewsIDs,
		GeneratedAt:      b.GeneratedAt,
	})
}

// LoadLatestBriefing returns the newest stored briefing, or nil when none exists.
func (s briefingStore) LoadLatestBriefing(ctx context.Context) (*llm.MarketBriefing, error) {
	b, err := s.repo.LoadLatestLLMBriefing(ctx)
	if err != nil || b == nil {
		return nil, err
	}
	return &llm.MarketBriefing{
		MarketOutlooks:   llm.NormaliseMarketOutlooks(b.MarketOutlooks, nil),
		OverallSentiment: b.OverallSentiment,
		KeyInsight:       b.KeyInsight,
		ArticleCount:     b.ArticleCount,
		GeneratedAt:      parseStoredTime(b.GeneratedAt),
	}, nil
}

// SaveReflection persists an outcome reflection.
func (s briefingStore) SaveReflection(ctx context.Context, r llm.Reflection) error {
	return s.repo.SaveLLMReflection(ctx, storage.LLMReflectionInput{
		Pattern:           r.Pattern,
		Suggestion:        r.Suggestion,
		InsightConfidence: r.InsightConfidence,
		GeneratedAt:       r.GeneratedAt,
	})
}

// LoadLatestReflection returns the newest stored reflection, or nil when none exists.
func (s briefingStore) LoadLatestReflection(ctx context.Context) (*llm.Reflection, error) {
	r, err := s.repo.LoadLatestLLMReflection(ctx)
	if err != nil || r == nil {
		return nil, err
	}
	return &llm.Reflection{
		Pattern:           r.Pattern,
		Suggestion:        r.Suggestion,
		InsightConfidence: r.InsightConfidence,
		GeneratedAt:       parseStoredTime(r.GeneratedAt),
	}, nil
}

// paperEquity exposes the paper engine as a portfolio.EquityPricedEngine.
type paperEquity struct{ engine *paper.Engine }

// GetCash returns the free paper cash.
func (p paperEquity) GetCash() float64 { return p.engine.Cash() }

// GetTotalEquity returns paper cash plus positions marked at prices.
func (p paperEquity) GetTotalEquity(prices map[string]float64) float64 {
	return p.engine.TotalEquity(prices)
}

// learnerOutcomes converts stored signal outcomes (newest first) for the
// learner; each carries its notional (entry price × |size|) so the learner
// scores percentage returns.
func learnerOutcomes(rows []storage.SignalOutcome) []learner.Outcome {
	out := make([]learner.Outcome, 0, len(rows))
	for _, r := range rows {
		out = append(out, learner.Outcome{
			StrategyID: r.StrategyID, Market: r.Market, Direction: r.Direction, Pnl: r.PnL,
			Notional: r.EntryPrice * abs(r.Size),
		})
	}
	return out
}

// storedTimeLayouts are the ISO layouts the repository returns (zone-less means UTC).
var storedTimeLayouts = []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999"}

// parseStoredTime parses an ISO timestamp from the repository, or returns the zero time.
func parseStoredTime(s string) time.Time {
	for _, layout := range storedTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// derefString returns *p, or "" when p is nil.
func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
