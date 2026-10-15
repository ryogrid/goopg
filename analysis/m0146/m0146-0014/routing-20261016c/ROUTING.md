# M0146-0014 — S9 census pass at 891d2127f (2026-10-16)

The `routing-20261016b` census is stale: 891d2127f (M0146-0199) touched `internal/optimizer`.

Captures at code equal to HEAD `891d2127f` (binary `tmp/m0199-bin`, built from the committed diff):

| corpus | capture | match |
|---|---|---|
| TPC-H | `analysis/m0146/m0146-0199/m0199-tpch*` (reloaded corpus) | 15 / 22 |
| TPC-DS SF0.25 | `analysis/m0146/m0146-0199/m0199-sf025*` | 84 / 99 |
| TPC-DS SF1 | `analysis/m0146/m0146-0199/m0199-sf1*` | 71 / 99 |

The first-divergence files in this directory were re-run on those captures.

## Routes

Routes are unchanged from `routing-20261016b/ROUTING.md`:

- **scan-type** (SF0.25 Q23, TPC-H Q13, Q16): the M0146-0019 owner corpus residual (no visibility map on the goopg
  corpora).
- **Waived:** the COSTTIE, GEQO-RNG, RELPAGES, ESTIMATE+RELPAGES and STALE-SF1 records.
- **Closed since the last pass:** M0146-0199 (SF1 Q32 and Q92 now MATCH).

## Actionable records by category

| category | records | count |
|---|---|---|
| qual-placement | TPC-H Q20, Q21, SF1 Q54 | 3 |
| join-method | SF1 Q68, TPC-H Q18 | 2 |
| sort-strategy | SF1 Q18, TPC-H Q9 | 2 |
| parameterisation | TPC-H Q2 | 1 |
| aggregation-strategy | SF1 Q45 | 1 |

**Filed: M0146-0200** (qual-placement, TPC-H Q21).

- **TPC-H Q21, first divergence at depth 3.** It sits under the Sort, at the `Nested Loop Semi Join` over the
  `idx_lineitem_orderkey_fkidx` probe of `l2`.
  - The plan shapes are otherwise identical, and so is the probe (`Index Cond: l_orderkey = l1.l_orderkey`).
  - PG keeps `Join Filter: (orders.o_orderkey = l2.l_orderkey)` on the semi join; goopg prints no Join Filter.
  - The EXISTS clause `l2.l_orderkey = l1.l_orderkey` merges into the equivalence class {o_orderkey, l1.l_orderkey,
    l2.l_orderkey}. The joinrel's derived clause pairs `orders` with `l2`, while the inner path enforces the
    `l1`–`l2` clause.
  - PG 18's `create_nestloop_path` (pathnode.c:2703) drops a join clause only when its `rinfo_serial` is among the
    serials the parameterised inner enforces (`get_param_path_clause_serials`). A differently-paired EC clause is
    therefore kept, redundant but present.
- **TPC-H Q20** diverges in join order (PG `Hash Right Semi Join` with `partsupp` probing `supplier`). It may be a
  different mechanism.
- **SF1 Q54** has the same two Join Filters as PG, placed at different nested-loop levels. It may also be a
  different mechanism.
