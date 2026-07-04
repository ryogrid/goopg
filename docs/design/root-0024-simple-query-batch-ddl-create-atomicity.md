# root-0024 — Simple-query multi-statement batch: CREATE TABLE/INDEX atomicity on abort

## Context

Discovered as a fresh, non-view-specific finding while auditing `ALTER VIEW`
grammar (M0110-0001 DU-002 slice 444, deferral-ledger row 2026-07-04):

```
CREATE TABLE t (a int);
ALTER TABLE t ALTER COLUMN a SET STORAGE external;
SELECT 1/0;
```

sent as one `psql -c "stmt1; stmt2; stmt3"` simple-query message left `t`
permanently registered in the live catalog (`\d t` shows it, `INSERT INTO t`
works) but with **zero** `pg_attribute` rows — `pg_dump` emits
`CREATE TABLE t (\n);`. Real PostgreSQL's simple-query protocol treats the
whole multi-statement message as one implicit transaction
(`postgres/src/backend/tcop/postgres.c` `exec_simple_query`); a later
statement's failure rolls back *everything* in that message, including an
earlier successful `CREATE TABLE`. goopg's dispatch already begins exactly one
`mvcc.Transaction` per Query message (`dispatchSimpleQueryViaExecutor`,
`internal/server/dispatch.go`), so the transactional catalog-heap writes
(`pg_class`/`pg_attribute` rows, written through that same `tx`) correctly
rolled back — but `catalog.InMemory.RegisterTable`, the live in-memory
catalog mutation `execCreateTable` performs, is **not** transactional; it was
never undone.

## Root cause

`sess.RecordDDLCreate` (`internal/executor/session.go`) — the mechanism that
already lets an explicit `BEGIN; CREATE TABLE; ROLLBACK;` clean up correctly —
records a pending `DDLUndoEntry` any time `o.ctx.Session` is a
`*executor.BasicSession`. `ProcessRollbackUndos` (`internal/executor/
operators_tx.go`) drains that list and calls `rollbackDDLCreate` (which does
`catalog.DropTable`/`DropIndex` plus stamps `xmax` on the catalog-heap rows)
for each entry — but it was only ever invoked from two places: the explicit
`ROLLBACK` statement handler and the `TxRollback` shortcut, both keyed off
`connTx.Session()`.

`connTx.Session()` (`internal/server/conn_tx.go`) returns the `*BasicSession`
**only when an explicit `BEGIN` is active** — for a plain autocommit
multi-statement batch it returns `nil`, so `dispatchSimpleQueryViaExecutor`'s
`ectx.Session` stayed `nil` for the whole message. Two consequences:

1. `RecordDDLCreate`'s type assertion (`o.ctx.Session.(*BasicSession)`) never
   succeeded, so the `CREATE TABLE` was never tracked for undo in the first
   place.
2. Even had it been tracked, the top-level `defer` in
   `dispatchSimpleQueryViaExecutor` that rolls back the implicit `tx` on abort
   (`autoCommit && !commit`) never called `ProcessRollbackUndos` — it went
   straight to `s.cfg.TxnMgr.Rollback(tx)`.

## Fix

`internal/server/dispatch.go`:

- `ectx` is now predeclared (`var ectx *executor.Context`) above the abort
  defer so the defer can reach it (it is assigned by `executor.NewContext()`
  a few lines later, once the executor context is actually built).
- The connTx-wiring block that sets `ectx.Session = sess` when an explicit
  transaction is active now has an `else` arm: when `connTx.Session()` is
  `nil` (autocommit), it wires a **message-scoped, throwaway**
  `executor.NewBasicSession()` instead of leaving `ectx.Session` nil. This
  session is never shared with `connTx` and is discarded when the function
  returns — it exists purely so `RecordDDLCreate` has somewhere to record
  against for the duration of this one Query message.
- The abort defer now calls `executor.ProcessRollbackUndos(ectx, bs)` (type
  asserting `ectx.Session` to `*executor.BasicSession`) **before**
  `s.cfg.TxnMgr.Rollback(tx)`, mirroring the ordering `ProcessRollbackUndos`'s
  own doc comment requires (catalog lookups must still work at that point).

`internal/executor/session.go`:

- `RecordDDLCreate` had a dead-code guard, `if !s.inTx { return }`. Historically
  every prior caller of this method only ever ran with `inTx == true` — the
  only way to obtain a non-nil `*BasicSession` in `ectx.Session` before this
  fix was `connTx.Session()`, which is populated exclusively by
  `connTx.Begin()`, and `Begin()` unconditionally calls
  `BeginExplicitTransaction` (`inTx = true`) first. The guard was therefore
  never actually reached with `inTx == false` — until this fix's throwaway
  autocommit-batch session, which deliberately keeps `inTx == false` (see
  "Why `inTx` stays false" below). Removed; the doc comment now describes the
  real invariant: recording is unconditional, and a session that never aborts
  simply never has its list drained (`ProcessRollbackUndos` is the only
  reader, and it is only called on an actual abort path).

### Why `inTx` stays false on the throwaway session

`BasicSession.InExplicitTransaction()` (`== s.inTx`) gates roughly twenty
other call sites unrelated to this bug — deferred UNIQUE/EXCLUDE/FK constraint
timing (`deferred_unique.go`, `deferred_exclusion.go`, `operators_fk.go`),
`TRUNCATE`/`DROP TABLE`-inside-savepoint page-snapshotting, enum/composite
pending-create tracking, `pg_stat_*` cumulative-stats scoping. Flipping
`inTx = true` on the throwaway session (e.g. via `BeginExplicitTransaction`)
would silently change ALL of those for every autocommit statement, which is a
much larger blast radius than this fix's scope and was deliberately avoided —
see "Deferred" below. The throwaway session therefore stays `inTx == false`
end-to-end; only `RecordDDLCreate`/`ProcessRollbackUndos` were touched.

## Verification

- `TestSimpleQueryBatchAbortUndoesEarlierCreateTable`
  (`internal/server/dispatch_batch_atomicity_test.go`): drives the exact
  `CREATE TABLE ...; SELECT * FROM <missing>;` batch over a real wire
  connection (`startCopyExecServer`), asserts a `CREATE TABLE` CommandComplete
  followed by an ErrorResponse, then asserts the table is **not** found in the
  live catalog afterward (confirmed RED without the fix — the table survived).
  A second assertion confirms a standalone (non-aborting) `CREATE TABLE` in
  its own message still persists normally, guarding against an
  over-broad "autocommit DDL is now always transient" regression.
- Full suites: `internal/server`, `internal/executor`, `internal/catalog`,
  `internal/mvcc`, `internal/wal`, `internal/initdb` — all PASS, no
  regression.
- `-race`: `internal/mvcc`, `internal/wal`, `internal/server` — PASS (practice
  card requirement for transaction-rollback-adjacent changes).
- `scripts/tpch-spotcheck.sh`: PASS (Q12=2, Q13=33).
- pgbench smoke: enforced by the `.githooks/pre-commit` hook at commit time.

## Deferred

- **Enum/composite-type creation and TRUNCATE/DROP-in-savepoint tracking are
  NOT yet undo-aware for an autocommit multi-statement batch.** These are
  gated by `Session.InExplicitTransaction()` (`inTx`), which — per "Why `inTx`
  stays false" above — this fix deliberately leaves `false` on the throwaway
  session to avoid a much wider blast radius (deferred-constraint-check
  timing in particular) in one bounded change. A batch like
  `CREATE TYPE mood AS ENUM ('a','b'); SELECT 1/0;` in one message still
  leaks the enum registration today, the same bug class as the one this fix
  closes for `CREATE TABLE`/`CREATE INDEX`. Resume point: introduce a
  dedicated flag on `BasicSession` (not `inTx`) that `RecordDDLCreate`-style
  call sites can share without perturbing the ~20 `InExplicitTransaction()`
  call sites enumerated above; audit `deferred_unique.go`/
  `deferred_exclusion.go`/`operators_fk.go` deferred-constraint-timing
  behavior separately before touching `inTx` itself.
- **A `CREATE TABLE t1(...); BEGIN; CREATE TABLE t2(...); ROLLBACK;`
  compound batch still loses `t1`'s undo entry.** When an explicit `BEGIN`
  appears mid-batch, `connTx.Begin()` creates the real `*BasicSession` and
  `dispatch.go` re-wires `ectx.Session` to it (pre-existing code, M0118-0009),
  discarding the throwaway session's already-recorded `pendingDDL` for `t1`.
  Not a regression (this compound case was never undoable before this fix
  either), but also not fixed by it. Resume point: when re-wiring
  `ectx.Session` off the throwaway onto the real session in that mid-batch
  `BEGIN` handler (`internal/server/dispatch.go`, the `connTx.Begin(ctx.Tx)`
  branch inside the `TxBegin` case), copy over the throwaway session's
  pending DDL/undo lists first.

Deferral ledger: row appended (M0110-0001, resolved) cross-referencing this
doc for both residuals.
