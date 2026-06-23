Loop #59: M0118-0008 — detach-partition-concurrently-2 PROMOTED (design 0118-0060)

detach-partition-concurrently-2 passes byte-for-byte across all 5 permutations
(TestPort_IsolationDetachPartitionConcurrently2, runIsoSpecStrict). Committed.

What landed (FK-safe concurrent detach):
- operators_fk.go: allDescendants now takes snapEpoch and prunes detach-pending
  leaves invisible to the snapshot (both FK existence-scan twins) ⇒ INSERT of a
  value living only in the detaching partition fails 23503.
- operators_fk.go: detachPartitionFKRefCheck + scanRefTableForDetachedPartitionMatch
  (RI_PartitionRemove_Check analog) — run BEFORE MarkPartitionDetachPending so
  routeToPartition resolves the child; errors "removing partition X violates
  foreign key constraint <fkname>_<N>" (N=child ordinal).
- HYBRID detach wait (operators_ddl.go): waitForRelationLockers(parent+leaves)
  for RC table-touchers + WaitForPinnedSnapshotsToCommit for RR/SSI pinned
  snapshots. New atomic procSlot.pinnedSnap marker (mvcc) set in SnapshotFor
  RR/SSI branch, cleared at Begin/txn-end. Replaces 0118-0059's
  WaitForOlderSlotsToCommit; a RC BEGIN-only session no longer blocks detacher.

Gates: detach-2 + detach-1 strict PASS; -race ./internal/mvcc PASS;
executor/catalog/planner/server PASS; gofmt clean; build clean; state-guard OK.

Next step: probe detach-partition-concurrently-3 (run throwaway probe with
RunAndCompare, log .Diff). detach-3/4 need persisted inhdetachpending state +
pg_partition_tree + DETACH … FINALIZE + cancel-then-resume (s1cancel) — larger
scope; first divergence will name the next bounded blocker.
