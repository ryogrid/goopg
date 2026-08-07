M0128-P2.1 COMPLETE — cooperative parallel hash build reopen-condition MET

Task: M0128-P2.1 — EXPLAIN ANALYZE sweep to determine if cooperative
  parallel hash build is justified

Files:
  - internal/executor/join_batch.go: added BuildTimeNs to HashJoinStats
  - internal/executor/operators_join_agg.go: build timer in
    buildLazyHashTable; recordBuildTime helper
  - internal/executor/operators_explain.go: formatHashJoinInfoLine gate
    widened (NBatch>0 or BuildTimeNs>0); "Build Time: N ms" line
  - analysis/m0128-p2.1-hash-build-measurement.md: measurement write-up
  - docs/design/parallel-query/10-roadmap.md: updated deferred table —
    reopen condition MET
  - .ralph/fix_plan.md: M0128-P2.1 checked off with verdict

Key symbols: HashJoinStats.BuildTimeNs, joinOp.recordBuildTime,
  formatHashJoinInfoLine

Hypothesis/Findings: VERDICT=GO
  - supplier 10K: build 0.7% of total (negligible)
  - customer 150K: build 34.6% of total
  - part 200K: build 12.6% of total
  - orders 1.5M: build 41.0% of total (spilling, 2 batches)
  - Reopen condition MET: cooperative parallel hash build is justified
  - BuildTimeNs instrumentation is a permanent EXPLAIN ANALYZE enhancement

Next step: M0128-P2.2 (bitmap heap scan design doc) or continue with
  other M0128 items in fix_plan.md order

Gates run: UNITS PASS, SPOT PASS (Q12=2/Q13=35)

In-flight: none
