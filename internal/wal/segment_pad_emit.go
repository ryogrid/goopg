package wal

import "fmt"

// emitSegmentPad is the cross-segment composer for M0107-0007 slice B
// (Phase D4 — 8-stripe WAL insert locks per
// `docs/design/perf-optimize/07-wal-fsm-insert.md` §2). It builds the
// XLOG_NOOP pad record that fills the gap `[gapStart, boundary)` opened
// when a stripe reservation crosses a segment boundary, and writes the
// pad bytes into both the bytes-side ring (`walBuffer`) and the
// walsender in-memory mirror (`MemRing`) without advancing either
// publication watermark.
//
// Foundation 12 of slice B. Composes three earlier foundations into the
// single action that [[0107-0007k]] `insertPosTracker`'s
// `onCrossSegment` hook fires synchronously under `posMu`:
//
//   - [[0107-0007j]] `buildSegmentPadRecord(padLen, gapPrev)` produces
//     the on-disk bytes for a single `RmgrXLog`/`XLOG_NOOP` record of
//     exactly padLen bytes with xl_prev stamped from gapPrev.
//   - [[0107-0007l]] `walBuffer.writeReserved(gapStart, pad)` lands the
//     bytes in the bytes-side ring without touching head/tail/base —
//     publication is left to the drain goroutine's
//     [[0107-0007q]] `walBuffer.publishTail`.
//   - [[0107-0007o]] `MemRing.WriteReserved(gapStart, pad)` mirrors the
//     bytes in the walsender RAM cache without touching the ring's
//     head/tail — publication is left to `MemRing.PublishUpTo`, driven
//     by the same [[0107-0007n]] `tailPublisher` watermark.
//
// gapPrev is the prev-LSN stamped into the pad record's xl_prev so the
// xl_prev chain stays continuous across the boundary; the
// immediately-following stripe reservation receives prev=gapStart from
// `insertPosTracker.reserveLocked` (the pad record's start LSN).
//
// Nil-safety. `walBuf` and `memRing` are independently nil-safe so the
// composer can be wired from `insertPosTracker.onCrossSegment` even
// when the active `Writer` was constructed with
// `Config.WALBuffers == 0` (no walBuf) or `wal_sender_memory_buffer
// == 0` (no memRing). Both nil → no-op after a successful pad build;
// the builder still runs so a malformed padLen surfaces an error
// regardless of ring configuration. This is a deliberate departure
// from [[0107-0007l]]'s `errWALBufferNil` convention — the composer
// represents a *call site* that the call-site rewrite will install
// once for every cross-segment crossing, and a per-call-site
// nil-guard is cheaper than a guard at every composer caller.
//
// Tail publication is intentionally NOT advanced here — the
// stripe-concurrent model defers visibility to the drain goroutine's
// `tailPublisher` chain so a single watermark serialises bytes-and-pad
// publication for both rings. A pad-side publication advance ahead of
// the drain would let `readAt` / `MemRing.ReadAt` see pad bytes before
// the stripe reservation that triggered the crossing has finished its
// own `writeReserved` — the same hazard that prompted
// [[0107-0007l]] / [[0107-0007o]] to leave tail untouched in the first
// place.
//
// PG counterpart:
// `postgres/src/backend/access/transam/xlog.c`'s
// `AdvanceXLInsertBuffer` + `XLogInsertRecord` together emit the pad
// record into the shared WAL buffer (and, when streaming, the
// walsender snapshot view) under the WAL insert lock. goopg's
// composer matches that single-call shape so the
// `insertPosTracker.onCrossSegment` hook stays a one-liner.
//
// Lock-ordering tier (when invoked from the planned call-site
// rewrite):
//
//	appendLockSet.lockByProcNum
//	  → insertPosTracker.reserveAndPublish  (posMu held)
//	      → (cross-segment slow path) emitSegmentPad
//	          → buildSegmentPadRecord
//	          → walBuffer.writeReserved
//	          → MemRing.WriteReserved
//	      → tracker.setInsertingAt(stripe, start)  (BEGIN edge)
//	    (posMu released)
//	  → walBuffer.writeReserved  (the triggering reservation's bytes)
//	  → MemRing.WriteReserved
//	  → insertionTracker.setInsertingAt(stripe, lsnIdle)
//	→ drop stripe lock
//
// emitSegmentPad runs entirely under posMu in the call-site rewrite;
// the read-only writeReserved / WriteReserved primitives take their
// own locks (walBuffer is mutex-free under the slice B model so it
// takes none; MemRing takes its RLock for the duration of the
// memcpy).
//
// Errors. Propagated verbatim from the composed primitives:
//   - boundary <= gapStart → composer-level error (defence in
//     depth; insertPosTracker.reserveLocked never produces such a
//     gap, but a future caller might).
//   - padLen below SizeOfXLogRecord → ErrPadLenTooSmall-style error
//     from buildSegmentPadRecord.
//   - padLen == 25 (1-byte body) → ErrPadLen1ByteBody-style error.
//   - walBuf.writeReserved out-of-window error.
//   - MemRing.WriteReserved out-of-window error.
//
// Callers under posMu should treat any error as fatal — the hook
// fires exactly once per crossing and a failed pad write breaks the
// xl_prev chain (the next stripe reservation would receive
// prev=gapStart pointing at non-existent bytes).
//
// Dead code until the slice B call-site rewrite installs the
// composer as `insertPosTracker.onCrossSegment`; foundation-first
// pattern matches slice C and the eleven earlier slice B
// foundations.
func emitSegmentPad(walBuf *walBuffer, memRing *MemRing, gapStart, boundary, gapPrev uint64) error {
	if boundary <= gapStart {
		return fmt.Errorf("wal: emitSegmentPad: boundary=%d must exceed gapStart=%d", boundary, gapStart)
	}
	padLen := int(boundary - gapStart)
	pad, err := buildSegmentPadRecord(padLen, gapPrev)
	if err != nil {
		return err
	}
	if walBuf != nil {
		if err := walBuf.writeReserved(int64(gapStart), pad); err != nil {
			return err
		}
	}
	if memRing != nil {
		if err := memRing.WriteReserved(int64(gapStart), pad); err != nil {
			return err
		}
	}
	return nil
}
