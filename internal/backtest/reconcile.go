package backtest

import (
	"fmt"
	"math"
	"math/big"
	"sort"
)

// Reconciliation tolerances (docs/14): float64 has about 16 significant
// digits, so a run's accumulated rounding is far below these.
const (
	reconcileRelTolerance = 1e-10 // of starting capital + traded value + fees
	reconcileAbsTolerance = 1e-9  // floor, in the quote currency
	exactDecimals         = 12    // digits after the point in the exact totals
)

// Reconciliation recomputes a run's money from its order log with exact
// rational arithmetic (each logged float64 is an exact rational) and compares
// it with the float64 totals the engines produced.
type Reconciliation struct {
	Method        string  `json:"method"`
	Tolerance     float64 `json:"tolerance"`
	ToleranceRule string  `json:"tolerance_rule"`
	Fills         int     `json:"fills"`

	ExactFinalCash  string  `json:"exact_final_cash"`
	EngineFinalCash float64 `json:"engine_final_cash"`
	CashDifference  float64 `json:"cash_difference"` // absolute

	ExactMarkedPositions string  `json:"exact_marked_positions"`
	ExactFinalEquity     string  `json:"exact_final_equity"`
	EngineFinalEquity    float64 `json:"engine_final_equity"`
	EquityDifference     float64 `json:"equity_difference"`

	ExactFees      string  `json:"exact_fees"` // Σ quantity × fill price × fee rate
	EngineFees     float64 `json:"engine_fees"`
	FeesDifference float64 `json:"fees_difference"`
	// MaxFeeDifference is the largest gap between a logged fee and its exact
	// formula value.
	MaxFeeDifference float64 `json:"max_fee_difference"`

	ExactNetPnL      string  `json:"exact_net_pnl"`
	TradesNetPnL     float64 `json:"trades_net_pnl"` // Σ round-trip P&L from the paper engine
	TradesDifference float64 `json:"trades_difference"`

	OpenQuantity string `json:"open_quantity"` // Σ |net filled quantity| over positions; 0 when all closed

	Warning  bool     `json:"warning"`
	Problems []string `json:"problems"`
}

// Reconcile checks, exactly: starting capital - Σ buys - Σ fees + Σ sells =
// final cash, and final cash + Σ positions marked at their last fill-tick
// close = final equity, plus the fee totals, each logged fee and the sum of
// round-trip P&L. A difference above the tolerance is an accounting warning.
func Reconcile(res *Result) Reconciliation {
	cash := rat(res.StartingCapital)
	fees := new(big.Rat)
	volume := 0.0
	maxFeeDiff := 0.0
	open := map[string]*big.Rat{}     // position -> net filled quantity
	posMarket := map[string]string{}  // position -> market
	lastClose := map[string]float64{} // market -> last fill tick close
	rec := Reconciliation{
		Method: "math/big.Rat on the exact values of the logged float64 quantities, prices and fee rates",
		ToleranceRule: fmt.Sprintf("max(%g, %g × (starting capital + traded value + fees)) in the quote currency",
			reconcileAbsTolerance, reconcileRelTolerance),
	}
	for _, o := range res.Orders {
		if o.Status != StatusFilled {
			continue
		}
		rec.Fills++
		value := new(big.Rat).Mul(rat(o.FilledQty), rat(o.FillPrice))
		fee := new(big.Rat).Mul(value, rat(o.FeeRate))
		fees.Add(fees, fee)
		maxFeeDiff = math.Max(maxFeeDiff, math.Abs(ratFloat(new(big.Rat).Sub(rat(o.Fee), fee))))
		volume += math.Abs(o.FilledQty*o.FillPrice) + math.Abs(o.Fee)
		q := open[o.PositionID]
		if q == nil {
			q = new(big.Rat)
			open[o.PositionID] = q
		}
		posMarket[o.PositionID] = o.Market
		lastClose[o.Market] = o.FillBarClose
		if o.Side == SideBuy {
			cash.Sub(cash, value).Sub(cash, fee)
			q.Add(q, rat(o.FilledQty))
		} else {
			cash.Add(cash, value).Sub(cash, fee)
			q.Sub(q, rat(o.FilledQty))
		}
	}
	rec.Tolerance = math.Max(reconcileAbsTolerance, reconcileRelTolerance*(math.Abs(res.StartingCapital)+volume))

	ids := make([]string, 0, len(open))
	for id := range open {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	marked, openQty := new(big.Rat), new(big.Rat)
	for _, id := range ids {
		q := open[id]
		marked.Add(marked, new(big.Rat).Mul(q, rat(lastClose[posMarket[id]])))
		openQty.Add(openQty, new(big.Rat).Abs(q))
	}
	equity := new(big.Rat).Add(cash, marked)
	net := new(big.Rat).Sub(equity, rat(res.StartingCapital))
	tradesNet := 0.0
	for _, t := range res.Trades {
		tradesNet += t.PnL
	}

	rec.ExactFinalCash, rec.EngineFinalCash = cash.FloatString(exactDecimals), res.EndingCash
	rec.CashDifference = absDiff(cash, res.EndingCash)
	rec.ExactMarkedPositions = marked.FloatString(exactDecimals)
	rec.ExactFinalEquity, rec.EngineFinalEquity = equity.FloatString(exactDecimals), res.EndingEquity
	rec.EquityDifference = absDiff(equity, res.EndingEquity)
	rec.ExactFees, rec.EngineFees = fees.FloatString(exactDecimals), res.FeesPaid
	rec.FeesDifference = absDiff(fees, res.FeesPaid)
	rec.MaxFeeDifference = maxFeeDiff
	rec.ExactNetPnL, rec.TradesNetPnL = net.FloatString(exactDecimals), tradesNet
	rec.TradesDifference = absDiff(net, tradesNet)
	rec.OpenQuantity = openQty.FloatString(exactDecimals)
	if openQty.Sign() == 0 {
		rec.OpenQuantity = "0"
	}

	rec.Problems = []string{}
	check := func(name string, diff float64) {
		if !(diff <= rec.Tolerance) { // also catches NaN
			rec.Problems = append(rec.Problems, fmt.Sprintf("%s differs by %.12g (tolerance %.3g)", name, diff, rec.Tolerance))
		}
	}
	check("final cash (order log vs engine)", rec.CashDifference)
	check("final equity (order log vs engine)", rec.EquityDifference)
	check("total fees (formula vs engine)", rec.FeesDifference)
	check("a logged fee (vs quantity × price × rate)", rec.MaxFeeDifference)
	check("net P&L (order log vs Σ round trips)", rec.TradesDifference)
	if openQty.Sign() != 0 {
		rec.Problems = append(rec.Problems, "positions remain open in the order log after the end-of-run valuation: quantity "+rec.OpenQuantity)
	}
	rec.Warning = len(rec.Problems) > 0
	return rec
}

// absDiff returns |exact - v| as a float64.
func absDiff(exact *big.Rat, v float64) float64 {
	return math.Abs(ratFloat(new(big.Rat).Sub(exact, rat(v))))
}
