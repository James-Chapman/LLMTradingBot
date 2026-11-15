package app

import (
	"fmt"
	"math"
	"strconv"

	"github.com/James-Chapman/LLMTradingBot/internal/control"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/learner"
	"github.com/James-Chapman/LLMTradingBot/internal/llm"
	"github.com/James-Chapman/LLMTradingBot/internal/observability"
	"github.com/James-Chapman/LLMTradingBot/internal/paper"
	"github.com/James-Chapman/LLMTradingBot/internal/portfolio"
	"github.com/James-Chapman/LLMTradingBot/internal/storage"
)

// The view types below are the JSON shapes the frontend consumes; their keys
// match the dicts main.py returned.

// SignalView is one entry of the dashboard signal buffer.
type SignalView struct {
	Strategy     string  `json:"strategy"`
	Market       string  `json:"market"`
	Direction    string  `json:"direction"`
	Confidence   float64 `json:"confidence"`
	Thesis       string  `json:"thesis"`
	RiskApproved bool    `json:"risk_approved"`
	RiskReason   string  `json:"risk_reason"`
	TradeIdeaID  string  `json:"trade_idea_id"`
}

// MarketView is one row of the dashboard market list.
type MarketView struct {
	Symbol     string   `json:"symbol"`
	Price      string   `json:"price"`       // formatPrice text, or "-" when not yet priced
	PriceValue *float64 `json:"price_value"` // raw price; null when not yet priced
	Enabled    bool     `json:"enabled"`
	Live       bool     `json:"live"` // orders on this market go to Kraken in the live environment
}

// PositionView is one open position on the dashboard.
type PositionView struct {
	PositionID     string  `json:"position_id"`      // short form for display
	PositionIDFull string  `json:"position_id_full"` // full UUID for API calls
	Market         string  `json:"market"`
	Direction      string  `json:"direction"`
	OpenedAt       string  `json:"opened_at"`
	Size           string  `json:"size"`
	AvgPrice       string  `json:"avg_price"`       // formatPrice text
	AvgPriceValue  float64 `json:"avg_price_value"` // raw average entry price
	UnrealizedPnL  string  `json:"unrealized_pnl"`  // money: always 2 decimals
	Strategy       string  `json:"strategy"`
	Source         string  `json:"source"`
	Status         string  `json:"status"`      // open; live rows also entry_pending | exit_pending
	Environment    string  `json:"environment"` // paper | live
	TradeIdeaID    string  `json:"trade_idea_id"`
}

// ApprovalView is a pending approval as shown on the dashboard and approvals page.
type ApprovalView struct {
	ID             string   `json:"id"`
	Market         string   `json:"market"`
	Direction      string   `json:"direction"`
	StrategyID     string   `json:"strategy_id"`
	Confidence     float64  `json:"confidence"`
	Size           float64  `json:"size"` // proposed sizing as a percentage of equity
	ExpiresAt      string   `json:"expires_at"`
	Thesis         string   `json:"thesis"`
	EntryPlan      string   `json:"entry_plan"`
	ExitPlan       string   `json:"exit_plan"`
	RiskApproved   bool     `json:"risk_approved"`
	RiskReason     string   `json:"risk_reason"`
	AdjustedSizing *float64 `json:"adjusted_sizing"`
	Status         string   `json:"status"`
}

// StrategyView is one strategy toggle on the dashboard.
type StrategyView struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Enabled  bool   `json:"enabled"`
	Selected bool   `json:"selected"`
}

// ReflectionView is the latest LLM reflection on the dashboard.
type ReflectionView struct {
	Pattern     string  `json:"pattern"`
	Suggestion  string  `json:"suggestion"`
	Confidence  float64 `json:"confidence"`
	GeneratedAt string  `json:"generated_at"`
}

// BriefingView is the latest LLM market briefing on the dashboard.
type BriefingView struct {
	KeyInsight       string                       `json:"key_insight"`
	OverallSentiment float64                      `json:"overall_sentiment"`
	MarketOutlooks   map[string]llm.MarketOutlook `json:"market_outlooks"`
	ArticleCount     int                          `json:"article_count"`
	GeneratedAt      string                       `json:"generated_at"`
}

// LLMView is the dashboard's LLM status block.
type LLMView struct {
	Available  bool            `json:"available"`
	Status     string          `json:"status"` // not_configured | available | unavailable
	Model      string          `json:"model"`
	Backend    string          `json:"backend"`
	Reflection *ReflectionView `json:"reflection"`
	Briefing   *BriefingView   `json:"briefing"`
}

// DashboardView is the full /api/dashboard payload.
type DashboardView struct {
	Mode                 string                        `json:"mode"`
	Environment          string                        `json:"environment"`
	BaseCurrency         string                        `json:"base_currency"`
	CurrencySymbol       string                        `json:"currency_symbol"`
	Equity               string                        `json:"equity"`
	Cash                 string                        `json:"cash"`
	OpenPositionIDs      []string                      `json:"open_position_ids"`
	Markets              []MarketView                  `json:"markets"`
	Signals              []SignalView                  `json:"signals"`
	Positions            []PositionView                `json:"positions"`
	Approvals            []ApprovalView                `json:"approvals"`
	EquityHistory        []portfolio.EquityPoint       `json:"equity_history"`
	RiskRejections       []storage.RiskRejection       `json:"risk_rejections"`
	Activity             []observability.ActivityEntry `json:"activity"`
	Control              control.Snapshot              `json:"control"`
	Strategies           []StrategyView                `json:"strategies"`
	WarmupTicksRemaining int                           `json:"warmup_ticks_remaining"`
	Learning             []learner.SummaryRow          `json:"learning"`
	Version              string                        `json:"version"`
	LLM                  LLMView                       `json:"llm"`
}

// OHLCPayload is a cached candle set for one market and interval.
type OHLCPayload struct {
	Symbol   string          `json:"symbol"`
	Interval int             `json:"interval"`
	Candles  []domain.Candle `json:"candles"`
}

// LLMHealth is the /api/health payload.
type LLMHealth struct {
	LLMAvailable bool   `json:"llm_available"`
	LLMBackend   string `json:"llm_backend"`
	LLMModel     string `json:"llm_model"`
}

// priceSignificantDigits is how many significant digits a price below 1 keeps,
// so coins priced far below a cent never show as 0.00.
const priceSignificantDigits = 4

// formatPrice formats a market price: 2 decimals at or above 1, otherwise
// enough decimals for priceSignificantDigits significant digits. Money amounts
// (equity, cash, P&L, order value) are not prices and keep "%.2f".
func formatPrice(price float64) string {
	magnitude := math.Abs(price)
	if magnitude >= 1 || magnitude == 0 || math.IsNaN(magnitude) || math.IsInf(magnitude, 0) {
		return fmt.Sprintf("%.2f", price)
	}
	// e.g. 0.0000085: floor(log10) = -6, so 3 + 6 = 9 decimals -> 0.000008500
	decimals := priceSignificantDigits - 1 - int(math.Floor(math.Log10(magnitude)))
	return strconv.FormatFloat(price, 'f', min(decimals, 12), 64)
}

// marketView builds one market row; ok is false while the market has no price yet.
func marketView(symbol string, price float64, ok, enabled, live bool) MarketView {
	view := MarketView{Symbol: symbol, Price: "-", Enabled: enabled, Live: live}
	if ok {
		view.Price = formatPrice(price)
		view.PriceValue = &price
	}
	return view
}

// positionView converts an open paper position and its metadata to its API view.
func positionView(pos domain.PositionRecord, meta paper.PositionMeta) PositionView {
	direction := "short"
	if pos.Size > 0 {
		direction = "long"
	}
	openedAt := ""
	if !pos.Timestamp.IsZero() {
		openedAt = portfolio.FormatISO(pos.Timestamp)
	}
	return PositionView{
		PositionID:     short8(pos.PositionID),
		PositionIDFull: pos.PositionID,
		Market:         pos.Market,
		Direction:      direction,
		OpenedAt:       openedAt,
		Size:           fmt.Sprintf("%.6f", abs(pos.Size)),
		AvgPrice:       formatPrice(pos.AvgPrice),
		AvgPriceValue:  pos.AvgPrice,
		UnrealizedPnL:  fmt.Sprintf("%.2f", pos.UnrealizedPnL),
		Strategy:       meta.StrategyID,
		Source:         "", // the paper engine never records a source
		Status:         "open",
		Environment:    string(domain.EnvPaper),
		TradeIdeaID:    meta.TradeIdeaID,
	}
}

// livePositionView converts a live position to its API view. Live positions
// are spot longs; Status is entry_pending, open or exit_pending.
func livePositionView(pos LivePositionRecord) PositionView {
	openedAt := ""
	if !pos.Timestamp.IsZero() {
		openedAt = portfolio.FormatISO(pos.Timestamp)
	}
	return PositionView{
		PositionID:     short8(pos.PositionID),
		PositionIDFull: pos.PositionID,
		Market:         pos.Market,
		Direction:      string(domain.Long),
		OpenedAt:       openedAt,
		Size:           fmt.Sprintf("%.6f", abs(pos.Size)),
		AvgPrice:       formatPrice(pos.AvgPrice),
		AvgPriceValue:  pos.AvgPrice,
		UnrealizedPnL:  fmt.Sprintf("%.2f", pos.UnrealizedPnL),
		Strategy:       pos.StrategyID,
		Source:         "",
		Status:         pos.Status,
		Environment:    pos.Environment,
		TradeIdeaID:    pos.TradeIdeaID,
	}
}

// approvalView converts an approval request to its API view.
func approvalView(req domain.ApprovalRequest) ApprovalView {
	idea, risk := req.TradeIdea, req.RiskDecision
	return ApprovalView{
		ID:             req.ID,
		Market:         idea.Market,
		Direction:      string(idea.Direction),
		StrategyID:     idea.StrategyID,
		Confidence:     idea.Confidence,
		Size:           idea.PositionSizingProposal * 100,
		ExpiresAt:      portfolio.FormatISO(req.ExpiresAt),
		Thesis:         idea.Thesis,
		EntryPlan:      idea.EntryPlan,
		ExitPlan:       idea.ExitPlan,
		RiskApproved:   risk.Approved,
		RiskReason:     risk.Reason,
		AdjustedSizing: risk.AdjustedSizing,
		Status:         req.Status,
	}
}
