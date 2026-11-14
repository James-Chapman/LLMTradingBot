// Package calc holds the shared strategy decision formulas: momentum, hard
// filters, indicator votes, confidence and position sizing.
//
// Raw technical indicators live in package analysis; this package turns their
// values into strategy gates, votes, confidence and sizing.
package calc

// Shared indicator thresholds used by every indicator-based strategy, so a
// threshold change applies to all strategies at once.
const (
	MomentumThreshold         = 0.002
	RSILongBlock              = 80.0
	RSIShortBlock             = 20.0
	BBLongBlock               = 95.0
	BBShortBlock              = 5.0
	RSIBullishThreshold       = 40.0
	RSIBearishThreshold       = 60.0
	BBBullishThreshold        = 30.0
	BBBearishThreshold        = 70.0
	StochBullishThreshold     = 20.0
	StochBearishThreshold     = 80.0
	WilliamsBullishThreshold  = -80.0
	WilliamsBearishThreshold  = -20.0
	MinIndicatorsForSignal    = 5
	MinConsensusVotes         = 1
	BaseConfidenceCap         = 0.50
	MomentumATRMultiplier     = 1.0
	IndicatorVoteWeight       = 0.05
	MinIndicatorBonus         = -0.20
	MaxIndicatorBonus         = 0.40
	ATRVolatilityThresholdPct = 1.0
	ATRPenaltyPerExtraPct     = 0.05
	MaxATRPenalty             = 0.10
	MinConfidence             = 0.10
	MaxConfidence             = 0.95
	MinActionableConfidence   = 0.20
	PositionSizingProposal    = 0.20
	MinDynamicPositionSize    = 0.05
	MaxDynamicPositionSize    = 0.25
)

// MomentumWindowPoints is the number of prices in each averaged momentum window.
const MomentumWindowPoints = 5
