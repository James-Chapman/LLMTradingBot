package livebook

import (
	"errors"
	"testing"
	"time"
)

// memoryStore is an in-memory Store that can be told to fail.
type memoryStore struct {
	rows    map[string]Position
	failing bool
}

// newMemoryStore returns an empty working store.
func newMemoryStore() *memoryStore { return &memoryStore{rows: map[string]Position{}} }

// SaveLivePosition stores p unless the store is failing.
func (s *memoryStore) SaveLivePosition(p Position) error {
	if s.failing {
		return errors.New("disk full")
	}
	s.rows[p.ID] = p
	return nil
}

// DeleteLivePosition removes a row unless the store is failing.
func (s *memoryStore) DeleteLivePosition(id string) error {
	if s.failing {
		return errors.New("disk full")
	}
	delete(s.rows, id)
	return nil
}

// LoadLivePositions returns the stored rows.
func (s *memoryStore) LoadLivePositions() ([]Position, error) {
	out := []Position{}
	for _, p := range s.rows {
		out = append(out, p)
	}
	return out, nil
}

// entry returns a pending live entry for market.
func entry(id, market string) Position {
	return Position{ID: id, Market: market, Status: EntryPending, RequestedSize: 0.02, AvgPrice: 50_000, EntryOrderID: "order-" + id, OpenedAt: time.Now().UTC()}
}

// TestBook covers the live position book.
func TestBook(t *testing.T) {
	t.Run("GIVEN a pending entry WHEN risk positions are read THEN it counts as a long of its requested size", func(t *testing.T) {
		book := New(newMemoryStore())
		_ = book.Save(entry("p1", "BTC/EUR"))

		records := book.Records()
		if len(records) != 1 || records[0].Market != "BTC/EUR" || records[0].Size != 0.02 || records[0].PositionID != "p1" {
			t.Fatalf("a pending entry must block another entry, got %+v", records)
		}
	})

	t.Run("GIVEN a filled position WHEN risk positions are read THEN the executed size is used", func(t *testing.T) {
		book := New(newMemoryStore())
		p := entry("p1", "BTC/EUR")
		p.Status, p.Size = Open, 0.015
		_ = book.Save(p)

		if records := book.Records(); records[0].Size != 0.015 {
			t.Fatalf("expected the executed size, got %+v", records)
		}
	})

	t.Run("GIVEN saved positions WHEN a new book restores from the same store THEN they are back", func(t *testing.T) {
		store := newMemoryStore()
		_ = New(store).Save(entry("p1", "BTC/EUR"))

		restored := New(store)
		n, err := restored.Restore()
		if err != nil || n != 1 {
			t.Fatalf("Restore: %d %v", n, err)
		}
		if _, ok := restored.Get("p1"); !ok {
			t.Fatal("restored book lost the position")
		}
	})

	t.Run("GIVEN the store fails WHEN a position is saved THEN it is still tracked in memory and the error is reported", func(t *testing.T) {
		store := newMemoryStore()
		store.failing = true
		book := New(store)

		err := book.Save(entry("p1", "BTC/EUR"))

		if err == nil {
			t.Fatal("expected the store error")
		}
		if len(book.ForMarket("BTC/EUR")) != 1 {
			t.Fatal("an accepted order must stay tracked even if it cannot be persisted")
		}
	})

	t.Run("GIVEN positions with entry and exit orders WHEN looked up by order ID THEN the owning position is found", func(t *testing.T) {
		book := New(newMemoryStore())
		p := entry("p1", "BTC/EUR")
		p.Status, p.Size, p.ExitOrderID = ExitPending, 0.02, "exit-1"
		_ = book.Save(p)

		if got, ok := book.ByOrderID("order-p1"); !ok || got.ID != "p1" {
			t.Errorf("entry order lookup failed: %+v", got)
		}
		if got, ok := book.ByOrderID("exit-1"); !ok || got.ID != "p1" {
			t.Errorf("exit order lookup failed: %+v", got)
		}
		if _, ok := book.ByOrderID("unknown"); ok {
			t.Error("unknown order must not match")
		}
	})

	t.Run("GIVEN a position WHEN found by its 8-character prefix THEN it is returned", func(t *testing.T) {
		book := New(newMemoryStore())
		_ = book.Save(entry("3fa85f64-5717-4562-b3fc-2c963f66afa6", "BTC/EUR"))

		if got, ok := book.Find("3fa85f64"); !ok || got.Market != "BTC/EUR" {
			t.Errorf("prefix lookup failed: %+v", got)
		}
	})

	t.Run("GIVEN a removed position WHEN the book is read THEN it is gone from memory and the store", func(t *testing.T) {
		store := newMemoryStore()
		book := New(store)
		_ = book.Save(entry("p1", "BTC/EUR"))

		if err := book.Remove("p1"); err != nil {
			t.Fatal(err)
		}
		if len(book.All()) != 0 || len(store.rows) != 0 {
			t.Errorf("position not removed: %+v / %+v", book.All(), store.rows)
		}
	})

	t.Run("GIVEN an open position WHEN prices rise and fall THEN the trailing high only moves up", func(t *testing.T) {
		book := New(newMemoryStore())
		p := entry("p1", "BTC/EUR")
		p.Status, p.Size = Open, 0.02
		_ = book.Save(p)

		book.UpdateTrailingHighs(map[string]float64{"BTC/EUR": 55_000})
		book.UpdateTrailingHighs(map[string]float64{"BTC/EUR": 52_000})

		if got, _ := book.Get("p1"); got.TrailingHigh != 55_000 {
			t.Errorf("expected the best price seen, got %v", got.TrailingHigh)
		}
	})

	t.Run("GIVEN an open position with no recorded high WHEN the first price seen is below the entry THEN the entry price stays the reference", func(t *testing.T) {
		book := New(newMemoryStore())
		p := entry("p1", "BTC/EUR")
		p.Status, p.Size, p.AvgPrice = Open, 0.02, 100
		_ = book.Save(p)

		book.UpdateTrailingHighs(map[string]float64{"BTC/EUR": 95})

		if got, _ := book.Get("p1"); got.TrailingHigh != 100 {
			t.Errorf("expected the entry price as the reference, got %v", got.TrailingHigh)
		}
	})

	t.Run("GIVEN an open position WHEN a new high is seen THEN it is written to the store so a restart keeps it", func(t *testing.T) {
		store := newMemoryStore()
		book := New(store)
		p := entry("p1", "BTC/EUR")
		p.Status, p.Size = Open, 0.02
		_ = book.Save(p)

		book.UpdateTrailingHighs(map[string]float64{"BTC/EUR": 55_000})

		if store.rows["p1"].TrailingHigh != 55_000 {
			t.Errorf("the new high was not persisted, store has %v", store.rows["p1"].TrailingHigh)
		}
	})

	t.Run("GIVEN a position with a recorded high WHEN an older copy of it is saved THEN the high is not lowered", func(t *testing.T) {
		book := New(newMemoryStore())
		p := entry("p1", "BTC/EUR")
		p.Status, p.Size = Open, 0.02
		_ = book.Save(p)
		book.UpdateTrailingHighs(map[string]float64{"BTC/EUR": 55_000})

		p.TrailingHigh = 51_000 // a copy taken before the new high
		_ = book.Save(p)

		if got, _ := book.Get("p1"); got.TrailingHigh != 55_000 {
			t.Errorf("expected the best price to survive, got %v", got.TrailingHigh)
		}
	})
}

// TestStopMaths covers the long-position stop calculations shared with the paper engine's rules.
func TestStopMaths(t *testing.T) {
	t.Run("GIVEN a long bought at 100 WHEN the price falls 5% with a 5% stop THEN the stop-loss triggers", func(t *testing.T) {
		if !StopLossHit(100, 95, 0.05) || StopLossHit(100, 95.5, 0.05) {
			t.Error("stop-loss must trigger at the configured loss and not before")
		}
	})

	t.Run("GIVEN no trailing high yet WHEN the price retraces from the entry THEN the entry is the reference", func(t *testing.T) {
		if !TrailingStopHit(0, 100, 97, 0.03) || TrailingStopHit(0, 100, 97.5, 0.03) {
			t.Error("trailing stop must use the entry price before any high is seen")
		}
	})

	t.Run("GIVEN a trailing high WHEN the price retraces by the trail distance THEN the trailing stop triggers", func(t *testing.T) {
		if !TrailingStopHit(120, 100, 116.3, 0.03) || TrailingStopHit(120, 100, 117, 0.03) {
			t.Error("trailing stop must trigger from the best price seen")
		}
	})

	t.Run("GIVEN an unknown entry price WHEN the stop-loss is checked THEN it does not trigger", func(t *testing.T) {
		if StopLossHit(0, 10, 0.05) {
			t.Error("a zero entry price must never trigger")
		}
	})
}
