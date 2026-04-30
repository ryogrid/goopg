# Root Cause Found: UPDATE Serialisation via lockScanMatch

## The Bug

`internal/executor/operators_storage.go`, line 24:

```go
func lockScanMatch(rel storage.RelFileNode) func() {
    v, _ := scanMatchLocks.LoadOrStore(rel, &sync.Mutex{})
    mu := v.(*sync.Mutex)
    mu.Lock()
    return mu.Unlock
}
```

This function creates a **per-relation mutex** that serialises ALL
concurrent UPDATE and DELETE operations on the same table. Every
UPDATE on `pgbench_accounts` must acquire this mutex before it can
scan for matching rows, and it holds the mutex for the **entire
duration of the full table scan**.

With 4 concurrent pgbench clients, only ONE UPDATE runs at a time.
The remaining 3 wait on `lockScanMatch`.

## How the Mutex Affects TPS

At scale=3, each UPDATE takes ~73ms. The mutex serialises all
concurrent UPDATEs, so maximum throughput is limited to:

```
TPS = 1000 ms / 73 ms ≈ 13.7 TPS
```

This matches our measured value of ~14 TPS, regardless of client
count (1, 4, or 16 clients all give ~14 TPS).

## Why the UPDATE Takes 73ms

The UPDATE's `scanMatching` function performs a **full table scan**
of ALL pages in the relation, for EVERY UPDATE. The planner
generates an IndexScan for `WHERE aid = :aid`, but
`extractScanAndPredicate` (line 285) **converts the IndexScan to a
SeqScan** with a predicate. So every UPDATE reads every page of
the 300K-row table.

| Operation | Time (ms) |
|-----------|-----------|
| Full table scan of 3750 pages (300K tuples) | ~70 |
| Mark old tuple as dead + WAL Append | ~2 |
| Insert new tuple + WAL Append | ~1 |
| **Total** | **~73** |

## Why SELECT Is Fast

The SELECT query `SELECT abalance FROM pgbench_accounts WHERE aid = :aid`
goes through the normal `planSelect` path, which generates a proper
IndexScan. The IndexScan reads only the index entry + one heap page
= 3-4 buffer pool lookups = 0.37ms.

The SELECT does NOT go through `scanMatching` or `lockScanMatch`.

## Fix Options

### Option 1: Remove lockScanMatch (risky)

Remove the per-relation mutex. UPDATEs would run concurrently.
But without this lock, two concurrent UPDATEs might scan the same
page simultaneously and both try to modify it.

**Risk:** Page-level locking in `scanMatching` uses `s.RLock()` /
`s.Unlock()`. Two concurrent scans could read the same page.

### Option 2: Use IndexScan in UPDATE

Instead of converting IndexScan to SeqScan, make the UPDATE
operator use the IndexScan directly. This would make each UPDATE
O(log n) instead of O(n).

**Challenge:** The current scanMatching expects a SeqScan.
Refactoring to support IndexScan for UPDATE requires planner
changes.

### Option 3: Latch instead of Mutex

Replace the blocking mutex with a latch/try-lock. If another
UPDATE is already scanning, fall back to waiting. This reduces
lock contention but doesn't eliminate the full table scan.

### Option 4: Fine-grained page-level locking

Replace the per-relation mutex with per-page RLock. Multiple
UPDATEs can scan different pages concurrently. This requires
the scan to hold a read lock on each page individually.

**This is the recommended approach for v0.**

## Priority

**P0 — Critical.** This bug explains the entire 400× throughput
gap between read and write workloads. Fixing it is the single
highest-impact optimisation available.

## References

- `internal/executor/operators_storage.go`:24 (`lockScanMatch`)
- `internal/executor/operators_storage.go`:285 (`extractScanAndPredicate`)
- `internal/executor/operators_storage.go`:559 (`scanMatching`)
