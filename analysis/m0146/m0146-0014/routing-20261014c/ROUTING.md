# M0146-0014 — S9 parity replenishment census, 2026-10-14 (third pass)

Capture sha: `b580d2344`, HEAD at capture, after M0146-0192 landed. Captures: the TPC-DS fire-set gate at both
scales (all 99 queries) and `scripts/jointree-parity-capture.sh tpch`. First-divergence records come from
`scripts/pg-plan-first-divergence.py`.

| corpus | match | first-divergence rollup |
|---|---|---|
| TPC-DS SF0.25 | 79 / 99 | sort-strategy 5, join-order 5, aggregation-strategy 3, parallelism 2, join-method 2, scan-type 1, parameterisation 1, qual-placement 1 |
| TPC-DS SF1 | 65 / 99 | join-order 8, sort-strategy 7, aggregation-strategy 6, parameterisation 4, parallelism 3, qual-placement 3, scan-type 2, join-method 1 |
| TPC-H | 14 / 22 | scan-type 2, qual-placement 2, parameterisation 1, sort-strategy 1, join-order 1, join-method 1 |

## Ranking (S9 step 3)

Waivers come from `routing-20261013/records-final.tsv`, plus the two cost-tie closes since:

- Q38/Q87 (M0146-0191, a sample-sensitive tie inside PG) count as COSTTIE.
- SF0.25 Q4 and SF1 Q52 are COSTTIE as before.
- SF1 records whose query matches at SF0.25 are STALE-SF1.

Actionable records per category:

- **sort-strategy, 5.** SF0.25 Q33 and Q72; SF1 Q33 and Q72; TPC-H Q9.
  - TPC-H Q9 is a join-tree row-estimate gap (49 vs 90575 rows at the join), not a sort mechanism.
- **aggregation-strategy, 4.** SF0.25 Q45 (M0146-0190 residual), Q61 and Q77; SF1 Q45.
- **parallelism, 4.** Q16 and Q39 at both scales.
- **join-order, 3.** SF0.25 Q19, SF1 Q77, TPC-H Q10.

## Filed

**M0146-0193 — TPC-DS SF0.25 Q33** (`depth=5 [sort-strategy] under GroupAggregate: PG Sort | goopg Gather
Merge`). The record is stable on tmp/pgdbg, which has its own ANALYZE sample.

The mechanism comes from tmp/pgdbg's partial pathlist dump (`q33-pgdbg-partial-pathlist*.txt`):

- PG's semi-join rel (b 1 2 3 4 7) holds ONE partial path: a Merge Join of the partial outer, explicitly sorted,
  with the unique-ified inner (UNIQUEPATH over item_1, i.e. JOIN_UNIQUE_INNER), costing 19543.70..19548.37 and
  sorted on i_manufact_id.
- With `enable_mergejoin = off`, PG's Parallel Hash Semi Join appears at 3502.44..19411.03. That is cheaper, but
  within STD_FUZZ_FACTOR and with worse pathkeys, so `add_partial_path`'s pathkeys rule
  (`keyscmp == PATHKEYS_BETTER2` -> `accept_new = false`) drops it.
- PG then plans Sort -> Hash Join (Gather, Hash(HashAggregate item_1)). goopg keeps the Parallel Hash Semi Join and
  elects GroupAggregate -> Gather Merge -> Sort -> Parallel Hash Semi Join.
