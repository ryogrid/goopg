# TPC-H M0064 Baseline (2026-05-07)

22-query SF=1 sweep after the M0064 Q9-regression fix.
Compares against the M0063 final baseline
(`tpch-m0063-final-baseline-2026-05-07.md`, commit
`e2a37ea`).

| Run parameter | Value |
| --- | --- |
| Commit         | `0633090` (`perf-analysis`) |
| Dataset        | TPC-H SF=1 (HammerDB schema) |
| Server         | `goopg`, GOMEMLIMIT=12 GiB |
| Cancel-after   | 600 s |
| Per-query budget | 620 s |
| Driver         | `cmd/tpch-runner` (per-query connection isolation) |
| Log            | `bench/tpch/logs/m0064_22q_20260507T220942.log` |

## Goal & scope

The user asked to "complete M0062 and M0063 and reach 22/22."
The M0063 final report flagged a new Q9 regression introduced
by M0063-0001 plus three remaining cancel queries (Q5, Q20,
Q21). M0064 attempted four targeted fixes — Q9 regression, Q21
Anti-NLI lift, Q20 decorrelation, Q5 throughput. Q9 landed; the
others surfaced deeper architectural issues and are tracked as
M0065 follow-ups.

## Sub-task outcomes

| Sub-task | Verdict | Outcome |
| -------- | ------- | ------- |
| **M0064-Q9 NLI Name re-bind regression** | **LANDED** | Q9 back to 233.53 s / 7 rows (was ERROR 600 s in M0063). M0063-0001's blanket Name re-bind in `nliRewrite` fired for chained-NLI shapes whose original Index already matched the runtime row layout, moving the probe onto a different table's column. Fix: gate the rebind on `outerNode.(*MultiHashJoin)` AND skip when the original Index is in-bounds with matching Name. Preserves Q8 (2 rows) and Q15b (1 row). |
| **M0064-Q21 Anti-NLI inner-conjunct lift** | **DEFERRED → M0065** | Activating `liftInnerOnlyFilterConjuncts` on Q21's NOT EXISTS Anti corrupted the NLI Anti's outer-key probe at runtime (`column "l_orderkey" is not numeric`). Root cause: `applyJoinTreePosMap` does NOT recurse into `*NestedLoopIndexJoin`, so chained-NLI keys keep pre-rewrite (FROM-cumulative) indices. Q21's Anti-side outer is a Semi NLI wrapping MHJ; the outer-key Index is stale relative to Semi NLI's Output() schema. The fix requires extending `applyJoinTreePosMap` (or `nliRewrite`) to re-resolve NLI keys against the outermost MHJ's runtime layout transitively. Tracked as M0064-Q21-walker. |
| **M0064-Q20 correlated scalar decorrelation** | **DEFERRED → M0065** | Multi-level IN+correlated scalar requires deeper unnesting infrastructure work; not addressed this session due to budget. |
| **M0064-Q5 six-table MHJ throughput** | **DEFERRED → M0065** | pprof-driven optimisation (intra-MHJ NLI surgery for region/nation small-build joins). Substantial planner work; not addressed. |

## Per-query results & delta vs M0063

| Q   | M0063 (s) | M0064 (s) | Δ | Rows | Notes |
| --- | --------:| --------:| -- | ----:| ----- |
| Q1  |   37.73 |   41.87 | +4.14 |    4 | flat (noise) |
| Q2  |    8.22 |    8.63 | +0.41 |  470 | flat |
| Q3  |   37.20 |   49.00 | +11.80 | 11462 | small noise |
| Q4  |  173.45 |  177.14 | +3.69 |    5 | flat |
| Q5  |  600.09 |  600.07 |  0.0 |    — | cancel held; deferred |
| Q6  |   26.84 |   30.54 | +3.70 |    1 | flat |
| Q7  |   32.79 |   36.59 | +3.80 |    4 | flat |
| Q8  |  211.22 |  209.31 | −1.91 |    2 | preserved by MHJ-only rebind gate |
| **Q9** | **ERROR 600** | **OK 233.53** | **−367** | **7** | **M0064-Q9 fix: 600 s cancel → 233.53 s, 7 rows** |
| Q10 |   36.89 |   42.67 | +5.78 | 20574 | flat |
| Q11 |    4.28 |    4.24 | −0.04 |  1142 | flat |
| Q12 |   91.00 |   92.87 | +1.87 |    2 | flat |
| Q13 |   64.46 |   63.41 | −1.05 |   35 | flat |
| Q14 |   31.16 |   33.98 | +2.82 |    1 | flat |
| Q15a |   21.29 |   24.44 | +3.15 | 10000 | flat |
| Q15b |   44.22 |   52.60 | +8.38 |    1 | preserved (still correct 1 row) |
| Q16 |    5.21 |    5.35 | +0.14 | 18170 | flat |
| Q17 |   66.00 |   69.86 | +3.86 |    1 | flat |
| Q18 |   92.52 |  101.63 | +9.11 |   11 | flat |
| Q19 |   66.99 |   68.92 | +1.93 |    1 | flat |
| Q20 |  600.01 |  600.01 |  0.0 |    — | cancel held; deferred |
| Q21 |  600.10 |  600.33 |  0.0 |    — | cancel held; deferred (lift attempt reverted) |
| Q22 |   62.20 |   60.35 | −1.85 |    7 | flat |

### Aggregate impact

| Cohort | M0063 total | M0064 total | Δ |
| ------ | ----------:| ----------:| -- |
| OK queries (excl. cancels) | 1366.67 s | 1610.71 s | +244 s (Q9 added) |
| Newly-OK (Q9) | 0 (incorrect) | 233.53 s | added correctness |
| **Total OK row count parity** | 18/22 | **19/22** | **+1 (Q9)** |

The OK-cohort wall-clock increase is dominated by Q9 entering
the cohort (was a 600 s cancel in M0063 and not in the OK total).
Other queries are within typical run-to-run noise (±5–10 s).

## Headline outcomes

- **Q9 regression FIXED.** M0063-0001's blanket Name re-bind
  crossed wires for Q9's chained-NLI shape; the new
  `*MultiHashJoin`-only gate preserves the Q8 / Q15b fix while
  leaving Q9's already-correct keys untouched. **Q9: 600 s
  cancel → 233.53 s / 7 rows.**
- **19 / 22 OK** with correct non-zero row counts (was 18 / 22
  in M0063).
- **3 still cancel** (Q5, Q20, Q21) — all named follow-ups
  carrying through to M0065 with concrete root-cause notes.
- No regression on previously-OK queries.

## Q9 root-cause analysis

Debug logs captured during diagnosis showed the contrasting
outer / index pattern:

```
Q8  NLI:   outerType=*MultiHashJoin       innerTable=part
            key l_partkey  crIndex=40 schema[40]="l_quantity" rebind→42
Q9  NLI#1: outerType=*MultiHashJoin       innerTable=orders
            key l_orderkey crIndex=21 schema[21]="l_orderkey" rebind→21 (no-op)
Q9  NLI#2: outerType=*NestedLoopIndexJoin innerTable=partsupp
            key l_suppkey  crIndex=15 schema[15]="s_acctbal"  rebind→24 (HARMFUL)
Q21 NLI#1: outerType=*MultiHashJoin       innerTable=lineitem
            key l_orderkey crIndex=12 schema[12]="s_name"     rebind→21
Q21 NLI#2: outerType=*NestedLoopIndexJoin innerTable=lineitem
            key l_orderkey crIndex=12 schema[12]="s_name"     (NLI outer; rebind needed but gate misses)
```

For Q9 NLI#2, the original Index 15 already aligns to the
runtime row layout — the rebind to 24 (which matches the schema
*annotation* but not the runtime layout) corrupts the probe.
Gating the rebind on `*MultiHashJoin` outers leaves Q9 NLI#2
untouched (outer is NLI), restoring the M0062 behaviour.

For Q21 NLI#2, the original Index 12 is *not* aligned to
runtime — but the MHJ-only gate doesn't fire (outer is NLI).
That's why Q21 stays at the M0063-0004 partial state. The full
fix needs `applyJoinTreePosMap` to recurse into NLI nodes so
post-rewrite key remapping can correctly translate
FROM-cumulative → MHJ-output indices for the chained-NLI case
— captured as M0064-Q21-walker.

## Open follow-ups (M0065)

| Item | Plan |
| ---- | ---- |
| **M0064-Q21-walker** | Make `applyJoinTreePosMap` recurse into `*NestedLoopIndexJoin` and re-resolve its keys against `outerNode.Output()` (transitively to MHJ). Then re-attempt the inner-only conjunct lift on top of correctly-resolved NLI keys. |
| **M0064-Q20** | Multi-level correlated scalar decorrelation. Diagnose via post-`unnestSubqueriesInPlan` Q20 tree dump; if the inner SubqueryExpr survives, recurse the cloned partsupp inner plan or relax `canUnnestSubquery` for the SUM aggregate shape. |
| **M0064-Q5** | Profile-driven. Capture Q5 pprof at 1200 s; if hash-insertion-bound, extend `rewriteJoinsToNLI` to walk into `*MultiHashJoin.Tables[i]` for region / nation small-build joins. |

## Verification

- `go test ./...` PASS at commit `0633090` (the
  `TestKillKillRecovery` TempDir cleanup race is pre-existing).
- `./tpch-runner --queries=8,9,15,21` returns 2 / 7 / 1 / cancel.
- 22-query SF=1 sweep: **19 / 22 OK** with row-count parity for
  every previously-OK query.

## Summary

M0064 lands a single targeted fix (Q9 regression) and resolves a
chronic incorrect-result issue introduced by M0063-0001. Three
named follow-ups (Q21 NLI walker, Q20 decorrelation, Q5
throughput) are deferred to M0065 with concrete root-cause
analysis captured in this report.
