# 0118-0051 — PL/pgSQL scalar-subquery expression (`x := (SELECT …)`)

**Status:** accepted
**Milestone:** M0118-0008 (Upstream Isolation Spec Suite Pass-Through — DDL/VACUUM/maintenance tail)
**Kind:** enabler in the `plpgsql-toast` chain — **NOT a spec promotion**
**Date:** 2026-06-23

## Summary

Implements a **top-level scalar subquery** in a PL/pgSQL expression: an assignment
RHS or `RETURN` operand of the form `(SELECT …)` is now planned and executed
against the live catalog, yielding the first column of the first row (NULL when
the query produces no rows; SQLSTATE `21000` when it produces more than one row),
matching PostgreSQL's scalar-subquery semantics.

Previously `evalPLpgSQLExpr` rejected every `*parser.SubqueryExpr` outright with
`0A000 subqueries are not supported in PL/pgSQL expressions in v0`. That blocked
`plpgsql-toast` step `assign2`:

```sql
do $$
  declare x text;
  begin
    x := (select test1.b from test1);   -- ← rejected before this change
    delete from test1;
    commit;
    ...
```

## Why this slice

`plpgsql-toast` is the hard tail of M0118-0008; it is being advanced one PL/pgSQL
gap per loop (the divergence walks `assign1 → assign2 → assign3 → …`). The prior
two enablers landed PL/pgSQL transaction control (`COMMIT`/`ROLLBACK` in a DO
block — design `0118-0049`) and `SELECT … INTO` (design `0118-0050`), which moved
the first divergence to `assign2`. This loop lifts the `assign2` blocker.

## Design

`evalPLpgSQLExpr(e parser.Expr, frame, ctx)` is the single entry point that
lowers a PL/pgSQL expression to a `planner.Expr` and evaluates it. A subquery
cannot be lowered to a `planner.Expr` — it must be *planned and executed* as a
SELECT against the live catalog (which requires `ctx`). So the subquery is
intercepted at `evalPLpgSQLExpr` **before** lowering, leaving `lowerPLpgSQLExpr`
pure (it still returns `0A000` for a subquery nested deeper inside a larger
expression, which no `port` spec exercises):

```go
func evalPLpgSQLExpr(e parser.Expr, frame *plpgsqlFrame, ctx *Context) (Datum, error) {
    if sq, ok := e.(*parser.SubqueryExpr); ok {
        return evalScalarSubquery(sq, ctx)
    }
    ...
}
```

`evalScalarSubquery` mirrors the existing `SelectIntoStmt`/`ForSelectStmt`
machinery (design `0118-0050`):

1. `planner.Plan(sq.Inner, ctxPlanCatalog(ctx))` — `sq.Inner` is the inner
   `*parser.SelectStmt`, planned directly (no re-parse from text needed).
2. `Build` + `Open(ctx)`.
3. First `op.Next()`: no row ⇒ NULL Datum (PostgreSQL's scalar-subquery
   no-row result); otherwise capture `row[0]` (a struct copy, matching the
   persistence convention the SELECT INTO STRICT path already relies on).
4. A second `op.Next()` returning a row ⇒ `21000 more than one row returned by
   a subquery used as an expression`, matching `ExecScanSubPlan`/`ExecSubPlan`'s
   cardinality check.

This works in **every** scalar PL/pgSQL context that funnels through
`evalPLpgSQLExpr` — assignment RHS, `RETURN`, `IF`, `RAISE … USING`, etc. — so
`x := (SELECT …)` and `RETURN (SELECT …)` are both covered by the single
interception point.

## Oracle

PostgreSQL evaluates a PL/pgSQL expression as a one-row SELECT
(`exec_eval_expr` → `exec_run_select`), and a parenthesised `(SELECT …)` inside
it is an ordinary SQL sub-SELECT: scalar context, first column of the (at most
one) row, NULL when empty, `ERRCODE_CARDINALITY_VIOLATION` (21000) when >1 row
(`src/backend/executor/nodeSubplan.c`). This change reproduces that contract for
a top-level subquery.

## Scope / blast radius

- New behaviour fires **only** when the PL/pgSQL expression *is itself* a
  `*parser.SubqueryExpr`. Every other expression takes the unchanged
  lower-then-eval path. A subquery embedded inside a larger PL/pgSQL expression
  (e.g. `x := (select …) + 1`) still returns the existing `0A000` — out of scope
  for any `port` spec and deferred.
- No parser, planner, or storage change; purely an executor-side dispatch.

## Effect on `plpgsql-toast`

Probe (`IsolationRunner.RunAndCompare` on `plpgsql-toast.spec`) confirms the
first divergence advances **past** `assign2`: `assign1` and `assign2` now match
PG 18.3 byte-for-byte (both emit `NOTICE: length(x) = 6000`). The new first
divergence is `assign3` (`length(r) = 6004` vs `<NULL>`) — *expanded-record field
assignment* (`r record; r.b := (select …)`), which needs record reassembly to
text. Remaining `plpgsql-toast` blockers (all Effort-L, deferred):

- `assign3`/`assign4`: expanded record (`r record`, `r test2`) with
  `r.b := (select …)` and `length(r::text)` — record-to-text reassembly.
- `assign5`/`assign6`: `FOR r IN SELECT … LOOP` detoast + COMMIT-in-loop
  (`length(r::text)` shows `6000` vs `6002` — the record-text framing bytes).
- detoast-across-COMMIT: free external TOAST pointers at the assignment boundary
  so a concurrent VACUUM cannot orphan chunks, plus the runner's
  advisory-lock/VACUUM `<waiting …>` timing marker.

## Tests / gates

- New `internal/executor/plpgsql_scalar_subquery_test.go`
  (`TestPlpgSQLScalarSubquery`): scalar-assignment, no-row→NULL, `RETURN
  (SELECT …)`, and `>1 row ⇒ 21000` cases.
- `TestPlpgSQLSelectInto` (sibling SELECT-INTO path) still PASS.
- `internal/executor` package PASS; `go build ./internal/executor` clean.
- Probe (throwaway) confirmed the `plpgsql-toast` divergence advances `assign2 →
  assign3`.
- pgbench smoke = pre-commit hook (no executor hot-path change).
