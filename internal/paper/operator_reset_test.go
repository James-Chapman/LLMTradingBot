package paper

import (
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

// learnedOutcome is one captured learner call.
type learnedOutcome struct {
	strategyID, market, direction string
	pnl                           float64
}

// capturingLearner records learner outcomes.
type capturingLearner struct{ outcomes []learnedOutcome }

// RecordOutcome captures an outcome.
func (c *capturingLearner) RecordOutcome(strategyID, market, direction string, pnl float64) {
	c.outcomes = append(c.outcomes, learnedOutcome{strategyID, market, direction, pnl})
}

// TestClosePositionsForOperatorReset covers closing every position on operator reset.
func TestClosePositionsForOperatorReset(t *testing.T) {
	t.Run("GIVEN an open position WHEN operator reset runs THEN close order, P&L and outcome are persisted", func(t *testing.T) {
		repo := &capturingRepo{}
		learner := &capturingLearner{}
		var riskPnLs []float64
		engine := NewEngine(500, repo, nil)
		_, positionID := engine.Execute(intent("BTC/EUR", domain.Long, 1), 100, ExecuteOptions{StrategyID: "combined"})

		closed := ClosePositionsForOperatorReset(engine, map[string]float64{"BTC/EUR": 110},
			func(pnl float64) { riskPnLs = append(riskPnLs, pnl) }, repo, learner)

		closeOrder := repo.orders[len(repo.orders)-1]
		outcomePnL := repo.outcomes[len(repo.outcomes)-1].PnL
		if closed != 1 || closeOrder.approvalID != "operator_reset" {
			t.Fatalf("closed %d, source %q", closed, closeOrder.approvalID)
		}
		if repo.deleted[len(repo.deleted)-1] != positionID || len(repo.outcomes) != 1 {
			t.Fatal("position must be deleted and one outcome saved")
		}
		if !near(repo.orderPnLs[closeOrder.order.ID], outcomePnL) {
			t.Fatalf("order P&L %v, outcome P&L %v", repo.orderPnLs[closeOrder.order.ID], outcomePnL)
		}
		if len(riskPnLs) != 1 || !near(riskPnLs[0], outcomePnL) {
			t.Fatalf("risk results %v", riskPnLs)
		}
		if learner.outcomes[0].strategyID != "combined" || len(engine.OpenPositions()) != 0 {
			t.Fatalf("learner %+v, open %d", learner.outcomes, len(engine.OpenPositions()))
		}
	})

	t.Run("GIVEN no live price for a market WHEN operator reset runs THEN the entry price is used", func(t *testing.T) {
		repo := &capturingRepo{}
		engine := NewEngine(500, repo, nil)
		engine.Execute(intent("ETH/EUR", domain.Long, 1), 100, ExecuteOptions{})

		closed := ClosePositionsForOperatorReset(engine, map[string]float64{}, func(float64) {}, nil, nil)

		if closed != 1 || len(engine.OpenPositions()) != 0 {
			t.Fatalf("closed %d", closed)
		}
	})
}
