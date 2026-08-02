# 02 — The Plan-Shape Contract: Left-Deep Binary Only

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) |
| date | 2026-08-02 |
| supersedes | [0034-0001-bushy-join-planning.md](../0034-0001-bushy-join-planning.md) (bushy enumeration); [analysis/cost-driven-second-try-200731/07-cost-model-interaction.md](../../../analysis/cost-driven-second-try-200731/07-cost-model-interaction.md) §6 prohibition line "no shape preference for left-deep-with-fact-outermost" (see §6) |

## 1. The invariant

After this bundle lands, every join subtree the planner emits satisfies:

> **P-LD (left-deep binary).** Every join node is a binary `*planner.Join`.
> For every join node `J` produced by the join search, `J.Right` is an
> **initial-rel path** — a base-relation access path (SeqScan / IndexScan /
> IndexOnlyScan, possibly under its pushed-down single-table Filter, or a
> Materialize over one of these) or a `PathPrebuilt` leaf (subquery / CTE /
> VALUES / pinned subtree, [03](03-join-search-pg-dp.md) §2) — and `J.Left`
> is either an initial-rel path (bottom of the chain) or another join node
> of the same chain. `MultiHashJoin` does not exist.

Formally: the join tree over base rels {r₁ … rₙ} is
`(((r_{π(1)} ⋈ r_{π(2)}) ⋈ r_{π(3)}) ⋈ … ) ⋈ r_{π(n)}` for some permutation π
chosen by cost. The DP's job ([03](03-join-search-pg-dp.md)) is to pick π and
the per-level join method; nothing downstream may change the shape.

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
in-place with `Join.BuildLeft` (`internal/planner/plan.go`), keeping the
printed tree left-deep.

We keep `BuildLeft`, with its meaning sharpened:

- `BuildLeft = false` (default): build the hash table from `J.Right` — a
  base relation — and **stream the composite** through it. This is the
  MHJ-equivalent pipelining mode and is what P-LD makes the common case.
- `BuildLeft = true`: build from `J.Left` (the composite), stream the base
  rel. Chosen by cost only (e.g. TPC-H Q5's deepest level, where the
  composite at 1.99M rows is smaller than the 6.0M-row base side —
  `analysis/cost-driven-second-try-200731/02-premise-audit.md` §10 verified
  the current planner picks this correctly).

This preserves the full expressiveness of PG's {left-deep tree + per-join
commutation} space while the *tree* stays left-deep. The executor keeps
exactly one binary hash-join operator; `BuildLeft` only swaps which child
feeds `buildLazyHashTable`.

Constraint carried over unchanged: LEFT-join null-padding currently requires
`!BuildLeft` (`operators_join_agg.go:1234-1241`, `:1360-1369`); the outer-join
generalisation (matched-bitmap fill, PG's `HJ_FILL_INNER`) is
[07](07-other-join-operators.md) §3. Until that lands, non-INNER joins pin
`BuildLeft = false` in path generation, exactly as today's forced-false for
Semi/Anti (`operators_join_agg.go:548-551`).

## 3. What left-deep deletes (the payoff besides speed)

The bushy DP's composite children are not in ascending base-table order, so
every `dpEntry` carries a `layout map[int]int` (`internal/planner/bushy.go:552`)
and the tree needs a re-coordinatisation layer: `remapKeyToLayout`,
`mergeSubsetLayouts`, `buildBindingsPosMap`, `applyJoinTreePosMap`, and — for
MHJ — the OID-sort + `buildMHJPosMap` (`bushy.go:2385`) +
`remapColumnRefsAfterRewrite` round-trip plus `remapExprRefsToMHJ`
(`bushy.go:2081`). The second-try analysis identified this layer as the
project's index-skew bug generator, and the strongest argument for removing
MHJ.

Under P-LD the composite's column layout is a **prefix sum**: chain order is
column order; appending `J.Right`'s columns to the end of `J.Left`'s output
is the only composition rule. Consequences:

- `dpEntry.layout` and the per-subset/per-node remap machinery are deleted
  with the bushy DP ([08](08-migration-and-removal.md) inventory). What is
  **not** deleted by shape alone: the chain order π is still a permutation
  of syntactic order, so a single **boundary translation** at the search
  root remains necessary for the enclosing tree — owned and specified in
  [03](03-join-search-pg-dp.md) §10, and the old machinery stays alive
  until that replacement is proven.
- The executor's composed `VirtualSlot` sources line up with chain order —
  the seam fix in [05](05-executor-pipeline-rework.md) §3 binds
  `[left-slot, right-slot]` with no permutation step.
- EXPLAIN output becomes structurally comparable with PG's, join for join —
  the precondition for the plan-shape parity gate
  ([09](09-verification-and-acceptance.md) §4). Today goopg emits zero bare
  `Hash` nodes and MHJ arms print an n-ary block PG cannot produce.

## 4. Cross products: why left-deep does not resurrect the Q2 problem

[0034-0001](../0034-0001-bushy-join-planning.md) motivated bushy DP with Q2:
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
ditch" pass (`joinrels.c:216-258`) — so genuinely disconnected FROM lists
still plan, and a tiny disconnected dimension can be crossed in early where
that is cheapest, as PG does.

What bushy *can* do that left-deep cannot is join two composites (e.g. build
one small dimension-pair hash table). The measured stake today is small — Q5's
build-side sub-join is the one identified beneficiary, and Q5 under
cost-driven left-deep-ish order was a 7.1× *win* in the symmetric A/B
(`evidence/stage3-order-ab.txt`, the "Q5 HANG" was a timeout-asymmetry
artifact) — and the framework keeps the bushy phase re-addable
([03](03-join-search-pg-dp.md) §4.3). Accepting this is the bundle's explicit
trade.

## 5. Risk register for the shape restriction

| risk | exposure | mitigation |
|---|---|---|
| a query where a bushy plan is strictly, materially cheaper | snowflake schemas joining two large composites; Q5-class build-side sub-joins | `BuildLeft = true` covers the "build the composite" half of the benefit; per-query bars in [09](09-verification-and-acceptance.md) catch material regressions; bushy phase re-addable behind the same DP |
| edge-poor graphs after partial pushdown (inferred-edge dependence) | `inferAnchoredEqualities` edges are penalised (×2.0, `bushy.go:67`) and could starve the connectivity rule | PG's last-ditch clauseless pass guarantees progress; the penalty moves into selectivity, not admissibility ([04](04-cost-and-cardinality.md) §5) |
| explicit `JOIN … ON` trees written bushy by the user | user-forced shape, PG honours it below `join_collapse_limit = 1` semantics | out of search per §1 (pinned inputs); with collapse enabled ([03](03-join-search-pg-dp.md) §6) they flatten and re-shape left-deep, matching PG |
| plans that relied on MHJ's early residual pruning across levels | Q5/Q21-class chains with filters the planner left above the join | qual placement is a planner responsibility: with per-level paths, residuals attach at their lowest legal level in path generation ([03](03-join-search-pg-dp.md) §5.4), which is where MHJ's `partitionFilters` put them at runtime |

## 6. Relationship to the second-try prohibition

`analysis/cost-driven-second-try-200731/07` §6 prohibited "shape preference
for left-deep-with-fact-outermost" — correctly, **as a cost-model rule**: a
penalty term that nudges totals toward a shape is unfalsifiable tuning and
was banned. This bundle does not add any such term. The left-deep property
here is a *search-space contract* (the enumerator cannot express anything
else), and fact-outermost is not encoded anywhere: it must **emerge** from
honest costs ([04](04-cost-and-cardinality.md)) or the acceptance bars fail.
That is the falsifiable version of the same intent, and it supersedes the
prohibition line rather than violating it.
