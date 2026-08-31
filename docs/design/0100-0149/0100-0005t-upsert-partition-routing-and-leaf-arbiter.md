# 0100-0005t — Partition-aware INSERT … ON CONFLICT + per-leaf arbiter inheritance

Status: accepted (2026-05-15)

## Context

`partition-key-update-2.spec` (and the `s2donothing` / `s3donothing` legs
of `partition-key-update-{3,4}.spec`) drive `INSERT … ON CONFLICT DO
NOTHING` against a partitioned table while a concurrent session moves
rows across partitions. Before this fix, all three legs diverged from
upstream in two related ways:

1. Conflicts on live duplicates were not detected. The runtime emitted
   no `<waiting ...>` line and silently dropped or silently double-
   inserted rows.
2. Subsequent `SELECT * FROM <parent>` returned the wrong row set:
   inserts went to the parent table's heap rather than the routed leaf
   partition, while the SELECT planner scanned only the children.

Root cause is three connected runtime gaps:

- `execCreatePartitionChild` (`internal/executor/operators_ddl.go`)
  never inherited the parent's PRIMARY KEY / UNIQUE B-tree indexes onto
  the child partition. Every partition child was index-free.
- The cross-partition UPDATE path in
  `updateOp.Next` (`internal/executor/operators_storage.go`) called
  `writeHeapRow` against the destination leaf but never maintained the
  destination's unique indexes. Even if the leaves had matching
  indexes, the moved tuple was invisible to them.
- `upsertOp` (`internal/executor/operators_upsert.go`) opened a single
  arbiter B-tree on `o.plan.OnConflict.ArbiterIndex` — always the
  parent's. For a partitioned target the parent's index has zero
  entries (`insertOp` already routes writes to leaves and maintains
  the *leaf's* indexes), so every probe missed every live duplicate.
  Worse, `applyInsert` then wrote to the parent's rel rather than the
  routed leaf.

## Decision

Make partition routing a first-class concern of the upsert + cross-
partition-UPDATE write paths, and inherit the parent's PRIMARY KEY /
UNIQUE B-tree indexes onto each partition child at child-creation
time. This mirrors upstream PG's "partitioned index → child index"
materialisation: each leaf carries the matching index, runtime probes
land on the leaf that actually owns the data, and maintenance and
enforcement become local to each partition.

## Implementation

Three independent edits, all under M0100-0005t:

### 1. `execCreatePartitionChild` — inherit parent indexes

After the child is created and registered in the partition tree, walk
`o.ctx.Catalog.IndexesOnTable(parent)`. For every B-tree index that is
`Primary` or `Unique`, synthesise a matching child index via the
existing `createBTreeIndex` helper. Names follow upstream auto-
generation: `<child>_pkey` for the primary case,
`<child>_<col>_key` (single-col) or `<child>_key` (multi-col) for the
unique case. `createBTreeIndex` handles WAL emission and catalog-heap
sync, so post-crash recovery reconstructs the child indexes too.

### 2. `updateOp` cross-partition write — maintain destination indexes

Hoist `destPart` (previously scoped inside the partition-routing
block) so it is visible after `writeHeapRow`. Swap the final
`writeHeapRow` for `writeHeapRowReturning` so the new ItemPointer is
in hand; immediately call `maintainUniqueIndexesForInsert(ctx,
destPart, destPart.Columns, newRow, newPtr)`. For non-partitioned
tables `destPart` stays `nil` and the maintenance call is skipped,
preserving prior semantics.

### 3. `upsertOp` — per-leaf arbiter resolution

New fields on `upsertOp`:

- `leafTrees map[uint32]*btree.BTree` caches open btree handles keyed
  by leaf OID so multi-row UPSERTs over the same partition reuse a
  single open tree.

New helpers:

- `routeAndOpenLeaf(inserted Row)` calls the existing
  `routeToPartition`, then `resolveLeafArbiter` to find the leaf
  index whose column list and primary/unique flag match the parent's
  planner-resolved arbiter index. Caches the open tree per leaf OID
  and returns `(leaf, leafTree, nil)`. A leaf with no matching index
  caches `nil` so probes short-circuit (no entries → no conflict);
  with the index-inheritance from edit (1) this branch is unreachable
  for PRIMARY-KEY arbiters.

`Next()` is now partition-aware:

- Save `parentTree := o.arbiterTree` for restoration on exit.
- For each child row, if `len(o.plan.Table.PartitionKey) > 0`, route
  to a leaf, swap `o.arbiterTree` to the leaf's tree, and use the
  leaf's rel + cols for `probeArbiterWaiting`, `applyInsert`, and
  `applyUpdate`.
- `encodeArbiterKey` is unchanged: it takes the parent table and the
  inserted row, indexes by `OnConflict.ArbiterColumns`, and encodes
  per-column via `tbl.Columns[ord]`. Because
  `execCreatePartitionChild` copies the parent's columns verbatim,
  the column ordinals are valid against the leaf and the byte
  encoding is identical to what `maintainUniqueIndexesForInsert`
  produced for the leaf's index — probes hit.

Routing failures (`leaf == nil`) raise `23514` ("no partition of
relation %q found for row") rather than silently writing to the
parent.

## Trade-offs and limitations

- ATTACH PARTITION can produce children whose column order differs
  from the parent. The current `resolveLeafArbiter` matches indexes
  by *column name* but the per-row `encodeArbiterKey` uses the parent
  table's column order. Until the row is also remapped to the leaf's
  column order (already implemented for `insertOp` via
  `remapRowForPartition` but not yet plumbed through `upsertOp`),
  ATTACH-with-reorder remains a known gap. Specs in the M0100-0005
  21-test target all use `PARTITION OF` which copies columns
  verbatim, so this gap does not block the milestone.
- Multi-column / non-PRIMARY unique indexes get an auto-generated
  name (`<child>_<col>_key` or `<child>_key`) that may collide with a
  user-defined index in pathological setups. The previous behaviour
  was "no inherited index at all", which is strictly worse; the
  collision case is logged via the `42P07` `createBTreeIndex` error
  path and bubbles up cleanly.
- `routeAndOpenLeaf` opens and caches a btree handle per leaf for the
  lifetime of the upsert. Multi-row UPSERTs covering N partitions
  hold N open handles; `Close()` already closes the parent's via the
  existing path. Leaf trees are not explicitly closed today (BTree
  open is a cheap pool-backed handle); a future pass can add a
  release loop in `Close` if profiling warrants it.

## Regression pins

- `TestPort_IsolationPartitionKeyUpdate2` — full PASS (was: SKIP
  with `<waiting ...>` missing and row count off-by-one).
- `TestUpsertPartitioned_RoutesToLeafAndProbesLeafArbiter`
  (`internal/server/upsert_partition_routing_test.go`) — three-step
  scenario: conflicting INSERT on existing key (skipped), routed
  INSERT into second partition (written), second duplicate INSERT
  on the second partition (skipped). Row count asserted at 2 after
  three INSERT statements.

Adjacent passes unchanged:
`TestPort_IsolationLockCommittedUpdate`,
`TestPort_IsolationInsertConflictDoUpdate`,
`TestPort_IsolationInsertConflictDoNothing`,
`TestPort_IsolationPartitionKeyUpdate1`.

`go test -race -count=1 ./internal/executor/ ./internal/storage/
./internal/server/ ./internal/mvcc/ ./internal/planner/
./internal/parser/ ./internal/analyzer/ ./internal/wal/
./internal/initdb/ ./internal/catalog/ ./internal/access/btree/`
PASS post-fix.
