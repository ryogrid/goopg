# M0146-0178 — B-tree rightmost-page splits at the fillfactor

Status: open — slice 1 (split rule) landed 2026-10-13. Slice 2 (the planner
reads real index sizes) waits for the TPC-DS clusters to be reloaded. The
heap-fetch term is the RELPAGES owner residual.

## Problem

TPC-DS Q24 (SF0.25) elects a Parallel Index Only Scan on
`store_returns_pkey` where PG keeps a Parallel Seq Scan. PG's index-only scans
are dearer by heap fetches (store\_returns +212), and by more index pages on
the full scans of `item_pkey` / `customer_pkey`.

Three mechanisms are involved (evidence: `analysis/m0146/m0146-0178/`):

1. **Heap fetches.**
   - PG's heaps keep trailing pages from its COPY bulk extension (hio.c
     `RelationAddBlocks`). Those pages are not all-visible, so `allvisfrac` is
     below 1 and an index-only scan pays heap fetches (`cost_index`).
   - goopg's heaps have no such pages. This is the RELPAGES owner residual.
2. **Index pages.**
   - PG reads an index's real file size (`get_relation_info`,
     `RelationGetNumberOfBlocks`).
   - goopg's `catalog.IndexRealPages` resolves a `DBOid`-0 index under
     `DefaultDBOid` (`base/1`), while the file sits in the session database
     (`base/5`).
   - The lookup fails and the planner uses a width estimate: 40 pages for
     `item_pkey`, against 90 real pages and PG's 52.
3. **Index density.**
   - goopg's ascending-key indexes are about 1.75x PG's size; random-order
     keys are about the same.
   - Both engines load the same way: PRIMARY KEY first, then COPY.
   - `_bt_findsplitloc` (nbtsplitloc.c) splits a rightmost page so the left
     half keeps the fillfactor: the leaf fillfactor (90) on the leaf level,
     `BTREE_NONLEAF_FILLFACTOR` (70) above it. Ascending insertions only ever
     land on the right half again.
   - goopg split every page for the most even halves, so every left page
     stayed half empty.

## Slice 1 — the split rule

- `splitFillFactorMult(leaf, rightmost)` (internal/access/nbtree/btree.go)
  gives `_bt_findsplitloc`'s `fillfactormult` for the `usemult` strategies:
  - 0.90 on a rightmost leaf;
  - 0.70 on a rightmost internal page;
  - 0 (balance free space) elsewhere.
- `compactSplitLocFill` scores each legal cut as `_bt_deltasortsplits` does:
  - with `usemult`: `|ff·leftfree − (1−ff)·rightfree|`;
  - otherwise the existing even split.
  - Free space is each half's budget less its compact footprint, with the left
    half also paying for the separator (`_bt_recsplitloc`).
- `insertIntoBlock` passes the page's level and whether it has a right
  sibling (`btpo_next`, PG's `P_RIGHTMOST`).
- **Oracle:** PG 18.3 page counts after ascending inserts, measured on a
  scratch cluster.

  | keys | PG | goopg before | goopg after |
  |---|---|---|---|
  | int4 ×18000 | 52 | 90 | 52 |
  | int4 ×100000 | 276 | 494 | 276 |
  | int4 ×500 | 4 | 4 | 4 |
  | (int, int) ×50000 | 138 | 247 | 139 |
  | bigint ×18000 | 52 | 90 | 52 |

- **Tests:**
  - `TestAscendingInsertsSplitRightmostAtFillfactor` fails on the old rule
    (90 pages).
  - `TestSplitFillFactorMult`.
- **Not ported (ledgered):**
  - the per-index `fillfactor` reloption: goopg uses 90 as the bulk loader
    does, so a `fillfactor = 70` index builds 52 pages where PG builds 65;
  - `_bt_afternewitemoff`'s localized-monotonic arm for non-rightmost leaves;
  - the split-interval / suffix-truncation penalty choice (`_bt_bestsplitloc`);
  - the bulk loader's non-leaf reserve (`BTREE_NONLEAF_FILLFACTOR` in
    `_bt_pagestate`).

## Order of the remaining slices

1. **Owner:** reload the goopg TPC-DS clusters (SF0.25 and SF1), so their
   indexes are rebuilt by inserts under the new split rule.
2. **Slice 2:** resolve `IndexRealPages` through the catalog's database, as
   `InMemory.RelAllVisibleBlocks` does, and thread the catalog
   (`searchCtx.cat`) into `estimateIndexGeometry`. Done before the reload, the
   planner would read today's half-empty files (90 pages against PG's 52) and
   move plans away from PG.
3. **Heap trailing pages:** the RELPAGES owner residual (bulk extension).
