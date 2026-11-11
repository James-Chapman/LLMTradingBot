package observability

import (
	"errors"
	"testing"
)

// fakeStore records saved entries and serves a fixed history.
type fakeStore struct {
	saved   []string
	history []ActivityEntry
	failGet bool
}

// SaveActivityLog records the saved entry for assertions.
func (f *fakeStore) SaveActivityLog(level, message, detail string) error {
	f.saved = append(f.saved, level+":"+message)
	return nil
}

// GetRecentActivity returns the canned history or a simulated failure.
func (f *fakeStore) GetRecentActivity(limit int) ([]ActivityEntry, error) {
	if f.failGet {
		return nil, errors.New("db down")
	}
	return f.history, nil
}

// TestActivityLog covers ordering, capacity, persistence and seeding.
func TestActivityLog(t *testing.T) {
	t.Run("GIVEN several events WHEN reading recent THEN newest comes first", func(t *testing.T) {
		log := NewActivityLog(10)
		log.Info("first", "")
		log.Warn("second", "")
		got := log.Recent(10)
		if len(got) != 2 || got[0].Message != "second" || got[0].Level != "warn" {
			t.Fatalf("unexpected order %+v", got)
		}
	})

	t.Run("GIVEN more events than capacity WHEN adding THEN the oldest are dropped", func(t *testing.T) {
		log := NewActivityLog(3)
		for _, m := range []string{"a", "b", "c", "d"} {
			log.Info(m, "")
		}
		got := log.Recent(10)
		if len(got) != 3 || got[2].Message != "b" {
			t.Fatalf("expected capacity-bounded buffer, got %+v", got)
		}
	})

	t.Run("GIVEN a store WHEN an event is added THEN it is persisted", func(t *testing.T) {
		store := &fakeStore{}
		log := NewActivityLog(5)
		log.SetStore(store)
		log.Success("filled", "detail")
		if len(store.saved) != 1 || store.saved[0] != "success:filled" {
			t.Fatalf("expected persisted entry, got %v", store.saved)
		}
	})

	t.Run("GIVEN DB history WHEN seeding THEN entries are loaded", func(t *testing.T) {
		store := &fakeStore{history: []ActivityEntry{{Message: "old"}}}
		log := NewActivityLog(5)
		log.SetStore(store)
		log.SeedFromStore()
		if got := log.Recent(5); len(got) != 1 || got[0].Message != "old" {
			t.Fatalf("expected seeded history, got %+v", got)
		}
	})

	t.Run("GIVEN a failing store WHEN seeding THEN the log stays empty without panicking", func(t *testing.T) {
		log := NewActivityLog(5)
		log.SetStore(&fakeStore{failGet: true})
		log.SeedFromStore()
		if len(log.Recent(5)) != 0 {
			t.Fatal("expected empty log")
		}
	})
}
