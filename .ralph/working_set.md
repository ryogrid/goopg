M0128-P5.2 COMPLETE — Rows Removed by Filter / by Join Filter

Task: M0128-P5.2 — EXPLAIN ANALYZE counters for scan qual rejects
  ("Rows Removed by Filter") and join residual rejects ("Rows Removed
  by Join Filter")

Files:
  - internal/executor/instrument.go: added filterRejected/joinFilterRejected
    to nodeStats; filterRemoveCounter/joinFilterRemoveCounter interfaces;
    maybeInstrument wiring
  - internal/executor/operators.go: filterOp gains filterRemoved pointer;
    setFilterRemoveCounter; increment on rejection
  - internal/executor/operators_nljoin.go: nestedLoopIndexJoinOp gains
    joinFilterRemoved; setJoinFilterRemoveCounter; increment on
    evalPredicateSlot reject
  - internal/executor/operators_join_agg.go: joinOp gains
    joinFilterRemoved; setJoinFilterRemoveCounter;
    joinPredicateMatch/joinPredicateMatchSlot increment on reject
  - internal/executor/join_merge_key.go: mergeResidualMatch increments
    joinFilterRemoved on reject
  - internal/executor/operators_explain.go: walkPlanAnalyzeFiltered
    signature +filterRowsRemoved int64; Filter collapse accumulates
    count; scan/join nodes emit "Rows Removed by Filter: N" and
    "Rows Removed by Join Filter: N" (per-loop average, zero suppressed
    in text); planToJSONWithStats emits both properties unconditionally
  - internal/executor/explain_analyze_test.go: golden tests
    TestExplainAnalyzeRowsRemovedByFilter +
    TestExplainAnalyzeRowsRemovedByJoinFilter
  - .ralph/fix_plan.md: M0128-P5.2 checked off with completion note

Key symbols: nodeStats.filterRejected, nodeStats.joinFilterRejected,
  filterRemoveCounter, joinFilterRemoveCounter, filterOp.filterRemoved,
  joinOp.joinFilterRemoved, walkPlanAnalyzeFiltered.filterRowsRemoved

Hypothesis/Findings:
  - Counter pattern mirrors heapFetches: interface+pointer handed by
    maybeInstrument, nil-safe increment at the rejection site
  - PG uses two counters (nfiltered1/nfiltered2) per node; goopg uses
    filterRejected (for collapsed Filter) and joinFilterRejected (for
    join residual). The PG-style nfiltered2 (non-join qual on join)
    is not needed in goopg because the planner places a separate Filter
    node above the join for non-join quals — that Filter node is then
    collapsed by the text walker and its count is passed down
  - Chained Filter nodes accumulate counts (outer + inner)
  - Zero row/checksum deltas expected: all increments are nil-guarded,
    only active under EXPLAIN ANALYZE

Next step: M0128-P2.1 (parallel hash build reopen-condition) or next
  M0128 task per fix_plan.md ordering (P5.2→P2.1)

Gates run: UNITS PASS, SPOT PASS (Q12=2/Q13=35), golden tests PASS

In-flight: none
