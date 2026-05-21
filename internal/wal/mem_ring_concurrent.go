package wal

import "errors"

// errMemRingReservedOutOfRange is returned by WriteReserved when the
// requested LSN byte range falls outside the ring's currently-
// allocated [head, head+cap) window. "Escape" means either pos < head
// (bytes already evicted by a prior PublishUpTo) or pos+len(data) >
// head+cap (write target would land outside the ring's address range).
//
// Sentinel error so call-site rewrites can distinguish window
// violations from other I/O errors when the stripe-concurrent
// MemRing.WriteReserved replaces sequential MemRing.Append in
// state.append (slice B call-site rewrite, [[0107-0007]] parent
// milestone).
var errMemRingReservedOutOfRange = errors.New("wal: MemRing.WriteReserved range outside ring window")

// WriteReserved writes len(data) bytes into the ring at LSN byte
// position pos, without advancing head or tail. The bytes become
// readable via ReadAt only after a subsequent PublishUpTo advances
// tail past pos+len(data).
//
// Foundation 8 for M0107-0007 slice B (Phase D4 — 8-stripe WAL insert
// locks per `docs/design/perf-optimize/07-wal-fsm-insert.md` §2). The
// bytes-write counterpart for the stripe-concurrent writer model: a
// stripe reserves an LSN range via [[0107-0007k]] insertPosTracker,
// lands the bytes into the WAL buffer via [[0107-0007l]]
// walBuffer.writeReserved AND into the in-memory mirror via this
// WriteReserved (concurrently with peer stripes writing disjoint LSN
// ranges). A separate drain goroutine then calls [[0107-0007n]]
// tailPublisher.publishUpTo and feeds the resulting safe-tail
// watermark to MemRing.PublishUpTo so walsender ReadAt callers can
// observe the resident bytes.
//
// PG counterpart. PG does not have a separate "memring" — its
// equivalent is the shared WAL buffer (`XLogCtl->pages`) that
// `CopyXLogRecordToWAL` writes into under WAL insert locks. The
// reserve/publish split is identical; the goopg MemRing exists for a
// different reason (M0010-0001's direct-IO write path bypasses the OS
// page cache, so walsender needs an explicit RAM mirror), but under
// stripe-concurrent writers both rings need the same publication
// discipline.
//
// Errors:
//   - errMemRingReservedOutOfRange if [pos, pos+len(data)) escapes the
//     ring's currently-allocated [head, head+cap) window. The exact
//     boundary pos+len(data) == head+cap is accepted (write lands at
//     the very last slot inclusive).
//
// Empty data is a no-op returning nil. Matches MemRing.Append's
// len==0 short-circuit and walBuffer.writeReserved's contract. Runs
// before the range check so a zero-length "reservation" with an
// out-of-window pos is still benign — eliminates a class of spurious
// error returns from defensive callers.
//
// Nil receiver is a no-op returning nil. Matches the MemRing.Append
// nil-safe convention (NewMemRing(0) == nil so the writer can stash
// the result directly), letting the slice B call-site rewrite leave
// the ring unset under `wal_sender_memory_buffer == 0` without an
// extra nil-guard at every write site.
//
// Concurrency. Holds the read lock for the duration of the memcpy.
// Multiple WriteReserveds writing into disjoint LSN ranges (and hence
// disjoint ring-slot ranges, since each reservation is smaller than
// the ring capacity — the slice B insertion path bounds individual
// records well below the 16 MiB default cap) run in parallel under
// the read lock. A concurrent PublishUpTo or Append (both take the
// write lock) is excluded.
//
// Concurrent WriteReserveds at OVERLAPPING LSN ranges are a contract
// violation and produce undefined ring contents (overlapping copy
// regions race). The slice B call site guarantees disjoint ranges by
// serialising reservation allocation through insertPosTracker (joint
// atomicity of (curr, prev) under posMu); this primitive does not
// detect the violation.
//
// Tail publication is deliberately a separate step (PublishUpTo) for
// the same reason walBuffer.writeReserved leaves walBuffer.tail
// untouched: tail cannot advance past LSN X until every reservation
// strictly below X has been fully byte-written by its owning stripe,
// and only the drain goroutine — consulting the insertion tracker
// via tailPublisher — knows when that condition holds.
//
// Lock-ordering tier (leaf reader; the write side never reaches back
// up the chain):
//
//	appendLockSet.lockByProcNum  (one of 8 stripes)
//	  → insertPosTracker.reserve  (briefly under posMu)
//	    → insertionTracker.setInsertingAt(stripe, start)
//	      → walBuffer.writeReserved
//	      → MemRing.WriteReserved   ← here (writer side)
//	    → insertionTracker.setInsertingAt(stripe, lsnIdle)
//	  → drop stripe lock
func (r *MemRing) WriteReserved(pos int64, data []byte) error {
	if r == nil {
		return nil
	}
	if len(data) == 0 {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := int64(len(data))
	if pos < r.head || pos+n > r.head+r.cap {
		return errMemRingReservedOutOfRange
	}
	writeIdx := pos % r.cap
	first := r.cap - writeIdx
	if first >= n {
		copy(r.buf[writeIdx:writeIdx+n], data)
	} else {
		copy(r.buf[writeIdx:r.cap], data[:first])
		copy(r.buf[0:n-first], data[first:])
	}
	return nil
}

// PublishUpTo advances the ring's tail monotonically to safeTail,
// making bytes in [r.tail, safeTail) — previously landed via
// WriteReserved — visible to ReadAt callers. If the new tail would
// extend the resident range beyond capacity, head advances so
// tail-head ≤ cap (evicting the oldest residents).
//
// Foundation 8 for M0107-0007 slice B. Driven by the drain goroutine
// from the watermark returned by [[0107-0007n]]
// tailPublisher.publishUpTo, after the stripe-concurrent writers
// have landed their bytes via WriteReserved above.
//
// Monotonic by construction: safeTail ≤ r.tail short-circuits with
// no mutation. The caller is expected to derive safeTail from
// tailPublisher.publishUpTo (already monotonic), so a regressing
// safeTail typically reflects either a fresh ring (r.tail == 0) or a
// caller error; either way silent no-op is the right response.
//
// PG counterpart. Mirrors PG's pattern where the flush coordinator
// advances `XLogCtl->LogwrtResult.Write` after
// `WaitXLogInsertionsToFinish` returns; downstream readers consult
// the published watermark before issuing a read.
//
// Nil receiver is a no-op. Matches the MemRing.Append nil-safe
// convention.
//
// Concurrency. Takes the write lock for the head/tail mutation;
// serialises against active WriteReserveds (read lock), ReadAts
// (read lock), and sequential Appends (write lock). The exclusion vs
// WriteReserveds is required: head advance reclaims ring slots that
// an in-flight WriteReserved at a LOW LSN might still be mid-memcpy
// on. (The slice B call site further constrains: tailPublisher's
// safeTail can never exceed any stripe's active reservation LSN, so
// a well-behaved drain never advances past an active write; the lock
// here is defence in depth for misbehaving callers and for the
// bootstrap window where stripes have not yet published their first
// active LSN.)
//
// Lock-ordering tier (leaf publisher; the publisher never reaches
// back up the chain):
//
//	(drain goroutine, after stripe writers complete:)
//	  tailPublisher.publishUpTo(upperBound, insertionTracker)
//	  walBuffer.advanceHead(published - prior)
//	  MemRing.PublishUpTo(published)   ← here (publisher side)
func (r *MemRing) PublishUpTo(safeTail int64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if safeTail <= r.tail {
		return
	}
	r.tail = safeTail
	if r.tail-r.head > r.cap {
		r.head = r.tail - r.cap
	}
}

// AdvanceWindow slides the ring's readable window forward so that a
// subsequent WriteReserved call ending at `upTo` will fit within
// [head, head+cap).  Specifically it advances head to max(head, upTo-cap),
// evicting old ring data that walsenders can no longer reach without a
// disk fallback.
//
// Must be called BEFORE WriteReserved whenever a new stripe-B record is
// about to be written.  Without it, the first write past the ring's initial
// window (total WAL written > cap) fails: PublishUpTo(end) sets tail=end and
// head=end-cap, leaving the window [end-cap, end) which excludes end itself
// (strict less-than in WriteReserved), so writes at end always fail.
func (r *MemRing) AdvanceWindow(upTo int64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	needed := upTo - r.cap
	if needed > r.head {
		r.head = needed
	}
}
