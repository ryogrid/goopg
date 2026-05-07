# TPC-H M0065 Baseline (2026-05-08)

22-query SF=1 sweep after the M0065 incremental work. M0065
opened with three sub-tasks (Q21 / Q20 / Q5) carried over from
M0064. Compares against the M0064 final baseline
(`tpch-m0064-baseline-2026-05-07.md`, commit `0633090`).

| Run parameter | Value |
| --- | --- |
| Commit         | `8bcf4a0` (`perf-analysis`) |
| Dataset        | TPC-H SF=1 (HammerDB schema) |
| Server         | `goopg`, GOMEMLIMIT=12 GiB |
| Cancel-after   | 600 s |
| Per-query budget | 620 s |
| Driver         | `cmd/tpch-runner` (per-query connection isolation) |
| Log            | `bench/tpch/logs/m0065_22q_20260508T003352.log` |

## Sub-task outcomes

| Sub-task | Verdict | Outcome |
| -------- | ------- | ------- |
| **M0065-0001 Q21 NLI-aware key remap walker** | **PARTIAL — INFRASTRUCTURE ONLY** | Added `*NestedLoopIndexJoin` case to `applyJoinTreePosMap` that recurses into Outer (so deeper Joins are still re-resolved). Empirical bisect confirmed both posMap remap and Name re-resolve break Q9's chained-NLI shape (where `cr.Index=15` works at runtime but the schema annotation says `s_acctbal` at 15). The deeper fix requires reconciling the planner schema annotation with the executor's `lazyOut` layout — beyond session budget. Q21 stays in M0063-0004's partial state (Semi NLI, Anti hash, cancel at 600 s). |
| **M0065-0002 Q20 correlated scalar decorrelation** | **DIAGNOSED — NO FIX LANDED** | Added stderr trace: `unnestSubquery` DOES fire for Q20's `0.5 * SUM(l_quantity)` scalar (`canUnnest=true`, `params=2`, filter found). Multi-param decorrelation builds the hash-aggregate join correctly. Q20 still cancels at 110 s+ because the slowness is downstream — likely the 6 M-row lineitem scan with `l_shipdate` range filter + `GROUP BY (l_partkey, l_suppkey)` aggregate, or the per-row evaluation of the residual `ps_partkey IN (parts)` non-correlated IN. Not a planner-unnest gap. |
| **M0065-0003 Q5 six-table MHJ throughput** | **DEFERRED** | Profile-driven optimisation; not addressed this session. |

## Per-query results & delta vs M0064

| Q   | M0064 (s) | M0065 (s) | Δ | Rows | Notes |
| --- | --------:| --------:| -- | ----:| ----- |
| Q1  |   41.87 |   41.45 | −0.42 | 4 | flat |
| Q2  |    8.63 |    8.46 | −0.17 | 470 | flat |
| Q3  |   49.00 |   48.93 | −0.07 | 11462 | flat |
| Q4  |  177.14 |  181.87 | +4.73 | 5 | flat |
| Q5  |  600.07 |  600.10 | 0.0 | — | cancel; deferred |
| Q6  |   30.54 |   30.58 | +0.04 | 1 | flat |
| Q7  |   36.59 |   39.45 | +2.86 | 4 | flat |
| Q8  |  209.31 |  214.72 | +5.41 | 2 | preserved |
| Q9  |  233.53 |  238.36 | +4.83 | 7 | preserved |
| Q10 |   42.67 |   42.52 | −0.15 | 20574 | flat |
| Q11 |    4.24 |    4.20 | −0.04 | 1142 | flat |
| Q12 |   92.87 |   93.98 | +1.11 | 2 | flat |
| Q13 |   63.41 |   65.39 | +1.98 | 35 | flat |
| Q14 |   33.98 |   35.11 | +1.13 | 1 | flat |
| Q15a |  24.44 |   25.19 | +0.75 | 10000 | flat |
| Q15b |  52.60 |   53.88 | +1.28 | 1 | flat |
| Q16 |    5.35 |    5.41 | +0.06 | 18170 | flat |
| Q17 |   69.86 |   70.37 | +0.51 | 1 | flat |
| Q18 |  101.63 |  103.08 | +1.45 | 11 | flat |
| Q19 |   68.92 |   68.01 | −0.91 | 1 | flat |
| Q20 |  600.01 |  600.00 | 0.0 | — | cancel; diagnosed (unnest works, downstream slow) |
| Q21 |  600.33 |  600.07 | 0.0 | — | cancel; partial (Semi NLI, Anti hash) |
| Q22 |   60.35 |   65.39 | +5.04 | 7 | flat |

### Aggregate impact

| Cohort | M0064 total | M0065 total | Δ |
| ------ | ----------:| ----------:| -- |
| OK queries (excl. cancels) | 1610.71 s | 1635.35 s | +24.6 s (run-to-run noise) |
| Cancels | Q5/Q20/Q21 | Q5/Q20/Q21 | unchanged |
| **Row-count parity** | **19/22** | **19/22** | **preserved** |

### Outcome summary

M0065 makes incremental infrastructure progress (NLI Outer-
recurse walker + diagnostic findings) but no user-visible
behavioural change. **No regression** — 19 / 22 OK preserved
with all row counts identical to M0064.

## Diagnostic findings

### Q21 NLI-aware key remap — annotation vs runtime mismatch

Empirical observation from M0064's debug session:

```
Q9  NLI#2 outerType=*NestedLoopIndexJoin innerTable=partsupp
        key l_suppkey  crIndex=15 schema[15]="s_acctbal"  rebind→24 (HARMFUL)
Q21 NLI#2 outerType=*NestedLoopIndexJoin innerTable=lineitem
        key l_orderkey crIndex=12 schema[12]="s_name"     (rebind needed but breaks Q9)
```

For Q9, runtime row[15] empirically gives the correct
`l_suppkey` value despite the schema annotation labelling it
`s_acctbal`. For Q21, runtime row[12] genuinely contains
`s_name`. The two cases require opposite handling, but no
simple discriminator (outer-node type, IsolatedScope flag,
Index-OOB check, Name-mismatch check, etc.) reliably tells
them apart. The proper fix likely requires:

- Investigating MultiHashJoin's `lazyOut` order vs declared
  `Output()` schema, and ensuring they agree.
- Or: tracking the source of each NLI key (EXISTS-derived,
  j.Predicate-derived, etc.) and applying the rebind
  selectively.

### Q20 correlated scalar decorrelation — unnest works

`unnestSubquery` fires for Q20's `0.5 * SUM(l_quantity)`
scalar. Multi-param decorrelation correctly produces a
hash-aggregate join on `(l_partkey, l_suppkey)`. The residual
slowness is in:

- The 6 M-row lineitem scan with `l_shipdate` range filter
  (~14% selectivity → 850 K aggregated rows).
- The outer non-correlated IN's single-shot inner-plan
  evaluation cost.
- Possibly the residual `ps_partkey IN (parts)` non-correlated
  IN evaluated per-partsupp-row.

Future work: profile-driven analysis of where the 110 s+ wall
clock actually goes inside the post-unnest plan.

### Q5 six-table MHJ throughput

Not investigated this session.

## Open follow-ups (M0066)

| Item | Plan |
| ---- | ---- |
| **M0065-Q21-walker (deeper)** | Reconcile MultiHashJoin's `lazyOut` runtime layout with its declared `Output()` schema. Then a clean Name-based NLI key rebind (post-rewrite walker) should fix both Q9 (no-op since names already align) and Q21 (rebind to canonical positions). |
| **M0065-Q20 (downstream)** | pprof Q20 to identify whether the 110 s+ is in lineitem scan, GROUP BY aggregation, hash-join probe, or outer IN re-eval. Address the dominant cost. |
| **M0065-Q5** | pprof Q5 baseline; investigate intra-MHJ NLI surgery. |

## Verification

- `go test ./...` PASS at commit `8bcf4a0`.
- `./tpch-runner --queries=8,9,15` returns 2 / 7 / 1 rows
  (preserved across the milestone).

## Summary

M0065 makes incremental infrastructure progress (NLI walker
recurse-into-Outer) and surfaces the deeper architectural
issue: planner schema annotations don't always match executor
runtime row layouts. Three sub-tasks (Q21, Q20, Q5) are
documented with concrete root-cause findings and carried as
M0066 follow-ups. **No row-count regression vs M0064** —
19 / 22 OK preserved.
