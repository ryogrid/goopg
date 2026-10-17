# M0146-0014 — S9 census pass at 6c0d1a3ac (2026-10-16)

The census `routing-20261016h` is stale: 6c0d1a3ac (M0146-0207) touched `internal/optimizer`.

Captures at code equal to HEAD `6c0d1a3ac`:

| corpus | capture | match |
|---|---|---|
| TPC-H | `analysis/m0146/m0146-0207/m0207-tpch*` (reloaded corpus) | 20 / 22 |
| TPC-DS SF0.25 | `analysis/m0146/m0146-0207/fireset/tpcds-sf025/m0207-tpcds-sf025-candidate*` | 84 / 99 |
| TPC-DS SF1 | `analysis/m0146/m0146-0207/fireset/tpcds-sf1/m0207-tpcds-sf1-candidate*` | 71 / 99 |

The fire-set candidate arm was built from the staged tree, which is the code committed as 6c0d1a3ac. The
first-divergence files in this directory were re-run on those captures. Against `routing-20261016g`, the only change
is TPC-H Q2, now MATCH. The TPC-H non-matches left are Q13 and Q16, which belong to the scan-type owner residual.

## Routes

- **Unchanged from `routing-20261016h`.** SF1 Q68 belongs to the M0146-0019 corpus-VACUUM residual.
- **scan-type:** the M0146-0019 owner residual.
- **Waived:** the COSTTIE, GEQO-RNG, RELPAGES, ESTIMATE+RELPAGES and STALE-SF1 records.
- **SF0.25 Q45 is re-routed.** Its 2026-10-13 COSTTIE waiver was measured on a different record (sort-strategy,
  depth 2). Today's record matches SF1 Q45 and is an estimate divergence, not a cost tie (below).
- **Closed since the last pass:** M0146-0207 (TPC-H Q2 MATCH).

## Actionable records by category

| category | records | count |
|---|---|---|
| aggregation-strategy | SF0.25 Q45, SF1 Q45 | 2 |
| sort-strategy | SF1 Q18 | 1 |
| qual-placement | SF1 Q54 | 1 |

**Filed: M0146-0208** (aggregation-strategy, TPC-DS Q45). Both scales show it; S9(3) makes SF0.25 the primary record.
The first divergence is at depth 1, under the Limit: PG plans a `GroupAggregate` over a `Sort` over a `Gather`, and
goopg plans a `Finalize GroupAggregate` over a `Gather Merge` of `Partial GroupAggregate`s.

The cause is the row estimate under the Nested Loop's Join Filter, `substr(ca_zip, 1, 5) = ANY ('{...}') OR
ANY (i_item_id = (hashed SubPlan 1).col1)`:

| scale | PG | goopg |
|---|---|---|
| SF0.25 | 95 → 50 per worker (0.53) | 91 → 68 (0.75) |
| SF1 | 292 → 153 (0.524) | 283 → 212 (0.749) |

goopg's larger group input makes the partial aggregation cheaper. PG gives the hashed SubPlan arm no estimator. It
falls to `boolvarsel`'s default of 0.5 (`postgres/src/backend/optimizer/path/clausesel.c:956`,
`selfuncs.c:boolvarsel`). The OR then yields s1 + 0.5 − 0.5·s1 = 0.524, which puts the substr arm at about 0.048.
goopg's 0.749 implies it prices one arm differently.
