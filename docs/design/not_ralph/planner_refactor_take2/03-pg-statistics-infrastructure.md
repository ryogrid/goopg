# 03 — PostgreSQL statistics infrastructure and selectivity estimation (PG 18.3 oracle)

Scope: how PostgreSQL 18.3 collects, stores, reads, and consumes planner
statistics. Every claim cites the oracle source under `postgres/` as
`src/backend/<path>/<file>.c:function` (or a header / SGML file). Constants are
quoted verbatim. Pseudo-code uses the real identifiers so it can be diffed
against another implementation. This document describes PostgreSQL only.

Conventions: `N` = estimated total rows in the relation, `n` = rows in the
ANALYZE sample, `CLAMP_PROBABILITY(x)` clamps to `[0,1]`
(`src/include/utils/selfuncs.h`), `clamp_row_est(x)` rounds to an integer `>= 1`
(`src/backend/optimizer/path/costsize.c:clamp_row_est`).

---

## 1. Relation-level statistics (`pg_class`)

### 1.1 The four columns and their writers

| column | meaning | sentinel |
|---|---|---|
| `relpages` | pages at last VACUUM/ANALYZE/CREATE INDEX | `0` for a fresh relation |
| `reltuples` | *live* tuples at that time (`float4`) | `-1` = "never vacuumed/analyzed" |
| `relallvisible` | pages with the VM all-visible bit set | `0` |
| `relallfrozen` | pages with the VM all-frozen bit set (new column in PG 18) | `0` |

All four are written by one routine, `src/backend/commands/vacuum.c:vac_update_relstats`.
It performs a **non-transactional in-place update** of the `pg_class` row
(`systable_inplace_update_begin` … `systable_inplace_update_finish`) so that
vacuuming `pg_class` itself does not bloat it; only the four stats plus the
DDL flags `relhasindex/relhasrules/relhastriggers` (skipped when
`in_outer_xact`) and `relfrozenxid/relminmxid` are touched. A field is written
only if it differs (`dirty` flag). The in-place update path registers a
relcache invalidation before it locks the buffer
(`src/backend/access/heap/heapam.c:heap_inplace_update_and_unlock` →
`CacheInvalidateHeapTupleInplace`), which is what makes other backends see the
new numbers (§11).

Writers:

* **CREATE TABLE**: `src/backend/catalog/heap.c:AddNewRelationTuple` stores
  `relpages = 0, reltuples = -1, relallvisible = 0` (sequences get 1/1).
* **TRUNCATE / rewrite** (`RelationSetNewRelfilenumber`,
  `src/backend/utils/cache/relcache.c`): resets to `relpages = 0,
  reltuples = -1, relallvisible = 0` for relations that are not sequences.
* **ANALYZE** (`src/backend/commands/analyze.c:do_analyze_rel`, non-inherited
  case only): `vac_update_relstats(onerel, relpages, totalrows, relallvisible,
  relallfrozen, hasindex, InvalidTransactionId, …)` where `relallvisible/
  relallfrozen` come from `visibilitymap_count`. For each index it stores
  `vac_update_relstats(Irel[ind], RelationGetNumberOfBlocks(Irel[ind]),
  totalindexrows, 0, 0, false, …)` with `totalindexrows =
  ceil(tupleFract * totalrows)` (`tupleFract` = fraction of sampled rows
  passing the partial-index predicate, §2.8). Partial ANALYZE of a column
  subset (`va_cols != NIL`) writes `vac_update_relstats(onerel, -1, totalrows,
  …)` i.e. only reltuples.
* **VACUUM** (`src/backend/access/heap/vacuumlazy.c:heap_vacuum_rel`):
  `new_live_tuples = vac_estimate_reltuples(rel, rel_pages, scanned_pages,
  live_tuples)`; `visibilitymap_count(rel, &new_rel_allvisible,
  &new_rel_allfrozen)`, clamped `allvisible <= new_rel_pages`,
  `allfrozen <= allvisible`; then `vac_update_relstats(rel, new_rel_pages,
  new_live_tuples, new_rel_allvisible, new_rel_allfrozen, …)`. Indexes get
  `vac_update_relstats(indrel, istat->num_pages, istat->num_index_tuples, 0, 0,
  false, …)` from `update_relstats_all_indexes`, unless the AM reported
  `estimated_count`.
* **CREATE INDEX / REINDEX** (`src/backend/catalog/index.c:index_build` →
  `index_update_stats(heapRelation, …, stats->heap_tuples)` and
  `index_update_stats(indexRelation, …, stats->index_tuples)`). If the caller
  passes `reltuples == 0` and the stored value is `-1`, the `-1` is preserved
  ("never analyzed" stays unknown); otherwise `relpages =
  RelationGetNumberOfBlocks(rel)` and, for heaps, `relallvisible/relallfrozen`
  from `visibilitymap_count`. Also done via in-place update.

### 1.2 `vac_estimate_reltuples` (VACUUM extrapolation)

`src/backend/commands/vacuum.c:vac_estimate_reltuples(relation, total_pages,
scanned_pages, scanned_tuples)`:

```
if scanned_pages >= total_pages:             return scanned_tuples
if old_rel_pages == total_pages and scanned_pages < total_pages*0.02:
                                             return old_rel_tuples   # may be -1
if scanned_pages <= 1:                       return old_rel_tuples
if old_rel_tuples < 0 or old_rel_pages == 0:
       return floor(scanned_tuples/scanned_pages * total_pages + 0.5)
old_density = old_rel_tuples / old_rel_pages
return floor(old_density*(total_pages-scanned_pages) + scanned_tuples + 0.5)
```

`scanned_tuples` counts **live** tuples only.

### 1.3 How the planner reads them: `estimate_rel_size`

`src/backend/optimizer/util/plancat.c:get_relation_info` calls
`estimate_rel_size(relation, rel->attr_widths - rel->min_attr, &rel->pages,
&rel->tuples, &rel->allvisfrac)` for every base relation. For relations with a
table AM this dispatches to `heapam_estimate_rel_size`
(`src/backend/access/heap/heapam_handler.c`) →
`src/backend/access/table/tableam.c:table_block_relation_estimate_size` with
`HEAP_OVERHEAD_BYTES_PER_TUPLE = MAXALIGN(SizeofHeapTupleHeader) +
sizeof(ItemIdData)` and `HEAP_USABLE_BYTES_PER_PAGE = BLCKSZ -
SizeOfPageHeaderData`:

```
curpages = RelationGetNumberOfBlocks(rel)          # live smgr count, not pg_class
if curpages < 10 and reltuples < 0 and !relhassubclass:
    curpages = 10                                    # never-vacuumed floor
*pages = curpages
if curpages == 0: *tuples = 0; *allvisfrac = 0; return
if reltuples >= 0 and relpages > 0:
    density = reltuples / relpages
else:
    fillfactor = RelationGetFillFactor(rel, HEAP_DEFAULT_FILLFACTOR)
    tuple_width = get_rel_data_width(rel, attr_widths) + overhead_bytes_per_tuple
    density = (usable_bytes_per_page * fillfactor / 100) / tuple_width   # integer division
    density = clamp_row_est(density)
*tuples = rint(density * curpages)
if relallvisible == 0 or curpages <= 0: *allvisfrac = 0
elif relallvisible >= curpages:          *allvisfrac = 1
else:                                    *allvisfrac = relallvisible / curpages
```

Key properties: page count is always the *current* physical size; tuple count
is *density scaled to current size*; `relallvisible` is **not** scaled
(pages added since the last VACUUM are assumed not all-visible).
`get_rel_data_width` (`plancat.c`) sums, over non-dropped attributes,
`get_attavgwidth(relid, attnum)` (= `pg_statistic.stawidth`, via
`src/backend/utils/cache/lsyscache.c:get_attavgwidth`, with an optional
`get_attavgwidth_hook`) falling back to `get_typavgwidth(atttypid, atttypmod)`,
and caches into `rel->attr_widths[]`.

For `RELKIND_INDEX` the same function (`plancat.c:estimate_rel_size`) does the
analogous computation, but subtracts one metapage from both `curpages` and
`relpages` when `relpages > 0` (comment: correct for btree/hash/GIN, suspect
for GiST) and does not apply the 10-page floor or fillfactor. Other relkinds
(foreign tables, sequences) return `pg_class` values as-is.

### 1.4 Index information (`IndexOptInfo`) in `get_relation_info`

`plancat.c:get_relation_info` builds one `IndexOptInfo` per valid index
(skips `!indisvalid`; skips `indcheckxmin` indexes whose xmin is not yet
visible). Fields:

| field | source |
|---|---|
| `ncolumns`, `nkeycolumns` | `indnatts`, `indnkeyatts` |
| `indexkeys[i]` | `indkey.values[i]` (0 = expression column) |
| `opfamily[i]`, `opcintype[i]`, `indexcollations[i]` | relcache `rd_opfamily/rd_opcintype/rd_indcollation` (key columns only) |
| `canreturn[i]` | `index_can_return(indexRelation, i+1)` |
| `relam`, `amcostestimate` | `rd_rel->relam`, `amroutine->amcostestimate` |
| `amcanorderbyop, amoptionalkey, amsearcharray, amsearchnulls, amcanparallel` | copied from `IndexAmRoutine` |
| `amhasgettuple` | `amroutine->amgettuple != NULL` |
| `amhasgetbitmap` | `amgetbitmap != NULL && relam != BRIN_AM_OID`-style check in the source (`amgetbitmap != NULL && …`) |
| `amcanmarkpos` | `ammarkpos != NULL && amrestrpos != NULL` |
| `sortopfamily, reverse_sort[], nulls_first[]` | if `amcanorder`: btree → `sortopfamily = opfamily`, `reverse_sort[i] = (indoption & INDOPTION_DESC)`, `nulls_first[i] = (indoption & INDOPTION_NULLS_FIRST)`; other ordered AMs → find `<` via `get_opfamily_member_for_cmptype` and map to a btree opfamily with `get_ordering_op_properties`, else `NULL`; non-orderable AMs → `NULL` |
| `indexprs`, `indpred` | `RelationGetIndexExpressions/Predicate`, `ChangeVarNodes(…,1,varno)` |
| `indextlist` | `build_index_tlist` |
| `predOK` | `false` here; set later by `indxpath.c:check_index_predicates` |
| `unique`, `immediate`, `hypothetical` | `indisunique`, `indimmediate`, `false` |
| `pages`, `tuples` | non-partial: `pages = RelationGetNumberOfBlocks(indexRelation)`, `tuples = rel->tuples`; partial: `estimate_rel_size(indexRelation, …)` then `tuples = min(tuples, rel->tuples)` |
| `tree_height` | `amroutine->amgettreeheight(indexRelation)` if provided (btree: `_bt_getrootheight`), else `-1` |

Partitioned indexes get zero pages/tuples and no AM flags. When the parent is
an inheritance parent (non-partitioned) only unique indexes are collected
(they serve as uniqueness proofs).

### 1.5 Column statistics access path

`pg_statistic` rows are fetched through the syscache `STATRELATTINH`
(unique index on `(starelid, staattnum, stainherit)`,
`src/include/catalog/pg_statistic.h`). Consumers:

* `src/backend/utils/adt/selfuncs.c:examine_simple_variable` →
  `SearchSysCache3(STATRELATTINH, relid, attnum, rte->inh)`.
* `src/backend/utils/cache/lsyscache.c:get_attstatsslot(sslot, statstuple,
  reqkind, reqop, flags)`: scans the 5 slots for `stakindN == reqkind &&
  (reqop == InvalidOid || staopN == reqop)`; fills `sslot->staop/stacoll`; with
  `ATTSTATSSLOT_VALUES` deconstructs `stavaluesN` (an `anyarray`) into
  `values[]/nvalues`, with `ATTSTATSSLOT_NUMBERS` deconstructs `stanumbersN`
  (`float4[]`) into `numbers[]/nnumbers`. `free_attstatsslot` releases.
* `lsyscache.c:get_attavgwidth` returns `stawidth` when `> 0`.
* Hooks: `get_relation_stats_hook` and `get_index_stats_hook`
  (`src/include/utils/selfuncs.h`) let extensions substitute a stats tuple.

---

## 2. ANALYZE (`src/backend/commands/analyze.c`)

### 2.1 `do_analyze_rel` flow

1. Build `vacattrstats[]` by `examine_attribute(onerel, attnum, NULL)` for the
   requested (or all) columns; for each index with expressions,
   `examine_attribute(Irel[ind], i+1, indexkey)` for expression columns.
2. `targrows = max(100, max over columns of stats->minrows)` (index expression
   columns included).
3. `numrows = acquire_sample_rows(...)` (or
   `acquire_inherited_sample_rows` when `inh`), also yielding `totalrows`,
   `totaldeadrows`.
4. Per column: `stats->compute_stats(stats, std_fetch_func, numrows,
   totalrows)`; then `ALTER TABLE … SET (n_distinct / n_distinct_inherited)`
   overrides: `if (n_distinct != 0.0) stats->stadistinct = n_distinct`
   (`get_attribute_options`).
5. `compute_index_stats(...)` for expression indexes.
6. `update_attstats(relid, inh, attr_cnt, vacattrstats)` writes `pg_statistic`
   rows (regular `CatalogTupleUpdate/Insert`, i.e. transactional), likewise
   for each index's expression columns with `inh = false`.
7. `BuildRelationExtStatistics(onerel, inh, totalrows, numrows, rows, attr_cnt,
   vacattrstats)` (§9).
8. If `!inh`: `vac_update_relstats` for the table and each index (§1.1).
9. `pgstat_report_analyze(onerel, totalrows, totaldeadrows, …)` resets the
   cumulative-stats `n_mod_since_analyze` counter (only for `!inh`; partitioned
   tables report 0/0).

### 2.2 `examine_attribute` and `std_typanalyze`

`examine_attribute` skips dropped columns, virtual generated columns, and
columns with `attstattarget == 0`. `attstattarget` is read from
`pg_attribute.attstattarget` (NULL → `-1`). The type's `typanalyze` function is
called if set, else `std_typanalyze`.

`std_typanalyze` (same file):

```
if attstattarget < 0: attstattarget = default_statistics_target   # GUC, default 100, max 10000
get_sort_group_operators(attrtypid, …, &ltopr, &eqopr, …)
if eqopr and ltopr: compute_stats = compute_scalar_stats
elif eqopr:         compute_stats = compute_distinct_stats   # (was compute_minimal_stats in older releases)
else:               compute_stats = compute_trivial_stats
minrows = 300 * attstattarget
```

The 300 constant is justified in the source comment by Chaudhuri, Motwani,
Narasayya (SIGMOD 1998), Corollary 1 to Theorem 5: `r = 4·k·ln(2n/γ)/f²`
with `f = 0.5, γ = 0.01, n = 10^6` gives `r = 305.82·k`, and the dependence on
`n` is logarithmic so no scaling with table size is needed.

### 2.3 `acquire_sample_rows` — two-stage sampling

`analyze.c:acquire_sample_rows(onerel, elevel, rows, targrows, &totalrows,
&totaldeadrows)`:

* Stage 1 (blocks): `BlockSampler_Init(&bs, totalblocks, targrows, randseed)`
  chooses `min(targrows, totalblocks)` blocks
  (`src/backend/utils/misc/sampling.c:BlockSampler_Init`). `BlockSampler_Next`
  implements Knuth's Algorithm S: with `K = N - t` remaining blocks and
  `k = n - m` still to pick, if `k >= K` take every remaining block; otherwise
  draw `V`, set `p = 1 - k/K`, and skip blocks while `V < p`, multiplying
  `p *= 1 - k/K` after each skip. Blocks are consumed through a read stream
  (`block_sampling_read_stream_next`).
* Stage 2 (rows): reservoir sampling per Vitter. The first `targrows` tuples
  fill `rows[]`; afterwards `rowstoskip = reservoir_get_next_S(&rstate,
  samplerows, targrows)` gives how many to skip; a selected tuple replaces
  `rows[k]` with `k = targrows * sampler_random_fract()`.
* Live/dead accounting: `table_scan_analyze_next_tuple(scan, OldestXmin,
  &liverows, &deadrows, slot)` counts every tuple on the sampled pages by
  `HeapTupleSatisfiesVacuum` against `OldestXmin =
  GetOldestNonRemovableTransactionId(onerel)`.
* Extrapolation: `bs.m` = blocks actually read.
  `*totalrows = floor(liverows/bs.m * totalblocks + 0.5)`,
  `*totaldeadrows = floor(deadrows/bs.m * totalblocks + 0.5)`.
* If the reservoir filled (`numrows == targrows`) the rows are sorted by TID
  (`compare_rows`) so that physical order is known for correlation.

`acquire_inherited_sample_rows` (same file) lists `find_all_inheritors`,
allocates `childtargrows = rint(targrows * childblocks / totalblocks)` per child
(capped by remaining budget) and concatenates the child samples, mapping
tuples through `convert_tuples_by_name` if needed.

### 2.4 `compute_scalar_stats` in full

Inputs: `stats`, fetch func, `samplerows`, `totalrows`. Locals:
`num_mcv = num_bins = attstattarget`, `track[num_mcv]` (ScalarMCVItem
`{count, first}`), `WIDTH_THRESHOLD = 1024`.

Pass 1 over sample rows:

```
if null: null_cnt++; continue
nonnull_cnt++
if is_varlena:
    total_width += VARSIZE_ANY(value)
    if toast_raw_datum_size(value) > WIDTH_THRESHOLD: toowide_cnt++; continue
    value = PG_DETOAST_DATUM(value)
elif is_varwidth (cstring): total_width += strlen+1
values[values_cnt] = {value, tupno=values_cnt}; tupnoLink[values_cnt] = values_cnt; values_cnt++
```

If `values_cnt > 0`:

1. `qsort_interruptible(values, values_cnt, compare_scalars, &cxt)` using
   `ssup` from `PrepareSortSupportFromOrderingOp(ltopr)` with the column
   collation. `compare_scalars` records for equal keys the highest `tupno`
   seen (`tupnoLink`), so after the sort `tupnoLink[tupno] == tupno` iff the
   item is the last of its duplicate group.
2. Scan in sorted order: `corr_xysum += i * tupno`; `dups_cnt++`; at the end
   of a duplicate group: `ndistinct++`; if `dups_cnt > 1`: `nmultiple++` and
   insert into `track[]` if `track_cnt < num_mcv || dups_cnt >
   track[track_cnt-1].count` (insertion sort by count descending, storing
   `first = i + 1 - dups_cnt`).
3. `stanullfrac = null_cnt / samplerows`; `stawidth = total_width /
   nonnull_cnt` (varwidth) or `typlen`.
4. `stadistinct`:
   * `nmultiple == 0` → `-1.0 * (1.0 - stanullfrac)` (all distinct).
   * `toowide_cnt == 0 && nmultiple == ndistinct` → `ndistinct` (every value
     repeated: assume a small fixed domain).
   * otherwise the Haas–Stokes **Duj1** estimator:
     `f1 = ndistinct - nmultiple + toowide_cnt`, `d = f1 + nmultiple`,
     `n = samplerows - null_cnt`, `N = totalrows * (1 - stanullfrac)`;
     `stadistinct = n*d / ((n - f1) + f1*n/N)` (0 if `N <= 0`), clamped to
     `[d, N]`, rounded `floor(x + 0.5)`.
   * Then: `if stadistinct > 0.1 * totalrows: stadistinct = -(stadistinct /
     totalrows)` (negative = fraction of rows, scales with table growth).
5. MCV selection:
   * complete-list case: `track_cnt == ndistinct && toowide_cnt == 0 &&
     stadistinct > 0 && track_cnt <= num_mcv` → keep all `track_cnt`.
   * else `num_mcv = min(num_mcv, track_cnt)` and
     `num_mcv = analyze_mcv_list(mcv_counts, num_mcv, stadistinct,
     stanullfrac, samplerows, totalrows)` (§2.5).
   * Slot: `stakind = STATISTIC_KIND_MCV (1)`, `staop = eqopr`,
     `stacoll = attrcollid`, `stanumbers[i] = track[i].count / samplerows`,
     `stavalues[i] = values[track[i].first].value`.
6. Histogram: `num_hist = ndistinct - num_mcv; if num_hist > num_bins:
   num_hist = num_bins + 1`. Built only if `num_hist >= 2`. MCV items are
   sorted by position (`compare_mcvs`) and collapsed out of `values[]`
   (`nvals` remaining). Bounds are `values[(i*(nvals-1))/(num_hist-1)]` for
   `i = 0..num_hist-1`, computed by stepping `delta = (nvals-1)/(num_hist-1)`,
   `deltafrac = (nvals-1) % (num_hist-1)` and carrying `posfrac` when it
   reaches `num_hist-1`. Slot: `STATISTIC_KIND_HISTOGRAM (2)`, `staop = ltopr`,
   `stanumbers = NULL`. The first bound is the sample MIN and the last the MAX
   (after MCV removal).
7. Correlation (if `values_cnt > 1`): Pearson coefficient between sorted
   position `i` and physical sample position `tupno`, using the closed forms
   `corr_xsum = (v-1)v/2`, `corr_x2sum = (v-1)v(2v-1)/6`:
   `corr = (v*corr_xysum - corr_xsum²) / (v*corr_x2sum - corr_xsum²)`.
   Slot: `STATISTIC_KIND_CORRELATION (3)`, `staop = ltopr`, one `stanumbers`
   entry.

Fallbacks: all non-null values too wide → `stadistinct = -(1 - nullfrac)`,
no slots; only nulls → `stanullfrac = 1.0, stawidth = 0 ("unknown"),
stadistinct = 0 ("unknown")`.

### 2.5 `analyze_mcv_list` (which MCVs to keep)

`analyze.c:analyze_mcv_list(mcv_counts, num_mcv, stadistinct, stanullfrac,
samplerows, totalrows)`. If `samplerows == totalrows || totalrows <= 1` keep
everything. Otherwise, with `ndistinct_table = stadistinct` (negated × totalrows
if negative), iterate from the least common entry:

```
sumcount = sum of counts of all but the last entry
loop:
  selec = 1 - sumcount/samplerows - stanullfrac, clamped [0,1]
  otherdistinct = ndistinct_table - (num_mcv - 1)
  if otherdistinct > 1: selec /= otherdistinct          # freq it would get as a non-MCV (cf. eqsel)
  N = totalrows; n = samplerows; K = N * count_last / n
  variance = n*K*(N-K)*(N-n) / (N*N*(N-1)); stddev = sqrt(variance)
  if count_last > selec*samplerows + 2*stddev + 0.5: break   # keep it and all more common ones
  num_mcv--; sumcount -= count of new last
```

This is a continuity-corrected hypergeometric confidence test ("significantly
more common than the non-MCV frequency"). **Note:** the older rule "keep if
`freq > 1.25 × average frequency`" (PostgreSQL ≤ 10; removed by `b5db1d93d2a`,
first released in PG 11) no longer exists in 18.3;
`analyze_mcv_list` replaced it. The "complete list" case (all sampled distinct
values fit) is retained as described in §2.4 step 5.

### 2.6 `compute_distinct_stats` (types with `=` but no `<`)

Same file. Differences from `compute_scalar_stats`: no sort; values are matched
linearly against a `track[]` of size `track_max = 2 * num_mcv` (Boyer–Moore-ish
counting), `f1 = nonnull_cnt - summultiple`, `d = f1 + nmultiple` for the same
Duj1 formula; only an MCV slot is produced (no histogram, no correlation).
`compute_trivial_stats` produces only `stanullfrac`, `stawidth`, and
`stadistinct = 0`.

### 2.7 Width accounting

`stawidth` is the average of `VARSIZE_ANY` over non-null values including
too-wide ones (`total_width` is accumulated before the width check), so TOASTed
sizes count the on-disk (compressed/toasted) datum size, not the raw size.

### 2.8 `compute_index_stats`

For each index with expressions: evaluate the index predicate (`ExecQual`) and
expression columns per sample row; `numindexrows` = rows passing the predicate;
`thisdata->tupleFract = numindexrows / numrows`; `totalindexrows =
ceil(tupleFract * totalrows)`; call `compute_stats` for each expression column
with `(numindexrows, totalindexrows)`. Plain (non-expression) index columns get
no `pg_statistic` rows; indexes receive only `relpages/reltuples` via
`vac_update_relstats` (§1.1).

### 2.9 Inheritance statistics and stat targets

`ANALYZE` on a table with children or a partitioned table runs `do_analyze_rel`
twice / once with `inh = true`, producing rows with `stainherit = true` from
the union sample. The planner picks the row by `rte->inh`. Per-column targets:
`ALTER TABLE … ALTER COLUMN … SET STATISTICS n` sets `attstattarget`;
`-1`/NULL means `default_statistics_target` (GUC, `PGC_USERSET`, default
`100`, range `1..MAX_STATISTICS_TARGET (10000)`,
`src/backend/utils/misc/guc_tables.c`).

### 2.10 Autovacuum triggering

`src/backend/postmaster/autovacuum.c:relation_needs_vacanalyze`:

```
vacthresh = autovacuum_vacuum_threshold(50) + autovacuum_vacuum_scale_factor(0.2) * reltuples
    capped at autovacuum_vacuum_max_threshold (100000000; -1 disables)
vacinsthresh = autovacuum_vacuum_insert_threshold + insert_scale_factor * reltuples * pcnt_unfrozen
anlthresh = autovacuum_analyze_threshold(50) + autovacuum_analyze_scale_factor(0.1) * reltuples
reltuples < 0 → treated as 0
dovacuum  = force_vacuum || dead_tuples > vacthresh || ins_since_vacuum > vacinsthresh
doanalyze = n_mod_since_analyze > anlthresh
```

Per-table `reloptions` override the GUCs. The inputs are the cumulative-stats
counters `dead_tuples`, `ins_since_vacuum`, `mod_since_analyze` from
`pgstat_fetch_stat_tabentry_ext`; ANALYZE resets `n_mod_since_analyze` via
`pgstat_report_analyze`.

---

## 3. `pg_statistic` layout and friends

### 3.1 `pg_statistic` (`src/include/catalog/pg_statistic.h`, OID 2619)

| column | type | meaning |
|---|---|---|
| `starelid` | oid | relation |
| `staattnum` | int2 | attribute |
| `stainherit` | bool | true if children included |
| `stanullfrac` | float4 | fraction of NULLs |
| `stawidth` | int4 | average width in bytes (non-null) |
| `stadistinct` | float4 | `>0` count, `<0` −fraction of rows, `0` unknown |
| `stakind1..5` | int2 | slot kinds |
| `staop1..5` | oid | operator the slot is meaningful for |
| `stacoll1..5` | oid | collation |
| `stanumbers1..5` | float4[] | numeric payload |
| `stavalues1..5` | anyarray | value payload (element type = column type unless typanalyze overrides) |

`STATISTIC_NUM_SLOTS = 5`. Unique index `pg_statistic_relid_att_inh_index
(starelid, staattnum, stainherit)`, syscache `STATRELATTINH`. `stavaluesN` is
declared `anyarray`; `update_attstats` builds it with `construct_array(values,
n, statypid, statyplen, statypbyval, statypalign)`, so the array's element type
is whatever the typanalyze routine set (`stats->statypid[k]`).

Slot kinds and payload (comments in `pg_statistic.h`):

| kind | value | `staop` | `stavalues` | `stanumbers` |
|---|---|---|---|---|
| `STATISTIC_KIND_MCV` | 1 | `=` | K most common non-null values, decreasing freq | their frequencies (fraction of all rows) |
| `STATISTIC_KIND_HISTOGRAM` | 2 | `<` | M ≥ 2 bounds: MIN … MAX of the non-MCV population, equal-population bins | NULL |
| `STATISTIC_KIND_CORRELATION` | 3 | `<` | NULL | one entry in [−1, 1] |
| `STATISTIC_KIND_MCELEM` | 4 | element `=` | most common elements, sorted in element order | per-element fraction of non-null rows, then two extra entries (min, max freq), optional third (null-element freq) |
| `STATISTIC_KIND_DECHIST` | 5 | element `=` | NULL | histogram of distinct-element counts per row, last entry = average |
| `STATISTIC_KIND_RANGE_LENGTH_HISTOGRAM` | 6 | `<` on subtype | histogram of range lengths | one entry: fraction of empty ranges |
| `STATISTIC_KIND_BOUNDS_HISTOGRAM` | 7 | `<` | histogram of lower and upper bounds (as ranges) | NULL |

### 3.2 `pg_stats` view

`src/backend/catalog/system_views.sql`: `CREATE VIEW pg_stats WITH
(security_barrier)` exposes `schemaname, tablename, attname, inherited,
null_frac, avg_width, n_distinct` and, for each kind `k`, a `CASE WHEN
stakind1 = k THEN stavalues1 … WHEN stakind5 = k THEN stavalues5 END` mapping
to `most_common_vals/most_common_freqs` (1), `histogram_bounds` (2),
`correlation` (3, `stanumbersN[1]`), `most_common_elems/most_common_elem_freqs`
(4), `elem_count_histogram` (5), `range_length_histogram/range_empty_frac` (6),
`range_bounds_histogram` (7). Filtered by `NOT attisdropped`,
`has_column_privilege(...,'select')`, and RLS not active.

### 3.3 Extended-statistics catalogs

`pg_statistic_ext` (`src/include/catalog/pg_statistic_ext.h`, OID 3381):
`stxrelid, stxname, stxnamespace, stxowner, stxkeys int2vector, stxstattarget
int2 (nullable), stxkind char[] , stxexprs pg_node_tree`. Kinds:
`STATS_EXT_NDISTINCT 'd'`, `STATS_EXT_DEPENDENCIES 'f'`, `STATS_EXT_MCV 'm'`,
`STATS_EXT_EXPRESSIONS 'e'`.

`pg_statistic_ext_data` (OID 3429): `stxoid, stxdinherit bool, stxdndistinct
pg_ndistinct, stxddependencies pg_dependencies, stxdmcv pg_mcv_list,
stxdexpr pg_statistic[]` — unique on `(stxoid, stxdinherit)`, syscache
`STATEXTDATASTXOID`.

`pg_stats_ext` view (`system_views.sql`) joins these, expands `stxkeys` to
`attnames`, exposes `kinds, inherited, n_distinct, dependencies`, and unnests
`pg_mcv_list_items(stxdmcv)` into `most_common_vals, most_common_val_nulls,
most_common_freqs, most_common_base_freqs`.

---

## 4. `examine_variable` / `VariableStatData` (`src/backend/utils/adt/selfuncs.c`)

`VariableStatData` fields: `var` (stripped node), `rel` (RelOptInfo or NULL),
`statsTuple` (pg_statistic HeapTuple or NULL), `freefunc`, `vartype`,
`atttype/atttypmod`, `isunique`, `acl_ok`.

`examine_variable(root, node, varRelid, vardata)`:

1. `basenode = strip_all_phvs_deep(node)`, then strip `RelabelType`.
2. If `basenode` is a `Var` and `varRelid == 0 || varRelid == var->varno`:
   `rel = find_base_rel`, `isunique = has_unique_index(rel, varattno)`
   (`plancat.c:has_unique_index`: a unique single-key-column index on that
   attno with no predicate or `predOK`), then `examine_simple_variable`.
3. Otherwise it is an expression. Determine `varnos` (minus
   `outer_join_rels`); if exactly one base rel → `onerel` and `rel`; if several
   and `varRelid == 0` → `rel = find_join_rel`.
4. With `onerel`: search `onerel->indexlist` for an expression index whose
   expression `equal()`s the node (after stripping RelabelType). On match:
   `isunique = true` if the index is unique, single-key, the match is column 0
   and (no predicate or `predOK`); stats via `get_index_stats_hook`, else if the
   index has no predicate `SearchSysCache3(STATRELATTINH, indexoid, pos+1,
   false)`; `acl_ok = all_rows_selectable(root, index->rel->relid, NULL)`.
   Partial-index expression stats are deliberately not used.
5. Still with `onerel` and no tuple: search `onerel->statlist` for a
   `STATS_EXT_EXPRESSIONS` object with `inherit == rte->inh` whose expression
   list contains the node → `statext_expressions_load(statOid, inh, pos)`.

`examine_simple_variable(root, var, vardata)`:

* `get_relation_stats_hook` first.
* `RTE_RELATION`: `SearchSysCache3(STATRELATTINH, relid, varattno, rte->inh)`;
  `acl_ok = all_rows_selectable(root, varno, {varattno})`.
* `RTE_SUBQUERY` (non-inh) or `RTE_CTE` (non-self-reference): locate the
  subquery's `PlannerInfo` (`rel->subroot`, or through `cte_plan_ids` →
  `glob->subroots`), give up if `setOperations` or `groupingSets`; take the
  output `TargetEntry` for `varattno` (from `returningList` if present, else
  `targetList`). If the subquery has `DISTINCT` on exactly that one column or
  `GROUP BY` exactly that one column → `isunique = true` and return (no
  stats). If `security_barrier` → return. If the tlist expression is a plain
  `Var` with `varlevelsup == 0` → recurse `examine_simple_variable(subroot,
  var, vardata)`. Whole-row (`varattno == 0`) → nothing.
* Everything else (functions, VALUES, joins) → no stats.

`statistic_proc_security_check(vardata, func_oid)`: stats are usable if
`acl_ok`, else only if the operator's function is `proleakproof`
(`get_func_leakproof`).

### 4.1 `get_variable_numdistinct(vardata, &isdefault)`

```
isdefault = false
if statsTuple:              stadistinct = stats->stadistinct; stanullfrac = stats->stanullfrac
elif vartype == BOOLOID:    stadistinct = 2.0
elif rel is RTE_VALUES:     stadistinct = -1.0
elif var is Var:            ctid → -1.0; tableoid → 1.0; else 0.0
else:                       stadistinct = 0.0
if vardata->isunique:       stadistinct = -1.0 * (1.0 - stanullfrac)
if stadistinct > 0:         return clamp_row_est(stadistinct)
if rel == NULL:             isdefault = true; return DEFAULT_NUM_DISTINCT (200)
ntuples = rel->tuples
if ntuples <= 0:            isdefault = true; return 200
if stadistinct < 0:         return clamp_row_est(-stadistinct * ntuples)
if ntuples < 200:           return clamp_row_est(ntuples)
isdefault = true; return 200
```

Note the "var is unique → nd = (1 − nullfrac)·ntuples" rule and that the
negative-fraction convention is applied against `rel->tuples` (the whole
relation), not `rel->rows`.

### 4.2 `get_variable_range(root, vardata, sortop, collation, &min, &max)`

Requires a stats tuple and security check on the sort operator. Uses the
HISTOGRAM slot whose `staop == sortop` and `stacoll == collation`
(first/last bound), else any histogram slot scanned with the sort operator
(`get_stats_slot_range`), then widens with MCV values (always if a histogram
was found; if no histogram, only when `sum(mcv freqs) + nullfrac > 0.99999`).
No index probe here (`get_actual_variable_range` call is `#ifdef NOT_USED`).

### 4.3 `get_actual_variable_range` (plan-time endpoint refresh)

Called from `ineq_histogram_selectivity` only when the binary search reaches
the first or last histogram bucket (or the histogram has exactly two bounds).
Requires a base relation (not partitioned) with a non-partial, non-hypothetical
btree-orderable index whose first column matches the variable, `canreturn[0]`,
and matching collation. It opens the heap and index with `NoLock` and runs
`get_actual_variable_endpoint` in the appropriate scan direction with a
`SK_SEARCHNOTNULL` scankey, using `SnapshotNonVacuumable` initialised with
`GlobalVisTestFor(heapRel)` — i.e. it accepts every index entry whose heap
tuple is not yet removable, which is exactly the set a subsequent
`kill_prior_tuple` hint could not have pruned, so the probe converges even when
the extreme values were recently deleted. It stops after
`VISITED_PAGES_LIMIT = 100` distinct heap pages (returning "no data") to bound
the cost. Rationale: histogram endpoints drift quickly for monotonically
growing keys (timestamps, serials), and a `<`/`>` comparison against a value
outside the recorded range would otherwise be clamped to the `0.01/(nbounds-1)`
cutoff instead of the true near-zero/near-one selectivity.

---

## 5. Restriction selectivity functions (`selfuncs.c`)

### 5.1 Constants (`src/include/utils/selfuncs.h`)

| constant | value | used by |
|---|---|---|
| `DEFAULT_EQ_SEL` | 0.005 | eqsel with no var/const, neqjoinsel fallback |
| `DEFAULT_INEQ_SEL` | 0.3333333333333333 | scalarineqsel without stats, scalarltjoinsel etc. |
| `DEFAULT_RANGE_INEQ_SEL` | 0.005 | clauselist range pairing fallback |
| `DEFAULT_MULTIRANGE_INEQ_SEL` | 0.005 | multirange operators |
| `DEFAULT_MATCH_SEL` | 0.005 | LIKE/regex without stats |
| `DEFAULT_MATCHING_SEL` | 0.010 | matchingsel (generic "matching" operators) |
| `DEFAULT_NUM_DISTINCT` | 200 | get_variable_numdistinct |
| `DEFAULT_UNK_SEL` | 0.005 | IS NULL / IS UNKNOWN without stats |
| `DEFAULT_NOT_UNK_SEL` | 0.995 | IS NOT NULL / IS NOT UNKNOWN |

`like_support.c` pattern constants: `FIXED_CHAR_SEL 0.20`, `CHAR_RANGE_SEL
0.25`, `ANY_CHAR_SEL 0.9`, `FULL_WILDCARD_SEL 5.0`, `PARTIAL_WILDCARD_SEL 2.0`.

### 5.2 `eqsel` / `eqsel_internal` / `var_eq_const`

`eqsel_internal(fcinfo, negate)`: `get_restriction_variable` splits the args
into a var side and an "other" side (if neither side is a single-rel variable
→ `DEFAULT_EQ_SEL`, or `1 - DEFAULT_EQ_SEL` when negated); if the other side
is a `Const` → `var_eq_const`, else `var_eq_non_const`.

`var_eq_const(vardata, oproid, collation, constval, constisnull, varonleft,
negate)`:

```
if constisnull: return 0.0
nullfrac = stats ? stanullfrac : 0
if vardata->isunique and rel and rel->tuples >= 1:
    selec = 1 / rel->tuples
elif statsTuple and security check on get_opcode(oproid):
    if MCV slot: evaluate op(mcv_value, const) for each entry (FunctionCallInvoke with collation)
    if match:  selec = numbers[i]                              # exact MCV hit
    else:
        sumcommon = Σ numbers
        selec = 1 - sumcommon - nullfrac; CLAMP
        otherdistinct = get_variable_numdistinct() - nnumbers
        if otherdistinct > 1: selec /= otherdistinct
        if nnumbers > 0 and selec > numbers[last]: selec = numbers[last]   # cap at least-common MCV
else:
    selec = 1 / get_variable_numdistinct()
if negate: selec = 1 - selec - nullfrac
CLAMP_PROBABILITY(selec)
```

`var_eq_non_const`: unique → `1/tuples`; with stats → `(1 - nullfrac) /
ndistinct` capped at the *largest* MCV frequency `numbers[0]`; else
`1/ndistinct`. `neqsel` = `eqsel_internal(fcinfo, true)`.

### 5.3 `scalarineqsel` and helpers

`scalarineqsel(root, operator, isgt, iseq, collation, vardata, constval,
consttype)`:

* No stats: if the var is `ctid` (`SelfItemPointerAttributeNumber`), compute
  the fraction of the table before/after the block number
  (`block / (pages - 0.5)`, half density on the last page); otherwise
  `DEFAULT_INEQ_SEL`.
* `mcv_selec = mcv_selectivity(vardata, &opproc, collation, constval, true,
  &sumcommon)`: sum of MCV frequencies for which `op(value, const)` is true;
  `sumcommon` = all MCV frequencies.
* `hist_selec = ineq_histogram_selectivity(...)` (below); `-1` if unavailable.
* `selec = 1 - stanullfrac - sumcommon; selec *= (hist_selec >= 0 ?
  hist_selec : 0.5); selec += mcv_selec; CLAMP`.

`ineq_histogram_selectivity(root, vardata, opoid, opproc, isgt, iseq,
collation, constval, consttype)` — requires a HISTOGRAM slot with
`nvalues > 1`, `stacoll == collation`, and
`comparison_ops_are_compatible(staop, opoid)`:

1. Binary search `lobound=0, hibound=nvalues` for the first bound not
   satisfying `op(bound, const)` (the test is inverted when `isgt`). When the
   probe hits index 0 or `nvalues-1` (and `nvalues > 2`), or when
   `nvalues == 2`, the corresponding endpoint is refreshed by
   `get_actual_variable_range` (§4.3); `have_end` records success.
2. `lobound <= 0` → `histfrac = 0`; `lobound >= nvalues` → `1`; otherwise with
   `i = lobound`: if `i == 1 || isgt == iseq` compute `eq_selec =
   1 / (ndistinct - nMCV)` (0 if ≤ 1). `convert_to_scalar(constval, consttype,
   collation, &val, values[i-1], values[i], vartype, &low, &high)` maps the
   constant and the two bin bounds to doubles (numeric types directly; strings
   via `convert_string_to_scalar` on a common prefix base; bytea; datetime →
   seconds; network types) — on failure or degenerate bin `binfrac = 0.5`,
   else `binfrac = (val - low) / (high - low)` clamped, `0.5` if NaN/out of
   range. `histfrac = ((i - 1) + binfrac) / (nvalues - 1)`; if `i == 1`
   add `eq_selec * (1 - binfrac)`; if `isgt == iseq` subtract `eq_selec`
   (removes/adds the boundary value's own probability).
3. `hist_selec = isgt ? 1 - histfrac : histfrac`. If `have_end` →
   `CLAMP_PROBABILITY`; else clamp into `[cutoff, 1 - cutoff]` with
   `cutoff = 0.01 / (nvalues - 1)` (never claim zero rows from a stale
   histogram).
4. Non-compatible operator with a histogram (`nvalues > 1`): fraction of bounds
   satisfying the operator, same cutoff clamp.

`histogram_selectivity(vardata, opproc, collation, constval, varonleft,
min_hist_size, n_skip, &hist_size)` (used by pattern ops) returns the fraction
of histogram bounds (skipping `n_skip` at each end) satisfying the operator,
or `-1` if fewer than `min_hist_size` bounds.

`scalarltsel/scalarlesel/scalargtsel/scalargesel` (`scalarineqsel_wrapper`)
handle the var/const orientation (`isgt` flips when the constant is on the
left), refuse non-constant others (→ `DEFAULT_INEQ_SEL`), and reject
pseudo-constants that are not `Const`.

### 5.4 NULL and boolean tests

* `nulltestsel`: with stats `IS NULL → stanullfrac`, `IS NOT NULL → 1 -
  stanullfrac`; system columns (`varattno < 0`) → 0 / 1; else
  `DEFAULT_UNK_SEL / DEFAULT_NOT_UNK_SEL`.
* `booltestsel`: with an MCV slot: `freq_true` = frequency of `true` (if
  `values[0]` is true it is `numbers[0]`, else `1 - numbers[0] - nullfrac`),
  `freq_false = 1 - freq_true - nullfrac`; then `IS TRUE → freq_true`,
  `IS NOT TRUE → 1 - freq_true`, `IS FALSE → freq_false`, `IS NOT FALSE →
  1 - freq_false`, `IS UNKNOWN → nullfrac`, `IS NOT UNKNOWN → 1 - nullfrac`.
  With stats but no MCV: `IS TRUE/FALSE → (1 - nullfrac)/2`, `IS NOT TRUE/
  NOT FALSE → (nullfrac + 1)/2`. Without stats: UNKNOWN → defaults; `IS TRUE`
  and `IS NOT FALSE` → `clause_selectivity(arg)`, the others → `1 - that`.
* `boolvarsel` (bare boolean Var/expression): with stats `var_eq_const(...,
  BooleanEqualOperator, true, …)`, else `0.5`.

### 5.5 Pattern operators (`src/backend/utils/adt/like_support.c`)

`patternsel_common(root, oprid, opfuncid, args, varRelid, collation, ptype,
negate)`: default `DEFAULT_MATCH_SEL` (or its complement); requires var on the
left and a non-null `text`/`bytea` constant; supports var types text, name,
bpchar, bytea (choosing `eqopr/ltopr/geopr`). `pattern_fixed_prefix` extracts
the fixed prefix and a `rest_selec` heuristic (`like_selectivity`: product of
`FIXED_CHAR_SEL` per literal char, `ANY_CHAR_SEL` per `_`,
`FULL_WILDCARD_SEL` per `%`, capped at 1; `regex_selectivity`: `regex_selectivity_sub`
plus `FULL_WILDCARD_SEL` unless anchored with `$`, divided by
`FIXED_CHAR_SEL^prefixlen`).

```
if pstatus == Pattern_Prefix_Exact: result = var_eq_const(prefix)
else:
    selec = histogram_selectivity(min_hist_size=10, n_skip=1, &hist_size)   # fraction of bounds matching the pattern
    if hist_size < 100:
        prefixsel = Partial prefix ? prefix_selectivity(...) : 1.0
        heursel = prefixsel * rest_selec
        selec = (selec < 0) ? heursel : selec*hist_size/100 + heursel*(1 - hist_size/100)
    clamp selec to [0.0001, 0.9999]
    mcv_selec = mcv_selectivity(pattern operator over MCVs, &sumcommon)
    result = selec * (1 - nullfrac - sumcommon) + mcv_selec
if negate: result = 1 - result - nullfrac
```

`prefix_selectivity`: `ineq_histogram_selectivity(geopr, isgt=true,
iseq=true, prefix)`; `make_greater_string(prefix, ltopr, collation)` builds
the smallest string greater than every string with that prefix (increment
last byte/character, truncating, verified with `<`); if it succeeds, `topsel =
ineq_histogram_selectivity(ltopr, false, false, greaterstr)` and
`prefixsel = topsel + prefixsel - 1`; finally `prefixsel = max(prefixsel,
var_eq_const(prefix))`.

### 5.6 `scalararraysel` (`= ANY(array)`, `<> ALL`, …)

Determines `isEquality/isInequality` from the element type's default `=`
operator. For equality/inequality on non-join clauses it first tries
`scalararraysel_containment` (uses MCELEM stats of an array-typed variable).
Otherwise it looks up `oprrest` (or `oprjoin`) and, for a `Const` array or a
non-multidim `ArrayExpr`, applies the per-element selectivity `s2` and
combines:

```
s1 = s1disjoint = useOr ? 0.0 : 1.0
for each element:
    if useOr:  s1 = s1 + s2 - s1*s2;   if isEquality:   s1disjoint += s2
    else:      s1 = s1 * s2;           if isInequality: s1disjoint += s2 - 1.0
if (useOr ? isEquality : isInequality) and 0 <= s1disjoint <= 1: s1 = s1disjoint
```

I.e. `= ANY(list)` is the plain *sum* of element selectivities when it stays
in range (elements are assumed disjoint), `<> ALL` is `1 - Σ(1 - s2)`. For a
non-constant array the element selectivity is evaluated once against a dummy
`CaseTestExpr` and combined `10` times (`estimate_array_length` default when
unknown). `estimate_array_length` returns the actual length for a `Const` or
`ArrayExpr`, otherwise `10`.

### 5.7 `rowcomparesel`

Uses only the first column pair `(largs[0], rargs[0])` with the first
operator; `is_join_clause` iff `varRelid == 0 && sjinfo != NULL &&
NumRelids > 1`; then `join_selectivity` or `restriction_selectivity` on that
pair.

---

## 6. `clauselist_selectivity` (`src/backend/optimizer/path/clausesel.c`)

`clauselist_selectivity(root, clauses, varRelid, jointype, sjinfo)` =
`clauselist_selectivity_ext(..., use_extended_stats = true)`:

1. Single clause → `clause_selectivity_ext`.
2. `rel = find_single_rel_for_clauses(root, clauses)`; if
   `use_extended_stats && rel && rel->rtekind == RTE_RELATION &&
   rel->statlist != NIL` → `s1 = statext_clauselist_selectivity(root, clauses,
   varRelid, jointype, sjinfo, rel, &estimatedclauses, is_or=false)` (§9.3);
   clauses covered are marked in the `estimatedclauses` bitmap by list index.
3. For every remaining clause: `s2 = clause_selectivity_ext(...)`.
   Pseudoconstant RestrictInfos multiply directly. A binary `OpExpr` with
   exactly one base rel and a pseudo-constant other side whose `oprrest` is
   `scalarltsel/scalarlesel` (high bound) or `scalargtsel/scalargesel` (low
   bound) is queued via `addRangeClause(&rqlist, clause, varonleft, isLTsel,
   s2)`; keyed by `equal(var)`; a repeated bound of the same kind keeps the
   **smaller** selectivity. Everything else: `s1 *= s2`.
4. Range pairing: for each `rqlist` entry with both bounds:
   `if hibound == DEFAULT_INEQ_SEL || lobound == DEFAULT_INEQ_SEL: s2 =
   DEFAULT_RANGE_INEQ_SEL` else `s2 = hibound + lobound - 1.0 +
   nulltestsel(IS_NULL, var)`; if `s2 <= 0`: `s2 < -0.01 →
   DEFAULT_RANGE_INEQ_SEL` (contradictory bounds from stale/default stats),
   else `1.0e-10`. `s1 *= s2`. One-sided entries multiply their single bound.

`clause_selectivity_ext(root, clause, varRelid, jointype, sjinfo,
use_extended_stats)`:

* `RestrictInfo` wrapper: `pseudoconstant && clause not Const → 1.0`; the
  cache is used when `varRelid == 0 || num_base_rels == 0 || (num_base_rels
  == 1 && varRelid ∈ clause_relids)`: `JOIN_INNER → rinfo->norm_selec`, other
  jointypes → `rinfo->outer_selec` (both `-1` when unset); on a miss the result
  is stored back. If `rinfo->orclause` is set (an OR with per-arm
  RestrictInfos) it is used instead of `clause`.
* `Var` (level 0, matching varRelid) → `boolvarsel`.
* `Const` → `constisnull ? 0 : (value ? 1 : 0)`; `Param` → after
  `estimate_expression_value`, same as Const if it folded, else stays 0.5.
* `NOT` → `1 - s(arg)`; `AND` → `clauselist_selectivity_ext(args)`;
  `OR` → `clauselist_selectivity_or`: extended stats first with
  `is_or = true`, then `s1 = s1 + s2 - s1*s2` across the remaining arms.
* `OpExpr`/`DistinctExpr`: `treat_as_join_clause` is true iff `varRelid == 0
  && sjinfo != NULL && (rinfo ? num_base_rels > 1 : NumRelids > 1)` →
  `join_selectivity(opno, args, inputcollid, jointype, sjinfo)` (calls
  `pg_operator.oprjoin`), else `restriction_selectivity(opno, args,
  inputcollid, varRelid)` (`oprrest`). Missing estimator → 0.5.
  `DistinctExpr` → `1 - s1`.
* `FuncExpr` → `function_selectivity` (`prosupport` request), default 0.5.
* `ScalarArrayOpExpr` → `scalararraysel`; `RowCompareExpr` →
  `rowcomparesel`; `NullTest` → `nulltestsel`; `BooleanTest` → `booltestsel`;
  `CurrentOfExpr` → `1 / crel->tuples`; `RelabelType`, `CoerceToDomain` →
  recurse on the argument; anything else → `boolvarsel`.

---

## 7. Join selectivity (`selfuncs.c`)

### 7.1 `eqjoinsel`

`get_join_variables(root, args, sjinfo, &vardata1, &vardata2,
&join_is_reversed)`; `nd1/nd2 = get_variable_numdistinct`; MCV slots are
loaded for both sides only if **both** sides have an MCV slot and pass the
security check with the operator's function (`get_mcv_stats`). Then
`selec_inner = eqjoinsel_inner(...)`, and by `sjinfo->jointype`:

* `JOIN_INNER/LEFT/FULL` → `selec_inner`.
* `JOIN_SEMI/ANTI` → `inner_rel = find_join_input_rel(root,
  sjinfo->min_righthand)`; `eqjoinsel_semi` on the (possibly commuted, when
  `join_is_reversed`) variables; then `selec = min(selec, inner_rel->rows *
  selec_inner)`.

### 7.2 `eqjoinsel_inner`

Both MCV lists present:

```
for i in mcv1: for j in mcv2 not yet matched:
    if eq(values1[i], values2[j]): hasmatch1[i] = hasmatch2[j] = true
                                   matchprodfreq += numbers1[i]*numbers2[j]; nmatches++; break
CLAMP(matchprodfreq)
matchfreq1 = Σ numbers1[matched]; unmatchfreq1 = Σ numbers1[unmatched]   (same for side 2), clamped
otherfreq1 = 1 - nullfrac1 - matchfreq1 - unmatchfreq1   (clamped; same for 2)
totalsel1 = matchprodfreq
if nd2 > nvalues2: totalsel1 += unmatchfreq1 * otherfreq2 / (nd2 - nvalues2)
if nd2 > nmatches: totalsel1 += otherfreq1 * (otherfreq2 + unmatchfreq2) / (nd2 - nmatches)
totalsel2 = symmetric with sides swapped
selec = min(totalsel1, totalsel2)
```

The equality is evaluated with the join operator's own function
(`fmgr_info(opfuncoid)`), which handles cross-type operators. No MCVs:
`selec = (1 - nullfrac1)(1 - nullfrac2) / max(nd1, nd2)`. `nd` values already
carry the `isdefault` / `ntuples` clamps from `get_variable_numdistinct`;
`eqjoinsel_inner` itself does not clamp to `rel->rows`.

### 7.3 `eqjoinsel_semi`

```
if vardata2->rel and nd2 >= vardata2->rel->rows: nd2 = rel->rows; isdefault2 = false
if nd2 >= inner_rel->rows:                       nd2 = inner_rel->rows; isdefault2 = false
if both MCVs and opfuncoid valid:
    clamped_nvalues2 = min(nvalues2, nd2)
    pairwise match (as above but only over the first clamped_nvalues2 inner MCVs)
    matchfreq1 = Σ numbers1[matched]
    if !isdefault1 and !isdefault2:
        nd1 -= nmatches; nd2 -= nmatches
        uncertainfrac = (nd1 <= nd2 or nd2 < 0) ? 1.0 : nd2 / nd1
    else: uncertainfrac = 0.5
    uncertain = 1 - matchfreq1 - nullfrac1 (clamped)
    selec = matchfreq1 + uncertainfrac * uncertain
else:
    if !isdefault1 and !isdefault2: selec = (nd1 <= nd2 or nd2 < 0) ? 1 - nullfrac1 : (nd2/nd1)*(1 - nullfrac1)
    else: selec = 0.5 * (1 - nullfrac1)
```

Semantics: the fraction of *outer* rows that have at least one match.

### 7.4 `neqjoinsel`, inequality join operators

`neqjoinsel`: for `JOIN_SEMI/ANTI` → `1 - nullfrac` of the outer variable
(practically every outer row finds some unequal inner row); otherwise
`1 - eqjoinsel(negator)` (or `1 - DEFAULT_EQ_SEL` if no negator).
`scalarltjoinsel/scalarlejoinsel/scalargtjoinsel/scalargejoinsel` return
`DEFAULT_INEQ_SEL` unconditionally.

### 7.5 `mergejoinscansel`

`mergejoinscansel(root, clause, opfamily, cmptype, nulls_first, &leftstart,
&leftend, &rightstart, &rightend)` estimates which fractions of each sorted
input a merge join must read. It resolves `<`, `<=` (or `>`, `>=` for
descending) members of the opfamily for both directions, obtains
`get_variable_range` for both sides, then:

```
leftend   = scalarineqsel(leop,  isgt, iseq=true,  leftvar,  rightmax)   # fraction of left  <= right max
rightend  = scalarineqsel(revleop, …,             rightvar, leftmax)
if leftend > rightend: leftend = 1  elif leftend < rightend: rightend = 1  else both = 1
leftstart = scalarineqsel(ltop,  isgt, iseq=false, leftvar,  rightmin)   # fraction of left  <  right min
rightstart = scalarineqsel(revltop, …,            rightvar, leftmin)
if leftstart < rightstart: leftstart = 0  elif leftstart > rightstart: rightstart = 0 else both = 0
if nulls_first: *leftstart += stanullfrac; CLAMP; *leftend += stanullfrac; CLAMP
                (plain addition, no (1-start) scaling, applied to BOTH start and end;
                 selfuncs.c:3230-3249)
```

A `DEFAULT_INEQ_SEL` result is treated as "unknown" and leaves the default
(0/1) in place; degenerate `start >= end` is reset to 0/1.

### 7.6 `estimate_hash_bucket_stats`

`estimate_hash_bucket_stats(root, hashkey, nbuckets, &mcv_freq,
&bucketsize_frac)`: `mcv_freq = numbers[0]` of the MCV slot (0 if none);
`ndistinct = get_variable_numdistinct`; if `isdefault`: `bucketsize_frac =
max(0.1, mcv_freq)` and return. Else `avgfreq = (1 - stanullfrac) /
ndistinct`; scale `ndistinct *= rel->rows / rel->tuples` (clamped ≥ 1);
`estfract = 1 / (ndistinct > nbuckets ? nbuckets : ndistinct)`; if
`mcv_freq > avgfreq`: `estfract *= mcv_freq / avgfreq`; clamp to
`[1e-6, 1]`.

---

## 8. Group / distinct estimation

### 8.1 `estimate_num_groups(root, groupExprs, input_rows, pgset, estinfo)`

```
input_rows = clamp_row_est(input_rows)
if groupExprs == NIL or (pgset and *pgset == NIL): return 1.0
numdistinct = 1.0
for each groupexpr (index i; skipped if pgset given and i ∉ *pgset):
    srf_multiplier = max(srf_multiplier, expression_returns_set_rows(groupexpr))
    if exprType == BOOLOID: numdistinct *= 2; continue
    examine_variable(groupexpr)
    if statsTuple or isunique: varinfos = add_unique_group_var(...); continue
    vars = pull_var_clause(groupexpr, RECURSE aggregates/windowfuncs/placeholders)
    if vars == NIL: if volatile: return input_rows; else continue      # constant
    for each var: add_unique_group_var(var)
if varinfos == NIL: return clamp(ceil(numdistinct * srf_multiplier), 1, input_rows)
repeat (group varinfos by rel):
    reldistinct = 1; relmaxndistinct = 1; relvarcount = 0
    while relvarinfos:
        if estimate_multivariate_ndistinct(root, rel, &relvarinfos, &mv):   # consumes covered vars
            reldistinct *= mv; relmaxndistinct = max(.., mv); relvarcount++
        else: for each remaining varinfo: reldistinct *= ndistinct; track max; relvarcount++;
              flag SELFLAG_USED_DEFAULT if isdefault; relvarinfos = NIL
    if rel->tuples > 0:
        clamp = rel->tuples
        if relvarcount > 1:
            clamp *= 0.1
            if clamp < relmaxndistinct: clamp = min(relmaxndistinct, rel->tuples)
        reldistinct = min(reldistinct, clamp)
        if reldistinct > 0 and rel->rows < rel->tuples:
            reldistinct *= 1 - pow((rel->tuples - rel->rows) / rel->tuples, rel->tuples / reldistinct)
        reldistinct = clamp_row_est(reldistinct)
        numdistinct *= reldistinct
until no varinfos left
numdistinct = ceil(numdistinct * srf_multiplier); clamp to [1, input_rows]
```

`add_unique_group_var`: `ndistinct = get_variable_numdistinct`; drops a var
that is `equal()` to one already present, and among vars of different rels
that are `exprs_known_equal` keeps the one with the **smaller** ndistinct.
The restriction-scaling formula is the classic "expected distinct values in a
random subset" (Yao): `D · (1 − (1 − rows/tuples)^(tuples/D))`.

### 8.2 `estimate_multivariate_ndistinct` (§9.5)

Selects, among `STATS_EXT_NDISTINCT` objects with `inherit == rte->inh`, the
one covering the most grouping vars/expressions (at least 2; expression
matches win ties), loads `statext_ndistinct_load`, finds the `MVNDistinctItem`
whose attribute set equals the matched set, returns its `ndistinct` and removes
the covered varinfos from the list.

### 8.3 Consumers

* `GROUP BY` / grouping sets: `src/backend/optimizer/plan/planner.c:get_number_of_groups`
  (calls `estimate_num_groups` with `pgset` per grouping set and sums).
* `DISTINCT`: `planner.c:create_distinct_paths` →
  `estimate_num_groups(root, distinctExprs, path_rows, NULL, NULL)`.
* `create_unique_path` (`src/backend/optimizer/util/pathnode.c`): rows =
  `estimate_num_groups(root, sjinfo->semi_rhs_exprs, rel->rows, …)`, or
  `rel->rows` when the rel is already provably unique.
* Set operations: `src/backend/optimizer/prep/prepunion.c:recurse_set_operations`
  → `*pNumGroups = estimate_num_groups(subroot, tlist exprs, rows, …)` for
  `UNION`/`INTERSECT`/`EXCEPT` group counts.
* `LIMIT/OFFSET` (`pathnode.c:adjust_limit_rows_costs`): `offset_rows` =
  `offset_est` if known, else `clamp_row_est(input_rows * 0.10)`;
  `count_rows` = `count_est` if known, else `clamp_row_est(input_rows * 0.10)`;
  both capped by the input rows; result `>= 1`. `planner.c:preprocess_limit`
  turns these into `tuple_fraction` / `limit_tuples` for path selection.
* Hash agg memory: `estimate_hashagg_tablesize(root, path, agg_costs,
  dNumGroups)` = `hash_agg_entry_size(nTrans, width, transitionSpace) *
  dNumGroups` (`selfuncs.c:4178-4195`). `hash_agg_entry_size`
  (`nodeAgg.c:1701-1731`) *already* includes
  `MAXALIGN(MAXALIGN(SizeofMinimalTupleHeader) + tupleWidth)`, so adding those
  terms again — the pre-PG16 shape — double-counts the header and width.
* Incremental sort / Memoize / window partitions
  (`costsize.c:cost_incremental_sort`, `cost_memoize_rescan`,
  `get_windowclause_startup_tuples`) all call `estimate_num_groups`.

---

## 9. Extended statistics (`src/backend/statistics/`)

### 9.1 Kinds and build

`CREATE STATISTICS name (ndistinct | dependencies | mcv) ON cols/exprs FROM
table` stores a `pg_statistic_ext` row; kind `'e'` is added implicitly when
expressions appear. `extended_stats.c:BuildRelationExtStatistics(onerel, inh,
totalrows, numrows, rows, natts, vacattrstats)` runs during `do_analyze_rel`
using the **same sample**: for each object, `stattarget =
statext_compute_stattarget(stxstattarget, nattrs, stats)` (explicit target,
else the max of the columns' `attstattarget`, else
`default_statistics_target`); `data = make_build_data(...)` (per-attribute
Datum arrays including expression values); then per kind:

* `'d'`: `mvdistinct.c:statext_ndistinct_build(totalrows, data)` — for every
  attribute combination of size 2..k, `ndistinct_for_combination` sorts the
  sample on the combination, counts groups `d` and singletons `f1`, and applies
  `estimate_ndistinct(totalrows, numrows, d, f1) = n*d / ((n - f1) + f1*n/N)`
  (the same Duj1 formula), clamped to `[d, N]`.
* `'f'`: `dependencies.c:statext_dependencies_build(data)` — for each ordered
  attribute list `(a1..ak-1) → ak` (`k = 2..nattrs`),
  `dependency_degree` sorts the sample on all `k` columns, walks groups of the
  first `k-1` columns and counts a group as *supporting* if the last column is
  constant within it: `degree = n_supporting_rows / numrows`. Dependencies
  with `degree == 0` are dropped.
* `'m'`: `mcv.c:statext_mcv_build(data, totalrows, stattarget)` —
  `build_sorted_items` + `build_distinct_groups` (sorted by count
  descending); `nitems = min(stattarget, ngroups)`; entries with `count <
  mincount` are cut, where
  `get_mincount_for_mcv_list(n, N) = n(N - n) / (N - n + 0.04 n (N - 1))`
  (a 4 % relative-error criterion); each item stores `values[], isnull[],
  frequency = count/numrows`, and `base_frequency = Π_j
  (count of values[j] in column j / numrows)` (the independence prediction).
* `'e'`: `build_expr_data` + `compute_expr_stats` runs the normal per-column
  `compute_stats` on each expression and serialises the resulting
  `pg_statistic` tuples into `stxdexpr`.

`statext_store(statOid, inh, ndistinct, dependencies, mcv, exprstats, stats)`
writes/updates the `pg_statistic_ext_data` row (transactionally).

### 9.2 Clause compatibility (`extended_stats.c:statext_is_compatible_clause`)

The outer routine rejects clauses that are not restrictions on `relid`
(`RestrictInfo` with `clause_relids != {relid}`, pseudoconstants, volatile
functions) and enforces the security rule (leakproof operators or SELECT
privilege on the table without RLS). `statext_is_compatible_clause_internal`
accepts: a bare `Var` (user attribute of `relid`, level 0); a binary `OpExpr`
`expr op Const` (or reversed) whose `oprrest` is one of `eqsel, neqsel,
scalarltsel, scalarlesel, scalargtsel, scalargesel`; a `ScalarArrayOpExpr`
with the expression on the left and the same operator set; `AND/OR/NOT` of
compatible clauses; `NullTest`; and any other expression (recorded as an
expression to be matched against `stxexprs`). Attribute numbers are collected
into `attnums` and expressions into `exprs`.

### 9.3 `statext_clauselist_selectivity`

```
sel = statext_mcv_clauselist_selectivity(root, clauses, …, estimatedclauses, is_or)
if is_or: return sel
sel *= dependencies_clauselist_selectivity(root, clauses, …, estimatedclauses)
```

`statext_mcv_clauselist_selectivity`: compute compatibility per clause; loop
`choose_best_statistics(statlist, STATS_EXT_MCV, rte->inh, list_attnums,
list_exprs, nclauses)` — pick the object covering the most distinct
attributes+expressions (need ≥ 2 … `best_num_matched` starts at 2 so a single
covered attribute does not qualify), ties broken by the fewest keys. Collect
the clauses fully covered by that object (`stat_clauses`), mark them in
`estimatedclauses`, remember which are "simple" (single column), and:

* AND: `simple_sel = clauselist_selectivity_ext(stat_clauses,
  use_extended_stats=false)`; `mcv_sel = mcv_clauselist_selectivity(...)`
  (`mcv.c`): load the list, `mcv_get_match_bitmap` evaluates every clause on
  every item (nulls handled per NullTest semantics), `mcv_sel = Σ frequency of
  matching items`, `basesel = Σ base_frequency of matching items`, `totalsel =
  Σ frequency of all items`; `stat_sel = mcv_combine_selectivities(simple_sel,
  mcv_sel, basesel, totalsel)`:
  `other_sel = clamp(simple_sel - basesel)`, capped at `1 - totalsel`;
  `sel = clamp(mcv_sel + other_sel)`. The whole-list result multiplies `sel`.
* OR: per clause, accumulate `simple_or_sel` with the OR formula, the MCV OR
  match (`mcv_clause_selectivity_or`) tracking overlaps, and combine
  `stat_sel += clause_sel - overlap_sel`; finally `sel = sel + stat_sel -
  sel*stat_sel`.

`dependencies_clauselist_selectivity` (`dependencies.c`): only clauses
compatible with dependencies (`dependency_is_compatible_clause`,
`dependencies.c:741-870`: a binary `OpExpr` whose `oprrest` is `F_EQSEL`, a
`ScalarArrayOpExpr` with `useOr`, an `is_orclause` BoolExpr, `NOT x`, a bare
boolean Var, and expression variants mapped to pseudo-attnums. There is **no
`NullTest` branch** — `IS NULL` is compatible with **MCV** extended statistics
only, `extended_stats.c:1385-1390`); repeatedly `find_strongest_dependency` (most
attributes, then highest degree, fully contained in the remaining clause
attnums), removing the dependent attribute each time; then
`clauselist_apply_dependencies`: per attribute `attr_sel[a] =
clauselist_selectivity_ext(clauses on a, no extended stats)`; for dependencies
from weakest to strongest with implying set `A` and dependent `b`,
`s1 = Π attr_sel[A]`, `s2 = attr_sel[b]`, `f = degree`:

```
attr_sel[b] = (s1 <= s2) ? f + (1 - f) * s2 : f * s2 / s1 + (1 - f) * s2
```

and the final result is `Π attr_sel[*]`. (This is the documented
`P(a,b) = P(a) * (f + (1-f) * P(b))` chain; the `s1 > s2` branch caps the
implied part at `s2/s1`.) Clauses consumed are added to `estimatedclauses`.

### 9.4 Statistics-object selection in `examine_variable`

Expression statistics (`'e'`) supply a full `pg_statistic` tuple for an
expression that has no expression index (§4 step 5), so all scalar estimators
(§5) work unchanged on expressions.

### 9.5 `estimate_multivariate_ndistinct`

See §8.2. Requires ≥ 2 matched keys; the returned `ndistinct` is used as the
combined distinct count for those keys in `estimate_num_groups`.

---

## 10. Foreign-key selectivity

### 10.1 Matching FKs to quals

`src/backend/optimizer/util/plancat.c:get_relation_foreign_keys` builds
`root->fkey_list` (`ForeignKeyOptInfo`: `con_relid, ref_relid, nkeys,
conkey[], confkey[], conpfeqop[]`) for every FK whose referenced table is also
in the query as a base rel. After equivalence classes exist,
`src/backend/optimizer/plan/initsplan.c:match_foreign_keys_to_quals` fills, per
column, either `eclass[colno]` (via `match_eclasses_to_foreign_key_col`;
`nmatched_ec++`, `nconst_ec++` if the EC has a constant) or `rinfos[colno]`
(join clauses of the form `ref.col = con.col` with the FK's
`conpfeqop` or its commutator; `nmatched_ri++`, `nmatched_rcols++`). Only FKs
with every column matched (`nmatched_ec + nmatched_rcols == nkeys`) survive
in `root->fkey_list`.

### 10.2 `get_foreign_key_join_selectivity`

`src/backend/optimizer/path/costsize.c:get_foreign_key_join_selectivity(root,
outer_relids, inner_relids, sjinfo, &restrictlist)`:

```
fkselec = 1.0
for fkinfo in root->fkey_list:
    skip unless (con ∈ outer and ref ∈ inner) or (ref ∈ outer and con ∈ inner)
    for SEMI/ANTI: skip if ref side is outer or inner side is not a single rel
    remove from worklist every RestrictInfo that is either the EC-derived clause of a
        matched EC (rinfo->parent_ec == fkinfo->eclass[i]) or one of fkinfo->rinfos[i]
    if the number removed != nmatched_ec - nconst_ec + nmatched_ri: put them back; continue
    ref_tuples = max(ref_rel->tuples, 1)
    SEMI/ANTI: fkselec *= ref_rel->rows / ref_tuples
    else:      fkselec *= 1.0 / ref_tuples
    for each matched EC with a constant: s0 = clause_selectivity(derived clause for the FK member);
                                         if s0 > 0: fkselec /= s0
*restrictlist = worklist; CLAMP(fkselec)
```

The FK-covered join clauses are *removed* from the restriction list before the
ordinary `clauselist_selectivity` runs, so they are not double-counted;
`1/ref_tuples` per FK reflects "each referencing row matches exactly one
referenced row". The `nconst_ec` division undoes the restriction selectivity
already applied on the referenced side when the join column is pinned to a
constant.

### 10.3 `calc_joinrel_size_estimate`

Same file. `fkselec` is computed first (mutating `restrictlist`); for outer
joins the remaining clauses split into `joinquals` (`jselec`) and pushed-down
quals (`pselec`), else all are `jselec`:

| jointype | rows |
|---|---|
| INNER | `outer * inner * fkselec * jselec` |
| LEFT | `max(outer*inner*fkselec*jselec, outer) * pselec` |
| FULL | `max(outer*inner*fkselec*jselec, outer, inner) * pselec` |
| SEMI | `outer * fkselec * jselec` |
| ANTI | `outer * (1 - fkselec*jselec) * pselec` |

Result `clamp_row_est`ed.

---

## 11. Plan-time invalidation, caching, import/export, standby

* **Relcache invalidation on ANALYZE/VACUUM**: `vac_update_relstats` goes
  through `systable_inplace_update_finish` →
  `heapam.c:heap_inplace_update_and_unlock`, which calls
  `CacheInvalidateHeapTupleInplace(relation, oldtup)` before releasing the
  buffer, queueing a relcache invalidation for the relation. `pg_statistic`
  rows are ordinary catalog tuples updated by `CatalogTupleUpdate`, which
  registers syscache invalidations for `STATRELATTINH` and, because
  `pg_statistic` has no relcache dependency, is seen by the next
  `SearchSysCache3` after the invalidation is processed (at transaction end
  for the writer, at the next `AcceptInvalidationMessages` for others).
* **Snapshot for catalog reads**: syscache lookups use the catalog snapshot
  (`GetCatalogSnapshot`), so a planner sees committed stats as of statement
  start.
* **Plan cache** (`src/backend/utils/cache/plancache.c`): each
  `CachedPlanSource` records `relationOids`; `PlanCacheRelCallback(arg,
  relid)` (registered via `CacheRegisterRelcacheCallback`) marks the source
  and its generic plan invalid when `relid` is in the list. Because ANALYZE
  updates `pg_class` in place with a relcache inval, ANALYZE **does**
  invalidate cached plans referencing the table; the next execution replans.
  `choose_custom_plan`: one-shot → custom; no params → generic;
  `plan_cache_mode = force_generic_plan / force_custom_plan` override; first
  5 executions always custom; thereafter generic if `generic_cost <
  avg_custom_cost`.
* **Statistics import/export (PG 18)**:
  `src/backend/statistics/relation_stats.c:pg_restore_relation_stats` /
  `pg_clear_relation_stats` and
  `src/backend/statistics/attribute_stats.c:pg_restore_attribute_stats` /
  `pg_clear_attribute_stats` accept `(relation, attname, inherited, version,
  null_frac, avg_width, n_distinct, most_common_vals, …)` as variadic
  name/value pairs. `relation_stats_update` builds a new tuple with
  `heap_modify_tuple_by_cols` and calls `CatalogTupleUpdate`
  (`relation_stats.c:137-192`) — an ordinary **transactional** catalog update,
  not `vac_update_relstats`' in-place write; `pg_statistic` is upserted. `pg_dump` (`src/bin/pg_dump/pg_dump.c:dumpRelationStats`)
  emits `SELECT * FROM pg_catalog.pg_restore_relation_stats(...)` and
  `pg_restore_attribute_stats(...)` statements when `--statistics` /
  `--statistics-only` is used (`--no-statistics` suppresses them).
* **Standby**: `pg_class` and `pg_statistic` changes are ordinary heap/inplace
  WAL records, so a hot standby plans with exactly the primary's statistics;
  no ANALYZE runs on the standby. `get_actual_variable_range`'s index probe
  works on the standby too (read-only index/heap access).

---

## 12. Worked examples

All from `doc/src/sgml/planstats.sgml` ("Row Estimation Examples") with the
formulas from the sections above.

### 12.1 Histogram inequality — `tenk1 WHERE unique1 < 1000`

`pg_class`: `relpages = 358, reltuples = 10000` → `rel->tuples = 10000`
(§1.3, density × current pages). `histogram_bounds =
{0,993,1997,3050,4040,5036,5957,7057,8029,9016,9995}` (11 bounds, 10 bins), no
MCVs, `null_frac = 0`. `ineq_histogram_selectivity` (§5.3): binary search
puts 1000 in bin `i = 2` (`values[1] = 993`, `values[2] = 1997`);
`convert_to_scalar` → `binfrac = (1000 - 993)/(1997 - 993) = 0.00697`;
`histfrac = (1 + 0.00697)/10 = 0.100697`. `scalarineqsel`: `selec = (1 - 0 -
0) × 0.100697 + 0 = 0.100697`; rows = `10000 × 0.100697 = 1007`. The
manual's text says `rows=1007`. PG 18.3 gives **1006**: for `<`,
`ineq_histogram_selectivity` subtracts `eq_selec = 1/10000 = 0.0001`, so
`histfrac = 0.100597`. §5.3 step 2 states that subtraction, so the manual's
figure (which the manual itself calls an oversimplification) contradicts this
document's own rule. The mechanism was confirmed empirically on the live PG
18.3 oracle with a 10,000-row temp table: `a < 1000` gives `rows=1003` and
`a <= 1000` gives `rows=1004`, the `eq_selec` term being exactly the
difference. An implementer calibrating against 1007 omits the term entirely.

### 12.2 MCV equality and miss — `tenk1 WHERE stringu1 = 'CRAAAA'` / `'xxx'`

`most_common_vals = {EJAAAA,BBAAAA,CRAAAA,…}` (10 entries),
`most_common_freqs = {0.00333333, 0.003 ×9}`, `n_distinct = 676`,
`null_frac = 0`. `var_eq_const` (§5.2): `'CRAAAA'` hits entry 3 → `selec =
0.003` → rows `30`. `'xxx'` misses: `sumcommon = 0.03033333`; `selec = (1 -
0.03033333 - 0) / (676 - 10) = 0.0014559`, which is below the least-common MCV
frequency 0.003 so the cap does not fire; rows = `10000 × 0.0014559 ≈ 15`.

Combined with §12.1 by `clauselist_selectivity` (§6): the two clauses touch
different columns and are not both range bounds, so `s = 0.100697 × 0.0014559
= 0.0001466`, rows = `ceil-clamped 1`.

### 12.3 MCV + histogram inequality — `stringu1 < 'IAAAAA'`

`mcv_selectivity` sums the six MCV entries satisfying `<`:
`0.00333333 + 5 × 0.003 = 0.01833333`; `sumcommon = 0.03033333`. The
histogram part (`convert_string_to_scalar` on the bin `FRAAAA…IBAAAA`) yields
`hist_selec = 0.298387`. `scalarineqsel`: `(1 - 0 - 0.03033333) × 0.298387 +
0.01833333 = 0.307669` → rows `3077`.

### 12.4 Join without MCVs — `t1.unique1 < 50 AND t1.unique2 = t2.unique2`

`unique2` has `n_distinct = -1` on both tables (unique index → `isunique`),
`null_frac = 0`. `eqjoinsel_inner` no-MCV branch (§7.2): `nd1 = nd2 = 10000`
(`-1 × 10000`), `selec = (1 - 0)(1 - 0) / max(10000, 10000) = 0.0001`.
`calc_joinrel_size_estimate` (§10.3, INNER): `50 × 10000 × 1 × 0.0001 = 50`
rows; the manual shows `rows=50`. Note the inner cardinality used is the
*unfiltered* 10000, not the 1 row shown for the inner index scan.

### 12.5 MCV-pairing join (numbers, §7.2)

Suppose `orders.status` has MCVs `{A:0.5, B:0.3, C:0.1}` (`nd1 = 5`,
`nullfrac1 = 0`) and `status_dim.code` has MCVs `{A:0.25, B:0.25, D:0.25}`
(`nd2 = 4`, `nullfrac2 = 0`). Pairwise matching finds A and B:
`matchprodfreq = 0.5×0.25 + 0.3×0.25 = 0.2`, `nmatches = 2`;
`matchfreq1 = 0.8`, `unmatchfreq1 = 0.1`, `otherfreq1 = 1 - 0 - 0.8 - 0.1 = 0.1`;
`matchfreq2 = 0.5`, `unmatchfreq2 = 0.25`, `otherfreq2 = 0.25`.
`totalsel1 = 0.2 + (nd2=4 > nvalues2=3 ? 0.1×0.25/(4-3) = 0.025 : 0) +
(4 > 2 ? 0.1×(0.25+0.25)/(4-2) = 0.025 : 0) = 0.25`.
`totalsel2 = 0.2 + (5 > 3 ? 0.25×0.1/(5-3) = 0.0125 : 0) + (5 > 2 ?
0.25×(0.1+0.1)/(5-2) = 0.01667 : 0) = 0.22917`. `selec = min = 0.22917`.
With 1000 orders and 4 dim rows the join estimate is `1000 × 4 × 0.22917 ≈
917` rows (the true answer with these MCVs is 800 + a little from the
unmatched tails).

### 12.6 Extended statistics example (manual §"Multivariate Statistics Examples")

Table `t(a, b)` with `INSERT … SELECT i % 100, i % 100 FROM generate_series(1,
10000)`. Without extended stats `WHERE a = 1 AND b = 1` → `0.01 × 0.01 ×
10000 = 1` row (independence). With `CREATE STATISTICS (dependencies)` the
dependency `a → b` has degree 1.0 and `clauselist_apply_dependencies` gives
`attr_sel[b] = 1 + 0 × 0.01 = 1`, so `0.01 × 1 × 10000 = 100`. With
`(ndistinct)` the `GROUP BY a, b` estimate goes from `min(100 × 100, 0.1 ×
10000) = 1000` to the stored `ndistinct(a,b) = 100`. With `(mcv)` the list
holds all 100 `{i,i}` items at `frequency 0.01, base_frequency 0.0001`;
`a = 1 AND b = 1` → `mcv_sel = 0.01`, `basesel = 0.0001`, `simple_sel =
0.0001`, `other_sel = 0` → 100 rows; `a = 1 AND b = 10` → no matching item,
`mcv_sel = 0`, `other_sel = clamp(0.0001 - 0) = 0.0001` capped by
`1 - totalsel = 0` → 0 → `rows=1` after clamping.

---

## 13. Reimplementation checklist

Each statement is atomic and testable against PG 18.3; the citation is the
function that establishes it.

**Relation stats**

1. A fresh table has `relpages = 0, reltuples = -1, relallvisible = 0`; `-1` means "never vacuumed/analyzed" (`heap.c:AddNewRelationTuple`).
2. TRUNCATE (relfilenode swap) resets the same three fields to `0/-1/0` (`relcache.c:RelationSetNewRelfilenumber`).
3. `pg_class.reltuples` counts live tuples only (`vacuum.c:vac_update_relstats` header comment).
4. `vac_update_relstats` updates `pg_class` in place, non-transactionally, and only fields that changed (`vacuum.c:vac_update_relstats`).
5. PG 18 stores `relallfrozen` alongside `relallvisible` (`vacuum.c:vac_update_relstats`).
6. VACUUM keeps the old `reltuples` when `< 2 %` of an unchanged-size table was scanned, or `<= 1` page was scanned (`vacuum.c:vac_estimate_reltuples`).
7. VACUUM extrapolates unscanned pages with the *old* density and adds the scanned live count (`vacuum.c:vac_estimate_reltuples`).
8. ANALYZE sets `reltuples = totalrows`, `relpages = RelationGetNumberOfBlocks`, `relallvisible/relallfrozen` from the VM (`analyze.c:do_analyze_rel`).
9. ANALYZE sets each index's `reltuples = ceil(tupleFract × totalrows)` (partial-index aware) (`analyze.c:do_analyze_rel`, `compute_index_stats`).
10. CREATE INDEX writes heap `reltuples` from the build scan and index `reltuples` from the index build, preserving a stored `-1` when passed 0 (`index.c:index_update_stats`).
11. `estimate_rel_size` always uses the live block count `RelationGetNumberOfBlocks` for `pages` (`tableam.c:table_block_relation_estimate_size`).
12. Never-analyzed non-inheritance-parent tables with `< 10` pages are treated as 10 pages (`tableam.c:table_block_relation_estimate_size`).
13. `tuples = rint(density × curpages)` with `density = reltuples/relpages` when `reltuples >= 0 && relpages > 0` (`tableam.c:table_block_relation_estimate_size`).
14. Without stats, density = `(usable_page_bytes × fillfactor/100) / (data_width + tuple_overhead)` using integer division, clamped ≥ 1 (`tableam.c:table_block_relation_estimate_size`).
15. `allvisfrac = relallvisible / curpages` unscaled, clamped to `[0,1]` (`tableam.c:table_block_relation_estimate_size`).
16. Data width sums `stawidth` per non-dropped column, falling back to `get_typavgwidth` (`plancat.c:get_rel_data_width`).
17. Index size estimation discounts one metapage from both `curpages` and `relpages` (`plancat.c:estimate_rel_size`).
18. Non-partial index `IndexOptInfo.tuples = rel->tuples`; partial indexes use `estimate_rel_size` capped at `rel->tuples` (`plancat.c:get_relation_info`).
19. Btree `tree_height` comes from `amgettreeheight` (`_bt_getrootheight`); other AMs get `-1` (`plancat.c:get_relation_info`).
20. Invalid indexes and `indcheckxmin` indexes whose xmin is not yet visible are skipped (`plancat.c:get_relation_info`).
21. `predOK` is false in `get_relation_info` and set by `check_index_predicates` (`plancat.c:get_relation_info`).
22. Btree `sortopfamily = opfamily`, `reverse_sort/nulls_first` from `indoption` bits (`plancat.c:get_relation_info`).

**ANALYZE**

23. Column target: `attstattarget` (NULL → −1 → `default_statistics_target`, default 100, max 10000); `0` disables the column (`analyze.c:examine_attribute`, `std_typanalyze`, `guc_tables.c`).
24. Types with `=` and `<` use `compute_scalar_stats`; only `=` → `compute_distinct_stats`; neither → `compute_trivial_stats` (`analyze.c:std_typanalyze`).
25. `minrows = 300 × attstattarget`; `targrows = max(100, max minrows)` over analyzed columns and index expressions (`analyze.c:std_typanalyze`, `do_analyze_rel`).
26. Block sampling is Knuth Algorithm S over `min(targrows, nblocks)` blocks (`sampling.c:BlockSampler_Init/Next`).
27. Row sampling is Vitter reservoir sampling within the selected blocks (`analyze.c:acquire_sample_rows`).
28. `totalrows = floor(liverows / blocks_read × totalblocks + 0.5)`; dead rows likewise (`analyze.c:acquire_sample_rows`).
29. Sampled rows are sorted by TID when the reservoir filled (`analyze.c:acquire_sample_rows`).
30. Inherited samples allocate `childtargrows = rint(targrows × childblocks / totalblocks)` (`analyze.c:acquire_inherited_sample_rows`).
31. Varlena values with raw size > 1024 bytes are counted in width but excluded from value stats and treated as distinct (`analyze.c:compute_scalar_stats`, `WIDTH_THRESHOLD`).
32. `stanullfrac = null_cnt / samplerows`; `stawidth` averages over non-null values (`analyze.c:compute_scalar_stats`).
33. No duplicates in sample → `stadistinct = -(1 - nullfrac)` (`analyze.c:compute_scalar_stats`).
34. All sampled values repeated and none too wide → `stadistinct = ndistinct` (`analyze.c:compute_scalar_stats`).
35. Otherwise Duj1: `n·d / ((n − f1) + f1·n/N)` with `n, N` non-null counts, clamped to `[d, N]`, rounded (`analyze.c:compute_scalar_stats`).
36. `stadistinct > 0.1 × totalrows` is stored as the negative fraction `−stadistinct/totalrows` (`analyze.c:compute_scalar_stats`).
37. MCV list is complete (all sampled values) when they all fit and `stadistinct > 0` (`analyze.c:compute_scalar_stats`).
38. Otherwise MCVs are pruned from the least common upward using a hypergeometric `2·stddev + 0.5` test against the non-MCV selectivity; no 1.25× rule exists in 18.3 (`analyze.c:analyze_mcv_list`).
39. MCV frequencies are `count / samplerows` (all rows incl. nulls) (`analyze.c:compute_scalar_stats`).
40. Histogram has `num_hist = min(ndistinct − num_mcv, target + 1)` bounds and needs ≥ 2; MCV values are removed first (`analyze.c:compute_scalar_stats`).
41. Histogram bound `i` is `values[(i·(nvals−1)) / (num_hist−1)]` computed by delta/posfrac stepping (`analyze.c:compute_scalar_stats`).
42. Correlation is Pearson between sorted rank and sample position, stored as one `stanumbers` entry with `staop = <` (`analyze.c:compute_scalar_stats`).
43. `compute_distinct_stats` produces only an MCV slot (no histogram, no correlation) (`analyze.c:compute_distinct_stats`).
44. Only expression index columns get `pg_statistic` rows; `compute_stats` is invoked with `(numindexrows, totalindexrows)` (`analyze.c:compute_index_stats`).
45. `ALTER TABLE … SET (n_distinct)` overrides `stadistinct` when non-zero; `n_distinct_inherited` for `inh` rows (`analyze.c:do_analyze_rel`).
46. `pg_statistic` rows are written transactionally with `stainherit = inh` (`analyze.c:update_attstats`).
47. Extended statistics are built from the same sample after column stats (`analyze.c:do_analyze_rel` → `extended_stats.c:BuildRelationExtStatistics`).
48. ANALYZE reports to cumulative stats, resetting `n_mod_since_analyze` (`analyze.c:do_analyze_rel` → `pgstat_report_analyze`).
49. Autoanalyze fires when `mod_since_analyze > 50 + 0.1 × reltuples` (reltuples < 0 → 0) (`autovacuum.c:relation_needs_vacanalyze`, `guc_tables.c`).
50. Autovacuum fires when `dead_tuples > 50 + 0.2 × reltuples`, capped by `autovacuum_vacuum_max_threshold` (100000000) (`autovacuum.c:relation_needs_vacanalyze`).

**Catalog layout**

51. `pg_statistic` has exactly 5 slots of `(stakind, staop, stacoll, stanumbers float4[], stavalues anyarray)` (`pg_statistic.h`).
52. Kind codes: MCV 1, HISTOGRAM 2, CORRELATION 3, MCELEM 4, DECHIST 5, RANGE_LENGTH_HISTOGRAM 6, BOUNDS_HISTOGRAM 7 (`pg_statistic.h`).
53. MCV `staop` is `=`; histogram and correlation `staop` is `<` (`analyze.c:compute_scalar_stats`).
54. Histogram slot has NULL `stanumbers`; correlation slot has NULL `stavalues` (`pg_statistic.h` comments).
55. `stavalues` element type is `stats->statypid[k]` (defaults to the column type) (`analyze.c:examine_attribute`, `update_attstats`).
56. Unique key `(starelid, staattnum, stainherit)`, syscache `STATRELATTINH` (`pg_statistic.h`).
57. `pg_stats` maps slots to columns by `CASE WHEN stakindN = k` and filters by column privilege/RLS (`system_views.sql`).
58. `pg_statistic_ext.stxkind` uses `'d','f','m','e'`; data lives in `pg_statistic_ext_data` keyed `(stxoid, stxdinherit)` (`pg_statistic_ext.h`, `pg_statistic_ext_data.h`).

**Variable examination**

59. Stats lookup uses `rte->inh` as `stainherit` (`selfuncs.c:examine_simple_variable`).
60. A Var is `isunique` if a unique single-key index (no predicate or `predOK`) covers it (`plancat.c:has_unique_index`).
61. Expression stats are searched first in non-partial expression indexes, then in `'e'` extended statistics (`selfuncs.c:examine_variable`).
62. Subquery/CTE output columns are resolved through the sub-PlannerInfo's target list unless the subquery has set operations or grouping sets (`selfuncs.c:examine_simple_variable`).
63. A subquery `DISTINCT`/`GROUP BY` on exactly that one column marks it unique without stats (`selfuncs.c:examine_simple_variable`).
64. Security-barrier subqueries and non-Var tlist entries yield no stats (`selfuncs.c:examine_simple_variable`).
65. Stats are used only if the user has SELECT on the table (and no RLS) or the operator function is leakproof (`selfuncs.c:statistic_proc_security_check`).
66. `get_variable_numdistinct`: bool → 2, VALUES → unique, ctid → unique, tableoid → 1 (`selfuncs.c:get_variable_numdistinct`).
67. `isunique` forces `stadistinct = −(1 − nullfrac)` (`selfuncs.c:get_variable_numdistinct`).
68. Negative `stadistinct` scales by `rel->tuples`, not `rel->rows` (`selfuncs.c:get_variable_numdistinct`).
69. Unknown ndistinct returns `min(ntuples, 200)` with `isdefault = true` only when 200 is returned (`selfuncs.c:get_variable_numdistinct`).
70. `get_variable_range` uses histogram endpoints, widened by MCVs; MCV-only when `Σmcv + nullfrac > 0.99999` (`selfuncs.c:get_variable_range`).
71. Histogram endpoints are refreshed by an index probe with `SnapshotNonVacuumable`, limited to 100 heap pages (`selfuncs.c:get_actual_variable_range`, `get_actual_variable_endpoint`).

**Restriction estimators**

72. `var_eq_const`: unique var → `1/tuples`; MCV hit → its frequency; miss → `(1 − Σmcv − nullfrac)/(nd − nmcv)` capped at the least MCV frequency; no stats → `1/nd` (`selfuncs.c:var_eq_const`).
73. `var_eq_non_const`: `(1 − nullfrac)/nd` capped at the largest MCV frequency (`selfuncs.c:var_eq_non_const`).
74. `neqsel = 1 − eqsel − nullfrac` (`selfuncs.c:eqsel_internal`, `var_eq_const` negate branch).
75. `scalarineqsel = mcv_part + (1 − nullfrac − Σmcv) × hist_part`, `hist_part = 0.5` when no histogram (`selfuncs.c:scalarineqsel`).
76. Histogram interpolation is linear within the bin via `convert_to_scalar`; failure → 0.5 of the bin (`selfuncs.c:ineq_histogram_selectivity`).
77. Histogram selectivity is clamped to `[0.01/(nbounds−1), 1 − 0.01/(nbounds−1)]` unless the endpoint was verified by index probe (`selfuncs.c:ineq_histogram_selectivity`).
78. `ctid` inequality without stats uses block-number arithmetic (`selfuncs.c:scalarineqsel`).
79. `IS NULL → stanullfrac`, `IS NOT NULL → 1 − stanullfrac`; defaults 0.005/0.995 (`selfuncs.c:nulltestsel`).
80. Boolean tests derive `freq_true` from the MCV slot; without MCV, `IS TRUE = (1 − nullfrac)/2` (`selfuncs.c:booltestsel`).
81. A bare boolean column is `var_eq_const(true)` or 0.5 (`selfuncs.c:boolvarsel`).
82. LIKE/regex: exact prefix → `var_eq_const`; else histogram-fraction weighted with the heuristic by `hist_size/100`, clamped `[0.0001, 0.9999]`, then MCV-adjusted (`like_support.c:patternsel_common`).
83. Pattern heuristic constants: fixed char 0.20, `_` 0.9, `%` 5.0, char range 0.25 (`like_support.c`).
84. `= ANY(const array)` sums element selectivities (disjoint assumption) when the sum is in `[0,1]`, else OR-combines (`selfuncs.c:scalararraysel`).
85. `<> ALL(array)` uses `1 − Σ(1 − s_i)` when in range (`selfuncs.c:scalararraysel`).
86. Non-constant arrays are assumed to have 10 elements (`selfuncs.c:scalararraysel`, `estimate_array_length`).
87. Row comparisons use only the first column pair (`selfuncs.c:rowcomparesel`).

**Clause lists**

88. Extended statistics are consulted before independence, on a single-relation clause list with `statlist != NIL` (`clausesel.c:clauselist_selectivity_ext`).
89. Range pairs `x > a AND x < b` on the same `equal()` var combine as `hi + lo − 1 + P(x IS NULL)` (`clausesel.c:clauselist_selectivity_ext`).
90. If either bound is `DEFAULT_INEQ_SEL` or the combination is `< −0.01`, the pair gets `DEFAULT_RANGE_INEQ_SEL = 0.005`; `[−0.01, 0]` → `1e-10` (`clausesel.c:clauselist_selectivity_ext`).
91. Duplicate bounds of the same direction keep the smaller selectivity (`clausesel.c:addRangeClause`).
92. OR → `s1 + s2 − s1·s2`; NOT → `1 − s`; AND → recursive list (`clausesel.c:clause_selectivity_ext`, `clauselist_selectivity_or`).
93. A clause is a join clause iff `varRelid == 0`, `sjinfo != NULL`, and it references > 1 base rel (`clausesel.c:treat_as_join_clause`).
94. `RestrictInfo` caches `norm_selec` (inner) / `outer_selec` (outer) selectivities; pseudoconstant non-Const clauses are 1.0 (`clausesel.c:clause_selectivity_ext`).
95. Unhandled node types default to 0.5 (`clausesel.c:clause_selectivity_ext`).

**Join estimators**

96. `eqjoinsel` loads MCVs only when both sides have them and pass security (`selfuncs.c:eqjoinsel`).
97. `eqjoinsel_inner` pairs MCVs with the join operator's function, computes `totalsel1/2`, and takes the minimum (`selfuncs.c:eqjoinsel_inner`).
98. Without MCVs: `(1 − nf1)(1 − nf2)/max(nd1, nd2)` (`selfuncs.c:eqjoinsel_inner`).
99. SEMI/ANTI: `nd2` is clamped to the inner rel's `rows`; result capped at `inner_rel->rows × selec_inner` (`selfuncs.c:eqjoinsel`, `eqjoinsel_semi`).
100. SEMI with MCVs: `matchfreq1 + uncertainfrac × (1 − matchfreq1 − nf1)`, `uncertainfrac = nd2/nd1` or 1, 0.5 when any nd is default (`selfuncs.c:eqjoinsel_semi`).
101. `neqjoinsel` for SEMI/ANTI is `1 − nullfrac(outer)`; otherwise `1 − eqjoinsel` (`selfuncs.c:neqjoinsel`).
102. `<`, `<=`, `>`, `>=` join operators return 0.3333 (`selfuncs.c:scalarltjoinsel` etc.).
103. Merge-join start/end fractions come from `scalarineqsel` of each side against the other side's min/max (`selfuncs.c:mergejoinscansel`).
104. Hash bucket fraction: `1/min(ndistinct_scaled, nbuckets)` × `mcv_freq/avgfreq` when skewed, clamped `[1e-6, 1]`; default `max(0.1, mcv_freq)` (`selfuncs.c:estimate_hash_bucket_stats`).

**Groups**

105. Boolean grouping expressions contribute factor 2 (`selfuncs.c:estimate_num_groups`).
106. Volatile constant-free group expressions return `input_rows` (`selfuncs.c:estimate_num_groups`).
107. Per-relation product of ndistincts is clamped to `rel->tuples`, and to `0.1 × tuples` (not below the largest single ndistinct) when > 1 var (`selfuncs.c:estimate_num_groups`).
108. Restriction scaling: `D × (1 − ((tuples − rows)/tuples)^(tuples/D))` (`selfuncs.c:estimate_num_groups`).
109. Final groups are `ceil(...)` clamped to `[1, input_rows]` (`selfuncs.c:estimate_num_groups`).
110. Extended ndistinct stats replace the product for ≥ 2 covered keys (`selfuncs.c:estimate_multivariate_ndistinct`).
111. Equivalent vars across relations collapse to the one with the smaller ndistinct (`selfuncs.c:add_unique_group_var`).
112. LIMIT/OFFSET without a known constant assume 10 % of input rows (`pathnode.c:adjust_limit_rows_costs`).

**Extended statistics**

113. Compatible clauses: `Var op Const` with `oprrest ∈ {eqsel, neqsel, scalar{lt,le,gt,ge}sel}`, `ScalarArrayOp` with var on left, `NullTest`, AND/OR/NOT thereof, bare bool Var, and matched expressions (`extended_stats.c:statext_is_compatible_clause_internal`).
114. The best MCV object covers the most attributes+expressions (≥ 2), ties → fewest keys (`extended_stats.c:choose_best_statistics`).
115. MCV combination: `sel = mcv_sel + clamp(simple_sel − basesel, 0, 1 − totalsel)` (`mcv.c:mcv_combine_selectivities`).
116. MCV items are cut below `n(N−n)/(N−n+0.04n(N−1))` sample occurrences and capped at the stats target (`mcv.c:statext_mcv_build`, `get_mincount_for_mcv_list`).
117. `base_frequency` is the product of per-column sample frequencies (`mcv.c:statext_mcv_build`).
118. Dependency degree = supporting rows / sample rows; applied as `f + (1−f)·s2` (or `f·s2/s1 + (1−f)·s2`) from weakest to strongest (`dependencies.c:dependency_degree`, `clauselist_apply_dependencies`).
119. Dependencies are applied only to AND lists, after MCV stats (`extended_stats.c:statext_clauselist_selectivity`).
120. Multivariate ndistinct uses the same Duj1 estimator per attribute combination (`mvdistinct.c:estimate_ndistinct`).

**Foreign keys & invalidation**

121. Only FKs with every column matched to an EC or a join clause are kept in `fkey_list` (`initsplan.c:match_foreign_keys_to_quals`).
122. FK selectivity is `1/max(ref_tuples, 1)` per FK (SEMI/ANTI: `ref_rows/ref_tuples`), divided by the selectivity of any constant EC clause on the FK column (`costsize.c:get_foreign_key_join_selectivity`).
123. FK-covered clauses are removed from the restriction list before `clauselist_selectivity` (`costsize.c:get_foreign_key_join_selectivity`).
124. Join rows: INNER `o·i·fk·j`; LEFT `max(o·i·fk·j, o)·p`; FULL also `≥ i`; SEMI `o·fk·j`; ANTI `o·(1 − fk·j)·p` (`costsize.c:calc_joinrel_size_estimate`).
125. In-place `pg_class` updates queue a relcache invalidation, which invalidates cached plans on the relation (`heapam.c:heap_inplace_update_and_unlock`, `plancache.c:PlanCacheRelCallback`).
126. Generic vs custom plan: first 5 executions custom, then generic if cheaper than the average custom cost; `plan_cache_mode` overrides (`plancache.c:choose_custom_plan`).
127. Statistics can be imported with `pg_restore_relation_stats` / `pg_restore_attribute_stats`, and `pg_dump --statistics` emits them (`relation_stats.c`, `attribute_stats.c`, `pg_dump.c:dumpRelationStats`).
