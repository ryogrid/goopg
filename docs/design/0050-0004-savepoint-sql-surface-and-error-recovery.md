# 0050-0004 — Savepoint SQL surface and error recovery

**Status:** accepted
**Date:** 2026-05-05
**Milestone:** 0050 — Savepoints and subtransactions
**Supersedes:** —

## Context

The previous three docs build the runtime; this one lands the SQL verbs
and the user-visible error-recovery semantics. psql `\set
ON_ERROR_ROLLBACK on` automatically wraps every statement in a savepoint
so a single error doesn't kill the whole session — that requires the
bare-named savepoint shorthand and clean error-rollback paths.

## Implementation (landed 2026-05-05)

1. **Parser (pre-existing):** `SavepointStmt{Name}`, `ReleaseSavepointStmt{Name}`,
   `RollbackToSavepointStmt{Name}` AST nodes and `KwSavepoint`/`KwRelease` keywords
   were already implemented in M0050-0001. `parseRollback` disambiguates `ROLLBACK`
   from `ROLLBACK TO [SAVEPOINT] name` via the `KwTo` check.

2. **Planner:** `TxSavepoint`, `TxRelease`, `TxRollbackTo` added to `TransactionVerb`
   enum; `Transaction` node gains a `Name string` field. `Plan()` routes all three
   AST types to `*Transaction` with the correct verb and name.

3. **Executor — subxact XID allocation:**  `Manager.AllocateSubXid(parentXid)`
   increments `nextXID`, registers the new XID as a child of `parentXid` in the
   global subxact map (M0050-0002), and returns it — without adding to the `active`
   map (sub-XIDs are not independent top-level transactions).

4. **Executor — BasicSession extension:** `BasicSession` gains:
   - `subxactStack mvcc.SubxactStack` — savepoint stack.
   - `currentSubXid storage.TransactionID` — current effective writer XID (0 = use
     top-level).
   - `txFailed bool` — in-failed-transaction flag (SQLSTATE 25P02 foundation).
   - `EffectiveWriterXID()`, `PushSavepoint()`, `ReleaseSavepoint()`,
     `RollbackToSavepoint()`, `IsTransactionFailed()`, `SetTransactionFailed()`.

5. **Executor — operators_tx.go:** `execSavepoint` / `execRelease` / `execRollbackTo`
   with SQLSTATE 25P01 guard (outside-transaction). `execSavepoint` allocates a
   sub-XID, captures a snapshot, pushes the stack, and updates `ctx.Tx.XID`.
   `execRollbackTo` marks all aborted sub-XIDs via `MarkSubxactAborted`, then sets
   `ctx.Tx.XID` to a fresh sub-XID. `execRelease` restores `ctx.Tx.XID` to the
   parent effective XID.

6. **Tuple visibility — `TupleVisibleSubxact` extended:** Added `isCurrentTxXID`
   helper that matches `currentXID` exactly OR its top-level ancestor XID (via
   `r.TopLevelXid`), so tuples inserted before a SAVEPOINT (under the top-level XID)
   remain visible inside the subtransaction. The xmax branch uses the same helper.

7. **operators_storage.go:** Both `seqScanOp.Next()` visibility checks changed from
   `mvcc.TupleVisible` to `mvcc.TupleVisibleSubxact(…, ctx.TxnMgr)` so the subxact
   resolver is consulted on every scan.

8. **dispatch.go:** `transactionTag` extended for `TxSavepoint → "SAVEPOINT"`,
   `TxRelease → "RELEASE"`, `TxRollbackTo → "ROLLBACK"`.

### Deferred

- Wire-protocol session-level transaction management (BEGIN/COMMIT/ROLLBACK/SAVEPOINT
  across Query messages). The current dispatch model creates one implicit transaction
  per Query message; savepoints within a single multi-statement Query work, but
  explicit `BEGIN` followed by `SAVEPOINT` in separate messages requires session-state
  plumbing in a follow-up loop.
- Implicit savepoint for `\set ON_ERROR_ROLLBACK on` (requires per-statement error
  recovery in the dispatcher).
- SQLSTATE 25P02 enforcement at the wire layer (requires session-level tx tracking).

## Original Plan

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
