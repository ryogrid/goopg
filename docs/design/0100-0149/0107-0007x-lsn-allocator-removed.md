# Phase D4 — Remove `lsnAllocator` as dead code (M0107-0007, slice B follow-up)

Status: accepted
Date: 2026-05-21

## Summary

Deletes `internal/wal/lsn_alloc.go` and `internal/wal/lsn_alloc_test.go`.
The `lsnAllocator` primitive landed as slice B foundation 1
(formerly indexed as `[[0107-0007h]]`) but has been structurally
subsumed by `[[0107-0007k]]` `insertPosTracker` and was never wired
into a production call site.

## Background

`lsnAllocator` was a CAS-fast-path LSN reserve with a `rotateMu`
segment-boundary slow path and an `onCrossSegment` hook. It tracked
only `next` (the cursor advanced by every reservation); it did not
track `prev` (the previous record's start LSN, stamped into the next
record's `xl_prev`).

`insertPosTracker` (slice B foundation 4) was added because the WAL
append path requires joint atomicity of `(curr, prev)` for chain
correctness — a peer must never observe `curr` advanced past LSN X
without `prev` set to the start of the record that reserved X.
`insertPosTracker.reserve` provides this guarantee under a single
`posMu` critical section and is otherwise contract-identical to
`lsnAllocator.reserve` (same `0 < size <= segSize` constraint, same
segment-crossing semantics where the gap `[oldCurr, boundary)` is
reported to `onCrossSegment` and the reservation hops to the start of
the new segment).

The original design doc kept `lsnAllocator` as a callable primitive
"suitable for callers that don't need the xl_prev chain". No such
caller materialised, and the slice B call-site rewrite (per the
`stripeWriterCore` packaging in `[[0107-0007v]]` and the mount in
`[[0107-0007w]]`) converges exclusively on `insertPosTracker`.

The "decide whether `lsnAllocator` becomes dead-code-removed" item
was explicitly carried in every slice B foundation note from
`[[0107-0007k]]` through `[[0107-0007w]]` as deferred. This loop
closes that decision.

## What changes

1. **Removed files**
   - `internal/wal/lsn_alloc.go` — the primitive itself.
   - `internal/wal/lsn_alloc_test.go` — eight tests covering the
     deleted primitive. Equivalent coverage is provided by
     `insert_pos_test.go` and `insert_pos_publish_test.go` for the
     `insertPosTracker` superset (joint-atomic reserve + cross-segment
     hook + concurrent disjoint reservations + invalid-size panic +
     constructor zero-segSize panic).
   - `docs/design/0107-0007h-wal-lsn-allocator.md` — the original
     design doc. Git history retains the full text and rationale.

2. **Updated comment references**
   - `internal/wal/padded_mutex.go` — lock-ordering comments rewritten
     to reference `[[0107-0007k]]` `insertPosTracker.reserve` instead
     of `lsnAllocator.reserve`.
   - `internal/wal/segment_pad.go` — `onCrossSegment` hook reference
     rewritten to reference `[[0107-0007k]]` `insertPosTracker`
     (composed with `[[0107-0007s]]` `emitSegmentPad`).
   - `internal/wal/insert_pos.go` — preamble "Foundations 1–3" list
     reworded; segment-crossing comment no longer compares to
     `lsnAllocator`.
   - `internal/wal/insert_pos_publish.go`,
     `internal/wal/insertion_tracker.go`,
     `internal/wal/tail_publisher.go`,
     `internal/wal/tail_publisher_test.go`,
     `internal/wal/stripe_append.go`,
     `internal/wal/stripe_writer_core.go`,
     `internal/wal/publish_visibility.go` — foundation-chain reference
     lists strip the `[[0107-0007h]]` entry; the slice B foundation
     count is decremented accordingly.

3. **Design index**
   - `docs/design/README.md` — row for `0107-0007h` removed; rows for
     `0107-0007i` and `0107-0007j` rewritten to drop their
     `[[0107-0007h]]` references in favour of `insertPosTracker`; a
     new row for this document (`0107-0007x`) added.

## Why this is safe

- The deleted symbol set is `{lsnAllocator, newLSNAllocator,
  (*lsnAllocator).load, (*lsnAllocator).reserve}`. None is referenced
  by production code; the only callers were the deleted tests.
- The call-site rewrite (`Writer.core` field mounted via
  `[[0107-0007w]]`) instantiates `stripeWriterCore` with
  `insertPosTracker`; the production WAL path (`state.append`) is
  still on the legacy single-mutex path and has never consulted
  `lsnAllocator`.
- `insertPosTracker` was tested to cover every behavioural scenario
  `lsnAllocator` exercised:
  contiguous reserves, recovery-resume start values, single-crossing
  hook invocation, exact-boundary fast path, invalid-size panics,
  zero-segSize constructor panic, concurrent-disjoint correctness,
  multi-boundary crossings — plus the extra joint-atomic `(curr, prev)`
  contract that `lsnAllocator` did not provide.

## Verification

- `go test -race -count=1 ./internal/wal/` PASS.
- `go vet ./internal/wal/` clean.
- `make ralph-state-guard` PASS.

## Out of scope

- Production call-site rewrite of `state.append` / `drainBufferBytes`
  remains slice B parts 2/3 work and is unchanged by this removal.
- Renaming of remaining `[[0107-0007h]]` references in legacy
  fix_plan entries — those are append-only loop notes and are kept
  verbatim as historical record.
