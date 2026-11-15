package backtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/histdata"
)

// ResultSchemaVersion is bumped whenever Result's JSON shape changes.
// Version 2 added initial_markets, learner_seed_outcomes, learned_outcomes,
// tick_stats and the equity points' peak. Version 3 added fill_bar_mid,
// settings_source, risk_fee, fill_empty_candles, stale_after,
// stale_positions, ending_equity_worst_case, data_check, data_warnings and
// the equity points' stale_value and stale_positions.
const ResultSchemaVersion = 3

// Order sides.
const (
	SideBuy  = "buy"
	SideSell = "sell"
)

// Why an order was sent.
const (
	ReasonSignal       = "signal"        // strategy idea (entry, or a sell that exits a long)
	ReasonStopLoss     = "stop_loss"     // stop-loss breached on a tick price
	ReasonTrailingStop = "trailing_stop" // trailing stop breached on a tick price
	ReasonEndOfRun     = "end_of_run"    // valuation of a position still open when the run ends
	// ReasonStaleLiquidation values, at the end of the run, a position whose
	// market had not traded for longer than the stale threshold.
	ReasonStaleLiquidation = "stale_liquidation"
)

// Order statuses.
const (
	StatusFilled    = "filled"
	StatusRejected  = "rejected"
	StatusCancelled = "cancelled"
)

// Order rejection reasons.
const (
	RejectLiquidity = "insufficient_liquidity" // no trades in the fill bar, or above the liquidity cap
	RejectFunds     = "insufficient_funds"     // cash cannot pay value plus fee
	RejectMinOrder  = "below_min_order"        // quantity below the pair's ordermin (or zero after lot rounding)
	RejectMinCost   = "below_min_cost"         // value below the pair's costmin
	RejectNoPrice   = "no_price"               // the market has no price to fill at
	RejectGone      = "position_closed"        // the position to sell was already closed
	RejectPaper     = "paper_engine_rejected"  // the paper engine refused the fill
	CancelRunEnded  = "run_ended"              // still waiting to fill when the run ended
)

// Fill price sources.
const (
	SourceTakerSide      = "taker_side_average"              // the fill bar's buy (or sell) taker average
	SourceMeasuredSpread = "close_plus_measured_half_spread" // fill bar close ± measured half-spread
	SourceFixedSpread    = "close_plus_fixed_half_spread"    // fill bar close ± fixed half-spread
	// SourceOtherSideSpread: the fill bar only had takers on the other side,
	// so its prints sit at the far quote; the fill crosses the full measured
	// spread from that side's average.
	SourceOtherSideSpread = "other_side_plus_full_spread"
)

// Decision outcomes for strategy ideas.
const (
	OutcomeSubmitted     = "order_submitted"
	OutcomeRiskRejected  = "risk_rejected"
	OutcomeNoHolding     = "spot_no_holding"      // a sell signal with no long held: ignored, never a short
	OutcomeLowConfidence = "below_min_confidence" // fully automated MinSignalConfidence gate
	OutcomePositionOpen  = "position_already_open"
	OutcomeOrderPending  = "order_pending"   // an order on the market has not filled yet
	OutcomeNotInUniverse = "not_in_universe" // entry on a market dropped from the point-in-time universe
	OutcomeNoPrice       = "no_price"
)

// OrderLogEntry is one order request and what became of it. Prices are in the
// quote currency per base unit; the cost components are per unit and signed
// so that a positive value is a cost to the bot. For a buy
// FillPrice-ReferencePrice, and for a sell ReferencePrice-FillPrice, equals
// LatencyDrift+SpreadCost+ImpactCost+RoundingCost.
type OrderLogEntry struct {
	ID         string `json:"id"`
	Market     string `json:"market"`
	Side       string `json:"side"`
	Reason     string `json:"reason"`
	StrategyID string `json:"strategy_id,omitempty"`
	IdeaID     string `json:"idea_id,omitempty"`
	PositionID string `json:"position_id,omitempty"`

	DecisionTime time.Time `json:"decision_time"` // tick the order was decided on
	FillTime     time.Time `json:"fill_time"`     // tick whose bar priced the fill (or the attempt)

	RequestedQty   float64 `json:"requested_qty"` // base units, after lot rounding
	FilledQty      float64 `json:"filled_qty"`
	ReferencePrice float64 `json:"reference_price"` // tick close at the decision
	FillBarClose   float64 `json:"fill_bar_close"`  // tick close at the fill
	FillBarMid     float64 `json:"fill_bar_mid"`    // estimated mid of the fill bar (see fillQuote)
	FillPrice      float64 `json:"fill_price"`
	PriceSource    string  `json:"price_source,omitempty"`
	HalfSpread     float64 `json:"half_spread,omitempty"` // fraction applied when no taker side was used

	LatencyDrift  float64  `json:"latency_drift"`          // fill bar mid vs reference
	SpreadCost    float64  `json:"spread_cost"`            // base fill price vs fill bar mid
	ImpactCost    float64  `json:"impact_cost"`            // size impact
	RoundingCost  float64  `json:"rounding_cost"`          // price rounded to the pair's decimals
	Participation float64  `json:"participation"`          // order value / fill bar same-side taker value (see sideTradedValue)
	Notional      float64  `json:"notional"`               // FilledQty × FillPrice
	Fee           float64  `json:"fee"`                    // quote currency
	FeeRate       float64  `json:"fee_rate"`               // fraction
	RealisedPnL   *float64 `json:"realised_pnl,omitempty"` // sells that closed a position

	Status       string `json:"status"`
	RejectReason string `json:"reject_reason,omitempty"`
}

// ClosedTrade is one position from entry to exit.
type ClosedTrade struct {
	PositionID  string    `json:"position_id"`
	Market      string    `json:"market"`
	StrategyID  string    `json:"strategy_id"`
	EntryOrder  string    `json:"entry_order"`
	ExitOrder   string    `json:"exit_order"`
	EntryTime   time.Time `json:"entry_time"`
	ExitTime    time.Time `json:"exit_time"`
	Qty         float64   `json:"qty"`
	EntryPrice  float64   `json:"entry_price"`
	ExitPrice   float64   `json:"exit_price"`
	EntryFee    float64   `json:"entry_fee"`
	ExitFee     float64   `json:"exit_fee"`
	PnL         float64   `json:"pnl"` // paper engine realised P&L, both fees included
	ExitReason  string    `json:"exit_reason"`
	EntryIdeaID string    `json:"entry_idea_id,omitempty"`
}

// Decision is one strategy idea and how the fully automated path handled it.
type Decision struct {
	Time          time.Time `json:"time"`
	IdeaID        string    `json:"idea_id"`
	Market        string    `json:"market"`
	StrategyID    string    `json:"strategy_id"`
	Direction     string    `json:"direction"`
	RawConfidence float64   `json:"raw_confidence"` // as the strategy returned it
	Confidence    float64   `json:"confidence"`     // after the learner
	RiskApproved  bool      `json:"risk_approved"`
	RiskReason    string    `json:"risk_reason"`
	Outcome       string    `json:"outcome"`
	OrderID       string    `json:"order_id,omitempty"`
}

// EquityPoint is the account at one tick. MinEquity is the lowest tick
// equity since the previous point, so sampling never hides a drawdown, and
// Peak is the highest equity of any tick up to and including this one (the
// starting capital included).
type EquityPoint struct {
	Time           time.Time `json:"time"`
	Equity         float64   `json:"equity"`
	Cash           float64   `json:"cash"`
	PositionsValue float64   `json:"positions_value"`
	MinEquity      float64   `json:"min_equity"`
	Peak           float64   `json:"peak"`
	OpenPositions  int       `json:"open_positions"`
	// StalePositions and StaleValue flag the positions in Equity whose market
	// had not traded for longer than the stale threshold, and their mark.
	StalePositions int     `json:"stale_positions"`
	StaleValue     float64 `json:"stale_value"`
}

// StalePosition is a position whose market stopped trading for longer than
// the stale threshold while it was held: its mark is the last trade, which
// may be far from what it could be sold for.
type StalePosition struct {
	PositionID   string    `json:"position_id"`
	Market       string    `json:"market"`
	FirstFlagged time.Time `json:"first_flagged"` // first tick the position was stale
	LastTrade    time.Time `json:"last_trade"`    // start of the market's last bar when last flagged
	Qty          float64   `json:"qty"`
	MarkedValue  float64   `json:"marked_value"` // qty × last trade price when last flagged
	AtEnd        bool      `json:"at_end"`       // still held, and stale, when the run ended
	// LiquidationValue is, for a position stale at the end, the net proceeds
	// booked by its stale_liquidation (last trade less spread and fee); the
	// worst case values it at zero instead.
	LiquidationValue float64 `json:"liquidation_value"`
}

// RiskFeeAlignment records the FEE_AND_SLIPPAGE the risk engine sized with.
type RiskFeeAlignment struct {
	Explicit           bool    `json:"explicit"`             // set by the settings file (or a variation); left as given
	FeeAndSlippage     float64 `json:"fee_and_slippage"`     // the value used
	ExchangeFeeRate    float64 `json:"exchange_fee_rate"`    // the simulated exchange's taker fee
	ExpectedHalfSpread float64 `json:"expected_half_spread"` // the fill model's expected half-spread
	Warning            string  `json:"warning,omitempty"`
}

// LearnedOutcome is one closed trade as the learner recorded it, in the
// keys live uses (a signal exit is recorded under the closing idea's
// direction). A walk-forward test window is seeded with its train window's.
type LearnedOutcome struct {
	Time       time.Time `json:"time"`
	StrategyID string    `json:"strategy_id"`
	Market     string    `json:"market"`
	Direction  string    `json:"direction"`
	PnL        float64   `json:"pnl"`
	Notional   float64   `json:"notional"`
}

// UniverseSelection is the market list chosen at a point in time.
type UniverseSelection struct {
	Time    time.Time `json:"time"`
	Markets []string  `json:"markets"`
}

// Assumption is one modelling choice that affects the result, with the
// direction it biases the result in (pessimistic, optimistic, neutral or unknown).
type Assumption struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Bias  string `json:"bias"`
	Note  string `json:"note"`
}

// Result is the raw outcome of one replay run. It holds no wall-clock time
// and no random id, so the same inputs give a byte-identical file.
type Result struct {
	SchemaVersion   int       `json:"schema_version"`
	StrategyID      string    `json:"strategy_id"`
	Start           time.Time `json:"start"`
	End             time.Time `json:"end"`
	TickInterval    string    `json:"tick_interval"`
	Ticks           int       `json:"ticks"`          // evaluated ticks
	EvaluatedTicks  int       `json:"strategy_ticks"` // ticks past warm-up where the strategy ran
	Seed            int64     `json:"seed"`           // recorded; nothing in the replay is random
	LearnerEnabled  bool      `json:"learner_enabled"`
	StartingCapital float64   `json:"starting_capital"`
	EndingEquity    float64   `json:"ending_equity"` // after end-of-run valuation (equals cash)
	EndingCash      float64   `json:"ending_cash"`
	FeesPaid        float64   `json:"fees_paid"`

	Markets            []string            `json:"markets"`         // every market evaluated at least once, sorted
	InitialMarkets     []string            `json:"initial_markets"` // explicit list as given, or the first universe selection
	UniverseSelections []UniverseSelection `json:"universe_selections"`
	Settings           map[string]string   `json:"settings"`
	Exchange           ExchangeConfig      `json:"exchange"`
	Assumptions        []Assumption        `json:"assumptions"`
	// DataProvenance cites the cached month files read (pre-roll included)
	// and the sources they were imported from; empty when not recorded.
	DataProvenance []histdata.Provenance `json:"data_provenance"`

	Orders          []OrderLogEntry `json:"orders"`
	Trades          []ClosedTrade   `json:"trades"`
	Decisions       []Decision      `json:"decisions"`
	DecisionCounts  map[string]int  `json:"decision_counts"`
	RejectionCounts map[string]int  `json:"rejection_counts"`
	EquityCurve     []EquityPoint   `json:"equity_curve"`

	// LearnerSeedOutcomes counts the outcomes the learner was seeded with
	// before the first tick (Config.LearnerSeed); LearnedOutcomes are the ones
	// this run recorded, in order.
	LearnerSeedOutcomes int              `json:"learner_seed_outcomes"`
	LearnedOutcomes     []LearnedOutcome `json:"learned_outcomes"`
	// TickStats are measured on every tick, not on the sampled curve.
	TickStats TickStats `json:"tick_stats"`

	// SettingsSource is the settings file the run used, or "built-in defaults".
	SettingsSource   string           `json:"settings_source"`
	RiskFee          RiskFeeAlignment `json:"risk_fee"`
	FillEmptyCandles bool             `json:"fill_empty_candles"`
	StaleAfter       string           `json:"stale_after"`
	// StalePositions lists every position that was stale at some tick.
	StalePositions []StalePosition `json:"stale_positions"`
	// EndingEquityWorstCase is EndingEquity with every position stale at the
	// end valued at zero instead of its last trade.
	EndingEquityWorstCase float64 `json:"ending_equity_worst_case"`
	// DataCheck is the data cache's coverage and findings for the range read
	// (pre-roll included); nil when the run was not given a cache to check.
	DataCheck    *histdata.RangeReport `json:"data_check"`
	DataWarnings []string              `json:"data_warnings"`
}

// WriteResult writes r as indented JSON to path, creating parent directories.
func WriteResult(r *Result, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
