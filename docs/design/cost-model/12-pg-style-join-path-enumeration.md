# 12 — PG-Style Join-Path Enumeration (the C4 pivot)

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) |
| date | 2026-07-22 |
| depends on | [06](06-scan-and-join-path-costs.md), [07](07-cost-driven-join-order.md) |
| supersedes | [06](06-scan-and-join-path-costs.md) §4 (the MHJ comparability invariant — MHJ leaves the cost path, §3) and [07](07-cost-driven-join-order.md) §4.5's "generate MHJ paths in the DP" correction |
| premise | the resulting plan should be **PG-shaped**, not necessarily goopg's fastest — the user's explicit directive |

## 0. Why this chapter exists

[07](07-cost-driven-join-order.md) §4.5 recorded the finding that killed the first
C4: costing **only binary hash-join order** in the bushy DP, while goopg's
`MultiHashJoin` packing (`rewriteMultiWayChain`) and NL-index conversion
(`rewriteJoinsToNLI`) remain **post-DP passes**, optimises a cost that does not
match how the plan executes — and it regressed Q9 (27 s → > 250 s).

The three sub-problems this left (composite-index NLI detection in the DP; MHJ
structural constraints; in-memory cost calibration) all trace to one root: goopg
decides **order first, method later**, in separate passes. **PostgreSQL does not.**
It decides join order, join method, and access path **together**, in one
cost-based path enumeration, and that is precisely why it plans TPC-H well. This
chapter adopts PG's model.

The enabling permission is the user's: the resulting plan **need not be goopg's
fastest** (goopg's hand-tuned `MultiHashJoin` plans may still be quicker on some
queries). The target is a **PG-shaped plan** — a binary join tree of hash /
merge / nested-loop-index joins — which goopg's executor already runs. Accepting
that target dissolves two of the three sub-problems (§5).

## 1. What PostgreSQL actually does

PG's join planning is one unified, cost-based enumeration. TPC-H queries have ≤ 8
relations (`geqo_threshold` is 12), so every one is planned by the exhaustive DP.

### 1.1 Base-relation paths, then a DP over joinrels

For each base relation PG generates every access path — seq scan, each applicable
index scan (`create_index_paths`, `indxpath.c:241`), bitmap scans — as costed
`Path`s, and `set_cheapest` keeps the non-dominated ones. Then
`join_search_one_level` (`joinrels.c:73`) builds joinrels bottom-up: level 2 from
pairs of base rels, level 3 from level-2 joinrels + base rels, and so on, up to
the full set. `make_join_rel` (`joinrels.c:696`) creates each joinrel.

### 1.2 The load-bearing step: `add_paths_to_joinrel` generates **every method**

For a given pair of sub-relations to be joined, `add_paths_to_joinrel`
(`joinpath.c:124`) generates **all applicable join methods**, each as a costed
path, and `add_path` keeps the survivors:

- `hash_inner_and_outer` (`joinpath.c:2220`) — hash join, both build orientations.
- `match_unsorted_outer` (`joinpath.c:1812`) — nested loop over the outer, with the
  inner as a **parameterised index scan** (the index-nested-loop) among other inner
  paths.
- `sort_inner_and_outer` (`joinpath.c:1357`) / `try_mergejoin_path` (`:1029`) —
  merge join, sorting inputs whose pathkeys do not already satisfy the merge clause.

There is **no separate "rewrite" pass**. By the time a joinrel is complete, its
cheapest path already encodes order *and* method *and* access path, all chosen by
one cost comparison. This is the single fact that makes PG's planning coherent.

### 1.3 Parameterised paths are the mechanism, not an afterthought

The index-nested-loop is a **parameterised path**: the inner index scan depends on
a value supplied by the outer row (`get_baserel_parampathinfo`, `relnode.c:1545`).
Its cost is `outer_rows × (one index probe)`, so it is cheap when the outer is
selective and ruinous when the outer is huge — ranked correctly against a hash
join **at every joinrel**, because both are costed paths in the same list. This is
exactly how PG expresses what goopg does with `MultiHashJoin + NL-index`, and it is
what a binary-hash-only cost model cannot see.

### 1.4 PostgreSQL has no MultiHashJoin

PG's multi-way joins are **binary trees** of hash / merge / nested-loop joins.
There is no N-way hash operator, so there is no "which tables go in the MHJ"
decision — and therefore none of goopg's structural-constraint trap. For Q9, PG's
plan is a binary tree in which the fact table drives and the dimensions are joined
by hash or index-nested-loop, each picked by cost at its joinrel.

### 1.5 Why the ranking is correct

Two ingredients: **accurate cardinality** (ANALYZE's MCVs, histograms, and a real
`n_distinct` estimator, plus extended statistics for correlated columns) so costs
apply to right-sized inputs; and a **consistent cost model** whose *relative* costs
(random index access vs sequential scan vs hash build) are calibrated. Even when
SF1 fits in memory, the *relative* ranking holds, and `effective_cache_size` tunes
the effective index cost.

## 2. The goopg adoption

goopg reproduces §1.2 directly. The bushy DP (`enumerateBushyPlans`) stops being a
binary-hash-order search and becomes an `add_paths_to_joinrel` analogue:

> For each joinrel the DP forms, over the two children's cheapest paths, **generate
> every applicable method as a costed `Path`** — hash (both orientations,
> [06](06-scan-and-join-path-costs.md) §2.1); nested-loop-index when the inner is a
> base relation with an index covering the join key (§4); merge when a child
> already delivers useful order — `add_path` each, and `set_cheapest` picks. The
> chosen path's method **is** the plan; there is no post-DP method rewrite.

The primitives already exist ([C4a-i](IMPLEMENTATION-TODO.md)): `hashJoinCost`,
`nestloopCost` + `indexProbeCost`, `mergeJoinCost`, `generateHashJoinPaths`,
`generateNLIPath`. This chapter is about wiring them in **as PG wires them** and
retiring the post-DP rewrites for cost-driven plans.

## 3. Dropping MultiHashJoin from the cost-driven path (resolves sub-problem #2)

**Decision: the cost-driven planner does not emit `MultiHashJoin`.** It produces a
PG-shaped binary tree. `MultiHashJoin` remains a valid executor operator and an
available *hand-tuned* optimisation, but the cost path does not generate it, so:

- `rewriteMultiWayChain` (`planner.go:988`) is **bypassed** when the cost-driven DP
  produced the join tree. The binary tree the DP chose is final.
- The "which tables go in the MHJ" structural-constraint problem (sub-problem #2)
  **disappears** — there is no MHJ to compose.

The trade the user has authorised: a query that goopg's MHJ executes faster may run
a PG-shaped binary tree instead. That is acceptable; correctness and a *coherent,
costable* plan are the goal, not beating goopg's own best operator.

## 4. NL-index awareness — costed in the DP, but NEVER reconstructed there

A design review corrected two things this section originally understated, and both
change the plan materially.

**(1) `pickIndexCoveringAllLeadingColumns` is necessary, not sufficient.**
`tryBuildNLI` (`nl_index_join.go:293`) runs that index check at `:435` and then
applies *further* decline gates — `nliCostGateAccepts` (`:445`),
`innerUnwrapCostAccepts` (`:448`), the `IsolatedScope` Project veto (`:466`), and
the rebind selectivity guards (`:513`+). A DP that generated an NL-index path
merely because an index covers the key could cost-and-choose an NLI that the
construction step then **declines**, silently falling back to a hash join whose
cost the DP never selected — the exact desync that broke the binary attempt, in a
new form.

**(2) NLI *construction* is ~90 lines of coordinate reconciliation, and
reimplementing it has already caused a reverted Q9 bug.** `tryBuildNLI`'s node
build (`nl_index_join.go:470`+) rebinds each probe key's `ColumnRef.Index` against
the concrete **outer node's runtime `Output()`**, branching on the outer node kind
and using `SourceTableIdx` disambiguation, a selectivity guard, and a type
override. The reverted-code comment at `nl_index_join.go:142-150` records a prior
composite-NLI attempt that got this wrong — *"Q9 returned 1 row instead of the
canonical 7."* A `create_plan` that rebuilds NLIs would re-open that hole.

**The corrected design: one predicate, one constructor.**

- **Construction stays solely in `rewriteJoinsToNLI`** (`nl_index_join.go:78`),
  unchanged, with its proven coordinate logic. `create_plan` never builds an NLI.
  The DP builds ordinary hash `*Join` nodes (via `buildJoinFromDP`); the post-DP
  `rewriteJoinsToNLI` converts the eligible ones exactly as it does today. So there
  is **no new coordinate bridge** and no second constructor to desync.
- **The DP's *cost* of a join is the cost of whatever `rewriteJoinsToNLI` will
  actually make it.** To know that without duplicating the logic, the DP asks
  `tryBuildNLI` itself (on a throwaway clone of the candidate `*Join`, so no
  mutation escapes): if it returns *convertible*, the DP costs that join with
  `nestloopCost` + `indexProbeCost`; otherwise with `hashJoinCost`. The DP's
  ranking and the executed method then share the **same predicate** — `tryBuildNLI`
  — by construction, closing the desync. Composite indexes are handled because
  `tryBuildNLI` already presents all join clauses to
  `pickIndexCoveringAllLeadingColumns` (`nl_index_join.go:950`); the DP inherits
  that for free by delegating rather than re-deriving.
- **Staging (important).** This NLI-cost step is the *second* move, not the first.
  Step one (§7) is simply **dropping MHJ** (§3) and measuring: the binary attempt
  already recovered Q8 *without* MHJ, so it is an open, cheap question whether
  removing the MHJ mis-composition alone fixes Q9 under the existing
  `rewriteJoinsToNLI`. Only if a binary-hash-cost order still mis-ranks the NLI
  choice does step two (delegated NLI costing above) land. Measuring before
  building the harder piece is the discipline [07](07-cost-driven-join-order.md)
  §4.5 was written to enforce.

**On performance:** consulting `tryBuildNLI` per candidate split is heavier than a
cost formula; if planning time suffers it is memoised per ordered table-pair, but
TPC-H's ≤ 12-relation DP does not need that at the milestone.

## 5. Cost calibration resolved by targeting PG's plan shape (sub-problem #3)

Sub-problem #3 was: goopg is in-memory at SF1, so PG's disk-based
`random_page_cost = 4` over-charges an index probe relative to goopg's actual
CPU-bound cost, so the constants mis-rank goopg's *fastest* plan.

**The pivot dissolves it.** The target is now PG's **plan shape**, not goopg's
fastest runtime. PG's plan shape is produced by **PG's constants**. So goopg uses
PG's exact GUC values ([02](02-pg-path-and-cost-oracle.md) §3, already registered)
**unchanged**, and success is defined as *matching PG's chosen plan shape*
(`scripts/pg-oracle-diff.sh`), not as minimising goopg's clock. `indexProbeCost`
([C4a-i](IMPLEMENTATION-TODO.md)) therefore keeps its PG-derived value (~8, =
2·`random_page_cost` + CPU); it is correct *for reproducing PG's ranking*, which is
the whole point. Any later retuning toward goopg's in-memory reality becomes an
*optimisation past PG parity*, explicitly out of this milestone.

**One precondition, stated plainly:** "PG's constants ⇒ PG's shape" holds only if
goopg's **cardinality** estimates are close to PG's — the constants are applied to
row counts, so if `estimateJoinCost`'s estimates diverge, the same constants give a
different shape. goopg reuses its existing cardinality estimator unchanged
(rows-invariant, [09](09-verification-and-acceptance.md) §1), adequate for the
TPC-H shapes ([07](07-cost-driven-join-order.md) §3 preserved Q5 on it). Where a
shape mismatch traces to cardinality it is a statistics gap
([05](05-statistics-and-estimation-inputs.md) §5), diagnosed there, not a
constants problem.

## 6. How Q9 is handled

PG's Q9 is a binary tree: `lineitem` drives; `part`, `supplier`, `partsupp`,
`orders`, `nation` are joined by hash or index-nested-loop, each chosen by cost.
goopg reproduces that: with MHJ dropped the join tree is binary, so there is **no
MHJ membership decision to get wrong**, and `rewriteJoinsToNLI` converts the
eligible joins as today; if a binary-hash-cost order still mis-ranks the
`partsupp`-vs-`orders` probe, the §4 delegated NLI costing makes the DP prefer the
order PG picks. Either way the specific trap — putting `partsupp` in a 4-way MHJ
and NL-probing `orders` 6 M times — cannot recur, because there is no MHJ.

**Q4 is a different fix, not this one.** Q4's regression is a *semi*-join method
choice (a Hash-Semi building 6 M-row `lineitem` where an NL-index-Semi wins). That
join is created by sub-query unnesting **after** the bushy DP, so this chapter's
join-order work does not reach it; it is recovered by `rewriteJoinsToNLI`'s
semi/anti cost gate (`nliCostGateAccepts` for `JOIN_SEMI`,
[06](06-scan-and-join-path-costs.md) §2.4), which the pivot **keeps running** and
can improve separately. §6 is about Q9 (order); Q4 is tracked as its own item.

## 7. What replaces the binary-only C4

The revised, **staged** C4 (see [11](11-roadmap.md), updated) — measure the cheap
hypothesis before building the hard piece:

1. **C4-pg-i (drop MHJ, measure).** Skip `rewriteMultiWayChain` for cost-driven
   plans so the DP's binary-hash tree is final; keep `rewriteJoinsToNLI` and the
   scan-input rewrite. Restore the binary-hash-cost DP switch and measure Q9/Q8/Q5
   on SF1. Open question this answers: does removing the MHJ mis-composition alone
   fix Q9?
2. **C4-pg-ii (delegated NLI costing, only if needed).** If C4-pg-i still
   mis-ranks, have the DP cost each candidate as its *actual* method by consulting
   `tryBuildNLI` on a clone (§4) — construction still solely in `rewriteJoinsToNLI`.
   Re-measure.
3. **C4-pg-iii (gate).** The five regressions recover, **Q9 does not regress**, Q5
   held, on SF1; `pg-oracle-diff` shows goopg's cost-driven shapes match PG's
   (modulo documented divergences).

**Pipeline passes** (`planner.go:988-1009`), per pass under cost-driven planning:
`rewriteMultiWayChain` (`:988`) — **skipped** (drop MHJ);
`rewriteScanInputsWithSingleTablePredicates` (`:996`, constant-predicate →
IndexScan, e.g. Q8's `p_type=…`) — **kept** (the DP does not yet generate these
single-table index paths; retiring it waits for base-rel `create_index_paths`);
`rewriteJoinsToNLI` (`:1003`) — **kept** (sole NLI constructor, §4; also serves
unnested semi/anti); `remapExprRefsToMHJ` (`:1004`) — harmless no-op with no MHJ;
`remapWithBindings` (`:1008`) — **kept**.

## 8. Divergence from PostgreSQL

- **goopg keeps `MultiHashJoin` in the executor but never emits it from the
  cost-driven planner** (§3). A deliberate, user-authorised divergence: goopg gives
  up a hand-tuned operator on the cost path to gain a coherent, costable, PG-shaped
  plan.
- **`rewriteMultiWayChain` is skipped for cost-driven plans; `rewriteJoinsToNLI` is
  kept** (§3, §4, §7). Only the MHJ packing is dropped. NLI conversion stays the
  sole responsibility of `rewriteJoinsToNLI` — the DP influences *whether* a join is
  costed as an NLI (§4) but never constructs one, so the reverted-Q9 coordinate bug
  (`nl_index_join.go:142-150`) is not re-opened.
- **PG's constants are used unchanged, and success is PG-shape parity, not goopg
  runtime** (§5). The in-memory recalibration is explicitly deferred as
  optimisation past parity.
- **The acceptance model shifts for cost-driven plans** (reconciling
  [09](09-verification-and-acceptance.md) §2/§3). ch09's classifier lists
  "`MultiHashJoin` where PG uses a hash cascade" and "hash semi/anti where PG uses
  index-NL" as *allowed* goopg divergences — but those describe the **pre-pivot /
  non-cost-driven** planner. For cost-driven plans the target is PG-**shape** match
  (§5): no MHJ is emitted, and NLI-semi is generated where PG uses it, so those two
  allow-list entries no longer apply to the cost path. ch09's classifier stands for
  non-cost-driven paths; cost-driven plans are gated on shape parity. (ch09 §3 to be
  annotated accordingly when this lands.)
- **No GEQO, no extended statistics, no bitmap-scan paths initially** — TPC-H is
  within the exhaustive DP, its correlations are not multi-column-critical for the
  shape, and goopg's index access is equality-probe-shaped
  ([06](06-scan-and-join-path-costs.md) §5).
