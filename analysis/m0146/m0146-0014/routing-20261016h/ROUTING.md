# M0146-0014 — S9 census pass at 0d1c2d879 (2026-10-16)

The census `routing-20261016g` is still fresh. It was captured at `355cf2d1f`; the two commits since,
`7010eab30` and `0d1c2d879`, touch only `.ralph/` and `analysis/` (no planner-relevant path), and no corpus was
reloaded. Its first-divergence files are reused unchanged.

## Routes

- Unchanged from `routing-20261016g`, except **SF1 Q68**. M0146-0204 re-routed it to the M0146-0019 owner residual
  (corpus VACUUM). The goopg `household_demographics` heap keeps an 11-page empty tail that PG's autovacuum
  truncated (`analysis/m0146/m0146-0204/FINDINGS.md`).
- scan-type: the M0146-0019 owner residual.
- Waived: the COSTTIE, GEQO-RNG, RELPAGES, ESTIMATE+RELPAGES and STALE-SF1 records.

## Actionable records by category

| category | records | count |
|---|---|---|
| parameterisation | TPC-H Q2 | 1 |
| aggregation-strategy | SF1 Q45 | 1 |
| sort-strategy | SF1 Q18 | 1 |
| qual-placement | SF1 Q54 | 1 |

Four categories tie at 1. The differ's category order puts parameterisation first.

**Filed: M0146-0207** (parameterisation, TPC-H Q2). The first divergence is at depth 1, the top Hash Join under
the Sort.

| | PG | goopg |
|---|---|---|
| Hash Cond | `(part.p_partkey = partsupp.ps_partkey) AND ((SubPlan 1) = partsupp.ps_supplycost)` | `(partsupp.ps_partkey = part.p_partkey)` |
| SubPlan clause | a hash key | `Join Filter: (partsupp.ps_supplycost = (SubPlan 1))` |
| outer (probe) side | `Gather` over `part` (779 rows) | `nation ⋈ region ⋈ supplier ⋈ partsupp` (160000 rows) |
| hashed side | the 160000-row join | `part` (899 rows) |
| total | 48872.73 | 27264.24 |

SubPlan 1's only outer reference is `part.p_partkey`, so `pull_varnos` gives the SubPlan relids {part}. That makes
the equality a hashjoinable join clause between {part} and {partsupp} (`check_hashjoinable`,
`postgres/src/backend/optimizer/plan/initsplan.c:3833`).

`final_cost_hashjoin` charges `hash_qual_cost.per_tuple`, which includes the SubPlan's cost, per outer row
(`postgres/src/backend/optimizer/path/costsize.c:4496`). That makes `part`-outer the cheaper direction in PG.
goopg never offers the SubPlan equality as a hash clause.

This is the join-clause counterpart of M0146-0203, which landed the same relids rule for a single-relation
restriction.
