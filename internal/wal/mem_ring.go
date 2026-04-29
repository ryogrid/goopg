package wal

import (
	"sync"
	"sync/atomic"
)

// MemRing is the bounded in-memory mirror of recently-written WAL
// bytes. The wal.Writer feeds every successful state.writeAt into
// it; RecordIterator (used by walsender) consults the ring first
// and only falls back to per-segment pread syscalls when the
// requested LSN range is no longer resident.
//
// Why this exists: M0010-0001 made WAL writes bypass the OS page
// cache (`wal_direct_io=on`), so a sender trying to stream from
// disk reads now pays full I/O cost — even for WAL the writer
// generated milliseconds ago. The ring closes that gap by giving
// senders a RAM-resident copy of recent bytes without depending on
// page-cache residency. Sized via the `wal_sender_memory_buffer`
// GUC; default 16 MiB. See
// docs/design/0010-0002-walsender-in-memory-wal-handoff.md.
//
// Concurrency model: one writer (the wal.Writer's loop goroutine
// calling Append after each writeAt) and many readers (a walsender
// goroutine per active replication connection, each calling
// ReadAt). A single sync.RWMutex serialises head/tail metadata
// updates against reader snapshots. Hot-path readers do (RLock →
// bounds check → memcpy → RUnlock); the actual copy happens under
// RLock so a concurrent eviction can't free the bytes mid-read.
//
// LSN ↔ byte-position convention: matches the rest of the wal
// package — `pos` is a 0-based byte offset into the WAL stream
// (start of a record at byte position N has LSN N+1). The ring
// stores the byte range `[head, tail)` where `tail` mirrors the
// writer's writePos and `head` is `max(0, tail - cap)` once the
// ring fills.
type MemRing struct {
	mu sync.RWMutex

	buf  []byte // capacity = cap; never reallocated
	cap  int64
	head int64 // 0-based byte offset of oldest resident byte
	tail int64 // 0-based byte offset one past newest resident byte

	// hits / misses are observability counters surfaced via
	// Hits()/Misses() so M0010-0003's `pg_stat_wal_io` /
	// `pg_stat_replication.send_buffer_*` surface can read them
	// without taking the mutex. Atomic loads / stores by design.
	hits   atomic.Uint64
	misses atomic.Uint64
}

// NewMemRing constructs a ring with the given capacity in bytes.
// capBytes <= 0 returns nil (callers treat nil as "no ring; every
// read goes to disk"). Capacities are not rounded; the ring stores
// exactly capBytes of recent WAL.
func NewMemRing(capBytes int64) *MemRing {
	if capBytes <= 0 {
		return nil
	}
	return &MemRing{
		buf: make([]byte, capBytes),
		cap: capBytes,
	}
}

// Cap returns the configured ring capacity in bytes. Used by the
// startup logger.
func (r *MemRing) Cap() int64 {
	if r == nil {
		return 0
	}
	return r.cap
}

// Append captures len(data) bytes at byte position pos. pos must
// equal r.tail (writes are sequential and gap-free). When the
// ring is full, oldest bytes are evicted to make room — eviction
// is implicit in the head/tail advance, no copying happens.
//
// Concurrency: writer-only call. Holds the write lock for the
// duration of the memcpy so concurrent readers either see the
// pre-Append head/tail or the post-Append head/tail, never a
// partial state.
func (r *MemRing) Append(pos int64, data []byte) {
	if r == nil || len(data) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	// Sanity: callers must feed bytes in stream order. A non-
	// matching pos would corrupt the ring's LSN→buffer mapping.
	// We don't panic — production is best-served by silently
	// resyncing (drop the in-flight Append, advance to pos),
	// because the alternative is killing the wal writer for what
	// is recoverable from disk anyway.
	if pos != r.tail {
		// Reset to the new position; the existing residence is
		// no longer trustworthy.
		r.head = pos
		r.tail = pos
	}

	// If data is bigger than the ring, only the trailing cap
	// bytes can fit. Drop the head portion — those bytes will
	// never be in RAM.
	if int64(len(data)) >= r.cap {
		offset := int64(len(data)) - r.cap
		copy(r.buf, data[offset:])
		r.tail = pos + int64(len(data))
		r.head = r.tail - r.cap
		return
	}

	// Fast path: data fits with the current resident range.
	// Wrap around the buffer: writeIdx = tail mod cap.
	writeIdx := r.tail % r.cap
	first := r.cap - writeIdx
	if first >= int64(len(data)) {
		copy(r.buf[writeIdx:], data)
	} else {
		copy(r.buf[writeIdx:], data[:first])
		copy(r.buf, data[first:])
	}
	r.tail += int64(len(data))
	if r.tail-r.head > r.cap {
		r.head = r.tail - r.cap
	}
}

// ReadAt fills out with bytes starting at byte position pos. The
// hit/miss decision is all-or-nothing: a partial overlap returns
// (0, false) so the caller fetches the whole range from disk in
// one go (avoids stitching two byte sources).
//
// Returns (len(out), true) on hit, (0, false) on miss. The hit
// path increments r.hits; misses increment r.misses. Both via
// atomic adds so the counters are visible to observability code
// without taking the ring's mutex.
//
// Concurrency: reader-only call. Holds the read lock during the
// memcpy. Multiple ReadAts can run in parallel; an Append blocks
// behind active readers (briefly — Append is a memcpy at most
// cap-bytes long).
func (r *MemRing) ReadAt(pos int64, out []byte) (int, bool) {
	if r == nil || len(out) == 0 {
		return 0, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	end := pos + int64(len(out))
	if pos < r.head || end > r.tail {
		r.misses.Add(1)
		return 0, false
	}

	readIdx := pos % r.cap
	first := r.cap - readIdx
	if first >= int64(len(out)) {
		copy(out, r.buf[readIdx:readIdx+int64(len(out))])
	} else {
		copy(out, r.buf[readIdx:r.cap])
		copy(out[first:], r.buf[:int64(len(out))-first])
	}
	r.hits.Add(1)
	return len(out), true
}

// Range returns (head, tail) — the LSN-byte range currently
// resident. Cheap snapshot; matches the same lock as ReadAt so a
// caller checking eligibility doesn't race an eviction.
func (r *MemRing) Range() (head, tail int64) {
	if r == nil {
		return 0, 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.head, r.tail
}

// BytesResident returns tail-head — the number of WAL bytes
// currently in the ring (always ≤ Cap). Surfaced by the
// pg_stat_wal_io view's `send_buffer_bytes_resident` column.
// Snapshot under RLock so it's coherent against an in-flight
// Append.
func (r *MemRing) BytesResident() int64 {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tail - r.head
}

// Hits returns the lifetime hit counter. M0010-0003's
// `pg_stat_replication.send_buffer_hits` reads this.
func (r *MemRing) Hits() uint64 {
	if r == nil {
		return 0
	}
	return r.hits.Load()
}

// Misses returns the lifetime miss counter. M0010-0003's
// `pg_stat_replication.send_buffer_misses` reads this.
func (r *MemRing) Misses() uint64 {
	if r == nil {
		return 0
	}
	return r.misses.Load()
}
