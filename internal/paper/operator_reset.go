package paper

// OrderPnLStore stamps realised P&L onto a close order (implemented by the
// storage repository).
type OrderPnLStore interface {
	UpdateOrderPnL(orderID string, pnl float64) error
}

// OutcomeLearner learns from realised trade outcomes; notional is the trade's
// entry price × size.
type OutcomeLearner interface {
	RecordOutcomeWithNotional(strategyID, market, direction string, pnl, notional float64)
}

// ClosePositionsForOperatorReset closes every open paper position through the
// normal close-order accounting path and returns how many were closed and
// recorded. Positions without a live price close at their entry price. Each
// close deletes its own database row, so a position opened while the reset
// runs is left open rather than wiped.
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
			strategyID, direction, notional := "unknown", directionOrFromSize("", pos.Size), 0.0
			if meta, found := engine.Meta(pos.PositionID); found {
				strategyID, direction = meta.StrategyID, meta.Direction
				notional = meta.AvgPriceAtClose * meta.SizeAtClose
			}
			learner.RecordOutcomeWithNotional(strategyID, pos.Market, direction, pnl, notional)
		}
		closed++
	}
	return closed
}
