# TPC-H E2E Power Test — shared_buffers=2000M with Go Heap Arena

**Date:** 2026-05-02
**goopg commit:** `167c9c2` (M0032-0001 landed)
**Test machine:** x86_64 Linux, 32 GB RAM, Go 1.25.0

## Configuration

| Parameter              | Value         |
|------------------------|---------------|
| `shared_buffers`       | 2000 MB       |
| `shared_buffers` slots | 256,000 (8 KiB each, 2.0 GB arena) |
| `GOMEMLIMIT`           | 40 GiB        |
| `checkpoint_timeout`   | 15 min        |
| `max_wal_size`         | 4 GB          |
| Arena type             | Go heap `make([]byte)` (M0032-0001) |
| Test dataset           | Synthetic (8 tables, 5–16 rows each) |
| Indexes                | 9 single-column + composite B-tree indexes |

## Results: Q1–Q22 Execution

| Query | Status | Notes |
|-------|--------|-------|
| Q1    | PASS   | Grouped aggregate with SUM/AVG/COUNT, ORDER BY |
| Q2    | PASS   | Correlated scalar subquery, 5-table join, ORDER BY (0 rows — no brass parts) |
| Q3    | PASS   | 3-table join, GROUP BY, ORDER BY |
| Q4    | PASS   | EXISTS subquery, GROUP BY |
| Q5    | PASS   | 6-table join, GROUP BY, ORDER BY |
| Q6    | PASS   | Range scan with aggregation |
| Q7    | FAIL   | `date_part` function not implemented (0A000) |
| Q8    | FAIL   | `date_part` function not implemented (0A000) |
| Q9    | FAIL   | `date_part` function not implemented (0A000) |
| Q10   | PASS   | 4-table join, GROUP BY, ORDER BY |
| Q11   | PASS   | HAVING with subquery, GROUP BY, ORDER BY |
| Q12   | PASS   | CASE expression, GROUP BY, ORDER BY |
| Q13   | PASS   | LEFT JOIN, derived table (subquery in FROM), ORDER BY |
| Q14   | PASS   | PROMO-revenue ratio with CASE, 2-table join |
| Q15   | PASS   | CREATE VIEW + SELECT with view + DROP VIEW |
| Q16   | PASS   | NOT IN subquery, COUNT(DISTINCT), GROUP BY |
| Q17   | PASS   | Correlated scalar subquery, 2-table join |
| Q18   | PASS   | IN subquery, 3-table join, GROUP BY, ORDER BY |
| Q19   | PASS   | Complex OR predicate with 3 branches, 2-table join |
| Q20   | PASS   | Nested IN subqueries with correlation |
| Q21   | PASS   | EXISTS / NOT EXISTS with self-join, GROUP BY |
| Q22   | FAIL   | `SUBSTRING(x FROM n FOR m)` syntax not supported |

**Score: 18 / 22 (82%)**

All 4 failures are pre-existing feature gaps (no regressions):
- Q7/Q8/Q9: `date_part(text, timestamp)` function — not yet implemented in goopg's
  built-in function registry.
- Q22: `SUBSTRING(string FROM start FOR count)` syntax — goopg's parser only supports
  `SUBSTRING(string, start, count)` variant.

## Memory Behaviour

| Metric        | After startup | After full query suite |
|---------------|---------------|----------------------|
| VmRSS         | 49,792 kB     | 79,360 kB            |
| VmSize        | ~4.0 GB       | ~4.0 GB              |
| VmPeak        | 4,076 MB      | 4,076 MB             |

- **No OOM**: The server ran stably with `shared_buffers=2000M` (2.0 GB Go heap arena)
  through all DDL, DML, index creation, and 22 query executions.
- **RSS stayed low (79 MB)**: The 2.0 GB arena is a demand-paged Go `[]byte` — only
  the pages actually touched by the workload become physically resident. The synthetic
  dataset was tiny (~100 rows), so only ~30 MB of the arena was faulted in.
- **VmPeak is 4 GB**: This includes the 2 GB arena allocation + Go runtime overhead +
  stack/heap. Under GC control with `GOMEMLIMIT=40GiB`, there is ample headroom.

## Key Observations

1. **mmap → Go heap switch is stable**: No regressions in the buffer pool, storage,
   or any executor path. All existing tests pass.

2. **shared_buffers=2000M no longer causes immediate OOM**: The previous issue
   (anonymous mmap pages growing RSS to the full arena size) is avoided because the
   Go heap arena pages are demand-paged and managed by the runtime. With 32 GB RAM,
   even fully-resident 2 GB arena would be fine.

3. **Remaining feature gaps for full HammerDB run**:
   - `date_part()` function (affects Q7, Q8, Q9)
   - `SUBSTRING(string FROM n FOR m)` syntax (affects Q22)
   - These are already tracked in `fix_plan.md` and are not blocking for the
     buffer-pool fix validation.

4. **Full SF=1 HammerDB run pending**: A complete TPC-H run with SF=1 data (~6M
   lineitem rows) is the next step (M0032-0002 follow-up). The synthetic dataset
   validates execution correctness but does not stress the buffer pool at scale.
   The full run requires HammerDB + TCL and is expected to take several hours.

## Next Steps

- M0032-0002: Run full HammerDB TPC-H power test at SF=1 with `shared_buffers=2000M`
  to verify query performance improvement from the larger buffer pool.
- Implement `date_part()` function (M0023 / syntax integration suite).
- Implement `SUBSTRING(x FROM n FOR m)` syntax variant (M0023).
