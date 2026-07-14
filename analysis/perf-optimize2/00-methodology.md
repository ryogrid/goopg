# 00 — Methodology (perf-optimize2, run 20260712_114859)

## Purpose

Re-measure the goopg-vs-PostgreSQL gap for the update-heavy short-transaction
workload (pgbench *simple-update*, `-N`) at 50 clients under the **exact
conditions** of the previous analysis (`analysis/perf-optimize/`, run
20260518_115032), then re-attribute the gap with fresh profiles. The prior
report's conclusions predate several landed optimizations (WAL group commit
M0098-0002/M0099-0003, mvcc.Manager.mu decomposition, the per-query
ReadMemStats fix M0107) and are stale.

## Benchmark conditions (parity with perf-optimize)

| item | value | identical to prior run? |
|---|---|---|
| workload | `pgbench -N` (simple-update: UPDATE accounts, INSERT history per txn wrapped in BEGIN/END) | yes |
| clients / threads | `-c 50 -j 50` | yes |
| duration | `-T 180 -P 10`, single run | yes |
| scale | `-i -s 100` (10 M accounts) | yes |
| query mode | simple protocol (pgbench default) | yes |
| pgbench / client libs | pgbench (PostgreSQL) 18.3, `postgres/local_install/{bin,lib}` | yes |
| goopg port / PG port / pprof | 5533 / 5534 / 6160 | yes |
| goopg env | `GOMEMLIMIT=18GiB GOOPG_MUTEX_PROFILE_RATE=1 GOOPG_BLOCK_PROFILE_RATE=1` | yes |
| goopg conf | `max_connections=200, shared_buffers=2560MB, wal_buffers=134217728, checkpoint_timeout=24h, max_wal_size=1024GB, min_wal_size=1024MB, checkpoint_completion_target=0.9` | yes |
| PG conf | same + `fsync=on` (wal_buffers written as `128MB`) | yes |
| synchronous_commit | default (`on`) on both | yes |
| resource caps | **none on either server** — no cgroup / systemd-run / taskset; GOMEMLIMIT is the only (soft, Go-runtime) limit | yes |
| server restart before the timed pattern | goopg restarted once after init (resets cumulative mutex/block profile state) | **no — see deviation 7** (prior suite ran c=50 *select-only* on the same instance before simple-update, so its shared_buffers were warm; here simple-update is the first workload after restart, i.e. cold cache) |

Driver: `analysis/perf-optimize2/scripts/run_su50.sh`, copy-derived from
`analysis/perf-optimize/scripts/run_perf_suite.sh` and restricted to the one
pattern. `pprof_collect.sh` is reused verbatim from the prior suite.

## Provenance

| item | value |
|---|---|
| goopg source | commit `10746d73` ("docs(M-NIGHTLY): correct TuplelockUpgradeNoDeadlock…"), built from a **clean `git worktree`** — the main tree carried unrelated in-flight (Ralph loop) modifications that were deliberately excluded |
| binary sha256 | `57bca9f5057db24755d30bd40543e0a31740ecc625b4031d29bfaef9f5792d0b` (archived as `runs/20260712_114859/goopg.bin`) |
| PostgreSQL | 18.3 (`postgres/local_install`, the repo's oracle build) |
| host | WSL2, 16 logical cores, 32 GiB RAM + 64 GiB swap, ext4; Linux 6.18.33.2-microsoft-standard-WSL2, go1.26.3 |
| data dirs | `tmp/perf-optimize2/20260712_114859/{goopg-data,pg-data}` (fresh `goopg init` / `initdb -U postgres --no-locale --encoding=UTF8`) |
| raw artifacts | `runs/20260712_114859/` (pgbench outputs, pprof profiles, wait samples, driver.log, env.txt) |

## Noise policy

The autonomous Ralph development loop that normally shares this machine was
**paused for the whole measurement window** (its in-flight worker process tree
was enumerated and stopped; the loop driver was already not running). Load
average at benchmark start was ≈1.1 with ~25 GiB RAM free. goopg and PG ran
under identical, uncapped conditions; the only other resident services were
two idle goopg instances on unrelated ports (5537, 5544) that serve no
traffic.

## Deviations and incidents (full disclosure)

1. **goopg readiness window raised (driver-only change).** goopg takes ~28 s
   to open a scale-100 data directory after restart (see §Startup finding in
   `02-bottleneck-analysis.md`); the prior driver's 20 s `wait_for_pg` window
   killed the server mid-startup. The window was raised to 150 s. This does
   not affect measured TPS.
2. **PG headline delayed ~92 min by a driver hang.** The first driver version
   hung in a shell bug (a background wait-event sampler held the stdout pipe
   of a command substitution) after the goopg headline completed and *before*
   the PG headline began; the hang lasted 92 min (12:02:12 → 13:34:09). The
   stuck driver was killed without touching either server, and the run was
   resumed with the same RUN_ID via a one-shot resume script that sourced the
   fixed driver's functions (the committed `run_su50.sh` contains the fix and
   reproduces the whole run in one invocation; the resume script is not
   needed for reproduction). Consequence: the goopg headline ran ~10 min
   after initdb, the PG headline ~105 min after initdb (~96 min after PG's
   own `pgbench -i`) on an otherwise idle, identical cluster. PG's pgbench_history was still empty
   and no checkpoint had triggered (24 h timeout); we consider the effect
   negligible, and PG's per-10 s progress lines were stable (15.6–16.0 k TPS)
   from the second interval onward.
3. **Aux (attribution) runs are 60 s, not 180 s**, and ran after the headline
   pair on the same clusters (heap/history bloat accumulates run over run).
   They are directional evidence, not headline numbers, and are reported
   separately.
4. **Profiler overhead is included in the headline by design** —
   `GOOPG_MUTEX_PROFILE_RATE=1 GOOPG_BLOCK_PROFILE_RATE=1` matches the prior
   run, preserving comparability. Aux run A5 measured this overhead at
   **+38 % TPS when disabled** (1,269 → 1,754 TPS); uninstrumented numbers
   are given alongside headline numbers in `01-results.md`.
5. **GOMEMLIMIT log line lies.** `cmd/goopg/main.go:323` logs the return of a
   double `debug.SetMemoryLimit` swap, which is the *temporary* maxint value,
   not the applied limit. The 18 GiB limit from the environment **is** in
   effect (the runtime consumes `GOMEMLIMIT` natively); the log line is
   cosmetic and should be fixed.
6. **WSL2 fsync caveat** (same as prior run): absolute fdatasync latency on
   this ext4-on-WSL2 volume is not representative of bare metal. Both servers
   share the volume, so the comparison remains symmetric.
7. **Cold vs warm cache (parity gap vs the prior suite).** In
   `run_perf_suite.sh` the c=50 simple-update measurement ran *after* a 180 s
   c=50 select-only run on the same goopg instance (restart happens once per
   client-count block, before the workload loop), so the prior run's
   shared_buffers were fully warmed. Here simple-update is the only and first
   workload after the restart — goopg starts with a cold buffer pool (the
   init-time COPY's buffers are discarded by the restart). This depresses
   goopg's early intervals (first interval 949 TPS vs 1,269 average) and is a
   likely contributor to the interval oscillation noted in `01-results.md`.
   Second-order relative to the 12× gap, but a real deviation from "identical
   conditions". PG's headline shares the property partially (its data was
   page-cache-resident from init; its own shared_buffers were also unwarmed —
   PG's first interval was 11.5 k vs 15.9 k steady-state).
8. **Diagnostic-artifact bug**: the driver's `pg_stat_checkpointer` snapshot
   used pre-PG17 column names (`checkpoints_timed`/`checkpoints_req` instead
   of `num_timed`/`num_requested`), so those snapshot files are empty. The
   "no checkpoint fired during the delay" claim in deviation 2 rests on the
   `pg_stat_wal` FPI delta (≈0 FPIs between init and the PG headline), not on
   those files. Fixed in the committed driver for future runs.

## What was captured

- goopg headline: full pprof set via `pprof_collect.sh` (cpu 120 s, trace
  30 s, heap/allocs, mutex/block base+final, goroutine dump), WAL-flush count
  from server.log, `/proc/<pid>` CPU+IO deltas.
- PG headline: wait-event samples every 250 ms from `pg_stat_activity`
  (client backends), `pg_stat_wal` snapshots before/after. Each sample is a
  fresh short-lived psql connection (~4 conn/s, ≈720 over the run) running
  one aggregate over `pg_stat_activity`, excluding its own backend; at
  15.5 k TPS this is <0.1 % load and only runs during the PG side —
  negligible, and if anything a handicap on PG, not goopg.
- Aux runs (goopg unless noted): A1 `-M prepared`; A2 `GOGC=400`;
  A3 `synchronous_commit=off` (goopg **and** PG); A4 `-c 1` (goopg **and**
  PG); A5 profiling rates 0.
- COPY diagnostic (`runs/20260712_114859/copydiag/`): separate throwaway
  goopg cluster, `pgbench -i -s 20`, with a 60 s CPU profile captured during
  the COPY phase — motivated by the 34× `pgbench -i` gap (see
  `01-results.md` §3). Reproduction (not part of `run_su50.sh`): init a
  throwaway data dir with the same conf block, start goopg with the same env
  on :5533/pprof :6160, run `pgbench -i -s 20`, and 10 s into the load fetch
  `curl -o copy.cpu.pb.gz "http://127.0.0.1:6160/debug/pprof/profile?seconds=60"`
  (plus allocs/mutex/block snapshots); stop and delete the data dir.

## Reproduction

```bash
go build -o bin/goopg ./cmd/goopg          # or build from a clean worktree
bash analysis/perf-optimize2/scripts/run_su50.sh
# env overrides: RUN_ID, DURATION, AUX_DURATION, RUN_AUX=0, GOOPG_BIN
```
