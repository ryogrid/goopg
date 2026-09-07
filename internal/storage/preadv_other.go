//go:build !linux

package storage

import "os"

// preadvSupported reports whether this build has a real vectored read.
const preadvSupported = false

// preadvAt is the portable fallback: a loop of ReadAt calls, one per buffer.
// Semantically identical to the Linux preadv(2) path (same bytes, same
// short-read reporting), just without the single-syscall benefit — mirroring
// how upstream degrades where readv is unavailable.
func preadvAt(f *os.File, bufs [][]byte, off int64) (int, error) {
	total := 0
	for _, b := range bufs {
		n, err := f.ReadAt(b, off+int64(total))
		total += n
		if err != nil {
			return total, err
		}
		if n < len(b) {
			return total, nil // short read: stop, let the caller interpret
		}
	}
	return total, nil
}
