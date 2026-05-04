# 0047-0001 — B-tree bulk load (sort-then-build)

**Status:** draft
**Date:** 2026-05-04
**Milestone:** 0047 — B-tree maturation
**Supersedes:** —

## Context

`CREATE INDEX` on a populated table currently calls `btree.Insert` once
per row. For pgbench-init's `pgbench_accounts` (1M rows, primary key on
`aid`) this takes ~31 s — almost all of it spent walking the tree from
root to leaf and splitting pages. Upstream's bulk-load path
(`nbtsort.c`) sorts the input keys, then writes leaf pages sequentially
and builds internal levels bottom-up, avoiding all descent / split
overhead. The same dataset takes < 2 s in upstream.

This doc specifies goopg's port. It is straight-line code on top of the
existing on-buffer-pool B-tree page format and the existing tuplesort
infrastructure used by ORDER BY.

## Plan

1. New file `internal/access/btree/bulk.go` with
   `BulkBuild(rel *catalog.Relation, source iter.Iterator,
   indexInfo *IndexInfo) error`.
2. **Sort phase.** Reuse the executor's `tuplesort` (or pull it down
   into `internal/util/sort`); sort by the index key in ascending order
   per the catalog opclass.
3. **Leaf-build phase.** Walk the sorted iterator. For each tuple,
   serialise the index entry (TID + key) and append to the current leaf
   buffer. When the leaf reaches `BTMaxItemSize` margin, allocate the
   next leaf, write a right-link from the previous leaf, and continue.
   Track `(highKey, blockNumber)` of each leaf for the next level.
4. **Internal-build phase.** Repeat the above with the high-keys-of-leaves
   list, producing the level-1 internal nodes; iterate until a single
   level produces one block — that becomes the root.
5. **Metapage write.** Initialise / overwrite the metapage to point at
   the new root with the right `level` count.
6. **WAL.** Emit one `RecordKindBtreeBulkBuild` (new) record per
   built page (carry the page contents as a full-page image — cheaper
   than logical insert per tuple). Ride on the existing M0002-0002 split
   WAL infrastructure for crash safety.
7. **Wire-in.** `executor/operators_ddl.go::CreateIndex` calls
   `BulkBuild` instead of looping over `Insert`. Old path stays available
   behind `enable_bulk_btree_build` (default on).

## Definition of Done

- `pgbench -i -s 10` index-build wall time ≤ 4 s (was ~31 s).
- Index built via bulk path is byte-identical (modulo LSNs) to one built
  via sequential `Insert` followed by an optimal pack.
- Crash mid-bulk-build leaves an inconsistent metapage but neither (a)
  the heap nor (b) the prior committed indexes are damaged. (The build
  is wrapped in a single transaction; on rollback the entire fork is
  truncated.)
- Existing `btree` test suite still green.

## Upstream reference

- `postgres/src/backend/access/nbtree/nbtsort.c` — sort + build entry
  points (`_bt_spool`, `_bt_leafbuild`, `_bt_buildadd`).
- `postgres/src/include/access/nbtxlog.h` —
  `XLOG_BTREE_NEWROOT` / `XLOG_BTREE_INSERT_LEAF` shapes (we collapse
  to a single new-page record).

## goopg references

- `internal/access/btree/` — current insert / split paths.
- `internal/executor/sort.go` — tuplesort.
- `docs/design/0002-0002-btree-concurrency.md` — split WAL infra reused.
