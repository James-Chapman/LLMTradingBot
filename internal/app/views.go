package app

import (
	"github.com/James-Chapman/LLMTradingBot/internal/control"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/learner"
	"github.com/James-Chapman/LLMTradingBot/internal/llm"
	"github.com/James-Chapman/LLMTradingBot/internal/observability"
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
	Symbol  string `json:"symbol"`
	Price   string `json:"price"` // "%.2f" or "-" when not yet priced
	Enabled bool   `json:"enabled"`
	Live    bool   `json:"live"`
}

// PositionView is one open position on the dashboard.
type PositionView struct {
	PositionID     string `json:"position_id"`      // short form for display
	PositionIDFull string `json:"position_id_full"` // full UUID for API calls
	Market         string `json:"market"`
	Direction      string `json:"direction"`
	OpenedAt       string `json:"opened_at"`
	Size           string `json:"size"`
	AvgPrice       string `json:"avg_price"`
	UnrealizedPnL  string `json:"unrealized_pnl"`
	Strategy       string `json:"strategy"`
	Source         string `json:"source"`
	Status         string `json:"status"`
	TradeIdeaID    string `json:"trade_idea_id"`
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
