# 0118-0034 — SELECT-list set-returning function nested in an expression

Status: accepted
Milestone: M0118-0008 (enabler) / general planner correctness
Date: 2026-06-22

## Summary

Fixes a silent row-dropping correctness bug: a set-returning function (SRF)
nested inside a larger SELECT-list scalar expression — e.g.
`generate_series(1,1000) % 4` — collapsed to a **single** row holding the SRF's
start value instead of expanding to one row per series element. Concretely,

```sql
CREATE TABLE b (a_id int);
INSERT INTO b SELECT generate_series(1,1000) % 4;   -- inserted 1 row, not 1000
SELECT * FROM b WHERE a_id = 3;                      -- (0 rows)
```

PostgreSQL evaluates the SRF first (1000 rows) and applies `% 4` per row. goopg
only expanded an SRF that was the **bare** target expression (optionally wrapped
in a single cast); an SRF buried in any other expression fell through to the
scalar `generate_series` fallback, which returns just `start`.

This is the root blocker for the `alter-table-1` / `alter-table-2` isolation
specs (their setup is `INSERT INTO b SELECT generate_series(1,1000) % 4`), and a
general correctness bug affecting any `SELECT <expr-over-srf()>`.

## Root cause

`buildSelectSrfProjectSet` (`internal/planner/planner.go`) detected SELECT-list
SRFs only when the target expression *was* a recognized SRF `FuncCall` (after
unwrapping at most one `CastExpr`). For `generate_series(1,1000) % 4` the target
is a `BinaryOp` whose left operand is the SRF, so detection skipped it; with no
SRF detected, no `ProjectSet` was planned and the SRF resolved to its scalar
form (`internal/planner/planner.go` scalar fallback → returns `start`).

## Fix

Detect an SRF **nested** inside a target expression and rewrite it into a
*wrapper* that applies the enclosing scalar expression to each expanded element.

### Planner (`internal/planner/planner.go`)

- New helpers:
  - `isNestedSRFName` — currently `generate_series` only (bare and FROM-clause
    SRFs keep their existing, well-tested paths; bounded blast radius).
  - `findFirstNestedSRF(Expr) *FuncCall` — DFS for the first SRF call in a
    resolved expression tree.
  - `replaceExprNode(e, target, repl) Expr` — rebuilds the tree with the
    pointer-matched node replaced. Both walk the same node kinds (`FuncCall`,
    `BinaryOp`, `UnaryOp`, `CastExpr`, `CollateExpr`, `CaseExpr`) and mirror
    `shiftColumnRefsBy`'s rebuild discipline.
- `buildSelectSrfProjectSet`: a new pre-scan resolves each non-bare-SRF target,
  locates a nested generate_series, assigns it a **temp eval-row slot**
  (`ChildWidth + k`, beyond the visible output columns), and records a wrapper =
  the resolved target with the SRF `FuncCall` replaced by a
  `ColumnRef{Index: slot}`. A nested-SRF-only query now still builds a
  `ProjectSet` (the early-return condition includes wrapped SRFs).

### Plan node (`internal/planner/plan.go`)

- `SrfCol`/`UnnestCol` gain `Wrapped bool` — when set, the executor writes the
  raw per-step SRF value into the eval row (temp slot at `ColIdx`), not the
  output row.
- `ProjectSet` gains `Wrappers []SrfWrapper{OutCol, Expr}`, `ChildWidth`, and
  `EvalRowWidth` (`ChildWidth + #wrapped`).

### Executor (`internal/executor/operators_project_set.go`)

In the zip loop, when wrappers exist, build a per-step `evalRow` of width
`EvalRowWidth`: the child row occupies `[0:ChildWidth)`, each wrapped SRF's
per-step value (or NULL when zip-padding a shorter series) goes into its temp
slot. After the bare SRF / unnest / user-SRF columns are placed, each wrapper is
evaluated against `evalRow` and written to `outRow[OutCol]`. Non-wrapped paths
are byte-for-byte unchanged (no `evalRow` is allocated when `Wrappers` is empty).

## Correctness of the eval-row indexing

A wrapper's non-SRF `ColumnRef`s keep their child-relative indices (`< ChildWidth`)
and resolve correctly because `evalRow`'s prefix is the child row; the
substituted SRF `ColumnRef` resolves to the appended temp slot. The plan-time
`ChildWidth` (= `len(child.Output())`) is the single source of truth shared by
slot assignment and eval-row construction.

## Scope / non-goals

- Only `generate_series` is expanded when nested; nested `unnest` / user-SETOF
  functions and multiple SRFs in one target expression keep current behavior
  (the pre-scan takes the first SRF and only when arity is valid). The `Wrapped`
  flag on `UnnestCol` is reserved for a future extension.
- WAL / storage / visibility untouched.

## Tests / gates

- `TestSelectListSRFInsideExpression` (`internal/executor/srf_in_expr_test.go`):
  `generate_series(1,10) % 4`, `+ 100`, three-arg `(2,10,2) * 10`, and a bare
  `generate_series(1,4)` regression.
- `go test ./internal/planner/... ./internal/executor/...` PASS.
- Regress-port parity suite (`TestPort_RegressSuite`) PASS.
- pgbench TPC-B smoke (pre-commit hook) 0-failed.

## Follow-up

`alter-table-1` / `alter-table-2` are still deferred: they additionally need FK
`... NOT VALID` parsing, `ALTER TABLE ... VALIDATE CONSTRAINT`, and the
ShareUpdateExclusive/ShareRowExclusive lock semantics. This change removes their
data-setup blocker.
