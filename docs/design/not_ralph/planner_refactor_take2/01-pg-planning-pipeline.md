# 01 — PostgreSQL planning pipeline (PG 18.3 oracle)

Scope: how PostgreSQL 18.3 turns a rewritten `Query` into a `PlannedStmt`.
Every claim is tied to a function in `postgres/src/backend/optimizer/...`
(paths below are relative to `postgres/`). Struct fields are quoted from
`src/include/nodes/pathnodes.h`. Version-specific behaviour is flagged
(PG16 outer-join relids, PG17 `JOIN_RIGHT_SEMI`, PG18 `disabled_nodes` and
self-join elimination). Nothing in this document describes goopg; §13 is the
checklist a later document diffs goopg against.

Notation: `root` = `PlannerInfo`, `glob` = `PlannerGlobal`, `rel` =
`RelOptInfo`. "cite: file:function" means the behaviour is read from that
function.

---

## 1. Entry: planner() / standard_planner()

`planner()` (`plan/planner.c`) only dispatches to `planner_hook` or
`standard_planner(parse, query_string, cursorOptions, boundParams)`
(`plan/planner.c:303`). `standard_planner` does, in order:

1. **PlannerGlobal setup.** `glob = makeNode(PlannerGlobal)`; all list fields
   NIL, `lastPHId/lastRowMarkId/lastPlanNodeId = 0`, `transientPlan =
   dependsOnRole = false`. Planner-relevant fields of `PlannerGlobal`:

   | field | meaning |
   |---|---|
   | `boundParams` | external Param values (for `estimate_expression_value`) |
   | `subplans / subpaths / subroots` | one entry per `SubPlan` (initplans, CTEs, sublinks); index = `plan_id-1` |
   | `rewindPlanIDs` | subplans needing `REWIND` |
   | `finalrtable`, `finalrteperminfos`, `finalrowmarks`, `resultRelations`, `appendRelations`, `partPruneInfos` | flattened by `set_plan_references` |
   | `relationOids`, `invalItems` | plan-cache invalidation dependencies |
   | `paramExecTypes` | types of PARAM_EXEC slots |
   | `parallelModeOK`, `parallelModeNeeded`, `maxParallelHazard` | see below |

2. **Parallel-mode decision** (cite `plan/planner.c:standard_planner`):
   ```
   if (cursorOptions & CURSOR_OPT_PARALLEL_OK) && IsUnderPostmaster
      && parse->commandType == CMD_SELECT && !parse->hasModifyingCTE
      && max_parallel_workers_per_gather > 0 && !IsParallelWorker():
        glob->maxParallelHazard = max_parallel_hazard(parse)   /* util/clauses.c */
        glob->parallelModeOK    = (maxParallelHazard != PROPARALLEL_UNSAFE)
   else: maxParallelHazard = PROPARALLEL_UNSAFE; parallelModeOK = false
   glob->parallelModeNeeded = parallelModeOK && (debug_parallel_query != DEBUG_PARALLEL_OFF)
   ```
   `parallelModeNeeded` is later forced true by `create_gather_path`/
   `create_gather_merge_path` (`util/pathnode.c`) when such a path is created.

3. **tuple_fraction from the cursor option:**
   ```
   if (cursorOptions & CURSOR_OPT_FAST_PLAN):
       tuple_fraction = cursor_tuple_fraction          /* GUC, default 0.1 */
       if >= 1.0 -> 0.0 ; if <= 0.0 -> 1e-10
   else tuple_fraction = 0.0                           /* "fetch all" */
   ```
   LIMIT/OFFSET refine it later in `grouping_planner` via `preprocess_limit`
   (§3). Semantics of the value: `0` = all rows; `<1` = fraction of rows;
   `>=1` = absolute row count (`preprocess_limit` comment; `get_cheapest_fractional_path`).

4. `root = subquery_planner(glob, parse, NULL, false, tuple_fraction, NULL)`
   (§2), then `final_rel = fetch_upper_rel(root, UPPERREL_FINAL, NULL)` and
   `best_path = get_cheapest_fractional_path(final_rel, tuple_fraction)`
   (`plan/planner.c:6617`):
   ```
   best = rel->cheapest_total_path
   if tuple_fraction <= 0: return best
   if tuple_fraction >= 1 && best->rows > 0: tuple_fraction /= best->rows
   for path in rel->pathlist: skip param_info != NULL;
       if compare_fractional_path_costs(best, path, tuple_fraction) > 0: best = path
   ```
   `compare_fractional_path_costs` (`util/pathnode.c`) compares
   `disabled_nodes` first, then `startup + fraction*(total-startup)`.

5. `top_plan = create_plan(root, best_path)` (§11). If
   `CURSOR_OPT_SCROLL` and `!ExecSupportsBackwardScan(top_plan)`, wrap in
   `materialize_finished_plan`.

6. `debug_parallel_query` (`on`/`regress`): if `top_plan->parallel_safe`, a
   single-copy `Gather` (`num_workers=1`, `single_copy=true`,
   `invisible = (mode==REGRESS)`) is put on top; costs add
   `parallel_setup_cost` and `parallel_tuple_cost * plan_rows`.

7. `SS_finalize_plan` on every subplan and the top plan if
   `glob->paramExecTypes != NIL` (`plan/subselect.c`).

8. `top_plan = set_plan_references(root, top_plan)` and the same for every
   `glob->subplans[i]` with its `subroots[i]` (§11).

9. Build `PlannedStmt`: `commandType, queryId, hasReturning, hasModifyingCTE,
   canSetTag, transientPlan, dependsOnRole, parallelModeNeeded, planTree,
   partPruneInfos, rtable = glob->finalrtable, unprunableRelids =
   allRelids - prunableRelids, permInfos, resultRelations, appendRelations,
   subplans, rewindPlanIDs, rowMarks, relationOids, invalItems,
   paramExecTypes, utilityStmt, stmt_location/len`, and `jitFlags`:
   `PGJIT_PERFORM` if `jit_enabled && top_plan->total_cost > jit_above_cost`,
   plus `PGJIT_OPT3` / `PGJIT_INLINE` above `jit_optimize_above_cost` /
   `jit_inline_above_cost`, `PGJIT_EXPR`/`PGJIT_DEFORM` from the booleans.

---

## 2. subquery_planner(): preprocessing order

`subquery_planner(glob, parse, parent_root, hasRecursion, tuple_fraction,
setops)` (`plan/planner.c:651`) is invoked for the top query, each CTE, each
uncorrelated/correlated sublink subplan and each un-pulled-up subquery RTE.
Steps in source order (line numbers are planner.c 18.3):

| # | call | file:function | what it does |
|---|---|---|---|
| 1 | new `PlannerInfo`; `query_level = parent ? parent+1 : 1`; `plan_params/outer_params/init_plans/cte_plan_ids = NIL`; `tuple_fraction` stored; `processed_groupClause/processed_distinctClause = NIL`; `hasPseudoConstantQuals = false` | planner.c:651-715 | |
| 2 | `transform_MERGE_to_join(parse)` | prep/prepjointree.c:183 | MERGE → outer join + `mergeJoinCondition` |
| 3 | `if (parse->cteList) SS_process_ctes(root)` (l.717) | plan/subselect.c:SS_process_ctes | see 2.1 |
| 4 | `replace_empty_jointree(parse)` (l.728) | prepjointree.c:410 | empty FROM → single `RTE_RESULT` |
| 5 | `if (parse->hasSubLinks) pull_up_sublinks(root)` (l.737) | prepjointree.c:468 | see 2.2 |
| 6 | `preprocess_function_rtes(root)` (l.745) | prepjointree.c:914 | inline SQL-function RTEs (`inline_set_returning_function`) |
| 7 | `expand_virtual_generated_columns(root)` (l.753) | prepjointree.c:969 | PG18: replace virtual generated columns by their expressions |
| 8 | `pull_up_subqueries(root)` (l.759) | prepjointree.c:1083 | see 2.3 |
| 9 | `if (parse->setOperations) flatten_simple_union_all(root)` (l.768) | prepjointree.c:2983 | UNION ALL tree → appendrel (`is_simple_union_all`) |
| 10 | scan rtable: set `root->hasJoinRTEs`, `hasLateralRTEs`, local `hasOuterJoins`, `hasResultRTEs`; recompute `parse->hasSubLinks`-independent flags | planner.c:784-880 | |
| 11 | `preprocess_rowmarks(root)` (l.888) | planner.c:2399 | FOR UPDATE/SHARE → `PlanRowMark` list |
| 12 | `root->hasHavingQual = (parse->havingQual != NULL)` | | remembered before const-folding may delete it |
| 13 | `preprocess_expression` on targetList (EXPRKIND_TARGET), WITH CHECK OPTION quals, returningList, `preprocess_qual_conditions(jointree)` (EXPRKIND_QUAL, recursive over FromExpr/JoinExpr quals), havingQual, window start/end offsets, limitOffset/limitCount (EXPRKIND_LIMIT), ON CONFLICT set/where, mergeJoinCondition, append_rel_list translated_vars (EXPRKIND_APPINFO), per-RTE: subquery lateral checks, `rte->functions` (EXPRKIND_RTFUNC[_LATERAL]), `tablefunc`, `values_lists` (EXPRKIND_VALUES), `groupexprs`, `securityQuals` | planner.c:900-1060 | see 2.4 |
| 14 | if `hasJoinRTEs`: `flatten_join_alias_vars` on remaining places; havingQual `flatten_group_exprs` | | |
| 15 | `parse->hasTargetSRFs = expression_returns_set(targetList)` | | |
| 16 | `expand_grouping_sets(parse->groupingSets, groupDistinct, -1)` | | |
| 17 | HAVING split (l.1130-1195): each HAVING item that contains no aggregate, no volatile function and no subplan (and for grouping sets does not reference the group RTE) is moved into `jointree->quals` (WHERE); with empty groupClause it is *copied* to both; otherwise it stays in HAVING | planner.c | |
| 18 | `if (hasOuterJoins) reduce_outer_joins(root)` (l.1207) | prepjointree.c:3102 | outer→inner join strength reduction using strict quals; JOIN_RIGHT is rewritten to JOIN_LEFT here (initsplan.c comment l.1403) |
| 19 | `if (hasResultRTEs || hasOuterJoins) remove_useless_result_rtes(root)` (l.1216) | prepjointree.c:3596 | drop `RTE_RESULT` when joined to anything |
| 20 | `grouping_planner(root, tuple_fraction, setops)` (l.1220) | §3 | |
| 21 | `SS_identify_outer_params(root)` (l.1227) | subselect.c:2184 | which PARAM_EXEC ids come from outer levels (`root->outer_params`) |
| 22 | `SS_charge_for_initplans(root, final_rel)` (l.1236); `set_cheapest(final_rel)` | subselect.c:2248 | add initplan cost to every FINAL path, mark them non-parallel-safe if any initplan is |

The join-removal family does **not** run here; it runs inside
`query_planner` (§4): `remove_useless_joins`, `reduce_unique_semijoins`,
`remove_useless_self_joins` (`plan/planmain.c:228-239`, `plan/analyzejoins.c`).

### 2.1 CTE handling — `SS_process_ctes` (subselect.c)

For each `CommonTableExpr`:
- `cterefcount == 0 && SELECT` → dropped (plan id −1).
- **Inlined** (`inline_cte`) iff
  `(ctematerialized == NEVER || (DEFAULT && cterefcount == 1))
  && !cterecursive && cmdType == SELECT && !contain_dml(query)
  && (cterefcount <= 1 || !contain_outer_selfref(query))
  && !contain_volatile_functions(query)`.
- Otherwise: `subroot = subquery_planner(glob, copy, root, cterecursive, 0.0, NULL)`
  (tuple_fraction 0 = whole result), `best_path = final_rel->cheapest_total_path`,
  `create_plan`, wrap in a `SubPlan{subLinkType=CTE_SUBLINK, setParam=[special param],
  plan_name="CTE name"}`, appended to `glob->subplans` and `root->init_plans`;
  `cost_subplan`. Later `set_cte_pathlist` (`path/allpaths.c:2907`) builds a
  `CteScan` path referencing that plan id.

### 2.2 Sublink pull-up — `pull_up_sublinks` (prepjointree.c:468)

Recurses the jointree (`pull_up_sublinks_jointree_recurse`) and the WHERE /
JOIN-ON quals (`pull_up_sublinks_qual_recurse`), converting top-level
`ANY`/`EXISTS` (and `NOT EXISTS`) sublinks appearing at AND-level to
semi/anti joins:

- `convert_ANY_sublink_to_join(root, sublink, available_rels)`
  (subselect.c:1333): requires the subselect's level-1 outer refs ⊆
  `available_rels` (then the new RTE is LATERAL), `testexpr` to reference at
  least one upper var and only `available_rels`, and no volatile function in
  `testexpr`. Result: new `RTE_SUBQUERY` ("ANY_subquery") + `JoinExpr`
  `JOIN_SEMI` whose quals are `testexpr` with Params replaced by subquery
  output Vars.
- `convert_EXISTS_sublink_to_join(root, sublink, under_not, available_rels)`
  (subselect.c:1450): `simplify_EXISTS_query` must succeed (no set-ops,
  aggs, grouping sets, window funcs, SRFs, modifying CTE, HAVING, OFFSET,
  row marks; LIMIT must be a constant > 0 and is then dropped; target list,
  GROUP/ORDER/DISTINCT/WINDOW clauses cleared). Then the WHERE clause is
  hoisted one level (`IncrementVarSublevelsUp`), its upper-var set must be ⊆
  `available_rels`, no volatile functions; result `jointype = under_not ?
  JOIN_ANTI : JOIN_SEMI`.
- `convert_EXISTS_to_ANY` (subselect.c:1731) is used by `make_subplan`
  for EXISTS sublinks that could not become joins: if the subselect has no
  level-1 refs except in a conjunction of `OpExpr`s with exactly one side
  outer, rewrite to `ANY` with a hashable testexpr so the subplan can use a
  hash table (`subplan_is_hashable`, `build_subplan`).
- Remaining sublinks become `SubPlan`/`InitPlan` in
  `SS_process_sublinks` (called from `preprocess_expression`); `build_subplan`
  makes an initPlan when `parParam == NIL` for EXISTS/EXPR/ARRAY/ROWCOMPARE,
  uses `useHashTable` for uncorrelated `ANY_SUBLINK` when
  `subplan_is_hashable(plan)` and `testexpr_is_hashable`, and otherwise (if
  `enable_material`) materializes a re-scanned subplan.

### 2.3 Subquery pull-up — `pull_up_subqueries` (prepjointree.c:1083)

`pull_up_subqueries_recurse` flattens `RTE_SUBQUERY` entries when
`is_simple_subquery(root, subquery, rte, lowest_outer_join)`
(prepjointree.c:1807) holds: SELECT, no `setOperations`, no
`hasAggs/hasWindowFuncs/hasTargetSRFs/groupClause/groupingSets/havingQual/
sortClause/distinctClause/limitOffset/limitCount/hasForUpdate/cteList`, not
`security_barrier`, lateral refs safe wrt `lowest_outer_join`, and no
volatile functions in the target list. Also: simple UNION ALL subqueries
(`is_simple_union_all` → appendrel via `pull_up_simple_union_all`), simple
VALUES with one row (`pull_up_simple_values`), and constant functions
(`pull_up_constant_function`). Outer-join nullable pulled-up expressions get
wrapped in `PlaceHolderVar`s.

### 2.4 `preprocess_expression(root, expr, kind)` (planner.c:1300)

```
if hasJoinRTEs and kind not in {RTFUNC, VALUES, TABLESAMPLE, TABLEFUNC}: flatten_join_alias_vars
if kind != RTFUNC:               eval_const_expressions(root, expr)      /* util/clauses.c */
if kind == QUAL:                 canonicalize_qual(expr, false)            /* prep/prepqual.c */
if kind in {QUAL, TARGET}:       convert_saop_to_hashed_saop(expr)
if parse->hasSubLinks:           SS_process_sublinks(root, expr, kind==QUAL)
if query_level > 1:              SS_replace_correlation_vars(root, expr)
if kind == QUAL:                 make_ands_implicit(expr)                  /* implicit AND list */
```

`eval_const_expressions` folds constants, inlines simple SQL functions,
simplifies `CASE`/`AND`/`OR`/`COALESCE`, uses `boundParams` when it is
`estimate_expression_value`.

### 2.5 `preprocess_minmax_aggregates` (plan/planagg.c:73)

Called from `grouping_planner` just before `query_planner`
(planner.c:1600 region, "only if there are aggregates and no grouping").
Rewrites `SELECT min(x)/max(x)` (no GROUP BY, no window, no set-op, single
plain table, aggregates all MIN/MAX with btree-sortable operands) into
`MinMaxAggPath` whose subpaths are `LIMIT 1` index scans ordered by the
aggregate's sort op; kept only if cheaper (`create_minmaxagg_path`).

---

## 3. grouping_planner(): the upper-rel pipeline

`grouping_planner(root, tuple_fraction, setops)` (planner.c:1434).

1. **LIMIT preprocessing.** If `limitCount || limitOffset`:
   `tuple_fraction = preprocess_limit(root, tuple_fraction, &offset_est, &count_est)`
   (planner.c:preprocess_limit). `estimate_expression_value` on each; constant
   count ≤0 becomes 1, NULL count means no limit (0), non-constant → −1
   (meaning "unknown, assume 10%"); offset NULL → 0, non-constant → −1. Then
   `limit_fraction = count+offset` (or 0.10 when either is unknown) and it is
   merged with the caller's fraction (min of absolute counts; fraction rules
   in the function). `limit_tuples = count_est + offset_est` when both known
   (used only by bounded sort costing). `root->tuple_fraction = tuple_fraction`.

2. **Set operations.** `plan_set_operations(root)` (`prep/prepunion.c`)
   produces `UPPERREL_SETOP`; `sort_pathkeys` computed; no DISTINCT/GROUP.

3. **Normal path.** `preprocess_grouping_sets`, `preprocess_targetlist`
   (`prep/preptlist.c`, builds `root->processed_tlist`, adds junk/rowid
   columns for UPDATE/DELETE/MERGE and row marks), `preprocess_aggrefs` (agg
   info), window activation (`select_active_windows`),
   `preprocess_minmax_aggregates`, `root->limit_tuples` set (−1 if grouping/
   distinct/window/setop present, else `limit_tuples`), and

   `current_rel = query_planner(root, standard_qp_callback, &qp_extra)` (§4).

4. **PathTargets** (all in planner.c; computed *after* query_planner because
   they need `root->processed_tlist` and pathkeys):
   - `final_target = create_pathtarget(root, root->processed_tlist)`.
   - `sort_input_target = make_sort_input_target(root, final_target, &have_postponed_srfs)`
     if there is ORDER BY (and/or LIMIT): postpones expensive/volatile/SRF
     columns until after sorting.
   - `grouping_target = make_window_input_target(root, final_target, activeWindows)`
     if there are windows: the window nodes' input (grouping columns + window
     args + partition/order keys).
   - `scanjoin_target = make_group_input_target(root, final_target)` if
     GROUP/agg/HAVING: Vars needed by grouping/aggregates/HAVING.
   - Each target is split for SRFs by `split_pathtarget_at_srfs` producing
     `*_targets` lists.
   - `apply_scanjoin_target_to_paths(root, current_rel, scanjoin_targets,
     scanjoin_targets_contain_srfs, scanjoin_target_parallel_safe,
     tlist_same_exprs)` (planner.c): rewrites every path in `pathlist` and
     `partial_pathlist` with a `ProjectionPath` (or in-place when
     `tlist_same_exprs`), for partitioned rels recurses into child rels and
     re-runs `generate_useful_gather_paths`, clears `partial_pathlist` and
     `consider_parallel` if the target is not parallel safe, then
     `generate_useful_gather_paths` (if `consider_parallel`) and `set_cheapest`.
   - `root->upper_targets[UPPERREL_FINAL/ORDERED] = final_target`,
     `[DISTINCT/PARTIAL_DISTINCT/WINDOW] = sort_input_target`,
     `[GROUP_AGG] = grouping_target`.

5. **Upper rels in order** (each takes `current_rel` and returns the new one;
   `fetch_upper_rel(root, kind, relids)` creates them on demand):

   | order | function | UpperRelationKind |
   |---|---|---|
   | 1 | `create_grouping_paths(root, current_rel, grouping_target, grouping_target_parallel_safe, gset_data)` if `hasAggs || groupClause` | `UPPERREL_PARTIAL_GROUP_AGG`, `UPPERREL_GROUP_AGG` |
   | 2 | `create_window_paths(...)` if `activeWindows` | `UPPERREL_WINDOW` |
   | 3 | `create_distinct_paths(root, current_rel, sort_input_target)` if `distinctClause` | `UPPERREL_PARTIAL_DISTINCT`, `UPPERREL_DISTINCT` |
   | 4 | `create_ordered_paths(root, current_rel, final_target, final_target_parallel_safe, have_postponed_srfs ? -1.0 : limit_tuples)` if `sortClause` | `UPPERREL_ORDERED` |
   | 5 | final rel: for each path of `current_rel`: `create_lockrows_path` if rowMarks; `create_limit_path` if `limit_needed(parse)`; `create_modifytable_path` (INSERT/UPDATE/DELETE/MERGE, incl. inherited-target expansion) if not SELECT; `add_path(final_rel, path)`; partial paths of `current_rel` are copied into `final_rel->partial_pathlist` when `final_rel->consider_parallel` | `UPPERREL_FINAL` |

   `UpperRelationKind` (pathnodes.h): `UPPERREL_SETOP, UPPERREL_PARTIAL_GROUP_AGG,
   UPPERREL_GROUP_AGG, UPPERREL_WINDOW, UPPERREL_PARTIAL_DISTINCT,
   UPPERREL_DISTINCT, UPPERREL_ORDERED, UPPERREL_FINAL`.
   `adjust_paths_for_srfs` inserts `ProjectSet` after grouping/window/ordered
   stages as needed. `final_rel->consider_parallel = current_rel->consider_parallel
   && is_parallel_safe(limitOffset) && is_parallel_safe(limitCount)`.

6. **Grouping decisions** (`create_grouping_paths`, planner.c):
   `GROUPING_CAN_USE_SORT` if `groupClause == NIL || grouping_is_sortable(processed_groupClause)`;
   `GROUPING_CAN_USE_HASH` if `parse->groupClause != NIL && numOrderedAggs == 0
   && grouping_is_hashable(...)` (grouping sets: `gd->any_hashable`);
   `GROUPING_CAN_PARTIAL_AGG` if `can_partial_agg(root)` (`hasAggs || groupClause`,
   no grouping sets, no `hasNonPartialAggs/hasNonSerialAggs`);
   `enable_partitionwise_aggregate && !groupingSets` allows partitionwise.
   `add_paths_to_grouping_rel`: for `can_sort`, every input path × each
   ordering from `get_useful_group_keys_orderings` (§9) gets Sort /
   IncrementalSort (if `enable_incremental_sort` and partially sorted) then
   `create_agg_path(AGG_SORTED|AGG_PLAIN)` or `create_group_path` (no aggs);
   the same over `partially_grouped_rel->pathlist` with
   `AGGSPLIT_FINAL_DESERIAL`. For `can_hash`: `create_agg_path(cheapest_path,
   AGG_HASHED)` and the finalize-hash over `partially_grouped_rel`. Hash agg
   memory is *costed*, not vetoed: `cost_agg` (costsize.c:2682) adds spill
   cost when `hashentrysize*numGroups > get_hash_memory_limit()`; the only
   node-level penalty is `disabled_nodes++` when `!enable_hashagg`.
   `create_partial_grouping_paths` builds `UPPERREL_PARTIAL_GROUP_AGG` from
   `input_rel->cheapest_total_path` (non-parallel partial agg for
   partitionwise/parallel-append) and `linitial(input_rel->partial_pathlist)`
   with `AGGSPLIT_INITIAL_SERIAL`; `gather_grouping_paths` adds Gather/Gather
   Merge over its partial paths.

7. **DISTINCT** (`create_final_distinct_paths`): `numDistinctRows =
   estimate_num_groups(...)`; if `grouping_is_sortable(processed_distinctClause)`
   then for every input path (sorted or sortable via
   `get_useful_pathkeys_for_distinct` when `enable_distinct_reordering`)
   `create_upper_unique_path`; `allow_hash = true` if no sort alternative,
   `false` if `hasDistinctOn || !enable_hashagg`; hashed path via
   `create_agg_path(AGG_HASHED)` when `grouping_is_hashable`. For DISTINCT ON,
   `needed_pathkeys = sort_pathkeys` when `distinct_pathkeys` is a prefix of it.

8. **ORDER BY** (`create_ordered_paths`): for each input path,
   `pathkeys_count_contained_in(sort_pathkeys, path->pathkeys, &presorted)`;
   unsorted paths (only the cheapest total path, or any path with
   `presorted_keys>0` when `enable_incremental_sort`) get `create_sort_path`
   or `create_incremental_sort_path` with `limit_tuples` (bounded-sort costing
   through `cost_tuplesort`, costsize.c:1898). If `ordered_rel->consider_parallel
   && sort_pathkeys != NIL`, partial paths are sorted and topped by
   `create_gather_merge_path`.

---

## 4. query_planner() (plan/planmain.c:54)

Resets `root->join_rel_list/join_rel_hash/join_rel_level/join_cur_level/
canon_pathkeys/left_join_clauses/right_join_clauses/full_join_clauses/
join_info_list/placeholder_list/fkey_list/initial_rels`, then:

| # | call | file:function | effect |
|---|---|---|---|
| 1 | `setup_simple_rel_arrays(root)` | util/relnode.c:94 | `simple_rel_array[]`, `simple_rte_array[]`, appendrel array |
| 2 | fast path: single `RTE_RESULT` FROM item → `build_simple_rel`, `create_group_result_path`, `set_cheapest`, call `qp_callback`, return | planmain.c:90-130 | `consider_parallel` only if `parallelModeOK && (query_level>1 || debug_parallel_query)` |
| 3 | `add_base_rels_to_query(root, jointree)` | plan/initsplan.c:158 | `build_simple_rel` for every RangeTblRef (calls `get_relation_info`, §5) |
| 4 | `remove_useless_groupby_columns(root)` | initsplan.c:412 | drop GROUP BY columns functionally dependent on a PK of the same rel |
| 5 | `build_base_rel_tlists(root, processed_tlist)` | initsplan.c:235 | `add_vars_to_targetlist` → `rel->reltarget->exprs`, `attr_needed[attno] |= all_query_rels` |
| 6 | `find_placeholders_in_jointree`, `find_lateral_references(root)` | placeholder.c / initsplan.c:658 | `rel->lateral_vars`, PHV eval levels |
| 7 | `joinlist = deconstruct_jointree(root)` | initsplan.c:1084 | see 4.1 |
| 8 | `reconsider_outer_join_clauses(root)` | path/equivclass.c:2135 | outer-join clauses `a.x = b.y` where a.x is in an EC with a constant → replace by `b.y = const`-style derived clauses |
| 9 | `generate_base_implied_equalities(root)` | equivclass.c:1188 | see 4.3; sets `root->ec_merging_done = true` |
| 10 | `(*qp_callback)(root, qp_extra)` = `standard_qp_callback` | planner.c:3453 | see 4.5 (needs canonical ECs) |
| 11 | `fix_placeholder_input_needed_levels` | placeholder.c | |
| 12 | `joinlist = remove_useless_joins(root, joinlist)` | plan/analyzejoins.c:90 | left-join removal: RHS is a single baserel whose columns are unused above the join and `rel_is_distinct_for` (unique index / DISTINCT subquery) on the join clauses |
| 13 | `reduce_unique_semijoins(root)` | analyzejoins.c:844 | SEMI whose inner is provably unique on the join clauses → INNER (drop the SpecialJoinInfo) |
| 14 | `joinlist = remove_useless_self_joins(root, joinlist)` | analyzejoins.c:2488 | **PG18** self-join elimination (guarded by `enable_self_join_elimination`): inner self-joins on a unique key of the same table are merged (`remove_self_joins_one_group`) |
| 15 | `add_placeholders_to_base_rels`, `create_lateral_join_info(root)` | initsplan.c:845 | `rel->lateral_relids`, `direct_lateral_relids`, `lateral_referencers` |
| 16 | `match_foreign_keys_to_quals(root)` | initsplan.c:3631 | fill `ForeignKeyOptInfo.rinfos/eclass/nmatched_*` used by `get_foreign_key_join_selectivity` |
| 17 | `extract_restriction_or_clauses(root)` | path/orclauses.c | derive per-rel restriction clauses from join OR clauses |
| 18 | `add_other_rels_to_query(root)` | initsplan.c:196 | expand inheritance/partition children (`expand_inherited_rtentry`) |
| 19 | `distribute_row_identity_vars(root)` | prep/preptlist.c | |
| 20 | `final_rel = make_one_rel(root, joinlist)` | path/allpaths.c:171 | §5–§7 |
| 21 | error unless `final_rel->cheapest_total_path` exists and is unparameterized | | |

### 4.1 `deconstruct_jointree` and outer-join representation (initsplan.c)

`deconstruct_recurse` builds one `JoinTreeItem` per jointree node
(`qualscope`, `inner_join_rels`, `left_rels/right_rels`,
`nonnullable_rels`, `jdomain` = `JoinDomain`) and returns the *joinlist*
(a nested list of `RangeTblRef`s that the join search will treat as
sub-problems). Collapsing rules (initsplan.c:1216-1250, 1418-1445):

- `FromExpr`: a child sub-joinlist is merged into the parent list if it has
  ≤1 member or `len(joinlist) + sub_members + remaining_children <=
  from_collapse_limit`; otherwise it stays a nested sub-list.
- `JoinExpr`: `JOIN_FULL` is always kept as a 2-element sub-problem;
  otherwise `left+right` are merged if `len(left)+len(right) <=
  join_collapse_limit`, else `list_make2(leftpart, rightpart)`.
- Outer/semi/anti joins get a `SpecialJoinInfo` from `make_outerjoininfo`
  (initsplan.c:1708) appended to `root->join_info_list`; inner joins get none.
- **PG16+ outer-join relids**: each outer join has its own RT index
  (`j->rtindex`, stored as `sjinfo->ojrelid`) and `root->outer_join_rels`;
  Vars nulled by an OJ carry `varnullingrels`. Joinrel relids include the OJ
  relids of joins already performed (`add_outer_joins_to_relids`,
  joinrels.c:793), so a join is identified by base+OJ relids and
  `RestrictInfo.clause_relids` / `required_relids` may include OJ relids.
  `mark_rels_nulled_by_join` records `rel->nulling_relids`.

Then `deconstruct_distribute` distributes quals: WHERE/inner-ON quals via
`distribute_qual_to_rels` and outer-join ON quals via
`deconstruct_distribute_oj_quals` (which also creates *clone* clauses for
commutable OJ orders, `has_clone/is_clone`).

**`SpecialJoinInfo` fields** (pathnodes.h): `min_lefthand`, `min_righthand`
(base+OJ relids that *must* be on each side — the join-order constraint),
`syn_lefthand`, `syn_righthand` (syntactic sides), `jointype` (INNER, LEFT,
FULL, SEMI or ANTI only; RIGHT is rewritten), `ojrelid`, `commute_above_l/r`,
`commute_below_l/r` (OJ identities 3), `lhs_strict` (join clause strict for
some LHS rel), `semi_can_btree`, `semi_can_hash`, `semi_operators`,
`semi_rhs_exprs` (from `compute_semijoin_info`, used by `create_unique_path`).
`make_outerjoininfo` rules: FULL → `min_* = syn_*`, `lhs_strict=false`;
otherwise `strict_relids = find_nonnullable_rels(clause)`, `lhs_strict =
overlap(strict_relids, left_rels)`, `min_lefthand = clause_relids ∩ left_rels`,
`min_righthand = (clause_relids ∪ inner_join_rels) ∩ right_rels`, then each
lower OJ is folded into min_lefthand/min_righthand unless one of the
commutation identities applies (initsplan.c:1760-1900).

### 4.2 `distribute_qual_to_rels` (initsplan.c:2545) and `RestrictInfo`

Parameters: `clause, jtitem, security_level, ojscope, outerjoin_nonnullable,
incompatible_relids, allow_equivalence, has_clone, is_clone, postponed_oj_qual_list`.
Logic: `relids = pull_varnos(clause)`; if `ojscope` and relids ⊄ ojscope →
relids = ojscope. Variable-free clauses (`relids` empty) become
**pseudoconstant** quals (`root->hasPseudoConstantQuals = true`,
evaluated once as a gating `Result`). If the qual is an outer-join ON clause
(`outerjoin_nonnullable != NULL`): `is_pushed_down = false`, `relids = ojscope`,
`maybe_outer_join = true`; else `is_pushed_down = true`, `maybe_equivalence
= allow_equivalence`. `make_restrictinfo(...)` builds the node;
`check_mergejoinable` fills `mergeopfamilies` (btree families where the
operator is an equality member, `op_mergejoinable`); if `maybe_equivalence`
and `process_equivalence` accepts it, the clause is *absorbed* into an EC and
not distributed. Mergejoinable outer-join clauses are also recorded in
`root->left_join_clauses/right_join_clauses/full_join_clauses`
(`OuterJoinClauseInfo`) for `reconsider_outer_join_clauses`. Finally
`distribute_restrictinfo_to_rels` (initsplan.c:3227): a single-rel
`required_relids` → `add_base_clause_to_rel` (`rel->baserestrictinfo`),
otherwise `check_hashjoinable` (sets `hashjoinoperator` via `op_hashjoinable`),
`check_memoizable` (`left/right_hasheqoperator`), and `add_join_clause_to_rels`
(`rel->joininfo` of every member rel).

**`RestrictInfo` planner fields** (pathnodes.h):

| field | meaning |
|---|---|
| `clause` | the boolean expression (may be an `OR` with `orclause` pre-split) |
| `is_pushed_down` | true = WHERE-level / can be applied below an outer join; false = OJ ON clause |
| `can_join` | binary opclause with vars on both sides only → usable as join clause |
| `pseudoconstant` | no Vars, no volatile → evaluate once |
| `has_clone`, `is_clone` | PG16 clone set for commuted OJs |
| `leakproof`, `has_volatile`, `security_level` | RLS ordering |
| `num_base_rels` | count of base relids in `clause_relids` |
| `clause_relids` | all relids referenced (incl. OJ relids) |
| `required_relids` | relids the join must include before applying (≥ clause_relids; = `ojscope` for OJ quals) |
| `incompatible_relids` | OJ relids that must NOT be in the joinrel |
| `outer_relids` | for OJ quals: the OJ relids it depends on |
| `left_relids`, `right_relids` | relids of each side when `can_join` |
| `orclause` | `OR` clause with each arm as RestrictInfo |
| `rinfo_serial` | unique serial (same for clones); used by `ppi_serials` |
| `parent_ec` | EC this clause was derived from (join-implied equalities) |
| `eval_cost` | cached `cost_qual_eval` |
| `norm_selec`, `outer_selec` | cached selectivity as inner-join clause / as outer-join clause (−1 = not cached; `clause_selectivity_ext`) |
| `mergeopfamilies` | btree opfamilies for merge join (NIL = not mergejoinable) |
| `left_ec`, `right_ec`, `left_em`, `right_em` | ECs / members for each side (`initialize_mergeclause_eclasses`) |
| `scansel_cache` | merge-join scan selectivity cache |
| `outer_is_left` | which side is the outer for the current mergejoin |
| `hashjoinoperator` | hash-joinable equality operator OID (0 = not) |
| `left_bucketsize`, `right_bucketsize`, `left_mcvfreq`, `right_mcvfreq` | hash-join bucket stats (`estimate_hash_bucket_stats`) |
| `left_hasheqoperator`, `right_hasheqoperator` | for Memoize key hashing |

### 4.3 Equivalence classes (path/equivclass.c)

- `process_equivalence(root, &restrictinfo, jdomain)` (equivclass.c:179):
  accepts a mergejoinable `A = B` clause unless `security_level > 0 &&
  !leakproof`, both sides identical (then it may become a `A IS NOT NULL`-
  like restriction via `make_restrictinfo`), or a side contains volatile
  functions (volatile ECs are single-member). Looks for existing ECs with
  equal `ec_opfamilies` containing either side (matching `em_jdomain` for
  constants), merges (`ec_merged`) or creates (`add_eq_member`).
- `generate_base_implied_equalities` (equivclass.c:1188): for each EC with
  ≥2 members: if `ec_has_const` → `generate_base_implied_equalities_const`
  (each non-const member gets `member = const` as a **base restriction**
  clause on its rel, `ec_broken` if no operator); else `_no_const` (members
  on the same rel get `m1 = m2` restrictions); single-member ECs only get
  `eclass_indexes` bookkeeping. Sets `rel->eclass_indexes`, `has_eclass_joins`
  computed later in `build_join_rel`.
- `generate_join_implied_equalities(root, join_relids, outer_relids,
  inner_rel, sjinfo)` (equivclass.c:1550): called from
  `build_joinrel_restrictlist` and `get_baserel_parampathinfo`; for each EC
  spanning both sides creates one join clause per outer/inner pairing
  (`create_join_clause`, cached in `ec_derives_list`/`ec_derives_hash`).
  Broken ECs fall back to `generate_join_implied_equalities_broken` (uses the
  original `ec_sources`).
- `EquivalenceClass` fields: `ec_opfamilies`, `ec_collation`, `ec_members`
  (+ `ec_childmembers` arrays for appendrel children), `ec_sources`,
  `ec_derives_list/ec_derives_hash`, `ec_relids`, `ec_has_const`,
  `ec_has_volatile`, `ec_broken`, `ec_sortref` (originating sort clause),
  `ec_min_security/ec_max_security`, `ec_merged`.
  `EquivalenceMember`: `em_expr`, `em_relids`, `em_is_const`, `em_is_child`,
  `em_datatype`, `em_jdomain`, `em_parent`.

### 4.4 Lateral, foreign keys, OR clauses

`create_lateral_join_info` computes transitive `lateral_relids` and
`direct_lateral_relids` and marks `lateral_referencers`; a rel with
`lateral_relids` can only produce paths parameterized by at least those rels
(`required_outer = rel->lateral_relids` in `set_plain_rel_pathlist`).
`match_foreign_keys_to_quals` fills `ForeignKeyOptInfo` (`con_relid,
ref_relid, nkeys, conkey/confkey/conpfeqop, nmatched_ec, nconst_ec,
nmatched_rcols, nmatched_ri, eclass[], fk_eclass_member[], rinfos[]`) so
`get_foreign_key_join_selectivity` (costsize.c) can replace per-column
selectivities by `1/referenced_rows`.
`extract_restriction_or_clauses` (path/orclauses.c) creates a base
restriction from a join `OR` when every arm references the rel, cost-free
(selectivity is then corrected via `consider_new_or_clause`).

### 4.5 `standard_qp_callback` (planner.c:3453) — pathkeys

```
group_pathkeys    = make_pathkeys_for_sortclauses_extended(&processed_groupClause, tlist,
                       remove_redundant=true, ...) if groupClause or numOrderedAggs>0 and sortable
                    (grouping sets: from the first rollup's groupClause);
                    adjust_group_pathkeys_for_groupagg if ordered aggs
num_groupby_pathkeys = len(group_pathkeys)
window_pathkeys   = make_pathkeys_for_window(first active window)  else NIL
distinct_pathkeys = make_pathkeys_for_sortclauses_extended(&processed_distinctClause, ...) if sortable
sort_pathkeys     = make_pathkeys_for_sortclauses(sortClause, tlist)
setop_pathkeys    = from generate_setop_child_grouplist if setop
query_pathkeys    = group_pathkeys ?: window_pathkeys ?:
                    (len(distinct_pathkeys) > len(sort_pathkeys) ? distinct_pathkeys : sort_pathkeys)
                    ?: setop_pathkeys ?: NIL
```
`query_pathkeys` is what base-rel and join paths try to satisfy
(`pathkeys_useful_for_ordering`).

---

## 5. make_one_rel() (path/allpaths.c:171)

```
set_base_rel_consider_startup(root)        /* allpaths.c:247 */
set_base_rel_sizes(root)                   /* allpaths.c:290 */
root->total_table_pages = Σ pages of non-dummy simple rels
set_base_rel_pathlists(root)               /* allpaths.c:333 */
rel = make_rel_from_joinlist(root, joinlist)   /* §6 */
```

- `set_base_rel_consider_startup`: for every SEMI/ANTI `sjinfo` whose
  `syn_righthand` is a single baserel, `rel->consider_param_startup = true`
  (parameterized inner of a semi/anti join stops after the first match).
  `consider_startup` itself is set in `build_simple_rel`/`build_join_rel` to
  `(root->tuple_fraction > 0)`.
- `set_base_rel_sizes`: for each `RELOPT_BASEREL`: if `glob->parallelModeOK`
  → `set_rel_consider_parallel` (§10), then `set_rel_size`.
- `set_rel_size` (allpaths.c:360) dispatch:

  | condition | call |
  |---|---|
  | `relation_excluded_by_constraints` | `set_dummy_rel_pathlist` (constraint_exclusion / partition pruning) |
  | `rte->inh` | `set_append_rel_size` (children sized recursively; partition pruning `prune_append_rel_partitions`) |
  | RTE_RELATION foreign | `set_foreign_size` (FDW `GetForeignRelSize`) |
  | RTE_RELATION partitioned (non-inh) | dummy |
  | tablesample | `set_tablesample_rel_size` |
  | plain RTE_RELATION | `set_plain_rel_size` = `check_index_predicates` + `set_baserel_size_estimates` |
  | RTE_SUBQUERY | `set_subquery_pathlist` (sizes **and** paths, see below) |
  | RTE_FUNCTION / TABLEFUNC / VALUES | `set_function_size_estimates` etc. |
  | RTE_CTE | `set_worktable_pathlist` (self-ref) / `set_cte_pathlist` |
  | RTE_NAMEDTUPLESTORE / RTE_RESULT | pathlist functions |

- `set_baserel_size_estimates` (costsize.c):
  `rel->rows = clamp_row_est(rel->tuples * clauselist_selectivity(baserestrictinfo, 0, JOIN_INNER, NULL))`;
  `cost_qual_eval(&rel->baserestrictcost, baserestrictinfo)`;
  `set_rel_width(root, rel)` (sum of `attr_widths` for `reltarget` Vars,
  from `pg_statistic.stawidth` / `get_typavgwidth`).
- `rel->tuples/pages/allvisfrac` come from `get_relation_info`
  (`util/plancat.c`) → `estimate_rel_size` → for heap
  `table_block_relation_estimate_size` (`access/table/tableam.c`):
  `curpages = RelationGetNumberOfBlocks(rel)`; if `curpages < 10 && reltuples < 0
  && !relhassubclass` → `curpages = 10`; `curpages == 0` → all zero;
  `density = reltuples/relpages` if `reltuples >= 0 && relpages > 0`, else
  `(usable_bytes_per_page*fillfactor/100) / (tuple_width + overhead)` with
  `tuple_width = get_rel_data_width` (attr widths); `tuples =
  clamp_row_est(density * curpages)`; `allvisfrac = 0 | 1 | relallvisible/curpages`.
  `get_relation_info` also fills `rel->rel_parallel_workers =
  RelationGetParallelWorkers(rel, -1)`, `indexlist` (skipping invalid
  indexes, `indcheckxmin` too-new indexes; `IndexOptInfo` with `pages`,
  `tuples`, `canreturn[]`, `sortopfamily[]`, `amcan*` flags, `indpred`,
  `predOK` set later by `check_index_predicates`), `statlist`,
  `notnullattnums`, partition info.
- `set_base_rel_pathlists` → `set_rel_pathlist` (allpaths.c:469): dummy →
  nothing; `inh` → `set_append_rel_pathlist`; RTE_RELATION →
  `set_foreign_pathlist` / `set_tablesample_rel_pathlist` /
  `set_plain_rel_pathlist`; FUNCTION/TABLEFUNC/VALUES pathlists; SUBQUERY,
  CTE, NAMEDTUPLESTORE, RESULT already done in size phase. Then
  `set_rel_pathlist_hook`, `generate_useful_gather_paths(root, rel, false)`
  if `RELOPT_BASEREL` and the rel is not the whole query, and `set_cheapest`.

- **`set_plain_rel_pathlist`** (allpaths.c:768):
  ```
  required_outer = rel->lateral_relids
  if (create_tidscan_paths(root, rel)) return;   /* PG18: true only for CURRENT OF */
  add_path(rel, create_seqscan_path(root, rel, required_outer, 0))
  if (rel->consider_parallel && required_outer == NULL) create_plain_partial_paths(root, rel)
  create_index_paths(root, rel)
  ```
  `create_tidscan_paths` (path/tidpath.c): `ctid = const`/`= ANY`/CURRENT OF
  quals → `TidPath` (added even if `!enable_tidscan` for CURRENT OF),
  `ctid` range quals → `TidRangePath`, plus parameterized TID paths from
  join clauses / ECs.

- **`create_index_paths`** (path/indxpath.c:241): for each index (skipping
  partial indexes whose `predOK` is false):
  1. `match_restriction_clauses_to_index` → `rclauseset`
     (`match_clause_to_indexcol`: opclause with the index column on one side
     and a pseudoconstant/outer-rel expression on the other; `ScalarArrayOp`
     `= ANY(array)`; `RowCompare`; boolean columns; support-function derived
     clauses `get_index_clause_from_support`).
  2. `get_index_paths(root, rel, index, &rclauseset, &bitindexpaths)` →
     `build_index_paths(..., ST_ANYSCAN)`; ordinary index paths are
     `add_path`'d, bitmap-capable ones collected.
  3. `match_join_clauses_to_index` (join clauses → `jclauseset` and OR
     clauses → `joinorclauses`) and `match_eclass_clauses_to_index`
     (`generate_implied_equalities_for_column`) → `eclauseset`; if either is
     non-empty `consider_index_join_clauses` → `consider_index_join_outer_rels`
     → `get_join_index_paths` builds **parameterized** index paths for each
     useful `required_outer` set (base rels supplying the other side).
  4. `generate_bitmap_or_paths` over `baserestrictinfo` (and over
     `joinorclauses` with baserestrictinfo as extra clauses).
  5. `choose_bitmap_and(root, rel, bitindexpaths)` picks a set of
     AND-combinable bitmap index paths by greedy cost (`path_usage_comparator`
     sorts by selectivity then cost; `bitmap_and_cost_est`), then
     `create_bitmap_heap_path` (unparameterized, `loop_count = 1`) and, if
     `consider_parallel && lateral_relids == NULL`, `create_partial_bitmap_paths`.
  6. Parameterized bitmap heap paths for each distinct `required_outer` among
     `bitjoinpaths` (`get_loop_count` estimates outer loops).

  **`build_index_paths`** (indxpath.c:811) per index and clause set:
  `outer_relids = lateral_relids ∪ clause outer rels − self`;
  `loop_count = get_loop_count(...)`; if the index is ordered
  (`sortopfamily != NULL`) and `has_useful_pathkeys(root, rel)`:
  `index_pathkeys = build_index_pathkeys(root, index, ForwardScanDirection)`
  then `useful_pathkeys = truncate_useless_pathkeys(root, rel, index_pathkeys)`;
  `amcanorderbyop` indexes use `match_pathkeys_to_index` against
  `query_pathkeys`. `index_only_scan = (scantype != ST_BITMAPSCAN) &&
  check_index_only(rel, index)`. A path is created if
  `index_clauses != NIL || useful_pathkeys != NIL || useful_predicate ||
  index_only_scan`, via `create_index_path` (and a partial version when
  `index->amcanparallel && rel->consider_parallel && outer_relids == NULL`);
  then the backward direction is tried if it yields useful pathkeys.
  `check_index_only` (indxpath.c:2229): false if `!enable_indexonlyscan`;
  attrs used by `reltarget->exprs` and by `index->indrestrictinfo` must be ⊆
  attrs the index `canreturn`.
  Parameterization: `create_index_path` → `get_baserel_parampathinfo(root,
  rel, required_outer)` (relnode.c:1545): `ppi_clauses` = movable join
  clauses (`join_clause_is_movable_into`) + `generate_join_implied_equalities`
  for `required_outer`; `ppi_rows = get_parameterized_baserel_size`;
  `ppi_serials`; cached in `rel->ppilist`. `PATH_REQ_OUTER(path)` =
  `param_info ? ppi_req_outer : NULL`; joinpath.c defines
  `PATH_PARAM_BY_REL(path, rel)` = param_info overlaps `rel->relids` (or its
  `top_parent_relids`).

- **Other rel kinds:** `set_subquery_pathlist` (allpaths.c:2529): pushes
  safe restriction quals into the subquery (`subquery_is_pushdown_safe`,
  `qual_is_pushdown_safe`, `subquery_push_qual`),
  `remove_unused_subquery_outputs`, plans it with
  `tuple_fraction = (parent has no LIMIT-ish needs) ? 0 : root->tuple_fraction`
  via `subquery_planner`, converts each sub-path into a `SubqueryScanPath`
  with `convert_subquery_pathkeys`, and partial paths when
  `consider_parallel`. Function/VALUES/TableFunc scans get one path each
  (plus lateral parameterization). `set_cte_pathlist` costs a `CteScan` from
  the CTE plan's cost. `set_append_rel_pathlist` → `add_paths_to_append_rel`
  (allpaths.c:1321): plain `Append` of children's `cheapest_total_path`;
  `Append` of `cheapest_startup_path`s when `consider_startup`;
  `generate_orderedappend_paths` (MergeAppend / ordered Append for
  `all_child_pathkeys` and `partition_pathkeys`); partial Append (workers =
  max of children's, or with `enable_parallel_append` `Max(…, fls(#children))`
  capped by `max_parallel_workers_per_gather`, `parallel_aware =
  enable_parallel_append`); mixed Parallel Append with non-partial children;
  parameterized Appends for each child parameterization. Partitionwise join
  (`enable_partitionwise_join`, `generate_partitionwise_join_paths`) reuses
  this for child joinrels.

- `generate_gather_paths` / `generate_useful_gather_paths`: §10.

---

## 6. Join search

`make_rel_from_joinlist(root, joinlist)` (allpaths.c): recursively converts
each joinlist item (RangeTblRef → base rel; sub-list → recursive result);
if one item, return it; else `root->initial_rels = initial_rels` and
`join_search_hook ?: (enable_geqo && levels_needed >= geqo_threshold ?
geqo(...) : standard_join_search(...))`. GEQO (`geqo/geqo_main.c`) is a
genetic search seeded by `geqo_seed`, sized by `geqo_effort/pool_size/
generations`, using `gimme_tree` → `make_join_rel`.

`standard_join_search(root, levels_needed, initial_rels)` (allpaths.c:3457):
```
join_rel_level[1] = initial_rels
for lev in 2..levels_needed:
    join_search_one_level(root, lev)
    for rel in join_rel_level[lev]:
        generate_partitionwise_join_paths(root, rel)
        if rel is not the whole query: generate_useful_gather_paths(root, rel, false)
        set_cheapest(rel)
return the single rel at join_rel_level[levels_needed]
```

`join_search_one_level(root, level)` (path/joinrels.c) has three phases:
1. For each rel of level−1: if it has `joininfo != NIL || has_eclass_joins ||
   has_join_restriction(root, rel)` → `make_rels_by_clause_joins(root, rel,
   joinrels[1], first_rel)` (joins with each initial rel that
   `have_relevant_joinclause` or `have_join_order_restriction`; at level 2
   only later initial rels to avoid duplicates); else
   `make_rels_by_clauseless_joins` (cartesian with every non-overlapping
   initial rel).
2. Bushy plans: for `k = 2..level/2`, pair every rel of level k with every
   rel of level−k that does not overlap and either
   `have_relevant_joinclause` or `have_join_order_restriction`.
3. Last resort: if nothing was produced, `make_rels_by_clauseless_joins` for
   every level−1 rel; error "failed to build any N-way joins" if still empty
   and there are no special joins/lateral refs.

`have_relevant_joinclause` (`util/joininfo.c`): some `joininfo` clause of
either rel references the other, or `have_relevant_eclass_joinclause`.
`have_join_order_restriction(root, rel1, rel2)` (joinrels.c:1066): true for
direct lateral references between them, PHVs whose `ph_eval_at` covers both,
or a non-FULL `sjinfo` whose `min_lefthand/min_righthand` are split across
the two rels or overlap both — unless `has_legal_joinclause` on either rel
(then normal clause-driven joining will find the order).

`make_join_rel(root, rel1, rel2)` (joinrels.c): `join_is_legal` (joinrels.c:350)
finds the unique `sjinfo` whose `min_lefthand ⊆ rel1 && min_righthand ⊆ rel2`
(or reversed), rejects joins that would split a `min_*` set, handles
`JOIN_SEMI` where `rel2 == syn_righthand` as *unique-ified* candidates,
forbids lateral references in both directions, requires `lhs_strict` when a
lower join must be a left join, etc. Then
`add_outer_joins_to_relids` (PG16), `build_join_rel` (relnode.c:665,
cached in `join_rel_hash` for ≥ ~32 rels / `join_rel_list`):
`consider_startup = tuple_fraction > 0`, `consider_param_startup = false`,
`reltarget` built by `build_joinrel_tlist` from both inputs restricted to
`attr_needed` above this join (with `varnullingrels` per PG16),
`lateral_relids = min_join_parameterization`, `restrictlist =
build_joinrel_restrictlist` (clauses from both `joininfo` lists whose
`required_relids ⊆ joinrelids` and not yet applied, plus
`generate_join_implied_equalities`), `joininfo` = remaining clauses,
`has_eclass_joins = has_relevant_eclass_joinclause`, partition info,
`set_joinrel_size_estimates` → `calc_joinrel_size_estimate` (costsize.c):
`fkselec * jselec` over join clauses (`clauselist_selectivity` with the
jointype and sjinfo) and `pselec` for pushed-down clauses:
INNER `outer*inner*fk*jselec`; LEFT `max(outer*inner*fk*jselec, outer)*pselec`;
FULL `max(that, outer, inner)*pselec`; SEMI `outer*fk*jselec`;
ANTI `outer*(1-fk*jselec)*pselec`; all `clamp_row_est`.
`consider_parallel = both inputs && is_parallel_safe(restrictlist) &&
is_parallel_safe(reltarget)`; `reltarget->width` = sum of input widths of
the kept Vars. The joinrel `rows` are the same for every path (paths of a
parameterized joinrel use `ppi_rows` from `get_joinrel_parampathinfo`).

`populate_joinrel_with_paths(root, rel1, rel2, joinrel, sjinfo, restrictlist)`
(joinrels.c) — jointype arms, each calling `add_paths_to_joinrel` (§7) with
both operand orders unless noted; dummy inputs / constant-false
`restriction_is_constant_false` short-circuit to `mark_dummy_rel`:

| sjinfo->jointype | calls |
|---|---|
| `JOIN_INNER` | `(rel1,rel2,JOIN_INNER)`, `(rel2,rel1,JOIN_INNER)` |
| `JOIN_LEFT` | `(rel1,rel2,JOIN_LEFT)`, `(rel2,rel1,JOIN_RIGHT)` (RIGHT exists only as a *path* jointype) |
| `JOIN_FULL` | both orders with `JOIN_FULL`; error if no path (needs merge/hash-joinable clause) |
| `JOIN_SEMI` | if `min_lefthand ⊆ rel1 && min_righthand ⊆ rel2`: `(rel1,rel2,JOIN_SEMI)`, `(rel2,rel1,JOIN_RIGHT_SEMI)` (**PG17**); additionally if `syn_righthand == rel2->relids && create_unique_path(rel2, cheapest_total, sjinfo) != NULL`: `(rel1,rel2,JOIN_UNIQUE_INNER)`, `(rel2,rel1,JOIN_UNIQUE_OUTER)` |
| `JOIN_ANTI` | `(rel1,rel2,JOIN_ANTI)`, `(rel2,rel1,JOIN_RIGHT_ANTI)` |

then `try_partitionwise_join`. `create_unique_path` (pathnode.c) builds a
`UniquePath` over the inner using sort+unique or hash-agg (`semi_can_btree`,
`semi_can_hash`, `enable_hashagg`, `hashentrysize*rows` vs
`get_hash_memory_limit()`; note this is a **hard veto** — it clears
`semi_can_hash` (pathnode.c:2023-2038) rather than adding `disabled_nodes`;
the `+1` rule applies in `create_setop_path`, not here), cached in
`rel->cheapest_unique_path`; it returns NULL when `semi_rhs_exprs` are not
unique-able.

---

## 7. add_paths_to_joinrel (path/joinpath.c:124)

```
JoinPathExtraData extra = { restrictlist, mergeclause_list=NIL, inner_unique,
                            sjinfo, semifactors, param_source_rels }
inner_unique: SEMI/ANTI -> false; UNIQUE_INNER -> min_lefthand ⊆ outerrel;
              UNIQUE_OUTER -> innerrel_is_unique(..., JOIN_INNER, ...);
              otherwise innerrel_is_unique(root, joinrel->relids, outerrel->relids,
                                           innerrel, jointype, restrictlist, false)
if enable_mergejoin || jointype == JOIN_FULL:
    extra.mergeclause_list = select_mergejoin_clauses(...)   /* also sets mergejoin_allowed */
if SEMI/ANTI or inner_unique: compute_semi_anti_join_factors(...) -> extra.semifactors
extra.param_source_rels = ∪ over sjinfo2 in join_info_list:
        (joinrelids overlaps sjinfo2->min_righthand and not min_lefthand)  -> all_baserels − min_righthand
        (FULL and overlaps min_lefthand and not min_righthand)             -> all_baserels − min_lefthand
    ∪ joinrel->lateral_relids
if mergejoin_allowed: sort_inner_and_outer(...)
if mergejoin_allowed: match_unsorted_outer(...)
if enable_hashjoin || jointype == JOIN_FULL: hash_inner_and_outer(...)
if joinrel->fdwroutine->GetForeignJoinPaths: call it
if set_join_pathlist_hook: call it
```

`param_source_rels` restricts which outer rels a parameterized inner path
may depend on: `try_nestloop_path` rejects `required_outer` that does not
overlap `param_source_rels` unless `allow_star_schema_join` (inner params
partly satisfied by the outer and partly from further out).

- **`sort_inner_and_outer`**: uses `cheapest_total_path` of each side
  (rejecting ones parameterized by the other side), unique-ifies for
  `JOIN_UNIQUE_*`, computes `all_pathkeys = select_outer_pathkeys_for_merge`
  (orderings of mergeclauses, favouring `query_pathkeys`), for each: outer
  sort keys, `find_mergeclauses_for_outer_pathkeys`, inner keys via
  `make_inner_pathkeys_for_merge`, then `try_mergejoin_path` with explicit
  sorts on both sides; partial merge joins (`try_partial_mergejoin_path`)
  when `joinrel->consider_parallel` and jointype not in
  {UNIQUE_OUTER, FULL, RIGHT, RIGHT_ANTI} with `cheapest_partial_outer` and a
  parallel-safe inner (`get_cheapest_parallel_safe_total_inner`).
- **`match_unsorted_outer`**: `nestjoinOK` false for RIGHT/RIGHT_ANTI/FULL
  (and RIGHT_SEMI is skipped entirely); `inner_cheapest_total` (NULL if
  parameterized by the outer); UNIQUE_INNER replaces it by
  `create_unique_path`; otherwise if `enable_material` and the inner does not
  already materialize (`ExecMaterializesOutput`) a `MaterialPath` over it is
  prepared. For every outer path (only cheapest total for UNIQUE_OUTER):
  `merge_pathkeys = build_join_pathkeys(outer pathkeys)`; nestloop with
  `inner_cheapest_total`; for `nestjoinOK`: nestloop with every
  `innerrel->cheapest_parameterized_paths` entry, plus
  `get_memoize_path` over each, plus the material path; then
  `generate_mergejoin_paths` (merge join using the outer's existing order,
  sorting/not sorting the inner; also tries inner paths that are already
  sorted, `cheapest_startup_path` variants, and truncated mergeclause lists
  when inner order is a prefix). Then, if `joinrel->consider_parallel` and
  jointype not in {UNIQUE_OUTER, FULL, RIGHT, RIGHT_ANTI, RIGHT_SEMI}:
  `consider_parallel_mergejoin` and `consider_parallel_nestloop` (§10).
- **`get_memoize_path`** (joinpath.c) returns a `MemoizePath` over the inner
  iff `enable_memoize`, `outer_path->parent->rows >= 2`, the inner is
  parameterized (`ppi_clauses != NIL`) or lateral, not (SEMI/ANTI without
  `inner_unique`), for `inner_unique` every restrictlist clause is enforced
  as a parameter clause (`ppi_serials`), no volatile functions in
  `innerrel->reltarget`, `baserestrictinfo` or `ppi_clauses`, and
  `paraminfo_get_equal_hashops` finds hashable equality for every parameter
  (`left/right_hasheqoperator`; `binary_mode` when the operator is not the
  type's default equality).
- **`hash_inner_and_outer`**: `hashclauses` = restrictlist entries with
  `hashjoinoperator` whose sides match the outer/inner split
  (`clause_sides_match_join`), excluding pushed-down clauses for outer
  joins; try `(cheapest_total_outer, cheapest_total_inner)`, for UNIQUE_*
  the unique-ified side, `(cheapest_startup_outer, cheapest_total_inner)`,
  and (non-unique cases) every pair from `outerrel->cheapest_parameterized_paths
  × innerrel->cheapest_parameterized_paths` that is not parameterized by the
  other rel. Partial hash joins when `consider_parallel` and jointype not
  {UNIQUE_OUTER, FULL, RIGHT, RIGHT_ANTI}: `cheapest_partial_outer` with
  either the cheapest parallel-safe total inner (non-parallel hash) or, when
  `enable_parallel_hash` and not UNIQUE_INNER, `cheapest_partial_inner`
  building a shared hash table (`parallel_hash = true` → `parallel_aware`).

**Two-stage costing.** Each `try_*_path` calls `initial_cost_nestloop` /
`initial_cost_mergejoin` / `initial_cost_hashjoin` (costsize.c:3267/3552/4160)
producing a `JoinCostWorkspace{startup_cost, total_cost, disabled_nodes,
run_cost, inner_run_cost, ...}` from cheap estimates (no qual costs, no
semi/anti factors), then `add_path_precheck(joinrel, workspace.disabled_nodes,
startup_cost, total_cost, pathkeys, required_outer)`; only if it can survive
is `create_*_path` called, which runs `final_cost_*` (full costing incl.
`cost_qual_eval` of join quals, `inner_unique`/`semifactors`, hash bucket
statistics `estimate_hash_bucket_stats`, mergejoin scan selectivities
`mergejoinscansel`, and `cost_rescan` of the inner for nestloop) and then
`add_path`. `try_nestloop_path` also derives `required_outer =
calc_nestloop_required_outer(...)` = outer params ∪ (inner params − outer
relids) and vetoes paths whose params include the join's own `ojrelid`.

`add_path_precheck` (pathnode.c): the new path is worthless (return false)
if some existing path has `disabled_nodes <= new` (strictly fewer → stop
scanning, fewer wins), `total_cost <= new*1.01` **and** either
`startup_cost <= new*1.01` or startup is not considered for that
parameterization, its pathkeys are ≥ the new ones, and the same
`required_outer`. Because `pathlist` is kept sorted by
`(disabled_nodes, total_cost)`, the scan stops at the first old path with
higher total cost.

---

## 8. Path model

**`Path`** (pathnodes.h):

| field | meaning |
|---|---|
| `pathtype` | `NodeTag` of the Plan node to build (`T_SeqScan`, `T_IndexScan`, `T_IndexOnlyScan`, `T_HashJoin`, …) — `IndexPath` covers both index and index-only scans |
| `parent` | owning `RelOptInfo` |
| `pathtarget` | `PathTarget{exprs, sortgrouprefs, cost, width, has_volatile_expr}` produced by this path (defaults to `parent->reltarget`) |
| `param_info` | `ParamPathInfo{ppi_req_outer, ppi_rows, ppi_clauses, ppi_serials}` or NULL |
| `parallel_aware` | node itself divides work among workers (Parallel Seq Scan, Parallel Hash, Parallel Append…) |
| `parallel_safe` | can run inside a worker |
| `parallel_workers` | planned worker count (0 for non-partial paths) |
| `rows` | estimated output rows (parent's `rows` or `ppi_rows`, except joins/upper paths compute their own) |
| `disabled_nodes` | **PG18**: count of disabled Plan nodes in the subtree; compared before cost everywhere |
| `startup_cost`, `total_cost` | in cost units |
| `pathkeys` | list of `PathKey` (canonical, pointer-equal) describing output order; NIL = unordered |

Subtypes (pathnodes.h, `} XxxPath;`): `IndexPath, BitmapHeapPath,
BitmapAndPath, BitmapOrPath, TidPath, TidRangePath, SubqueryScanPath,
ForeignPath, CustomPath, AppendPath, MergeAppendPath, GroupResultPath,
MaterialPath, MemoizePath, UniquePath, GatherPath, GatherMergePath, JoinPath
(NestPath, MergePath, HashPath), ProjectionPath, ProjectSetPath, SortPath,
IncrementalSortPath, GroupPath, UpperUniquePath, AggPath, GroupingSetsPath,
MinMaxAggPath, WindowAggPath, SetOpPath, RecursiveUnionPath, LockRowsPath,
ModifyTablePath, LimitPath`; plain `Path` is used for SeqScan, SampleScan,
FunctionScan, ValuesScan, TableFuncScan, CteScan, WorkTableScan,
NamedTuplestoreScan, Result. `JoinPath` adds `jointype, inner_unique,
outerjoinpath, innerjoinpath, joinrestrictinfo`; `IndexPath` adds
`indexinfo, indexclauses (IndexClause list), indexorderbys, indexorderbycols,
indexscandir, indextotalcost, indexselectivity`.

**`RelOptInfo` fields the planner reads** (pathnodes.h): `reloptkind`
(`RELOPT_BASEREL`, `RELOPT_JOINREL`, `RELOPT_OTHER_MEMBER_REL`,
`RELOPT_OTHER_JOINREL`, `RELOPT_UPPER_REL`, `RELOPT_OTHER_UPPER_REL`),
`relids`, `rows`, `consider_startup`, `consider_param_startup`,
`consider_parallel`, `reltarget`, `pathlist`, `ppilist`, `partial_pathlist`,
`cheapest_startup_path`, `cheapest_total_path`, `cheapest_unique_path`,
`cheapest_parameterized_paths`, `direct_lateral_relids`, `lateral_relids`,
`relid`, `reltablespace`, `rtekind`, `min_attr`, `max_attr`, `attr_needed[]`
(Relids per attno: which rels above need it; drives `use_physical_tlist` and
join tlists), `attr_widths[]`, `notnullattnums`, `nulling_relids`,
`lateral_vars`, `lateral_referencers`, `indexlist`, `statlist`, `pages`,
`tuples`, `allvisfrac`, `eclass_indexes`, `subroot`, `subplan_params`,
`rel_parallel_workers`, `amflags`, `serverid/userid/useridiscurrent/
fdwroutine/fdw_private`, `unique_for_rels`, `non_unique_for_rels` (cache
for `innerrel_is_unique`), `baserestrictinfo`, `baserestrictcost`,
`baserestrict_min_security`, `joininfo`, `has_eclass_joins`,
`consider_partitionwise_join`, `parent`, `top_parent`, `top_parent_relids`,
partition fields (`part_scheme, nparts, boundinfo, partbounds_merged,
partition_qual, part_rels, live_parts, all_partrels, partexprs,
nullable_partexprs`). `fkey_list` lives on `PlannerInfo`, not the rel.

**`add_path(parent_rel, new_path)`** (util/pathnode.c:464). For each old
path: `costcmp = compare_path_costs_fuzzily(new, old, STD_FUZZ_FACTOR=1.01)`:
```
disabled_nodes differ -> fewer wins (BETTER1/BETTER2)
new.total > old.total*1.01 -> if startup considered for new and old.startup > new.startup*1.01 : DIFFERENT else BETTER2
old.total > new.total*1.01 -> symmetric -> DIFFERENT or BETTER1
else startup: new.startup > old.startup*1.01 -> BETTER2; reverse -> BETTER1; else EQUAL
```
(`CONSIDER_PATH_STARTUP_COST(p)` = `parent->consider_startup` for
unparameterized paths, `parent->consider_param_startup` otherwise.)
If `costcmp != DIFFERENT`: `keyscmp = compare_pathkeys(new_keys, old_keys)`
where parameterized paths are treated as having NIL pathkeys; if
`keyscmp != PATHKEYS_DIFFERENT`:
- `COSTS_EQUAL`: `outercmp = bms_subset_compare(REQ_OUTER(new), REQ_OUTER(old))`.
  If new has better keys: remove old when `outercmp ∈ {EQUAL, SUBSET1}` and
  `new.rows <= old.rows` and `new.parallel_safe >= old.parallel_safe`. If old
  has better keys: reject new under the mirrored conditions. Equal keys and
  `outercmp == EQUAL`: prefer higher `parallel_safe`, then fewer `rows`,
  then `compare_path_costs_fuzzily(new, old, 1.0000000001) == BETTER1`
  removes old, otherwise old wins (ties keep the older path). Equal keys
  with SUBSET1/SUBSET2: dominance by fewer required outer rels + rows +
  parallel_safe.
- `COSTS_BETTER1`: remove old unless old has strictly better keys, when
  `outercmp ∈ {EQUAL, SUBSET1}`, `new.rows <= old.rows`, `parallel_safe >=`.
- `COSTS_BETTER2`: reject new symmetrically.
Insert position keeps `pathlist` ordered by `(disabled_nodes, total_cost)`
ascending; rejected/removed non-IndexPath nodes are `pfree`d (IndexPaths
may be shared by bitmap paths).

**`add_partial_path`** (pathnode.c): compares only `total_cost` (with the
same fuzz), `disabled_nodes`, and pathkeys; no parameterization (partial
paths are never parameterized), no rows tie-break; list ordered by
total_cost. Requires `new_path->parallel_safe` and `parent_rel->consider_parallel`.
`add_partial_path_precheck` exists analogously.

**`set_cheapest(rel)`** (pathnode.c): error if `pathlist == NIL`.
`cheapest_total_path` = unparameterized path with the lowest total cost
(`compare_path_costs` — which compares `disabled_nodes` first — with
pathkeys as tie-break via `compare_pathkeys`); `cheapest_startup_path`
likewise by startup; `cheapest_parameterized_paths` = [cheapest_total] ++
all parameterized paths (each is the cheapest for its `required_outer` by
construction of `add_path`); if no unparameterized path exists (possible for
lateral rels) `cheapest_total_path = best_param_path` (the parameterized path
with the smallest `required_outer` set, ties by total cost) and
`cheapest_startup_path = NULL`. `cheapest_unique_path = NULL` (lazy).

---

## 9. Pathkeys and ordering (path/pathkeys.c)

- `PathKey{pk_eclass, pk_opfamily, pk_cmptype (COMPARE_LT/COMPARE_GT for
  ASC/DESC, PG18 uses CompareType), pk_nulls_first}`; canonical via
  `make_canonical_pathkey` (interned in `root->canon_pathkeys`, so lists are
  compared by pointer). `pathkey_is_redundant`: a key whose EC
  `ec_has_const` or which already appears in the list is dropped
  (`make_pathkeys_for_sortclauses_extended` with `remove_redundant`).
- `make_pathkeys_for_sortclauses(root, sortclauses, tlist)`:
  `make_pathkey_from_sortop` → `get_eclass_for_sort_expr` (creates
  single-member ECs with `ec_sortref` as needed).
- `build_index_pathkeys(root, index, scandir)` (pathkeys.c:740): one pathkey
  per key column using `sortopfamily[i]`, `reverse_sort[i]`,
  `nulls_first[i]` flipped for `BackwardScanDirection`; stops at the first
  column whose expression has no EC usable by the query (`get_eclass_for_sort_expr`
  with `create_it=false`), and drops redundant keys.
- `truncate_useless_pathkeys(root, rel, pathkeys)` (pathkeys.c:2270): keeps
  the longest prefix useful for merging (`pathkeys_useful_for_merging`:
  each key's EC appears in a mergeable join clause of the rel), ordering
  (`pathkeys_useful_for_ordering`: prefix of `query_pathkeys` with
  `pathkeys_count_contained_in` – PG18 also counts partial prefixes to
  support incremental sort), grouping (`group_pathkeys`, any subset order),
  distinct (`distinct_pathkeys`), or set-op (`setop_pathkeys`).
- `has_useful_pathkeys(root, rel)`: `joininfo != NIL || has_eclass_joins ||
  group_pathkeys || query_pathkeys`.
- `pathkeys_contained_in(keys1, keys2)`: keys1 is a prefix of keys2;
  `compare_pathkeys` returns `PATHKEYS_EQUAL/BETTER1/BETTER2/DIFFERENT`;
  `pathkeys_count_contained_in` returns the common prefix length (the
  incremental-sort input).
- `build_join_pathkeys`: a nestloop/mergejoin output keeps the outer's
  pathkeys, truncated by `truncate_useless_pathkeys`; hash join output has
  NIL pathkeys (PG18: HashJoin paths are created with NIL); merge join
  output pathkeys = outer pathkeys.
- Incremental sort: any place that sorts checks `pathkeys_count_contained_in`
  and, if `presorted_keys > 0 && enable_incremental_sort`, uses
  `create_incremental_sort_path` (cost via `cost_incremental_sort`, which
  estimates group counts with `estimate_num_groups` over the presorted keys)
  instead of a full sort — `create_ordered_paths`, `generate_useful_gather_paths`,
  `add_paths_to_grouping_rel`, `create_window_paths`, `create_final_distinct_paths`,
  merge join inner/outer sorts (`try_mergejoin_path` via `create_sort_path`
  only, no incremental), and `apply_scanjoin_target_to_paths`.
- GROUP BY reordering (PG16+, `enable_group_by_reordering`):
  `get_useful_group_keys_orderings(root, path)` (pathkeys.c:467) returns the
  original `group_pathkeys` and, if the path's pathkeys match some
  permutation of the group keys (`group_keys_reorder_by_pathkeys`), that
  permuted ordering (only when `enable_incremental_sort` or all keys match);
  not applied with grouping sets. `adjust_group_pathkeys_for_groupagg`
  appends ordered-aggregate sort keys after group keys.
- DISTINCT reordering (PG18, `enable_distinct_reordering`):
  `get_useful_pathkeys_for_distinct` (planner.c) tries `distinct_pathkeys`
  permutations matching an input path's order (via
  `group_keys_reorder_by_pathkeys`) so a sorted input avoids a re-sort.

---

## 10. Parallel query in the planner

- `set_rel_consider_parallel(root, rel, rte)` (allpaths.c:589), called only
  when `glob->parallelModeOK`: false for temp tables, unsafe tablesample
  methods/args, FDWs without `IsForeignScanParallelSafe`, subqueries with
  LIMIT (`limit_needed`), unsafe function/VALUES expressions, TABLEFUNC,
  CTE, NAMEDTUPLESTORE; requires `is_parallel_safe(baserestrictinfo)` and
  `is_parallel_safe(reltarget->exprs)`. Join rels inherit (§6);
  upper rels set it from the input rel and target safety.
- `create_plain_partial_paths` (allpaths.c:806):
  `parallel_workers = compute_parallel_worker(rel, rel->pages, -1,
  max_parallel_workers_per_gather)`; if `> 0`,
  `add_partial_path(rel, create_seqscan_path(root, rel, NULL, parallel_workers))`
  (`parallel_aware = true`, `cost_seqscan` divides by `get_parallel_divisor`:
  `workers + (parallel_leader_participation ? 1 - 0.3*min(workers,4)… : 0)`
  formula in costsize.c:get_parallel_divisor — leader contributes
  `1 - 0.1*workers` capped at workers≤4).
- `compute_parallel_worker(rel, heap_pages, index_pages, max_workers)`
  (allpaths.c): if `rel->rel_parallel_workers != -1` (the `parallel_workers`
  reloption) use it; else return 0 for a `RELOPT_BASEREL` whose
  `heap_pages < min_parallel_table_scan_size` (default 1024 pages = 8MB) or
  `index_pages < min_parallel_index_scan_size` (64 pages = 512kB); otherwise
  `workers = 1 + ⌊log₃(pages / threshold)⌋` (loop: while `pages >=
  threshold*3` → workers++, threshold *= 3) for heap and index separately,
  taking the min when both are given; finally `min(workers, max_workers)`.
- Partial index scans: `build_index_paths` (§5) adds a partial
  `IndexPath` (`create_index_path(..., partial_path=true)`) when
  `amcanparallel && rel->consider_parallel && outer_relids == NULL`, workers
  from `compute_parallel_worker(rel, rand_heap_pages, index_pages, …)` in
  `cost_index`. Partial bitmap heap scans: `create_partial_bitmap_paths`
  with `compute_parallel_worker(rel, pages_fetched, -1, …)`.
- `generate_gather_paths(root, rel, override_rows)` (allpaths.c): if
  `partial_pathlist` non-empty: `Gather` over `linitial(partial_pathlist)`
  (cheapest, rows = `compute_gather_rows` = subpath rows ×
  `get_parallel_divisor`) and a `GatherMerge` over every partial path that
  has pathkeys. `generate_useful_gather_paths` additionally, for each
  ordering in `get_useful_pathkeys_for_relation` (query_pathkeys and
  join-useful EC orderings), sorts partial paths — full `Sort` only on the
  cheapest partial path, `IncrementalSort` on any partially-sorted one when
  `enable_incremental_sort` — and adds a `GatherMerge` on top. Called from
  `set_rel_pathlist`, `standard_join_search`, `apply_scanjoin_target_to_paths`
  and the grouping/ordering stages via `gather_grouping_paths`.
- Parallel joins (§7): `consider_parallel_nestloop` pairs every partial
  outer with each entry of `innerrel->cheapest_parameterized_paths` that is
  `parallel_safe` (unique-ifying for UNIQUE_INNER), a Memoize over it, and a
  Material over the cheapest total inner (`enable_material`);
  `consider_parallel_mergejoin` / `try_partial_mergejoin_path`; partial hash
  joins with a non-partial inner (`parallel_aware = false`) or a partial
  inner with `enable_parallel_hash` (`parallel_hash = true`, cost via
  `initial_cost_hashjoin(parallel_hash)` which uses the inner rows total
  `inner_rows_total` and a shared table).
- Partial aggregation: `create_partial_grouping_paths` (§3.6) with
  `AGGSPLIT_INITIAL_SERIAL` costs from `get_agg_clause_costs`; the final
  stage uses `AGGSPLIT_FINAL_DESERIAL` over Gather/Gather Merge of the
  partial rel. `create_partial_grouping_paths` (planner.c:7351) builds it and
  `add_paths_to_grouping_rel` (planner.c:7109) is the step that
  fills both `pathlist` (non-parallel partial agg, used by partitionwise and
  Parallel Append) and `partial_pathlist`.
- `apply_projection_to_path(root, rel, path, target)` (pathnode.c) modifies
  a path's target in place (and cost) and clears `parallel_safe` when the
  new target is not parallel safe; `create_projection_path` is used when a
  `Result` node may be needed. `apply_scanjoin_target_to_paths` drops
  `partial_pathlist` if the scan/join target is not parallel safe.
- `PlannedStmt.parallelModeNeeded` is what makes the executor enter parallel
  mode; it is set to true by `create_gather_path`/`create_gather_merge_path`
  and by the `debug_parallel_query` wrapper.

---

## 11. create_plan (plan/createplan.c) and set_plan_references (plan/setrefs.c)

`create_plan(root, best_path)` (createplan.c:337): `root->curOuterRels =
NULL; curOuterParams = NIL`; `plan = create_plan_recurse(root, best_path,
CP_EXACT_TLIST)`; the top plan's tlist is forced to `processed_tlist`
(`apply_tlist_labeling` for sortgrouprefs); `SS_attach_initplans`.

`create_plan_recurse(root, path, flags)` dispatch on `path->pathtype`:

| pathtype | builder |
|---|---|
| `T_SeqScan, T_SampleScan, T_IndexScan, T_IndexOnlyScan, T_BitmapHeapScan, T_TidScan, T_TidRangeScan, T_SubqueryScan, T_FunctionScan, T_TableFuncScan, T_ValuesScan, T_CteScan, T_WorkTableScan, T_NamedTuplestoreScan, T_ForeignScan, T_CustomScan` | `create_scan_plan` → per-kind `create_*scan_plan` |
| `T_HashJoin, T_MergeJoin, T_NestLoop` | `create_join_plan` |
| `T_Append` / `T_MergeAppend` | `create_append_plan` / `create_merge_append_plan` |
| `T_Result` | `create_projection_plan` (ProjectionPath), `create_minmaxagg_plan` (MinMaxAggPath), `create_group_result_plan` (GroupResultPath), or scan plan for RTE_RESULT |
| `T_ProjectSet` | `create_project_set_plan` |
| `T_Material` / `T_Memoize` | `create_material_plan` / `create_memoize_plan` |
| `T_Unique` | `create_upper_unique_plan` (UpperUniquePath) or `create_unique_plan` (UniquePath: sort+Unique or hash Agg) |
| `T_Gather` / `T_GatherMerge` | `create_gather_plan` / `create_gather_merge_plan` |
| `T_Sort` / `T_IncrementalSort` | `create_sort_plan` / `create_incrementalsort_plan` |
| `T_Group` / `T_Agg` | `create_group_plan` / `create_groupingsets_plan` or `create_agg_plan` |
| `T_WindowAgg`, `T_SetOp`, `T_RecursiveUnion`, `T_LockRows`, `T_ModifyTable`, `T_Limit` | corresponding `create_*_plan` |

`CP_*` flags (createplan.c:70): `CP_EXACT_TLIST` (must return exactly
`pathtarget`), `CP_SMALL_TLIST` (prefer narrow: used under Sort/Material/
Memoize/Gather), `CP_LABEL_TLIST` (needs sortgrouprefs: under Agg/Group/
Sort/Unique/WindowAgg), `CP_IGNORE_TLIST` (parent replaces it).
`use_physical_tlist(root, path, flags)` (createplan.c): returns true — the
scan returns the whole tuple (cheaper, no projection) — only for base rels
of kind RELATION/SUBQUERY/FUNCTION/TABLEFUNC/VALUES/CTE, not CustomPath, not
a BitmapHeapPath with an empty target, no system columns needed
(`attr_needed[i]` for i ≤ 0 empty), no PlaceHolderVars evaluated here but
needed above, for IndexOnlyScan all columns `canreturn`, and with
`CP_LABEL_TLIST` only if sortgroupref'd exprs are distinct plain Vars.
`create_scan_plan` then either uses `build_physical_tlist` or
`build_path_tlist`; quals are `order_qual_clauses` (cheapest first, security
levels respected) and `extract_actual_clauses` (pseudoconstant quals go to a
gating `Result`). Parameterized scans get `replace_nestloop_params` on quals.

- `create_indexscan_plan` (createplan.c:2999): builds
  `IndexScan`/`IndexOnlyScan` with `fix_indexqual_references` (index
  column side becomes `INDEX_VAR` references, commuting operators as
  needed, `indexqualorig` kept for recheck), `fix_indexorderby_references`,
  `indexorderdir`; index-only scans convert tlist Vars to `INDEX_VAR`
  (`set_indexonlyscan_references` in setrefs), qual clauses that are
  redundant with index quals are dropped (`is_redundant_with_indexclauses`).
- `create_bitmap_scan_plan` (createplan.c:3195): `create_bitmap_subplan`
  recursively builds `BitmapAnd`/`BitmapOr`/`BitmapIndexScan`; recheck quals
  = original index quals not implied; partial bitmap scans mark
  `bitmap_subplan_mark_shared`.
- `create_join_plan` → `create_nestloop_plan` (createplan.c): inner path is
  `reparameterize_path_by_child` if needed; outer plan is built first, then
  `root->curOuterRels ∪= outer relids` so that the inner plan's
  `replace_nestloop_params` (createplan.c:5036) turns Vars/PHVs of those
  rels into `PARAM_EXEC` Params (`assign_nestloop_param_var/placeholdervar`,
  recorded in `root->curOuterParams`), then `identify_current_nestloop_params`
  produces the `NestLoopParam` list attached to the `NestLoop`; join quals
  are `order_qual_clauses`d and split into `joinclauses` / `otherclauses`
  (pushed-down quals of an outer join, `extract_actual_join_clauses`).
- `create_mergejoin_plan` (createplan.c:4493): adds explicit `Sort` nodes
  (`make_sort_from_pathkeys`) when `outersortkeys`/`innersortkeys` set,
  `Material` on the inner when `materialize_inner`, computes
  `mergeFamilies/mergeCollations/mergeReversals/mergeNullsFirst` per clause
  from the pathkeys (`skip_mark_restore` when `inner_unique`).
- `create_hashjoin_plan` (createplan.c:4847): outer plan with `CP_SMALL_TLIST`
  if the inner is not the cheapest; the **skew optimization** is a planner
  decision: if there is exactly one hash clause and its outer side is a
  plain `Var` of a base relation with `RELKIND` table, `skewTable =
  rte->relid`, `skewColumn = varattno`, `skewInherit = rte->inh` are put on
  the `Hash` node so the executor can pre-seed skew buckets from MCV stats
  (`ExecHashBuildSkewHash`); batch/bucket sizing itself is executor side
  (`ExecChooseHashTableSize`, `get_hash_memory_limit`). `hashoperators`,
  `hashcollations`, `hashkeys` per clause; `parallel_aware` Hash gets
  `rows_total = inner_rows_total`.
- `create_gather_plan` (`single_copy=false`, `num_workers` from path,
  `rescan_param = assign_special_exec_param`), `create_gather_merge_plan`
  (sort columns from `prepare_sort_from_pathkeys`).
- `create_sort_plan` → subplan with `CP_SMALL_TLIST` → `make_sort_from_pathkeys`
  → `prepare_sort_from_pathkeys` (finds/adds tlist entries for each EC
  member) → `make_sort` (`disabled_nodes = lefttree + (enable_sort==false)`);
  `create_incrementalsort_plan` sets `nPresortedCols`. Bounded sort is *not*
  a plan-node property: the `Limit` above supplies the bound at execution
  (`ExecSetTupleBound`); the planner accounts for it only through
  `cost_sort(..., limit_tuples)` → `cost_tuplesort` (heap sort cost
  `2*output*log2(output)*comparison_cost` when `limit_tuples < tuples`).
- `create_agg_plan` / `create_group_plan`: `CP_LABEL_TLIST` subplan,
  `extract_grouping_cols/ops/collations`, `numGroups`, `aggstrategy`,
  `aggsplit`, `transitionSpace`. `create_groupingsets_plan` chains Agg nodes.
- `create_limit_plan` (createplan.c:2849): `make_limit` with
  `limitOffset/limitCount/limitOption`; for `WITH TIES` the unique columns
  come from `parse->sortClause`.
- `create_memoize_plan`: `param_exprs`, `hash_operators`, `collations`,
  `singlerow`, `binary_mode`, `est_entries`, `keyparamids`;
  `create_material_plan`; `create_setop_plan`; `create_unique_plan`;
  `create_modifytable_plan`; `create_lockrows_plan`.
- `copy_generic_path_info` copies `startup_cost, total_cost, plan_rows,
  plan_width, parallel_safe, disabled_nodes` from path to plan.

**`set_plan_references(root, plan)`** (setrefs.c:288): `rtoffset =
len(glob->finalrtable)`; `add_rtes_to_flat_rtable` (flattens this level's
rtable into `glob->finalrtable`, strips subquery bodies, collects
`relationOids`), row marks and append-rel infos are offset and appended,
then `set_plan_refs(root, plan, rtoffset)` recursively: scan nodes get
`scanrelid += rtoffset` and `fix_scan_expr` (adjusts varnos, replaces
`PARAM_MULTIEXPR`, `fix_expr_common` records function dependencies and
`Aggref`/`OpExpr` funcids, `inline`-ready `SubPlan` handling); join nodes
`set_join_references` → `fix_join_expr`, replacing Vars by `OUTER_VAR`(−2)
/ `INNER_VAR`(−1) references into the child tlists (`build_tlist_index`,
`search_indexed_tlist_for_var`), `nestParams` fixed, hash clauses split
into outer/inner keys (`set_hash_references`); upper nodes (Agg, Group,
Sort, Unique, WindowAgg, Result, Limit, …) `set_upper_references` →
`fix_upper_expr` (`OUTER_VAR` into the single child) or
`set_dummy_tlist_references` for pass-through nodes; `IndexOnlyScan` →
`set_indexonlyscan_references` (`INDEX_VAR`); Append/MergeAppend children
and `part_prune_info` offsets; `SubqueryScan` with a trivial projection is
removed (`trivial_subqueryscan`, `clean_up_removed_plan_level`); `SubPlan`
params, `AlternativeSubPlan` selection (`fix_alternative_subplan` chooses by
cost per `num_exec`), `plan_node_id = glob->lastPlanNodeId++`. Var
references to `ROWID_VAR`(−4) are resolved for ModifyTable.

---

## 12. Planner GUCs (PG 18 defaults, `utils/misc/guc_tables.c`)

`enable_*` (all `true` unless noted): `enable_seqscan, enable_indexscan,
enable_indexonlyscan, enable_bitmapscan, enable_tidscan, enable_sort,
enable_incremental_sort, enable_hashagg, enable_material, enable_memoize,
enable_nestloop, enable_mergejoin, enable_hashjoin, enable_gathermerge,
enable_partitionwise_join (false), enable_partitionwise_aggregate (false),
enable_parallel_append, enable_parallel_hash, enable_partition_pruning,
enable_presorted_aggregate, enable_async_append, enable_self_join_elimination
(PG18), enable_group_by_reordering, enable_distinct_reordering (PG18)` — 24.

Other GUCs read by this pipeline:

| GUC | default | consumer |
|---|---|---|
| `from_collapse_limit` | 8 | `deconstruct_recurse` FromExpr merge |
| `join_collapse_limit` | 8 | `deconstruct_recurse` JoinExpr merge (1 = preserve explicit join order) |
| `geqo` | on; `geqo_threshold` 12; `geqo_effort` 5; `geqo_pool_size` 0; `geqo_generations` 0; `geqo_selection_bias` 2.0; `geqo_seed` 0.0 | `make_rel_from_joinlist`, `geqo/*` |
| `max_parallel_workers_per_gather` | 2 | parallelModeOK, `compute_parallel_worker` cap, Append workers |
| `max_parallel_workers` | 8 | executor cap only |
| `min_parallel_table_scan_size` | 8MB (1024 blocks) | `compute_parallel_worker` |
| `min_parallel_index_scan_size` | 512kB (64 blocks) | `compute_parallel_worker` |
| `parallel_leader_participation` | on | `get_parallel_divisor` |
| `parallel_setup_cost` / `parallel_tuple_cost` | 1000 / 0.1 | `cost_gather`, `cost_gather_merge`, debug Gather |
| `seq_page_cost` / `random_page_cost` | 1.0 / 4.0 | `cost_seqscan`, `cost_index` (`genericcostestimate`/`btcostestimate`), `cost_bitmap_heap_scan`, `cost_tuplesort` |
| `cpu_tuple_cost` / `cpu_index_tuple_cost` / `cpu_operator_cost` | 0.01 / 0.005 / 0.0025 | all cost functions |
| `effective_cache_size` | 524288 pages (4GB) | `index_pages_fetched` (Mackert–Lohman) |
| `work_mem` / `hash_mem_multiplier` | 4096 kB / 2.0 | `cost_tuplesort` (`sort_mem`), `cost_agg` spill, `initial_cost_hashjoin` batches (`get_hash_memory_limit`), `cost_memoize_rescan`, `create_unique_path` |
| `cursor_tuple_fraction` | 0.1 | `standard_planner` |
| `constraint_exclusion` | partition | `relation_excluded_by_constraints` |
| `recursive_worktable_factor` | 10.0 | `set_worktable_pathlist` size |
| `default_statistics_target` | 100 | ANALYZE (stat depth used by selectivity) |
| `plan_cache_mode` | auto | `plancache.c` (`choose_custom_plan`: generic after 5 custom plans if not costlier) — outside the planner proper |
| `jit`, `jit_above_cost` 100000, `jit_optimize_above_cost` 500000, `jit_inline_above_cost` 500000 | | `standard_planner` jitFlags |
| `debug_parallel_query` | off | `standard_planner`, `query_planner` RESULT fast path |

**How `enable_*` work in PG 18 (`disabled_nodes`).** There is no
`disable_cost` addition in most paths — the one surviving use is in
`final_cost_hashjoin` (`costsize.c:4421`). A disabled node contributes `+1` to
`Path.disabled_nodes`, propagated up through children, and `add_path`,
`add_path_precheck`, `add_partial_path`, `compare_path_costs`,
`compare_path_costs_fuzzily`, `compare_fractional_path_costs` all order by
`disabled_nodes` **before** looking at cost — so a plan with fewer disabled
nodes always wins regardless of cost, and a disabled path is still generated
(it can be the only way to satisfy the query). Increment sites:
`cost_seqscan` (`enable_seqscan ? 0 : 1`), `cost_index`
(`enable_indexscan ? 0 : 1`; index-only is gated at path generation by
`check_index_only`, not costed), `cost_bitmap_heap_scan` (`enable_bitmapscan`),
`cost_tidscan` (asserts `enable_tidscan`; TID paths are simply not built
except CURRENT OF), `cost_sort` (`input + (enable_sort ? 0 : 1)`; `make_sort`
mirrors it on the Plan), `cost_incremental_sort` (asserts enabled — not built),
`cost_material` (`enable_material`), `cost_gather_merge` (`enable_gathermerge`),
`cost_agg` (`!enable_hashagg` for `AGG_HASHED` and `AGG_MIXED`),
`create_setop_path` (hashed setop `!enable_hashagg`, plus +1 when the hash
table would exceed `get_hash_memory_limit()`); `create_unique_path` applies the
same memory rule as a **hard veto** (it clears `semi_can_hash` and can return
NULL) rather than as a `disabled_nodes` increment. `initial_cost_nestloop` (`enable_nestloop ? 0 : 1` +
children), `initial_cost_mergejoin` (`enable_mergejoin ? 0 : 1` + children
incl. explicit sorts), `initial_cost_hashjoin` (`enable_hashjoin ? 0 : 1` +
children). Path-generation gates (no path at all): `enable_memoize`
(`get_memoize_path`), `enable_material` (Material over inner nestloop,
subplan materialization), `enable_mergejoin`/`enable_hashjoin` skip the
generator unless the join is FULL, `enable_geqo`, `enable_parallel_append`,
`enable_parallel_hash`, `enable_partitionwise_*`, `enable_partition_pruning`
(createplan `make_partition_pruneinfo`), `enable_incremental_sort`,
`enable_presorted_aggregate`, `enable_async_append`,
`enable_self_join_elimination`, `enable_group_by_reordering`,
`enable_distinct_reordering`. `Plan.disabled_nodes` is copied to the plan
(`copy_generic_path_info`) and printed by `EXPLAIN` as "Disabled Nodes"
(PG18: `Disabled: true` per node).

---

## 13. Reimplementation checklist

Each item is one testable PG behaviour; the citation is where to verify it.

**Entry / global**
1. `tuple_fraction` from cursor options: fast-plan cursors use
   `cursor_tuple_fraction` (≥1 → 0, ≤0 → 1e-10), else 0. (planner.c:standard_planner)
2. `parallelModeOK` requires SELECT, no modifying CTE,
   `max_parallel_workers_per_gather > 0`, `CURSOR_OPT_PARALLEL_OK`, no
   PROPARALLEL_UNSAFE function (`max_parallel_hazard`). (standard_planner)
3. Final path chosen by `get_cheapest_fractional_path`: fraction ≥1 is
   divided by `cheapest_total_path->rows`; parameterized paths excluded;
   `startup + f*(total-startup)` comparison after `disabled_nodes`. (planner.c:6617, pathnode.c:compare_fractional_path_costs)
4. Scrollable cursor whose plan cannot scan backwards gets a top `Material`. (standard_planner)
5. JIT flags computed from `top_plan->total_cost` thresholds. (standard_planner)

**subquery_planner preprocessing order**
6. Exact order: MERGE→join, CTEs, empty-jointree Result, sublink pull-up,
   function RTE inlining, virtual generated cols, subquery pull-up, UNION ALL
   flattening, row marks, expression preprocessing, HAVING→WHERE move,
   reduce_outer_joins, remove_useless_result_rtes, grouping_planner. (planner.c:651-1240)
7. CTE inlining rule: not-materialized-explicitly or (default and refcount
   ==1), non-recursive SELECT, no DML, no outer self-ref if refcount>1, no
   volatile functions; else planned once as an initplan `CTE_SUBLINK` with
   tuple_fraction 0. (subselect.c:SS_process_ctes)
8. `ANY` sublink → semi join with LATERAL subquery when testexpr has
   upper vars ⊆ available rels, no volatile functions. (subselect.c:convert_ANY_sublink_to_join)
9. `EXISTS`/`NOT EXISTS` → SEMI/ANTI join only after
   `simplify_EXISTS_query` (constant LIMIT>0 dropped; no aggs/setops/…);
   WHERE hoisted one level. (subselect.c:convert_EXISTS_sublink_to_join)
10. Uncorrelated `ANY` sublink uses a hashed subplan when
    `subplan_is_hashable` / `testexpr_is_hashable`; EXISTS may be rewritten
    to ANY for hashing. (subselect.c:build_subplan, convert_EXISTS_to_ANY)
11. Subquery pull-up conditions of `is_simple_subquery` (no aggs, window,
    SRFs, GROUP/HAVING/ORDER/DISTINCT/LIMIT/FOR UPDATE/CTE, not security
    barrier, lateral-safe, no volatile tlist). (prepjointree.c:1807)
12. Simple `UNION ALL` in FROM becomes an appendrel; top-level UNION ALL
    tree is flattened into an appendrel. (prepjointree.c:pull_up_simple_union_all, flatten_simple_union_all)
13. `preprocess_expression`: flatten join aliases → `eval_const_expressions`
    → `canonicalize_qual` (quals) → hashed SAOP → sublinks → correlation
    vars → implicit-AND. (planner.c:preprocess_expression)
14. HAVING clauses without aggregates/volatile/subplans move to WHERE
    (copied to both when there is no GROUP BY). (planner.c:1130-1195)
15. `reduce_outer_joins` converts LEFT/FULL to INNER (and RIGHT to LEFT)
    using strict WHERE quals on the nullable side. (prepjointree.c:3102)
16. `RTE_RESULT` removed when joined with anything and no quals depend on it. (prepjointree.c:3596)
17. `preprocess_minmax_aggregates` turns ungrouped MIN/MAX into
    `LIMIT 1` ordered index scans when cheaper. (planagg.c:73)

**grouping_planner**
18. `preprocess_limit`: constant LIMIT/OFFSET → `limit_tuples`, unknown →
    10% fraction; merge rules with the cursor fraction. (planner.c:preprocess_limit)
19. Upper-rel order GROUP_AGG → WINDOW → DISTINCT → ORDERED → FINAL
    (LockRows → Limit → ModifyTable), each stage reading the previous rel's
    `pathlist` (and `partial_pathlist`). (planner.c:1434-1990)
20. PathTargets: `make_group_input_target`, `make_window_input_target`,
    `make_sort_input_target` (postpone volatile/expensive/SRF exprs past the
    Sort), `apply_scanjoin_target_to_paths` re-projects scan/join paths and
    re-runs gather generation. (planner.c)
21. Grouping strategy flags: sort allowed if groupClause sortable or empty;
    hash allowed only with a non-empty groupClause, no ordered aggregates,
    hashable clause; partial agg if no non-partial/non-serial aggs and no
    grouping sets. (planner.c:create_grouping_paths, can_partial_agg)
22. Sorted grouping is tried on every input path with every useful GROUP BY
    key ordering, inserting Sort/IncrementalSort as needed; hashed grouping
    on the cheapest path only; hash memory overflow is costed, not vetoed. (planner.c:add_paths_to_grouping_rel, costsize.c:cost_agg)
23. `create_final_distinct_paths`: Unique over sorted inputs (all paths),
    HashAggregate on the cheapest unless DISTINCT ON or `!enable_hashagg`;
    `numDistinctRows` via `estimate_num_groups`. (planner.c)
24. `create_ordered_paths`: sort only the cheapest total path (full sort) or
    any partially-sorted path (incremental); `limit_tuples` bounds sort
    cost; Gather Merge over sorted partial paths. (planner.c)
25. `final_rel->consider_parallel` depends on LIMIT expressions being
    parallel safe; partial paths propagate to FINAL. (planner.c:1860-1880)

**query_planner**
26. Single `RTE_RESULT` fast path with `GroupResultPath`. (planmain.c:90-130)
27. Order: add_base_rels → remove_useless_groupby_columns →
    build_base_rel_tlists → placeholders → lateral refs →
    deconstruct_jointree → reconsider_outer_join_clauses →
    generate_base_implied_equalities → qp_callback → remove_useless_joins →
    reduce_unique_semijoins → remove_useless_self_joins → lateral join info
    → FK matching → OR-clause extraction → other rels → make_one_rel. (planmain.c:54-300)
28. `attr_needed[attno]` per base rel = set of relids above that need the
    column; drives join tlists and `use_physical_tlist`. (initsplan.c:add_vars_to_targetlist)
29. `from_collapse_limit`: FromExpr children merged when total ≤ limit;
    `join_collapse_limit`: JoinExpr sides merged when `len(l)+len(r) ≤ limit`;
    FULL JOIN never merged. (initsplan.c:1216-1250, 1418-1445)
30. `SpecialJoinInfo.min_lefthand/min_righthand` computed by
    `make_outerjoininfo` with the OJ identities; `lhs_strict` from
    `find_nonnullable_rels`; `semi_*` from `compute_semijoin_info`. (initsplan.c:1708)
31. PG16 outer-join relids: OJs have RT indexes, joinrel relids include
    performed OJs, Vars carry `varnullingrels`, clauses may be cloned for
    commuted orders. (initsplan.c:deconstruct_distribute_oj_quals, joinrels.c:add_outer_joins_to_relids)
32. `distribute_qual_to_rels`: pseudoconstant quals, `is_pushed_down`,
    `required_relids`/`ojscope`, `check_mergejoinable` → EC absorption via
    `process_equivalence`, outer-join clause lists. (initsplan.c:2545)
33. `distribute_restrictinfo_to_rels`: single rel → `baserestrictinfo`;
    multi-rel → `check_hashjoinable`, `check_memoizable`, `joininfo` of each
    member. (initsplan.c:3227)
34. EC rules: mergejoinable equality only, no volatile members merged,
    constants keyed by join domain, `ec_sortref`; base implied equalities
    `x = const` become base restrictions on each rel; join implied
    equalities generated per joinrel and per parameterization; broken ECs
    fall back to source clauses. (equivclass.c:179, 1188, 1550)
35. `reconsider_outer_join_clauses` replaces OJ clauses with EC-derived
    constant comparisons. (equivclass.c:2135)
36. Left-join removal (`remove_useless_joins`): inner side unique on join
    clauses and unused above. Semi→inner (`reduce_unique_semijoins`).
    PG18 self-join elimination when `enable_self_join_elimination`. (analyzejoins.c:90, 844, 2488)
37. Foreign-key matching feeds `get_foreign_key_join_selectivity`
    (`1/referenced rows` instead of per-column products). (initsplan.c:3631, costsize.c)
38. `extract_restriction_or_clauses`: derived single-rel OR restrictions
    from join ORs. (orclauses.c)
39. `standard_qp_callback` pathkeys and the `query_pathkeys` preference
    order group → window → longer of distinct/sort → setop. (planner.c:3453)

**Base rel sizing & scan paths**
40. `consider_startup = tuple_fraction > 0`; `consider_param_startup` only
    for single-rel RHS of SEMI/ANTI. (relnode.c:build_simple_rel, allpaths.c:set_base_rel_consider_startup)
41. `estimate_rel_size`: `curpages` from the file, `<10 pages && reltuples<0`
    → assume 10, density from `reltuples/relpages` else from tuple width and
    fillfactor, `allvisfrac`. (tableam.c:table_block_relation_estimate_size)
42. `rel->rows = clamp_row_est(tuples * clauselist_selectivity(baserestrictinfo))`,
    `baserestrictcost`, width from `attr_widths`. (costsize.c:set_baserel_size_estimates)
43. Constraint exclusion / partition pruning marks rels dummy before
    pathing. (allpaths.c:set_rel_size)
44. Plain rel paths in order: TID paths (CURRENT OF short-circuits),
    SeqScan (parameterized by `lateral_relids`), partial SeqScan, index
    paths. (allpaths.c:768)
45. `create_index_paths`: restriction clauses → index/bitmap paths; join
    and EC clauses → parameterized index paths per outer-rel set; OR clauses
    → BitmapOr; `choose_bitmap_and` greedy AND selection; parameterized
    bitmap heap paths per distinct `required_outer`. (indxpath.c:241)
46. `build_index_paths`: path created only if there are index clauses,
    useful pathkeys, a useful predicate, or index-only eligibility; forward
    and backward directions; `amcanorderbyop` ordering; partial index path
    when `amcanparallel`. (indxpath.c:811)
47. `check_index_only`: `enable_indexonlyscan` and all attrs of the target
    and `indrestrictinfo` ⊆ `canreturn` columns. (indxpath.c:2229)
48. Partial index used only when `predOK` (`predicate_implied_by`
    restriction clauses). (indxpath.c:check_index_predicates)
49. `ParamPathInfo` for a base rel: movable join clauses + join implied
    equalities; `ppi_rows = get_parameterized_baserel_size`; cached per
    `required_outer`. (relnode.c:1545)
50. `use_physical_tlist` / `truncate_useless_pathkeys` / `has_useful_pathkeys`
    semantics as in §9 and §11. (pathkeys.c:2270, 2319; createplan.c)
51. Subquery RTE: qual pushdown when safe, unused outputs removed, planned
    with the parent's tuple_fraction only when no upper processing above it,
    pathkeys converted, partial paths preserved. (allpaths.c:2529)
52. Append rel: cheapest-total Append, cheapest-startup Append when
    `consider_startup`, MergeAppend/ordered Append for useful orderings,
    partial and mixed Parallel Append with the worker formula, parameterized
    Appends. (allpaths.c:1321)
53. `generate_useful_gather_paths` after each base rel and join level (not
    for the top-level joinrel, which is gathered in the upper stages). (allpaths.c:set_rel_pathlist, standard_join_search)

**Join search**
54. Dynamic programming by level with the three phases (clause-driven
    left-deep, bushy, clauseless last resort); GEQO at
    `levels_needed >= geqo_threshold`. (allpaths.c:make_rel_from_joinlist, joinrels.c:join_search_one_level)
55. `have_relevant_joinclause` / `have_join_order_restriction` gating;
    `join_is_legal` with `min_lefthand/min_righthand`, unique-ified SEMI,
    lateral direction rules. (joinrels.c:350, 1066; joininfo.c)
56. `build_join_rel`: joinrel cache, `reltarget` from `attr_needed`,
    `restrictlist` = applicable joininfo + join implied equalities,
    remaining `joininfo`, `consider_parallel` from inputs + clause/target
    safety, `rows` via `calc_joinrel_size_estimate` per jointype formulas. (relnode.c:665, costsize.c)
57. `populate_joinrel_with_paths` jointype arms including `JOIN_RIGHT`,
    `JOIN_RIGHT_SEMI` (PG17), `JOIN_RIGHT_ANTI`, `JOIN_UNIQUE_INNER/OUTER`
    via `create_unique_path`; FULL JOIN error without merge/hash clause;
    dummy propagation and constant-false detection. (joinrels.c)
58. Partitionwise join attempted after the normal arms. (joinrels.c:try_partitionwise_join)

**add_paths_to_joinrel**
59. `inner_unique` determination per jointype (`innerrel_is_unique`,
    cached in `unique_for_rels`). (joinpath.c:124)
60. `param_source_rels` rule and `allow_star_schema_join`. (joinpath.c)
61. Merge join generation: `select_outer_pathkeys_for_merge` orderings,
    sort both sides; `match_unsorted_outer` uses existing outer order with
    sorted/unsorted inner variants and truncated clause lists. (joinpath.c:sort_inner_and_outer, generate_mergejoin_paths)
62. Nested loop generation: inner cheapest total, every
    `cheapest_parameterized_paths` inner, Memoize over parameterized inner
    (conditions of `get_memoize_path`), Material over the inner when
    `enable_material` and not already materializing. (joinpath.c:match_unsorted_outer)
63. Hash join generation: hash clauses selection, `(total,total)`,
    `(startup,total)`, all parameterized pairs; partial hash join with
    non-partial or partial (Parallel Hash) inner. (joinpath.c:hash_inner_and_outer)
64. Two-stage costing with `initial_cost_*` + `add_path_precheck` before
    `final_cost_*` + `add_path`. (joinpath.c:try_*_path, costsize.c)
65. Nestloop `required_outer = calc_nestloop_required_outer` and the
    `ojrelid`-in-params veto. (joinpath.c:try_nestloop_path)

**Path bookkeeping**
66. `add_path` dominance exactly as §8 (fuzz 1.01, disabled_nodes first,
    pathkeys, `bms_subset_compare` on required outer, rows, parallel_safe,
    1e-10 tie-break, ordered insertion, old path kept on exact tie). (pathnode.c:464)
67. `add_partial_path` uses total cost + pathkeys only. (pathnode.c)
68. `set_cheapest`: cheapest total/startup among unparameterized paths with
    pathkeys tie-break; `cheapest_parameterized_paths` = cheapest total +
    all parameterized; parameterized-only rels fall back to the smallest
    `required_outer`. (pathnode.c:set_cheapest)
69. Paths of a rel share `rel->rows` (or `ppi_rows`); joins do not
    re-estimate rows per path. (costsize.c, relnode.c)

**Pathkeys**
70. Canonical `PathKey` interning; redundant keys (constant EC or
    duplicate) dropped; `build_index_pathkeys` stops at the first
    non-EC column; `build_join_pathkeys` keeps the outer order truncated
    to useful keys; hash join has no order. (pathkeys.c)
71. Incremental sort used wherever a prefix is presorted and
    `enable_incremental_sort`, with the "only the cheapest path gets a full
    sort" rule in gather/ordered stages. (allpaths.c:generate_useful_gather_paths, planner.c:create_ordered_paths)
72. GROUP BY key reordering to match input order; DISTINCT reordering
    (PG18). (pathkeys.c:467, planner.c:get_useful_pathkeys_for_distinct)

**Parallel**
73. `set_rel_consider_parallel` exclusions (temp tables, LIMIT subqueries,
    CTEs, unsafe exprs). (allpaths.c:589)
74. `compute_parallel_worker` log₃ ladder with `min_parallel_*_scan_size`
    thresholds, `parallel_workers` reloption override, cap by
    `max_parallel_workers_per_gather`. (allpaths.c:compute_parallel_worker)
75. Gather over the cheapest partial path; Gather Merge over every ordered
    partial path and over sorted partial paths for useful orderings. (allpaths.c:generate_gather_paths, generate_useful_gather_paths)
76. Partial aggregation (`AGGSPLIT_INITIAL_SERIAL` / `FINAL_DESERIAL`)
    through `UPPERREL_PARTIAL_GROUP_AGG`; `parallelModeNeeded` set by
    Gather path creation. (planner.c:create_partial_grouping_paths, pathnode.c:create_gather_path)

**create_plan / setrefs**
77. `CP_*` flag propagation and `use_physical_tlist` conditions. (createplan.c:559, use_physical_tlist)
78. Quals ordered by `order_qual_clauses` (cost, security level);
    pseudoconstant quals in a gating Result. (createplan.c)
79. Nestloop params: outer Vars/PHVs referenced by the inner become
    `PARAM_EXEC` with `NestLoopParam` entries; parameterized index quals
    reference them. (createplan.c:replace_nestloop_params, identify_current_nestloop_params)
80. Index scan plan: `fix_indexqual_references` (INDEX_VAR, commuted
    operators, `indexqualorig`), redundant quals dropped; bitmap plans with
    recheck quals. (createplan.c:2999, 3195)
81. Hash join plan: single-clause plain-Var outer side → skew table/column;
    merge join plan: explicit sorts, inner Material, per-clause families. (createplan.c:4847, 4493)
82. `set_plan_references`: flat rtable with `rtoffset`, OUTER_VAR/INNER_VAR/
    INDEX_VAR/ROWID_VAR rewriting, trivial SubqueryScan removal,
    AlternativeSubPlan choice, `plan_node_id` assignment, dependency
    collection. (setrefs.c:288, 619)
83. `Plan.disabled_nodes`, costs, rows, width, `parallel_safe` copied from
    the path. (createplan.c:copy_generic_path_info)

**GUC semantics**
84. All 24 `enable_*` GUCs exist with PG 18 defaults; disabled nodes are
    counted in `disabled_nodes` rather than priced with `disable_cost` (whose
    one surviving use is `final_cost_hashjoin`, costsize.c:4421), and
    compared before cost at every path comparison. (guc_tables.c, costsize.c, pathnode.c)
85. `join_collapse_limit = 1` preserves explicit JOIN order;
    `from_collapse_limit` limits FROM-list flattening; GEQO parameters. (initsplan.c, allpaths.c)
