# Milestone 0015 — PL/pgSQL Stored Routines (Function-First Delivery)

**Status:** planned
**Depends on:** Milestone 0001 (core parser/planner/executor pipeline), Milestone 0003 (broader SQL expression coverage), Milestone 0006 (statistics and planner maturity), Milestone 0012 (lock and concurrency semantics used by routine-internal SQL).
**Drives:** Practical server-side routine support for production workloads, delivered in two stages to shorten initial release time: Function-first, Procedure-second.

## Context

goopg currently supports expression-level built-in functions through a fixed in-tree registry, but does not support user-defined routines, `CREATE FUNCTION`, `CREATE PROCEDURE`, `CALL`, or PL/pgSQL execution.

This milestone introduces PL/pgSQL as a first-class routine language with explicit staged delivery:

- Stage A (initial release): Functions first.
- Stage B (follow-up within the same milestone): Procedures and `CALL` semantics.

The staging objective is to deliver useful server-side programmability earlier, while keeping Procedure-specific semantics isolated so they do not block Function availability.

This milestone intentionally expands scope relative to the foundational requirements document, where PL/* languages were deferred.

## Delivery Strategy

### Stage A — Function-First (Initial Release)

Deliver a production-usable subset centered on `CREATE OR REPLACE FUNCTION ... LANGUAGE plpgsql`, callable from SQL expressions and `SELECT` targets.

Primary value:

- Move deterministic business logic close to data.
- Reduce client round trips for read/compute-heavy paths.
- Provide a stable, testable routine surface before Procedure semantics land.

### Stage B — Procedure Follow-Up

Add `CREATE PROCEDURE` and `CALL` on top of the Stage A runtime and catalog substrate, reusing parser/runtime/error infrastructure wherever possible.

Primary value:

- Explicit imperative entry points for operational tasks and orchestration.
- Full routine family coverage expected by PostgreSQL-oriented tooling and users.

## In Scope

### SQL Parser and AST (Independent Implementation)

- Parse and represent:
  - `CREATE OR REPLACE FUNCTION`
  - `DROP FUNCTION`
  - `CREATE PROCEDURE`
  - `DROP PROCEDURE`
  - `CALL`
  - `LANGUAGE plpgsql`
  - argument modes and defaults needed by the supported subset.
- Keep parser implementation in-tree and independent (no parser replacement dependency).
- Maintain parser error quality (byte position and stable SQLSTATE mapping).

### PL/pgSQL Parser and Runtime Core

- Implement an in-tree PL/pgSQL parser and AST for routine bodies.
- Stage A language subset:
  - `DECLARE`
  - assignment (`:=`)
  - `IF` / `ELSIF` / `ELSE`
  - `LOOP`, `WHILE`, integer `FOR`
  - `EXIT` / `CONTINUE`
  - `RETURN`
  - `PERFORM`
  - `SELECT ... INTO`
  - embedded `INSERT` / `UPDATE` / `DELETE` / `SELECT`.
- Stage B extends execution entry points to Procedure calls, while sharing the same statement engine.

### Catalog, Metadata, and Name Resolution

- Add routine metadata storage for function/procedure identity, argument signatures, return shape, language, and body source.
- Expose minimal catalog visibility required for practical inspection and debugging.
- Implement deterministic routine lookup and resolution for the supported overload rules.

### SQL Execution Bridge (SPI-Like Substrate)

- Provide a routine-internal SQL execution bridge that can plan and execute embedded SQL using current session/transaction context.
- Ensure parameter binding and datum conversion are deterministic and typed for the supported subset.
- Preserve lock/MVCC correctness for statements executed inside routines.

### Error Model and Diagnostics

- Map routine/runtime failures to stable SQLSTATE classes and include byte-position/context where available.
- Implement Stage A exception handling subset:
  - `EXCEPTION` blocks with practical, production-relevant matching.
- Ensure Stage B `CALL` errors follow the same structured diagnostics path.

### Wire-Protocol and Planner/Executor Integration

- Stage A:
  - function invocation from expression contexts and `SELECT` targets.
- Stage B:
  - `CALL` statement planning/execution path.
- Keep simple and extended query protocol behavior consistent.

### Testing and Operability

- Add unit, integration, and concurrency regression coverage for parser/runtime/catalog/execution seams.
- Add workload-oriented tests demonstrating practical routine usage.
- Add observability hooks for routine execution counts, failures, and latency buckets.

## Out of Scope

- Trigger functions and trigger manager integration.
- Event triggers.
- `DO $$ ... $$ LANGUAGE plpgsql` anonymous blocks.
- Full PostgreSQL overloading and polymorphic type behavior.
- Advanced features such as `SECURITY DEFINER`, cost/rows planner hints, leakproof metadata, and dependency-tracking parity.
- Non-PL/pgSQL routine languages.
- Extension-based routine language loading.

## Required Design Docs

Place under `docs/design` with sequential numbering at creation time:

- `0015-0001-routine-sql-parser-and-ast.md`
- `0015-0002-plpgsql-parser-and-runtime-core.md`
- `0015-0003-routine-catalog-and-resolution.md`
- `0015-0004-sql-execution-bridge-and-error-model.md`
- `0015-0005-procedure-call-semantics-and-rollout.md`

These design docs should cross-link to:

- `docs/design/root-0010-parser.md`
- `docs/design/root-0011-planner.md`
- `docs/design/root-0012-executor.md`
- `docs/design/root-0008-wal-and-recovery.md`
- `docs/design/0012-0003-lock-wait-integration-and-test-matrix.md`

## Reference

Upstream sources to consult:

- `postgres/src/backend/parser/gram.y`
- `postgres/src/backend/parser/scan.l`
- `postgres/src/pl/plpgsql/src/pl_gram.y`
- `postgres/src/pl/plpgsql/src/pl_exec.c`
- `postgres/src/pl/plpgsql/src/pl_comp.c`
- `postgres/src/include/catalog/pg_proc.dat`
- `postgres/src/include/catalog/pg_language.dat`

## Definition of Done

### Stage A Gate (Initial Release)

1. `CREATE OR REPLACE FUNCTION`, `DROP FUNCTION`, and `LANGUAGE plpgsql` parse and validate under the supported subset.
2. PL/pgSQL function bodies execute with the Stage A statement/control-flow subset.
3. Function calls are available from expression contexts used by practical application queries.
4. Routine-internal SQL execution works for supported `SELECT`/DML forms with correct MVCC and lock behavior.
5. Exception handling subset is implemented with stable SQLSTATE mapping and routine-context diagnostics.
6. Catalog metadata for functions is persistent, queryable, and consistent after restart.
7. Regression suite covers parser, runtime, catalog resolution, and concurrency-sensitive invocation paths.
8. Required design docs `0015-0001` through `0015-0004` are merged with status `accepted`.

### Stage B Gate (Milestone Accepted)

9. `CREATE PROCEDURE`, `DROP PROCEDURE`, and `CALL` parse and execute under the defined subset.
10. Procedure argument modes supported by this milestone execute correctly and deterministically.
11. Procedure execution reuses Stage A runtime substrate without semantic regressions in existing function behavior.
12. `CALL` behaves consistently across simple and extended query protocol paths.
13. Procedure-related diagnostics and SQLSTATE mapping are aligned with Stage A error-model conventions.
14. Required design doc `0015-0005` is merged with status `accepted`.
15. End-to-end regression and workload tests demonstrate that Stage A and Stage B can coexist without planner/executor or durability regressions.
