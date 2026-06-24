Loop #27: M0118-0008 — `LockManager.AllLocks()` enumeration enabler (design 0118-0069).
COMMITTED + pushed at loop end. NOT a promotion.

## What landed (enabler)
`lockmgr.LockManager.AllLocks() []LockHolding` — read-side analog of upstream
`GetLockStatusData()` (lock.c) that backs pg_locks. Walks `lm.states` under the
manager lock, returns one `LockHolding{Tag,Backend,Mode,Granted}` per (backend,mode):
holders→Granted=true (a holder Mask is expanded one row per set mode bit, matching
pg_locks one-LOCK-per-mode), waiters→Granted=false. Tuple tags (Block/Offset!=0)
included; relation-only callers filter Tag.Block==0&&Tag.Offset==0. Pure addition —
NO live call site references it yet → byte-identical behaviour, zero regression risk.

Files: internal/lockmgr/lockmgr.go (new LockHolding struct + AllLocks method,
inserted after the Waiters method), internal/lockmgr/alllocks_test.go (new),
docs/design/0118-0069-lockmanager-alllocks-enumeration.md + README index,
.ralph/deferral_ledger.md.

Key symbols: lockmgr.LockManager.AllLocks, lockmgr.LockHolding, lockState.holders
(map[BackendID]Mask), lockState.waiters, modeNames (already pg_locks spelling),
bit()/maxMode (mode-bit iteration), Mode.String().

Gates: go test -race ./internal/lockmgr/ PASS (5 new AllLocks tests + full suite);
go build ./... clean; pgbench smoke = pre-commit hook.

## partition-drop-index-locking remaining blockers (resume point)
Full promotion needs THREE more pieces (AllLocks is necessary, not sufficient):
1. **Live pg_locks→tableLockMgr bridge** (next loop, the mechanical one): wire
   `AllLocks()` into `catalog.RelationLockRowsFunc` (relation_locks.go) emitting one
   row per LockHolding: locktype=relation, relation=Tag.Rel, mode=Mode.String()
   (already "AccessExclusiveLock" etc.), granted=Granted, pid=holder's session pid.
   Needs a BackendID→pid registry (TxnLockBackendID → pg_stat_activity pid). DEDUP:
   LOCK TABLE records in BOTH globalRelLockMgr AND tableLockMgr → must unify on one
   source or s1's AccessShare doubles. Regression surface TINY: only
   partition-drop-index-locking reads relation locks (insert-conflict-specconflict
   filters spectoken/transactionid) — verified by grep over ported specs.
2. **SELECT locks the leaf's INDEXES** (AccessShare), not just the table —
   acquireScanReadLockTxn (0118-0018) locks the table only; spec s1select rows include
   ..._subpart_child_id_idx{,1}.
3. **Transactional-DDL cross-session catalog visibility** (MILESTONE-SIZED, shared with
   alter-table-4 / partition-concurrent-attach): the 2nd s3getlocks (after s2drop
   completes, before s2commit) must still show the dropped index's pg_class row — goopg
   removes it from the shared in-memory catalog synchronously, so s3's snapshot loses it.

Next step: the live pg_locks bridge (blocker 1) — wire AllLocks + BackendID→pid map +
dedup. Budget the lock-table/conflict spec suite as the gate (regression surface is
small but verify partition-drop-index-locking + insert-conflict-specconflict).

## M0118-0008 hard tail (all Effort-L, deferred)
- partition-drop-index-locking: live bridge + SELECT-index-locks + txnl-DDL visibility.
- alter-table-4 + partition-concurrent-attach: transactional-DDL cross-session catalog
  visibility (milestone-sized MVCC catalog subsystem).
- reindex-concurrently-toast: real TOAST relations (reltoastrelid=0; text inline).
- WHERE CURRENT OF positioned UPDATE/DELETE: project-wide, parsed (CurrentOf) no executor site.
