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

## PostgreSQL Implementation (Deep Dive)

### EXPLAIN (BUFFERS)

`EXPLAIN (BUFFERS)` adds buffer usage counters to each plan node:

```
Seq Scan on t  (cost=… rows=… width=…)
  Buffers: shared hit=382 read=3 dirtied=1 written=1
```

- `hit` — pages found in the buffer pool.
- `read` — pages read from disk.
- `dirtied` — pages dirtied by this node.
- `written` — pages written by background writer.

goopg does not track buffer usage per node.

### EXPLAIN (WAL)

`EXPLAIN (WAL)` adds WAL record counters:
```
Insert on t
  WAL: records=1 bytes=68
```

goopg does not expose WAL stats per query.

### Auto-explain

PostgreSQL's `auto_explain` module logs EXPLAIN output for
queries that exceed a configurable duration threshold:

```ini
shared_preload_libraries = 'auto_explain'
auto_explain.log_min_duration = '1s'
auto_explain.log_analyze = true
```

goopg does not have an auto-explain feature.

### Planning Time

PostgreSQL's `EXPLAIN ANALYZE` shows:
```
Planning Time: 0.123 ms
Execution Time: 45.678 ms
```

goopg's EXPLAIN ANALYZE shows only execution time.

### Node Timing Propagation

In PostgreSQL's EXPLAIN ANALYZE, timing for each node includes
the time spent in its children. The root node shows the total
execution time. Each child node shows its own time + its
children's times.

goopg's instrumentOp records timing per node independently.

## goopg Improvement Analysis

### P2: Buffer Counters

Add per-node buffer hit/read/dirty/written counters. Track them
in the slot's `BufferDesc` and aggregate in `instrumentOp`.

**Impact:** Better observability for query performance debugging.

### P2: Planning Time

Add a `planningTime` field to the explain output. Measure the
time spent in `planner.Plan()`.

## References

- goopg: `internal/executor/operators_explain.go`
- PG explain: `postgres/src/backend/commands/explain.c`
- PG auto_explain: `postgres/contrib/auto_explain/auto_explain.c`
