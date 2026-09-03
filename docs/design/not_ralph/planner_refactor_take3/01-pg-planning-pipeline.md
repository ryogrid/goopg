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

---

## 14. Worked end-to-end planning walkthrough (illustrative trace — representative numbers, not a measured `EXPLAIN`)

> The numbers below are an illustrative trace with representative (hand-picked)
> sizes and costs derived from the formulas cited in §§1–13. They are not a
> measured `EXPLAIN` and do not describe any real dataset. Only functions
> already cited above are named.

Example query (3-table join, one range predicate, one `GROUP BY`, one `ORDER BY`):

```sql
SELECT c_mktsegment, count(*)
  FROM customer c JOIN orders o ON c_custkey = o_custkey
                 JOIN lineitem l ON o_orderkey = l_orderkey
 WHERE l_shipdate >= DATE '1994-01-01'
 GROUP BY c_mktsegment
 ORDER BY c_mktsegment;
```

### 14.1 `standard_planner` entry (`postgres/src/backend/optimizer/plan/planner.c:standard_planner`)

No cursor options are set, so `tuple_fraction` is `0.0` ("fetch all", §1 step 3).
There is no `LIMIT`/`OFFSET`, so the later `preprocess_limit` call leaves the
fraction at `0.0` and `limit_tuples` is `-1` once grouping is seen (§3 step 3:
`-1` with grouping/distinct/window/setop). `parallelModeOK` is decided by
`max_parallel_hazard` as in §1 step 2; the trace below assumes a small
illustrative sizing where no partial paths survive (see §14.3), so no
`create_gather_path` / `create_gather_merge_path` fires. Final selection uses
`get_cheapest_fractional_path` with fraction `<= 0`, which returns
`cheapest_total_path` directly without running
`compare_fractional_path_costs` (§1 step 4).

### 14.2 `subquery_planner` preprocessing (`plan/planner.c:subquery_planner`)

Each step below is the §2 table row for this query shape (all no-ops state what
was checked, not skipped silently):

- `SS_process_ctes`: no `cteList`, nothing to inline or plan as `CTE_SUBLINK`.
- `transform_MERGE_to_join`: not a `MERGE`, no-op.
- `replace_empty_jointree`: `FROM` is non-empty, no `RTE_RESULT` injected.
- `pull_up_sublinks`: no sublinks, so `convert_ANY_sublink_to_join`,
  `convert_EXISTS_sublink_to_join` (gated by `simplify_EXISTS_query`), and
  `convert_EXISTS_to_ANY` (gated by `subplan_is_hashable` /
  `testexpr_is_hashable`) never trigger.
- `preprocess_function_rtes`: no SQL-function RTEs to inline.
- `expand_virtual_generated_columns`: no virtual generated columns.
- `pull_up_subqueries` / `flatten_simple_union_all`: no `RTE_SUBQUERY` and no
  set-operations, so the `is_simple_subquery` gate and
  `pull_up_simple_union_all` are vacuous here.
- Rtable scan records `hasJoinRTEs = true` (three base RTEs), no lateral, outer,
  or result RTEs.
- `preprocess_rowmarks`: no `FOR UPDATE`/`SHARE`, no-op.
- `preprocess_expression`: `flatten_join_alias_vars` (join aliases exist, so it
  runs), then `eval_const_expressions` folds `DATE '1994-01-01'` to a constant,
  `canonicalize_qual` normalises the range qual, `convert_saop_to_hashed_saop`
  finds no `ScalarArrayOp`, `SS_process_sublinks` finds nothing,
  `SS_replace_correlation_vars` is vacuous at level 1, and
  `make_ands_implicit` leaves the single range qual as an implicit-AND list.
- No `HAVING`, so no `HAVING`-to-`WHERE` move; `reduce_outer_joins` and
  `remove_useless_result_rtes` are no-ops (inner joins only, no `RTE_RESULT`).
- `preprocess_minmax_aggregates` does not apply (`count(*)` is not
  `min`/`max`, and there is a `GROUP BY`).
- Control passes to `grouping_planner` (§14.5), which calls `query_planner`
  first (§14.3); afterwards `SS_identify_outer_params`,
  `SS_charge_for_initplans`, and `set_cheapest` run on the final rel (no
  initplans here, so no parallel-unsafe marking).

### 14.3 `query_planner` and base-rel pathlists (`plan/planmain.c:query_planner`, `path/allpaths.c`)

- `setup_simple_rel_arrays`, then `add_base_rels_to_query` builds three base
  rels via `build_simple_rel` + `get_relation_info` (each gets
  `pages`/`tuples`/`allvisfrac` from `estimate_rel_size`).
- `remove_useless_groupby_columns`: the single `GROUP BY` column is not
  functionally dependent on a same-rel PK here, no-op.
- `build_base_rel_tlists` sets `attr_needed[attno]` (join keys, the range
  column, and the group key stay; wider columns drop out of scan/join tlists).
- `find_placeholders_in_jointree` / `find_lateral_references`: no
  `PlaceHolderVar`s and no lateral refs.
- `deconstruct_jointree` via `deconstruct_recurse`: one `FromExpr` with three
  members merges into a single joinlist because `3 <= from_collapse_limit`
  (`8`); both joins are inner, so `make_outerjoininfo` creates no
  `SpecialJoinInfo`. `distribute_qual_to_rels` puts the range qual on the
  `lineitem` rel as `is_pushed_down = true` `baserestrictinfo` and each
  equijoin clause on the two member rels' `joininfo` (with
  `check_mergejoinable` filling `mergeopfamilies` and `check_hashjoinable`
  filling `hashjoinoperator`). `reconsider_outer_join_clauses` and
  `generate_base_implied_equalities` (via `process_equivalence`) add nothing
  (no outer joins, no constant ECs).
- `standard_qp_callback`: `group_pathkeys = [c_mktsegment]`,
  `sort_pathkeys = [c_mktsegment]`, so `query_pathkeys = group_pathkeys`
  (group beats window/distinct/sort/setop, §4.5). This is the ordering every
  scan/join path below tries to satisfy.
- `fix_placeholder_input_needed_levels`, `remove_useless_joins`,
  `reduce_unique_semijoins`, `remove_useless_self_joins` (guarded by
  `enable_self_join_elimination`), `add_placeholders_to_base_rels`,
  `create_lateral_join_info`, `match_foreign_keys_to_quals` (records the two
  FKs for `calc_joinrel_size_estimate`), `extract_restriction_or_clauses`,
  `add_other_rels_to_query` (no inheritance children), and
  `distribute_row_identity_vars` all run in §4 order; only the FK recording
  changes state here.
- `make_one_rel` runs `set_base_rel_consider_startup` (no `SEMI`/`ANTI`, so no
  `consider_param_startup`), `set_base_rel_sizes` (`set_rel_consider_parallel`
  then `set_rel_size` → `set_plain_rel_size` → `check_index_predicates` +
  `set_baserel_size_estimates`), and `set_base_rel_pathlists`.

Scan paths generated per base rel (`set_plain_rel_pathlist`, §5):

- `required_outer = lateral_relids = NULL` everywhere (no lateral refs).
- TID paths (`path/tidpath.c`): none — the `CURRENT OF` short-circuit
  does not apply, so no TID path is built.
- One SeqScan path per rel.
- No partial SeqScan via `create_plain_partial_paths`: at these illustrative
  sizes `compute_parallel_worker` returns `0` (below
  `min_parallel_table_scan_size`), so `consider_parallel` is false and
  `generate_useful_gather_paths` adds nothing; `set_cheapest` then sees only
  unparameterized paths.
- `create_index_paths` per rel: `build_index_paths` creates a path only when
  `index_clauses || useful_pathkeys || useful_predicate || index-only`
  holds. Concretely: the `lineitem` range column yields a restriction index
  path when a matching btree exists; the equijoin columns yield
  `useful_pathkeys` (prefix of `query_pathkeys` after
  `truncate_useless_pathkeys`) and, where the AM supports it, forward and
  backward variants; `check_index_only` admits an index-only scan only when
  `enable_indexonlyscan` holds and the target plus `indrestrictinfo` are
  covered by `canreturn`. Join/EC-driven parameterized index paths go through
  `get_baserel_parampathinfo` (movable join clauses plus
  `generate_join_implied_equalities`, rows from
  `get_parameterized_baserel_size`, cached in `ppilist`); none is cheapest
  here because every join clause needs a rel not yet joined. Bitmap paths
  (`generate_bitmap_or_paths`, greedy `choose_bitmap_and`) appear only where a
  bitmap-capable index matched; the trace assumes plain btree equijoin/range
  matches, so at least the SeqScan plus the useful-ordering index paths enter
  `add_path`.

Illustrative sizes via `set_baserel_size_estimates`
(`rows = clamp_row_est(tuples * clauselist_selectivity(baserestrictinfo))`,
`set_rel_width` for widths):

- `customer`: `150` tuples, no quals → `150` rows.
- `orders`: `1500` tuples, no quals → `1500` rows.
- `lineitem`: `6000` tuples × range selectivity `0.20` → `1200` rows
  (`6000 * 0.20 = 1200.0`, verified with `python3 -c` before writing).

### 14.4 Join search levels (`path/allpaths.c:standard_join_search`, `path/joinrels.c`, `path/joinpath.c`)

`levels_needed = 3 < geqo_threshold` (`12`), so `standard_join_search` runs,
not GEQO. Level 1 holds the three base rels.

- Level 2 (`join_search_one_level` → `make_rels_by_clause_joins`): pairs form
  only under `have_relevant_joinclause` (or `have_join_order_restriction`,
  absent here). `customer × orders` forms on `c_custkey = o_custkey`;
  `orders × lineitem` forms on `o_orderkey = l_orderkey`. `customer × lineitem`
  has no direct clause, so `make_rels_by_clauseless_joins` does not fire for
  it (clauseless is not the last resort yet — two pairs already formed).
- Level 3: each level-2 rel joins the remaining base rel through a relevant
  clause, so `(customer × orders) × lineitem` and
  `(orders × lineitem) × customer` address the same relid set; the
  `build_join_rel` joinrel cache yields one 3-way joinrel.
  `build_joinrel_restrictlist` collects the applicable `joininfo` clauses plus
  `generate_join_implied_equalities`; `consider_parallel` stays false (inputs
  are non-parallel and the illustrative target adds nothing unsafe, but the
  AND of inputs already gates it).

Row estimates via `calc_joinrel_size_estimate` (`INNER o*i*fk*j`, §6) with
illustrative selectivities (`fkselec = 1.0` after FK matching):

- `customer × orders`: `150 * 1500 * 1.0 * (1/150) = 1500.0`.
- `(customer × orders) × lineitem`: `1500 * 1200 * 1.0 * (1/1500) = 1200.0`.
  (Both products verified with `python3 -c` before writing.)

Join methods per pair (`populate_joinrel_with_paths` → `add_paths_to_joinrel`
with both `JOIN_INNER` orders, §7):

- `sort_inner_and_outer`: cheapest-total inputs, orderings from
  `select_outer_pathkeys_for_merge` favouring `query_pathkeys`, explicit sorts
  on both sides; `try_partial_mergejoin_path` skipped (not parallel).
- `match_unsorted_outer`: nestloop on the cheapest-total inner plus every
  `cheapest_parameterized_paths` inner, `get_memoize_path` where its gate
  holds, a `Material` over the inner when `enable_material` applies, and
  `generate_mergejoin_paths` reusing existing outer order (including truncated
  clause lists on prefix order).
- `hash_inner_and_outer`: `(total,total)`, `(startup,total)`, and all usable
  parameterized pairs; partial hash variants skipped.
- Two-stage costing throughout: `initial_cost_*` bounds →
  `add_path_precheck` → `create_*_path` with `final_cost_*` (join-qual costs,
  `compute_semi_anti_join_factors` is vacuous for inner joins,
  `calc_nestloop_required_outer` for nestloops; the `ojrelid` veto is vacuous
  with no outer joins) → `add_path`.

Concrete dominance triple on the illustrative `customer × orders` joinrel
(`compare_path_costs_fuzzily` with `STD_FUZZ_FACTOR = 1.01`, then
`compare_pathkeys`, §8):

- Hash path `H`: `disabled_nodes = 0`, `startup_cost = 120.0`,
  `total_cost = 850.0`, `pathkeys = NIL`.
- Merge path `M`: `disabled_nodes = 0`, `startup_cost = 200.0`,
  `total_cost = 857.0`, `pathkeys = [c_mktsegment]` (useful for §14.5).
- `850.0 * 1.01 = 858.5 >= 857.0`, so the totals are within fuzz
  (verified with `python3 -c` before writing); with `EQUAL`-ish costs but
  `DIFFERENT` keys, `add_path` keeps **both** paths instead of pruning the
  nominally costlier ordered path. `set_cheapest` then records the cheapest
  total among unparameterized paths (`H`) while `M` survives as the ordered
  alternative the grouping/sort stages need.

### 14.5 `grouping_planner` upper stages (`plan/planner.c:grouping_planner`, `create_grouping_paths`, `create_ordered_paths`)

- `preprocess_limit`: no `LIMIT`, fraction stays `0.0`.
- `plan_set_operations`: no set-ops.
- `preprocess_grouping_sets`, `preprocess_targetlist`, `preprocess_aggrefs`,
  `select_active_windows` run; `limit_tuples` is `-1` because grouping is
  present (§3 step 3).
- PathTargets (§3 step 4): `final_target` via `create_pathtarget`;
  `sort_input_target` via `make_sort_input_target` (no expensive/volatile/SRF
  columns to postpone here); `grouping_target` via
  `make_window_input_target`; `scanjoin_target` via
  `make_group_input_target` (join columns plus group key and `count(*)` arg);
  `split_pathtarget_at_srfs` finds no SRFs, so `adjust_paths_for_srfs` adds no
  `ProjectSet`; `apply_scanjoin_target_to_paths` re-projects scan/join paths
  (in place where tlists already match), finds the target parallel-safe but
  there are no partials to drop, re-runs `generate_useful_gather_paths`
  (no-op), and calls `set_cheapest`.
- `create_grouping_paths`: `CAN_USE_SORT` holds
  (`grouping_is_sortable` on `[c_mktsegment]`);
  `CAN_USE_HASH` holds (non-empty group clause, no ordered aggs,
  `grouping_is_hashable`); `CAN_PARTIAL_AGG` is computed via
  `can_partial_agg` (no grouping sets, only the partial-safe `count(*)`) but
  the partial rel plus `gather_grouping_paths` path is not the winner at these
  non-parallel sizes. Sorted aggregation tries every input path × each
  `get_useful_group_keys_orderings` ordering: the `M`-derived input already
  carries `[c_mktsegment]`, so it takes `AGG_SORTED` via `create_agg_path`
  (or `create_group_path` shape when no aggregates remained) with no new sort;
  the `H`-derived input gets a Sort (or `create_incremental_sort_path` on a
  presorted prefix when `enable_incremental_sort` applies). Hashed aggregation
  builds `AGG_HASHED` via `create_agg_path` on the cheapest input only. Memory
  overflow is costed by `cost_agg`, never vetoed; the only hash penalty here
  would be `disabled_nodes++` under `!enable_hashagg`, which is enabled, so
  both sort and hash contenders reach `add_path` on `UPPERREL_GROUP_AGG`.
  `estimate_num_groups` sizes the illustrative group count (assume a handful
  of `c_mktsegment` values — the exact figure feeds costing, not gating).
- `create_window_paths` / `create_distinct_paths`: no windows, no `DISTINCT`.
- `create_ordered_paths`: `sort_pathkeys = [c_mktsegment]`;
  `pathkeys_count_contained_in` shows the grouped output already satisfies the
  full prefix, so no new Sort/IncrementalSort is added on the winning path.
  Had it not, only the cheapest total path would get a full sort, presorted
  prefixes would use `create_incremental_sort_path` under
  `enable_incremental_sort`, costing would be bounded by `limit_tuples`
  (here `-1`, unbounded), and sorted partials would feed a Gather Merge.
- `UPPERREL_FINAL` wraps the surviving paths (`create_lockrows_path` /
  `create_limit_path` / `create_modifytable_path` all vacuous for plain
  `SELECT`).

### 14.6 `create_plan` and `set_plan_references` (`plan/createplan.c:create_plan`, `plan/setrefs.c:set_plan_references`)

- `get_cheapest_fractional_path` returns the cheapest-total final path
  (fraction `0.0`); `create_plan` runs `create_plan_recurse` with
  `CP_EXACT_TLIST` (top tlist forced to `processed_tlist`) and
  `SS_attach_initplans` (none).
- Scans via `create_scan_plan` (`fix_indexqual_references` turns index-side
  clauses into `INDEX_VAR` with `indexqualorig` kept,
  `is_redundant_with_indexclauses` drops covered quals, bitmaps via
  `create_bitmap_subplan` where present); the join via `create_join_plan`
  (nestloop inner gets `replace_nestloop_params` →
  `assign_nestloop_param_var` + `identify_current_nestloop_params` only for
  the nestloop winner; merge winner gets explicit sorts from
  `prepare_sort_from_pathkeys` + `make_sort` and inner `Material` when
  `materialize_inner`; hash winner records `skewTable`/`skewColumn`/
  `skewInherit` only for a single-clause plain-`Var` base-rel outer side). Quals are laid out by
  `order_qual_clauses`; there are no pseudoconstant quals needing a gating
  `Result`. `copy_generic_path_info` copies costs, rows, width,
  `parallel_safe`, and `disabled_nodes` onto each plan node.
- No `CURSOR_OPT_SCROLL` wrapper (`ExecSupportsBackwardScan` unchecked),
  no `debug_parallel_query` Gather, no `SS_finalize_plan` (no
  `paramExecTypes`). `set_plan_references` flattens the rtable into
  `finalrtable` with `rtoffset`, rewrites scans (`fix_scan_expr`), joins
  (`set_hash_references`), and upper nodes (`fix_upper_expr`), drops any
  trivial `SubqueryScan`, and assigns `plan_node_id`s before the `PlannedStmt`
  (with `jitFlags` from the `total_cost` thresholds) is built.

## 15. Extended checklist appendix (take2 substance restored; items 40–69 continue §13)

Items 1–39 in §13 are unchanged. Items 40–69 below restore take2 detail that
the tight rewrite compressed, grouped by the requested topics. All paths are
relative to `postgres/`; function names are the ones already cited in §§1–13.

**`Path` / `RelOptInfo` struct fields**

40. `Path` core fields: `pathtype` (builds the `Plan` `NodeTag`;
    `IndexPath` covers index and index-only), `parent`, `pathtarget`
    (`exprs`/`sortgrouprefs`/`cost`/`width`, default `parent->reltarget`),
    `param_info` (`ppi_req_outer`/`ppi_rows`/`ppi_clauses`/`ppi_serials`),
    `parallel_aware`/`parallel_safe`/`parallel_workers`, `rows`
    (`parent->rows` or `ppi_rows` except joins/upper which compute their own),
    `disabled_nodes` (PG18, compared before cost), `startup_cost`/`total_cost`,
    `pathkeys` (`NIL` = unordered); copied to plans by
    `copy_generic_path_info`. (`nodes/pathnodes.h`, `plan/createplan.c`)
41. `JoinPath` / `IndexPath` extras: `JoinPath` adds `jointype`,
    `inner_unique`, `outerjoinpath`, `innerjoinpath`, `joinrestrictinfo`
    (covers `NestPath`/`MergePath`/`HashPath`); `IndexPath` adds `indexinfo`,
    `indexclauses`, `indexorderbys`, `indexorderbycols`, `indexscandir`,
    `indextotalcost`, `indexselectivity`; plain `Path` serves Seq/Sample/
    Function/Values/TableFunc/Cte/WorkTable/NamedTuplestore/Result scans.
    (`nodes/pathnodes.h`, `plan/createplan.c:fix_indexqual_references`)
42. `RelOptInfo` planner-read fields: `reloptkind` (`BASEREL`/`JOINREL`/
    `OTHER_MEMBER`/`OTHER_JOIN`/`UPPER`/`OTHER_UPPER`), `relids`, `rows`,
    `consider_startup`/`consider_param_startup`/`consider_parallel`,
    `reltarget`, `pathlist`/`ppilist`/`partial_pathlist`,
    `cheapest_startup_path`/`cheapest_total_path`/`cheapest_unique_path`
    (lazy)/`cheapest_parameterized_paths`,
    `lateral_relids`, `relid`, `attr_needed[]`/`attr_widths[]`,
    `notnullattnums`, `nulling_relids`, `lateral_vars`/`lateral_referencers`,
    `indexlist`/`statlist`, `pages`/`tuples`/`allvisfrac`, `eclass_indexes`,
    `subroot`, `rel_parallel_workers`, `baserestrictinfo`/`baserestrictcost`,
    `joininfo`/`has_eclass_joins`, `unique`/`non_unique_for_rels`
    (`innerrel_is_unique` cache), partition fields; `fkey_list` lives on
    `PlannerInfo`. (`nodes/pathnodes.h`, `util/relnode.c:build_join_rel`)
43. `RestrictInfo` / `EquivalenceClass` planner fields: `clause`,
    `is_pushed_down`, `can_join`, `pseudoconstant`,
    `has_clone`/`is_clone`, `leakproof`/`has_volatile`/`security_level`,
    `clause_relids`, `required_relids` (`ojscope` for OJ quals),
    `incompatible_relids`, `outer_relids`, `left_relids`/`right_relids`,
    `orclause`, `rinfo_serial` (shared by clones, feeds `ppi_serials`),
    `parent_ec`, `eval_cost`, `norm_selec`/`outer_selec` (`-1` unset),
    `mergeopfamilies`, `left_ec`/`right_ec`/`left_em`/`right_em`,
    `scansel_cache`, `hashjoinoperator`, bucket/`mcvfreq`/`hasheqoperator`
    pairs; `EquivalenceClass` keeps `ec_opfamilies`, `ec_collation`,
    `ec_members`/`ec_childmembers`, `ec_sources`, `ec_derives_list`/`hash`,
    `ec_relids`, `ec_has_const`/`volatile`/`broken`, `ec_sortref`,
    `ec_min_security`/`ec_max_security`, `ec_merged`.
    (`nodes/pathnodes.h`, `path/equivclass.c:process_equivalence`)

**`consider_parallel` gating and parallel-safety checks**

44. `set_rel_consider_parallel` gate: runs only when `parallelModeOK`; false
    for temp tables, unsafe tablesample methods/args, FDWs without parallel
    safety, `LIMIT` subqueries (`limit_needed`), unsafe function/`VALUES`
    exprs, `TABLEFUNC`, `CTE`, `NAMEDTUPLESTORE`; needs
    `is_parallel_safe` on `baserestrictinfo` and the target; joinrels inherit
    from both inputs plus restrictlist/target safety; upper rels derive from
    input plus target. (`path/allpaths.c:set_rel_consider_parallel`,
    `util/relnode.c:build_join_rel`)
45. Projection / initplan parallel clearing:
    `apply_scanjoin_target_to_paths` drops `partial_pathlist` and clears
    `consider_parallel` when the scan/join target is not parallel-safe, then
    re-runs `generate_useful_gather_paths` and `set_cheapest`;
    `SS_charge_for_initplans` adds initplan cost to every `FINAL` path and
    marks them non-parallel-safe when any initplan is;
    `final_rel->consider_parallel` additionally needs parallel-safe `LIMIT`
    expressions. (`plan/planner.c`, `plan/subselect.c`)
46. `compute_parallel_worker` ladder and Gather generation: `parallel_workers`
    reloption overrides; `0` below `min_parallel_table_scan_size` (`1024`
    pages = 8 MB) / `min_parallel_index_scan_size` (`64` pages); else
    `1 + floor(log3(pages / threshold))` per side, min across sides, capped by
    `max_parallel_workers_per_gather`; `Gather` over the cheapest partial
    (`compute_gather_rows`), `GatherMerge` over every ordered partial, plus
    useful-ordering Gather Merges (full Sort only on the cheapest partial,
    IncrementalSort on presorted ones); `parallelModeNeeded` is set by
    `create_gather_path` / `create_gather_merge_path`. Illustrative:
    `5000 / 1024 ~= 4.88`, `log3 ~= 1.44`, workers `= 1 + 1 = 2` (verified
    with `python3 -c` before writing).
    (`path/allpaths.c:compute_parallel_worker,generate_gather_paths`,
    `util/pathnode.c`)

**Partitionwise joins / aggregates**

47. Partitionwise join: the per-level partitionwise step in
    `standard_join_search` plus `try_partitionwise_join` after each
    `populate_joinrel_with_paths` arm; gated by `enable_partitionwise_join`
    (`false` default); reuses `add_paths_to_append_rel` over child joinrels.
    (`path/allpaths.c:standard_join_search`, `path/joinrels.c`)
48. Partitionwise / partial aggregate split: `enable_partitionwise_aggregate`
    (`false` default) excludes grouping sets; the partial-agg stage
    builds `UPPERREL_PARTIAL_GROUP_AGG` with `AGGSPLIT_INITIAL_SERIAL` from
    the cheapest total plus the first partial path, finalized with
    `AGGSPLIT_FINAL_DESERIAL` over `Gather`/`Gather Merge` via
    `gather_grouping_paths`; the non-parallel partial path also serves
    partitionwise and Parallel Append inputs.
    (`plan/planner.c:create_grouping_paths`)

**View / subquery pull-up conditions**

49. `is_simple_subquery` gate: plain `SELECT`, no set-operations, no
    `hasAggs`/`hasWindowFuncs`/`hasTargetSRFs`, no `groupClause`/
    `groupingSets`/`havingQual`/`sortClause`/`distinctClause`/
    `limitOffset`/`limitCount`/`hasForUpdate`/`cteList`, not
    `security_barrier`, lateral-safe, no volatile
    targetlist; outer-join-nullable pulled-up expressions are wrapped in
    `PlaceHolderVar`s. (`prep/prepjointree.c:is_simple_subquery`)
50. Simple `UNION ALL` / `VALUES` / function cases: simple-`UNION ALL`
    pull-up (`pull_up_simple_union_all` → appendrel), single-row `VALUES` pull-up,
    constant-function pull-up; top-level `flatten_simple_union_all` flattens
    the `UNION ALL` tree into an appendrel. (`prep/prepjointree.c`)
51. Subquery RTE pathing: `set_subquery_pathlist` pushes only safe
    restriction quals, removes unused
    outputs (`remove_unused_subquery_outputs`), plans with the parent
    `tuple_fraction` only when no upper processing sits above, converts keys
    with `convert_subquery_pathkeys`, and keeps partial paths when
    `consider_parallel`. (`path/allpaths.c:set_subquery_pathlist`)

**`PlaceHolderVar` / lateral handling**

52. `PlaceHolderVar` levels: `find_placeholders_in_jointree` records eval
    levels, `fix_placeholder_input_needed_levels` runs after `qp_callback`,
    `add_placeholders_to_base_rels` attaches them; PHVs spanning both sides
    of a candidate join act as a join-order restriction via
    `have_join_order_restriction` unless `has_legal_joinclause` already forces
    the order. (`util/placeholder.c`, `plan/initsplan.c`, `path/joinrels.c`)
53. Lateral parameterization: `find_lateral_references` fills `lateral_vars`,
    `create_lateral_join_info` computes transitive `lateral_relids` plus
    `lateral_referencers`; `required_outer` starts at `lateral_relids`
    in `set_plain_rel_pathlist`; inner Vars/PHVs become `PARAM_EXEC` via
    `replace_nestloop_params` + `assign_nestloop_param_var`, listed by
    `identify_current_nestloop_params`. (`plan/initsplan.c`,
    `plan/createplan.c`)

**Outer-join legalization**

54. `join_is_legal` order rule: exactly one `sjinfo` with `min_lefthand ⊆
    rel1 && min_righthand ⊆ rel2` (or reversed); joins splitting a `min_*`
    set are rejected; `SEMI` with `rel2 == syn_righthand` tries unique-ified
    arms; lateral refs in both directions are forbidden; lower-left joins
    require `lhs_strict`; `JOIN_FULL` always stays a 2-element subproblem and
    `JOIN_RIGHT` is rewritten to `JOIN_LEFT` by `reduce_outer_joins`.
    (`path/joinrels.c:join_is_legal`, `prep/prepjointree.c`)
55. OJ relids / clones / nulling: each OJ owns an RT index (`ojrelid`,
    PG16+); joinrel relids absorb performed OJs via
    `add_outer_joins_to_relids`; nulled Vars carry `varnullingrels`
    (recorded in `nulling_relids`); commuted OJ orders get
    clone clauses (`has_clone`/`is_clone`) from
    `deconstruct_distribute_oj_quals`; OJ quals use `is_pushed_down = false`,
    `required_relids = ojscope`. (`plan/initsplan.c`, `path/joinrels.c`)
56. Outer-join reduction and removal: `reduce_outer_joins` strengthens
    `LEFT`/`FULL` to inner on strict nullable-side quals; surviving
    `remove_useless_joins` needs a single-baserel RHS unused above plus
    `rel_is_distinct_for` on the join clauses; `reduce_unique_semijoins`
    turns unique-inner `SEMI` into `INNER`; PG18
    `remove_useless_self_joins` merges unique-key inner self-joins only when
    `enable_self_join_elimination`. (`prep/prepjointree.c`,
    `plan/analyzejoins.c`)

**Unique / Sort upper paths**

57. Unique paths: `create_unique_path` over the cheapest total using
    `semi_can_btree`/`semi_can_hash`/`semi_operators`/`semi_rhs_exprs` (sort
    plus Unique, or hash Agg); exceeding `get_hash_memory_limit()` is a hard
    veto (clears `semi_can_hash`, may return `NULL`), not a `disabled_nodes`
    increment; the winner is cached as `cheapest_unique_path`; distinct
    grouping (`create_distinct_paths`) puts sorted Unique over every input
    (PG18 reordering via `get_useful_pathkeys_for_distinct`) and hashed
    `AGG_HASHED` via `create_agg_path` on the cheapest unless `DISTINCT ON`
    or `!enable_hashagg`, with rows from `estimate_num_groups`.
    (`util/pathnode.c`, `plan/planner.c`)
58. Sort upper rule: `create_ordered_paths` measures
    `pathkeys_count_contained_in` presorted prefixes; only the cheapest total
    path gets a full sort while any presorted prefix may use
    `create_incremental_sort_path` under `enable_incremental_sort`;
    `limit_tuples` bounds `cost_sort`/`cost_tuplesort`; sorted partials feed
    Gather Merge; mergejoin's own sorts (inside `create_*_path`) sort both
    sides fully with no incremental variant; plan-time sorts come from
    `prepare_sort_from_pathkeys` + `make_sort` with plan `disabled_nodes`
    mirroring `!enable_sort`. (`plan/planner.c`, `plan/createplan.c`)

**`LIMIT` / tuple-fraction interplay**

59. `preprocess_limit`: constant count `<= 0` becomes `1`, `NULL` count means
    no limit (`0`), non-constant becomes `-1` ("assume 10%"); `NULL` offset
    becomes `0`, non-constant `-1`; `limit_tuples = count + offset` only when
    both are known (else `-1` under grouping/distinct/window/setop); unknown
    sides contribute a 10% fraction merged with the cursor fraction into
    `root->tuple_fraction`. Illustrative: `LIMIT 10 OFFSET 5` gives
    `limit_tuples = 15` (`10 + 5 = 15`, verified with `python3 -c` before
    writing). (`plan/planner.c:preprocess_limit`)
60. Fractional choice and startup: `get_cheapest_fractional_path` returns
    `cheapest_total_path` for fraction `<= 0`, divides fractions `>= 1` by its
    rows, skips parameterized paths, and otherwise compares
    `startup + fraction * (total - startup)` after `disabled_nodes` via
    `compare_fractional_path_costs`; `consider_startup = (tuple_fraction >
    0)` (`consider_param_startup` only for single-rel `SEMI`/`ANTI` RHS) also
    admits a cheapest-startup Append via `add_paths_to_append_rel`.
    Illustrative at fraction `0.1`: `(100.0, 1000.0)` scores
    `100.0 + 0.1 * 900.0 = 190.0` while `(10.0, 1100.0)` scores
    `10.0 + 0.1 * 1090.0 = 119.0`, so the higher-total path wins the
    fractional comparison (verified with `python3 -c` before writing).
    (`plan/planner.c:standard_planner`, `util/pathnode.c`,
    `util/relnode.c`, `path/allpaths.c`)

**Parallel-safety coverage**

61. `is_parallel_safe` coverage: `baserestrictinfo`, `reltarget` exprs,
    join `restrictlist` plus join target, every upper `PathTarget`, and
    `LIMIT` expressions must all pass; `TABLEFUNC` / unsafe function /
    `VALUES` exprs, `CTE` / self-ref worktable / named tuplestore never pass;
    any `PROPARALLEL_UNSAFE` function forces `parallelModeOK = false` through
    `max_parallel_hazard`. (`path/allpaths.c`, `util/clauses.c`,
    `plan/planner.c:standard_planner`)

**Per-node `enable_*` effects**

62. Cost-side `disabled_nodes` increments (`+1` propagated through children,
    compared before cost in `add_path` / `add_path_precheck` /
    `add_partial_path` / `compare_path_costs_fuzzily` /
    `compare_fractional_path_costs`): `initial_cost_*` adds for
    `!enable_nestloop` / `!enable_mergejoin` (plus explicit sorts) /
    `!enable_hashjoin`; `cost_sort` / `make_sort` mirror `!enable_sort`;
    `cost_agg` adds for `AGG_HASHED`/`MIXED` under `!enable_hashagg`;
    `cost_material`, `cost_gather_merge`, `cost_seqscan`, `cost_index`,
    `cost_bitmap_heap_scan` mirror their `enable_*`; `create_setop_path`
    adds for hashed setop under `!enable_hashagg` and on hash-memory
    overflow; the sole surviving `disable_cost = 1e10` sits in
    `final_cost_hashjoin` for the inner-MCV over-memory case.
    (`costsize.c`, `util/pathnode.c`, `utils/misc/guc_tables.c`)
63. Generation gates (no path at all when off): `check_index_only` needs
    `enable_indexonlyscan`; TID paths need `enable_tidscan` except `CURRENT
    OF`; `get_memoize_path` needs `enable_memoize`; inner-`Material` and
    subplan materialization need `enable_material`; merge/hash generators are
    skipped unless the join is `FULL`; `enable_incremental_sort`,
    `enable_parallel_append`, `enable_parallel_hash`,
    `enable_partitionwise_join` / `enable_partitionwise_aggregate`,
    `enable_partition_pruning`, `enable_presorted_aggregate`,
    `enable_async_append`, `enable_self_join_elimination`,
    `enable_group_by_reordering`, `enable_distinct_reordering`, and
    `enable_geqo` each gate their own generator. (`path/indxpath.c`,
    `path/tidpath.c`, `path/joinpath.c`, `plan/planner.c`,
    `utils/misc/guc_tables.c`)
64. GUC defaults restated: all 24 `enable_*` true except
    `partitionwise_join` / `partitionwise_aggregate` (`false`);
    `from_collapse_limit` / `join_collapse_limit` = `8` (`1` keeps explicit
    order); GEQO on / threshold `12` / effort `5` / pool `0` / generations
    `0` / bias `2.0` / seed `0.0`; `max_parallel_workers_per_gather = 2`;
    `min_parallel_table_scan_size` 8 MB / `min_parallel_index_scan_size`
    512 kB; `parallel_setup_cost = 1000` / `parallel_tuple_cost = 0.1`
    (illustrative debug-Gather add-on `1000 + 0.1 * 1200 = 1120.0`, verified
    with `python3 -c` before writing); `seq_page_cost = 1.0` /
    `random_page_cost = 4.0`; `cpu_tuple_cost = 0.01` /
    `cpu_index_tuple_cost = 0.005` / `cpu_operator_cost = 0.0025`;
    `effective_cache_size = 524288`; `work_mem = 4096` /
    `hash_mem_multiplier = 2.0`; `cursor_tuple_fraction = 0.1`;
    `constraint_exclusion = partition`; `recursive_worktable_factor = 10.0`;
    JIT thresholds and `debug_parallel_query = off`.
    (`utils/misc/guc_tables.c`, `include/optimizer/cost.h`)

**Restored take2 mechanics**

65. TID / scan / index pipeline order: TID paths first with the `CURRENT OF`
    short-circuit, then SeqScan, then partial SeqScan under
    `consider_parallel`, then `create_index_paths`; `build_index_paths`
    creates a path only for index clauses, useful pathkeys (forward plus
    backward, `amcanorderbyop` against `query_pathkeys`), a useful predicate,
    or index-only eligibility, with a partial variant when `amcanparallel`
    plus unparameterized `consider_parallel`; partial indexes need `predOK`
    from `check_index_predicates`; restriction/join/EC clause sets feed
    ordinary, parameterized, bitmap-`OR` (`generate_bitmap_or_paths`),
    greedy-`AND` (`choose_bitmap_and`),
    and parameterized-bitmap-heap paths. (`path/tidpath.c`,
    `path/allpaths.c`, `path/indxpath.c:241,811,1786`)
66. Parameterization bookkeeping: `get_baserel_parampathinfo` collects
    movable join clauses plus
    `generate_join_implied_equalities` for each `required_outer`, with
    `ppi_rows = get_parameterized_baserel_size` and `ppi_serials` from
    `rinfo_serial`, cached per outer set in `ppilist`; paths of one rel share
    `rel->rows` (parameterized paths use `ppi_rows`; join/upper paths use the
    once-computed joinrel/upper rows, never per-path re-estimates).
    (`util/relnode.c`, `path/equivclass.c`, `costsize.c`)
67. Pathkey mechanics: `make_canonical_pathkey` interns keys (pointer
    compared), dropping constant-EC and duplicate keys;
    `build_index_pathkeys` emits per-column keys from
    `sortopfamily`/`reverse_sort`/`nulls_first` (flipped backward) stopping at
    the first non-EC column; `truncate_useless_pathkeys` keeps the longest
    prefix useful for merging, ordering (prefix of `query_pathkeys`),
    grouping, distinct, or set-op; `has_useful_pathkeys` is
    `joininfo || has_eclass_joins || group_pathkeys || query_pathkeys`;
    `pathkeys_contained_in` is the prefix test, `compare_pathkeys` yields
    `EQUAL`/`BETTER1`/`BETTER2`/`DIFFERENT`, `pathkeys_count_contained_in`
    yields the incremental-sort prefix length; `build_join_pathkeys` keeps
    truncated outer order for nestloop/mergejoin (output = outer) and `NIL`
    for hash. (`path/pathkeys.c`)
68. Two-stage join costing and Memoize gate: `initial_cost_*` cheap bounds
    (no qual costs, semi factors, hash-bucket stats, merge-scan selectivity,
    or inner rescan) feed `add_path_precheck` (`disabled_nodes`, total within
    `1.01`, startup within `1.01` or unconsidered, keys at least as good,
    same `required_outer`, early stop on the `(disabled_nodes, total)` order),
    then `create_*_path` runs `final_cost_*` (join-qual costs,
    `inner_unique`/semifactors, `estimate_hash_bucket_stats`,
    `mergejoinscansel`, inner `cost_rescan`) before `add_path`;
    `try_nestloop_path` sets `required_outer =
    calc_nestloop_required_outer` and vetoes params containing the join's own
    `ojrelid`; `get_memoize_path` additionally needs `enable_memoize`, outer
    rows `>= 2`, a parameterized/lateral inner, no unsafe `SEMI`/`ANTI`
    shape, unique-inner clauses as `ppi_serials`, no volatile target /
    restriction / parameter clauses, and hashable equality per parameter.
    (`path/joinpath.c:124,675`, `costsize.c`, `util/pathnode.c`)
69. `create_plan` / `set_plan_references` tail: `CP_EXACT_TLIST` at top,
    `CP_SMALL_TLIST` under Sort/Material/Memoize/Gather, `CP_LABEL_TLIST`
    under Agg/Group/Sort/Unique/WindowAgg, `CP_IGNORE_TLIST` where the parent
    replaces;     `use_physical_tlist` only for base `RELATION`/`SUBQUERY`/
    `FUNCTION`/`TABLEFUNC`/`VALUES`/`CTE` scans (no system columns, no here-evaluated `PlaceHolderVar`s,
    index-only needs `canreturn` cover, labelled needs distinct plain `Var`s);
    quals via `order_qual_clauses` with a gating `Result` for pseudoconstants
    and `replace_nestloop_params` for parameterized scans;
    `fix_indexqual_references` (`INDEX_VAR`, commuted operators,
    `indexqualorig`) plus `is_redundant_with_indexclauses` drops and
    `create_bitmap_subplan` recheck/shared marking; hash skew only for a
    single-clause plain-`Var` base-rel outer side (parallel Hash records
    `rows_total`); `set_plan_references` flattens with `rtoffset`
    (`OUTER_VAR`/`INNER_VAR`/`INDEX_VAR`/`ROWID_VAR`), removes trivial
    `SubqueryScan`s, picks `AlternativeSubPlan` by cost, assigns
    `plan_node_id`, and collects dependencies.
    (`plan/createplan.c:337`, `plan/setrefs.c`)

