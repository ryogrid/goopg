//go:build linux

package initdb

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// syncfsSupported reports whether the --sync-method=syncfs option is
// available on this build. Mirrors upstream initdb's HAVE_SYNCFS gate
// (configure-detected); on Linux syncfs(2) is always present.
const syncfsSupported = true

// syncfsPath flushes the entire filesystem containing path via syncfs(2),
// porting upstream initdb's do_syncfs (src/common/file_utils.c): open the
// path read-only and call syncfs on the descriptor. A single syncfs covers
// every file on that filesystem, so one call per filesystem boundary
// (pg_data, and a relocated pg_wal) is sufficient.
func syncfsPath(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %q for syncfs: %w", path, err)
	}
	defer f.Close()
	if err := unix.Syncfs(int(f.Fd())); err != nil {
		return fmt.Errorf("syncfs %q: %w", path, err)
	}
	return nil
}
