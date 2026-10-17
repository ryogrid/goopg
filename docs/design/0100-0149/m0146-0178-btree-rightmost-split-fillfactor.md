# M0146-0178 — B-tree rightmost-page splits at the fillfactor

Status: open — slice 1 (split rule) landed 2026-10-13. Slice 2 (the planner
reads real index sizes) was parked as a patch on 2026-10-15 and lands after the
owner's XFAIL pin unblocked the acceptance arm (OWNER DECISIONS 2026-10-15 (c)).
The heap-fetch term is the RELPAGES owner residual.

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
   indexes are rebuilt by inserts under the new split rule. Done 2026-10-15.
2. **Slice 2:** resolve `IndexRealPages` through the catalog's database, as
   `InMemory.RelAllVisibleBlocks` does, and thread the catalog
   (`searchCtx.cat`) into `estimateIndexGeometry`. Done before the reload, the
   planner would read today's half-empty files (90 pages against PG's 52) and
   move plans away from PG.
3. **Heap trailing pages:** the RELPAGES owner residual (bulk extension).

## Slice 2 — index size read in the catalog's database (2026-10-15)

**Cause.** `get_relation_info` sizes the index relation it opened with
`RelationGetNumberOfBlocks` (`./postgres/src/backend/optimizer/util/plancat.c:466`),
and that relation is always in the current database. goopg's package-level
`catalog.IndexRealPages` read the file at `(DBOid, OID)`, with `idx.DBOid` taken
literally. An index created in the `postgres` database carries the namespace key
`DefaultDBOid` (1), while its file sits under the catalog's storage database:
`base/5` on a server (`NamespaceDBOid`, `InMemory.IndexRelFileNode`). The lookup
found no file under `base/1`, so `estimateIndexGeometry` fell back to its
width-derived guess. Example: TPC-DS `item_pkey` was priced at 40 pages; the real
file has 52 pages, and so does PG's. This is the mismatch that M0145-0029 slice 5
fixed for `relallvisible`.

**Change** (`analysis/m0146/m0146-0178/slice2/slice2.patch`):

- `catalog.IndexRealPagesIn(cat, idx)` resolves the file through
  `cat.IndexRelFileNode`. A nil catalog falls back to the old lookup.
- The pg_class view maps its namespace key back to the storage database the
  same way, so `relpages` for an index reads the real file.
- The catalog is threaded into `estimateIndexGeometry`, `indexPagesForPath` and
  `seqWinsEqualityProbe`. Every caller already had it: `searchCtx.cat` or a
  `cat` parameter.

**Tests.**

- `TestIndexRealPagesInUsesCatalogDatabase` (catalog, including the pg_class
  cell).
- `TestEstimateIndexGeometryReadsFileInCatalogDatabase` (optimizer). It also
  asserts that the nil-catalog lookup still misses, so the case keeps
  reproducing.

**Measured on the reloaded SF0.25 corpus.** Both captures were taken through
`scripts/jointree-parity-capture.sh`, with a clean-HEAD binary `d7c05acde` and
the patched binary:

| measure | HEAD | patched |
|---|---|---|
| match | 80 | 80 |
| `CATEGORIES-EXCL-MATCH` | — | identical, except `parallelism` 10 → 9 (Q77) |
| changed index-scan nodes vs PG | — | 45 closer, 4 farther |
| plan totals vs PG | — | 45 closer, 36 farther |
| sum of abs(log(goopg / PG)) | 11.940 | 11.899 |

- Many index-scan nodes now equal PG exactly: Q30 `date_dim_pkey` 0.68,
  Q46 `customer_pkey` 5.64 against 5.63, Q49 `web_sales_pkey` 7.94.
- Q24, the task's witness, already matched at HEAD after the reload.
- A plan total that moved away from PG reflects other cost terms, now that the
  index input is right. Nothing was tuned (R6).

**Gates on the patched tree.** units PASS; `tpch-spotcheck` PASS (Q12=2,
Q13=35); `tpcds-sf025` PASS=99 with MISMATCH=0; `tpcds-fireset` PASS
(introduced=none).

**Parking and landing.** The change waited as a patch while
`tpch-acceptance-arm` could not pass for any tree. Every compared query matched,
but `bench/tpch/baseline-digests.txt` pinned Q5's timeout as an error, and
`digestdiff` failed a both-arms error by design. The owner resolved it with an
`XFAIL` pin: a baseline-pinned error now passes as `EXPECTED-ERROR`, and a
recovered query is `XFAIL-RESOLVED`, which forces a re-pin. The patch was then
applied unchanged and re-gated.

**Worker-count sizing (2026-10-16 slice).** The same default-database lookup
lived at the two parallel worker-count sites. PG's `compute_parallel_worker`
(`./postgres/src/backend/optimizer/path/allpaths.c:4274`) sizes a scan on
`rel->pages` and, for an index-only scan, `index->pages`, both read from the
opened relations (`plancat.c`). Changes:

- **`catalog.TableRealPagesIn`** is `IndexRealPagesIn`'s heap sibling,
  resolving through `cat.RelFileNode`.
- **The upper-rel split** (`upperSplitWorkersForScan`) receives the
  statement's catalog. `upperRels` gains a `cat` field, set where
  `planSelectWithSettings`, `createGroupingPaths` and `createDistinctPaths`
  create it.
- **The post-pass** (`computeParallelWorkersForScan`) reads index pages through
  the new `ParallelSettings.BlocksForIndex`. Both postmaster dispatch paths wire
  it to `IndexRealPagesIn`, never to `pool.NBlocks`, which would create a
  missing fork. The table side already resolved through `cat.RelFileNode`
  (`BlocksForTable`).

Tests: `TestTableRealPagesInUsesCatalogDatabase` and
`TestUpperSplitWorkersSizeOnCatalogDatabase`. In the latter, the index-only
scan is sized on its 30000-page index; without the catalog the count falls back
to a statistics guess of zero workers.

**Residuals.**

- The two parallel worker-count sites (`computeParallelWorkersForScan`,
  `upperSplitWorkersForScan`) and `catalog.TableRealPages` still use the
  package-level lookup: no catalog reaches them. Both sizings miss the same
  file, so they still agree with each other.
- The heap trailing-page residual (RELPAGES).

