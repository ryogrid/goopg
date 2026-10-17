# M0146-0014 — S9 census pass at 355cf2d1f (2026-10-16)

The `routing-20261016f` census is stale: 355cf2d1f (M0146-0203) touched `internal/optimizer`.

Captures at code equal to HEAD `355cf2d1f` (built from the committed diff):

| corpus | capture | match |
|---|---|---|
| TPC-H | `analysis/m0146/m0146-0203/m0203-tpch*` (reloaded corpus) | 19 / 22 |
| TPC-DS SF0.25 | `analysis/m0146/m0146-0203/m0203-sf025*` | 84 / 99 |
| TPC-DS SF1 | `analysis/m0146/m0146-0203/m0203-sf1*` | 71 / 99 |

The first-divergence files in this directory were re-run on those captures. Against `routing-20261016f` the only
change is TPC-H Q20, now MATCH. The SF0.25 and SF1 records are line-for-line identical. TPC-H's non-MATCH queries
are Q2, Q13 and Q16; Q13 and Q16 belong to the scan-type owner residual.

## Routes

Routes are unchanged from `routing-20261016f`:

- **scan-type**: the M0146-0019 owner corpus residual.
- **Waived:** the COSTTIE, GEQO-RNG, RELPAGES, ESTIMATE+RELPAGES and STALE-SF1 records.
- **Closed since the last pass:** M0146-0203 (TPC-H Q20 MATCH).

## Actionable records by category

| category | records | count |
|---|---|---|
| join-method | SF1 Q68 | 1 |
| parameterisation | TPC-H Q2 | 1 |
| aggregation-strategy | SF1 Q45 | 1 |
| sort-strategy | SF1 Q18 | 1 |
| qual-placement | SF1 Q54 | 1 |

All five categories tie at 1. The differ's category order (`CATEGORIES` in `scripts/pg-plan-parity-diff.py`) puts
join-method first. SF0.25 Q68 is MATCH, so the SF1 record is the only one for this query.

**Filed: M0146-0204** (join-method, SF1 Q68). Its first divergence is at depth 8, inside the parallel subtree under
`Gather Merge`. Row estimates agree at every level, so the difference is in the costs.

| | PG | goopg |
|---|---|---|
| `household_demographics` | `Hash Join` on `ss_hdemo_sk`, over a `Hash` on a `Seq Scan` (1800 rows, 161.00) | `Nested Loop` over a `household_demographics_pkey` probe (0.28..0.31 per row) |
| join order above `store_sales ⋈ date_dim` | `household_demographics`, then `store` | `store`, then `household_demographics` |
| `store_sales ⋈ date_dim` | 63104.18, 687 rows | 63105.03, 658 rows |
| after both joins | 63291.35, 157 rows | 63300.56, 150 rows |

Rough check: PG's `cost_hashjoin` arithmetic on goopg's inputs gives about 63105 + 161 + (0.0025 + 0.01) × 1800 plus a
few units of probe and output cost. That is about 63291 for `household_demographics`, then about 63294 with `store`.
goopg's chosen pair is 63300.56. If goopg priced the hash alternative this way, it would win by about 6 units.

So either goopg does not build that path (a partial hash join with a serial `Hash` inner on `household_demographics`
under the parallel outer; it does build one for `store`), or it prices it higher. The gap is about 0.01% of the
subtree, so the task's first step must also measure whether this record belongs in the COSTTIE band.
