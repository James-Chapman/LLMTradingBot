package app

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/control"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/learner"
	"github.com/James-Chapman/LLMTradingBot/internal/observability"
	"github.com/James-Chapman/LLMTradingBot/internal/paper"
	"github.com/James-Chapman/LLMTradingBot/internal/portfolio"
	"github.com/James-Chapman/LLMTradingBot/internal/storage"
)

// StatusError is an operation failure with the HTTP status and detail main.py
// raised as an HTTPException.
type StatusError struct {
	Status int
	Detail string
}

// Error returns the detail text.
func (e *StatusError) Error() string { return e.Detail }

// statusError builds a *StatusError.
func statusError(status int, detail string) error {
	return &StatusError{Status: status, Detail: detail}
}

// Settings returns the settings the app was built with.
func (a *App) Settings() config.Settings { return a.cfg() }

// CurrencySymbol returns the display symbol of the base currency.
func (a *App) CurrencySymbol() string { return a.currencySymbol }

// Repository returns the storage repository for read-only API endpoints.
func (a *App) Repository() *storage.Repository { return a.repo }

// Activity returns the activity log the app writes to.
func (a *App) Activity() *observability.ActivityLog { return a.activity }

// DashboardVersion returns the counter used as the dashboard ETag.
func (a *App) DashboardVersion() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.dashboardVersion
}

// ActiveMarkets returns a copy of the validated market list.
func (a *App) ActiveMarkets() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string{}, a.activeMarkets...)
}

// News returns a copy of the cached latest news, newest first.
func (a *App) News() []storage.NewsEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]storage.NewsEntry{}, a.latestNews...)
}

// LearningSummary returns the learner's per-key stats.
func (a *App) LearningSummary() []learner.SummaryRow {
	return a.learner.Summary()
}

// LLMHealth returns the LLM availability block for /api/health.
func (a *App) LLMHealth() LLMHealth {
	return LLMHealth{
		LLMAvailable: a.llmClient.Available(),
		LLMBackend:   a.llmClient.ActiveBackend(),
		LLMModel:     a.llmClient.ModelName(),
	}
}

// OHLC returns cached candles for market; interval 15 selects the 15-minute
// cache and anything else the 5-minute one. An empty payload echoes the
// requested interval so the chart waits gracefully.
func (a *App) OHLC(market string, interval int) OHLCPayload {
	a.mu.Lock()
	defer a.mu.Unlock()
	cache := a.ohlc5
	if interval == 15 {
		cache = a.ohlc15
	}
	if cached, ok := cache[market]; ok && len(cached.Candles) > 0 {
		return cached
	}
	return OHLCPayload{Symbol: market, Interval: interval, Candles: []domain.Candle{}}
}

// PendingApprovals returns the non-expired approvals as API views.
func (a *App) PendingApprovals() []ApprovalView {
	pending := a.approvals.GetPending()
	out := make([]ApprovalView, 0, len(pending))
	for _, req := range pending {
		out = append(out, approvalView(req))
	}
	return out
}

// Dashboard builds the /api/dashboard payload.
func (a *App) Dashboard() DashboardView {
	ctrl := a.control.Snapshot()
	disabled := toSet(ctrl.DisabledMarkets)
	live := toSet(ctrl.LiveMarkets)

	a.mu.Lock()
	markets := []MarketView{}
	seen := map[string]bool{}
	var extra []string
	for sym := range a.currentPrices {
		extra = append(extra, sym)
	}
	sort.Strings(extra)
	// Priced markets first (active order, then any other priced symbol),
	// followed by active markets that have not been priced yet.
	ordered := append(append([]string{}, a.activeMarkets...), extra...)
	for _, sym := range ordered {
		price, ok := a.currentPrices[sym]
		if !ok || seen[sym] {
			continue
		}
		seen[sym] = true
		markets = append(markets, MarketView{Symbol: sym, Price: fmt.Sprintf("%.2f", price), Enabled: !disabled[sym], Live: live[sym]})
	}
	for _, sym := range a.activeMarkets {
		if !seen[sym] {
			seen[sym] = true
			markets = append(markets, MarketView{Symbol: sym, Price: "-", Enabled: !disabled[sym], Live: live[sym]})
		}
	}
	signals := append([]SignalView{}, a.latestSignals...)
	rejections := append([]storage.RiskRejection{}, trimHead(a.riskRejections, riskRejectionsShown)...)
	history := append([]portfolio.EquityPoint{}, a.equityHistory...)
	equity, cash := a.currentEquity, a.currentCash
	sessionTick := a.sessionTick
	strategies := a.strategies
	a.mu.Unlock()

	positions := []PositionView{}
	openIDs := []string{}
	for _, pos := range a.paper.OpenPositions() {
		meta, _ := a.paper.Meta(pos.PositionID)
		direction := "short"
		if pos.Size > 0 {
			direction = "long"
		}
		openedAt := ""
		if !pos.Timestamp.IsZero() {
			openedAt = portfolio.FormatISO(pos.Timestamp)
		}
		positions = append(positions, PositionView{
			PositionID:     short8(pos.PositionID),
			PositionIDFull: pos.PositionID,
			Market:         pos.Market,
			Direction:      direction,
			OpenedAt:       openedAt,
			Size:           fmt.Sprintf("%.6f", abs(pos.Size)),
			AvgPrice:       fmt.Sprintf("%.2f", pos.AvgPrice),
			UnrealizedPnL:  fmt.Sprintf("%.2f", pos.UnrealizedPnL),
			Strategy:       meta.StrategyID,
			Source:         "", // the paper engine never records a source
			Status:         "open",
			TradeIdeaID:    meta.TradeIdeaID,
		})
		openIDs = append(openIDs, pos.PositionID)
	}

	strategyViews := make([]StrategyView, 0, len(strategies))
	for _, s := range strategies {
		label, ok := StrategyLabels[s.ID()]
		if !ok {
			label = s.ID()
		}
		selected := a.control.IsStrategySelected(s.ID())
		strategyViews = append(strategyViews, StrategyView{ID: s.ID(), Label: label, Enabled: selected, Selected: selected})
	}

	llmView := LLMView{
		Available: a.llmClient.Available(),
		Status:    a.llmStatus(),
		Model:     a.llmClient.ModelName(),
		Backend:   a.llmClient.ActiveBackend(),
	}
	if ref := a.analyser.LatestReflection(); ref != nil {
		llmView.Reflection = &ReflectionView{
			Pattern: ref.Pattern, Suggestion: ref.Suggestion, Confidence: ref.InsightConfidence,
			GeneratedAt: portfolio.FormatISO(ref.GeneratedAt),
		}
	}
	if brf := a.analyser.LatestBriefing(); brf != nil {
		llmView.Briefing = &BriefingView{
			KeyInsight: brf.KeyInsight, OverallSentiment: brf.OverallSentiment, MarketOutlooks: brf.MarketOutlooks,
			ArticleCount: brf.ArticleCount, GeneratedAt: portfolio.FormatISO(brf.GeneratedAt),
		}
	}

	return DashboardView{
		Mode:                 a.TradingMode(),
		Environment:          a.TradingEnvironment(),
		BaseCurrency:         a.cfg().BaseCurrency,
		CurrencySymbol:       a.currencySymbol,
		Equity:               fmt.Sprintf("%.2f", equity),
		Cash:                 fmt.Sprintf("%.2f", cash),
		OpenPositionIDs:      openIDs,
		Markets:              markets,
		Signals:              signals,
		Positions:            positions,
		Approvals:            a.PendingApprovals(),
		EquityHistory:        portfolio.AlignEquityHistoryWithCurrent(history, equity, a.now()),
		RiskRejections:       rejections,
		Activity:             nonNilActivity(a.activity.Recent(activityShown)),
		Control:              ctrl,
		Strategies:           strategyViews,
		WarmupTicksRemaining: max(0, TradeWarmupTicks-sessionTick),
		Learning:             a.learner.Summary(),
		Version:              Version,
		LLM:                  llmView,
	}
}

// FallbackDashboard is the reduced payload main.py returned when building
// the dashboard failed.
func (a *App) FallbackDashboard() map[string]any {
	return map[string]any{
		"mode":            a.TradingMode(),
		"environment":     a.TradingEnvironment(),
		"base_currency":   a.cfg().BaseCurrency,
		"currency_symbol": a.currencySymbol,
		"equity":          fmt.Sprintf("%.2f", a.cfg().StartingCapital),
		"markets":         []any{},
		"signals":         []any{},
		"positions":       []any{},
		"approvals":       []any{},
		"equity_history": []portfolio.EquityPoint{{
			Timestamp: portfolio.FormatISO(a.now()), Equity: a.cfg().StartingCapital,
		}},
		"risk_rejections": []any{},
		"activity":        nonNilActivity(a.activity.Recent(activityShown)),
		"control":         a.control.Snapshot(),
		"strategies":      []any{},
	}
}

// ControlSnapshot returns the operator control state.
func (a *App) ControlSnapshot() control.Snapshot {
	return a.control.Snapshot()
}

// ActivateEmergencyStop halts trading, clears the approval queue and alerts.
func (a *App) ActivateEmergencyStop(ctx context.Context) map[string]any {
	a.control.ActivateStop()
	cleared := a.approvals.ClearPending()
	a.bumpVersion()
	a.logger.Warn("Emergency stop activated - approval queue cleared", "cleared", cleared)
	a.sendAlert(ctx, "emergency_stop_activated", map[string]any{"cleared_approvals": cleared})
	return map[string]any{"status": "stopped", "timestamp": portfolio.FormatISO(a.now())}
}

// Resume clears the emergency stop.
func (a *App) Resume() map[string]any {
	a.control.Resume()
	a.bumpVersion()
	return map[string]any{"status": "resumed", "timestamp": portfolio.FormatISO(a.now())}
}

// ToggleMarket flips whether the bot trades market and returns the new state.
func (a *App) ToggleMarket(market string) map[string]any {
	enabled := !a.control.IsMarketEnabled(market)
	if enabled {
		a.control.EnableMarket(market)
	} else {
		a.control.DisableMarket(market)
	}
	a.bumpVersion()
	return map[string]any{"market": market, "enabled": enabled}
}

// ToggleMarketLive flips whether market routes to live execution.
func (a *App) ToggleMarketLive(market string) map[string]any {
	live := !a.control.IsMarketLive(market)
	a.control.SetMarketLive(market, live)
	a.bumpVersion()
	return map[string]any{"market": market, "live": live}
}

// SelectStrategy makes the strategy (legacy IDs accepted) the active one and
// returns its canonical ID; 404 when no such strategy exists.
func (a *App) SelectStrategy(strategyID string) (string, error) {
	s := a.strategyByID(strategyID)
	if s == nil {
		return "", statusError(http.StatusNotFound, "Strategy not found")
	}
	a.control.SelectStrategy(s.ID())
	a.bumpVersion()
	return s.ID(), nil
}

// ReloadStrategies rebuilds the strategy instances from settings.
func (a *App) ReloadStrategies() []string {
	fresh := a.strategyFactory(a.cfg())
	ids := make([]string, 0, len(fresh))
	for _, s := range fresh {
		ids = append(ids, s.ID())
	}
	a.mu.Lock()
	a.strategies = fresh
	a.mu.Unlock()
	a.activity.Info("Strategies reloaded", strings.Join(ids, ", "))
	return ids
}

// ApproveTrade executes a pending approval at the current market price,
// routing to Kraken for live markets and to the paper engine otherwise.
func (a *App) ApproveTrade(ctx context.Context, approvalID string) (map[string]any, error) {
	if a.control.EmergencyStop() {
		return nil, statusError(http.StatusServiceUnavailable, "Emergency stop is active - cannot execute trades")
	}
	if _, ok := a.approvals.Get(approvalID); !ok {
		return nil, statusError(http.StatusNotFound, "Approval not found or expired")
	}
	req, ok := a.approvals.Approve(approvalID)
	if !ok {
		return nil, statusError(http.StatusNotFound, "Approval expired between peek and approve")
	}

	idea := req.TradeIdea
	prices := a.pricesCopy()
	marketPrice, ok := prices[idea.Market]
	if !ok || marketPrice <= 0 {
		a.logger.Warn("Cannot execute: no live price for market", "market", idea.Market)
		return map[string]any{
			"status": "approved_no_price",
			"id":     approvalID,
			"detail": fmt.Sprintf("No live price for %s; trade logged but not executed", idea.Market),
		}, nil
	}

	account := a.accountSnapshot(ctx, prices)
	size := sizeInBase(idea, req.RiskDecision, account.Equity, marketPrice)
	intent := domain.ExecutionIntent{ApprovalRequestID: approvalID, Market: idea.Market, Direction: idea.Direction, Size: size}
	order, posID, isLive := a.executeIntent(ctx, intent, idea, marketPrice)
	environment := string(domain.EnvPaper)
	if isLive {
		environment = string(domain.EnvLive)
	}
	a.logger.Info("Trade executed after approval",
		"approval_id", approvalID, "order_id", order.ID, "market", idea.Market,
		"status", order.Status, "environment", environment, "position_id", posID)

	label := directionLabel(string(idea.Direction))
	if order.Status == "rejected" {
		a.activity.Error(
			fmt.Sprintf("Manual approval: %s %s - order REJECTED (insufficient funds)", idea.Market, label),
			fmt.Sprintf("Equity %s%.2f  Cash %s%.2f  Needed %s%.2f",
				a.currencySymbol, account.Equity, a.currencySymbol, account.Cash, a.currencySymbol, size*marketPrice),
		)
	} else {
		if !isLive {
			a.savePaperEquity(ctx, prices)
		}
		// Record outcome and P&L when this approval closed an existing long.
		if !isLive && idea.Direction == domain.Short && posID != "" {
			if pnl, ok := a.paper.RecordClosedTrade(posID, order.Price, "manual_approve", idea.ID); ok {
				a.recordClosedOutcome(ctx, order.ID, idea.StrategyID, idea.Market, string(idea.Direction), pnl)
			}
		}
		shortPos := "-"
		if posID != "" {
			shortPos = short8(posID)
		}
		a.activity.Success(
			fmt.Sprintf("Manual approval: %s %s - %s order %s", idea.Market, label, environment, order.Status),
			fmt.Sprintf("Size: %.6f @ %s%s  Order: %s  [%s]", size, a.currencySymbol, fmtMoney(marketPrice), short8(order.ID), shortPos),
		)
	}
	a.bumpVersion()
	return map[string]any{"status": "approved", "id": approvalID, "order_id": order.ID, "order_status": order.Status}, nil
}

// RejectTrade declines a pending approval.
func (a *App) RejectTrade(approvalID string) (map[string]any, error) {
	req, ok := a.approvals.Reject(approvalID)
	if !ok {
		return nil, statusError(http.StatusNotFound, "Approval not found")
	}
	a.logger.Info("Trade rejected via API", "approval_id", approvalID)
	a.activity.Warn(fmt.Sprintf("Manual rejection: %s %s - operator declined", req.TradeIdea.Market, directionLabel(string(req.TradeIdea.Direction))), "")
	a.bumpVersion()
	return map[string]any{"status": "rejected", "id": approvalID}, nil
}

// ClosePosition manually closes one open paper position (full ID or the
// dashboard's 8-character prefix) at the current market price.
func (a *App) ClosePosition(ctx context.Context, positionID string) (map[string]any, error) {
	if a.control.EmergencyStop() {
		return nil, statusError(http.StatusServiceUnavailable, "Emergency stop is active")
	}
	pos, found := a.paper.Position(positionID)
	if !found {
		for _, p := range a.paper.OpenPositions() {
			if strings.HasPrefix(p.PositionID, positionID) {
				pos, found = p, true
				positionID = p.PositionID
				break
			}
		}
	}
	if !found {
		return nil, statusError(http.StatusNotFound, "Position not found")
	}

	prices := a.pricesCopy()
	marketPrice := prices[pos.Market]
	if marketPrice == 0 {
		return nil, statusError(http.StatusServiceUnavailable, fmt.Sprintf("No live price for %s", pos.Market))
	}

	order, ok := a.paper.ClosePosition(positionID, marketPrice, paper.CloseOptions{ApprovalRequestID: "manual_close"})
	if !ok {
		return nil, statusError(http.StatusNotFound, "Position not found or already closed")
	}
	pnl, ok := a.paper.RecordClosedTrade(positionID, order.Price, "manual_close", "")
	if !ok {
		return nil, statusError(http.StatusInternalServerError, "Closed position outcome could not be recorded")
	}
	strategyID, direction := a.metaStrategyDirection(positionID)
	a.recordClosedOutcome(ctx, order.ID, strategyID, pos.Market, direction, pnl)
	a.savePaperEquity(ctx, prices)

	cash := a.paper.Cash()
	a.activity.Success(
		fmt.Sprintf("Position manually closed: %s %s", pos.Market, directionLabel(string(order.Direction))),
		fmt.Sprintf("@ %s%s  PnL %s%s  Cash now %s%s",
			a.currencySymbol, fmtMoney(order.Price), a.currencySymbol, fmtSignedMoney(pnl), a.currencySymbol, fmtMoney(cash)),
	)
	a.logger.Info("Position manually closed",
		"position_id", positionID, "market", pos.Market, "fill_price", order.Price, "order_id", order.ID, "pnl", pnl)
	a.bumpVersion()
	return map[string]any{
		"status":      "closed",
		"position_id": positionID,
		"order_id":    order.ID,
		"fill_price":  order.Price,
		"pnl":         pnl,
		"cash":        cash,
	}, nil
}

// ResetPositions closes and logs every open paper position, then clears
// them from memory and the database. Refused in live mode.
func (a *App) ResetPositions(ctx context.Context) (int, error) {
	if a.TradingEnvironment() == string(domain.EnvLive) {
		return 0, statusError(http.StatusForbidden, "Position reset is not allowed in live mode")
	}
	prices := a.pricesCopy()
	count := paper.ClosePositionsForOperatorReset(a.paper, prices, a.recordTradeResult, paperStore{repo: a.repo}, a.learner)
	if _, err := a.repo.ClearAllOpenPositions(ctx); err != nil {
		return 0, err
	}
	a.paper.ClearPositions()
	a.savePaperEquity(ctx, prices)

	a.logger.Warn("Open positions reset by operator", "cleared", count)
	a.activity.Warn(fmt.Sprintf("Positions reset by operator - %d position(s) cleared and logged", count), "")
	a.bumpVersion()
	return count, nil
}

// toSet converts a string slice into a membership set.
func toSet(items []string) map[string]bool {
	out := make(map[string]bool, len(items))
	for _, s := range items {
		out[s] = true
	}
	return out
}

// nonNilActivity guarantees a JSON array for the activity feed.
func nonNilActivity(entries []observability.ActivityEntry) []observability.ActivityEntry {
	if entries == nil {
		return []observability.ActivityEntry{}
	}
	return entries
}

// abs returns |v|.
func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
