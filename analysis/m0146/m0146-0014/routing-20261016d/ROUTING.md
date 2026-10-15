# M0146-0014 — S9 census pass at d1042cfb6 (2026-10-16)

The `routing-20261016c` census is stale: d1042cfb6 (M0146-0200) touched `internal/optimizer`.

Captures at code equal to HEAD `d1042cfb6` (binary `tmp/m0200-bin`, built from the committed diff):

| corpus | capture | match |
|---|---|---|
| TPC-H | `analysis/m0146/m0146-0200/m0200-tpch*` (reloaded corpus) | 16 / 22 |
| TPC-DS SF0.25 | `analysis/m0146/m0146-0200/m0200-sf025*` | 84 / 99 |
| TPC-DS SF1 | `analysis/m0146/m0146-0200/m0200-sf1*` | 71 / 99 |

The first-divergence files in this directory were re-run on those captures.

## Routes

Routes are unchanged from `routing-20261016c/ROUTING.md`:

- **scan-type**: the M0146-0019 owner corpus residual.
- **Waived:** the COSTTIE, GEQO-RNG, RELPAGES, ESTIMATE+RELPAGES and STALE-SF1 records.
- **Closed since the last pass:** M0146-0200 (TPC-H Q21 MATCH).

## Actionable records by category

| category | records | count |
|---|---|---|
| join-method | TPC-H Q18, SF1 Q68 | 2 |
| sort-strategy | SF1 Q18, TPC-H Q9 | 2 |
| qual-placement | TPC-H Q20, SF1 Q54 | 2 |
| parameterisation | TPC-H Q2 | 1 |
| aggregation-strategy | SF1 Q45 | 1 |

Three categories tie at 2. The differ's category order puts join-method first.

**Filed: M0146-0201** (join-method, TPC-H Q18). No SF0.25 record exists in this category, and the TPC-H corpus is the
realigned, non-stale one.

**TPC-H Q18, first divergence at depth 3, under the Gather.**

| | PG | goopg |
|---|---|---|
| top join | `Parallel Hash Join` (`orders.o_custkey = customer.c_custkey`), 301873.25 | `Nested Loop` over the `idx_lineitem_orderkey_fkidx` probe |
| `customer` | joined last, through `Parallel Hash` over a `Parallel Seq Scan`, 4287.00 | joined first, through a serial `Hash` over a `Seq Scan`, 5151.00 |
| order below | `orders ⋈ agg(lineitem_1)` Hash Join, then the `lineitem` index probe | `customer` join, then the `lineitem` index probe |
| total | 404713.55 | 388186.47 |

- goopg keeps a `Subquery Scan on "ANY_subquery"` that PG does not print.
- goopg's Sort Key is unqualified (`o_totalprice`), where PG prints `orders.o_totalprice`.
- Parallel Hash is producible in goopg (SF1 Q68's goopg plan shows `Parallel Hash Join` / `Parallel Hash`). The
  question is why the `customer` join is not offered or not won as a parallel-hash join above the `lineitem` probe.

**SF1 Q68** (depth 8, under the Nested Loop) is a different shape: PG hash-joins `household_demographics` (1800-row
Hash) below `store`, while goopg probes `household_demographics_pkey`. The cost gap is a few units out of ~63300, so
it may be a near-tie.
