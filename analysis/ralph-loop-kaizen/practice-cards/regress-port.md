# Practice card — PostgreSQL oracle / TAP test porting

**Load when** the task ports upstream regress/isolation/recovery/TAP tests, or
touches `internal/testport/`, or the test-port inventory CSV.

**Why:** the port status is governed by an authoritative CSV with a strict
workflow; closing a deferred entry without updating it desyncs the inventory.

**Canonical reference:** `docs/test-port/README.md` — read it first. The
**single** authoritative data file is
`docs/test-port/postgres-oracle-target-inventory.csv` (schema
`id,suite_id,kind,item_path,status,pass_required,deferred_to,rationale`).

## Status vocabulary

| status | pass_required | meaning |
|--------|--------------|---------|
| `port` | yes | ported TAP test; must always pass (rationale names its `TestPort_*`) |
| `pass` | yes | regress/isolation case passing; must stay passing |
| `failed` | no | in-scope case, currently diverging |
| `not-tried` | no | in-scope, not yet executed |
| `defer` | no | in scope, deferred to a milestone (`deferred_to`) |
| `excluded` | no | out of scope by policy — do not port |

## Workflow when you satisfy a `defer`/`failed` entry's blocker (same loop)

1. Port the test to Go in `internal/testport/` (or the subsystem test file).
2. Update the CSV row: `status → port`/`pass`, `pass_required → yes`, fill
   `rationale` with the Go test function name, clear `deferred_to`.
3. Regenerate the derived docs: `make regen-testport`.
4. Verify it passes: `go test -v -run <TestFuncName> ./internal/testport/`.
5. If the case was in `ci/batch/expected-failures.csv`, delete its row.

## Running ported tests (NOT part of `go test ./...`)

They invoke real client binaries and are slow — run explicitly, and through the
memory cap if they boot a server (see [[server-test]]):

```bash
go test -v -run TestPort_ ./internal/testport/            # all ported TAP
scripts/goopg-test-run.sh go test -v -tags integration ./internal/testport/...
```

## Deferred-suite unlock conditions

D-001 regress (pg_regress runner), D-002 isolation (multi-session scheduler),
D-003/004 recovery+subscription (replication), D-005 bin/scripts, D-006 modules,
D-007 contrib. Don't promote a deferred suite until its prerequisite has landed.

## Consistency gate

`make check-testport-inventory` (and the nightly testport stage) validates the
on-disk CSV: a malformed row, an `excluded`+`pass_required=yes` combination, a
`defer` without `deferred_to`, a `port` without a named test func, or a duplicate
id fails the gate.

## Compatibility oracle

Normalise output against vanilla PG 18.3 from `./postgres/local_install`. Any
divergence is a goopg bug to fix in goopg — never branch PG behavior.
