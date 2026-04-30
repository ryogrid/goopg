# REF-007: Heap Storage & MVCC

## Overview

The heap is the primary storage structure for table rows. Each row is stored as a `HeapTuple` on an 8 KiB page. MVCC (Multi-Version Concurrency Control) provides snapshot-based isolation: each transaction sees a consistent view of rows as of its snapshot time, without blocking readers.

## goopg Implementation

**Packages:** `internal/storage/heap.go`, `internal/mvcc/`

### Key Types

- `HeapTuple` — an in-memory tuple representation with a `Header` (xmin, xmax, infomask) and `Data` (column values).
- `HeapTupleHeader` — `xmin` (creating XID), `xmax` (deleting/locking XID), `infomask` (tuple flags), `t_hoff` (header offset).
- `Page` — 8 KiB byte array. Contains line-pointer array (slot → offset+length) and tuple data.
- `Manager` (mvcc) — handles transaction begin/commit/rollback, snapshot creation, XID assignment.

### Page Layout

```
┌──────────────────────────────────┐
│ PageHeaderData (24 bytes)        │
│  - pd_lsn, pd_checksum, flags    │
│  - lower (start of line pointers) │
│  - upper (start of free space)    │
├──────────────────────────────────┤
│ Line pointers (ItemIdData)        │
│  slot 1: {offset, length, flags}  │
│  slot 2: {offset, length, flags}  │
│  ...                              │
├──────────────────────────────────┤
│ Free space                        │
├──────────────────────────────────┤
│ Tuple data                        │
│  tuple 2: HeapTupleHeader + cols  │
│  tuple 1: HeapTupleHeader + cols  │
└──────────────────────────────────┘
```

### Tuple Visibility

`mvcc.TupleVisible(header, snap, currentXID)` checks:
1. `xmin` is committed in the snapshot → visible.
2. `xmin` is our own transaction → visible (in-progress).
3. `xmax` is InvalidTransactionID → visible (not deleted).
4. `xmax` is committed → not visible (deleted by committed xact).
5. `xmax` is our own → visible only if we aborted (will be rolled back).

### Insert Path

`writeHeapRowReturning`:
1. Encode columns → binary tuple bytes.
2. Marshal into `HeapTuple` (xmin = current XID, xmax = invalid).
3. Pin the last block of the relation.
4. Call `PageAddHeapTuple` (finds free space on the page).
5. If no space, extend the relation (lock `heapExtendLocks`).
6. Mark dirty + WAL-log the insert.

### Update Path

`updateOp.Next()`:
1. Find matching tuple (via IndexScan or SeqScan).
2. Stamp `xmax` on old tuple → `PageSetHeapTupleXmax`.
3. Mark dirty + WAL-log the delete.
4. `writeHeapRow` → insert new tuple version.

### Lock Manager Integration (MVCC)

The `HeapXmaxLockOnly` infomask bit distinguishes a row lock from a delete. Lock-only xmax values are checked in `foreignLockOnly` and cause the UPDATE/DELETE to block on `acquireTupleLock`.

## PostgreSQL Implementation

PostgreSQL's heap (`heapam.c`) is structurally very similar but includes several optimisations:

- **Heap only tuple (HOT)** — when an UPDATE touches no indexed columns, the new tuple is placed on the same page and a HOT chain links old→new. Readers follow the chain without touching the index. goopg does not implement HOT updates.
- **Pruning** — `heap_page_prune` removes dead tuple versions during reads. goopg relies on VACUUM for this.
- **Free space map (FSM)** — each relation has an FSM tracking page-level free space. goopg always tries the last block, then extends.
- **Visibility map (VM)** — tracks which pages contain only all-visible tuples, enabling index-only scans and skipping VACUUM. goopg does not have a VM.
- **Tuple freezing** — `FreezeTuple` stamps `xmin = FrozenTransactionId` to prevent XID wraparound. goopg does not freeze.

### Key Differences

| Aspect | goopg | PostgreSQL |
|--------|-------|------------|
| HOT updates | Not implemented | On-page UPDATEs with HOT chain |
| Pruning | VACUUM only | `heap_page_prune` during reads |
| Free space search | Try last block, then extend | FSM (free space map) |
| Visibility map | None | VM page per relation (index-only scans, vacuum skipping) |
| XID freeze | Not implemented | `FreezeTuple` during VACUUM or eagerly |
| Tuple flags | Basic xmin/xmax/infomask | Extended infomask2, HEAP_KEYS_UPDATED, etc. |

## Potential Optimisations or Corrections

- **HOT updates** would eliminate unnecessary index updates for UPDATEs that don't modify indexed columns. This is common in pgbench's TPC-B workload.
- **Free space map** would reduce relation extension frequency by finding pages with available space.
- **Visibility map** would enable index-only scans and let VACUUM skip all-visible pages.

## References

- goopg: `internal/storage/heap.go`
- goopg: `internal/mvcc/manager.go`
- goopg: `internal/executor/operators_storage.go` (writeHeapRow)
- PG heap: `postgres/src/backend/access/heap/heapam.c`
- PG visibility: `postgres/src/backend/utils/time/tqual.c`
- PG HOT: `postgres/src/backend/access/heap/heapam.c` (heap_update, HOT chain management)
