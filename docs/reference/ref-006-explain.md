# REF-006: EXPLAIN / ANALYZE

## Overview

`EXPLAIN` displays the execution plan for a statement. `EXPLAIN ANALYZE` executes the statement and shows actual timings and row counts alongside the plan. goopg supports both with JSON and text output formats.

## goopg Implementation

**Package:** `internal/executor/operators_explain.go`

### Key Types

- `Explain` plan node — wraps the inner plan and holds options (ANALYZE, FORMAT, TIMING, SUMMARY).
- `explainOp` — the executor operator. Renders the plan tree as tabular text or JSON.

### EXPLAIN (non-ANALYZE)

`explainOp.Next()` walks the plan tree, calling `describePlan` for each node:

```
describePlan(node)
  ├─ SeqScan: "Seq Scan on table"
  ├─ IndexScan: "Index Scan using idx on table"
  ├─ Project: "Project"
  ├─ Filter: "Filter: predicate"
  ├─ Join: "Nested Loop / Hash Join"
  ├─ Aggregate: "Aggregate (group-by keys)"
  ├─ Sort: "Sort (sort-key)"
  ├─ Limit: "Limit"
  ├─ CTEScan: "CTE Scan on cte-name"
  ├─ RecursiveUnion: "Recursive Union"
  ├─ WindowAgg: "Window Aggregate"
  └─ LockRows: "LockRows (for update/share)"
```

Each node also shows estimated rows and estimated cost (hard-coded estimates, not from statistics).

### EXPLAIN ANALYZE

When ANALYZE is specified, the executor wraps each operator in an `instrumentOp` that records:
- Actual rows returned.
- Actual loops (number of times Next() was called).
- Timing (startup time and total time).

After execution, the plan is rendered with actual values alongside estimates.

### JSON Format

EXPLAIN (FORMAT JSON) produces:
```json
[{
  "Plan": {
    "Node Type": "Project",
    "Actual Rows": 3,
    "Actual Loops": 1,
    "Plans": [
      { "Node Type": "Values", "Actual Rows": 3 }
    ]
  }
}]
```

## PostgreSQL Implementation

PostgreSQL's EXPLAIN (`explain.c`) is more detailed:

- **Buffers** — shows shared/hit/read/dirtied/written buffer counts
  (`EXPLAIN (BUFFERS)`).
- **Settings** — shows GUC settings that differ from defaults
  (`EXPLAIN (SETTINGS)`).
- **WAL** — shows WAL record counts and sizes (`EXPLAIN (WAL)`).
- **Summary** — shows planning time and execution time breakdown.
- **Timing** — can be disabled to reduce overhead
  (`EXPLAIN (TIMING OFF)`).
- **Auto-explain** — automatic EXPLAIN logging for slow queries
  (`auto_explain` module). goopg does not have this.

### Key Differences

| Aspect | goopg | PostgreSQL |
|--------|-------|------------|
| Buffer counters | Not shown | EXPLAIN (BUFFERS) |
| Settings | Not shown | EXPLAIN (SETTINGS) |
| WAL stats | Not shown | EXPLAIN (WAL) |
| Planning time | Not shown | Shown with SUMMARY |
| Auto-explain | Not implemented | auto_explain module |
| Cost estimates | Hard-coded | Based on statistics |

## References

- goopg: `internal/executor/operators_explain.go`
- PG explain: `postgres/src/backend/commands/explain.c`
