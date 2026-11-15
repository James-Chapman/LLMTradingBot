package backtest

import (
	"math"
	"math/big"
	"strings"
	"testing"
	"time"
)

// filled returns a filled order of qty at fill with the given per-unit cost
// components (positive = cost) and an 80 bp fee.
func filled(id, side string, at time.Time, qty, ref, fill, drift, spread, impact float64) OrderLogEntry {
	return OrderLogEntry{
		ID: id, Market: "BTC/EUR", Side: side, Reason: ReasonSignal, PositionID: "pos-" + id,
		DecisionTime: at.Add(-30 * time.Second), FillTime: at, RequestedQty: qty, FilledQty: qty,
		ReferencePrice: ref, FillBarClose: ref, FillPrice: fill,
		LatencyDrift: drift, SpreadCost: spread, ImpactCost: impact,
		Notional: qty * fill, Fee: qty * fill * 0.008, FeeRate: 0.008, Status: StatusFilled,
	}
}

// handResult returns a hand-computed run: capital 1000, two round trips on
// BTC/EUR (+7.4192 and -11.92 net), one rejected order.
//
//	buy  1 @ ref 100 fill 100.5 (drift 0.2, spread 0.25, impact 0.05) fee 0.804
//	sell 1 @ ref 110 fill 109.6 (drift 0.1, spread 0.3)               fee 0.8768
//	buy  2 @ ref  50 fill  50.1 (spread 0.1)                           fee 0.8016
//	sell 2 @ ref  45 fill  44.9 (spread 0.1)                           fee 0.7184
//
// gross = 1×(110-100) + 2×(45-50) = 0; execution cost 1.3; fees 3.2008;
// net = -4.5008; ending cash 995.4992.
func handResult() *Result {
	o := []OrderLogEntry{
		filled("ord-000001", SideBuy, t0.Add(time.Hour), 1, 100, 100.5, 0.2, 0.25, 0.05),
		filled("ord-000002", SideSell, t0.Add(3*time.Hour), 1, 110, 109.6, 0.1, 0.3, 0),
		filled("ord-000003", SideBuy, t0.Add(5*time.Hour), 2, 50, 50.1, 0, 0.1, 0),
		filled("ord-000004", SideSell, t0.Add(6*time.Hour), 2, 45, 44.9, 0, 0.1, 0),
		{ID: "ord-000005", Market: "BTC/EUR", Side: SideBuy, Reason: ReasonSignal, Status: StatusRejected, RejectReason: RejectFunds,
			DecisionTime: t0.Add(7 * time.Hour), FillTime: t0.Add(7*time.Hour + 30*time.Second), RequestedQty: 100, ReferencePrice: 45},
	}
	o[1].PositionID, o[3].PositionID = o[0].PositionID, o[2].PositionID
	pnl1 := 109.6 - 100.5 - o[0].Fee - o[1].Fee
	pnl2 := 2*44.9 - 2*50.1 - o[2].Fee - o[3].Fee
	o[1].RealisedPnL, o[3].RealisedPnL = &pnl1, &pnl2
	cash := 1000.0
	for _, x := range o[:4] {
		if x.Side == SideBuy {
			cash -= x.Notional + x.Fee
		} else {
			cash += x.Notional - x.Fee
		}
	}
	return &Result{
		SchemaVersion: ResultSchemaVersion, StrategyID: "scripted", Start: t0, End: t0.Add(24 * time.Hour),
		StartingCapital: 1000, EndingCash: cash, EndingEquity: cash, FeesPaid: o[0].Fee + o[1].Fee + o[2].Fee + o[3].Fee,
		Markets: []string{"BTC/EUR"}, InitialMarkets: []string{"BTC/EUR"}, Exchange: DefaultExchange(),
		Orders: o,
		Trades: []ClosedTrade{
			{PositionID: o[0].PositionID, Market: "BTC/EUR", EntryOrder: o[0].ID, ExitOrder: o[1].ID, EntryTime: o[0].FillTime, ExitTime: o[1].FillTime,
				Qty: 1, EntryPrice: 100.5, ExitPrice: 109.6, EntryFee: o[0].Fee, ExitFee: o[1].Fee, PnL: pnl1, ExitReason: ReasonSignal},
			{PositionID: o[2].PositionID, Market: "BTC/EUR", EntryOrder: o[2].ID, ExitOrder: o[3].ID, EntryTime: o[2].FillTime, ExitTime: o[3].FillTime,
				Qty: 2, EntryPrice: 50.1, ExitPrice: 44.9, EntryFee: o[2].Fee, ExitFee: o[3].Fee, PnL: pnl2, ExitReason: ReasonStopLoss},
		},
		Decisions: []Decision{
			{IdeaID: "idea-000001", Outcome: OutcomeSubmitted}, {IdeaID: "idea-000002", Outcome: OutcomeSubmitted},
			{IdeaID: "idea-000003", Outcome: OutcomeLowConfidence}, {IdeaID: "idea-000004", Outcome: OutcomeRiskRejected, RiskReason: "Daily loss limit reached: 12 > 10"},
			{IdeaID: "idea-000005", Outcome: OutcomeNoHolding},
		},
		DecisionCounts:  map[string]int{OutcomeSubmitted: 2, OutcomeLowConfidence: 1, OutcomeRiskRejected: 1, OutcomeNoHolding: 1},
		RejectionCounts: map[string]int{RejectFunds: 1},
		TickStats: TickStats{Ticks: 4, TicksInMarket: 1, AverageEquity: 1000, AverageUtilisation: 0.25, MaxUtilisation: 0.5,
			Daily: []DailyPoint{{Time: t0.Add(24 * time.Hour), Value: cash}}},
	}
}

// withPnLs replaces res's trades by trades with the given net P&Ls.
func withPnLs(res *Result, pnls ...float64) *Result {
	res.Trades = nil
	for _, p := range pnls {
		res.Trades = append(res.Trades, ClosedTrade{Market: "BTC/EUR", PnL: p, ExitReason: ReasonSignal})
	}
	return res
}

// TestMetrics covers each metric's definition on hand-computed inputs.
func TestMetrics(t *testing.T) {
	t.Run("GIVEN a hand-computed order log WHEN metrics are computed THEN capital, P&L, fees and costs match the hand figures", func(t *testing.T) {
		m := ComputeMetrics(handResult())
		checks := []struct {
			name      string
			got, want float64
		}{
			{"initial", m.InitialCapital, 1000}, {"final", m.FinalValue, 995.4992}, {"net", m.NetPnL, -4.5008},
			{"net %", m.NetReturnPct, -0.45008}, {"gross", m.GrossPnL, 0}, {"fees", m.TotalFees, 3.2008},
			{"execution", m.TotalExecutionCost, 1.3}, {"traded value", m.TradedValue, 100.5 + 109.6 + 100.2 + 89.8},
			{"exposure", m.ExposurePct, 25}, {"avg utilisation", m.AvgUtilisationPct, 25}, {"max utilisation", m.MaxUtilisationPct, 50},
		}
		for _, c := range checks {
			if !near(c.got, c.want, 1e-9) {
				t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
			}
		}
		if m.Turnover == nil || !near(*m.Turnover, 400.1/1000, 1e-12) {
			t.Errorf("turnover %v, want traded value / average equity", m.Turnover)
		}
		if m.RoundTrips != 2 || m.Wins != 1 || m.Losses != 1 || m.Breakeven != 0 {
			t.Errorf("counts %+v", m)
		}
	})

	t.Run("GIVEN wins, losses and a breakeven trade WHEN metrics are computed THEN the trade statistics are exact", func(t *testing.T) {
		m := ComputeMetrics(withPnLs(handResult(), 10, -5, 0.001, 20, -15))
		if m.Wins != 2 || m.Losses != 2 || m.Breakeven != 1 {
			t.Fatalf("counts w%d l%d b%d", m.Wins, m.Losses, m.Breakeven)
		}
		for name, c := range map[string][2]any{
			"win rate": {m.WinRatePct, 40.0}, "average": {m.AvgNetPnL, 10.001 / 5}, "median": {m.MedianNetPnL, 0.001},
			"average win": {m.AvgWin, 15.0}, "average loss": {m.AvgLoss, -10.0}, "payoff": {m.PayoffRatio, 1.5}, "profit factor": {m.ProfitFactor, 1.5},
		} {
			got, want := c[0].(*float64), c[1].(float64)
			if got == nil || !near(*got, want, 1e-12) {
				t.Errorf("%s = %v, want %v", name, got, want)
			}
		}
	})

	t.Run("GIVEN no losing trade WHEN metrics are computed THEN the profit factor and payoff are null with a note, never a currency amount", func(t *testing.T) {
		m := ComputeMetrics(withPnLs(handResult(), 10, 20))
		if m.ProfitFactor != nil || m.PayoffRatio != nil || !strings.Contains(m.ProfitFactorNote, "no losing") {
			t.Fatalf("profit factor %v payoff %v note %q", m.ProfitFactor, m.PayoffRatio, m.ProfitFactorNote)
		}
	})

	t.Run("GIVEN no trades WHEN metrics are computed THEN per-trade statistics are null", func(t *testing.T) {
		m := ComputeMetrics(withPnLs(handResult()))
		if m.WinRatePct != nil || m.AvgNetPnL != nil || m.MedianNetPnL != nil || m.ProfitFactor != nil {
			t.Fatalf("metrics %+v", m)
		}
	})

	t.Run("GIVEN a period under 30 days WHEN metrics are computed THEN no annualised return is reported", func(t *testing.T) {
		m := ComputeMetrics(handResult())
		if m.AnnualisedReturnPct != nil || !strings.Contains(m.AnnualisedNote, "30 days") {
			t.Fatalf("annualised %v note %q", m.AnnualisedReturnPct, m.AnnualisedNote)
		}
	})

	t.Run("GIVEN a 365-day period WHEN metrics are computed THEN the annualised return compounds the net return", func(t *testing.T) {
		res := handResult()
		res.End = res.Start.Add(365 * 24 * time.Hour)
		res.EndingEquity = 1100
		m := ComputeMetrics(res)
		if m.AnnualisedReturnPct == nil || !near(*m.AnnualisedReturnPct, 10, 1e-9) {
			t.Fatalf("annualised %v", m.AnnualisedReturnPct)
		}
	})

	t.Run("GIVEN a drawdown that never recovers WHEN metrics are computed THEN it is reported without a recovery time", func(t *testing.T) {
		res := handResult()
		tr := newTracker(res.Start, 1000)
		tr.mark(res.Start.Add(time.Hour), 1050)
		tr.final(res.End, 900)
		res.TickStats = tr.stats()
		m := ComputeMetrics(res)
		if m.MaxDrawdown == nil || !near(m.MaxDrawdown.Pct, 100*150.0/1050, 1e-12) || m.MaxDrawdown.RecoveryTime != nil {
			t.Fatalf("drawdown %+v", m.MaxDrawdown)
		}
		if m.LongestDrawdownRecovered || m.LongestDrawdownSeconds != int64(23*3600) {
			t.Fatalf("longest %d recovered %v", m.LongestDrawdownSeconds, m.LongestDrawdownRecovered)
		}
	})
}

// dailySeries returns n daily values starting from capital with returns
// alternating a, b.
func dailySeries(start time.Time, capital float64, n int, a, b float64) []DailyPoint {
	out := make([]DailyPoint, n)
	v := capital
	for i := range out {
		r := a
		if i%2 == 1 {
			r = b
		}
		v *= 1 + r
		out[i] = DailyPoint{Time: start.Add(time.Duration(i+1) * 24 * time.Hour), Value: v}
	}
	return out
}

// TestRiskAdjustedRatios covers when Sharpe and Sortino are reported.
func TestRiskAdjustedRatios(t *testing.T) {
	const a, b = 0.01, -0.005
	ratioRun := func(days, trades int) *Result {
		res := handResult()
		res.End = res.Start.Add(time.Duration(days) * 24 * time.Hour)
		res.TickStats.Daily = dailySeries(res.Start, 1000, days, a, b)
		res.EndingEquity = res.TickStats.Daily[days-1].Value
		pnls := make([]float64, trades)
		for i := range pnls {
			pnls[i] = float64(i%3) - 1
		}
		return withPnLs(res, pnls...)
	}

	t.Run("GIVEN 60 daily returns and 30 round trips WHEN metrics are computed THEN Sharpe and Sortino follow the closed forms", func(t *testing.T) {
		const n = 60
		m := ComputeMetrics(ratioRun(n, 30))
		mean := (a + b) / 2
		sd := math.Abs(a-b) / 2 * math.Sqrt(float64(n)/float64(n-1)) // sample standard deviation
		down := math.Abs(b) / math.Sqrt2                             // sqrt(mean of min(r,0)^2)
		if m.DailyReturns != n || m.Sharpe == nil || !near(*m.Sharpe, mean/sd*math.Sqrt(365), 1e-6) {
			t.Fatalf("sharpe %v (n %d), want %v", m.Sharpe, m.DailyReturns, mean/sd*math.Sqrt(365))
		}
		if m.Sortino == nil || !near(*m.Sortino, mean/down*math.Sqrt(365), 1e-6) {
			t.Fatalf("sortino %v, want %v", m.Sortino, mean/down*math.Sqrt(365))
		}
	})

	for name, c := range map[string][2]int{"59 daily returns": {59, 30}, "29 round trips": {60, 29}} {
		t.Run("GIVEN "+name+" WHEN metrics are computed THEN Sharpe and Sortino are null with the reason", func(t *testing.T) {
			m := ComputeMetrics(ratioRun(c[0], c[1]))
			if m.Sharpe != nil || m.Sortino != nil || !strings.Contains(m.RatioNote, "at least 60") {
				t.Fatalf("sharpe %v sortino %v note %q", m.Sharpe, m.Sortino, m.RatioNote)
			}
		})
	}
}

// TestSignalStats covers counting signals and why they were not traded.
func TestSignalStats(t *testing.T) {
	t.Run("GIVEN decisions with several outcomes WHEN counted THEN generated, submitted and each reason are reported", func(t *testing.T) {
		s := ComputeSignals(handResult())
		if s.Ideas != 5 || s.Submitted != 2 || s.NotTraded[OutcomeLowConfidence] != 1 || s.NotTraded[OutcomeNoHolding] != 1 ||
			s.RiskReasons["Daily loss limit reached"] != 1 || s.OrderRejections[RejectFunds] != 1 {
			t.Fatalf("signals %+v", s)
		}
	})
}

// TestBridgeAndCosts covers the gross-to-net bridge and the cost breakdown.
func TestBridgeAndCosts(t *testing.T) {
	t.Run("GIVEN a hand-computed order log WHEN bridged THEN gross minus each cost and the fees equals the net exactly", func(t *testing.T) {
		res := handResult()
		e := exactBridge(res)
		sum := new(big.Rat).Set(e.gross)
		for _, c := range []*big.Rat{e.drift, e.spread, e.impact, e.rounding, e.residual, e.fees} {
			sum.Sub(sum, c)
		}
		if sum.Cmp(e.net) != 0 {
			t.Fatalf("bridge %s != net %s", sum.FloatString(18), e.net.FloatString(18))
		}
		b := ComputeBridge(res)
		if !near(b.GrossPnL, 0, 1e-12) || !near(b.Fees, 3.2008, 1e-12) || !near(b.SpreadCost, 0.95, 1e-12) ||
			!near(b.LatencyDrift, 0.3, 1e-12) || !near(b.ImpactCost, 0.05, 1e-12) || !near(b.NetPnL, -4.5008, 1e-9) {
			t.Fatalf("bridge %+v", b)
		}
		if math.Abs(b.Residual) > 1e-12 {
			t.Fatalf("residual %v", b.Residual)
		}
	})

	t.Run("GIVEN a real replay WHEN bridged THEN the bridge sums exactly, the breakdown has no residual and the components sum to the execution cost", func(t *testing.T) {
		cfg := testConfig([]string{"BTC/EUR", "ETH/EUR"}, 899)
		cfg.Settings.StartingCapital = 5000
		res := mustRun(t, twoMarkets(900), withScript(cfg, randomPlan(5, cfg.Markets, 36, 890, 12), nil))
		e := exactBridge(res)
		if e.residual.Sign() != 0 {
			t.Fatalf("per-order cost components do not telescope to fill - reference: residual %s", e.residual.FloatString(20))
		}
		sum := new(big.Rat).Sub(e.gross, e.execution)
		sum.Sub(sum, e.fees)
		if sum.Cmp(e.net) != 0 {
			t.Fatalf("gross - execution - fees %s != net %s", sum.FloatString(18), e.net.FloatString(18))
		}
		parts := new(big.Rat).Add(e.drift, e.spread)
		parts.Add(parts, e.impact).Add(parts, e.rounding)
		if parts.Cmp(e.execution) != 0 {
			t.Fatal("cost components do not sum to the execution cost")
		}
		c := ComputeCosts(res)
		total := c.Total
		if !near(total.LatencyDrift.Amount+total.Spread.Amount+total.Impact.Amount+total.Rounding.Amount, total.Execution.Amount, 1e-9) ||
			!near(total.Execution.Amount+total.Fees.Amount, total.All.Amount, 1e-9) {
			t.Fatalf("cost totals %+v", total)
		}
		perMarket := 0.0
		for _, m := range c.ByMarket {
			perMarket += m.Costs.All.Amount
		}
		if len(c.ByMarket) != 2 || !near(perMarket, total.All.Amount, 1e-9) {
			t.Fatalf("per-market costs %v do not add to the total %v", perMarket, total.All.Amount)
		}
		if !near(total.Fees.PctOfStartingCapital, 100*total.Fees.Amount/5000, 1e-12) ||
			!near(total.Fees.PctOfTradedValue, 100*total.Fees.Amount/c.Total.TradedValue, 1e-12) {
			t.Fatalf("fee percentages %+v", total.Fees)
		}
	})
}

// TestReconciliation covers the exact decimal check of the engine's money.
func TestReconciliation(t *testing.T) {
	replay := func(t *testing.T) *Result {
		cfg := testConfig([]string{"BTC/EUR", "ETH/EUR"}, 899)
		cfg.Settings.StartingCapital = 5000
		return mustRun(t, twoMarkets(900), withScript(cfg, randomPlan(5, cfg.Markets, 36, 890, 12), nil))
	}

	t.Run("GIVEN a real replay WHEN reconciled exactly THEN cash, equity, fees and trade P&L agree within the tolerance", func(t *testing.T) {
		rec := Reconcile(replay(t))
		if rec.Warning || len(rec.Problems) != 0 {
			t.Fatalf("reconciliation warned: %+v", rec)
		}
		if rec.Fills == 0 || rec.CashDifference > rec.Tolerance || rec.EquityDifference > rec.Tolerance || rec.OpenQuantity != "0" {
			t.Fatalf("reconciliation %+v", rec)
		}
		if !strings.Contains(rec.ExactFinalCash, ".") || rec.ExactFinalCash == "" {
			t.Fatalf("exact total %q", rec.ExactFinalCash)
		}
	})

	corruptions := map[string]func(*Result){
		"a fill price moved by 0.01 %": func(r *Result) {
			for i := range r.Orders {
				if r.Orders[i].Status == StatusFilled && r.Orders[i].Side == SideSell {
					r.Orders[i].FillPrice *= 1.0001
					return
				}
			}
		},
		"a fee changed by one cent": func(r *Result) {
			for i := range r.Orders {
				if r.Orders[i].Status == StatusFilled {
					r.Orders[i].Fee += 0.01
					return
				}
			}
		},
		"a sell dropped from the log": func(r *Result) {
			for i := range r.Orders {
				if r.Orders[i].Status == StatusFilled && r.Orders[i].Side == SideSell {
					r.Orders = append(r.Orders[:i], r.Orders[i+1:]...)
					return
				}
			}
		},
	}
	for name, corrupt := range corruptions {
		t.Run("GIVEN "+name+" WHEN reconciled THEN an accounting warning is raised", func(t *testing.T) {
			res := replay(t)
			corrupt(res)
			if rec := Reconcile(res); !rec.Warning || len(rec.Problems) == 0 {
				t.Fatalf("corruption not detected: %+v", rec)
			}
		})
	}

	t.Run("GIVEN the hand-computed log WHEN reconciled THEN it passes", func(t *testing.T) {
		if rec := Reconcile(handResult()); rec.Warning {
			t.Fatalf("%+v", rec)
		}
	})
}
