# 0103-0018 — Heap FPI and Logical-Record Coexistence

**Status:** Accepted (2026-05-14, M0103-0008 rung 12 second half)
**Owner:** logical replication / WAL
**Tracks:** `.ralph/fix_plan.md` → M0103-0008 (Scenario B E2E: goopg primary
+ PG subscriber)

## Problem

Rung 12 of M0103-0008 left a single open question: live trace of
`TestPort_PgoutputInteropGoopgToPG` showed that the FIRST `INSERT` into a
freshly-allocated heap page (`block 0` of a brand-new table) produces a
`RecordKindPageImage` (kind 1) record in the WAL — and *no*
`RecordKindHeapInsert` (kind 4). The classifier's logical-decoding path
silently skips PageImage records (they aren't user-data transactional
events; the standby's physical replay applies them as torn-page
protection). The downstream PG subscriber never receives the row, so
zero data replicates from a goopg publisher under the most common
shape of all (one table, one INSERT).

The same suppression bites every "first-dirty-in-checkpoint-epoch"
heap mutation, not just first-INSERT-into-fresh-page. Subsequent
DML on the same page within the same epoch *does* emit logical
records (HeapInsert / HeapDelete / HeapHotUpdate), so the bug is
intermittent — exactly the worst kind for replication correctness.

## Root cause

`storage.Pool.MarkDirtyChangeRecord` (the change-record-aware variant
of `MarkDirty`) implements upstream PG's "FPI then change records"
replay invariant via an *optimisation*: on first-dirty-in-epoch it
emits the FPI **alone** and skips the caller's logical `emitter`,
relying on the FPI's post-mutation page bytes to fully describe the
mutation for redo. On second-and-later dirties in the same epoch it
runs `emitter` (the logical record) and skips the FPI. Net: exactly
one WAL record per dirty.

For redo this is correct — the FPI restores the post-mutation page
in one shot. For **logical decoding** the optimisation is fatal:
the per-row logical event the M0008 classifier turns into a pgoutput
`Insert/Update/Delete` message is *missing from the WAL stream*.

The previous-loop diagnosis (rung 12 first half, design doc
0103-0017) closed the missing UPDATE classifier cases
(`RecordKindHeapHotUpdate`, `RecordKindHeapUpdate`) and explicitly
flagged this PageImage emission as the next sub-step. This doc
closes that sub-step.

## Decision

Heap mutation paths emit the logical record **unconditionally**, and
on first-dirty-in-epoch *also* emit a separate FPI record AFTER the
logical record. Two records, in WAL order:

```
LSN_log  HeapInsert / HeapDelete / HeapHotUpdate   (logical change)
LSN_fpi  PageImage                                 (FPI, LSN_fpi > LSN_log)
```

Index / metapage paths (B-tree split, B-tree metapage update, heap
opportunistic-prune, heap-lock) keep using `MarkDirtyChangeRecord` —
their `emitter` IS a PageImage-equivalent, so the FPI-or-emitter
toggle is correct (would emit two PageImages otherwise).

### Why "logical first, FPI second" (and not the reverse)

Per-record idempotency on the standby relies on the page header's
`pd_lsn` carrying the *most-recently-applied* record's LSN.
`replayHeapInsert` treats `pd_lsn >= record.EndLSN` as "already
applied" and short-circuits. `replayPageImage` writes the bytes
verbatim (no LSN guard, no tuple-slot drift to worry about).

In the chosen order:

1. **Replay HeapInsert at `LSN_log`.** Page on disk has the prior
   epoch's state (or is freshly extended). `PageAddHeapTuple` lands
   the tuple at `lineSlot` (matches because the prior state on the
   standby matches the prior state on the primary — that's the whole
   recovery contract). Set `pd_lsn = LSN_log`.

2. **Replay PageImage at `LSN_fpi` (`> LSN_log`).** Writes the FPI
   bytes — which were snapshotted *after* the in-memory page got
   `SetLSN(LSN_log)` — so the FPI's encoded `pd_lsn` is `LSN_log`.
   Standby's page bytes now byte-identical to the primary's
   in-memory snapshot.

The reverse order ("FPI first, logical second") fails because after
the FPI restores the page, `replayHeapInsert` would try to add the
tuple at `lineSlot` against a page that *already* has it — slot
drift error. We'd then need slot-level idempotency in
`replayHeapInsert`, which complicates a hot replay path for no win.

### Why one new method, not a flag on the existing one

`MarkDirtyChangeRecord` has TWO live callers with mutually
exclusive contracts:

* B-tree / metapage paths — `emitter` returns the FPI's LSN itself
  (the emitter IS the FPI). Adding an "always also emit FPI" mode
  would double-emit PageImages on first-dirty-in-epoch.

* Heap mutation paths — `emitter` returns the logical record's LSN
  (FPI and logical are *different* records, both wanted on
  first-dirty-in-epoch).

A new method (`MarkDirtyLogicalChange`) makes the contract
explicit at the call site. The existing `MarkDirtyChangeRecord`
keeps its FPI-or-emitter semantics for non-logical paths.

## Implementation

### `internal/storage/bufpool.go`

New method `Pool.MarkDirtyLogicalChange(s *Slot, emitter func() (LSN, error)) error`:

1. Always run `emitter()` → `LSN_log`. `SetLSN(LSN_log)` on `s.page`.
2. If `needFPI && p.logFPI != nil && p.fullPageWrites.Load()`:
   snapshot `s.page` (which now carries `pd_lsn = LSN_log`),
   `p.logFPI(...)` → `LSN_fpi`. `SetLSN(LSN_fpi)`.
3. Mark `s.dirty = true` and `s.fpiSinceCheckpoint = true`.

Doc comment cross-references this design doc and explains the
upstream-PG semantic mapping.

### `internal/executor/operators_storage.go`

Three helpers re-routed from `MarkDirtyChangeRecord` to
`MarkDirtyLogicalChange`:

* `markHeapInsertDirty` — used by every INSERT path (regular insert,
  multi-insert, INSERT … SELECT, MERGE NOT MATCHED, ON CONFLICT
  insert, partition routing target).
* `markHeapDeleteDirty` — used by every DELETE path including the
  UPDATE-old-image stamp (`updateOp` calls it for the pre-image of a
  non-HOT update before `markHeapInsertDirty` for the new tuple).
* `markHeapHotUpdateDirty` — used by the HOT update path (atomic
  `RecordKindHeapHotUpdate` covering both old-slot xmax stamp and
  new-slot insert in one record).

Each call site picks up an inline `// see design 0103-0018` pointer
so future readers don't relapse into `MarkDirtyChangeRecord`.

Other callers of `MarkDirtyChangeRecord` (B-tree split / metapage,
heap opportunistic-prune, heap-lock, vacuum) keep their existing
behaviour — they have no logical-decoding requirement and double
PageImage emission would just waste WAL.

## Tests

* `internal/storage/bufpool_test.go::TestMarkDirtyLogicalChangeEmitsLogicalAndFPIOnFirstDirty` —
  pins both records appear in the captured WAL (logical first,
  FPI second), `pd_lsn` matches the FPI's LSN.
* `internal/storage/bufpool_test.go::TestMarkDirtyLogicalChangeEmitsLogicalOnlyOnSecondDirty` —
  pins that the second dirty in the same epoch emits only the
  logical record (no extra FPI).
* `internal/storage/bufpool_test.go::TestMarkDirtyLogicalChangeWithoutFPIHookEmitsLogicalOnly` —
  pins that the no-FPI-hook path always emits just the logical
  record (no double-emit on first dirty).
* `internal/wal/classifier_test.go::TestClassifyHeapInsertAfterFPIStillEmitsChange` —
  pins that a WAL stream containing PageImage followed by HeapInsert
  (the new emission shape) still routes the HeapInsert to a
  `ChangeInsert` event in the decoder.

## Affected packages

`internal/storage`, `internal/executor`, `internal/wal` (test only).
The replay path (`internal/wal/recovery.go`) needs no changes —
`replayHeapInsert`'s existing `pd_lsn`-based idempotency carries
the new "logical-then-FPI" emission shape correctly.

## Out of scope (deferred)

* `RecordKindHeapMultiInsert` / `RecordKindHeapUpdate` (non-HOT) —
  no executor emission site uses these encoders today; rung 12's
  first half (design 0103-0017) added classifier decode for the
  *replay* side (so an upstream-PG WAL stream classifies cleanly),
  but goopg's executor never produces them, so the
  `MarkDirtyLogicalChange` routing has nothing to wire up.
* `RecordKindHeapLock` — row-locking xmax stamps are not currently
  classified as logical-decoding events. If a future REPLICA
  IDENTITY FULL / `FOR KEY SHARE`-decoded path is added, it will
  need the same `MarkDirtyLogicalChange` re-routing.
* Restoring the `t.Skip` on `TestPort_PgoutputInteropGoopgToPG` —
  rung 12 is closed by this design doc plus 0103-0017; the live
  interop ladder advances to rung 13 (which surfaces its own
  next blocker). The Skip stays in place so each rung lands with
  a dedicated design doc + targeted unit pin, per the rung
  protocol.

## References

* `docs/design/0103-0017-classify-heap-update-records.md` — rung 12
  first half: `RecordKindHeapHotUpdate` / `RecordKindHeapUpdate`
  classifier decode.
* `docs/design/0008-0001-logical-decoding-pipeline.md` — overall
  logical decoding pipeline.
* `docs/design/0002-0003-redo-records.md` — `MarkDirtyChangeRecord`
  semantics and the "FPI then change records" replay invariant.
