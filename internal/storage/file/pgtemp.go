// Package file implements PostgreSQL's temporary-file directory convention
// for query-time spill files (sorts, hash-join batches, materialised sides).
//
// The package is named for its PG home, src/backend/storage/file (fd.c), and
// is the receptacle for the rest of that layer as goopg grows it — BufFile,
// FileSet, the crash-time reinit sweep. Callers: do not shadow the package
// name with a local `file` variable; in a package that opens files that is an
// easy accident, and it silently breaks every `file.X` reference after it.
//
// PG keeps them in `<datadir>/base/pgsql_tmp/` and names every file with the
// `pgsql_tmp` prefix (`PG_TEMP_FILES_DIR` / `PG_TEMP_FILE_PREFIX`,
// postgres/src/include/common/file_utils.h:63-64). The prefix is not
// decoration: it is what makes the crash sweep safe. `RemovePgTempFilesInDir`
// (postgres/src/backend/storage/file/fd.c) deletes only entries whose name
// starts with it, so a file that some other subsystem left in the directory is
// never mistaken for a stray spill file.
//
// goopg previously wrote spill files into `os.TempDir()` with a `goopg-spill-`
// name, which put them outside the cluster (a `rm -rf $PGDATA` left them
// behind, and they escaped the datadir's filesystem/quota entirely) and gave a
// restart nothing to sweep. This package is the shared substrate M0127-P3.3
// moves them onto; see docs/design/leftdeep-joins/06 §3.
package file

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// DirName mirrors PG_TEMP_FILES_DIR (file_utils.h:63).
	DirName = "pgsql_tmp"
	// FilePrefix mirrors PG_TEMP_FILE_PREFIX (file_utils.h:64). Every file
	// this package's users create must start with it or the sweep will not
	// reclaim it after a crash.
	FilePrefix = "pgsql_tmp"
)

// Dir returns the cluster's default-tablespace temp directory,
// `<dataDir>/base/pgsql_tmp` — PG's `snprintf(temp_path, ..., "base/%s",
// PG_TEMP_FILES_DIR)` in RemovePgTempFiles. Non-default tablespaces have their
// own pgsql_tmp directories in PG; goopg has no temp_tablespaces support yet,
// so this one directory is the whole story (deferral ledger 2026-08-03).
func Dir(dataDir string) string {
	return filepath.Join(dataDir, "base", DirName)
}

// EnsureDir creates the temp directory if it does not exist and returns its
// path. PG creates it lazily too (`PathNameCreateTemporaryDir`), because a
// cluster that never spills should not carry an empty directory from initdb.
func EnsureDir(dataDir string) (string, error) {
	dir := Dir(dataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("pgtemp: create %s: %w", dir, err)
	}
	return dir, nil
}

// FilePattern returns the os.CreateTemp pattern for one backend's spill files:
// `pgsql_tmp<pid>.*`. PG names them `pgsql_tmp%d.%ld` (pid, per-backend file
// counter) in `OpenTemporaryFileInTablespace`; the random suffix os.CreateTemp
// substitutes for `*` plays the counter's role while keeping creation atomic.
func FilePattern(pid int) string {
	return fmt.Sprintf("%s%d.*", FilePrefix, pid)
}

// RemoveStrayFiles deletes every `pgsql_tmp*` entry left in the cluster's temp
// directory and returns how many it removed. It is goopg's
// RemovePgTempFilesInDir(base/pgsql_tmp, missing_ok=true, unlink_all=false):
// a missing directory is not an error (a cluster that never spilled has none),
// and entries WITHOUT the prefix are left alone.
//
// Call it at startup before accepting connections. Anything still present then
// is by definition a stray: a live backend's temp files are unlinked by its own
// per-query registry, so only a crash (or SIGKILL) can leave one behind.
func RemoveStrayFiles(dataDir string) (int, error) {
	dir := Dir(dataDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("pgtemp: read %s: %w", dir, err)
	}
	removed := 0
	var firstErr error
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), FilePrefix) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		// unlink_all=true for subdirectories, matching fd.c: a shared
		// file-set directory carries no prefix on its members.
		var rmErr error
		if e.IsDir() {
			rmErr = os.RemoveAll(path)
		} else {
			rmErr = os.Remove(path)
		}
		if rmErr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("pgtemp: remove %s: %w", path, rmErr)
			}
			continue
		}
		removed++
	}
	return removed, firstErr
}
