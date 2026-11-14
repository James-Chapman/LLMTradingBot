package observability

import (
	"sync"
	"time"
)

// ActivityEntry is one line in the dashboard activity feed.
type ActivityEntry struct {
	Timestamp string `json:"timestamp"` // RFC 3339, UTC
	Level     string `json:"level"`     // info | warn | error | success
	Message   string `json:"message"`
	Detail    string `json:"detail"`
}

// ActivityStore persists activity entries (implemented by storage.Repository).
type ActivityStore interface {
	SaveActivityLog(level, message, detail string) error
	GetRecentActivity(limit int) ([]ActivityEntry, error)
}

// ActivityLog is a rolling, newest-first buffer of recent bot events that is
// mirrored to the database when a store is attached.
type ActivityLog struct {
	mu      sync.Mutex
	entries []ActivityEntry // newest first
	maxLen  int
	store   ActivityStore
}

// NewActivityLog creates an empty log holding at most maxLen entries.
func NewActivityLog(maxLen int) *ActivityLog {
	return &ActivityLog{maxLen: maxLen}
}

// Activity is the process-wide activity log used by the server and loops.
var Activity = NewActivityLog(200)

// SetStore wires in persistence so new entries are also saved to the DB.
func (a *ActivityLog) SetStore(store ActivityStore) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.store = store
}

// SeedFromStore pre-populates the buffer from DB history (call once at startup).
// Failures are ignored — the log simply starts empty.
func (a *ActivityLog) SeedFromStore() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.store == nil {
		return
	}
	rows, err := a.store.GetRecentActivity(a.maxLen)
	if err != nil {
		return
	}
	// rows are newest-first, matching our ordering.
	a.entries = append(rows[:0:0], rows...)
	if len(a.entries) > a.maxLen {
		a.entries = a.entries[:a.maxLen]
	}
}

// add prepends an entry, trims the buffer and persists best-effort.
func (a *ActivityLog) add(level, message, detail string) {
	entry := ActivityEntry{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Level:     level,
		Message:   message,
		Detail:    detail,
	}
	a.mu.Lock()
	a.entries = append([]ActivityEntry{entry}, a.entries...)
	if len(a.entries) > a.maxLen {
		a.entries = a.entries[:a.maxLen]
	}
	store := a.store
	a.mu.Unlock()

	if store != nil {
		_ = store.SaveActivityLog(level, message, detail) // never let DB errors break the bot loop
	}
}

// Info records an informational event.
func (a *ActivityLog) Info(message, detail string) { a.add("info", message, detail) }

// Warn records a warning event.
func (a *ActivityLog) Warn(message, detail string) { a.add("warn", message, detail) }

// Error records an error event.
func (a *ActivityLog) Error(message, detail string) { a.add("error", message, detail) }

// Success records a success event.
func (a *ActivityLog) Success(message, detail string) { a.add("success", message, detail) }

// Recent returns up to n newest-first entries.
func (a *ActivityLog) Recent(n int) []ActivityEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	if n > len(a.entries) || n <= 0 {
		n = len(a.entries)
	}
	return append([]ActivityEntry(nil), a.entries[:n]...)
}
