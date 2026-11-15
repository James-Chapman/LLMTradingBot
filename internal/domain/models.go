// Package domain holds the core value types shared across the bot.
//
// Optional numeric fields use pointers so "unset" is distinguishable from 0.
// All timestamps are UTC.
package domain

import (
	"crypto/rand"
	"fmt"
	"time"
)

// TradingMode controls how signals turn into trades.
type TradingMode string

const (
	ModeManual         TradingMode = "manual"
	ModeSemiAutomated  TradingMode = "semi_automated"
	ModeFullyAutomated TradingMode = "fully_automated"
)

// TradingEnvironment selects paper (simulated) or live (real Kraken) execution.
type TradingEnvironment string

const (
	EnvPaper TradingEnvironment = "paper"
	EnvLive  TradingEnvironment = "live"
)

// Direction of a trade.
type Direction string

const (
	Long  Direction = "long"
	Short Direction = "short"
)

// MarketSnapshot is one price observation for a market.
type MarketSnapshot struct {
	Symbol    string    `json:"symbol"`
	Timestamp time.Time `json:"timestamp"`
	Price     float64   `json:"price"`
	Volume    *float64  `json:"volume,omitempty"`
	Bid       *float64  `json:"bid,omitempty"`
	Ask       *float64  `json:"ask,omitempty"`
}

// Candle is one OHLC bar; T is Unix seconds.
type Candle struct {
	T int64   `json:"t"`
	O float64 `json:"o"`
	H float64 `json:"h"`
	L float64 `json:"l"`
	C float64 `json:"c"`
	V float64 `json:"v"`
}

// NewsItem is a raw article from a news source.
type NewsItem struct {
	ID          string    `json:"id"`
	Source      string    `json:"source"`
	Title       string    `json:"title"`
	Content     string    `json:"content"`
	PublishedAt time.Time `json:"published_at"`
	URL         string    `json:"url,omitempty"`
}

// NewsSignal is a processed news item.
type NewsSignal struct {
	NewsItemID        string   `json:"news_item_id"`
	AssetMentions     []string `json:"asset_mentions"`
	HeadlineSentiment float64  `json:"headline_sentiment"` // -1..1
	SummarySentiment  *float64 `json:"summary_sentiment,omitempty"`
	EventType         string   `json:"event_type,omitempty"`
	EventSeverity     *float64 `json:"event_severity,omitempty"`
	Confidence        float64  `json:"confidence"` // 0..1
}

// EventSignal is a detected market event.
type EventSignal struct {
	EventType      string    `json:"event_type"`
	AssetsAffected []string  `json:"assets_affected"`
	Severity       float64   `json:"severity"`
	Description    string    `json:"description"`
	Timestamp      time.Time `json:"timestamp"`
}

// TradeIdea is a strategy-generated trade proposal.
type TradeIdea struct {
	ID                     string         `json:"id"`
	StrategyID             string         `json:"strategy_id"`
	Market                 string         `json:"market"`
	Direction              Direction      `json:"direction"`
	Thesis                 string         `json:"thesis"`
	SupportingSignals      map[string]any `json:"supporting_signals"`
	Confidence             float64        `json:"confidence"` // 0..1
	EntryPlan              string         `json:"entry_plan"`
	ExitPlan               string         `json:"exit_plan"`
	StopOrInvalidation     string         `json:"stop_or_invalidation"`
	PositionSizingProposal float64        `json:"position_sizing_proposal"` // fraction of equity
	ModeEligibility        []TradingMode  `json:"mode_eligibility"`
	// Exit marks a sell whose only purpose is to close a held long (the
	// 4-hour strategies' exit rules). Fully automated trading never drops it
	// for MIN_SIGNAL_CONFIDENCE, and with nothing held it is ignored rather
	// than opening a paper short.
	Exit bool `json:"exit,omitempty"`
}

// RiskDecision is the risk engine's verdict on a TradeIdea.
type RiskDecision struct {
	TradeIdeaID    string    `json:"trade_idea_id"`
	Approved       bool      `json:"approved"`
	Reason         string    `json:"reason"`
	AdjustedSizing *float64  `json:"adjusted_sizing,omitempty"`
	ProposedSize   *float64  `json:"proposed_size_eur,omitempty"` // exact quote-currency amount approved
	Timestamp      time.Time `json:"timestamp"`
}

// ApprovalRequest is a trade awaiting operator approval.
type ApprovalRequest struct {
	ID           string       `json:"id"`
	TradeIdea    TradeIdea    `json:"trade_idea"`
	RiskDecision RiskDecision `json:"risk_decision"`
	ExpiresAt    time.Time    `json:"expires_at"`
	Status       string       `json:"status"` // pending | approved | rejected | expired
}

// ExecutionIntent is an instruction to execute a trade.
type ExecutionIntent struct {
	ApprovalRequestID string    `json:"approval_request_id"`
	Market            string    `json:"market"`
	Direction         Direction `json:"direction"`
	Size              float64   `json:"size"`            // base-currency volume (e.g. BTC), sent to Kraken as `volume`
	Price             *float64  `json:"price,omitempty"` // limit price, if any
}

// OrderRecord is an order sent to (or simulated against) the exchange.
type OrderRecord struct {
	ID              string    `json:"id"`
	Market          string    `json:"market"`
	Direction       Direction `json:"direction"`
	Size            float64   `json:"size"`
	Price           float64   `json:"price"`
	Status          string    `json:"status"` // pending | filled | cancelled | rejected
	Timestamp       time.Time `json:"timestamp"`
	ExchangeOrderID string    `json:"exchange_order_id,omitempty"`
	PositionID      string    `json:"position_id,omitempty"`
}

// FillRecord is a fill against an order.
type FillRecord struct {
	OrderID   string    `json:"order_id"`
	FillPrice float64   `json:"fill_price"`
	FillSize  float64   `json:"fill_size"`
	Fee       float64   `json:"fee"`
	Timestamp time.Time `json:"timestamp"`
}

// PositionRecord is an open position.
type PositionRecord struct {
	PositionID    string    `json:"position_id"`
	Market        string    `json:"market"`
	Size          float64   `json:"size"` // positive long, negative short
	AvgPrice      float64   `json:"avg_price"`
	UnrealizedPnL float64   `json:"unrealized_pnl"`
	Timestamp     time.Time `json:"timestamp"`
}

// EquitySnapshot is a point-in-time account value.
type EquitySnapshot struct {
	TotalEquity    float64   `json:"total_equity"`
	Cash           float64   `json:"cash"`
	PositionsValue float64   `json:"positions_value"`
	Timestamp      time.Time `json:"timestamp"`
}

// UniverseSnapshot is the resolved set of tradable markets.
type UniverseSnapshot struct {
	FixedMarkets   []string          `json:"fixed_markets"`
	DynamicMarkets []string          `json:"dynamic_markets"`
	ResolverSource string            `json:"resolver_source"`
	ResolvedAt     time.Time         `json:"resolved_at"`
	Mapping        map[string]string `json:"mapping"` // coin -> market
}

// NewID returns a random RFC 4122 version-4 UUID string.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand failing is unrecoverable
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Float returns a pointer to v; handy for optional fields.
func Float(v float64) *float64 { return &v }

// NowUTC returns the current time in UTC.
func NowUTC() time.Time { return time.Now().UTC() }
