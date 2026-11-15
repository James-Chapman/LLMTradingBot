package histdata

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// marketSource writes a one-file archive with a few July 2026 trades for the
// Kraken file stem (XBTEUR, ETHEUR) and returns its directory.
func marketSource(t *testing.T, stem string, price string) string {
	t.Helper()
	src := t.TempDir()
	lines := []string{archiveHeader}
	for i := range 4 {
		v := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * 31 * time.Second)
		lines = append(lines, tradeLine(v, price, "1", "b", int64(i+1)))
	}
	writeFile(t, src, stem+".csv", strings.Join(lines, "\n")+"\n")
	return src
}

// manifestMonths returns the month keys the manifest on disk lists for market.
func manifestMonths(t *testing.T, dir, market string) []string {
	t.Helper()
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	if mr := m.Markets[market]; mr != nil {
		for k := range mr.Months {
			keys = append(keys, k)
		}
	}
	return keys
}

// lockPath returns the cache's import lock file.
func lockPath(dir string) string { return filepath.Join(dir, LockName) }

// TestImportLock covers imports into one cache from several processes:
// each import is serialised by the cache lock and merges into the manifest
// on disk, never over it.
func TestImportLock(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN two caches opened on one directory before either imports WHEN each imports a different market THEN the manifest lists both markets", func(t *testing.T) {
		dir := t.TempDir()
		btc, eth := openCache(t, dir), openCache(t, dir) // two processes, both started before either import
		if _, err := btc.ImportArchive(ctx, marketSource(t, "XBTEUR", "50000"), ImportOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, err := eth.ImportArchive(ctx, marketSource(t, "ETHEUR", "3000"), ImportOptions{}); err != nil {
			t.Fatal(err)
		}
		if got := manifestMonths(t, dir, "BTC/EUR"); len(got) != 1 {
			t.Fatalf("BTC/EUR months in the manifest = %v, want [2026-07]: the later import overwrote it", got)
		}
		if got := manifestMonths(t, dir, "ETH/EUR"); len(got) != 1 {
			t.Fatalf("ETH/EUR months in the manifest = %v, want [2026-07]", got)
		}
	})

	t.Run("GIVEN two caches importing different markets at the same time WHEN both finish THEN the cache verifies with every month in the manifest", func(t *testing.T) {
		dir := t.TempDir()
		sources := map[string]string{"XBTEUR": "50000", "ETHEUR": "3000"}
		caches := map[string]*Cache{}
		srcs := map[string]string{}
		for stem, price := range sources {
			caches[stem], srcs[stem] = openCache(t, dir), marketSource(t, stem, price)
		}
		var wg sync.WaitGroup
		errs := make(chan error, len(sources))
		for stem := range sources {
			wg.Go(func() {
				_, err := caches[stem].ImportArchive(ctx, srcs[stem], ImportOptions{})
				errs <- err
			})
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		rep, err := openCache(t, dir).Verify(VerifyOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if n := rep.Counts[KindNotInManifest]; n != 0 || len(rep.Markets) != 2 {
			t.Fatalf("%d not_in_manifest findings over %d markets: %+v", n, len(rep.Markets), rep.Findings)
		}
	})

	t.Run("GIVEN a lock left by a crashed import WHEN another import runs THEN it takes the lock over, imports, and leaves no lock behind", func(t *testing.T) {
		dir := t.TempDir()
		c := openCache(t, dir)
		writeFile(t, dir, LockName, "pid 1 crashed\n")
		old := time.Now().Add(-time.Hour)
		if err := os.Chtimes(lockPath(dir), old, old); err != nil {
			t.Fatal(err)
		}
		if _, err := c.ImportArchive(ctx, marketSource(t, "XBTEUR", "50000"), ImportOptions{}); err != nil {
			t.Fatal(err)
		}
		if got := manifestMonths(t, dir, "BTC/EUR"); len(got) != 1 {
			t.Fatalf("manifest months %v after taking over a stale lock", got)
		}
		if _, err := os.Stat(lockPath(dir)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("lock file still present after the import: %v", err)
		}
	})

	t.Run("GIVEN another import holding the lock WHEN an import's context ends while it waits THEN it returns the context error and writes nothing", func(t *testing.T) {
		dir := t.TempDir()
		c := openCache(t, dir)
		writeFile(t, dir, LockName, "pid 2 busy\n") // fresh mtime: a live holder
		wait, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		defer cancel()
		_, err := c.ImportArchive(wait, marketSource(t, "XBTEUR", "50000"), ImportOptions{})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want the context deadline", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "bars", "BTC-EUR")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("bars written while another import held the lock: %v", err)
		}
		if data, _ := os.ReadFile(lockPath(dir)); string(data) != "pid 2 busy\n" {
			t.Fatalf("the holder's lock was touched: %q", data)
		}
	})

	t.Run("GIVEN a dry run WHEN imported THEN no lock and no manifest are created", func(t *testing.T) {
		dir := t.TempDir()
		c := openCache(t, dir)
		if _, err := c.ImportArchive(ctx, marketSource(t, "XBTEUR", "50000"), ImportOptions{DryRun: true}); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{LockName, ManifestName} {
			if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s exists after a dry run: %v", name, err)
			}
		}
	})

	t.Run("GIVEN a long import WHEN it holds the lock THEN the lock's time is refreshed so waiters do not take it over", func(t *testing.T) {
		dir := t.TempDir()
		restore := setLockTimings(20*time.Millisecond, 200*time.Millisecond, 5*time.Millisecond)
		defer restore()
		release, err := acquireLock(ctx, dir)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(400 * time.Millisecond) // twice the stale age
		wait, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		if _, err := acquireLock(wait, dir); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("a waiter got a lock that was held and refreshed: %v", err)
		}
		release()
		again, err := acquireLock(ctx, dir)
		if err != nil {
			t.Fatalf("lock not free after release: %v", err)
		}
		again()
	})
}
