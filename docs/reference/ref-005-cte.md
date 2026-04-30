# REF-005: CTE / WITH Clause

## Overview

Common Table Expressions (CTEs) — the `WITH` clause — allow queries to define named subqueries that can be referenced multiple times in the main query body. goopg supports non-recursive CTEs (inlined per reference) and recursive CTEs (`WITH RECURSIVE`) via fixpoint iteration.

## goopg Implementation

**Packages:** `internal/planner/with.go`, `internal/parser/`, `internal/analyzer/`, `internal/executor/`

### Non-Recursive CTEs

Non-recursive CTEs use inline-substitution: each reference to a CTE name in the FROM clause clones the planned body tree. Multiple consumers each get their own copy.

```
planSelect → preplanWithClause(s.With, cat)
  ├─ plan each CTE body (left-to-right; earlier CTEs visible to later ones)
  ├─ store planned body in planCTEs map
  └─ planScanRangeVar → lookupPlannedCTE(name) → CTEScan(plannedBody)
```

### Recursive CTEs

`WITH RECURSIVE r AS (anchor UNION ALL recursive_member)` is handled as:

1. **Analyzer** (`analyzeRecursiveCTE`): analyses the anchor (left side of UNION ALL) to determine output columns, then registers the CTE in the scope so the recursive member's self-reference resolves.
2. **Planner** (`planRecursiveCTE`):
   - Saves and clears the UNION ALL's `SetOp` to plan the anchor.
   - Registers a `WorkTableScan` placeholder for the CTE name.
   - Plans the recursive member (CTE references → WorkTableScan).
   - Returns a `RecursiveUnion{Anchor, Recursive}` node.
3. **Executor** (`recursiveUnionOp`):
   - Drains the anchor → working table.
   - Iterates fixpoint: for each row in the working table, executes the recursive member with `ctx.WorkTableRows` set, collects new rows, replaces the working table, repeats until empty.

### CTE Column Aliases

Optional `(col, …)` aliases rename the CTE's output columns. An arity mismatch between aliases and the body's target list produces a planner error (42P10).

## PostgreSQL Implementation

PostgreSQL's CTE implementation differs in several important ways:

- **Materialisation** — PostgreSQL treats CTEs as optimisation
  fences by default: the CTE body is materialised once and its
  result is shared among all consumers. goopg inlines the body
  (different semantics — side effects may execute multiple times).
  PostgreSQL can be forced to inline via `WITH … AS NOT MATERIALIZED`.
- **Recursive CTE** — PostgreSQL's recursive CTE supports
  `UNION ALL` and `UNION DISTINCT` forms. goopg only supports
  `UNION ALL`. PostgreSQL also supports `SEARCH` and `CYCLE`
  clauses for graph traversal.
- **CTE in subqueries** — PostgreSQL allows CTEs in subqueries
  and nested WITH clauses. goopg supports nested WITH.

### Key Differences

| Aspect | goopg | PostgreSQL |
|--------|-------|------------|
| Materialisation | Inline per consumer | Materialise once by default |
| Recursive UNION [DISTINCT] | UNION ALL only | UNION ALL + UNION DISTINCT |
| SEARCH / CYCLE clauses | Not implemented | Supported for recursive CTEs |
| CTE in DML | Not tested | Supported (INSERT/UPDATE/DELETE with WITH) |

## References

- goopg: `internal/planner/with.go`
- goopg: `internal/executor/operators_recursive_cte.go`
- PG CTE: `postgres/src/backend/parser/parse_cte.c`
- PG recursive union: `postgres/src/backend/executor/nodeRecursiveunion.c`
