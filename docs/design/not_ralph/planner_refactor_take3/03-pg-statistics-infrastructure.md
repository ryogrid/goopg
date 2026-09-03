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

---

## 12. Worked selectivity examples (synthetic inputs)

All inputs in this section are SYNTHETIC and hand-constructed for
illustration. They are not measurements from the live PG 18.3 oracle.
Each example names the estimator in
`postgres/src/backend/utils/adt/selfuncs.c` it exercises and shows every
arithmetic step. All arithmetic below was verified with `python3 -c`.

### 12.1 Scalar inequality via histogram interpolation (`scalarineqsel` / `ineq_histogram_selectivity`)

SYNTHETIC INPUT: column `t.x` with `stanullfrac = 0.1`, MCV total
`sumcommon = 0.2`, satisfying-MCV part `mcv_selec = 0.05`, histogram
bounds `nvalues = 5` with values `[0, 10, 20, 30, 40]`, constant `25`,
operator `<` (`isgt = False`, `iseq = False`), `ndistinct = 22`,
`nMCV = 2`.

1. Binary search for the first bound failing `op(bound, const)`: bounds
   `0`, `10`, `20` satisfy `< 25`; bound `30` fails. Hence bin index
   `i = 4` with `low = 20` and `high = 30`.
2. `binfrac = (val - low) / (high - low) = (25 - 20) / (30 - 20) =
   5 / 10 = 0.5`.
3. `histfrac = ((i - 1) + binfrac) / (nvalues - 1) = (3 + 0.5) / 4 =
   3.5 / 4 = 0.875`.
4. Because `isgt == iseq` (`False == False`), compute `eq_selec = 1 /
   (ndistinct - nMCV) = 1 / (22 - 2) = 1 / 20 = 0.05` and subtract it:
   `hist = histfrac - eq_selec = 0.875 - 0.05 = 0.825`.
5. Cutoff is `0.01 / (nvalues - 1) = 0.01 / 4 = 0.0025`, so the allowed
   interval is `[0.0025, 0.9975]`. `0.825` is inside, no clamp.
6. `scalarineqsel` combination: `selec = (1 - nullfrac - sumcommon) *
   hist + mcv_selec = (1 - 0.1 - 0.2) * 0.825 + 0.05 = 0.7 * 0.825 +
   0.05 = 0.5775 + 0.05 = 0.6275`.
7. On `N = 10000` rows, `rows = 10000 * 0.6275 = 6275` after rounding
   (`python3 -c` gives `6274.999999999999`, which rounds to `6275`).

This is the `ineq_histogram_selectivity` path in
`postgres/src/backend/utils/adt/selfuncs.c:ineq_histogram_selectivity`
called from `scalarineqsel`, with the `convert_to_scalar` linear
interpolation inside the bin and the `isgt == iseq` boundary correction
from §5.3.

### 12.2 Equality via MCV plus histogram remainder (`var_eq_const`)

SYNTHETIC INPUT: `stanullfrac = 0.05`, MCV slot with three entries
`A = 0.3`, `B = 0.15`, `C = 0.05`, `ndistinct = 10`, `nnumbers = 3`.
Exercised estimator:
`postgres/src/backend/utils/adt/selfuncs.c:var_eq_const` after
`examine_simple_variable` loads the `STATRELATTINH` tuple.

1. `sumcommon = 0.3 + 0.15 + 0.05 = 0.5`.
2. Hit case: constant equals `B`, so `selec = 0.15`. On `N = 10000`,
   `rows = 10000 * 0.15 = 1500.0`.
3. Miss case: constant equals `D` (not in the MCV list). Remainder is
   `1 - sumcommon - nullfrac = 1 - 0.5 - 0.05 = 0.45`.
4. `otherdistinct = ndistinct - nnumbers = 10 - 3 = 7`.
5. Uncapped miss selectivity is `0.45 / 7 = 0.0642857142857143`.
6. Cap at the least-common MCV frequency `0.05`: because
   `0.0642857142857143 > 0.05` is true, `selec = 0.05`. On
   `N = 10000`, `rows = 10000 * 0.05 = 500.0`.
7. Negated form (`<>`, `negate = True`) on the hit case is `1 - selec -
   nullfrac = 1 - 0.15 - 0.05 = 0.8`, followed by
   `CLAMP_PROBABILITY`.

Without stats the same call would fall back to `1 / ndistinct = 1 / 10
= 0.1`; with `isunique` and `tuples = 1000` it would return `1 / 1000 =
0.001` (both fallback arms of `var_eq_const` in §5.2).

### 12.3 Inner equi-join MCV pairing (`eqjoinsel_inner`)

SYNTHETIC INPUT: left MCVs `{A: 0.5, B: 0.3, C: 0.1}` with `nd1 = 5`
and `nullfrac1 = 0.0`; right MCVs `{A: 0.25, B: 0.25, D: 0.25}` with
`nd2 = 4` and `nullfrac2 = 0.0`. Estimator:
`postgres/src/backend/utils/adt/selfuncs.c:eqjoinsel_inner`, both-MCV
branch from §7.2.

1. Pairwise match with the join operator finds `A` and `B`, so
   `nmatches = 2`.
2. `matchprodfreq = 0.5 * 0.25 + 0.3 * 0.25 = 0.125 + 0.075 = 0.2`.
3. Left side: `matchfreq1 = 0.5 + 0.3 = 0.8`, `unmatchfreq1 = 0.1`,
   `otherfreq1 = 1 - 0.0 - 0.8 - 0.1 = 0.1`.
4. Right side: `matchfreq2 = 0.25 + 0.25 = 0.5`, `unmatchfreq2 = 0.25`,
   `otherfreq2 = 1 - 0.0 - 0.5 - 0.25 = 0.25`.
5. `totalsel1 = matchprodfreq + unmatchfreq1 * otherfreq2 / (nd2 -
   nvalues2) + otherfreq1 * (otherfreq2 + unmatchfreq2) / (nd2 -
   nmatches) = 0.2 + 0.1 * 0.25 / 1 + 0.1 * 0.5 / 2 = 0.2 + 0.025 +
   0.025 = 0.25`. Here `nvalues2 = 3`, so `nd2 - nvalues2 = 4 - 3 = 1`
   and `nd2 - nmatches = 4 - 2 = 2`.
6. `totalsel2 = 0.2 + 0.25 * 0.1 / (5 - 3) + 0.25 * (0.1 + 0.1) / (5 -
   2) = 0.2 + 0.0125 + 0.016666666666666666 = 0.22916666666666669`.
   The two terms are `0.25 * 0.1 / 2 = 0.0125` and `0.25 * 0.2 / 3 =
   0.016666666666666666`.
7. `selec = min(totalsel1, totalsel2) = min(0.25,
   0.22916666666666669) = 0.22916666666666669`.
8. With `1000` outer rows and `4` inner rows, the INNER join estimate is
   `1000 * 4 * 0.22916666666666669 = 916.6666666666667`, i.e. `917`
   after `clamp_row_est` rounding.

Without MCVs the same inputs would use `(1 - 0.0) * (1 - 0.0) / max(5,
4) = 1 / 5 = 0.2`.

### 12.4 Multivariate ndistinct and functional-dependency down-weighting

SYNTHETIC INPUT (ndistinct): table `t(a, b)` with `N = 10000` rows,
`ndistinct(a) = 100`, `ndistinct(b) = 100`. Estimators:
`postgres/src/backend/utils/adt/selfuncs.c:estimate_num_groups` with
`estimate_multivariate_ndistinct`.

1. Independence product is `100 * 100 = 10000`.
2. Multi-column clamp is `0.1 * 10000 = 1000.0`. Hence `min(10000,
   1000) = 1000` without extended statistics.
3. With a `STATS_EXT_NDISTINCT` object storing `ndistinct(a, b) = 100`,
   the product is replaced by `100`.

SYNTHETIC INPUT (dependencies): `WHERE a = 1 AND b = 1` with
`s1 = 0.01` and `s2 = 0.01`, dependency `a -> b` of degree `f = 0.9`.
Estimator: `clauselist_apply_dependencies` in
`postgres/src/backend/statistics/dependencies.c` via
`extended_stats.c:statext_clauselist_selectivity`.

1. Independence gives `s1 * s2 = 0.01 * 0.01 = 0.0001`.
2. Because `s1 <= s2` (`0.01 <= 0.01`), `attr[b] = f + (1 - f) * s2 =
   0.9 + 0.1 * 0.01 = 0.9 + 0.001 = 0.901`.
3. Result is `s1 * attr[b] = 0.01 * 0.901 = 0.00901`. On `N = 10000`,
   `10000 * 0.00901 = 90.10000000000001` (about `90` rows) versus `10000
   * 0.0001 = 1.0` row under independence.

Second branch illustration with `s1 = 0.1`, `s2 = 0.02`, `f = 0.8`
(`s1 > s2`): `attr[b] = f * s2 / s1 + (1 - f) * s2 = 0.8 * 0.02 / 0.1
+ 0.2 * 0.02 = 0.16 + 0.004 = 0.164`; result `0.1 * 0.164 = 0.0164`
versus independence `0.1 * 0.02 = 0.002`; on `N = 10000` that is
`164.0` rows versus `20.0` rows.

### 12.5 `estimate_num_groups` on a small distinct-count example

SYNTHETIC INPUT: `GROUP BY a, b` with `ndistinct(a) = 5`,
`ndistinct(b) = 4`, `rel->tuples = 1000`, `rel->rows = 1000`,
`input_rows = 1000`. Estimator:
`postgres/src/backend/utils/adt/selfuncs.c:estimate_num_groups`.

1. Product is `5 * 4 = 20`.
2. With `2` grouping variables the clamp is `0.1 * 1000 = 100.0`, and
   `min(20, 100) = 20`.
3. No Yao restriction scaling applies because `rows = 1000` equals
   `tuples = 1000`. After `ceil` and clamping to `[1, 1000]`, groups
   `= 20`.

Yao scaling illustration with `D = 500`, `tuples = 1000`, `rows = 100`:

1. Remaining fraction is `(1000 - 100) / 1000 = 900 / 1000 = 0.9`.
2. Exponent is `tuples / D = 1000 / 500 = 2.0`.
3. `0.9 ** 2.0 = 0.81`, so `D * (1 - 0.81) = 500 * 0.19 = 95.0`
   (`python3 -c` gives `94.99999999999997`, i.e. `95.0`). After `ceil`
   and clamping to `[1, 100]`, groups `= 95`.

Near-unity illustration with `D = 20`, `tuples = 1000`, `rows = 500`:
remaining fraction `500 / 1000 = 0.5`, exponent `1000 / 20 = 50.0`,
`0.5 ** 50.0 = 8.881784197001252e-16`, `20 * (1 -
8.881784197001252e-16) = 19.999999999999982` (about `20.0`), so almost
no reduction when the subset still covers half the table.

### 12.6 Nullfrac-corrected conjunction (`clauselist_selectivity`)

SYNTHETIC INPUT: `WHERE x > 10 AND x < 20` on the same `equal()` variable
with low-bound selectivity `lo = 0.6`, high-bound selectivity `hi = 0.7`,
and `P(x IS NULL) = 0.02` from `nulltestsel`. Estimator:
`postgres/src/backend/optimizer/path/clausesel.c:clauselist_selectivity_ext`
range pairing from §6.

1. Valid-range case: `s2 = hi + lo - 1.0 + P(NULL) = 0.7 + 0.6 - 1.0 +
   0.02 = 1.3 - 1.0 + 0.02 = 0.3 + 0.02 = 0.32` (`python3 -c` gives
   `0.31999999999999984`, i.e. `0.32`). On `N = 10000`, `rows = 10000 *
   0.32 = 3200.0`.
2. Contradictory-bounds case with `hi = 0.3` and `lo = 0.4`:
   `0.3 + 0.4 - 1.0 + 0.02 = 0.7 - 1.0 + 0.02 = -0.3 + 0.02 = -0.28`.
   Because `-0.28 < -0.01`, the pair falls back to
   `DEFAULT_RANGE_INEQ_SEL = 0.005`. On `N = 10000`, `rows = 10000 *
   0.005 = 50.0`.
3. Near-zero case with `hi = 0.5`, `lo = 0.49`, `P(NULL) = 0.005`:
   `0.5 + 0.49 - 1.0 + 0.005 = 0.99 - 1.0 + 0.005 = -0.01 + 0.005 =
   -0.005000000000000009` (about `-0.005`), which lies in `[-0.01, 0]`
   and therefore yields `1e-10`.

---

## 13. Extended checklist appendix (items 43-72)

This appendix continues the §11 numbering (last existing item is `42`)
and restores take2 checklist substance that §11 condenses. It adds the
per-estimator fallback chains, the `DEFAULT_*` constants table, the
extended-statistics `d`/`f`/`m`/`e` application order, the `ANALYZE`
sampling parameters, the invalidation message flow, and the per-type
`pg_statistic` slot layout. Existing §11 items and the catalog-layout
tables in §1-§3 are untouched.

### 13.1 `DEFAULT_*` and pattern constants (verified against take2 §5.1)

| constant | value | used by |
|---|---|---|
| `DEFAULT_EQ_SEL` | `0.005` | `eqsel` with no var/const, `neqjoinsel` fallback |
| `DEFAULT_INEQ_SEL` | `0.3333333333333333` | `scalarineqsel` without stats, inequality joins |
| `DEFAULT_RANGE_INEQ_SEL` | `0.005` | `clauselist` range-pair fallback |
| `DEFAULT_MULTIRANGE_INEQ_SEL` | `0.005` | multirange operators |
| `DEFAULT_MATCH_SEL` | `0.005` | `LIKE`/regex without stats |
| `DEFAULT_MATCHING_SEL` | `0.01` | generic matching operators |
| `DEFAULT_NUM_DISTINCT` | `200` | `get_variable_numdistinct` |
| `DEFAULT_UNK_SEL` | `0.005` | `IS NULL` / `IS UNKNOWN` without stats |
| `DEFAULT_NOT_UNK_SEL` | `0.995` | `IS NOT NULL` / `IS NOT UNKNOWN` (`1 - 0.005 = 0.995`) |
| `FIXED_CHAR_SEL` | `0.2` | `like_selectivity` per literal char |
| `CHAR_RANGE_SEL` | `0.25` | `like_selectivity` per char range |
| `ANY_CHAR_SEL` | `0.9` | `like_selectivity` per `_` |
| `FULL_WILDCARD_SEL` | `5.0` | `like_selectivity` per `%`, regex anchoring |
| `PARTIAL_WILDCARD_SEL` | `2.0` | `like_selectivity` partial prefix |
| pattern clamp | `[0.0001, 0.9999]` | `patternsel_common` result clamp |
| range-pair epsilon | `1e-10` | `clauselist` near-zero range result |
| hash-bucket clamp | `[1e-6, 1]` | `estimate_hash_bucket_stats` |
| histogram cutoff base | `0.01` | `ineq_histogram_selectivity` cutoff `0.01 / (nvalues - 1)` |

Header: `postgres/src/include/utils/selfuncs.h`; pattern constants:
`postgres/src/backend/utils/adt/like_support.c`.

### 13.2 New checklist items

**Per-estimator fallback chains**

43. Neither side is a single-rel variable in `eqsel_internal` yields
`DEFAULT_EQ_SEL = 0.005`, negated `1 - 0.005 = 0.995`. (`selfuncs.c:eqsel`)
44. `var_eq_const` with a `NULL` constant yields `0.0`; unique variable
with `rel->tuples = 1000` yields `1 / 1000 = 0.001`; no stats and no
unique yields `1 / ndistinct` (e.g. `1 / 10 = 0.1` with `ndistinct =
10`). (`selfuncs.c:var_eq_const`)
45. `scalarineqsel` without stats uses `ctid` block-fraction arithmetic
and otherwise `DEFAULT_INEQ_SEL = 0.3333333333333333`; a failed
`convert_to_scalar` contributes `0.5` of the bin; a missing histogram
contributes `hist = 0.5`. (`selfuncs.c:scalarineqsel`,
`ineq_histogram_selectivity`)
46. `nulltestsel` on system columns (`varattno < 0`) yields `0` for `IS
NULL` and `1` for `IS NOT NULL`; `booltestsel` without stats maps
`IS TRUE` and `IS NOT FALSE` to the argument selectivity; bare
`boolvarsel` without stats yields `0.5`. (`selfuncs.c:nulltestsel`,
`booltestsel`, `boolvarsel`)
47. `patternsel_common` without stats or without a usable histogram
(`hist_size < 10`) falls back to the heuristic; `hist_size < 100`
blends histogram and heuristic by `hist_size / 100`; the result is
clamped to `[0.0001, 0.9999]` and then MCV-adjusted with `1 - nullfrac
- sumcommon`. (`like_support.c:patternsel_common`)
48. `scalararraysel` on a non-constant array assumes `10` elements via
`estimate_array_length`; `= ANY` uses the disjoint sum while it stays in
`[0, 1]` and otherwise OR-combines with `s1 + s2 - s1 * s2`; `<> ALL`
uses the dual `1 - s1disjoint` form. (`selfuncs.c:scalararraysel`)
49. `neqjoinsel` without a negator yields `1 - DEFAULT_EQ_SEL = 1 - 0.005
= 0.995`; `<`, `<=`, `>`, `>=` joins unconditionally yield
`DEFAULT_INEQ_SEL = 0.3333333333333333`; `mergejoinscansel` leaves the
`0`/`1` defaults in place on `DEFAULT_INEQ_SEL` inputs and resets
degenerate `start >= end` to `0`/`1`. (`selfuncs.c:neqjoinsel`,
`scalarltjoinsel`, `mergejoinscansel`)
50. `estimate_num_groups` with a volatile constant-free expression
returns `input_rows`; constant expressions are skipped; unknown
`LIMIT`/`OFFSET` assume `0.1` of input (e.g. `0.1 * 1000 = 100.0`);
`function_selectivity` defaults to `0.5`; unhandled
`clause_selectivity_ext` nodes yield `0.5`. (`selfuncs.c:3449`,
`pathnode.c:adjust_limit_rows_costs`, `clausesel.c:667`)

**`ANALYZE` sampling parameters**

51. `minrows = 300 * attstattarget`: target `100` gives `300 * 100 =
30000`; target `1` gives `300 * 1 = 300`; target `10000` gives `300 *
10000 = 3000000`; `targrows = max(100, max minrows)` (e.g.
`max(100, 30000) = 30000`). (`analyze.c:std_typanalyze`,
`do_analyze_rel`)
52. The `300` multiplier cites Chaudhuri-Motwani-Narasayya with `f =
 0.5`, `gamma = 0.01`, `n = 1000000`: `4 * log(2 * n / gamma) / (f * f)`
evaluates to `305.821246792197` (about `305.82`), rounded down to `300`.
(`analyze.c:std_typanalyze`)
53. Block sampling reads `min(targrows, nblocks)` blocks with Knuth
Algorithm S; row sampling is Vitter reservoir sampling; the sample is
sorted by `TID` when the reservoir filled (`numrows = 30000` with
`targrows = 30000` sorts). Live/dead counting uses
`HeapTupleSatisfiesVacuum` against `OldestXmin`.
(`analyze.c:acquire_sample_rows`, `sampling.c`)
54. Extrapolation is `floor(liverows / blocks_read * totalblocks +
0.5)`; inherited sampling budgets `rint(targrows * childblocks /
totalblocks)` per child with `convert_tuples_by_name` mapping.
(`analyze.c:acquire_sample_rows`, `acquire_inherited_sample_rows`)
55. Values with raw size above `WIDTH_THRESHOLD = 1024` are counted in
`stawidth` but excluded from value stats and treated as distinct;
`stanullfrac = null_cnt / samplerows`; `stawidth` averages over
non-nulls. (`analyze.c:compute_scalar_stats`)
56. `stadistinct` above `0.1 * totalrows` (e.g. `0.1 * 10000 = 1000.0`)
is stored as the negative fraction `-stadistinct / totalrows`; the
all-distinct case stores `-(1 - nullfrac)` (e.g. `-(1 - 0.05) = -0.95`
with `nullfrac = 0.05`). (`analyze.c:compute_scalar_stats`)
57. Kept MCVs pass the continuity-corrected hypergeometric test
`count_last > selec * samplerows + 2 * stddev + 0.5` from the least
common upward; the pre-PG11 `1.25 * average` rule does not exist.
(`analyze.c:analyze_mcv_list`)
58. Extended MCV build cuts below `get_mincount_for_mcv_list(n, N) = n *
(N - n) / (N - n + 0.04 * n * (N - 1))`; with `n = 30000` and `N =
1000000` the cutoff is `24.230437959753825` (about `24.23`) sample
occurrences, capped at `stattarget` items. (`mcv.c:statext_mcv_build`)

**`pg_statistic` slot layout per type**

59. `STATISTIC_NUM_SLOTS = 5`; kind codes are `1`, `2`, `3`, `4`, `5`,
`6`, `7` for `MCV`, `HISTOGRAM`, `CORRELATION`, `MCELEM`, `DECHIST`,
`RANGE_LENGTH_HISTOGRAM`, `BOUNDS_HISTOGRAM`. (`pg_statistic.h`)
60. `MCELEM` (`4`) stores most-common elements in element order with
per-element fractions of non-null rows plus `2` extra entries (minimum
and maximum frequency) and an optional third null-element entry; its
`staop` is the element `=`. (`pg_statistic.h`, array `selfuncs.c`)
61. `DECHIST` (`5`) stores a distinct-count histogram with no values and
last numbers entry equal to the average; `RANGE_LENGTH_HISTOGRAM`
(`6`) stores a length histogram whose single numbers entry is the empty
fraction; `BOUNDS_HISTOGRAM` (`7`) stores lower/upper bounds as ranges
with `NULL` numbers. (`pg_statistic.h`)
62. `CORRELATION` (`3`) stores exactly `1` numbers entry in `[-1, 1]`
with `NULL` values and `staop = <`; `HISTOGRAM` (`2`) stores `>= 2`
bounds with `NULL` numbers and `staop = <`; `MCV` (`1`) stores
frequencies of all rows with `staop = =`. (`pg_statistic.h`,
`analyze.c`)

**Extended-statistics kinds and application order**

63. Build order per object is `d` (`mvdistinct.c` Duj1 per combination of
size `2` to `k`), `f` (`dependencies.c` supporting-rows ratio, zero
degree dropped), `m` (`mcv.c` `4` percent relative-error cut with
`base_frequency` as the independence product), `e` (normal
`compute_stats` serialised into `stxdexpr`) from the same sample.
(`extended_stats.c:BuildRelationExtStatistics`)
64. `statext_clauselist_selectivity` runs MCV first on `AND` lists only,
then multiplies by the dependencies result; `OR` returns the MCV result
directly. Covered clauses are marked in `estimatedclauses`.
(`extended_stats.c:1981`)
65. `choose_best_statistics` needs at least `2` covered
attributes-plus-expressions (`best_num_matched` starts at `2`);
ties break toward fewer keys. (`extended_stats.c`)
66. MCV combination is `sel = mcv_sel + clamp(simple_sel - basesel, 0, 1
- totalsel)` (e.g. `simple_sel = 0.0001`, `basesel = 0.0001`,
`mcv_sel = 0.01` gives `other_sel = 0.0` and `sel = 0.01`).
(`mcv.c:mcv_combine_selectivities`)
67. Dependencies apply weakest to strongest with `attr[b] = f + (1 - f)
* s2` when `s1 <= s2` and `f * s2 / s1 + (1 - f) * s2` otherwise (e.g.
`f = 0.9`, `s2 = 0.01` gives `0.901`); `IS NULL` has no dependency
branch and is MCV-only. (`dependencies.c`)
68. Expression statistics (`e`) supply a full `pg_statistic` tuple so
scalar estimators work unchanged; multivariate ndistinct needs at least
`2` matched keys in `estimate_multivariate_ndistinct`.
(`extended_stats.c`, `selfuncs.c`)

**Invalidation message flow and related behavior**

69. `vac_update_relstats` finishes through
`systable_inplace_update_finish` into
`heap_inplace_update_and_unlock`, which calls
`CacheInvalidateHeapTupleInplace` before unlocking; `pg_statistic`
writes use transactional `CatalogTupleUpdate` with `STATRELATTINH`
syscache invalidations read under the catalog snapshot.
(`vacuum.c`, `heapam.c`, `analyze.c:update_attstats`)
70. `PlanCacheRelCallback` marks cached plans on the relation invalid;
`ANALYZE` therefore invalidates plans referencing the table.
`choose_custom_plan` runs `5` custom plans first, then picks generic
when cheaper than the average custom cost; `plan_cache_mode` overrides.
(`plancache.c`)
71. `pg_restore_relation_stats` and `pg_restore_attribute_stats` perform
transactional upserts (`heap_modify_tuple_by_cols` plus
`CatalogTupleUpdate`), not in-place writes; `pg_dump --statistics`
emits them; standbys replay the same WAL and plan with the primary
statistics. (`statistics/relation_stats.c`,
`statistics/attribute_stats.c`, `pg_dump.c:dumpRelationStats`)
72. The plan-time endpoint probe uses `SnapshotNonVacuumable`, needs a
non-partial btree-orderable index with `canreturn[0]`, stops after
`100` visited heap pages (`VISITED_PAGES_LIMIT = 100`), and widens
histogram endpoints with MCVs (MCV-only when `sum(mcv) + nullfrac >
0.99999`, e.g. `0.9 + 0.09999 = 0.99999`). (`selfuncs.c:6581`)
