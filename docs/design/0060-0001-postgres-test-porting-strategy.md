# PostgreSQL Test-Porting Strategy (M0060)

| field      | value |
|------------|-------|
| status     | draft |
| date       | 2026-05-06 |
| milestone  | 0060 — PostgreSQL Oracle Test-Port Foundation |
| supersedes | — |

## 1. Problem

goopg lacks a unified, auditable framework that treats upstream
PostgreSQL tests as an oracle at suite level. Existing migrated tests,
generated TAP coverage, and feature milestones are useful but fragmented.

We need one strategy that:

- normalizes migration across different upstream harness styles,
- records what is a migration target,
- makes non-passing-yet-allowed tests visible,
- and cleanly separates architecture-excluded tests from deferred tests.

## 2. Scope

### In scope

- Migration strategy by upstream test type.
- Upstream migration target code list.
- Status model for pass/defer/excluded tracking.
- Integration points with goopg test tooling and CI reporting.

### Out of scope

- Completing all migrated tests in one loop.
- Implementing all missing server features required by deferred suites.

## 3. Migration Target Code List

M0060 migration targets include at least the following upstream code:

- `postgres/src/test/regress`
- `postgres/src/test/isolation`
- `postgres/src/test/recovery`
- `postgres/src/test/subscription`
- `postgres/src/bin/*/t` (client-tool TAP tests)
- `postgres/src/test/modules/*`
- `postgres/contrib/*/{sql,expected,t}`

## 4. Porting Methods by Test Type

### 4.1 pg_regress (`src/test/regress`)

- Source shape: SQL files + expected outputs.
- Porting method:
  - build a Go runner that executes canonical SQL scripts against goopg,
  - normalize output using stable formatting rules,
  - compare against expected snapshots with allowlisted differences.
- Main challenge: output normalization and feature-gated expectations.

### 4.2 TAP (Perl) suites (`src/bin/*/t`, `src/test/recovery/t`, `src/test/subscription/t`)

- Source shape: Perl TAP scripts with upstream harness helpers.
- Porting method:
  - translate each scenario into `go test` cases,
  - reuse `internal/testutil/cluster` and related harness packages,
  - preserve upstream lineage in per-test headers.
- Main challenge: replacing Perl orchestration and upstream helper APIs.

### 4.3 isolation specs (`src/test/isolation`)

- Source shape: `.spec` concurrency scripts with expected schedules.
- Porting method:
  - implement a Go scheduler that drives multiple sessions/connections,
  - model deterministic step orchestration,
  - compare observed schedule outcomes with expected files.
- Main challenge: deterministic coordination and lock/timeout semantics.

### 4.4 modules/contrib suites (`src/test/modules`, `contrib`)

- Source shape: mixed pg_regress/TAP + extension-specific assumptions.
- Porting method:
  - classify by dependency (core-compatible vs extension-specific),
  - port core-compatible subsets first,
  - keep extension-heavy items defer/excluded with explicit rationale.
- Main challenge: extension ABI and feature availability gaps.

## 5. Deferred/Excluded Visibility Model

Status tracking file:

- `docs/test-port/postgres-oracle-port-status.md`

Minimum fields:

- `id` (stable logical identifier)
- `upstream_path`
- `suite_type` (regress/tap/isolation/modules/contrib)
- `status` (`port` / `defer` / `excluded`)
- `pass_required` (`yes`/`no`)
- `rationale`
- `deferred_to` (milestone/task reference, `-` if excluded)

Rules:

- `defer`: in scope but not yet pass-required; must reference a follow-up.
- `excluded`: must cite architecture/scope rationale.
- no silent non-pass: every non-passing target must appear in the status file.

## 6. Exclusion Criteria

A test may be `excluded` only if at least one criterion is met:

1. It fundamentally depends on PostgreSQL process-model internals that
   goopg intentionally does not implement.
2. It depends on C-extension ABI surfaces intentionally outside goopg scope.
3. It depends on implementation details incompatible with Go runtime
   architecture choices (goroutine scheduling/GC model) and not intended
   for compatibility emulation.

## 7. Affected goopg Paths

- `internal/testport/`
- `internal/testutil/cluster/`
- `internal/testutil/replcluster/`
- `cmd/gen-tap-coverage/`
- future: new runners for regress/isolation migration under `cmd/` or
  `internal/testport/`.

## 8. Validation and Reporting

- Add suite-level summary reporting that counts `port`, `defer`, and
  `excluded` entries and highlights unexpected failures.
- Keep milestone-level oracle progress auditable from docs without
  requiring ad hoc log inspection.