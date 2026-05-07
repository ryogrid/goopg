# Milestone 0066 — TPC-H Residual Q5/Q20/Q21 Final

## Goal

Close the three queries still cancelling at 600 s on SF=1
after M0065: **Q5, Q20, Q21**. Reach 22/22 OK on the SF=1
sweep.

## Context

M0065 (commit `5829312`) reached 19/22 OK by fixing Q9 (M0064)
and adding the partial NLI Outer-recurse walker. The three
deferred items each have specific root-cause notes from the
M0065 baseline report:

- **Q21**: NLI's outer key indices stale relative to MHJ
  output; `applyJoinTreePosMap` recurses into Outer but does
  NOT re-resolve NLI's own keys.
- **Q20**: `unnestSubquery` works but the lineitem scan inside
  the IN subquery has no predicate pushdown for the
  `l_shipdate` range — confirmed via `bench/tpch/pprof/cpu_q20.prof`
  where `evalInExpr` dominates 85 % of CPU.
- **Q5**: 6-table chained MHJ; the `o_orderdate` date filter
  may be mis-classified into `leafFilters` causing per-match
  re-evaluation in `initStepHelper`.

## Sub-tasks

- **M0066-0001 Q5 date filter + IndexScan promotion.**
  Phase 1: verify `partitionFilters` classification.
  Phase 2: promote orders SeqScan to range-bounded IndexScan
  on `o_orderdate` if not already.
- **M0066-0002 Q20 IN-subquery predicate pushdown.**
  Add post-unnest pass that walks SubqueryExpr/InExpr inner
  plans and applies `rewriteScanInputsWithSingleTablePredicates`.
- **M0066-0003 Q21 NLI walker key Name-rebind.**
  Add `reresolveNLIByName` and wire into
  `applyJoinTreePosMap` + `remapPosMapAfterRewrite` NLI
  cases. Verify Q9 row-count parity (acceptable outcomes
  documented in plan).
- **M0066-0004 Final 22-query SF=1 sweep + report.**

## Acceptance

- **Soft**: Q5 / Q20 / Q21 complete in < 600 s with canonical
  row counts.
- **Hard**: 22/22 OK with row-count parity OR newly-deferred
  worst-case (Q9 cardinality explosion) explicitly named in
  the M0066 report.

## NO-DEFERRAL POLICY

Each sub-task either lands or carries a concrete root-cause
note for a successor milestone.
