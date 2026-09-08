//go:build linux

package storage

import (
	"os"

	"golang.org/x/sys/unix"
)

// preadvSupported reports whether this build has a real vectored read.
const preadvSupported = true

// preadvAt performs a single preadv(2) filling bufs from off, and returns the
// byte count the kernel reported. Short reads are the caller's to interpret —
// exactly as with pread(2) — because a vectored read at end-of-file legitimately
// returns fewer bytes than the iovec total.
//
// This is E-19 S1's reason for existing. PG's read stream merges up to
// io_combine_limit adjacent blocks into ONE vectored read
// (postgres/src/backend/storage/aio/read_stream.c:474-477, issued by
// StartReadBuffersImpl via io_pages[MAX_IO_COMBINE_LIMIT],
// src/backend/storage/buffer/bufmgr.c:1777); goopg had no vectored read at
// all, so a run of N adjacent blocks cost N syscalls instead of one. The
// destination buffers are separate slices by construction — they are pages of
// the buffer pool's arena belonging to different slots — which is precisely the
// case an iovec exists for.
func preadvAt(f *os.File, bufs [][]byte, off int64) (int, error) {
	n, err := unix.Preadv(int(f.Fd()), bufs, off)
	if err != nil {
		return n, err
	}
	return n, nil
}
