# goopg — session guidance

goopg is a from-scratch Go reimplementation of PostgreSQL. Vanilla-PG
compatibility is absolute; the read-only oracle is PG 18.3 under
`./postgres/` (never modify it).

## Test gates

- Manual pre-commit bar: `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
  (unit/component suite). The git hook runs the pgbench smoke on EVERY
  commit — never `git commit --no-verify`.
- **Never pass `-count=1` to a gate's `go test`** — it defeats the Go
  test-result cache (~5 min warm vs ~40 min cold `units`). `-count=1` is for
  one-off validation runs only (flake screens, probes). A cached PASS is a
  real PASS; policy and limits: `ci/design/test-gate-speedups/05` §1.
- Gate wrappers must keep the `go test` command line and environment
  byte-identical run-to-run (changing env vars silently cold-start the cache).

## Test-cluster durability defaults (fast by design)

Throwaway test clusters skip durability work, matching upstream
pg_regress/`PostgreSQL::Test::Cluster`: the smoke gate inits with
`--no-sync`; `internal/testutil/cluster` inits from a per-process cached
template (`--no-sync`, sysid re-randomized per clone) and runs servers with
`fsync = off` (a real GUC, default `on`). Durability-asserting tests —
crash-recovery, WAL-replay, replication, restart-durability — must opt out
with `SyncInit: true` + `SyncRuntime: true` (`newDurableCluster` in
testport). Write NEW recovery tests durable-by-default. Allowlist criteria:
`ci/design/test-gate-speedups/02` §4.

## Running a server manually

Always memory-cap (WSL2 OOM containment):
`GOOPG_CG_UNIT=<name> scripts/goopg-test-run.sh ./bin/goopg start -D <dir> --listen 127.0.0.1:<port>`.
Use ports 5533/5534 and re-init the data dir between runs. The flag is
`--listen`, not `-p`.

## Git

- Commit style: `area(scope): summary — detail`. Stage by explicit pathspec
  (a concurrent Ralph loop's WIP may be present in the tree; never `git add -A`).
- Repo gofmt baseline is go1.25 — never run `gofmt -w` wholesale (a newer
  local gofmt rewrites unrelated lines); match formatting manually.
