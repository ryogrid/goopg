# REF-011: Planner & Optimiser

## Overview

The planner converts an analysed AST into an executable plan tree. goopg's planner is optimiser-light: it applies heuristic transformations (join order, index selection) but does not perform cost-based optimisation with accurate cardinality estimates.

## goopg Implementation

**Package:** `internal/planner/`

### Key Types

- `Node` — interface for all plan nodes (SeqScan, IndexScan, Project, Filter, Join, Sort, Aggregate, …).
- `Plan(stmt, cat)` — the entry point. Dispatches on statement type:
  - `SelectStmt` → `planSelect` → builds the plan tree bottom-up.
  - `InsertStmt` → `planInsert` → SeqScan of target (for RETURNING) + insert node.
  - `DDL` statements → `&DDL{Stmt: stmt}` pass-through.
- `Expr` — planner-side expression node (planner.Expr). Converted from `parser.Expr` during planning.

### Planning a SELECT

```
planSelect:
  1. preplanWithClause — handle WITH/CTE (inline each CTE body)
  2. planScan — resolve FROM clause
       - plain table → SeqScan
       - with index equality predicate → IndexScan
       - subquery → recursive planSelect
       - CTE → CTEScan (label wrap over the CTE body)
  3. planFilter — WHERE clause
  4. planJoin — JOINs (nested loop or hash join)
  5. planAggregate — GROUP BY / aggregate functions
  6. planWindow — window functions
  7. planProject — target list
  8. planSetOp — UNION/INTERSECT/EXCEPT
  9. planSort — ORDER BY
  10. planLimit — LIMIT / OFFSET
```

### Index Selection

`planScanRangeVar` checks whether the WHERE clause contains an equality predicate on an indexed column. If so, it produces an `IndexScan` instead of a `SeqScan`. The index key expression is attached to the IndexScan node.

### Cardinality Estimation

`cardinality.go` provides a simple heuristic:
- SeqScan: `rows = table_stats.row_count` (or 10 000 if unknown).
- IndexScan: `rows = table_stats.row_count / distinct_values`.
- Join: `rows = left_rows * right_rows / max(distinct_values)`.
These estimates are used only for join ordering (hash vs nested-loop).

## PostgreSQL Implementation (Deep Dive)

### Path Generation Framework

PostgreSQL's planner is structured as a **path generator**:

1. **`query_planner`** — generates access paths for the base
   relations (sequential scan, index scan, bitmap scan, etc.).
2. **`add_paths_to_joinrel`** — generates join paths (nested
   loop, hash join, merge join) and considers multiple join
   orders.
3. **`create_plan`** — converts the cheapest path into a plan
   node.

Each path carries a **cost** estimate (startup + run). The cost
model uses configurable parameters:
- `seq_page_cost` (default 1.0)
- `random_page_cost` (default 4.0)
- `cpu_tuple_cost` (default 0.01)
- `cpu_index_tuple_cost` (default 0.005)
- `cpu_operator_cost` (default 0.0025)

goopg uses hard-coded cost estimates (1.0 for page access).

### GEQO (Genetic Query Optimiser)

For queries with many joins (≥ 12 tables by default), PostgreSQL
switches from exhaustive dynamic programming to a genetic
algorithm. GEQO considers a subset of join orderings and
iteratively improves them.

goopg always joins tables in the order they appear in the FROM
clause (fixed left-deep).

### Parallel Plan Creation

PostgreSQL generates parallel-aware plans when parallel workers
are available:
- **Parallel SeqScan** — divides the table into chunks, each
  worker scans a chunk.
- **Parallel IndexScan** — workers cooperate on an index scan.
- **Gather** / **Gather Merge** — gathers results from workers.

goopg does not generate parallel plans.

### Expression Optimisation

PostgreSQL's planner performs several expression transformations:
- **Constant folding** — `1 + 2` → `3`.
- **Predicate simplification** — `x = 1 AND x = 2` → false.
- **LIKE to indexable predicate** — `col LIKE 'foo%'` →
  `col >= 'foo' AND col < 'fop'`.
- **Subquery flattening** — simple subqueries in FROM/WHERE
  are inlined into the parent query.

goopg performs none of these optimisations.

### SubqueryScan

PostgreSQL wraps subqueries in a `SubqueryScan` node that
preserves the subquery's plan identity for EXPLAIN and
authority checks. goopg inlines subqueries directly into the
parent plan tree.

## goopg Improvement Analysis

### P1: Subquery Flattening

Add an analysis pass that identifies simple subqueries
(e.g., `WHERE x IN (SELECT y FROM t)`) and flattens them into
semi-joins or anti-joins.

**Impact:** Better join plans for subquery-heavy queries.

### P2: Constant Folding

Add a simple expression-folding pass: evaluate constant
expressions (`1 + 2`, `'foo' || 'bar'`) during planning.

**Impact:** Slightly faster execution for queries with constant
expressions. Trivial to implement.

### P2: LIKE-to-Range Translation

Translate `col LIKE 'prefix%'` into `col >= 'prefix' AND
col < 'prefix\x7F'` to enable index scans for prefix-match
patterns.

**Impact:** Enables index scans for LIKE queries on indexed
columns.

## References

- goopg: `internal/planner/planner.go`
- goopg: `internal/planner/cardinality.go`
- PG planner: `postgres/src/backend/optimizer/plan/planner.c`
- PG paths: `postgres/src/backend/optimizer/path/`
  (`add_paths_to_joinrel`, `costsize.c`)
- PG GEQO: `postgres/src/backend/optimizer/geqo/`
- PG parallel plan: `postgres/src/backend/optimizer/plan/planner.c`
  (`create_parallel_seqscan_path` and friends)
