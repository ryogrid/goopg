# 02 — Execution Scripts & Make Targets

Snapshot 2026-07-06. Everything here is *usable or referenceable* for building a
regression batch. Runtime characteristics are approximate.

---

## A. `scripts/` — runnable scripts

| Script | Purpose | Key invocation / tunables |
|--------|---------|---------------------------|
| `scripts/ralph-precommit-test.sh` | CI-parity gate: Go unit suite + pgbench smoke | `RALPH_PRECOMMIT_SCOPE=units\|smoke\|full` (default `full`); flags `RALPH_PRECOMMIT_RACE=1`, `RALPH_PRECOMMIT_PLAN_DIFF=1`; `RALPH_PRECOMMIT_PGPORT` (default 5535, probes upward) |
| `scripts/tpch-spotcheck.sh` | Fast Q12/Q13 row-count regression gate from a fresh server | SKIPs (exit 0) if no data ≥ `TPCH_SPOTCHECK_MIN_MB` (100); `TPCH_SPOTCHECK_TIMEOUT`=600s, `TPCH_SPOTCHECK_READY_TIMEOUT`=120s; sources `bench/tpch/env_goopg.sh` (port 65433) + `spotcheck_expected.env` |
| `scripts/goopg-test-run.sh` | **cgroup v2 memory-cap wrapper** — wrap any server/benchmark run | env `GOOPG_MEM_HIGH`/`GOOPG_MEM_MAX`/`GOOPG_MEM_SWAP_MAX`/`GOOPG_CG_UNIT` (see `03-execution-constraints.md`) |
| `scripts/pg-regress-runner.sh` | Run upstream PG regress `.sql` vs goopg, report parity % | bare (~40 quick type tests) · `--all` (232, slow) · `-v int4 float8` (named, diff on fail); auto-starts goopg on 15435 or `--port`; out `tmp/regress-diffs/` |
| `scripts/pg-oracle-diff.sh` | Diff a SQL file/inline against goopg **and** vanilla PG 18.3 | `query.sql` · `--sql "..."` · `--auto-start`; goopg 65433 / PG 5433 (PG comparison out of scope per current task) |
| `scripts/gen-parity-dashboard.sh` | Write `docs/parity-dashboard.md` (GUC/SQLSTATE/catalog parity) | read-only, no server, no build; `--stdout` |
| `scripts/runtimeshim_go_matrix.sh` | Run `internal/runtimeshim` under every Go toolchain in PATH | bare (all) or `runtimeshim_go_matrix.sh go1.24 go`; default + `-tags noLinkname` |

### `ralph-precommit-test.sh` scopes (the main batch building block)

- **`units`** — Part 1 only: `go test -timeout 10m $(go list ./... | grep -vE EXCLUDE)`
  (the CI unit set; ~10 min). This is the agent's mandatory manual pre-commit run.
- **`smoke`** — Part 2 only: pgbench smoke (~2–3 min). What the git hook runs.
- **`full`** — both.
- **Part 1b** (`RALPH_PRECOMMIT_RACE=1`): `go test -race -timeout 15m` (+5–10 min).
- **Part 3** (`RALPH_PRECOMMIT_PLAN_DIFF=1`): runs `make plan-gate`.
- Isolation: per-run-unique datadir/scope suffixed with PID
  (`tmp/ralph-precommit-goopg-data-$$`, `GOOPG_CG_UNIT=ralph-precommit-goopg-$$`);
  cleanup trap kills the real PID (from `postmaster.pid`) + stops the scope.

---

## B. Makefile — gate & bench targets

There is **no bare `make test` or `make bench`** — the module test entry is
`go test` / `ralph-precommit-test.sh`. Relevant targets:

| Target | What it runs |
|--------|--------------|
| `make race-gate` | `go test -race -timeout $(RACE_TIMEOUT=15m) $(go list ./... \| grep -vE RACE_EXCLUDE)` — same EXCLUDE as CI |
| `make plan-gate` | Build `cmd/plan-snapshot`; diff EXPLAIN of TPC-H queries vs latest `plan_snapshots/*.txt`. **SKIP(0)** if no baseline or goopg not reachable on `PLAN_PORT=65433`. `MODE=structural`(default)/`strict-text`/`semantic-cost` |
| `make parity-dashboard` | `bash scripts/gen-parity-dashboard.sh` |
| `make start` / `stop` / `restart` | Background capped server (`GOOPG_CG_UNIT=goopg-server`, `LISTEN=127.0.0.1:5432`, `DATA_DIR=tmp/goopg-data`) / graceful stop / both |
| `make goopg-test-server` | Foreground capped server (`GOOPG_CG_UNIT=goopg-test-server`) |
| `make ralph-state-guard` | `go run ./cmd/validate-ralph-state` — checks `.ralph/status.json` vs `progress.json`, repairs, re-verifies |
| `make install-hooks` | `git config core.hooksPath .githooks` + chmod (run once per clone) |
| `make build` | `GOAMD64=v3 go build [-pgo=default.pgo] -o bin/goopg` |
| `make bench-build` / `bench-build-optimized` | `tmp/goopg-bench-bin` (optimized: PGO + GOAMD64=v3 + `-ldflags='-s -w'` + `-trimpath`) |
| `make pgo-profile` | Capture `default.pgo` over 480s driving TPC-H Q1,3,12,13,21 (needs pprof `:6060` + bench server) |
| `make plan-snapshot-build` / `-capture` / `-diff` | Capture/diff EXPLAIN snapshots (require `LABEL=`) |
| `make pgbench-compare` / `-matrix` / `-report` | `bench/pgbench-compare/*.sh` (PG-vs-goopg; comparison out of scope per task) |
| `make runtimeshim-matrix` | `bash scripts/runtimeshim_go_matrix.sh` |

Cap defaults exported to capped targets: `GOOPG_MEM_HIGH?=20G GOOPG_MEM_MAX?=24G GOOPG_MEM_SWAP_MAX?=0`.

---

## C. git pre-commit hook

`.githooks/pre-commit` runs the **pgbench smoke on every commit**:
```bash
RALPH_PRECOMMIT_SCOPE=smoke "$ROOT/scripts/ralph-precommit-test.sh"
```
(~2–3 min: build → init → pgbench standard / `-N` / `-S`). Commit is **rejected**
on failure. Escape hatch `GOOPG_SKIP_PRECOMMIT=1` exists only for hosts without
pgbench; `git commit --no-verify` is explicitly forbidden. Wired by
`make install-hooks`.

---

## D. Coverage-CSV regenerators (`cmd/gen-*`) — the promotion path

After a deferred/failed test flips to pass and you edit the source CSV, regen the
rendered docs with the matching tool (each is `cmd/<name>/main.go`):

| Generator | Renders |
|-----------|---------|
| `cmd/gen-oracle-port-status` | `docs/test-port/postgres-oracle-port-status.md` (from the port-status CSV; fails if CSV invalid) |
| `cmd/gen-oracle-inventory` | `postgres-oracle-target-inventory.md` (per-spec) |
| `cmd/gen-regress-coverage` | `upstream-regress-coverage.md` |
| `cmd/gen-isolation-coverage` | `upstream-isolation-coverage.md` |
| `cmd/gen-tap-coverage` | `upstream-tap-coverage.md` |
| `cmd/gen-oracle-report` | consolidated oracle report |
| `cmd/validate-ralph-state` | (loop bookkeeping, not a test doc) |

---

## E. Quick "what to invoke" cheat-sheet

| Goal | Command |
|------|---------|
| CI-parity unit/component suite | `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` |
| pgbench smoke (also the hook) | `RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh` |
| Race detector | `make race-gate` |
| TPC-H row-count spot-check | `scripts/tpch-spotcheck.sh` (SKIPs w/o data) |
| EXPLAIN plan diff | `make plan-gate` (SKIPs w/o bench server on 65433) |
| Oracle ports (all) | `go test -v -run TestPort_ ./internal/testport/` |
| One oracle port | `go test -v -run TestPort_Psql001Basic ./internal/testport/` |
| Regress parity | `scripts/pg-regress-runner.sh [--all]` |
