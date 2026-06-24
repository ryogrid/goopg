# 0118-0061 — `detach-partition-concurrently-3` PROMOTED: incomplete-detach state, pg_partition_tree, FINALIZE & cross-session locking

**Milestone:** M0118-0008 (Upstream Isolation Spec Suite Pass-Through)
**Status:** accepted
**Spec:** `postgres/src/test/isolation/specs/detach-partition-concurrently-3.spec` (18 permutations)
**Test:** `TestPort_IsolationDetachPartitionConcurrently3` (`runIsoSpecStrict`)
**Oracle:** PostgreSQL 18.3 `src/backend/commands/tablecmds.c`
(`ATPrepCmd` incomplete-detach guard, `MarkInheritDetached`,
`DetachPartitionFinalize`), `src/backend/utils/adt/partitionfuncs.c`
(`pg_partition_tree`), `src/backend/catalog/partition.c`
(`get_partition_ancestors` stopping at the detached boundary).

## Problem

`detach-partition-concurrently-3` exercises every operation that can happen to a
partition left in an **incomplete-detach** state — a
`ALTER TABLE … DETACH PARTITION … CONCURRENTLY` that was **cancelled**
(`pg_cancel_backend`) while it waited for concurrent transactions. Upstream
commits the `inhdetachpending` mark in its own transaction *before* the wait, so
a cancel during the wait does **not** roll it back.

Before this loop goopg did the opposite: the interrupt path reverted phase 1
(`ClearPartitionDetachPending`), so the cancelled detach left the partition fully
attached — diverging at the very first `s1describe` step. The fix builds the
whole incomplete-detach lifecycle on top of the 0118-0058..0060 epoch
machinery.

## Changes

All changes are gated on `DetachPendingEpoch != 0` (the incomplete-detach
marker), so they are no-ops for every normal partition.

1. **Persist on cancel** (`operators_ddl.go`, concurrent-detach branch). The
   interrupt path (`werr != nil`) no longer clears the pending mark — the
   partition stays registered with `relpartbound` set and `DetachPendingEpoch`
   marked, omitted from every newer snapshot via `VisiblePartitionChildren`.

2. **`already pending detach` guard** (same branch, before
   `detachPartitionFKRefCheck`). A partitioned table may have at most one
   partition pending detach (`MarkInheritDetached`): scan the parent's children
   and, if any is already pending, raise
   `55000 partition "X" already pending detach in partitioned table "public.Y"`
   naming the already-pending child.

3. **ALTER-on-pending-detach guard** (`execAlterTable`, after the table is
   resolved). Mirrors `ATPrepCmd`: any `ALTER TABLE` on a partition with
   `DetachPendingEpoch != 0 && PartitionParentOID != 0` raises
   `55000 cannot alter partition "X" with an incomplete detach`. The only escape
   is `DETACH … FINALIZE`, which targets the *parent* (so its `tbl` is not the
   pending child).

4. **`pg_partition_tree` detach-awareness**
   (`operators_pg_partition_tree.go`, `partitionTableTree`). A detach-pending
   child is skipped when enumerating a parent's children, and when it is itself
   the queried root its parent is reported NULL — mirroring
   `find_all_inheritors` omitting `inhdetachpending` children and
   `get_partition_ancestors` stopping at the detached boundary.

5. **TRUNCATE omits the pending child** (`truncateTableAndPartitions`). A
   `TRUNCATE` of the parent no longer recurses into a detach-pending child
   (it is logically detached); a `DROP` of the parent still cascades to it (the
   pg_inherits dependency persists).

6. **FINALIZE completes & locks** (`operators_ddl.go`, plain-detach branch).
   `DETACH … FINALIZE` clears the pending mark in addition to unregistering, and
   acquires an `AccessExclusiveLock` on the partition via
   `acquireRelLockMaybeTransient` — held to commit inside an explicit block (so a
   later concurrent read/insert of the partition blocks until COMMIT) and
   transiently in autocommit (the wait still happens during acquisition). It
   does **not** lock the parent (only `ShareUpdateExclusive` is held there,
   compatible with a concurrent reader), so a session that scanned the parent
   *after* the partition went detach-pending — and thus never locked the
   partition — does not block FINALIZE.

7. **DROP of the pending child grabs the parent lock** (`dropTableByRef`).
   Dropping a partition with `DetachPendingEpoch != 0` also takes a txn-scoped
   `AccessExclusiveLock` on the parent (`acquireDDLLockTxn`), so a concurrent
   `SELECT` on the parent blocks until the dropping transaction commits.

8. **Partitioned-parent scan locks the parent** (`planner.SeqScan.LockParentOID`
   + `seqScanOp`). A leaf scan expanded from a partitioned parent now carries the
   parent OID and takes `AccessShare` on the parent too — PostgreSQL locks the
   whole hierarchy from the queried root — so the DROP's parent `AccessExclusive`
   (change 7) actually blocks the parent scan. A leaf scanned directly carries no
   parent OID and does not lock the parent.

9. **Autocommit writes block behind DDL** (`acquireWriteLockTxn`). Made
   symmetric with the read-side `acquireScanReadLockTxn`: instead of a hard
   no-op in autocommit it now routes through `acquireRelLockMaybeTransient`
   (`RowExclusiveLock`), so an autocommit `INSERT`/`UPDATE`/`DELETE` waits behind
   a conflicting `AccessExclusiveLock` (e.g. FINALIZE holding the partition in an
   explicit txn). `RowExclusive` is self-compatible and compatible with
   `AccessShare`/`RowShare`, so concurrent DML/reads never block at the table
   level — only DDL-grade modes do, matching PostgreSQL. (pgbench TPC-B already
   pays an equivalent transient acquire on the read side; the write side now
   matches.)

## Blast radius

Changes 1–7 are gated on `DetachPendingEpoch != 0` and affect only an
incomplete-detach partition. Change 8 adds an `AccessShare` lock on a
partitioned parent during a parent-routed scan (compatible with everything but
`AccessExclusive`, so no new blocking outside concurrent DDL). Change 9 adds a
transient `RowExclusive` acquire to autocommit writes (compatible with all DML
modes); verified by the pgbench TPC-B smoke (0 failed).

## Gates

- `TestPort_IsolationDetachPartitionConcurrently3` strict PASS (18 perms,
  byte-for-byte).
- Sibling `TestPort_IsolationDetachPartitionConcurrently1`/`…2`,
  `…CreateTrigger`, `…AlterTable3`, `…InheritTemp`, `…TruncateConflict` PASS
  (no regression from the write-lock / parent-scan-lock change).
- `-race ./internal/executor/… ./internal/lockmgr/… ./internal/mvcc/…`.
- `go build ./...` clean; pgbench TPC-B smoke 0-failed (pre-commit hook).

## Deferred

`detach-partition-concurrently-4` still needs cancel-then-resume of the
concurrent detach itself (resuming an interrupted `DETACH … CONCURRENTLY` and
re-driving the wait), beyond the incomplete-detach state this loop landed.
