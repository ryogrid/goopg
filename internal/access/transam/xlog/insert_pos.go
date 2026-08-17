package xlog

import "sync"

// insertPosTracker is the PG-compat WAL insert position primitive. It
// atomically advances `curr` (the next-record start LSN) and `prev`
// (the most-recently-reserved record's start LSN, stamped into the
// NEXT record's `xl_prev`) as one indivisible action — matching PG's
// `ReserveXLogInsertLocation` (`postgres/src/backend/access/transam/
// xlog.c`), which holds `XLogCtl->Insert.insertpos_lck` over the
// (CurrBytePos, PrevBytePos) update.
//
// Foundation 4 for M0107-0007 slice B (Phase D4 — 8-stripe WAL insert
// locks per `docs/design/perf-optimize/07-wal-fsm-insert.md` §2).
// Earlier foundations — [[0107-0007i]] appendLockSet,
// [[0107-0007j]] buildSegmentPadRecord — landed first per the
// foundation-first pattern. The call-site rewrite that
// consumes this primitive (lifting `state.appendMu`'s four invariants
// into per-stripe local state) is multi-loop scope and is NOT in this
// loop.
//
// Why a mutex instead of a CAS pair? The (curr, prev) update has to be
// atomic together: a peer must never observe `curr` advanced past `X`
// without `prev` set to the start of the record that reserved `X`. Go
// has no native 128-bit CAS, so the choice is either a small mutex or
// a CAS-loop on a packed 64-bit value (limited to ~32-bit positions —
// too small for goopg's 64-bit LSNs). PG faces the same constraint and
// chooses a spinlock; we use `sync.Mutex` for the same reason. The
// uncontended cost (~10 ns) is dominated by the per-record encoding
// and buffer write that follow inside the stripe lock; the contention
// is bounded by the 8 stripe locks above this primitive in the lock
// order, so at most 8 backends ever race on the position mutex.
//
// Lock ordering for the future call-site rewrite:
//
//	appendLockSet.lockByProcNum  (one of 8 stripes)
//	  → insertPosTracker.reserve (always, briefly under posMu)
//	    → (rare, only on segment crossings) onCrossSegment hook,
//	      e.g. building an XLOG_NOOP pad record via
//	      [[0107-0007j]] buildSegmentPadRecord and emitting it
//	      into the WAL buffer.
//
// Returns:
//
//	start — the first LSN of the new reservation.
//	prev  — the start LSN of the immediately-prior reservation.
//	        Stored as a single field; not a per-reservation queue.
//	        Stamped verbatim into the new record's `xl_prev`
//	        header field by the caller.
//
// Segment-crossing semantics: a reservation that would straddle the next segment
// boundary is bumped to the start of the new segment; the gap
// `[oldCurr, boundary)` is reported to `onCrossSegment` so the caller
// can pad it with an XLOG_NOOP record (the gap's prev pointer is the
// returned `prev` value at the time of the crossing). The reservation
// that triggered the crossing then receives a `prev` equal to the
// pad record's start (i.e. `oldCurr`), preserving the xl_prev chain
// across the boundary.
type insertPosTracker struct {
	posMu   sync.Mutex
	curr    uint64
	prev    uint64
	segSize uint64
	// onCrossSegment returns true when it wrote a PAD RECORD over the gap and
	// false when the gap was only zero-filled (too small for a well-formed
	// record — M0131-S30.6). Only a real pad record advances `prev`.
	onCrossSegment func(start, boundary, prev uint64) (padded bool)
}

// newInsertPosTracker initialises a tracker. startCurr is the LSN
// where the next reservation will land; startPrev is the prev LSN
// to return on the first reservation (typically 0 for a fresh
// cluster, or the last-known prev recovered from segment scan on
// startup). segSize must be > 0. onCross may be nil; when set, it
// fires exactly once per crossing with payload (gapStart, boundary,
// gapPrev) where the gap covers `[gapStart, boundary)` and gapPrev
// is the prev pointer to stamp into the pad record.
func newInsertPosTracker(startCurr, startPrev, segSize uint64, onCross func(start, boundary, prev uint64) (padded bool)) *insertPosTracker {
	if segSize == 0 {
		panic("wal: insertPosTracker requires non-zero segSize")
	}
	return &insertPosTracker{
		curr:           startCurr,
		prev:           startPrev,
		segSize:        segSize,
		onCrossSegment: onCross,
	}
}

// load returns the current (curr, prev) snapshot without reserving
// anything. Used by observability surfaces and recovery; not for
// hot-path serialisation. The snapshot is taken under posMu so the
// two fields are mutually consistent.
func (t *insertPosTracker) load() (curr, prev uint64) {
	t.posMu.Lock()
	curr, prev = t.curr, t.prev
	t.posMu.Unlock()
	return curr, prev
}

// resetPosition force-sets (curr, prev) under posMu. Called by Path A
// of state.append after a direct-to-disk write bypasses stripe B so
// subsequent stripe-B Path B appends resume from the correct position.
// Must not be called while any stripe reservation is in flight.
func (t *insertPosTracker) resetPosition(curr, prev uint64) {
	t.posMu.Lock()
	t.curr = curr
	t.prev = prev
	t.posMu.Unlock()
}

// reserve atomically claims `size` bytes of LSN space and returns the
// starting LSN of the new reservation together with the prev pointer
// the caller must stamp into the record's xl_prev header field. The
// caller must materialise the range `[start, start+size)` before any
// later reserve reads beyond `start+size`.
//
// size must satisfy 0 < size <= segSize. Segment-spanning records
// must be split or padded by the caller before reserve is invoked;
// PG's `XLogInsertRecord` enforces the same upper bound upstream of
// `ReserveXLogInsertLocation`.
func (t *insertPosTracker) reserve(size uint64) (start, prev uint64) {
	if size == 0 || size > t.segSize {
		panic("wal: insertPosTracker.reserve: size out of range")
	}
	t.posMu.Lock()
	defer t.posMu.Unlock()
	return t.reserveLocked(size)
}

// reserveLocked is the body of reserve, assuming posMu is held. Shared
// by reserve and reserveAndPublish so the joint-atomicity rule (the
// (curr, prev) update happens together) lives in exactly one place.
// Callers MUST hold posMu.
func (t *insertPosTracker) reserveLocked(size uint64) (start, prev uint64) {
	old := t.curr
	end := old + size
	oldSeg := old / t.segSize
	endSeg := (end - 1) / t.segSize
	if oldSeg == endSeg {
		// Common case: reservation stays inside the current segment.
		prev = t.prev
		t.curr = end
		t.prev = old
		return old, prev
	}

	// Cross-segment slow path: bump to the new segment's start, fire
	// the onCross hook so the caller can pad the gap. The pad record
	// occupies `[old, boundary)`; its xl_prev is the current `prev`.
	// The actual reservation that triggered the crossing then lands
	// at `boundary` with xl_prev = old (the pad record's start).
	boundary := (oldSeg + 1) * t.segSize
	gapPrev := t.prev
	if t.onCrossSegment != nil {
		// Legacy (non-emitted) reservation path: `prev` is set to `old`
		// unconditionally below, so the padded/zero-filled distinction the
		// emitted path makes does not apply here.
		_ = t.onCrossSegment(old, boundary, gapPrev)
	}
	start = boundary
	prev = old
	t.curr = boundary + size
	t.prev = boundary
	return start, prev
}
