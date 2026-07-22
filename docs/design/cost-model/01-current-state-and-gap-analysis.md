# 01 — Current State and Gap Analysis

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) |
| date | 2026-07-22 |
| depends on | none — this is the entry point |

## 0. Why this chapter exists

Before proposing a cost model, this chapter fixes on the record exactly what
goopg's planner does today, decision point by decision point, so that later
chapters can say precisely which mechanism each Path cost *replaces*. The single
most important fact is structural and is stated first: **goopg does not compare
plans.** It builds one.

## 1. The planner is single-shot

`planner.Plan` (`internal/planner/planner.go:89`) → `planStmt` → `planSelect`
constructs exactly one executor-Node tree through a fixed pipeline of rewrite
passes. For a SELECT the ordered pipeline is, roughly (inside `planSelect`,
`internal/planner/planner.go:627`, whose body runs these passes in sequence):

1. `reorderCommaFromByCardinality` — a parser-level greedy join-order pre-pass
   (`internal/planner/joinorder.go:60`).
2. `planFromClause` builds a left-deep CROSS chain in FROM order.
3. `tryBushyDP` — the bushy join-order DP (`internal/planner/bushy.go:66`).
4. `pushPredicatesIntoCrossJoins` — turns CROSS into hash joins, choosing the
   build side (`internal/planner/pushdown.go:241`).
5. `unnestSubqueriesInPlan`.
6. `rewriteMultiWayChain` — packs a ≥3-way inner-hash-join chain into one
   `MultiHashJoin` (`internal/planner/bushy.go:1193`).
7. `rewriteJoinsToNLI`, Memoize, and the remaining local rewrites.

Each pass mutates the one tree and commits. There is no pathlist, no
back-tracking, and no second candidate kept alive to be compared later. The
output is a `planner.Node` the executor runs.

**Parallelism is bolted on after the fact.** `MaybeAddGather`
(`internal/planner/parallel.go:84`) runs *outside* the planner, in the server
dispatch layer (`internal/server/dispatch.go:1201`,
`dispatch_extended.go:124`), as a **non-mutating post-pass** over the finished,
plan-cached serial tree. It never reconsiders join order or method; it only
decides where — if anywhere — to splice a `Gather`.

## 2. The one real comparison: the bushy DP

The lone place goopg enumerates and compares alternatives is the bushy join-order
DP, `enumerateBushyPlans` (`internal/planner/bushy.go:500`): a DPccp-style subset
DP over a `uint16` table bitmask, keeping the minimum-`cost` join for each subset.
It runs for 3–12 base relations whose FROM items are scan/`MultiHashJoin` leaves
(`bushy.go:80`).

The metric it minimises is `estimateJoinCost` (`internal/planner/bushy.go:785`),
an **integer** three-part cost:

```
outputRows = leftRows · rightRows / max(NDistinct(L.key), NDistinct(R.key))
cost       = outputRows · 1  +  buildRows · 4  +  probeRows · 1
```

with `outputRowWeight = 1`, `hashBuildWeight = 4`, `hashProbeWeight = 1`
(`bushy.go:759`), build = the smaller side, probe = the larger. Singleton rows
come from `baseRelInfo.filteredRows` (post-local-filter) or `Stats.RowCount` or 1
(`bushy.go:519`). This cost, and `dpEntry.rows`, are the machinery
[fix-for-q5/02](../fix-for-q5/02-cost-model-and-selective-equivalence.md) added.

Three limits matter for later chapters. The cost is **relative** — it is compared
only within one DP run and then discarded; it has no absolute scale and no
per-page or per-tuple CPU term. It is **integer**, so it cannot resolve close
calls. And it prices **only** the hash join's build/probe/output — it has no
model of a sort, a nested loop, an aggregate, or a Gather, so no decision
involving those can be a cost comparison against a join.

## 3. Everything else is an isolated heuristic

The decisions a real cost model unifies, each currently a private rule with its
own constants:

| decision | function (`internal/planner/…`) | current rule |
| --- | --- | --- |
| join method (inner) | `chooseInnerJoinAlgo` `joincost.go:19` | min of unit-row `hash`, `merge = n·log₂n` sort, `nestloop = l·r`; hash on ties |
| hash build side | `buildJoinFromDP` `bushy.go:846`; `pushdown.go:246` | smaller side, with a `SmallDimension` override |
| MHJ probe table | `collectMultiHashTables` `bushy.go:962` | the scan with the largest `EstimateRows` (probe pick at `:1071`) |
| NLI vs hash (inner) | `nliCostGateAccepts` `nl_index_join.go:1244` | accept if `outerRows ≤ 100000` (unknown → accept) |
| NLI unwrap | `innerUnwrapCostAccepts` `nl_index_join.go:1335` | `outerRows·(matchSet+residual) < innerRows+outerRows`; unknown → **decline** |
| SubPlan placement | `estimateSubplanCostPerCall` `subplan_cost.go:29` | per-call rescan cost; unknown → 0 |
| parallel worker count | `computeParallelWorkers` `parallel.go:459` | PG's block-size ×3 ladder |
| Gather placement | `findPartialSubtree` `parallel.go:243` | structural push-down |
| parallel-agg split | `splitAggregateIsProfitable` `parallel_agg.go:260` | ratio `ρ = min(1,(ndistinct/R)·d)` self-contained comparison |

The last row is the exception that proves the rule: it is the **only** gate today
shaped like a cost model, and it exists only because
[parallel-query/11](../parallel-query/11-partial-aggregation-cost-model.md) could
build a *self-contained* comparison (the split and no-split alternatives share
their whole subtree, so the shared cost cancels and no absolute scale is needed).
Every other gate makes a local decision with a threshold nobody can relate to any
other gate's threshold, because there is no common cost unit.

## 4. There is real estimation — it is just not a cost model

goopg is not estimation-free. It has genuine, if "deliberately rough", cardinality
and selectivity layers, all reading in-memory statistics:

- `EstimateRows(n)` (`internal/planner/cardinality.go:38`) — bottom-up per-node
  row estimate; `0` means "no estimate". `SeqScan → Stats.RowCount`, `Filter →
  child · selectivity`, `Join → estimateJoin` (`|L|·|R| / max(ndistinct)`),
  `Aggregate → estimateAggregate`.
- `clauseSelectivity` / `clauseSelectivityWithSource`
  (`internal/planner/selectivity.go`) — returns `selectivityEstimate{value,
  reliable}`, where `reliable` is true only when the estimate came from real
  column statistics, not the `defaultEqSelectivity = 0.005` / `1/3` fallbacks
  (`cardinality.go:25`).

What is missing is not estimation. It is **cost**: a function from
`(rows, width, node type, ...)` to a `(startup, total)` pair in a unit that is the
same for a scan, a sort, a join, and a Gather, so that any two plans can be ranked
and any operator's price can be weighed against any other's. That single missing
abstraction is this bundle.

## 5. The Round-4 evidence: why "rough" stopped being enough

[`analysis/tpch-evolution-round4-parallel-query-20260722.md`](../../../analysis/tpch-evolution-round4-parallel-query-20260722.md)
measured what happens when statistics are switched on for the whole TPC-H stream.
The result is the argument for this bundle, so it is quoted in numbers:

| query | R3 (no stats) | R4 w0 (stats, serial) | factor | attributed to |
| --- | ---: | ---: | ---: | --- |
| Q5 | 415 s | 18 s | **0.04×** (fixed) | stats let the DP find a good order |
| Q22 | 0.8 s | 103 s | **128×** | join order the DP cannot cost |
| Q4 | 3.4 s | 269 s | **79×** | NL-Semi flipped to a Hash-Semi building 6 M-row `lineitem` |
| Q8 | 3.8 s | 200 s | **53×** | 8-way tree rearranged, `lineitem` buried in an MHJ |
| Q2 | 2.6 s | 67 s | **26×** | join order the DP cannot cost |
| Q12 | 27 s | 121 s | **4.4×** | join order the DP cannot cost |

The `w0` column is the serial, statistics-on sweep, so these are **not**
parallelism effects — parallelism (the `w2/w4/w8` sweeps) never regressed a single
query. Every one of the 24 row counts matched R3 exactly: the regressions are
plan *choice*, not plan *correctness*. The mechanism is uniform. Without
statistics the DP leaned on a robust heuristic — hard-tagging `region`/`nation` as
small dimensions (`SmallDimension`, set by name at `internal/initdb/open.go:2886`)
— which is suboptimal but never catastrophic. With statistics the DP is handed
real cardinalities and makes aggressive join-order and build-side choices whose
consequences its integer, join-only cost cannot see: it cannot tell that a
particular order forces a 6-million-row hash build, because it has no term for the
build's absolute cost relative to the rest of the plan.

That is the precise failure a PG-faithful cost model removes: it gives the DP a
term for every operator, in one unit, so the order that builds `lineitem` is
visibly more expensive than the order that does not.

## 6. What "roughly appropriate plans" will mean

The milestone is deliberately *not* "match PostgreSQL's plans" — [09](09-verification-and-acceptance.md)
makes the case that this is both unachievable (goopg's executor has operators PG
lacks, and lacks some PG has) and the wrong target. The concrete, measurable bar
this bundle commits to:

1. the five Round-4 regressions (Q22, Q4, Q8, Q2, Q12) recover, **without**
   surrendering the Q5 win;
2. no TPC-H row count changes (the cost model re-selects, never re-estimates);
3. the parallelize decision and worker count become cost/size decisions that a
   plan snapshot can be shown to make sensibly.

The rest of the bundle builds the machinery to hit that bar and the gates
([09](09-verification-and-acceptance.md)) that prove it.

## 7. Divergence from PostgreSQL

This chapter is descriptive, so its divergences are the ones the bundle *starts*
from, not ones it introduces:

- goopg plans by rewriting one tree; PG plans by enumerating paths. Closing this
  gap is the whole bundle.
- goopg's `EXPLAIN` cost is a literal (`operators_explain.go:378`); PG's is the
  chosen path's real cost. [03](03-path-substrate-and-plan-creation.md) §5 makes
  goopg's real.
- goopg has a `MultiHashJoin` PG does not, and (today) hash-only semi/anti where
  PG has index-nested-loop variants. These are permanent, principled divergences
  the acceptance criteria account for, not gaps to close.
