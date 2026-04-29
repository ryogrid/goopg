# 0020-0002 - Window Function Analyzer and Planner (Stage A)

**Status:** accepted (analyzer + planner wiring for Stage A)
**Milestone:** [0020 - Window Function Support](../milestones/0020-window-functions-over-row-number-rank-lag-lead.md)
**Spans seam:** analyzer, planner
**Cross-links:**
[root-0010](root-0010-parser.md),
[root-0011](root-0011-planner.md),
[0020-0001](0020-0001-window-parser-and-ast.md).

## Scope

This slice promotes window functions from parser-only acceptance to
planner-visible semantics for Stage A.

Supported in this step:

- `row_number() OVER (...)`
- `rank() OVER (...)`
- `PARTITION BY` and `ORDER BY` inside `OVER (...)`
- Window references in the SELECT target list and ORDER BY

Explicitly deferred:

- `lag`/`lead`
- Frame clauses (`ROWS`, `RANGE`, `GROUPS`)
- Named windows (`OVER win_name`, `WINDOW win AS (...)`)
- Multiple distinct window specifications in one SELECT

## Analyzer rules

`analyzeExpr` now validates window-function calls when `FuncCall.Over != nil`
instead of rejecting all windows.

- Allowed function names: `row_number`, `rank`
- Argument shape:
  - `row_number` requires zero arguments, no `*`, no `DISTINCT`
  - `rank` requires zero arguments, no `*`, no `DISTINCT`
- Return type: `int8`
- Partition/order expressions are analyzed recursively so column/type errors
  surface at analyze time.

Clause placement guardrails (in `analyzeSelectWithParent`):

- Window functions in `WHERE`, `GROUP BY`, or `HAVING` are rejected with `0A000`
- Target list and ORDER BY remain allowed

This mirrors upstream placement rules in a scoped way while keeping
existing v0 diagnostics deterministic.

## Planner model

Planner introduces an explicit `WindowAgg` stage between aggregate/having and
final ORDER BY/LIMIT/Project.

Pipeline shape becomes:

1. FROM/WHERE
2. Aggregate/HAVING (if present)
3. WindowAgg (if needed)
4. ORDER BY
5. LIMIT/OFFSET
6. Project

`WindowAgg` carries:

- Resolved `PartitionBy []Expr`
- Resolved `OrderBy []SortKey`
- Resolved function list (`row_number` / `rank`)

The planner rewrites window function expressions in target/ORDER BY trees to
`ColumnRef`s that point at appended `WindowAgg` output columns.

## Stage-A constraints

To keep execution deterministic and small for the first pass:

- All window calls in one SELECT must share the same `OVER (...)` definition
  (`PARTITION BY` and `ORDER BY` lists must match byte-for-byte)
- Violations raise `0A000`

This keeps Stage A aligned with milestone intent while reducing execution
complexity.

## Test plan

- Analyzer tests:
  - accepts `row_number` and `rank`
  - rejects unsupported window functions/argument shapes
  - rejects window usage in WHERE/GROUP BY/HAVING
- Planner tests:
  - emits `WindowAgg` in the expected pipeline position
  - rewrites target expressions to `ColumnRef`s on top of `WindowAgg`
  - rejects mixed window specs in one SELECT

Executor semantics and EXPLAIN rendering are covered in
`0020-0003` and `0020-0004`.
