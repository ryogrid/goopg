# Milestone 0060 — PostgreSQL Oracle Test-Port Foundation

**Status:** completed
**Depends on:** Milestone 0004 (TAP test port), Milestone 0023 (syntax integration suite)
**Drives:** Continuous PostgreSQL-oracle compatibility validation through migrated upstream test suites.

## Context

goopg already ports a subset of upstream TAP tests and has broad
in-tree unit/integration coverage, but there is no unified foundation
that treats PostgreSQL's upstream test corpus as the long-term oracle.

M0060 defines that foundation by making upstream test migration
first-class across TAP, pg_regress, isolation, recovery/subscription,
and client-tool-oriented suites.

This milestone also standardizes explicit visibility for tests that are
temporarily non-passing and tests that are architecture-excluded.

## In-Scope Upstream Test Sources (Migration Target Code List)

The following upstream code directories are migration targets under
M0060:

- `postgres/src/test/regress`
- `postgres/src/test/isolation`
- `postgres/src/test/recovery`
- `postgres/src/test/subscription`
- `postgres/src/bin/*/t` (client-tool TAP tests)
- `postgres/src/test/modules/*`
- `postgres/contrib/*/{sql,expected,t}`

## Scope Notes

- Client-tool-oriented tests are IN scope. goopg should maintain
  compatibility with PostgreSQL client behavior as far as practical.
- Existing generated TAP coverage classifications that label some
  client-tool suites as skip are treated as legacy state and will be
  reclassified under this milestone.

## Exclusion Policy

Tests may be excluded only when one of the following is true and the
reason is explicitly recorded:

1. The test depends on PostgreSQL's process model or runtime internals
   that are intentionally not implemented in goopg (for example,
   postmaster/fork-per-backend specific semantics).
2. The test targets C-extension ABI surfaces or infrastructure that is
   intentionally outside goopg scope.
3. The test verifies behavior tied to implementation details that are
   fundamentally incompatible with goopg's Go runtime model (for
   example, assumptions invalidated by goroutine scheduling/GC model).

Every exclusion must be listed in:

- `docs/test-port/postgres-oracle-port-status.md`

## Non-Passing-but-Allowed Visibility

Tests that are migration targets but not yet required to pass must be
listed in:

- `docs/test-port/postgres-oracle-port-status.md`

This file is the auditable source for:

- `status = defer` (in scope, not yet pass-required), and
- `status = excluded` (out of scope by policy),

including rationale and follow-up milestone references.

## Required Design Docs

- `docs/design/0060-0001-postgres-test-porting-strategy.md`
- `docs/design/0060-0002-postgres-oracle-port-framework.md`

## Definition of Done

- [x] Migration strategy by test type is accepted and tracked in design docs.
- [x] Upstream migration target code list is documented and maintained.
- [x] Explicit defer/excluded tracking file exists and is reviewable.
- [x] Client-tool test migration path is implemented (not treated as skip-only).
- [x] Initial oracle report can distinguish pass/defer/excluded by suite.
- [x] `go test ./...` remains green for in-tree tests.

## Landed Artifacts

- `docs/test-port/postgres-oracle-target-inventory.csv`
- `docs/test-port/postgres-oracle-target-inventory.md`
- `docs/test-port/postgres-oracle-port-status.csv`
- `docs/test-port/postgres-oracle-port-status.md`
- `docs/test-port/upstream-regress-coverage.md`
- `docs/test-port/upstream-isolation-coverage.md`
- `docs/test-port/upstream-tap-coverage.md`
- `analysis/postgres-oracle-compatibility-report.md`
- `cmd/gen-oracle-inventory/`
- `cmd/gen-regress-coverage/`
- `cmd/gen-isolation-coverage/`
- `cmd/gen-oracle-port-status/`
- `cmd/gen-oracle-report/`
- `internal/testport/framework/`
- `internal/testport/scripts_port_test.go` — 13 tests porting `postgres/src/bin/scripts/t/*.pl`
  - P-011 (`080_pg_isready.pl`): fully ported, passes
  - 12 others (clusterdb, createdb, createuser, dropdb, dropuser, reindexdb, vacuumdb, connstr):
    skeleton + `t.Skip` with specific blockers (see D-005a–D-005l in port-status CSV)

## scripts/t Skip Rationale

| Test file | Skip reason |
|-----------|-------------|
| 100_vacuumdb.pl, 102_vacuumdb_stages.pl | `VACUUM (opts)` parenthesized syntax not in goopg parser; also needs `pg_catalog.pg_namespace` |
| 101_vacuumdb_all.pl | Same + multi-database iteration via `pg_database` |
| 020_createdb.pl | `CREATE DATABASE` not in parser/executor |
| 050_dropdb.pl | `DROP DATABASE` not in parser/executor; depends on CREATE DATABASE |
| 040_createuser.pl | `CREATE ROLE/USER` not in parser/executor |
| 070_dropuser.pl | `DROP ROLE/USER` not in parser/executor |
| 090_reindexdb.pl | `REINDEX DATABASE/TABLE` not in parser/executor |
| 091_reindexdb_all.pl | REINDEX + multi-database |
| 010_clusterdb.pl | `CLUSTER` not in parser/executor |
| 011_clusterdb_all.pl | CLUSTER + multi-database |
| 200_connstr.pl | CREATE DATABASE + LATIN1 encoding (goopg is UTF8-only) |

## Out of Scope

- One-shot full pass of every upstream test in a single loop.
- Emulating PostgreSQL internal implementation details that are
  intentionally not part of goopg's architecture.