# M0146-0233 — ineq\_histogram\_selectivity's actual endpoints and cutoff clamp

Status: closed 2026-10-20. Parent: M0146-0214. Evidence: `analysis/m0146/m0146-0233/`.

## 1. Record

Filed by M0146-0214 slice 6. On SF1 Q64's GEQO evaluation 41, the merge start fraction of store\_sales was 9.545e-5 in
PG and 7.2e-5 in goopg (275 and 207 rows skipped). goopg's `histogramOpSelectivity` had two "Not ported" notes: the
cutoff clamp, and get\_actual\_variable\_range's endpoint refresh.

## 2. What PG does (selfuncs.c)

`ineq_histogram_selectivity` finds the literal's bin by binary search over the histogram bounds.

- **Endpoint refresh.** When the search is about to compare against the first or last bound, it first replaces
  that bound with the column's actual current minimum or maximum, `get_actual_variable_range`. With only two bounds,
  both are refreshed up front. `have_end` records whether the last refresh succeeded.
- **Clamp.** The result is clamped to [0, 1] when `have_end`. Otherwise it is clamped to a hundredth of the
  histogram resolution from either end, 0.01 / (nvalues − 1): "the histogram boundaries are only approximate to
  begin with, and may well be out of date anyway".
- **`get_actual_variable_range`.** It takes the first index of `rel->indexlist` that is:
  - a btree with an ordering operator family;
  - not partial and not hypothetical;
  - led by the plain column, under the comparison's collation;
  - in an operator family where the histogram's sort operator has a strategy (a descending index answers from the
    other end).

  A partitioned parent has no real indexes. A derived rel (a subquery or CTE) has an empty indexlist, so its Vars
  are never probed.
- **`get_actual_variable_endpoint`.** It runs an index-only scan in the wanted direction with an `IS NOT NULL` key
  under SnapshotNonVacuumable:
  - an entry on an all-visible heap page is taken without a heap visit;
  - otherwise the heap is fetched, and an entry with no non-dead tuple is skipped;
  - a heap page that yielded nothing counts toward VISITED\_PAGES\_LIMIT (100), and passing that limit abandons the
    probe.
- **No histogram.** `scalarineqsel` takes half of the non-MCV mass when `ineq_histogram_selectivity` has no usable
  histogram (-1).

## 3. Port

- **nbtree** `ScanFromEnd` (endpoint\_scan.go) is `_bt_endpoint`'s backward arm. It descends each level's last
  downlink to the rightmost leaf, reads entries from the highest offset down (posting lists in reverse), and walks
  the left links, skipping deleted and half-dead pages. The forward end is the existing `RangeScan(nil, nil, …)`.
- **executor** `ActualVariableEndpointProbe` and `ActualVariableRange` (actual\_variable\_endpoint.go):
  - the probe decodes each entry's leading key and skips NULLs;
  - it takes an entry on an all-visible page, or one whose HOT chain is not entirely dead to all
    (`heapChainDeadToAll`, the prune/VACUUM predicate);
  - it counts empty heap pages against VISITED\_PAGES\_LIMIT;
  - it renders the value as ANALYZE renders histogram bounds;
  - the index selection follows §2, with `*_pattern_ops` classes treated as non-default.
- **catalog** `ActualVariableRangeFunc` is wired by initdb with the runtime catalog captured, because the
  restriction selectivity path has no catalog handle.
- **optimizer** `histogramOpSelectivity` is PG's binary search, endpoint refresh, `have_end` and clamp, and returns
  -1 with no histogram; `scalarIneqSelStats` then uses 0.5 (it used DEFAULT\_INEQ\_SEL). An `endpointProbe` reaches
  every caller:
  - the restriction path (`childBaseColumnProbe`): only when the column is the scan's own table, not one resolved
    through a derived boundary;
  - the join search's OR arm and its range arm (`relProbe`): base rels only;
  - mergejoinscansel's `scalarineqsel` calls, through `joinVarStats.probe`.

## 4. Result

- **End to end** (`endpoint-probe.sql`): ANALYZE with ids 1..10000, then 10000 more rows. `id > 15000` is beyond the
  stale histogram. PG estimates selectivity 0.00495 (98 of the 19778 rows it scales reltuples to). goopg now
  estimates 50 of its 10000, the same selectivity; before, it clamped to 1 row. The row-count gap is
  `estimate_rel_size`'s current-pages scaling, which goopg lacks (filed as M0146-0237).
- **Fire set:** plan parity holds at 98 / 99 (SF0.25) and 96 / 99 (SF1). Q13 and Q48 changed plans, with results
  identical.
- **Row-estimate census** (rows equal to PG's, node by node):
  - SF0.25: 2536 → 2539 nodes; queries with every estimate equal, 86 → 87.
  - SF1: 2603 → 2606 nodes; queries with every estimate equal, 85 → 86.
  - Q48 is now estimate-identical at both scales, and Q13 moved closer.

## 5. Pins

- `TestInequalityEstimateReadsActualIndexEndpoints` (postmaster, through the wired hook): 50 rows. Unwiring the
  hook gives 1.
- `TestHistogramOpSelectivityActualEndpoints`:
  - refreshed ends, both the last bin and the first;
  - no clamp above the actual maximum;
  - no probe inside the histogram;
  - a failing probe falls back to the clamp;
  - two-bound refresh.

  Ignoring `have_end` fails it.
- `TestHistogramOpSelectivityEqSel` now carries PG's 0.002 cutoff at the ends.
- `TestScanFromEndVisitsEntriesInReverseKeyOrder` (nbtree): 20000 scrambled inserts, every entry in reverse key
  order, early stop.

## 6. Residuals (ledger)

- SnapshotNonVacuumable also treats a tuple whose inserter aborted as dead. `heapChainDeadToAll` judges deleters
  only, so such an index entry can be taken as the endpoint.
- The probe opens the btree and builds its key descriptor on every call; PG caches the relcache entry. Plans are
  unaffected.
- Index validity (indisvalid, CREATE INDEX CONCURRENTLY in progress) is not consulted; goopg's catalog index carries
  no such flag.
- M0146-0237: estimate\_rel\_size's density × current-pages scaling of a stale reltuples.
