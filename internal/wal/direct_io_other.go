//go:build !linux

// O_DIRECT is a Linux-specific kernel feature; macOS / *BSD / Windows
// have no equivalent open-flag-driven uncached write path. The
// non-Linux probe always reports a fallback so the writer continues
// in buffered mode — same behaviour as a Linux host whose filesystem
// returns EINVAL. Mirrors the M0009 io_uring stub
// (`internal/aio/method_iouring_other.go`).

package wal

func probeDirectIO(walDir string) (string, error) {
	return "O_DIRECT is Linux-only; not supported on this platform", nil
}
