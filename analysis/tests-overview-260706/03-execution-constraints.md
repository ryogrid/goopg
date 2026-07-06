# 03 — Execution Constraints (resource caps, parallelism, isolation)

Snapshot 2026-07-06. These are the rules a regression batch must honor to avoid
crashing the WSL2 host or colliding with a concurrent Ralph loop.

---

## A. cgroup v2 memory cap — MANDATORY for any server/benchmark run

The host is a 32 GiB WSL2 box with a 64 GiB swap file. A runaway goopg (huge
TPC-H intermediate, unbounded benchmark) thrashes swap and then trips the Linux
**system-wide** OOM killer, which on WSL2 can take down the whole distro.
`GOMEMLIMIT` is only a Go *soft* target and does not prevent this.

**Rule:** any command that starts a goopg server, or drives one with a heavy
workload (oracle/integration tests, TPC-H, `pgbench`, perf runs) **must** go
through `scripts/goopg-test-run.sh`, which runs it inside a `systemd-run --user
--scope` cgroup with a hard memory cap and swap disabled. A runaway is then
SIGKILLed *inside its own scope* and the host survives.

Mechanism:
```bash
systemd-run --user --scope --quiet --collect --expand-environment=no \
    --unit="${GOOPG_CG_UNIT}" \
    -p MemoryHigh="${GOOPG_MEM_HIGH}" -p MemoryMax="${GOOPG_MEM_MAX}" \
    -p MemorySwapMax="${GOOPG_MEM_SWAP_MAX}" -- "$@"
```

| Env var | Default | Meaning |
|---------|---------|---------|
| `GOOPG_MEM_HIGH` | `20G` | soft cap — reclaim + throttle |
| `GOOPG_MEM_MAX` | `24G` | hard cap — cgroup-local OOM kill |
| `GOOPG_MEM_SWAP_MAX` | `0` | swap allowed to the scope (0 = none) |
| `GOOPG_CG_UNIT` | `goopg-test` | transient scope unit name |
| `GOMEMLIMIT` | `18GiB` (exported if unset) | Go soft heap target, kept below the soft cap |

- **Each concurrent capped run needs a distinct `GOOPG_CG_UNIT`** — systemd
  refuses a second scope with a name already in use.
- Stop a backgrounded scope: `systemctl --user stop <unit>.scope`
  (+ `reset-failed` if a failed unit lingers).
- **Graceful fallback:** where systemd/cgroup delegation is unavailable (e.g.
  CI), the wrapper prints a WARNING and runs the command **uncapped** rather than
  failing — so it is safe everywhere. (CI runs goopg uncapped for this reason.)
- Light, server-less unit tests (`go test ./internal/<pkg>/...`) do **not** need
  the wrapper.

`make start`/`goopg-test-server`/`bench-*`/`pgo-profile` and
`tpch-spotcheck.sh`/`ralph-precommit-test.sh` already route through the cap.

---

## B. Parallelism & the memory watchdog

- **`export GOMEMLIMIT=15GiB`** at session start (per `.ralph/AGENT.md`). Soft
  target only.
- **Avoid unbounded `go test ./...` fan-out** — cap `-p`/`-parallel` on big
  packages; don't generate oversized data sets in-process (`.ralph/PROMPT.md`
  Memory Guard section).
- **`mem_guard.py` watchdog** (`~/.ralph/mem_guard.py`, one per `ralph_loop.sh`
  run): every few seconds sums RSS+swap of the loop's descendants **excluding
  `claude` and its MCP/helper infra**; above **70 % of (physical RAM + swap)** it
  sends `kill -KILL` to the **single heaviest** unprotected process — usually a
  runaway `go test`/build/`goopg`/`pgbench`.
  - **Implication for a batch:** a process dying with signal 9 / "Killed" and no
    panic/assertion may be the guard relieving pressure, **not** a product bug —
    check `~/.ralph/logs/mem_guard.log` for a `PRESSURE …` line before
    debugging. Keep aggregate heavy-run memory bounded.

---

## C. Port & tmp-dir isolation lanes

Different test/bench lanes use deliberately distinct ports and data dirs so they
don't collide (important because a **Ralph loop may be running concurrently** and
holding some of these):

| Port | Lane | Data dir |
|-----:|------|----------|
| 5432 | standard PG / CI server / `make start` default | `tmp/goopg-data` |
| 5433 / 5434 | pgbench-compare (goopg / PostgreSQL) | `tmp/pgbench-compare/{goopg-data,postgres-data}` |
| 5533 / 5534 | perf / isolation runs | `tmp/perf-optimize/data` |
| 5535 | `ralph-precommit-test.sh` (probes upward to a free port) | `tmp/ralph-precommit-goopg-data-$$` |
| 65433 | TPC-H bench / `tpch-runner` / `plan-gate` / `pg-oracle-diff` goopg side | `bench/tpch/runtime_goopg/data` |
| 15435 | `pg-regress-runner.sh` auto-start default | (throwaway) |

**Rule for a batch running alongside the loop:** pick free ports and a unique
`GOOPG_CG_UNIT` per concurrent capped run; use a distinct tmp data dir per lane.
Never `pkill -f goopg` (self-matches, kills unrelated servers) — stop via
`goopg stop -D <datadir>` / `systemctl --user stop <unit>.scope`.

---

## D. CI parity (what "green" must mean)

`.github/workflows/test.yml` (`Run Tests`, Go 1.25.0, ubuntu-latest):

- **Unit/component step** — the bar every commit must clear:
  ```bash
  go test -timeout 10m $(go list ./... | grep -vE \
    'internal/testport|internal/server|internal/testutil/cluster|internal/testutil/replcluster|internal/testutil/pgcluster|internal/testutil/pubsubcluster|internal/testutil/tpch|/bench/')
  ```
- **Heavier CI steps (run separately, by path — NOT part of the unit set):**
  - pgbench on port 5432 (`-i` → standard / `-N` / `-S`, each `-T 30 -c 2 -j 2`).
  - HammerDB TPC-H load: `go test -v -timeout 30m ./bench/tpch/cmd/hammerdb_load/...`.
  - TPC-H query run ~SF 0.5: `go test -v -run TestTPCHScaleLoadAndQueryRun -timeout 120m ./internal/testutil/tpch/` (750k orders, Q1..Q22; override `GOOPG_TPCH_ORDERS`/`GOOPG_TPCH_QUERIES`).
- CI runs goopg **uncapped** (no user cgroup on the runner) — the wrapper degrades
  gracefully there.

A batch that mirrors CI = unit set (§D) + pgbench smoke + (optionally) race,
plan-gate, tpch-spotcheck, and the heavier TPC-H/HammerDB runs where data exists.
