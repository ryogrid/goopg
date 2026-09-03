# 01 — PostgreSQL 18.3 planning pipeline

Scope: how PG 18.3 turns a rewritten `Query` into a `PlannedStmt`.
All paths are relative to `postgres/`; every section cites the function
that establishes it. Struct fields are quoted from
`postgres/src/include/nodes/pathnodes.h`.
Notation: `root` = `PlannerInfo`, `glob` = `PlannerGlobal`, `rel` = `RelOptInfo`.

Verification: every `global -x` symbol below was re-checked against the
oracle on 2026-09-03 (all present at the cited locations).

---

## 1. Entry: `planner()` / `standard_planner()`

`planner()` only dispatches to `planner_hook` or
`standard_planner(parse, query_string, cursorOptions, boundParams)`
(`postgres/src/backend/optimizer/plan/planner.c:standard_planner`, line 303).
`standard_planner` does, in order:

1. **PlannerGlobal setup.** `glob = makeNode(PlannerGlobal)`; lists NIL,
   `lastPHId/lastRowMarkId/lastPlanNodeId = 0`. Key fields: `boundParams`
   (external Params); `subplans/subpaths/subroots` (index = `plan_id-1`);
   `rewindPlanIDs`; `finalrtable/resultRelations/appendRelations/
   partPruneInfos` (flattened by `set_plan_references`); `relationOids/
   invalItems` (cache invalidation); `paramExecTypes`;
   `parallelModeOK/parallelModeNeeded/maxParallelHazard`.
2. **Parallel-mode decision** (same function): `parallelModeOK` iff
   `CURSOR_OPT_PARALLEL_OK`, under postmaster, `CMD_SELECT`, no modifying
   CTE, `max_parallel_workers_per_gather > 0`, not a worker, and
   `max_parallel_hazard(parse)` (`util/clauses.c`) is not
   `PROPARALLEL_UNSAFE`. `parallelModeNeeded` starts as
   `parallelModeOK && debug_parallel_query != DEBUG_PARALLEL_OFF`, forced
   true later by `create_gather_path` / `create_gather_merge_path`
   (`util/pathnode.c`).
3. **`tuple_fraction`:** `CURSOR_OPT_FAST_PLAN` → `cursor_tuple_fraction`
   (default 0.1; ≥1 clamped to 0, ≤0 to 1e-10), else 0.0 ("fetch all").
   LIMIT/OFFSET refine it via `preprocess_limit` (§3). Semantics: `0` =
   all rows, `<1` = fraction, `>=1` = absolute count.
4. `root = subquery_planner(...)` (§2), `final_rel =
   fetch_upper_rel(root, UPPERREL_FINAL, NULL)`, `best_path =
   get_cheapest_fractional_path(final_rel, tuple_fraction)` (line 6617):
   `cheapest_total_path` when fraction ≤ 0; fraction ≥1 divided by its
   rows; scan `pathlist` skipping parameterized paths with
   `compare_fractional_path_costs` (`util/pathnode.c`: `disabled_nodes`
   first, then `startup + fraction*(total-startup)`).
5. `top_plan = create_plan(root, best_path)` (§11). `CURSOR_OPT_SCROLL`
   without `ExecSupportsBackwardScan(top_plan)` → top `Material`.
6. `debug_parallel_query` `on`/`regress` with `parallel_safe` top plan →
   forced single-copy `Gather` (`num_workers=1`), adding
   `parallel_setup_cost + parallel_tuple_cost * plan_rows`.
7. `SS_finalize_plan` on all subplans + top plan when
   `paramExecTypes != NIL` (`plan/subselect.c`).
8. `set_plan_references` on the top plan and every subplan (§11).
9. Build `PlannedStmt` (`commandType, queryId, hasReturning,
   hasModifyingCTE, transientPlan, dependsOnRole, parallelModeNeeded,
   planTree, rtable = glob->finalrtable, resultRelations,
   appendRelations, subplans, rewindPlanIDs, rowMarks, relationOids,
   invalItems, paramExecTypes`, `jitFlags`: `PGJIT_PERFORM` if
   `jit_enabled && total_cost > jit_above_cost`, plus `PGJIT_OPT3` /
   `PGJIT_INLINE` above their thresholds).

---

## 2. `subquery_planner()`: preprocessing order

`subquery_planner` (`plan/planner.c`, line 651) runs per top query, CTE,
sublink subplan, and un-pulled-up subquery RTE:

| # | call | file:function | effect |
|---|---|---|---|
| 1 | new `PlannerInfo` | `plan/planner.c:subquery_planner` | `query_level`, `tuple_fraction`, empty params/init_plans |
| 2 | `SS_process_ctes` | `plan/subselect.c` | §2.1 |
| 3 | `transform_MERGE_to_join` | `prep/prepjointree.c` | MERGE → outer join + `mergeJoinCondition` |
| 4 | `replace_empty_jointree` | `prep/prepjointree.c` | empty FROM → single `RTE_RESULT` |
| 5 | `pull_up_sublinks` | `prep/prepjointree.c` | §2.2 |
| 6 | `preprocess_function_rtes` | `prep/prepjointree.c` | inline SQL-function RTEs |
| 7 | `expand_virtual_generated_columns` (PG18) | `prep/prepjointree.c` | virtual generated cols → expressions |
| 8 | `pull_up_subqueries` | `prep/prepjointree.c` | §2.3 |
| 9 | `flatten_simple_union_all` (set-ops) | `prep/prepjointree.c` | UNION ALL tree → appendrel |
| 10 | rtable scan | `plan/planner.c` | `hasJoinRTEs/hasLateralRTEs/hasOuterJoins/hasResultRTEs` |
| 11 | `preprocess_rowmarks` | `plan/planner.c` | FOR UPDATE/SHARE → `PlanRowMark`s |
| 12 | `preprocess_expression` (target, quals, HAVING, WINDOW, LIMIT, ON CONFLICT, per-RTE funcs/VALUES/tablefunc) | `plan/planner.c` | §2.4 |
| 13 | HAVING split (no agg/volatile/subplan → WHERE; copied to both if no GROUP BY) | `plan/planner.c` | HAVING→WHERE |
| 14 | `reduce_outer_joins` | `prep/prepjointree.c` | outer→inner reduction; RIGHT→LEFT |
| 15 | `remove_useless_result_rtes` | `prep/prepjointree.c` | drop joined `RTE_RESULT` |
| 16 | `grouping_planner` | §3 | path generation |
| 17 | `SS_identify_outer_params`, `SS_charge_for_initplans` + `set_cheapest(final_rel)` | `plan/subselect.c` | initplan costing, parallel-unsafe marks |

Join removal runs inside `query_planner` (§4), not here:
`remove_useless_joins`, `reduce_unique_semijoins`,
`remove_useless_self_joins` (`plan/planmain.c`, `plan/analyzejoins.c`).

### 2.1 CTE handling — `SS_process_ctes`

`cterefcount == 0 && SELECT` → dropped. **Inlined** iff
`(ctematerialized == NEVER || (DEFAULT && cterefcount == 1)) &&
!cterecursive && SELECT && !contain_dml && (refcount ≤ 1 ||
!contain_outer_selfref) && !contain_volatile_functions`. Else planned
once via `subquery_planner` (fraction 0) as `SubPlan{CTE_SUBLINK}` in
`glob->subplans` + `root->init_plans`, costed with `cost_subplan`;
`set_cte_pathlist` (`path/allpaths.c`) later builds `CteScan` paths.

### 2.2 Sublink pull-up — `pull_up_sublinks`

- `convert_ANY_sublink_to_join` (`plan/subselect.c`): level-1 outer refs
  ⊆ `available_rels`, upper var in testexpr, no volatile → new
  `RTE_SUBQUERY` + `JOIN_SEMI`.
- `convert_EXISTS_sublink_to_join`: `simplify_EXISTS_query` must succeed
  (no set-ops/aggs/grouping sets/window/SRFs/modifying CTE/HAVING/OFFSET;
  constant `LIMIT > 0` dropped) → `JOIN_SEMI`, `JOIN_ANTI` under NOT.
- `convert_EXISTS_to_ANY`: unpullable EXISTS → hashable ANY
  (`subplan_is_hashable`/`testexpr_is_hashable`).
- Remainder → `SubPlan`/`InitPlan` in `SS_process_sublinks`: initPlan
  when `parParam == NIL` (EXISTS/EXPR/ARRAY/ROWCOMPARE); `useHashTable`
  for uncorrelated hashable ANY; else materialized rescan if
  `enable_material`.

### 2.3 Subquery pull-up — `pull_up_subqueries`

`is_simple_subquery` (`prep/prepjointree.c`): SELECT, no set-ops,
`hasAggs/hasWindowFuncs/hasTargetSRFs/groupClause/groupingSets/
havingQual/sortClause/distinctClause/limit/hasForUpdate/cteList`, not
`security_barrier`, lateral-safe, no volatile tlist. Plus simple UNION
ALL (`pull_up_simple_union_all`), single-row VALUES, constant functions.
OJ-nullable pulled-up expressions → `PlaceHolderVar`s.

### 2.4 `preprocess_expression` and `preprocess_minmax_aggregates`

```
flatten_join_alias_vars (non-RTFUNC/VALUES/TABLESAMPLE/TABLEFUNC) → eval_const_expressions (util/clauses.c,
  boundParams-aware) → canonicalize_qual (QUAL, prep/prepqual.c) → convert_saop_to_hashed_saop (QUAL/TARGET)
  → SS_process_sublinks → SS_replace_correlation_vars (level > 1) → make_ands_implicit (QUAL)
```

`preprocess_minmax_aggregates` (`plan/planagg.c`, line 73, verified):
ungrouped `min/max` over one plain table (all MIN/MAX, btree-sortable,
no window/set-op) → `MinMaxAggPath` of `LIMIT 1` ordered index scans,
kept iff cheaper.

---

## 3. `grouping_planner()`: the upper-rel pipeline

`grouping_planner` (`plan/planner.c`, line 1434).

1. **LIMIT** (`preprocess_limit`): count ≤0 → 1, NULL → no limit (0),
   non-constant → −1 ("assume 10%"); offset NULL → 0, non-constant → −1.
   `limit_tuples = count+offset` when both known; unknown sides give a
   10% fraction merged with the cursor fraction.
2. **Set-ops**: `plan_set_operations` (`prep/prepunion.c`) →
   `UPPERREL_SETOP`.
3. **Normal path**: `preprocess_grouping_sets`,
   `preprocess_targetlist` (`prep/preptlist.c`: `processed_tlist` +
   junk/rowid), `preprocess_aggrefs`, `select_active_windows`,
   `preprocess_minmax_aggregates`, `limit_tuples` (−1 with
   grouping/distinct/window/setop), then `current_rel =
   query_planner(root, standard_qp_callback, &qp_extra)` (§4).
4. **PathTargets** (all `planner.c`, after `query_planner`):
   `final_target = create_pathtarget(processed_tlist)`;
   `sort_input_target = make_sort_input_target(...)` (postpones
   expensive/volatile/SRF cols past Sort);
   `grouping_target = make_window_input_target(...)`;
   `scanjoin_target = make_group_input_target(...)` (grouping/agg/HAVING
   Vars); SRF splits via `split_pathtarget_at_srfs`;
   `apply_scanjoin_target_to_paths` re-projects scan/join paths
   (`ProjectionPath`/in-place), recurses into partitioned children,
   clears `partial_pathlist`/`consider_parallel` when unsafe, re-runs
   gather generation, `set_cheapest`.
5. **Upper rels in order** (`fetch_upper_rel` on demand):

   | # | function | UpperRelationKind |
   |---|---|---|
   | 1 | `create_grouping_paths` | `UPPERREL_PARTIAL_GROUP_AGG`, `UPPERREL_GROUP_AGG` |
   | 2 | `create_window_paths` | `UPPERREL_WINDOW` |
   | 3 | `create_distinct_paths` | `UPPERREL_PARTIAL_DISTINCT`, `UPPERREL_DISTINCT` |
   | 4 | `create_ordered_paths` | `UPPERREL_ORDERED` |
   | 5 | `create_lockrows_path` → `create_limit_path` (`limit_needed`) → `create_modifytable_path` (non-SELECT) | `UPPERREL_FINAL` |

   Enum verified at `src/include/nodes/pathnodes.h:UPPERREL_*`.
   `adjust_paths_for_srfs` inserts `ProjectSet` after grouping/window/
   ordered stages. `final_rel->consider_parallel` needs parallel-safe
   LIMIT expressions.
6. **Grouping** (`create_grouping_paths`, line 3780, verified):
   `CAN_USE_SORT` if no groupClause or `grouping_is_sortable`;
   `CAN_USE_HASH` if non-empty groupClause, no ordered aggs,
   `grouping_is_hashable`; `CAN_PARTIAL_AGG` if `can_partial_agg` (no
   grouping sets, no non-partial/non-serial aggs). Sorted: every input
   path × each `get_useful_group_keys_orderings` ordering (§9) →
   Sort/IncrementalSort + `AGG_SORTED|AGG_PLAIN` or `create_group_path`.
   Hashed: cheapest path `AGG_HASHED` + finalize over
   `partially_grouped_rel`. Overflow is *costed* (`cost_agg`), never
   vetoed; only `disabled_nodes++` when `!enable_hashagg`. Partial agg →
   `UPPERREL_PARTIAL_GROUP_AGG` (`AGGSPLIT_INITIAL_SERIAL`) +
   `gather_grouping_paths` (Gather/Gather Merge).
7. **DISTINCT** (`create_distinct_paths`, line 4790, verified):
   `numDistinctRows = estimate_num_groups(...)`; sorted Unique over every
   input (PG18 reordering via `get_useful_pathkeys_for_distinct` when
   `enable_distinct_reordering`); hashed `AGG_HASHED` on cheapest unless
   DISTINCT ON or `!enable_hashagg`.
8. **ORDER BY** (`create_ordered_paths`, line 5308, verified):
   `pathkeys_count_contained_in(sort_pathkeys, pathkeys, &presorted)`;
   full sort only on cheapest total path, `create_incremental_sort_path`
   on presorted prefixes (`enable_incremental_sort`); `limit_tuples`
   bounds costing; Gather Merge over sorted partials.

---

## 4. `query_planner()` (`plan/planmain.c`, line 54, verified)

| # | call | file:function | effect |
|---|---|---|---|
| 1 | `setup_simple_rel_arrays` | `util/relnode.c` | `simple_rel_array[]`, appendrel array |
| 2 | single `RTE_RESULT` fast path → `GroupResultPath` | `plan/planmain.c` | return early |
| 3 | `add_base_rels_to_query` | `plan/initsplan.c` | `build_simple_rel` per RangeTblRef (`get_relation_info`, §5) |
| 4 | `remove_useless_groupby_columns` | `plan/initsplan.c` | drop GROUP BY cols FD on a same-rel PK |
| 5 | `build_base_rel_tlists` | `plan/initsplan.c` | `attr_needed[attno]` = relids above needing the column |
| 6 | `find_placeholders_in_jointree`, `find_lateral_references` | `util/placeholder.c`, `plan/initsplan.c` | PHV levels, `lateral_vars` |
| 7 | `joinlist = deconstruct_jointree` | `plan/initsplan.c` | §4.1 |
| 8 | `reconsider_outer_join_clauses` | `path/equivclass.c` | OJ `a.x=b.y` + EC-constant `a.x` → `b.y=const` |
| 9 | `generate_base_implied_equalities` | `path/equivclass.c` | §4.3 |
| 10 | `standard_qp_callback` | `plan/planner.c` | pathkeys, §4.5 |
| 10b | `fix_placeholder_input_needed_levels` | `util/placeholder.c` | after qp_callback, before join removal |
| 11 | `remove_useless_joins` | `plan/analyzejoins.c` | left-join removal: single-baserel RHS, unused above, `rel_is_distinct_for` |
| 12 | `reduce_unique_semijoins` | `plan/analyzejoins.c` | unique-inner SEMI → INNER |
| 13 | `remove_useless_self_joins` (PG18, `enable_self_join_elimination`) | `plan/analyzejoins.c` (line 2488, verified) | unique-key inner self-joins merged |
| 14 | `add_placeholders_to_base_rels`, `create_lateral_join_info` | `plan/initsplan.c` | `lateral_relids`, `lateral_referencers` |
| 15 | `match_foreign_keys_to_quals` | `plan/initsplan.c` | `ForeignKeyOptInfo` for FK selectivity |
| 16 | `extract_restriction_or_clauses` | `path/orclauses.c` | per-rel restrictions from join ORs |
| 17 | `add_other_rels_to_query` | `plan/initsplan.c` | inheritance/partition children |
| 17b | `distribute_row_identity_vars` | `util/appendinfo.c` | after add_other_rels, before make_one_rel |
| 18 | `final_rel = make_one_rel(root, joinlist)` | `path/allpaths.c` (line 171, verified) | §5–§6 |

### 4.1 Jointree deconstruction and outer joins

`deconstruct_recurse` → `JoinTreeItem` per node + *joinlist* (nested
RangeTblRef lists = join-search sub-problems). `FromExpr`: child merged
when ≤1 member or total ≤ `from_collapse_limit`. `JoinExpr`:
`JOIN_FULL` always a 2-element sub-problem; else merged when
`len(l)+len(r) ≤ join_collapse_limit`, else nested. Outer/semi/anti get
`SpecialJoinInfo` from `make_outerjoininfo`; inner joins none. **PG16+
OJ relids**: each OJ has an RT index (`sjinfo->ojrelid`); nulled Vars
carry `varnullingrels`; joinrel relids include performed OJs
(`add_outer_joins_to_relids`, `path/joinrels.c`). `deconstruct_distribute`
sends WHERE/inner-ON via `distribute_qual_to_rels`, outer-ON via
`deconstruct_distribute_oj_quals` (clone clauses for commuted orders).

`SpecialJoinInfo` (`pathnodes.h`): `min_lefthand/min_righthand`
(order constraints), `syn_lefthand/syn_righthand`, `jointype` (RIGHT
rewritten), `ojrelid`, `commute_above/below_l/r` (OJ identities 3),
`lhs_strict`, `semi_can_btree/semi_can_hash/semi_operators/
semi_rhs_exprs` (from `compute_semijoin_info`, for `create_unique_path`).

### 4.2 `distribute_qual_to_rels` and `RestrictInfo`

`relids = pull_varnos(clause)`; var-free → **pseudoconstant**
(`hasPseudoConstantQuals`, gating `Result`). OJ ON: `is_pushed_down =
false`, `relids = ojscope`, `maybe_outer_join = true`; else
`is_pushed_down = true` + EC absorption (`process_equivalence`).
`check_mergejoinable` → `mergeopfamilies`; `check_hashjoinable` →
`hashjoinoperator`; `check_memoizable` → `left/right_hasheqoperator`.
Single-rel → `baserestrictinfo`, multi-rel → each member's `joininfo`.
Outer-mergeable OJ clauses also recorded in
`left/right/full_join_clauses` for step 8.

`RestrictInfo` planner fields (`pathnodes.h`): `clause`,
`is_pushed_down`, `can_join`, `pseudoconstant`, `has_clone/is_clone`,
`leakproof/has_volatile/security_level`, `clause_relids`,
`required_relids` (OJ: `ojscope`), `incompatible_relids`,
`outer_relids`, `left/right_relids`, `orclause`, `rinfo_serial`,
`parent_ec`, `eval_cost`, `norm_selec/outer_selec` (cache, −1 unset),
`mergeopfamilies`, `left/right_ec/em`, `scansel_cache`,
`hashjoinoperator`, `left/right_bucketsize`, `left/right_mcvfreq`,
`left/right_hasheqoperator`.

### 4.3 Equivalence classes (`path/equivclass.c`)

`process_equivalence`: mergejoinable `A = B` only (no volatile merge,
no `security_level > 0 && !leakproof`, identical sides rejected);
matches ECs on `ec_opfamilies` (+ `em_jdomain` for constants), merges or
`add_eq_member`. `generate_base_implied_equalities`: const EC → each
non-const member gets `member = const` as a **base restriction**;
const-less → same-rel pairs get `m1 = m2`; sets `eclass_indexes`.
`generate_join_implied_equalities`: per joinrel/parameterization, one
clause per outer/inner pairing (cached `ec_derives_list/hash`; broken
ECs fall back to `ec_sources`). `EquivalenceClass`: `ec_opfamilies`,
`ec_collation`, `ec_members/ec_childmembers`, `ec_sources`,
`ec_derives_*`, `ec_relids`, `ec_has_const/volatile/broken`,
`ec_sortref`, `ec_min/max_security`, `ec_merged`.

### 4.4 Lateral, FKs, ORs, pathkeys callback

`create_lateral_join_info` → transitive `lateral_relids` (rel only
produces paths parameterized by ≥ those). `match_foreign_keys_to_quals`
→ `ForeignKeyOptInfo(con_relid, ref_relid, nkeys, conkey/confkey/
conpfeqop, nmatched_ec, nconst_ec, nmatched_rcols/ri, eclass[],
rinfos[])`. `standard_qp_callback` (`plan/planner.c`, line 3453):
`query_pathkeys = group ?: window ?: longer(distinct, sort) ?: setop ?:
NIL` — what scan/join paths try to satisfy.

---

## 5. `make_one_rel()` and base-rel paths (`path/allpaths.c`, line 171, verified)

```
set_base_rel_consider_startup   # single-rel SEMI/ANTI RHS → consider_param_startup
set_base_rel_sizes              # set_rel_consider_parallel + set_rel_size per baserel
total_table_pages = Σ non-dummy pages
set_base_rel_pathlists          # set_rel_pathlist + gather + set_cheapest per baserel
make_rel_from_joinlist(root, joinlist)   # §6
```

`consider_startup = (tuple_fraction > 0)` (`build_simple_rel` /
`build_join_rel`). `set_rel_size`: constraint-exclusion/partition dummy
→ `set_dummy_rel_pathlist`; `rte->inh` → `set_append_rel_size`; foreign
→ FDW `GetForeignRelSize`; plain → `set_plain_rel_size` =
`check_index_predicates` + `set_baserel_size_estimates`
(`rows = clamp_row_est(tuples × clauselist_selectivity
(baserestrictinfo, 0, JOIN_INNER))`, `baserestrictcost`,
`set_rel_width` from `stawidth`/`get_typavgwidth`); SUBQUERY →
`set_subquery_pathlist`; CTE → `set_cte/worktable_pathlist`.

`rel->tuples/pages/allvisfrac` from `get_relation_info`
(`util/plancat.c:estimate_rel_size`, line 1075, verified): `curpages` =
live blocks (`<10 && reltuples<0 && !relhassubclass` → 10; 0 → zeros);
density `reltuples/relpages` else
`(usable×fillfactor/100)/(width+overhead)`; `tuples =
clamp(density×curpages)`; `allvisfrac = relallvisible/curpages`. Also
`rel_parallel_workers`, `indexlist` (`IndexOptInfo`: pages/tuples,
`canreturn[]`, `sortopfamily[]`, AM flags; `predOK` later), `statlist`,
`notnullattnums`.

- **`set_plain_rel_pathlist`**: `required_outer = lateral_relids`; TID
  paths (`path/tidpath.c`; CURRENT OF short-circuits); SeqScan; partial
  SeqScan (`consider_parallel`); `create_index_paths`.
- **`create_index_paths`** (`path/indxpath.c`, line 241, verified): per
  index (skip `!predOK`): restriction→`rclauseset` →
  `build_index_paths(ST_ANYSCAN)` (ordinary `add_path`'d, bitmap ones
  collected); join+EC clauses → **parameterized** index paths per
  `required_outer`; `generate_bitmap_or_paths`; greedy
  `choose_bitmap_and` (line 1786, verified) → `create_bitmap_heap_path`
  (+ partial when safe) + parameterized bitmap heaps per `required_outer`.
- **`build_index_paths`** (line 811, verified): created iff
  `index_clauses || useful_pathkeys || useful_predicate ||
  index_only_scan`; forward + backward; `amcanorderbyop` ordering;
  partial when `amcanparallel && consider_parallel`. `check_index_only`:
  `enable_indexonlyscan`, target + `indrestrictinfo` ⊆ `canreturn`.
  Parameterization: `get_baserel_parampathinfo` (`util/relnode.c`):
  movable join clauses + join implied equalities, `ppi_rows =
  get_parameterized_baserel_size`, cached in `ppilist`.
- **Others**: `set_subquery_pathlist` (safe qual pushdown,
  `remove_unused_subquery_outputs`, parent fraction only without upper
  processing above, `convert_subquery_pathkeys`, partials);
  `set_append_rel_pathlist` → `add_paths_to_append_rel`: cheapest-total
  Append, cheapest-startup Append (`consider_startup`),
  MergeAppend/ordered Append, partial Append (max child workers,
  `enable_parallel_append` adds `Max(…, fls(#children))` capped by
  `max_parallel_workers_per_gather`), mixed Parallel Append,
  parameterized Appends; partitionwise join reuses this.

---

## 6. Join search

`make_rel_from_joinlist`: single item returned; else GEQO
(`enable_geqo && levels_needed >= geqo_threshold`,
`geqo/geqo_main.c`: `geqo_seed/effort/pool_size/generations`) or
`standard_join_search` (line 3457, verified): level-by-level DP;
per level `join_search_one_level` then partitionwise join, gather
(unless whole query), `set_cheapest`.

`join_search_one_level` (`path/joinrels.c`, line 73, verified):
clause-driven left-deep (`make_rels_by_clause_joins`, level 2 deduped)
or cartesian (`make_rels_by_clauseless_joins`); bushy pairs
`k = 2..level/2` with relevant clause or order restriction
(`have_relevant_joinclause` in `util/joininfo.c`;
`have_join_order_restriction`: lateral/PHV/split-`min_*` unless
`has_legal_joinclause`); clauseless last resort.

`make_join_rel` (line 696, verified): `join_is_legal` (unique `sjinfo`
with `min_lefthand ⊆ rel1 && min_righthand ⊆ rel2` or reversed; no
split `min_*`; SEMI unique-ification; no two-way lateral). Then
`build_join_rel` (`util/relnode.c`, joinrel cache): `reltarget` from
`attr_needed` (+ `varnullingrels`); `restrictlist =
build_joinrel_restrictlist` (applicable joininfo + join implied
equalities); `consider_parallel` from inputs + clause/target safety;
`rows` via `calc_joinrel_size_estimate` (`path/costsize.c`): INNER
`o·i·fk·j`; LEFT `max(o·i·fk·j,o)·p`; FULL `max(…,o,i)·p`; SEMI
`o·fk·j`; ANTI `o·(1−fk·j)·p`; clamped.

`populate_joinrel_with_paths` arms: INNER both orders; LEFT +
(RIGHT-flipped); FULL both (error if pathless — needs merge/hashable
clause); SEMI + PG17 `(rel2,rel1,RIGHT_SEMI)` + `JOIN_UNIQUE_INNER/
OUTER` when `create_unique_path` (`util/pathnode.c`, line 1730,
verified) succeeds (hash veto clears `semi_can_hash`, may return NULL,
when `hashentrysize×rows > get_hash_memory_limit()`); ANTI +
RIGHT_ANTI. Dummy propagation + constant-false short-circuit, then
`try_partitionwise_join`.

---

## 7. `add_paths_to_joinrel` (`path/joinpath.c`, line 124, verified)

`inner_unique`: SEMI/ANTI → false; UNIQUE_INNER → `min_lefthand ⊆
outer`; UNIQUE_OUTER → `innerrel_is_unique(INNER)`; else
`innerrel_is_unique(jointype)`. `mergeclause_list =
select_mergejoin_clauses` (if `enable_mergejoin` or FULL). SEMI/ANTI/
unique → `compute_semi_anti_join_factors`. `param_source_rels` =
OJ-constrained outers ∪ `lateral_relids` (`try_nestloop_path` rejects
other `required_outer` unless `allow_star_schema_join`).

- **`sort_inner_and_outer`**: cheapest totals (no cross-parameterized),
  unique-ified for UNIQUE_*; orderings from
  `select_outer_pathkeys_for_merge` (favours `query_pathkeys`); explicit
  sorts both sides; partial merge via `try_partial_mergejoin_path`
  (not UNIQUE_OUTER/FULL/RIGHT/RIGHT_ANTI).
- **`match_unsorted_outer`**: `nestjoinOK` false for RIGHT/RIGHT_ANTI/
  FULL (RIGHT_SEMI skipped); nestloop with cheapest total inner, every
  `cheapest_parameterized_paths` inner, `get_memoize_path` over each
  (line 675, verified: `enable_memoize`, outer rows ≥ 2,
  parameterized/lateral inner, no SEMI/ANTI-without-unique, no volatile,
  hashable equality per parameter), Material over inner if
  `enable_material` and not materializing; `generate_mergejoin_paths`
  (existing outer order, sorted/unsorted inner, truncated clause lists
  on prefix order); parallel nestloop/merge when `consider_parallel`
  (exclusions as above + RIGHT_SEMI).
- **`hash_inner_and_outer`**: `hashjoinoperator` clauses matching the
  split (no pushed-down OJ quals); `(total,total)`, `(startup,total)`,
  all parameterized pairs; partial hash with non-partial inner or
  partial inner + `enable_parallel_hash` (shared table).
- **Two-stage costing**: `initial_cost_*` bounds → `add_path_precheck`
  → `create_*_path` runs `final_cost_*` (qual costs, semifactors,
  `estimate_hash_bucket_stats`, `mergejoinscansel`, `cost_rescan`) →
  `add_path`. Nestloop `required_outer =
  calc_nestloop_required_outer`; params containing the join's own
  `ojrelid` vetoed.

---

## 8. Path model: `Path` / `RelOptInfo` / `add_path` / `set_cheapest`

**`Path`** (`pathnodes.h`): `pathtype` (Plan NodeTag; `IndexPath` =
index + index-only); `parent`; `pathtarget`
(`PathTarget{exprs, sortgrouprefs, cost, width}`, default
`parent->reltarget`); `param_info`
(`ParamPathInfo{ppi_req_outer, ppi_rows, ppi_clauses, ppi_serials}`);
`parallel_aware/safe/workers`; `rows` (parent rows/`ppi_rows`; joins/
upper compute their own); **`disabled_nodes`** (PG18, verified
`pathnodes.h:1795` — compared before cost); `startup/total_cost`;
`pathkeys` (NIL = unordered). Subtypes: all `*Path` nodes incl. `JoinPath
(NestPath, MergePath, HashPath)` + `jointype/inner_unique/outerjoinpath/
innerjoinpath/joinrestrictinfo`, and plain-`Path` scans (Seq, Sample,
Function, Values, TableFunc, Cte, WorkTable, NamedTuplestore, Result).

**`RelOptInfo`** (planner-read fields): `reloptkind`
(BASEREL/JOINREL/OTHER_MEMBER/OTHER_JOIN/UPPER/OTHER_UPPER), `relids`,
`rows`, `consider_startup/param_startup/parallel`, `reltarget`,
`pathlist/ppilist/partial_pathlist`,
`cheapest_startup/total/unique_path`, `cheapest_parameterized_paths`,
`lateral_relids`, `relid`, `attr_needed[]/attr_widths[]`,
`notnullattnums`, `nulling_relids`, `indexlist/statlist`,
`pages/tuples/allvisfrac`, `eclass_indexes`, `subroot`,
`rel_parallel_workers`, `baserestrictinfo/cost`,
`joininfo/has_eclass_joins`, `unique/non_unique_for_rels`
(`innerrel_is_unique` cache), partition fields.

**`add_path`** (`util/pathnode.c`, line 464, verified) via
`compare_path_costs_fuzzily` (line 185, `STD_FUZZ_FACTOR = 1.01`):
`disabled_nodes` first; totals within 1%; startups (only under
`consider_startup/param_startup`); `compare_pathkeys` (parameterized =
NIL); `COSTS_EQUAL` + equal keys → `bms_subset_compare(required_outer)`:
fewer outers + `rows ≤` + `parallel_safe ≥` removes old; exact tie →
`parallel_safe`, fewer `rows`, fuzz-1e-10 re-compare, else old wins.
List ordered by `(disabled_nodes, total_cost)`; `add_path_precheck`
stops at the first worse-or-equal old path. **`add_partial_path`**:
total + `disabled_nodes` + pathkeys only (requires `parallel_safe` +
`consider_parallel`). **`set_cheapest`** (line 272, verified): cheapest
total/startup among unparameterized (pathkeys tie-break);
`cheapest_parameterized_paths` = cheapest total + all parameterized;
parameterized-only rels → smallest `required_outer`
(`cheapest_startup_path = NULL`).

---

## 9. Pathkeys and ordering (`path/pathkeys.c`)

`PathKey{pk_eclass, pk_opfamily, pk_cmptype (ASC/DESC), pk_nulls_first}`,
interned by `make_canonical_pathkey` (pointer-compared); redundant
(const-EC, duplicate) keys dropped. `build_index_pathkeys`: per key
column from `sortopfamily/reverse_sort/nulls_first` (flipped backward);
stops at first non-EC column. `truncate_useless_pathkeys`: longest
prefix useful for merging / ordering (prefix of `query_pathkeys`) /
grouping (any subset) / distinct / set-op. `has_useful_pathkeys`:
`joininfo || has_eclass_joins || group_pathkeys || query_pathkeys`.
`pathkeys_contained_in` (prefix), `compare_pathkeys`
(EQUAL/BETTER1/BETTER2/DIFFERENT), `pathkeys_count_contained_in`
(prefix length = incremental-sort input). `build_join_pathkeys`:
nestloop/mergejoin keep truncated outer order; hash = NIL; merge output
= outer. Incremental sort on any presorted prefix
(`enable_incremental_sort`) in ordered/gather/grouping/window/distinct
stages (mergejoin sorts: full sort only). PG16 group reordering
(`get_useful_group_keys_orderings`, `enable_group_by_reordering`, no
grouping sets) and PG18 distinct reordering
(`get_useful_pathkeys_for_distinct`, `enable_distinct_reordering`)
match key permutations to input order.

---

## 10. Parallel query in the planner

`set_rel_consider_parallel` (`path/allpaths.c`): false for temp tables,
unsafe tablesample, non-parallel-safe FDWs, LIMIT subqueries, unsafe
function/VALUES exprs, TABLEFUNC, CTE, NAMEDTUPLESTORE; needs
`is_parallel_safe(baserestrictinfo)` + target. Joins inherit; upper
rels derive from input + target. `create_plain_partial_paths`:
`compute_parallel_worker(rel, pages, -1, max_parallel_workers_per_gather)`
(line 4274, verified) → partial SeqScan (`parallel_aware`).
`compute_parallel_worker`: reloption overrides; 0 below
`min_parallel_table_scan_size` (1024 pages = 8 MB) /
`min_parallel_index_scan_size` (64 pages); else
`1 + floor(log3(pages/threshold))` per side, min, capped. Partial index
(`amcanparallel`, unparameterized) and partial bitmap heap scans
likewise. `generate_gather_paths` (line 3099, verified): Gather over
cheapest partial (`rows = compute_gather_rows = sub-rows × divisor`);
GatherMerge over every ordered partial;
`generate_useful_gather_paths` adds GatherMerge over useful orderings
(full Sort only on cheapest partial, IncrementalSort on presorted ones)
— called from `set_rel_pathlist`, `standard_join_search`,
`apply_scanjoin_target_to_paths`, grouping stages. Parallel joins (§7):
partial-outer nestloop/merge; partial hash with non-partial or shared
(`enable_parallel_hash`) inner. Partial agg:
`UPPERREL_PARTIAL_GROUP_AGG` (`AGGSPLIT_INITIAL_SERIAL` /
`FINAL_DESERIAL`) over Gather/Gather Merge. Projections clear
`parallel_safe` / drop partials when the target is unsafe;
`parallelModeNeeded` set by Gather creation.

---

## 11. `create_plan` and `set_plan_references`

`create_plan(root, best_path)` (`plan/createplan.c`, line 337,
verified): `create_plan_recurse(..., CP_EXACT_TLIST)`; top tlist forced
to `processed_tlist`; `SS_attach_initplans`. Dispatch: scans →
`create_scan_plan` (per-kind); joins → `create_join_plan`;
Append/MergeAppend; Result variants (projection, minmaxagg,
group-result, RTE_RESULT); ProjectSet; Material/Memoize; Unique/
upper-Unique; Gather/GatherMerge; Sort/IncrementalSort;
Group/Agg/GroupingSets; WindowAgg/SetOp/RecursiveUnion/LockRows/
ModifyTable/Limit. `CP_EXACT_TLIST` / `CP_SMALL_TLIST` (under
Sort/Material/Memoize/Gather) / `CP_LABEL_TLIST` (Agg/Group/Sort/
Unique/WindowAgg) / `CP_IGNORE_TLIST`. `use_physical_tlist`:
whole-tuple scan only for RELATION/SUBQUERY/FUNCTION/TABLEFUNC/VALUES/
CTE base rels, no system cols/PHVs-here, index-only `canreturn`-covered,
labelled only for distinct plain Vars. Quals: `order_qual_clauses`
(cost, security); pseudoconstants → gating `Result`; parameterized
scans → `replace_nestloop_params`.

Index plan: `fix_indexqual_references` (index side → `INDEX_VAR`,
commuted ops, `indexqualorig` kept), redundant quals dropped
(`is_redundant_with_indexclauses`); bitmap via `create_bitmap_subplan`
(recheck quals, shared marking). Nestloop: outer first,
`curOuterRels ∪= outer`, inner Vars/PHVs → `PARAM_EXEC`
(`assign_nestloop_param_var`) + `NestLoopParam` list
(`identify_current_nestloop_params`); quals split
join/other-clauses. Merge: explicit Sorts from
`outersortkeys/innersortkeys`, inner Material when
`materialize_inner`, per-clause families/collations/reversals
(`skip_mark_restore` if `inner_unique`). Hash: `CP_SMALL_TLIST` outer
when inner not cheapest; **skew** (single clause, plain-Var base-rel
outer side → `skewTable/skewColumn/skewInherit` for
`ExecHashBuildSkewHash`); parallel Hash `rows_total =
inner_rows_total`. Sort: `prepare_sort_from_pathkeys` + `make_sort`
(`disabled_nodes = lefttree + !enable_sort`); bounded sort is no node
property — LIMIT bounds execution (`ExecSetTupleBound`), costing only
via `cost_sort(..., limit_tuples)`. `copy_generic_path_info` copies
costs/rows/width/`parallel_safe`/`disabled_nodes` path → plan.

**`set_plan_references`** (`plan/setrefs.c`): flatten rtable into
`glob->finalrtable` (`rtoffset`), then recurse: scans (`scanrelid +=
rtoffset`, `fix_scan_expr`); joins (OUTER/INNER_VAR via indexed tlists,
`set_hash_references`); upper nodes (`fix_upper_expr`, dummy tlists);
IndexOnlyScan (INDEX_VAR); Append offsets; trivial-SubqueryScan removal;
AlternativeSubPlan by cost; `plan_node_id = lastPlanNodeId++`;
ROWID_VAR resolution; `relationOids` collection.

---

## 12. Planner GUCs (defaults verified in `guc_tables.c` / `cost.h`)

`enable_*` (true unless noted): seqscan, indexscan, indexonlyscan,
bitmapscan, tidscan, sort, incremental_sort, hashagg, material, memoize,
nestloop, mergejoin, hashjoin, gathermerge, parallel_append,
parallel_hash, partition_pruning, presorted_aggregate, async_append,
self_join_elimination (PG18, `guc_tables.c:1019`), group_by_reordering,
distinct_reordering (PG18); false: partitionwise_join,
partitionwise_aggregate.

| GUC | default | consumer |
|---|---|---|
| `from_collapse_limit` / `join_collapse_limit` | 8 / 8 (1 = keep order) | FromExpr/JoinExpr merge (`initsplan.c`) |
| `geqo[_threshold/_effort/_pool_size/_generations/_selection_bias/_seed]` | on / 12 / 5 / 0 / 0 / 2.0 / 0.0 | `make_rel_from_joinlist`, `geqo/*` |
| `max_parallel_workers_per_gather` | 2 | parallelModeOK, worker cap |
| `min_parallel_table_scan_size` / `min_parallel_index_scan_size` | 8 MB / 512 kB | `compute_parallel_worker` |
| `parallel_leader_participation` | on | `get_parallel_divisor` |
| `parallel_setup_cost` / `parallel_tuple_cost` | 1000 / 0.1 | Gather costing |
| `seq_page_cost` / `random_page_cost` | 1.0 / 4.0 | scan/sort costing |
| `cpu_tuple_cost` / `cpu_index_tuple_cost` / `cpu_operator_cost` | 0.01 / 0.005 / 0.0025 | all cost functions |
| `effective_cache_size` | 524288 pages (4 GB) | Mackert–Lohman |
| `work_mem` / `hash_mem_multiplier` | 4096 kB / 2.0 | sort/agg/hash memory |
| `cursor_tuple_fraction` | 0.1 | `standard_planner` |
| `constraint_exclusion` | partition | `relation_excluded_by_constraints` |
| `recursive_worktable_factor` | 10.0 | CTE self-ref size |
| `plan_cache_mode` | auto | generic-vs-custom (outside planner) |
| `jit[_above_cost 100000/_optimize 500000/_inline 500000]` | | jitFlags |
| `debug_parallel_query` | off | forced Gather wrapper |

**PG18 `disabled_nodes`** (commit `e2225346794`; `pathnodes.h:1795`):
no `disable_cost` addition — +1 per disabled node propagated through
children, ordered before cost in every comparison (`add_path`,
`add_path_precheck`, `add_partial_path`, `compare_path_costs[_fuzzily]`,
`compare_fractional_path_costs`). Sole surviving `disable_cost = 1e10`:
`final_cost_hashjoin` (inner MCV bucket over memory). Hard gates (no
path): `enable_indexonlyscan`, `enable_tidscan` (except CURRENT OF),
`enable_memoize`, `enable_incremental_sort`; `create_unique_path` hash
veto returns NULL instead of counting.

---

## 13. Reimplementation checklist

**Entry / global**
1. `tuple_fraction` from cursor options (`cursor_tuple_fraction`; ≥1→0, ≤0→1e-10), else 0. (`planner.c:standard_planner`)
2. `parallelModeOK`: SELECT + no modifying CTE + workers > 0 + `CURSOR_OPT_PARALLEL_OK` + no PROPARALLEL_UNSAFE fn.
3. `get_cheapest_fractional_path`: fraction ≥1 ÷ cheapest-total rows; parameterized excluded; `disabled_nodes` first. (`planner.c:6617`)
4. Scroll cursor + non-backward plan → top Material; JIT flags from `total_cost` thresholds.
5. Subplan `SS_finalize_plan`, `set_plan_references`, `PlannedStmt` assembly incl. `parallelModeNeeded`.

**subquery_planner order**
6. Order: CTEs, MERGE→join, empty-jointree Result, sublink pull-up, function inlining, PG18 virtual generated cols, subquery pull-up, UNION ALL flatten, row marks, expression preprocessing, HAVING→WHERE, `reduce_outer_joins`, `remove_useless_result_rtes`, `grouping_planner`. (`planner.c:651`)
7. CTE inline rule (flag/refcount/recursion/DML/outer-self-ref/volatility); else once-planned `CTE_SUBLINK` initplan, fraction 0. (`subselect.c`)
8. ANY→SEMI (upper vars ⊆ available, no volatile); EXISTS→SEMI/ANTI after `simplify_EXISTS_query`; hashed ANY + EXISTS→ANY rewrite. (`subselect.c`)
9. `is_simple_subquery` pull-up conditions; UNION ALL/VALUES/const-function cases; PHV wrapping. (`prepjointree.c`)
10. `preprocess_expression` stage order (aliases → const-fold → canonicalize → hashed SAOP → sublinks → correlation → implicit AND).
11. HAVING→WHERE move; RIGHT→LEFT + outer→inner reduction; ungrouped MIN/MAX→`MinMaxAggPath` iff cheaper.

**grouping_planner**
12. `preprocess_limit`: unknown LIMIT/OFFSET → 10% fraction; `limit_tuples` only when both known.
13. Upper order GROUP_AGG → WINDOW → DISTINCT → ORDERED → FINAL (LockRows → Limit → ModifyTable).
14. PathTargets (group/window/sort inputs, SRF postponement, `apply_scanjoin_target_to_paths` + gather regen).
15. Grouping flags (sortable/hashable/partial-agg); sorted over every input × useful orderings; hashed on cheapest; spill costed not vetoed. (`planner.c:3780`, `costsize.c`)
16. DISTINCT: sorted Unique over all inputs (+PG18 reordering), HashAggregate unless DISTINCT ON/`!enable_hashagg`; `estimate_num_groups` rows.
17. ORDER BY: full sort on cheapest total only; incremental on presorted prefixes; `limit_tuples` bounds costing; Gather Merge over sorted partials.

**query_planner**
18. `RTE_RESULT` fast path (`GroupResultPath`); full step order with post-callback removals → PG18 self-join elimination. (`planmain.c`, `analyzejoins.c:2488`)
19. `attr_needed[attno]` drives join tlists + `use_physical_tlist`. (`initsplan.c`)
20. Collapse limits (FULL never merged); `SpecialJoinInfo` via `make_outerjoininfo`; PG16 OJ relids + `varnullingrels` + clones.
21. `distribute_qual_to_rels` (pseudoconstants, pushdown, EC absorption, merge/hash/memoize marks); EC rules (mergejoinable equality, volatile isolation, const-by-joindomain, base/join implied equalities, broken fallback). (`initsplan.c`, `equivclass.c`)
22. Join-removal conditions; FK matching; OR extraction; `query_pathkeys` preference group → window → longer(distinct, sort) → setop. (`analyzejoins.c`, `planner.c:3453`)

**Base rels & scans**
23. `consider_startup = fraction > 0`; `consider_param_startup` only for single-rel SEMI/ANTI RHS.
24. `estimate_rel_size`: live pages, 10-page floor, density vs fillfactor/width formula, unscaled `allvisfrac`. (`plancat.c: estimate_rel_size`; heap impl `tableam.c: table_block_relation_estimate_size`)
25. `rows = clamp(tuples × clauselist_selectivity(baserestrictinfo))`. (`costsize.c`)
26. Plain-rel order: TID (CURRENT OF short-circuits) → SeqScan → partial SeqScan → index paths. (`allpaths.c`)
27. `create_index_paths` phases (restriction/bitmap, parameterized per outer set, BitmapOr, greedy AND, parameterized bitmap heaps); `build_index_paths` creation gate + both directions + partial when `amcanparallel`. (`indxpath.c:241,811,1786`)
28. `check_index_only` (`enable_indexonlyscan`, `canreturn`); `predOK` via `check_index_predicates`; `ParamPathInfo`/`ppilist` caching.
29. Subquery RTE (pushdown safety, output pruning, fraction rule); Append variants + worker formula; gather after each base rel / join level.

**Join search & paths**
30. Level DP (clause-driven, bushy, clauseless fallback); GEQO at `levels_needed >= geqo_threshold`. (`allpaths.c:3457`, `joinrels.c:73`)
31. `join_is_legal` (`min_*` subsets); `build_join_rel` cache/tlist/restrictlist/parallelism; per-jointype row formulas. (`joinrels.c:696`)
32. `populate_joinrel_with_paths` arms incl. PG17 `RIGHT_SEMI`, UNIQUE_* via `create_unique_path`, FULL error, dummy propagation.
33. `add_paths_to_joinrel`: per-jointype `inner_unique`, `param_source_rels` + `allow_star_schema_join`, sort/match/hash generators, Memoize conditions, two-stage costing + `add_path_precheck`, nestloop `required_outer` + `ojrelid` veto. (`joinpath.c:124,675`)

**Bookkeeping / pathkeys / parallel / create_plan**
34. `add_path` dominance (fuzz 1.01, `disabled_nodes` first, pathkeys, outer-subset, rows, `parallel_safe`, 1e-10 tie-break, ordered insert, old-wins ties); `add_partial_path` (total + pathkeys); `set_cheapest` (+ parameterized fallback). (`pathnode.c:464,272,185`)
35. Interned canonical PathKeys; `truncate_useless_pathkeys`; join keys = truncated outer order, hash = NIL; incremental sort on presorted prefixes; PG16 group / PG18 distinct reordering. (`pathkeys.c`)
36. `compute_parallel_worker` log₃ ladder + size thresholds + reloption override; Gather/GatherMerge generation; partial agg splits; `parallelModeNeeded` from Gather creation. (`allpaths.c:4274,3099`)
37. `create_plan` dispatch + `CP_*` flags + `use_physical_tlist`; ordered quals + gating Result; nestloop PARAM_EXEC; index/bitmap/join/sort plan details; skew columns; `copy_generic_path_info`. (`createplan.c:337`)
38. `set_plan_references` (flat rtable + `rtoffset`, OUTER/INNER/INDEX/ROWID rewriting, trivial-SubqueryScan removal, AlternativeSubPlan choice, `plan_node_id`, dependencies). (`setrefs.c`)
39. All `enable_*` defaults + PG18 comparison-before-cost; `join_collapse_limit = 1` keeps order; GEQO parameters. (`guc_tables.c`)
