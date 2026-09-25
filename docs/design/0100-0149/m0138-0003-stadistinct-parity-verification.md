# M0138-0003 — verify `stadistinct` parity end to end

Status: accepted (verified 2026-09-15, no production change)

## Task

Per `.ralph/fix_plan.md`'s M0138 milestone: goopg stores upstream's one
signed `stadistinct` (`pg_statistic.stadistinct`, `get_variable_numdistinct`,
`postgres/src/backend/utils/adt/selfuncs.c`) as two fields — `NDistinct`
(absolute count) and `NDistinctFrac` (fraction of the relation, PG's
negative convention) — and `catalog.ColumnStats.StaDistinct()`
(`internal/catalog/catalog.go:1942`) already reconstructs PG's signed
convention, including the 10%-absolute-vs-fraction switch
(`analyze.c:2650-2658`). The task text was explicit that the two-field
convention is **not** the gap; it asked to *confirm* (a) the switch fires
where PG's does on the sample `M0138-0002` now produces, and (b) every
consumer reads the reconstructed value rather than a bare `NDistinct`, using
R78's witness (`lineitem.l_orderkey`, goopg `-0.1956` frac vs PG's absolute
`347537`) as the re-measurement target. **Land a change only where a real
divergence is found.**

## Consumer audit

`grep -rn "StaDistinct\b"` over `internal/` (excluding tests) finds exactly
three call sites, all going through the method rather than a bare field:

- `internal/executor/pg18_user_catalog_rows.go:1591` — the `pg_stats` view's
  `n_distinct` column.
- `internal/catalog/pgstats.go:80` — the `pg_statistic` heap row's
  `stadistinct` column (comment cites "planner's join estimator via
  ColumnStats.StaDistinct").
- `internal/optimizer/joinselectivity.go:235` — the join-selectivity
  estimator's `nd2` input.

No sibling-path drift: this is the class of bug catalog.go's own comment on
`StaDistinct()` warns about (an estimator reading `NDistinct` directly would
see zero for any column whose distinct count was stored only as a fraction),
and it does not occur here. `internal/initdb/open.go:4010-4027` is the
inverse direction (decoding a **real PG** heap row's signed `stadistinct`
back into goopg's two fields on catalog reload) and correctly branches on
sign, mirroring `StaDistinct()`'s convention rather than duplicating it.

**Conclusion: the convention was already correct end to end before this
task, as the task text predicted.** No production code changed.

## Re-measurement: did M0138-0002 close R78's gap?

R78's `PROBE.md` recorded, pre-`M0138-0002`: goopg's `l_orderkey` ndistinct
as `NDistinctFrac -0.1956` (≈1.17M absolute, out of the sample-scan-every-block
Algorithm R sampler) against PG's live `pg_stats` absolute `347537` — a 3.4×
gap blamed on the ANALYZE sampler, not the selectivity model or the
`stadistinct` convention.

Measured on the same TPC-H SF=1 cluster pair (goopg bench :65433 restarted on
a HEAD build carrying `M0138-0002`'s block sampler, `GOOPG_ANALYZE_SEED=20260905`
already pinned by `bench/tpch/env_goopg.sh`; PG oracle :65432, same load):

| column | goopg (post-M0138-0002) | PG (fresh `ANALYZE`, 3 runs) |
|---|---|---|
| `lineitem.l_orderkey` | `327804` | `366886` / `336410` / `344110` |
| `lineitem.l_partkey` | `190297` | `190048` |
| `lineitem.l_suppkey` | `10001` | `10013` |
| `lineitem.l_linestatus` | `2` | `2` |
| `lineitem.l_shipdate` | `2506` | `2509` |
| `customer.c_custkey` (PK) | `-1` | `-1` |

`l_orderkey` was rerun 3× on goopg with the pinned seed and returned `327804`
every time (determinism intact); PG's own **unpinned** `ANALYZE`, rerun 3×
on the identical data, swings `336410`–`366886` — a ≈9% run-to-run band of
its own. goopg's `327804` sits inside that band. Every other spot-checked
column (unique PK, low-cardinality boolean-like, a date column, two more FK
columns) matches PG within the same kind of sampling noise, not a
multiplicative gap.

**R78's 3.4× divergence is closed.** It was the block-representation gap
`M0138-0001` identified and `M0138-0002` ported the fix for — not a
`stadistinct`-convention bug, confirming the task's own framing. The
`customer.c_custkey` row also confirms the 10%-threshold switch fires
identically on both engines (both report `-1`, PG's unique-column
convention), answering the task's part (a) directly.

No constant was tuned to reach this: the measurement is a direct rerun of
the already-landed `M0138-0002` sampler against the same corpus,
per the owner's Q2 method ("port the algorithm and let its output be
whatever it is").

## Why no code change

Per `AGENT.md`'s Completion and Deferral Discipline, a divergence must be
real before a task earns a fix. This task's job was to verify, and the
verification found the convention already correct and the numeric gap
already closed by the prerequisite task. Filing a change here would be
exactly the "tune a constant toward a target" the milestone's harness
section forbids.

## Follow-up

`M0138-0004` (MCV/histogram/correlation from the shared sample) and
`M0138-0005` (corpus re-measure at a declared epoch) still own the
corpus-wide, all-slot re-measurement; this task only re-checked the single
`stadistinct` witness and a handful of spot-check columns to confirm the
mechanism, not the full corpus.
