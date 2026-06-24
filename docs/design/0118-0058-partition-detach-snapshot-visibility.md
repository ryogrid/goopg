# 0118-0058 — Partition-detach snapshot-visibility foundation (M0118-0008)

**Status:** accepted
**Type:** Foundation enabler — **NOT a spec promotion** (zero behavioural change).
**Spec target:** `detach-partition-concurrently-1` (and, later, `-2`, plus the
shared blocker under `alter-table-4` / `partition-concurrent-attach`).
**Builds on:** 0118-0048 (parser), 0118-0057 (the `<waiting …>` wait).

## Problem

`detach-partition-concurrently-1` requires a *snapshot-relative* view of a
partition that is being detached with `ALTER TABLE … DETACH PARTITION …
CONCURRENTLY`:

- **READ COMMITTED** — the partition disappears from a concurrent transaction's
  view *immediately* (each statement takes a fresh snapshot):
  `permutation s1b s1s s2detach s1s s1c s1s` → the second `s1s` returns **1 row**.
- **REPEATABLE READ** — the partition stays visible until the reader commits,
  because the reader's snapshot predates the detach:
  `permutation s1brr s1s s2detach s1s s1c` → the second `s1s` still returns
  **2 rows**.

The same epoch-vs-snapshot rule governs INSERT routing: an autocommit
`INSERT INTO d_listp VALUES (2)` after the detach raises *no partition of
relation "d_listp" found for row* (the partition is gone from the routing set),
while a REPEATABLE-READ `EXECUTE f(2)` whose snapshot predates the detach still
routes into it.

PostgreSQL implements this with a **two committed phases**: phase 1 sets
`pg_inherits.inhdetachpending = true` and commits, then waits; phase 2 removes
the row. After phase 1 commits, `find_inheritance_children_extended` omits the
partition for any snapshot taken afterwards while keeping it for older snapshots
— ordinary MVCC catalog visibility keyed on the `pg_inherits` tuple's xmin.

goopg keeps a **single, non-MVCC shared catalog** and currently
`UnregisterPartitionChild`s the child *synchronously* the instant `s2detach`
runs (operators_ddl.go), then waits (0118-0057). Two consequences:

1. The cross-session **plan cache** still holds `s1`'s `SELECT * FROM d_listp`
   Append over the original `{d_listp1, d_listp2}` set, so the repeated `s1s`
   returns 2 rows in *all* isolation levels (perm 1 diverges: 2 vs 1).
2. There is no snapshot dimension at all, so even with the cache fixed a
   READ-COMMITTED-correct synchronous unregister would make the REPEATABLE READ
   permutations *wrong* (they would also see 1 row). The two cases are coupled
   and cannot be solved by cache invalidation alone — they need a
   snapshot-relative partition descriptor.

## This loop — the foundation (behaviour-neutral)

Following the `inherit-temp` precedent (0118-0036 laid the central pieces; the
wiring landed in 0118-0037), this loop lands the **zero-blast-radius primitives**
that a snapshot-relative partition descriptor needs, with **nothing wired into a
live planner/executor/detach path** — so observable behaviour is byte-identical
and there is no row-count regression risk on the partitioned-table hot path.

### mvcc — a global detach epoch ordered against snapshots
`internal/mvcc/partition_detach_epoch.go`

- `partitionDetachEpoch atomic.Uint64` — process-global monotonic counter (0 =
  "no detach observed").
- `NextPartitionDetachEpoch() uint64` — advance + return; call once when a
  partition transitions to detach-pending.
- `CurrentPartitionDetachEpoch() uint64` — read without advancing.
- `Snapshot.PartitionDetachEpoch uint64` — captured from
  `CurrentPartitionDetachEpoch()` in `Manager.captureSnapshot` (and preserved in
  `Snapshot.Clone`). RR reuses the snapshot taken at BEGIN (epoch frozen *before*
  the detach); RC takes a fresh snapshot per statement (epoch advances *past* the
  detach). The field is currently written but **read by no visibility path**.

### catalog — the detach-pending mark + the filter chokepoint
`internal/catalog/catalog.go`

- `Table.DetachPendingEpoch uint64` — non-zero marks a child detach-pending; the
  child stays physically registered (so an older snapshot still scans it).
- `(*InMemory).MarkPartitionDetachPending(childOID, epoch) bool` /
  `ClearPartitionDetachPending(childOID)` — set/clear the mark; maintain an O(1)
  `pendingPartitionDetachCount`. Idempotent.
- `(*InMemory).HasPendingPartitionDetach() bool` — `count > 0`; the future
  plan-cache-bypass gate (the partition analog of `HasTempInheritanceChildren`).
- `VisiblePartitionChildren(children, snapshotEpoch) []*Table` — the chokepoint
  filter (analog of `AccessibleInheritanceChildren`): drops a child stamped
  `DetachPendingEpoch == e` when `snapshotEpoch >= e` (detach visible to this
  statement) and keeps it when `snapshotEpoch < e` (snapshot predates the detach)
  or `e == 0`. In-place fast path / nil-in nil-out.

### Tests
- `catalog`: `TestVisiblePartitionChildren` (RC drops at `>=`, RR keeps at `<`,
  epoch 0 keeps), `TestPartitionDetachPendingLifecycle` (Mark/Clear/Has count,
  re-stamp idempotence, unknown-OID no-op).
- `mvcc`: `TestPartitionDetachEpochMonotonic`,
  `TestCaptureSnapshotRecordsPartitionDetachEpoch` (capture + Clone preserve).

## Next loop — the wiring (the actual promotion of detach-1)

The wiring is an **atomic multi-site change** (sibling-paths discipline — both
the SELECT-scan and INSERT-routing partition enumerations must agree, or row
counts silently diverge). Steps:

1. **Detach executor** (`operators_ddl.go`, `AlterTableDetachPartition` +
   `DetachConcurrently`): replace the synchronous `UnregisterPartitionChild` /
   bounds-clear with `epoch := mvcc.NextPartitionDetachEpoch();
   im.MarkPartitionDetachPending(childOID, epoch)` **keeping the child
   registered**, then `waitForRelationLockers` (unchanged), then — *after* the
   wait — `UnregisterPartitionChild` + clear `PartitionBounds` +
   `ClearPartitionDetachPending` (phase-2 finalize). This makes `s3i`
   (`relpartbound IS NULL`) correctly read `f` during the wait and `t` after.
2. **Thread the snapshot epoch to the planner.** The planner only sees a static
   session token today (`SearchPathCatalog.TempOwnerToken`/`currentTempOwner`).
   Add a parallel `SearchPathCatalog.SnapshotPartitionDetachEpoch` set per
   statement in `sessionPlanCatalog`/`ctxPlanCatalog` from the session's
   **current snapshot** `ctx.Snap.PartitionDetachEpoch`. **Open question to
   confirm first:** is `ctx.Snap` established *before* `planner.Plan` runs for the
   statement? If planning precedes snapshot acquisition, the snapshot must be
   captured earlier (or the plan-cache bypass forces a re-plan after the snapshot
   is taken). This ordering check is the first task of the wiring loop.
3. **SELECT expansion** (`planner.go` `collectAllPartitionLeaves` /
   the `len(tbl.PartitionKey) > 0` branch ~L2139): wrap
   `im.PartitionChildren(parentOID)` in
   `catalog.VisiblePartitionChildren(children, epoch)`.
4. **INSERT routing** (`operators_storage.go` `routeToPartition` /
   `routeToPartitionDepth`): filter the candidate children through the same
   `VisiblePartitionChildren` using `ctx.Snap.PartitionDetachEpoch` so a routed
   row sees the same partition set as a scan (sibling paths).
5. **Plan-cache bypass** (`dispatch.go` L730 + `dispatch_extended.go` sibling):
   extend the gate to `… || partitionDetachActive(s.cfg.Catalog)` (walk-unwrap to
   `HasPendingPartitionDetach()`) so a partitioned-parent plan is neither served
   from nor stored in the cross-session cache while a detach is pending.

After wiring, verify with the throwaway-probe pattern
(`IsolationRunner.RunAndCompare` → `.Diff`) that all 13 `detach-1` permutations
byte-match PG 18.3, then promote `TestPort_IsolationDrop…` → strict and update
the D-002 CSV / coverage md. `detach-2` (the `s3i` relpartbound timing) likely
falls out of the same wiring; `detach-3`/`-4` additionally need the two-phase
`inhdetachpending` / `pg_partition_tree` / `DETACH FINALIZE` / cancel-then-resume
state (separate slice).

## Risk

Nil this loop: every new symbol is unreferenced by a live path; the only writes
are an unused `Snapshot` field and unused catalog state. `go build ./...`,
`go vet`, full `internal/catalog`, and `-race ./internal/mvcc/... ./internal/wal/...`
all pass; pgbench smoke via the pre-commit hook.

## Oracle

`postgres/src/backend/catalog/pg_inherits.c`
(`find_inheritance_children_extended`, the `detached_xmin` / `omit_detached`
snapshot test) and `postgres/src/backend/commands/tablecmds.c`
(`ATExecDetachPartition` → `DetachPartitionFinalize`).
