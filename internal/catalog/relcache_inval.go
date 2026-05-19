package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// relCacheInitMu serializes relcache-init-file operations across goroutines.
// Mirrors PG's RelCacheInitLock (relcache.c:6859-6882).
var relCacheInitMu sync.Mutex

// RelcacheInitFileUnlink removes both pg_internal.init files — the shared
// one at <dataDir>/global/pg_internal.init and the per-database one at
// <dataDir>/base/<dboid>/pg_internal.init — so the next backend recreates
// them from scratch. ENOENT is silently ignored; any other error is returned.
//
// Must be called inside WithRelCacheInitLock to prevent TOCTOU races with
// concurrent readers. Mirrors PG's RelationCacheInitFilePreInvalidate.
func RelcacheInitFileUnlink(dataDir string, dboid uint32) error {
	paths := [2]string{
		filepath.Join(dataDir, "global", "pg_internal.init"),
		filepath.Join(dataDir, "base", fmt.Sprintf("%d", dboid), "pg_internal.init"),
	}
	var firstErr error
	for _, p := range paths {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			if firstErr == nil {
				firstErr = fmt.Errorf("relcache init unlink %s: %w", p, err)
			}
		}
	}
	return firstErr
}

// WithRelCacheInitLock holds the process-wide relcache-init-file lock for the
// duration of fn. Every call site that unlinks or rewrites pg_internal.init
// must go through this lock to prevent a writer from recreating a file that
// another goroutine is in the process of unlinking.
//
// Mirrors PG's RelCacheInitLock (LWLock acquired exclusively in
// RelationCacheInitFilePreInvalidate / PostInvalidate).
func WithRelCacheInitLock(fn func() error) error {
	relCacheInitMu.Lock()
	defer relCacheInitMu.Unlock()
	return fn()
}
