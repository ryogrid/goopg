package xlog

import (
	"path/filepath"
	"sync"
)

// recoveryReadCache memoizes ReadAll during startup recovery.
//
// At startup ~30 catalog-recovery modules (internal/initdb/*_ddl_recovery.go)
// each call ReadAll on the SAME pg_wal directory to scan for their own record
// kinds. Because the WAL writer is not started until recovery completes, the
// segment files are immutable across that whole window, so every one of those
// ReadAll calls decodes byte-identical records. Previously each call re-read
// and re-decoded the entire WAL — ~20 full passes over a multi-GB WAL and the
// dominant startup allocation (analysis/perf-optimize2, fix-05). This cache
// decodes the WAL once and hands the same slice to every module.
//
// Correctness: the cache is active ONLY between BeginRecoveryCache and
// EndRecoveryCache (bracketed around the recovery block in initdb.Open, where
// nothing appends to the WAL). Outside that window — notably the per-module
// unit tests that call the replay functions directly — ReadAll falls straight
// through to readAllUncached, so behavior is exactly as before (no staleness).
// Only segmentSize==0 (the recovery default) is cached; any other size bypasses
// the cache. The cached records are read-only for every consumer.
type recoveryReadCache struct {
	mu     sync.Mutex
	active bool
	walDir string // filepath.Clean'd
	recs   []Record
	err    error
	loaded bool
}

var recoveryCache recoveryReadCache

// BeginRecoveryCache enables ReadAll memoization for walDir. Call once, before
// the startup recovery passes; pair with a deferred EndRecoveryCache. Safe to
// call redundantly (it resets the cache each time).
func BeginRecoveryCache(walDir string) {
	recoveryCache.mu.Lock()
	recoveryCache.active = true
	recoveryCache.walDir = filepath.Clean(walDir)
	recoveryCache.recs = nil
	recoveryCache.err = nil
	recoveryCache.loaded = false
	recoveryCache.mu.Unlock()
}

// EndRecoveryCache disables the cache and releases the retained records so the
// decoded WAL is not pinned in memory after recovery completes.
func EndRecoveryCache() {
	recoveryCache.mu.Lock()
	recoveryCache.active = false
	recoveryCache.walDir = ""
	recoveryCache.recs = nil
	recoveryCache.err = nil
	recoveryCache.loaded = false
	recoveryCache.mu.Unlock()
}

// ReadAll decodes every WAL record under walDir (see readAllUncached). During
// startup recovery (between BeginRecoveryCache/EndRecoveryCache for the same
// directory, segmentSize 0) it returns a memoized decode shared across all
// recovery modules; otherwise it decodes fresh on every call.
func ReadAll(walDir string, segmentSize int64) ([]Record, error) {
	if recs, err, ok := recoveryCacheGet(walDir, segmentSize); ok {
		return recs, err
	}
	return readAllUncached(walDir, segmentSize)
}

// recoveryCacheGet returns the memoized records for walDir when the cache is
// active for that directory, loading them once on first use. ok is false when
// the cache is inactive or the request does not match, in which case the
// caller must decode uncached.
func recoveryCacheGet(walDir string, segmentSize int64) (recs []Record, err error, ok bool) {
	recoveryCache.mu.Lock()
	defer recoveryCache.mu.Unlock()
	if !recoveryCache.active || segmentSize != 0 || filepath.Clean(walDir) != recoveryCache.walDir {
		return nil, nil, false
	}
	if !recoveryCache.loaded {
		// Loaded under the lock: recovery is single-threaded, so there is no
		// contention, and this guarantees exactly one decode.
		recoveryCache.recs, recoveryCache.err = readAllUncached(walDir, segmentSize)
		recoveryCache.loaded = true
	}
	return recoveryCache.recs, recoveryCache.err, true
}
