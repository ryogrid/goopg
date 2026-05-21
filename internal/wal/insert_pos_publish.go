package wal

// reserveAndPublish is the joint-atomic reserve + stripe-publish
// primitive that closes the pre-reserve race documented in
// `docs/design/0107-0007m-wal-insertion-tracker.md` §"Pre-reserve race"
// and re-cited in `docs/design/0107-0007n-wal-tail-publisher.md` and
// `docs/design/0107-0007o-wal-mem-ring-write-reserved.md`.
//
// Foundation 9 for M0107-0007 slice B (Phase D4 — 8-stripe WAL insert
// locks per `docs/design/perf-optimize/07-wal-fsm-insert.md` §2).
// Foundations 1–8 — [[0107-0007h]] lsnAllocator, [[0107-0007i]]
// appendLockSet, [[0107-0007j]] buildSegmentPadRecord, [[0107-0007k]]
// insertPosTracker, [[0107-0007l]] walBuffer.writeReserved,
// [[0107-0007m]] insertionTracker, [[0107-0007n]] tailPublisher,
// [[0107-0007o]] MemRing.WriteReserved/PublishUpTo — landed first
// per the foundation-first pattern. The call-site rewrite that
// consumes this primitive (mounting it on `Writer.Append`) is
// multi-loop scope and is NOT in this loop.
//
// The race. Until this foundation lands, the slice B contract is:
//
//	start, prev := insertPosTracker.reserve(size)   // (1) curr advanced
//	// ← race window: reader sees curr advanced
//	//                but insertionTracker still idle for this stripe
//	insertionTracker.setInsertingAt(stripe, start)  // (2) publish
//
// A drain reader that calls `tailPublisher.publishUpTo(upperBound,
// tracker)` between (1) and (2) observes `lowestActiveLSN ==
// lsnNoActive` for an in-flight reservation, advancing the published
// watermark past the reservation's bytes — a soundness bug that
// would let `walBuffer.advanceHead` or `MemRing.PublishUpTo` reclaim
// ring slots that an unfinished `writeReserved` is still memcpying
// into.
//
// The closure (option A). Move (1) and (2) under the same `posMu`
// critical section so they appear to all observers as a single
// indivisible action. Any code path that takes `posMu` afterwards
// (notably `insertPosTracker.load`, used by drain to obtain
// `upperBound`) is guaranteed to observe the publication, and the
// `setInsertingAt` Store is sequenced-before the `posMu.Unlock` that
// releases the lock. PG implements the same pattern: its
// `ReserveXLogInsertLocation` is followed inside the same WAL insert
// lock by `WALInsertLockUpdateInsertingAt`.
//
// Why not option B (pre-reserve marker). Option B writes a floor LSN
// to the stripe slot *before* calling `reserve`, then refines it to
// the exact start after reserve returns. This works but introduces a
// second atomic write per insert (vs zero additional writes here,
// since the `setInsertingAt` would have happened anyway), and the
// floor LSN must be conservative enough to never falsely block
// publication — picking it requires the caller to know `curr` ahead
// of time, which defeats the point of having `posMu` be authoritative
// over `curr`. Option A composes more cleanly with the existing
// primitive's lock geometry.
//
// Cost. One additional `atomic.Int64.Store` under the existing posMu
// critical section. The Store does not extend the critical section
// meaningfully (it is a single instruction on amd64/arm64); the cost
// is dominated by the existing posMu Lock/Unlock pair. No new locks
// introduced.
//
// Contract.
//
//   - size must satisfy `0 < size <= segSize`. Matches `reserve`.
//   - tracker MUST be non-nil. If the call site has no insertion
//     tracker (e.g. legacy non-striped path), it should call
//     `reserve` instead. Panics on nil rather than silently
//     skipping publication, because callers of *this* method must
//     have observable race-closure semantics — silently degrading
//     to the racy path would defeat the foundation's purpose.
//   - stripe must satisfy `0 <= stripe < appendLockStripes`.
//     Matches `insertionTracker.setInsertingAt`'s panic semantics.
//
// Sequencing for callers.
//
//	start, prev := tracker_pos.reserveAndPublish(size, stripe, tracker_insert)
//	// (bytes write happens here — walBuffer.writeReserved + MemRing.WriteReserved)
//	tracker_insert.setInsertingAt(stripe, lsnIdle)
//
// The end-of-write `setInsertingAt(stripe, lsnIdle)` is NOT part of
// this primitive — it remains a separate call by the caller, sequenced
// after the byte write. Closing the race only requires sealing the
// BEGIN side under `posMu`; the END side has a natural happens-before
// chain through the stripe lock that the caller holds across the
// whole insert.
//
// Cross-segment crossings publish the new reservation's start
// (the post-boundary LSN), NOT the gap's start. This matches the
// xl_prev chain: the pad record at `[gap, boundary)` is fed via the
// `onCrossSegment` hook (which the caller drains by writing the pad
// record's bytes into the buffer), and the reservation that triggered
// the crossing lands at `boundary` with `prev = gap`. The
// insertionTracker stripe slot is interested in *the reservation's*
// LSN — the pad record itself is not a stripe reservation; it is a
// gap-fill emitted synchronously under `posMu`.
func (t *insertPosTracker) reserveAndPublish(size uint64, stripe int, tracker *insertionTracker) (start, prev uint64) {
	if size == 0 || size > t.segSize {
		panic("wal: insertPosTracker.reserveAndPublish: size out of range")
	}
	if tracker == nil {
		panic("wal: insertPosTracker.reserveAndPublish: nil tracker")
	}
	if stripe < 0 || stripe >= appendLockStripes {
		panic("wal: insertPosTracker.reserveAndPublish: stripe out of range")
	}
	t.posMu.Lock()
	defer t.posMu.Unlock()
	start, prev = t.reserveLocked(size)
	tracker.setInsertingAt(stripe, int64(start))
	return start, prev
}
