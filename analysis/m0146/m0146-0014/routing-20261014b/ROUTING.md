# M0146-0014 — S9 parity replenishment census, 2026-10-14 (second pass)

Capture sha: `adaed8786` (HEAD at capture; no planner change after it in this pass). Captures: the TPC-DS fire-set
gate at both scales (all 99 queries, current tree vs the PG reference) and `scripts/jointree-parity-capture.sh
tpch`. First-divergence records come from `scripts/pg-plan-first-divergence.py`.

| corpus | match | first-divergence rollup |
|---|---|---|
| TPC-DS SF0.25 | 79 / 99 | sort-strategy 5, join-order 5, aggregation-strategy 3, parallelism 2, join-method 2, scan-type 1, qual-placement 1, parameterisation 1 |
| TPC-DS SF1 | 65 / 99 | join-order 8, sort-strategy 7, aggregation-strategy 6, parameterisation 4, qual-placement 3, parallelism 3, scan-type 2, join-method 1 |
| TPC-H | 13 / 22 | sort-strategy 2, scan-type 2, qual-placement 2, parameterisation 1, join-order 1, join-method 1 |

## Ranking (S9 step 3)

Waivers and owner residuals are taken from the 2026-10-13 routes (`routing-20261013/records-final.tsv`) where the
record is unchanged. The cost-tie labels that pass gave the Gather Merge and partial-DISTINCT records are not
reused, because M0146-0190 showed the Q26 "tie" was a mechanism.

- **join-order, 14 records.** Waived: SF0.25 Q42 and Q52 (RELPAGES), Q64 (GEQO-RNG), Q99 (COSTTIE); SF1 Q4, Q11,
  Q31, Q46 and Q62 (STALE-SF1), Q64 (GEQO-RNG), Q78 and Q99 (COSTTIE). That leaves 3 actionable: SF0.25 Q19, SF1
  Q77, TPC-H Q10.
- **sort-strategy, 14 records.** Waived: SF0.25 Q4 (COSTTIE, M0146-0185), SF1 Q52 (COSTTIE, M0146-0189), SF1 Q56
  (STALE-SF1). SF1 Q60 routes to 0014d-F2. That leaves 10 actionable: SF0.25 Q33, Q38, Q72, Q87; SF1 Q18, Q33, Q38,
  Q72; TPC-H Q9, Q22. **This is the top category.**
- **aggregation-strategy, 9 records.** Waived: SF1 Q13, Q23, Q48 and Q71 (STALE-SF1), leaving 5.

Within sort-strategy the largest single shape is `under Gather Merge: PG Unique | goopg Sort`, the partial
DISTINCT under a set operation (SF0.25 Q38 and Q87, SF1 Q38). The next is `under GroupAggregate: PG Sort | goopg
Gather Merge` (SF0.25 Q33, SF1 Q18).

## Filed

- M0146-0191 — sort-strategy, "under Gather Merge: PG Unique | goopg Sort", TPC-DS SF0.25 Q38 (also Q87 at
  SF0.25 and Q38 at SF1).
