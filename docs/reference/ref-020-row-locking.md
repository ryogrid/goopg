# REF-020: Row Locking (FOR UPDATE)

…(existing content through "Key Differences" unchanged)…

## PostgreSQL Implementation (Deep Dive)

### MultiXact for Multi-Holder Share Locks

When multiple transactions hold `FOR SHARE` or `FOR KEY SHARE`
on the same tuple, PostgreSQL uses a **MultiXactId** (multi-
transaction ID) in the `xmax` field instead of a single XID.
The MultiXactId references a shared `pg_multixact` entry that
lists all locker XIDs and their lock strengths.

This allows an arbitrary number of concurrent share-lock holders
on the same tuple. Each holder adds its XID to the MultiXact
entry. The entry is cleared when all holders release.

goopg uses a single XID in `xmax`. Multiple concurrent share
locks on the same tuple are not supported — the second locker
blocks on the first's tuple lock.

### Lock Strength (4 Levels)

PostgreSQL supports four row-locking strengths:

1. **FOR KEY SHARE** — prevents deletion of the key columns.
   Weakest. Compatible with all non-conflicting lock modes.
2. **FOR SHARE** — prevents UPDATE/DELETE. Allows concurrent
   FOR SHARE and FOR KEY SHARE.
3. **FOR NO KEY UPDATE** — prevents concurrent KEY SHARE/UPDATES.
   Allows concurrent FOR SHARE.
4. **FOR UPDATE** — prevents any concurrent update/lock.
   Strongest.

goopg supports FOR UPDATE and FOR SHARE but not the intermediate
levels.

### NOWAIT Error Code

PostgreSQL returns SQLSTATE `55P03` (`lock_not_available`) when
`NOWAIT` cannot acquire a lock immediately. Clients can catch
this specific error code and handle it differently from other
lock errors.

goopg returns a generic `ExecError` without a specific SQLSTATE
for NOWAIT failures.

### Tuple-Level Lock Storage

PostgreSQL stores tuple locks via the `t_infomask` bitfield:

- `HEAP_XMAX_LOCK_ONLY` — xmax is a locker, not a deleter.
- `HEAP_XMAX_KEYSHR_LOCK` — FOR KEY SHARE.
- `HEAP_XMAX_SHR_LOCK` — FOR SHARE.
- `HEAP_XMAX_EXCL_LOCK` — FOR UPDATE / FOR NO KEY UPDATE.
- `HEAP_KEYS_UPDATED` — the update modified key columns.

goopg uses `PageSetHeapTupleXmax` to stamp the xmax and sets
`HEAP_XMAX_LOCK_ONLY` for FOR SHARE (determined by `IsForShare`
in `planLockRows`).

## goopg Improvement Analysis

### P1: MultiXact for Share Locks

Implement MultiXact entries to allow multiple concurrent
FOR SHARE holders on the same tuple.

**Impact:** Correctness for concurrent SELECT FOR SHARE on the
same row. Currently, FOR SHARE behaves like FOR UPDATE under
concurrency.

### P2: FOR NO KEY UPDATE / FOR KEY SHARE

Implement the two intermediate lock strengths. FOR NO KEY UPDATE
is the default for UPDATE statements (PG14+).

### P2: NOWAIT Error Code

Return SQLSTATE 55P03 (lock_not_available) for NOWAIT failures.

## References

- goopg: `internal/executor/operators_lockrows.go`
- PG row locking: `postgres/src/backend/executor/nodeLockRows.c`
- PG heap lock: `postgres/src/backend/access/heap/heapam.c`
  (`heap_lock_tuple`)
- PG MultiXact: `postgres/src/backend/access/transam/multixact.c`
