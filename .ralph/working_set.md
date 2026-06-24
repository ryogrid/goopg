Loop #10 (this run): M0118-0008 — `partition-concurrent-attach` **enabler 0118-0076**
(NOT a promotion). Landed piece (b): ATTACH locks the DEFAULT partition. COMMITTED.
Spec stays `defer`.

## What landed
`ALTER TABLE … ATTACH PARTITION` (non-default, inside an explicit txn) now takes a
transaction-scoped `AccessExclusiveLock` on the parent's existing DEFAULT
partition before the conflict check — PG `ATExecAttachPartition`
(`get_default_oid_from_partdesc` → `LockRelationOid(defaultPartOid,
AccessExclusiveLock)`). A concurrent INSERT routing to the default
(`RowExclusiveLock`) would then block until the attach commits.

- operators_ddl.go: new `(*ddlOp).lockDefaultPartitionForAttach(parent)` (near
  `lockPartitionSubtreeAccessExcl`) — scans `InMemory.PartitionChildren` for
  `PartitionBound.IsDefault`, locks via `acquireDDLLockTxn` (no-op in autocommit /
  for system rels). Called in the `AlterTableAttachPartition` case before
  `checkDefaultPartitionDataConflict`.
- attach_default_lock_test.go (new): TestAttachPartitionLocksDefaultPartition,
  TestAttachPartitionNoDefaultNoLock.
- design 0118-0076 + README index; ledger + fix_plan note.

Key symbols: lockDefaultPartitionForAttach, acquireDDLLockTxn, AlterTableAttachPartition case.

## partition-concurrent-attach — remaining 3-piece interlock
- (a) deferred-until-commit ATTACH visibility — concurrent s2 must NOT see the
  uncommitted new partition, so its INSERT routes to the DEFAULT. **Today goopg's
  SHARED catalog makes the uncommitted tpart_2 visible ⇒ s2's insert routes there,
  so piece (b)'s lock is never contended along the spec path.** THIS is the blocker.
- (b) DONE this loop (lock the default).
- (c) constraint re-validation after the wait sees s2's committed rows (perm 3).
(a)+(c) are the milestone-sized per-session MVCC catalog visibility work shared
with alter-table-4.

## M0118-0008 hard tail (remaining, all Effort-L)
- alter-table-4 + partition-concurrent-attach: per-session MVCC catalog visibility
  (THE next milestone — highest leverage, unlocks two specs). For
  partition-concurrent-attach the NEXT concrete piece is (a): defer the ATTACH's
  partition registration (RegisterPartitionChild + PartitionBounds) to COMMIT via a
  session PendingPartitionAttach, applied before TxnMgr.Commit on BOTH commit paths
  (mirror loop-#8 DROP INDEX deferral 0118-0074) + a global attach-epoch so older
  snapshots don't route to the new child.
- reindex-concurrently-toast: real TOAST relations (reltoastrelid=0).
- WHERE CURRENT OF positioned UPDATE/DELETE: parsed (CurrentOf), no executor site.

Next step: start (a) — deferred-until-commit ATTACH. Record a PendingPartitionAttach
on BasicSession when InExplicitTransaction (defer RegisterPartitionChild +
PartitionBounds assignment), apply at commit on both paths, discard on rollback via
EndExplicitTransaction; add the attach-epoch routing guard.

Gates run: go build ./... clean; new attach_default_lock_test.go (2) PASS; go test
./internal/executor/ PASS; TestPort_IsolationDetachPartitionConcurrently1 strict
PASS; make ralph-state-guard (before status block); pgbench smoke = pre-commit hook.
