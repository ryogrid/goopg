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

## Second filing from this census (after M0146-0191 closed as a PG-internal tie)

M0146-0191's close showed that the Q38/Q87 partial-DISTINCT records are a statistics-sample tie inside PG 18.3,
so they count as COSTTIE. Before filing again, each remaining sort-strategy record was checked for PG-side
stability. On the scratch PG build (tmp/pgdbg, with its own ANALYZE sample), SF0.25 Q33 and Q72 plan exactly as
on :65438, so neither record is a sample-flipping tie. Their records, however, span 4 and 6 categories. SF1 Q18
matches at SF0.25, which puts it in the STALE-SF1 class.

TPC-H Q22 (`depth=1 [sort-strategy] under GroupAggregate: PG Gather Merge | goopg Sort`) has a clear mechanism,
not a near-tie. goopg plans the anti join entirely serially. Its filter compares against `(InitPlan 1).col1`, and
goopg judges that uncorrelated scalar subquery as a SubPlan whose body holds a Gather (`isParallelSafeExpr` ->
`subPlanParallelSafe`), so the qual is parallel-restricted. PG makes the initplan's output a PARAM_EXEC Param and
lists it in `safe_param_ids` (`is_parallel_safe`, clauses.c), because the value is computed at the Gather and
passed to the workers. Plans: `tpch-q22-pg.plans.txt`, `tpch-q22-goopg.plans.txt`.

- Filed M0146-0192 — TPC-H Q22.

Confirmed with a minimal probe on SF0.25 (`initplan-parallel-probe.sql`). Take
`SELECT ss_item_sk, count(*) FROM store_sales WHERE ss_net_paid > (SELECT avg(ss_net_paid) FROM store_sales) GROUP BY ss_item_sk`:

- PG plans Finalize HashAggregate → Gather → Partial HashAggregate → Parallel Seq Scan, with the
  `(InitPlan 1).col1` filter inside the workers.
- goopg plans a serial HashAggregate over a serial Seq Scan.
- With the constant `ss_net_paid > 100` instead, both plan the parallel split. The initplan qual alone causes the
  refusal.
