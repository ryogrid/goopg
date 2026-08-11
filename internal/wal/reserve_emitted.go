package wal

// reserveEmittedAndPublish jointly predicts the PG-compat emitted size
// of a `recordLen`-byte record at the current `curr` position, reserves
// that exact emitted byte count in LSN space, optionally handles a
// segment-boundary crossing by firing `onCrossSegment` for the gap, and
// publishes the stripe's active-LSN slot on `tracker` — all under a
// single `posMu` critical section.
//
// Foundation 18 for M0107-0007 slice B (Phase D4 — 8-stripe WAL insert
// locks per `docs/design/perf-optimize/07-wal-fsm-insert.md` §2).
//
// Closes the prediction race documented in
// `docs/design/0107-0007z-wal-predict-emitted-size.md` §"Out of scope":
//
//	// Racy two-step sequence (foundation 17 only):
//	curr, _   := core.Load()                                      // (A)
//	total, _  := predictEmittedSize(recordLen, curr, segSize)     // (B)
//	// ← peer stripe lands a reservation, advancing curr past
//	//   the value (A) observed — total is now stale for the
//	//   reservation we're about to make.
//	start, prev, err := core.AppendBuilt(procNum, total, build)   // (C)
//
// Between (A) and (C) a peer stripe with the same procNum-hash collision
// risk profile can land its own `reserveAndPublish` and shift `curr` by
// `total_peer` bytes. When our (C) finally takes `posMu`, the predicted
// `total` no longer matches the actual leading-header / contrecord
// layout at the (now-advanced) `curr`. Foundation 17 already plants the
// `errStripeAppendBuildSizeMismatch` defence in `stripeAppendBuild` so
// the failure mode is loud, but loud retry-on-mismatch is not free —
// the build closure has to be re-run, which on the slice B hot path
// would re-encode the record from scratch.
//
// Foundation 18's fix: do the prediction at the same `t.curr` that
// `reserveLocked` will consume, in the same critical section. A peer
// reservation cannot land between the predict and the reserve because
// both happen under `posMu`. The size mismatch becomes structurally
// impossible.
//
// Segment-crossing handling. Two distinct cases collapse under one
// algorithm:
//
//  1. The predicted emission `[t.curr, t.curr+total)` stays inside the
//     current segment. Reserve straightforwardly: `start = t.curr`,
//     `prev = t.prev`, advance `curr` and `prev`.
//
//  2. The predicted emission would straddle the next segment boundary.
//     Mirror `reserveLocked`'s slow path: fire `onCrossSegment` so a
//     pad record fills `[t.curr, boundary)`, then re-predict at
//     `boundary` (where the leading header is the 40 B long page
//     header). The reservation lands at `boundary` with `prev`
//     pointing at the pad record's start.
//
// The re-predict at the boundary is mandatory: predict-at-curr counted
// short/long page headers based on `curr`'s offset within its segment,
// but a reservation that lands at `boundary` instead has a different
// header schedule (long header up front, then short headers at every
// 8 KiB boundary). Foundation 17's `predictEmittedSize` walks pages
// using `pageHeaderSizeAt(pos, segSize)` which already returns the
// long header at segment boundaries — re-calling it at `boundary` is
// the natural way to express the boundary-aware layout.
//
// Contract.
//
//   - `recordLen > 0`. Empty records would panic inside
//     `reserveLocked` (size == 0) and have no useful WAL meaning.
//   - `tracker != nil`. Silent skip of publication would defeat the
//     foundation's purpose — callers of *this* method must have
//     observable race-closure semantics.
//   - `0 <= stripe < appendLockStripes`. Matches
//     `insertionTracker.setInsertingAt`.
//   - The emitted size at any reachable start position must be
//     `<= segSize`. Records that exceed this are out of scope (PG's
//     `XLogInsertRecord` enforces the same upper bound upstream;
//     goopg's call site applies `maxAlignXLog` long before reaching
//     here, so the emitted total is bounded by `recordLen` plus a
//     few hundred bytes of page-header overhead).
//
// Cost. One additional `predictEmittedSize` call per reservation
// (~tens of nanoseconds for typical records — a few loop iterations
// in a pure-arithmetic helper). Cross-segment reservations pay a
// second `predictEmittedSize` call; that path is rare (records
// straddling a 16 MiB segment boundary happen ~once per 16 MiB of
// WAL, dominated by the segment-rotation fsync cost). No new locks.
//
// PG counterpart. PG's `XLogInsertRecord`
// (`postgres/src/backend/access/transam/xlog.c`) computes the
// `actualBytes` for header insertion *before* calling
// `ReserveXLogInsertLocation` — but it does so while holding the
// per-backend WAL insert lock, which is the same primitive that
// gates `ReserveXLogInsertLocation`. The net effect is identical to
// foundation 18: prediction and reservation happen under the same
// serialising primitive so no peer can advance `curr` between the
// two.
//
// Returns.
//
//	start    — first LSN byte of the new reservation
//	          (== t.curr at entry on the common path; == boundary
//	          on the cross-segment path).
//	prev     — start LSN of the immediately-prior reservation;
//	          stamped verbatim into `xl_prev` of the record the
//	          caller is about to encode.
//	total    — total emitted byte count (record body + page
//	          headers + contrecord headers). Equals the LSN range
//	          consumed by the reservation: `[start, start+total)`.
//	leading  — size of the leading page header at `start`
//	          (0 if `start` is mid-page; 24 if at a page boundary;
//	          40 if at a segment boundary). Caller uses this to
//	          drive the first chunk-header emit before the record
//	          body.
// xlogMinimumRecordSize is the smallest valid WAL record: SizeOfXLogRecord
// (24 bytes) with no payload and no chunk header. A gap before a segment
// boundary must be ≥ this value to fit a well-formed XLOG_NOOP pad record.
const xlogMinimumRecordSize = SizeOfXLogRecord // 24 bytes

func (t *insertPosTracker) reserveEmittedAndPublish(
	recordLen int, stripe int, tracker *insertionTracker,
) (start, prev uint64, total, leading int) {
	if recordLen <= 0 {
		panic("wal: insertPosTracker.reserveEmittedAndPublish: recordLen must be > 0")
	}
	if tracker == nil {
		panic("wal: insertPosTracker.reserveEmittedAndPublish: nil tracker")
	}
	if stripe < 0 || stripe >= appendLockStripes {
		panic("wal: insertPosTracker.reserveEmittedAndPublish: stripe out of range")
	}

	t.posMu.Lock()
	defer t.posMu.Unlock()

	segSize := t.segSize
	startCandidate := t.curr
	boundary := (startCandidate/segSize + 1) * segSize

	total, leading = predictEmittedSize(recordLen, int64(startCandidate), int64(segSize))
	if uint64(total) > segSize {
		panic("wal: insertPosTracker.reserveEmittedAndPublish: emitted size exceeds segSize at curr")
	}

	if startCandidate+uint64(total) > boundary {
		gapLen := boundary - startCandidate
		gapPrev := t.prev
		if gapLen < xlogMinimumRecordSize {
			// Gap is too small to fit a valid XLOG_NOOP record (minimum
			// xlogMinimumRecordSize bytes). Skip the gap silently — those
			// bytes remain zeroed in the pre-allocated segment file and WAL
			// recovery treats them as end-of-segment. The xl_prev chain is
			// preserved by keeping t.prev unchanged so the new record at
			// boundary links directly to the last valid record before the gap.
			// PG avoids this case via page-alignment invariants on record sizes;
			// goopg handles it here as defence-in-depth.
		} else {
			// Normal cross-segment slow path: pad the gap [startCandidate, boundary)
			// via the onCrossSegment hook (matches reserveLocked's semantics),
			// then re-predict at boundary so the reservation lands with a
			// boundary-correct header schedule.
			if t.onCrossSegment != nil {
				t.onCrossSegment(startCandidate, boundary, gapPrev)
			}
			// NOTE (M0131-S30.1b, deferred): xl_prev names a record's CONTENT
			// start (see `t.prev = start + leading` below), so when the pad
			// itself lands on a page boundary this should be
			// `startCandidate + padLeading`. Deferral-ledger row 2026-08-12.
			t.prev = startCandidate
		}
		startCandidate = boundary
		total, leading = predictEmittedSize(recordLen, int64(startCandidate), int64(segSize))
		if uint64(total) > segSize {
			panic("wal: insertPosTracker.reserveEmittedAndPublish: emitted size exceeds segSize at boundary")
		}
	}

	start = startCandidate
	prev = t.prev
	t.curr = start + uint64(total)
	t.prev = start + uint64(leading) // record-CONTENT start: skip leading PHD bytes

	tracker.setInsertingAt(stripe, int64(start))

	return start, prev, total, leading
}
