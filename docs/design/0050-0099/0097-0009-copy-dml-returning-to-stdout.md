# 0097-0009 — COPY (INSERT/UPDATE/DELETE … RETURNING) TO STDOUT

**Milestone:** M0097-0009 (COPY + sequences + identity + generated columns)
**Date:** 2026-05-25
**Status:** Implemented

## Problem

`COPY (query) TO STDOUT` only accepted a `SELECT` body. PostgreSQL also
accepts data-modifying statements that carry a `RETURNING` clause:

```sql
COPY (INSERT INTO t (c) VALUES ('f') RETURNING id) TO STDOUT;
COPY (UPDATE t SET c = 'g' WHERE c = 'f' RETURNING id) TO STDOUT;
COPY (DELETE FROM t WHERE c = 'g' RETURNING id) TO STDOUT;
```

The COPY runs the DML and streams its `RETURNING` rows as COPY data.
This is exercised by the `copydml` regress case. Before this change the
parser rejected the body with `syntax error at or near "expected
keyword select (got insert)"`.

## Changes

### Parser (`internal/parser/copy.go`, `ast.go`)
- `CopyStmt` gains a `QueryDML Stmt` field (alongside the existing
  `Query *SelectStmt`). The SELECT form keeps using `Query`; the DML
  form uses `QueryDML`.
- New `parseCopyInnerQuery` dispatches on the leading keyword inside
  `COPY ( … )`: `INSERT`/`UPDATE`/`DELETE` parse via the existing DML
  parsers, `WITH` via `parseStatementWithCTE`, everything else via
  `parseSelect`.
- The "query form is only valid with TO" check now covers `QueryDML`
  too.

### Planner (`internal/planner/copy.go`)
- `planCopy` handles `QueryDML` by planning the statement through the
  normal `Plan` entry point (which runs the analyzer + `planInsert` /
  `planUpdate` / `planDelete`).
- The plan **must** have a `RETURNING` clause — `returningSchemaOf`
  reads `ReturningSchema` from the `Insert`/`Update`/`Delete`/`Merge`
  node. A `nil` schema yields `0A000 "COPY query must have a RETURNING
  clause"`, matching PostgreSQL.
- The `Copy` node's `schema` is set from the RETURNING schema (note:
  `Insert.Output()` returns `nil` even with RETURNING, so the schema is
  stashed on the `Copy` node directly).

### Executor (`internal/executor/copy.go`)
- `buildCopySource` now prefers `plan.Output()` (the `Copy` node's
  resolved schema) over `plan.Query.Output()`, because the inner
  `Insert` plan reports `Output()==nil`.

### Server (`internal/server/copy.go`) — commit-ordering fix
- The old `runCopyTo` wrote `CopyDone` **and** `CommandComplete` +
  `ReadyForQuery`, then the caller committed the COPY-internal
  transaction. For read-only COPY this is harmless, but for COPY (DML)
  the client saw "ready" and raced ahead with its next query **before**
  the commit landed — the just-inserted row was invisible (and the
  follow-up `SELECT`/`UPDATE` missed it).
- Split into `runCopyToStream`, which streams rows + `CopyDone` and
  returns the row count. `dispatchCopyViaExecutor` now **commits the
  transaction before** emitting `CommandComplete` + `ReadyForQuery`,
  mirroring the existing `CopyFrom` path. On a mid-stream executor
  error it rolls back and emits nothing further.

## Verification

- Live server (`psql`): `COPY (INSERT/UPDATE/DELETE … RETURNING) TO
  STDOUT` streams the RETURNING ids; the row is immediately visible to
  the next connection; no-RETURNING forms give `COPY query must have a
  RETURNING clause`. Existing `COPY (SELECT …)`, `COPY table TO`, and
  `COPY … FROM STDIN` paths unchanged.
- Unit tests:
  - `internal/parser/copy_test.go`: `TestParseCopyDMLToStdout`,
    `TestParseCopyDMLFromRejected`.
  - `internal/planner/copy_test.go`: `TestPlanCopyDMLReturningToStdout`,
    `TestPlanCopyDMLWithoutReturningRejected`.
  - `internal/server/copy_executor_test.go`:
    `TestCopyDMLReturningExecutorEndToEnd` (asserts the commit-ordering
    visibility fix), `TestCopyDMLNoReturningRejected`.

## Residual (copydml does not yet fully pass)

The `copydml` regress case also exercises `CREATE RULE` (`DO
ALSO`/`DO INSTEAD`/conditional/multi-statement rules over COPY) and
row-level triggers whose `RAISE NOTICE` output must appear. goopg has no
rewrite-rule system and the trigger-NOTICE path is unimplemented, so
those statements (and their PG-specific error wording such as `DO ALSO
rules are not supported for COPY`) remain divergent. Those are separate,
larger features tracked outside this sub-milestone.
