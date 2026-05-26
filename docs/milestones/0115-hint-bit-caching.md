# Milestone 0115 — Heap Tuple Hint Bit Caching

**Status:** planned
**Filed:** 2026-05-26
**Depends on:** heap tuple format in PG-compatible layout (completed in M0111-0002 S2, 2026-05-26)
**Reference design:** `docs/design/mvcc-optimize/0115-0001-hint-bit-caching.md`

## Problem

PostgreSQL's MVCC visibility check (`HeapTupleSatisfiesMVCC`) can short-circuit
immediately for committed tuples by reading cached flag bits stored directly in
the tuple's `t_infomask`:

```
HEAP_XMIN_COMMITTED  — xmin is a committed transaction
HEAP_XMIN_INVALID    — xmin is invalid (rolled back, or crashed)
HEAP_XMAX_COMMITTED  — xmax is a committed transaction
HEAP_XMAX_INVALID    — xmax is invalid (row is live)
```

Once these bits are set, subsequent scans of the same tuple require no
snapshot arithmetic — they check one bit and return immediately.

goopg defines `HeapXmaxCommitted` and `HeapXmaxInvalid` in
`internal/storage/heap.go` but does **not** define `HeapXminCommitted` /
`HeapXminInvalid`, and does not read or write any hint bits in `TupleVisible`
(`internal/mvcc/visibility.go`). Adding the xmin constants is part of
M0115-0002.
Every visibility check calls `snap.SeesCommittedXID`, which performs:

1. A nil-check and `HasAborted` scan (O(abortedXIDs length); binary search above threshold)
2. An `xid < s.Xmin` comparison
3. An `xid >= s.Xmax` comparison
4. A `HasInProgress` scan (O(in-progress length); binary search above threshold)

For a sequential scan over a table with millions of committed tuples, steps
1–4 execute once per tuple per scan even though the result is always the same.
Hint bits eliminate this redundant work after the first scan of each tuple.

A secondary CPU optimization: `TupleVisible` has no special case for
`xmin == FrozenTransactionID` (value 2). Frozen tuple visibility is already
correct (handled by the `xid < s.Xmin` branch), but adding an explicit
early-exit skips all four `SeesCommittedXID` conditions and saves cycles for
tables that have been fully vacuumed/frozen.

## Goal

Implement the full hint-bit read/write path in the MVCC visibility layer:

1. **FrozenTransactionID fast path** — if `xmin == FrozenTransactionID`, return
   visible immediately.
2. **Hint-bit read** — check `HEAP_XMIN_COMMITTED / HEAP_XMIN_INVALID` before
   calling `SeesCommittedXID(xmin)`, and likewise for xmax.
3. **Hint-bit write** — after confirming xmin commit status from the snapshot
   (or clog), stamp the result into the tuple's infomask on-page. Repeat for
   xmax. The stamped page is marked dirty via a new **hint-bit-only** dirty
   marker that does **not** emit a WAL record (hint bits are re-derived on
   recovery from transaction status, matching PG's design).
4. **Context wiring** — the scan path must pass a writable page slot to
   `TupleVisible` so the hint-bit write can be applied under the page's content
   lock. Read-only paths (e.g., standby replay, testing) omit the slot and
   skip the write.

## Motivation

- **Reduced per-tuple CPU cost** for sequential scans of committed data.
  TPC-H workloads (large analytical scans) and pgbench `SELECT` paths both
  benefit.
- **PG alignment** — hint bits are a fundamental feature of PG's heap tuple
  design; implementing them closes the parity gap.
- **Reduced snapshot pressure** — fewer `HasInProgress` walks reduce contention
  on the ProcArray under concurrent workloads.

## Key Design Areas

See `docs/design/mvcc-optimize/0115-0001-hint-bit-caching.md` for the full
design. Summary:

- `mvcc.TupleVisible` gains a `*storage.Slot` parameter; nil means read-only.
- Hint-bit write uses `storage.Pool.MarkDirtyHintBit(slot)` — a new variant
  that marks the buffer dirty without emitting a WAL record or logical change.
- `HeapXminCommitted` (0x0100) and `HeapXminInvalid` (0x0200) constants must
  be added to `internal/storage/heap.go` (M0115-0002); they are absent today.
- The `HeapXminCommitted` / `HeapXminInvalid` pair is mutually exclusive;
  exactly one is set after the first visibility check on a committed tuple.
- On WAL recovery, the replay path does **not** replay hint-bit writes (there
  are none); it simply leaves hint bits unset; they are lazily re-set on the
  first scan after recovery.

## Out of Scope

- Writing hint bits during WAL replay (not needed; PG also skips this).
- `HEAP_XMAX_COMMITTED` on rows deleted by aborted transactions (handled by
  `HEAP_XMAX_INVALID` instead).
- Multi-transaction xmax (`MultiXactId`); that is M0083.

## Sub-Milestones

- **M0115-0001** — FrozenTransactionID fast path in `TupleVisible` and
  `TupleVisibleSubxact`.
- **M0115-0002** — Hint-bit read path: check `HEAP_XMIN_COMMITTED /
  HEAP_XMIN_INVALID` in `TupleVisible` before `SeesCommittedXID`.
- **M0115-0003** — `storage.Pool.MarkDirtyHintBit` — dirty a buffer without
  WAL emission.
- **M0115-0004** — Hint-bit write path: after `SeesCommittedXID`, stamp the
  result and call `MarkDirtyHintBit`.
- **M0115-0005** — Context wiring: pass `*storage.Slot` through the seqScan /
  indexScan callers.
- **M0115-0006** — Unit tests: hint-bit short-circuit, frozen-xid fast path,
  hint-bit-dirty-no-wal invariant, recovery re-derive.
- **M0115-0007** — Benchmark regression check: pgbench simple-update and
  TPC-H Q1/Q6 scan times do not regress.

## Definition of Done

- [ ] `TupleVisible` returns immediately for `xmin == FrozenTransactionID`.
- [ ] `TupleVisible` skips `SeesCommittedXID(xmin)` when `HEAP_XMIN_COMMITTED`
  is set; returns false immediately when `HEAP_XMIN_INVALID` is set.
- [ ] After a visibility check that calls `SeesCommittedXID`, the appropriate
  hint bit is written to the on-page tuple header via a `MarkDirtyHintBit`
  call.
- [ ] `MarkDirtyHintBit` does not append a WAL record.
- [ ] All existing MVCC tests pass (`go test ./internal/mvcc/...`).
- [ ] `go test ./internal/executor/...` and `go test ./internal/server/...`
  pass.
- [ ] pgbench simple-update TPS does not regress vs. pre-milestone baseline.
