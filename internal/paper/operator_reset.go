package paper

// OrderPnLStore stamps realised P&L onto a close order (implemented by the
// storage repository).
type OrderPnLStore interface {
	UpdateOrderPnL(orderID string, pnl float64) error
}

// OutcomeLearner learns from realised trade outcomes.
type OutcomeLearner interface {
	RecordOutcome(strategyID, market, direction string, pnl float64)
}

// ClosePositionsForOperatorReset closes every open paper position through the
// normal close-order accounting path and returns how many were closed and
// recorded. Positions without a live price close at their entry price.
// recordTradeResult receives each realised P&L (e.g. the risk engine);
// store and learner may be nil.
func ClosePositionsForOperatorReset(
	engine *Engine,
	prices map[string]float64,
	recordTradeResult func(pnl float64),
	store OrderPnLStore,
	learner OutcomeLearner,
) int {
	closed := 0
	for _, pos := range engine.OpenPositions() {
		exitPrice, ok := prices[pos.Market]
		if !ok {
			exitPrice = pos.AvgPrice
		}
		order, ok := engine.ClosePosition(pos.PositionID, exitPrice, CloseOptions{ApprovalRequestID: "operator_reset"})
		if !ok {
			continue
		}
		pnl, ok := engine.RecordClosedTrade(pos.PositionID, order.Price, "operator_reset", "")
		if !ok {
			continue
		}
		if recordTradeResult != nil {
			recordTradeResult(pnl)
		}
		if store != nil {
			if err := store.UpdateOrderPnL(order.ID, pnl); err != nil {
				logger.Error("Could not stamp order P&L", "order_id", order.ID, "error", err.Error())
			}
		}
		if learner != nil {
			strategyID, direction := "unknown", "long"
			if meta, found := engine.Meta(pos.PositionID); found {
				strategyID, direction = meta.StrategyID, meta.Direction
			}
			learner.RecordOutcome(strategyID, pos.Market, direction, pnl)
		}
		closed++
	}
	return closed
}
