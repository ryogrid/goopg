# TPC-H E2E Power Test — Feature Gaps Closed (date_part, SUBSTRING FROM FOR)

**Date:** 2026-05-02
**goopg commit:** `966cd5d` + follow-up feature implementation
**Test machine:** x86_64 Linux, 32 GB RAM, Go 1.25.0

## Configuration

| Parameter              | Value         |
|------------------------|---------------|
| `shared_buffers`       | 2000 MB       |
| `shared_buffers` slots | 256,000 (8 KiB each, 2.0 GB arena) |
| `GOMEMLIMIT`           | 40 GiB        |
| Arena type             | Go heap `make([]byte)` (M0032-0001) |

## Features Implemented (2026-05-02)

### `date_part(text, timestamp)` — SQL built-in function

- **Files:** `internal/executor/expr.go` (dispatch + implementation),
  `internal/planner/planner.go` (type resolution → `int8`)
- Shared extraction logic via `extractTimestampField()` — reused by both
  `date_part` and `EXTRACT`.
- Supported fields: `year`, `month`, `day`, `hour`, `minute`, `second`,
  `dow`, `doy`, `epoch`, `quarter`.
- Required for TPC-H Q7, Q8, Q9.

### `SUBSTRING(str FROM start [FOR count])` — SQL-standard syntax

- **Files:** `internal/parser/select.go` (special-grammar parser)
- Both comma form (`SUBSTRING(str, start, count)`) and SQL-standard form
  (`SUBSTRING(str FROM start FOR count)`) are supported.
- Desugars to a regular `FuncCall` — existing executor `evalSubstr()` handles
  both forms identically.
- Required for TPC-H Q22.

## Results: Q1–Q22 Execution

| Query | Status | Notes |
|-------|--------|-------|
| Q1    | PASS   | Grouped aggregate with SUM/AVG/COUNT, ORDER BY |
| Q2    | PASS   | Correlated scalar subquery, 5-table join, ORDER BY |
| Q3    | PASS   | 3-table join, GROUP BY, ORDER BY |
| Q4    | PASS   | EXISTS subquery, GROUP BY |
| Q5    | PASS   | 6-table join, GROUP BY, ORDER BY |
| Q6    | PASS   | Range scan with aggregation |
| Q7    | PASS   | `date_part('year', ...)` — 6-table join + derived table |
| Q8    | PASS   | `date_part('year', ...)` — 7-table join + CASE + derived table |
| Q9    | PASS   | `date_part('year', ...)` — 6-table join + derived table |
| Q10   | PASS   | 4-table join, GROUP BY, ORDER BY |
| Q11   | PASS   | HAVING with subquery, GROUP BY, ORDER BY |
| Q12   | PASS   | CASE expression, GROUP BY, ORDER BY |
| Q13   | PASS   | LEFT JOIN, derived table, ORDER BY |
| Q14   | PASS   | PROMO-revenue ratio with CASE, 2-table join |
| Q15   | PASS   | CREATE VIEW + SELECT with view + DROP VIEW |
| Q16   | PASS   | NOT IN subquery, COUNT(DISTINCT), GROUP BY |
| Q17   | PASS   | Correlated scalar subquery, 2-table join |
| Q18   | PASS   | IN subquery, 3-table join, GROUP BY, ORDER BY |
| Q19   | PASS   | Complex OR predicate with 3 branches, 2-table join |
| Q20   | PASS   | Nested IN subqueries with correlation |
| Q21   | PASS   | EXISTS / NOT EXISTS with self-join, GROUP BY |
| Q22   | PASS   | `SUBSTRING(c_phone FROM 1 FOR 2)` syntax |

**Score: 22 / 22 (100%)**

All 22 TPC-H queries parse, plan, and execute without error on goopg. The
synthetic dataset produces results (some queries return 0 rows where the
small sample data falls outside the filter range, but execution is
confirmed error-free).

No OOM crash. RSS stable at ~54 MB throughout.

## Memory Behaviour

| Metric        | Value      |
|---------------|-----------|
| VmRSS (after full suite) | 53,888 kB |
| VmSize                    | ~4.0 GB   |
| VmPeak                    | ~4.0 GB   |

## Files Changed

| File | Change |
|------|--------|
| `internal/executor/expr.go` | Added `evalDatePart()` + `extractTimestampField()` helper; refactored `evalExtract()` to use shared helper |
| `internal/planner/planner.go` | Added `date_part` → `int8` type resolution |
| `internal/parser/select.go` | Added `parseSubstringFuncCall()` — both comma and FROM/FOR forms; wiring in `parseColumnOrCall()` |

## Next Steps

- Full SF=1 HammerDB data load with `shared_buffers=2000M` (background run).
- Production-scale query timing comparison (256MB vs 2000MB buffer pool).
