# 0118-0068 — CREATE INDEX recurses the partition tree (M0118-0008 `partition-drop-index-locking` enabler)

Status: accepted
Date: 2026-06-24
Milestone: M0118-0008 (DDL / VACUUM / maintenance concurrency isolation specs)
Spec: `postgres/src/test/isolation/specs/partition-drop-index-locking.spec`

## Summary

**Enabler, NOT a promotion.** Make `CREATE INDEX` on a partitioned table fan out
into every existing partition descendant, building a matching child index on
each and attaching it (via `pg_inherits`) to its *immediate* parent index — as
PostgreSQL's `DefineIndex` does. Before this change goopg's `execCreateIndex`
built exactly one index on the named relation and ignored its partitions, so the
child indexes the spec observes never existed.

The child indexes are auto-named `<partition>_<col>_idx`, deduped to
`_idx1`/`_idx2`/… when two ancestor indexes both reach the same leaf. That dedup
is exactly why the `partition-drop-index-locking` spec's `s3getlocks` output
references both `part_drop_index_locking_subpart_child_id_idx` (inherited from
the *grandparent* index `part_drop_index_locking_idx`) and
`part_drop_index_locking_subpart_child_id_idx1` (inherited from the *parent*
sub-partition index `part_drop_index_locking_subpart_idx`).

## Spec mechanics

The spec creates the partition tree first and the indexes afterward:

```
CREATE TABLE part_drop_index_locking (id int) PARTITION BY RANGE(id);
CREATE TABLE part_drop_index_locking_subpart PARTITION OF … PARTITION BY RANGE(id);
CREATE TABLE part_drop_index_locking_subpart_child PARTITION OF …_subpart FOR VALUES …;
CREATE INDEX part_drop_index_locking_idx         ON part_drop_index_locking(id);          -- top parent
CREATE INDEX part_drop_index_locking_subpart_idx ON part_drop_index_locking_subpart(id);  -- sub-partition
```

Because the partitions already exist when each `CREATE INDEX` runs, the
attach-time inheritance path (`CREATE TABLE child PARTITION OF parent`, which
clones *existing* parent indexes onto a *new* child) never fires here — the index
tree must be built by `CREATE INDEX` recursing **down** into existing partitions.

Tree: `part_drop_index_locking` (range) → `…_subpart` (range) → `…_subpart_child`
(leaf). The two `CREATE INDEX` statements therefore produce:

| CREATE INDEX on | child index built | on |
|---|---|---|
| `part_drop_index_locking` | `…_subpart_id_idx` | `…_subpart` (intermediate) |
| `part_drop_index_locking` | `…_subpart_child_id_idx` | `…_subpart_child` (leaf) |
| `…_subpart` | `…_subpart_child_id_idx1` | `…_subpart_child` (leaf, deduped) |

## Implementation

`internal/executor/operators_ddl.go`:

- `execCreateIndex`: after the parent index is created and its metadata stored
  (and *before* the `CONCURRENTLY` drain), when the target is partitioned
  (`tbl.PartitionMethod != ""`) call the new `createPartitionChildIndexes`,
  rooted at the just-created parent index.
- `createPartitionChildIndexes(s, parentTbl, parentIdx, resolvedPred)`: for each
  `im.PartitionChildren(parentTbl.OID)`, auto-name the child via the existing
  `autoIndexNameWithIncludes` (which already dedups against live index names →
  `_idx`/`_idx1`), build it with `createBTreeIndex` (carrying the same key/expr
  columns, `UNIQUE`, and const-folded partial predicate), then:
  - set `childIdx.PartitionParentOID = parentIdx.OID` and
    `im.RegisterIndexPartitionChild(parentIdx.OID, childIdx.OID)` — the same
    marking `ALTER INDEX … ATTACH PARTITION` uses, so `pg_inherits` renders the
    child as inheriting from its parent index and an external `pg_dump` recreates
    it implicitly (no standalone `CREATE INDEX`);
  - copy the parent's partial-predicate / `INCLUDE` / expression-string metadata
    so the child's `pg_get_indexdef` matches;
  - recurse when the child is itself partitioned (`PartitionMethod != ""`),
    rooting each grandchild at *this* level's index.

Expression columns map their empty key name to `"expr"` for naming, matching the
attach-time inheritance path (so two expression indexes on one child dedup to
`_expr_idx` / `_expr_idx1`).

## Blast radius

Confined to `CREATE INDEX` whose target relation is partitioned — previously this
created a single childless index, an incomplete state no correct query/INSERT
path relied on. Leaf indexes are ordinary `btree` indexes; intermediate
partitioned partitions get an index entry plus an (empty) build over their own
zero-row heap. The child indexes are marked `PartitionParentOID != 0`, so the
virtual `pg_inherits` rows already consumed by `pg_dump` keep them out of
standalone dump output.

## Does NOT promote `partition-drop-index-locking`

Two blockers remain (tracked in the deferral ledger):

1. **`pg_locks` → real `tableLockMgr` bridge.** `s3getlocks` joins `pg_locks`
   against `pg_class` (`l.relation`) and `pg_stat_activity` (`l.pid`). goopg's
   `pg_locks` still surfaces only the explicit `LOCK TABLE` registry
   (`globalRelLockMgr`, with `pid`/`granted` hardcoded), not the real
   `tableLockMgr` holders/waiters, and the `SELECT`'s implicit `ACCESS SHARE`
   locks on a table's indexes are not surfaced at all. Closing this needs a
   `LockManager.AllLocks()` enumerator wired into `pg_locks` with per-backend
   `granted` (t for holders / f for waiters) and a `BackendID → pid` map for the
   `pg_stat_activity` join — a broadly reusable change that touches the
   currently-passing `pg_locks`-observing specs, so it is its own loop.
2. **`DROP INDEX` cascade to child indexes.** The second `s3getlocks` shows the
   completed `DROP INDEX` holding `AccessExclusiveLock` on every child index it
   removed; goopg's `DROP INDEX` (0118-0067) locks the partition *table* tree but
   does not yet cascade through `IndexPartitionChildren` to drop/lock the child
   indexes this enabler creates.

This loop lands the catalog half (the child indexes exist with PG-faithful
names and inheritance); the spec stays `defer`.

## Tests / gates

- New `TestCreateIndexRecursesPartitionTree` (`internal/executor`): builds the
  spec's two-level tree, runs both `CREATE INDEX` statements, asserts the three
  child indexes exist with the expected names + `PartitionParentOID` linkage and
  that the top index registers its direct child.
- `go test ./internal/executor/ ./internal/catalog/` PASS; `go build ./...`
  clean; pgbench smoke = pre-commit hook.

## Oracle

`postgres/src/backend/commands/indexcmds.c` `DefineIndex` (the
`partitioned`/recurse branch that builds a child index per partition and calls
`IndexSetParentIndex`).
