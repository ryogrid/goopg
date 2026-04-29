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
// Phase 2 (also in this file): enableDirectIO toggles O_DIRECT on a
// segment fd via fcntl(F_SETFL) AFTER preallocation completes — the
// preallocator's 64-KiB zero-fill uses non-aligned heap buffers
// which would EINVAL under O_DIRECT, so the flip happens once the
// segment body is laid down. allocAlignedScratch / freeAlignedScratch
// give the writer a page-aligned RMW buffer (mmap MAP_PRIVATE|
// MAP_ANONYMOUS — same pattern as `internal/storage/arena.go`).

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

// enableDirectIO turns on O_DIRECT on an already-open file
// descriptor via fcntl(F_SETFL). Mirrors
// `internal/storage/direct_io_linux.go::setDirectIOIfRequested`'s
// shape but without ORing in O_DSYNC (the WAL writer's commit-path
// `fdatasync` already provides the durability barrier — see
// docs/design/0007-0002-fdatasync-commit-path.md). Used after
// preallocation so the 64-KiB heap-buffer zero-fill writes don't
// EINVAL under O_DIRECT alignment requirements.
func enableDirectIO(f *os.File) error {
	flags, err := unix.FcntlInt(f.Fd(), unix.F_GETFL, 0)
	if err != nil {
		return fmt.Errorf("wal: F_GETFL: %w", err)
	}
	flags |= unix.O_DIRECT
	if _, err := unix.FcntlInt(f.Fd(), unix.F_SETFL, flags); err != nil {
		return fmt.Errorf("wal: F_SETFL O_DIRECT: %w", err)
	}
	return nil
}

// allocAlignedScratch returns a page-aligned []byte of length size
// suitable for O_DIRECT pread/pwrite. Backed by `unix.Mmap` —
// MAP_PRIVATE|MAP_ANONYMOUS pages are returned 4 KiB-aligned by
// the kernel, which satisfies the alignment constraint on every
// modern Linux filesystem (ext4, XFS default to a 4 KiB DIO
// boundary). Mirrors `internal/storage/arena.go::newArena`.
func allocAlignedScratch(size int) ([]byte, error) {
	return unix.Mmap(-1, 0, size,
		unix.PROT_READ|unix.PROT_WRITE,
		unix.MAP_PRIVATE|unix.MAP_ANONYMOUS)
}

// freeAlignedScratch releases an mmap'd scratch buffer. Safe to
// call with nil.
func freeAlignedScratch(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	return unix.Munmap(b)
}
