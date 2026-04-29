//go:build linux

// O_DIRECT WAL probe (Phase 1 of M0010-0001). Probes whether the
// filesystem hosting the WAL directory honours O_DIRECT by opening
// a sentinel file with O_RDWR|O_CREATE|O_DIRECT and observing the
// result. Returns the human-readable reason on probe failure so the
// startup log can surface it to the operator. Removes the sentinel
// before returning so a successful probe leaves no trace.
//
// O_DIRECT support is filesystem-dependent: ext4 / XFS honour it;
// tmpfs / overlayfs do not (kernel returns EINVAL at open time).
// We probe rather than assume so a misconfigured `wal_direct_io=on`
// on a tmpfs-backed data directory degrades to buffered I/O with a
// loud warning rather than crashing the server at first segment
// open. Mirrors upstream's `pgaio_uring_setup` shape from
// `postgres/src/backend/storage/aio/method_io_uring.c` — open the
// kernel feature, fall back on error.
//
// Phase 1 (this file) only delivers the probe. Phase 2 (a sibling
// slice tracked under M0010-0001 in `.ralph/fix_plan.md`) lights up
// the actual O_DIRECT segment open + per-write read-modify-write
// alignment path; without that, the probe outcome is plumbing-only
// and `wal_direct_io=on` does not yet drive O_DIRECT writes.

package wal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// probeDirectIO opens a sentinel file under walDir with O_DIRECT to
// determine whether the filesystem honours the flag. Returns ("",
// nil) on success — the empty reason signals "no fallback needed".
// Returns (reason, nil) when O_DIRECT is unsupported (EINVAL); the
// caller treats this as a successful fallback (the writer continues
// in buffered mode). Returns ("", err) on unexpected I/O errors
// (mkdir failure, permission denied) so the writer fails fast.
func probeDirectIO(walDir string) (string, error) {
	if err := os.MkdirAll(walDir, 0o700); err != nil {
		return "", fmt.Errorf("wal: mkdir %s: %w", walDir, err)
	}
	path := filepath.Join(walDir, ".wal_direct_io_probe")
	// Best-effort cleanup: a stale probe file from a previous run
	// (process killed mid-probe) is harmless, but tidier to remove.
	_ = os.Remove(path)

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|unix.O_DIRECT, 0o600)
	if err != nil {
		_ = os.Remove(path)
		if errors.Is(err, unix.EINVAL) {
			return "filesystem does not support O_DIRECT (open returned EINVAL)", nil
		}
		// EOPNOTSUPP is returned by some FUSE filesystems; treat as
		// fallback rather than fatal so a misconfigured operator
		// just sees buffered writes + a warning.
		if errors.Is(err, unix.EOPNOTSUPP) {
			return "filesystem does not support O_DIRECT (open returned EOPNOTSUPP)", nil
		}
		return "", fmt.Errorf("wal: probe O_DIRECT: %w", err)
	}
	_ = f.Close()
	_ = os.Remove(path)
	return "", nil
}
