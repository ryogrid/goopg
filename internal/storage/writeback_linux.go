//go:build linux

package storage

import (
	"os"

	"golang.org/x/sys/unix"
)

// syncFileRangeHint issues a real sync_file_range(2) write-behind hint over
// f's whole current extent (offset 0, nbytes 0 means "to current EOF" per
// sync_file_range(2)). SYNC_FILE_RANGE_WRITE alone (no _WAIT_BEFORE/_AFTER)
// matches upstream's pg_flush_data: start writeback asynchronously, don't
// block waiting for it and don't guarantee durability (fsync still owns
// that). ENOSYS/EINVAL (e.g. an unsupported filesystem) is reported as
// ErrWritebackUnsupported so callers don't count a writeback that didn't
// really happen.
func syncFileRangeHint(f *os.File) error {
	err := unix.SyncFileRange(int(f.Fd()), 0, 0, unix.SYNC_FILE_RANGE_WRITE)
	if err == unix.ENOSYS || err == unix.EINVAL {
		return ErrWritebackUnsupported
	}
	return err
}
