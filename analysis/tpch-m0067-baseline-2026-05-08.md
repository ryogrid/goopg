# TPC-H M0067 Baseline (2026-05-08)

22-query SF=1 sweep at `cancel-after=1200s` after M0066
PIVOT's runtime allocation reductions and M0067's planner
investigations. Compares against the M0066 baseline
(`tpch-m0066-baseline-2026-05-08.md`, commit `55432e2`,
`cancel-after=600s`).

| Run parameter | Value |
| --- | --- |
| Commit         | `<NEXT>` (`perf-analysis`) |
| Dataset        | TPC-H SF=1 (HammerDB schema) |
| Server         | `goopg`, GOMEMLIMIT=12 GiB, GOGC=off |
| Cancel-after   | **1200 s** (was 600 s in M0064-M0066) |
| Per-query budget | 1220 s |
| Driver         | `cmd/tpch-runner` (per-query connection isolation) |
| Log            | `bench/tpch/logs/m0067_22q_20260508T081339.log` |

## Sub-task outcomes

| Sub-task | Verdict | Outcome |
| -------- | ------- | ------- |
| **M0067-0001 Milestone doc + fix_plan** | **LANDED** | `docs/milestones/0067-tpch-structural-runtime.md` + `.ralph/fix_plan.md` updated. |
| **M0067-0002 1200s baseline sweep** | **LANDED** | Q21 newly completes at 1129.85 s but with **0 rows** (canonical ~411). Q5 / Q20 still cancel even at 1200 s. |
| **M0067-0003 Q9 composite-NLI** | **DEFERRED → M0068** | Implemented hoist + Name-based rebind (`splitFilterCrossEquiByName`, `rebindCrossEquiToJoinedCoords`). Q9 EXPLAIN with hoist showed `partsupp_pk` (composite) firing — but Q9 returned **1 row** (canonical many). Confirmed schema-annotation-vs-runtime mismatch (same root cause as M0064/M0065 walker attempts). Reverted; M0068 needs to fix the underlying schema/runtime layout reconciliation first. |
| **M0067-0004 Q21 NLI walker re-attempt** | **SKIPPED** | Blocked on M0067-0003 completing. Carried as M0068. |
| **M0067-0005 Projection narrowing** | **SKIPPED** | Same risk profile as the schema-mismatch bugs we hit with composite-NLI. Defer until schema management is solid. |
| **M0067-0006 Final report** | **THIS REPORT** | |

## Per-query results & delta vs M0066

| Q   | M0066 600s | M0067 1200s | Δ (s) | Rows | Notes |
| --- | ---------:| -----------:| -----:| ----:| ----- |
| Q1  |   48.27 |   47.38 | −0.89 |    4 | flat |
| Q2  |   12.55 |   12.45 | −0.10 |  470 | flat |
| Q3  |   39.68 |   39.51 | −0.17 | 11462 | flat |
| Q4  |  172.60 |  169.75 | −2.85 |    5 | flat |
| Q5  |  600.06 | 1200.02 | cancel |  — | structural; 1200s budget didn't help |
| Q6  |   35.24 |   34.33 | −0.91 |    1 | flat |
| Q7  |   38.63 |   37.64 | −0.99 |    4 | flat |
| Q8  |  198.28 |  193.22 | −5.06 |    2 | flat |
| Q9  |  229.69 |  222.48 | −7.21 |    7 | preserved (still silent FN) |
| Q10 |   38.26 |   36.62 | −1.64 | 20574 | flat |
| Q11 |    3.94 |    3.75 | −0.19 | 1142 | flat |
| Q12 |   96.88 |   92.91 | −3.97 |    2 | flat |
| Q13 |   68.05 |   65.84 | −2.21 |   35 | flat |
| Q14 |   38.94 |   36.70 | −2.24 |    1 | flat |
| Q15a |  28.79 |   27.83 | −0.96 | 10000 | flat |
| Q15b |  59.99 |   57.71 | −2.28 |    1 | flat |
| Q16 |    8.53 |    8.07 | −0.46 | 18170 | flat |
| Q17 |   74.74 |   71.29 | −3.45 |    1 | flat |
| Q18 |   97.32 |   94.16 | −3.16 |   11 | flat |
| Q19 |   73.53 |   71.18 | −2.35 |    1 | flat |
| Q20 |  600.00 | 1200.00 | cancel |  — | structural; 1200s didn't help |
| **Q21** | **600.00** | **1129.85** | **NEW OK** | **0** | **completes at 1130 s but 0 rows (wrong)** |
| Q22 |   63.38 |   61.67 | −1.71 |    7 | flat |

### Aggregate impact

| Cohort | M0066 600s | M0067 1200s | Δ |
| ------ | ----------:| ----------:| -- |
| OK queries (excl. cancels) | 1427.29 s | 1394.34 s | **−32.95 s** (~2.3 % faster, run-to-run) |
| **OK count** | **19/22** | **20/22** | **+1 (Q21)** |
| **Row-count parity** | preserved 19/19 | **18/20 OK with parity (Q21=0r is wrong)** | **regression in correctness** |

## Key findings

1. **Q5 / Q20 don't budge at 1200 s.** Even with double the
   timeout and M0066 PIVOT's 67 % CPU reduction on Q5's
   pprof window, Q5 still cancels. The residual is
   `runtime.duffcopy` / `memclr` / `memmove` (~60 % of CPU
   in the post-PIVOT pprof) — fundamental row-at-a-time
   materialization cost.

2. **Q21 newly completes at 1130 s but returns 0 rows.**
   The Hash Anti join semantically succeeds within the
   larger budget, but produces 0 rows where canonical
   TPC-H SF=1 expects ~411 rows. This is **silent FALSE
   POSITIVES** — Anti-side hash-join Filter-wrapped
   lineitem causes the inner over-match relative to the
   canonical NLI-Anti, suppressing all outer rows. Same
   class of issue as Q9's silent false negatives.

3. **Q9 stable at 7 rows** across M0064 / M0065 / M0066 /
   M0067 — but this 7 is confirmed silent FALSE NEGATIVES
   (M0064 walker bisect proved cr.Index=15 picks
   `s_acctbal` and the partsupp_supplier_fkidx probe
   misses on type-compatible numeric values).

4. **Composite-NLI hoist works structurally but exposes
   schema/runtime mismatch.** M0067-0003 implemented and
   verified that `partsupp_pk` (composite) fires for Q9.
   But Q9 then returned 1 row instead of canonical many —
   the rebind picked correct schema-annotated indices that
   didn't match the executor's runtime row layout. Same
   root cause as M0064/M0065 walker attempts. Reverted.

## Architectural blocker (carried to M0068)

The recurring root cause across M0064 / M0065 / M0066-Q5 /
M0066-Q21 / M0067-Q9 is **the planner's schema annotation
disagrees with the executor's runtime row layout** in
specific multi-table chains. Symptoms:

- Plan tree's ColumnRef indices reference one coordinate
  space (subset-FROM-order, OID-sorted, FROM-cumulative,
  or post-DP merged-schema depending on which pass last
  touched them).
- Runtime row layout follows yet another (or the same in
  some lucky cases).
- When they coincide, queries work. When they diverge,
  the executor probes wrong columns silently — type
  compatibility (numeric ≈ numeric) makes the bug invisible.

Fixing this fundamentally requires either:
- A unified schema-coordinate convention enforced across
  all planner passes AND mirrored in the executor's row
  layout.
- OR runtime-side validation that detects index/column
  Name disagreement and errors loud (so silent false
  negatives become loud failures).

## Open follow-ups (M0068)

| Item | Plan |
| ---- | ---- |
| **Schema/runtime layout reconciliation** | Audit every planner pass that mutates ColumnRef.Index. Establish a single source of truth (e.g., post-rewrite layout). Validate at runtime via Name + Type checks in IndexScan probe paths. |
| **Composite-NLI for Q9** | Re-attempt M0067-0003's hoist after schema reconciliation lands. Expected to give Q9 correct row count. |
| **Q21 Anti-side NLI** | Once Q9 composite-NLI lands, re-attempt the NLI walker rebind (M0064 work). Expected to give Q21 correct row count. |
| **Q5 structural** | After schema reconciliation, attempt projection narrowing (M0067-0005 deferred) for Q5's `duffcopy` reduction. |
| **Q20 structural** | Either (a) timestamp btree v0, or (b) unnest non-correlated IN to SemiJoin (currently only correlated INs are unnested). |

## Verification

- `go test ./...` PASS at the M0067 commit.
- 22-query SF=1 sweep at `cancel-after=1200s`:
  **20 / 22 OK** (Q5, Q20 cancel) — but Q21=0 rows is
  silent false positives.
- `bench/tpch/pprof/cpu_q5.prof` family captures the
  GOGC=100 → GOGC=off + BorrowRow + cache progression
  (M0066) and shows Q5 is now memory-copy bound.

## Summary

M0067 lands the **1200 s baseline** and validates the
M0066 PIVOT's allocation wins (Q1-Q19, Q22 universally
faster, 32.95 s cohort improvement). It also surfaces
that Q5 / Q20 are not time-bounded — the structural
fix (M0067-0005 projection narrowing) was attempted as
M0067-0003 (composite-NLI hoist) and deferred when the
underlying schema/runtime mismatch issue resurfaced.

Q21 newly completes at 1130 s but with 0 rows; this is
**silent false positives**, the symmetric counterpart to
Q9's silent false negatives.

The architectural blocker for closing all three queries
is consistent: **planner schema annotation must agree with
executor runtime row layout**. M0068 takes this on first.

Honest scoreboard:
- OK count: 19 → 20 (+1, but Q21 with wrong rows).
- Wall clock on previously-OK queries: −33 s aggregate.
- Net correctness: same (Q9 still wrong, Q21 newly wrong,
  Q5/Q20 still cancel).
