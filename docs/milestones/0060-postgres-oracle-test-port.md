# Milestone 0060 — PostgreSQL Oracle Test-Port Foundation

**Status:** planned
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

## Definition of Done

- [ ] Migration strategy by test type is accepted and tracked in design docs.
- [ ] Upstream migration target code list is documented and maintained.
- [ ] Explicit defer/excluded tracking file exists and is reviewable.
- [ ] Client-tool test migration path is implemented (not treated as skip-only).
- [ ] Initial oracle report can distinguish pass/defer/excluded by suite.
- [ ] `go test ./...` remains green for in-tree tests.

## Out of Scope

- One-shot full pass of every upstream test in a single loop.
- Emulating PostgreSQL internal implementation details that are
  intentionally not part of goopg's architecture.