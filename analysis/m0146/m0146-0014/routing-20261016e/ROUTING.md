# M0146-0014 — S9 census pass at a08d4ceb4 (2026-10-16)

The `routing-20261016d` census is stale: a08d4ceb4 (M0146-0201) touched `internal/optimizer`.

Captures at code equal to HEAD `a08d4ceb4` (binary `tmp/m0201-bin`, built from the committed diff):

| corpus | capture | match |
|---|---|---|
| TPC-H | `analysis/m0146/m0146-0201/m0201-tpch*` (reloaded corpus) | 17 / 22 |
| TPC-DS SF0.25 | `analysis/m0146/m0146-0201/m0201-sf025*` | 84 / 99 |
| TPC-DS SF1 | `analysis/m0146/m0146-0201/m0201-sf1*` | 71 / 99 |

The first-divergence files in this directory were re-run on those captures.

## Routes

Routes are unchanged from `routing-20261016d`:

- **scan-type**: the M0146-0019 owner corpus residual.
- **Waived:** the COSTTIE, GEQO-RNG, RELPAGES, ESTIMATE+RELPAGES and STALE-SF1 records.
- **Closed since the last pass:** M0146-0201 (TPC-H Q18 MATCH).

## Actionable records by category

| category | records | count |
|---|---|---|
| sort-strategy | TPC-H Q9, SF1 Q18 | 2 |
| qual-placement | TPC-H Q20, SF1 Q54 | 2 |
| join-method | SF1 Q68 | 1 |
| parameterisation | TPC-H Q2 | 1 |
| aggregation-strategy | SF1 Q45 | 1 |

sort-strategy and qual-placement tie at 2. The differ's category order puts sort-strategy first.

**Filed: M0146-0202** (sort-strategy, TPC-H Q9). Its first divergence is at depth 0: PG has a top `Sort` over a
`Finalize HashAggregate`, goopg a `GroupAggregate` over `Gather Merge`. That is a consequence of an estimate four
orders of magnitude low.

- **The estimate.** goopg estimates the `lineitem` probe's Nested Loop at 39 rows, against 13034 outer rows. PG
  estimates 116871 rows from 15640 outer rows, about 7.5 per outer row, which is 6M lineitem / 800k partsupp.
- **PG's mechanism.** PG applies the declared FK `lineitem_partsupp_fk` (`l_partkey`, `l_suppkey` →
  `partsupp`) through `get_foreign_key_join_selectivity`, giving selectivity 1/800000.
  - The `l_suppkey` column is matched through its equivalence class, {ps_suppkey, s_suppkey, l_suppkey}, all
    three equalities written in Q9.
  - `fkinfo->eclass[i]` matches any restrictlist clause whose `parent_ec` is that class (costsize.c:5718-5733).
    The clause the join applies can therefore be `s_suppkey = l_suppkey`, not the FK's own pair.
- **goopg's mechanism.** `superkeyJoinSelectivity` (joinrelsize.go) runs over `oneClausePerEquivClass(clauses)`.
  That reduction picks the first explicit clause, `s_suppkey = l_suppkey`. `joinKeyPairOf` then pairs `lineitem`
  with `supplier`, not `partsupp`, so the FK is not covered and goopg falls back to per-clause eqjoinsel:
  13034 × 6M × 1/200000 × 1/10000 ≈ 39.
- **SF1 Q18** (depth 3, PG `Sort` | goopg `Gather Merge` under GroupAggregate) may be a different mechanism.
