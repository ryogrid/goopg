# Heap-Only Tuples (HOT) — M0046-0001

| field      | value                         |
|------------|-------------------------------|
| status     | accepted                      |
| date       | 2026-05-05                    |
| supersedes | —                             |

## 1. Problem

Every UPDATE on a heap relation currently follows the "delete + insert" pattern:
1. Stamp `xmax` on the old tuple (in-place header write).
2. Append the new tuple to the last heap page (may extend the relation).

This has two costs:
- **Heap bloat**: each UPDATE adds a new page slot; old slots are only reclaimed
  by VACUUM.
- **Index bloat** (future): once index maintenance is wired for regular INSERTs,
  every UPDATE would append a new index entry pointing at the new TID even when
  no indexed column changed.

PostgreSQL's *Heap-Only Tuple* mechanism (HOT, upstream
`postgres/src/backend/access/heap/heapam.c`) eliminates both costs when no
indexed column is modified and there is free space on the existing page.

## 2. Design

### 2.1 Eligibility

A HOT update is attempted when **all** of the following hold:

1. `hotUpdateEligible(plan, ctx)` returns `true`: none of the columns referenced
   in `plan.Set[i] != nil` appears in any index on the table
   (`ctx.Catalog.IndexesOnTable`).
2. `tryApplyHOTUpdate` succeeds: `PageAddHeapTuple` on the *same* block as the
   old tuple finds enough free space.

When either condition fails the update falls back to the classic delete+insert
path unchanged.

### 2.2 Storage layer additions (`internal/storage/heap.go`)

Two new infomask constants mirror upstream:

```go
HeapHotUpdated uint16 = 0x4000   // HEAP_HOT_UPDATED upstream
HeapOnlyTuple  uint16 = 0x8000   // HEAP_ONLY_TUPLE  upstream
```

`PageStampHotOldTuple(page, oldSlot, xmax, blk, newSlot)` performs in-place
header mutations on the old tuple:
- Write `xmax` at `off+4`.
- Write CTID `(blk, newSlot)` at `off+12`–`off+18`.
- Clear `HeapXmaxLockOnly | HeapXmaxLockMask` (same as `PageSetHeapTupleXmax`).
- Set `HeapHotUpdated` in infomask at `off+20`.

### 2.3 HOT update execution (`internal/executor/operators_storage.go`)

`tryApplyHOTUpdate(ctx, rel, cols, blk, oldSlot, newRow) (bool, error)`:
1. Encode `newRow`; set `HeapOnlyTuple` in `tuple.Header.Infomask` before
   marshalling.
2. Pin page `blk` with exclusive lock.
3. `PageAddHeapTuple(page, tuple)` → `newSlot`; return `(false, nil)` on
   `ErrNoSpaceInPage` (caller falls back to normal path).
4. `PageStampHotOldTuple(page, oldSlot, ctx.Tx.XID, blk, newSlot)`.
5. `markHeapHotUpdateDirty` (WAL record or FPI fallback).
6. Return `(true, nil)`.

Both `updateViaIndex` and the sequential `Next()` path call
`hotUpdateEligible` once per statement and gate `tryApplyHOTUpdate` per row.

### 2.4 WAL record (`internal/wal/recovery.go`)

`RecordKindHeapHotUpdate = 13` (new constant after `RecordKindSmgrTruncate = 12`).

Fixed header (20 bytes) + variable new-tuple bytes:
```
kind(1) | DBOid(4) | RelOid(4) | Fork(1) | Block(4) | oldSlot(2) | xmax(4)
```

`tupleBytes` is the marshalled new tuple with `HeapOnlyTuple` already set in its
infomask so the replay path gets the correct flag without extra bookkeeping.

**Replay** (`replayHeapHotUpdate`):
1. Read page `blk` (error if block absent or uninitialized).
2. pd_lsn idempotency check — skip if `LSN >= record.EndLSN`.
3. `ParseHeapTuple(tupleBytes)` → `tup`.
4. `PageAddHeapTuple(page, tup)` → `newSlot`.
5. `PageStampHotOldTuple(page, oldSlot, xmax, blk, newSlot)`.
6. Update pd_lsn; write page back.

The WAL hook is wired in `internal/initdb/open.go` as `logHeapHotUpdate`,
following the same pattern as `logHeapDelete` / `logHeapInsert`.

### 2.5 HOT chain following (`internal/executor/operators_index.go`)

`followHOTChain(page, startSlot, snap, xid) (HeapTuple, uint16, bool)`:
- Walk `startSlot → CTID.Offset` up to 64 steps (chain depth guard).
- For each slot: `TupleVisible` → return it. Not visible + `HeapHotUpdated` →
  advance to `CTID.Offset`.
- Return `(_, _, false)` when chain is exhausted or a self-reference is detected.

Both `indexScanOp.Open()` `scanFn` and `updateViaIndex` call `followHOTChain`
instead of the previous direct `PageGetHeapTuple` + visibility check. The `tids`
slice in `indexScanOp` records the *actual live slot* from the chain walk so
`lockRowsOp` stamps the correct version for `SELECT FOR UPDATE`.

Sequential scans (`seqScanOp`) do **not** use `followHOTChain`: MVCC handles
HOT-only tuples directly (they have `xmin = update_xid`; standard visibility
applies).

## 3. Invariants

| Property | Guarantee |
|---|---|
| Same-page chain | All versions in a HOT chain reside on the same block. |
| HOT-only flag | Any tuple with `HeapOnlyTuple` has no direct index entry (future index maintenance must respect this). |
| Atomic WAL record | Old-tuple stamp + new-tuple insert encoded in one record; replay is all-or-nothing under pd_lsn idempotency. |
| Fallback | Page-full → classic delete + insert path, no data loss. |

## 4. Tests (`internal/executor/hot_update_test.go`)

| Test | Coverage |
|---|---|
| `TestHOTUpdateSamePage` | New tuple lands on same page; old tuple flags `HeapHotUpdated` + CTID chain; new tuple flags `HeapOnlyTuple`; no heap page growth. |
| `TestHOTUpdateIndexScanFindsNewVersion` | Index scan returns HOT-updated value via chain following. |
| `TestHOTUpdateIndexedColumnFallback` | Update of indexed column → normal path (no `HeapHotUpdated` flag). |
| `TestHOTUpdateChainDepthTwo` | Two consecutive HOT updates → depth-2 chain; index scan returns final version. |
| `TestFollowHOTChainDirect` | Unit test: manually constructed HOT chain traversed correctly. |

## 5. Deferred

- **ItemIDRedirect** line pointers — VACUUM-phase HOT chain compaction (M0046-0002).
- **Visibility map integration** — cleared on any page modification once the VM fork exists.
- **Index maintenance for regular INSERTs** — when wired, non-HOT UPDATEs will
  insert into indexes; `hotUpdateEligible` already prevents double-indexing on
  the HOT path.
