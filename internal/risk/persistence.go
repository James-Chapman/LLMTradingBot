package risk

import "time"

// StateStore persists the daily risk counters (implemented by the storage
// repository). lastResetDate is a UTC calendar date at midnight UTC.
type StateStore interface {
	SaveRiskState(dailyLoss, dailyStartEquity float64, lastResetDate time.Time) error
}

// RecordTradeResultAndPersist updates risk state for a realised trade and saves
// the daily counters so the daily loss limit survives a restart. The engine is
// always updated; the store error (if any) is returned.
func RecordTradeResultAndPersist(engine *Engine, store StateStore, pnl float64) error {
	engine.mu.Lock()
	engine.recordTradeResultLocked(pnl)
	state := State{
		DailyLoss:        engine.dailyLoss,
		DailyStartEquity: engine.dailyStartEquity,
		LastResetDate:    engine.lastResetDate,
	}
	engine.mu.Unlock()
	if store == nil {
		return nil
	}
	return store.SaveRiskState(state.DailyLoss, state.DailyStartEquity, state.LastResetDate)
}
