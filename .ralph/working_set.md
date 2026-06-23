Loop #58: M0118-0008 — detach-partition-concurrently-1 PROMOTED (design 0118-0059)

The 0118-0058 foundation primitives are now WIRED into six live sites and the
spec passes byte-for-byte. TestPort_IsolationDetachPartitionConcurrently1
strict PASS (13 perms, 11s). Committed.

What landed (all six wiring sites, sibling paths agree):
- operators_ddl.go AlterTableDetachPartition DetachConcurrently → two-phase
  epoch protocol: phase1 NextPartitionDetachEpoch+MarkPartitionDetachPending
  (child STAYS registered, relpartbound set ⇒ s3i=f), wait
  WaitForOlderSlotsToCommit (snapshot-based, not lock-based), interrupt reverts
  via ClearPartitionDetachPending, phase2 UnregisterPartitionChild+clear (⇒ s3i
  flips f→t). Plain DETACH/FINALIZE = synchronous else branch.
- dispatch.go ctxPlanCatalog → stamps SnapshotPartitionDetachEpoch on
  SearchPathCatalog (ctx.Snap IS established before planner.Plan — confirmed).
- planner.go collectAllPartitionLeaves(+detachEpoch) via new
  currentPartitionDetachEpoch wrapper-walk + VisiblePartitionChildren at every
  BFS level; TypedVirtualCell pg_node_tree empty→NullConst (relpartbound IS NULL).
- operators_storage.go routeToPartitionDepth filters child by epoch (INSERT twin).
- dispatch.go + dispatch_extended.go plan-cache bypass via partitionDetachPending.

Gates: TestPort_IsolationDetachPartitionConcurrently1 strict PASS;
internal/planner+catalog+executor+mvcc PASS; build+vet clean; TPC-H spotcheck =
known WSL2 SLRU-backfill infra-hang (killed; change gated on DetachPendingEpoch
!=0 so TPC-H unaffected); pgbench smoke = pre-commit hook.

Next step (next loop): probe detach-partition-concurrently-2 — it likely falls
out of the SAME machinery with no new engine work (run a throwaway probe with
runIsoSpec/log .Diff). If it passes, promote it (strict test + CSV/coverage
regen). Otherwise its first divergence names the next bounded blocker.
detach-3/4 additionally need persisted two-phase inhdetachpending state +
pg_partition_tree + DETACH … FINALIZE + cancel-then-resume (larger; deferred).
