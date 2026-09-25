# M0144-0003 — Route-order verification (route-diff table)

Owner directive 2026-09-20: "fixes landed but plans didn't move because the
processing order differs from PG" — verified item by item. Verdict:
**4 confirmed ordering/route divergences (a, b, d, e — with e the same root
as a), 1 refuted (c — a gated-off arm at the correct position, not ordering).**

## Route-diff table

| item | verdict | goopg route position | PG route position (oracle citation) |
|---|---|---|---|
| (a) `M0142-0008-producer` SJInfo inert — every IN statement declines `leaf-count` | **ORDERING DIVERGENCE (level)** | Plan-level pull-up: `unnestSubqueriesInPlan` (unnest.go:424) rewrites `Filter(IN/EXISTS, chain)` → `Filter{retained}(Semi(Filter{sunk}(origChain), rhs))` — `unnestInExpr` tail `filter.Child = join` (unnest.go:3498), sunk conjuncts via `pushConjunctsBelowSemiAnti` (unnest.go:372). Phase B searches `spineJoins[0]` (predp.go:197); `extractSearchLeaves` treats the sunk `*Filter` inside `Semi.Left` as ONE opaque leaf (joinsearchseam.go:1456-1461) → `len(scans)=1+nSemi ≠ nprefix+nSemi` → decline at :325 BEFORE `semiAntiLinksHaveSJInfos` (:1889). Corpus-measured: `leaf-count×62`, zero semianti-class declines; Q56 witness `nrels=4 nleaves=2` (producer doc §4.1). | Jointree-level pull-up: `pull_up_sublinks` (prepjointree.c:468) called from `subquery_planner` (planner.c:737) BEFORE `query_planner`; converts ANY/EXISTS sublinks into `JoinExpr`+`SpecialJoinInfo` in `join_info_list`. `deconstruct_jointree` distributes leftover conjuncts as base-rel restrict clauses — no Filter plan node ever exists. The semi join is a first-class DP participant via `make_join_rel`→`join_is_legal` (joinrels.c:350, call site :713). |
| (b) Parallel Append admitted 0/99 | **ORDERING DIVERGENCE (level)** | `addPartialSetOpPath` (windowsetoppaths.go:551, called from `createSetOpPaths` :385) is the only producer — files partial paths on the SETOP upper rel of a two-child `*SetOp` plan node, gated on both branch rels' `ConsiderParallel` + per-branch partial/parallel-safe picks. There is no `pull_up_simple_union_all` analog: UNION ALL inside FROM stays a nested `*SetOp` whose branches are independently-planned subplans — their `PartialPathlist`s never populate corpus-wide → 0 admissions vs PG's 9 sites in the SF0.25 reference. | `pull_up_simple_union_all` (prepjointree.c:1617, reached via `pull_up_subqueries` at planner.c:754) flattens UNION ALL-in-FROM into an **appendrel** during jointree preprocessing — before path generation. `set_append_rel_pathlist` (allpaths.c:1251) → `add_paths_to_append_rel` (allpaths.c:1321) files `partial_subpaths` (:1539) + `pa_subpaths` mixed arm (:1339/:1588) into the appendrel's `partial_pathlist` *inside per-rel path generation* — the appendrel is a join-input citizen (Q5: `Parallel Append` under `Parallel Hash Join`). |
| (c) Incremental Sort reaches executor 0× | **NOT an ordering divergence** | `addIncrementalSortPaths` (incrementalsortpaths.go:148) sits at the correct position — `addOrderedPaths`'s third arm (upperordered.go:186), the analog of PG's `create_ordered_paths` candidate loop. Inert because `GOOPG_INCREMENTAL_SORT` defaults OFF (incrementalsortpaths.go:81) — deliberate fail-closed staging flag from before the executor arm (S7-exec-b) landed. And when offered, it loses on cost anyway (M0141-S7: 3733.01 vs 3730.89) — a COSTING question, owned by banner item 5. | `create_ordered_paths` (planner.c:5308, called :1852 from `grouping_planner`) iterates the input rel's pathlist and files `create_incremental_sort_path` (pathnode.c:3171) per candidate with `presorted_keys>0`, gated by `enable_incremental_sort` (default ON). |
| (d) `applyUpperNarrowing` post-cost | **ORDERING DIVERGENCE** | Runs at `Plan()`'s tail (planner.go:190) on the FINISHED tree — narrows Aggregate/Sort input widths after `addPath`/`setCheapest` already elected a winner. The narrowed width can shrink executor memory but can never change which plan won → "post-cost narrowing inert" is exactly right. | Width narrowing is an INPUT to costing: `grouping_planner` builds per-level input targets (`make_group_input_target`/`make_sort_input_target`/`make_window_input_target`, planner.c:5501+; driven at :1676-1744), each finalized by `set_pathtarget_cost_width` (costsize.c:6367) BEFORE path generation — `cost_sort` reads `subpath->pathtarget->width` (costsize.c:2328), hash/agg sizing reads it at :3709/:3722/:3765. Every candidate is priced on the narrow row. |
| (e) EXISTS/IN pinned pre-DP vs `join_is_legal` | **ORDERING DIVERGENCE — same root as (a)** | The pulled-up Semi/Anti is *pinned* as a spine node above `origChain` (`runJoinSearchBelowPinned` descent, predp.go:94-129; engaged at planner.go:1529-1541 under `GOOPG_UNNEST_PREDP`, default on). Phase A DP searches only the subtree BELOW the pin; Phase B (predp.go:195-200) is the un-pinning mechanism — inert via (a)'s leaf-count decline. So the semi/anti position is fixed syntactically, not chosen by cost. | A pulled-up semi join is a jointree member like any other: `join_search_one_level` → `make_join_rel` → `join_is_legal` (joinrels.c:350, :713) admits the pair when `min_righthand ⊆ rel2` etc. — its POSITION in the join order is a DP cost decision. `SemiRhsExprs`/unique-ify (initsplan.c `compute_semijoin_info`) further lets DP collapse the RHS when provably unique. |

## Root-cause clustering

Items (a) and (e) are **one divergence**: goopg unnests sublinks at *plan*
level and re-derives DP eligibility via `extractSearchLeaves`, where the
sunk-conjunct `Filter` inside `Semi.Left` is an opaque leaf. PG unnests at
*jointree* level where no such wrapper exists. Fix (a) and the pin of (e)
dissolves — Phase B running IS the un-pinning.

Item (b) is the same *class* at a different site: PG does the UNION ALL
flattening at jointree level (appendrel), goopg keeps it a plan node and
files the parallel arm on the `*SetOp` — a position that can never see the
corpus shapes (UNION ALL-in-FROM with aggregated/joined branches).

Item (d) is a true ordering divergence of the opposite kind: PG runs the
narrowing *before* costing as a candidate input; goopg runs it after as a
tree mutation.

Item (c) is refuted as ordering: the producer is at PG's position; the
inertness is a staging flag plus a measured cost loss (M0141-S7, banner
item 5's scope).

## Filed impl tasks (fix_plan, under M0144)

- **M0144-0003a** — Phase-B leaf admission through the sunk-conjunct Filter
  (fixes both (a) and (e)'s facet).
- **M0144-0003b** — jointree-level UNION ALL flattening (`pull_up_simple_union_all`
  analog → appendrel citizen with per-rel partial paths).
- **M0144-0003c** — pre-cost upper narrowing (move width into target sizing
  before `addPath`, the `set_pathtarget_cost_width` position).
