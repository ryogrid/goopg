# 02 — PostgreSQL cost model (costsize.c and friends, PG 18.3 oracle)

Scope: every cost function the PostgreSQL 18.3 planner calls, at formula level,
with the constants and helper calls written out so that a reimplementation can
reproduce the same `startup_cost`, `total_cost`, `rows` and `disabled_nodes`
and therefore the same `add_path` decisions. All citations are paths under
`postgres/` (the read-only oracle); line numbers are those of PG 18.3.
Section 7 only defines the selectivity *interfaces* the cost model consumes;
the statistics machinery itself is doc 03.

Notation used throughout:

- `LOG2(x)` = `log(x) / 0.693147180559945` (`costsize.c:113`).
- `MAXALIGN(n)` rounds up to a multiple of 8 (`MAXIMUM_ALIGNOF 8`, `src/include/pg_config.h:570`).
- `SizeofHeapTupleHeader` = 23 → `MAXALIGN` = 24 (`src/include/access/htup_details.h:185`);
  `SizeofMinimalTupleHeader` = 15 → `MAXALIGN` = 16 (`htup_details.h:704`).
- `BLCKSZ` = 8192.
- `Cost` and `Selectivity` are C `double`s; `rows` is `Cardinality` (double).

---

## 1. Cost currency and GUCs

### 1.1 Cost GUCs (defaults from `src/include/optimizer/cost.h` and `src/backend/utils/misc/guc_tables.c`)

| GUC | C variable | PG 18 default | Unit / note | Citation |
|---|---|---|---|---|
| `seq_page_cost` | `seq_page_cost` | 1.0 | per sequentially fetched page | `cost.h:24`, `guc_tables.c:3881` |
| `random_page_cost` | `random_page_cost` | 4.0 | per random page | `cost.h:25`, `guc_tables.c:3892` |
| `cpu_tuple_cost` | `cpu_tuple_cost` | 0.01 | per tuple processed | `cost.h:26`, `guc_tables.c:3903` |
| `cpu_index_tuple_cost` | `cpu_index_tuple_cost` | 0.005 | per index entry visited | `cost.h:27`, `guc_tables.c:3914` |
| `cpu_operator_cost` | `cpu_operator_cost` | 0.0025 | per operator/function call (× `procost`) | `cost.h:28`, `guc_tables.c:3925` |
| `parallel_tuple_cost` | `parallel_tuple_cost` | 0.1 | per tuple passed worker→leader | `cost.h:29`, `guc_tables.c:3936` |
| `parallel_setup_cost` | `parallel_setup_cost` | 1000.0 | per Gather/GatherMerge | `cost.h:30`, `guc_tables.c:3947` |
| `effective_cache_size` | `effective_cache_size` | 524288 pages (4 GB) | **pages**, not bytes | `cost.h:34`, `guc_tables.c:3714` |
| `work_mem` | `work_mem` | 4096 kB | kB; min 64 | `guc_tables.c:2574` |
| `hash_mem_multiplier` | `hash_mem_multiplier` | 2.0 | range 1.0..1000.0 | `guc_tables.c:4038` |
| `recursive_worktable_factor` | `recursive_worktable_factor` | 10.0 | | `cost.h:33`, `guc_tables.c:4004` |
| `max_parallel_workers_per_gather` | | 2 | | `guc_tables.c:3626` |
| `min_parallel_table_scan_size` | | 1024 pages (8 MB) | `(8*1024*1024)/BLCKSZ` | `guc_tables.c:3726` |
| `min_parallel_index_scan_size` | | 64 pages (512 kB) | `(512*1024)/BLCKSZ` | `guc_tables.c:3737` |
| `parallel_leader_participation` | | true | consumed by `get_parallel_divisor` | `guc_tables.c:2011` |
| `jit_above_cost` / `jit_optimize_above_cost` / `jit_inline_above_cost` | | 100000 / 500000 / 500000 | thresholds on final plan `total_cost`; name only | `guc_tables.c:3959..3981` |
| `maintenance_work_mem` | | — | not consulted by costsize.c | |

`get_hash_memory_limit()` (`src/backend/executor/nodeHash.c:3622`):
`mem_limit = (double) work_mem * hash_mem_multiplier * 1024.0`, clamped to
`SIZE_MAX`, returned as `size_t` bytes. Default = 4096 × 2 × 1024 = 8,388,608 B.

Tablespace overrides: every scan cost obtains `spc_seq_page_cost` /
`spc_random_page_cost` from `get_tablespace_page_costs(reltablespace, …)`
(`src/backend/utils/cache/spccache.c:182`), which returns the tablespace's
`random_page_cost`/`seq_page_cost` reloption if set (≥ 0) else the GUC.

### 1.2 `enable_*` GUCs and `disabled_nodes` (PG 18 semantics)

All `enable_*` default to `true` except `enable_partitionwise_join` and
`enable_partitionwise_aggregate` (false) (`costsize.c:145-165`,
`guc_tables.c:801-1041`). PG 18 additionally has `enable_self_join_elimination`,
`enable_group_by_reordering`, `enable_distinct_reordering` (all true) — these
gate rewrites, not costs.

Since **PG 18** (commit `e2225346794`, 2024-08-21; PG 17 still used
`disable_cost` throughout) a disabled node type is no longer priced by adding a
huge constant; instead every `Path` carries an `int disabled_nodes`, the count
of disabled plan nodes in the subtree. Rules:

- A cost function sets `path->disabled_nodes = (enable_X ? 0 : 1)` for the node
  it prices and **adds the children's `disabled_nodes`** where it has children:
  `cost_seqscan` (`enable_seqscan`, `costsize.c:352`), `cost_index`
  (`enable_indexscan`, `:614`), `cost_bitmap_heap_scan` (`enable_bitmapscan`,
  `:1116`), `cost_sort` (`input + (enable_sort?0:1)`, `:2166`), `cost_material`
  (`enable_material`, `:2530`), `cost_gather_merge` (`enable_gathermerge`,
  `:548`), `cost_agg` (`++disabled_nodes` for `AGG_HASHED`, and for `AGG_MIXED`,
  when `!enable_hashagg`, `:2734,2748`), `initial_cost_nestloop`
  (`enable_nestloop + inner + outer`, `:3282`), `initial_cost_mergejoin`
  (`enable_mergejoin` + each input's or its sort's, `:3686`),
  `initial_cost_hashjoin` (`enable_hashjoin + inner + outer`, `:4181`).
- Some switches are hard gates rather than counters: `enable_indexonlyscan`
  (`indxpath.c:2238` — no IOS path built), `enable_tidscan` (`tidpath.c:514,536`,
  except `CURRENT OF`), `enable_memoize` (`joinpath.c:687`),
  `enable_incremental_sort` (never generated; `cost_incremental_sort` asserts it,
  `costsize.c:2129`), `enable_hashagg` for DISTINCT (`planner.c:4988`) and for
  hashed SetOp (`pathnode.c:3875` increments `disabled_nodes`).
- Pass-through nodes copy the child's count (`cost_gather`, `cost_subqueryscan`,
  `cost_append` sums children, `cost_merge_append`, `cost_windowagg`,
  `cost_group`, `cost_recursive_union` = nrterm + rterm).
- `add_path` compares `disabled_nodes` **before** any cost:
  `compare_path_costs_fuzzily` (`pathnode.c:185-247`) returns `COSTS_BETTER1/2`
  immediately if the counts differ; `compare_path_costs` (`:72`) and
  `add_partial_path` (`:829`) do the same; the pathlist is kept sorted by
  `(disabled_nodes, total_cost)` (`:436, :644`); `add_path_precheck` stops
  scanning at the first old path with more disabled nodes (`:716`).
- `disable_cost = 1.0e10` (`costsize.c:141`) still exists but is used in exactly
  one place: `final_cost_hashjoin` adds it to `startup_cost` when the inner MCV
  bucket would not fit in hash memory (`:4419-4421`). Everything else that added
  `disable_cost` in PG ≤ 16 now increments `disabled_nodes`.
- EXPLAIN prints `Disabled: true` for a node whose `disabled_nodes` exceeds the
  sum of its children's (`explain.c:1246 plan_is_disabled`, `:1880-1882`).

### 1.3 `add_path` cost comparison (what the numbers must reproduce)

`compare_path_costs_fuzzily(p1, p2, fuzz)` with `STD_FUZZ_FACTOR = 1.01`
(`pathnode.c:50`):

```
if p1.disabled_nodes != p2.disabled_nodes: fewer wins
if p1.total > p2.total * fuzz:
    if CONSIDER_STARTUP(p1) and p2.startup > p1.startup * fuzz: DIFFERENT
    else BETTER2
if p2.total > p1.total * fuzz: symmetric -> DIFFERENT or BETTER1
# totals fuzzily equal:
if p1.startup > p2.startup * fuzz: BETTER2
if p2.startup > p1.startup * fuzz: BETTER1
EQUAL
```

`CONSIDER_STARTUP(p)` = `parent->consider_startup` for unparameterized paths,
`parent->consider_param_startup` otherwise. On `COSTS_EQUAL` with equal
pathkeys and equal `required_outer`, the tie-break is `parallel_safe`, then
`rows` (fewer wins), then a second fuzzy compare with factor `1.0000000001`,
else the old path is kept (`pathnode.c:540-575`).

---

## 2. Helpers

### 2.1 Clamps (`costsize.c:213-283`)

```
clamp_row_est(n):   if n > 1e100 or isnan(n): 1e100
                    elif n <= 1.0: 1.0
                    else: rint(n)                       # MAXIMUM_ROWCOUNT 1e100, costsize.c:120
clamp_width_est(w): min(w, MaxAllocSize) as int32       # never negative
clamp_cardinality_to_long(x): NaN->LONG_MAX; <=0 -> 0; else (long)x if < LONG_MAX
```

### 2.2 `get_parallel_divisor(path)` (`costsize.c:6474`)

```
divisor = path->parallel_workers
if parallel_leader_participation:
    leader = 1.0 - 0.3 * parallel_workers
    if leader > 0: divisor += leader
```
Workers 1→1.7, 2→2.4, 3→3.1, ≥4→workers. `compute_gather_rows(path)` =
`clamp_row_est(path->rows * get_parallel_divisor(path))` (`:6625`).

### 2.3 `cost_qual_eval` / `cost_qual_eval_node` / `cost_qual_eval_walker` (`costsize.c:4756-5066`)

Output `QualCost {startup, per_tuple}`. Top-level implicit AND costs nothing.
Per node:

| Node | Charge | Citation |
|---|---|---|
| `RestrictInfo` | cached in `rinfo->eval_cost` (startup < 0 = not computed); walks `orclause` if present else `clause`; if `pseudoconstant`, per_tuple is folded into startup and per_tuple := 0; **does not recurse further** | `:4811-4848` |
| `Var`, `Const`, AND/OR/NOT | 0 | comment `:4851` |
| `FuncExpr` | `add_function_cost(funcid)` | `:4875` |
| `OpExpr`, `DistinctExpr`, `NullIfExpr` | `set_opfuncid`; `add_function_cost(opfuncid)` | `:4879-4885` |
| `ScalarArrayOpExpr` (non-hashed) | startup += op.startup; per_tuple += op.per_tuple × `estimate_array_length` × 0.5 | `:4920-4929` |
| `ScalarArrayOpExpr` (hashed, `hashfuncid` valid) | startup += op.startup + hash.startup + arraylen × hash.per_tuple; per_tuple += hash.per_tuple + op.per_tuple | `:4897-4918` |
| `Aggref`, `WindowFunc` | 0, no recursion (charged in Agg/WindowAgg node) | `:4931-4944` |
| `GroupingFunc` | per_tuple += `cpu_operator_cost`, no recursion | `:4945-4950` |
| `CoerceViaIO` | `add_function_cost(result type's input fn)` + `add_function_cost(arg type's output fn)` | `:4951-4967` |
| `ArrayCoerceExpr` | cost of `elemexpr`: startup += its startup; per_tuple += its per_tuple × `estimate_array_length(arg)` if > 0 | `:4968-4978` |
| `RowCompareExpr` | `add_function_cost(get_opcode(op))` for **every** opno | `:4979-4991` |
| `MinMaxExpr`, `SQLValueFunction`, `XmlExpr`, `CoerceToDomain`, `NextValueExpr`, `JsonExpr` | per_tuple += `cpu_operator_cost` | `:4992-5001` |
| `SubLink` | `elog(ERROR)` (must be planned) | `:5002` |
| `SubPlan` | startup += `subplan->startup_cost`; per_tuple += `subplan->per_call_cost`; no recursion into testexpr | `:5007-5025` |
| `AlternativeSubPlan` | cost of the **first** alternative only | `:5026-5036` |
| `PlaceHolderVar` | 0, no recursion (charged in `set_rel_width`) | `:5037-5050` |
| anything else | recurse with `expression_tree_walker` | `:5053` |

`add_function_cost(root, funcid, node, cost)` (`src/backend/optimizer/util/plancat.c:2125`):
if `pg_proc.prosupport` is set and the support function answers
`SupportRequestCost`, add its `startup`/`per_tuple`; otherwise
`per_tuple += procost * cpu_operator_cost`. (`get_func_cost` is not used by the
walker; `procost` is read directly from the syscache.)

`estimate_array_length(root, arrayexpr)` (`selfuncs.c:2147`): `Const` array →
element count (NULL → 0); non-multidim `ArrayExpr` → `list_length(elements)`;
else DECHIST stats' last stanumber (avg distinct element count) if available;
else **10**.

### 2.4 `get_restriction_qual_cost(root, baserel, param_info, &qpqual_cost)` (`costsize.c:5072`)

```
if param_info: cost = cost_qual_eval(param_info->ppi_clauses) + baserel->baserestrictcost
else:          cost = baserel->baserestrictcost
```
`baserestrictcost` is set by `set_baserel_size_estimates` (`:5349`).

### 2.5 Byte-size helpers (`costsize.c:6453-6470`)

```
relation_byte_size(tuples, width) = tuples * (MAXALIGN(width) + MAXALIGN(SizeofHeapTupleHeader))   # header = 24
page_size(tuples, width)          = ceil(relation_byte_size(tuples, width) / BLCKSZ)
```

### 2.6 `index_pages_fetched(tuples_fetched, pages, index_pages, root)` (`costsize.c:908`)

Mackert–Lohman with `T` = table pages, `b` = pro-rated cache:

```
T = pages > 1 ? pages : 1.0
total_pages = max(root->total_table_pages + index_pages, 1.0)
b = effective_cache_size * T / total_pages
b = (b <= 1.0) ? 1.0 : ceil(b)
if T <= b:
    pf = 2*T*tuples_fetched / (2*T + tuples_fetched)
    pf = (pf >= T) ? T : ceil(pf)
else:
    lim = 2*T*b / (2*T - b)
    if tuples_fetched <= lim: pf = 2*T*tuples_fetched / (2*T + tuples_fetched)
    else:                     pf = b + (tuples_fetched - lim) * (T - b) / T
    pf = ceil(pf)
return pf
```
`root->total_table_pages` is the sum of `pages` of all base rels in the query
(set in `make_one_rel`); `index_pages` is the pages of the index under
consideration (or the summed bitmap-tree index pages, `get_indexpath_pages`, `:973`).
Caller must pass `tuples_fetched > 0`, already rounded (`clamp_row_est`).

### 2.7 `approx_tuple_count(root, joinpath, quals)` (`costsize.c:5304`)

`selec = Π clause_selectivity(qual, 0, JOIN_INNER, dummy_inner_sjinfo)` (each
cached); `return clamp_row_est(selec * outer_rows * inner_rows)`. Used for the
"tuples passing the merge/hash clauses" count.

### 2.8 Width and target cost

`set_rel_width(root, rel)` (`costsize.c:6210`): sums per-expression widths of
`reltarget->exprs`; for a `Var` of this rel uses cached `attr_widths[]`, else
`get_attavgwidth(reloid, attno)` (pg_statistic `stawidth`), else
`get_typavgwidth(type, typmod)`; `PlaceHolderVar` adds `ph_width` and its
expression's eval cost to `reltarget->cost`; other expressions use
`get_typavgwidth` and add eval cost. A whole-row Var adds
`MAXALIGN(SizeofHeapTupleHeader) + get_relation_data_width(...)`. Result
`reltarget->width = clamp_width_est(sum)`.

`set_pathtarget_cost_width(root, target)` (`:6367`): width = Σ
`get_expr_width` (cached attr width for Vars, else type avg width); cost = Σ
`cost_qual_eval_node` over non-Var expressions. Join rel width is computed by
`build_joinrel_tlist` summing the same cached `attr_widths` (the
`pathtarget->width` printed as EXPLAIN `width`).

Row/page/visibility inputs (`plancat.c:1075 estimate_rel_size` →
`tableam.c table_block_relation_estimate_size`): `pages = RelationGetNumberOfBlocks`
(forced to 10 if < 10 and never vacuumed and no children); `tuples = rint(density
× curpages)` with `density = reltuples/relpages` when both known, else
`(usable_bytes_per_page × fillfactor/100) / (data_width + overhead)`;
`allvisfrac = relallvisible / curpages` clamped to [0,1] (0 when
`relallvisible = 0`).

---

## 3. Scan costs

Common preamble for every base-rel scan: `path->rows = param_info ?
param_info->ppi_rows : baserel->rows`; the target-list eval cost is charged as
`startup += pathtarget->cost.startup; run += pathtarget->cost.per_tuple × path->rows`
(per **output** row).

### 3.1 `cost_seqscan(path, root, baserel, param_info)` (`costsize.c:295`)

```
disk_run_cost = spc_seq_page_cost * baserel->pages
qp = get_restriction_qual_cost(...)
startup   = qp.startup + target.startup
cpu_run   = (cpu_tuple_cost + qp.per_tuple) * baserel->tuples + target.per_tuple * rows
if parallel_workers > 0:
    cpu_run /= get_parallel_divisor(path)          # disk cost NOT divided
    rows = clamp_row_est(rows / divisor)
disabled_nodes = enable_seqscan ? 0 : 1
total = startup + cpu_run + disk_run_cost
```

### 3.2 `cost_samplescan` (`:370`)

Same as seqscan with `baserel->pages/tuples` already reduced by the sampling
method; page cost is `spc_random_page_cost` if the TSM has `NextSampleBlock`,
else `spc_seq_page_cost`; TABLESAMPLE argument expressions are not charged;
`disabled_nodes = 0`.

### 3.3 `cost_index(path, root, loop_count, partial_path)` (`:560`)

Inputs: `IndexOptInfo` (`pages`, `tuples`, `tree_height`, `unique`,
`nkeycolumns`, `indpred`, `reltablespace`), `baserel->{pages,tuples,rows,
allvisfrac}`, `path->indexclauses`, `param_info->{ppi_rows, ppi_clauses}`,
`amcostestimate` outputs.

```
rows = param_info ? ppi_rows : baserel->rows
qpquals = extract_nonindex_conditions(index->indrestrictinfo, indexclauses)
          [+ extract_nonindex_conditions(ppi_clauses, indexclauses) if parameterized]
          # drops pseudoconstants and clauses redundant with an indexclause (:850)
disabled_nodes = enable_indexscan ? 0 : 1
amcostestimate(root, path, loop_count,
               &indexStartupCost, &indexTotalCost, &indexSelectivity,
               &indexCorrelation, &index_pages)
path->indextotalcost = indexTotalCost; path->indexselectivity = indexSelectivity
startup = indexStartupCost
run     = indexTotalCost - indexStartupCost
tuples_fetched = clamp_row_est(indexSelectivity * baserel->tuples)

if loop_count > 1:
    pf = index_pages_fetched(tuples_fetched * loop_count, baserel->pages, index->pages, root)
    if indexonly: pf = ceil(pf * (1 - allvisfrac))
    rand_heap_pages = pf
    max_IO_cost = pf * spc_random_page_cost / loop_count
    pf = ceil(indexSelectivity * baserel->pages)
    pf = index_pages_fetched(pf * loop_count, baserel->pages, index->pages, root)
    if indexonly: pf = ceil(pf * (1 - allvisfrac))
    min_IO_cost = pf * spc_random_page_cost / loop_count
else:
    pf = index_pages_fetched(tuples_fetched, baserel->pages, index->pages, root)
    if indexonly: pf = ceil(pf * (1 - allvisfrac))
    rand_heap_pages = pf
    max_IO_cost = pf * spc_random_page_cost
    pf = ceil(indexSelectivity * baserel->pages)
    if indexonly: pf = ceil(pf * (1 - allvisfrac))
    min_IO_cost = pf > 0 ? spc_random_page_cost + (pf - 1) * spc_seq_page_cost : 0
    # (pf == 1 -> exactly random_page_cost)

if partial_path:
    if indexonly: rand_heap_pages = -1
    parallel_workers = compute_parallel_worker(baserel, rand_heap_pages, index_pages,
                                               max_parallel_workers_per_gather)
    if parallel_workers <= 0: return          # path will be rejected
    parallel_aware = true

csquared = indexCorrelation^2
run += max_IO_cost + csquared * (min_IO_cost - max_IO_cost)

qpqual_cost = cost_qual_eval(qpquals)
startup += qpqual_cost.startup + target.startup
cpu_run = (cpu_tuple_cost + qpqual_cost.per_tuple) * tuples_fetched + target.per_tuple * rows
if parallel_workers > 0:
    rows = clamp_row_est(rows / divisor); cpu_run /= divisor
run += cpu_run
total = startup + run
```

`compute_parallel_worker(rel, heap_pages, index_pages, max)`
(`allpaths.c:4274`): 0 if `heap_pages < min_parallel_table_scan_size` or
`index_pages < min_parallel_index_scan_size` (for base rels); else workers =
1 + number of times `threshold*3^k <= pages` for heap and for index, take the
min of the two, cap at `max`; the `parallel_workers` reloption overrides.

### 3.4 `genericcostestimate(root, path, loop_count, costs)` (`selfuncs.c:7051`)

```
indexQuals = get_quals_from_indexclauses(indexclauses)        # RestrictInfos, :6967
selectivityQuals = add_predicate_to_index_quals(index, indexQuals)   # partial index predicate clauses not implied, :7274
num_sa_scans = costs->num_sa_scans; if < 1: product of estimate_array_length() over SAOP quals (>1 only)
indexSelectivity = clauselist_selectivity(root, selectivityQuals, rel->relid, JOIN_INNER, NULL)
numIndexTuples = costs->numIndexTuples
if <= 0: numIndexTuples = rint(indexSelectivity * rel->tuples / num_sa_scans)
numIndexTuples = min(numIndexTuples, index->tuples); max(., 1.0)
numIndexPages = (index->pages > 1 && index->tuples > 1)
                ? ceil(numIndexTuples * index->pages / index->tuples) : 1.0
num_scans = num_sa_scans * loop_count
if num_scans > 1:
    pf = index_pages_fetched(numIndexPages * num_scans, index->pages, index->pages, root)
    indexTotalCost = pf * spc_random_page_cost / loop_count      # pro-rate over outer scans only
else:
    indexTotalCost = numIndexPages * spc_random_page_cost
qual_arg_cost = index_other_operands_eval_cost(indexQuals) + index_other_operands_eval_cost(indexOrderBys)
                # eval cost (startup+per_tuple) of the non-index operand of each clause, :6997
qual_op_cost  = cpu_operator_cost * (len(indexQuals) + len(indexOrderBys))
indexStartupCost = qual_arg_cost
indexTotalCost  += qual_arg_cost + numIndexTuples * num_sa_scans * (cpu_index_tuple_cost + qual_op_cost)
indexCorrelation = 0.0
```

### 3.5 `btcostestimate(root, path, loop_count, …)` (`selfuncs.c:7342`)

Determines `indexBoundQuals` (leading `=` quals, plus quals on the next
column; skip-array bridging for columns lacking `=` per the ndistinct logic at
`:7443-7580`; a `RowCompareExpr` counts only its first column; `IS NULL`
counts as `=`), `num_sa_scans` (product of SAOP array lengths > 1 and of
skip-array ndistinct), then:

```
if index->unique && indexcol == nkeycolumns-1 && eqQualHere && !found_array && !found_is_null_op:
    numIndexTuples = 1.0
else:
    btreeSelectivity = clauselist_selectivity(root, add_predicate_to_index_quals(index, indexBoundQuals),
                                              rel->relid, JOIN_INNER, NULL)
    numIndexTuples = btreeSelectivity * rel->tuples
    num_sa_scans = max(min(num_sa_scans, ceil(index->pages * 0.3333333)), 1)
    numIndexTuples = rint(numIndexTuples / num_sa_scans)
costs.numIndexTuples = numIndexTuples; costs.num_sa_scans = num_sa_scans
genericcostestimate(root, path, loop_count, &costs)
if index->tuples > 1:
    descentCost = ceil(log(index->tuples) / log(2.0)) * cpu_operator_cost
    indexStartupCost += descentCost; indexTotalCost += num_sa_scans * descentCost
descentCost = (index->tree_height + 1) * DEFAULT_PAGE_CPU_MULTIPLIER(50.0) * cpu_operator_cost   # selfuncs.c:145
indexStartupCost += descentCost; indexTotalCost += num_sa_scans * descentCost
indexCorrelation = btcost_correlation(index, first column stats)
```
`btcost_correlation` (`:7305`): pg_statistic `STATISTIC_KIND_CORRELATION` of
the first key column for the opfamily `<` operator; negated if
`reverse_sort[0]`; × 0.75 if `nkeycolumns > 1`; 0 if no stats.
`tree_height` comes from `amgettreeheight` = `_bt_getrootheight`
(`nbtree.c:1751`, `plancat.c:493`).

Other AMs (`hashcostestimate :7805`, `gistcostestimate :7847`,
`spgcostestimate :7902`, `gincostestimate :8257`, `brincostestimate :8647`) all
start from `genericcostestimate`; GiST/SP-GiST add `ceil(log(tuples)) *
cpu_operator_cost` descent with `tree_height = log(pages)/log(100)` and the same
`(tree_height+1)*50*cpu_operator_cost` page charge.

### 3.6 `cost_bitmap_heap_scan(path, root, baserel, param_info, bitmapqual, loop_count)` (`costsize.c:1023`)

```
pages_fetched = compute_bitmap_pages(root, baserel, bitmapqual, loop_count, &indexTotalCost, &tuples_fetched)
startup = indexTotalCost
T = baserel->pages > 1 ? pages : 1.0
cost_per_page = pages_fetched >= 2.0
                ? spc_random_page_cost - (spc_random_page_cost - spc_seq_page_cost) * sqrt(pages_fetched / T)
                : spc_random_page_cost
run = pages_fetched * cost_per_page
qp = get_restriction_qual_cost(...)          # ALL restriction clauses rechecked
startup += qp.startup
cpu_run = (cpu_tuple_cost + qp.per_tuple) * tuples_fetched
if parallel_workers > 0: cpu_run /= divisor; rows = clamp_row_est(rows / divisor)
run += cpu_run
startup += target.startup; run += target.per_tuple * rows
disabled_nodes = enable_bitmapscan ? 0 : 1
```

`compute_bitmap_pages` (`:6514`):
```
cost_bitmap_tree_node(bitmapqual, &indexTotalCost, &indexSelectivity)
tuples_fetched = clamp_row_est(indexSelectivity * baserel->tuples)
T = max(pages, 1)
pages_fetched = 2*T*tuples_fetched / (2*T + tuples_fetched)
heap_pages = min(pages_fetched, baserel->pages)
maxentries = tbm_calculate_entries(work_mem * 1024)
if loop_count > 1:
    pages_fetched = index_pages_fetched(tuples_fetched * loop_count, pages, get_indexpath_pages(bitmapqual), root) / loop_count
pages_fetched = (pages_fetched >= T) ? T : ceil(pages_fetched)
if maxentries < heap_pages:
    lossy_pages = max(0, heap_pages - maxentries / 2)
    exact_pages = heap_pages - lossy_pages
    if lossy_pages > 0:
        tuples_fetched = clamp_row_est(indexSelectivity * (exact_pages/heap_pages) * tuples
                                       + (lossy_pages/heap_pages) * tuples)
```
`tbm_calculate_entries(maxbytes)` (`tidbitmap.c:1545`) =
`clamp(maxbytes / (sizeof(PagetableEntry) + 2*sizeof(Pointer)), 16, INT_MAX-1)`;
`sizeof(PagetableEntry)` = 48 on 64-bit (`blockno`+3 flag bytes padded to 8,
plus `words[5]` since `MaxHeapTuplesPerPage` = 291 → 5 bitmapwords), so the
divisor is 64 and work_mem = 4 MB gives 65,536 entries.

`cost_bitmap_tree_node` (`:1122`): for an `IndexPath`, cost =
`indextotalcost + 0.1 * cpu_operator_cost * path->rows`, selec =
`indexselectivity`; for And/Or paths, `total_cost` and `bitmapselectivity`.

`cost_bitmap_and_node` (`:1165`): `selec = Π sub_selec`; cost = Σ sub_cost +
`100 * cpu_operator_cost` per sub-node after the first; `rows = 0`;
startup = total. `cost_bitmap_or_node` (`:1210`): `selec = min(Σ sub_selec,
1.0)`; cost = Σ sub_cost + `100 * cpu_operator_cost` for each non-first child
that is **not** an `IndexPath`.

### 3.7 `cost_tidscan(path, root, baserel, tidquals, param_info)` (`:1258`)

`ntuples` = Σ over tidquals of (`estimate_array_length` for SAOP, else 1);
`run = spc_random_page_cost * ntuples`; `startup = qp.startup +
tid_qual_cost.per_tuple`; `cpu_per_tuple = cpu_tuple_cost + qp.per_tuple -
tid_qual_cost.per_tuple`; `run += cpu_per_tuple * ntuples`; target costs;
`disabled_nodes = 0`.

### 3.8 `cost_tidrangescan` (`:1363`)

`selectivity = clauselist_selectivity(tidrangequals, relid, JOIN_INNER)`;
`pages = max(ceil(selectivity * pages), 1)`; `ntuples = selectivity * tuples`;
`run = spc_random_page_cost + spc_seq_page_cost * (pages - 1)`; CPU as tidscan;
`disabled_nodes = 0`.

### 3.9 `cost_subqueryscan(path, root, baserel, param_info, trivial_pathtarget)` (`:1457`)

`rows = clamp_row_est(subpath->rows × clauselist_selectivity(qpquals, 0,
JOIN_INNER))` where qpquals = `ppi_clauses ++ baserestrictinfo`; cost starts
as the subpath's (`disabled_nodes` copied); if `qpquals == NIL && trivial_pathtarget`
return unchanged (node will be elided); else `startup += qp.startup +
target.startup`, `total += startup_delta + (cpu_tuple_cost + qp.per_tuple) ×
subpath->rows + target.per_tuple × rows`.

### 3.10 `cost_functionscan` (`:1538`), `cost_tablefuncscan` (`:1600`)

`exprcost = cost_qual_eval_node(rte->functions | rte->tablefunc)`;
`startup = exprcost.startup + exprcost.per_tuple + qp.startup`;
`run = (cpu_tuple_cost + qp.per_tuple) × baserel->tuples`; target costs;
`disabled_nodes = 0`.

### 3.11 `cost_valuesscan` (`:1657`), `cost_ctescan` (`:1708`), `cost_namedtuplestorescan` (`:1750`), `cost_resultscan` (`:1788`)

All: `run = cpu_per_tuple × baserel->tuples` with
`cpu_per_tuple = X + cpu_tuple_cost + qp.per_tuple` where X =
`cpu_operator_cost` (VALUES), `cpu_tuple_cost` (CTE, named tuplestore), 0
(Result). Values/CTE add target costs; NamedTuplestore/Result do not.
`disabled_nodes = 0`.

### 3.12 `cost_recursive_union(runion, nrterm, rterm)` (`:1826`)

`startup = nrterm.startup`; `total = nrterm.total + 10 × rterm.total`;
`rows = nrterm.rows + 10 × rterm.rows`; `total += cpu_tuple_cost × rows`;
`disabled_nodes = nrterm + rterm`; width = max of both.

### 3.13 `cost_gather` (`:446`), `cost_gather_merge` (`:485`)

```
Gather:      startup = sub.startup + parallel_setup_cost
             total   = startup + (sub.total - sub.startup) + parallel_tuple_cost * rows
GatherMerge: N = num_workers + 1; logN = LOG2(N); cmp = 2 * cpu_operator_cost
             startup = cmp * N * logN + parallel_setup_cost + input_startup
             run     = rows * cmp * logN + cpu_operator_cost * rows + parallel_tuple_cost * rows * 1.05
             total   = startup + run + input_total   (input_* are the sub-path costs passed in)
             disabled_nodes = input_disabled_nodes + (enable_gathermerge ? 0 : 1)
```
`rows` = explicit `*rows` argument if given, else `ppi_rows`/`rel->rows`.

---

## 4. Sort and materialization

### 4.1 `cost_tuplesort(&startup, &run, tuples, width, comparison_cost, sort_mem, limit_tuples)` (`costsize.c:1898`)

```
input_bytes = relation_byte_size(tuples, width)
sort_mem_bytes = sort_mem * 1024
tuples = max(tuples, 2.0)
comparison_cost += 2.0 * cpu_operator_cost
if limit_tuples > 0 && limit_tuples < tuples:
    output_tuples = limit_tuples; output_bytes = relation_byte_size(output_tuples, width)
else: output_tuples = tuples; output_bytes = input_bytes
if output_bytes > sort_mem_bytes:                         # external sort
    npages = ceil(input_bytes / BLCKSZ)
    nruns  = input_bytes / sort_mem_bytes
    mergeorder = tuplesort_merge_order(sort_mem_bytes)
    startup = comparison_cost * tuples * LOG2(tuples)
    log_runs = nruns > mergeorder ? ceil(log(nruns) / log(mergeorder)) : 1.0
    npageaccesses = 2.0 * npages * log_runs
    startup += npageaccesses * (seq_page_cost * 0.75 + random_page_cost * 0.25)
elif tuples > 2 * output_tuples || input_bytes > sort_mem_bytes:   # bounded heap sort
    startup = comparison_cost * tuples * LOG2(2.0 * output_tuples)
else:                                                       # quicksort
    startup = comparison_cost * tuples * LOG2(tuples)
run = cpu_operator_cost * tuples
```
`tuplesort_merge_order(allowedMem)` (`tuplesort.c:1778`) =
`clamp(allowedMem / (2*TAPE_BUFFER_OVERHEAD + MERGE_BUFFER_SIZE), MINORDER=6, MAXORDER=500)`
with `TAPE_BUFFER_OVERHEAD = BLCKSZ`, `MERGE_BUFFER_SIZE = 32*BLCKSZ`
(divisor 278,528). Note the disk term uses the global `seq_page_cost` /
`random_page_cost`, not tablespace values.

### 4.2 `cost_sort(path, root, pathkeys, input_disabled_nodes, input_cost, tuples, width, comparison_cost, sort_mem, limit_tuples)` (`:2144`)

`cost_tuplesort(...)`; `startup += input_cost` (the input's **total** cost);
`rows = tuples`; `disabled_nodes = input_disabled_nodes + (enable_sort?0:1)`;
`total = startup + run`. `pathkeys` is unused.

### 4.3 `cost_incremental_sort(path, root, pathkeys, presorted_keys, input_disabled_nodes, input_startup, input_total, input_tuples, width, comparison_cost, sort_mem, limit_tuples)` (`:2000`)

```
input_tuples = max(input_tuples, 2.0); input_run = input_total - input_startup
input_groups = min(input_tuples, DEFAULT_NUM_DISTINCT=200)
presortedExprs = first presorted_keys pathkey EC-member exprs (stop, keep default, if any Var has varno 0)
if no varno-0: input_groups = estimate_num_groups(root, presortedExprs, input_tuples, NULL, NULL)
group_tuples = input_tuples / input_groups; group_input_run = input_run / input_groups
cost_tuplesort(&gs, &gr, group_tuples, width, comparison_cost, sort_mem, limit_tuples)
startup = gs + input_startup + group_input_run
run = gr + (gr + gs) * (input_groups - 1) + group_input_run * (input_groups - 1)
run += (cpu_tuple_cost + comparison_cost) * input_tuples      # comparison_cost here is the caller's extra, NOT +2*cpu_operator_cost
run += 2.0 * cpu_tuple_cost * input_groups
rows = input_tuples; disabled_nodes = input_disabled_nodes
```

### 4.4 `cost_append(apath)` (`:2250`)

- Empty subpaths: all zero.
- Non-parallel-aware, no pathkeys: `startup = first subpath startup`;
  `rows/disabled_nodes/total = Σ children`.
- Non-parallel-aware, ordered: children not already sorted are costed through
  `cost_sort(…, work_mem, apath->limit_tuples)`; `startup = Σ startups`
  (not max); rows/disabled/total summed.
- Parallel-aware: `divisor = get_parallel_divisor(apath)`; startup = min of
  startup over the first `parallel_workers` children; for i <
  `first_partial_path` (non-partial children) `rows += sub.rows / divisor`
  (cost added later), for partial children `rows += sub.rows ×
  (get_parallel_divisor(sub) / divisor)` and `total += sub.total`; rows are
  `clamp_row_est`ed per iteration; then `total += append_nonpartial_cost(subpaths,
  first_partial_path, parallel_workers)` (`:2174`: greedy assignment of the
  non-partial children, sorted by decreasing cost, to `min(workers, n)` slots,
  returning the max slot load).
- Always: `total += cpu_tuple_cost × APPEND_CPU_COST_MULTIPLIER(0.5) × rows`
  (`:119`).

### 4.5 `cost_merge_append(path, root, pathkeys, n_streams, input_disabled_nodes, input_startup, input_total, tuples)` (`:2432`)

`N = max(n_streams, 2)`; `cmp = 2 × cpu_operator_cost`;
`startup = cmp × N × LOG2(N) + input_startup`; `run = tuples × cmp × LOG2(N) +
cpu_tuple_cost × 0.5 × tuples`; `total = startup + run + input_total`.

### 4.6 `cost_material(path, input_disabled_nodes, input_startup, input_total, tuples, width)` (`:2483`)

`startup = input_startup`; `run = input_total - input_startup + 2 ×
cpu_operator_cost × tuples`; if `relation_byte_size(tuples, width) > work_mem×1024`:
`run += seq_page_cost × ceil(nbytes / BLCKSZ)`; `rows = tuples`;
`disabled_nodes = input + (enable_material?0:1)`.

### 4.7 `cost_rescan(root, path, &rescan_startup, &rescan_total)` (`:4641`)

| Path type | rescan_startup | rescan_total |
|---|---|---|
| `FunctionScan` | 0 | `total - startup` |
| `HashJoin`, `num_batches == 1` | 0 | `total - startup` |
| `HashJoin`, batches > 1 | startup | total |
| `CteScan`, `WorkTableScan` | 0 | `cpu_tuple_cost × rows` (+ `seq_page_cost × ceil(nbytes/BLCKSZ)` if `nbytes > work_mem`) |
| `Material`, `Sort` | 0 | `cpu_operator_cost × rows` (+ same spill term) |
| `Memoize` | `cost_memoize_rescan` | `cost_memoize_rescan` |
| default | startup | total |

### 4.8 `cost_memoize_rescan(root, mpath, …)` (`:2541`)

```
hash_mem_bytes = get_hash_memory_limit()
est_entry_bytes = relation_byte_size(tuples, width) + ExecEstimateCacheEntryOverheadBytes(tuples)
                  + Σ get_expr_width(param_exprs)
                  # overhead = sizeof(MemoizeEntry)=24 + sizeof(MemoizeKey)=24 + 16*ntuples  (nodeMemoize.c:1172)
est_cache_entries = floor(hash_mem_bytes / est_entry_bytes)
ndistinct = estimate_num_groups(root, param_exprs, calls, NULL, &estinfo)
if estinfo.flags & SELFLAG_USED_DEFAULT: ndistinct = calls
mpath->est_entries = min(min(ndistinct, est_cache_entries), UINT32_MAX)
evict_ratio = 1.0 - min(est_cache_entries, ndistinct) / ndistinct
hit_ratio = ((calls - ndistinct) / calls) * (est_cache_entries / max(ndistinct, est_cache_entries))
total   = input_total * (1 - hit_ratio) + cpu_operator_cost
total  += cpu_tuple_cost * evict_ratio + cpu_operator_cost / 10 * evict_ratio * tuples
total  += cpu_tuple_cost + cpu_operator_cost * tuples
startup = input_startup * (1 - hit_ratio) + cpu_tuple_cost
```
`calls` is the outer path row count set by `create_memoize_path`.

### 4.9 `cost_subplan(root, subplan, plan)` (`:4534`)

```
sp = cost_qual_eval(make_ands_implicit(testexpr), root=NULL)
if useHashTable:
    sp.startup += plan->total_cost + cpu_operator_cost * plan->plan_rows
else:
    run = plan->total_cost - plan->startup_cost
    EXISTS:     sp.per_tuple += run / clamp_row_est(plan_rows)
    ALL/ANY:    sp.per_tuple += 0.5 * run + 0.5 * plan_rows * cpu_operator_cost
    other:      sp.per_tuple += run
    if parParam == NIL && ExecMaterializesOutput(top node): sp.startup += plan->startup_cost
    else:                                                    sp.per_tuple += plan->startup_cost
subplan->startup_cost = sp.startup; subplan->per_call_cost = sp.per_tuple
```

### 4.10 `cost_agg(path, root, aggstrategy, aggcosts, numGroupCols, numGroups, quals, disabled_nodes, input_startup, input_total, input_tuples, input_width)` (`:2682`)

```
AGG_PLAIN:  startup = input_total + trans.startup + trans.per_tuple*input_tuples + final.startup + final.per_tuple
            total = startup + cpu_tuple_cost; output = 1
AGG_SORTED / AGG_MIXED:
            startup = input_startup; total = input_total
            if MIXED && !enable_hashagg: ++disabled_nodes
            total += trans.startup + trans.per_tuple*input_tuples
                   + cpu_operator_cost*numGroupCols*input_tuples
                   + final.startup + final.per_tuple*numGroups + cpu_tuple_cost*numGroups
            output = numGroups
AGG_HASHED: startup = input_total; if !enable_hashagg: ++disabled_nodes
            startup += trans.startup + trans.per_tuple*input_tuples
                     + cpu_operator_cost*numGroupCols*input_tuples + final.startup
            total = startup + final.per_tuple*numGroups + cpu_tuple_cost*numGroups
            output = numGroups
if HASHED or MIXED:                                   # spill model
    hashentrysize = hash_agg_entry_size(len(root->aggtransinfos), input_width, transitionSpace)
    hash_agg_set_limits(hashentrysize, numGroups, 0, &mem_limit, &ngroups_limit, &num_partitions)
    nbatches = max(ceil(max(numGroups*hashentrysize/mem_limit, numGroups/ngroups_limit)), 1)
    num_partitions = max(num_partitions, 2)
    depth = ceil(log(nbatches) / log(num_partitions))
    pages = relation_byte_size(input_tuples, input_width) / BLCKSZ
    pages_written = pages_read = pages * depth * 2.0
    startup += pages_written * random_page_cost
    total   += pages_written * random_page_cost + pages_read * seq_page_cost
    spill_cost = depth * input_tuples * 2.0 * cpu_tuple_cost
    startup += spill_cost; total += spill_cost
if quals (HAVING): qc = cost_qual_eval(quals); startup += qc.startup; total += qc.startup + output*qc.per_tuple
    output = clamp_row_est(output * clauselist_selectivity(quals, 0, JOIN_INNER))
rows = output
```
Same ordering of intermediate sums is intentional so SORTED and HASHED produce
bit-identical totals (`:2711-2719`). Helpers: `hash_agg_entry_size`
(`nodeAgg.c:1701`) = `sizeof(TupleHashEntryData)=16 + MAXALIGN(16 + width) +
numTrans × sizeof(AggStatePerGroupData)=16 + (transitionSpace > 0 ?
CHUNKHDRSZ + nextpower2(transitionSpace) : 0)`; `hash_agg_set_limits`
(`:1809`): if `groups × entrysize <= hash_mem_limit` → `num_partitions = 0`,
`mem_limit = hash_mem_limit`, `ngroups_limit = hash_mem_limit / entrysize`;
else `npartitions = clamp(1 + 1.5×groups×entrysize/hash_mem_limit, 4, 1024)`
(also ≤ `(hash_mem_limit×0.25 − BLCKSZ)/BLCKSZ`), `partition_mem = BLCKSZ ×
(1 + npartitions)`, `mem_limit = hash_mem_limit − partition_mem` if
`hash_mem_limit > 4 × partition_mem` else `hash_mem_limit × 0.75`,
`ngroups_limit = max(mem_limit / entrysize, 1)`. When a nonzero `nbatches`
comes out at depth 0 (`nbatches == 1` → `log(1) = 0` → depth 0) the spill
terms vanish.

### 4.11 `cost_windowagg(path, root, windowFuncs, winclause, input_disabled_nodes, input_startup, input_total, input_tuples)` (`:3098`)

For each `WindowFunc`: `add_function_cost(winfnoid)` (startup → startup_cost,
per_tuple → wfunccost) + `cost_qual_eval_node(args)` + `cost_qual_eval_node(aggfilter)`;
`total += wfunccost × input_tuples`. Then `total += cpu_operator_cost ×
(numPartCols + numOrderCols) × input_tuples + cpu_tuple_cost × input_tuples`;
`rows = input_tuples`. Startup adjustment: `startup_tuples =
get_windowclause_startup_tuples(...)` (`:2884`; uses `estimate_num_groups` on
partition and order exprs, frame options, `DEFAULT_INEQ_SEL` for non-const
offsets); if `> 1`: `startup += (total - startup) / input_tuples ×
(startup_tuples - 1)`.

### 4.12 `cost_group(path, root, numGroupCols, numGroups, quals, input_disabled_nodes, input_startup, input_total, input_tuples)` (`:3195`)

`total = input_total + cpu_operator_cost × input_tuples × numGroupCols`;
HAVING handled exactly as in `cost_agg`; `rows = numGroups` (after HAVING).

---

## 5. Join costs (two-stage)

`JoinCostWorkspace` public fields: `disabled_nodes`, `startup_cost`,
`total_cost`; private: `run_cost`, `inner_run_cost`, `inner_rescan_run_cost`,
`outer_rows`, `inner_rows`, `outer_skip_rows`, `inner_skip_rows`,
`numbuckets`, `numbatches`, `inner_rows_total`. `initial_*` produce lower
bounds checked by `add_path_precheck`; `final_*` add CPU costs. In all
`final_*`: `rows = param_info ? ppi_rows : parent->rows`, then for partial
paths `rows = clamp_row_est(rows / get_parallel_divisor(path))`; target-list
cost is added per output row.

### 5.1 `compute_semi_anti_join_factors(root, joinrel, outerrel, innerrel, jointype, sjinfo, restrictlist, &semifactors)` (`costsize.c:5114`)

```
joinquals = IS_OUTER_JOIN(jointype) ? non-pushed-down restrictlist entries : restrictlist
jselec = clauselist_selectivity(joinquals, 0, jointype==JOIN_ANTI ? JOIN_ANTI : JOIN_SEMI, sjinfo)
nselec = clauselist_selectivity(joinquals, 0, JOIN_INNER, dummy_sjinfo(outer, inner))
avgmatch = jselec > 0 ? max(1.0, nselec * innerrel->rows / jselec) : 1.0
semifactors.outer_match_frac = jselec; semifactors.match_count = avgmatch
```

### 5.2 `initial_cost_nestloop(root, ws, jointype, outer, inner, extra)` (`:3267`)

```
disabled = (enable_nestloop?0:1) + inner.disabled + outer.disabled
cost_rescan(inner, &rs_start, &rs_total)
startup = outer.startup + inner.startup
run = outer.total - outer.startup + (outer_rows > 1 ? (outer_rows-1) * rs_start : 0)
inner_run = inner.total - inner.startup; inner_rescan_run = rs_total - rs_start
if SEMI/ANTI or extra->inner_unique: save inner_run, inner_rescan_run (deferred)
else: run += inner_run + (outer_rows > 1 ? (outer_rows-1) * inner_rescan_run : 0)
ws.total = startup + run; ws.run_cost = run
```

### 5.3 `final_cost_nestloop(root, path, ws, extra)` (`:3349`)

```
outer_rows = max(outer.rows, 1); inner_rows = max(inner.rows, 1)
if SEMI/ANTI/inner_unique:
    outer_matched = rint(outer_rows * outer_match_frac); outer_unmatched = outer_rows - outer_matched
    inner_scan_frac = 2.0 / (match_count + 1.0)
    ntuples = outer_matched * inner_rows * inner_scan_frac
    if has_indexed_join_quals(path):          # :5211 all ppi join clauses are index clauses, no joinrestrictinfo
        run += inner_run * inner_scan_frac
        if outer_matched > 1: run += (outer_matched-1) * inner_rescan_run * inner_scan_frac
        run += outer_unmatched * inner_rescan_run / inner_rows
    else:
        ntuples += outer_unmatched * inner_rows
        run += inner_run
        if outer_unmatched >= 1: outer_unmatched -= 1 else: outer_matched -= 1
        if outer_matched > 0:   run += outer_matched * inner_rescan_run * inner_scan_frac
        if outer_unmatched > 0: run += outer_unmatched * inner_rescan_run
else:
    ntuples = outer_rows * inner_rows
rq = cost_qual_eval(path->joinrestrictinfo)
startup += rq.startup; run += (cpu_tuple_cost + rq.per_tuple) * ntuples
startup += target.startup; run += target.per_tuple * rows
```

### 5.4 `initial_cost_mergejoin(root, ws, jointype, mergeclauses, outer, inner, outersortkeys, innersortkeys, outer_presorted_keys, extra)` (`:3552`)

```
outer_rows = max(outer.rows,1); inner_rows = max(inner.rows,1)
if mergeclauses && jointype != FULL:
    cache = cached_scansel(root, first mergeclause, first pathkey)   # mergejoinscansel, cached per (opfamily, collation, cmptype, nulls_first) on the RestrictInfo (:4081)
    assign outer/inner start/end sel by which side of the clause is outer
    LEFT/ANTI:        outerstartsel=0, outerendsel=1
    RIGHT/RIGHT_ANTI: innerstartsel=0, innerendsel=1
else: start=0, end=1 on both sides
outer_skip = rint(outer_rows*outerstartsel); inner_skip = rint(inner_rows*innerstartsel)
outer_rows' = clamp_row_est(outer_rows*outerendsel); inner_rows' = clamp_row_est(inner_rows*innerendsel)
re-derive sels from the rounded counts (startsel = skip/rows, endsel = rows'/rows)
disabled = enable_mergejoin ? 0 : 1
outer side: if outersortkeys: sort_path = (enable_incremental_sort && outer_presorted_keys>0)
                ? cost_incremental_sort(..., work_mem, -1) : cost_sort(..., outer.total, outer_rows, width, 0, work_mem, -1)
            src = sort_path else src = outer
            disabled += src.disabled; startup += src.startup + (src.total-src.startup)*outerstartsel
            run += (src.total-src.startup)*(outerendsel-outerstartsel)
inner side: if innersortkeys: src = cost_sort(...)  (never incremental: no mark/restore)
            disabled += src.disabled; startup += src.startup + (src.total-src.startup)*innerstartsel
            inner_run_cost = (src.total-src.startup)*(innerendsel-innerstartsel)
ws.total = startup + run + inner_run_cost;  save run, inner_run_cost, outer_rows', inner_rows', skips
```

### 5.5 `final_cost_mergejoin(root, path, ws, extra)` (`:3837`)

```
mq = cost_qual_eval(mergeclauses); qq = cost_qual_eval(joinrestrictinfo) - mq
skip_mark_restore = (SEMI/ANTI/inner_unique) && len(joinrestrictinfo) == len(mergeclauses)
mergejointuples = approx_tuple_count(root, path, mergeclauses)
rescannedtuples = (outer is UniquePath || skip_mark_restore) ? 0 : max(mergejointuples - inner_path_rows, 0)
rescanratio = 1.0 + rescannedtuples / inner_rows
bare_inner_cost = inner_run_cost * rescanratio
mat_inner_cost  = inner_run_cost + cpu_operator_cost * inner_rows * rescanratio
materialize_inner =
    skip_mark_restore                                            -> false
    enable_material && mat_inner_cost < bare_inner_cost          -> true
    innersortkeys == NIL && !ExecSupportsMarkRestore(inner)      -> true   (required, ignores enable_material)
    enable_material && innersortkeys != NIL
        && relation_byte_size(inner_path_rows, inner.width) > work_mem*1024 -> true
    else false
run += materialize_inner ? mat_inner_cost : bare_inner_cost
startup += mq.startup + mq.per_tuple * (outer_skip + inner_skip * rescanratio)
run += mq.per_tuple * ((outer_rows - outer_skip) + (inner_rows - inner_skip) * rescanratio)
startup += qq.startup; run += (cpu_tuple_cost + qq.per_tuple) * mergejointuples
startup += target.startup; run += target.per_tuple * rows
```

### 5.6 `initial_cost_hashjoin(root, ws, jointype, hashclauses, outer, inner, extra, parallel_hash)` (`:4160`)

```
disabled = (enable_hashjoin?0:1) + inner.disabled + outer.disabled
startup = outer.startup + inner.total
run = outer.total - outer.startup
startup += (cpu_operator_cost * num_hashclauses + cpu_tuple_cost) * inner_rows
run += cpu_operator_cost * num_hashclauses * outer_rows
inner_rows_total = parallel_hash ? inner_rows * get_parallel_divisor(inner) : inner_rows
ExecChooseHashTableSize(inner_rows_total, inner.width, useskew=true, try_combined_hash_mem=parallel_hash,
                        outer.parallel_workers, &space_allowed, &numbuckets, &numbatches, &num_skew_mcvs)
if numbatches > 1:
    outerpages = page_size(outer_rows, outer.width); innerpages = page_size(inner_rows, inner.width)
    startup += seq_page_cost * innerpages
    run += seq_page_cost * (innerpages + 2 * outerpages)
ws.total = startup + run; save run, numbuckets, numbatches, inner_rows_total
```

`ExecChooseHashTableSize` (`nodeHash.c:658`), 64-bit sizes:
```
ntuples = ntuples <= 0 ? 1000 : ntuples
tupsize = HJTUPLE_OVERHEAD(16) + MAXALIGN(SizeofMinimalTupleHeader)(16) + MAXALIGN(tupwidth)
inner_rel_bytes = ntuples * tupsize
hash_table_bytes = get_hash_memory_limit();  if try_combined: *= (parallel_workers + 1)
skew: bytes_per_mcv = tupsize + 8*8 + 4 + SKEW_BUCKET_OVERHEAD(16)
      skew_mcvs = (hash_table_bytes / bytes_per_mcv) * SKEW_HASH_MEM_PERCENT(2) / 100
      hash_table_bytes -= skew_mcvs * bytes_per_mcv
max_pointers = prevpower2(min(hash_table_bytes / 8, MaxAllocSize / 8)); min(., INT_MAX/2+1)
nbuckets = nextpower2_32(max(min(ceil(ntuples / NTUP_PER_BUCKET(1)), max_pointers), 1024))
bucket_bytes = 8 * nbuckets
if inner_rel_bytes + bucket_bytes > hash_table_bytes:
    if try_combined: recurse with try_combined=false and return
    bucket_size = tupsize + 8
    sbuckets = hash_table_bytes <= bucket_size ? 1 : nextpower2(hash_table_bytes / bucket_size)
    nbuckets = nextpower2_32(min(sbuckets, max_pointers)); bucket_bytes = 8 * nbuckets
    dbatch = min(ceil(inner_rel_bytes / (hash_table_bytes - bucket_bytes)), max_pointers)
    nbatch = nextpower2_32(max(2, (int) dbatch))
while nbatch > 1:                          # PG 18 memory-balancing walk-back
    if nbuckets > MaxAllocSize/8/2 or space_allowed > SIZE_MAX/2: break
    if nbatch < space_allowed / BLCKSZ: break
    nbuckets *= 2; num_skew_mcvs *= 2; space_allowed *= 2; nbatch /= 2
```

### 5.7 `final_cost_hashjoin(root, path, ws, extra)` (`:4275`)

```
path->num_batches = numbatches; path->inner_rows_total = inner_rows_total
virtualbuckets = numbuckets * numbatches
if inner is UniquePath: innerbucketsize = 1/virtualbuckets; innermcvfreq = 0
else:
    innerbucketsize = innermcvfreq = 1.0
    otherclauses = estimate_multivariate_bucketsize(root, inner rel, hashclauses, &innerbucketsize)   # extended stats
    for each remaining hashclause: pick the inner-side operand; use cached rinfo->{left,right}_bucketsize/_mcvfreq
        (compute via estimate_hash_bucket_stats(root, inner_operand, virtualbuckets, ...) when < 0)
        innerbucketsize = min(...); innermcvfreq = min(...)
if relation_byte_size(clamp_row_est(inner_rows * innermcvfreq), inner.width) > get_hash_memory_limit():
    startup += disable_cost                         # 1e10
hq = cost_qual_eval(hashclauses); qq = cost_qual_eval(joinrestrictinfo) - hq
if SEMI/ANTI/inner_unique:
    outer_matched = rint(outer_rows * outer_match_frac); inner_scan_frac = 2/(match_count+1)
    startup += hq.startup
    run += hq.per_tuple * outer_matched * clamp_row_est(inner_rows * innerbucketsize * inner_scan_frac) * 0.5
    run += hq.per_tuple * (outer_rows - outer_matched) * clamp_row_est(inner_rows / virtualbuckets) * 0.05
    hashjointuples = ANTI ? outer_rows - outer_matched : outer_matched
else:
    startup += hq.startup
    run += hq.per_tuple * outer_rows * clamp_row_est(inner_rows * innerbucketsize) * 0.5
    hashjointuples = approx_tuple_count(root, path, hashclauses)
startup += qq.startup; run += (cpu_tuple_cost + qq.per_tuple) * hashjointuples
startup += target.startup; run += target.per_tuple * rows
```

`estimate_hash_bucket_stats(root, hashkey, nbuckets, &mcv_freq, &bucketsize_frac)`
(`selfuncs.c:4060`): `mcv_freq` = first MCV frequency (0 if none);
`ndistinct = get_variable_numdistinct`; if default → `bucketsize = max(0.1,
mcv_freq)`; else `avgfreq = (1 - nullfrac)/ndistinct`; `ndistinct *=
rel->rows / rel->tuples` (clamped); `estfract = 1/nbuckets` if `ndistinct >
nbuckets` else `1/ndistinct`; if `mcv_freq > avgfreq`: `estfract *= mcv_freq /
avgfreq`; clamp to [1e-6, 1.0].

---

## 6. Cardinality inside costs

### 6.1 Base relations

`set_baserel_size_estimates(root, rel)` (`costsize.c:5349`):
`rel->rows = clamp_row_est(rel->tuples × clauselist_selectivity(baserestrictinfo,
0, JOIN_INNER, NULL))`; `rel->baserestrictcost = cost_qual_eval(baserestrictinfo)`;
`set_rel_width`.

`get_parameterized_baserel_size(root, rel, param_clauses)` (`:5379`):
`nrows = clamp_row_est(rel->tuples × clauselist_selectivity(param_clauses ++
baserestrictinfo, rel->relid, JOIN_INNER, NULL))`, capped at `rel->rows`. Note
`varRelid = rel->relid` (non-zero) so join clauses are treated as restrictions.

Other RTE kinds set `rel->tuples` then call `set_baserel_size_estimates`:
subquery (`:5903`, `tuples = cheapest_total_path->rows` of the sub-final rel,
attr widths copied for plain Vars), function (`:5983`, max of
`expression_returns_set_rows` over functions), tablefunc (`:6021`, 100),
values (`:6043`, list length), CTE (`:6075`, `cte_rows`, or
`clamp_row_est(recursive_worktable_factor × cte_rows)` for a self-reference),
named tuplestore (`:6113`, `enrtuples` or 1000), result (`:6146`, 1).
`set_foreign_size_estimates` (`:6175`) sets `rows = 1000` and
`baserestrictcost`/width only; the FDW overrides rows.

### 6.2 Join relations

`set_joinrel_size_estimates` (`:5428`) → `calc_joinrel_size_estimate(root,
rel, outer_rel, inner_rel, outer_rel->rows, inner_rel->rows, sjinfo,
restrictlist)`. `get_parameterized_joinrel_size` (`:5460`) does the same with
the input **paths'** rows and clamps to `rel->rows`.

`calc_joinrel_size_estimate` (`:5501`):
```
fkselec = get_foreign_key_join_selectivity(root, outer_relids, inner_relids, sjinfo, &restrictlist)  # removes FK-matched clauses
if IS_OUTER_JOIN(jointype):
    jselec = clauselist_selectivity(joinquals (not RINFO_IS_PUSHED_DOWN), 0, jointype, sjinfo)
    pselec = clauselist_selectivity(pushedquals, 0, jointype, sjinfo)
else: jselec = clauselist_selectivity(restrictlist, 0, jointype, sjinfo); pselec = 0
INNER: nrows = outer * inner * fkselec * jselec
LEFT:  nrows = max(outer * inner * fkselec * jselec, outer) * pselec
FULL:  nrows = max(outer * inner * fkselec * jselec, outer, inner) * pselec
SEMI:  nrows = outer * fkselec * jselec
ANTI:  nrows = outer * (1 - fkselec * jselec) * pselec
return clamp_row_est(nrows)
```
(`JOIN_RIGHT`/`RIGHT_ANTI`/`RIGHT_SEMI` are canonicalised before reaching here;
`elog(ERROR)` otherwise.)

### 6.3 `get_foreign_key_join_selectivity(root, outer_relids, inner_relids, sjinfo, &restrictlist)` (`:5651`)

For each `ForeignKeyOptInfo` in `root->fkey_list`:

1. Relevance: `con_relid ∈ outer && ref_relid ∈ inner` → `ref_is_outer = false`;
   `ref_relid ∈ outer && con_relid ∈ inner` → `ref_is_outer = true`; else skip.
2. SEMI/ANTI: skip if `ref_is_outer` or the inner side is not a single base rel.
3. Remove from the worklist every clause that matches a key column either by
   `rinfo->parent_ec == fkinfo->eclass[i]` (EC-derived) or by membership in
   `fkinfo->rinfos[i]` (loose match). If the number removed ≠
   `nmatched_ec − nconst_ec + nmatched_ri`, put them back and skip the FK.
4. Selectivity: SEMI/ANTI → `fkselec *= ref_rel->rows / max(ref_rel->tuples, 1)`;
   otherwise `fkselec *= 1 / max(ref_rel->tuples, 1)` (raw table size, not
   filtered rows). Multi-column FKs therefore contribute a single 1/N.
5. `nconst_ec > 0`: for each key whose EC `ec_has_const`, find the derived
   `var = const` clause for the FK member (`find_derived_clause_for_ec_member`)
   and divide `fkselec` by its `clause_selectivity(rinfo, 0, jointype, sjinfo)`
   if > 0 (undoes double counting).
6. No null-fraction derating is applied (explicit XXX). Inheritance ignored.

Return `CLAMP_PROBABILITY(fkselec)`; `*restrictlist` becomes the pruned copy.

### 6.4 Rows from `compute_bitmap_pages`

The tuple count fed to CPU costing of a bitmap heap scan is `tuples_fetched`
after lossy adjustment (§3.6), not `path->rows`; `path->rows` stays
`baserel->rows`/`ppi_rows`.

---

## 7. Selectivity entry points the cost model calls (interface only; details in doc 03)

| Function | Signature | Used by | Citation |
|---|---|---|---|
| `clauselist_selectivity(root, clauses, varRelid, jointype, sjinfo)` | → `Selectivity`; wraps `clauselist_selectivity_ext(..., use_extended_stats=true)` | size estimates, `genericcostestimate`, `btcostestimate`, `cost_tidrangescan`, HAVING, subqueryscan | `clausesel.c:100,117` |
| `clause_selectivity(root, clause, varRelid, jointype, sjinfo)` / `_ext` | single clause; caches on `RestrictInfo`: `norm_selec` (JOIN_INNER) and `outer_selec` (other), only when `varRelid == 0` or the clause references ≤ 1 base rel that equals `varRelid`; `pseudoconstant` non-Const → 1.0; OR clauses walk `orclause` | `approx_tuple_count`, FK const correction | `clausesel.c:667,684,725-745,960-966` |
| `restriction_selectivity(root, opno, args, collid, varRelid)` | calls `pg_operator.oprrest` (`OidFunctionCall4Coll`); 0.5 if none; error if out of [0,1] | via `clause_selectivity_ext` for `OpExpr` with ≤1 rel | `plancat.c:1983` |
| `join_selectivity(root, opno, args, collid, jointype, sjinfo)` | calls `oprjoin` (`OidFunctionCall5Coll`); 0.5 if none | join clauses | `plancat.c:2022` |
| `estimate_num_groups(root, groupExprs, input_rows, pgset, estinfo)` | → double; sets `SELFLAG_USED_DEFAULT` in `estinfo->flags` when a default ndistinct was used | `cost_incremental_sort`, `cost_memoize_rescan`, window startup tuples, Agg/Group callers | `selfuncs.c:3449` |
| `estimate_hash_bucket_stats(root, hashkey, nbuckets, &mcv_freq, &bucketsize_frac)` | see §5.7 | `final_cost_hashjoin` | `selfuncs.c:4060` |
| `mergejoinscansel(root, clause, opfamily, cmptype, nulls_first, &lstart, &lend, &rstart, &rend)` | fractions in [0,1] of each input scanned before first match / before termination | `cached_scansel` → `initial_cost_mergejoin` | `selfuncs.c:2963` |
| `estimate_array_length(root, arrayexpr)` | see §2.3 | qual walker, tidscan, SAOP scan counts | `selfuncs.c:2147` |
| `get_variable_numdistinct`, `examine_variable`, `get_attavgwidth`, `get_typavgwidth` | stats accessors | bucket stats, widths | `selfuncs.c`, `lsyscache.c` |

Defaults referenced by the cost paths: `DEFAULT_EQ_SEL 0.005`,
`DEFAULT_INEQ_SEL 0.3333333333333333`, `DEFAULT_RANGE_INEQ_SEL 0.005`,
`DEFAULT_NUM_DISTINCT 200` (`src/include/utils/selfuncs.h:34-52`).

---

## 8. Costing walkthrough examples (PG 18 defaults, hand-computed)

Shared assumptions unless stated: default GUCs (§1.1), `work_mem = 4 MB`,
`effective_cache_size = 524288` pages, a single table in the query so
`root->total_table_pages = its pages`, no tablespace overrides, no target-list
expressions (`pathtarget->cost = 0`), Const comparison operands
(`qual_arg_cost = 0`), `procost = 1` operators.

### 8.1 Equality index scan on a unique key, `loop_count = 1` and `1000`

Table: `tuples = 1,000,000`, `pages = 10,000`. Unique btree on the key:
`index->pages = 2,745`, `index->tuples = 1,000,000`, `tree_height = 2`. Stats:
`n_distinct = -1` so `eqsel` gives selectivity 1e-6; first-column correlation
assumed 0 (the I/O term below is correlation-independent anyway because
`min_IO_cost == max_IO_cost`).

`btcostestimate`: unique + `=` on the only key column → `numIndexTuples = 1`,
`num_sa_scans = 1`. `genericcostestimate`: `indexSelectivity = 1e-6`;
`numIndexPages = ceil(1 × 2745 / 1e6) = 1`; `num_scans = 1` →
`indexTotalCost = 1 × 4.0 = 4.0`; `qual_op_cost = 0.0025`;
`indexTotalCost += 1 × (0.005 + 0.0025) = 4.0075`; `indexStartupCost = 0`.
Descent: `ceil(log2(1e6)) = 20 → 20 × 0.0025 = 0.05`; page charge
`(2+1) × 50 × 0.0025 = 0.375`. → `indexStartupCost = 0.425`,
`indexTotalCost = 4.4325`.

`cost_index`, `loop_count = 1`: `tuples_fetched = clamp(1e-6 × 1e6) = 1`.
`index_pages_fetched(1, 10000, 2745)`: `T = 10000`; `total_pages = 12745`;
`b = ceil(524288 × 10000 / 12745) = 411369 ≥ T`; `pf = 2×10000×1/(20001) =
0.99995 → ceil = 1`. `max_IO = 4.0`. `min_IO`: `pf = ceil(1e-6 × 10000) = 1` →
`4.0`. `run = (4.4325 − 0.425) + 4.0 = 8.0075`; CPU `= (0.01 + 0) × 1 = 0.01`.
**`startup = 0.425`, `total = 8.4425`, `rows = 1`** → EXPLAIN
`(cost=0.42..8.44 rows=1 …)`.

`loop_count = 1000` (inner side of a nestloop with 1000 outer rows):
`genericcostestimate` `num_scans = 1000`: `pf = index_pages_fetched(1000,
2745, 2745)`: `T = 2745`, `b = ceil(524288 × 2745/12745) = 112923`, `pf =
2×2745×1000/(5490+1000) = 845.9 → 846`; `indexTotalCost = 846 × 4 / 1000 =
3.384 + 0.0075 = 3.3915`; plus descent 0.425 → `indexTotalCost = 3.8165`,
`indexStartupCost = 0.425`. `cost_index`: `pf = index_pages_fetched(1 ×
1000, 10000, 2745) = ceil(2×10000×1000/21000) = ceil(952.38) = 953`;
`max_IO = 953 × 4 / 1000 = 3.812`; correlated case: `ceil(1e-6 × 10000) = 1`,
`index_pages_fetched(1000, …) = 953` → `min_IO = 3.812`. `run = 3.3915 +
3.812 + 0.01 = 7.2135`. **`startup = 0.425`, `total = 7.6385`** per
iteration; the nestloop charges `0.425 + 7.2135` for the first and
`cost_rescan` (default: same) for the other 999.

### 8.2 Hash join `lineitem` (6,000,000 rows) ⋈ `orders` (1,500,000 rows), `work_mem` 4 MB vs 512 MB

Widths assumed: orders (inner) 100 B, lineitem (outer) 130 B; one hash clause
`l_orderkey = o_orderkey`; `o_orderkey` unique (`n_distinct = -1`, no MCV).

Common terms (`initial_cost_hashjoin`): `startup += (0.0025 × 1 + 0.01) ×
1.5e6 = 18,750`; `run += 0.0025 × 6e6 = 15,000` (plus the input path costs).

`ExecChooseHashTableSize(1.5e6, 100)`: `tupsize = 16 + 16 + 104 = 136`;
`inner_rel_bytes = 204,000,000`.

- **4 MB**: `hash_table_bytes = 8,388,608`. Skew: `bytes_per_mcv = 136 + 64 +
  4 + 16 = 220`; `skew_mcvs = floor(8388608/220) × 2/100 = 762`;
  `hash_table_bytes = 8,388,608 − 167,640 = 8,220,968`. `max_pointers =
  prevpow2(1,027,621) = 524,288`. `nbuckets = nextpow2(min(1.5e6, 524288)) =
  524,288`; `bucket_bytes = 4,194,304`; `204,000,000 + 4,194,304 > 8,220,968`
  → batching: `bucket_size = 144`; `sbuckets = nextpow2(57,090) = 65,536`;
  `nbuckets = 65,536`, `bucket_bytes = 524,288`; `dbatch = ceil(204,000,000 /
  7,696,680) = 27` → `nbatch = nextpow2(27) = 32`. Walk-back: `32 <
  8,388,608/8192 = 1024` → stop. **`numbuckets = 65,536`, `numbatches = 32`.**
  Batch I/O: `innerpages = ceil(1.5e6 × (104+24) / 8192) = 23,438`;
  `outerpages = ceil(6e6 × (136+24) / 8192) = 117,188`; `startup += 23,438`;
  `run += 23,438 + 2 × 117,188 = 257,814`.
- **512 MB**: limit `1,073,741,824`; skew removes `97,612 × 220 =
  21,474,640` → `1,052,267,184`; `max_pointers = prevpow2(min(131,533,398,
  134,217,727)) = 67,108,864`; `nbuckets = nextpow2(1,500,000) = 2,097,152`;
  `bucket_bytes = 16,777,216`; `204,000,000 + 16,777,216 < 1,052,267,184` →
  **`numbatches = 1`**, no batch I/O.

`final_cost_hashjoin` (identical in both cases): `virtualbuckets = 65536 × 32
= 2,097,152` (4 MB) or `2,097,152 × 1` (512 MB); `estimate_hash_bucket_stats`:
`ndistinct = 1.5e6 < virtualbuckets` → `estfract = 1/1.5e6 = 6.67e-7` → clamp
to `1e-6`. Probe: `0.0025 × 6e6 × clamp_row_est(1.5e6 × 1e-6 = 1.5 → 2) × 0.5
= 15,000`. `hashjointuples = approx_tuple_count = clamp(6e6 × 1.5e6 × (1/1.5e6))
= 6,000,000`; `run += 0.01 × 6e6 = 60,000`.

Net join-node increment over the input paths: 4 MB → `startup 42,188`,
`run 347,814`; 512 MB → `startup 18,750`, `run 90,000`. The `cost_rescan`
treatment also differs: a single-batch hash join rescans at
`total − startup`, a 32-batch one at full cost.

### 8.3 Bitmap heap scan at 1 % selectivity on a 100,000-page table

`pages = 100,000`, `tuples = 10,000,000` (100/page), one btree index
(`pages = 27,000`, `tuples = 1e7`, `tree_height = 3`), one range qual with
selectivity 0.01, correlation irrelevant, `loop_count = 1`,
`baserestrictinfo` = that one qual (`per_tuple = 0.0025`).

Index path (via `btcostestimate`, non-unique): `numIndexTuples = 0.01 × 1e7 =
100,000`; `num_sa_scans = 1`; `numIndexPages = ceil(100000 × 27000 / 1e7) =
270`; `indexTotalCost = 270 × 4 = 1,080 + 100,000 × 0.0075 = 1,830`; descent
`ceil(log2(1e7)) = 24 → 0.06`, page `(3+1) × 50 × 0.0025 = 0.5` →
`indexStartupCost = 0.56`, `indexTotalCost = 1,830.56`.
`cost_bitmap_tree_node`: `+ 0.1 × 0.0025 × rows(100,000) = 25` →
`indexTotalCost = 1,855.56`.

`compute_bitmap_pages`: `tuples_fetched = 100,000`; `T = 100,000`;
`pages_fetched = 2×1e5×1e5 / (2e5 + 1e5) = 66,666.67`; `heap_pages =
66,666.67`; `maxentries = 4,194,304 / 64 = 65,536`; `pages_fetched = ceil =
66,667`. Lossy: `65,536 < 66,666.67` → `lossy_pages = 66,666.67 − 32,768 =
33,898.67`, `exact_pages = 32,768`; `tuples_fetched = clamp(0.01 × (32768 /
66666.67) × 1e7 + (33898.67 / 66666.67) × 1e7) = clamp(49,152 + 5,084,800) =
5,133,952`.

`cost_bitmap_heap_scan`: `startup = 1,855.56`; `cost_per_page = 4 − 3 ×
sqrt(66667/100000) = 4 − 3 × 0.81650 = 1.5505`; `run = 66,667 × 1.5505 =
103,367.2`; CPU `= (0.01 + 0.0025) × 5,133,952 = 64,174.4`.
**`startup = 1,855.56`, `total = 169,397.2`, `rows = 100,000`.** For
comparison the seqscan is `total = 100,000 + 0.0125 × 1e7 = 225,000`, so the
bitmap scan wins despite the lossy blow-up; with `work_mem = 8 MB`
(`maxentries = 131,072 > heap_pages`) `tuples_fetched` stays 100,000 and the
CPU term drops to 1,250.

### 8.4 Sort of 1,000,000 rows (width 100): in memory vs external vs bounded

`input_bytes = 1e6 × (104 + 24) = 128,000,000`; `comparison_cost = 0.005`;
`LOG2(1e6) = 19.93157`; CPU term `= 0.005 × 1e6 × 19.93157 = 99,657.8`;
`run = 0.0025 × 1e6 = 2,500` in every case.

- **work_mem = 256 MB** (268,435,456 ≥ 128,000,000): quicksort branch →
  `startup = 99,657.8`, `total = startup + input_cost + 2,500`.
- **work_mem = 4 MB**: `npages = ceil(128e6/8192) = 15,625`; `nruns =
  128e6/4,194,304 = 30.52`; `mergeorder = clamp(4,194,304/278,528 = 15, 6,
  500) = 15`; `30.52 > 15` → `log_runs = ceil(ln 30.52 / ln 15) =
  ceil(1.2623) = 2`; `npageaccesses = 2 × 15,625 × 2 = 62,500`; disk `=
  62,500 × (0.75 + 1.0) = 109,375`. **`startup = 209,032.8`**, `run = 2,500`.
- **`LIMIT 10`, 4 MB**: `output_tuples = 10`, `output_bytes = 1,280 ≤ 4 MB`,
  `tuples > 20` → bounded heap: `startup = 0.005 × 1e6 × LOG2(20) = 0.005 ×
  1e6 × 4.32193 = 21,609.6`; `run = 2,500` (LIMIT pro-rates it later).

### 8.5 Parallel seqscan with 4 workers

`pages = 100,000`, `tuples = 10,000,000`, no quals, `parallel_workers = 4`
(requires `max_parallel_workers_per_gather ≥ 4`; `compute_parallel_worker`
would allow 5: thresholds 1024·3^k = 3072, 9216, 27648, 82944 ≤ 100,000).
`get_parallel_divisor`: `leader = 1 − 0.3 × 4 = −0.2` → not added → **divisor
= 4.0**. `disk = 100,000`; `cpu_run = 0.01 × 1e7 / 4 = 25,000`; `rows =
clamp(1e7/4) = 2,500,000`; **partial path `total = 125,000`, `rows =
2,500,000`.** `cost_gather`: `rows = compute_gather_rows = 2,500,000 × 4 =
10,000,000`; `startup = 0 + 1000`; `run = 125,000 + 0.1 × 1e7 = 1,125,000`;
Gather `total = 1,126,000` versus serial `200,000` — with no filtering the
`parallel_tuple_cost` term dominates and the serial plan wins. With 2 workers
the divisor is `2 + 0.4 = 2.4`: `cpu = 41,666.7`, `rows = 4,166,667`, partial
`total = 141,666.7`.

---

## 9. EXPLAIN surface (what a plan-parity diff needs)

`ExplainNode` (`src/backend/commands/explain.c`):

- Text format prints `  (cost=%.2f..%.2f rows=%.0f width=%d)` from
  `plan->startup_cost`, `plan->total_cost`, `plan->plan_rows`,
  `plan->plan_width` (`:1799`). Non-text: `Startup Cost` / `Total Cost` (2
  decimals), `Plan Rows` (0 decimals), `Plan Width` (integer) (`:1805-1812`).
- `plan_rows` is the Path's `rows`, i.e. `ppi_rows` for parameterized paths
  and the per-worker row count for partial paths; `Gather`/`Gather Merge`
  print `compute_gather_rows`. `plan_width` = `pathtarget->width`.
- Node name is prefixed with `Parallel ` when `plan->parallel_aware`
  (`:1631`) and `Async ` when `async_capable`; non-text formats emit
  `Parallel Aware` / `Async Capable` booleans (`:1652-1653`).
- `Disabled: true` is printed (text) only when `plan_is_disabled(plan)`;
  non-text formats always emit the boolean (`:1880-1882`).
  `plan_is_disabled` = `plan->disabled_nodes > Σ children's disabled_nodes`
  (`:1246`).
- `Workers Planned: N` follows Gather/GatherMerge; `Sort Method`, `Batches`,
  `Memory Usage` etc. are ANALYZE-only.
- `JIT:` block (with `Functions`, `Options`) appears only when
  `es->costs` and JIT was used (`:636-641`, `:901`); `Settings:` appears with
  `EXPLAIN (SETTINGS)` and lists non-default `GUC_EXPLAIN` GUCs (`:689-743`).
  Every cost GUC in §1.1 is `GUC_EXPLAIN`.
- `HashAggregate` prints `Planned Partitions: N` when `hash_planned_partitions
  > 0` and `es->costs` (`:3749`).

Costs are `double`s formatted with `%.2f`; the parity target is therefore the
rounded 2-decimal strings, but `add_path` decisions depend on the unrounded
values, so the arithmetic order in §3-5 must be preserved.

---

## 10. Reimplementation checklist

Each statement is atomic and testable; citation in brackets.

**Currency / GUCs**
1. Defaults: seq 1.0, random 4.0, cpu_tuple 0.01, cpu_index_tuple 0.005, cpu_operator 0.0025, parallel_tuple 0.1, parallel_setup 1000, effective_cache_size 524288 pages, work_mem 4096 kB, hash_mem_multiplier 2.0 [cost.h:24-34, guc_tables.c:2574,4038].
2. `get_hash_memory_limit() = work_mem × hash_mem_multiplier × 1024` bytes [nodeHash.c:3622].
3. Page costs come from the relation's tablespace reloptions, falling back to the GUCs [spccache.c:182].
4. `LOG2(x) = log(x)/0.693147180559945` [costsize.c:113].
5. `clamp_row_est` returns 1e100 for NaN/>1e100, 1.0 for ≤ 1.0, `rint` otherwise [costsize.c:213].
6. `relation_byte_size = tuples × (MAXALIGN(width) + 24)`; `page_size = ceil(bytes/8192)` [costsize.c:6453-6470].

**disabled_nodes**
7. Each cost function sets `disabled_nodes = (enable_X ? 0 : 1)` plus children's counts; no cost constant is added [costsize.c:352,614,1116,2166,2530,548,2734,2748,3282,3686,4181].
8. `enable_indexonlyscan`, `enable_tidscan` (except CURRENT OF), `enable_memoize`, `enable_incremental_sort` suppress path generation rather than counting [indxpath.c:2238, tidpath.c:514,536, joinpath.c:687].
9. `AGG_MIXED` and `AGG_HASHED` both increment `disabled_nodes` when `!enable_hashagg` [costsize.c:2734,2748].
10. `disable_cost = 1e10` is added only in `final_cost_hashjoin` when the inner MCV bucket exceeds hash memory [costsize.c:141,4421].
11. `compare_path_costs_fuzzily` compares `disabled_nodes` before costs; fuzz 1.01; startup considered only under `consider_startup`/`consider_param_startup` [pathnode.c:185-247].
12. The pathlist is sorted by `(disabled_nodes, total_cost)` and `add_path_precheck` stops at the first entry that is worse on either [pathnode.c:436,644,716].
13. On COSTS_EQUAL with equal pathkeys/outer rels, tie-break is parallel_safe, then rows, then fuzz 1.0000000001, else keep old [pathnode.c:540-575].
14. EXPLAIN prints `Disabled: true` iff `disabled_nodes` exceeds the sum over children [explain.c:1246,1882].

**Qual and target costs**
15. Operators/functions cost `procost × cpu_operator_cost` per tuple unless a support function answers `SupportRequestCost` [plancat.c:2125].
16. Vars, Consts, AND/OR/NOT, Aggref, WindowFunc, PlaceHolderVar cost 0; GroupingFunc, MinMaxExpr, SQLValueFunction, XmlExpr, CoerceToDomain, NextValueExpr, JsonExpr cost one `cpu_operator_cost` [costsize.c:4851-5050].
17. Non-hashed SAOP charges `op.per_tuple × estimate_array_length × 0.5`; hashed SAOP charges array-length × hash cost at startup and one hash + one compare per tuple [costsize.c:4897-4929].
18. `estimate_array_length` defaults to 10 without a Const/ArrayExpr/DECHIST [selfuncs.c:2147].
19. `CoerceViaIO` charges the target type's input function plus the source type's output function [costsize.c:4951].
20. `RowCompareExpr` charges every column's operator [costsize.c:4979].
21. `RestrictInfo.eval_cost` is cached; pseudoconstant clauses move per_tuple into startup [costsize.c:4811-4848].
22. `SubPlan` charges `startup_cost` + `per_call_cost` per `cost_subplan`; `AlternativeSubPlan` uses only its first alternative [costsize.c:5007-5036].
23. Target-list eval cost is charged per output row (`path->rows`), qual cost per scanned tuple [every scan function].
24. `set_rel_width` prefers `stawidth` (`get_attavgwidth`) over `get_typavgwidth`; whole-row Var adds 24 + data width [costsize.c:6210].

**Scans**
25. `cost_seqscan`: disk = `seq_page_cost × pages` (never divided by the parallel divisor); CPU divided; rows divided and clamped [costsize.c:295].
26. `get_parallel_divisor = workers + max(0, 1 − 0.3×workers)` when `parallel_leader_participation` [costsize.c:6474].
27. `index_pages_fetched` uses `b = ceil(effective_cache_size × T / (total_table_pages + index_pages))` and the three Mackert–Lohman branches; result ≥ 1 and integral, capped at T [costsize.c:908].
28. `cost_index` interpolates `max_IO + corr² × (min_IO − max_IO)`; `min_IO = random + (pages−1) × seq` for loop_count = 1, and both use `index_pages_fetched(… × loop_count)/loop_count` with random cost for loop_count > 1 [costsize.c:686-774].
29. Index-only scans multiply heap `pages_fetched` by `(1 − allvisfrac)` with `ceil` in both min and max branches [costsize.c:702,731,749,760].
30. `qpquals` for an index path are the rel's `indrestrictinfo` (plus `ppi_clauses`) minus clauses redundant with an indexclause [costsize.c:850].
31. `genericcostestimate`: `numIndexPages = ceil(numIndexTuples × index.pages / index.tuples)`; I/O = `numIndexPages × random_page_cost` for a single scan, else Mackert–Lohman over `index.pages` pro-rated by `loop_count` only [selfuncs.c:7051-7231].
32. `genericcostestimate` CPU = `numIndexTuples × num_sa_scans × (cpu_index_tuple_cost + cpu_operator_cost × nquals)`; `indexStartupCost = qual_arg_cost` [selfuncs.c:7248-7254].
33. `btcostestimate`: unique index with `=` on every key column and no array/IS NULL → `numIndexTuples = 1` [selfuncs.c:7644-7650].
34. btree descent cost = `ceil(log2(index.tuples)) × cpu_operator_cost` (startup, and × num_sa_scans in total) plus `(tree_height + 1) × 50 × cpu_operator_cost` [selfuncs.c:7754-7774, :145].
35. `num_sa_scans` is clamped to `ceil(index.pages / 3)` and ≥ 1 for non-unique btree scans [selfuncs.c:7685-7686].
36. `btcost_correlation` = first-column correlation (negated for DESC), × 0.75 when `nkeycolumns > 1` [selfuncs.c:7305].
37. Partial-index predicates not implied by the index quals are ANDed into the selectivity quals [selfuncs.c:7274].
38. Bitmap heap `pages_fetched = 2·T·N/(2·T+N)` (ceil, cap T) for loop_count 1; Mackert–Lohman with summed index pages, divided by loop_count, otherwise [costsize.c:6514-6575].
39. `cost_per_page = random − (random − seq) × sqrt(pages_fetched/T)` when `pages_fetched ≥ 2`, else `random` [costsize.c:1071-1076].
40. Lossy bitmap: `maxentries = work_mem×1024 / 64` (≥16); `lossy = max(0, heap_pages − maxentries/2)`; `tuples_fetched` recomputed as exact-fraction × selectivity + lossy-fraction × all tuples [costsize.c:6580-6610, tidbitmap.c:1545].
41. Bitmap index node cost = `indextotalcost + 0.1 × cpu_operator_cost × rows`; And/Or add `100 × cpu_operator_cost` per combine (Or skips IndexPath children) [costsize.c:1122-1250].
42. BitmapAnd selectivity = product; BitmapOr = min(sum, 1) [costsize.c:1187,1237].
43. TID scan: `random_page_cost` per expected tuple; TID range scan: `random + seq × (pages − 1)` [costsize.c:1258,1363].
44. Function/tablefunc scans charge expression cost once at startup [costsize.c:1538,1600].
45. VALUES adds `cpu_operator_cost`, CTE/named tuplestore add `cpu_tuple_cost` per tuple on top of `cpu_tuple_cost + qual` [costsize.c:1657-1786].
46. Recursive union = nrterm + 10 × rterm cost/rows + `cpu_tuple_cost × rows` [costsize.c:1826].
47. SubqueryScan with no quals and trivial target costs exactly the subpath [costsize.c:1508].
48. Gather adds `parallel_setup_cost` to startup and `parallel_tuple_cost × rows` to run; GatherMerge additionally `2·cpu_operator_cost·N·log2N` startup, `(2·cpu_operator_cost·log2N + cpu_operator_cost)` per row and a 1.05 factor on the tuple cost, N = workers + 1 [costsize.c:446-551].

**Sort / materialize / agg**
49. `cost_tuplesort` comparison cost = caller's extra + `2 × cpu_operator_cost`; CPU = `cmp × N × log2 N`; run = `cpu_operator_cost × N`; N forced ≥ 2 [costsize.c:1898-1990].
50. External sort when `output_bytes > sort_mem`: disk = `2 × npages × ceil(logM(nruns)) × (0.75 seq + 0.25 random)`, `log_runs = 1` when `nruns ≤ M`, `M = clamp(sort_mem/278528, 6, 500)` [costsize.c:1930-1953, tuplesort.c:1778].
51. Bounded sort (`limit_tuples < tuples`, `tuples > 2×limit`) CPU = `cmp × N × log2(2 × limit)` [costsize.c:1955-1963].
52. `cost_sort` adds the input's total cost to startup; `disabled_nodes += !enable_sort` [costsize.c:2144].
53. Incremental sort: groups from `estimate_num_groups` (default `min(N, 200)`), per-group `cost_tuplesort`, `+ (cpu_tuple_cost + extra_cmp) × N + 2 × cpu_tuple_cost × groups` [costsize.c:2000-2137].
54. Append adds `0.5 × cpu_tuple_cost × rows`; ordered Append startup = Σ child startups; parallel Append uses per-child divisor ratios and `append_nonpartial_cost` [costsize.c:2250-2425].
55. MergeAppend: N ≥ 2, `2·cpu_operator_cost·N·log2N` startup, per row `2·cpu_operator_cost·log2N + 0.5·cpu_tuple_cost` [costsize.c:2432].
56. Material: `2 × cpu_operator_cost` per tuple plus `seq_page_cost × pages` if over work_mem; rescan = `cpu_operator_cost` per tuple (+ spill pages) [costsize.c:2483,4697].
57. `cost_rescan`: FunctionScan and single-batch HashJoin rescan at `total − startup`; CTE/WorkTable at `cpu_tuple_cost` per row; default = original cost [costsize.c:4641].
58. Memoize rescan: `est_entry_bytes = relation_byte_size + 24 + 24 + 16×tuples + key widths`; `hit_ratio = (calls−ndistinct)/calls × entries/max(ndistinct, entries)`; default ndistinct → `ndistinct = calls` [costsize.c:2541-2675].
59. `cost_subplan`: hashed → one-time `plan.total + cpu_operator_cost × rows`; EXISTS → `run/rows`; ANY/ALL → `0.5 × run + 0.5 × rows × cpu_operator_cost`; startup paid once only if uncorrelated and top node materializes [costsize.c:4534].
60. `cost_agg` PLAIN/SORTED/HASHED formulas as §4.10, with SORTED and HASHED sharing identical total CPU [costsize.c:2725-2769].
61. HashAgg spill: `nbatches = max(ceil(max(groups×entry/mem_limit, groups/ngroups_limit)),1)`, `depth = ceil(log(nbatches)/log(max(partitions,2)))`, pages × depth × 2 written at `random_page_cost` and read at `seq_page_cost`, plus `depth × input × 2 × cpu_tuple_cost` [costsize.c:2790-2845].
62. HAVING quals: cost per output tuple and selectivity applied to rows (Agg and Group) [costsize.c:2852-2869,3221-3235].
63. WindowAgg: per window function `add_function_cost + args + aggfilter` per input tuple; `cpu_operator_cost × (partcols + ordercols)` and `cpu_tuple_cost` per tuple; startup pro-rated by `get_windowclause_startup_tuples` [costsize.c:3098-3190].
64. Group: `cpu_operator_cost × numGroupCols` per input tuple [costsize.c:3195].

**Joins**
65. Nestloop initial: startup = both startups; run = outer run + (outer_rows−1) × inner rescan startup + inner run + (outer_rows−1) × inner rescan run (the inner terms deferred for SEMI/ANTI/unique) [costsize.c:3267].
66. Nestloop final: `ntuples = outer × inner` normally; SEMI/ANTI/unique use `outer_matched = rint(outer × outer_match_frac)`, `inner_scan_frac = 2/(match_count+1)`, and the indexed-vs-unindexed branches of §5.3 [costsize.c:3349-3535].
67. `has_indexed_join_quals` requires empty `joinrestrictinfo`, a parameterized plain index/IOS/simple bitmap inner, and every movable ppi clause redundant with an indexclause [costsize.c:5211].
68. `compute_semi_anti_join_factors`: `outer_match_frac = clauselist_selectivity(SEMI|ANTI)`, `match_count = max(1, nselec × inner_rows / jselec)` [costsize.c:5114].
69. Mergejoin initial: scan fractions from `mergejoinscansel` on the first merge clause (cached per RestrictInfo); LEFT/ANTI force outer 0..1, RIGHT/RIGHT_ANTI force inner 0..1, FULL/clauseless force both [costsize.c:3595-3654].
70. Mergejoin sorts: outer may use incremental sort when presorted keys > 0 and enabled; inner always full `cost_sort`; costs pro-rated by start/end selectivities [costsize.c:3688-3776].
71. Mergejoin final: `rescannedtuples = max(mergejointuples − inner_rows, 0)` (0 if outer is UniquePath or mark/restore skipped); `rescanratio = 1 + rescanned/inner_rows` [costsize.c:3922-3937].
72. `mat_inner_cost = inner_run + cpu_operator_cost × inner_rows × rescanratio`; materialize if cheaper and `enable_material`, or required (no mark/restore support), or (enable_material and) sorted inner exceeds work_mem [costsize.c:3948-4020].
73. `skip_mark_restore` iff SEMI/ANTI/inner_unique and all join quals are merge clauses [costsize.c:3894-3901].
74. Merge-qual cost is charged over `outer_skip + inner_skip × rescanratio` (startup) and the scanned remainder (run); `cpu_tuple_cost + other quals` per `mergejointuples` [costsize.c:4033-4055].
75. Hashjoin initial: startup = outer startup + inner **total** + `(cpu_operator_cost × nclauses + cpu_tuple_cost) × inner_rows`; run += `cpu_operator_cost × nclauses × outer_rows` [costsize.c:4184-4203].
76. Multi-batch hash join adds `seq_page_cost × innerpages` startup and `seq_page_cost × (innerpages + 2 × outerpages)` run [costsize.c:4243-4256].
77. `ExecChooseHashTableSize`: `tupsize = 16 + 16 + MAXALIGN(width)`; skew reserves 2 % of memory at `MAXALIGN(width) + 116` bytes per MCV entry (220 in the §8.2 worked example, where width = 100); buckets = nextpow2(ntuples) capped by memory; batches = nextpow2(max(2, ceil(bytes/(mem − bucket_bytes)))); PG 18 walk-back halves batches while `nbatch ≥ space_allowed/BLCKSZ` [nodeHash.c:658-940].
78. `virtualbuckets = numbuckets × numbatches`; inner bucket size = min over hash clauses of cached `estimate_hash_bucket_stats` (extended stats first) [costsize.c:4335-4406].
79. `estimate_hash_bucket_stats`: default ndistinct → `max(0.1, mcv_freq)`; else `1/min(nbuckets, ndistinct×rows/tuples)` scaled by `mcv_freq/avgfreq`, clamped to [1e-6, 1] [selfuncs.c:4060].
80. Hash probe cost = `hq.per_tuple × outer_rows × clamp_row_est(inner_rows × bucketsize) × 0.5`; SEMI/ANTI variants use 0.5 for matched and 0.05 for unmatched rows [costsize.c:4440-4499].
81. `hashjointuples = approx_tuple_count` (product of cached per-clause JOIN_INNER selectivities) for inner joins; `outer_matched` (or `outer − outer_matched` for ANTI) otherwise [costsize.c:4480-4498].
82. Parallel hash: inner rows scaled up by the inner path's divisor for sizing; combined memory `hash_mem × (workers + 1)` tried first [costsize.c:4218, nodeHash.c:697-704].

**Cardinality**
83. Base rel rows = `clamp_row_est(tuples × clauselist_selectivity(baserestrictinfo, 0, JOIN_INNER))` [costsize.c:5349].
84. Parameterized base rel rows use `varRelid = relid` over `ppi_clauses ++ baserestrictinfo` and are capped at `rel->rows` [costsize.c:5379].
85. Join rows: INNER `o×i×fk×j`; LEFT `max(o×i×fk×j, o)×p`; FULL `max(…, o, i)×p`; SEMI `o×fk×j`; ANTI `o×(1−fk×j)×p`; outer-join `jselec` uses only non-pushed-down quals [costsize.c:5501-5645].
86. Parameterized join rows use the input paths' rows and cap at the joinrel's rows [costsize.c:5460].
87. FK selectivity = `1/max(ref_tuples, 1)` per matched FK (SEMI/ANTI: `ref_rows/ref_tuples`, only when the referenced rel is the whole inner side); matched clauses are removed only if their count equals `nmatched_ec − nconst_ec + nmatched_ri`; divided by the const-clause selectivity for `ec_has_const` keys; no null derating [costsize.c:5651-5896].
88. Subquery rel tuples = cheapest-total rows of the sub-final rel; function rel = max SRF rows; tablefunc = 100; VALUES = list length; CTE self-ref = `10 × cte_rows`; named tuplestore default 1000; RESULT = 1; foreign default rows 1000 [costsize.c:5903-6205].
89. `estimate_rel_size`: pages from the smgr (min 10 if never analyzed and small), tuples = `rint(reltuples/relpages × curpages)`, `allvisfrac = relallvisible/curpages` clamped [plancat.c:1075, tableam.c table_block_relation_estimate_size].

**Selectivity interface / EXPLAIN**
90. `clause_selectivity_ext` caches `norm_selec`/`outer_selec` on the RestrictInfo when `varRelid == 0` or the clause is single-rel on `varRelid`; pseudoconstant non-Const → 1.0 [clausesel.c:684-745,960].
91. `restriction_selectivity`/`join_selectivity` dispatch through `oprrest`/`oprjoin`, 0.5 when absent, error outside [0,1] [plancat.c:1983,2022].
92. `estimate_num_groups` reports `SELFLAG_USED_DEFAULT`; Memoize treats that as "every call distinct" [selfuncs.c:3449, costsize.c:2591].
93. EXPLAIN prints `cost=%.2f..%.2f rows=%.0f width=%d`; rows are per-worker for partial nodes and `compute_gather_rows` for Gather; `Parallel ` prefix from `parallel_aware` [explain.c:1631,1799].
