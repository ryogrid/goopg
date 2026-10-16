# M0146-0216 — physical-parity census and stored `pg_class.relallvisible`

Status: slice 1 (census + attribution), slice 2 (stored relallvisible) and slice 3 (`_bt_findsplitloc` interval
and penalty) landed 2026-10-16. Evidence: `analysis/m0146/m0146-0216/`.

## 1. Census (slice 1)

The census compares `pg_class` relpages / relallvisible / reltuples and `pg_relation_size` for every public table
and index of both TPC-DS corpora: private clones of goopg's `data-sf025` and SF1 `data`, against the reference
`:65438` (`tpcds025`, `tpcds`, SELECT only).

- **Heap pages are at parity** at both scales: relpages and physical size are identical on every table. A
  replayed COPY carries a bulk-extension tail that VACUUM truncates; both sides are post-truncation.
- **relallvisible differs on 20 / 23 tables.** goopg is fully all-visible after the owner's VACUUM. PG has partial
  tails on large tables and 0 on small ones, which autovacuum's insert threshold never reached. That is load
  history, not an algorithm.
- **Index pages:** the corpus indexes predate the M0146-0213 split port (inventory_pkey 14502 against PG's 12606).
  Replaying the same COPY on the current engine brings them within 0-3% of PG (inventory_pkey 12774,
  web_sales_pkey exact). Both clusters' indexes are insert-built: schema with keys first, then COPY.

## 2. The PG mechanism goopg lacked (slice 2)

PG's planner reads the **stored** `pg_class.relallvisible` for `allvisfrac` (`plancat.c` estimate_rel_size
:1112-1169). It does not read the visibility map. The stored value is set by `vac_update_relstats` from
`visibilitymap_count` in VACUUM (`heap_vacuum_rel`, clamped to relpages), in ANALYZE (`do_analyze_rel`) and in
index builds (`index_update_stats`), and `pg_restore_relation_stats` overwrites it. So it goes stale between
maintenance passes. goopg read the live map, so neither PG's staleness nor an imported value could reach the
planner.

Landed:

- `TableStats.AllVisible` / `AllVisibleSet`, set through `InMemory.SetRelAllVisible`.
- `snapshotRelAllVisible` (`visibilitymap_count` → `vac_update_relstats`) runs at the three PG sites: VACUUM,
  ANALYZE and the index-build heap-stats update.
- Readers use the stored value when set: the planner's `RelAllVisibleBlocks` / `RelAllVisible` and the `pg_class`
  view cell.
- Persistence: a fourth `allvisible` column in the size sidecar (`GoopgRelStatsColumns`), NULL when unset. An old
  three-attribute row decodes as unset, so existing corpora plan exactly as before.
- `pg_restore_relation_stats` applies `relallvisible`. It used to accept and ignore it (M0146-0215 ledger row,
  now resolved).

Fallback: a relation that no VACUUM, ANALYZE, index build or import has touched since this change reads the live
map, which is the old behaviour. PG would read the stored value there: 0 for a never-vacuumed relation. Ledger row.

## 3. Measured

- On a fresh SF0.25 clone with the new binary, old sidecar rows read the same values as before. After importing
  the reference's full statistics, including relallvisible, PLAN-PARITY stays at 88 / 99 (the M0146-0215 import
  alone gave 88); CATEGORIES-EXCL-MATCH parallelism goes 5 → 4. relallvisible therefore flips no further SF0.25
  query by itself.
- With PG-exact statistics, the 11 remaining SF0.25 first divergences
  (`import-census/first-divergence-sf025.txt`) are planner divergences. sort-strategy accounts for 5 (Q4, Q38, Q72,
  Q77, Q87) and parallelism for 2 (Q16, Q39). The rest are Q61 aggregation-strategy, Q69 join-method, Q78
  qual-placement, and Q64 join-order, which is still physical through index geometry (M0146-0214).

## 4. Pins

- `TestRelAllVisibleIsStoredLikePG` (postmaster, over the wire and across a restart):
  - VACUUM stores relallvisible = relpages;
  - an UPDATE leaves it stale, as PG's is;
  - `pg_restore_relation_stats` sets it, and the value survives a restart.
  - The test server wires the VM as `cmd/goopg` does.
- `TestImportedRelAllVisibleReachesPlanner`: the same index-only plan costs differently with relallvisible 0 and
  with full.
- Mutations fail both tests: the view ignoring the stored value, and the planner reading the live map.

## 5. Next

- Owner: refresh the corpus indexes, by reload on the current engine; and the stats-import decision
  (M0146-0215).
- Engine: the residual 0-3% insert-split gap against `_bt_findsplitloc`. Then the sort-strategy residual class,
  which is the largest under PG-exact statistics, through S9.

## 6. Slice 3 (2026-10-16): `_bt_findsplitloc`'s interval and suffix-truncation penalty

Method: the SF0.25 fact-table loads were replayed with `CREATE TABLE t_rep (LIKE t INCLUDING ALL)` and the same
server-side COPY, on a private goopg clone and on the `tmp/pgdbg` scratch PG. The scratch replay reproduces the
reference's index sizes exactly (2569 / 12606 / 1250), so it is a faithful oracle. `split/btleaves.py` decodes the
raw index files of both engines (PG's btree layout) and walks the leaves left to right.

**Cause.** goopg cut a leaf at the single best-balanced split. PG (`nbtsplitloc.c`) does more:

- records every legal split (`_bt_recsplitloc`);
- sorts them by distance from the optimum with `qsort` (`_bt_deltasortsplits`), which is unstable, so ties keep
  pg_qsort's order;
- bounds an interval of near-optimal splits (`_bt_defaultinterval`, LEAF_SPLIT_DISTANCE 5%);
- picks the one in it whose new high key keeps the fewest key attributes (`_bt_bestsplitloc` /
  `_bt_split_penalty` = `_bt_keep_natts_fast`), with the many-duplicates and single-value strategies of
  `_bt_strategy`.

On a composite key that moves each split a few items, which is visible as shifted items-per-leaf histograms
(inventory: PG 180/185/240/155, goopg 183/181/178/188).

**Port.** The new `splitloc.go` holds `pgFindSplitLoc`, `deltaSort` (on `pgqsort`), `defaultInterval` and
`intervalEdges`. `splitCandidates` was extracted from `compactSplitLocFill`, so the old path and the PG path price a
candidate identically (goopg's footprint accounting equals PG's free space on a unique index). It is used for
descriptor-format leaf splits; the "split after new item" exact cut still comes first, as in PG.

**Measured** (SF0.25 replay, index pages):

| index | HEAD | ported | PG |
|---|---|---|---|
| inventory_pkey | 12774 | **12606** | 12606 |
| catalog_sales_pkey | 1226 | **1250** | 1250 |
| store_returns_pkey | 277 | **280** | 280 |
| web_sales_pkey | 598 | **598** | 598 |
| catalog_returns_pkey | 138 | **137** | 137 |
| store_sales_pkey | 2496 | 2567 | 2569 |

inventory's 12560-leaf sequence is identical to PG's, leaf for leaf. store_sales is identical up to leaf 640. There
PG splits once more, because its heap TIDs differ by one block: PG's COPY records a page's free space in the FSM
when it moves on (`RecordAndGetPageWithFreeSpace`, `hio.c`) and puts a later short tuple back into it (block 6
holds 56 rows in PG, 55 in goopg). That changes a `_bt_afternewitemoff` heap-TID adjacency test. It is a heap
placement item, recorded in the ledger.

**Pin.** `TestInsertBuiltCompositeIndexMatchesPGPageCount` covers three 60000-key scrambled composite workloads with
PG's heap TID layout. Its page counts were measured on PG 18.3: 273 / 199 / 288. HEAD built 269 / 249 / 304, and
routing leaves back to the old cut fails the test.

**Not ported:** internal-page splits keep goopg's cut. PG's internal penalty is the firstright size, and its
free-space accounting discards the right page's first key; ledger row.

The shipped corpora are unchanged until they are reloaded on this engine (owner action). A reload would now give
index geometry within 2 pages of PG on every SF0.25 fact table.
