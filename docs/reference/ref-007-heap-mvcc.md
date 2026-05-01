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

## PostgreSQL Implementation (Deep Dive)

### Heap-Only Tuples (HOT)

When an UPDATE does not modify any indexed column, PostgreSQL can
store the new tuple version on the same heap page and link it to
the old version via the `t_ctid` field. This creates a **HOT
chain**: `old → new`. Subsequent index scans that find the old
entry follow `t_ctid` to find the new tuple without touching the
index.

goopg always inserts the new tuple version via `writeHeapRow`,
which may place it on any page, not necessarily the same page.
This means every UPDATE in goopg invalidates the index entry and
could require an index update (though goopg currently does not
update the index on UPDATE).

**HOT benefits:**
- No index writes for non-key-column UPDATEs.
- No index bloat from UPDATE traffic.

### Heap Page Pruning (`heap_page_prune`)

PostgreSQL's `heap_page_prune` runs as a side effect of index
scans and bitmap scans. When a page is read, the scan checks
for dead line pointers (tuples whose xmax has committed). If
found, it reclaims the space immediately by compacting the page.

goopg does not prune dead tuples during reads. Dead tuples are
only reclaimed by VACUUM. This means heap pages grow
monotonically between VACUUM runs.

### Free Space Map (FSM)

Each relation has an FSM (a secondary relation) that tracks free
space per page at a granularity of 256 levels. When inserting a
new tuple, `GetPageWithFreeSpace` searches the FSM for a page
that can accommodate the tuple. This avoids the linear "try the
last block, then extend" approach in goopg.

**Implementation sketch for goopg:**

```go
func findPageForInsert(pool *Pool, rel RelFileNode, tupleSize int) (blk BlockNumber, err error) {
    fsm := openFSM(rel)
    blk = fsm.Search(tupleSize)
    if blk == InvalidBlock {
        return extendRelation(pool, rel) // no page found — extend
    }
    return blk, nil
}
```

### Visibility Map (VM)

Each relation has a VM (a secondary relation) with one bit per
heap page. The bit is set when all tuples on the page are visible
to all active snapshots. The VM enables:

- **Index-only scans** — if the VM bit is set, the index scan
  does not need to visit the heap page to check visibility.
- **VACUUM skipping** — VACUUM skips pages whose VM bit is set,
  dramatically reducing vacuum time for mostly-static tables.

goopg does not have a VM.

### Tuple Freezing

PostgreSQL's XID space is 32-bit (about 4 billion transactions).
To prevent XID wraparound, `FREEZE` replaces `xmin` with
`FrozenTransactionId` (2) for tuples whose xmin is older than
`vacuum_freeze_min_age` (default 50 million). Frozen tuples are
considered visible regardless of their original xmin.

goopg does not freeze tuples. For long-running deployments, this
will eventually cause XID wraparound issues.

### TOAST (The Oversized-Attribute Storage Technique)

PostgreSQL stores column values larger than ~2 KB in a separate
TOAST table. The heap tuple contains a reference (OID) to the
TOAST entry. This keeps heap tuples small and prevents page
overflow.

goopg does not implement TOAST. Large column values are stored
directly in the heap tuple, which may cause `PageAddHeapTuple`
to fail with `ErrNoSpaceInPage`.

## goopg Improvement Analysis

### P0: HOT Updates

**Impact:** Eliminates index bloat for UPDATEs that do not modify
indexed columns. In the pgbench TPC-B workload, 100% of UPDATEs
target indexed columns (primary key), so HOT would not apply
there. For general workloads with non-key UPDATEs, the benefit
is significant.

### P1: Heap Page Pruning

Add a pruning step in `scanMatching` and `seqScanOp.Next()` that
reclaims dead tuples when reading a page. Check each slot's xmax;
if committed, clear the line pointer.

**Impact:** Reduces heap bloat between VACUUM runs. Each read
becomes self-cleaning.

### P1: Free Space Map

Implement a simple FSM with one `uint8` per page (256 levels of
fill). Update on INSERT (space consumed) and on VACUUM (space
reclaimed). Search on INSERT.

**Impact:** Reduces relation extension frequency. Tuples land on
pages that already have space instead of always growing the file.

### P2: Visibility Map

Add a VM with one bit per page. Set the bit when a VACUUM or
pruning confirms all tuples on the page are all-visible. Clear it
on any INSERT/UPDATE on the page.

**Impact:** Enables index-only scans and reduces VACUUM I/O for
read-heavy workloads.

### P2: Tuple Freezing

Add `freezePage` that scans a page and replaces `xmin` with
`FrozenTransactionId` for tuples older than the freeze horizon.
Call it during VACUUM on pages that haven't been frozen recently.

**Impact:** Prevents XID wraparound on long-running deployments.

## References

- goopg: `internal/storage/heap.go`
- goopg: `internal/mvcc/manager.go`
- goopg: `internal/executor/operators_storage.go`
- PG heap: `postgres/src/backend/access/heap/heapam.c`
- PG HOT: `postgres/src/backend/access/heap/heapam.c` (`heap_update`)
- PG pruning: `postgres/src/backend/access/heap/pruneheap.c`
- PG FSM: `postgres/src/backend/storage/freespace/`
- PG VM: `postgres/src/backend/access/heap/visibilitymap.c`
- PG freeze: `postgres/src/backend/access/heap/heapam.c` (`FreezeTuple`)
- PG TOAST: `postgres/src/backend/access/heap/tuptoaster.c`
