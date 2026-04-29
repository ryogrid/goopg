//go:build !linux

package wal

import "os"

// dataSync falls back to full fsync(2) on platforms without a
// portable Go wrapper for fdatasync(2). macOS lacks fdatasync
// entirely (F_FULLFSYNC is the closest equivalent and has its own
// trade-offs); BSDs vary; Windows has no equivalent. The fallback
// preserves the durability contract — a successful return still
// means the bytes are on stable storage — at the cost of paying
// for inode metadata updates that fdatasync would have skipped.
// See docs/design/0007-0002-fdatasync-commit-path.md.
func dataSync(f *os.File) error {
	return f.Sync()
}
