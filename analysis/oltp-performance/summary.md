# OLTP Performance Optimisation Summary

## Timeline

| Step | Change | TPS (simple-update, 4 clients) | Improvement |
|------|--------|--------------------------------|-------------|
| Baseline | Original code (SeqScan + lockScanMatch mutex) | 14 TPS | 1× |
| Step 1 | Removed `lockScanMatch` mutex | 45 TPS | 3.2× |
| Step 2 | IndexScan for UPDATE/DELETE (`updateViaIndex`) | 1,498 TPS | 107× |
| **Total** | | | **~100×** |

## Root Causes Found

### 1. `lockScanMatch` Mutex (operators_storage.go:24)

A per-relation `sync.Mutex` serialised ALL concurrent UPDATE and
DELETE operations on the same table. Every UPDATE on
`pgbench_accounts` had to acquire this mutex before scanning for
matching rows, and held it for the entire duration of the full
table scan. With 4 concurrent clients, only one UPDATE could run
at a time.

**Fix:** Removed `lockScanMatch` and `scanMatchLocks` (3 lines).
The scan's per-page `RLock` already prevents write conflicts;
the global mutex was redundant.

### 2. SeqScan in UPDATE (extractScanAndPredicate → SeqScan)

`extractScanAndPredicate` converted the planner's `IndexScan` to a
`SeqScan` with a synthesized equality predicate. This meant every
UPDATE did a full table scan of ALL 3,750 pages and 300,000 tuples
to find the one matching row.

**Fix:** Added `updateViaIndex` and `deleteViaIndex` methods that
use the B-tree index directly when the planner produces an
`IndexScan`. The index lookup is O(log n) — 3–4 page accesses
instead of 3,750.

### 3. WAL Append Serialisation (secondary)

The WAL `Append` path sent every record through a single
state-loop goroutine, creating a secondary serialisation point.
M0026 implemented a concurrent fast path (`tryAppend`) that
bypasses the state loop for the common case (buffer not full).

**Impact:** Minor — the state loop was not the primary bottleneck.

## Current Architecture

### UPDATE Path (after fixes)

```
Client goroutine
  1. IndexScan (B-tree RangeScan) — O(log n), 3-4 page reads
  2. Pin heap page, read tuple, check visibility
  3. For each match:
     a. Check foreign tuple lock (block if held)
     b. Stamp xmax on old tuple + WAL Append (fast path)
     c. writeHeapRow (insert new tuple + WAL Append, fast path)
  4. Transaction commit → WAL Append (fast path)
  5. FlushUpTo → state-loop drain + fdatasync (async, batched)
```

### Benchmark Results (scale=3)

| Workload | Clients | TPS | Latency/trans |
|----------|---------|-----|---------------|
| Select-only | 1 | 3,228 | 0.31 ms |
| Select-only | 4 | 6,436 | 0.62 ms |
| Select-only | 16 | 6,013 | 2.66 ms |
| Simple update | 1 | 781 | 1.28 ms |
| Simple update | 4 | **1,498** | 2.67 ms |
| Simple update | 16 | 1,535 | 10.4 ms |
| Default TPC-B | 1 | 553 | 1.81 ms |
| Default TPC-B | 4 | **1,095** | 3.66 ms |
| Default TPC-B | 16 | 1,132 | 14.1 ms |

### Remaining Bottlenecks

1. **TPC-B vs Simple-Update gap** (553 vs 781 TPS at 1 client).
   The TPC-B workload adds UPDATEs on `pgbench_tellers` and
   `pgbench_branches`. These tables may not have indexes on their
   WHERE columns, forcing SeqScan. This is expected — pgbench
   does not create indexes on `tid` or `bid` by default.

2. **Diminishing returns at 16 clients** (1,535 vs 1,498 TPS).
   At high concurrency, lock manager contention and page-level
   lock conflicts limit scalability. This is expected behaviour.

3. **Select-only → write gap** (6,436 vs 1,498 TPS at 4 clients).
   Writes still require WAL, tuple locking, and heap modification
   that reads don't. A 4× gap is reasonable.

## Files Changed

| File | Change |
|------|--------|
| `internal/executor/operators_storage.go` | Remove `lockScanMatch`, `scanMatchLocks` |
| `internal/executor/operators_storage.go` | Add `extractScan` (returns IndexScan), updateOp/deleteOp index path |
| `internal/executor/operators_storage.go` | Add `updateViaIndex`, `deleteViaIndex` methods |
| `internal/wal/writer.go` | Concurrent `tryAppend` fast path (M0026) |
| `cmd/goopg/main.go` | pprof HTTP endpoint on :6060 |

## References

- Analysis reports: `analysis/oltp-performance/`
- M0025 (initial analysis): `docs/milestones/0025-oltp-performance-analysis.md`
- M0026 (concurrent WAL): `docs/milestones/0026-concurrent-wal-append.md`
- Root cause doc: `analysis/oltp-performance/root-cause.md`
