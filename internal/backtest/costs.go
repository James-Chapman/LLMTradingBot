package backtest

import (
	"math/big"
	"sort"
)

// rat returns v as an exact rational; every finite float64 is one.
func rat(v float64) *big.Rat {
	r := new(big.Rat)
	if r.SetFloat64(v) == nil {
		return new(big.Rat) // NaN or Inf: never produced by the engines; the reconciliation flags the totals
	}
	return r
}

// ratFloat returns r rounded to the nearest float64.
func ratFloat(r *big.Rat) float64 {
	f, _ := r.Float64()
	return f
}

// mulAdd adds a×b to sum and returns sum.
func mulAdd(sum *big.Rat, a, b float64) *big.Rat {
	return sum.Add(sum, new(big.Rat).Mul(rat(a), rat(b)))
}

// bridgeRats is the gross-to-net bridge in exact arithmetic on the logged
// float64 values.
type bridgeRats struct {
	gross, execution, drift, spread, impact, rounding, residual, fees, net *big.Rat
}

// exactBridge computes the bridge over a run's filled orders:
//
//	gross     = Σ sells qty×reference - Σ buys qty×reference
//	execution = Σ qty × (fill - reference), signed so a positive value is a cost
//	components: Σ qty × each logged per-unit cost; residual = execution - components
//	net       = gross - execution - fees  (= Σ sells qty×fill - Σ buys qty×fill - fees)
func exactBridge(res *Result) bridgeRats {
	b := bridgeRats{}
	for _, p := range []**big.Rat{&b.gross, &b.execution, &b.drift, &b.spread, &b.impact, &b.rounding, &b.residual, &b.fees, &b.net} {
		*p = new(big.Rat)
	}
	for _, o := range res.Orders {
		if o.Status != StatusFilled {
			continue
		}
		q := rat(o.FilledQty)
		ref, fill := rat(o.ReferencePrice), rat(o.FillPrice)
		slip := new(big.Rat).Sub(fill, ref) // fill - reference
		if o.Side == SideBuy {
			b.gross.Sub(b.gross, new(big.Rat).Mul(q, ref))
		} else {
			b.gross.Add(b.gross, new(big.Rat).Mul(q, ref))
			slip.Neg(slip)
		}
		b.execution.Add(b.execution, new(big.Rat).Mul(q, slip))
		mulAdd(b.drift, o.FilledQty, o.LatencyDrift)
		mulAdd(b.spread, o.FilledQty, o.SpreadCost)
		mulAdd(b.impact, o.FilledQty, o.ImpactCost)
		mulAdd(b.rounding, o.FilledQty, o.RoundingCost)
		b.fees.Add(b.fees, rat(o.Fee))
	}
	b.residual.Sub(b.execution, b.drift)
	b.residual.Sub(b.residual, b.spread).Sub(b.residual, b.impact).Sub(b.residual, b.rounding)
	b.net.Sub(b.gross, b.execution)
	b.net.Sub(b.net, b.fees)
	return b
}

// Bridge explains the net P&L from the gross P&L: gross - latency drift -
// spread - impact - rounding - residual - fees = net (from the order log).
// The sum is exact (rational arithmetic on the logged values); the fields
// are that exact value rounded to float64.
type Bridge struct {
	Definition    string  `json:"definition"`
	GrossPnL      float64 `json:"gross_pnl"`
	LatencyDrift  float64 `json:"latency_drift"`
	SpreadCost    float64 `json:"spread_cost"`
	ImpactCost    float64 `json:"impact_cost"`
	RoundingCost  float64 `json:"rounding_cost"`
	Residual      float64 `json:"residual"` // execution cost not explained by the logged components (0 when they telescope)
	ExecutionCost float64 `json:"execution_cost"`
	Fees          float64 `json:"fees"`
	NetPnL        float64 `json:"net_pnl"`        // from the order log
	EngineNetPnL  float64 `json:"engine_net_pnl"` // ending equity - starting capital, as the engines computed it
	Difference    float64 `json:"difference"`     // engine - order log
}

// bridgeDefinition is written into every bridge.
const bridgeDefinition = "gross P&L values every fill at its reference price (the last price at the decision tick) with no fee: it excludes fees, " +
	"latency drift (price move between decision and fill), spread, size impact and price rounding. Net = gross - those costs - fees. " +
	"Every position is closed by the end of the run, so no unrealised P&L remains."

// ComputeBridge returns a run's gross-to-net bridge.
func ComputeBridge(res *Result) Bridge {
	e := exactBridge(res)
	engine := res.EndingEquity - res.StartingCapital
	net := ratFloat(e.net)
	return Bridge{
		Definition: bridgeDefinition, GrossPnL: ratFloat(e.gross), LatencyDrift: ratFloat(e.drift), SpreadCost: ratFloat(e.spread),
		ImpactCost: ratFloat(e.impact), RoundingCost: ratFloat(e.rounding), Residual: ratFloat(e.residual),
		ExecutionCost: ratFloat(e.execution), Fees: ratFloat(e.fees), NetPnL: net, EngineNetPnL: engine, Difference: engine - net,
	}
}

// CostLine is one cost as an amount and as a share of the traded value and
// of the starting capital, in percent.
type CostLine struct {
	Amount               float64 `json:"amount"`
	PctOfTradedValue     float64 `json:"pct_of_traded_value"`
	PctOfStartingCapital float64 `json:"pct_of_starting_capital"`
}

// CostSet is the cost breakdown of a set of fills. Execution is the sum of
// the four execution components; All adds the fees.
type CostSet struct {
	Fills        int      `json:"fills"`
	TradedValue  float64  `json:"traded_value"`
	Fees         CostLine `json:"fees"`
	Spread       CostLine `json:"spread"`
	Impact       CostLine `json:"impact"`
	LatencyDrift CostLine `json:"latency_drift"`
	Rounding     CostLine `json:"rounding"`
	Execution    CostLine `json:"execution"`
	All          CostLine `json:"all"`
}

// MarketCosts is one market's breakdown.
type MarketCosts struct {
	Market string  `json:"market"`
	Costs  CostSet `json:"costs"`
}

// Costs is the run's cost breakdown, in total and per market (costs.json).
type Costs struct {
	Definition string        `json:"definition"`
	Total      CostSet       `json:"total"`
	ByMarket   []MarketCosts `json:"by_market"`
}

// costsDefinition is written into every cost breakdown.
const costsDefinition = "Each filled order's cost against its reference price, quantity × per-unit cost: latency drift (fill tick close - decision tick close), " +
	"spread (fill quote - fill tick close), size impact and price rounding, signed so a positive amount is a cost; fees as charged. " +
	"Latency drift can be negative (the price moved in the bot's favour)."

// costAcc accumulates one CostSet exactly.
type costAcc struct {
	fills                                        int
	value, fees, spread, impact, drift, rounding *big.Rat
}

// newCostAcc returns an empty accumulator.
func newCostAcc() *costAcc {
	return &costAcc{value: new(big.Rat), fees: new(big.Rat), spread: new(big.Rat), impact: new(big.Rat), drift: new(big.Rat), rounding: new(big.Rat)}
}

// add adds a filled order.
func (a *costAcc) add(o OrderLogEntry) {
	a.fills++
	a.value.Add(a.value, rat(o.Notional))
	a.fees.Add(a.fees, rat(o.Fee))
	mulAdd(a.spread, o.FilledQty, o.SpreadCost)
	mulAdd(a.impact, o.FilledQty, o.ImpactCost)
	mulAdd(a.drift, o.FilledQty, o.LatencyDrift)
	mulAdd(a.rounding, o.FilledQty, o.RoundingCost)
}

// set returns the accumulated breakdown.
func (a *costAcc) set(capital float64) CostSet {
	value := ratFloat(a.value)
	line := func(r *big.Rat) CostLine {
		l := CostLine{Amount: ratFloat(r)}
		if value > 0 {
			l.PctOfTradedValue = 100 * l.Amount / value
		}
		if capital > 0 {
			l.PctOfStartingCapital = 100 * l.Amount / capital
		}
		return l
	}
	exec := new(big.Rat).Add(a.spread, a.impact)
	exec.Add(exec, a.drift).Add(exec, a.rounding)
	all := new(big.Rat).Add(exec, a.fees)
	return CostSet{
		Fills: a.fills, TradedValue: value, Fees: line(a.fees), Spread: line(a.spread), Impact: line(a.impact),
		LatencyDrift: line(a.drift), Rounding: line(a.rounding), Execution: line(exec), All: line(all),
	}
}

// ComputeCosts returns a run's cost breakdown, markets sorted.
func ComputeCosts(res *Result) Costs {
	total := newCostAcc()
	byMarket := map[string]*costAcc{}
	for _, o := range res.Orders {
		if o.Status != StatusFilled {
			continue
		}
		total.add(o)
		if byMarket[o.Market] == nil {
			byMarket[o.Market] = newCostAcc()
		}
		byMarket[o.Market].add(o)
	}
	markets := make([]string, 0, len(byMarket))
	for m := range byMarket {
		markets = append(markets, m)
	}
	sort.Strings(markets)
	c := Costs{Definition: costsDefinition, Total: total.set(res.StartingCapital), ByMarket: []MarketCosts{}}
	for _, m := range markets {
		c.ByMarket = append(c.ByMarket, MarketCosts{Market: m, Costs: byMarket[m].set(res.StartingCapital)})
	}
	return c
}
