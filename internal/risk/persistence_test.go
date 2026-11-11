package risk

import (
	"errors"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
)

// capturingStateStore records the last saved risk state.
type capturingStateStore struct {
	saved []State
	err   error
}

// SaveRiskState captures the state passed by the engine.
func (c *capturingStateStore) SaveRiskState(dailyLoss, dailyStartEquity float64, lastResetDate time.Time) error {
	if c.err != nil {
		return c.err
	}
	c.saved = append(c.saved, State{DailyLoss: dailyLoss, DailyStartEquity: dailyStartEquity, LastResetDate: lastResetDate})
	return nil
}

// TestRecordTradeResultAndPersist covers persisting risk state after each trade.
func TestRecordTradeResultAndPersist(t *testing.T) {
	t.Run("GIVEN a losing realised trade WHEN the result is recorded THEN the daily risk state is persisted", func(t *testing.T) {
		engine := newTestEngine(nil)
		store := &capturingStateStore{}

		err := RecordTradeResultAndPersist(engine, store, -12.5)

		if err != nil {
			t.Fatalf("unexpected error %v", err)
		}
		if len(store.saved) != 1 {
			t.Fatalf("expected one save, got %d", len(store.saved))
		}
		saved := store.saved[0]
		if !almostEqual(saved.DailyLoss, 12.5) || !almostEqual(saved.DailyStartEquity, engine.Snapshot().DailyStartEquity) {
			t.Fatalf("unexpected persisted state %+v", saved)
		}
		if !saved.LastResetDate.Equal(engine.Snapshot().LastResetDate) {
			t.Fatalf("persisted reset date %v does not match engine", saved.LastResetDate)
		}
	})

	t.Run("GIVEN consecutive losses WHEN each is persisted THEN the latest save holds the cumulative loss", func(t *testing.T) {
		engine := newTestEngine(nil)
		store := &capturingStateStore{}

		_ = RecordTradeResultAndPersist(engine, store, -10)
		_ = RecordTradeResultAndPersist(engine, store, -15)

		if !almostEqual(store.saved[len(store.saved)-1].DailyLoss, 25) {
			t.Fatalf("latest saved loss %v, want 25", store.saved[len(store.saved)-1].DailyLoss)
		}
	})

	t.Run("GIVEN a persisted state WHEN a new engine restores it on the same day THEN the loss survives the restart", func(t *testing.T) {
		now, _ := fixedClock(time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC))
		first := NewEngine(config.Defaults(), now)
		store := &capturingStateStore{}
		_ = RecordTradeResultAndPersist(first, store, -30)

		restarted := NewEngine(config.Defaults(), now)
		restarted.RestoreState(store.saved[0])

		if !almostEqual(restarted.Snapshot().DailyLoss, 30) {
			t.Fatalf("restored daily loss %v, want 30", restarted.Snapshot().DailyLoss)
		}
	})

	t.Run("GIVEN a failing store WHEN the result is recorded THEN the engine is still updated and the error returned", func(t *testing.T) {
		engine := newTestEngine(nil)
		store := &capturingStateStore{err: errors.New("db down")}

		err := RecordTradeResultAndPersist(engine, store, -5)

		if err == nil {
			t.Fatal("expected the store error to be returned")
		}
		if !almostEqual(engine.Snapshot().DailyLoss, 5) {
			t.Fatalf("engine state must update even if persistence fails")
		}
	})
}
