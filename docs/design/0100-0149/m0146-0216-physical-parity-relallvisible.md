# M0146-0216 — physical-parity census and stored `pg_class.relallvisible`

Status: slice 1 (census + attribution), slice 2 (stored relallvisible) and slice 3 (`_bt_findsplitloc` interval
and penalty) landed 2026-10-16. Slice 4 (internal-page splits, §7) and slice 5 (COPY heap placement through the FSM, §8)
landed 2026-10-20. Evidence:
`analysis/m0146/m0146-0216/`.

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

## 7. Slice 4 (2026-10-20): internal-page splits

**Record.** After the owner's 2026-10-19 reload, customer\_demographics\_pkey is 5269 pages on both corpora against
PG's 5268. That one page adds +4.0 to every probe of the index: the +4.0 / +4.57 offsets on SF1 Q64's joins and the
index-only outer of the cd ⋈ store\_sales merge (M0146-0214). The table's 1920800 keys arrive in key order, so every
split is a rightmost one. The leaf levels are identical: 5247 leaves of 367 items, plus a rightmost leaf of 398. The
gap is the internal level. PG has 18 pages: 17 of 285 downlinks plus a high key, and a rightmost page of 403. goopg
had 19 pages of 284.

**Cause.** goopg priced an internal split with the leaf's arithmetic (`compactSplitLocFill`). `nbtsplitloc.c` and
`nbtinsert.c` treat a non-leaf page differently in two ways:

- `_bt_recsplitloc` charges the left half's new high key at the firstright tuple's size. It adds no heap-TID
  allowance, because that is the leaf's worst case under suffix truncation. It also credits the right half with
  the firstright tuple's key data: `rightfree += firstrightitemsz - (MAXALIGN(sizeof(IndexTupleData)) +
  sizeof(ItemIdData))`.
- `_bt_split` stores that credit. On an internal page the right half's first item is written key-less, as a
  minus-infinity pivot (`newfirstdataitem`, `_bt_pgaddtup`), so it takes 8 bytes where goopg kept a 16-byte keyed
  tuple.

The interval is `INTERNAL_SPLIT_DISTANCE` (7.5%). The fill factor is `BTREE_NONLEAF_FILLFACTOR` on a rightmost page
and an even balance elsewhere. Within the interval `_bt_bestsplitloc` takes the split with the smallest firstright
tuple, because that tuple becomes the parent's separator (`_bt_split_penalty`). It stops at the page-wide minimum
(`_bt_strategy`'s perfect penalty for internal pages).

**Port.** `pgFindSplitLocInternal` (splitloc.go) is `_bt_findsplitloc` for a non-leaf page. It reuses `deltaSort`,
and `defaultInterval` becomes `splitInterval` with the distance as a parameter. `insertIntoBlock` routes
descriptor-format internal pages through it and truncates the right half's first item to `downlinkItem(nil,
child)`. That is the same form every root's first downlink already has.

**Measured.** The pkeys were replayed on clones: the same inline-key DDL, then server-side COPY from the TSVs the
corpus loaded. On the HEAD binary the replay reproduces every shipped size exactly. INSERT … SELECT in heap order
does not reproduce them: store\_sales gives 2578 against COPY's 2567, so a replay must use COPY.
`slice4/replay-results.txt`:

| scale | index | corpus (HEAD) | slice 4 | PG |
|---|---|---|---|---|
| SF0.25 | customer\_demographics\_pkey | 5269 | **5268** | 5268 |
| SF0.25 | store\_sales\_pkey | 2567 | 2567 | 2569 |
| SF0.25 | store\_returns / catalog\_sales | 280 / 1250 | 280 / 1250 | 280 / 1250 |
| SF1 | customer\_demographics\_pkey | 5269 | **5268** | 5268 |
| SF1 | store\_sales\_pkey | 11042 | 11042 | 11041 |
| SF1 | store\_returns\_pkey | 1081 | 1081 | 1082 |
| SF1 | catalog\_sales\_pkey | 5624 | 5624 | 5625 |

**The remaining four are heap placement.** At SF1 the per-block heap row counts differ from PG's replica:

- catalog\_sales: 7361 blocks differ (block 1 holds 39 rows in PG, 38 in goopg);
- store\_returns: 689 blocks differ;
- store\_sales: 17539 blocks differ.

This is §6's mechanism at scale. PG's COPY records a page's free space in the FSM when it moves on, and later
short tuples refill that page (`RecordAndGetPageWithFreeSpace`, hio.c). goopg fills forward. The heap TIDs differ,
so `_bt_afternewitemoff`'s adjacency test and the leaf splits differ with them. The ledger row of 2026-10-16 owns
this. It is the last engine mechanism between the corpora and PG's index geometry.

**Pins** (`internal_split_test.go`):

- `TestInsertBuiltSerialIndexMatchesPGInternalLevel` builds the 1920800-key ascending index. It checks PG's 5268
  pages, the 17 × 285 + 403 internal level, and an 8-byte first downlink on every internal page.
- `TestFindSplitLocInternalTakesSmallestFirstRight` has three cases. The smallest-firstright pick: 93 against the
  balanced 89. The balanced pick once the short item leaves the interval. The 70/30 rightmost split: 127, which the
  leaf's 8-byte allowance would move to 126.
- Mutations fail them:
  - Keeping the right half's first key fails the first pin (16-byte first downlinks).
  - Ignoring the penalty, or charging the heap-TID allowance, fails the second.
  - Reverting both halves fails the page count (5269).
  - The 1.92M-key layout alone cannot tell the two halves apart: either saving yields the 285th downlink.

**Plans.** The shipped corpora are unchanged until an owner rebuild (`scripts/tpcds-corpus-rebuild.sh`). Gates on
them confirm no regression: sf025 sweep same=99, and the fire set fired nothing (SF1 95 / 99). A rebuild would
remove the +4.0 customer\_demographics\_pkey offset from SF1 Q64 and SF0.25 Q77's merge.

**Not changed.** The bulk loader (`bulkload.go`, CREATE INDEX on a filled table) still fills internal pages with
its own reserve arithmetic. Upstream `_bt_buildadd` fills them to `BTREE_NONLEAF_FILLFACTOR` and truncates each new
page's first item. No measured index here is bulk-built: both corpora and the TPC-H bench declare keys before
loading. Ledger row.

## 8. Slice 5 (2026-10-20): COPY heap placement through PG's free space map

**Record.** After slice 4, four indexes were still 1-2 pages off PG: SF0.25 store\_sales\_pkey 2567 against 2569,
and SF1 store\_sales 11042 / 11041, store\_returns 1081 / 1082, catalog\_sales 5624 / 5625. Each followed heap
TIDs. PG's block 6 of SF0.25 store\_sales holds 56 rows, goopg's 55. The extra row is a 77-byte tuple that PG put
back on block 6 and goopg put at block 520.

**What PG does.** RelationGetBufferForTuple (hio.c) with COPY's BulkInsertState:

1. keeps filling the current page;
2. then takes the bulk extension's next free page, recording the page it leaves (`RecordPageWithFreeSpace`);
3. once those are used up, calls `RecordAndGetPageWithFreeSpace`;
4. only then calls RelationAddBlocks.

RelationAddBlocks enters the pages beyond the caller's need into the FSM and calls
`FreeSpaceMapVacuumRange(first_fsm_block, last_block)`. That range's end is exclusive, so the last page stays out.
The search itself is freespace.c / fsmpage.c:

- A leaf FSM page covers 4069 heap blocks and is a binary max-tree of one-byte categories. Space rounds down;
  requests round up.
- `fsm_search_avail` starts at the page's `fp_next_slot` and moves right with wraparound. It leaves the hint one
  past the slot it returns.
- `RecordAndGetPageWithFreeSpace` searches the old page's own leaf first, then the tree from the root
  (`fsm_search`).
- Upper levels change only through `FreeSpaceMapVacuumRange` (each upper slot gets its leaf's maximum, and every
  visited page's hint resets to 0), or when `fsm_search` corrects a stale slot it descended into.

**What goopg did.** The bulk path (`bulk_insert.go`, M0146-0009h) already followed hio.c step for step, over
goopg's flat first-fit FSM. But server-side COPY built its executor context by hand (`postmaster/copy.go`) without
`FSM` or `VM`. With `ctx.FSM == nil`:

- `tryAppendToBlock` recorded nothing;
- every lookup returned "no page", so the heap filled strictly forward;
- the VM bit of an all-visible page COPY appended to was left set.

A trace of the SF0.25 load showed every RecordAndGet search failing, block 519's included, while block 6 had 104
bytes free.

**Port.**

- `storage/fsm_pgtree.go` holds PG's FSM tree per relation. It implements:
  - addressing: `fsm_logical_to_physical`, `fsm_get_location`, parent and child;
  - `fsm_set_avail` and `fsm_search_avail` with the hint, `fsm_rebuild_page` and `fsm_truncate_avail`;
  - `fsm_set_and_search`, and `fsm_search` with its stale-slot correction and its restart bound;
  - `fsm_vacuum_page` / `FreeSpaceMapVacuumRange`;
  - the page-existence rule of `fsm_readbuf`, where a page exists below the fork's size;
  - truncation.
- The FSM keeps the tree beside its flat array:
  - every record sets the leaf, with `PageGetHeapFreeSpace` when the page is at hand;
  - load rebuilds the tree from the fork's categories, as a vacuumed FSM;
  - truncate and drop follow the flat array.
  - `PGGetPageWithFreeSpace`, `PGRecordAndGetPageWithFreeSpace` and `PGVacuumRange` read and maintain the tree;
    goopg's other insert paths still use the flat first-fit lookups (ledger).
- The bulk path's lookups are PG's:
  - the fresh state calls GetPageWithFreeSpace;
  - leaving a page calls RecordAndGetPageWithFreeSpace with the page's `PageGetHeapFreeSpace`;
  - each extension calls the vacuum range.
- COPY's context gets `FSM` and `VM`, as dispatch wires them.
- VACUUM ends with `PGVacuumRange(0, nblocks)`, lazy\_scan\_heap's last `FreeSpaceMapVacuumRange`.
- `MaxFSMRequestSize` is corrected to PG's `MaxHeapTupleSize`, 8160 (it was 8164).

**Measured** (`slice5/`): the full COPY replay of all 24 tables at both scales.

- Every pkey equals PG's page count. That includes the four above: 2569, 11041, 1082 and 5625.
- Heap pages equal PG's, except the bulk-extension tails COPY leaves before VACUUM. VACUUM of the replayed copies
  truncates them to PG's 242 / 721 / 53 / 332.
- Per-block heap row counts against the PG replica: 0 differing blocks on store\_sales, store\_returns and
  catalog\_sales at both scales. Before, 4303 / 387 / 58 blocks differed at SF0.25 and 17539 / 689 / 7361 at SF1.
- Together with slice 4, a corpus reload on this engine now reproduces the reference's physical layout heap for
  heap and index for index.

**Pins.**

- `TestCopyFromRefillsEarlierPagesThroughTheFSM` (postmaster) COPYs 3000 rows of 800-1200 bytes. It checks PG
  18.3's measured layout: 411 blocks and their row counts, plus the 8 rows PG placed back on earlier pages. It
  fails when the context's FSM is removed, when the bulk path goes back to first-fit, and when the extension's
  vacuum range is dropped.
- `fsm_pgtree_test.go` covers four behaviours:
  - fsmpage.c's worked example: search right from the hint, wrap, and leave the hint;
  - upper levels move only on a vacuum range, whose end is exclusive at a leaf boundary;
  - RecordAndGet prefers the old page's leaf;
  - the category edges.

**Found on the way** (M0146-0236, S2): above a WindowAgg, `ctid` reads NULL and `xmin` does not resolve. The pin
finds its back-jumps without `lag(ctid)` for that reason.

**Plans.** The shipped corpora are unchanged: sf025 same=99, and the fire set fired nothing (SF0.25 98 / 99, SF1
95 / 99). An owner rebuild on this engine gives PG-exact index geometry. Its first effect is the +4.0
customer\_demographics\_pkey offset on SF1 Q64 and SF0.25 Q77.

