package histdata

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// LockName is the file in the cache directory that serialises imports.
// While it exists another import (another process, or another Cache on the
// same directory) is writing month files and the manifest.
const LockName = ".lock"

// Lock timings, variables so tests can shorten them. The holder refreshes
// the lock's modification time every lockHeartbeat; a lock not refreshed for
// lockStaleAfter belongs to a crashed import and may be taken over; a waiter
// retries every lockPoll.
var (
	lockHeartbeat  = 30 * time.Second
	lockStaleAfter = 2 * time.Minute
	lockPoll       = 250 * time.Millisecond
)

// setLockTimings replaces the lock timings and returns a function that
// restores them (tests only).
func setLockTimings(heartbeat, staleAfter, poll time.Duration) func() {
	h, s, p := lockHeartbeat, lockStaleAfter, lockPoll
	lockHeartbeat, lockStaleAfter, lockPoll = heartbeat, staleAfter, poll
	return func() { lockHeartbeat, lockStaleAfter, lockPoll = h, s, p }
}

// acquireLock takes the cache's import lock, waiting while another live
// import holds it, until ctx ends. The lock is a file created exclusively
// (O_EXCL is atomic on every supported OS), so no system lock API is needed.
// The returned release stops the heartbeat and removes the file.
//
// Taking over a stale lock renames it away before creating a new one; if two
// waiters judge the same lock stale at the same instant, the second rename
// can move the first waiter's fresh lock. That needs a crashed holder and two
// waiters within microseconds of each other, and is accepted.
func acquireLock(ctx context.Context, dir string) (release func(), err error) {
	path := filepath.Join(dir, LockName)
	content := fmt.Sprintf("pid %d since %s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
	for {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			_, werr := f.WriteString(content)
			if cerr := f.Close(); werr == nil {
				werr = cerr
			}
			if werr != nil {
				_ = os.Remove(path)
				return nil, werr
			}
			return startHeartbeat(path), nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("cache lock: %w", err)
		}
		if takeOverStale(path) {
			continue
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for the cache lock %s (another import is running): %w", path, ctx.Err())
		case <-time.After(lockPoll):
		}
	}
}

// takeOverStale moves the lock at path aside when its holder has not
// refreshed it for lockStaleAfter, and reports whether it did.
func takeOverStale(path string) bool {
	st, err := os.Stat(path)
	if err != nil || time.Since(st.ModTime()) < lockStaleAfter {
		return false
	}
	aside := fmt.Sprintf("%s.stale-%d-%d", path, os.Getpid(), time.Now().UnixNano())
	if os.Rename(path, aside) != nil {
		return false // another waiter got there first
	}
	_ = os.Remove(aside)
	return true
}

// startHeartbeat refreshes the lock's modification time until the returned
// release is called, which then removes the lock file.
func startHeartbeat(path string) func() {
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		t := time.NewTicker(lockHeartbeat)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				now := time.Now()
				_ = os.Chtimes(path, now, now)
			}
		}
	})
	var once sync.Once
	return func() {
		once.Do(func() {
			close(stop)
			wg.Wait()
			_ = os.Remove(path)
		})
	}
}
