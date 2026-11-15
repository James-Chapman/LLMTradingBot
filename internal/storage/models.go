package storage

import (
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

// Retention limits and default page sizes (ported from repository.py).
const (
	MaxPriceRowsPerSymbol = 5_760  // 48 h of price ticks at 30 s intervals
	MaxEquityRows         = 17_280 // 48 h of equity snapshots at 10 s intervals
	DefaultActivityKeep   = 2_000

	DefaultLedgerLimit          = 200
	DefaultSignalOutcomeLimit   = 1_000
	DefaultRecentPricesLimit    = 60
	DefaultRecentNewsLimit      = 60
	DefaultEquityHistoryLimit   = 288
	DefaultClosedTradesLimit    = 200
	DefaultPnLSummaryLimit      = 1_000
	DefaultActivityLimit        = 200
	DefaultRiskRejectionsLimit  = 50
	DefaultRejectedTradesLimit  = 100
	DefaultSelectedStrategy     = "basic_and_llm_strategy"
	DefaultRejectedTradeReason  = "insufficient_funds"
	defaultUnknownStrategyLabel = "unknown"
)

// ── Inputs ─────────────────────────────────────────────────────────────────

// LLMAnalysis is the LLM verdict stored alongside a trade idea.
type LLMAnalysis struct {
	Used            bool
	Sentiment       float64
	ConfidenceScale float64
	Reasoning       string
}

// NewsContextItem is one article snippet shown to the LLM for a signal.
type NewsContextItem struct {
	Source  string `json:"source"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Summary string `json:"summary"`
}

// SignalContext is the optional context persisted with a trade idea.
type SignalContext struct {
	MomentumPct  float64
	Indicators   map[string]any // e.g. analysis.Indicators
	LLM          *LLMAnalysis
	NewsContext  []NewsContextItem
	RiskDecision *domain.RiskDecision
}

// SaveOrderOptions are the extra order_records fields not on domain.OrderRecord.
type SaveOrderOptions struct {
	ApprovalID  string  // "auto", "stop_loss", an approval ID, or "" (system)
	Fee         float64 // order fee
	Environment string  // "paper" (default) or "live"
	TradeIdeaID string  // signal that triggered the order
	// ClientOrderID is the cl_ord_id a live order was sent with, so an order
	// whose Kraken reply was lost can be found again; empty for paper orders.
	ClientOrderID string
}

// OpenPositionInput is the data written by UpsertOpenPosition.
type OpenPositionInput struct {
	PositionID       string
	Market           string
	Size             float64
	AvgPrice         float64
	Direction        string
	StrategyID       string
	SignalConfidence *float64
	TradeIdeaID      string
	EntryFee         float64  // fee paid when the position was opened
	ShortProceeds    *float64 // locked sale value of a paper short; nil for longs
}

// SignalOutcomeInput describes a closed position to record for learning.
type SignalOutcomeInput struct {
	StrategyID         string
	Market             string
	Direction          string
	EntryPrice         float64
	ExitPrice          float64
	Size               float64
	PnL                float64
	Confidence         *float64
	ExitReason         string // stop_loss | manual_approve | manual_reject | auto
	EntryAt            time.Time
	PositionID         string
	TradeIdeaID        string
	ClosingTradeIdeaID string
}

// RiskRejectionInput is a risk-engine rejection to record; zero Timestamp means now.
type RiskRejectionInput struct {
	Market      string
	Direction   string
	Confidence  float64
	Thesis      string
	Reason      string
	TradeIdeaID string
	Timestamp   time.Time
}

// RejectedTradeInput is an execution-level rejection; zero Timestamp means now
// and an empty Reason means "insufficient_funds".
type RejectedTradeInput struct {
	Market      string
	Direction   string
	Size        float64
	Price       float64
	Confidence  *float64
	Reason      string
	TradeIdeaID string
	Timestamp   time.Time
}

// LLMBriefingInput is a market briefing to store; zero GeneratedAt means now.
type LLMBriefingInput struct {
	KeyInsight       string
	OverallSentiment float64
	MarketOutlooks   map[string]any // {market: {bias, score, note}}
	ArticleCount     int
	BriefedNewsIDs   []string
	GeneratedAt      time.Time
}

// LLMReflectionInput is an outcome reflection to store; zero GeneratedAt means now.
type LLMReflectionInput struct {
	Pattern           string
	Suggestion        string
	InsightConfidence float64
	GeneratedAt       time.Time
}

// ── Row structs (Python returned ORM rows) ─────────────────────────────────

// OpenPosition is one row of open_positions.
type OpenPosition struct {
	PositionID       string    `json:"position_id"`
	Market           string    `json:"market"`
	Size             float64   `json:"size"`
	AvgPrice         float64   `json:"avg_price"`
	SignalConfidence *float64  `json:"signal_confidence"`
	StrategyID       *string   `json:"strategy_id"`
	Direction        *string   `json:"direction"`
	TradeIdeaID      *string   `json:"trade_idea_id"`
	UnrealizedPnL    float64   `json:"unrealized_pnl"`
	OpenedAt         time.Time `json:"opened_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	EntryFee         float64   `json:"entry_fee"`      // 0 for rows saved before it was recorded
	ShortProceeds    *float64  `json:"short_proceeds"` // nil for longs and older rows
}

// SignalOutcome is one row of signal_outcomes (a closed trade).
type SignalOutcome struct {
	ID                 int64     `json:"id"`
	StrategyID         string    `json:"strategy_id"`
	Market             string    `json:"market"`
	Direction          string    `json:"direction"`
	EntryPrice         float64   `json:"entry_price"`
	ExitPrice          float64   `json:"exit_price"`
	Size               float64   `json:"size"`
	PnL                float64   `json:"pnl"`
	PnLPct             float64   `json:"pnl_pct"`
	ConfidenceAtEntry  *float64  `json:"confidence_at_entry"`
	ExitReason         string    `json:"exit_reason"`
	EntryAt            time.Time `json:"entry_at"`
	ExitAt             time.Time `json:"exit_at"`
	PositionID         *string   `json:"position_id"`
	TradeIdeaID        *string   `json:"trade_idea_id"`
	ClosingTradeIdeaID *string   `json:"closing_trade_idea_id"`
}

// ── API result structs (Python returned dicts; JSON keys must not change) ──

// SignalDetail is the full signal context for one trade idea.
type SignalDetail struct {
	ID                 string            `json:"id"`
	StrategyID         string            `json:"strategy_id"`
	Market             string            `json:"market"`
	Direction          string            `json:"direction"`
	Thesis             string            `json:"thesis"`
	SupportingSignals  map[string]any    `json:"supporting_signals"`
	Confidence         float64           `json:"confidence"`
	MomentumPct        *float64          `json:"momentum_pct"`
	Indicators         map[string]any    `json:"indicators"`
	LLMUsed            bool              `json:"llm_used"`
	LLMSentiment       *float64          `json:"llm_sentiment"`
	LLMConfidenceScale *float64          `json:"llm_confidence_scale"`
	LLMReasoning       *string           `json:"llm_reasoning"`
	NewsContext        []NewsContextItem `json:"news_context"`
	RiskApproved       *bool             `json:"risk_approved"`
	RiskReason         *string           `json:"risk_reason"`
	EntryPlan          string            `json:"entry_plan"`
	ExitPlan           string            `json:"exit_plan"`
	StopOrInvalidation string            `json:"stop_or_invalidation"`
	CreatedAt          string            `json:"created_at"`
}

// LedgerEntry is one row of the operational trade ledger.
type LedgerEntry struct {
	ID              string   `json:"id"`
	PositionID      string   `json:"position_id"`
	PositionIDFull  string   `json:"position_id_full"`
	TradeIdeaID     string   `json:"trade_idea_id"`
	Strategy        string   `json:"strategy"`
	Market          string   `json:"market"`
	Direction       string   `json:"direction"`
	TradeType       string   `json:"trade_type"` // open | close
	Size            float64  `json:"size"`
	Price           float64  `json:"price"`
	Fee             float64  `json:"fee"`
	Value           float64  `json:"value"`
	PnL             *float64 `json:"pnl"` // nil for opens
	Status          string   `json:"status"`
	Environment     string   `json:"environment"`
	ExchangeOrderID string   `json:"exchange_order_id"`
	Source          string   `json:"source"` // stop-loss | auto | manual | system
	Timestamp       string   `json:"timestamp"`
}

// PendingLiveOrder is a live order awaiting Kraken reconciliation.
type PendingLiveOrder struct {
	ID              string    `json:"id"`
	ExchangeOrderID string    `json:"exchange_order_id"` // empty when only the client order id is known
	ClientOrderID   string    `json:"client_order_id"`   // empty for orders sent before client ids were used
	PositionID      string    `json:"position_id"`       // live position the order belongs to, if any
	Direction       string    `json:"direction"`         // "long" (buy) or "short" (sell)
	Size            float64   `json:"size"`
	Price           float64   `json:"price"`
	Timestamp       time.Time `json:"timestamp"`
}

// OrderExecution is a reconciled live order: its final (or still pending)
// status and what Kraken executed. ExecutedSize 0 keeps the requested size;
// an empty ExchangeOrderID keeps the stored txid.
type OrderExecution struct {
	OrderID         string
	ExchangeOrderID string
	Status          string
	ExecutedSize    float64
	AvgPrice        float64
	Fee             float64
}

// NewsEntry is a recent news item for the dashboard.
type NewsEntry struct {
	ID          string  `json:"id"`
	Source      string  `json:"source"`
	Title       string  `json:"title"`
	URL         *string `json:"url"`
	Summary     string  `json:"summary"`
	PublishedAt string  `json:"published_at"`
}

// EquityPoint is one point on the equity chart.
type EquityPoint struct {
	Timestamp string  `json:"timestamp"`
	Equity    float64 `json:"equity"`
}

// ClosedTrade is a signal outcome joined with its entry indicators.
type ClosedTrade struct {
	Strategy           string         `json:"strategy"`
	Market             string         `json:"market"`
	Direction          string         `json:"direction"`
	EntryPrice         float64        `json:"entry_price"`
	ExitPrice          float64        `json:"exit_price"`
	Size               float64        `json:"size"`
	PnL                float64        `json:"pnl"`
	PnLPct             float64        `json:"pnl_pct"`
	Confidence         *float64       `json:"confidence"`
	ExitReason         string         `json:"exit_reason"`
	TradeIdeaID        string         `json:"trade_idea_id"`
	ClosingTradeIdeaID string         `json:"closing_trade_idea_id"`
	EntryAt            string         `json:"entry_at"`
	ExitAt             string         `json:"exit_at"`
	PositionID         string         `json:"position_id"`
	PositionIDFull     string         `json:"position_id_full"`
	Indicators         map[string]any `json:"indicators"` // nil when no linked trade idea
}

// PnLSummary is realised P&L grouped by day and by market.
type PnLSummary struct {
	TotalPnL float64            `json:"total_pnl"`
	ByDay    map[string]float64 `json:"by_day"`
	ByMarket map[string]float64 `json:"by_market"`
}

// RiskRejection is a stored risk-engine rejection.
type RiskRejection struct {
	Market      string  `json:"market"`
	Direction   string  `json:"direction"`
	Confidence  float64 `json:"confidence"`
	Thesis      string  `json:"thesis"`
	Reason      string  `json:"reason"`
	TradeIdeaID string  `json:"trade_idea_id"`
	Timestamp   string  `json:"timestamp"`
}

// RejectedTrade is a stored execution-level rejection.
type RejectedTrade struct {
	ID          int64    `json:"id"`
	Market      string   `json:"market"`
	Direction   string   `json:"direction"`
	Size        float64  `json:"size"`
	Price       float64  `json:"price"`
	Confidence  *float64 `json:"confidence"`
	Reason      string   `json:"reason"`
	TradeIdeaID string   `json:"trade_idea_id"`
	Strategy    string   `json:"strategy"`
	Timestamp   string   `json:"timestamp"`
}

// ControlState holds the operator toggles that survive restarts.
type ControlState struct {
	EmergencyStop      bool     `json:"emergency_stop"`
	DisabledMarkets    []string `json:"disabled_markets"`
	DisabledStrategies []string `json:"disabled_strategies"`
	LiveMarkets        []string `json:"live_markets"`
	SelectedStrategy   string   `json:"selected_strategy"`
	// TradingMode and TradingEnvironment are the values chosen in the web
	// interface; empty means never chosen, so the default applies.
	TradingMode        string `json:"trading_mode"`
	TradingEnvironment string `json:"trading_environment"`
	// EmergencyStopSince is when the active emergency stop was pressed; nil
	// when not stopped or for rows saved before it was recorded.
	EmergencyStopSince *time.Time `json:"emergency_stop_since"`
}

// LLMBriefing is a stored LLM market briefing.
type LLMBriefing struct {
	KeyInsight       string         `json:"key_insight"`
	OverallSentiment float64        `json:"overall_sentiment"`
	MarketOutlooks   map[string]any `json:"market_outlooks"`
	ArticleCount     int            `json:"article_count"`
	BriefedNewsIDs   []string       `json:"briefed_news_ids"`
	GeneratedAt      string         `json:"generated_at"`
}

// LLMReflection is a stored LLM outcome reflection.
type LLMReflection struct {
	Pattern           string  `json:"pattern"`
	Suggestion        string  `json:"suggestion"`
	InsightConfidence float64 `json:"insight_confidence"`
	GeneratedAt       string  `json:"generated_at"`
}

// RiskState is the persisted daily risk tracking; LastResetDate is a UTC date (midnight).
type RiskState struct {
	DailyLoss        float64   `json:"daily_loss"`
	DailyStartEquity float64   `json:"daily_start_equity"`
	LastResetDate    time.Time `json:"last_reset_date"`
}
