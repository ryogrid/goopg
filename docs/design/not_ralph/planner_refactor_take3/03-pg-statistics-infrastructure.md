# 03 — PostgreSQL 18.3 statistics infrastructure and selectivity

Scope: how PG 18.3 collects, stores, reads, and consumes planner
statistics. Every claim cites `postgres/<path>:<function>`.
All `global -x` symbols below were re-verified against the oracle.

Conventions: `N` = relation rows, `n` = ANALYZE sample rows,
`CLAMP_PROBABILITY` → [0,1] (`src/include/utils/selfuncs.h`),
`clamp_row_est` → integer ≥ 1
(`src/backend/optimizer/path/costsize.c:clamp_row_est`).

---

## 1. Relation-level statistics (`pg_class`)

### 1.1 The four columns and their writers

| column | meaning | sentinel |
|---|---|---|
| `relpages` | pages at last VACUUM/ANALYZE/CREATE INDEX | `0` fresh |
| `reltuples` | *live* tuples then (`float4`) | `-1` = never vacuumed/analyzed |
| `relallvisible` | VM all-visible pages | `0` |
| `relallfrozen` | VM all-frozen pages (**new in PG 18**) | `0` |

All four are written by one routine,
`postgres/src/backend/commands/vacuum.c:vac_update_relstats` (line 1442,
verified): **non-transactional in-place update** of the `pg_class` row
(only changed fields + DDL flags + freeze XIDs), which queues a relcache
invalidation via
`postgres/src/backend/access/heap/heapam.c:heap_inplace_update_and_unlock`
→ `CacheInvalidateHeapTupleInplace` (§10).

Writers: CREATE TABLE (`catalog/heap.c:AddNewRelationTuple`: 0/−1/0);
TRUNCATE/rewrite (`utils/cache/relcache.c:RelationSetNewRelfilenumber`:
reset 0/−1/0); ANALYZE (`commands/analyze.c:do_analyze_rel`:
`relpages` = block count, `reltuples` = `totalrows`, VM counts from
`visibilitymap_count`; indexes get
`ceil(tupleFract × totalrows)` with partial-predicate fraction §2.8;
column-subset ANALYZE writes only reltuples); VACUUM
(`access/heap/vacuumlazy.c`: extrapolated live tuples §1.2, clamped VM
counts; indexes from `update_relstats_all_indexes`); CREATE
INDEX/REINDEX (`catalog/index.c:index_update_stats`, preserving a stored
−1 when passed 0).

### 1.2 `vac_estimate_reltuples` (`commands/vacuum.c`, line 1346, verified)

```
scanned == total                   → scanned_tuples
old pages == total and scanned < 2% → old reltuples (may be -1)
scanned ≤ 1 page                   → old reltuples
no old stats                       → floor(scanned/scanned_pages * total + 0.5)
else                               → floor(old_density*(total-scanned) + scanned + 0.5)
```

`scanned_tuples` counts **live** tuples only.

### 1.3 How the planner reads them: `estimate_rel_size`

`postgres/src/backend/optimizer/util/plancat.c:estimate_rel_size` (line
1075, verified) → `table_block_relation_estimate_size`
(`access/table/tableam.c`), with `HEAP_OVERHEAD_BYTES_PER_TUPLE` and
usable-bytes-per-page constants:

```
curpages = RelationGetNumberOfBlocks(rel)     # live size, not pg_class
if curpages < 10 and reltuples < 0 and !relhassubclass: curpages = 10
if curpages == 0: tuples = 0; allvisfrac = 0; return
density = (reltuples ≥ 0 and relpages > 0) ? reltuples/relpages
        : (usable_bytes * fillfactor/100) / (data_width + overhead)   # integer division, clamp ≥ 1
tuples = rint(density * curpages)
allvisfrac = (relallvisible == 0) ? 0 : relallvisible ≥ curpages ? 1 : relallvisible/curpages
```

Properties: pages always live; tuples = density scaled to live size;
`relallvisible` unscaled (new pages assumed not visible).
`get_rel_data_width` sums `get_attavgwidth` (`pg_statistic.stawidth`,
`utils/cache/lsyscache.c`) else `get_typavgwidth` over non-dropped
columns. Indexes: same minus one metapage from both counts (no 10-page
floor). Foreign tables/sequences: `pg_class` values as-is.

### 1.4 `IndexOptInfo` in `get_relation_info` (`util/plancat.c`)

One per valid index (skips `!indisvalid` and too-new `indcheckxmin`):
`ncolumns/nkeycolumns`, `indexkeys`, `opfamily/opcintype/collations`
(key cols), `canreturn[]` (`index_can_return`), `relam/amcostestimate`,
`amcanorderbyop/optionalkey/searcharray/searchnulls/canparallel`,
`amhasgettuple/getbitmap/markpos`, btree `sortopfamily/reverse_sort/
nulls_first` from `indoption` bits (other ordered AMs mapped via
`<`-member lookup, else NULL), `indexprs/indpred` (`ChangeVarNodes`
to varno 1), `predOK = false` (set later by
`path/indxpath.c:check_index_predicates`), `unique/immediate`,
`pages/tuples` (non-partial: block count + `rel->tuples`; partial:
`estimate_rel_size` capped at `rel->tuples`), `tree_height` from
`amgettreeheight` (btree `_bt_getrootheight`) else −1.

### 1.5 Column statistics access path

`pg_statistic` via syscache `STATRELATTINH` on
`(starelid, staattnum, stainherit)`
(`src/include/catalog/pg_statistic.h`). Consumers:
`selfuncs.c:examine_simple_variable` (verified line 5684);
`lsyscache.c:get_attstatsslot` (scans 5 slots for kind+op, deconstructs
`stavaluesN anyarray` / `stanumbersN float4[]`);
`get_attavgwidth` (`stawidth` when > 0). Hooks:
`get_relation_stats_hook`, `get_index_stats_hook`
(`src/include/utils/selfuncs.h`).

---

## 2. ANALYZE (`src/backend/commands/analyze.c`)

### 2.1 `do_analyze_rel` flow

1. `vacattrstats[]` from `examine_attribute` per column (+ expression
   columns of expression indexes).
2. `targrows = max(100, max stats->minrows)`.
3. `numrows = acquire_sample_rows(...)` (or inherited variant), yielding
   `totalrows/totaldeadrows`.
4. Per column `compute_stats(...)`; `ALTER TABLE … SET (n_distinct /
   n_distinct_inherited)` overrides non-zero `stadistinct`.
5. `compute_index_stats` for expression indexes.
6. `update_attstats` writes `pg_statistic` rows (transactional).
7. `BuildRelationExtStatistics` (§8).
8. Non-inherited: `vac_update_relstats` for table + indexes (§1.1);
   `pgstat_report_analyze` resets `n_mod_since_analyze`.

### 2.2 `examine_attribute` and `std_typanalyze`

Skips dropped columns, virtual generated columns,
`attstattarget == 0`. `attstattarget` NULL → −1 →
`default_statistics_target` (GUC default 100, max 10000):

```
eqopr and ltopr → compute_scalar_stats
eqopr only     → compute_distinct_stats
neither        → compute_trivial_stats
minrows = 300 * attstattarget      # Chaudhuri–Motwani–Narasayya: r = 4·k·ln(2n/γ)/f² ≈ 305.82·k
```

### 2.3 `acquire_sample_rows` (line 1199, verified) — two-stage sampling

- Blocks: `BlockSampler_Init(&bs, totalblocks, targrows, seed)` picks
  `min(targrows, totalblocks)`; `BlockSampler_Next` implements Knuth
  Algorithm S (`utils/misc/sampling.c`).
- Rows: Vitter reservoir sampling within selected blocks; live/dead
  counted by `HeapTupleSatisfiesVacuum` vs `OldestXmin`.
- `totalrows = floor(liverows/blocks_read × totalblocks + 0.5)` (dead
  likewise). Rows sorted by TID when the reservoir filled (for
  correlation). Inherited variant allocates
  `childtargrows = rint(targrows × childblocks/totalblocks)` per child.

### 2.4 `compute_scalar_stats` (line 2402, verified), in full

`num_mcv = num_bins = attstattarget`; `WIDTH_THRESHOLD = 1024`.
Pass 1: nulls counted; varlena `total_width += VARSIZE_ANY` (raw size
> 1024 → `toowide_cnt++`, excluded from values but kept in width);
sortable values collected with sample positions.

1. Sort with the `<` operator's sort support; equal-key groups tracked;
   `corr_xysum += i × tupno`.
2. Sorted scan: `ndistinct++` per group; repeated groups counted
   (`nmultiple++`) and inserted into `track[]` (count-descending, cap
   `num_mcv`).
3. `stanullfrac = null_cnt/samplerows`; `stawidth` = avg over non-nulls
   (varwidth) or `typlen`.
4. `stadistinct`:
   - `nmultiple == 0` → `-(1 - nullfrac)` (all distinct).
   - no too-wide and every value repeated → `ndistinct` (small domain).
   - else Haas–Stokes **Duj1**: `f1 = ndistinct - nmultiple +
     toowide_cnt`, `d = f1 + nmultiple`, `n`/`N` non-null sample/table
     counts; `n·d/((n-f1) + f1·n/N)`, clamped `[d, N]`, rounded.
   - `> 0.1 × totalrows` → stored as negative fraction `−d/totalrows`
     (scales with growth).
5. MCV: complete-list case (all sampled values fit, `stadistinct > 0`)
   keeps all; else `analyze_mcv_list` (§2.5). Slot `STATISTIC_KIND_MCV`
   (1), `staop = =`, frequencies `count/samplerows`.
6. Histogram: `num_hist = min(ndistinct − num_mcv, target+1)` bounds,
   needs ≥ 2; MCV values removed first; bound `i` =
   `values[(i·(nvals−1))/(num_hist−1)]` by delta/posfrac stepping; first =
   sample MIN, last = MAX. Slot `STATISTIC_KIND_HISTOGRAM` (2),
   `staop = <`, no numbers.
7. Correlation (values > 1): Pearson of sorted rank vs sample position
   via closed-form sums; slot `STATISTIC_KIND_CORRELATION` (3), one
   numbers entry. Fallbacks: all-too-wide → `−(1−nullfrac)`, no slots;
   only nulls → nullfrac 1, width 0, ndistinct 0.

### 2.5 `analyze_mcv_list` (line 2980, verified)

Keeps everything when `samplerows == totalrows`. Else, from the least
common entry upward, with `ndistinct_table` (negatives × totalrows):

```
selec = 1 - sumcount/samplerows - nullfrac, clamp [0,1]
otherdistinct = ndistinct_table - (num_mcv - 1); if > 1: selec /= otherdistinct
K = N * count_last/n; variance = n·K·(N-K)·(N-n)/(N²·(N-1)); stddev = sqrt
if count_last > selec*samplerows + 2*stddev + 0.5: break   # keep it and all above
num_mcv--; sumcount -= new last
```

Continuity-corrected hypergeometric test ("significantly more common
than the non-MCV frequency"). The pre-PG11 `1.25× average` rule is gone.

### 2.6–2.9 The rest of ANALYZE

- `compute_distinct_stats` (line 2059, verified): no sort (linear
  match into `2×num_mcv` track), same Duj1 with
  `f1 = nonnull − summultiple`; MCV slot only (no histogram/correlation).
  `compute_trivial_stats`: nullfrac/width/`stadistinct = 0` only.
- `compute_index_stats`: predicate-aware `(numindexrows,
  totalindexrows)`; `tupleFract = numindexrows/numrows`;
  `totalindexrows = ceil(tupleFract × totalrows)`. Plain index columns
  get no `pg_statistic` rows.
- Inheritance: `inh = true` rows from the union sample; planner picks by
  `rte->inh`. Per-column `SET STATISTICS n` → `attstattarget`.
- Autovacuum (`postmaster/autovacuum.c:relation_needs_vacanalyze`):
  vacuum at `dead > 50 + 0.2×reltuples` (capped by
  `autovacuum_vacuum_max_threshold` = 1e8); analyze at
  `mod_since_analyze > 50 + 0.1×reltuples` (`reltuples < 0` → 0);
  per-table reloptions override.

---

## 3. `pg_statistic` layout and friends

### 3.1 `pg_statistic` (`src/include/catalog/pg_statistic.h`, OID 2619)

`starelid, staattnum, stainherit, stanullfrac float4, stawidth int4,
stadistinct float4, stakind1..5 int2, staop1..5 oid, stacoll1..5 oid,
stanumbers1..5 float4[], stavalues1..5 anyarray`.
`STATISTIC_NUM_SLOTS = 5`; unique index on
`(starelid, staattnum, stainherit)`; `stavalues` element type =
typanalyze's `statypid[k]` (defaults to column type).

| kind | value | `staop` | values | numbers |
|---|---|---|---|---|
| MCV | 1 | `=` | K most common, freq-desc | their frequencies (of all rows) |
| HISTOGRAM | 2 | `<` | M ≥ 2 equal-population bounds (MIN…MAX, MCVs removed) | NULL |
| CORRELATION | 3 | `<` | NULL | one entry in [−1,1] |
| MCELEM | 4 | element `=` | most common elements | per-element fractions + min/max (+null) extras |
| DECHIST | 5 | element `=` | NULL | distinct-count histogram, last = average |
| RANGE_LENGTH_HISTOGRAM | 6 | subtype `<` | length histogram | one entry: empty fraction |
| BOUNDS_HISTOGRAM | 7 | `<` | lower/upper bounds as ranges | NULL |

### 3.2 Views and extended catalogs

`pg_stats` (`catalog/system_views.sql`, security-barrier view): maps
slots by `CASE WHEN stakindN = k`, filters dropped columns, column
privilege, RLS. `pg_statistic_ext` (OID 3381):
`stxrelid, stxname, stxnamespace, stxowner, stxkeys int2vector,
stxstattarget int2, stxkind char[] ('d','f','m','e'), stxexprs
pg_node_tree`. `pg_statistic_ext_data` (OID 3429): `stxdinherit,
stxdndistinct pg_ndistinct, stxddependencies pg_dependencies, stxdmcv
pg_mcv_list, stxdexpr pg_statistic[]`, unique `(stxoid, stxdinherit)`,
syscache `STATEXTDATASTXOID`. `pg_stats_ext` joins + unnests MCV items.

---

## 4. `examine_variable` / `VariableStatData` (`utils/adt/selfuncs.c`, line 5292, verified)

Fields: `var` (stripped), `rel` (RelOptInfo or NULL), `statsTuple`,
`freefunc`, `vartype`, `atttype/atttypmod`, `isunique`, `acl_ok`.

1. Strip PHVs (`strip_all_phvs_deep`) + RelabelType.
2. Plain Var on one rel → `find_base_rel`, `isunique =
   has_unique_index(rel, attno)`
   (`plancat.c:has_unique_index`: unique single-key, no predicate or
   `predOK`), then `examine_simple_variable` (line 5684, verified).
3. Expression: single base rel → `onerel`; expression-index match
   (`equal()` after RelabelType strip; partial-index stats deliberately
   unused); else `'e'` extended-statistics match (`statext_expressions_load`).
4. `examine_simple_variable`: stats hook first; RTE_RELATION →
   `STATRELATTINH` by `(relid, attno, rte->inh)`,
   `acl_ok = all_rows_selectable`; RTE_SUBQUERY (non-inh) / RTE_CTE:
   resolve through `subroot`/`cte_plan_ids` → `glob->subroots` (give up
   on set-ops/grouping sets/security-barrier); single-column DISTINCT/
   GROUP BY → `isunique`, no stats; plain-Var tlist → recurse.
5. `statistic_proc_security_check`: usable iff `acl_ok` or operator fn
   `proleakproof`.

### 4.1 `get_variable_numdistinct` (line 6258, verified)

```
stats → stadistinct/stanullfrac; bool → 2.0; VALUES → -1.0; ctid → -1.0; tableoid → 1.0; else 0.0
isunique → stadistinct = -(1 - nullfrac)
stadistinct > 0 → clamp_row_est
rel == NULL or ntuples ≤ 0 → 200 default (`isdefault = true`; `selfuncs.c:6352/6358`)
stadistinct < 0 → clamp(-stadistinct * rel->tuples)     # whole-relation tuples, not rows
ntuples < 200 → clamp(ntuples); else 200 default (`isdefault = true` large-relation no-data fallback; `selfuncs.c:6376` — three default arms total)
```

### 4.2–4.3 Ranges and endpoint refresh

`get_variable_range`: HISTOGRAM slot with matching `staop`/`stacoll`
(first/last bound), widened by MCVs (MCV-only when
`Σmcv + nullfrac > 0.99999`). `get_actual_variable_range` (line 6581,
verified): called from `ineq_histogram_selectivity` only at the extreme
buckets; needs a non-partial btree-orderable index with `canreturn[0]`
on the variable; probes with `SnapshotNonVacuumable` (sees
not-yet-removable tuples so it converges after deletes); stops after
`VISITED_PAGES_LIMIT = 100` heap pages. Rationale: histogram endpoints
drift for monotonic keys; without the probe an out-of-range comparison
clamps to the `0.01/(nbounds−1)` cutoff instead of ~0/~1.

---

## 5. Restriction selectivity (`selfuncs.c`)

### 5.1 Constants (`src/include/utils/selfuncs.h`, verified `DEFAULT_EQ_SEL = 0.005`)

| constant | value | use |
|---|---|---|
| `DEFAULT_EQ_SEL` | 0.005 | eqsel w/o var/const, neqjoin fallback |
| `DEFAULT_INEQ_SEL` | 0.3333… | scalarineq w/o stats, inequality joins |
| `DEFAULT_RANGE_INEQ_SEL` | 0.005 | range-pair fallback |
| `DEFAULT_MULTIRANGE_INEQ_SEL` | 0.005 | multirange ops |
| `DEFAULT_MATCH_SEL` / `DEFAULT_MATCHING_SEL` | 0.005 / 0.010 | LIKE/regex, generic matching |
| `DEFAULT_NUM_DISTINCT` | 200 | numdistinct default |
| `DEFAULT_UNK_SEL` / `DEFAULT_NOT_UNK_SEL` | 0.005 / 0.995 | IS NULL / IS NOT NULL |

Pattern constants (`utils/adt/like_support.c`): `FIXED_CHAR_SEL 0.20`,
`CHAR_RANGE_SEL 0.25`, `ANY_CHAR_SEL 0.9`, `FULL_WILDCARD_SEL 5.0`,
`PARTIAL_WILDCARD_SEL 2.0`.

### 5.2 `eqsel` / `var_eq_const` / `var_eq_non_const` (`selfuncs.c:eqsel`, line 235, verified)

`get_restriction_variable` splits var/other sides (neither → default,
negated complement). Const side → `var_eq_const`:

```
const NULL → 0
nullfrac = stats ? stanullfrac : 0
isunique and tuples ≥ 1 → 1/tuples
stats + security check on opfunc:
    MCV hit (op(mcv, const) true) → its frequency
    miss: selec = 1 - Σmcv - nullfrac, CLAMP; otherdistinct = nd - nmcv; if > 1: /= otherdistinct
          cap at least-common MCV frequency
else → 1/nd
negate → 1 - selec - nullfrac; CLAMP_PROBABILITY
```

`var_eq_non_const`: unique → `1/tuples`; stats →
`(1−nullfrac)/nd` capped at the *largest* MCV frequency; else `1/nd`.
`neqsel` = negated `eqsel`.

### 5.3 `scalarineqsel` and the histogram (`selfuncs.c`, line 588, verified)

- No stats: ctid → block-fraction arithmetic, else `DEFAULT_INEQ_SEL`.
- `mcv_selec = mcv_selectivity(...)` (line 740, verified; MCV
  frequencies satisfying the op), `sumcommon` = all MCV frequencies.
- `hist_selec = ineq_histogram_selectivity(...)` (line 1049, verified)
  or −1; `selec = (1 − nullfrac − sumcommon) × (hist ≥ 0 ? hist : 0.5) +
  mcv_selec`.
- Histogram: binary search for first bound failing `op(bound, const)`
  (endpoint refresh §4.3 at extremes); bin `i = lobound` with
  `convert_to_scalar` mapping (numerics direct, strings via common-prefix
  base, bytea, datetimes, networks; failure → 0.5 of bin);
  `histfrac = ((i−1) + binfrac)/(nvalues−1)`; `i == 1` adds
  `eq_selec = 1/(ndistinct−nMCV)`; `isgt == iseq` subtracts it;
  `hist = isgt ? 1−histfrac : histfrac`. Verified endpoint: clamp to
  `[0.01/(nvalues−1), 1−…]` unless the endpoint was probe-verified.
  Incompatible-but-present histogram: fraction of satisfying bounds,
  same cutoff clamp.
- Wrappers (`scalarltsel/scalarlesel/scalargtsel/scalargesel`) flip
  `isgt` for left-side constants, require Const others.

### 5.4 NULL, boolean, pattern, array, row-compare

- `nulltestsel` (line 1706, verified): stats → `stanullfrac` /
  `1−stanullfrac`; system cols → 0/1; else defaults.
- `booltestsel`: MCV → true/false frequencies (`values[0]`-aware);
  stats w/o MCV → `(1−nullfrac)/2` vs `(nullfrac+1)/2`; no stats →
  UNKNOWN defaults, `IS TRUE`/`IS NOT FALSE` = arg selectivity.
  `boolvarsel`: bare boolean = `var_eq_const(=, true)` or 0.5.
- `patternsel_common` (`like_support.c`, line 483, verified): default
  `DEFAULT_MATCH_SEL`; needs left var + non-null text/bytea Const;
  exact prefix → `var_eq_const`; else histogram fraction
  (`min_hist_size = 10, n_skip = 1`; blended with heuristic by
  `hist_size/100` when < 100), clamped [0.0001, 0.9999], MCV-adjusted,
  negated complement. Prefix selectivity via `ineq_histogram_selectivity`
  on `>= prefix` and `< greaterstr` (`make_greater_string`), floored by
  `var_eq_const(prefix)`.
- `scalararraysel` (line 1824, verified): MCELEM containment first for
  equality on array vars; else per-element `s2` combined —
  `= ANY`: disjoint sum `Σs2` when in [0,1] else OR-combine
  (`s1+s2−s1·s2`); `<> ALL`: `1−Σ(1−s2)` analog; non-constant arrays
  evaluated once against `CaseTestExpr` and combined 10 times.
- `rowcomparesel`: first column pair + first operator only, as join or
  restriction by context.

---

## 6. `clauselist_selectivity` (`optimizer/path/clausesel.c`, line 100, verified)

`clauselist_selectivity` = `_ext(..., use_extended_stats = true)`:

1. Single clause → `clause_selectivity_ext` (line 667, verified).
2. Single-relation clause list with `statlist != NIL` →
   `statext_clauselist_selectivity(..., is_or=false)` (§8.3); covered
   clauses marked in `estimatedclauses`.
3. Remaining: pseudoconstants multiply directly; binary `OpExpr` range
   bounds (`scalarltsel/scalarlesel` high, `scalargtsel/scalargesel`
   low) on the same `equal()` var queued via `addRangeClause` (same
   direction keeps the smaller); else `s1 *= s2`.
4. Range pairing per var with both bounds: either bound default →
   `DEFAULT_RANGE_INEQ_SEL`; else `hi + lo − 1 + P(IS NULL)`; `≤ 0`:
   `< −0.01` → `DEFAULT_RANGE_INEQ_SEL` (stale/contradictory), else
   `1e-10`; one-sided entries multiply singly.

`clause_selectivity_ext`: RestrictInfo wrapper (pseudoconstant non-Const
→ 1.0; `norm_selec` for JOIN_INNER / `outer_selec` otherwise, cached
when single-rel on `varRelid`; `orclause` walked when set); Var →
`boolvarsel`; Const → null/0/1 (Param folded via
`estimate_expression_value` else 0.5); NOT → `1−s`; AND → recursive
list; OR → `s1+s2−s1·s2` (extended stats first, `is_or = true`);
OpExpr/DistinctExpr → `join_selectivity` (`oprjoin`) iff `varRelid ==
0 && sjinfo && >1 base rel`, else `restriction_selectivity`
(`oprrest`), 0.5 when missing, error outside [0,1];
`DistinctExpr` → `1−s`; FuncExpr → `function_selectivity` (prosupport),
default 0.5; SAOP → `scalararraysel`; RowCompare → `rowcomparesel`;
NullTest/BooleanTest → above; `CurrentOfExpr` → `1/tuples`;
RelabelType/CoerceToDomain → recurse; else `boolvarsel` (0.5-class).

---

## 7. Join selectivity (`selfuncs.c`)

### 7.1–7.3 `eqjoinsel` family (lines 2280/2445/2642, verified)

`eqjoinsel`: `get_join_variables`; `nd1/nd2 =
get_variable_numdistinct`; MCVs loaded only when **both** sides have
slots and pass security; `selec_inner = eqjoinsel_inner(...)`; SEMI/ANTI
→ `eqjoinsel_semi` on (possibly commuted) vars, capped at
`inner_rel->rows × selec_inner`.

`eqjoinsel_inner`, both MCVs: pairwise match with the join operator's
own function (cross-type safe); `matchprodfreq = Σ matched products`;
`matchfreq/unmatchfreq/otherfreq = 1−null−match−unmatch` per side;
`totalsel1 = matchprodfreq + unmatch1·other2/(nd2−nvalues2) [nd2 >
nvalues2] + other1·(other2+unmatch2)/(nd2−nmatches) [nd2 > nmatches]`;
symmetric `totalsel2`; result = min. No MCVs:
`(1−nf1)(1−nf2)/max(nd1, nd2)`.

`eqjoinsel_semi` (fraction of *outer* rows with ≥1 match): `nd2`
clamped to `rel->rows` and `inner_rel->rows`; with MCVs over the first
`min(nvalues2, nd2)` inner entries:
`matchfreq1 + uncertainfrac × (1−matchfreq1−nf1)` where
`uncertainfrac = (nd1 ≤ nd2 or nd2 < 0) ? 1 : nd2/nd1` (0.5 when any nd
default, after `nd1−=nmatches, nd2−=nmatches` when both exact); without:
`(nd1 ≤ nd2 or nd2 < 0) ? 1−nf1 : (nd2/nd1)(1−nf1)` (0.5× when default).

### 7.4–7.6 The rest

- `neqjoinsel`: SEMI/ANTI → `1−nullfrac(outer)`; else `1 −
  eqjoinsel(negator)` (or `1 − DEFAULT_EQ_SEL`). Inequality joins
  (`scalarlt/le/gt/gejoinsel`) → `DEFAULT_INEQ_SEL` unconditionally.
- `mergejoinscansel` (line 2963, verified): each side's end/start
  fractions from `scalarineqsel` against the other side's max/min
  (`get_variable_range`); larger end / smaller start wins (both reset on
  tie); `nulls_first` adds `stanullfrac` to both start and end (plain
  addition); default-valued results leave 0/1 in place; degenerate
  `start ≥ end` resets to 0/1.
- `estimate_hash_bucket_stats` (line 4060, verified): `mcv_freq =
  numbers[0]` (0 if none); default nd → `max(0.1, mcv)`; else
  `avgfreq = (1−nullfrac)/nd`, `nd *= rows/tuples` (clamped),
  `estfract = 1/(nd > nbuckets ? nbuckets : nd)`, skew-scaled by
  `mcv/avgfreq`, clamped [1e-6, 1].

---

## 8. Group / distinct estimation

### 8.1 `estimate_num_groups` (line 3449, verified)

```
input = clamp(input_rows); empty exprs (or empty pgset) → 1.0
bool expr → ×2; stats/unique expr → unique var; Vars pulled (aggregates/window/PHVs recursed);
    no Vars: volatile → input_rows, else skip (constant)
exprs_known_equal Vars collapse to smaller ndistinct (add_unique_group_var)
per rel: product of ndistincts (multivariate ndistinct first, §8.2),
    clamped to rel->tuples (×0.1, floored at largest single nd, when >1 var);
    restriction scaling D·(1−((tuples−rows)/tuples)^(tuples/D)) when rows < tuples  (Yao)
× srf_multiplier; ceil; clamp [1, input_rows]
```

`SELFLAG_USED_DEFAULT` set in `estinfo->flags` when a default nd was
used (Memoize treats that as "every call distinct").

### 8.2 Consumers

GROUP BY/grouping sets (`plan/planner.c:get_number_of_groups`, per-set
sum); DISTINCT (`create_distinct_paths`); `create_unique_path`
(`util/pathnode.c`: over `semi_rhs_exprs`, or rel rows when provably
unique); set-ops (`prep/prepunion.c` group counts); LIMIT/OFFSET
(`pathnode.c:adjust_limit_rows_costs`: unknown offset/count → 10% of
input, capped, ≥ 1); hash-agg sizing (`estimate_hashagg_tablesize =
hash_agg_entry_size × groups`); incremental sort / Memoize / window
startup tuples.

---

## 9. Extended statistics (`src/backend/statistics/`)

### 9.1 Build (`extended_stats.c:BuildRelationExtStatistics`, same ANALYZE sample)

Target = explicit `stxstattarget`, else max column target, else default.
Per kind: `'d'` (`mvdistinct.c`): every combo of size 2..k, Duj1
`n·d/((n−f1)+f1·n/N)` clamped `[d, N]`; `'f'`
(`dependencies.c:statext_dependencies_build`): ordered lists
`a1..ak−1 → ak`, `degree = supporting_rows/numrows`, zero-degree
dropped; `'m'` (`mcv.c:statext_mcv_build`): count-desc groups,
`nitems = min(target, ngroups)`, cut below
`get_mincount_for_mcv_list(n, N) = n(N−n)/(N−n+0.04n(N−1))` (4%
relative error), `frequency = count/numrows`, `base_frequency = Π
per-column sample frequencies` (independence prediction); `'e'`:
normal `compute_stats` per expression, serialised into `stxdexpr`.
Stored transactionally by `statext_store` into
`pg_statistic_ext_data`.

### 9.2–9.3 Compatibility and application (`extended_stats.c:statext_clauselist_selectivity`, line 1981, verified)

Compatible: restrictions on the rel (no pseudoconstants/volatile;
leakproof or SELECT privilege); `Var op Const` with `oprrest ∈
{eqsel, neqsel, scalar{lt,le,gt,ge}sel}`; left-expr `ScalarArrayOp`;
AND/OR/NOT thereof; `NullTest`; bare bool Var; matched expressions.
`statext_mcv_clauselist_selectivity`: `choose_best_statistics` (most
covered attrs+exprs, need ≥ 2, ties → fewest keys); AND:
`simple_sel` (no ext stats) vs `mcv_sel`/`basesel`/`totalsel` from
`mcv_get_match_bitmap`, combined
`sel = mcv_sel + clamp(simple_sel − basesel, 0, 1 − totalsel)`
(`mcv.c:mcv_combine_selectivities`); OR: per-clause accumulation with
overlap tracking. `dependencies_clauselist_selectivity`
(`dependencies.c`): equality/`useOr`-SAOP/OR/NOT/bare-Var clauses only
(**no NullTest branch** — IS NULL is MCV-only);
`find_strongest_dependency` (most attrs, then degree), dependent
attribute removed each round; applied weakest→strongest:
`attr[b] = (s1 ≤ s2) ? f + (1−f)·s2 : f·s2/s1 + (1−f)·s2`
(`P(a,b) = P(a)·(f + (1−f)·P(b))` chain, capped); result = Π.
MCV runs first (AND only), then dependencies; covered clauses marked
in `estimatedclauses`.
Expression stats (`'e'`) feed `examine_variable` (§4) so scalar
estimators work unchanged; multivariate ndistinct (≥ 2 matched keys)
replaces the product in `estimate_num_groups`.

---

## 10. Foreign keys, join sizes, invalidation

- `util/plancat.c:get_relation_foreign_keys` builds `root->fkey_list`
  (both tables in query); `plan/initsplan.c:match_foreign_keys_to_quals`
  keeps FKs with every column matched to an EC
  (`nmatched_ec`/`nconst_ec`) or a `conpfeqop` join clause
  (`nmatched_ri`/`nmatched_rcols`).
- `get_foreign_key_join_selectivity`
  (`optimizer/path/costsize.c`, line 5651, verified):
  `fkselec = Π 1/max(ref_tuples,1)` per matched FK (SEMI/ANTI:
  `ref_rows/ref_tuples`, referenced side = whole inner); matched clauses
  removed only on exact count (`nmatched_ec − nconst_ec + nmatched_ri`);
  divide by const-EC clause selectivity; no null derating; clamped;
  covered clauses pruned before `clauselist_selectivity`.
- `calc_joinrel_size_estimate` (line 5501, verified):

  | jointype | rows |
  |---|---|
  | INNER | `outer × inner × fk × j` |
  | LEFT | `max(outer×inner×fk×j, outer) × p` |
  | FULL | `max(…, outer, inner) × p` |
  | SEMI | `outer × fk × j` |
  | ANTI | `outer × (1 − fk×j) × p` |

  (`j` over non-pushed-down quals for outer joins, `p` over
  pushed-down; result `clamp_row_est`ed.)
- Invalidation/caching: in-place `pg_class` writes queue relcache
  invalidations, so ANALYZE invalidates cached plans referencing the
  table (`utils/cache/plancache.c:PlanCacheRelCallback`); `pg_statistic`
  rows are ordinary catalog tuples (syscache `STATRELATTINH`
  invalidations; catalog snapshot reads). Generic-vs-custom:
  `plan_cache_mode` override; first 5 executions custom; then generic
  iff cheaper than average custom cost (`choose_custom_plan`). PG 18
  statistics import/export: `statistics/relation_stats.c` +
  `attribute_stats.c` `pg_restore_*_stats` (transactional upserts, not
  in-place), `pg_dump --statistics` emits them
  (`bin/pg_dump/pg_dump.c:dumpRelationStats`). Standby plans with the
  primary's stats via WAL; the index endpoint probe is read-only.

---

## 11. Reimplementation checklist

**Relation stats**
1. Fresh table `0/−1/0`; TRUNCATE resets; `reltuples` = live tuples. (`heap.c`, `relcache.c`, `vacuum.c`)
2. `vac_update_relstats` in-place, non-transactional, changed-fields-only; PG 18 `relallfrozen`. (`vacuum.c:1442`)
3. VACUUM keeps old tuples at < 2% scanned (same size) or ≤ 1 page; else old-density extrapolation + scanned live. (`vacuum.c:1346`)
4. ANALYZE: `reltuples = totalrows`, live block count, VM counts; indexes `ceil(tupleFract × totalrows)`; column-subset writes reltuples only. (`analyze.c:do_analyze_rel`)
5. CREATE INDEX writes both sides, preserves stored −1 on 0. (`index.c:index_update_stats`)
6. `estimate_rel_size`: live pages, 10-page floor (never-analyzed, non-parent), density or fillfactor/width formula (integer division, ≥ 1), unscaled `allvisfrac` ∈ [0,1]. (`plancat.c: estimate_rel_size`; heap impl `tableam.c: table_block_relation_estimate_size`)
7. Widths: `stawidth` else type width. (`plancat.c:get_rel_data_width`)
8. Index metapage discount; non-partial `tuples = rel->tuples`; partial capped; btree height via AM else −1; skip invalid/too-new; `predOK` later. (`plancat.c:get_relation_info`)

**ANALYZE**
9. Targets: `attstattarget` → default_statistics_target 100 (max 10000); 0 disables. (`analyze.c`, `guc_tables.c`)
10. `=`+`<` → scalar, `=`-only → distinct, neither → trivial; `minrows = 300 × target`; `targrows = max(100, max)`. (`analyze.c:std_typanalyze`)
11. Knuth-S block sampling of `min(targrows, nblocks)`; Vitter reservoir rows; `totalrows = floor(live/read × total + 0.5)`; TID sort when full; inherited `rint(targrows × childblocks/total)`. (`analyze.c:1199`, `sampling.c`)
12. Too-wide (> 1024 raw) counted in width, excluded from values, treated distinct. (`analyze.c:compute_scalar_stats`)
13. `stanullfrac = null/sample`; `stawidth` over non-nulls. (same)
14. ndistinct: all-distinct → negative fraction; all-repeated → count; else Duj1 clamped/rounded; > 10% of table → negative fraction. (same)
15. Complete MCV list kept when it fits; else hypergeometric `2σ+0.5` prune from the bottom; no 1.25× rule. (`analyze.c:2980`)
16. MCV freqs = count/sample-rows; histogram `min(nd−mcv, target+1)` bounds ≥ 2, MCVs removed, delta/posfrac bounds, MIN…MAX; Pearson correlation, `staop = <`. (same)
17. `compute_distinct_stats` MCV-only. (`analyze.c:2059`)
18. Expression-index columns get stats with `(numindexrows, totalindexrows)`; `n_distinct[_inherited]` overrides; `stainherit = inh`; same-sample ext-stats build; cumulative-stats report. (`analyze.c`)
19. Autoanalyze `mod > 50 + 0.1×reltuples`; autovacuum `dead > 50 + 0.2×reltuples` (cap 1e8). (`autovacuum.c`)

**Catalog**
20. 5 slots `(kind, op, coll, numbers float4[], values anyarray)`; kinds 1–7 (MCV/HISTOGRAM/CORRELATION/MCELEM/DECHIST/RANGE_LENGTH/BOUNDS). (`pg_statistic.h`)
21. MCV `=` / histogram+correlation `<`; histogram NULL numbers, correlation NULL values; values type = typanalyze's. (`analyze.c`)
22. Unique `(starelid, staattnum, stainherit)` → `STATRELATTINH`; `pg_stats` CASE-mapping + privilege/RLS filter. (`pg_statistic.h`, `system_views.sql`)
23. Ext kinds `'d','f','m','e'`; data keyed `(stxoid, stxdinherit)` → `STATEXTDATASTXOID`. (`pg_statistic_ext*.h`)

**Variables**
24. Lookup by `rte->inh`; unique-index Var → `isunique`; expression-index then `'e'` stats; subquery/CTE resolution (no set-ops/grouping-sets/security-barrier; single-col DISTINCT/GROUP → unique); leakproof-or-SELECT rule. (`selfuncs.c:5292,5684`)
25. numdistinct: bool 2, VALUES/ctid unique, tableoid 1; unique forces `−(1−nullfrac)`; negatives × `rel->tuples`; default 200 (flag only then). (`selfuncs.c:6258`)
26. Range = histogram ± MCVs (MCV-only at `Σmcv+null > 0.99999`); extreme-bucket index probe, `SnapshotNonVacuumable`, 100-page cap. (`selfuncs.c:6581`)

**Restriction estimators**
27. eq-const: unique `1/tuples`; MCV hit = freq; miss `(1−Σmcv−null)/(nd−nmcv)` capped at least MCV; else `1/nd`; negate `1−s−null`. (`selfuncs.c:var_eq_const`)
28. eq-nonconst `(1−null)/nd` capped at max MCV; neq = complement. (same)
29. ineq = MCV part + `(1−null−Σmcv) × hist` (0.5 w/o hist); scalar bin interpolation, 0.5 on failure; probe-less clamp `[0.01/(nb−1), 1−…]`; ctid block math w/o stats. (`selfuncs.c:588,1049`)
30. NULL → nullfrac / 1−nullfrac (defaults 0.005/0.995); bool from MCV else halves; bare bool = eq-true or 0.5. (`selfuncs.c:1706`, booltestsel/boolvarsel)
31. Pattern: exact-prefix → eq; else histogram/heuristic blend by hist_size/100, [0.0001, 0.9999], MCV adjust; prefix via `>=`/`<greaterstr` floored by eq. (`like_support.c:483`)
32. `= ANY` disjoint sum in [0,1] else OR-combine; `<> ALL` dual; non-const = 10 elements. (`selfuncs.c:1824,2147`)
33. RowCompare = first column pair only. (`selfuncs.c:rowcomparesel`)

**Clause lists / joins / groups / ext / FK**
34. Ext stats before independence (single-rel + `statlist`); range pair `hi+lo−1+P(NULL)`; default/contradictory → 0.005 (`< −0.01`) else 1e-10; same-direction keeps smaller. (`clausesel.c`)
35. OR `s1+s2−s1·s2`; NOT `1−s`; join-iff `varRelid==0 && sjinfo && >1 rel`; RestrictInfo `norm/outer_selec` cache; pseudoconstant non-Const = 1.0; fallback 0.5. (`clausesel.c:667`)
36. eqjoin needs both MCVs; inner = min(totalsel1, totalsel2); no-MCV `(1−nf1)(1−nf2)/max(nd)`; SEMI `nd2` clamped + `rows×inner` cap; SEMI-MCV match+uncertain formula; neq SEMI/ANTI `1−null(outer)` else complement; inequality joins 0.3333. (`selfuncs.c:2280,2445,2642`)
37. Mergejoin scansel: scalarineq vs opposite min/max, larger-end/smaller-start wins, nulls_first plain-adds nullfrac, defaults keep 0/1, degenerate resets. (`selfuncs.c:2963`)
38. Hash buckets: default `max(0.1, mcv)`; else `1/min(buckets, nd×rows/tuples)` × skew, [1e-6,1]. (`selfuncs.c:4060`)
39. Groups: bool ×2; volatile-const-free → input rows; per-rel product clamped to tuples (×0.1 multi-var, floor max single); Yao restriction scaling; ceil, [1, input]; ndistinct-ext for ≥ 2 keys; equal-vars collapse to smaller nd; LIMIT/OFFSET default 10%. (`selfuncs.c:3449`, `pathnode.c:adjust_limit_rows_costs`)
40. Ext build: per-combo Duj1 (d), supporting-rows ratio (f, zero dropped), MCV 4%-error cut + base frequencies, expr stats serialised; MCV-first-then-dependencies; best object ≥ 2 covered (fewest-keys tiebreak); `mcv_sel + clamp(simple−base, 0, 1−total)`; dependency chain weakest→strongest; NullTest MCV-only. (`statistics/`, `extended_stats.c:1981`)
41. FK: keep fully-matched only; `1/max(ref_tuples,1)` (SEMI/ANTI `ref_rows/ref_tuples`); exact-count pruning; const-EC division; no null derating; join-size table (§10). (`initsplan.c`, `costsize.c:5651,5501`)
42. In-place `pg_class` writes invalidate cached plans; generic after 5 customs when cheaper; `plan_cache_mode` overrides; PG18 stats import/export + `pg_dump --statistics`. (`heapam.c`, `plancache.c`, `statistics/`)
