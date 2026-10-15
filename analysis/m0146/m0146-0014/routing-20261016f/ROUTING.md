# M0146-0014 — S9 census pass at 692c9df93 (2026-10-16)

The `routing-20261016e` census is stale: 692c9df93 (M0146-0202) touched `internal/optimizer`.

Captures at code equal to HEAD `692c9df93` (binary `tmp/m0202-bin`, built from the committed diff):

| corpus | capture | match |
|---|---|---|
| TPC-H | `analysis/m0146/m0146-0202/m0202-tpch*` (reloaded corpus) | 18 / 22 |
| TPC-DS SF0.25 | `analysis/m0146/m0146-0202/m0202-sf025*` | 84 / 99 |
| TPC-DS SF1 | `analysis/m0146/m0146-0202/m0202-sf1*` | 71 / 99 |

The first-divergence files in this directory were re-run on those captures. TPC-H's non-MATCH queries are now Q2,
Q13, Q16 and Q20. Q13 and Q16 belong to the scan-type owner residual.

## Routes

Routes are unchanged from `routing-20261016e`:

- **scan-type**: the M0146-0019 owner corpus residual.
- **Waived:** the COSTTIE, GEQO-RNG, RELPAGES, ESTIMATE+RELPAGES and STALE-SF1 records.
- **Closed since the last pass:** M0146-0202 (TPC-H Q9 MATCH).

## Actionable records by category

| category | records | count |
|---|---|---|
| qual-placement | TPC-H Q20, SF1 Q54 | 2 |
| parameterisation | TPC-H Q2 | 1 |
| aggregation-strategy | SF1 Q45 | 1 |
| join-method | SF1 Q68 | 1 |
| sort-strategy | SF1 Q18 | 1 |

**Filed: M0146-0203** (qual-placement, TPC-H Q20).

**TPC-H Q20, first divergence at depth 2.** It sits under the semi join, on the `supplier ⋈ nation` input.

| | PG | goopg |
|---|---|---|
| plan | `Seq Scan on nation` with `Filter: n_name = 'CANADA'` (1 row), then a `Bitmap Heap Scan on supplier` over `supplier_nation_fkidx` (400 rows) | `Seq Scan on supplier` (10000 rows) Nested Loop over a Memoize'd `nation_pk` probe |
| where the restriction sits | on the nation scan | `Filter: (nation.n_name = 'CANADA')` on the Nested Loop |
| cost | Nested Loop 252.34 | Nested Loop 104.00 |
| `supplier` seq scan | — | 100.00 |

goopg's `supplier` seq scan costs 100.00. The same scan costs 325.00 in goopg's own Q9 plan and in PG's, so the
cost lacks the page term, which may be what makes goopg's order win. The semi-join side then differs: PG builds a Hash
Right Semi Join with `partsupp` as the probe, goopg a Hash Semi Join with `supplier` outer.

**SF1 Q54** has the same two Join Filters as PG, swapped between two nested-loop levels. It may be a different
mechanism.
