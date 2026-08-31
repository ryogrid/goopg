# 0118-0060 — `detach-partition-concurrently-2`: FK safety + hybrid detach wait (M0118-0008)

**Status:** accepted
**Milestone:** M0118-0008 (upstream isolation spec suite — DDL/VACUUM/maintenance concurrency)
**Kind:** spec promotion (builds on 0118-0058 primitives and 0118-0059 wiring)

## Problem

`detach-partition-concurrently-2` asserts that `ALTER TABLE … DETACH PARTITION
… CONCURRENTLY` makes the partition **safe for a foreign key that references
the partitioned table**. With the 0118-0059 snapshot-visibility wiring in place,
probing the spec exposed three successive divergences:

1. A concurrent `INSERT` into the referencing table of a value that lives only
   in the detaching partition **did not** raise the expected FK violation — the
   FK existence scan still found the parent key in the (now snapshot-invisible)
   detaching partition.
2. When a referencing row was committed **before** the detach, the `DETACH`
   completed instead of failing with `removing partition … violates foreign key
   constraint`.
3. A `READ COMMITTED` session that only issued `BEGIN` (no table access)
   wrongly blocked the detacher: goopg's snapshot-based wait (0118-0059) waited
   for it because goopg's `BEGIN` itself takes a snapshot.

## Upstream behaviour

- The FK referencing-side check is `RI_FKey_check` (`ri_triggers.c`): an INSERT
  on the referencing table looks the key up in the referenced (partitioned)
  table; once the partition is detach-pending it is no longer part of the
  referenced set for snapshots after the detach, so the key is not found → 23503.
- `RI_PartitionRemove_Check` (`ri_triggers.c`, called from
  `ATDetachCheckNoForeignKeyRefs` → `tablecmds.c`): before finalising the detach,
  PG runs `SELECT fk.keycols FROM fk JOIN pk ON … WHERE <partition-constraint>`
  to find any referencing row that routes to the partition being removed; if one
  exists the detach fails with `removing partition "%s" violates foreign key
  constraint "%s"`, naming the per-partition cloned action-trigger constraint
  `<fkname>_<N>`.
- The detach wait is `WaitForLockersMultiple(parentrelid, AccessExclusiveLock)`
  (`tablecmds.c`) — purely **lock based**. PG holds table locks (including those
  acquired during planning, e.g. by `PREPARE`) until commit, so any transaction
  that has touched the parent — by reading it OR by planning a statement against
  it — is waited for; a transaction that only did `BEGIN` holds no such lock.

## Change

### 1. Snapshot-detach-aware FK existence scan
`allDescendants` (executor `operators_fk.go`) now takes a `snapEpoch` and prunes
any partition child stamped `DetachPendingEpoch != 0` with `snapEpoch >=
DetachPendingEpoch` (and its subtree), mirroring the planner's
`collectAllPartitionLeaves` / `catalog.VisiblePartitionChildren`. Both FK
existence-scan twins (`scanTableForMatch`, `scanTableForMatchFKWait`) pass
`snapDetachEpoch(ctx)` (= `ctx.Snap.PartitionDetachEpoch`). A detach-pending
leaf invisible to the inserter's snapshot no longer satisfies the FK, so the
INSERT fails 23503 — exactly as the SELECT-side expansion omits it.

### 2. `RI_PartitionRemove_Check` analog
`detachPartitionFKRefCheck` (+ `scanRefTableForDetachedPartitionMatch`) runs in
the concurrent-detach path **before** `MarkPartitionDetachPending` (so
`routeToPartition` still resolves the not-yet-pending child). It scans every
table whose FK references the parent, routes each non-null referencing key
through the parent's partition scheme, and fails 23503 if any row lands in the
detaching child. The error names `<fkConstraintName>_<N>`, where `N` is the
child's 1-based ordinal among the parent's partition children (PG's
`ChooseConstraintName` dedup suffix for the cloned constraint).

### 3. Hybrid detach wait
goopg cannot replicate PG's "locks held to commit" because its heavyweight locks
are statement-scoped and its `BEGIN` takes a snapshot — so neither a pure
lock-wait nor a pure snapshot-wait is correct alone. The detach now waits via a
**hybrid**:

- **`waitForRelationLockers`** on the parent and every partition leaf — catches
  `READ COMMITTED` sessions that touched the table (a scan holds a txn-scoped
  `AccessShare` on the leaves it read until commit, `acquireScanReadLockTxn`).
- **`WaitForPinnedSnapshotsToCommit`** — catches `REPEATABLE READ` / `SERIALIZABLE`
  sessions holding a snapshot pinned before the detach even when they hold no
  table lock (e.g. they only `PREPARE`d). A new atomic `procSlot.pinnedSnap`
  marker is set when an RR/SSI snapshot is pinned in `SnapshotFor` and cleared at
  `Begin`/txn-end; `SnapshotActiveOtherPinnedSlots` filters on it.

A `READ COMMITTED` session that merely issued `BEGIN` holds neither, so it does
not block the detacher (permutation 5). This replaces 0118-0059's
`WaitForOlderSlotsToCommit`; `detach-partition-concurrently-1` still passes
(its sessions always read the table or pin an RR snapshot before the detach).

## Sibling paths

- FK existence scan twins (`scanTableForMatch` / `scanTableForMatchFKWait`) both
  filter by snapshot epoch.
- INSERT routing (`routeToPartitionDepth`), SELECT expansion
  (`collectAllPartitionLeaves`) and now FK existence scan (`allDescendants`) all
  honour `VisiblePartitionChildren` semantics.
- `pinnedSnap` is set/cleared at every snapshot-pin / txn-init / txn-end site.

## Tests

- `TestPort_IsolationDetachPartitionConcurrently2` (`runIsoSpecStrict`) — all 5
  permutations byte-identical to PG 18.3.
- `TestPort_IsolationDetachPartitionConcurrently1` — unchanged, still passes
  under the hybrid wait.
- `go test -race ./internal/mvcc/…`, executor/catalog/planner suites green.

## Deferred

`detach-partition-concurrently-3/4` still need persisted `inhdetachpending`
state, `pg_partition_tree`, `DETACH … FINALIZE`, and cancel-then-resume.
