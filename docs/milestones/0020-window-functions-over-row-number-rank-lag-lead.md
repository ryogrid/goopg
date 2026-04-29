# Milestone 0020 - Window Function Support (OVER, ROW_NUMBER, RANK, LAG/LEAD)

**Status:** planned
**Depends on:** Milestone 0001 (core parser/planner/executor path), Milestone 0003 (TPC-H and analytic SQL coverage), Milestone 0006 (planner/statistics maturity), Milestone 0018 (EXPLAIN and EXPLAIN ANALYZE introspection).
**Drives:** PostgreSQL-compatible analytic query support for window functions, focused on `OVER`, `ROW_NUMBER`, `RANK`, `LAG`, and `LEAD`.

## Context

goopg currently executes aggregates, joins, and subqueries over a growing SQL subset, but does not support SQL window functions. This blocks a large class of reporting and ranking queries used by BI tools, ORMs, and migration workloads.

This milestone introduces practical window-function support in staged form:

- Stage A: `OVER`, `ROW_NUMBER`, and `RANK` with partition/order semantics.
- Stage B: `LAG` and `LEAD` with offset/default support in the scoped subset.

The goal is to unlock common analytic patterns quickly while keeping frame and execution semantics explicit, testable, and operable.

## In Scope

### SQL Parser and AST

- Parse and represent window function invocation in the supported subset:
  - `func() OVER (...)`
  - named and inline window definitions in the scoped subset.
- Parse and represent:
  - `PARTITION BY`
  - `ORDER BY`
  - frame clauses in the supported subset required by selected functions.
- Parse and represent function-specific arguments for `LAG/LEAD` (value, optional offset, optional default).
- Return stable parser diagnostics for invalid window-clause or argument shapes.

### Analyzer and Name Resolution

- Validate allowed window-function placement in supported query contexts.
- Validate partition/order expression references and type constraints.
- Validate `LAG/LEAD` argument counts and supported type combinations.
- Reject unsupported frame/function combinations with deterministic SQLSTATE errors.

### Planner Integration

- Introduce planning structures for window partitions and order keys.
- Add a window execution stage in plan pipelines where required.
- Ensure planner behavior remains deterministic when window and aggregate/projection paths coexist.

### Executor Integration

- Stage A:
  - compute `ROW_NUMBER` and `RANK` over partitioned and ordered streams.
- Stage B:
  - compute `LAG/LEAD` with optional offset/default in the supported subset.
- Implement partition-aware state management and row-sequencing behavior.
- Ensure null ordering and tie handling are deterministic in the scoped subset.

### Explain and Observability

- Surface window execution nodes/attributes in `EXPLAIN` and `EXPLAIN ANALYZE` output.
- Add runtime counters/timing for window stages where instrumentation is available.
- Ensure explain output remains stable enough for regression tests.

### Regression and Compatibility Testing

- Add parser/analyzer/planner/executor tests for all supported window functions.
- Add edge-case tests:
  - empty partitions,
  - single-row partitions,
  - tie-heavy ranking groups,
  - offset out of range for `LAG/LEAD`.
- Add compatibility tests against representative PostgreSQL query shapes using these functions.

## Out of Scope

- Full PostgreSQL window-function catalog parity in one pass.
- Every frame variant and frame-exclusion rule.
- Ordered-set aggregates and hypothetical-set functions.
- Incremental sort and all planner optimizations specific to window execution.
- Full collation and locale parity for every ordering edge case.

## Required Design Docs

Place under docs/design with sequential numbering at creation time:

- `0020-0001-window-parser-ast-and-analysis.md`
- `0020-0002-window-planner-and-execution-model.md`
- `0020-0003-row-number-rank-semantics.md`
- `0020-0004-lag-lead-semantics-and-testing.md`

These design docs should cross-link to:

- `docs/design/root-0010-parser.md`
- `docs/design/root-0011-planner.md`
- `docs/design/root-0012-executor.md`
- `docs/design/0003-0002-join-executors.md`
- `docs/design/0018-0003-explain-analyze-instrumentation.md`

## Reference

Upstream sources to consult:

- `postgres/src/backend/parser/gram.y`
- `postgres/src/backend/parser/parse_clause.c`
- `postgres/src/backend/parser/parse_agg.c`
- `postgres/src/backend/optimizer/plan/planner.c`
- `postgres/src/backend/executor/nodeWindowAgg.c`
- `postgres/src/include/nodes/execnodes.h`

## Definition of Done

### Stage A Gate (Initial Release)

1. `OVER` syntax parses and analyzes for supported partition/order forms.
2. `ROW_NUMBER` executes correctly across partitioned and non-partitioned inputs.
3. `RANK` executes correctly with deterministic tie behavior in supported ordering cases.
4. Planner/executor pipeline includes a stable window stage for supported function paths.
5. `EXPLAIN` output includes clear window-stage visibility for supported queries.
6. Required design docs `0020-0001` through `0020-0003` are merged with status `accepted`.

### Stage B Gate (Milestone Accepted)

7. `LAG` and `LEAD` execute with optional offset/default in the supported subset.
8. Out-of-range offset behavior matches documented PostgreSQL-compatible semantics in this milestone scope.
9. Unsupported argument/frame combinations fail deterministically with stable SQLSTATE diagnostics.
10. `EXPLAIN ANALYZE` includes runtime metrics for supported window execution nodes.
11. Regression and compatibility suites for supported window functions are green.
12. Required design doc `0020-0004` is merged with status `accepted`.
