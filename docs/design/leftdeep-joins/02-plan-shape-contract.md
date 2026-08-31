# 02 — The Plan-Shape Contract: PG-Shaped Binary Joins

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) |
| date | 2026-08-02 |
| supersedes | [0034-0001-bushy-join-planning.md](../0000-0049/0034-0001-bushy-join-planning.md) (bushy enumeration); [analysis/cost-driven-second-try-200731/07-cost-model-interaction.md](../../../analysis/cost-driven-second-try-200731/07-cost-model-interaction.md) §6 prohibition line "no shape preference for left-deep-with-fact-outermost" (see §6) |

## 1. The invariant

After this bundle lands, every join subtree the planner emits satisfies:

> **P-PG (PG-shaped binary).** Every join node is a binary `*planner.Join`.
> The tree shape is exactly one PG's `join_search_one_level` can produce:
> for every join node `J` produced by the join search, `J.Right` is a
> **base relation or a composite** — a joinrel path (in a bushy plan) or an
> initial-rel path (a base-relation access path: SeqScan / IndexScan /
> IndexOnlyScan, possibly under its pushed-down single-table Filter, or a
> Materialize over one of these; or a `PathPrebuilt` leaf: subquery / CTE /
> VALUES / pinned subtree, [03](03-join-search-pg-dp.md) §2) — and `J.Left`
> is a base relation or a composite, with `relids(J.Left) ∪ relids(J.Right)`
> exactly the relset of the join. `MultiHashJoin` does not exist.

Formally: the join tree over base rels {r₁ … rₙ} is a binary tree whose
leaves are the rᵢ and whose internal nodes are joins, with the additional
restriction that every subtree is a *connected* group: it was assembled by
PG's level-wise DP, so each join pairs two smaller relsets whose union is
the subtree's relset and whose connection is a join clause, a join-order
restriction (once `join_is_legal` inference lands,
[03](03-join-search-pg-dp.md) §4.4), or the clauseless fallback
(`joinrels.c:120-137`, `:200-256`) when the relset has no join clause at all. The left-deep chain
`(((r_{π(1)} ⋈ r_{π(2)}) ⋈ r_{π(3)}) ⋈ … ) ⋈ r_{π(n)}` is the common case;
bushy shapes `(A ⋈ B) ⋈ (C ⋈ D)` appear wherever PG produces them. The DP's
job ([03](03-join-search-pg-dp.md)) is to pick the shape, the permutation,
and the per-level join method; nothing downstream may change the shape.

Out-of-search join producers keep their current shapes and are *pinned
inputs* to the search, not violations: subquery/CTE scans, `unnest.go`'s
semi/anti joins, and LATERAL constructs enter the search as opaque initial
rels exactly as PG treats sub-query RelOptInfos
(`postgres/src/backend/optimizer/path/allpaths.c:3352`,
`make_rel_from_joinlist` recursion).

## 2. `BuildLeft` is join commutation, not a shape escape

PostgreSQL expresses "build the other side" by swapping which input is the
hash join's inner (the `Hash` child) — a *path-level* choice: for each new
joinrel PG tries both input orders, commuting the jointype where needed
(`populate_joinrel_with_paths`,
`postgres/src/backend/optimizer/path/joinrels.c:916-988` — LEFT commutes to
`JOIN_RIGHT` at :936-938, SEMI to `JOIN_RIGHT_SEMI` at :985-988; the method
paths for each order then come from `add_paths_to_joinrel`, e.g.
`hash_inner_and_outer`, `joinpath.c:2220`). goopg expresses the same thing
in-place with `Join.BuildLeft` (`internal/planner/plan.go`), without
re-shaping the printed tree.

We keep `BuildLeft`, with its meaning sharpened:

- `BuildLeft = false` (default): build the hash table from `J.Right` — a
  base relation (left-deep chain) or a composite (bushy plan) — and
  **stream the other side** through it. This is the MHJ-equivalent
  pipelining mode and is what P-PG makes the common case (in a left-deep
  chain, every interior seam is a probe seam — [05](05-executor-pipeline-rework.md) §1).
- `BuildLeft = true`: build from `J.Left`, stream the right side. Chosen by
  cost only (e.g. TPC-H Q5's deepest level, where the composite at 1.99M
  rows is smaller than the 6.0M-row base side —
  `analysis/cost-driven-second-try-200731/02-premise-audit.md` §10 verified
  the current planner picks this correctly). A build-side composite is
  built once at `Open` (blocking hash-build semantics), so it costs once
  into the table, never per probe row.

This preserves the full expressiveness of PG's {binary tree + per-join
commutation} space. The executor keeps exactly one binary hash-join
operator; `BuildLeft` only swaps which child feeds `buildLazyHashTable`.

Constraint carried over unchanged: LEFT-join null-padding currently requires
`!BuildLeft` (`operators_join_agg.go:1234-1241`, `:1360-1369`); the outer-join
generalisation (matched-bitmap fill, PG's `HJ_FILL_INNER`) is
[07](07-other-join-operators.md) §3. Until that lands, non-INNER joins pin
`BuildLeft = false` in path generation, exactly as today's forced-false for
Semi/Anti (`operators_join_agg.go:548-551`).

## 3. What PG-shaped binary trees simplify (the payoff besides speed)

The old subset-bitmask DP's composite children are not in ascending
base-table order, and — worse — a composite's column order depended on the
split that built it, so every `dpEntry` carried a `layout map[int]int`
(`internal/planner/bushy.go:552`) and the tree needed a
re-coordinatisation layer: `remapKeyToLayout`, `mergeSubsetLayouts`,
`buildBindingsPosMap`, `applyJoinTreePosMap`, and — for MHJ — the OID-sort +
`buildMHJPosMap` (`bushy.go:2385`) + `remapColumnRefsAfterRewrite`
round-trip plus `remapExprRefsToMHJ` (`bushy.go:2081`). The second-try
analysis identified this layer as the project's index-skew bug generator,
and the strongest argument for removing MHJ.

Under P-PG the layout rule is goopg's **canonical relid-order layout** — a
design choice stricter than PG's: every RelOptInfo's output columns are in
relid order of its relset, a *pure function of the relset*, never of build
history. PG itself makes no such promise — its `build_joinrel_tlist` appends
the outer rel's columns then the inner rel's new ones, and the `setrefs`
machinery resolves the resulting order at plan-creation time
(`postgres/src/backend/optimizer/plan/setrefs.c`; see also the NOTE at
`postgres/src/backend/optimizer/util/relnode.c:780-782`). goopg adopts the
stricter relid-order rule so the layout problem *disappears*: because the
layout is relset-determined, no per-subset state is ever needed — the
boundary translation can be computed once, at the search root, from the
final relset alone ([03](03-join-search-pg-dp.md) §10). Consequences:

- `dpEntry.layout` and the per-subset/per-node remap pair
  (`remapKeyToLayout`, `mergeSubsetLayouts`) are deleted with the old bushy
  DP ([08](08-migration-and-removal.md) inventory) — the new DP carries no
  layout state at all. What is **not** deleted by shape alone: relid order
  is still a permutation of syntactic (binding) order, so a single
  **boundary translation** at the search root remains necessary for the
  enclosing tree — owned and specified in [03](03-join-search-pg-dp.md)
  §10, and the old machinery stays alive until that replacement is proven.
- The executor's composed `VirtualSlot` binds `[left-slot, right-slot]`
  with no per-row permutation — the seam fix in
  [05](05-executor-pipeline-rework.md) §3 is shape-independent. Canonical
  emission is a static property: each emitted `Join` node carries a
  **column-binding map** — the relid merge of its two children, derived
  once at `createPlan` from their `relids`, zero per-row cost, identity for
  relid-ascending pairings (the common phase-1 case) — so every joinrel's
  composed output presents relid order and every expression between joins
  and above the root references canonical positions. Ordering relative to
  the enclosing tree is then the boundary map's job
  ([03](03-join-search-pg-dp.md) §10), exactly as PG's setrefs does it.
- EXPLAIN output becomes structurally comparable with PG's, join for join —
  the precondition for the plan-shape parity gate
  ([09](09-verification-and-acceptance.md) §4). Today goopg emits zero bare
  `Hash` nodes and MHJ arms print an n-ary block PG cannot produce.

## 4. Cross products: why the PG-shaped search does not resurrect the Q2 problem

[0034-0001](../0000-0049/0034-0001-bushy-join-planning.md) motivated bushy DP with Q2:
`part` and `supplier` share no join edge, and the then-current **greedy
left-deep ordering** placed them adjacently, forcing a 2×10⁹-row cross join.
The correct conclusion is narrower than "left-deep needs cross products":

> For any **connected** join graph, there exists a left-deep order with every
> prefix connected (grow a spanning traversal from any vertex: each new rel
> joins the composite through at least one edge). A connectivity-constrained
> left-deep DP therefore never emits an avoidable cross product.

Q2's graph *is* connected (`part — partsupp — supplier — nation — region`);
the greedy heuristic failed, not the shape. The DP in
[03](03-join-search-pg-dp.md) §4 enforces exactly PG's rule: a join pair is
considered only when a join clause (or outer-join ordering restriction)
connects it (`have_relevant_joinclause`, mirrored from
`make_rels_by_clause_joins`, `postgres/src/backend/optimizer/path/joinrels.c:118`),
with PG's own two fallbacks when nothing connects — the per-rel clauseless
cartesian branch (`joinrels.c:120-137`, fires at every level) and the "last
ditch" pass (`joinrels.c:200-256`) — so genuinely disconnected FROM lists
still plan, and a tiny disconnected dimension can be crossed in early where
that is cheapest, as PG does.

Bushy joins — joining two composites (e.g. building one small
dimension-pair hash table before joining it to the fact) — are **inside**
the search space of this bundle: [03](03-join-search-pg-dp.md) §4.3
implements PG's phase 2 verbatim, so any composite-composite pair with a
connecting clause is generated and costed honestly. Q5's build-side
sub-join is exactly the case that now gets a real costed alternative to the
left-deep order (whose cost-driven form was itself a 7.1× *win* in the
symmetric A/B — `evidence/stage3-order-ab.txt`, the "Q5 HANG" was a
timeout-asymmetry artifact). The DP picks whichever shape is cheaper; there
is no shape trade to accept.

## 5. Risk register for the PG-shaped shape contract

| risk | exposure | mitigation |
|---|---|---|
| bushy-phase search-space growth | the phase-2 pair count reaches (3ⁿ − 2ⁿ⁺¹ + 1)/2 in the worst case (~3k `makeJoinRel`-eligible pairs at n=8, ~7M at n=15) | PG's own gates, implemented verbatim: phase 2 skips rels with no join clauses/restrictions and only calls `makeJoinRel` on clause-connected pairs (`joinrels.c:170-172`, `:190-191`); the mirror-half rule halves equal-k pairs (`:174-177`); the 16-rel ceiling with over-ceiling chunking ([03](03-join-search-pg-dp.md) §7) bounds the rest. PG runs this same search up to `geqo_threshold − 1` = 11 rels without incident (at 12 it switches to GEQO, `allpaths.c:3420`) |
| composite build sides (bushy plans) in execution | a build side that is itself a join must be fully drained into the hash table before probing | the blocking build at `Open` is unchanged and shape-independent ([05](05-executor-pipeline-rework.md) §7); a build-side composite pays once, not per probe row (seam counting rule, [05](05-executor-pipeline-rework.md) §1); Q5-class verification via the stage0 A/B |
| `join_is_legal` constraint inference unimplemented (v1 pin) | for queries where PG would legalise an outer/semi/anti join *inside* the DP, goopg keeps the pinned shape and may emit a different (possibly costlier) plan than PG | the pin is explicitly temporary ([03](03-join-search-pg-dp.md) §4.4); the bushy phase — now implemented — is the structural half PG relies on to plan under restrictions, so the remaining prerequisite is the inference itself; the parity gate's ratchet ([09](09-verification-and-acceptance.md) §4) records such spines |
| edge-poor graphs after partial pushdown (inferred-edge dependence) | `inferAnchoredEqualities` edges are penalised (×2.0, `bushy.go:67`) and could starve the connectivity rule | PG's last-ditch clauseless pass guarantees progress; the penalty moves into selectivity, not admissibility ([04](04-cost-and-cardinality.md) §5) |
| explicit `JOIN … ON` trees written bushy by the user | user-forced shape, PG honours it below `join_collapse_limit = 1` semantics | out of search per §1 (pinned inputs); with collapse enabled ([03](03-join-search-pg-dp.md) §6) they flatten and re-shape in the DP, matching PG |
| plans that relied on MHJ's early residual pruning across levels | Q5/Q21-class chains with filters the planner left above the join | qual placement is a planner responsibility: with per-level paths, residuals attach at their lowest legal level in path generation ([03](03-join-search-pg-dp.md) §5.4), which is where MHJ's `partitionFilters` put them at runtime |

## 6. Relationship to the second-try prohibition

`analysis/cost-driven-second-try-200731/07` §6 prohibited "shape preference
for left-deep-with-fact-outermost" — correctly, **as a cost-model rule**: a
penalty term that nudges totals toward a shape is unfalsifiable tuning and
was banned. This bundle does not add any such term. The PG-shaped binary
property here is a *search-space contract* (the enumerator can express
exactly the shapes PG's `join_search_one_level` can express, and nothing
else), and fact-outermost is not encoded anywhere: it must **emerge** from
honest costs ([04](04-cost-and-cardinality.md)) or the acceptance bars fail.
That is the falsifiable version of the same intent, and it supersedes the
prohibition line rather than violating it.
