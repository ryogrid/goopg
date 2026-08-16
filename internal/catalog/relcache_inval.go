package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/goopg/goopg/internal/utils/misc"
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

// RelcacheInitFileRemoveAll removes EVERY relcache init file in the cluster —
// the shared `global/pg_internal.init` plus one per database directory under
// `base/` and under each non-default tablespace
// (`pg_tblspc/<oid>/<TABLESPACE_VERSION_DIRECTORY>/<dboid>/`). It is the
// startup-time sweep, as opposed to RelcacheInitFileUnlink's reactive,
// single-database invalidation.
//
// Mirrors RelationCacheInitFileRemove (relcache.c) plus its
// RelationCacheInitFileRemoveInDir helper: only directory entries whose names
// are entirely digits are treated as database OIDs, and a missing init file is
// not an error. Upstream calls this unconditionally from StartupXLOG
// (xlog.c:5633) BEFORE any replay, for the reason its comment gives — a
// replayed WAL record can make an init file disagree with reality, and even a
// cleanly shut down cluster is swept "just to be safe".
//
// For goopg the sweep matters for a reason upstream never faces: the directory
// may have been written by real PostgreSQL, so the init files present at
// startup can be PG-authored caches of a catalog goopg is about to change
// underneath them. This is the mirror image of M0131-S10, where PG must
// discard goopg's init file.
//
// Errors from individual unlinks are collected rather than aborting the sweep
// (upstream's unlink_initfile passes elevel = LOG here, deliberately not
// ERROR): a stale init file left behind is a correctness hazard, but so is
// refusing to start, and the remaining files should still go.
func RelcacheInitFileRemoveAll(dataDir string) error {
	var firstErr error
	note := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	note(unlinkInitFile(filepath.Join(dataDir, "global", "pg_internal.init")))
	note(removeInitFilesInDir(filepath.Join(dataDir, "base")))

	// Non-default tablespaces: pg_tblspc/<tablespace oid>/<version dir>/
	// holds the per-database directories. In goopg these are real
	// directories rather than upstream's symlinks (initdb.go:560), which
	// makes no difference to the scan.
	tblspcDir := filepath.Join(dataDir, "pg_tblspc")
	entries, err := os.ReadDir(tblspcDir)
	if err != nil {
		if !os.IsNotExist(err) {
			note(fmt.Errorf("relcache init scan %s: %w", tblspcDir, err))
		}
		return firstErr
	}
	for _, e := range entries {
		if !allDigits(e.Name()) {
			continue
		}
		note(removeInitFilesInDir(filepath.Join(tblspcDir, e.Name(), misc.TablespaceVersionDirectory)))
	}
	return firstErr
}

// removeInitFilesInDir unlinks <dir>/<dboid>/pg_internal.init for every
// all-digits entry of dir. Mirrors RelationCacheInitFileRemoveInDir.
func removeInitFilesInDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("relcache init scan %s: %w", dir, err)
	}
	var firstErr error
	for _, e := range entries {
		if !allDigits(e.Name()) {
			continue
		}
		if err := unlinkInitFile(filepath.Join(dir, e.Name(), "pg_internal.init")); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func unlinkInitFile(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("relcache init unlink %s: %w", path, err)
	}
	return nil
}

// allDigits reports whether s is non-empty and consists only of ASCII digits,
// the test upstream spells `strspn(de->d_name, "0123456789") == strlen(...)`.
// It is what keeps the sweep from descending into names like `pgsql_tmp`.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
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
