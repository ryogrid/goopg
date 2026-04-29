# Milestone 0006 — Planner-Grade Statistics

**Status:** planned
**Depends on:** Milestone 0003 (delivered v0 stats — `TableStats.RowCount` /
`Pages` / `AvgWidth`, `ColumnStats.NDistinct` / `NullFrac`, and the bottom-up
`planner.EstimateRows` cardinality estimator).
**Drives:** Better TPC-H plan quality at SF1 and beyond, and the foundation
for any cost-driven algorithm-selection work in later milestones.

## Context

Milestone 0003 introduced a deliberately minimal statistics layer so the
cost-based planner could leave the ground:

- `ANALYZE` does a full-table scan and records `RowCount`, `Pages`,
  `AvgWidth` per relation, plus `NDistinct` and `NullFrac` per column.
- `planner.EstimateRows` flows row counts bottom-up. It drives hash-join
  build-side selection and a cost-aware join-order pre-pass.
- MCV lists, histograms, sampling, catalog persistence of stats,
  multivariate stats, and any planner consumption of those are explicitly
  deferred. See "Out of scope (deferred to subsequent loops)" in
  `docs/design/0003-0010-analyze-statistics.md` and
  `docs/design/0003-0003-statistics-and-cardinality.md`.

That deferral list has now accumulated into a coherent next step: stand up
upstream-shaped per-column statistics, persist them, and let the planner
actually use them. Today every `Filter` node multiplies its child estimate
by a constant `1/3`, every equality predicate falls back to either NDistinct
or `1/200`, and the choice between hash / merge / nested-loop joins for
INNER joins remains a rules-based decision (see "Algorithm-vs-algorithm
choice … is still open as a future refinement" in `.ralph/fix_plan.md`'s
M0003 cost-based-planner block). This milestone closes all three gaps.

This milestone is upstream-shape-faithful: the statistics produced and the
selectivity rules consuming them mirror upstream PostgreSQL's
`pg_statistic` model and `clauselist_selectivity` decomposition closely
enough that operators can reason about plans in the same vocabulary.

## In Scope

### Statistics Collection

- **Sampling-based ANALYZE.** Replace the v0 full-table scan with a
  reservoir-sampling collector. Sample size scales with a per-column
  statistics target, defaulting to upstream's 100, capped per-relation by
  the same multiplier upstream uses.
- **MCV lists.** Per-column most-common-values + frequencies for skewed
  columns, with the slot count controlled by the statistics target. The
  MCV-vs-histogram split rule follows upstream: a value is "common enough"
  to be in the MCV list when its frequency exceeds the average frequency
  of the remaining values by a documented margin.
- **Equi-depth histograms.** Per-column histograms over the non-MCV
  portion of the column, with the bucket count controlled by the
  statistics target. Used for range-predicate selectivity.
- **`default_statistics_target` GUC.** Upstream-named integer GUC that
  controls MCV-slot count and histogram-bucket count globally. Per-column
  override via `ALTER TABLE … ALTER COLUMN … SET STATISTICS n` is in scope
  as a parser stub only; full per-column targets may be deferred to a
  later loop without losing the milestone.

### Catalog Persistence

- **Stats persistence through the catalog snapshot.** Today the catalog
  persistence machinery serialises Tables but not their `Stats` (per
  `0003-0010-analyze-statistics.md`). Extend the snapshot wire format to
  carry `TableStats` plus `ColumnStats` (including MCV + histogram
  payloads) so `ANALYZE` survives a clean stop / start. The format must be
  forwards-compatible with the existing snapshots that omit stats: an old
  snapshot must still load cleanly and simply present unanalysed
  relations.

### Planner Consumption

- **Filter-clause selectivity that uses the new stats.** Replace the flat
  `1/3` generic multiplier in `EstimateRows`'s Filter case with predicate
  decomposition along the lines of upstream `clauselist_selectivity`:
  - `col = const` → MCV-frequency lookup, falling back to
    `(1 - sum(MCV freq)) / NDistinct(non-MCV)`, then to the existing
    no-stats `1/200`.
  - `col IN (v1, v2, …)` → sum of per-value selectivities (MCV-aware).
  - `col < const`, `col <= const`, `col > const`, `col >= const`,
    `col BETWEEN a AND b` → histogram-bucket interpolation using the
    stored boundary values.
  - Conjunction (`AND`) → product of clause selectivities under the
    independence assumption (upstream's default).
  - Disjunction (`OR`) → inclusion-exclusion across operands.
  - Anything not recognised falls through to the existing `1/3`
    constant.
- **Cost-driven INNER-join algorithm selection.** Today the planner picks
  between hash, merge, and nested-loop based on predicate shape and a few
  rules (see `internal/planner` / `0003-0002-join-executors.md`). Add a
  cost function that scores the available algorithms using the estimated
  input sizes and key NDistinct, and let the cheapest plan win for INNER
  joins. The existing rules remain the documented fallback when stats are
  unavailable. RIGHT / FULL / LEFT semantics-driven choices stay as they
  are.
- **Stats-aware EXPLAIN.** Surface enough of the new picture in EXPLAIN
  text output for operators to verify the planner is consuming the stats:
  per-node selectivity for `Filter`, the chosen join-algorithm justification
  ("build=left", "merge"), and a marker on `Seq Scan` lines indicating
  that MCV / histogram stats are present.

## Out of Scope

- **Cross-column / multivariate statistics.** Upstream's `CREATE STATISTICS`
  with `ndistinct`, `dependencies`, and `mcv` slot kinds. Deferred.
- **Extended statistics over expressions** (`CREATE STATISTICS … ON (expr)`).
  Deferred.
- **Auto-vacuum-driven auto-analyze.** Manual `ANALYZE` (and any cron-style
  trigger from a higher milestone) remains the only entry point. Deferred.
- **Parallel sampling.** Single-threaded sampling is acceptable; analyze is
  not on the critical query path.
- **Functional-dependency / dependency-pair statistics.** Out of scope.
- **Detailed cost model with disk / CPU constants.** This milestone aims at
  cardinality and basic algorithmic preference; full upstream-style cost
  units (`seq_page_cost`, `random_page_cost`, etc.) are out of scope unless
  trivially required to make the algorithm-selection decision land.

## Required Design Docs

Place under `docs/design/` with sequential numbering at creation time:

- `0006-0001-sampling-and-mcv-histograms.md` — sampling algorithm,
  MCV-vs-histogram split rule, target-driven sizing, reproducibility /
  seeding policy.
- `0006-0002-stats-persistence.md` — wire format for serialising
  `TableStats` and `ColumnStats` (including MCV + histogram payloads)
  through the existing catalog snapshot path; forward-compat with
  snapshots that omit stats.
- `0006-0003-clauselist-selectivity.md` — predicate decomposition rules,
  per-shape selectivity formulas, conjunction / disjunction combination,
  fallback ladder, cross-references to upstream `selfuncs.c`.
- `0006-0004-join-algorithm-selection.md` — cost model for hash vs merge
  vs nested loop on INNER joins; how it integrates with the existing
  rules-based `JoinAlgoHash` / `JoinAlgoMerge` decisions and with the
  cost-driven join-order pre-pass from M0003.

These design docs supersede the corresponding "Out of scope (deferred)"
sections of `docs/design/0003-0003-statistics-and-cardinality.md` and
`docs/design/0003-0010-analyze-statistics.md`; both should gain a
`Refines` / `Superseded by` cross-link when M0006 begins implementation.

## Reference

Upstream sources to consult:

- `postgres/src/backend/commands/analyze.c` — sampling driver, MCV /
  histogram construction (`compute_scalar_stats`, `compute_distinct_stats`).
- `postgres/src/include/catalog/pg_statistic.h` — `stadistinct`,
  `stanullfrac`, `stakind*`, MCV / histogram array shape.
- `postgres/src/backend/utils/adt/selfuncs.c` — `clauselist_selectivity`,
  `eqsel`, `scalarineqsel`, `histogram_selectivity`, `mcv_selectivity`.
- `postgres/src/backend/optimizer/path/costsize.c` — join-path cost,
  `final_cost_hashjoin` / `final_cost_mergejoin` / `final_cost_nestloop`.

## Definition of Done

1. `ANALYZE` on a representative TPC-H-shaped relation produces MCV lists,
   equi-depth histograms, and NDistinct populated via reservoir sampling
   sized by `default_statistics_target`.
2. The new statistics survive a clean stop / start of the cluster via the
   catalog snapshot. Old snapshots without stats still load and simply
   present unanalysed relations.
3. `planner.EstimateRows` for `Filter` consumes the new stats: equality on
   an MCV value uses the MCV frequency, range predicates use histogram
   buckets, conjunctions and disjunctions combine selectivities, and the
   no-stats branch is the only path that returns the `1/3` constant.
4. The planner picks between hash, merge, and nested-loop INNER-join
   algorithms based on a cost function that consumes the new stats; the
   prior rules-only behaviour remains the documented fallback when stats
   are absent.
5. `EXPLAIN` text output renders the selectivity / chosen-algorithm picture
   well enough that items 3 and 4 are verifiable by inspection on a
   TPC-H-shaped query.
6. All required design docs (`0006-0001` … `0006-0004`) are merged with
   status `accepted`, and the deferred sections of `0003-0003` and
   `0003-0010` are cross-linked to the documents that supersede them.
