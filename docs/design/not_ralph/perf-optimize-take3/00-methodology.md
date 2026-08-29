# 00 — Methodology

## 1. Configuration

Both engines were configured **identically**, with durability at PostgreSQL 18.3
defaults so that the WAL persistence path is fully exercised:

```
max_connections    = 200
shared_buffers     = 2560MB
wal_buffers        = 100MB          # goopg: 104857600 (see §6)
checkpoint_timeout = 24h
max_wal_size       = 1024GB
fsync              = on
synchronous_commit = on
```

`fsync` and `synchronous_commit` are stated explicitly rather than left at
`BootVal`, and both were verified to be genuinely consumed by goopg at runtime —
`cmd/goopg/main.go:445` (`fsyncEnabled := boolGUC(registry, "fsync", true)`) and
`internal/postmaster/dispatch.go:1430` / `:1448` (`sessionSyncCommitMode` /
`sessionAsyncCommit`). This matters because goopg has a documented history of
GUCs that are declared but never read.

| item | value |
|---|---|
| goopg | `9ecc840b5`, branch `after-parser-refac`, `bin/goopg` |
| PostgreSQL | 18.3, `postgres/local_install` |
| scale factor | 100 (`pgbench -i -s 100`, ~1.5 GB) |
| clients / jobs | `-c 50 -j 50` |
| duration | 180 s per run |
| protocol | simple (`pgbench` default) |
| goopg port / data | 5533, `tmp/take3/goopg-data` |
| PG port / data | 5534, `tmp/take3/pg-data` |
| goopg pprof | `GOOPG_PPROF_ADDR=127.0.0.1:6160` |
| host | 16 cores, 31 GiB RAM + 64 GiB swap, WSL2 kernel 6.18 |
| goopg GC | `GOGC=200`, `GOMEMLIMIT=15GiB` (server-applied defaults) |

goopg ran inside a cgroup v2 scope for every run
(`GOOPG_CG_UNIT=take3-goopg scripts/goopg-test-run.sh`, MemoryHigh=20G /
MemoryMax=24G / SwapMax=0), per the project's WSL2 OOM-containment rule.

**The two engines never ran concurrently.** PG was fully stopped before goopg
started and vice versa, so no run competes with the other for CPU, page cache or
disk.

## 2. Run matrix

Eight primary runs. **The server was restarted fresh before every run** — a
goopg server that has just finished a heavy run sits at `GOMEMLIMIT` with
degraded GC behaviour and mimics a regression (the project's documented
"sweep-tail collapse" confound).

| tag | engine | workload | instrumentation | role |
|---|---|---|---|---|
| `R1-goopg-N` | goopg | `-N` | pprof CPU + `-r` + wait sampling | **headline write** |
| `R2-goopg-S` | goopg | `-S` | pprof CPU + `-r` + wait sampling | **headline read** |
| `R3-goopg-N-contention` | goopg | `-N` | block + mutex profiles | contention rank (perturbed) |
| `R4-goopg-S-contention` | goopg | `-S` | block + mutex profiles | contention rank (perturbed) |
| `R5b-goopg-N-perf` | goopg | `-N` | `perf stat`, `perf record`, alloc/heap | Go cost accounting (perturbed) |
| `R6-goopg-S-perf` | goopg | `-S` | `perf stat`, `perf record`, alloc/heap | Go cost accounting (perturbed) |
| `P1-pg-N` | PG 18.3 | `-N` | `-r` + wait sampling | **oracle write** |
| `P2-pg-S` | PG 18.3 | `-S` | `-r` + wait sampling | **oracle read** |

Supplementary: `WALPROBE-goopg-N` (90 s `-N`, `pg_wal` growth + `/proc/<pid>/io`)
for WAL volume; `P1perf-pg-N` / `P2perf-pg-S` (discarded as headline, see §5).

**Headline TPS is only ever taken from R1/R2/P1/P2** — the runs with no
profiler attached beyond the standard pprof CPU capture.

## 3. Harness

`tmp/take3/run.sh` — one pgbench run plus a 500 ms `pg_stat_activity` sampling
loop, plus an optional concurrent pprof CPU capture. It is a generalisation of
the repository's `scripts/pgbench-wait-sample.sh`, which hardcodes `-N`; this
study needed `-S` as well. The sampling query is the same one that script uses:

```sql
select pid, state, wait_event_type, wait_event
from pg_catalog.pg_stat_activity
where application_name = 'pgbench' and backend_type = 'client backend'
```

Sampling ran at 2 Hz for the whole 180 s, yielding ~17,500 backend-samples per
run (~350 sweeps × 50 clients) on both engines — enough that a 1 % share is ~175
samples.

`tmp/take3/perfrun.sh` wraps a run with `perf stat` (60 s window starting at
t+25 s) and `perf record -g --call-graph fp -F 199` (45 s window at t+95 s),
plus `/debug/pprof/allocs` and `/debug/pprof/heap` snapshots before and after,
and `/proc/<pid>/io` deltas.

Reproduction, end to end:

```bash
systemctl --user stop goopg-probe-<n>.scope     # reap any orphan on 5533
./bin/goopg init -D tmp/take3/goopg-data        # then append the §1 config
GOOPG_CG_UNIT=take3-goopg GOOPG_PPROF_ADDR=127.0.0.1:6160 \
  scripts/goopg-test-run.sh ./bin/goopg start -D tmp/take3/goopg-data \
  --listen 127.0.0.1:5533
pgbench -i -s 100 -h 127.0.0.1 -p 5533 -U postgres postgres
tmp/take3/run.sh R1-goopg-N 5533 -N 180 127.0.0.1:6160
tmp/take3/perfrun.sh R5b-goopg-N-perf -N
```

## 4. Profiler configuration

goopg's pprof endpoint is **always on**, with no flag —
`cmd/goopg/main.go:330-353` spawns it unconditionally on `127.0.0.1:6060`,
overridable via `GOOPG_PPROF_ADDR`. Block and mutex profiling are env-gated and
off by default (`main.go:265-291`):

```
GOOPG_BLOCK_PROFILE_RATE=1   # runtime.SetBlockProfileRate(1)
GOOPG_MUTEX_PROFILE_RATE=1   # runtime.SetMutexProfileFraction(1)
```

Measured cost of enabling both at rate 1: **−1.9 % on `-N`** (10,585 vs 10,786
TPS) and **−6.2 % on `-S`** (87,353 vs 93,083 TPS). This is far cheaper than the
−28 % recorded in `analysis/perf-optimize3-dash`; the separation of headline and
contention runs is retained anyway, because the read-path figure is not
negligible.

## 5. Threats to validity — read this before quoting any number

- **`perf stat -p <pid> -- sleep N` silently counts nothing.** The first PG runs
  were taken with that form and are discarded. The working form omits `--`:
  `perf stat -p <pid> sleep 60`. Any future harness must check for
  `<not counted>` in the output.
- **`perf stat` attached to PG's 50 backends measurably perturbs it.** The
  discarded `P1perf`/`P2perf` runs show `-S` dropping from ~115 k to ~74 k TPS
  inside the counter window. goopg, being a single process, is far less
  affected. PG's headline numbers therefore come from clean runs only.
- **`perf_event_paranoid = 2` on this host.** All hardware counters are
  **user-mode only** (`:u` suffix) — no kernel-mode cycles, no kernel symbols,
  no system-wide sampling. Kernel time (syscalls, `fdatasync`, socket I/O) is
  therefore *absent* from the `perf record` profiles, and the `perf stat`
  "CPUs utilized" figure counts user time only. Kernel-side cost is instead
  measured through Go's own `Syscall6` frames in the pprof CPU profile,
  `/proc/<pid>/io` counters, and the wait-event distribution. Lowering the
  sysctl to 1 would strengthen §04; it was not available for this run.
- **The simple protocol defeats goopg's plan cache entirely.** pgbench
  substitutes variables client-side, so every statement arrives with distinct
  literal text (`... WHERE aid = 8215525`), and the cache key
  (`planCacheKey`, `internal/postmaster/dispatch.go:2397`) preserves literals by
  design. Every query is therefore a full parse + plan. This is a faithful
  measurement of the default pgbench workload, but it is **not** representative
  of a `-M prepared` client. See 05 §6.
- **The two engines' `pgbench_accounts` heaps differ by 3×, in goopg's favour.**
  After an identical `pgbench -i -s 100`, goopg's heap is **442,818,560 B
  (44.3 B/row)** and PostgreSQL's **1,365,778,432 B (136.6 B/row)** — a **3.08×**
  difference. Cause: pgbench declares `filler char(84)` and sets it to a
  blank-padded empty string (`postgres/src/bin/pgbench/pgbench.c:4886-4890`,
  *"the `filler` column is set to blank-padded empty string"*), while goopg
  stores `bpchar` **trimmed** by design
  (`internal/executor/bpchar_declared_width_test.go:80-86`: *"goopg's storage
  convention is the opposite (trimmed, so … the compact heap image hold)"*).
  goopg's `-S` therefore scans a heap one-third the size, with correspondingly
  better page and cache locality. **This systematically favours goopg on the read
  path**, and it bears directly on the 1.23× read gap and on the cache-miss
  discussion in [02 §4](02-cpu-and-allocation.md). It is not a measurement error
  — both engines ran the stock loader — but it is a real difference in what was
  measured, and the read gap should be read as a lower bound on the engine gap.
  (The index cuts the other way, mildly: goopg's `pgbench_accounts_pkey` starts
  at 202.3 MB against PG's 224.6 MB.)
- **`pgbench -r` per-statement latencies are client-side** and include network
  and client scheduling; they are used for *decomposition ratios* between the
  two engines, not as absolute server timings.
- **Repeatability**: `-N` was measured at 10,786 (R1) and 10,733 (R5) TPS —
  0.5 % apart. Note R5 is *not* a clean run and is not in the §2 matrix: it
  carried `perf record` and the alloc snapshots, and its `perf stat` output was
  `<not counted>` throughout (the `--` bug above), which is why R5b exists; its
  TPS is quoted only as a repeatability check. `-S` headline is 93,083 (R2); the
  perturbed runs bound it from **below** by 6 % (R4, 87,353) and 15 % (R6,
  79,485) — they do not bracket it. R6 in particular swings 66 k–85 k within the
  run, and it is the source of the allocation-per-transaction and `perf stat`
  figures, so those should be read as shape rather than precision. No run
  reported a failed transaction (`0 failed` on all eight).

## 6. Two PostgreSQL-compatibility gaps found while setting up

Both are incidental to the study but real, and neither has an existing ledger
row:

1. **`wal_buffers` rejects a unit suffix.** `wal_buffers = 100MB` — valid in
   PostgreSQL — is rejected by goopg at startup with
   `variable does not accept unit "mb"`; the value must be given as raw bytes
   (`104857600`). PG accepts `MB` on this GUC
   (`postgres/src/backend/utils/misc/guc_tables.c`, `GUC_UNIT_XBLOCKS`). An
   operator lifting a tuned PostgreSQL `postgresql.conf` onto goopg — the exact
   scenario the GUC-sample discipline exists to support — hits a hard startup
   failure.
2. **`pg_current_wal_lsn()` is seeded but not callable.** It is present in
   `internal/initdb/pg_proc_seed_data.go:1849` (OID 2849) and listed in
   `internal/executor/pg_nonimmutable_builtins.go:148`, but calling it returns
   `ERROR: function pg_current_wal_lsn does not exist`. There is no executor
   handler behind the catalog entry. This is why goopg's WAL volume in
   [01-results.md](01-results.md) is measured from `pg_wal` directory growth
   rather than an LSN delta.
