# REF-020: Row Locking (FOR UPDATE / FOR SHARE)

## Overview

`SELECT … FOR UPDATE` and `SELECT … FOR SHARE` lock selected rows against concurrent updates or deletions. `FOR UPDATE` prevents other transactions from modifying or locking the row; `FOR SHARE` prevents modification but allows concurrent share locks. `NOWAIT` and `SKIP LOCKED` control behaviour when the lock cannot be acquired immediately.

## goopg Implementation

**Package:** `internal/executor/operators_lockrows.go`

### Plan Node

`planner.LockRows` wraps a child plan (typically a SeqScan or
IndexScan) and adds row-locking behaviour:

```go
type LockRows struct {
    pos          int
    Child        Node
    LockStrength LockStrength   // ForUpdate, ForNoKeyUpdate, ForShare, ForKeyShare
    WaitPolicy   LockWaitPolicy // Wait, NoWait, SkipLocked
    LockedRels   []LockedRel   // specific tables to lock
}
```

### Executor

`lockRowsOp` intercepts rows from the child plan and for each row:

1. Checks if the row is already visible (MVCC snapshot).
2. If visible and the row has `xmax` set by another transaction,
   blocks on the tuple lock via the lock manager.
3. Stamps the locking transaction's XID into `xmax` and sets
   `HEAP_XMAX_LOCK_ONLY` in the infomask.
4. Returns the locked row.

### NOWAIT / SKIP LOCKED

- **NOWAIT**: if the row is already locked, return a lock-error
  immediately instead of blocking.
- **SKIP LOCKED**: skip rows that are already locked, returning
  only those that can be locked immediately.

### HeapTuple Locking

Tuple-level locks are stored in the tuple's `xmax` field:
- `xmax` = locking XID.
- `HEAP_XMAX_LOCK_ONLY` infomask bit distinguishes a lock from a
  delete.
- `HEAP_XMAX_KEYSHR_LOCK`, `HEAP_XMAX_SHR_LOCK` bits distinguish
  lock strength (FOR SHARE vs FOR UPDATE).

## PostgreSQL Implementation

PostgreSQL's row locking (`nodeLockRows.c`, `heapam.c`) is more
comprehensive:

- **Multi-holdership** — PostgreSQL allows multiple transactions
  to hold `FOR SHARE` locks on the same tuple (via
  MultiXactId). goopg uses a simpler single-holder approach.
- **Lock strength** — PostgreSQL distinguishes
  `FOR KEY SHARE`, `FOR SHARE`, `FOR NO KEY UPDATE`, and
  `FOR UPDATE`. goopg supports `FOR UPDATE` and `FOR SHARE`.
- **Locking clauses on joins** — `SELECT … FROM t1 JOIN t2 … FOR
  UPDATE OF t1` limits locking to specific tables. goopg
  supports `LockedRels`.
- **NOWAIT error code** — PostgreSQL returns `55P03` when NOWAIT
  cannot acquire a lock. goopg returns a generic lock error.

### Key Differences

| Aspect | goopg | PostgreSQL |
|--------|-------|------------|
| Lock strength | FOR UPDATE, FOR SHARE | 4 levels (KEY SHARE, SHARE, NO KEY UPDATE, UPDATE) |
| Multi-holdership | Single holder | MultiXact for multiple share holders |
| NOWAIT error code | Generic | 55P03 (lock_not_available) |
| Skipped rows (SKIP LOCKED) | Supported | Supported |

## References

- goopg: `internal/executor/operators_lockrows.go`
- PG row locking: `postgres/src/backend/executor/nodeLockRows.c`
- PG heap lock: `postgres/src/backend/access/heap/heapam.c` (heap_lock_tuple)
