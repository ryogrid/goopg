# M0146-0192 — an initplan's output is parallel-safe; the leader evaluates it for the workers

Status: closed 2026-10-14.

## Problem

TPC-H Q22 filters customer with `c_acctbal > (SELECT avg(c_acctbal) …)`, an uncorrelated scalar subquery:

- PG 18.3 plans GroupAggregate → Gather Merge → Sort → Nested Loop Anti Join over a Parallel Seq Scan. The
  filter `c_acctbal > (InitPlan 1).col1` runs inside the workers.
- goopg planned the anti join entirely serially: GroupAggregate → Sort → Nested Loop Anti Join over a Seq Scan.

A minimal SF0.25 probe shows the same split (`analysis/m0146/m0146-0014/routing-20261014b/initplan-parallel-probe.sql`):

- with a constant in place of the subquery, goopg parallelises;
- with the subquery, it does not.

## Diagnosis

**PG.** An uncorrelated EXPR sublink becomes an initplan whose output is a PARAM_EXEC Param.

- `is_parallel_safe` (clauses.c) adds every initplan's `setParam` to `safe_param_ids`, so a qual reading that Param
  is parallel-safe.
- The initplan body is not judged as part of the qual.
- At execution, `ExecInitParallelPlan` (execParallel.c) evaluates the Gather's `initParam` in the leader and
  serialises the values to the workers. The body, with its own Gather, never runs in a worker.

**goopg.** `isParallelSafeExpr` (considerparallel.go) judged every sublink by its plan (`subPlanParallelSafe`).
The initplan body holds a Gather, so the qual was parallel-restricted. Customer got no partial path, and
`subtreeHasParallelRestrictedQual` refused the upper splits as well.

The executor could not have run such a qual in a worker anyway:

- `NewWorkerContext` gives each worker empty sublink caches;
- so each worker would have run the initplan body itself, nesting a Gather.

## Change

Planner (considerparallel.go):

- `InitPlanEvaluatedByLeader(sq)` admits exactly the sublinks the leader can evaluate. It requires:
  - an uncorrelated scalar `SubqueryExpr`;
  - no PARAM_EXEC binding (`ParParam` / `Args`);
  - not the min/max rewrite's param initplan;
  - no outer reference or `ExecParamRef` anywhere in the body, checked by a deep walk. The `IsNonCorrelated`
    flag's own walker does not descend into nested sublinks.
- `isParallelSafeExpr` prunes such a sublink as safe without judging its body: PG's `safe_param_ids`.

Executor:

- `prepareWorkerInitPlans` (parallel_initplan.go) runs at every worker launch site: Gather, Gather Merge and the
  cooperative Parallel Hash build. It walks the worker subtree deeply (`optimizer.WalkPlanExprsDeep`, including
  the bodies of correlated SubPlans the workers run), evaluates each admitted initplan once in the leader, and
  returns a fresh map.
- Each worker Context carries that map as `InitPlanValues`, and `evalSubquery` serves it before anything else.
- The map is built before the workers start and never mutated afterwards, so it needs no lock.
- Planner and executor share the one predicate, so a sublink the planner admitted is always pre-evaluated.

## Evidence

Files are in `analysis/m0146/m0146-0192/`.

- **TPC-H Q22:** now matches PG's plan shape. TPC-H match 13 → 14. Categories (CATEGORIES-EXCL-MATCH): join-order
  6 → 5, parameterisation 3 → 2, sort-strategy 2 → 1, parallelism 3 → 2.
- **SF0.25 probe:** goopg plans PG's Finalize HashAggregate → Gather → Partial HashAggregate, with the initplan
  filter in the workers. The result equals PG's (17987 groups, sum 226634). EXPLAIN ANALYZE shows the initplan
  missed the cache once (`misses=1`), so it was evaluated once.
- **TPC-DS fire set:** SF0.25 79/99 and SF1 65/99, unchanged.
- **SF0.25 sweep:** 99 of 99 pass.
- **TPC-H:** the acceptance arm is value-identical; spotcheck passes.
- **Regress:** select_parallel, subselect, aggregates and write_parallel diff identically to HEAD.
- **Test:** `TestInitPlanQualRunsInsideParallelWorkers` checks PG 18.3's exact plan text and the count, which
  needs the leader-evaluated value.

## Not ported (ledgered)

- An uncorrelated EXISTS is an initplan in PG too, with its Param in `safe_param_ids`. goopg still judges
  `ExistsExpr` by its body.
- PG also admits initplans of enclosing query levels (`proot->parent_root`). goopg admits only bodies with no outer
  or param reads at all.
- EXPLAIN ANALYZE does not count worker-side reads of the shipped value as SubPlan calls.
- Q22's Index Only Scan on `order_customer_fkidx` still costs 45.17 against PG's 4.01. That is the heap-fetch
  term on the unvacuumed :65433 load (M0146-0178).
