# Milestone 0004 — TAP Test Port & Go Utility Library

**Status:** planned
**Depends on:** Milestone 0001 (need a server to test).
**Parallelizable:** Can run concurrently with Milestone 0002 and Milestone 0003. In fact it is most valuable when running in parallel: ported tests catch regressions in those milestones as they land.

## Context

PostgreSQL's TAP (Test Anything Protocol) test suite is one of the highest-leverage assets upstream maintains. It covers end-to-end scenarios that unit tests can't: lifecycle management, crash recovery, replication, CLI tools, edge-case interactions between subsystems. Reinventing equivalent coverage from scratch for goopg is wasteful and error-prone. Porting it gives goopg, for free, a battle-tested test corpus aligned with the very system it claims compatibility with.

The work has two distinct parts: building the Go utility library that ported tests will sit on top of, and selecting and porting individual tests.

## In Scope

### Part 1 — Go Utility Library

Build Go equivalents of PostgreSQL's Perl test modules under `postgres/src/test/perl/PostgreSQL/Test/`. Suggested layout inside the goopg repository:

- `internal/testutil/cluster` — equivalent of `PostgreSQL::Test::Cluster`. Responsibilities:
  - Initialize a cluster in a temp data directory (calls `goopg init`).
  - Start, stop, restart, with mode flags equivalent to `pg_ctl -m smart|fast|immediate`. These map to the dedicated CLI subcommands introduced under `REQUIREMENTS.md` §3.3.
  - Issue queries via `psql` and via a Go-native libpq-compatible client.
  - Capture and inspect server logs.
  - Background-psql sessions equivalent to `PostgreSQL::Test::BackgroundPsql`.
  - Programmatic edits to `postgresql.conf` and `pg_hba.conf` between runs.
  - Multi-cluster scenarios for tests that need two or more nodes (relevant when replication eventually lands; out of scope to implement but the API should not preclude it).
- `internal/testutil/util` — equivalent of `PostgreSQL::Test::Utils`. Responsibilities:
  - Tempfile and tempdir helpers.
  - Command runners with timeout, stdout/stderr capture, and exit-code inspection.
  - Log file scanning helpers (wait-for-log-line with timeout, line iteration, regex matching).
  - Path helpers for cross-cluster file operations.

API design priorities:

- Idiomatic Go. Methods on a `*Cluster` value, errors returned (not panics), `t.Helper()` integration where useful, `context.Context` for cancellation, no global state.
- Run on top of Go's standard `testing` package. TAP-format output for CI integration, if needed, can come from external tooling like `gotestsum`; do not bake TAP output into the library.
- Place under `internal/` initially. The library should remain liftable to a public package later — keep dependencies on goopg's internals behind small, documented interfaces.

Reference reading: `postgres/src/test/perl/PostgreSQL/Test/Cluster.pm`, `postgres/src/test/perl/PostgreSQL/Test/Utils.pm`, `postgres/src/test/perl/PostgreSQL/Test/BackgroundPsql.pm`.

### Part 2 — Test Selection and Classification

Walk every TAP test under the upstream tree (`postgres/src/test/recovery/t/`, `postgres/src/bin/*/t/`, `postgres/src/test/subscription/t/`, `postgres/src/test/modules/*/t/`, etc.) and classify each file as:

- **port** — Covers a feature goopg supports. Port to Go.
- **skip** — Covers an out-of-scope feature (extensions, replication features not yet built, PL languages, on-disk `pg_upgrade` flows, contrib modules). Record a one-line reason.
- **defer** — Covers an in-scope feature that goopg doesn't yet implement well enough. Reopen when the feature lands.

Persist the classification under `docs/test-port/upstream-tap-coverage.md` as a tracked file. Each row links to the upstream path and, for `port` rows, to the Go test file once it exists.

### Part 3 — Ported Tests

For each `port` row, produce a Go test under a path that mirrors upstream's structure where practical. Examples:

- `postgres/src/bin/pg_ctl/t/001_start_stop.pl` → `cmd/goopg/ctl_test/001_start_stop_test.go`
- `postgres/src/bin/initdb/t/001_initdb.pl` → `cmd/goopg/init_test/001_init_test.go`

Each ported test must reference its upstream source path in a header comment so that the lineage is traceable.

## Out of Scope

- Upstream's `pg_regress` SQL regression tests (`postgres/src/test/regress/`). That is a separate, larger port handled by its own future milestone.
- Tests for features goopg explicitly does not implement (extensions, PL/pgSQL, replication features that are still deferred).
- Building a TAP-format output formatter inside the library. External tooling handles this.

## Required Design Docs

- `0004-0001-go-test-utility-library.md` — API design and rationale for `cluster` and `util` packages, including how they interact with goopg's CLI subcommands.
- `0004-0002-tap-test-port-strategy.md` — selection criteria, porting conventions, and the structure of `docs/test-port/upstream-tap-coverage.md`.

## Definition of Done

1. `internal/testutil/cluster` and `internal/testutil/util` packages exist with godoc-documented public APIs.
2. `docs/test-port/upstream-tap-coverage.md` lists every upstream TAP test file in the supported paths with one of `port` / `skip` / `defer` and a one-line rationale. The list is reproducible from a tool or script committed alongside it, so that upstream churn can be re-classified without manual sweeping.
3. At least 80% of tests classified `port` are ported and passing against goopg. Anything ported but failing has a tracking issue or doc-comment explaining why.
4. `go test ./...` runs the ported tests as part of the standard test suite. They are not gated behind a separate build tag unless they are slow enough to warrant it (in which case the gating convention is documented).
5. Both required design docs merged with status `accepted`.
