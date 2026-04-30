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

## PostgreSQL Implementation

PostgreSQL's planner (`planner.c`) is a full cost-based optimiser:

- **Path generation** — PostgreSQL generates multiple access paths
  (sequential scan, index scan, bitmap scan, etc.) and estimates
  the cost of each. The cheapest path is chosen.
- **Cost parameters** — `seq_page_cost`, `random_page_cost`,
  `cpu_tuple_cost`, `cpu_index_tuple_cost`, etc. are configurable
  GUCs. goopg hard-codes these.
- **Statistics** — `ANALYZE` builds MCV lists, histograms, and
  `ndistinct` estimates. goopg collects basic row-count and
  null-fraction statistics but does not build histograms.
- **Join order** — PostgreSQL considers multiple join orders
  (left-deep, right-deep, bushy) via dynamic programming.
  goopg uses a fixed left-deep join order.
- **Parallel plans** — PostgreSQL can generate parallel scan,
  join, and aggregate plans. goopg is single-goroutine per query.
- **Expression optimisation** — constant folding, predicate
  simplification, etc. goopg does minimal expression optimisation.

### Key Differences

| Aspect | goopg | PostgreSQL |
|--------|-------|------------|
| Cost model | Heuristic (hard-coded) | Full cost-based with configurable parameters |
| Statistics | Row count, null fraction | MCV lists, histograms, ndistinct, correlation |
| Join order | Fixed left-deep | Dynamic programming with GEQO for many tables |
| Access paths | SeqScan or IndexScan | SeqScan, IndexScan, BitmapScan, TidScan, etc. |
| Parallelism | None | Parallel scan/join/aggregate |
| Expression optimisation | None | Constant folding, predicate simplification |

## Potential Optimisations or Corrections

- **MCV-based cardinality estimation** would improve join order
  for queries involving skewed data distributions.
- **Bitmap scans** would benefit queries with `WHERE col IN (…)`
  or `WHERE col > x AND col < y` on indexed columns.
- **Expression optimisation** (constant folding, predicate
  push-down) would reduce plan execution time for queries with
  complex WHERE clauses.

## References

- goopg: `internal/planner/planner.go`
- goopg: `internal/planner/cardinality.go`
- PG planner: `postgres/src/backend/optimizer/plan/planner.c`
- PG paths: `postgres/src/backend/optimizer/path/`
- PG statistics: `postgres/src/backend/commands/analyze.c`
