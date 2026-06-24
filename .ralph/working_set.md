Loop #11 (this run): M0118-0008 — `partition-concurrent-attach` **enabler 0118-0077**
(NOT a promotion). Landed piece (a): deferred-until-commit ATTACH visibility. COMMITTED.
Spec stays `defer`.

## What landed
`ALTER TABLE … ATTACH PARTITION` (non-default, inside an explicit txn) now DEFERS
its catalog registration (RegisterPartitionChild + PartitionBounds) to COMMIT, so
the uncommitted new partition is invisible to other sessions and a concurrent
INSERT routes to the DEFAULT partition (where it blocks on piece (b)'s
AccessExclusiveLock). Mirrors 0118-0074 DROP INDEX deferral, opposite direction
(keep invisible vs visible).

- session.go: PendingPartitionAttach{ParentOID,ChildOID,Bounds,SavepointDepth} +
  pendingPartAttaches slice + AddPendingPartitionAttach/TakePendingPartitionAttaches/
  CancelPendingPartitionAttachesToDepth; EndExplicitTransaction nils it.
- operators_ddl.go: AlterTableAttachPartition case computes boundsToSet, records a
  PendingPartitionAttach when InExplicitTransaction over *InMemory (else immediate);
  new ApplyPendingPartitionAttaches(ctx,sess) (LookupTableByOID → set bounds →
  RegisterPartitionChild).
- operators_tx.go: ApplyPendingPartitionAttaches in execCommit (beside
  ApplyPendingIndexDrops) + CancelPendingPartitionAttachesToDepth in rollbackToSavepoint.
- dispatch.go: ApplyPendingPartitionAttaches in TxCommit branch.
- attach_default_lock_test.go: +TestAttachPartitionDeferredUntilCommit /
  +TestAttachPartitionImmediateInAutocommit.
- design 0118-0077 + README index; ledger.

Key symbols: PendingPartitionAttach, ApplyPendingPartitionAttaches,
AlterTableAttachPartition case, CancelPendingPartitionAttachesToDepth.

## partition-concurrent-attach — remaining pieces (probed)
Probe after (a): permutation 2 (s2i2 = direct INSERT into tpart_default) NOW shows
`<waiting ...>`/`<... completed>` ✓. Remaining to PROMOTE:
- perm 1 (INSERT INTO tpart, routes THROUGH the default subtree): goopg locks only
  the routed leaf, not the intermediate tpart_default — so s2i does NOT wait. Needs
  INSERT routing to take RowExclusiveLock on EACH partition along the routing path.
- piece (c): post-wait constraint re-validation — perm 1/2 must ERROR "new row for
  relation tpart_default violates partition constraint"; perm 3 reverse: attach
  waits for s2's insert then re-scans the default leaf → "updated partition
  constraint for default partition tpart_default_default would be violated".
These are milestone-sized per-session MVCC-catalog + routing-lock work, shared with
alter-table-4.

## M0118-0008 hard tail (remaining, all Effort-L)
- alter-table-4 + partition-concurrent-attach: per-session MVCC catalog visibility +
  routing-path locks + post-wait constraint re-validation (THE next milestone).
- reindex-concurrently-toast: real TOAST relations (reltoastrelid=0).
- WHERE CURRENT OF positioned UPDATE/DELETE: parsed (CurrentOf), no executor site.

Next step: piece (c) — post-wait constraint re-validation. When a buffered INSERT
that waited on the default-partition lock unblocks (s1 committed), re-check the
default's now-narrowed partition constraint against the rows and raise 23514 if
violated. Then perm-1 routing-path RowExclusiveLocks.

Gates run: go build ./... clean; new attach tests (2) PASS; go test ./internal/executor/
+ ./internal/server/ PASS; TestPort_IsolationDetachPartitionConcurrently1 +
PartitionDropIndexLocking strict PASS (no regression); probe confirmed perm-2 wait;
make ralph-state-guard (before status block); pgbench smoke = pre-commit hook.
