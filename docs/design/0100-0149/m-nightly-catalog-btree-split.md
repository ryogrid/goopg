# M\-NIGHTLY — catalog btrees split a full page in place instead of rebuilding

**Task:** `M-NIGHTLY-catalog-btree-split` (root cause of AI\-20261103\-004134\-002,
`TestE2E_PGReadsThreeLevelCatalogBtree`) · **Status:** landed 2026\-11\-03

## Problem

Every catalog DDL inserts entries into the system catalog btrees
(`insertCanonicalSysBtreeLeaf`). On a multi\-level tree the insert descended to
the target leaf and, when the leaf was full, fell back to
`rebuildSysBtreeWithNewEntry`. That function re\-collects every tuple, runs the
bulk layout, and rewrites every page with a full\-page image.

- The bulk layout packs every non\-rightmost leaf 100% full, so after a rebuild
  the next insert into any middle leaf rebuilt the whole index again. An
  example is the table's `_name` array type in `pg_type_typname_nsp_index`.
- Catalog DDL cost therefore grew with the catalog size. A bulk `CREATE TABLE`
  run was quadratic: 4000 tables took 67–130 s with fsync off, and the
  nightly test's 100\-table batches passed its 60 s deadline near 8000
  relations.
- A CPU profile at 5000 relations showed `mirrorCatalogRelToPostgresDB` at 40%
  (copying and FPI\-logging the rewritten pages into base/5) and `pwrite` at
  37%.

## PostgreSQL

PG never rebuilds an index on insert.

- `_bt_doinsert` → `_bt_split` (nbtinsert.c) splits the full page into itself
  plus a new right sibling.
- `_bt_insert_parent` posts the right page's downlink to the parent, splitting
  upward as needed, and `_bt_newroot` grows a new root when the root splits.
- `_bt_findsplitloc` (nbtsplitloc.c, default strategy) minimises the
  free\-space difference between the halves. On the rightmost page of a level
  it weighs the left page's free space by the fillfactor (`usemult`):
  `BTREE_DEFAULT_FILLFACTOR` 90 for leaves, `BTREE_NONLEAF_FILLFACTOR` 70 for
  internal pages.

## Design

`internal/executor/sys_catalog_btree_insert_split.go`:

- `insertSysBtreeMultiLevel` descends while recording the internal path
  (`descendSysBtreeWithPath`, `_bt_search`'s stack). It tries the in\-place
  leaf insert, and on `ErrNoSpaceInPage` splits the leaf.
- `splitSysBtreePage` handles leaf and internal pages.
  - The left page keeps its block and `btpo_prev`, and takes the first right
    item as its high key.
  - The right page, a new block appended to the relation, takes the old high
    key and `btpo_next`. The old right sibling's `btpo_prev` is updated.
  - On an internal split the right page's first item becomes a
    minus\-infinity downlink; its key moves up.
- `insertSysBtreeDownlink` places the downlink right after the left page's
  own downlink. A full parent splits recursively, and a split root produces
  a new `BTP_ROOT` page plus a metapage update (`btm_root`, `btm_level`).
- `sysBtreeFindSplitLoc` implements the default split\-location strategy
  described above.
- Write order is nbtree's: the new right page, the left page, the old right
  sibling, the parent level, and the metapage last. Every write is a
  full\-page image, as for the other catalog btree writes.

Catalog btree writes are single\-writer at this layer, the same assumption
the in\-place insert and the rebuild made. No lock coupling or
`BTP_INCOMPLETE_SPLIT` is modelled.

## Verification

- `TestSysBtreeSplitsInPlace`: 12000 random\-order inserts into a 2663 tree
  seeded with 300 entries, on a 16\-slot pool.
  - Expected: no rebuild, the tree grows to level ≥ 2, the structural walk
    holds, all tuples are present and sorted, and every key is reachable by
    descent.
  - Checked by mutation: with the rebuild restored, it fails (127 rebuilds).
- `TestSysBtreeAscendingSplitsLeaveFillfactorPages`: under ascending
  inserts, the split\-off leaves hold 85–95% of capacity. With the rebuild
  they were 99%.
- Probe `tmp/ddl-probe.sh` (fsync off): 4000 tables in 11.2 s against
  67–130 s; 6000 tables in 24.6 s.
- `TestE2E_PGReadsThreeLevelCatalogBtree` passes in 45.5 s; it failed at
  643 s on HEAD. Real PG reads the split tree.
- All 34 `TestE2E_` tests pass. `TestPort_RegressSuite` diverges only in
  `create_function_sql`, which fails identically at HEAD (0001h cause 3).

## Remaining (deferral ledger 2026\-11\-03)

- The bulk layouts (`buildBulkSysBtreeLayout` / `Variable` and initdb's
  `pgBuildBtreeBulkLoadSized`) still pack leaves 100%; PG's nbtsort uses
  fillfactor 90. `rebuildSysBtreeWithNewEntry` is no longer reached from the
  insert path.
- `mirrorCatalogRelToPostgresDB` still walks every block of every mirrored
  catalog per DDL, so creation time still grows slowly with catalog size.
- The single\-leaf\-root split (`splitLeafRootAndInsert`) still splits 50/50.
