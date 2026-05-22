package wal

// stripeAppendBuiltEmitted is the joint-atomic predict+reserve+build+write
// composer for M0107-0007 slice B (Phase D4 — 8-stripe WAL insert locks per
// `docs/design/perf-optimize/07-wal-fsm-insert.md` §2). It bundles
// foundation 18 ([[0107-0007aa]] `reserveEmittedAndPublish`) with the build
// closure and ring writes so the call-site rewrite can land one PG-compat
// record per call without exposing predict/reserve sequencing to callers.
//
// Foundation 19 for M0107-0007 slice B. Closes the deferred decision noted
// in foundation 18 ([[0107-0007aa]] §"Out of scope") about adding the
// wrapper that bundles `reserveEmittedAndPublish` + `build(prev)` + ring
// writes + END marker — keeps the eventual call-site rewrite a one-liner.
//
// Why this is distinct from [[0107-0007y]] `stripeAppendBuild`. Build's
// `size` argument is the on-the-wire byte count including page headers —
// callers must compute it themselves via [[0107-0007z]] `predictEmittedSize`
// against the current `posTracker.curr`. Foundation 18 closed the race
// where a peer reservation lands between the predict and the matching
// build call (advancing curr so the prediction no longer matches the
// reservation's actual startPos). stripeAppendBuiltEmitted threads
// `recordLen` (the unpadded WAL record length, i.e. the body the caller
// produces) all the way through `reserveEmittedAndPublish` so the
// composer itself knows both `total` (reserved emit size) and `leading`
// (page header bytes the build closure must emit before the record body).
// The build closure receives all three — prev, total, leading — and is
// responsible for materialising exactly `total` bytes of page-headered
// output (typically by calling `emitWithPageHeaders` with the prev
// stamped into the record's xl_prev field).
//
// Why the build closure receives the (start, prev, total, leading) tuple.
// Page-header emission via `emitWithPageHeaders` needs (a) the reservation's
// start LSN to compute page boundaries, contrecord splits, and the system
// ID / timeline stamped into each header; (b) the total reserved length so
// the closure validates its own output against the contract; (c) the
// leading-header byte count so the closure can stamp a long PHD vs short
// PHD vs no leading PHD without re-running `predictEmittedSize`. start,
// total, and leading are all known inside `reserveEmittedAndPublish` under
// posMu (including the cross-segment re-prediction at boundary), so
// threading them through avoids a duplicate post-reservation predict on
// the hot path. The build closure's output MUST be exactly `total` bytes
// long, of which the first `leading` are a page header (or zero if start
// is not page-aligned).
//
// Lock-ordering tier — extends [[0107-0007u]] stripeAppend with the
// joint-atomic predict step inside `reserveAndPublish`:
//
//	stripeAppendBuiltEmitted(locks, posTracker, insertTracker, walBuf, memRing,
//	                        procNum, recordLen, build)
//	  → appendLockSet.lockByProcNum             (one of 8 stripes)
//	    → insertPosTracker.reserveEmittedAndPublish  (posMu held)
//	        → predictEmittedSize @ t.curr        (pure arithmetic)
//	        → (rare) onCrossSegment(start, boundary, gapPrev)
//	            → emitSegmentPad → buildSegmentPadRecord +
//	              walBuffer.writeReserved + MemRing.WriteReserved
//	        → predictEmittedSize @ boundary       (cross-seg re-predict)
//	        → insertionTracker.setInsertingAt(stripe, start)
//	      (posMu released)
//	    → build(start, prev, total, leading)     (caller's encoder + page headers)
//	    → walBuffer.writeReserved                (no lock; leaf)
//	    → MemRing.WriteReserved                  (memRing.mu read-lock)
//	    → insertionTracker.setInsertingAt(stripe, lsnIdle)
//	  → drop stripe lock
//
// Error handling and END marker. Same contract as
// [[0107-0007y]] stripeAppendBuild: if build returns an error or the ring
// writes fail, the END marker still fires before unlock so the drain's
// publication watermark is not frozen. The reservation cannot be unwound —
// peer stripes may have already advanced past it — so a failed build
// leaves a published LSN range that no bytes will ever cover. The slice B
// caller treats build errors as fatal at the WAL append boundary.
//
// Size-mismatch contract. `build(start, prev, total, leading)` MUST return
// a slice of length exactly `total`. The composer validates this before
// the ring writes; mis-size → `errStripeAppendBuildSizeMismatch` with
// END marker fired. Mismatch is a programmer bug in the build closure
// (e.g. forgot to call emitWithPageHeaders, or computed total
// differently from predictEmittedSize), and surfaces loudly rather than
// silently corrupting peer-stripe data.
//
// Nil-checks. Same set as stripeAppendBuild: `locks`, `posTracker`,
// `insertTracker`, `build` are required; `walBuf` and `memRing` are
// individually nil-safe. recordLen must be > 0 — empty inserts are
// always caller bugs and the underlying reserveEmittedAndPublish panics
// on recordLen <= 0 by design; the composer rejects with the same
// structured error sentinel stripeAppend/stripeAppendBuild use.
//
// Concurrency. Two stripeAppendBuiltEmitted calls with procNums hashing
// to different stripes proceed fully in parallel. The posMu-held
// critical section in reserveEmittedAndPublish is short — two
// predictEmittedSize calls (pure arithmetic, ~tens of nanoseconds each)
// plus the joint (curr, prev) update plus the stripe-slot atomic.Store
// — so cross-stripe contention on posMu is bounded.
//
// Dead code until the slice B call-site rewrite mounts this composer at
// the PG-compat write entry point in `state.append` /
// `state.appendTryEnqueue` / `state.appendBatch`; foundation-first
// pattern matches slice C ([[0107-0007b]] / [[0107-0007c]] /
// [[0107-0007d]] before [[0107-0007e]] / [[0107-0007f]] /
// [[0107-0007g]]) and the eighteen earlier slice B foundations
// ([[0107-0007i]] through [[0107-0007aa]] minus the dead-code-removed
// [[0107-0007h]]).
func stripeAppendBuiltEmitted(
	locks *appendLockSet,
	posTracker *insertPosTracker,
	insertTracker *insertionTracker,
	walBuf *walBuffer,
	memRing *MemRing,
	procNum int32,
	recordLen int,
	build func(start, prev uint64, total, leading int) ([]byte, error),
) (start, prev uint64, total, leading int, err error) {
	if locks == nil {
		return 0, 0, 0, 0, errStripeAppendNilLocks
	}
	if posTracker == nil {
		return 0, 0, 0, 0, errStripeAppendNilPosTracker
	}
	if insertTracker == nil {
		return 0, 0, 0, 0, errStripeAppendNilInsertTracker
	}
	if build == nil {
		return 0, 0, 0, 0, errStripeAppendNilBuild
	}
	if recordLen <= 0 {
		return 0, 0, 0, 0, errStripeAppendEmptyRecord
	}

	stripe := stripeForProcNum(procNum)
	locks.locks[stripe].mu.Lock()
	defer locks.locks[stripe].mu.Unlock()
	// END marker before unlock (LIFO defer order) — same contract as
	// stripeAppend/stripeAppendBuild: drain-side tailPublisher must be
	// unblocked even if build or the ring writes fail, otherwise
	// publication freezes.
	defer insertTracker.setInsertingAt(stripe, lsnIdle)

	start, prev, total, leading = posTracker.reserveEmittedAndPublish(recordLen, stripe, insertTracker)

	out, berr := build(start, prev, total, leading)
	if berr != nil {
		return start, prev, total, leading, berr
	}
	if len(out) != total {
		return start, prev, total, leading, errStripeAppendBuildSizeMismatch
	}

	end := int64(start) + int64(total)
	if walBuf != nil {
		if werr := walBuf.writeReserved(int64(start), out); werr != nil {
			return start, prev, total, leading, werr
		}
	}
	if memRing != nil {
		// Advance the MemRing window to accommodate [start, end) before writing.
		// Without this, once total WAL written exceeds ring capacity, PublishUpTo
		// has already set head = tail - cap, leaving the window boundary at tail
		// which excludes the next write's start LSN.
		memRing.AdvanceWindow(end)
		if merr := memRing.WriteReserved(int64(start), out); merr != nil {
			return start, prev, total, leading, merr
		}
	}
	return start, prev, total, leading, nil
}
