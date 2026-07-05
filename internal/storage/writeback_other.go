//go:build !linux

package storage

import "os"

// syncFileRangeHint has no portable equivalent outside Linux (macOS lacks
// sync_file_range entirely; BSDs vary). Mirrors upstream's behaviour on
// platforms without HAVE_SYNC_FILE_RANGE: writeback is simply unavailable,
// not approximated by a full fsync (that would change the durability/
// blocking contract this hint deliberately doesn't have).
func syncFileRangeHint(f *os.File) error {
	return ErrWritebackUnsupported
}
