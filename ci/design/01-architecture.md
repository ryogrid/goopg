# 01 — Architecture

## Entrypoints (requirement: one script / one make command)

- **`ci/batch/run-nightly.sh`** — the orchestrator. Runs the full batch from a
  clean invocation with zero arguments. All tunables are env-overridable with
  safe defaults (see doc 03).
- **`make nightly-batch`** — thin Makefile wrapper:
  `bash ci/batch/run-nightly.sh`. Exists so the batch is discoverable next to
  `race-gate`/`plan-gate`; adds nothing else.
- Both the scheduler and a human use the *same* entrypoint; overlap is
  prevented by the run lock (doc 06), so a manual `make nightly-batch` while
  the 00:00 run is active exits immediately with a clear message.

## `ci/` directory layout (to be created at implementation time)

```
ci/
  design/                     # this design (committed)
  batch/
    run-nightly.sh            # orchestrator (entrypoint)
    nightly-scheduler.sh      # resident daemon (doc 06)
    lib/
      common.sh               # log helpers, lock helpers, port probe, summary emit
    stages/
      stage-preflight.sh      # S0
      stage-units.sh          # S1 Lane L step 1
      stage-race.sh           # S1 Lane L step 2
      stage-testport.sh       # S1 Lane H step 1
      stage-pgbench.sh        # S1 Lane H step 2
      stage-tpch.sh           # S2 (doc 05)
      stage-summary.sh        # S3
    expected-failures.csv     # batch-local expected-fail list (doc 02)
  logs/                       # run outputs (gitignored; doc 04)
```

Per requirement 10: the orchestrator *calls* established repo tools
(`scripts/ralph-precommit-test.sh`, `scripts/goopg-test-run.sh`,
`scripts/tpch-spotcheck.sh`, `make race-gate`, `tmp/tpch-runner`) but all NEW
glue — stage wrappers, budget logic, summary emission, the scheduler — lives
under `ci/batch/`. No new logic is scattered into `scripts/` or `bench/`.

## Stage DAG

```
S0 preflight  (sequential)
   │
   ├────────────────┬─────────────────┐
   ▼                ▼                 │
S1 Lane L        S1 Lane H            │  (two lanes in parallel)
  units            testport suite     │
  race-gate        pgbench nightly    │
   └────────────────┴─────────────────┘
   ▼  (barrier: both lanes complete)
S2 TPC-H (solo — nothing else running)
  spotcheck → EXPLAIN capture → 22-query sweep (2h budget)
   ▼
S3 summarize → summary.md / summary.json → exit code
```

### S0 — preflight (sequential, fast, fail-fast)

0. **First action of the run:** create `ci/logs/<ts>/`, start `progress.log`,
   update the `ci/logs/latest` symlink — so every later step (including a
   failing build) has a home for its artifacts.
1. Record run metadata: `git rev-parse HEAD`, `git status --porcelain`
   (dirty-tree note — the Ralph loop's WIP is *expected* to be present),
   timestamp, hostname, `go version` → `meta.json`.
2. `make build` → `preflight/build.log` (the batch tests the *current tree*;
   a build failure is itself the first regression signal — batch stops here:
   top-level status `fail`, stage detail `fail(build)`, exit code 2).
3. Environment checks, each producing `ok` / `skip-with-note`:
   - free disk ≥ 10 GB on the repo filesystem;
   - client binaries present (`psql`, `pgbench` via PATH or
     `postgres/local_install/bin`) — absent tools do not fail the batch, the
     affected tests `t.Skip` themselves (doc 02);
   - TPC-H data dir `bench/tpch/runtime_goopg/data` present and ≥ 100 MB
     (`TPCH_SPOTCHECK_MIN_MB` semantics) — else S2 is `skip(no-data)`;
   - port probes for the batch's reserved ports (doc 03) — busy ⇒ bounded
     wait then `skip(port-busy)` for that stage only.

SKIP semantics follow `scripts/tpch-spotcheck.sh` precedent: a skipped stage
exits 0 and is *reported* as skipped (never silently absent from the summary).

### S1 — two parallel lanes

Rationale for exactly two lanes: they have **complementary resource
profiles** — Lane L is pure-CPU `go test` with no server; Lane H spends much
of its wall clock waiting on a capped server process (I/O + IPC). Running them
together roughly halves the S1 wall clock without doubling peak memory.
Anything wider (e.g. splitting testport into parallel shards) multiplies
server processes and OOM surface for little gain. See doc 03 for the caps.

**Lane L (server-less), sequential within lane** — both stages run inside a
`scripts/goopg-test-run.sh` cgroup scope of their own (doc 03 §A), so every
batch stage is memory-capped, not only the server-based ones:
1. `stage-units.sh` → the CI-parity unit/component set, run directly
   (`go test -timeout ${NIGHTLY_UNITS_TIMEOUT:-30m}` over the CI EXCLUDE
   list; the precommit tool's hard-coded 10m is too tight under nightly
   co-load — see doc 02 §A).
2. `stage-race.sh` → `make race-gate RACE_TIMEOUT=${NIGHTLY_RACE_TIMEOUT:-45m}`
   (same EXCLUDE list with `-race`).

**Lane H (server-based), sequential within lane:**
1. `stage-testport.sh` → the oracle-port suite: the **whole
   `./internal/testport/` package with NO `-run` filter**, wrapped in
   `scripts/goopg-test-run.sh` (exact command in doc 02). No filter is
   deliberate: 4 of the 60 must-pass oracle rows are pinned by
   `TestE2E_Failover*` functions, not `TestPort_*` — a `-run 'TestPort_'`
   filter would silently never run them. This one invocation covers the
   60 must-pass oracle rows *and* the regress (232 cases) and isolation
   (121 specs) sub-suites — they are subtests of `TestPort_RegressSuite` /
   `TestPort_IsolationSuite`, not separate stages (dedup rule, doc 02).
2. `stage-pgbench.sh` → self-contained nightly pgbench run (scale 50,
   100 clients, 20 threads, 180 s × the standard/-N/-S trio; ~12–15 min;
   0-failed-transactions gate). Deliberately NOT `ralph-precommit-test.sh`:
   the nightly parameters differ from the per-commit smoke, and changing the
   shared tool would alter the git-hook gate for every commit.

Lane implementation: the orchestrator backgrounds each lane's runner
(`lane_l.pid`, `lane_h.pid`), `wait`s on both, and merges statuses. A lane
failure does **not** abort the other lane (maximize information per night),
but any must-pass failure marks the run failed.

### S2 — TPC-H solo stage

Deliberately after the barrier: the only stage allowed a double-digit-GiB
cap, so nothing else from the batch may be running. Full design in doc 05.

### S3 — summarize

Aggregates per-stage status files into `summary.md` + `summary.json`,
classifies failures (regression / expected-fail / resource-kill / skip),
**regenerates `ci/logs/action-items.md`** (the agent-facing feedback file the
Ralph loop's standing M-NIGHTLY milestone consumes — doc 07), prunes old run
dirs, sets the batch exit code. Schema in doc 04.

## Reuse-vs-consolidate table (requirement 10)

| Function | Reused as-is (established tool) | New, lives in `ci/batch/` |
|----------|--------------------------------|---------------------------|
| Unit suite | (CI EXCLUDE list mirrored) | `stage-units.sh` — direct `go test`, nightly timeout |
| pgbench nightly run | (server plumbing pattern mirrored from `ralph-precommit-test.sh`) | `stage-pgbench.sh` — own params s=50/c=100/j=20/T=180 |
| Race detector | `make race-gate` | — |
| cgroup capping | `scripts/goopg-test-run.sh` | per-stage unit naming |
| TPC-H spotcheck | `scripts/tpch-spotcheck.sh` | — |
| TPC-H query driver | `cmd/tpch-runner` (built to `tmp/tpch-runner`; flags `-queries`, `-per-query-timeout`, `-explain`, `-signal-file`) | budget loop around it |
| Plan diff (informational) | `make plan-gate` | — |
| Must-pass authority | `docs/test-port/*.csv` + `internal/testport/framework` | `expected-failures.csv` |
| Orchestration, lanes, locks, progress log, summary | — | all of it |
| Scheduling | — | `nightly-scheduler.sh` + a ~10-line `ralph_loop.sh` hook (doc 06) |

## Failure containment

- Each stage runs under `set -o pipefail` with its own log file; a stage
  script's nonzero exit is recorded, never propagated as an orchestrator
  crash.
- Every server started by a stage is stopped in that stage's `trap ... EXIT`
  (PID from `postmaster.pid`, then `systemctl --user stop <unit>.scope`,
  `reset-failed` if needed) — mirroring `ralph-precommit-test.sh`'s cleanup.
  Never `pkill -f` (self-match hazard; may kill the Ralph loop's servers).
- The orchestrator itself traps EXIT/INT/TERM to stop live lanes' scopes and
  write a `summary.json` with `"status": "aborted"` so a killed run is still
  accounted for.
