package wal

import "errors"

// walBuffer is the bounded in-memory WAL buffer introduced by
// M0013-0001 (see docs/design/0013-0001-wal-buffers-architecture.md).
// It sits between state.append and state.writeAt: appended records
// land in RAM until either (a) the next append would overflow the
// buffer (overflow drain runs first to make room) or (b) flushUpTo
// demands a byte ≤ targetLSN be on disk (Stage 1 drains the buffer
// up to targetLSN, Stage 2 fdatasyncs the touched segments).
//
// The buffer is byte-addressed by absolute LSN: its head/tail are
// LSN-relative pointers, not slice offsets. The drainedLSN watermark
// matches the buffer's logical head so a flushUpTo can compute "how
// much do I need to drain" by `targetLSN - drainedLSN`.
//
// All methods run on the writer goroutine — no locking. The
// walsender-side MemRing (M0010-0002) is a separate, concurrent
// data structure that mirrors every successful append.
type walBuffer struct {
	cap  int64  // fixed at construction; equals Config.WALBuffers
	buf  []byte // ring backing storage; len == cap
	base int64  // LSN that buf[0] currently represents (in bytes)
	head int64  // first un-drained byte LSN; head ≥ base
	tail int64  // first unwritten byte LSN; tail ≥ head
}

// newWALBuffer returns a new buffer with the given capacity.
// cap ≤ 0 returns nil — callers must guard against it (the
// state.append fast-path checks `s.walBuf == nil` before use).
func newWALBuffer(cap int64) *walBuffer {
	if cap <= 0 {
		return nil
	}
	return &walBuffer{cap: cap, buf: make([]byte, cap)}
}

// reset re-anchors the buffer to a new starting LSN. Used at
// startup when detectWritePos discovers we're restarting partway
// through a segment — the buffer is empty, head=tail=base=startLSN.
func (b *walBuffer) reset(startLSN int64) {
	b.base = startLSN
	b.head = startLSN
	b.tail = startLSN
}

// resident returns the number of bytes currently held in the
// buffer (waiting to be drained to segments).
func (b *walBuffer) resident() int64 { return b.tail - b.head }

// free returns the number of bytes that could be appended
// without forcing a drain.
func (b *walBuffer) free() int64 { return b.cap - b.resident() }

// canHold reports whether `n` bytes fit in the buffer at all
// (independent of whether a drain would be needed first).
func (b *walBuffer) canHold(n int) bool { return int64(n) <= b.cap }

// append copies `record` into the buffer at position `tail` and
// advances tail. The caller must guarantee free() ≥ len(record)
// (i.e. drained any overflow first). Wraparound is handled
// byte-wise.
func (b *walBuffer) append(record []byte) {
	off := (b.tail - b.base) % b.cap
	n := int64(len(record))
	first := b.cap - off
	if first >= n {
		copy(b.buf[off:off+n], record)
	} else {
		copy(b.buf[off:b.cap], record[:first])
		copy(b.buf[0:n-first], record[first:])
	}
	b.tail += n
}

// readForDrain returns up to `n` bytes of the head of the buffer
// (the next bytes to be drained to segments). The returned slice
// is owned by the buffer until advanceHead is called — callers
// must consume / write it before another buffer mutation.
//
// Returns two slices to handle wrap: the second is empty if the
// requested range doesn't cross the underlying ring boundary.
// Caller writes them in order.
func (b *walBuffer) readForDrain(n int64) (a, c []byte) {
	if n <= 0 {
		return nil, nil
	}
	if n > b.resident() {
		n = b.resident()
	}
	off := (b.head - b.base) % b.cap
	first := b.cap - off
	if first >= n {
		return b.buf[off : off+n], nil
	}
	return b.buf[off:b.cap], b.buf[0 : n-first]
}

// advanceHead is called after readForDrain's bytes have been
// successfully written to a segment file. Advances head and, when
// head crosses a buffer-cap boundary, slides base forward so the
// modulo arithmetic stays well-defined.
func (b *walBuffer) advanceHead(n int64) {
	b.head += n
	// Slide base forward by full-cap multiples so head ∈
	// [base, base+cap]. Avoids unbounded modulo growth on
	// long-running writers.
	if b.head-b.base >= b.cap {
		k := (b.head - b.base) / b.cap
		b.base += k * b.cap
	}
}

// readAt copies up to len(out) bytes starting at absolute byte position pos
// from the resident [head, tail) range and returns the number of bytes copied.
// Caller is responsible for synchronization.
func (b *walBuffer) readAt(pos int64, out []byte) int {
	if b == nil || len(out) == 0 || pos < b.head || pos >= b.tail {
		return 0
	}
	avail := int(b.tail - pos)
	if avail > len(out) {
		avail = len(out)
	}
	off := (pos - b.base) % b.cap
	first := b.cap - off
	if first >= int64(avail) {
		copy(out[:avail], b.buf[off:off+int64(avail)])
	} else {
		copy(out[:first], b.buf[off:b.cap])
		copy(out[first:avail], b.buf[:int64(avail)-first])
	}
	return avail
}


// errWALBufferNil is returned by writeReserved when invoked on a nil
// receiver — callers normally guard with `s.walBuf != nil` (the
// state.append fast-path does so), but the explicit error keeps the
// 8-stripe slice B call-site rewrite from segfaulting if the buffer
// happens to be disabled (Config.WALBuffers == 0).
var errWALBufferNil = errors.New("wal: writeReserved on nil buffer")

// errWALBufferReservedOutOfRange is returned by writeReserved when
// the requested LSN range [lsn, lsn+len(record)) falls outside the
// ring's currently-mapped LSN window [base, base+cap). The caller's
// LSN reserve (insert_pos.go's insertPosTracker.reserve) is supposed
// to keep reservations inside the window — this error catches a
// contract violation rather than handling overflow gracefully.
var errWALBufferReservedOutOfRange = errors.New("wal: writeReserved range outside buffer window")

// writeReserved copies `record` into the ring at the offset
// corresponding to absolute byte LSN `lsn`, without mutating head,
// tail, or base. This is the bytes-write counterpart to the
// 8-stripe slice B path's LSN reserve ([[0107-0007k]]
// insertPosTracker): a stripe holds appendLocks[i], reserves
// [lsn, lsn+n) atomically, then lands bytes here while peer
// stripes write into disjoint ranges in parallel.
//
// PG counterpart: CopyXLogRecordToWAL (xlog.c) writes into the
// shared XLogCtl->pages buffer at the offset reserved by the
// preceding ReserveXLogInsertLocation call. PG's WALInsertLocks
// serialise writes WITHIN a stripe's LSN range; ACROSS stripes
// the writes proceed concurrently because the LSN reservations
// are disjoint.
//
// Errors:
//   - errWALBufferNil if the receiver is nil.
//   - errWALBufferReservedOutOfRange if [lsn, lsn+len(record))
//     extends below base or past base+cap.
//
// Empty record is a no-op (returns nil without touching the
// ring) — this matches state.appendRaw's len==0 short-circuit.
//
// Concurrent safety: two writers writing into disjoint LSN
// ranges of the same walBuffer are safe — Go's memory model
// permits concurrent writes to disjoint byte slice regions.
// Two writers writing into overlapping ranges is a contract
// violation by the caller (insertPosTracker guarantees disjoint
// reservations) and produces undefined byte contents.
//
// The caller is responsible for advancing `tail` (separately)
// only after every reservation strictly below the new tail has
// actually been written by its owning stripe — otherwise
// readers (readForDrain, readAt) would observe uninitialised
// bytes in the gap. That tail-publication step is a separate
// slice B foundation; this primitive only writes bytes.
func (b *walBuffer) writeReserved(lsn int64, record []byte) error {
	if b == nil {
		return errWALBufferNil
	}
	if len(record) == 0 {
		return nil
	}
	n := int64(len(record))
	if lsn < b.base || lsn+n > b.base+b.cap {
		return errWALBufferReservedOutOfRange
	}
	off := (lsn - b.base) % b.cap
	first := b.cap - off
	if first >= n {
		copy(b.buf[off:off+n], record)
	} else {
		copy(b.buf[off:b.cap], record[:first])
		copy(b.buf[0:n-first], record[first:])
	}
	return nil
}
