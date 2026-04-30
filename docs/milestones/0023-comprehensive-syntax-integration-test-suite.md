# Milestone 0023 - Comprehensive Syntax Integration Test Suite

**Status:** accepted
**Depends on:** All preceding milestones (M0007-M0022) — every syntax feature shipped by those milestones must have at least one end-to-end test in this suite.
**Drives:** Go-portable TAP-style integration tests that exercise every implemented SQL statement family through both the Go-native wire protocol (`database/sql` via lib/pq) and the `psql` CLI. This suite is the goopg equivalent of PostgreSQL's `src/bin/psql/t/` and `src/test/regress/` TAP tests.

## Context

goopg currently has integration tests in `internal/testport/` (ported upstream TAP tests) and `internal/testport/plpgsql_test.go` (PL/pgSQL-specific tests). These cover a subset of the shipped syntax but do not systematically verify every statement family that each milestone delivered.

The gap creates a risk that:
- a refactoring or new feature breaks an older feature without CI noticing,
- edge cases in statement parsing, planning, or execution go undetected,
- the PostgreSQL-compatibility surface regresses between milestone deliveries.

This milestone introduces a structured, extensible integration test suite organised by SQL statement family. Tests run through both the Go-native wire protocol (`Cluster.Query`) and, when `psql` is available, through the `psql` CLI (`Cluster.PSQL`). Each test verifies correct execution, expected error codes (SQLSTATE), error messages, and result shapes.

## In Scope

### Test Organisation

- A new test file per statement family under `internal/testport/`:
  - `ddl_test.go` — CREATE/DROP TABLE, INDEX, VIEW, FUNCTION, PROCEDURE
  - `dml_test.go` — INSERT, UPDATE, DELETE, TRUNCATE, COPY
  - `select_test.go` — SELECT, WHERE, FROM, JOIN, GROUP BY, HAVING, ORDER BY, LIMIT, OFFSET, DISTINCT, subqueries, derived tables, CTEs (non-recursive), UNION ALL
  - `recursive_cte_test.go` — WITH RECURSIVE fixpoint behaviour
  - `plpgsql_test.go` — PL/pgSQL functions and procedures (already exists, extend coverage)
  - `explain_test.go` — EXPLAIN, EXPLAIN ANALYZE
  - `upsert_test.go` — INSERT ... ON CONFLICT DO UPDATE / NOTHING
  - `window_test.go` — Window functions (ROW_NUMBER, RANK, OVER, PARTITION BY, ORDER BY)
  - `locking_test.go` — SELECT ... FOR UPDATE / FOR SHARE, NOWAIT, SKIP LOCKED
  - `transaction_test.go` — BEGIN, COMMIT, ROLLBACK, SAVEPOINT
  - `catalog_test.go` — pg_catalog virtual views (pg_class, pg_proc, pg_stat_activity, etc.)
  - `expression_test.go` — CASE, COALESCE, NULLIF, CAST, type expressions, operator expressions
  - `error_test.go` — SQLSTATE error code verification for common error conditions

- Each test follows PostgreSQL TAP conventions:
  - Starts a dedicated `cluster.Cluster` (or reuses one per subtest group).
  - Runs SQL through `Cluster.Query` (Go-native wire protocol).
  - When `psql` is available, also runs through `Cluster.PSQL` as a cross-check.
  - Asserts result shapes (row count, column count, cell values).
  - Asserts error codes via SQLSTATE matching.
  - Uses `t.Run` subtests for logical grouping.
  - Clean shutdown via `defer c.Stop(cluster.ShutdownImmediate)`.

### Coverage Requirements Per Statement Family

Each statement family test must cover at least:

| Statement | Happy path | Edge cases | Error cases |
|-----------|------------|------------|-------------|
| CREATE TABLE | basic table, WITH OIDS, TEMPORARY, IF NOT EXISTS, column constraints, DEFAULT | duplicate column, reserved-name table | already exists (42P07) |
| DROP TABLE | basic drop, IF EXISTS, CASCADE | — | not found (42P01) |
| CREATE INDEX | basic index, UNIQUE, IF NOT EXISTS | — | already exists (42P07) |
| DROP INDEX | basic drop, IF EXISTS | — | not found (42P01) |
| CREATE VIEW | basic view, OR REPLACE | — | — |
| INSERT | basic insert, multiple values, DEFAULT, RETURNING | — | not-null violation, type mismatch |
| UPDATE | basic update, WHERE, RETURNING | zero-row update | — |
| DELETE | basic delete, WHERE, RETURNING | zero-row delete | — |
| SELECT | *, column list, WHERE, AND/OR/NOT, IN, BETWEEN, LIKE, IS NULL, AS alias | empty result, all-null result | ambiguous column (42702) |
| SELECT JOIN | INNER, LEFT, RIGHT, CROSS, NATURAL, multiple JOINs, USING, ON | cartesian product | ambiguous column |
| SELECT aggregate | COUNT, SUM, AVG, MIN, MAX, GROUP BY, HAVING | empty-group, ALL | not-in-group-by (42803) |
| SELECT subquery | scalar subquery, EXISTS, IN (subquery), derived table (FROM subquery) | correlated subquery | more-than-one-row |
| SELECT set op | UNION ALL (2-way, 3-way, mixed types) | empty branches | incompatible types |
| WITH (CTE) | simple CTE, multiple CTEs, CTE shadowing base table | CTE with no consumers | duplicate name (42710) |
| WITH RECURSIVE | basic fixpoint (1,2,3), multi-iteration | single-iteration, empty anchor | non-UNION-ALL (0A000) |
| CASE | simple CASE, searched CASE, ELSE, NULL WHEN | all-false (ELSE NULL) | — |
| Function UDF | CREATE FUNCTION, SELECT udf(...), nested UDF calls | void return, text parameter | not found (42883), ambiguous (42725) |
| Procedure UDF | CREATE PROCEDURE, CALL, IN arguments | zero-arg CALL | not found (42883) |
| Procedure OUT params | IN/OUT/INOUT parameters, CALL with OUT returns | multiple OUT params | — |
| DROP FUNCTION / PROCEDURE | basic drop, IF EXISTS, arg-based overload resolution | — | not found (42883), ambiguous (42725) |
| EXPLAIN | EXPLAIN SELECT, EXPLAIN INSERT, EXPLAIN ANALYZE | — | — |
| UPSERT | ON CONFLICT DO NOTHING, ON CONFLICT DO UPDATE, arbiter index | conflict on multiple rows | no arbiter (42P10) |
| Window functions | ROW_NUMBER, RANK, OVER (PARTITION BY, ORDER BY) | empty window, all-null partition | — |
| Row locking | FOR UPDATE, FOR SHARE, NOWAIT, SKIP LOCKED | — | — |
| pg_catalog views | pg_class, pg_proc, pg_stat_activity | zero rows | — |

### Error Code Verification

For every error case, the test must:
1. Execute the SQL and assert the operation fails.
2. Extract the SQLSTATE code from the error message or error detail.
3. Assert the SQLSTATE matches the expected upstream PostgreSQL code.
4. Log the actual error message for manual inspection.

### psql Cross-Check

When `psql` is available on the system PATH or at the in-tree path (`postgres/local_install/bin/psql`), each statement family test should also run a representative query through `Cluster.PSQL` to verify the psql CLI path. The psql cross-check uses the same cluster instance.

### Test Isolation

- Each `t.Run` subtest creates its own `cluster.Cluster` via `newCluster` so tests are fully isolated.
- Clusters use `t.TempDir()` for automatic cleanup.
- No shared mutable state between subtests.
- Parallel subtests (`t.Parallel()`) are allowed for independent statement families.

### Non-psql Fallback

All tests must work WITHOUT `psql`. The Go-native `Cluster.Query` path is the primary test mechanism. psql cross-checks are gated by `psqlPath(t)` and skipped when psql is not found.

## Out of Scope

- Full SQL-parser-level negative testing (syntax error coverage).
- Performance/benchmark tests (those belong in `internal/testutil/tpch/`).
- Cross-version compatibility with upstream PostgreSQL.
- Randomized or fuzz testing.
- Distributed/concurrent test scenarios beyond basic row-locking.

## Required Design Docs

None — this milestone codifies existing test patterns (from `internal/testport/`) into an organised suite rather than introducing new infrastructure.

## Reference

- PostgreSQL TAP tests: `postgres/src/bin/psql/t/`, `postgres/src/test/regress/`
- Existing goopg test patterns: `internal/testport/tap_port_test.go`, `internal/testport/plpgsql_test.go`
- Test harness cluster package: `internal/testutil/cluster/cluster.go`
- Test utility library: `internal/testutil/util/util.go`

## Definition of Done

1. A test file exists for each statement family listed under "Test Organisation".
2. Every statement family listed under "Coverage Requirements" has at least one happy-path and one error-case test.
3. All tests pass with `go test ./internal/testport/...`.
4. All tests pass when `psql` is available (the psql cross-check paths).
5. The full repository test suite (`go test ./...`) remains green.
6. The fix_plan.md is updated to reflect this milestone's delivery.
