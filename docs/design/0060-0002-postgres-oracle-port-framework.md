# PostgreSQL Oracle Port Framework (M0060)

| field      | value |
|------------|-------|
| status     | draft |
| date       | 2026-05-07 |
| milestone  | 0060 — PostgreSQL Oracle Test-Port Foundation |
| supersedes | 0060-0001-postgres-test-porting-strategy.md |

## 1. Problem

M0060-0001 defined migration policy and scope, but it did not provide a
single executable framework to:

- freeze inventory from upstream trees,
- record defer/excluded/port status in machine-readable form,
- support regress/isolation harness foundations before full test ports,
- and generate auditable compatibility reports.

Without this framework, status tracking remains document-only and drifts.

## 2. Goals

- Canonical inventory generation for all in-scope families.
- Machine-readable status source of truth with validation.
- Baseline harness foundation for pg_regress-style and isolation-style
  migration workflows.
- Deterministic report generation for CI and reviews.

## 3. Scope

### In scope

- `internal/testport/framework` shared package.
- New generators under `cmd/` for inventory, coverage, status, and report.
- `docs/test-port/*.csv` and generated markdown outputs.

### Out of scope

- Porting additional upstream tests beyond already landed TAP rows.
- Feature work needed to promote deferred suites to pass-required.

## 4. Architecture

### 4.1 Inventory

`framework.BuildSuiteInventory(repoRoot)` scans:

- `postgres/src/test/regress/sql/*.sql`
- `postgres/src/test/regress/expected/*.out`
- `postgres/src/test/isolation/specs/*.spec`
- `postgres/src/test/recovery/t/*.pl`
- `postgres/src/test/subscription/t/*.pl`
- `postgres/src/bin/*/t/*.pl`
- `postgres/src/test/modules/*`
- `postgres/contrib/*`

Output is generated to:

- `docs/test-port/postgres-oracle-target-inventory.csv`
- `docs/test-port/postgres-oracle-target-inventory.md`

### 4.2 Status Governance

Machine-readable source:

- `docs/test-port/postgres-oracle-port-status.csv`

Validation rules in `framework.ValidateStatusRows`:

- `status` must be one of `port` / `defer` / `excluded`.
- `pass_required` must be `yes` or `no`.
- `defer` requires `deferred_to`.
- `excluded` must use `deferred_to = '-'` (or empty during load).
- IDs must be unique.

Rendered output:

- `docs/test-port/postgres-oracle-port-status.md`

### 4.3 pg_regress Harness Foundation

`framework` exposes:

- `DiscoverRegressCases(repoRoot)`
- `RunRegressSubset(ctx, repoRoot, cases, executor)`
- `NormalizeRegressOutput(raw)`

Result model is policy-aligned (`port` / `defer` / `excluded`) so initial
stages can report outcomes before full parity.

Coverage snapshot output:

- `docs/test-port/upstream-regress-coverage.md`

### 4.4 Isolation Harness Foundation

`framework` exposes:

- `DiscoverIsolationSpecs(repoRoot)`
- `ParseIsolationSpec(path)`
- `RunIsolationPermutation(ctx, spec, index, executor)`

This implements deterministic permutation execution over parsed step order,
which is sufficient for stage-0 scheduling validation.

Coverage snapshot output:

- `docs/test-port/upstream-isolation-coverage.md`

### 4.5 TAP Scope and Client-Tool Tracking

`cmd/gen-tap-coverage` scope now includes:

- `postgres/src/test/recovery/t/*.pl`
- `postgres/src/test/subscription/t/*.pl`
- `postgres/src/bin/*/t/*.pl`

Status labels align with governance model:

- `port` / `defer` / `excluded`.

Output:

- `docs/test-port/upstream-tap-coverage.md`

### 4.6 Oracle Compatibility Reporting

`cmd/gen-oracle-report` aggregates inventory and status into:

- `analysis/postgres-oracle-compatibility-report.md`

Includes:

- inventory snapshot,
- suite-level status summary,
- deferred blocker table.

## 5. Validation

- Unit tests: `internal/testport/framework/*_test.go`
- TAP representative ports: `internal/testport/tap_port_test.go`
- Full repository gate: `go test ./...`

## 6. Operational Commands

- `go run ./cmd/gen-oracle-inventory --repo-root .`
- `go run ./cmd/gen-regress-coverage --repo-root .`
- `go run ./cmd/gen-isolation-coverage --repo-root .`
- `go run ./cmd/gen-tap-coverage --repo-root .`
- `go run ./cmd/gen-oracle-port-status --repo-root .`
- `go run ./cmd/gen-oracle-report --repo-root .`
