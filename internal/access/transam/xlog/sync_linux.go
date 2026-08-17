//go:build linux

package xlog

import (
	"os"

	"golang.org/x/sys/unix"
)

// dataSync persists the file's data blocks via fdatasync(2) on
// Linux. Unlike fsync(2) it does not flush inode metadata
// (mtime, size, …) — which is exactly what we want now that
// preallocated WAL segments have a fixed inode size between
// commits. Mirrors upstream's `pg_fdatasync` from
// postgres/src/backend/storage/file/fd.c. See
// docs/design/0007-0002-fdatasync-commit-path.md.
func dataSync(f *os.File) error {
	return unix.Fdatasync(int(f.Fd()))
}

// fullSync persists the file's data blocks AND inode metadata via
// fsync(2) on Linux. Backs wal_sync_method=fsync — unlike dataSync
// (fdatasync) it also flushes mtime/size, at extra cost. See
// docs/design/0007-0002-fdatasync-commit-path.md.
func fullSync(f *os.File) error {
	return unix.Fsync(int(f.Fd()))
}
