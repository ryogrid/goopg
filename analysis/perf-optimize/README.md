# goopg pgbench Performance-Optimisation Analysis

> Side-by-side `pgbench` analysis of `goopg` vs upstream PostgreSQL 18.3 across three workloads (TPC-B-like, simple-update, select-only) and three client counts (10 / 50 / 100), with full pprof telemetry, source cross-references to `internal/...`, and PG-counterpart citations under `./postgres/`.

## Chapters

1. [`00-methodology.md`](00-methodology.md) — environment, GUCs, run matrix, profile schedule, Ralph isolation, reproducibility.
2. [`01-results-matrix.md`](01-results-matrix.md) — TPS / latency / ratio tables for all 9 patterns × 2 systems.
3. [`02-cpu-bottlenecks.md`](02-cpu-bottlenecks.md) — top hot symbols from CPU profiles, mapped to goopg source.
4. [`03-memory-and-allocs.md`](03-memory-and-allocs.md) — heap inuse, allocs, GC pressure (correlated with `practice/go_rdbms_performance_techniques.md` §1, §6).
5. [`04-contention.md`](04-contention.md) — mutex + block + goroutine analysis; the lock manager / WAL / buffer pool contention story.
6. [`05-runtime-trace.md`](05-runtime-trace.md) — `go tool trace` findings: scheduler latency, syscall blocking, GC STW.
7. [`06-postgres-comparison.md`](06-postgres-comparison.md) — for each bottleneck in §02–§05, the PG counterpart that does it better, with `postgres/src/...` source citations.
8. [`07-postgres-optimizations-relevant.md`](07-postgres-optimizations-relevant.md) — checklist of upstream PG optimisations that matter for pgbench, each with goopg status.
9. [`08-recommendations.md`](08-recommendations.md) — ranked optimisation backlog (estimated lift × cost) drawn from §02–§07 and the practice playbook.

## Artefacts

```
analysis/perf-optimize/
├── 00-methodology.md … 08-recommendations.md
├── scripts/
│   ├── run_perf_suite.sh          # full 9-pattern driver
│   ├── pprof_collect.sh           # background profile collector (curl-based)
│   └── analyze.sh                 # post-process pprof + build results TSV
└── runs/<RUN_ID>/
    ├── driver.log                 # the driver's own log
    ├── pgbench_<target>_c<C>_<wl>.txt   # raw pgbench stdout
    ├── SKIPPED_*.txt              # only when c=100 fails on goopg
    ├── init_<target>.txt          # pgbench -i -s 100 stdout
    ├── profiles/                  # all pprof artefacts per goopg run
    │   ├── goopg_c<C>_<wl>.cpu.pb.gz
    │   ├── goopg_c<C>_<wl>.heap.pb.gz
    │   ├── goopg_c<C>_<wl>.allocs.pb.gz
    │   ├── goopg_c<C>_<wl>.mutex.pb.gz + .mutex_base.pb.gz
    │   ├── goopg_c<C>_<wl>.block.pb.gz + .block_base.pb.gz
    │   ├── goopg_c<C>_<wl>.goroutine.txt
    │   ├── goopg_c<C>_<wl>.trace.out
    │   └── goopg_c<C>_<wl>.threadcreate.txt
    ├── pprof_top/                 # human-readable pprof -top + -list dumps
    ├── results_summary.tsv        # flat TSV of TPS/latency
    └── goopg.bin                  # the exact binary used (so go tool pprof keeps working)
```

## TL;DR

Run ID `20260518_115032` (2026-05-18 11:50 → 13:15 JST; ~85 min wall). 7/9 goopg patterns succeeded; both `c=100` write patterns deadlocked at 0 TPS after ~30 s and were SKIPPED per user directive (see [`01`](01-results-matrix.md)).

| measurement | result |
|---|---|
| TPS gap range (PG / goopg) | **6.7× – 29.3×** depending on (workload, clients) |
| Headline c=10 select-only | 2 308 TPS goopg / 37 062 TPS PG (**16.1×**) |
| Headline c=50 simple-update | 347 TPS goopg / 10 166 TPS PG (**29.3×**) |
| c=100 simple-update, c=100 standard | goopg deadlocks at 0 TPS (SKIPPED) |

**The three structural bottlenecks identified:**

1. **GC dominates CPU** — `runtime.gcBgMarkWorker` + `scanobject` = **54–77 % of CPU** in every goopg pattern. goopg allocates ~19 KB per SELECT (5 GB/120 s churn) where PG's `palloc`/`MemoryContext` allocates zero GC-scanned bytes. [`02`](02-cpu-bottlenecks.md), [`03`](03-memory-and-allocs.md). Top single allocator: `planner.Plan` (26 % of c=100 SO allocs).
2. **`mvcc.Manager.mu` is a single mutex for Begin/Snapshot/Commit/OldestXmin** (`internal/mvcc/manager.go:73`). Drives **92 % of mutex delay + 63 % of block delay** on write workloads. PG splits this across three separate lock classes (`ProcArrayLock`, `XidGenLock`, CLOG bank locks) with a lock-free read fast path. [`04`](04-contention.md) §4.2.
3. **`activity.Registry.mu` (wait-event tracking) is contended on every protocol frame** — **95 % of mutex delay on c=100 select-only**. PG's `PGPROC->wait_event_info` is a per-backend `uint32` in shared memory with lock-free reads. [`04`](04-contention.md) §4.3.

**The c=100 write deadlock** is `pgbench_history`'s tail page hashing to a single bufpool partition; 19 goroutines blocked on `bufpool.go:927 part.mu.Lock()` for 23 minutes. PG breaks this hotspot via FSM-driven insert distribution. [`04`](04-contention.md) §4.4.

**Top three recommendations** (from [`08`](08-recommendations.md)):

1. **Per-statement / per-transaction arena** (palloc-style) — projected **3–5× lift** on c=10 select-only / simple-update (cost: L).
2. **Decompose `mvcc.Manager.mu`** into per-backend `txState` slots with lock-free `GetSnapshotData` walk — projected **3–6× lift** on c=50 writes (cost: M).
3. **Lock-free `activity.Registry`** (per-backend atomic `wait_event_info`) — projected **1.5–2× lift** on c=100 SO (cost: S–M, smallest-first win).

**Where goopg already matches PG** (no further work needed): 128-way buffer pool partitioning (M0098-0003), WAL group commit (M0098-0002), pin fast path (M0099-0001/0002), GC tuning (M0098-0007). See [`07`](07-postgres-optimizations-relevant.md).

## Reproducing

```bash
# Rebuild goopg (binary is archived per run for stable symbol resolution)
go build -o bin/goopg ./cmd/goopg

# Drive the full suite (~60 min wall-clock; rather you not run during a Ralph turn)
bash analysis/perf-optimize/scripts/run_perf_suite.sh

# Post-process the most recent run
bash analysis/perf-optimize/scripts/analyze.sh "$(ls -t analysis/perf-optimize/runs/ | head -1)"
```

`run_perf_suite.sh` honours `CLIENT_COUNTS`, `DURATION`, `RUN_ID`, `GOMEMLIMIT`,
and `GOOPG_PPROF_ADDR` env overrides — see [`00-methodology.md`](00-methodology.md).

## Source notes

- The benchmark uses `GOOPG_PPROF_ADDR=127.0.0.1:6160` (default `:6060`) to keep pprof out of Ralph's way. This env hook is a one-line addition to `cmd/goopg/main.go` made for this exercise.
- Profile capture deliberately starts 30 s into each 180 s pgbench run and ends 30 s before completion, sampling steady state and avoiding ramp/wind-down skew.
- Cumulative mutex/block profiles are baselined at `T+30` and the report uses `pprof -base` deltas to scope contention to the measured workload only.
