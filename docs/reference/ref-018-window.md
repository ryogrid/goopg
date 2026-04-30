# REF-018: Window Functions

## Overview

Window functions compute aggregate-like values over a set of rows related to the current row, without collapsing the result set. goopg supports `ROW_NUMBER()` and `RANK()` with `OVER (PARTITION BY … ORDER BY …)`.

## goopg Implementation

**Packages:** `internal/executor/operators_window.go`, `internal/planner/`

### Plan Node

`planner.WindowAgg` wraps a child plan and adds window function evaluation:

```go
type WindowAgg struct {
    pos       int
    Child     Node
    Functions []WindowFunc
    Partition []Expr   // PARTITION BY expressions
    OrderBy   []SortBy // ORDER BY expressions
}
```

### Executor

`windowAggOp` implements window function evaluation:

1. **Partition** — the child plan must produce rows in partition +
   order by order (the planner adds a Sort node when needed).
2. **Frame** — for each partition, iterate rows and compute
   window functions:
   - `ROW_NUMBER()` — sequential row number within the partition.
   - `RANK()` — like ROW_NUMBER but equal ORDER BY values get
     the same rank, leaving gaps.

### Window Function Types

| Function | Implementation |
|----------|---------------|
| `ROW_NUMBER()` | Incrementing counter per partition, reset at partition boundary. |
| `RANK()` | Counter that advances only when ORDER BY value changes. |

### Frame Clause

`ROWS / RANGE / GROUPS` frame clauses are parsed but rejected with
a 0A000 error. Only the default frame (`RANGE BETWEEN UNBOUNDED
PRECEDING AND CURRENT ROW`) is supported.

## PostgreSQL Implementation

PostgreSQL's window function execution (`nodeWindowAgg.c`) is
significantly more capable:

- **Supported functions** — ROW_NUMBER, RANK, DENSE_RANK,
  PERCENT_RANK, NTILE, LEAD, LAG, FIRST_VALUE, LAST_VALUE,
  NTH_VALUE, CUME_DIST, and aggregate functions with OVER.
- **Frame clauses** — ROWS, RANGE, GROUPS with various boundary
  types (UNBOUNDED PRECEDING, value PRECEDING, CURRENT ROW,
  value FOLLOWING, UNBOUNDED FOLLOWING).
- **EXCLUDE** — PostgreSQL 12+ supports `EXCLUDE CURRENT ROW`,
  `EXCLUDE GROUP`, `EXCLUDE TIES` frame exclusion.
- **WINDOW clause** — named window definitions reusable across
  multiple function calls.
- **Aggregate window functions** — any aggregate (SUM, AVG, etc.)
  can be used with OVER.

### Key Differences

| Aspect | goopg | PostgreSQL |
|--------|-------|------------|
| Functions | ROW_NUMBER, RANK | 10+ built-in + aggregates |
| Frame clause | Default only | ROWS, RANGE, GROUPS |
| WINDOW clause | Not supported | Named window definitions |
| LEAD / LAG | Not implemented | Supported |
| Aggregate functions with OVER | Not supported | Supported |

## References

- goopg: `internal/executor/operators_window.go`
- PG window: `postgres/src/backend/executor/nodeWindowAgg.c`
- PG window functions: `postgres/src/backend/utils/adt/windowfuncs.c`
