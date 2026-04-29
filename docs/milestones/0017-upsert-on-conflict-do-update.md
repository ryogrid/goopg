# Milestone 0017 — UPSERT Support (INSERT ... ON CONFLICT DO UPDATE)

**Status:** planned
**Depends on:** Milestone 0001 (core parser/planner/executor pipeline), Milestone 0002 (durability and concurrent storage), Milestone 0006 (planner/statistics maturity), Milestone 0012 (lock manager and deadlock detection).
**Drives:** PostgreSQL-compatible UPSERT support for application write paths that require idempotent insert-or-update behavior under contention.

## Context

goopg currently supports `INSERT`, `UPDATE`, and `DELETE`, but does not support `INSERT ... ON CONFLICT ...`.

This is a practical compatibility gap for modern PostgreSQL clients and ORMs, where UPSERT is a default write pattern for:

- idempotent event ingestion,
- cache/materialized state maintenance,
- conflict-safe retry logic under concurrent workers.

This milestone introduces `INSERT ... ON CONFLICT DO UPDATE` as the primary contract, with deterministic behavior under concurrent writes and clear SQLSTATE diagnostics.

## In Scope

### SQL Parser and AST

- Parse and represent:
  - `INSERT ... ON CONFLICT (index_cols...) DO UPDATE SET ...`
  - `INSERT ... ON CONFLICT ON CONSTRAINT constraint_name DO UPDATE SET ...`
  - `DO UPDATE ... WHERE ...` predicate in the supported subset.
- Parse and represent `excluded` references required by `DO UPDATE` expressions.
- Provide stable parser errors and byte-position diagnostics for invalid ON CONFLICT syntax.

### Analyzer and Name Resolution

- Validate conflict target shape against table metadata and unique/exclusion constraints supported by goopg.
- Validate `excluded` column references and target-table column references in `SET` and `WHERE` clauses.
- Reject unsupported conflict-target forms with deterministic SQLSTATE errors.

### Planner Integration

- Add plan nodes/fields required to express conflict target selection and `DO UPDATE` actions.
- Resolve conflict arbiter (index/constraint) deterministically.
- Preserve deterministic target-row identity and assignment semantics for the supported subset.

### Executor and Concurrency Semantics

- Execute insert path first, then conflict resolution path under correct lock ordering.
- On conflict, run `DO UPDATE` with access to:
  - current stored row,
  - incoming row,
  - `excluded` references.
- Ensure conflict resolution is correct under concurrent writers and does not regress durability semantics.
- Integrate with lock manager/deadlock surfaces so contention behavior remains diagnosable.

### Error Model and Diagnostics

- Preserve expected SQLSTATE behavior for:
  - unsupported feature shape,
  - missing/invalid conflict target,
  - ambiguity and invalid column references,
  - deadlock/serialization-like conflict outcomes in supported paths.
- Ensure diagnostics include routine planner/executor context fields used elsewhere in goopg.

### Testing and Operability

- Add parser/analyzer/planner/executor regression tests for ON CONFLICT DO UPDATE.
- Add concurrency stress tests (multi-session conflicting inserts) that validate no lost updates in the supported subset.
- Add compatibility tests for representative PostgreSQL UPSERT patterns used by common client libraries.
- Add observability counters for conflict hits and conflict-update executions.

## Out of Scope

- Full PostgreSQL ON CONFLICT feature parity in one pass.
- Exclusion-constraint conflict arbitration if not already supported by goopg indexing paths.
- Partitioned-table specific ON CONFLICT semantics.
- `RETURNING` parity for every unsupported corner case.
- MERGE statement support.

## Required Design Docs

Place under `docs/design` with sequential numbering at creation time:

- `0017-0001-on-conflict-parser-ast-and-analysis.md`
- `0017-0002-upsert-planner-and-arbiter-selection.md`
- `0017-0003-upsert-executor-concurrency-and-locking.md`
- `0017-0004-upsert-observability-and-compat-tests.md`

These design docs should cross-link to:

- `docs/design/root-0010-parser.md`
- `docs/design/root-0011-planner.md`
- `docs/design/root-0012-executor.md`
- `docs/design/0012-0001-lock-manager-architecture.md`
- `docs/design/0012-0003-lock-wait-integration-and-test-matrix.md`

## Reference

Upstream sources to consult:

- `postgres/src/backend/parser/gram.y`
- `postgres/src/backend/parser/parse_clause.c`
- `postgres/src/backend/parser/parse_target.c`
- `postgres/src/backend/optimizer/plan/createplan.c`
- `postgres/src/backend/executor/nodeModifyTable.c`
- `postgres/src/backend/executor/execIndexing.c`

## Definition of Done

1. `INSERT ... ON CONFLICT ... DO UPDATE` parses and analyzes for supported conflict-target forms.
2. Conflict target resolution by column-list and by constraint name works deterministically for supported unique constraints/indexes.
3. `excluded` references in `SET` and `WHERE` clauses behave correctly for the supported subset.
4. Under concurrent conflicting inserts, UPSERT produces stable, correct final rows without lost updates in supported scenarios.
5. Locking/deadlock interactions on UPSERT paths are surfaced through existing lock/SQLSTATE error channels.
6. Existing INSERT/UPDATE/DELETE behavior remains unchanged for non-UPSERT statements.
7. Regression and stress tests for parser/analyzer/planner/executor/concurrency paths are merged and green.
8. Observability surfaces expose conflict-hit and conflict-update execution counts.
9. Required design docs `0017-0001` through `0017-0004` are merged with status `accepted`.
