# 02 — PostgreSQL 18.3 cost model

Scope: every cost function the planner calls, at formula level, so a
reimplementation reproduces the same `startup_cost`, `total_cost`,
`rows`, and `disabled_nodes` — and therefore the same `add_path`
decisions. All citations are `postgres/<path>:<function>`.
Selectivity *interfaces* consumed here are defined in doc 03.

Notation: `LOG2(x) = log(x)/0.693147180559945`
(`postgres/src/backend/optimizer/path/costsize.c`, line 113);
`MAXALIGN` rounds to 8; `SizeofHeapTupleHeader` = 23 → 24,
`SizeofMinimalTupleHeader` = 15 → 16
(`postgres/src/include/access/htup_details.h`); `BLCKSZ` = 8192;
`Cost`/`Selectivity`/`rows` are C doubles. All `cost_*` /
`initial_cost_*` / `final_cost_*` symbols below were re-verified with
`global -x` at the cited `costsize.c` lines.

---

## 1. Cost currency and GUCs

### 1.1 Cost GUCs (C defaults in `src/include/optimizer/cost.h`; ranges in `src/backend/utils/misc/guc_tables.c`, verified)

| GUC | PG 18 default | unit / note |
|---|---|---|
| `seq_page_cost` | 1.0 | per sequential page |
| `random_page_cost` | 4.0 | per random page |
| `cpu_tuple_cost` | 0.01 | per tuple processed |
| `cpu_index_tuple_cost` | 0.005 | per index entry |
| `cpu_operator_cost` | 0.0025 | per operator call (× `procost`) |
| `parallel_tuple_cost` | 0.1 | per worker→leader tuple |
| `parallel_setup_cost` | 1000.0 | per Gather/GatherMerge |
| `effective_cache_size` | 524288 pages (4 GB) | **pages**, not bytes |
| `work_mem` | 4096 kB | kB; min 64 |
| `hash_mem_multiplier` | 2.0 | range 1.0–1000.0 (verified `guc_tables.c`) |
| `recursive_worktable_factor` | 10.0 | CTE self-ref rows multiplier |
| `max_parallel_workers_per_gather` | 2 | worker cap |
| `min_parallel_table_scan_size` | 1024 pages (8 MB) | |
| `min_parallel_index_scan_size` | 64 pages (512 kB) | |
| `parallel_leader_participation` | true | read by `get_parallel_divisor` |
| `jit_above_cost` / `jit_optimize_above_cost` / `jit_inline_above_cost` | 100000 / 500000 / 500000 | thresholds on final `total_cost` |

`get_hash_memory_limit()`
(`postgres/src/backend/executor/nodeHash.c`, line 3622, verified):
`work_mem * hash_mem_multiplier * 1024` bytes (default 8,388,608).
Tablespace overrides: each scan cost reads `spc_seq/random_page_cost`
from `get_tablespace_page_costs`
(`postgres/src/backend/utils/cache/spccache.c`), falling back to the GUCs.

### 1.2 `enable_*` and `disabled_nodes` (PG 18 semantics)

All `enable_*` default true except `enable_partitionwise_join` /
`enable_partitionwise_aggregate` (false). Since PG 18 (commit
`e2225346794`) a disabled node type adds no cost constant; each `Path`
carries `int disabled_nodes`:

- Setters add children's counts:
  `cost_seqscan` (`enable_seqscan`, `costsize.c:cost_seqscan` line 295),
  `cost_index` (`enable_indexscan`, line 560),
  `cost_bitmap_heap_scan` (`enable_bitmapscan`, line 1023),
  `cost_sort` (input + `enable_sort`, line 2144),
  `cost_material` (`enable_material`, line 2483),
  `cost_gather_merge` (`enable_gathermerge`, line 485),
  `cost_agg` (`AGG_HASHED`/`AGG_MIXED` when `!enable_hashagg`, line 2682),
  `initial_cost_nestloop`/`initial_cost_mergejoin`/`initial_cost_hashjoin`
  (lines 3267/3552/4160) each add their own flag plus children's.
- Hard gates (no path built): `enable_indexonlyscan`
  (`path/indxpath.c:check_index_only`), `enable_tidscan` (except CURRENT
  OF, `path/tidpath.c`), `enable_memoize` (`path/joinpath.c:get_memoize_path`),
  `enable_incremental_sort` (`cost_incremental_sort` asserts enabled).
- Pass-through copies/sums: `cost_gather`, `cost_subqueryscan`,
  `cost_append` (sums children), `cost_merge_append`, `cost_windowagg`,
  `cost_group`, `cost_recursive_union` (nrterm + rterm).
- `disable_cost = 1e10` (`costsize.c:141`) survives in exactly one place:
  `final_cost_hashjoin` adds it to startup when the inner MCV bucket
  exceeds hash memory. EXPLAIN prints `Disabled: true` when a node's
  `disabled_nodes` exceeds its children's sum (`commands/explain.c`).

### 1.3 `add_path` comparison (what the numbers must reproduce)

`compare_path_costs_fuzzily(p1, p2, fuzz)`, `STD_FUZZ_FACTOR = 1.01`
(`postgres/src/backend/optimizer/util/pathnode.c`, line 185, verified):

```
disabled_nodes differ → fewer wins (before any cost)
p1.total > p2.total*1.01 → DIFFERENT if startup considered and p2.startup > p1.startup*1.01 else BETTER2
symmetric for p2.total; then startup fuzz compare; else EQUAL
```

Tie-break on `COSTS_EQUAL` (equal keys + equal `required_outer`):
`parallel_safe`, then fewer `rows`, then fuzz `1.0000000001`, else keep
old. Pathlist sorted by `(disabled_nodes, total_cost)`.

---

## 2. Helpers

### 2.1 Clamps (`costsize.c:clamp_row_est` ff.)

```
clamp_row_est(n): NaN or >1e100 → 1e100; ≤1.0 → 1.0; else rint(n)
clamp_width_est(w): caps at MaxAllocSize; negativity is Assert-only, not clamped
```

### 2.2 `get_parallel_divisor(path)` (`costsize.c`, line 6474)

```
divisor = parallel_workers
if parallel_leader_participation: leader = 1.0 - 0.3*workers; if leader > 0: divisor += leader
```

Workers 1→1.7, 2→2.4, 3→3.1, ≥4→workers.
`compute_gather_rows = clamp_row_est(rows * divisor)`.

### 2.3 `cost_qual_eval` (`costsize.c`, line 4756, verified)

Output `QualCost{startup, per_tuple}`; top-level implicit AND is free:

| node | charge |
|---|---|
| `RestrictInfo` | cached `eval_cost`; `orclause` else `clause`; pseudoconstant folds per_tuple into startup |
| `Var`, `Const`, AND/OR/NOT | 0 |
| `FuncExpr` / `OpExpr` / `DistinctExpr` / `NullIfExpr` | `add_function_cost` (`procost * cpu_operator_cost`, or support-function `SupportRequestCost`; `util/plancat.c`) |
| `ScalarArrayOpExpr` plain | startup += op.startup; per_tuple += op.per_tuple × `estimate_array_length` × 0.5 |
| `ScalarArrayOpExpr` hashed | startup += op + hash startup + arraylen × hash.per_tuple; per_tuple += hash + compare |
| `Aggref`, `WindowFunc` | 0 (charged in Agg/WindowAgg) |
| `GroupingFunc`, `MinMaxExpr`, `SQLValueFunction`, `XmlExpr`, `CoerceToDomain`, `NextValueExpr`, `JsonExpr` | one `cpu_operator_cost` |
| `CoerceViaIO` | target input fn + source output fn |
| `ArrayCoerceExpr` | elemexpr cost × `estimate_array_length` |
| `RowCompareExpr` | every column's operator |
| `SubPlan` | `startup_cost` + `per_call_cost` per `cost_subplan`; no testexpr recursion |
| `AlternativeSubPlan` | first alternative only |
| `PlaceHolderVar` | 0 |
| `SubLink` | `elog(ERROR)` (must be planned) |

`estimate_array_length` (`selfuncs.c`): Const array → count;
non-multidim `ArrayExpr` → element count; DECHIST last stanumber; else **10**.

### 2.4 Byte helpers and Mackert–Lohman

```
relation_byte_size(t, w) = t * (MAXALIGN(w) + 24)
page_size(t, w)          = ceil(bytes / BLCKSZ)
```

`index_pages_fetched(fetched, pages, index_pages, root)` (`costsize.c`,
line 908, verified), with `T = max(pages,1)` and
`b = ceil(effective_cache_size * T / max(total_table_pages + index_pages, 1))`
(floored to 1): if `T <= b`: `pf = 2*T*f/(2*T+f)` (cap T); else
`lim = 2*T*b/(2*T-b)`, below-lim same formula, above-lim
`b + (f-lim)*(T-b)/T`; always `ceil`, capped at T.
`root->total_table_pages` = Σ base-rel pages (`make_one_rel`).

`approx_tuple_count(root, joinpath, quals)` (`costsize.c`):
`clamp_row_est(outer_rows * inner_rows * Π clause_selectivity(JOIN_INNER))`.

### 2.5 Widths

`set_rel_width` (`costsize.c`): per-expr widths; Vars use cached
`attr_widths[]` (`pg_statistic.stawidth` via `get_attavgwidth`, else
`get_typavgwidth`); PHVs add `ph_width` + expr eval cost; whole-row Var
adds 24 + data width. `set_pathtarget_cost_width`: width = Σ
`get_expr_width`, cost = Σ `cost_qual_eval_node` over non-Vars.

---

## 3. Scan costs

Preamble for every base-rel scan: `rows = ppi_rows` if parameterized
else `baserel->rows`; target cost charged per **output** row
(`startup += target.startup; run += target.per_tuple * rows`).

### 3.1 `cost_seqscan` (`costsize.c`, line 295, verified)

```
disk  = spc_seq_page_cost * pages                       # never parallel-divided
qp    = get_restriction_qual_cost(...)                  # ppi_clauses + baserestrictcost
startup = qp.startup + target.startup
cpu   = (cpu_tuple_cost + qp.per_tuple) * tuples + target.per_tuple * rows
if workers > 0: cpu /= divisor; rows = clamp(rows / divisor)
disabled = enable_seqscan ? 0 : 1
total = startup + cpu + disk
```

`cost_samplescan`: same with reduced pages/tuples; page cost is random
iff the TSM has `NextSampleBlock`; `disabled_nodes = 0`.

### 3.2 `cost_index` (`costsize.c`, line 560, verified)

```
qpquals = indrestrictinfo (+ ppi_clauses) minus indexclause-redundant (:850)
disabled = enable_indexscan ? 0 : 1
amcostestimate(..., &indexStartupCost, &indexTotalCost, &indexSelectivity, &indexCorrelation, &index_pages)
startup = indexStartupCost; run = indexTotalCost - indexStartupCost
tuples_fetched = clamp(indexSelectivity * baserel->tuples)
loop_count == 1:
    max_IO = pf(tuples_fetched) * random
    min_IO = pf_sel = ceil(indexSelectivity * pages) → random + (pf-1)*seq  (1 page → exactly random)
loop_count > 1:
    both via index_pages_fetched(... * loop_count)/loop_count at random cost
index-only: heap pf *= (1 - allvisfrac), ceil, in both branches
partial: workers = compute_parallel_worker(heap pf or -1, index_pages); ≤0 → reject
run += max_IO + corr² * (min_IO - max_IO)               # correlation interpolation
qp = cost_qual_eval(qpquals); startup += qp.startup + target.startup
run += (cpu_tuple_cost + qp.per_tuple) * tuples_fetched + target.per_tuple * rows
parallel: rows = clamp(rows/divisor); cpu /= divisor
```

### 3.3 `genericcostestimate` (`selfuncs.c`, line 7051, verified)

```
selectivityQuals = indexQuals + unimplied partial-index predicate (:7274)
num_sa_scans = Π estimate_array_length(SAOP quals), min 1
indexSelectivity = clauselist_selectivity(selectivityQuals, relid, JOIN_INNER)
numIndexTuples = rint(indexSelectivity * rel->tuples / num_sa_scans); clamp [1, index->tuples]
numIndexPages  = (pages>1 && tuples>1) ? ceil(numIndexTuples * pages/tuples) : 1
single scan: indexTotalCost = numIndexPages * random
multi (SAOP × loop): pf = index_pages_fetched(numIndexPages*scans, index.pages, index.pages); /loop_count
qual_arg_cost = non-index operand eval costs; qual_op_cost = cpu_operator_cost * nquals
indexStartupCost = qual_arg_cost
indexTotalCost  += qual_arg_cost + numIndexTuples*num_sa_scans*(cpu_index_tuple_cost + qual_op_cost)
indexCorrelation = 0
```

### 3.4 `btcostestimate` (`selfuncs.c`, line 7342, verified)

Leading `=` quals (+ next column; skip-array bridging; RowCompare counts
first column only; `IS NULL` counts as `=`); unique + `=` on all key
cols, no array/IS NULL → `numIndexTuples = 1`; else
`btreeSelectivity * rel->tuples`, `num_sa_scans` clamped to
`ceil(pages/3)` min 1; then `genericcostestimate`; plus descent
`ceil(log2(tuples)) * cpu_operator_cost` (× num_sa_scans in total) and
`(tree_height+1) * 50 * cpu_operator_cost`
(`DEFAULT_PAGE_CPU_MULTIPLIER = 50`); `indexCorrelation =
btcost_correlation` (first-col correlation, negated for DESC, ×0.75 when
`nkeycolumns > 1`, 0 without stats). Other AMs (hash/GiST/SP-GiST/GIN/
BRIN) start from `genericcostestimate` with their own descent terms.

### 3.5 `cost_bitmap_heap_scan` (`costsize.c`, line 1023, verified)

```
pages_fetched, indexTotalCost, tuples_fetched = compute_bitmap_pages(...)
startup = indexTotalCost
T = max(pages, 1)
cost_per_page = (pages_fetched >= 2) ? random - (random-seq)*sqrt(pf/T) : random
run = pf * cost_per_page
qp = get_restriction_qual_cost(...)     # ALL restrictions rechecked
startup += qp.startup; run += (cpu_tuple_cost + qp.per_tuple) * tuples_fetched
parallel: cpu /= divisor, rows clamped; target costs; disabled = enable_bitmapscan ? 0 : 1
```

`compute_bitmap_pages` (`costsize.c:6514`, verified):
`cost_bitmap_tree_node` → selectivity; `tuples_fetched =
clamp(sel * tuples)`; `pf = 2*T*N/(2*T+N)`, ceil, cap T; loop>1 →
Mackert–Lohman over summed index pages / loop; lossy: `maxentries =
tbm_calculate_entries(work_mem*1024)` (`costsize.c:6555`;
`nodes/tidbitmap.c:1545`; divisor `sizeof(PagetableEntry)+2*ptr` ≈ 104,
not 64), `lossy = max(0, heap - maxentries/2)` (`costsize.c:6589`),
`tuples_fetched = clamp(sel * exact_frac * tuples + lossy_frac * tuples)`.
`cost_bitmap_tree_node`: IndexPath → `indextotalcost + 0.1 *
cpu_operator_cost * rows`; And: `selec = Π`, cost Σ + `100 *
cpu_operator_cost` per extra child; Or: `selec = min(Σ, 1)`, extra cost
skips IndexPath children.

### 3.6 Remaining scans

- `cost_tidscan`: `random_page_cost` per expected tuple
  (`estimate_array_length` per SAOP else 1); `disabled_nodes = 0`.
- `cost_tidrangescan`: `sel = clauselist_selectivity(tidrangequals)`;
  `run = random + seq*(ceil(sel*pages)-1)`.
- `cost_subqueryscan`: subpath cost + quals over `subpath->rows`;
  unchanged when no quals and trivial target (node elided).
- `cost_functionscan`/`cost_tablefuncscan`: expression cost once at
  startup; `run = (cpu_tuple_cost + qp) * tuples`.
- `cost_valuesscan` (+`cpu_operator_cost`), `cost_ctescan` /
  `cost_namedtuplestorescan` (+`cpu_tuple_cost`), `cost_resultscan`
  (+0), each over `cpu_tuple_cost + qp`.
- `cost_recursive_union`: `startup = nrterm.startup`;
  `total = nrterm.total + 10*rterm.total + cpu_tuple_cost*rows`;
  `rows = nrterm.rows + 10*rterm.rows`.
- `cost_gather` (line 446): `startup = sub.startup + parallel_setup_cost`;
  `total = startup + (sub.total-sub.startup) + parallel_tuple_cost*rows`.
- `cost_gather_merge` (line 485): `N = workers+1`, `cmp =
  2*cpu_operator_cost`; `startup = cmp*N*LOG2(N) + setup + input_startup`;
  `run = rows*cmp*LOG2(N) + cpu_operator_cost*rows +
  parallel_tuple_cost*rows*1.05 + input_total`.

---

## 4. Sort and materialization

### 4.1 `cost_tuplesort` (`costsize.c`, line 1898, verified)

```
tuples = max(tuples, 2); comparison_cost += 2*cpu_operator_cost
limit>0 && limit<tuples → output = limit else output = tuples
output_bytes vs sort_mem = work_mem*1024:
  external (output_bytes > mem):
      startup = cmp*tuples*LOG2(tuples)
      npages = ceil(input_bytes/BLCKSZ); nruns = input_bytes/mem
      mergeorder = clamp(mem/278528, 6, 500)   # tuplesort.c:tuplesort_merge_order
      log_runs = nruns > mergeorder ? ceil(log(nruns)/log(mergeorder)) : 1
      startup += 2*npages*log_runs*(0.75*seq + 0.25*random)   # global GUCs, not tablespace
  bounded (tuples > 2*output or input_bytes > mem):
      startup = cmp*tuples*LOG2(2*output)
  quicksort: startup = cmp*tuples*LOG2(tuples)
run = cpu_operator_cost * tuples
```

### 4.2 `cost_sort` / `cost_incremental_sort` / append / material

- `cost_sort` (line 2144): tuplesort; `startup += input_total`;
  `disabled = input + (enable_sort?0:1)`; `total = startup + run`.
- `cost_incremental_sort` (line 2000): groups = `min(tuples, 200)` or
  `estimate_num_groups` over presorted keys; per-group tuplesort;
  `startup = group_startup + input_startup + group_run_share`;
  `run += (cpu_tuple_cost + extra_cmp)*tuples + 2*cpu_tuple_cost*groups`;
  `disabled = input` (generation gated by `enable_incremental_sort`).
- `cost_append` (line 2250): unordered non-parallel: startup = first
  child startup, totals/rows/disabled summed; ordered: unsorted children
  costed through `cost_sort(work_mem, limit_tuples)`, startup = Σ;
  parallel-aware: per-child divisor ratios +
  `append_nonpartial_cost` (greedy max-slot load) +
  `0.5*cpu_tuple_cost*rows` always.
- `cost_merge_append` (line 2432): `N = max(nstreams,2)`,
  `cmp = 2*cpu_operator_cost`; `startup = cmp*N*LOG2(N) + input_startup`;
  `run = tuples*cmp*LOG2(N) + 0.5*cpu_tuple_cost*tuples + input_total`.
- `cost_material` (line 2483): `startup = input_startup`;
  `run = input_run + 2*cpu_operator_cost*tuples`
  (+ `seq_page_cost*pages` when over `work_mem`);
  `disabled = input + (enable_material?0:1)`.

### 4.3 `cost_rescan` (`costsize.c`, line 4641) and Memoize/Subplan

| path | rescan_startup | rescan_total |
|---|---|---|
| FunctionScan | 0 | `total-startup` |
| HashJoin, 1 batch | 0 | `total-startup` |
| HashJoin, multi-batch | startup | total |
| CteScan/WorkTableScan | 0 | `cpu_tuple_cost*rows` (+ spill pages) |
| Material/Sort | 0 | `cpu_operator_cost*rows` (+ spill) |
| Memoize | `cost_memoize_rescan` | same |
| default | startup | total |

`cost_memoize_rescan` (line 2541, verified):
`entry_bytes = relation_byte_size + 24 + 24 + 16*tuples + key widths`;
`entries = floor(hash_mem_limit / entry_bytes)`;
`ndistinct = estimate_num_groups(param_exprs, calls)` (default flag →
`ndistinct = calls`); `est_entries = min(ndistinct, entries)`;
`evict = 1 - min(entries,nd)/nd`;
`hit = (calls-nd)/calls * entries/max(nd, entries)`;
`total = input_total*(1-hit) + cpu_operator_cost + cpu_tuple_cost*evict
+ 0.1*cpu_operator_cost*evict*tuples + cpu_tuple_cost +
cpu_operator_cost*tuples`;
`startup = input_startup*(1-hit) + cpu_tuple_cost`.

`cost_subplan` (line 4534): hashed → one-time `plan.total +
cpu_operator_cost*rows`; EXISTS → `run/rows` per call; ALL/ANY →
`0.5*run + 0.5*rows*cpu_operator_cost`; other → `run`; startup paid once
iff uncorrelated and top node materializes.

### 4.4 `cost_agg` / `cost_windowagg` / `cost_group`

`cost_agg` (line 2682, verified); SORTED and HASHED share identical CPU
ordering by construction:

```
PLAIN:  startup = input_total + trans.startup + trans.per_tuple*in + final.*; total = startup + cpu_tuple_cost; rows = 1
SORTED/MIXED: startup = input_startup; total = input_total + trans.* + cpu_operator_cost*groupcols*in + final.* + cpu_tuple_cost*groups
HASHED: startup = input_total + trans.* + cpu_operator_cost*groupcols*in + final.startup; total = startup + final.per_tuple*groups + cpu_tuple_cost*groups
MIXED/HASHED: disabled++ when !enable_hashagg
HASHED/MIXED spill: entry = hash_agg_entry_size(trans, width, space) (nodeAgg.c: 16 + MAXALIGN(16+width) + 16*ntrans + chunk);
  nbatches = max(ceil(max(groups*entry/mem, groups/ngroups_limit)), 1); depth = ceil(log(nbatches)/log(max(parts,2)));
  pages*depth*2 written at random + read at seq; + depth*in*2*cpu_tuple_cost
HAVING: startup += qc.startup; total += qc.startup + out*qc.per_tuple; rows = clamp(out * clauselist_selectivity(quals))
```

`cost_windowagg` (line 3098): per WindowFunc `add_function_cost + args +
aggfilter` per input tuple; `+ cpu_operator_cost*(partcols+ordercols)`
and `cpu_tuple_cost` per tuple; startup pro-rated by
`get_windowclause_startup_tuples` (`estimate_num_groups` on partition/
order exprs, frame options).
`cost_group` (line 3195): `total = input_total +
cpu_operator_cost*in*groupcols`; HAVING as in `cost_agg`.

---

## 5. Join costs (two-stage)

`JoinCostWorkspace`: public `disabled_nodes/startup_cost/total_cost`;
private `run_cost/inner_run_cost/inner_rescan_run_cost/outer_rows/
inner_rows/outer_skip/inner_skip/numbuckets/numbatches/inner_rows_total`.
`initial_*` = cheap bounds for `add_path_precheck`; `final_*` adds CPU.
All `final_*`: `rows = ppi_rows` or parent rows (partial: clamped /
divisor); target cost per output row.

### 5.1 `compute_semi_anti_join_factors` (`costsize.c`, line 5114)

```
jselec = clauselist_selectivity(joinquals, SEMI or ANTI)
nselec = clauselist_selectivity(joinquals, INNER, dummy sjinfo)
avgmatch = jselec > 0 ? max(1, nselec*inner_rows/jselec) : 1
outer_match_frac = jselec; match_count = avgmatch
```

### 5.2 Nestloop — `initial_cost_nestloop` (3267) / `final_cost_nestloop` (3349)

```
initial: disabled = (enable_nestloop?0:1) + inner + outer
  cost_rescan(inner) → rs_start/rs_total
  startup = outer.startup + inner.startup
  run = outer_run + (outer_rows>1 ? (outer_rows-1)*rs_start : 0)
  SEMI/ANTI/unique: defer inner terms; else run += inner_run + (outer_rows-1)*inner_rescan_run
final: outer_rows/inner_rows = max(rows,1)
  SEMI/ANTI/unique: matched = rint(outer*match_frac); scan_frac = 2/(match_count+1)
    indexed (has_indexed_join_quals: empty joinrestrictinfo, parameterized plain index/IOS/bitmap inner,
             every movable ppi clause index-redundant):
        run += inner_run*scan_frac + (matched-1)*rescan*scan_frac + unmatched*rescan/inner_rows
    else: ntuples += unmatched*inner_rows; run += inner_run + matched*rescan*scan_frac + unmatched*rescan
  else: ntuples = outer*inner; rq = cost_qual_eval(joinrestrictinfo)
        startup += rq.startup + target.startup; run += (cpu_tuple_cost + rq.per_tuple)*ntuples + target.per_tuple*rows
```

### 5.3 Mergejoin — `initial_cost_mergejoin` (3552) / `final_cost_mergejoin` (3837)

```
initial: scan fractions from mergejoinscansel on first merge clause (cached per RestrictInfo);
  LEFT/ANTI → outer 0..1; RIGHT/RIGHT_ANTI → inner 0..1; FULL/clauseless → both;
  skip = rint(rows*startsel); rows' = clamp(rows*endsel); re-derive sels
  outer sort: incremental when presorted>0 and enabled else cost_sort(work_mem,-1); inner: full cost_sort only
  startup += src.startup + src_run*startsel; run += src_run*(endsel-startsel) per side
final: mq = merge clauses; qq = other quals
  skip_mark_restore iff SEMI/ANTI/unique and all quals are merge clauses
  mergejointuples = approx_tuple_count(mergeclauses)
  rescanned = (UniquePath outer or skip_mark_restore) ? 0 : max(mergejointuples - inner_rows, 0)
  rescanratio = 1 + rescanned/inner_rows
  bare = inner_run*rescanratio; mat = inner_run + cpu_operator_cost*inner_rows*rescanratio
  materialize_inner = skip_mark_restore→false; enable_material && mat<bare→true;
      no-mark-restore support→true (required); enable_material && sorted inner > work_mem→true
  startup += mq.startup + mq.per_tuple*(outer_skip + inner_skip*rescanratio)
  run += mq.per_tuple*((outer_rows-outer_skip) + (inner_rows-inner_skip)*rescanratio)
       + (cpu_tuple_cost + qq.per_tuple)*mergejointuples + target costs
```

### 5.4 Hashjoin — `initial_cost_hashjoin` (4160) / `final_cost_hashjoin` (4275)

```
initial: disabled = (enable_hashjoin?0:1) + inner + outer
  startup = outer.startup + inner.total + (cpu_operator_cost*nclauses + cpu_tuple_cost)*inner_rows
  run = outer_run + cpu_operator_cost*nclauses*outer_rows
  inner_rows_total = parallel_hash ? inner_rows*divisor(inner) : inner_rows
  ExecChooseHashTableSize(inner_rows_total, width, skew=true, combined-mem if parallel, workers → buckets/batches)
  multi-batch: startup += seq*innerpages; run += seq*(innerpages + 2*outerpages)
final: num_batches/inner_rows_total stored; virtualbuckets = buckets*batches
  innerbucketsize = min over hash clauses of estimate_hash_bucket_stats (extended stats first; UniquePath → 1/virtual)
  MCV-bucket over memory → startup += disable_cost (sole 1e10 use)
  SEMI/ANTI/unique: matched = rint(outer*match_frac); scan_frac = 2/(match_count+1)
      run += hq.per_tuple*matched*clamp(inner_rows*bucketsize*scan_frac)*0.5
           + hq.per_tuple*(outer-matched)*clamp(inner_rows/virtual)*0.05
      hashjointuples = ANTI ? outer-matched : matched
  else: run += hq.per_tuple*outer_rows*clamp(inner_rows*bucketsize)*0.5
      hashjointuples = approx_tuple_count(hashclauses)
  run += (cpu_tuple_cost + qq.per_tuple)*hashjointuples + target costs
```

`ExecChooseHashTableSize` (`nodeHash.c:658`): `tupsize = 16 + 16 +
MAXALIGN(width)`; skew reserves 2% of memory at `tupsize + 84` bytes per
MCV; buckets = nextpow2(ntuples) capped by memory; batches =
nextpow2(max(2, ceil(bytes/(mem-bucket_bytes)))); PG 18 walk-back halves
batches while `nbatch >= space_allowed/BLCKSZ`.
`estimate_hash_bucket_stats` (`selfuncs.c`, line 4060, verified):
default nd → `max(0.1, mcv_freq)`; else
`1/min(nbuckets, nd*rows/tuples)` scaled by `mcv_freq/avgfreq`, clamped
`[1e-6, 1]`.

---

## 6. Cardinality inside costs

- `set_baserel_size_estimates` (`costsize.c`):
  `rows = clamp_row_est(tuples * clauselist_selectivity(baserestrictinfo,
  0, JOIN_INNER))`.
- `get_parameterized_baserel_size`: same over `ppi_clauses ++
  baserestrictinfo` with `varRelid = relid`, capped at `rel->rows`.
- Other RTEs set `tuples` then size: subquery = sub-final
  cheapest-total rows; function = max SRF rows; tablefunc = 100; VALUES
  = list length; CTE self-ref = `10 * cte_rows`; named tuplestore =
  `enrtuples` or 1000; RESULT = 1; foreign default rows 1000.
- `calc_joinrel_size_estimate` (line 5501, verified):
  `fkselec = get_foreign_key_join_selectivity(...)` (prunes FK clauses);
  outer joins split `jselec` (non-pushed-down) / `pselec` (pushed-down):

  | jointype | rows |
  |---|---|
  | INNER | `o*i*fk*j` |
  | LEFT | `max(o*i*fk*j, o)*p` |
  | FULL | `max(o*i*fk*j, o, i)*p` |
  | SEMI | `o*fk*j` |
  | ANTI | `o*(1-fk*j)*p` |

- `get_foreign_key_join_selectivity` (line 5651, verified): per matched
  FK `1/max(ref_tuples,1)` (SEMI/ANTI: `ref_rows/ref_tuples`, referenced
  side must be the whole inner); matched clauses removed only when their
  count equals `nmatched_ec - nconst_ec + nmatched_ri`; divide by const-EC
  clause selectivity; no null derating; result clamped.

---

## 7. Selectivity entry points (interface only; doc 03 for internals)

| function | used by |
|---|---|
| `clauselist_selectivity(root, clauses, varRelid, jointype, sjinfo)` (`path/clausesel.c`, line 100, verified) | size estimates, `genericcostestimate`, `btcostestimate`, HAVING, subqueryscan |
| `clause_selectivity` / `_ext` (line 667, verified; caches `norm_selec`/`outer_selec` on RestrictInfo) | `approx_tuple_count`, FK const correction |
| `restriction_selectivity` / `join_selectivity` (via `oprrest`/`oprjoin`; 0.5 if none) | OpExpr clauses |
| `estimate_num_groups` (`selfuncs.c`, line 3449, verified) | incremental sort, Memoize, Agg/Group, window startup |
| `estimate_hash_bucket_stats` (line 4060, verified) | `final_cost_hashjoin` |
| `mergejoinscansel` (`selfuncs.c`, line 2963, verified) | `initial_cost_mergejoin` |
| `estimate_array_length` | qual walker, tidscan, SAOP counts |

---

## 8. Worked examples (PG 18 defaults, hand-computed)

Shared: `work_mem` = 4 MB, `effective_cache_size` = 524288 pages, single
table (`total_table_pages` = its pages), no tablespace overrides, empty
targets, Const operands (`qual_arg_cost` = 0), `procost` = 1.

### 8.1 Equality index scan on a unique key

Table `tuples = 1e6, pages = 10,000`; unique btree `pages = 2,745,
tuples = 1e6, tree_height = 2`; eqsel selectivity 1e-6.

`btcostestimate`: unique + `=` on the only key → `numIndexTuples = 1`.
`genericcostestimate`: `numIndexPages = ceil(1*2745/1e6) = 1`;
`indexTotalCost = 1*4.0 = 4.0 + 1*(0.005+0.0025) = 4.0075`;
`indexStartupCost = 0`. Descent: `ceil(log2(1e6)) = 20 → 0.05`; page
`(2+1)*50*0.0025 = 0.375`. → startup 0.425, total 4.4325.

`cost_index` (`loop_count = 1`): `tuples_fetched = 1`;
`index_pages_fetched(1, 10000, 2745)`: `T = 10000`,
`b = ceil(524288*10000/12745) = 411369 ≥ T`,
`pf = ceil(2*10000*1/20001) = 1`. `max_IO = 4.0`; `min_IO`:
`pf = ceil(1e-6*10000) = 1 → 4.0` (correlation-independent).
`run = (4.4325-0.425) + 4.0 = 8.0075`; CPU `0.01*1 = 0.01`.
**`startup = 0.425`, `total = 8.44`, `rows = 1`**
(EXPLAIN `cost=0.42..8.44 rows=1`).

`loop_count = 1000`: `num_scans = 1000`;
`pf = index_pages_fetched(1000, 2745, 2745) = ceil(2*2745*1000/6490) =
846`; `indexTotalCost = 846*4/1000 + 0.0075 + 0.425 = 3.8165`.
Heap: `pf = ceil(2*10000*1000/21000) = 953`;
`max_IO = min_IO = 953*4/1000 = 3.812`.
**`startup = 0.425`, `total = 7.64` per iteration**; the nestloop pays
full cost once and `cost_rescan` (default: same) for the other 999.

### 8.2 Hash join 6M ⋈ 1.5M rows, 4 MB vs 512 MB `work_mem`

Inner orders 1.5M rows, width 100; outer lineitem 6M rows, width 130;
one hash clause, unique outer key (no MCV).

`initial_cost_hashjoin` common terms: `startup += (0.0025+0.01)*1.5e6 =
18,750`; `run += 0.0025*6e6 = 15,000` (plus input path costs).

`ExecChooseHashTableSize(1.5e6, 100)`: `tupsize = 16+16+104 = 136`;
`inner_bytes = 204,000,000`.

- **4 MB** (limit 8,388,608; skew removes 762 entries → 8,220,968):
  `nbuckets = 524,288` capped by memory → re-sized `65,536`;
  `dbatch = ceil(204e6/7,696,680) = 27 → nbatch = 32` (walk-back stops:
  `32 < 8388608/8192 = 1024`). **buckets 65,536, batches 32.**
  Batch I/O: `innerpages = 23,438`, `outerpages = 117,188`;
  `startup += 23,438`; `run += 23,438 + 2*117,188 = 257,814`.
- **512 MB** (limit ~1.07e9): `nbuckets = 2,097,152`, fits →
  **batches 1**, no batch I/O.

`final_cost_hashjoin` both: `virtualbuckets = 2,097,152`;
`estimate_hash_bucket_stats`: `nd = 1.5e6 < virtual` → `1/1.5e6`,
clamped to `1e-6`; probe `0.0025*6e6*clamp(1.5)*0.5 = 15,000`;
`hashjointuples = approx_tuple_count = 6,000,000`;
`run += 0.01*6e6 = 60,000`. Net join increment: 4 MB →
startup 42,188 / run 347,814; 512 MB → startup 18,750 / run 90,000.

### 8.3 Sort of 1M rows (width 100)

`input_bytes = 1e6*(104+24) = 128,000,000`; `cmp = 0.005`;
`LOG2(1e6) = 19.93157`; CPU `0.005*1e6*19.93157 = 99,657.8`;
`run = 0.0025*1e6 = 2,500` always.

- **256 MB** (≥128 MB): quicksort → `startup = 99,657.8`.
- **4 MB**: `npages = 15,625`; `nruns = 30.52`; `mergeorder =
  clamp(4194304/278528 = 15, 6, 500) = 15`; `30.52 > 15 → log_runs =
  ceil(ln30.52/ln15) = 2`; disk `2*15625*2*(0.75+1.0) = 109,375`.
  **`startup = 209,032.8`.**
- **LIMIT 10, 4 MB**: bounded heap:
  `startup = 0.005*1e6*LOG2(20) = 21,609.6`.

---

## 9. Reimplementation checklist

**Currency / GUCs**
1. Defaults: seq 1.0, random 4.0, cpu_tuple 0.01, cpu_index_tuple 0.005, cpu_operator 0.0025, parallel_tuple 0.1, parallel_setup 1000, eff_cache 524288 pages, work_mem 4096 kB, hash_mem_multiplier 2.0. (`cost.h`, `guc_tables.c`)
2. `get_hash_memory_limit() = work_mem × hash_mem_multiplier × 1024` bytes. (`nodeHash.c:3622`)
3. Page costs from tablespace reloptions, else GUCs. (`spccache.c`)
4. `LOG2 = log/0.693147180559945`; `clamp_row_est`: NaN/>1e100→1e100, ≤1→1, else rint. (`costsize.c:113,213`)
5. `relation_byte_size = tuples × (MAXALIGN(width)+24)`; `page_size = ceil/8192`.

**disabled_nodes**
6. Each cost fn sets `(enable_X?0:1)` + children; no cost constant. (`costsize.c` setters)
7. Index-only/TID (except CURRENT OF)/Memoize/incremental-sort gate generation, not counting. (`indxpath.c`, `tidpath.c`, `joinpath.c`)
8. `disable_cost = 1e10` only in `final_cost_hashjoin` (MCV bucket over memory). (`costsize.c:4421`)
9. Fuzz-1.01 compare, `disabled_nodes` first; pathlist sorted by `(disabled_nodes, total)`; tie-break parallel_safe → rows → fuzz 1e-10 → keep old. (`pathnode.c:185`)

**Qual / target**
10. Operators cost `procost × cpu_operator_cost` unless support-function cost. (`plancat.c`)
11. Var/Const/AND/OR/NOT/Aggref/WindowFunc/PHV = 0; GroupingFunc/MinMax/SQLValue/Xml/CoerceToDomain/NextValue/Json = 1 op. (`costsize.c:4851`)
12. Plain SAOP = `op × arraylen × 0.5`; hashed = arraylen×hash at startup + hash+compare per tuple; default arraylen 10. (`costsize.c:4897`, `selfuncs.c:2147`)
13. `CoerceViaIO` = input + output fn; RowCompare = every column. (`costsize.c:4951,4979`)
14. RestrictInfo eval cached; pseudoconstant folds per_tuple→startup; SubPlan = startup+per_call; AlternativeSubPlan = first only. (`costsize.c:4811,5007`)
15. Target cost per output row; qual cost per scanned tuple. (all scans)
16. Widths prefer `stawidth`, whole-row Var +24. (`costsize.c:6210`)

**Scans**
17. Seqscan: disk never parallel-divided; CPU/rows divided + clamped. (`costsize.c:295`)
18. `get_parallel_divisor = workers + max(0, 1-0.3×workers)` with leader participation. (`costsize.c:6474`)
19. `index_pages_fetched` Mackert–Lohman three branches; integral, ≥1, ≤T. (`costsize.c:908`)
20. `cost_index` correlation interpolation `max_IO + corr²(min_IO-max_IO)`; loop>1 pro-rates both via Mackert–Lohman at random cost; index-only `×(1-allvisfrac)` + ceil. (`costsize.c:560`)
21. `genericcostestimate`: `numIndexTuples = rint(sel×rel.tuples/num_sa_scans)`, clamped [1, index.tuples]; `numIndexPages = (pages>1 && tuples>1) ? ceil(numIndexTuples×pages/tuples) : 1`; single-scan `×random`, multi Mackert–Lohman/index-pages pro-rated by loop only; CPU = `tuples×scans×(index_tuple + ops)`; startup = arg cost. (`selfuncs.c:7051`)
22. btree unique-full-`=` → 1 tuple; descent `ceil(log2)×op` + `(height+1)×50×op`; SA scans clamped to `ceil(pages/3)`; correlation first-col ×0.75 multi-col. (`selfuncs.c:7342`)
23. Bitmap heap `pf = 2·T·N/(2·T+N)` (loop 1) else Mackert–Lohman/summed-index-pages/loop; `cost_per_page = random-(random-seq)·sqrt(pf/T)` for pf≥2. (`costsize.c:1023,6514`)
24. Lossy: `maxentries = tbm_calculate_entries(work_mem×1024)` (≈ work_mem×1024/104 via `sizeof(PagetableEntry)+2*ptr`); `lossy = max(0, heap-maxentries/2)`; tuples recomputed exact+lossy fractions. (`costsize.c:6555`, `nodes/tidbitmap.c:1545`)
25. Bitmap tree: index `+0.1×op×rows`; And/Or combine costs (+100×op, Or skips IndexPath children); And = Π, Or = min(Σ,1). (`costsize.c:1122`)
26. TID = random per tuple; TID-range = random + seq×(ceil(sel×pages)-1), min 1 page. (`costsize.c:1258,1363`)
27. VALUES +1 op, CTE/tuplestore +1 tuple-cost, Result +0, over `cpu_tuple_cost+qp`. (`costsize.c:1657`)
28. Recursive union = nrterm + 10×rterm + tuple-cost×rows. (`costsize.c:1826`)
29. Gather = setup + tuple-cost×rows; GatherMerge += `2·op·N·log2N` startup, per-row `2·op·log2N + op`, 1.05 tuple factor, N = workers+1. (`costsize.c:446`)

**Sort / materialize / agg**
30. Tuplesort: cmp = extra + 2×op; external `2×npages×ceil(logM(nruns))×(0.75seq+0.25random)`, M = clamp(mem/278528,6,500); bounded `cmp×N×log2(2×limit)`; run = op×N; N≥2. (`costsize.c:1898`, `tuplesort.c`)
31. `cost_sort` adds input total to startup; `disabled += !enable_sort`. (`costsize.c:2144`)
32. Incremental: groups via `estimate_num_groups` (default min(N,200)); per-group tuplesort + `(tuple+cmp)×N + 2×tuple×groups`. (`costsize.c:2000`)
33. Append +`0.5×tuple×rows`; ordered startup = Σ; parallel per-child divisor ratios + `append_nonpartial_cost`. (`costsize.c:2250`)
34. MergeAppend N≥2 `2·op·N·log2N` startup, per-row `2·op·log2N + 0.5·tuple`. (`costsize.c:2432`)
35. Material `2×op` per tuple + seq pages over work_mem; rescan table (§4.3). (`costsize.c:2483,4641`)
36. Memoize rescan entry math, hit/evict ratios, default ndistinct = calls. (`costsize.c:2541`)
37. `cost_subplan`: hashed once; EXISTS run/rows; ANY/ALL 0.5/0.5 mix; startup-once iff uncorrelated + materializing top. (`costsize.c:4534`)
38. `cost_agg` PLAIN/SORTED/HASHED formulas; spill batches/depth/pages/CPU; HAVING per-output + selectivity. (`costsize.c:2682`)
39. WindowAgg per-func + args/filter per input tuple, partition/order CPU, startup pro-rating. (`costsize.c:3098`)
40. Group = `op×groupcols` per input tuple. (`costsize.c:3195`)

**Joins**
41. Nestloop initial defers inner terms for SEMI/ANTI/unique; final uses matched/scan_frac with indexed vs full-scan branches. (`costsize.c:3267,3349`)
42. `compute_semi_anti_join_factors`: `match_frac = sel(SEMI|ANTI)`, `match_count = max(1, inner_sel×inner_rows/match_frac)`. (`costsize.c:5114`)
43. Mergejoin scan fractions from cached `mergejoinscansel`; LEFT/ANTI outer 0..1, RIGHT/RIGHT_ANTI inner 0..1; outer incremental / inner full sort pro-rated. (`costsize.c:3552`)
44. Mergejoin final: `rescanratio = 1 + max(mergeTuples-inner_rows,0)/inner_rows` (0 if Unique outer or mark/restore skipped); materialize iff cheaper+enabled, required, or sorted-inner overflows. (`costsize.c:3837`)
45. Hashjoin initial: startup = outer startup + inner **total** + `(op×nclauses+tuple)×inner_rows`; multi-batch seq-page terms. (`costsize.c:4160`)
46. `ExecChooseHashTableSize`: tupsize 16+16+MAXALIGN(width); 2% skew reserve; nextpow2 buckets/batches; PG18 walk-back. (`nodeHash.c:658`)
47. `virtualbuckets = buckets×batches`; probe `hq×outer×clamp(inner×bucketsize)×0.5` (SEMI/ANTI 0.5/0.05 split); tuples = `approx_tuple_count` or matched. (`costsize.c:4275`)
48. `estimate_hash_bucket_stats`: default `max(0.1, mcv)`; else `1/min(buckets, nd×rows/tuples)` × skew, clamp [1e-6,1]. (`selfuncs.c:4060`)

**Cardinality**
49. Base rows = `clamp(tuples × clauselist_selectivity(baserestrictinfo, 0, INNER))`; parameterized capped at `rel->rows`. (`costsize.c:5349,5379`)
50. Join rows: INNER `o·i·fk·j`; LEFT `max(·,o)·p`; FULL also `≥i`; SEMI `o·fk·j`; ANTI `o·(1-fk·j)·p`; outer `j` = non-pushed-down only. (`costsize.c:5501`)
51. FK = `1/max(ref_tuples,1)` per FK (SEMI/ANTI `ref_rows/ref_tuples`); exact-count clause removal; const-EC correction; no null derating. (`costsize.c:5651`)
52. RTE tuples: subquery = sub-cheapest rows; SRF max; tablefunc 100; VALUES length; CTE self-ref 10×; tuplestore 1000 default; RESULT 1; foreign 1000. (`costsize.c:5903`)
53. EXPLAIN `cost=%.2f..%.2f rows=%.0f width=%d`; partial rows per-worker, Gather `compute_gather_rows`; `Parallel ` from `parallel_aware`. (`explain.c`)

---

## 10. Additional worked examples (PG 18.3 defaults, computed end-to-end)

Shared defaults for every example below (PG 18.3, `cost.h` / `guc_tables.c`):
`seq_page_cost = 1.0`, `random_page_cost = 4.0`, `cpu_tuple_cost = 0.01`,
`cpu_index_tuple_cost = 0.005`, `cpu_operator_cost = 0.0025`,
`parallel_tuple_cost = 0.1`, `parallel_setup_cost = 1000.0`,
`effective_cache_size = 524288` pages, `work_mem = 4096` kB,
`hash_mem_multiplier = 2.0` so `get_hash_memory_limit() = 4096 × 2 × 1024 = 8388608`
bytes. Single table in the query so `root->total_table_pages` equals its pages,
no tablespace overrides (global GUCs apply everywhere including the sort disk
term), empty `pathtarget->cost`, Const comparison operands (`qual_arg_cost = 0`),
`procost = 1`. Every number below was produced with `python3 -c` before writing;
inputs → formula → result are all shown. Citations are `postgres/<path>:<function>`.

### 10.1 Btree index scan with correlation (`cost_index`, `btcostestimate`, `genericcostestimate`, `index_pages_fetched`)

Inputs: table `tuples = 1000000`, `pages = 10000`; btree `pages = 2745`,
`tuples = 1000000`, `tree_height = 2`; non-unique range qual with
`indexSelectivity = 0.01`; `loop_count = 1`; first-column correlation varied.
`tuples_fetched = clamp(0.01 × 1e6) = 10000`.

`genericcostestimate` (`selfuncs.c`): `numIndexPages = ceil(10000 × 2745 / 1e6) = 28`;
single scan I/O `= 28 × 4.0 = 112.0`; CPU `= 10000 × (0.005 + 0.0025) = 75.0`;
subtotal `112.0 + 75.0 = 187.0`. Descent (`btcostestimate`): `ceil(LOG2(1e6)) = 20`
so `20 × 0.0025 = 0.05`; page charge `(2 + 1) × 50 × 0.0025 = 0.375`;
`indexStartupCost = 0.425`, `indexTotalCost = 187.425`.

`index_pages_fetched` (`costsize.c:908`): `T = 10000`,
`total_pages = 10000 + 2745 = 12745`,
`b = ceil(524288 × 10000 / 12745) = 411368 ≥ T`, so
`pf = ceil(2 × 10000 × 10000 / 30000) = ceil(6666.67) = 6667`;
`max_IO_cost = 6667 × 4.0 = 26668.0`.
`min_IO_cost`: `pf_sel = ceil(0.01 × 10000) = 100`, so
`4.0 + 99 × 1.0 = 103.0` (the `pf == 1 → random_page_cost` rule does not trigger).
Correlation interpolation (`cost_index`, `costsize.c:560`):
`run_IO = max_IO + corr² × (min_IO − max_IO)`.

| `corr` | `run_IO` | `total = 0.425 + 187.0 + run_IO + 100.0` |
|---|---|---|
| `0.0` | `26668.0` | `26955.425` |
| `0.5` | `20026.75` | `20314.175` |
| `0.75` | `11725.1875` | `12012.6125` |
| `1.0` | `103.0` | `390.425` |

CPU is correlation-independent: `(0.01 + 0) × 10000 = 100.0`. At `corr = 1.0` the
scan costs `390.43`; at `corr = 0.0` it costs `26955.43` — the full
Mackert–Lohman curve, which is why unanalyzed (zero-correlation) large range
scans lose to seqscan.

### 10.2 Bitmap heap scan lossy vs exact (`cost_bitmap_heap_scan`, `compute_bitmap_pages`, `cost_bitmap_tree_node`, `tbm_calculate_entries`)

Inputs: `pages = 100000`, `tuples = 10000000`; btree `pages = 27000`,
`tuples = 1e7`, `tree_height = 3`; one range qual, selectivity `0.01`;
`loop_count = 1`; `baserestrictinfo` = that qual (`per_tuple = 0.0025`).

Index leg (`btcostestimate` non-unique + `cost_bitmap_tree_node`,
`costsize.c:1122`): `numIndexTuples = 0.01 × 1e7 = 100000.0`;
`numIndexPages = ceil(100000 × 27000 / 1e7) = 270`;
`270 × 4.0 + 100000 × 0.0075 = 1830.0`; descent
`ceil(LOG2(1e7)) = 24 → 24 × 0.0025 = 0.06` plus `(3 + 1) × 50 × 0.0025 = 0.5`,
so `indexStartupCost = 0.56`, `indexTotalCost = 1830.56`; tree node adds
`0.1 × 0.0025 × 100000 = 25.0` → `startup = 1855.56`.

`compute_bitmap_pages` (`costsize.c:6514`): `N = 100000.0`, `T = 100000`,
`pages_fetched = 2 × 1e5 × 1e5 / 3e5 = 66666.67 → ceil 66667`, `heap = 66666.67`.
`tbm_calculate_entries(work_mem × 1024)` (`nodes/tidbitmap.c:1545`) uses divisor
`sizeof(PagetableEntry) + 2 × ptr ≈ 104` (as in §3.5), floored, min 16.

- `work_mem = 4 MB`: `maxentries = floor(4194304 / 104) = 40329`;
  `40329 < 66666.67` → `lossy_pages = 66666.67 − 40329 / 2 = 46502.17`,
  `exact_pages = 20164.5`;
  `tuples_fetched = clamp(0.01 × (20164.5 / 66666.67) × 1e7 + (46502.17 / 66666.67) × 1e7) = 7005572`.
- `work_mem = 64 MB`: `maxentries = floor(67108864 / 104) = 645277 > heap` →
  exact, `tuples_fetched = 100000`.

`cost_bitmap_heap_scan` (`costsize.c:1023`):
`cost_per_page = 4.0 − 3.0 × sqrt(66667 / 100000) = 1.55050413` (`pf ≥ 2`);
`run_pages = 66667 × 1.55050413 = 103367.46` in both cases.
CPU `= (0.01 + 0.0025) × tuples_fetched`: lossy `7005572 × 0.0125 = 87569.65`;
exact `100000 × 0.0125 = 1250.0`.

**`startup = 1855.56` both; `total = 192792.67` (lossy, 4 MB) vs `106473.02`
(exact, 64 MB), `rows = 100000`.** The page I/O is identical; the entire
`86319.65` gap is recheck CPU on lossy pages.

### 10.3 External merge sort across 1 vs 2 passes (`cost_tuplesort`)

Inputs: `tuples = 5000000`, `width = 100` so
`input_bytes = 5e6 × (104 + 24) = 640000000`; `comparison_cost = 0.005`
(`2 × cpu_operator_cost` already included, `costsize.c:1898`);
`LOG2(5e6) = 22.25349666`; CPU `= 0.005 × 5e6 × 22.25349666 = 556337.42`;
`run = 0.0025 × 5e6 = 12500.0` always.
`mergeorder = clamp(sort_mem_bytes / 278528, 6, 500)` (`tuplesort.c:1778`).

- `work_mem = 4 MB` (`4194304` B): `npages = ceil(64e7 / 8192) = 78125`;
  `nruns = 64e7 / 4194304 = 152.58789062`; `mergeorder = 15.0588 → 15`;
  `152.59 > 15` → `log_runs = ceil(ln152.59 / ln15) = 2`;
  `npageaccesses = 2 × 78125 × 2 = 312500`;
  disk `= 312500 × (0.75 × 1.0 + 0.25 × 4.0) = 546875.0`;
  **`startup = 556337.42 + 546875.0 = 1103212.42`.**
- `work_mem = 64 MB` (`67108864` B): `nruns = 64e7 / 67108864 = 9.53674316`;
  `mergeorder = 240.9412 → 240`; `9.54 ≤ 240` → `log_runs = 1.0`;
  `npageaccesses = 2 × 78125 × 1 = 156250`;
  disk `= 156250 × 1.75 = 273437.5`;
  **`startup = 556337.42 + 273437.5 = 829774.92`.**

The second merge pass costs exactly `273437.5` here. The disk term uses the
global GUCs, never tablespace values.

### 10.4 Hash vs sorted aggregation crossover (`cost_agg`, `hash_agg_entry_size`, `hash_agg_set_limits`)

Inputs: `input_tuples = 1000000`, `input_width = 100`, `numGroupCols = 2`,
one transition (`numTrans = 1`, `transitionSpace = 0`), transition/final costs
zero so only grouping CPU and spill show. Per §4.4 SORTED and HASHED share
identical base CPU by construction:
`cpu_operator_cost × numGroupCols × input = 0.0025 × 2 × 1e6 = 5000.0`,
plus `cpu_tuple_cost × groups`.

- `groups = 10000`: base increment `= 5000.0 + 0.01 × 10000 = 5100.0` for both.
  `hash_agg_entry_size` (`nodeAgg.c:1701`):
  `16 + MAXALIGN(16 + 100) + 16 × 1 = 16 + 120 + 16 = 152`.
  `mem_limit = 8388608`, `ngroups_limit = 8388608 / 152 = 55188.21`;
  `groups × entry / mem = 10000 × 152 / 8388608 = 0.18119812`,
  same for `groups / ngroups_limit` → `nbatches = max(ceil(0.1812), 1) = 1`,
  `depth = 0`, no spill. **Tie at `5100.0`.**
- `groups = 500000`: base increment `= 5000.0 + 5000.0 = 10000.0` for both.
  `500000 × 152 / 8388608 = 9.05990601` → `nbatches = 10`;
  `num_partitions = clamp(1 + 1.5 × 9.05990601, 4, 1024) = 14.58985901`;
  `depth = ceil(log(10) / log(14.58985901)) = ceil(0.859) = 1`;
  `pages = 1e6 × 128 / 8192 = 15625.0`;
  `pages_written = pages_read = 15625.0 × 1 × 2.0 = 31250.0`;
  `startup += 31250.0 × 4.0 = 125000.0`;
  `total += 125000.0 + 31250.0 × 1.0 = 156250.0`;
  spill CPU `= 1 × 1e6 × 2.0 × 0.01 = 20000.0` added to both startup and total.
  **Hash increment `= 10000.0 + 176250.0 = 186250.0` vs sorted `10000.0`.**

Crossover: below memory the strategies tie on CPU; the first spilling group
count flips the choice to SORTED by `176250.0`.

### 10.5 Nested-loop with rescan scaling (`initial_cost_nestloop`, `final_cost_nestloop`, `cost_rescan`)

Inputs: outer seqscan `startup = 0.0`, `total = 2000.0` (`run = 2000.0`),
`outer_rows = 1000`; inner index scan per-iteration `startup = 0.425`,
`total = 8.44` (the §8.1 unique-key scan), so `inner_run = 8.015`;
`cost_rescan` default (`costsize.c:4641`): `rs_start = 0.425`, `rs_total = 8.44`.

`initial_cost_nestloop` (`costsize.c:3267`), inner join (no deferral):
`startup = 0.0 + 0.425 = 0.425`;
`run = 2000.0 + (1000 − 1) × 0.425 + 8.015 + (1000 − 1) × 8.015 = 10439.575`;
**`total = 10440.0`.**
`final_cost_nestloop` adds `(0.01 + 0.0025) × 1000 × 1 = 12.5` for a one-clause
joinqual over `ntuples = 1000 × 1` → **`10452.5`**.
First-iteration only would be `2000.0 + 8.44 = 2008.44`; the other 999 rescans
contribute `999 × 0.425 = 424.575` startup-equivalent and `999 × 8.015 = 8006.985`
run. A single-batch hash inner would instead rescan at `total − startup = 8.015`
with `rs_start = 0`, which is why the rescan table matters more than the
first-scan cost for large outer sides.

### 10.6 Incremental-sort vs full-sort (`cost_incremental_sort`, `cost_sort`, `cost_tuplesort`)

Inputs: `input_tuples = 1000000`, `width = 100`, caller `comparison_cost = 0.0`
so tuplesort `cmp = 0.005`; `input_startup = 0.0`, `input_total = 20000.0`
(`input_run = 20000.0`); presorted key with `input_groups = 100`
(`estimate_num_groups` value, `costsize.c:2000`).

Full sort (`cost_sort`, `costsize.c:2144`):
tuplesort `startup = 0.005 × 1e6 × LOG2(1e6) = 0.005 × 1e6 × 19.93156857 = 99657.84`,
`run = 2500.0`; **`startup = 99657.84 + 20000.0 = 119657.84`,
`total = 122157.84`.**

Incremental (`cost_incremental_sort`):
`group_tuples = 1e6 / 100 = 10000.0`; `LOG2(10000) = 13.28771238`;
`group_startup = 0.005 × 10000 × 13.28771238 = 664.38561898`;
`group_run = 0.0025 × 10000 = 25.0`; `group_input_run = 20000.0 / 100 = 200.0`;
`startup = 664.38561898 + 0.0 + 200.0 = 864.38561898`;
`run = 25.0 + (25.0 + 664.38561898) × 99 + 200.0 × 99 + (0.01 + 0.0) × 1e6 + 2 × 0.01 × 100 = 98076.17627877`;
**`total = 98940.56189775`.**
Incremental wins by `122157.84 − 98940.56 = 23217.28` because 100 sorted runs of
10000 rows cost far less comparison CPU than one run of 1000000. With
`input_groups = 1000` the same math gives `startup = 69.83`, `total = 82348.92` —
more, narrower groups sort cheaper until the `2 × cpu_tuple_cost × groups` term
(not the comparison term) dominates.

---

## 11. Extended checklist appendix (continues §9 numbering; §9 items 1–53 unchanged)

**Clamps / rows / widths / targets**
54. `clamp_width_est` caps at `MaxAllocSize`; negative widths are Assert-only, never clamped. (`costsize.c:213`)
55. `clamp_cardinality_to_long`: NaN → `LONG_MAX`, `≤ 0` → `0`, else C cast when `< LONG_MAX`. (`costsize.c:213`)
56. `compute_gather_rows = clamp_row_est(rows × get_parallel_divisor)`; Gather startup/run use the passed `rows`, defaulting to `ppi_rows`/`rel->rows`. (`costsize.c:446,6625,6474`)
57. `get_restriction_qual_cost`: parameterized → `cost_qual_eval(ppi_clauses) + baserestrictcost`, else `baserestrictcost` alone; `baserestrictcost` is set by `set_baserel_size_estimates`. (`costsize.c:5072,5349`)
58. `set_rel_width` charges `PlaceHolderVar` `ph_width` plus its expression eval cost into `reltarget->cost`; whole-row Var adds `MAXALIGN(SizeofHeapTupleHeader) + get_relation_data_width`. (`costsize.c:6210`)
59. `set_pathtarget_cost_width`: width `= Σ get_expr_width`, cost `= Σ cost_qual_eval_node` over non-Var expressions; joinrel width comes from `build_joinrel_tlist` over cached `attr_widths`. (`costsize.c:6367`)
60. Target cost is per output `path->rows`; qual cost is per scanned tuple (`baserel->tuples`, `tuples_fetched`, or `subpath->rows` for subquery scans). (all scans, `costsize.c:1457`)

**Qual evaluation details**
61. Top-level implicit AND is free; `RestrictInfo.eval_cost` cached (`startup < 0` = uncomputed); `orclause` walked when present else `clause`; pseudoconstant folds `per_tuple` into `startup`. (`costsize.c:4756,4811`)
62. `SubLink` is `elog(ERROR)` (must be planned away); `PlaceHolderVar` costs 0 with no recursion; anything unlisted recurses via `expression_tree_walker`. (`costsize.c:5002,5037,5053`)
63. `ArrayCoerceExpr` charges `elemexpr` startup plus `per_tuple × estimate_array_length(arg)`; `CoerceViaIO` charges result input fn plus source output fn. (`costsize.c:4951,4968`)
64. `add_function_cost` prefers a `SupportRequestCost` answer when `pg_proc.prosupport` is set, else `procost × cpu_operator_cost`; `get_func_cost` is not consulted by the walker. (`util/plancat.c:2125`)
65. `estimate_array_length`: Const array → element count (NULL → 0); non-multidim `ArrayExpr` → element count; DECHIST last stanumber when available; else `10`. (`selfuncs.c:2147`)

**Scan inputs / parallelism / AM specifics**
66. `cost_seqscan`: `startup = qp.startup + target.startup`; `cpu = (cpu_tuple_cost + qp.per_tuple) × tuples + target.per_tuple × rows`. (`costsize.c:295`)
67. `cost_samplescan`: page cost is `spc_random_page_cost` iff the TSM provides `NextSampleBlock`, else `spc_seq_page_cost`; TABLESAMPLE argument expressions are uncharged. (`costsize.c:370`)
68. `cost_index` `qpquals = extract_nonindex_conditions(indrestrictinfo, indexclauses)` plus `ppi_clauses` when parameterized; pseudoconstants and indexclause-redundant quals dropped. (`costsize.c:850`)
69. `genericcostestimate` selectivity quals `= indexQuals + unimplied partial-index predicate` (`add_predicate_to_index_quals`); `num_sa_scans = Π estimate_array_length(SAOP)` min 1; `numIndexTuples = rint(sel × rel.tuples / num_sa_scans)` clamped to `[1, index.tuples]`. (`selfuncs.c:7051,7274`)
70. `genericcostestimate` `numIndexPages = (pages > 1 && tuples > 1) ? ceil(numIndexTuples × pages / tuples) : 1.0`; multi-scan `= index_pages_fetched(numIndexPages × scans, index.pages, index.pages) × random / loop_count`; CPU `= tuples × scans × (cpu_index_tuple_cost + cpu_operator_cost × nquals)`; `indexStartupCost = qual_arg_cost` (`index_other_operands_eval_cost` of quals plus `indexOrderBys`). (`selfuncs.c:7051`)
71. `btcostestimate` `indexBoundQuals` = leading `=` quals plus next-column quals with skip-array bridging; `RowCompareExpr` counts first column only; `IS NULL` counts as `=`; unique full-`=` without array/`IS NULL` forces `numIndexTuples = 1`. (`selfuncs.c:7342`)
72. `btcost_correlation` reads first-key-column `STATISTIC_KIND_CORRELATION` for the `<` opfamily operator, negates for `reverse_sort[0]`, scales `× 0.75` when `nkeycolumns > 1`, returns 0 without stats; `tree_height` comes from `amgettreeheight` (`_bt_getrootheight`). (`selfuncs.c:7305`, `nbtree.c:1751`, `plancat.c:493`)
73. Other AMs (`hashcostestimate`, `gistcostestimate`, `spgcostestimate`, `gincostestimate`, `brincostestimate`) start from `genericcostestimate`; GiST/SP-GiST add `ceil(log(tuples)) × cpu_operator_cost` descent with `tree_height = log(pages) / log(100)` plus the same `(tree_height + 1) × 50 × cpu_operator_cost` page charge. (`selfuncs.c:7805,7847,7902,8257,8647`)
74. `compute_parallel_worker`: 0 when `heap_pages < min_parallel_table_scan_size` or `index_pages < min_parallel_index_scan_size`; else `1 +` count of `threshold × 3^k ≤ pages` per side, min of both sides, capped at `max_parallel_workers_per_gather`; the `parallel_workers` reloption overrides. (`allpaths.c:4274`)
75. Partial paths divide CPU by the divisor and clamp `rows / divisor`; disk/page terms are never divided; `parallel_leader_participation = false` reduces the divisor to exactly `workers`. (`costsize.c:295,560,1023,6474`)

**Bitmap / TID / subquery / function / VALUES / Gather**
76. `cost_bitmap_tree_node`: IndexPath contributes `indextotalcost + 0.1 × cpu_operator_cost × rows` with `selec = indexselectivity`; And/Or nodes contribute `total_cost`/`bitmapselectivity`; `get_indexpath_pages` sums the tree index pages for the loop `> 1` branch. (`costsize.c:1122,6514`)
77. `cost_bitmap_and_node`: `selec = Π`, cost `= Σ + 100 × cpu_operator_cost` per extra child, `rows = 0`, startup = total; `cost_bitmap_or_node`: `selec = min(Σ, 1)`, the `100 ×` extra skips IndexPath children. (`costsize.c:1165,1210`)
78. `cost_tidscan`: `ntuples = Σ estimate_array_length(SAOP) else 1`; `run = random × ntuples`; `startup = qp.startup + tid_qual.per_tuple`; per-tuple CPU `= cpu_tuple_cost + qp.per_tuple − tid_qual.per_tuple`. (`costsize.c:1258`)
79. `cost_tidrangescan`: `sel = clauselist_selectivity(tidrangequals)`; `pages = max(ceil(sel × pages), 1)`; `ntuples = sel × tuples`; `run = random + seq × (pages − 1)`. (`costsize.c:1363`)
80. `cost_subqueryscan`: `rows = clamp(subpath.rows × clauselist_selectivity(ppi_clauses ++ baserestrictinfo, 0, INNER))`; `disabled_nodes` copied from the subpath; NIL quals with trivial target returns the subpath costs unchanged (node elided). (`costsize.c:1457`)
81. `cost_functionscan` / `cost_tablefuncscan`: `exprcost = cost_qual_eval_node(functions | tablefunc)`; `startup = exprcost.startup + exprcost.per_tuple + qp.startup`; `run = (cpu_tuple_cost + qp.per_tuple) × tuples`. (`costsize.c:1538,1600`)
82. `cost_valuesscan` / `cost_ctescan` / `cost_namedtuplestorescan` / `cost_resultscan`: shared `cpu_tuple_cost + qp` base with `X = cpu_operator_cost` (VALUES), `cpu_tuple_cost` (CTE, tuplestore), `0` (Result); VALUES/CTE add target costs, named tuplestore/Result do not. (`costsize.c:1657,1708,1750,1788`)
83. `cost_recursive_union`: `width = max(nrterm, rterm)`; `cost_gather_merge` uses `N = workers + 1`, `cmp = 2 × cpu_operator_cost`, `disabled = input + (enable_gathermerge ? 0 : 1)`. (`costsize.c:1826,485`)

**Sort / material / memoize / subplan / agg / LIMIT and fractional costing**
84. `cost_tuplesort`: `input_bytes = relation_byte_size`, `sort_mem_bytes = sort_mem × 1024`, `tuples = max(tuples, 2.0)`; `LIMIT` selects `output_tuples/output_bytes`; external iff `output_bytes > sort_mem_bytes` with `npages = ceil(input_bytes / BLCKSZ)`, `nruns = input_bytes / sort_mem_bytes`, `mergeorder` divisor `2 × TAPE_BUFFER_OVERHEAD + MERGE_BUFFER_SIZE = 278528` (`BLCKSZ`, `32 × BLCKSZ`); bounded iff `tuples > 2 × output or input_bytes > sort_mem_bytes`; callers thread `limit_tuples` (`cost_sort`, ordered `cost_append` via `cost_sort(work_mem, limit_tuples)`, `cost_incremental_sort` group sorts). (`costsize.c:1898`, `tuplesort.c:1778`, `costsize.c:2144,2250,2000`)
85. `cost_append` empty subpaths cost zero; unordered non-parallel startup = first child startup; parallel-aware startup = min over first `parallel_workers` children with per-child divisor ratios and greedy `append_nonpartial_cost` (`min(workers, n)` slots by decreasing cost); always `+= cpu_tuple_cost × 0.5 × rows` (`APPEND_CPU_COST_MULTIPLIER`). (`costsize.c:2174,2250`)
86. `cost_rescan`: FunctionScan and single-batch HashJoin rescan at `total − startup` (startup 0); multi-batch HashJoin at full cost; CTE/WorkTable at `cpu_tuple_cost × rows`; Material/Sort at `cpu_operator_cost × rows` (each plus `seq × pages` when spilled); Memoize delegates to `cost_memoize_rescan`; default is the original startup/total. (`costsize.c:4641,2541`)
87. `cost_memoize_rescan`: `est_cache_entries = floor(hash_mem / est_entry_bytes)`; `est_entries = min(min(ndistinct, entries), UINT32_MAX)`; `evict = 1 − min(entries, nd) / nd`; `hit = (calls − nd) / calls × entries / max(nd, entries)`; `calls` is the outer row count from `create_memoize_path`; `SELFLAG_USED_DEFAULT` forces `ndistinct = calls`. (`costsize.c:2541`, `nodeMemoize.c:1172`)
88. `cost_subplan`: testexpr wrapped with `make_ands_implicit` at `root = NULL`; hashed adds `plan.total + cpu_operator_cost × rows` once; EXISTS adds `run / clamp(plan_rows)`; ALL/ANY adds `0.5 × run + 0.5 × plan_rows × cpu_operator_cost`; startup paid once iff `parParam == NIL` and `ExecMaterializesOutput` top node, else folded per call. (`costsize.c:4534`)
89. `cost_agg` spill: `hash_agg_entry_size = 16 + MAXALIGN(16 + width) + 16 × numTrans + (transitionSpace > 0 ? CHUNKHDRSZ + nextpower2 : 0)`; `hash_agg_set_limits` splits `mem_limit`/`ngroups_limit`/`num_partitions` (partitions clamped `[4, 1024]` and by `(mem × 0.25 − BLCKSZ) / BLCKSZ`); `nbatches = max(ceil(max(groups × entry / mem, groups / ngroups_limit)), 1)`; `depth = ceil(log(nbatches) / log(max(partitions, 2)))`; written/read `= pages × depth × 2.0` at `random`/`seq`; plus `depth × input × 2.0 × cpu_tuple_cost` to both startup and total; `nbatches == 1` gives depth 0 and vanishing spill terms. (`costsize.c:2682`, `nodeAgg.c:1701,1809`)
90. `cost_windowagg` startup pro-rating uses `get_windowclause_startup_tuples` (`estimate_num_groups` on partition/order exprs, frame options, `DEFAULT_INEQ_SEL` for non-const offsets); `startup += (total − startup) / input × (startup_tuples − 1)` when `> 1`. (`costsize.c:2884,3098`)
91. `LIMIT` / `tuple_fraction` effects beyond `limit_tuples`: bounded heap selection in `cost_tuplesort`, per-group limit threading in `cost_incremental_sort`, ordered-append limit sorts, and fractional-path comparison (`startup + fraction × (total − startup)`) in `compare_fractional_path_costs` / `set_cheapest` when the query has `LIMIT` — a small startup gap can outweigh a large total gap. (`costsize.c:1898,2000,2250`, `pathnode.c:set_cheapest`)

**Join costing details**
92. `JoinCostWorkspace` carries public `disabled_nodes/startup_cost/total_cost` and private `run_cost/inner_run_cost/inner_rescan_run_cost/outer_rows/inner_rows/outer_skip/inner_skip/numbuckets/numbatches/inner_rows_total`; `initial_*` bounds feed `add_path_precheck`, `final_*` adds the qual/target CPU with `rows = ppi_rows` else parent rows (partial: clamped `/ divisor`). (`costsize.c:3267,3552,4160`)
93. `initial_cost_nestloop` defers both inner terms for SEMI/ANTI/unique; `final_cost_nestloop` uses `outer_matched = rint(outer × match_frac)`, `inner_scan_frac = 2.0 / (match_count + 1.0)`, with the indexed branch (`inner_run × scan_frac + (matched − 1) × rescan × scan_frac + unmatched × rescan / inner_rows`) selected by `has_indexed_join_quals`: empty `joinrestrictinfo`, parameterized plain index/IOS/simple-bitmap inner, every movable ppi clause index-redundant. (`costsize.c:3267,3349,5211`)
94. `initial_cost_mergejoin` caches `mergejoinscansel` per `(opfamily, collation, cmptype, nulls_first)` on the first merge clause's `RestrictInfo`; re-derives start/end sels from the rounded skip/row counts; outer sorts may be incremental when `outer_presorted_keys > 0` and enabled, inner sorts are always full `cost_sort` (no mark/restore); both sides pro-rate `src.startup + src_run × startsel` / `src_run × (endsel − startsel)`. (`costsize.c:3552,4081`)
95. `final_cost_mergejoin`: `skip_mark_restore` iff SEMI/ANTI/unique and every join qual is a merge clause; `rescanned = (UniquePath outer or skip) ? 0 : max(mergejointuples − inner_path_rows, 0)`; `bare = inner_run × rescanratio`, `mat = inner_run + cpu_operator_cost × inner_rows × rescanratio`; materialize iff cheaper+enabled, required (no mark/restore support, ignoring `enable_material`), or enabled with sorted inner over `work_mem`; merge-qual CPU splits over `outer_skip + inner_skip × rescanratio` (startup) and the scanned remainder (run) plus `(cpu_tuple_cost + qq.per_tuple) × mergejointuples`. (`costsize.c:3837`)
96. `initial_cost_hashjoin`: `startup = outer.startup + inner.total + (op × nclauses + tuple) × inner_rows` (inner **total**, not run); multi-batch adds `seq × innerpages` to startup and `seq × (innerpages + 2 × outerpages)` to run with `page_size(outer/inner rows, width)`; parallel hash sizes `inner_rows × get_parallel_divisor(inner)` and first tries combined `hash_mem × (workers + 1)`. (`costsize.c:4160`, `nodeHash.c:658`)
97. `ExecChooseHashTableSize` (64-bit): `ntuples ≤ 0 → 1000`; `tupsize = 16 + 16 + MAXALIGN(width)`; skew `bytes_per_mcv = tupsize + 64 + 4 + 16`, `skew_mcvs = (mem / bytes_per_mcv) × 2 / 100`; `max_pointers = prevpow2(min(mem / 8, MaxAllocSize / 8))`; `nbuckets = nextpow2_32(max(min(ceil(ntuples / 1), max_pointers), 1024))`; over-memory path re-sizes via `bucket_size = tupsize + 8`, `dbatch = min(ceil(bytes / (mem − bucket_bytes)), max_pointers)`, `nbatch = nextpow2_32(max(2, dbatch))`; PG 18 walk-back halves `nbatch` (doubling buckets) while `nbatch ≥ space_allowed / BLCKSZ`. (`nodeHash.c:658`)
98. `final_cost_hashjoin`: `virtualbuckets = buckets × batches`; Unique inner → `1 / virtual`; else extended-stats `estimate_multivariate_bucketsize` first, then cached per-clause `estimate_hash_bucket_stats` minima; MCV bucket over memory adds `disable_cost`; SEMI/ANTI probe splits `0.5` (matched) / `0.05` (unmatched over `inner / virtual`); inner-join tuples `= approx_tuple_count` (product of cached per-clause `JOIN_INNER` selectivities), ANTI `= outer − matched`. (`costsize.c:4275`)
99. `estimate_hash_bucket_stats`: `mcv_freq` = first MCV frequency else 0; default ndistinct → `max(0.1, mcv)`; else `avgfreq = (1 − nullfrac) / ndistinct`, `ndistinct ×= rows / tuples`, `estfract = 1 / nbuckets` if `ndistinct > nbuckets` else `1 / ndistinct`, scaled by `mcv / avgfreq` when MCV exceeds average, clamped `[1e-6, 1]`. (`selfuncs.c:4060`)

**Cardinality / selectivity interfaces**
100. Parameterized base sizes use `varRelid = relid` over `ppi_clauses ++ baserestrictinfo` (join clauses act as restrictions), capped at `rel->rows`; parameterized join sizes reuse the input paths' rows and cap at the joinrel rows. (`costsize.c:5379,5460`)
101. Non-base `tuples`: subquery = cheapest-total rows of the sub-final rel (attr widths copied for plain Vars); function = max `expression_returns_set_rows`; CTE = `cte_rows` or `clamp(recursive_worktable_factor × cte_rows)` for self-reference; named tuplestore = `enrtuples` or 1000; `set_foreign_size_estimates` sets rows 1000 plus `baserestrictcost`/width for the FDW to override. (`costsize.c:5903,5983,6075,6113,6175`)
102. `calc_joinrel_size_estimate` prunes FK-matched clauses first; outer joins split non-pushed-down `jselec` from pushed-down `pselec`; `RIGHT`/`RIGHT_ANTI`/`RIGHT_SEMI` are canonicalised before arrival, anything else is `elog(ERROR)`. (`costsize.c:5501`)
103. `get_foreign_key_join_selectivity` per-FK relevance (`con ∈ outer && ref ∈ inner` or swapped `ref_is_outer`); SEMI/ANTI skips `ref_is_outer` or non-single-base-rel inner; removal requires count `== nmatched_ec − nconst_ec + nmatched_ri` or the FK is skipped; `ec_has_const` keys divide by the derived `var = const` clause selectivity; raw `ref_tuples` (not filtered rows) so multi-column FKs contribute a single `1/N`; `CLAMP_PROBABILITY`, no null derating. (`costsize.c:5651`)
104. Bitmap CPU uses post-lossy `tuples_fetched`, never `path->rows`; `estimate_rel_size` takes pages from the smgr (min 10 if never vacuumed and small), `tuples = rint(reltuples / relpages × curpages)`, `allvisfrac = relallvisible / curpages` clamped `[0, 1]` (0 when `relallvisible = 0`). (`costsize.c:6514`, `plancat.c:1075`)
105. `clause_selectivity_ext` caches `norm_selec` (`JOIN_INNER`) / `outer_selec` (other) on the `RestrictInfo` when `varRelid == 0` or the clause touches at most the one `varRelid` rel; pseudoconstant non-Const → `1.0`; OR clauses walk `orclause`. (`clausesel.c:667,684,725`)
106. `restriction_selectivity` / `join_selectivity` dispatch through `oprrest` / `oprjoin` (`OidFunctionCall4Coll` / `OidFunctionCall5Coll`), default `0.5` when absent, error outside `[0, 1]`; `estimate_num_groups` sets `SELFLAG_USED_DEFAULT` on default ndistinct (Memoize then treats every call as distinct); cost-path defaults `DEFAULT_EQ_SEL 0.005`, `DEFAULT_INEQ_SEL 0.3333333333333333`, `DEFAULT_RANGE_INEQ_SEL 0.005`, `DEFAULT_NUM_DISTINCT 200`. (`plancat.c:1983,2022`, `selfuncs.c:3449`, `selfuncs.h:34`)
