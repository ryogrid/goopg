# TPC-H M0058 Verification Report (2026-05-07)

## Scope

Post-fix verification of milestone M0058 (`feat(m0058)` commit
`d509107`) against the HammerDB-loaded TPC-H SF=1 dataset on
`bench/tpch/runtime_goopg`. Six queries were re-run to validate each
fix landed in the M0058 batch and to identify remaining gaps.

| Run parameter | Value |
| --- | --- |
| Commit | `d509107` (`perf-analysis`) |
| Dataset | TPC-H SF=1 (HammerDB schema, all integer columns NUMERIC) |
| Server | `goopg` listening on `127.0.0.1:65433` |
| Per-query timeout | 320 s |
| Per-query cancel-after | 300 s |
| Driver | `cmd/tpch-runner` |
| Log | `bench/tpch/logs/m0058_verify_20260507T005726.log` |

## Results

| Query | Status | Elapsed | Rows | Baseline (pre-M0058) | Speedup | Hypothesis verified |
| --- | --- | --- | --- | --- | --- | --- |
| Q17 | OK    | 73.42 s  | 1     | 70.4 s             | ~1× (within noise) | M0058-0003 NUMERIC fast path: no regression on NLI-pruned shapes |
| Q11 | OK    | 4.55 s   | 1142  | 3600 s (timeout)   | **≥790×**          | M0058-0001 SubPlan cache: non-correlated `(SELECT SUM(...) FROM partsupp)` collapsed to one execution |
| Q22 | ERROR | 300.00 s | —     | 1248 s (cancelled) | n/a                | **Gap remains.** Correlated `NOT EXISTS` not unnested; M0058-0002 deferred |
| Q16 | OK    | 4.44 s   | 18170 | 1248 s (cancelled) | **≥280×**          | M0058-0001 SubPlan cache: non-correlated `IN (SELECT ... FROM supplier WHERE s_comment LIKE ...)` collapsed |
| Q18 | OK    | 107.29 s | 11    | timeout/cancelled  | **≥30×**           | M0058-0001 SubPlan cache + correlated key plan finally completes within budget |
| Q19 | (cancelled at 300 s by runner; partial — see notes) | n/a | n/a | timeout (CROSS JOIN) | n/a | M0058-0004 promotes the OR-of-ANDs equijoin to a Hash Join key, but the full residual OR predicate is still expensive on SF=1; needs follow-up |

## Discussion

### Confirmed wins

- **M0058-0001 (Non-correlated SubPlan cache)** is the headline fix.
  Q11 (3600 s → 4.55 s) and Q16 (1248 s → 4.44 s) move from "timed
  out" to "interactive" because the planner now flags subqueries with
  no `OuterColumnRef` inside their plan tree as non-correlated; the
  executor takes a constant cache key, executes the SubPlan once, and
  reuses the result across every probe row.
- **Q18** completes in 107 s — well inside the 320 s budget — for the
  first time, validating that the cache logic is correct under
  multi-level subquery nesting (Q18 has both correlated and
  non-correlated sub-shapes).
- **M0058-0003 (NUMERIC int64 fast path)** has no measurable effect on
  Q17 (73.4 s vs 70.4 s baseline; within run-to-run noise). This is
  expected: Q17's hot path is the NLI-style anti-join that prunes
  most rows before NUMERIC arithmetic dominates. The fast path's
  benefit shows up where NUMERIC decode is on the critical path
  (COPY-time bulk loads, Q1-style aggregates over `l_extendedprice`),
  not on Q17.

### Remaining gaps

- **Q22 — correlated `NOT EXISTS` not unnested.** Q22's predicate is
  `NOT EXISTS (SELECT * FROM orders WHERE o_custkey = c_custkey)`,
  which is a correlated EXISTS that must be unnested to an anti-join
  to be efficient. The executor's correlated re-execution path runs
  the inner subquery per outer row, which scales as O(|customer| ×
  |orders|). M0058-0002 (EXISTS/NOT EXISTS unnesting to
  semi-/anti-join) was deferred from this milestone batch and is
  tracked as `M0058-0002-followup`. Until that lands, Q22 will
  continue to time out.
- **Q19 — OR-of-ANDs residual predicate.** M0058-0004 successfully
  extracts `l_partkey = p_partkey` as a Hash Join key (verified by
  `or_of_ands_test.go` and a manual EXPLAIN), removing the prior
  CROSS JOIN. However, the residual three-branch OR-of-ANDs filter
  (each branch combining brand, container, quantity, and shipmode
  predicates) is still evaluated row-by-row and dominates at SF=1.
  Q19 was cancelled at 300 s. A follow-up (vectorising the residual
  predicate, or pushing branch-specific selectivity hints into the
  build side) is needed to bring it inside budget.

### Process notes

- The `--cancel-after=300s --per-query-timeout=320s` runner flags
  exercised the M0058-0005 NL/MHJ probe-phase cancellation. Both Q22
  and Q19 returned a SQLSTATE `57014` ("canceling statement due to
  user request") within ~5 ms of the deadline — confirming that the
  prior bug (NL/MHJ ignoring `ctx.Err()` and running 60+ min past
  cancel) is fixed.
- The TCP-keepalive change (30 s probe period in
  `internal/server/server.go`) was not directly exercised here but is
  in the build under test.

## Status of M0058 sub-tasks

| Sub-task | Status |
| --- | --- |
| M0058-0001 Non-correlated SubPlan constant-key cache | LANDED — verified by Q11/Q16/Q18 |
| M0058-0002 EXISTS/NOT EXISTS unnesting                | DEFERRED — tracked as `M0058-0002-followup`; Q22 remains the canonical reproducer |
| M0058-0003 NUMERIC int64 fast path                    | LANDED — Q17 unchanged (expected); larger win expected on Q1/COPY |
| M0058-0004 OR-of-ANDs join-key extraction             | LANDED partial — CROSS JOIN gone for Q19; residual OR cost remains |
| M0058-0005 NL/MHJ probe-phase ctx.Err() + keepalive   | LANDED — cancel latency now ms-scale on Q22/Q19 |
| M0058-0006 WaitEventEnd hooks for I/O paths           | LANDED — `pg_stat_activity.wait_event` now reports I/O waits |
| M0058-0007 Verification re-run after fixes            | THIS REPORT |

## Recommended next steps

1. **Open M0058-0002-followup.** EXISTS/NOT EXISTS unnesting to
   semi-/anti-join is the single highest-value remaining fix; it
   unblocks Q22 and likely also helps Q4/Q21 shapes.
2. **File a Q19 residual-OR optimisation task.** Either vectorise the
   branch evaluation or teach the planner to convert the
   three-branch OR-of-ANDs into a UNION ALL of three independent
   joins with branch-specific build-side filters.
3. **Re-baseline the full 22-query sweep** once M0058-0002-followup
   lands, with a 600 s budget to capture Q19 and any other
   long-tail. The current run only re-checked the six queries
   directly affected by M0058.
