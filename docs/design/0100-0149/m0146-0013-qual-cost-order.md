# M0146-0013 — `order_qual_clauses`: quals evaluated cheapest first

Status: done 2026-10-05. Parent: none (M0145-0028's ledger residual).

## PG mechanism

- `order_qual_clauses` (createplan.c) sorts a qual list stably by
  `security_level` and then by `cost_qual_eval_node`'s per-tuple cost.
  Leakproof quals cheaper than 10 × `cpu_operator_cost` drop to level 0.
- It runs on every scan's `qpqual` and on every join's `joinqual` and
  `otherqual` (`create_scan_plan`, `create_nestloop_plan`,
  `create_mergejoin_plan`, `create_hashjoin_plan`). So the plan evaluates,
  and EXPLAIN prints, cheaper clauses first; equal costs keep their order.

## goopg

- `qualEvalOps` (qualevalcost.go) is the existing `cost_qual_eval` port:
  operator and function counts, ScalarArrayOp, and `cost_subplan`.
- The leaf-qual sort already existed in `equivalenceClausesLast`
  (M0146-0005co).
- Two of the sites this task named are gone: `flattenStrandedSeqScanFilters`
  was deleted by M0146-0012 slice 2, and the bypass Filter build by the
  M0145-0008 cutover.
- The remaining site was the join quals. `joinPredicate`, the one funnel for
  the hash, merge, nested-loop, NLI and NLI-bitmap arms, kept the
  restriction list's written order.

## Change

- `orderQualClauses` and `orderQualRestrictInfos` (qualevalcost.go) do the
  stable cost sort. Every security level is 0, which is PG's value without
  security-barrier views or RLS.
- `joinPredicate` orders the residual through `orderQualRestrictInfos`.
- `equivalenceClausesLast` now calls the shared sort.

## Result

- `TestJoinFilterOrderedByQualCost`: `ON a.k = b.k AND (a.x = 1 OR
  b.y = 2) AND a.z < b.z` prints PG's
  `Join Filter: ((a.z < b.z) AND ((a.x = 1) OR (b.y = 2)))`. HEAD printed
  the OR first.
- No corpus query has such a residual. TPC-DS EXPLAIN text is
  byte-identical, the TPC-H arm is 24/24, and fire-set categories are
  unchanged.

## Open (ledgered)

- Security levels (security-barrier views, RLS quals) and the leakproof
  refinement are not modelled.
