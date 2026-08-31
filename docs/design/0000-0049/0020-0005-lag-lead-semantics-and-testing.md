# 0020-0005 — Window Stage B: LAG/LEAD Semantics and Testing

**Status:** accepted
**Date:** 2026-05-04
**Supersedes:** —

## Summary

Implements Stage B of Milestone 0020: the `lag()` and `lead()` window
functions with offset and default-value support. Extends the Stage-A
`windowOp` executor, planner `buildWindowFunc`, and analyzer
`analyzeWindowFuncCall` added in `0020-0002`/`0020-0003`.

## SQL Form

```sql
lag(value [, offset [, default]])  OVER (...)
lead(value [, offset [, default]]) OVER (...)
```

- `value` — expression evaluated against the target row.  Return type
  matches this expression's type.
- `offset` — integer expression; rows to look backward (lag) or forward
  (lead).  Default `1`.
- `default` — expression returned when the target row falls outside the
  current partition.  Default `NULL`.

Both functions are restricted to the same placement rules as Stage A
(SELECT target-list and ORDER BY only; not in WHERE/GROUP BY/HAVING).

## Changes

### `internal/planner/plan.go`

`WindowFunc` gains an `Args []Expr` field:

```go
type WindowFunc struct {
    pos  int
    Name string
    Type catalog.Type
    Args []Expr // lag/lead: [value, offset?, default?]
}
```

`row_number` and `rank` continue to use zero-length `Args`.

### `internal/planner/planner.go`

**`inferExprType(e Expr) catalog.Type`** — new helper that returns the
catalog type of a resolved planner Expr by type-switching on the common
concrete types (`*ColumnRef`, `*OuterColumnRef`, `*IntegerConst`,
`*NumericConst`, `*StringConst`, `*BooleanConst`).  Falls back to
`text` for composite or unknown shapes.

**`buildWindowFunc`** — signature extended to accept
`(fc, inputCtx, agg)` so argument expressions can be resolved via the
existing `resolveExprForWindowInput` helper.  New `lag`/`lead` branch
resolves up to 3 args and derives return type from `inferExprType(args[0])`.

### `internal/analyzer/analyzer.go`

`analyzeWindowFuncCall` restructured:
- `row_number`/`rank` — unchanged validation, return type `int8`.
- `lag`/`lead` — no `*`/DISTINCT allowed; 1–3 args required;
  `analyzeExpr` called for each arg; return type taken from the first
  arg's analyzed type.
- OVER-clause validation (partition/order expression analysis) shared
  by all branches.

### `internal/executor/operators_window.go`

`evalWindowFuncs` refactored from a single sequential pass to a
**two-phase partition-aware loop**:

1. **Partition discovery** — one forward pass computes `pStarts[]`, the
   start index of each partition group in the already-sorted `o.rows`.
2. **Per-partition evaluation** — for each partition `[pStart, pEnd)`,
   iterates rows with a `localIdx` (0-based within the partition).

   - `row_number` / `rank` — identical semantics to Stage A (rank uses
     `samePeer` for gap detection).
   - `lag` — `targetLocal = localIdx - offset`; if out-of-partition,
     return `fn.Args[2]` or `NullDatum`; otherwise evaluate
     `fn.Args[0]` against `o.rows[pStart+targetLocal]`.
   - `lead` — symmetric with `targetLocal = localIdx + offset`.

The `o.rows` slice is modified in place (window function output slots
were pre-allocated as `NullDatum` during `Open()`).

## Semantics

| Situation | Result |
|---|---|
| `lag(col)` at first row of partition | NULL |
| `lag(col, n)` where n > localIdx | NULL (or explicit default) |
| `lead(col)` at last row of partition | NULL |
| `lead(col, n)` where localIdx + n ≥ partition size | NULL (or explicit default) |
| Partition boundary | lag/lead never crosses partition boundary |

The offset expression is evaluated against the **current row** (not the
target row), matching PostgreSQL semantics.

## Tests Added (`internal/executor/operators_window_test.go`)

| Test | Covers |
|---|---|
| `TestWindowOpLagBasic` | 3-row single-partition, offset=1 (default), first row = NULL |
| `TestWindowOpLeadBasic` | 3-row single-partition, offset=1 (default), last row = NULL |
| `TestWindowOpLagWithExplicitOffset` | 4-row, offset=2; first two rows = NULL |
| `TestWindowOpLagWithDefault` | 2-row, offset=1, default=-1 replaces NULL |
| `TestWindowOpLeadWithDefault` | 2-row, offset=1, default=99 for last row |
| `TestWindowOpLagLeadWithPartitions` | PARTITION BY grp; lag/lead cannot cross boundary |

All six new tests pass alongside the four Stage-A tests.

## Out of Scope (Deferred)

- Frame clauses (`ROWS BETWEEN … AND …`, `RANGE BETWEEN … AND …`,
  `GROUPS BETWEEN … AND …`) — parser accepts them but emits `0A000`.
- Named window definitions (`WINDOW w AS (…)`).
- `IGNORE NULLS` / `RESPECT NULLS` variants.
- Multiple `OVER (…)` specs in one SELECT (already blocked by Stage A
  planner check).
