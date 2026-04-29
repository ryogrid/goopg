# Milestone 0021 - Pessimistic Row Locking Support (SELECT ... FOR UPDATE)

**Status:** planned
**Depends on:** Milestone 0001 (core parser/planner/executor path), Milestone 0012 (lock manager and deadlock detection baseline), Milestone 0020 (window/query pipeline maturity for broader SELECT support).
**Drives:** PostgreSQL-compatible pessimistic row-lock behavior for `SELECT ... FOR UPDATE`, enabling safe read-modify-write workflows under contention.

## Context

goopg currently supports relation-level lock integration and basic concurrency controls, but does not support tuple-level pessimistic locking through `SELECT ... FOR UPDATE`. This prevents a common correctness pattern used by applications that must serialize updates around selected rows.

This milestone introduces `SELECT ... FOR UPDATE` semantics in staged form:

- Stage A: base `FOR UPDATE` row-lock behavior with blocking waits.
- Stage B: wait-policy controls (`NOWAIT`, `SKIP LOCKED`) in the supported subset.

The goal is to provide deterministic row-level locking semantics that compose correctly with existing transactions, lock manager behavior, and deadlock diagnostics.

## In Scope

### SQL Parser and AST

- Parse and represent `SELECT ... FOR UPDATE` in supported query forms.
- Parse and represent wait-policy modifiers in the supported subset:
  - `NOWAIT`
  - `SKIP LOCKED`.
- Parse and represent target relation qualifiers (`FOR UPDATE OF ...`) in the scoped subset.
- Return stable parser diagnostics for invalid locking-clause combinations.

### Analyzer and Name Resolution

- Validate legal placement of `FOR UPDATE` in supported query forms.
- Validate relation aliases in `FOR UPDATE OF ...` where supported.
- Reject unsupported locking-clause combinations with deterministic SQLSTATE errors.

### Planner Integration

- Add planning metadata that marks locking clauses and target relations.
- Ensure lock intents survive rewrites/projections and reach executor lock points.
- Keep lock-clause handling deterministic with joins, filters, and supported subquery forms.

### Executor and Locking Semantics

- Stage A:
  - acquire row-level lock intents for selected tuples before returning rows.
  - block on conflicting row locks according to default wait behavior.
- Stage B:
  - implement `NOWAIT` failure behavior with deterministic SQLSTATE mapping.
  - implement `SKIP LOCKED` behavior in the supported subset.
- Ensure lock release follows transaction boundaries and integrates with existing lock-manager cleanup paths.

### Deadlock, Timeout, and Error Behavior

- Integrate row-lock waits with existing deadlock detection/reporting paths.
- Ensure lock wait cancellation and timeout behavior use existing SQLSTATE conventions where applicable.
- Surface clear diagnostics for lock-conflict outcomes.

### Testing and Operability

- Add parser/analyzer/planner/executor regression tests for locking-clause handling.
- Add multi-session integration tests for blocking, wakeup ordering, and lock release on commit/rollback.
- Add `NOWAIT` and `SKIP LOCKED` behavior tests for supported query shapes.
- Add observability counters/log events for row-lock waits, skips, and nowait failures.

## Out of Scope

- Full PostgreSQL row-lock mode matrix in one pass (`FOR NO KEY UPDATE`, `FOR SHARE`, `FOR KEY SHARE`) unless explicitly included in follow-up design docs.
- Every locking-clause permutation across all query forms.
- Full cross-partition row-lock semantics for partitioned tables where partitioning support is not yet complete.
- Serializable isolation redesign beyond current lock/transaction model.

## Required Design Docs

Place under docs/design with sequential numbering at creation time:

- `0021-0001-for-update-parser-analysis-and-ast.md`
- `0021-0002-row-lock-planner-executor-integration.md`
- `0021-0003-wait-policy-nowait-skip-locked.md`
- `0021-0004-deadlock-observability-and-test-matrix.md`

These design docs should cross-link to:

- `docs/design/root-0010-parser.md`
- `docs/design/root-0011-planner.md`
- `docs/design/root-0012-executor.md`
- `docs/design/0012-0001-lock-manager-architecture.md`
- `docs/design/0012-0002-deadlock-detection-algorithm.md`
- `docs/design/0012-0003-lock-wait-integration-and-test-matrix.md`

## Reference

Upstream sources to consult:

- `postgres/src/backend/parser/gram.y`
- `postgres/src/backend/parser/analyze.c`
- `postgres/src/backend/executor/nodeLockRows.c`
- `postgres/src/backend/access/heap/heapam.c`
- `postgres/src/backend/storage/lmgr/lock.c`
- `postgres/src/backend/storage/lmgr/deadlock.c`

## Definition of Done

### Stage A Gate (Initial Release)

1. `SELECT ... FOR UPDATE` parses and analyzes for supported query forms.
2. Planner carries lock-clause metadata into execution consistently.
3. Executor acquires row-level pessimistic locks before returning selected rows in supported paths.
4. Conflicting `FOR UPDATE` operations block and resume correctly with commit/rollback.
5. Lock ownership and release follow transaction boundaries with no leaked locks.
6. Required design docs `0021-0001` and `0021-0002` are merged with status `accepted`.

### Stage B Gate (Milestone Accepted)

7. `NOWAIT` returns deterministic lock-conflict errors in supported query shapes.
8. `SKIP LOCKED` skips locked tuples deterministically in supported query shapes.
9. Row-lock waits participate in deadlock detection and report stable SQLSTATE diagnostics.
10. Observability surfaces expose row-lock wait/skip/failure counters or events for supported paths.
11. Regression and multi-session integration suites for `FOR UPDATE` paths are green.
12. Required design docs `0021-0003` and `0021-0004` are merged with status `accepted`.
