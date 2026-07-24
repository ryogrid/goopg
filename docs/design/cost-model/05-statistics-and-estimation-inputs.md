# 05 — Statistics and Estimation Inputs

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) |
| date | 2026-07-22 |
| depends on | [02](02-pg-path-and-cost-oracle.md), [03](03-path-substrate-and-plan-creation.md) |

## 0. Why this chapter exists

A cost function is only as good as the `rows` and `width` it is handed. goopg
already has a cardinality and selectivity layer ([01](01-current-state-and-gap-analysis.md) §4);
what it lacks are three things the cost model needs: a **single, once-computed**
row estimate per relation (invariant #2), a real **tuple width** (currently
`width=0` is a literal, `operators_explain.go:378`), and a **row estimate that
survives a server restart** without statistics persistence. This chapter supplies
all three, and it is where the milestone's independence from persistence
([10](10-statistics-persistence.md)) is actually earned.

## 1. `set_baserel_size_estimates`, goopg-style

PG computes each base relation's `rows` once, in `set_baserel_size_estimates`
(`costsize.c:5349`): `rel->tuples · selectivity(baserestrictinfo)`, clamped to at
least 1. goopg reproduces this into `RelOptInfo.Rows`
([03](03-path-substrate-and-plan-creation.md) §1.1):

```
Rel.Rows = baseRows(tbl) · clauseSelectivity(local filters, reliable-only)
```

reusing the machinery
[fix-for-q5/02](../fix-for-q5/02-cost-model-and-selective-equivalence.md) §2
already built: `baseRelInfo.filteredRows` localises the predicate to the scan
schema and scales `baseRows` by `clauseSelectivityWithSource`, but **only when the
selectivity is `reliable`** (derived from real column statistics, not the
`defaultEqSelectivity = 0.005` / `1/3` fallback). When it is not reliable,
`Rel.Rows = baseRows` unscaled — the model does not invent certainty from a
guessed selectivity.

The single-source-of-truth invariant makes this stronger than today's usage: the
value is computed once and **every path over the rel, and every cost function,
reads `Rel.Rows`** — no code path re-derives it via `EstimateRows`. Join-rel rows
are computed once per joinrel by the join-size estimator
([06](06-scan-and-join-path-costs.md) §3.1) as the DP forms each subset, and
stored on the joinrel's `RelOptInfo`.

## 2. The tuple-width estimator (a new surface)

`width=0` in `EXPLAIN` is a literal (`operators_explain.go:378`); it has never
mattered because nothing consumed a width. The cost model changes that: `cost_sort`
([02](02-pg-path-and-cost-oracle.md) §4.3) charges by `N · W`, the external-sort
page count is `N · W / BLCKSZ`, and the `estimate_rel_size` row fallback (§4) needs
`W` to convert bytes to rows. So a real width estimator must exist.

PG derives per-attribute widths in `set_rel_width` (`costsize.c`), reading
`pg_statistic.stawidth` when present and falling back to the type's `typlen`
(fixed-width types) or a type-specific default (`get_typavgwidth`, e.g. 32 for
unbounded `varlena`). goopg's analogue:

```
Width(rel) = Σ over projected columns of columnWidth(col)

columnWidth(col):
    fixed-width type (int4, float8, date, …)  → typlen
    varlena with ANALYZE stats               → TableStats.AvgWidth-derived per-col width (when available)
    varlena without stats                    → type default (PG's get_typavgwidth fallback, e.g. 32)
```

**Caveat, stated because it is a real gap.** goopg's persisted `pg_statistic`
writes a **placeholder** `stawidth = 8` (`internal/executor/pg18_user_catalog_rows.go:1346`),
and `TableStats.AvgWidth` is table-level, not per-column, and is **not restored**
after a restart (§3). So the milestone's width estimator leans primarily on the
**type-derived** widths (`typlen` / type default), which need no statistics at
all, and treats ANALYZE-derived per-column widths as a refinement available only
within the session that ran ANALYZE. This is sufficient for TPC-H, whose sort and
transfer costs are dominated by well-known fixed-width and bounded-`char(n)`
columns, and it is honest about where a persisted per-column `stawidth` would
later sharpen the number ([10](10-statistics-persistence.md)).

## 3. The uncomfortable truth: `RowCount` is not restored

This is ledger row `pq-P6`, and it is the single fact that shapes the whole
statistics story. ANALYZE computes `TableStats.RowCount`
(`internal/executor/operators_analyze.go:333`) and `pg_class.reltuples` renders
from it (`internal/catalog/catalog.go:6946`), but the restart restore path
rebuilds `TableStats` from `pg_statistic` rows as
`&catalog.TableStats{Columns: colStats}` (`internal/initdb/open.go:3546`) — leaving
`RowCount`, `Pages`, and `AvgWidth` **zero**. Column statistics (NDistinct, MCV,
histogram, and the negative-stadistinct `NDistinctFrac`) survive a restart; the
**row count they scale does not**.

The effect on a naïve cost model is total: after a restart `baseRows(tbl)` is 0,
so `Rel.Rows` is 0 (or the "use 1" floor), so every join cost degenerates and the
DP goes blind — exactly the regime `EstimateRows` already hits, which is why so
many of today's gates have "unknown stats" branches
([01](01-current-state-and-gap-analysis.md) §3). "ANALYZE has been run" stops
being true the moment the server bounces.

## 4. The fix that needs no persistence: the `estimate_rel_size` row fallback

PG faces the same startup problem — `pg_class.reltuples` can be `-1` (unknown) on
a never-analysed relation — and solves it in `estimate_rel_size`
(`postgres/src/backend/optimizer/util/plancat.c:1075`): when `reltuples` is
unknown, it derives a row count from the **live block count** and the tuple width:

```
density = (BLCKSZ − page overhead) / tuple_width
rows    = relpages · density
```

goopg can reproduce this exactly, and the inputs already exist:

- the **live block count** is `smgr.NBlocks` (`internal/storage/smgr.go:426`), an
  O(1) counter, already plumbed into the planner as
  `ParallelSettings.BlocksForTable` (ledger `pq-P6`/`pq-P10`) and used by the
  parallel size ladder (`computeParallelWorkers`, `parallel.go:459`);
- the **tuple width** is §2's estimator.

So:

```
baseRows(tbl):
    if TableStats.RowCount > 0:                  # ANALYZE ran this session
        return TableStats.RowCount
    blocks := BlocksForTable(tbl)                # live smgr.NBlocks — no persistence
    if blocks > 0:
        return blocks · (usableBytesPerBlock / Width(tbl))
    return 0                                     # genuinely empty / unknown
```

This is the load-bearing move that makes **the milestone persistence-independent**
(README invariant #4). It also generalises: reviving a live-block row estimate
resurrects `EstimateRows` after a restart for *every* planner decision keyed on
row count, not just this cost model — the wider win ledger `pq-P10` option (b)
identified. And it is strictly a *floor* under the real value: within a session
that ran ANALYZE, the exact `RowCount` wins; only cold-started relations fall back
to the block estimate, which is precisely PG's behaviour.

The block estimate is coarser than ANALYZE (it assumes uniform packing and cannot
see dead tuples), so it is a **cold-start safety net, not a replacement** for
statistics. Warm, in-session ANALYZE remains the accurate path the measurement
protocol uses ([09](09-verification-and-acceptance.md) §4).

## 5. Selectivity, reliability, and n_distinct

The cost model consumes the existing selectivity stack unchanged in shape, with
two properties it must respect:

- **Reliability gates certainty.** `selectivityEstimate.reliable`
  (`internal/planner/selectivity.go`) is true only for stats-derived estimates.
  `Rel.Rows` scaling (§1), the NLI semi/anti `match_frac`
  ([06](06-scan-and-join-path-costs.md) §3.3), and the join size estimate all use
  the scaled value only when reliable, and hold the unscaled row count otherwise —
  never substituting a fallback constant as if it were evidence.
- **n_distinct is a sample count that saturates.** `computeColumnStats` stores the
  raw distinct count of a ~30 000-row sample
  (`internal/executor/operators_analyze.go:474`), so a high-cardinality column
  saturates (`l_orderkey`'s 1.5 M distinct values report as ~30 000). This is the
  known limitation [parallel-query/11](../parallel-query/11-partial-aggregation-cost-model.md) §4.1
  documents. For **join costing** the effect is bounded — `estimateJoin` uses
  `max(ndistinct)` in the denominator, so an under-reported ndistinct *over*-states
  the join output, biasing the DP toward smaller intermediate results
  conservatively — but it is a real source of error and the Haas–Stokes estimator
  remains the correct eventual fix (deferred, [10](10-statistics-persistence.md)),
  not a milestone item.

## 6. Divergence from PostgreSQL

- **Row count falls back to a live-block estimate, not persisted `reltuples`**
  (§4). PG persists `reltuples`; goopg (until [10](10-statistics-persistence.md))
  derives it from `smgr.NBlocks` on cold start. Same *formula* as PG's
  `estimate_rel_size`; different *source* for the missing input.
- **Width is primarily type-derived, not `stawidth`-derived** (§2). goopg's
  persisted `stawidth` is a placeholder (`pg18_user_catalog_rows.go:1346`), so the
  milestone leans on `typlen` / type defaults and treats per-column widths as an
  in-session refinement. PG uses `stawidth` when available.
- **n_distinct saturates at the sample size** (§5) — a documented in-memory
  limitation; the on-disk negative-stadistinct convention already carries the
  correct fraction, and Haas–Stokes is the deferred fix.
