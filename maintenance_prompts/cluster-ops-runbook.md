# Cluster Operations Runbook — OWNER ONLY

**Every procedure in this document is owner-only.** The Ralph loop and its
agents must never perform them (AGENT.md §R "Hard prohibitions"): the
reference clusters are read-only to the loop, and the only lifecycle actions
a loop may take are `scripts/ref-clusters-ensure.sh` (start-only, never
stop/reset) and `scripts/tpcds-sf025-regression.sh` managing its own
`:65437` gate cluster. Anything else — stop, restart, reload, `--reset`,
recovery, fresh bootstrap — escalates to the owner via a `[!]` block in
`.ralph/fix_plan.md` instead of being acted on.

This file documents the scripts an owner uses for cluster lifecycle, plus
the memory-containment model they run under.

## Cluster inventory

| port | engine | cluster | data dir | managed by |
|---|---|---|---|---|
| 65432 | PostgreSQL 18.3 | TPC-H reference | `bench/tpch/runtime/pgdata` | ensure.sh (pg_ctl) |
| 65433 | goopg | TPC-H bench (SF=1) | `bench/tpch/runtime_goopg/data` | ensure.sh → ref-clusters.sh |
| 65434 | — | reserved: nightly TPC-H clone lane | `tmp/goopg-nightly-tpch-data` | ci/batch only |
| 65435 | — | reserved: nightly TPC-DS clone lane | `tmp/goopg-nightly-tpcds-data` | ci/batch only |
| 65436 | goopg | TPC-DS SF=1 lane | `bench/tpcds/runtime_goopg/data` | `bench/tpcds/server.sh` (on demand) |
| 65437 | goopg | TPC-DS SF=0.25 gate | `bench/tpcds/runtime_goopg/data-sf025` | `tpcds-sf025-regression.sh` (gate-owned); ensure.sh only via explicit `--only 65437` |
| 65438 | PostgreSQL 18.3 | TPC-DS reference | `bench/tpcds/runtime/pgdata` | ensure.sh (pg_ctl) |
| 55xx | goopg | private throwaway clones | `tmp/*-data` | whichever script spawned them |

## Post-reboot model (what runs automatically)

After a host reboot nothing is running, and nothing needs manual bring-up
beyond starting the loop:

1. `~/.ralph/ralph_loop.sh` runs `scripts/ref-clusters-ensure.sh` **before
   every iteration**. Any listed cluster that is down and not HOLD-marked is
   restarted — PG clusters via `pg_ctl`, goopg clusters through
   `scripts/lib/ref-clusters.sh` → `scripts/goopg-test-run.sh`.
2. Every harness-managed goopg start goes through `goopg-test-run.sh`,
   which re-applies the shared slice `goopg-workloads.slice` caps on each
   launch (runtime properties, no persistence needed across reboot) and
   places the scope under it. **Exception:** the one-shot bootstrap start
   inside `bench/tpch/setup_goopg.sh` runs uncapped (see its entry below) —
   restart the cluster through ensure afterwards so it lands in the slice.
3. `ralph_loop.sh` starts `~/.ralph/mem_guard.py` bound to its own PID and
   spawns the detached nightly scheduler (flock-deduped).
4. `:65436` and `:65437` are **not** in ensure's default sweep
   (`REF_ALL_PORTS` is 65432/65433/65438): the `:65436` lane is started on
   demand via `bench/tpcds/server.sh start sf1`, and the gate-owned
   `:65437` is started by `scripts/tpcds-sf025-regression.sh` or explicitly
   via `ref-clusters-ensure.sh --only 65437`.

What the loop will NOT fix by itself (each escalates instead):

- Missing data directories → fresh bootstrap is owner-run (see below).
- A `*.HOLD` file next to a data dir → evidence marker; owner must inspect
  and recover (`tpch-ref-recover.sh`).
- A `postmaster.pid` naming a dead PID → ensure writes a HOLD and stops;
  owner recovery as above.
- A `postmaster.pid` naming a LIVE but non-listening PID → refused; the
  process is starting up or wedged, inspect manually.

## Script catalog

### `scripts/ref-clusters-ensure.sh` — start-only restorer

```
scripts/ref-clusters-ensure.sh [--dry-run] [--strict] [--only PORT[,PORT...]]
```

Brings shared clusters back UP. Never stops, restarts, resets, reloads,
inits or rebuilds anything. Per cluster it checks: already listening → OK;
`*.HOLD` → refused; `PG_VERSION` missing → refused; goopg binary missing →
refused (never built here); a peer owner running (`tpch-ref-recover.sh` for
`:65433`, `tpcds-sf025-regression.sh` for `:65437`) → skipped;
`postmaster.pid` live → refused; `postmaster.pid` dead → writes HOLD and
refuses; start-failure backoff (30 min marker under
`tmp/ref-clusters-ensure/`). Otherwise it starts the cluster. Every action
is appended to `ci/logs/ref-clusters-ensure.log`. Concurrent invocations
are flock-deduped. `--strict` exits 1 when anything is not OK at the end.
Safe to run by hand at any time — the loop already runs it every
iteration.

### `scripts/tpch-ref-recover.sh` — owner-run `:65433` recovery

```
scripts/tpch-ref-recover.sh --i-am-owner [--evidence-dir DIR] [--from-clone DIR] [--dry-run]
scripts/tpch-ref-recover.sh --i-am-owner --evidence-only [--evidence-dir DIR] [--dry-run]
```

Refuses without `--i-am-owner`, and always refuses under `RALPH_LOOP=1`.
Full run: graceful stop → verified evidence copy of the crashed data dir →
verified restore from `preloss-clone-20260915` (or `--from-clone`) →
rebuild the pinned `goopg-bin` from a clean worktree → capped start →
canonical spotcheck (Q12/Q13) → release the HOLD back into ensure's care.
`--evidence-only` runs stop + evidence copy then writes a HOLD pointing at
the evidence. Always `--dry-run` first to review the plan.

### `bench/tpch/setup_goopg.sh` / `stop_goopg.sh` — TPC-H goopg lifecycle

`setup_goopg.sh [--reset]` builds the pinned `goopg-bin`, `goopg init`s
`bench/tpch/runtime_goopg/data` when `PG_VERSION` is absent, writes
`postgresql.conf`/`pg_hba.conf`, and starts the server. `--reset` wipes the
data dir and re-inits it **empty** — the dataset is NOT reloaded by this
script; the HammerDB 5.0 SF=1 load is a separate step
(`bench/tpch/build_schema_goopg.sh`, ~12 min + indexes, data generated
client-side — no dbgen files exist). **Caveat:** its one-shot bootstrap
start uses `nohup` directly — uncapped, outside `goopg-workloads.slice`,
and invisible to mem_guard's scope watch; stop it and let
`ref-clusters-ensure.sh` restart it capped once setup finishes.
`stop_goopg.sh` stops the server cleanly. Never run either while a
`data.HOLD` marker exists — the HOLD is evidence.

### `bench/tpch/setup_pg.sh` / `stop_pg.sh` — TPC-H PG reference lifecycle

Same shape for the PostgreSQL reference cluster on `:65432`.

### `bench/tpcds/server.sh` — TPC-DS lane lifecycle

```
bench/tpcds/server.sh {start|stop|status} [sf1|sf025|pg|all]
```

Manages the lane/gate clusters (`:65436` sf1, `:65437` sf025) and the PG
reference (`:65438`). `start` **rebuilds the lane binary first** (default
`GOOPG_BIN` is `tmp/goopg-tpcds-bin`; the sf025 gate overrides via
`GOOPG_BIN=tmp/goopg-sf025-bin`), refuses outright if `GOOPG_BIN` is the
pinned `:65433` reference binary OR is being executed by any other live
process, then stops the target and starts it capped via
`goopg-test-run.sh`.

TPC-DS bootstrap is split across two scripts: `scripts/tpcds-setup.sh`
only builds `dsdgen`/`dsqgen` and generates the `.dat`/`.tsv` data and
query files (no cluster init, no load), and `scripts/tpcds-load.sh` loads
into a running server. Note nothing scripts `goopg init` for the sf1
`data` dir — `server.sh` refuses to start when the datadir is missing
(only the sf025 gate self-inits `data-sf025`).

### `scripts/goopg-test-run.sh` — the mandatory cgroup wrapper

```
GOOPG_CG_UNIT=<name> scripts/goopg-test-run.sh <command> [args...]
```

Every goopg server and every heavy driver (pgbench, oracle tests, bench
runs) goes through this wrapper: `systemd-run --user --scope` with
`MemoryHigh`/`MemoryMax`/`MemorySwapMax`, inside the shared
`goopg-workloads.slice` aggregate budget. Knobs (defaults for this 32 GiB
host): `GOOPG_MEM_HIGH=20G`, `GOOPG_MEM_MAX=24G`, `GOOPG_MEM_SWAP_MAX=0`,
`GOOPG_CG_UNIT` (unique per concurrent run), `GOOPG_CG_SLICE` (empty
disables), `GOOPG_SLICE_MEM_HIGH=22G`, `GOOPG_SLICE_MEM_MAX=26G`,
`GOOPG_SLICE_SWAP_MAX=8G`, `GOOPG_OOM_SCORE_ADJ=400`, `GOMEMLIMIT=18GiB`.
The wrapper refuses (exit 2) when `GOOPG_MEM_HIGH < GOMEMLIMIT` — with
`GOGC=off` that parks the scope permanently in the kernel throttle band
("throttle-trap" guard); raise the cap or lower GOMEMLIMIT instead of
working around it.

### `~/.ralph/mem_guard.py` — OOM pre-emption watchdog

Started once per `ralph_loop.sh` instance, exits with it. Watches
`memory.current + memory.swap.current` of every `*goopg*` scope plus
non-scope loop descendants; at 75 % of physical RAM it kills the heaviest
killable contributor — a scope atomically via `cgroup.kill`, a bare process
via SIGKILL. `goopg-ref-*` scopes count toward pressure but are never
victims. Knobs: `GUARD_INTERVAL`, `GUARD_RAM_THRESHOLD`,
`GUARD_SCOPE_GLOBS`, `GUARD_PROTECT_SCOPE_GLOBS`, `GUARD_DRY_RUN`,
`GUARD_LOG` (default `~/.ralph/logs/mem_guard.log`).

### `~/.ralph/ralph_loop.sh` / `~/.ralph/ralph-stop.sh` — loop lifecycle

```
~/.ralph/ralph-stop.sh [--dry-run] [--force]
```

Stops the outermost `ralph_loop.sh` via SIGTERM (cleanup trap runs), sweeps
its descendant tree (agent, mem_guard, pipeline helpers), then the detached
nightly scheduler by process group. Identifies loop roots by argv, so agent
prompts or editors merely mentioning the name never match, and refuses to
run from inside a loop without `--force`. It deliberately leaves the goopg
workload scopes and the shared slice running — those are shared
infrastructure, not loop property. `--dry-run` lists what it would signal.

## Common owner procedures

### Bring everything up after a reboot

Start the Ralph loop — ensure does the rest on the first iteration. To do
it without the loop:

```
scripts/ref-clusters-ensure.sh --strict        # shared clusters (65432/65433/65438)
scripts/ref-clusters-ensure.sh --only 65437    # gate cluster, if needed now
bench/tpcds/server.sh start sf1                # :65436 lane, if needed
```

### Graceful stop of a goopg server

```
<server-binary> stop -D <data-dir> -mode fast
# e.g.:  bench/tpch/runtime_goopg/goopg-bin stop -D bench/tpch/runtime_goopg/data -mode fast
```

**Never `pkill -f goopg`** — the pattern self-matches the invoking shell.
For a scope-level stop: `systemctl --user kill -s KILL <unit>.scope`, or
write `1` to the scope's `cgroup.kill` file (SIGKILL-equivalent, atomic;
a bare `systemctl --user kill` sends SIGTERM).

### Restart a reference cluster

Stop it via the binary above, then `scripts/ref-clusters-ensure.sh
--only <port>`. The ref-lane start path (`ref-clusters.sh`) applies
env-overridable defaults to every goopg ref lane: `GOGC=100`,
`GOMEMLIMIT=12GiB`, `GOOPG_ANALYZE_SEED=20260905`,
`GOOPG_OOM_SCORE_ADJ=0`.

### HOLD recovery (`:65433`)

```
scripts/tpch-ref-recover.sh --i-am-owner --dry-run --evidence-dir tmp/evidence-<tag>
scripts/tpch-ref-recover.sh --i-am-owner --evidence-dir tmp/evidence-<tag>
```

### Inspect the memory budget

```
systemctl --user show goopg-workloads.slice -p MemoryHigh -p MemoryMax -p MemorySwapMax
cat /sys/fs/cgroup/user.slice/user-$(id -u).slice/user@$(id -u).service/goopg.slice/goopg-workloads.slice/memory.current
systemctl --user list-units --type=scope | grep goopg
```

Defence layering after the 2026-09-19 fix (see
`~/.ralph/OOM_INCIDENT/2026-09-18-global-oom.md`): slice `MemoryHigh` 22G
throttles → mem_guard ~24.6G kills the heaviest killable scope/process →
slice `MemoryMax` 26G is a slice-local OOM (prefers +400-adj throwaway
workloads) → a host-global OOM is unreachable by construction.

### Stop the Ralph loop

```
~/.ralph/ralph-stop.sh            # or --dry-run first
```

Leaves the workload scopes/slice and shared clusters running by design.

## Environment prerequisites

- systemd with the `memory` controller delegated to the user manager
  (`user@$(id -u).service` running — automatic on login; on this WSL2 host
  it is already delegated).
- Without delegation `goopg-test-run.sh` warns and runs uncapped — never
  treat that as healthy for heavy runs.
- `postgres/local_install/bin` on `PATH` for `psql`/`pg_ctl`/`pg_isready`
  (the bench env files export it).
