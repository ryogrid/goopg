# M0146-0003d — sorted split producer: evidence

Slice S6 of M0146-0003 (row-emitting PartialAgg): the planner now files
`PathFinalizeAgg(Sorted) -> PathGatherMerge -> PathSort -> PathAgg(Hashed)`
beside the hashed split, and the canonical corpus measures the movement.
Design doc: `docs/design/0100-0149/m0146-0003d-partial-agg-sorted-producer.md`.

## Files

- `m0146-0003d-tpch.plans.txt` / `m0146-0003d-tpch.pg.plans.txt` —
  canonical TPC-H plan-only capture pair (goopg vs PG 18.3, parallel,
  shipped config).
- `m0146-0003d-tpch.txt` / `m0146-0003d-tpch.pg.txt` — raw audit output.
- `tpch-diff.txt` — `pg-plan-parity-diff.py` output on the pair.
- `m0146-0003d-baseline-rev.txt` — rev the baseline was captured at.
- `tpcds-sf025/`, `tpcds-sf1/` — private-clone fire-set executions for the
  moved queries (all PASS; FORCE=1 values-only, nightly co-resident).

## Headline

```
PLAN-PARITY: queries=22 match=6 shapediff=16 unparsed=0 missingnode=0 error=0 timeout=0
CATEGORIES: join-order=12 join-method=6 scan-type=9 parameterisation=3
            aggregation-strategy=5 sort-strategy=9 parallelism=9
            qual-placement=2 rendering=0
```

vs the M0146-0003c capture: `parallelism` 13 -> 9, `sort-strategy` 10 -> 9,
`join-order` 13 -> 12, `join-method` 7 -> 6, `rendering` 1 -> 0.
Matches 5 -> 6.

## Per-query movement

- **Q1** MATCH (was SHAPE-DIFF). Emits PG's exact spine:
  `Finalize GroupAggregate -> Gather Merge -> Sort -> Partial
  HashAggregate -> Parallel Seq Scan on lineitem`, cost 166053 vs PG 200862.
- **Q4 Q5 Q7 Q12** shed their `parallelism` records.
- **Q9** MATCH -> SHAPE-DIFF `[sort-strategy, parallelism]`: goopg elects
  the presorted split where PG hashes. Cause: goopg's `partialGroups`
  estimate ~5000 vs PG's ~60125 — at goopg's own estimate the arm is
  honestly cheap enough; a stats/cardinality divergence upstream of this
  change (M0146-0009 territory), not plan forcing.
- TPC-DS (both scales): Q5 Q19 Q42 Q43 Q44 Q52 Q55 Q58 Q60 Q77 Q93 moved;
  every fire execution returned correct values.

## M0137-0019a re-evaluation

Both unblock tasks landed (M0146-0002 `[x]`, M0146-0003 `[x]` this slice),
so the owner disposition's re-evaluation fired: the `parallelism` category
reads 9/22 vs the 16/22 floor the task recorded. Family B resolved as
predicted; residual members (Q8 Q9 Q15a Q16 Q18 Q19 Q20 Q21 Q22) are
join-election / stats territory, not the floored executor-model class.
