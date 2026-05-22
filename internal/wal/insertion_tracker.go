package wal

import (
	"math"
	"sync/atomic"
)

// lsnIdle is the sentinel stored in an insertionTracker slot when a
// stripe is not currently inserting a record. Zero is the natural
// choice (all-zero state is "idle"); call sites that legitimately
// reserve LSN 0 do not exist in production paths (LSN 0 is the
// invalid sentinel throughout PG and goopg).
const lsnIdle = int64(0)

// lsnNoActive is the return value of insertionTracker.lowestActiveLSN
// when every stripe is idle. math.MaxInt64 is a deliberate choice so
// callers can compute `tail = min(upperBound, lowestActive)` without
// branching for the "no active" case — every concrete LSN compares
// lower than the sentinel.
const lsnNoActive = int64(math.MaxInt64)

// insertionTracker records the per-stripe insertion-in-progress LSN
// for slice B's 8-stripe WAL insert path. Mirrors PG's
// `WALInsertLock[i].insertingAt` field in
// `postgres/src/backend/access/transam/xlog.c` (line drifts between
// minor versions; the symbol is the anchor), set under the stripe
// lock via `WALInsertLockUpdateInsertingAt` and read by
// `WaitXLogInsertionsToFinish` when the flush coordinator needs to
// publish `XLogCtl->LogwrtRqst.Write` past pending insertions.
//
// Foundation 6 for M0107-0007 slice B (Phase D4 — 8-stripe WAL insert
// locks per `docs/design/perf-optimize/07-wal-fsm-insert.md` §2).
// Earlier foundations — [[0107-0007i]] appendLockSet,
// [[0107-0007j]] buildSegmentPadRecord, [[0107-0007k]]
// insertPosTracker, [[0107-0007l]] walBuffer.writeReserved — landed
// first per the foundation-first pattern. The call-site rewrite that
// consumes this primitive (advancing the walBuffer.tail watermark
// past concurrent stripe writes) is multi-loop scope and is NOT in
// this loop.
//
// Why it exists. [[0107-0007l]] (walBuffer.writeReserved) deliberately
// does not advance tail because doing so under concurrent stripe
// writes is unsound: tail must not advance past LSN X until every
// reservation strictly below X has had its bytes written into the
// buffer by its owning stripe. This tracker is the data structure
// that lets a publication walker compute "the highest LSN such that
// no in-flight stripe is writing below it" — i.e., where tail can
// safely advance to.
//
// Lock-ordering tier for the future call-site rewrite:
//
//	appendLockSet.lockByProcNum  (one of 8 stripes)
//	  → insertPosTracker.reserve  (briefly under posMu)
//	    → insertionTracker.setInsertingAt(stripe, start)
//	      → (rare) buildSegmentPadRecord
//	      → walBuffer.writeReserved
//	    → insertionTracker.setInsertingAt(stripe, lsnIdle)
//
// The tracker itself takes no locks: each stripe writes only to its
// own slot, and publication walkers Load-only across all slots. The
// caller is responsible for ordering `setInsertingAt(stripe, start)`
// before the byte write and `setInsertingAt(stripe, lsnIdle)` after,
// which it does naturally by holding the stripe lock across both.
//
// Concurrent model. The atomic per-slot Store + Load gives the
// sequenced-before relationship Go's memory model requires: a
// publication walker that loads `inserting[i] == lsnIdle` is
// guaranteed (per the happens-before rule of atomic load-acquire vs
// the matching store-release) to also observe all byte writes that
// occurred before that store on the owning stripe. Without atomics,
// a relaxed read could observe `lsnIdle` while still reading stale
// pre-write bytes from the buffer; with atomic.Int64 Load/Store the
// pairing is sequentially consistent on amd64/arm64.
//
// Per-stripe contract:
//
//  1. Stripe takes appendLockSet.lockByProcNum(procNum). The stripe
//     index is `stripeForProcNum(procNum)`.
//  2. Stripe calls insertPosTracker.reserve(size) → (start, prev).
//  3. Stripe calls setInsertingAt(stripe, int64(start)) BEFORE the
//     byte write. This is the publication-blocking step: until this
//     call returns, the tracker may report `lsnNoActive` for this
//     stripe even though a reservation has already advanced curr.
//     The race window between reserve returning and setInsertingAt
//     completing is closed at the call-site rewrite layer (see the
//     "future-loop concern" note in 0107-0007m.md §"Pre-reserve race").
//  4. Stripe calls walBuffer.writeReserved(start, bytes).
//  5. Stripe calls setInsertingAt(stripe, lsnIdle).
//  6. Stripe drops the stripe lock.
type insertionTracker struct {
	// inserting[i] is the start LSN of the record currently being
	// written by stripe i, or lsnIdle when the stripe is not in
	// the middle of an insert. atomic.Int64 (not uint64) so the
	// signed comparison in lowestActiveLSN does not need a cast.
	inserting [appendLockStripes]atomic.Int64
}

// newInsertionTracker returns an idle tracker — every stripe slot is
// lsnIdle. The zero value of atomic.Int64 is 0 == lsnIdle, so this
// is structurally a zero-state struct; the constructor exists only
// to match the foundation pattern (newInsertPosTracker,
// newWALBuffer, etc.) and to centralise any future per-construction
// invariants.
func newInsertionTracker() *insertionTracker {
	return &insertionTracker{}
}

// setInsertingAt publishes that stripe i is currently inserting a
// record starting at lsn. Pass lsnIdle (== 0) to mark the stripe
// idle once the byte write is complete. The caller MUST hold the
// matching stripe lock (`appendLockSet.locks[i]`) for the duration
// of the begin/end pair; the tracker itself does no validation
// because the stripe lock already serialises begin/end on a single
// slot.
//
// Panics if stripe is out of range. We deliberately panic rather
// than error: a wrong stripe index is a programmer bug at the
// call-site rewrite layer, not a runtime condition, and panicking
// surfaces the bug at the first test that exercises the wrong
// index instead of corrupting the publication walker's view.
func (t *insertionTracker) setInsertingAt(stripe int, lsn int64) {
	if stripe < 0 || stripe >= appendLockStripes {
		panic("wal: insertionTracker.setInsertingAt: stripe out of range")
	}
	t.inserting[stripe].Store(lsn)
}

// insertingAt returns stripe i's current insertion LSN, or lsnIdle
// if it is not inserting. Exposed for tests, diagnostics, and
// targeted publication queries; the bulk publication walker uses
// lowestActiveLSN.
func (t *insertionTracker) insertingAt(stripe int) int64 {
	if stripe < 0 || stripe >= appendLockStripes {
		panic("wal: insertionTracker.insertingAt: stripe out of range")
	}
	return t.inserting[stripe].Load()
}

// lowestActiveLSN returns the smallest non-idle insertion LSN
// across all stripes, or lsnNoActive (math.MaxInt64) when every
// stripe is idle. A publication walker computes the safe tail
// watermark as:
//
//	safeTail = min(upperBound, lowestActiveLSN())
//
// where upperBound is the highest LSN known to be reserved (from
// insertPosTracker.load). Because the sentinel is MaxInt64, the
// min collapses cleanly to upperBound when no stripe is busy.
//
// The walker is sequentially consistent across the 8 atomic loads
// on platforms where atomic.Int64.Load is SC (amd64, arm64). On
// weaker memory models the loads are still per-slot acquire, so
// the "no observation of lsnIdle without observing the preceding
// byte writes" invariant holds per-stripe; cross-stripe ordering
// (which stripe finished first) is not required for tail safety —
// safety depends only on the per-stripe pair.
func (t *insertionTracker) lowestActiveLSN() int64 {
	lowest := lsnNoActive
	for i := range t.inserting {
		v := t.inserting[i].Load()
		if v != lsnIdle && v < lowest {
			lowest = v
		}
	}
	return lowest
}
