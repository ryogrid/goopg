# Logical Redo Records (Milestone 0002)

| Field      | Value                                                  |
| ---------- | ------------------------------------------------------ |
| Status     | draft                                                  |
| Date       | 2026-04-28                                             |
| Milestone  | 0002 — Production-Grade Checkpointing & Concurrent B-tree |
| Refines    | [0002-0001-checkpointing.md](0002-0001-checkpointing.md), [root-0008-wal-and-recovery.md](root-0008-wal-and-recovery.md) |
| Supersedes | —                                                      |

## Problem

The previous M0002 loop (recovery-test landing) flipped FPI emission
from "first dirty per epoch" to "every MarkDirty" because v0 had no
logical change records — without them, recording only the first-dirty
state silently lost every subsequent mutation on the same page across
crash recovery.

The fix was correctness-correct but expensive: every mutation now
emits a full 8 KB FPI. `pgbench -i -s 1` WAL volume jumped from
~10 MB to ~1.6 GB and `pgbench -i` runtime from ~7 s to ~18 s.
Logical change records are the upstream-aligned way to recover the
optimisation without losing replay correctness.

## Upstream reference

- `postgres/src/backend/access/heap/heapam.c` — `XLogRegisterBuffer`,
  `heap_xlog_insert`, `heap_xlog_delete`, `heap_xlog_update`.
- `postgres/src/backend/access/transam/xloginsert.c` —
  `XLogInsert` semantics: full-page-image bundled with the FIRST
  modification of a page after each checkpoint, change records
  for subsequent modifications. Idempotency keyed off
  `PageGetLSN(page) >= record.lsn`.
- `postgres/src/include/access/xlog.h` — record-kind taxonomy.

## Decisions

### Taxonomy: migrate hot paths incrementally

Each landing defines one or two record kinds and migrates the
corresponding mutation site:

- **0002-0003a** (loop landing this doc): `RecordKindHeapInsert` +
  `writeHeapRow`. pgbench-i WAL: ~1.6 GB → ~801 MB.
- **0002-0003b**: `RecordKindBtreeInsert` +
  `btree.insertIntoBlock` non-split path. pgbench-i WAL:
  ~801 MB → ~21 MB. `btree.ApplyInsertRecord` is the public
  replay helper that `wal/recovery.go` calls.
- **0002-0003c**: `RecordKindHeapDelete` (fixed
  20-byte record: rel + blk + lineSlot + xmax). Migrated both
  `updateOp` (xmax stamp on the old image) and `deleteOp`
  (xmax stamp on the visible match) via a new
  `markHeapDeleteDirty` helper. `pgbench -t 30` default-mixed
  workload now keeps WAL flat at ~21 MB.
- **0002-0003d** (this landing): `RecordKindHeapVacuum`
  (`kind | rel(9) | blk(4) | count(2) | slots[count](2 each)`,
  16 + 2*count bytes). VACUUM now collects the dead-slot list
  via the new `storage.CollectDeadHeapSlots`, applies the prune
  via `storage.VacuumHeapPageBySlots`, and emits the slot list
  through `Pool.MarkDirtyChangeRecord` so a pruned page only
  spends an FPI on the first prune-per-epoch. Replay re-runs
  `VacuumHeapPageBySlots` against the existing page bytes for a
  bit-exact prune; idempotent via pd_lsn. The per-page content
  latch (`Slot.Lock`) is now taken around the dead-set scan +
  repack + LSN stamp so concurrent inserters can't tear the
  prune.

Follow-up landing (2026-04-29): B-tree metadata/root-maintenance
paths (`CreateWithOptions`, `updateRootMeta`, `clearRootFlag`,
`createNewRoot`) now route through
`markDirtyWithPageRecord` -> `Pool.MarkDirtyChangeRecord`.
Subsequent same-epoch mutations on those pages emit a page-image
record via the pool's `LogPageImage` hook; first dirty keeps the
baseline FPI behaviour. With those paths migrated, `Pool.MarkDirty`
is restored to strict once-per-epoch FPI globally.

### Record format

```
RecordKindHeapInsert (4):
  byte 0      kind = 4
  bytes 1..5  DBOid     (uint32 LE)
  bytes 5..9  RelOid    (uint32 LE)
  byte 9      Fork
  bytes 10..14 Block     (uint32 LE)
  bytes 14..16 LineSlot  (uint16 LE; 1-based line-pointer slot
                          assigned by PageAddHeapTuple)
  bytes 16..end Tuple bytes (variable; the marshalled HeapTuple
                              payload, including header)
```

Replay: read existing page bytes, `pd_lsn`-compare for
idempotency, then `PageAddHeapTuple` at the recorded slot. If the
page is missing on disk (block >= NBlocks), Extend with an
InitPage'd page first, then add. After apply, set
`pd_lsn = record.endLSN` and write back via `WriteBlock`.

### Per-call selector: `Pool.MarkDirtyChangeRecord`

Existing `MarkDirty` keeps emitting FPI on every dirty (correct
for paths that haven't been migrated yet). The new method
`MarkDirtyChangeRecord(slot, emitter)` flips the once-per-epoch
optimisation back on for callers that supply a logical
change-record emitter:

- If `slot.fpiSinceCheckpoint` is false (first dirty in epoch),
  emit the FPI as the baseline. The logical record is redundant
  for replay (FPI already captures the post-mutation state) so
  it is NOT emitted in this case.
- Otherwise, call `emitter()` to append the logical record and
  stamp the resulting LSN onto `pd_lsn`. No FPI.
- Either way, set `dirty = true` and `fpiSinceCheckpoint = true`.

This gives the upstream invariant — at flush time, replay can
reconstruct the page from the FPI plus all subsequent change
records — while keeping the migration scoped to the heap-insert
path.

### Plumbing

`storage.PoolConfig.LogHeapInsert` carries the encoder/append
closure (matching the existing `LogPageImage` and
`LogBtreeSplit` patterns). `initdb.Open` constructs it from
`wal.EncodeHeapInsert` + `walWriter.Append`. Pool's
`LogHeapInsert()` accessor exposes it; `writeHeapRow` calls
`Pool.MarkDirtyChangeRecord` with a closure that emits the
record via the accessor. The executor doesn't import
`internal/wal` directly.

## Replay idempotency

The runtime calls `wal.ReplayFromDirWithMgr` on startup
(internal/initdb/open.go landed in the previous loop). HeapInsert
records replay via:

1. Read page (or InitPage if missing).
2. If `pd_lsn(page) >= record.endLSN`, skip — already applied.
3. Else `PageAddHeapTuple` at the recorded slot. Since slot
   numbers are monotonically assigned by `PageAddHeapTuple`,
   replaying the records in order reproduces the same line-pointer
   assignments.
4. Set page LSN, write back.

The slot-recording is what makes replay deterministic; v0 keeps
the line-pointer compaction simple, so the same slot index
re-emitted produces the same bytes.

## Out of scope (deferred to subsequent loops)

- Compact logical record kinds for B-tree metadata/root-maintenance
  paths (they currently use page-image change records).
- Catalog-page (`pg_class` etc.) change records — v0's catalog
  is JSON-on-disk so it's not a buffer-pool path.

## Test strategy

- `internal/wal/recovery_test.go` gets `TestReplayHeapInsertIdempotent`
  — applies the same record twice; second is a no-op via pd_lsn
  guard.
- `internal/wal/recovery_test.go` gets `TestEncodeDecodeHeapInsertRoundTrip`
  — pins the on-the-wire shape.
- `internal/initdb/recovery_test.go` (the M0002 crash-recovery test
  from the previous loop) already covers heap inserts end-to-end via
  the btree path; it must continue passing once the heap insert
  path is migrated.
- Manual `pgbench -i -s 1` to confirm WAL volume drops back near the
  pre-regression baseline (~10 MB target).

## Cross-references

- Milestone definition:
  [`docs/milestones/0002-durability-and-concurrent-storage.md`](../milestones/0002-durability-and-concurrent-storage.md).
- Crash-recovery test that surfaced the regression: see `internal/initdb/recovery_test.go`.
- Atomic split records (sibling design):
  [0002-0002-btree-concurrency.md](0002-0002-btree-concurrency.md).
