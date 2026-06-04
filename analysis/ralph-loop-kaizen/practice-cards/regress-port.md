# Practice card — PostgreSQL oracle / TAP test porting

**Load when** the task ports upstream regress/isolation/recovery/TAP tests, or
touches `internal/testport/`, or the oracle-port status CSV.

**Why:** the port status is governed by an authoritative CSV with a strict
workflow; closing a deferred entry without updating it desyncs the inventory.

## Authoritative source

`docs/test-port/postgres-oracle-port-status.csv` decides what must pass:

| status | pass_required | meaning |
|--------|--------------|---------|
| `port` | yes | ported; must always pass |
| `defer` | no | in scope, not yet pass-required |
| `excluded` | no | out of scope by policy — do not port |

## Workflow when you satisfy a `defer` entry's blocker (same loop)

1. Port the test to Go in `internal/testport/` (or the subsystem test file).
2. Update the CSV: `status → port`, `pass_required → yes`, fill `rationale` with
   the Go test function name, clear `deferred_to`.
3. Regenerate the MD: `go run ./cmd/gen-oracle-port-status`.
4. Verify it passes: `go test -v -run <TestFuncName> ./internal/testport/`.

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

## Compatibility oracle

Normalise output against vanilla PG 18.3 from `./postgres/local_install`. Any
divergence is a goopg bug to fix in goopg — never branch PG behavior.
