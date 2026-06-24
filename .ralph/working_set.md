Loop #12 (this run): M0118-0008 — `partition-concurrent-attach` **enabler 0118-0078**
(NOT a promotion). Landed piece (c): INSERT-time DEFAULT partition constraint
enforcement. COMMITTED + pushed. Spec stays `defer`.

## What landed (piece (c))
`insertOp.Next` now enforces a DEFAULT partition's implicit constraint at INSERT
time (PG `ExecPartitionCheck`). New `checkDefaultPartitionInsertConstraint(ctx,
leaf, leafCols, leafRow, pos)` in operators_storage.go walks `PartitionParentOID`
ancestry; for every ancestor that IS a default partition it re-routes the row's
parent partition-key value ONE level via `routePartitionKeyToImmediateChild`
(→ `FindRangePartitionForDatums`/`FindPartitionForValue`); a NON-default sibling
match ⇒ `23514 new row for relation "<default>" violates partition constraint`.
Wired at BOTH heap-write sites: routed-leaf path (on partTable/partRow) + the
non-partitioned path (on o.plan.Table, for a direct insert into a leaf default).
Reads the LIVE catalog AFTER the INSERT's RowExclusiveLock is granted (the
0118-0076/0077 wait), so it sees the just-committed sibling — PG's
route-pre-commit / check-post-commit. No-op in ordinary operation.
Helpers: `isDefaultPartitionChild`, `partitionKeyDatumToRangeStr/ListStr`.

Files: internal/executor/operators_storage.go (helpers ~after routeToPartitionDepth
+ 2 call sites in insertOp.Next), internal/executor/default_partition_constraint_test.go
(NEW: TestDefaultPartitionInsertConstraint + TestDefaultPartitionConstraintSubPartitioned),
docs/design/0118-0078 + README index; ledger.

## Spec state (probed) — partition-concurrent-attach (3 perms)
- perm 2 (`s2i2` direct INSERT INTO tpart_default): **NOW byte-for-byte PG** —
  waits, completes, ERRORs 23514, final 3 rows. ✓
- perm 1 (`s2i` INSERT INTO tpart, routes THROUGH default): does NOT yet wait —
  goopg locks only the routed leaf, not the intermediate tpart_default along the
  routing path, so the re-route runs before tpart_2 commits → row lands (6 rows).
- perm 3 (reverse: s2i first, then s1a attach): attach doesn't wait for s2's
  routed insert. Attach-side leaf re-scan ALREADY exists
  (`checkDefaultPartitionDataConflict` → 23P01 "updated partition constraint for
  default partition tpart_default_default would be violated by some row").

## Next step (next enabler): INSERT routing-path locks
Make INSERT routing take a `RowExclusiveLock` on EACH partition along the routing
path (not just the routed leaf) — esp. the intermediate DEFAULT partition. Then:
- perm 1: s2i's route-to-default takes RowExclusiveLock on tpart_default → blocks
  behind s1's AccessExclusiveLock (0118-0076) → after s1c the constraint re-route
  (already landed) sees tpart_2 → 23514. Likely fully fixes perm 1.
- perm 3: s2i's routed insert holds RowExclusiveLock on tpart_default → s1a's
  attach AccessExclusiveLock on the default contends → attach waits for s2c →
  re-scan (existing) finds the committed rows → 23P01. Likely fully fixes perm 3.
If perms 1+3 both go green, the spec PROMOTES. Routing-path lock site: insertOp
routing in operators_storage.go (~line 1335, routeToPartition); take the write
lock on each intermediate parent along the path, mirroring PG ExecInsert's
`ExecGetTriggerResultRel`/partition lock acquisition. Milestone-shared w/ alter-table-4.

## M0118-0008 hard tail (remaining, all Effort-L)
- partition-concurrent-attach: routing-path locks (above) → PROMOTE.
- alter-table-4: per-session MVCC catalog visibility.
- reindex-concurrently-toast: real TOAST relations (reltoastrelid=0).
- WHERE CURRENT OF positioned UPDATE/DELETE: parsed (CurrentOf), no executor site.

Gates run: go build ./... clean; new unit tests (2) PASS; go test ./internal/executor/
PASS; probe confirmed perm-2 byte-match; TestPort_IsolationDetachPartitionConcurrently1
+ PartitionDropIndexLocking + InheritTemp strict PASS (no regression);
make ralph-state-guard (before status block); pgbench smoke = pre-commit hook.
