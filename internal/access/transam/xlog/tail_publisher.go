package xlog

import "sync/atomic"

// tailPublisher is the publication-watermark primitive for slice B's
// 8-stripe WAL insert path. It computes the "safe tail" — the highest
// LSN below which every reservation has been fully written into the
// WAL buffer — and monotonically advances a published watermark so
// downstream consumers (walBuffer drain, MemRing readers, replication)
// observe contiguous, fully-initialised bytes.
//
// Foundation 7 for M0107-0007 slice B (Phase D4 — 8-stripe WAL insert
// locks per `docs/design/perf-optimize/07-wal-fsm-insert.md` §2).
// Earlier foundations — [[0107-0007i]] appendLockSet,
// [[0107-0007j]] buildSegmentPadRecord, [[0107-0007k]]
// insertPosTracker, [[0107-0007l]] walBuffer.writeReserved, and
// [[0107-0007m]] insertionTracker — landed first per the
// foundation-first pattern. The call-site rewrite that mounts this
// publisher on `Writer` and drives it from the drain/flush path is
// multi-loop scope and is NOT in this loop.
//
// PG counterpart. PG does not have a dedicated "tail publisher" type;
// the same role is split across two collaborating pieces in
// `postgres/src/backend/access/transam/xlog.c`:
//
//   - `WaitXLogInsertionsToFinish(upTo)` walks the per-stripe
//     `insertingAt` array and busy-waits until every stripe finishes
//     its in-flight insert below `upTo`. After the wait it returns the
//     observed watermark.
//   - `XLogCtl->LogwrtRqst.Write` (and a few neighbouring atomics)
//     hold the monotonic write-side watermark that flush coordinators
//     advance.
//
// goopg merges the two roles into one primitive because Go's
// `atomic.Int64.CompareAndSwap` makes the monotonic-publish loop
// trivial and because the wait-vs-poll policy is owned by the caller
// (the drain loop already has its own scheduling discipline), so the
// publisher is non-blocking by construction.
//
// Why monotonic publication. Two concurrent calls to `publishUpTo`
// may observe different `lowestActiveLSN` values depending on which
// stripes happened to be idle when each call read the tracker. Without
// a monotonic published watermark, an earlier call observing a HIGHER
// safe tail (because no stripes were active) could be followed by a
// later call observing a LOWER safe tail (because a stripe started
// inserting at a lower LSN in between), which would publish a
// watermark that goes backwards — exposing readers to bytes that were
// previously declared written but now might be racing with a new
// insert below the old watermark. The CAS-loop closes this by
// publishing only forward; a candidate watermark below the current
// published value is silently dropped.
//
// Why "candidate ≤ current" early returns the current value (not
// candidate). Callers want to know "what was published as a result of
// this call attempt" so they can use that watermark to drive their
// downstream step (e.g., drain up to N bytes). Returning `current`
// when the candidate did not advance preserves the invariant "the
// returned LSN is published and safe to read up to" — the caller can
// always use the return value as a safe upper bound without an
// independent `Load`.
//
// Lock-ordering tier for the future call-site rewrite. The publisher
// holds no locks; it composes with the slice B stripe chain as a leaf
// reader:
//
//	appendLockSet.lockByProcNum  (one of 8 stripes)
//	  → insertPosTracker.reserve  (briefly under posMu)
//	    → insertionTracker.setInsertingAt(stripe, start)
//	      → walBuffer.writeReserved
//	    → insertionTracker.setInsertingAt(stripe, lsnIdle)
//	  → drop stripe lock
//
//	(separately, from the drain/flush goroutine, after the above:)
//	  tailPublisher.publishUpTo(upperBound, insertionTracker)
//	  walBuffer.advanceHead(published - prior)
//
// Pre-reserve race carry-over. The race window documented in
// [[0107-0007m]] §"Pre-reserve race" — between
// `insertPosTracker.reserve` returning and the matching
// `insertionTracker.setInsertingAt(stripe, start)` — can momentarily
// raise the observed `lowestActiveLSN` above the true minimum (the
// reservation has advanced `curr` but the stripe slot is still
// `lsnIdle`, so `lowestActiveLSN` does not see it yet). The publisher
// cannot close this race by itself; it is the call-site rewrite's
// responsibility to either (a) move `setInsertingAt` under `posMu`
// so it is sequenced with the reserve, or (b) emit a pre-reserve
// marker. The publisher's contract is "given an honest
// `(upperBound, tracker)` pair, compute and monotonically publish the
// safe tail"; resolving the pre-reserve race is upstream.
type tailPublisher struct {
	// published is the highest LSN observed to be safe (i.e., every
	// reservation strictly below it has been fully byte-written by
	// its owning stripe). Monotonically advances under CAS in
	// publishUpTo; never decreases.
	published atomic.Int64
}

// newTailPublisher returns a publisher with watermark 0. The zero
// value of `atomic.Int64` is already 0, so the constructor exists
// only to match the foundation pattern (newInsertionTracker,
// newInsertPosTracker, newWALBuffer) and to centralise any future
// per-construction invariants (e.g., starting the watermark at a
// recovery-resume LSN).
func newTailPublisher() *tailPublisher {
	return &tailPublisher{}
}

// load returns the current published watermark. Lock-free single
// atomic load; safe to call concurrently with publishUpTo.
func (p *tailPublisher) load() int64 {
	return p.published.Load()
}

// publishUpTo computes the safe-tail candidate as
//
//	safeTail = min(upperBound, tracker.lowestActiveLSN())
//
// and CAS-loops to advance the published watermark monotonically.
// Returns `min(currentPublishedWatermark, upperBound)` — i.e., the
// caller's safe upper bound for reading from the WAL buffer. This
// cap matches PG's `WaitXLogInsertionsToFinish(upTo)` return
// contract: the result never exceeds the caller's request, even if
// a peer publisher has already advanced the internal watermark
// past the caller's upperBound. The internal watermark itself is
// monotonic; only the cap at return time reflects the caller's
// narrower request.
//
// `upperBound` is the highest LSN that has been reserved (typically
// the `curr` field of `insertPosTracker.load()`) — by construction
// every reservation strictly below it has at least entered its
// stripe's setInsertingAt→writeReserved→setInsertingAt(idle)
// sequence.
//
// `tracker.lowestActiveLSN()` returns the smallest start LSN of any
// currently-active insertion, or `lsnNoActive` (math.MaxInt64) when
// every stripe is idle. The sentinel composes cleanly with the min:
// when no stripe is active, `safeTail = upperBound`.
//
// nil receiver is safe: returns 0 (no watermark established). This
// matches the defensive contract used elsewhere in foundation code
// and lets future Writer constructors leave `tailPublisher` unset
// under `Config.WALBuffers == 0`.
//
// nil tracker is safe: behaves as if every stripe were idle, so
// `safeTail = upperBound`. Useful for tests and for transitional
// state during call-site rewrite when the tracker is wired in
// after the publisher.
//
// Concurrent safety: multiple callers may invoke publishUpTo
// concurrently. The CAS loop ensures monotonicity; the return value
// is always ≥ the value any prior call observed as published.
func (p *tailPublisher) publishUpTo(upperBound int64, tracker *insertionTracker) int64 {
	if p == nil {
		return 0
	}
	candidate := upperBound
	if tracker != nil {
		active := tracker.lowestActiveLSN()
		if active < candidate {
			candidate = active
		}
	}
	for {
		cur := p.published.Load()
		if candidate <= cur {
			// Cap the return at the caller's upperBound so a peer
			// that pushed the internal watermark past upperBound
			// does not leak that information back as a safe LSN
			// the caller might try to drain past their reservation
			// view.
			if cur > upperBound {
				return upperBound
			}
			return cur
		}
		if p.published.CompareAndSwap(cur, candidate) {
			return candidate
		}
	}
}
