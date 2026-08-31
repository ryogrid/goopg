# 0100-0005h — `EXPLAIN EXECUTE <name>` renders the prepared plan

**Status:** accepted
**Milestone:** M0100-0005 (drop-index-concurrently-1 EXPLAIN parity)
**Date:** 2026-05-15

## Problem

`EXPLAIN (COSTS OFF) EXECUTE getrow_idxscan;` produced

    QUERY PLAN
    ---------------------------
    Utility *parser.ExecuteStmt
    (1 row)

instead of expanding the prepared statement and rendering its actual plan
tree.  Every isolation spec that uses `EXPLAIN … EXECUTE` (currently
`drop-index-concurrently-1`, with more in the deferred queue) diverges from
upstream on the first EXPLAIN line and the diff drags through the rest of
the run.

## Root cause

`planner.Plan` handles `*parser.PrepareStmt`, `*parser.ExecuteStmt`,
`*parser.DeallocateStmt` by wrapping them in a `Utility` placeholder
(`internal/planner/planner.go:125`).  That node carries the unevaluated
`parser.Stmt` and `describePlan` formats it as

    fmt.Sprintf("Utility %T", p.Stmt)
    // → "Utility *parser.ExecuteStmt"

at `internal/executor/operators_explain.go:354`.  The EXPLAIN walker has no
hook to expand the prepared statement: by the time it reaches the
`Utility` node the per-connection prepared-statement registry is out of
reach (it is a per-`net.Conn` map maintained in `internal/server`).

PG resolves this in `ExplainOneUtility` (`postgres/src/backend/commands/
explain.c`) by detecting an `ExecuteStmt` and recursing through
`ExplainExecuteQuery`, which fetches the cached plan from the prepared
statement and explains *that*.

## Fix

Substitute the `ExecuteStmt` for the prepared statement's parsed `Query`
at the dispatch layer — the layer that already owns the prepared-statement
registry (`preparedStatements`) — before the planner runs.

`internal/server/dispatch.go` (top of the per-statement loop in
`dispatchSimpleQueryViaExecutor`):

    if es, ok := stmt.(*parser.ExplainStmt); ok {
        if ex, exok := es.Inner.(*parser.ExecuteStmt); exok {
            // … prepStmts.Lookup(ex.Name) → re-parse → ps.Query → es.Inner
            es.Inner = ps.Query
            rewroteExplainExecute = true
        }
    }

After the in-place rewrite the loop falls through to `executeOneSimpleStmt`
which calls `planner.Plan(stmt, …)`.  Plan now sees a regular
`*parser.ExplainStmt{Inner: <SelectStmt|InsertStmt|…>}` and produces
`Explain{Child: <normal plan tree>}` exactly as it would for a literal
`EXPLAIN SELECT …`.

The substitution registers `rewroteExplainExecute = true` so the
plan-cache fast path is skipped for this statement.  The cache key is the
literal SQL (`EXPLAIN (COSTS OFF) EXECUTE getrow_idxscan`) and a later
`DEALLOCATE` + re-`PREPARE` of the same name would otherwise serve a stale
plan; `DEALLOCATE` does not invalidate the plan cache today (only DDL
does, per `executeOneSimpleStmt`).  Skipping cache for this path is cheap:
re-planning a small SELECT is a sub-millisecond cost and the path is
diagnostic-only.

Mis-uses surface as standard errors:

| Condition                              | SQLSTATE | Message                                              |
|----------------------------------------|----------|------------------------------------------------------|
| No prepared statements registry        | 26000    | `prepared statement "X" does not exist`              |
| Name not in registry                   | 26000    | `prepared statement "X" does not exist`              |
| Stored SQL fails to re-parse           | 42601    | `could not parse prepared statement for EXPLAIN`     |
| Stored entry is not a `PrepareStmt`    | XX000    | `prepared statement "X" has no body`                 |

## Why not at the planner

The planner does not (and should not) hold per-connection state.  Threading
a prepared-statement lookup callback through `planner.Plan` would either
expose connection-scoped state across packages or require pre-resolving
prepared statements before planning.  Doing the substitution at the
dispatch level keeps the boundary clean: the planner continues to see only
parser ASTs, and the executor continues to see only fully-planned trees.

## Why not store the parsed `Query` at PREPARE time

The current registry stores raw SQL (`preparedStatements.stmts
map[string]string`) so that a later `EXECUTE` can re-dispatch the original
text verbatim.  Extending that struct to also hold a parsed `Stmt` is the
right long-term move (it would also fix the parallel issue where
`EXECUTE` re-runs the *PREPARE* statement instead of the inner Query —
see follow-up below), but it changes a registry interface used by every
PREPARE/EXECUTE/DEALLOCATE call site.  This change is intentionally
scoped to the EXPLAIN path; the re-parse on the EXPLAIN branch is a
no-allocation cost for the lifecycle.

## Regression pins

- `internal/server/explain_execute_test.go::TestExplainExecuteRendersPreparedPlan`
  asserts a `PREPARE get_items AS SELECT id, label FROM items` followed
  by `EXPLAIN (COSTS OFF) EXECUTE get_items` produces a DataRow that
  contains the literal `Seq Scan on items` and **never** the substrings
  `Utility` or `ExecuteStmt`.
- `internal/server/explain_execute_test.go::TestExplainExecuteUnknownPreparedReports26000`
  asserts the un-prepared name path emits `ErrorResponse` carrying
  SQLSTATE 26000 and naming the missing statement.
- `internal/testport/TestPort_IsolationDropIndexConcurrently1` advances
  past the `EXPLAIN (COSTS OFF) EXECUTE` step (the diff now begins on
  the EXPLAIN-format-parity gap — Projection wrapper, `Sort Key:`/`Index
  Cond:` rendering, `(rows=N)` suffix under `COSTS OFF` — out of scope
  for this slice).

## Verification

    go test -race -count=1 ./internal/server/ ./internal/executor/ \
                            ./internal/planner/ ./internal/parser/
    # all green

## Follow-ups (out of scope)

1. `EXECUTE <name>` (no EXPLAIN) also re-dispatches the stored raw SQL,
   which re-runs the `PREPARE` statement rather than the inner query.
   Storing both raw SQL and the parsed `Query` AST in the registry
   removes the redundant parse and fixes this in one shot.
2. EXPLAIN output under `COSTS OFF` still appends `(rows=N)` to each
   node label and lacks `Sort Key:` / `Index Cond:` detail.  These are
   the next diff sources surfaced by `drop-index-concurrently-1` and
   need a dedicated EXPLAIN-formatter parity pass.
