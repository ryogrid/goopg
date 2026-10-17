# M0146-0213 — nbtree "split after new item" (`_bt_afternewitemoff`) and the TPC-H Q16 partsupp scan

Status: engine fix landed 2026-10-16. The census record (TPC-H Q16, scan-type) moves only once the `:65433` corpus
index is rebuilt the way PG's was (§4). That rebuild is an owner action.

## 1. The record

The post-VACUUM census (`routing-20261016q`) left TPC-H Q16 as the one actionable record. PG scans `partsupp` with
`Parallel Index Only Scan using partsupp_pk (cost=350.43..19178.43 rows=100000)`, 4 workers. goopg scanned it with
`Parallel Seq Scan (cost=0.00..21257.81 rows=129032)`, 3 workers.

## 2. Diagnosis

An `addPath` trace (`GOOPG_PGSHAPED_DP_TRACE=1` via the estimate-audit arm, `analysis/m0146/m0146-0213/`) shows the
candidate exists. `indexonly.partial` built a Parallel Index Only Scan with PG's 100000 rows (4 workers) at
`0.425..27180.4`, and it lost to the 21257.8 seq scan. So the path, the worker count and the hashed-SubPlan filter
admission were all PG's; only the cost differed.

The cost gap is the index size. `partsupp_pk` is 5170 pages in goopg and 3082 in PG; heap pages and `relallvisible`
agree. `cost_index` charges the full index's pages at `random_page_cost`: 2088 × 4 = 8352, exactly the IOS cost gap
(27180 − 18828).

Decoding the leaf pages (both data directories, read-only) gives:

| | goopg | PG |
|---|---|---|
| tuple size | 24 B | 24 B |
| leaves | 5141 | 3066 |
| items per leaf | 156.6 | 261.9 |
| free bytes per leaf | 3763 | 820 |

4668 goopg leaves hold 140-159 items: uniformly half full. The cause is the corpus's construction. The 2026-10-15
realign reloaded `:65433` by INSERT replay into tables whose indexes already existed. Heap order shows 17,326
descending steps in 800,000 rows: the replay interleaves ascending runs from different key ranges, the shape a
multi-VU HammerDB load leaves in PG's heap. Each run appends at the end of its own range, which is a NON-rightmost
leaf, so goopg split it 50:50 and the left half was never refilled. PG's index was built by CREATE INDEX after the
load, at fillfactor 90.

## 3. The missing PG mechanism

`_bt_findsplitloc` (`postgres/src/backend/access/nbtree/nbtsplitloc.c`) has three leaf strategies:
- fillfactor on the rightmost page (goopg had this, M0146-0178 slice 1);
- 50:50 elsewhere;
- **"split after new item"**, `_bt_afternewitemoff`, for localized monotonically increasing insertion into a
  composite index. goopg had not ported it (deferral ledger, M0146-0178 slice 1 item (2) and the M0146-0195 close
  row).

`afterNewItemOff` (`internal/access/nbtree/btree.go`) is that function. It declines unless all of these hold:
- a composite key;
- the new item is not first on the page;
- equal-sized tuples on the page, so no posting list;
- the tuple is no wider than two int8 attributes;
- `_bt_keep_natts_fast` against the left neighbour is greater than 1 and at most nkeyatts: the first attribute is
  equal by image, and the item is not a full duplicate (`keepNAttsFast`).

When it applies:
- **New item right of every existing item:** use the leaf fillfactor multiplier.
- **Otherwise:** the left neighbour must also be heap-TID adjacent (`_bt_adjacenthtid`, `adjacentHeapTID`). Then:
  - split exactly after the new item, leaving it as lastleft;
  - or use the fillfactor multiplier, when `newitemoff / (maxoff + 1)` exceeds the fillfactor.
  - An exact cut that does not fit falls back to 50:50, as upstream's defensive branch does (`compactSplitAtFits`).

## 4. Measured

| build of the same 800k partsupp rows in the replay's heap order | pages |
|---|---|
| goopg before (the `:65433` corpus index) | 5170 |
| goopg with this change (insert-built) | 3973 |
| PG 18.3, scratch cluster, same inserts | 4080 |
| PG 18.3 / goopg, CREATE INDEX / ADD PRIMARY KEY after load | 3082 / 3078 |

Scratch patterns, 200k rows (goopg before → after, PG):

| pattern | goopg before | goopg after | PG |
|---|---|---|---|
| 4 interleaved ascending streams | 1234 | 917 | 902 |
| shuffled 23-row runs | 1027 | 1173 | 1146 |
| in key order | 773 | 773 | 775 |

goopg's insert path now tracks PG's. With an index built like PG's reference (private clone, bulk-built 3078 pages),
goopg plans PG's Q16 shape: `Parallel Index Only Scan ... (cost=0.42..18812.42 rows=100000)`, 4 workers, against
PG's 18828 run cost. The remaining startup gap is PG's hashed-SubPlan load (350.43), which goopg's scan costing
drops; that is ledgered under M0146-0019a item (2). It is equal for every scan rival of a relation, so no plan turns
on it.

**The census record moves only after `:65433`'s `partsupp_pk` is rebuilt as PG's was.** One route is `REINDEX
TABLE partsupp`, which needs M0146-0196 (REINDEX cannot resolve relations in a CREATE DATABASE'd database). The
other is a reload that creates the indexes after the data. Both are owner actions on a reference cluster.
`lineitem_pk`'s 1.8× (M0146-0195 close row) is the same mechanism.

## 5. Pins

- **`TestAfterNewItemOffDecisions`:** every decline and both apply arms, including next-block adjacency, postings,
  a single-attribute key and the blob format.
- **`TestLocalizedAscendingInsertsPackLeaves`:** four interleaved ascending streams must build within 10 % of a
  single ascending stream (71 vs 68 blocks). With the port disabled it builds 106, so the test fails without it.
- **Gates:** nbtree `-race`, units, tpch-spotcheck, sf025 PASS=99 PLAN-SHAPE same=99, full `TestPort_RegressSuite`.
