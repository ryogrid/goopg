# M0106-0010 follow-up — system-catalog btrees of any height

Status: landed 2026-09-29 (M-NIGHTLY, AI-20260928-004845-004 /
AI-20260929-003700-004). Code: `internal/executor/sys_catalog_btree_levels.go`,
`sys_catalog_btree_multilevel.go`.

## Defect

goopg keeps PG18-canonical on-disk btrees for the system catalogs a user DDL
touches (`pg_class_relname_nsp_index` 2663, `pg_proc_proname_args_nsp_index`
2691, …) so that real PostgreSQL can read the directory (standby / cold
start). A leaf that overflows triggers a full rebuild
(`rebuildSysBtreeWithNewEntry`). The rebuild's bulk layout
(`buildBulkSysBtreeLayout` and its variable-size twin) supported ONE internal
root only. 2663's 80-byte downlinks fit ~97 per page, so once pg_class needed
more than ~97 leaves (about 9,300 relations), every CREATE failed:

    ERROR: DDL catalog sync: pg_class_relname_nsp_index: rebuild sys btree
    2663: rebuild: bulk-build: bulk layout: internal root: internal-root
    overflow inserting downlink 97

The rest of the session saw "relation does not exist". The nightly
`TestPort_RegressSuite` hit it in `varchar` and `vacuum_parallel`, and only in
the full suite. Bisect points at M0146-0030 (`0994872a3`), but that commit only
stopped `join`/`arrays` from aborting early, so the suite created enough
relations to cross the limit. The limit predates it.

## Fix

`layoutSysBtreeInternalLevels` builds internal levels bottom-up until a single
page holds them all, as PG's `_bt_uppershutdown` / `_bt_buildadd` do
(nbtsort.c), in PG's page format:

- every internal page's first downlink is minus infinity (key truncated);
- a non-rightmost page carries a high key at P_HIKEY: the low key of its
  right sibling, as a pivot tuple;
- sibling links chain each level, `btpo_level` is the level, and only the top
  page is `BTP_ROOT`. The metapage gets the root block and level.

A two-level tree is byte-identical to the old single-root layout (root at
`nLeaves+1`). Both readers (`descendSysBtreeToLeaf`, `collectAllLeafTuples`)
used to read slot 1 as the minus-infinity downlink. That holds only on a
rightmost page, so they now start at `firstDataSlot` (2 on a page with a
right sibling).

## Verification

- `TestBulkLayoutBuildsThreeLevels`: 8,000 variable tuples build a tree of
  level ≥ 2; every tuple is collected in order and reachable by descent.
- `TestE2E_PGReadsThreeLevelCatalogBtree`: 9,600 CREATEs on goopg take 2663 to
  level ≥ 2. Real PG 18.3 cold-starts on the directory and resolves tables by
  name through RELNAMENSP and through a forced index scan on pg_class.
- `TestPort_RegressSuite`: full suite passes (it failed on `varchar` and
  `vacuum_parallel`).

## Remaining (ledgered)

- An overflowing leaf still rebuilds the whole index (O(n) per overflow);
  PG splits the page (`_bt_split`) and inserts one downlink.
- initdb's bootstrap twin (`pgBuildBtreeBulkLoadSized`) keeps the two-level
  limit. Its inputs are the fixed bootstrap catalogs, far below the limit.
