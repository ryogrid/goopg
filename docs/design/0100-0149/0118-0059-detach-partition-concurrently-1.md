# 0118-0059 — `detach-partition-concurrently-1` PROMOTED: snapshot-relative partition visibility wiring (M0118-0008)

**Status:** accepted
**Type:** **Spec promotion** — `detach-partition-concurrently-1` now passes
byte-for-byte via `TestPort_IsolationDetachPartitionConcurrently1`
(`runIsoSpecStrict`, all 13 permutations).
**Builds on:** 0118-0048 (parser), 0118-0055/0056 (`pg_backend_pid` /
`pg_cancel_backend`), 0118-0057 (the `<waiting …>` wait), 0118-0058 (the
snapshot-visibility *foundation* primitives this loop wires).

## Problem

0118-0058 laid the primitives for a *snapshot-relative* view of a partition
being detached with `ALTER TABLE … DETACH PARTITION … CONCURRENTLY` but wired
none of them into a live path, so behaviour was unchanged: goopg
`UnregisterPartitionChild`'d the child synchronously and reused a stale
cross-session plan, so perm 1 returned 2 rows in *all* isolation levels where PG
returns 1 (READ COMMITTED) / 2 (REPEATABLE READ).

`detach-partition-concurrently-1` requires:

- **READ COMMITTED** — the partition disappears from a concurrent transaction's
  view *immediately* (each statement takes a fresh snapshot):
  `s1b s1s s2detach s1s s1c` → the second `s1s` returns **1 row**.
- **REPEATABLE READ** — the partition stays visible until the reader commits
  (its snapshot predates the detach):
  `s1brr s1s s2detach s1s s1c` → the second `s1s` still returns **2 rows**.
- The same epoch-vs-snapshot rule governs **INSERT routing**: an autocommit
  `INSERT INTO d_listp VALUES (2)` after the detach raises *no partition of
  relation found for row*, while a REPEATABLE-READ `EXECUTE f(2)` whose snapshot
  predates the detach still routes into the partition.
- `s3i` reads `relpartbound IS NULL`: **f** while the detach is mid-flight (the
  child is still attached) and **t** once finalize unsets the bound.

PostgreSQL implements this with two committed phases keyed on ordinary MVCC
catalog visibility of the `pg_inherits` tuple's xmin. goopg keeps a single,
non-MVCC shared catalog, so it orders the change with a **global epoch** instead
(0118-0058): a snapshot captures the current epoch, a detach bumps it and stamps
the child `DetachPendingEpoch`, and `VisiblePartitionChildren` drops a child
stamped `e` for any snapshot whose epoch `≥ e` while keeping it for `< e`.

## Change

Six wiring sites, all referencing the 0118-0058 primitives. The SELECT-expansion
and INSERT-routing filters are **sibling paths** and must agree or row counts
silently diverge.

1. **Detach executor** (`operators_ddl.go` `execAlterTable`
   `AlterTableDetachPartition`, `DetachConcurrently` branch). Replaces the
   synchronous unregister-then-wait (0118-0057) with the two-phase epoch
   protocol:
   - **Phase 1:** `epoch := mvcc.NextPartitionDetachEpoch()` then
     `im.MarkPartitionDetachPending(childTbl.OID, epoch)` — the child stays
     *registered* and keeps `relpartbound` set (so `s3i` reads
     `relpartbound IS NULL` = **f**). Every snapshot taken afterwards captures
     the higher epoch and omits the child; an older snapshot still includes it.
   - **Wait:** `TxnMgr.WaitForOlderSlotsToCommit(ctx, Tx.Handle)` — the DROP
     INDEX CONCURRENTLY drain. The wait is **snapshot-based, not lock-based**:
     upstream phase 1 commits then waits for every transaction whose snapshot
     predates the change, so a REPEATABLE READ session that has only `PREPARE`d a
     statement (holds no table lock yet but has an older snapshot) is still
     waited for. A lock-only wait (`waitForRelationLockers`, 0118-0057) would let
     the detacher finish too early for those permutations. The wait inherits
     57014 cancellation (`statement_timeout` / `lock_timeout` / peer
     `pg_cancel_backend`).
   - **Interrupt revert:** if the wait returns an error (timeout/cancel),
     `ClearPartitionDetachPending` reverts phase 1 so the partition stays
     attached (resuming an interrupted concurrent detach via `FINALIZE` is
     detach-3/4 work, deferred).
   - **Phase 2:** `UnregisterPartitionChild` + clear `PartitionParentOID` /
     `PartitionBounds` + `ClearPartitionDetachPending` (so `relpartbound` is now
     NULL ⇒ `s3i` flips f→t).
   - Plain `DETACH` / `DETACH FINALIZE` take the new `else` branch: synchronous
     single-phase unregister, unchanged.

2. **Snapshot-epoch threading** (`dispatch.go` `ctxPlanCatalog`,
   `catalog.SearchPathCatalog`). `ctxPlanCatalog` now stamps
   `wrapped.SnapshotPartitionDetachEpoch = ctx.Snap.PartitionDetachEpoch`;
   `SearchPathCatalog.CurrentPartitionDetachEpoch()` exposes it. The snapshot is
   established before `planner.Plan` runs (confirmed empirically — the spec's
   RC/RR row-count split only resolves if the planner sees the per-statement
   snapshot epoch), parallel to `TempOwnerToken` (0118-0036).

3. **SELECT expansion** (`planner.go`). `currentPartitionDetachEpoch(cat)` walks
   the catalog wrapper chain (peeling `Unwrap`, like `currentTempOwner`) to the
   carrier; `collectAllPartitionLeaves` takes a `detachEpoch` arg and filters
   `im.PartitionChildren(parentOID)` through `catalog.VisiblePartitionChildren`
   at **every** level of the BFS, so a concurrently-detached intermediate node
   (and all its leaves) vanishes from the scan.

4. **INSERT routing** (`operators_storage.go` `routeToPartitionDepth`). After
   resolving the target child, drops it (`child = nil` ⇒ *no partition found*)
   when `child.DetachPendingEpoch != 0 && ctx.Snap.PartitionDetachEpoch >=
   child.DetachPendingEpoch` — the routing twin of the SELECT-side
   `VisiblePartitionChildren` filter.

5. **Plan-cache bypass** (`dispatch.go` simple + `dispatch_extended.go` extended,
   sibling paths). New `partitionDetachPending(base)` walks the wrapper chain to
   `HasPendingPartitionDetach()`; the cross-session plan cache is bypassed while
   any detach is pending so each statement re-plans against its own snapshot
   epoch rather than reusing a plan baked at a different epoch. Mirrors the
   `sessionTempInheritanceActive` gate (0118-0037).

6. **`pg_node_tree` NULL framing** (`planner.go` `TypedVirtualCell`). A
   decompiled-expression catalog column (`relpartbound`, `pg_attrdef.adbin`,
   `pg_constraint.conbin`, `pg_index.indexprs`/`indpred`, …) stores SQL NULL when
   the expression is absent. An empty cell previously routed through the default
   `StringConst` branch yielding a non-NULL empty string, so `relpartbound IS
   NULL` read **false** even after finalize. The new `pg_node_tree` case returns
   `NullConst` for an empty value, so `s3i`'s `relpartbound IS NULL` correctly
   flips f→t.

## Sibling-path audit

- **SELECT expansion ↔ INSERT routing** — sites 3 and 4 both filter the visible
  partition set by the same `(DetachPendingEpoch, Snap.PartitionDetachEpoch)`
  rule. A row that the SELECT omits is exactly the row the INSERT refuses to
  route. Verified jointly by the spec (`s3` INSERT after detach raises *no
  partition found* in the same permutations the SELECT drops the partition).
- **dispatch.go simple ↔ dispatch_extended.go extended** — both gain the
  `partitionDetachPending` plan-cache bypass (site 5).

## Scope / deferrals

- **detach-2** likely falls out of this same machinery (probe next loop).
- **detach-3 / detach-4** additionally need the two-phase persisted
  `inhdetachpending` state, `pg_partition_tree`, `DETACH … FINALIZE`, and
  cancel-then-resume of an interrupted detach — recorded in the deferral ledger.
- The epoch is process-global (not persisted): a crash mid-detach leaves the
  child attached (phase 2 never ran), which is the safe outcome and matches a
  plain interrupted detach.

## Testing / gates

- `TestPort_IsolationDetachPartitionConcurrently1` strict PASS (13 permutations,
  byte-identical to PG 18.3).
- `internal/planner` + `internal/catalog` + `internal/executor` + `internal/mvcc`
  PASS; `go build ./...` + `go vet` clean.
- TPC-H Q12/Q13 spotcheck (planner/executor change) — canonical row counts, no
  silent regression on the partitioned-scan / routing hot path.
- pgbench TPC-B smoke = pre-commit hook.
