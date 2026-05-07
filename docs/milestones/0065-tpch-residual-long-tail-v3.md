# Milestone 0065 — TPC-H Residual Long-Tail v3

## Goal

Close the three TPC-H queries still cancelling at 600 s on
SF=1 after M0064: **Q5, Q20, Q21**. Reach 22/22 OK on the
SF=1 sweep.

## Context

M0064 (2026-05-07, commit `0633090`) fixed the Q9 regression
introduced by M0063-0001 by gating the NLI Name re-bind on
`*MultiHashJoin` outers. The 22-query SF=1 sweep
(`bench/tpch/logs/m0064_22q_20260507T220942.log`) reached
**19/22 OK**. The remaining three cancels are:

- **Q21** — NOT EXISTS Anti-side stays on hash join because
  `liftInnerOnlyFilterConjuncts` corrupts NLI key probing at
  runtime when the Anti's outer is itself an NLI (Semi → MHJ).
  Root cause: `applyJoinTreePosMap` doesn't recurse into
  `*NestedLoopIndexJoin`, so chained-NLI keys retain
  pre-rewrite indices that don't align with the runtime row
  layout.
- **Q20** — Multi-level correlated scalar `SUM(l_quantity)`
  inside a non-correlated IN survives unnesting; the inner
  SubqueryExpr remains a per-row evaluation.
- **Q5** — Six-table MHJ chain throughput. Cancel-prop is
  responsive; the per-step cost dominates. Region / nation
  small-build joins should drive the chain via index-driven
  inner instead of the current hash build.

## Sub-tasks

- **M0065-0001 Q21 NLI-aware key remap walker.**
  Extend `applyJoinTreePosMap` to recurse into
  `*NestedLoopIndexJoin` so post-NLI-rewrite key remap fires.
  Then re-enable `liftInnerOnlyFilterConjuncts` in
  `unnestExistsExpr`. Q9 must NOT regress.
- **M0065-0002 Q20 correlated scalar decorrelation.**
  Diagnose the post-unnest plan tree for Q20; ensure the
  IN's cloned partsupp inner plan recurses into its scalar
  SubqueryExpr (`unnestSubqueriesInPlan`). Relax
  `canUnnestSubquery` for the SUM-over-single-Filter shape
  if rejected.
- **M0065-0003 Q5 six-table MHJ throughput.**
  Profile Q5 baseline (capture pprof at 1200 s).
  If hash-insertion-bound, extend `rewriteJoinsToNLI` to
  walk into `*MultiHashJoin.Tables[i]` for region / nation
  small-build joins.
- **M0065-0004 Final 22-query SF=1 sweep + report.**
  Document the outcome in
  `analysis/tpch-m0065-baseline-<date>.md`.

## Acceptance

- **Soft**: Q5 / Q20 / Q21 all complete in < 600 s with
  canonical row counts (~411 for Q21, 411 for Q20, 5 for Q5).
- **Hard**: 22/22 OK with row-count parity on the SF=1 sweep.

## NO-DEFERRAL POLICY

Each sub-task either lands or carries a concrete root-cause
note for a successor milestone. No partial fixes that leave a
correctness regression behind.
