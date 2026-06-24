# 0118-0032 — `alter-table-3` isolation spec: ENABLE/DISABLE TRIGGER lock + abort-time lock release (M0118-0008)

**Status:** accepted
**Date:** 2026-06-22
**Spec:** `postgres/src/test/isolation/specs/alter-table-3.spec`
**Test:** `TestPort_IsolationAlterTable3` (`internal/testport/isolation_port_test.go`)

## Summary

Promotes the `alter-table-3` isolation spec to **pass-required** (byte-identical
to PG 18.3 across all 48 permutations). The spec mixes
`ALTER TABLE … ENABLE/DISABLE TRIGGER` with a concurrent `SELECT … FOR UPDATE`
and a duplicate-key `INSERT`, to observe what waits and what proceeds:

```
session s1: BEGIN; ALTER TABLE a DISABLE TRIGGER t; ALTER TABLE a ENABLE TRIGGER t; COMMIT;
session s2: BEGIN; SELECT * FROM a WHERE i = 1 LIMIT 1 FOR UPDATE; INSERT INTO a VALUES (0); COMMIT;
```

`ENABLE/DISABLE TRIGGER` uses `ShareRowExclusiveLock`, which **conflicts** with a
concurrent write's `RowExclusiveLock` but **not** with a reader's
`AccessShareLock` or `SELECT … FOR UPDATE`'s `RowShareLock`.

This was the closest of the M0118-0008 tail to passing: a throwaway probe ranked
every remaining spec by first-divergence cost and `alter-table-3` diverged only
at output line 62 (the first 61 lines already matched). Two engine fixes closed
the gap; no parser feature work and no new lock primitives were required.

## Fixes

### 1. ENABLE/DISABLE TRIGGER takes a transaction-scoped ShareRowExclusiveLock

Previously `ALTER TABLE … ENABLE/DISABLE TRIGGER` was parsed as a pure no-op
(the parser consumed the tokens and emitted no action), so the executor took no
lock. PostgreSQL's `AlterTableGetLockLevel` returns `ShareRowExclusiveLock` for
`AT_EnableTrig` / `AT_DisableTrig` (and the REPLICA/ALWAYS variants).

- **Parser** (`internal/parser/ddl.go`, `internal/parser/ast.go`): the
  ENABLE/DISABLE arm now scans the remainder of the statement for a `TRIGGER`
  target and, when found, sets the new `AlterTableStmt.EnableDisableTrigger`
  flag. RULE / other variants keep the old no-op (no lock), bounding the change.
- **Executor** (`internal/executor/operators_ddl.go`, `execAlterTable`): when the
  flag is set it looks up the table and acquires
  `acquireDDLLockTxn(rel, lockmgr.ShareRowExclusiveLock)` — the same
  transaction-scoped DDL-lock helper `CREATE TRIGGER` uses (0118-0027). The
  enable/disable itself remains a semantic no-op in v0 (the spec's trigger never
  fires — there is no UPDATE — so only the lock is observable).

Because `acquireDDLLockTxn` is confined to explicit transactions and user
relations (no-op in autocommit and for system catalogs), and `RowExclusiveLock`
is self-compatible, ordinary concurrent DML never blocks at the table level.

### 2. Abort releases the transaction's table locks immediately

The duplicate-key `INSERT` (`s2c`) acquires a `RowExclusiveLock` on `a`
(`acquireWriteLockTxn`) and then errors. In several permutations a later
`ALTER TABLE … DISABLE TRIGGER` in **session 1** ran while session 2 was still
in the aborted-but-open state. goopg held the failed transaction's
`tableLockMgr` locks until the explicit `ROLLBACK`, so s1's
`ShareRowExclusiveLock` request blocked on the dead `RowExclusiveLock` — but PG
shows it proceeding immediately.

Verified against PG 18.3 (`./postgres/local_install`): after a statement errors,
`pg_locks` shows **zero** locks held on the table by the aborted backend even
though the transaction block is still open (`idle in transaction (aborted)`).
PostgreSQL's `AbortTransaction` releases heavyweight locks at the moment of
abort, not at the eventual `ROLLBACK`.

`connTxState.Fail()` (`internal/server/conn_tx.go`) — invoked by the dispatcher
whenever a statement errors inside an explicit transaction — now mirrors this:
it releases the transaction's `tableLockMgr` locks via
`executor.ReleaseTableLocks(c.LockBackendID)`.

**Savepoint correctness.** PostgreSQL releases locks only on **top-level** abort;
a **subtransaction** abort (`ROLLBACK TO SAVEPOINT`) transfers the subxact's
locks to the parent and retains them. The release is therefore gated on
`c.sess.SavepointDepth() == 0` — when a savepoint is open the locks are kept and
`End()` releases them on the eventual COMMIT/ROLLBACK. The release is idempotent
(`tableLockMgr.ReleaseAll`), so `End()` releasing again under the same identity
is harmless.

Scope: only the `tableLockMgr` (LOCK TABLE / DDL / DML table) locks are released
in `Fail()` — relation-level (`globalRelLockMgr`) and advisory locks are left to
`End()`, keeping blast radius minimal and untouching the SSI/predicate and
advisory-lock specs.

## Why this is safe

- The ENABLE/DISABLE TRIGGER lock reuses the established `acquireDDLLockTxn`
  path; `create-trigger`, `sequence-ddl`, and `reindex-*` strict specs continue
  to pass.
- The abort-time release exactly mirrors PG's `AbortTransaction` semantics and is
  gated by savepoint depth, so the savepoint specs (`delete-abort-savept{,2}`,
  `aborted-keyrevoke`) are unaffected — verified PASS.

## Gates

- `TestPort_IsolationAlterTable3` strict **PASS** (48 permutations, byte-identical).
- Lock-sibling regression: `create-trigger`, `sequence-ddl`, `reindex-schema`,
  `multiple-cic`, `lock-nowait`, `nowait`(×5), `insert-conflict-*` — **PASS**.
- Savepoint/abort regression: `delete-abort-savept`, `delete-abort-savept2`,
  `aborted-keyrevoke` — **PASS**.
- SSI/serializable regression: `simple-write-skew`, `receipt-report`,
  `read-only-anomaly{,2}`, `serializable-parallel{,2}` — **PASS**.
- `-race` lockmgr + server; parser/executor units; build; pgbench smoke
  (pre-commit hook).

## Remaining M0118-0008 tail (deferred)

`alter-table-{1,2,4}` (ADD/VALIDATE CONSTRAINT lock semantics + FK `NOT VALID`
parsing; INHERITS), the `*-conflict` family (truncate/vacuum/cluster — need
CREATE ROLE/GRANT/SET ROLE privilege infrastructure), `reindex-concurrently-toast`
(`allow_system_table_mods` + TOAST reindex), partition specs (ATTACH/DETACH
PARTITION CONCURRENTLY), `vacuum-{skip-locked,concurrent-drop,no-cleanup-lock}`,
`inherit-temp`, `plpgsql-toast`.
