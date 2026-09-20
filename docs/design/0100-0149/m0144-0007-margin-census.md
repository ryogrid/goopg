# M0144-0007 — cost-margin census (forced-shape + DPPATH pricing)

Status: landed (recon; instrument `scripts/goopg-margin-census.py` +
`analysis/m0144/m0144-0007-margin-census.md`).

## Task

03-forward-plan §2: split "almost chose it" from "cannot reach it" for
every first-divergence record in M0144-0002's census — price PG's winning
shape inside goopg's model at the divergence point and append a margin
column to the census tables.

## Instrument design

`scripts/goopg-margin-census.py` — three margin evidences per record:

| evidence | mechanism | measures |
|---|---|---|
| FORCED | session `enable_*=off` arms + `max_parallel_workers_per_gather=0` + parallel-encourage cost GUCs | `rootd%` root-cost delta when the forced plan re-censuses to a *different* record (or MATCH) against the committed PG capture |
| CANDIDATE | `GOOPG_PGSHAPED_DP_TRACE=1` lane; per-query DPPATH slice | `candm%` — a PG-producer-family candidate's cost delta vs the same-rel accepted winner; `offered=0` = never generated |
| ORDERED-SEED | `upper.ordered.candidates`/`upper.ordered.seed` DPPATH summary lines | `nonemptykeys`/`keys` — whether any presorted input existed at all |

Re-census of the forced plan reuses `census_query()` from
`pg-plan-first-divergence.py` verbatim — "produced" is defined as the
record *moving*, not by string-matching the plan.

Arm map (goopg child kind → exclusion SET): the PG-faithful `enable_*`
GUCs plus two parallel presets. Kinds with no arm (CTE, Subquery Scan,
HashSetOp, WindowAgg, GroupAggregate, same-kind qual/parameterisation
records) get `unexpressible/no-arm` — measurable only via CANDIDATE.

## Classes

§2's three bands (`<1%` election / `1–20%` input / `>20%` or
unexpressible structural) plus two signatures the data required:

- `forced-cheaper` — the arm produced PG's shape *below* the winner's
  cost: the winner never had a real cost advantage (election/fuzz).
- `dominated-noncost` — the PG-shape candidate exists and is cheaper,
  yet is dominated: dominance is multi-criteria (pathkeys,
  parameterisation), so the loss is on feasibility grounds — an
  input-shape structural gap, misclassified as election if read by
  price alone.

## Lanes

| corpus | lane | data dir |
|---|---|---|
| TPC-DS SF0.25 | :5591 | `tmp/c20a/data-sf025` (private clone) |
| TPC-DS SF1 | :5592 | `bench/tpcds/runtime_goopg/data` (canonical; EXPLAIN-only) |
| TPC-H parallel | :5590 | `tmp/goopg-spotcheck-tpch-data` (private Sep-20 clone of :65433) |

All `tmp/m0144-0007-bin/goopg` at HEAD `06d9d9ea8`,
`GOOPG_PGSHAPED_DP_TRACE=1`, cgroup-capped via `goopg-test-run.sh`,
EXPLAIN-only (no ANALYZE) — reference clusters untouched. A second
SF0.25/SF1 pass restarted the lanes with
`GOOPG_INCREMENTAL_SORT=1 GOOPG_PARTIAL_SORT_PATHS=1` for the
`PG Incremental Sort` records (env arms are process-start, not session).

TPC-H query texts were dumped from `internal/testutil/tpch.Queries()`
(same source `estimate-audit` uses) to `tmp/m0144-0007/tpch-queries/`.

## Verification

- All three corpora measured: 210 divergent records (3 both-side `error`
  excluded per corpus, 2/1/1 census MATCHes skipped).
- Forced-plan re-census uses the identical alignment machinery as 0002 —
  no second implementation of "did the divergence move".
- `baseline-record-not-reproduced` flag on ~9 records: HEAD drift vs the
  committed captures (binary newer than the sweep binary); margins still
  measured on the current baseline.
- Result headline: **~72% of first divergences are structural**
  (unexpressible/no-arm 50% + dominated-noncost 9% + priced-structural
  13%); election+forced-cheaper+input ~28%. The dominant
  `Limit → {GroupAggregate|Incremental Sort} | Sort` family is a
  *generation* gap (`nonemptykeys=0` even with the IS arm) — upstream
  pathkey propagation, not a cost loss.

## Limits / caveats

- `rootd%` conflates collateral plan changes when an arm fires (the
  disabled mechanism changes paths elsewhere too) — recorded as the
  root-level margin it is; node-level attribution is a refinement.
- `candm%` outliers >10⁴% (Q47/Q57/Q75/Q34-sf1) are cross-subtree-scale
  candidate comparisons — class is correct (`priced-structural`), the
  magnitude is not literal.
- `unexpressible/no-arm` conflates "GOOPG_* env arm exists but wasn't
  tried" with "no arm exists"; only `GOOPG_INCREMENTAL_SORT`/
  `GOOPG_PARTIAL_SORT_PATHS` got a second lane pass.
- DPPATH producer→kind mapping (`FAMILY`) is approximate for
  join/scan kinds (e.g. `Parallel Hash Join` and `Hash Join` share a
  family — parallel vs serial is a flag, not a producer).
