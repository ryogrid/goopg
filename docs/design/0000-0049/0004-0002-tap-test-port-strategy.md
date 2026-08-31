# TAP Test Port Strategy (Milestone 0004)

| Field      | Value                                                  |
| ---------- | ------------------------------------------------------ |
| Status     | draft                                                  |
| Date       | 2026-04-29                                             |
| Milestone  | 0004 - TAP Test Port and Go Utility Library            |
| Refines    | [docs/test-port/upstream-tap-coverage.md](../../test-port/upstream-tap-coverage.md), [0004-0001-go-test-utility-library.md](0004-0001-go-test-utility-library.md) |
| Supersedes | -                                                      |

## Goal

Port at least 80% of upstream tests currently classified as `port` in
`docs/test-port/upstream-tap-coverage.md`, with each Go test referencing the
upstream TAP source path in a header comment.

## Scope

Current `port` rows are focused on lifecycle and client behavior:

- `initdb` basic init flow
- `pg_ctl` start/stop/status/promote/logrotate-adjacent flows
- `pgbench` with/without server
- `psql` basic, tab-completion-adjacent smoke, cancel-adjacent behavior

## Porting Rules

- Keep one Go test per upstream TAP row.
- Add a top-of-test comment: `// upstream: <path>`.
- Use `internal/testutil/cluster` and `internal/testutil/util`; avoid bespoke
  shell scripts in individual tests.
- If a required external binary is missing (`psql`, `pgbench`), call `t.Skip`.
- Where upstream semantics rely on features not present in goopg v0
  (replication promote, interactive tty completion, server-side cancel hooks),
  port an adapted smoke test and document the adaptation inline.

## Automation

`cmd/gen-tap-coverage` regenerates the coverage matrix from the upstream tree:

```bash
go run ./cmd/gen-tap-coverage --repo-root . --out docs/test-port/upstream-tap-coverage.md
```

## Deferred

- Recovery/replication TAP suites (`postgres/src/test/recovery/t`) remain
  deferred until replication/failover semantics exist.
- Non-server client tool suites (backup/upgrade/dump/verify families) remain
  skip/defer per coverage matrix.
