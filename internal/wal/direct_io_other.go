//go:build !linux

// O_DIRECT is a Linux-specific kernel feature; macOS / *BSD / Windows
// have no equivalent open-flag-driven uncached write path. The
// non-Linux probe always reports a fallback so the writer continues
// in buffered mode — same behaviour as a Linux host whose filesystem
// returns EINVAL. Mirrors the M0009 io_uring stub
// (`internal/aio/method_iouring_other.go`).

package wal

import (
	"errors"
	"os"
)

func probeDirectIO(walDir string) (string, error) {
	return "O_DIRECT is Linux-only; not supported on this platform", nil
}

// enableDirectIO is a no-op stub on non-Linux. Phase 2 only flips
// directIOActive when the probe succeeds, and the probe always
// falls back on non-Linux — so this function is never reached at
// runtime. Kept compilable so internal/wal/writer.go can call the
// helper without build-tagging every call site.
func enableDirectIO(f *os.File) error {
	return errors.New("wal: enableDirectIO unsupported on non-Linux (probe should have failed)")
}

func allocAlignedScratch(size int) ([]byte, error) {
	return nil, errors.New("wal: allocAlignedScratch unsupported on non-Linux")
}

func freeAlignedScratch(b []byte) error { return nil }
