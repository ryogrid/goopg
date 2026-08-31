# 0118-0027 — `create-trigger` isolation spec: transaction-scoped DDL/DML table-lock conflict (M0118-0008)

**Status:** accepted
**Date:** 2026-06-22
**Milestone:** M0118-0008 (DDL / VACUUM / maintenance concurrency isolation specs)
**Spec:** `postgres/src/test/isolation/specs/create-trigger.spec`

## Summary

Promotes `create-trigger` to pass-required (byte-identical to PostgreSQL 18.3)
by giving DDL and DML statements **transaction-scoped** heavyweight table locks
that conflict per the standard PostgreSQL lock matrix:

- `CREATE TRIGGER` now takes a transaction-scoped **`ShareRowExclusiveLock`** on
  its target table (`acquireDDLLockTxn`).
- `INSERT` / `UPDATE` / `DELETE` now take a transaction-scoped
  **`RowExclusiveLock`** on the target relation (`acquireWriteLockTxn`).

These are the **write** and **DDL** siblings of the existing read-side
`acquireScanReadLockTxn` (transaction-scoped `AccessShareLock`, landed for the
`timeouts` spec in 0118-0018). With all three modes present on the
`tableLockMgr`, a concurrent `UPDATE` (`RowExclusiveLock`) blocks until a
`CREATE TRIGGER` transaction commits, while a concurrent `SELECT ... FOR UPDATE`
(`RowShareLock`) proceeds — exactly matching PG 18.3 across all 25 permutations.

## Background — what the spec exercises

`create-trigger.spec` (comment: *"CREATE TRIGGER uses ShareRowExclusiveLock so
we mix writes with it to see what works or waits"*) interleaves, across 25
permutations of two sessions:

- **s1:** `BEGIN` → `CREATE TRIGGER t AFTER UPDATE ON a EXECUTE PROCEDURE f()`
  → `COMMIT`.
- **s2:** `BEGIN` → `SELECT * FROM a WHERE i = 1 FOR UPDATE` → `UPDATE a SET i =
  4 WHERE i = 3` → `COMMIT`.

The observable PG behavior the spec pins:

| s2 step | table-level mode | vs `CREATE TRIGGER` (`ShareRowExclusiveLock`) |
|---------|------------------|-----------------------------------------------|
| `SELECT ... FOR UPDATE` | `RowShareLock` | **compatible** → proceeds |
| `UPDATE` | `RowExclusiveLock` | **conflicts** → waits until s1 commits |

The lock-conflict matrix (verbatim from `lock.c`, in
`internal/lockmgr/lockmgr.go`) confirms: `ShareRowExclusiveLock` conflicts with
`RowExclusiveLock` but **not** `RowShareLock`.

## Before this change

`internal/testport/framework` ranked the spec at `defer`: its sole divergence
from expected was a missing `<waiting ...>` on the `s2c` `UPDATE` step — goopg
let the `UPDATE` run immediately because:

- `CREATE TRIGGER` took **no** transaction-scoped table lock (it only mutated
  the in-memory `catalog.Table.Triggers` slice), so nothing was held across s1's
  open transaction for s2 to conflict with; and
- `UPDATE` / `DELETE` took only a **statement-scoped** `RowExclusiveLock` (via
  `acquireRelLock` on the per-Query lockmgr, released at the end of each Query
  message — see `lockmgr_locks_are_statement_scoped` memory), plus an
  `AccessShareLock` on the txn-scoped `tableLockMgr` for the scan portion.
  `AccessShareLock` does **not** conflict with `ShareRowExclusiveLock`, so even a
  trigger lock would not have blocked it.

## Change

Three additions, all confined to the same blast-radius envelope as the existing
`acquireScanReadLockTxn` (`internal/executor/context.go`):

```
no-op when  c.TxnLockBackendID == 0        (single-statement autocommit)
        or  rel.RelOid < firstNormalObjectOID  (system catalogs)
```

1. **`Context.acquireWriteLockTxn(rel)`** — transaction-scoped
   `RowExclusiveLock`. Wired into `insertOp.Open`, `updateOp.Open`,
   `deleteOp.Open` (`internal/executor/operators_storage.go`), right after the
   existing statement-scoped `acquireRelLock(rel, RowExclusiveLock)`.

2. **`Context.acquireDDLLockTxn(rel, mode)`** — transaction-scoped lock in the
   requested mode. Wired into `execCreateTrigger`
   (`internal/executor/operators_ddl.go`) with `ShareRowExclusiveLock`.

3. Both reuse the existing `acquireRelLockTxn` → `tableLockMgr.AcquireWithTimeout`
   path, so they inherit deadlock detection, `lock_timeout`/`statement_timeout`
   handling, and per-transaction lifetime (released at COMMIT/ROLLBACK by
   dispatch.go, surviving the per-statement `ReleaseAll`).

### Why the conflict resolves correctly (no spurious self-deadlock)

When s2 requests `RowExclusiveLock` it already holds `AccessShareLock` on the
same tag (from its earlier `SELECT ... FOR UPDATE` scan). The lockmgr tracks
held modes as a per-backend bitmask, so the second acquire is an upgrade, not a
re-entrant block. With s1 holding a conflicting `ShareRowExclusiveLock` and **no
other waiters** on the tag, `canGrantImmediately` takes its simple branch
(`!ConflictsWith(RowExclusiveLock, ShareRowExclusiveLock)` → false) and s2 parks
until s1's `COMMIT` releases the trigger lock — never a `hasSimpleDeadlock`
(s1 is a holder, not a conflicting waiter).

## Blast radius

- `RowExclusiveLock` is **self-compatible** and compatible with `AccessShare` /
  `RowShare` / `RowExclusive`, conflicting only with the DDL-grade modes
  (`Share`, `ShareRowExclusive`, `Exclusive`, `AccessExclusive`). Concurrent DML
  on the same table therefore never blocks at the table level — verified by the
  pgbench TPC-B smoke (UPDATE-heavy, **0 failed transactions**, no TPS
  regression).
- The new acquires are no-ops in autocommit and for system catalogs, so the
  hot path for single-statement queries is untouched.
- Read-side behavior is unchanged (`acquireScanReadLockTxn` still
  `AccessShareLock`), so `timeouts` and the other LOCK TABLE specs are
  unaffected.

## Tests / gates

- New `TestPort_IsolationCreateTrigger` (`runIsoSpecStrict`) — PASS,
  byte-identical to PG 18.3 (25 permutations).
- `-race` on `internal/lockmgr`, `internal/mvcc`, and the executor lock
  integration tests — PASS.
- Row-lock / deadlock / merge / timeout isolation batch
  (`drop-index-concurrently-1`, `fk-snapshot`, `merge-update`,
  `tuplelock-*`, `lock-update-*`, `timeouts` table-level) — PASS
  (`tuplelock-upgrade-no-deadlock` is timing-sensitive and can `defer` under
  heavy parallel load on WSL2; passes in isolation both before and after this
  change — not a regression).
- Full `internal/executor` unit suite — PASS.
- pgbench TPC-B + select-only smoke — 0 failed, no TPS regression.

## Deferred (M0118-0008 group stays open)

The rest of the DDL/VACUUM group needs genuine feature work (ledger
2026-06-22):

- `alter-table-1..4` — `ALTER TABLE ADD CONSTRAINT ... NOT VALID` /
  `VALIDATE CONSTRAINT` with `ShareUpdateExclusiveLock` semantics.
- `truncate-conflict`, `vacuum-conflict`, `cluster-conflict`,
  `cluster-conflict-partition` — `CREATE ROLE` / `GRANT` / `SET ROLE` /
  `ALTER TABLE OWNER` privilege infrastructure and permission-denied paths.
- `reindex-schema`, `reindex-concurrently`, `reindex-concurrently-toast` —
  `REINDEX SCHEMA [CONCURRENTLY]` parsing + `allow_system_table_mods` GUC +
  the concurrent two-phase wait.
- `sequence-ddl` — `ALTER SEQUENCE` lock that `nextval` waits on (since PG10).
- `multiple-cic`, `partition-concurrent-attach`,
  `partition-drop-index-locking`, `detach-partition-concurrently-1..4` —
  `CREATE INDEX CONCURRENTLY` waiting + `ATTACH`/`DETACH PARTITION`.
- `inherit-temp` — exclude another backend's temporary child relations from
  inheritance expansion (`RELATION_IS_OTHER_TEMP`).
- `plpgsql-toast` — TOAST round-trip inside PL/pgSQL.

## Oracle references

- `postgres/src/backend/storage/lmgr/lock.c` — `LockConflicts[]` (mirrored in
  `conflictTab`), `ShareRowExclusiveLock` for `CREATE TRIGGER`.
- `postgres/src/backend/commands/trigger.c` — `CreateTrigger` takes
  `ShareRowExclusiveLock` on the table.
- `postgres/src/backend/executor/execMain.c` — `RowExclusiveLock` on the
  result relation of `INSERT`/`UPDATE`/`DELETE`.
