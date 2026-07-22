# 06 — Scan and Join Path Costs

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) |
| date | 2026-07-22 |
| depends on | [02](02-pg-path-and-cost-oracle.md), [05](05-statistics-and-estimation-inputs.md) |

## 0. Why this chapter exists

This chapter turns the oracle formulas of [02](02-pg-path-and-cost-oracle.md) into
the goopg path-generation-and-costing functions, and it is where the isolated
heuristics of [01](01-current-state-and-gap-analysis.md) §3 are retired: build
side, join method, and the `MultiHashJoin` probe pick stop being private rules and
become outcomes of `add_path` choosing the cheapest costed path. It also settles
the one operator with no oracle — `MultiHashJoin` — with an explicit comparability
invariant.

## 1. Scan paths

For each base `RelOptInfo`, generate the candidate scan paths and `add_path` them:

- **SeqScan path** — always generated. Cost from `cost_seqscan`
  ([02](02-pg-path-and-cost-oracle.md) §4.1):
  `startup = qual_startup`, `run = seq_page_cost · relpages + (cpu_tuple_cost +
  cpu_operator_cost · qual_ops) · reltuples`, where `relpages = BlocksForTable(rel)`
  and `reltuples = Rel.Rows` before the local-filter selectivity is applied to the
  *output* (the scan reads all rows, emits the selective fraction). Its pathkeys
  are empty.
- **IndexScan path** — generated when an index covers a `baserestrictinfo` clause.
  Cost from `cost_index` ([02](02-pg-path-and-cost-oracle.md) §4.2). It may carry
  the index's ordering as pathkeys ([04](04-pathkeys-and-ordering.md) §3).

`set_cheapest` then picks the base rel's cheapest scan, which seeds the join
search.

### 1.1 The partial scan path

If the rel clears the parallel size ladder (`computeParallelWorkers`,
`parallel.go:459`, run on the live block count), also generate a **partial
SeqScan path**: identical cost formula, but with the page and tuple terms divided
by `get_parallel_divisor` for that worker count ([02](02-pg-path-and-cost-oracle.md) §4.8),
`ParallelWorkers > 0`, and `add_partial_path`'d into `Rel.PartialPathlist`. This
is the object [08](08-parallel-paths-and-degree.md) gathers. Generating it here,
at scan time, is what makes the later parallelize decision a real cost comparison
rather than a bolt-on.

## 2. Join path generation

For each join `RelOptInfo` the DP forms ([07](07-cost-driven-join-order.md)),
generate every applicable join path over the two child rels' cheapest paths and
`add_path` them; `set_cheapest` picks. The candidate methods:

### 2.1 Hash join

Two-stage cost, `initial_cost_hashjoin` then `final_cost_hashjoin`
([02](02-pg-path-and-cost-oracle.md) §4.5). Generate **both** build-side
orientations (build-left and build-right) as separate paths and let `add_path`
keep the cheaper: because the build cost is `startup` and scales with the inner
side, the orientation with the smaller build side wins automatically. **This
retires `chooseInnerJoinAlgo`'s build-side rule and the `SmallDimension` override**
(`bushy.go:846`, `pushdown.go:246`): the small dimension ends up on the build side
because it is cheaper to build, not because it was tagged by name at
`internal/initdb/open.go:2886`. (The `SmallDimension` tag can remain as a
tie-breaker input where statistics are absent, but it is no longer the primary
mechanism.)

### 2.2 Merge join

Cost from `final_cost_mergejoin` ([02](02-pg-path-and-cost-oracle.md) §4.7), which
adds a `cost_sort` on each input whose pathkeys do not already satisfy the merge
clause ([04](04-pathkeys-and-ordering.md) §1). A merge join is chosen only when a
child path already delivers the order cheaply (or the sort pays for itself via a
downstream `ORDER BY`), which is rare in goopg's TPC-H plans today — but it must be
*costed*, not refused, or the model can never discover the cases where it wins.

### 2.3 Nested loop and NLI

Cost from `final_cost_nestloop` ([02](02-pg-path-and-cost-oracle.md) §4.6). For an
inner index path the per-outer-row cost is one parameterised probe, cheap for a
selective outer side. The inner index path is a **parameterized path** — it depends
on the outer key, tracked by `Path.RequiredOuter` and built by `create_plan`'s
nestloop-param threading ([03](03-path-substrate-and-plan-creation.md) §3.1); this
is the one parameterized shape the milestone designs in. This **replaces the
`outerRows ≤ 100000` threshold** in `nliCostGateAccepts` (`nl_index_join.go:1244`)
with a real comparison: the NLI path and the hash path both enter `add_path`, and
NLI wins when `outerRows · probeCost < innerBuildCost` — the selective-outer
condition that threshold was approximating.

### 2.4 Semi / anti

`compute_semi_anti_join_factors` (`costsize.c:5114`) supplies the `match_frac` that
lets a semi join charge only the probe up to the first match. This is the oracle
[correlated-subquery-planning/06](../correlated-subquery-planning/06-cost-model-touchpoints.md) §3.2
named for the NLI-semi/anti cost gate; the cost model subsumes that gate's
`match_frac` table into `final_cost_nestloop` with the semi/anti factor, and the
no-stats fallback stays conservative-hash (a wrong NLI-anti probes the full inner
per outer row — the asymmetry that doc §7 Q2 justifies).

## 3. Join size and reliability

### 3.1 Join-rel rows, once

Each join `RelOptInfo.Rows` is computed once when the DP first forms the subset,
reproducing `set_joinrel_size_estimates`: `|outer| · |inner| · join_selectivity`,
where for an equijoin `join_selectivity = 1 / max(ndistinct(outer.key),
ndistinct(inner.key))` — the shape `estimateJoin` (`cardinality.go`) already uses.
Stored on the joinrel and shared by every join *method* path over it (hash, merge,
nest all produce the same rows), preserving invariant #2.

### 3.2 The sort charge is why pathkeys exist

A merge join over two already-sorted inputs is cheap; over two unsorted inputs it
pays two `cost_sort`s and almost always loses to hash. The **only** way the model
can tell these apart is the child paths' pathkeys ([04](04-pathkeys-and-ordering.md)).
This is the concrete payoff of the two-number cost: a sort's cost is nearly all
startup, so a merge join under a `LIMIT` can beat a hash join whose full output
must materialise, and `add_path` sees it because it compares startup and total
separately.

### 3.3 The no-statistics posture

Where the join key columns carry no reliable statistics, the size estimate holds
the unscaled product and the method costs fall back to their structural defaults
(hash, conservative). The cost model must not manufacture a selective join
selectivity from the `defaultEqSelectivity` constant — that is the exact error
that produced the Round-4 regressions in a different guise
([01](01-current-state-and-gap-analysis.md) §5).

## 4. MultiHashJoin: the operator with no oracle

> **Superseded for the cost-driven path (see [12](12-pg-style-join-path-enumeration.md) §3).**
> This section designed `MultiHashJoin` as a first-class costed DP path under a
> comparability invariant. The C4 pivot **drops MHJ from the cost-driven planner**
> entirely (PG has no MHJ; keeping it created the order-then-rewrite trap that
> regressed Q9, [07](07-cost-driven-join-order.md) §4.5). MHJ remains a valid
> executor operator and a hand-tuned optimisation, but the cost path emits
> PG-shaped binary trees instead. The rest of this section is retained as the
> record of the rejected approach.

`MultiHashJoin` (`internal/executor/multi_hash_join.go`, built by
`rewriteMultiWayChain`, `bushy.go:1193`) is goopg's N-way hash join with **no PG
counterpart**, so no `cost_*` function to copy. It probes one driving table
against several pre-built dimension hash tables in a single pass, avoiding the
intermediate materialisation a left-deep cascade of two-way hash joins produces.

### 4.1 The comparability invariant

Because there is no oracle, the MHJ path must be costed in a way that is
*commensurable* with the plans it competes against, or `add_path` cannot rank it.
The invariant:

> A `MultiHashJoin` path is costed in the same PG units as the equivalent
> left-deep hash cascade over the same tables, and competes against the cascade's
> paths through ordinary `add_path` dominance. It is never selected by a private
> rule, and it survives only when it is not dominated.

Concretely, the MHJ path's cost is the sum, in PG units, of: building each
dimension hash table (`initial_cost_hashjoin`'s build term per dimension, all
`startup`), plus one probe pass over the driving table charging the per-row hash-
and-walk cost across all dimension tables (`final_cost_hashjoin`'s probe term,
summed). This makes its cost directly comparable to the cascade, whose cost is the
same builds plus the same probes **plus** the per-tuple materialisation of each
intermediate result. So the MHJ path wins when eliminating the intermediates pays —
its actual advantage — and loses when it does not, rather than being force-selected
by `rewriteMultiWayChain` running unconditionally.

**Selection is `add_path`, not a strict `≤`.** Because both the MHJ path and the
cascade paths land in the *same joinrel's* pathlist, the winner is decided by
`compare_path_costs_fuzzily` with `STD_FUZZ_FACTOR = 1.01`
([02](02-pg-path-and-cost-oracle.md) §2.1): an MHJ within 1 % of the cascade is
non-dominated and survives to be tie-broken on pathkeys / `parallel_safe` (an MHJ
carries no ordering, so a cascade path with useful pathkeys can retain it). "Chosen
only when it wins" therefore means "chosen only when it is the cheapest
non-dominated path", not a hand-coded threshold — the same rule every other path
obeys.

**Where the MHJ path is generated.** `rewriteMultiWayChain` is a post-DP rewrite
today (`bushy.go:1193`); under the Path model the MHJ path is generated **for the
joinrel covering all N tables it spans**, at the DP level where that subset is
formed. At that subset the DP already builds the cascade paths (the left-deep
two-way hash joins over the same relids); the MHJ path is one more candidate
`add_path`'d into the *same* `RelOptInfo`, so it competes on equal footing over an
identical row estimate ([§3.1](#31-join-rel-rows-once)). This replaces the
unconditional post-DP rewrite with a costed candidate at the correct joinrel.

### 4.2 The probe table falls out of cost

Today `collectMultiHashTables` (`bushy.go:962`) picks the probe table as the one
with the largest `EstimateRows` (the `probeIdx` scan at `bushy.go:1071`). Under the
cost model, generate the MHJ path with
each candidate as the probe (or the largest few, to bound enumeration) and let
`add_path` keep the cheapest — the largest table usually wins because probing is
cheaper per row than building, but now that is a *derived* result the cost can
justify, and a skewed case where it does not can be discovered.

This mirrors how [parallel-query/12](../parallel-query/12-parallel-multi-way-hash-join.md)
already reasons about the MHJ probe side; the cost model makes the choice a price,
not a heuristic.

## 5. Divergence from PostgreSQL

- **`MultiHashJoin` has no oracle** and is costed against the equivalent hash
  cascade under the §4.1 comparability invariant. This is the largest single
  divergence in the bundle and the reason acceptance is never "match PG's plan"
  ([09](09-verification-and-acceptance.md) §3).
- **Build side is derived, not tagged** (§2.1). The `SmallDimension`
  name-tag (`open.go:2886`) degrades to a no-stats tie-breaker rather than the
  primary rule.
- **goopg's index scan materialises the whole TID list eagerly**
  (`operators_index.go`), so `cost_index`'s incremental-fetch model overstates how
  incrementally goopg actually fetches; the milestone's TPC-H plans are hash-join
  dominated, so this rarely bites, and it is noted rather than corrected.
- **Semi/anti no-stats fallback is conservative-hash**, asymmetric with plain
  nested-loop's optimism — inherited from
  [correlated-subquery-planning/06](../correlated-subquery-planning/06-cost-model-touchpoints.md) §7,
  justified by the cost of a wrong anti-probe.
