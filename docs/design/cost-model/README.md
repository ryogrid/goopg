# A Cost Model for the goopg Planner — Design Bundle

| field | value |
| --- | --- |
| status | draft — **DESIGN ONLY**, implementation not started |
| date | 2026-07-22 |
| branch | authored on `introduce-costmodel`; targets the planner regardless of branch |
| scope | introduce a PostgreSQL-faithful **Path / cost** model into goopg's planner: `RelOptInfo`/`Path` objects, `(startup, total)` cost in PG's units, `add_path`/`set_cheapest`, `partial_pathlist` for parallelism, and `create_plan` (Path → executor Node) |
| non-goals | a new executor; parallel index scan / parallel DML / SERIALIZABLE parallelism (owned by [parallel-query/](../parallel-query/README.md)); extended-statistics-driven costing; a genetic join optimiser (GEQO) |
| baseline | PostgreSQL 18.3 (`postgres/` submodule, read-only) — the cost **oracle** |
| milestone 1 | TPC-H SF1: roughly appropriate plans are selected — **including whether to parallelize a query and how many workers** — and the Round-4 statistics regressions are recovered without losing the Round-4 statistics win |

## The problem, in one measurement

goopg's planner has **no absolute cost model**. `EXPLAIN` prints
`cost=0.00..0.00 rows=N width=0`, and the `cost=` and `width=` halves are
literals — `internal/executor/operators_explain.go:378`; only `rows=` is real.
There is no `Path`, no `add_path`, no `set_cheapest`, and no node carries a
`(startup, total)` pair. `internal/planner/parallel.go:8` states it plainly:
*"goopg's planner has no path abstraction to extend."*

The planner is **single-shot**: `planner.Plan` (`internal/planner/planner.go:89`)
builds exactly one executor-Node tree through a fixed sequence of rewrite
passes, each making a local, greedy decision. The single place that compares
alternatives is the bushy join-order DP (`enumerateBushyPlans` /
`estimateJoinCost`, `internal/planner/bushy.go:500` / `:785`), and it minimises
an **integer** relative cost, `outputRows·1 + build·4 + probe·1`. Everything
else — the join method (`chooseInnerJoinAlgo`, `internal/planner/joincost.go:19`),
the hash build side, the `MultiHashJoin` probe-table pick, the nested-loop-index
gates, the parallel worker count, the partial-aggregate split — is an isolated
heuristic with its own private rule.

This is survivable **until statistics are turned on**. The Round-4 measurement
([`analysis/tpch-evolution-round4-parallel-query-20260722.md`](../../../analysis/tpch-evolution-round4-parallel-query-20260722.md))
is this bundle's motivation. Running `ANALYZE` before the TPC-H stream **fixed**
Q5 (415 s → 18 s, 23×) but **broke** five queries — Q22 (128×), Q4 (79×), Q8
(53×), Q2 (26×), Q12 (4.4×) — because real statistics let the join-order DP make
aggressive choices its integer cost function cannot price. Every row count stayed
correct; only plan **choice** regressed. The document's own conclusion:

> The most actionable follow-up this round exposes is a cost model. Statistics
> are not safe to keep on until the planner can cost the join orders they enable.

This bundle is that cost model.

## This is "the 0077 cost-model line"

The repository has deferred this work by name, more than once, always to a future
bundle. This is that bundle, and it **discharges** those deferrals:

- [`parallel-query/10-roadmap.md`](../parallel-query/10-roadmap.md) — *"A real
  cost model … PG's `parallel_setup_cost` / `parallel_tuple_cost` need a
  `(startup, total)` pair that does not exist … Its own bundle; benefits far more
  than parallelism."*
- [`correlated-subquery-planning/06-cost-model-touchpoints.md`](../correlated-subquery-planning/06-cost-model-touchpoints.md)
  §1, §6 — *"real cost surfacing in `EXPLAIN`, selectivity refinement,
  per-operator cost constants … stays with the 0077 cost-model line."*
- [`fix-for-q5/02-cost-model-and-selective-equivalence.md`](../fix-for-q5/02-cost-model-and-selective-equivalence.md)
  — the integer three-part join cost (`outputRows·1 + build·4 + probe·1`) and the
  `baseRelInfo.filteredRows` post-filter estimates were "enough for Q5"; this
  bundle turns those integer weights into PG-unit floats.

It also **generalises** the one existing cost-model-shaped gate in the tree:
[`parallel-query/11-partial-aggregation-cost-model.md`](../parallel-query/11-partial-aggregation-cost-model.md)
built a self-contained *ratio* comparison for a single decision (whether to split
an aggregate) precisely because there was no absolute cost model to appeal to.
This bundle provides the absolute model, and chapter 11's split becomes a special
case of it ([08](08-parallel-paths-and-degree.md) §5).

## The architecture, and why the larger one was chosen

Two shapes were possible:

1. **Bolt-on.** Adopt PG's cost *units* and carry a `Cost` computed by a
   `costOf(node)` recursion over the existing single-shot Node tree, feeding the
   one DP and converting each isolated gate into a cost comparison. Incremental,
   reaches the milestone fastest.
2. **Reproduce PG's Path model.** First-class `RelOptInfo` / `Path` objects with
   `(startup, total)` cost, `add_path` dominance pruning, `set_cheapest`,
   `partial_pathlist`, and a `create_plan` step that turns the winning `Path`
   into an executor Node.

**This bundle designs (2).** It is the larger, higher-risk change, and it is the
right one for three reasons. First, it is what goopg's whole project is: a
byte-faithful reproduction of PostgreSQL, and PG's cost model is *inseparable*
from its Path enumeration — `set_cheapest` has no meaning without a pathlist.
Second, it makes the parallelize decision **honest**: PG does not ask "is the
serial plan cheaper than the serial plan with a Gather bolted on top" — it
compares the cheapest *partial* path, gathered, against the cheapest serial path,
where the partial path's per-node costs are already divided by the parallel
divisor ([08](08-parallel-paths-and-degree.md) §2). A bolt-on cannot express that
without inventing the partial path anyway. Third, `add_path`'s fuzzy cost
comparison (`compare_path_costs_fuzzily`, `STD_FUZZ_FACTOR = 1.01`) is exactly the
tie-break discipline an integer→float migration needs, and it comes for free by
reproducing it.

The cost of the choice is stated where it is paid: a new `create_plan` layer
([03](03-path-substrate-and-plan-creation.md) §3), a scoped pathkeys sub-system
([04](04-pathkeys-and-ordering.md)), and a roadmap whose first observable
behaviour change is several phases in ([11](11-roadmap.md)).

## Four invariants this bundle must not violate

Extracted up front because every chapter leans on them, and a review of an
earlier internal draft found each one hiding a real bug:

1. **One cost function, absolute-faithful everywhere.** "Relative cost is enough
   for join order" is a trap: the parallelize decision compares a plan's total
   against the absolute constant `parallel_setup_cost = 1000`, so any per-tuple
   term the DP omits as "rank-preserving" corrupts the number the parallel gate
   later reads. There is no relative-only sub-mode.
2. **A single source of truth for rows.** The cost model re-*selects* plans; it
   must never re-*estimate* cardinality. `RelOptInfo.rows` is computed once and
   costing consumes it — costing never calls `EstimateRows` again. This is what
   preserves Round-4's one safe property (all row counts stayed correct).
3. **Worker count stays the size ladder.** PG does **not** cost-optimise the
   number of workers — `compute_parallel_worker` (`allpaths.c:4274`) is a pure
   block-size ladder, which goopg already mirrors in `computeParallelWorkers`
   (`parallel.go:459`). Only *whether to parallelize* is a cost decision. Ripping
   out the ladder to cost the "split count" would be a divergence *from* the
   oracle, not toward it.
4. **The milestone does not depend on statistics persistence.** Column statistics
   persist and restore; `reltuples` does not (ledger `pq-P6`). The model rides
   the `estimate_rel_size` live-block fallback ([05](05-statistics-and-estimation-inputs.md) §4)
   so it works on a freshly started server, and persistence is designed last
   ([10](10-statistics-persistence.md)) as cold-start future-proofing.

## Documents in this bundle

| # | chapter | what it settles |
| --- | --- | --- |
| — | [README](README.md) | this page |
| 01 | [current-state-and-gap-analysis](01-current-state-and-gap-analysis.md) | the single-shot planner, the one DP, the isolated gates, the Round-4 evidence |
| 02 | [pg-path-and-cost-oracle](02-pg-path-and-cost-oracle.md) | PG's `RelOptInfo`/`Path`/`add_path`/`set_cheapest` and every cost function, as the oracle |
| 03 | [path-substrate-and-plan-creation](03-path-substrate-and-plan-creation.md) | the goopg `Path` types, `add_path`/`set_cheapest`, and `create_plan` (Path → Node) |
| 04 | [pathkeys-and-ordering](04-pathkeys-and-ordering.md) | the minimal ordering sub-system (ORDER BY, merge join, Gather Merge) and what is deferred |
| 05 | [statistics-and-estimation-inputs](05-statistics-and-estimation-inputs.md) | row/width estimation, reliability, and the persistence-independent `estimate_rel_size` fallback |
| 06 | [scan-and-join-path-costs](06-scan-and-join-path-costs.md) | scan and join path generation + costs, and the MultiHashJoin comparability invariant |
| 07 | [cost-driven-join-order](07-cost-driven-join-order.md) | the DP over pathlists; how real cost recovers the Round-4 regressions |
| 08 | [parallel-paths-and-degree](08-parallel-paths-and-degree.md) | partial paths, the honest parallelize decision, and worker degree |
| 09 | [verification-and-acceptance](09-verification-and-acceptance.md) | the three-tier acceptance bar and honest measurement |
| 10 | [statistics-persistence](10-statistics-persistence.md) | PG-like `reltuples`/`relpages` persistence — **deferred, designed for later** |
| 11 | [roadmap](11-roadmap.md) | phases C0…C7, gates, and the deliberately-deferred table |

## Reading order

[01](01-current-state-and-gap-analysis.md) then [02](02-pg-path-and-cost-oracle.md)
establish the gap and the oracle. [03](03-path-substrate-and-plan-creation.md) is
the load-bearing structural chapter — read it before anything downstream.
[04](04-pathkeys-and-ordering.md)–[06](06-scan-and-join-path-costs.md) build the
inputs and the per-node costs; [07](07-cost-driven-join-order.md) and
[08](08-parallel-paths-and-degree.md) are where plans change and the milestone is
won; [09](09-verification-and-acceptance.md) is how we know it worked.
[10](10-statistics-persistence.md) and [11](11-roadmap.md) are last by design.

## Divergence from PostgreSQL

Every chapter ends with an explicit **Divergence from PostgreSQL** section. The
standing divergences, stated once here and refined where they bite:

- **No `Plan` freeze from `Path` alone.** PG's `create_plan` produces a `Plan`
  tree that the executor runs directly; goopg's executor runs its existing Node
  types, so `create_plan` is a *translation* to those Nodes, not a new executor
  IR ([03](03-path-substrate-and-plan-creation.md) §3).
- **`MultiHashJoin` has no PG counterpart**, so it has no oracle cost. It is
  costed against the equivalent left-deep hash cascade in the same units and
  chosen only when it wins ([06](06-scan-and-join-path-costs.md) §4).
- **Partial aggregate state crosses a mutex, not the Gather**
  ([parallel-query/11](../parallel-query/11-partial-aggregation-cost-model.md) §2.1),
  so `cost_gather` over a partial aggregate is reconciled, not copied
  ([08](08-parallel-paths-and-degree.md) §5).
- **PG is the oracle for cost *functions and constants*, never for the final
  *plan*.** goopg legitimately picks plans PG cannot express (an MHJ; historically
  no index-nested-loop). Acceptance is therefore never "match PG's plan"
  ([09](09-verification-and-acceptance.md) §3).
