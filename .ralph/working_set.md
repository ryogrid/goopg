Loop #57: M0118-0008 — partition-detach snapshot-visibility FOUNDATION LANDED
(NOT a promotion; design 0118-0058).

Why a foundation, not the "step 1 plan-cache bypass" the prev working_set named:
analysis this loop showed goopg unregisters the partition child SYNCHRONOUSLY
(operators_ddl.go:5207) on a non-MVCC shared catalog, so the detach-1 RC view
("partition gone now") and RR view ("partition stays until commit") are COUPLED.
A plan-cache bypass alone fixes RC perms but REGRESSES the RR perms that pass
today by accident (stale cache → 2 rows). The real fix is a snapshot-relative
partition descriptor; this loop lays its zero-blast-radius primitives (mirrors
inherit-temp 0118-0036 → 0118-0037).

Landed (nothing wired to a live path → behaviour byte-identical):
- mvcc: global partitionDetachEpoch (Next/CurrentPartitionDetachEpoch) +
  Snapshot.PartitionDetachEpoch captured in captureSnapshot/Clone.
- catalog: Table.DetachPendingEpoch; InMemory.MarkPartitionDetachPending /
  ClearPartitionDetachPending (O(1) pendingPartitionDetachCount) /
  HasPendingPartitionDetach; VisiblePartitionChildren(children, snapshotEpoch)
  filter (drop child stamped e when snapshotEpoch>=e; keep when <e or e==0).

Files: internal/mvcc/partition_detach_epoch.go (NEW), internal/mvcc/{snapshot,
manager}.go, internal/mvcc/partition_detach_epoch_test.go (NEW),
internal/catalog/catalog.go, internal/catalog/partition_detach_visibility_test.go
(NEW), docs/design/0118-0058 + README index, deferral_ledger.

Gates: TestVisiblePartitionChildren/TestPartitionDetachPendingLifecycle +
TestPartitionDetachEpochMonotonic/TestCaptureSnapshotRecordsPartitionDetachEpoch
PASS; go build ./... + go vet clean; full internal/catalog PASS; -race
./internal/mvcc/... ./internal/wal/... PASS; pgbench smoke = pre-commit hook.

Next step (the actual detach-1 promotion — atomic multi-site WIRING, design
0118-0058 §"Next loop"):
0. FIRST confirm: is ctx.Snap established BEFORE planner.Plan runs for a
   statement? (decides the threading approach / whether re-plan is needed).
1. Detach executor (operators_ddl.go AlterTableDetachPartition + DetachConcurrently):
   mvcc.NextPartitionDetachEpoch() + MarkPartitionDetachPending KEEPING the child
   registered; wait; THEN UnregisterPartitionChild + clear bounds +
   ClearPartitionDetachPending (so s3i relpartbound = 'f' during wait, 't' after).
2. Thread ctx.Snap.PartitionDetachEpoch to planner via new
   SearchPathCatalog.SnapshotPartitionDetachEpoch (parallel to TempOwnerToken in
   sessionPlanCatalog/ctxPlanCatalog; read like currentTempOwner).
3. Filter BOTH SELECT expansion (planner.go collectAllPartitionLeaves ~L2139 /
   the len(tbl.PartitionKey)>0 branch) AND INSERT routing (operators_storage.go
   routeToPartition/routeToPartitionDepth) through VisiblePartitionChildren —
   sibling paths MUST agree or row counts silently diverge.
4. Plan-cache bypass: extend dispatch.go L730 gate (+ dispatch_extended sibling)
   with HasPendingPartitionDetach() (walk-unwrap like sessionTempInheritanceActive).
Then probe all 13 detach-1 perms byte-match PG 18.3 → promote
TestPort_IsolationDetachPartitionConcurrently1 strict + update D-002 CSV +
regen coverage/inventory. detach-2 likely falls out; detach-3/4 additionally
need two-phase inhdetachpending/pg_partition_tree/FINALIZE/cancel-then-resume.
