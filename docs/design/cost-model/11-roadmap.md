# 11 — Phased Roadmap

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) |
| date | 2026-07-22 |

Eight phases. The ordering principle mirrors
[parallel-query/10](../parallel-query/10-roadmap.md): **the substrate is proven
before any plan changes**, and the single phase that first changes plan choice
(C4) is late enough that the Path objects, the cost functions, and the estimation
inputs it depends on have each landed and been gated in isolation. The milestone —
TPC-H reasonable plans including the parallelize/degree decision — is met at the end
of **C5**; C6 is observability polish and C7 is deferred.

Every phase carries the standard gates ([09](09-verification-and-acceptance.md) §5):
units, `make race-gate`, `scripts/tpch-spotcheck.sh` (Q12 = 2 / Q13 = 33),
`make plan-gate`, and the pre-commit pgbench smoke. Only the **additional** gate is
listed per phase.

## C0 — Path substrate and create_plan

The whole of [03](03-path-substrate-and-plan-creation.md), with **no plan change**.

- `Path` / `RelOptInfo` / `Cost` types (§1); `add_path` / `set_cheapest` with the
  fuzzy comparator (§2); `create_plan` translating a chosen path to the existing
  executor Nodes (§3).
- The DP still selects by its current integer cost; the winning subtree is routed
  **through** `create_plan`, which is fed a path *trivially built from the integer
  DP's chosen order* ([03](03-path-substrate-and-plan-creation.md) §3.1 staging
  note), so the Path→Node round-trip is exercised on every query while selection is
  unchanged.

**Gate: plan-gate zero diffs.** The substrate is correct exactly when it produces
byte-identical plans to today. This is the phase's whole point — it de-risks the
largest structural change before it can alter a single result. Note `add_path` /
`set_cheapest` are *landed* here but not yet *exercised* by selection (nothing feeds
them competing paths until C3), so their own gate is deferred to C3.

## C1 — Pathkeys (minimal)

The whole of [04](04-pathkeys-and-ordering.md): the `PathKey` type,
`pathkeysContainedIn`, and the produce/consume sites. Nothing selects on ordering
yet (no merge or Gather-Merge path is generated until C3/C5), so no plan moves.

**Gate: plan-gate zero diffs.** A unit test for `pathkeysContainedIn` (prefix
containment; the deliberate false-negative on an equivalence-class case, §2.1).

## C2 — Estimation inputs

The whole of [05](05-statistics-and-estimation-inputs.md): `RelOptInfo.Rows` via the
`set_baserel_size_estimates` analogue (§1), the tuple-**width** estimator (§2), and
the `estimate_rel_size` **row fallback** off the live block count (§4).

**Gate: rows-invariant warm** — with in-session ANALYZE, `EXPLAIN rows=` is
byte-identical to today. Plus a **dedicated cold-start test**: after a restart with
no re-ANALYZE, `baseRows` returns the block-derived estimate rather than 0
([05](05-statistics-and-estimation-inputs.md) §4), asserted directly (the one place
an estimate legitimately changes, and it changes from "blind" to "coarse", never a
plan-gate regression because plan-gate runs warm).

## C3 — Cost functions and path generation

[02](02-pg-path-and-cost-oracle.md) §4 and [06](06-scan-and-join-path-costs.md):
implement each `cost_*` function against the oracle, and generate the costed scan
and join paths (both hash build orientations, merge, nest/NLI, the MHJ
comparability path) into each rel's pathlist. **Selection still uses the integer
DP** — the costed pathlists are built alongside but not yet consulted, so no plan
changes.

**Gate: unit cost checks vs the oracle** ([09](09-verification-and-acceptance.md) §6)
— `cost_seqscan`, `get_parallel_divisor(2) = 2.4`, `initial_cost_hashjoin`, etc.,
each pinned to a hand-computed PG number. **Plus a `add_path` / `set_cheapest`
dominance unit gate** (deferred from C0): assert a dominated path is dropped, a
better-ordered dearer path survives, two within-fuzz paths tie-break
deterministically, and `disabled_nodes` (trivially 0 in goopg) does not perturb the
result — because these are landed in C0 but only now have competing costed paths to
act on. **Plan-gate zero diffs** (selection still on the integer DP).

## C4 — Switch join order to the costed pathlists

The first phase where behaviour changes. [07](07-cost-driven-join-order.md): retire
the integer `estimateJoinCost`; the DP composes subsets via `add_path` /
`set_cheapest` over the C3 pathlists; the top rel's cheapest path drives
`create_plan`. The greedy comma reorder retires within the DP's 12-relation range
(§1.1).

**Gate: the milestone bar, Tier 1 + Tier 2 + Tier 3** ([09](09-verification-and-acceptance.md)):
rows byte-identical (Tier 1) and the double-plan determinism check (§4); the five
Round-4 regressions recover **without** losing Q5, on SF1 (Tier 2); and a **large,
expected plan-gate diff** classified against the divergence allow-list, every entry
explained before recapture (Tier 3). This is the phase to be slowest on — it
changes what every query gets.

## C5 — Parallel paths and the parallelize decision

[08](08-parallel-paths-and-degree.md): partial paths at scan and join level
(divisor-divided cost), `generate_gather_paths` adding Gather / Gather-Merge paths
into the serial pathlist, `set_cheapest` making the parallelize decision; the
worker count stays the size ladder (§4); the partial-aggregate split becomes the
two-path special case (§5, carrying chapter 11's mutex-merge cost). This completes
the milestone's "whether to parallelize and how many workers".

**Gate:** the serial ≡ parallel **identity gate** over the corpus still holds
(unchanged results); a plan snapshot shows the parallelize decision and degree are
made sensibly (a large scan-bound query gathers, a tiny one does not); the worker
count matches the ladder for known table sizes; race-gate under a parallel workload.

## C6 — Surface real cost and width in EXPLAIN

[03](03-path-substrate-and-plan-creation.md) §5: replace the literal
`cost=0.00..0.00 ... width=0` (`operators_explain.go:378`) with the chosen path's
`Cost{Startup, Total}` and the rel's `Width`. Discharges
[correlated-subquery-planning/06](../correlated-subquery-planning/06-cost-model-touchpoints.md) §6.

**Gate: a large, expected plan-gate diff** (every query's cost/width line), reviewed
and recaptured; `rows=` unchanged. Cost *numbers* are not gated against PG
([09](09-verification-and-acceptance.md) §3) — only that they are internally
consistent and non-zero.

## C7 — Statistics persistence *(deferred)*

The whole of [10](10-statistics-persistence.md): append-and-reload
`reltuples`/`relpages` into `pg_class`, real `stawidth`, via `UpdateRelStats`.
**Not required for the milestone** — it removes the cold-start `RowCount == 0`
fallback for analysed relations and nothing else. Blocked on the second-ANALYZE-
after-restart investigation ([10](10-statistics-persistence.md) §4); reopened when a
cold-start accuracy gap is shown to matter.

**Gate:** two ANALYZE + restart cycles round-trip `reltuples` (the currently-failing
case); autovacuum is no longer suppressed on restart
([10](10-statistics-persistence.md) §5).

## Deliberately deferred

| Item | Why deferred | Reopen when |
| --- | --- | --- |
| Statistics persistence (C7) | The milestone rides the `estimate_rel_size` live-block fallback; persistence needs a runtime in-place on-disk-catalog update path goopg lacks, and its second-restart bug is unsolved ([10](10-statistics-persistence.md) §2,§4) | a cold-start accuracy gap is measured, or the append-reload restart bug is root-caused |
| `ANALYZE` invalidates the plan cache | `pc.Invalidate()` fires only on DDL (`dispatch.go:2974`); a stale cached *join order* is a valid plan, and the parallel decision (which most needs fresh stats) already runs post-cache ([03](03-path-substrate-and-plan-creation.md) §4.1) | a query is shown to keep a stale join order across an in-session ANALYZE that matters |
| `disable_cost` / `enable_*` GUCs | goopg has no `enable_hashjoin`-style knobs; `add_path` prunes by cost without a disabled-node penalty ([02](02-pg-path-and-cost-oracle.md) §2.2) | operator-level plan forcing becomes a testing or parity need |
| Equivalence-class pathkeys | Syntactic pathkey comparison is a false-negative-only approximation; the EC builder exists (`equiv_class.go`) but wiring it is a refinement TPC-H does not need ([04](04-pathkeys-and-ordering.md) §2.1) | a query pays a redundant sort EC-aware pathkeys would elide |
| Haas–Stokes n_distinct estimator | The ratio/fraction formulation and join costing tolerate the sample-count saturation; the negative-stadistinct fraction already carries the correct value on disk ([05](05-statistics-and-estimation-inputs.md) §5) | a query mis-plans on a saturated high-cardinality n_distinct |
| Incremental sort, merge-append ordering | goopg's executor lacks the operators, so the cost model has nothing to plan toward ([04](04-pathkeys-and-ordering.md) §4) | the operators exist |
| Index-scan incremental-fetch cost accuracy | goopg materialises the whole TID list eagerly (`operators_index.go`), so `cost_index`'s incremental model overstates goopg's actual behaviour; TPC-H is hash-join dominated ([06](06-scan-and-join-path-costs.md) §5) | an index-scan-bound workload appears |
| Extended statistics, GEQO | No planner code reads `pg_statistic_ext`; the DP handles ≤12-relation joins without a genetic fallback ([02](02-pg-path-and-cost-oracle.md) §7) | multi-column correlation or >12-relation joins dominate a workload |

## A note on sequencing risk

As in the parallel-query bundle, the largest risk is that C0–C3 build substantial
machinery — the whole Path layer, `create_plan`, every cost function — that produces
**no observable change** until C4. That is deliberate: the alternative is switching
selection and debugging the substrate, the cost formulas, and the estimation inputs
simultaneously. The mitigation is that each of C0–C3 has a gate that genuinely fails
if the phase is wrong — byte-identical plans for C0/C1/C3, and the unit cost checks
for C3 — so none is "landed and looks fine". C4 is then a change of *selection* over
machinery already proven, not a leap.

## Divergence from PostgreSQL

This chapter is a sequencing plan, so its divergences are those of the chapters it
orders, not new ones. Two are worth restating as roadmap facts: the milestone is met
at **C5** with statistics persistence (**C7**) still deferred — goopg reaches
"reasonable plans including parallelize/degree" on the `estimate_rel_size`
live-block fallback, whereas PG always has persisted `reltuples`
([05](05-statistics-and-estimation-inputs.md) §4, [10](10-statistics-persistence.md)).
And unlike PG, goopg lands the entire Path substrate (C0) in a **plan-preserving**
phase gated on byte-identical output before any selection changes — a staging PG's
own history did not need, because PG was built path-first. The per-chapter Divergence
sections are the authority for the rest.
