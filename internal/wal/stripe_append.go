package wal

import "errors"

// stripeAppend is the writer-side append composer for M0107-0007 slice B
// (Phase D4 — 8-stripe WAL insert locks per
// `docs/design/perf-optimize/07-wal-fsm-insert.md` §2). It performs one
// stripe-locked append in the exact order the slice B drain/writer
// contract requires:
//
//  1. Select stripe by procNum and take the stripe lock from
//     [[0107-0007i]] `appendLockSet`.
//  2. Reserve `len(record)` LSN bytes and publish the stripe's active
//     LSN under `posMu` via [[0107-0007p]] `reserveAndPublish` — this
//     closes the pre-reserve race documented in [[0107-0007m]]
//     §"Pre-reserve race" by sealing the (curr advance, stripe publish)
//     pair as one indivisible step.
//  3. Land bytes into the bytes-side ring via [[0107-0007l]]
//     `walBuffer.writeReserved` (nil-safe via per-receiver guard so the
//     composer works under `Config.WALBuffers == 0`).
//  4. Land bytes into the walsender mirror via [[0107-0007o]]
//     `MemRing.WriteReserved` (nil-safe).
//  5. Publish END marker — [[0107-0007m]] `insertionTracker.setInsertingAt`
//     to `lsnIdle` — so the drain-side [[0107-0007n]] `tailPublisher`
//     stops capping the safe tail at this stripe's start LSN.
//  6. Release the stripe lock.
//
// stripeAppend is the writer-side symmetric counterpart of [[0107-0007t]]
// `publishVisibility`: stripeAppend writes bytes into both rings under
// the stripe lock without advancing visibility; publishVisibility
// advances visibility in both rings without writing bytes. The two
// composers together cover the full slice B write/publish lifecycle so
// the call-site rewrite can install each as a one-liner at its natural
// site (stripeAppend at the per-record write entry point; publishVisibility
// inside the drain goroutine's per-tick loop).
//
// Lock-ordering tier — matches [[0107-0007t]]'s "stripe writer" chain:
//
//	stripeAppend(locks, posTracker, insertTracker, walBuf, memRing, procNum, record)
//	  → appendLockSet.lockByProcNum            (one of 8 stripes acquired)
//	    → insertPosTracker.reserveAndPublish   (posMu held: reserveLocked +
//	                                            insertionTracker.setInsertingAt(start);
//	                                            posMu released)
//	        → (rare, only on segment crossings, fired synchronously under
//	           posMu) insertPosTracker.onCrossSegment → e.g.
//	           [[0107-0007s]] emitSegmentPad → buildSegmentPadRecord +
//	           walBuffer.writeReserved + MemRing.WriteReserved
//	    → walBuffer.writeReserved              (no lock; leaf)
//	    → MemRing.WriteReserved                (memRing.mu read-lock)
//	    → insertionTracker.setInsertingAt(stripe, lsnIdle)
//	  → drop stripe lock
//
// The cross-segment hook is configured at insertPosTracker construction
// time via `newInsertPosTracker`'s `onCross` parameter; stripeAppend
// does not see it directly — the slice B call-site rewrite will install
// [[0107-0007s]] `emitSegmentPad` as that hook so a stripe whose
// reservation straddles a segment boundary automatically gets a
// well-formed XLOG_NOOP pad record dropped into the gap (and the
// reservation lands at the post-boundary LSN, preserving the xl_prev
// chain).
//
// Empty record contract. `len(record) == 0` is rejected with
// `errStripeAppendEmptyRecord` rather than silently no-op. Three
// reasons matter: (a) `reserveAndPublish(0, …)` panics on size == 0 by
// design — we want a structured error from the composer, not a panic
// for a benign empty case; (b) the slice B caller has no useful "did
// the empty write happen" semantics — empty records are always caller
// bugs in the WAL path; (c) early rejection avoids acquiring the
// stripe lock for a definitively-no-op call.
//
// Nil-checks. All four pointer arguments are required for the
// composer's contract to hold:
//
//   - `locks` is the stripe-lock arena. nil → `errStripeAppendNilLocks`
//     before any side effect.
//   - `posTracker` is the LSN authority. nil → `errStripeAppendNilPosTracker`.
//   - `insertTracker` is the per-stripe publication slot array. nil
//     would let the BEGIN/END publication race re-open. nil →
//     `errStripeAppendNilInsertTracker`.
//   - `walBuf` and `memRing` are individually nil-safe — the composer
//     skips the corresponding writeReserved when the receiver is nil,
//     matching [[0107-0007s]] `emitSegmentPad`'s per-receiver convention.
//
// Error handling and END marker. If `walBuf.writeReserved` or
// `memRing.WriteReserved` returns an error, the composer still publishes
// the END marker (setInsertingAt(stripe, lsnIdle)) before unlocking the
// stripe, so a failed write does NOT leave the stripe slot stuck at the
// failed reservation's start LSN — that would block the drain's safe
// tail indefinitely. Both side effects use defer with LIFO ordering so
// the END marker runs *before* the unlock, matching the lock-ordering
// tier above. Errors are returned verbatim from the failing primitive
// so callers can pattern-match against `errWALBufferReservedOutOfRange`
// / `errMemRingReservedOutOfRange` for diagnostic purposes; the
// returned `start, prev` reflect the reservation that was already
// granted by `reserveAndPublish` (the reservation cannot be unwound
// because peer stripes may have already published into LSN ranges
// past it).
//
// Concurrency. Two stripeAppend calls with procNums that hash to
// different stripes proceed in parallel — only the per-stripe mutex
// serialises within a stripe. PG's
// `postgres/src/backend/access/transam/xlog.c` is the same model:
// `WALInsertLockAcquire(MyProcNumber % NUM_XLOGINSERT_LOCKS)` →
// `ReserveXLogInsertLocation` → `CopyXLogRecordToWAL` →
// `WALInsertLockRelease`. goopg fuses the equivalent five steps into
// this one composer so the call-site rewrite stays a one-liner.
//
// Dead code until the slice B call-site rewrite mounts `appendLockSet`
// + `insertPosTracker` + `insertionTracker` on `Writer` and switches
// `state.append`'s body to call `stripeAppend`; foundation-first
// pattern matches slice C ([[0107-0007b]] / [[0107-0007c]] /
// [[0107-0007d]] before [[0107-0007e]] / [[0107-0007f]] /
// [[0107-0007g]]) and the twelve earlier slice B foundations
// ([[0107-0007i]] / [[0107-0007j]] / [[0107-0007k]] /
// [[0107-0007l]] / [[0107-0007m]] / [[0107-0007n]] / [[0107-0007o]] /
// [[0107-0007p]] / [[0107-0007q]] / [[0107-0007r]] / [[0107-0007s]] /
// [[0107-0007t]]).
func stripeAppend(
	locks *appendLockSet,
	posTracker *insertPosTracker,
	insertTracker *insertionTracker,
	walBuf *walBuffer,
	memRing *MemRing,
	procNum int32,
	record []byte,
) (start, prev uint64, err error) {
	if locks == nil {
		return 0, 0, errStripeAppendNilLocks
	}
	if posTracker == nil {
		return 0, 0, errStripeAppendNilPosTracker
	}
	if insertTracker == nil {
		return 0, 0, errStripeAppendNilInsertTracker
	}
	if len(record) == 0 {
		return 0, 0, errStripeAppendEmptyRecord
	}

	stripe := stripeForProcNum(procNum)
	locks.locks[stripe].mu.Lock()
	defer locks.locks[stripe].mu.Unlock()
	// END marker before unlock (LIFO defer order): the drain-side
	// tailPublisher can stop capping safeTail at this stripe's start
	// LSN as soon as the bytes are in the ring, regardless of whether
	// the byte write succeeded — a failed write leaves no bytes the
	// drain could leak, but leaves the slot occupied, which would
	// freeze publication forever.
	defer insertTracker.setInsertingAt(stripe, lsnIdle)

	start, prev = posTracker.reserveAndPublish(uint64(len(record)), stripe, insertTracker)

	if walBuf != nil {
		if werr := walBuf.writeReserved(int64(start), record); werr != nil {
			return start, prev, werr
		}
	}
	if memRing != nil {
		if merr := memRing.WriteReserved(int64(start), record); merr != nil {
			return start, prev, merr
		}
	}
	return start, prev, nil
}

// errStripeAppendNilLocks is returned when stripeAppend is invoked with
// a nil appendLockSet. The composer rejects rather than panicking so
// callers can pattern-match programmatically.
var errStripeAppendNilLocks = errors.New("wal: stripeAppend: nil appendLockSet")

// errStripeAppendNilPosTracker is returned when stripeAppend is invoked
// with a nil insertPosTracker.
var errStripeAppendNilPosTracker = errors.New("wal: stripeAppend: nil insertPosTracker")

// errStripeAppendNilInsertTracker is returned when stripeAppend is
// invoked with a nil insertionTracker — without it the pre-reserve
// race re-opens, so silently skipping publication would defeat the
// foundation's purpose.
var errStripeAppendNilInsertTracker = errors.New("wal: stripeAppend: nil insertionTracker")

// errStripeAppendEmptyRecord is returned when stripeAppend is invoked
// with an empty record — empty WAL inserts are always caller bugs and
// the underlying reserveAndPublish panics on size == 0 by design.
var errStripeAppendEmptyRecord = errors.New("wal: stripeAppend: empty record")
