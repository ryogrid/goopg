Loop #28: M0118-0008 — live `tableLockMgr`→`pg_locks` bridge (design 0118-0070).
COMMITTED + pushed at loop end. NOT a promotion (blocker 1 of 4).

## What landed (enabler)
The live pg_locks bridge for partition-drop-index-locking's s3getlocks
(`pg_locks JOIN pg_class JOIN pg_stat_activity`). Three parts:
1. `lockBackendPID` registry (relation_locks.go): connection's stable
   `LockBackendID` → wire PID. Register/Unregister per connection in server.go
   (runPostStartupLoop, has both LockBackendID + pid). Per-statement autocommit
   BackendIDs NOT registered → pid 0 (dropped by the pg_stat_activity join).
2. `tableLockMgrPgLockRows()`: enumerates `tableLockMgr.AllLocks()` (0118-0069),
   filters relation-level tags (Block==0&&Offset==0), one pg_locks row per
   holder/waiter (mode=Mode.String(), granted=t/f), appended to
   globalRelLockMgr.PgLockRows() in the RelationLockRowsFunc init.
3. Dedup (lockRelationTransitively, operators_ddl.go): record in globalRelLockMgr
   ONLY when no real lock taken (autocommit TxnLockBackendID==0 OR lmMode==NoLock).
   Explicit-txn LOCK TABLE is now surfaced solely by the bridge with a real PID
   (else doubled).

Files: internal/executor/relation_locks.go, operators_ddl.go (dedup gate),
internal/server/server.go (register/unregister), relation_locks_bridge_test.go
(new), docs/design/0118-0070-*.md + README index, deferral_ledger.md.

Key symbols: lockBackendPID, RegisterLockBackendPID/UnregisterLockBackendPID,
tableLockMgrPgLockRows, lockmgr.LockManager.AllLocks, lockRelationTransitively
(operators_ddl.go ~12546), connTxState.LockBackendID, runPostStartupLoop (pid).

Gates: new bridge test -race PASS; lock-touching strict spec set PASS ~131s
(lock-nowait/alter-table-1/2/3/create-trigger/truncate-vacuum-cluster-conflict/
inherit-temp/reindex-concurrently/sequence-ddl/drop-index-concurrently-1/
vacuum-no-cleanup-lock/detach-partition-concurrently-1..4/insert-conflict-specconflict);
go test -race ./internal/lockmgr/; go build ./... clean; pgbench smoke=pre-commit hook.

## Probe result (2026-06-24)
Spec's 1st s3getlocks advanced 0 → 4/7 expected rows, all PID-joined:
DROP INDEX partition-tree AccessExclusive (parent/subpart t, _child f-waiter) +
s1 _child AccessShare t. PID mapping validated through the real SQL join.

## partition-drop-index-locking remaining blockers (resume point)
1. **DROP INDEX must lock the index relation itself** (mechanical): expected has
   `part_drop_index_locking_idx` AccessExclusive|t. lockDropIndexTableTree
   (0118-0067) locks idx.Table + partition descendants but NOT the index oid.
2. **SELECT locks the leaf's INDEXES** (mechanical, blocker 2): acquireScanReadLockTxn
   (0118-0018) locks the table only; the 2 missing rows are
   _subpart_child_id_idx{,1} AccessShare|t. Do (1)+(2) next → advances to 6/7.
3. **pg_stat_activity idle-query retention**: s.query empty for the s1 AccessShare
   row — goopg clears Query on return to idle (activity/registry.go UpdateState
   `else if state=="idle"`); PG retains the most-recent query for idle-in-txn.
4. **Transactional-DDL cross-session catalog visibility** (MILESTONE-SIZED, shared
   with alter-table-4 / partition-concurrent-attach): 2nd s3getlocks (after
   s1commit, before s2commit) must still show the dropped index's pg_class row +
   index-child AccessExclusive locks; goopg removes it from the shared in-memory
   catalog synchronously.

Next step: blockers (1)+(2) — index-relation + SELECT-index AccessShare locks
(both mechanical, advance 4/7 → 6/7). Then idle-query retention + the txnl-DDL
visibility milestone.

## M0118-0008 hard tail (all Effort-L, deferred)
- partition-drop-index-locking: blockers above.
- alter-table-4 + partition-concurrent-attach: transactional-DDL cross-session
  catalog visibility (milestone-sized MVCC catalog subsystem).
- reindex-concurrently-toast: real TOAST relations (reltoastrelid=0; text inline).
- WHERE CURRENT OF positioned UPDATE/DELETE: project-wide, parsed (CurrentOf) no executor site.
