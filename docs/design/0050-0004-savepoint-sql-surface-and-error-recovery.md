# 0050-0004 — Savepoint SQL surface and error recovery

**Status:** draft
**Date:** 2026-05-04
**Milestone:** 0050 — Savepoints and subtransactions
**Supersedes:** —

## Context

The previous three docs build the runtime; this one lands the SQL verbs
and the user-visible error-recovery semantics. psql `\set
ON_ERROR_ROLLBACK on` automatically wraps every statement in a savepoint
so a single error doesn't kill the whole session — that requires the
bare-named savepoint shorthand and clean error-rollback paths.

## Plan

1. **Parser.** New AST nodes:
   - `SavepointStmt{Name string}`
   - `ReleaseSavepointStmt{Name string}`
   - `RollbackToSavepointStmt{Name string}`
   New keywords `KwSavepoint`, `KwRelease`. (`ROLLBACK`, `TO`,
   `SAVEPOINT` already exist.)
   Disambiguate `ROLLBACK` and `ROLLBACK TO SAVEPOINT name`: if the
   next token after `ROLLBACK` is `TO`, parse as RollbackToSavepoint.
2. **Executor.**
   - `SavepointStmt` → `mvcc.PushSubxact(name)`.
   - `ReleaseSavepointStmt` → `mvcc.ReleaseSubxact(name)`.
   - `RollbackToSavepointStmt` → `mvcc.RollbackToSubxact(name)`.
3. **Outside-of-transaction guard.** All three verbs return SQLSTATE
   25P01 (`no_active_sql_transaction`) outside an explicit BEGIN.
4. **Implicit-savepoint helper.** `Backend.WithImplicitSavepoint(fn)`
   wraps a single statement in a transparent savepoint that's released
   on success and rolled-back-to on error. Dispatcher consults the
   session GUC `enable_on_error_rollback` (matches psql's `\set
   ON_ERROR_ROLLBACK on` per-statement scope).
5. **Error-aware rollback.** When a statement errors inside a subxact:
   - If `enable_on_error_rollback`: silent ROLLBACK TO of the implicit
     savepoint, session stays usable.
   - Otherwise: top-level xact moves to `XACT_ABORTED`; subsequent
     statements (other than `ROLLBACK` and `COMMIT`) error with
     SQLSTATE 25P02 (`in_failed_sql_transaction`) — matches upstream.

## Definition of Done

- Parser tests for each verb (positive + negative cases).
- Executor integration test mirroring the upstream worked example:
  ```
  BEGIN;
    INSERT a;
    SAVEPOINT s;
    INSERT b;
    ROLLBACK TO s;
    INSERT c;
  COMMIT;
  ```
  → only rows `a` and `c` are visible.
- psql `\set ON_ERROR_ROLLBACK on` round-trip test passes.

## Upstream reference

- `postgres/src/backend/parser/gram.y` — `SAVEPOINT`, `RELEASE`,
  `ROLLBACK TO SAVEPOINT` productions.
- `postgres/src/backend/access/transam/xact.c` —
  `DefineSavepoint`, `ReleaseSavepoint`, `RollbackToSavepoint`.
- `postgres/src/bin/psql/common.c` — `ON_ERROR_ROLLBACK` behaviour.

## goopg references

- `internal/parser/parser.go`, `internal/parser/keywords.go`.
- `internal/executor/transactions.go`.
- `internal/server/dispatch.go` — implicit-savepoint wrapper.
- 0050-0001..0003 — runtime support.
