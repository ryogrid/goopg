# Milestone 0018 — EXPLAIN / EXPLAIN ANALYZE Support

**Status:** planned
**Depends on:** Milestone 0001 (core parser/planner/executor path), Milestone 0003 (planner and TPC-H query coverage), Milestone 0006 (statistics-driven plan choices), Milestone 0016 (WITH/CTE planning paths for realistic explain output).
**Drives:** Operator-grade plan introspection and runtime execution diagnostics compatible with PostgreSQL-style `EXPLAIN` and `EXPLAIN ANALYZE` workflows.

## Context

goopg already has a basic `EXPLAIN` statement path, but practical PostgreSQL operations require richer plan visibility and execution-time instrumentation. Without `EXPLAIN ANALYZE`, it is difficult to diagnose cardinality errors, join mis-selection, or unexpected executor hot spots.

This milestone adds production-usable plan and runtime diagnostics in two stages:

- Stage A: expanded `EXPLAIN` options and stable static plan rendering.
- Stage B: `EXPLAIN ANALYZE` with runtime instrumentation.

The goal is parity at the operator workflow level: users can inspect planned shape, then validate real execution behavior on the same query.

## In Scope

### SQL Parser and AST

- Parse and represent PostgreSQL-style option syntax for explain statements in the supported subset:
  - `EXPLAIN`
  - `EXPLAIN ANALYZE`
  - `VERBOSE`
  - `COSTS`
  - `BUFFERS`
  - `SETTINGS`
  - `TIMING`
  - `SUMMARY`
  - `FORMAT TEXT` and `FORMAT JSON`.
- Support both keyword and parenthesized option forms in the scoped subset.
- Return stable parser diagnostics for invalid option combinations.

### Plan Rendering (Stage A)

- Produce deterministic static plan trees for supported plan nodes, including new nodes added by active milestones.
- Improve node-level attributes shown in output (relation/index names, filter predicates, join conditions, key ordering, estimated rows/cost where available).
- Ensure output remains stable enough for regression tests and operator diff workflows.

### Runtime Instrumentation (Stage B)

- Add executor instrumentation hooks required by `EXPLAIN ANALYZE`:
  - per-node timing
  - rows produced / loops
  - optional timing suppression behavior (`TIMING OFF` in supported subset)
  - summary timing and row accounting.
- Ensure instrumentation overhead is bounded and disabled outside ANALYZE paths.

### Option Semantics

- Implement deterministic behavior for supported options:
  - `ANALYZE` executes the statement and reports runtime stats.
  - `COSTS`, `TIMING`, `SUMMARY` control output sections.
  - `BUFFERS` reports available buffer-level counters in the supported subset.
  - `FORMAT JSON` returns machine-readable structured explain output in a stable schema subset.
- Reject unsupported options or unsupported option combinations with stable SQLSTATE errors.

### Planner/Executor Integration

- Ensure explain paths include realistic plans for supported query families:
  - joins/aggregates
  - subqueries and derived tables
  - CTE paths (as available in milestone dependency order).
- Ensure `EXPLAIN ANALYZE` behaves correctly with write statements in the supported subset, including row counts and side-effect semantics aligned to documented behavior.

### Testing and Operability

- Add parser/analyzer/planner/executor tests for EXPLAIN option parsing and output shaping.
- Add execution tests that validate `EXPLAIN ANALYZE` runtime counters against known query behavior.
- Add JSON-format snapshot tests for stable tooling integration.
- Add regression tests proving non-EXPLAIN query behavior is unchanged when instrumentation is disabled.

## Out of Scope

- Full byte-for-byte textual parity with every PostgreSQL version.
- Every PostgreSQL EXPLAIN option in one pass (only the listed subset is required in this milestone).
- Full `WAL`, `JIT`, and parallel-worker explain fields where corresponding subsystems are not yet implemented in goopg.
- Auto-explain background logging features.
- External visualizer integration.

## Required Design Docs

Place under `docs/design` with sequential numbering at creation time:

- `0018-0001-explain-parser-options-and-ast.md`
- `0018-0002-static-plan-rendering-and-output-contract.md`
- `0018-0003-explain-analyze-instrumentation.md`
- `0018-0004-json-format-and-regression-strategy.md`

These design docs should cross-link to:

- `docs/design/root-0010-parser.md`
- `docs/design/root-0011-planner.md`
- `docs/design/root-0012-executor.md`
- `docs/design/0003-0007-explain.md`
- `docs/design/0016-0002-nonrecursive-cte-planner-executor.md` (when present)

## Reference

Upstream sources to consult:

- `postgres/src/backend/parser/gram.y`
- `postgres/src/backend/commands/explain.c`
- `postgres/src/include/commands/explain.h`
- `postgres/src/backend/executor/instrument.c`
- `postgres/src/include/executor/instrument.h`
- `postgres/src/backend/utils/adt/ruleutils.c`

## Definition of Done

### Stage A Gate (Initial Release)

1. EXPLAIN option syntax in the scoped subset parses and validates correctly.
2. Static explain output includes deterministic plan structure and key node attributes for supported node types.
3. `FORMAT TEXT` and `FORMAT JSON` are both available for non-ANALYZE explain paths.
4. Unsupported explain options fail with deterministic SQLSTATE diagnostics.
5. Required design docs `0018-0001` and `0018-0002` are merged with status `accepted`.

### Stage B Gate (Milestone Accepted)

6. `EXPLAIN ANALYZE` executes supported queries and reports per-node runtime stats (rows, loops, timing) in the scoped subset.
7. `TIMING`, `SUMMARY`, and `COSTS` options influence output as documented in this milestone.
8. `BUFFERS` reports available buffer counters in supported paths and fails deterministically when not available.
9. JSON explain output for ANALYZE paths is stable and covered by regression tests.
10. Non-EXPLAIN query execution overhead remains unchanged when instrumentation is not requested.
11. Required design docs `0018-0003` and `0018-0004` are merged with status `accepted`.
12. End-to-end regression suites remain green with EXPLAIN and EXPLAIN ANALYZE enabled.
