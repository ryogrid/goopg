# M0146-0014 — S9 census pass at 4a75cefbb (2026-10-16)

The census `routing-20261016i` is stale: 4a75cefbb (M0146-0208) touched `internal/optimizer`.

Captures at code equal to HEAD `4a75cefbb`:

| corpus | capture | match |
|---|---|---|
| TPC-H | `analysis/m0146/m0146-0208/m0208-tpch*` | 20 / 22 |
| TPC-DS SF0.25 | `analysis/m0146/m0146-0208/fireset/tpcds-sf025/m0208-tpcds-sf025-candidate*` | 85 / 99 |
| TPC-DS SF1 | `analysis/m0146/m0146-0208/fireset/tpcds-sf1/m0208-tpcds-sf1-candidate*` | 72 / 99 |

The fire-set candidate arm is the staged tree committed as 4a75cefbb. Against `routing-20261016i`, the only change is
Q45, now MATCH at both scales.

## Routes

- **Unchanged.** scan-type is the M0146-0019 owner residual. SF1 Q68 belongs to the M0146-0019 corpus-VACUUM
  residual.
- **Waived:** the COSTTIE, GEQO-RNG, RELPAGES, ESTIMATE+RELPAGES and STALE-SF1 records.
- **Closed since the last pass:** M0146-0208 (Q45 MATCH at SF0.25 and SF1).

## Actionable records by category

| category | records | count |
|---|---|---|
| sort-strategy | SF1 Q18 | 1 |
| qual-placement | SF1 Q54 | 1 |

The two categories tie at 1. The differ's category order puts sort-strategy first. SF0.25 Q18 is MATCH.

**Filed: M0146-0209** (sort-strategy, SF1 Q18). The first divergence is at depth 3, under the GroupAggregate with
grouping sets.

| | PG | goopg |
|---|---|---|
| plan | `Sort` (46169.49) over a `Gather` (46167.84, 53 rows) | `Gather Merge` (46015.57, 49 rows) over per-worker `Sort`s |
| `catalog_sales ⋈ date_dim` | 2105 rows per worker | 1788 rows per worker |
| `date_dim`, `d_year = 2001` | 252 rows per worker | 214 rows per worker |

In goopg's own terms the two shapes nearly tie. Its Gather would cost 45009.43 + 1000 + 0.1 × 49 = 46014.33, plus a
sort of 49 rows, against Gather Merge's 46015.57. That is well inside add_path's 1% fuzz. The record may therefore be
a COSTTIE residual, or it may come from the `d_year` estimate, which is sample-dependent at SF1. The task's first
step decides which.
