// Package risk enforces portfolio-level rules before any trade idea reaches
// approval or execution. The same rules apply to paper and live trading:
//   - one open position per trading pair
//   - sufficient cash before any buy
//   - per-trade loss limit
//   - daily loss limit
//   - minimum trade size (a floor the size is clamped up to, not a gate)
package risk

import (
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/observability"
)

var logger = observability.Logger("risk_engine")

// State is the daily risk tracking that is persisted across restarts.
// LastResetDate is a UTC calendar date (midnight UTC).
type State struct {
	DailyLoss        float64
	DailyStartEquity float64
	LastResetDate    time.Time
}

// EvaluateOptions carries the optional market context for EvaluateTrade.
// Nil pointers mean "not supplied" and skip the related check.
type EvaluateOptions struct {
	OpenPositions   []domain.PositionRecord
	AvailableCash   *float64
	MarketPrice     *float64
	MarketVolume24h *float64
}

// Engine is the centralised risk manager. It is safe for concurrent use.
type Engine struct {
	mu               sync.Mutex
	settings         config.Settings
	now              func() time.Time
	currentEquity    float64
	dailyLoss        float64
	dailyStartEquity float64
	lastResetDate    time.Time
}

// NewEngine creates a risk engine seeded with the configured starting capital.
// A nil clock defaults to the current UTC time.
func NewEngine(settings config.Settings, now func() time.Time) *Engine {
	if now == nil {
		now = domain.NowUTC
	}
	return &Engine{
		settings:         settings,
		now:              now,
		currentEquity:    settings.StartingCapital,
		dailyStartEquity: settings.StartingCapital,
		lastResetDate:    utcDate(now()),
	}
}

// StopLossAssumption returns the stop-loss fraction used to estimate trade loss
// (the Python STOP_LOSS_ASSUMPTION constant, sourced from settings).
func (e *Engine) StopLossAssumption() float64 {
	return e.settings.StopLossPct
}

// CurrentEquity returns the equity used by the risk limits.
func (e *Engine) CurrentEquity() float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.currentEquity
}

// UpdateEquity sets the equity used by risk limits.
func (e *Engine) UpdateEquity(newEquity float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.currentEquity = newEquity
}

// Snapshot returns the current daily risk tracking state.
func (e *Engine) Snapshot() State {
	e.mu.Lock()
	defer e.mu.Unlock()
	return State{
		DailyLoss:        e.dailyLoss,
		DailyStartEquity: e.dailyStartEquity,
		LastResetDate:    e.lastResetDate,
	}
}

// RestoreState applies persisted daily tracking if it belongs to today (UTC).
// It reports whether the state was applied; stale state is ignored so the
// daily reset still happens after a restart on a new day.
func (e *Engine) RestoreState(state State) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	today := utcDate(e.now())
	if !utcDate(state.LastResetDate).Equal(today) {
		return false
	}
	e.dailyLoss = state.DailyLoss
	e.dailyStartEquity = state.DailyStartEquity
	e.lastResetDate = today
	return true
}

// RecordTradeResult records a completed trade's P&L and updates daily tracking.
func (e *Engine) RecordTradeResult(pnl float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.recordTradeResultLocked(pnl)
}

// recordTradeResultLocked applies a trade result; the caller holds e.mu.
func (e *Engine) recordTradeResultLocked(pnl float64) {
	e.checkDailyResetLocked()
	e.dailyLoss += math.Max(0, -pnl)
	e.currentEquity += pnl
	logger.Info("Trade result recorded",
		"pnl", pnl, "daily_loss", e.dailyLoss, "current_equity", e.currentEquity)
}

// EvaluateTrade checks a trade idea against all risk constraints.
func (e *Engine) EvaluateTrade(idea domain.TradeIdea, opts EvaluateOptions) domain.RiskDecision {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.checkDailyResetLocked()

	s := e.settings
	symbol := config.CurrencySymbol(s.BaseCurrency)
	feeFactor := 1 + s.FeeAndSlippage

	strategySize := idea.PositionSizingProposal * e.currentEquity
	proposedSize := strategySize
	if s.TargetTradeAmount > 0 {
		proposedSize = s.TargetTradeAmount
	}
	// The exchange minimum is a floor, not a risk gate: clamp up silently so the
	// remaining checks run against the size that will actually be traded.
	adjusted := math.Abs(proposedSize-strategySize) > 0.000001
	if proposedSize < s.MinTradeSize {
		proposedSize = s.MinTradeSize
		adjusted = true
	}

	if s.Min24hVolume > 0 && opts.MarketVolume24h != nil && *opts.MarketVolume24h < s.Min24hVolume {
		return e.reject(idea, fmt.Sprintf("Insufficient liquidity: 24h volume %.2f below minimum %.2f",
			*opts.MarketVolume24h, s.Min24hVolume))
	}

	var marketPositions []domain.PositionRecord
	for _, p := range opts.OpenPositions {
		if p.Market == idea.Market {
			marketPositions = append(marketPositions, p)
		}
	}
	for _, p := range marketPositions {
		if (p.Size > 0 && idea.Direction == domain.Long) || (p.Size < 0 && idea.Direction == domain.Short) {
			return e.reject(idea, fmt.Sprintf("Position already open for %s - one position per pair", idea.Market))
		}
	}

	// A short that fully closes an existing long sells holdings, so needs no cash.
	totalClosableLong := 0.0
	for _, p := range marketPositions {
		if p.Size > 0 {
			totalClosableLong += p.Size
		}
	}
	hasPrice := opts.MarketPrice != nil && *opts.MarketPrice > 0
	proposedBaseSize := 0.0
	if hasPrice {
		proposedBaseSize = proposedSize / *opts.MarketPrice
	}
	shortFullyClosesLong := idea.Direction == domain.Short && proposedBaseSize > 0 && totalClosableLong >= proposedBaseSize
	requiresCash := idea.Direction == domain.Long || (idea.Direction == domain.Short && !shortFullyClosesLong)

	if opts.AvailableCash != nil && hasPrice && requiresCash {
		cash := *opts.AvailableCash
		if cash < proposedSize*feeFactor {
			cashSized := cash / feeFactor
			if cashSized < s.MinTradeSize {
				return e.reject(idea, fmt.Sprintf("Insufficient cash: need at least %s%.2f, have %s%.2f",
					symbol, s.MinTradeSize*feeFactor, symbol, cash))
			}
			proposedSize = cashSized
			adjusted = true
		}
	}

	maxLoss := e.currentEquity * s.MaxLossPerTradePercent / 100
	estimatedLoss := proposedSize * s.StopLossPct
	if estimatedLoss > maxLoss {
		return e.reject(idea, fmt.Sprintf("Estimated loss %s%.2f exceeds per-trade limit %s%.2f",
			symbol, estimatedLoss, symbol, maxLoss))
	}

	dailyLimit := e.currentEquity * s.MaxDailyLossPercent / 100
	if e.dailyLoss+estimatedLoss >= dailyLimit {
		return e.reject(idea, fmt.Sprintf(
			"Daily loss limit reached: %s%.2f accumulated + %s%.2f estimated = %s%.2f of %s%.2f",
			symbol, e.dailyLoss, symbol, estimatedLoss, symbol, e.dailyLoss+estimatedLoss, symbol, dailyLimit))
	}

	decision := domain.RiskDecision{
		TradeIdeaID:  idea.ID,
		Approved:     true,
		Reason:       "All risk checks passed",
		ProposedSize: domain.Float(proposedSize),
		Timestamp:    e.now().UTC(),
	}
	if adjusted {
		decision.AdjustedSizing = domain.Float(proposedSize / e.currentEquity)
	}
	return decision
}

// checkDailyResetLocked resets daily counters when the UTC date changes; the
// caller holds e.mu.
func (e *Engine) checkDailyResetLocked() {
	today := utcDate(e.now())
	if today.Equal(e.lastResetDate) {
		return
	}
	logger.Info("Daily risk reset",
		"previous_date", e.lastResetDate.Format(time.DateOnly), "daily_loss", e.dailyLoss)
	e.dailyLoss = 0
	e.dailyStartEquity = e.currentEquity
	e.lastResetDate = today
}

// reject builds a rejected risk decision with the supplied reason.
func (e *Engine) reject(idea domain.TradeIdea, reason string) domain.RiskDecision {
	return domain.RiskDecision{
		TradeIdeaID: idea.ID,
		Approved:    false,
		Reason:      reason,
		Timestamp:   e.now().UTC(),
	}
}

// utcDate truncates t to midnight of its UTC calendar date.
func utcDate(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
