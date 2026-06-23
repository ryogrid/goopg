Loop #60: M0118-0008 — detach-partition-concurrently-3 PROMOTED (design 0118-0061)

detach-partition-concurrently-3 passes byte-for-byte across all 18 permutations
(TestPort_IsolationDetachPartitionConcurrently3, runIsoSpecStrict). Committed.

What landed (incomplete-detach lifecycle — built on detach-1/2 epoch machinery):
- operators_ddl.go: cancel mid-wait now PERSISTS the detach-pending mark (no
  revert); `already pending detach` guard (55000) before a 2nd concurrent detach;
  ALTER-on-pending guard (55000) in execAlterTable; DETACH FINALIZE clears the
  mark + takes AccessExclusive on the partition via acquireRelLockMaybeTransient;
  DROP of a pending child grabs AccessExclusive on the PARENT (acquireDDLLockTxn).
- operators_pg_partition_tree.go: partitionTableTree skips a detach-pending child
  from the parent, NULL-parent when it's the queried root.
- truncateTableAndPartitions: omits a detach-pending child.
- planner SeqScan.LockParentOID + seqScanOp: a partitioned-parent scan locks the
  parent relation (AccessShare) so a concurrent parent AEL (from DROP) blocks it.
- context.go acquireWriteLockTxn: made SYMMETRIC with acquireScanReadLockTxn —
  routes through acquireRelLockMaybeTransient(RowExclusive) so an autocommit write
  WAITS behind a conflicting AEL (FINALIZE). RowExclusive only conflicts with
  DDL-grade modes ⇒ no new DML/read blocking; pgbench smoke 0-failed.

Gates: detach-3 strict PASS; detach-1/2 + create-trigger/alter-table-1/2/3/
inherit-temp/truncate-conflict/vacuum-conflict/cluster-conflict/timeouts/
row-lock/write-skew/merge/FK siblings PASS; -race executor+lockmgr+mvcc PASS;
executor/planner/catalog units PASS; build clean; state-guard OK.

Next step: probe detach-partition-concurrently-4 (RunAndCompare, log .Diff).
detach-4 adds cancel-then-resume of the concurrent detach itself (re-driving the
wait after an interrupted DETACH … CONCURRENTLY) on top of the now-landed
incomplete-detach state. Other M0118-0008 tail: alter-table-4 (INHERITS +
transactional-DDL visibility), partition-concurrent-attach, partition-drop-index-
locking (pg_locks view), reindex-concurrently-toast (toast as catalog objects).
