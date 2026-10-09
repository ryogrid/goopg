# M0146-0002f: a parallel-safe SubPlan keeps its relation parallel-safe

## PG behaviour

`max_parallel_hazard_walker` (postgres/src/backend/optimizer/util/clauses.c)
has a SubPlan arm. A SubPlan is parallel-restricted only when its plan is
not `parallel_safe`. The walker then checks the test expression (with the
SubPlan's own params counted as safe) and the args. A plan is
`parallel_safe` when every path in it is. So an uncorrelated subquery over
ordinary relations is safe, and each participant runs it in full. A
correlated subquery reads its outer values through PARAM_EXEC params,
which are parallel-restricted unless an initplan supplies them, so it is
not safe. TPC-H Q16's `NOT IN (SELECT s_suppkey FROM supplier ...)` filter
on partsupp is therefore safe, and PG plans a Parallel Hash Join over the
filtered partsupp scan.

## goopg before

`isParallelSafeExpr` treated every expression owning an inner plan as
restricted, so Q16's partsupp relation could not be parallel.

## Change

- `isParallelSafeExpr` walks with `scopeSignal`. For each sublink it hands
  the inner plan to `subPlanParallelSafe`; the operand is walked as
  before. A multi-assignment row subquery stays restricted.
- `subPlanParallelSafe` answers `parallel_safe` for the body:
  - no unsafe node (`subtreeHasUnsafeNode`: row locks, temp and virtual
    relations);
  - an allowlist of node kinds (scans, Project, Filter, Join,
    NestedLoopIndexJoin, Aggregate, Sort, Distinct, DistinctOn, Limit,
    Values). Gather, Gather Merge, CTE and worktable scans and anything
    unenumerated refuse;
  - children through `planChildNodes`, failing closed;
  - every expression parallel-safe through `walkPlanExprs`.
    `OuterColumnRef` / `ExecParamRef` (correlation) refuse, and nested
    sublinks recurse.
- Execution needs no change. A worker's Context is created with empty
  sublink caches (`NewWorkerContext`), the hashed probe set lives in that
  cache, and the plan is read-only. `TestParallelSubPlanIdentity` runs
  NOT IN, IN, a scalar comparison and [NOT] EXISTS under a real Gather at
  1, 2 and 4 workers against the serial result, under `-race`. It shows
  each worker reads the SubPlan's whole relation rather than a
  block-allocator partition.

## Results

- TPC-H Q16: Parallel Hash Join over the filtered partsupp scan, with the
  SubPlan evaluated in the workers (`analysis/m0146/m0146-0002f/`). The
  TPC-H census is unchanged; values are identical.
- TPC-DS: Q6, Q14, Q45 and Q58 change plan.
  - Q6's record moves from parallelism to sort-strategy (both scales).
  - Q45 moves depth 2 -> 4 at SF1 and 3 -> 2 at SF0.25, where goopg now
    parallelises a sort PG keeps serial.
  - join-order and join-method each drop by one at both scales.
- Sweep 96/96, fire set PASS, regress cases unchanged.

## Not covered (ledgered)

- PG evaluates an uncorrelated scalar subquery once, as an InitPlan in the
  leader, and passes its value to workers as a param (`safe_param_ids`).
  goopg evaluates it in each worker. The result is the same (volatile
  bodies are excluded), but the work is repeated.
- EXPLAIN (ANALYZE) SubPlan-site counters of worker executions.
