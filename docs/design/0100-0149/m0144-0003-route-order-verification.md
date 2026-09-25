# M0144-0003 — Route-order verification

Status: done — recon only, no production change. Route-diff table:
`analysis/m0144/m0144-0003-route-order-verification.md`. Three `Kind: impl`
tasks filed (M0144-0003a/b/c) for the confirmed divergences.

## Method

For each landed-but-inert item the task named, the goopg decision point was
traced in `internal/optimizer/` and PG's ordering cited from the oracle
(`./postgres/src/backend/optimizer/…`, GNU GLOBAL). Corpus evidence comes
from the committed captures (`analysis/m0142/m0142-0012verify-tpcds-pg.txt`,
the latest SF0.25 goopg sweep `plans-20260920-074115.txt`) and the prior
loop's own measurements (M0142-0008-producer §4.1's DPTRACE census).

## Verdicts

**(a) IN-derived SJInfo inert — CONFIRMED ordering divergence.** The fix
landed at the right seam but the corpus never reaches it, and the reason is
a route-*level* divergence: PG's `pull_up_sublinks` (prepjointree.c:468,
called from `subquery_planner` at planner.c:737 — before `query_planner`)
converts sublinks into *jointree* members, so leftover conjuncts become
base-rel restrict clauses and the semi join enters DP natively via
`make_join_rel`/`join_is_legal` (joinrels.c:350/:713). goopg's
`unnestSubqueriesInPlan` runs at *plan* level (planner.go:1539): the shape
it produces is `Filter{retained}(Semi(Filter{sunk}(origChain), rhs))`
— `pushConjunctsBelowSemiAnti` (unnest.go:372) installs the sunk conjuncts
as a `*Filter` inside `Semi.Left`. `extractSearchLeaves` then counts that
Filter as ONE opaque leaf (joinsearchseam.go:1456-1461), so Phase B's
leaf-count check (`len(scans) != nprefix+len(semiAnti)`, :325) fires before
`semiAntiLinksHaveSJInfos` is ever consulted — measured `nrels=4 nleaves=2`
on TPC-DS Q56, `leaf-count×62` corpus-wide, zero semianti-class declines.
(The no-sinkable-conjuncts twin shape `Filter{true}(Semi(origChain))` never
even reaches Phase B — `runJoinSearchBelowPinned`'s `cur == origChain` arm
returns early, predp.go:120-127.)

**(b) Parallel Append 0/99 — CONFIRMED ordering divergence.** PG flattens
`UNION ALL`-in-FROM into an appendrel during jointree preprocessing
(`pull_up_simple_union_all`, prepjointree.c:1617, via `pull_up_subqueries`
planner.c:754); `add_paths_to_append_rel` (allpaths.c:1321) then files the
pure and mixed partial-append arms per-rel inside path generation, making
the append a join-input citizen (SF0.25 PG reference: 9 Parallel Append
sites, e.g. Q5's `Parallel Append` under `Parallel Hash Join`). goopg has
no jointree-level union-all flattening at all: `addPartialSetOpPath`
(windowsetoppaths.go:551, called :385) produces at the `*SetOp` plan-node
level and needs each branch's searched rel to carry a populated
`PartialPathlist` — which never happens corpus-wide (branches are
independently-planned subplans). The producer is real but positioned where
the corpus shapes can't reach it.

**(c) Incremental Sort 0× — REFUTED (not ordering).** The producer sits at
PG's exact position — `addOrderedPaths`'s third arm (upperordered.go:186) is
the `create_ordered_paths` candidate loop (planner.c:5308, :5374's
`create_incremental_sort_path` call). Inertness is `GOOPG_INCREMENTAL_SORT`
default-off (incrementalsortpaths.go:81) — a deliberate fail-closed staging
knob — compounded by the measured cost loss (M0141-S7: 3733.01 vs 3730.89).
The residual question is costing, owned by banner item 5; no ordering impl
task is filed for it.

**(d) `applyUpperNarrowing` post-cost — CONFIRMED ordering divergence.**
It mutates the finished tree at `Plan()`'s tail (planner.go:190) — after
`setCheapest` elected a winner — so narrowed width can shrink executor
memory but never the plan choice. PG narrows the tuple the sort/agg must
carry *before* costing: `grouping_planner` builds per-level input targets
(`make_group_input_target`/`make_sort_input_target`/`make_window_input_target`,
planner.c:5501+, driven at :1676-1744) finalized by
`set_pathtarget_cost_width` (costsize.c:6367), and every candidate's
`cost_sort`/`cost_agg`/hash sizing reads the narrow `pathtarget->width`
(costsize.c:2328/:3709/:3722/:3765).

**(e) EXISTS/IN pinned pre-DP — CONFIRMED, same root as (a).** The pulled-up
semi/anti join is pinned as a spine node above `origChain`
(`runJoinSearchBelowPinned`, predp.go:94-129); Phase A searches below the
pin and Phase B — the un-pinning mechanism — is inert via (a). In PG the
semi join's position is a `join_is_legal` DP decision (joinrels.c:350/:713).
Fixing (a) dissolves (e)'s pinning: a running Phase B IS DP choosing the
semi join's position.

## Pattern

Three of four confirmed divergences share one shape: **PG rewrites the
jointree/targets before planning; goopg rewrites the plan tree after** and
must re-derive eligibility downstream — where a plan-level wrapper (Filter,
`*SetOp` boundary, post-tournament mutation) defeats the derived gate. The
durable fix direction is jointree-level pull-up / pre-cost target sizing,
not more seam admission surgery.

## Filed impl tasks

See `.ralph/fix_plan.md` M0144-0003a/b/c — each names its confirmed
divergence, the PG citation, and expected movement.
