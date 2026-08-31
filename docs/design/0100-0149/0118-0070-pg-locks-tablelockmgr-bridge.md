# 0118-0070 — Live `tableLockMgr` → `pg_locks` bridge (M0118-0008 partition-drop-index-locking enabler)

Status: **enabler landed** (NOT a spec promotion). Part of the
`partition-drop-index-locking` chain (after 0118-0067 DROP INDEX partition-tree
locking, 0118-0068 CREATE INDEX recursion, 0118-0069 `LockManager.AllLocks()`).

## Problem

The `partition-drop-index-locking` isolation spec's `s3getlocks` step joins
`pg_locks` to `pg_class` and `pg_stat_activity`:

```sql
SELECT s.query, c.relname, l.mode, l.granted
FROM pg_locks l
  JOIN pg_class c ON l.relation = c.oid
  JOIN pg_stat_activity s ON l.pid = s.pid
WHERE c.relname LIKE 'part_drop_index_locking%'
ORDER BY s.query, c.relname, l.mode, l.granted;
```

goopg serves `pg_locks` from a virtual builder. Relation-lock rows came **only**
from `globalRelLockMgr` (`relation_locks.go`) — the display-only registry for
`LOCK TABLE`, which stamps every row with `pid = 0`. But the real
transaction-scoped relation locks the spec needs to observe — `DROP INDEX`'s
`AccessExclusiveLock` on the partition tree (0118-0067) and a `SELECT`/`LOCK
TABLE`'s `AccessShareLock` — are held on the executor's dedicated
`tableLockMgr` (`context.go`), which `pg_locks` never read. So `s3getlocks`
returned **0 joined rows** (pid 0 never matches a `pg_stat_activity` pid).

## Change

A read-side bridge from `tableLockMgr` into `pg_locks`, the live analog of
upstream `GetLockStatusData()` (lock.c) feeding the `pg_locks` SRF.

1. **PID registry** (`relation_locks.go`): `lockBackendPID` maps a connection's
   stable transaction-scoped lock identity (`connTxState.LockBackendID`, minted
   in `runPostStartupLoop`) to its wire-protocol backend PID.
   `RegisterLockBackendPID`/`UnregisterLockBackendPID` are called once per
   connection at startup/teardown in `server.go`. The per-statement `BackendID`
   used for autocommit-transient locks is deliberately **not** registered — such
   locks live for one statement only, so a concurrent `pg_locks` reader rarely
   observes them, and when it does they surface with `pid = 0` (dropped by the
   `pg_stat_activity` join).

2. **Bridge enumeration** (`tableLockMgrPgLockRows`): calls
   `tableLockMgr.AllLocks()` (0118-0069), filters to RELATION-level tags
   (`Block == 0 && Offset == 0` — tuple-level tags have no meaningful
   `pg_locks.relation`), and emits one row per holding/waiter:
   `locktype=relation`, `database=Tag.DB`, `relation=Tag.Rel`,
   `mode=Mode.String()` (already the canonical `"AccessExclusiveLock"`
   spelling), `granted=t/f`, `pid` resolved via the registry (`"0"` when
   unknown). The `pg_locks` init hook now appends these rows to
   `globalRelLockMgr.PgLockRows()`.

3. **Dedup** (`lockRelationTransitively`, operators_ddl.go): a `LOCK TABLE` in an
   explicit transaction records in **both** `globalRelLockMgr` and (via
   `acquireRelLockTxn`) `tableLockMgr`. To avoid a doubled row, the display-only
   registration is now gated to the cases that take **no** real heavyweight lock
   — autocommit (`TxnLockBackendID == 0`, where `acquireRelLockTxn` is a no-op)
   or an exotic/unparsable mode (`lmMode == NoLock`). An explicit-txn `LOCK
   TABLE` with a parsable mode is surfaced solely by the bridge, with a real PID.

## Effect (probe, 2026-06-24)

The spec's first `s3getlocks` advances from 0 joined relation-lock rows to **4
of 7** expected, all joined correctly through the real SQL join:

- `DROP INDEX … | part_drop_index_locking | AccessExclusiveLock | t` ✓
- `DROP INDEX … | part_drop_index_locking_subpart | AccessExclusiveLock | t` ✓
- `DROP INDEX … | part_drop_index_locking_subpart_child | AccessExclusiveLock | f` ✓
  (waiter behind s1's ACCESS SHARE — `granted=f` proves the waiter path)
- `… | part_drop_index_locking_subpart_child | AccessShareLock | t` ✓
  (s1's LOCK TABLE / SELECT AccessShare, PID-joined)

The PID mapping is validated end-to-end: rows appear only because
`l.pid = s.pid` matched a live `pg_stat_activity` row.

## Remaining blockers (spec stays `defer`)

1. **`DROP INDEX` does not lock the index relation itself** — expected shows
   `part_drop_index_locking_idx` `AccessExclusiveLock|t`; `lockDropIndexTableTree`
   (0118-0067) locks the table + partition descendants but not the index oid.
2. **`SELECT` does not lock the leaf's indexes** — `acquireScanReadLockTxn`
   (0118-0018) locks the scanned table only; the spec expects the two child
   index relations (`…_child_id_idx{,1}`) in `AccessShareLock`.
3. **idle-query retention** — the AccessShare row's `s.query` column is empty:
   goopg's `pg_stat_activity` clears `query` when a backend returns to idle,
   whereas PG retains the most-recent query text for an idle-in-transaction
   backend.
4. **Transactional-DDL cross-session catalog visibility** (milestone-sized,
   shared with alter-table-4 / partition-concurrent-attach) — the second
   `s3getlocks` (after `s1commit`, before `s2commit`) must still show the
   dropped index's `pg_class` row + the index-child AccessExclusive locks; goopg
   removes the index from the shared in-memory catalog synchronously, so the
   `JOIN pg_class` loses it.

## Blast radius

Among ported specs only `partition-drop-index-locking` (deferred) and
`insert-conflict-specconflict` read `pg_locks`; the latter filters
`spectoken`/`transactionid` rows, never relation locks. The dedup change only
alters explicit-txn `LOCK TABLE` `pg_locks` output (pid `0` → real pid — strictly
more correct). No regression across the full lock-touching strict spec set
(lock-nowait, alter-table-1/2/3, create-trigger, truncate/vacuum/cluster-conflict,
inherit-temp, reindex-concurrently, sequence-ddl, drop-index-concurrently-1,
vacuum-no-cleanup-lock, detach-partition-concurrently-1..4,
insert-conflict-specconflict).

## Gates

- `go test -race ./internal/lockmgr/` PASS.
- New `internal/executor/relation_locks_bridge_test.go` (granted holder +
  PID stamp / waiter `granted=f` + own PID / unregistered-backend `pid=0` /
  tuple-tag filtered out) PASS with `-race`.
- Full lock-touching strict isolation spec set PASS (~131 s).
- `go build ./...` clean; pgbench smoke = pre-commit hook.

## Oracle

`postgres/src/backend/storage/lmgr/lock.c` `GetLockStatusData()` →
`pg_lock_status()` SRF (`pg_locks` view). Behavior compared against
`./postgres/local_install` PG 18.3 via the spec's expected output.
