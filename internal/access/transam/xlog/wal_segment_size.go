package xlog

import "sync/atomic"

// walSegmentSize is the cluster's WAL segment size — PG's wal_segment_size
// global (xlog.c), which the server reads from pg_control's xlog_seg_size at
// startup (ReadControlFile) and every segment-number ↔ file-name and LSN ↔
// segment computation uses. 0 means "not set": DefaultSegmentSize.
var walSegmentSize atomic.Int64

// WALSegmentSize is the cluster's WAL segment size: the value adopted from
// pg_control at startup, DefaultSegmentSize before that (and in processes,
// like unit tests, that never adopt one).
func WALSegmentSize() int64 {
	if n := walSegmentSize.Load(); n > 0 {
		return n
	}
	return DefaultSegmentSize
}

// SetWALSegmentSize adopts the cluster's WAL segment size. A value that is
// not a valid segment size (IsValidWalSegSize) is ignored.
func SetWALSegmentSize(n int64) {
	if IsValidWalSegSize(n) {
		walSegmentSize.Store(n)
	}
}
