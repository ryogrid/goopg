# Go Test Utility Library (Milestone 0004)

| Field      | Value                                                  |
| ---------- | ------------------------------------------------------ |
| Status     | draft                                                  |
| Date       | 2026-04-29                                             |
| Milestone  | 0004 - TAP Test Port and Go Utility Library            |
| Refines    | [docs/milestones/0004-tap-test-port.md](../../milestones/0004-tap-test-port.md), [root-0017-data-directory.md](../../root/root-0017-data-directory.md) |
| Supersedes | -                                                      |

## Problem

Porting PostgreSQL TAP tests requires reusable Go-side test harnesses for:

- temporary directory/file operations,
- command execution with timeout and stdout/stderr capture,
- cluster lifecycle orchestration (`init/start/stop/status/reload/checkpoint`),
- SQL client helpers (`psql`, `pgbench`, Go `database/sql`),
- log assertions.

Without a shared library, each ported test re-implements process control and
becomes brittle.

## Implemented Packages

### internal/testutil/util

Provides utility primitives analogous to `PostgreSQL::Test::Utils`:

- `MkdirTemp`
- `WriteTextFile`
- `RunCommand` (exit-code capture, timeout, stdout/stderr capture)
- `FileContains`
- `WaitForFileContains`

### internal/testutil/cluster

Provides single-cluster orchestration analogous to
`PostgreSQL::Test::Cluster` (multi-cluster intentionally deferred):

- `Init`, `Start`, `Stop`, `Restart`, `Reload`, `Checkpoint`, `Status`
- `WaitForStatus`
- `Query` (`database/sql` via `lib/pq`)
- `PSQL`, `StartPSQL`, `PGbench`
- config mutation helpers (`AppendPostgresqlConf`, `AppendPGHBA`)
- log helpers (`WaitForLogContains`, `TruncateLog`)

## Design Notes

- Harness defaults to `go run ./cmd/goopg` for maximal reproducibility in CI.
- Exit-code semantics from `goopg status` are preserved so TAP-like assertions
  can map directly.
- External tool tests (`psql`, `pgbench`) are skip-aware when binaries are not
  installed.

## Deferred

- Multi-cluster orchestration API.
- Rich process supervision (stderr streaming channels, asynchronous log cursor).
- Direct libpq protocol-level hooks for cancellation/socket fault injection.
